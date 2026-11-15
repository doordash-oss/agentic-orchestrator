// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package agent — final_review_loop.go is the feature-level Final Review
// loop. Each iteration runs a round of review axes over the cumulative
// cross-repo diff, then one fix session when any axis requests changes. FR
// atomicity (every staged repo records the same outcome, or all stay/fail
// together) is enforced via AtomicPhaseStamp.
//
// Rounds narrow after the first: only axes with open findings rerun, and
// approvals stick until a single confirmation pass. The loop hands off to the
// user, through the fix session's ask-user path, when a fix round changes
// nothing or an axis stalls.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// finalStatusReviewPassed is the FinalStatus value shared by every loop
// result type in this package (FeatureFinalReviewResult, LoopResult,
// PhaseImplementLoopResult, ...) to signal that review approved the work.
const finalStatusReviewPassed = "review_passed"

// finalStatusStopped is the Final Review result when the user chose to stop
// at a handoff.
const finalStatusStopped = "stopped"

// FeatureFinalReviewResult is the unified feature-level FR loop's outcome.
//
// FinalStatus values:
//   - "review_passed":   reviewer APPROVED. AtomicPhaseStamp records the
//     FR-pass outcome on the staged subset (no per-repo state change;
//     feature-level Status carries the verdict).
//   - "max_iterations":  hit cfg.MaxIterations without an APPROVED verdict.
//     Every staged repo's RepoState records the failure.
//   - "safety_rail":     consecutive-failure rail tripped. Every staged
//     repo's RepoState records the failure.
//   - "protocol_violation": the fix agent ended its turn without satisfying
//     the completion contract. Every staged repo's RepoState records the failure.
//   - "interrupted":     graceful shutdown / feature stopped while
//     running. No atomic stamp written; persisted state preserved.
//   - "stopped":         the user chose to stop at a handoff. No atomic
//     stamp written; the feature can resume Final Review later.
//   - "failed":          dispatch error before iteration began.
//
// Repos is the deduplicated, sorted list of touched repos at loop entry —
// the canonical "FR-staged subset" the AtomicPhaseStamp wrote to.
type FeatureFinalReviewResult struct {
	FinalStatus string
	Iterations  int
	LastError   string
	Repos       []string
}

// runFeatureFinalReviewLoop runs the feature-level Final Review loop. Cwd is
// the active run dir; --add-dir mounts every Feature.Repos worktree.
//
// Iteration order is INVERTED relative to phase implement: review runs
// FIRST inside the iteration, and the fix agent runs SECOND when any axis
// requests changes. This inverted order lives in this loop function rather
// than as a kernel parameter so the control flow stays legible.
//
// FR atomicity guarantee: AtomicPhaseStamp commits the outcome in one
// FeatureStore.Modify write at the end of the loop. Either every staged
// repo records the FR-pass outcome (success) or every staged repo records
// the failure (max_iterations / safety_rail / dispatch error) — no
// partial FR shipment.
//
// Crash recovery: re-runs the interrupted iteration from scratch. Iteration
// metas record each round's axis verdicts and reviewed HEADs, so the resumed
// loop schedules the same narrowed rounds. ArtifactManager.LatestIteration()
// picks up where the previous run stopped.
func runFeatureFinalReviewLoop(cfg OrchestratorConfig, sm ports.SessionManager) (*FeatureFinalReviewResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("feature final review loop: feature is nil")
	}
	if cfg.FeatureStore == nil {
		return nil, fmt.Errorf("feature final review loop: feature store is nil")
	}

	// Determine the FR-staged subset by reading persisted state. Every
	// repo any phase touched is in scope; the AtomicPhaseStamp at end-of-loop
	// transitions exactly this set.
	stagedRepos := touchedReposFresh(cfg.FeatureStore, cfg.Feature)
	if len(stagedRepos) == 0 {
		// Nothing to review — degenerate "all repos already past FR" case.
		// Treat as a no-op success so the orchestrator can fall through to
		// MarkCodeReady / auto-publish unchanged.
		return &FeatureFinalReviewResult{FinalStatus: finalStatusReviewPassed}, nil
	}

	// Build the cross-repo workspace. Cwd at the active run dir, with
	// --add-dir for every Feature.Repos worktree (and the active run).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := buildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("feature final review loop: workspace setup: %w", err)
	}

	// FR artifact dir: feature-level under runs/run-NNN/review/. The
	// per-repo subdir collapses under the unified flow.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, feature.PhaseReview.DirName())

	// Mark mid-flight phase status at the feature level so observers can
	// surface "final reviewing" without per-repo lying.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "final_reviewing")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	loopState := &featureFinalReviewLoopState{
		cfg:         cfg,
		sm:          sm,
		workspace:   workspace,
		stateDir:    stateDir,
		artifactDir: artifactDir,
		stagedRepos: stagedRepos,
	}

	result, runErr := loopState.run()

	// Translate the loop outcome into atomic state transitions.
	switch {
	case runErr != nil:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   phaseOutcomeFailed,
		})
		return &FeatureFinalReviewResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			Repos:       stagedRepos,
			Iterations:  result.Iterations,
		}, runErr
	default:
	}

	switch result.FinalStatus {
	case finalStatusReviewPassed:
		// FR success: AtomicPhaseStamp records the outcome with no
		// per-repo state mutation (feature-level Status carries the
		// verdict); PR URL writes are mirrored when supplied.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFinalReviewPassed,
		})
		result.Repos = stagedRepos
		return result, nil

	case "interrupted", finalStatusStopped:
		// No atomic stamp on interrupt; persisted state is left
		// untouched so the next start picks up the loop.
		result.Repos = stagedRepos
		return result, nil

	default:
		// max_iterations / safety_rail map to a feature-level FR failure.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   phaseOutcomeFailed,
		})
		result.Repos = stagedRepos
		return result, nil
	}
}

// featureFinalReviewLoopState carries the per-call state for the FR
// iteration loop. The outer RunFeatureFinalReviewLoop derives invariants
// once (workspace, staged repos); the run() method drives the iteration
// cursor and the per-axis round history.
type featureFinalReviewLoopState struct {
	cfg         OrchestratorConfig
	sm          ports.SessionManager
	workspace   workspaceSetup
	stateDir    string
	artifactDir string
	stagedRepos []string

	axes    []implementationReviewAxis
	history *reviewRoundHistory
}

// finalReviewRound is what one iteration persists for resume.
type finalReviewRound struct {
	verdicts map[string]string
	state    repoState
	changed  bool
	handoff  string
	pending  string
}

// Stages a review iteration can still owe after its review.
const (
	pendingFix     = "fix"
	pendingHandoff = "handoff"
)

func roundFromMeta(meta iterationMeta) finalReviewRound {
	return finalReviewRound{verdicts: meta.AxisVerdicts, state: metaRepoState(meta), changed: meta.MadeProgress, handoff: meta.Handoff}
}

// run executes the FR iteration loop. Returns a result with FinalStatus
// set; never returns a wrapped error because per-iteration failures are
// translated into safety-rail trips by the consecutive-failure counter.
func (s *featureFinalReviewLoopState) run() (*FeatureFinalReviewResult, error) {
	cfg := s.cfg

	// Recover from prior iterations on disk.
	am := newArtifactManager(s.artifactDir)
	startIter := am.latestIteration()

	consecutiveFailures := 0
	if startIter > 0 {
		for j := startIter; j >= 1; j-- {
			jDir := filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", j))
			meta, err := am.readMeta(jDir)
			if err != nil {
				break
			}
			if isFailureBudgetAgentStatus(meta.AgentStatus) {
				consecutiveFailures++
			} else {
				break
			}
		}
	}

	// Fix-round counter recovered from persisted iteration metas so a
	// crash-resumed loop numbers fix rounds identically to the interrupted
	// run: the fix following the Nth changes-requested review is "fix N".
	roundCommits := newFRRoundCommitTracker(am, s.artifactDir, startIter)
	s.axes = s.finalReviewAxes()
	s.history = loadReviewRoundHistory(am, s.artifactDir, startIter)

	maxConsecFails := cfg.MaxConsecFails
	if maxConsecFails == 0 {
		maxConsecFails = 3
	}

	// A round owed to the user, a confirmation or the review of their
	// guidance, runs even past MaxIterations.
	owed := false
	if startIter > 0 {
		iterDir := filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", startIter))
		if meta, err := am.readMeta(iterDir); err == nil && meta.Pending != "" {
			done, nextOwed := s.resumePending(startIter, iterDir, meta, roundCommits.changesRequested, &consecutiveFailures)
			if done != nil {
				return done, nil
			}
			owed = nextOwed
		}
	}

	for i := startIter + 1; i <= cfg.MaxIterations || owed; i++ {
		owed = false
		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &FeatureFinalReviewResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
		}

		if cfg.FeatureStore != nil {
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.ReviewIteration = i
				return nil
			})
		}

		iterDir, mkdirErr := am.createIterationDir(i)
		if mkdirErr != nil {
			return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: mkdirErr.Error()}, nil
		}

		now := currentRepoState(s.workspace.RepoPaths)
		plan := s.history.planRound(s.axes, now)
		if len(plan) == 0 {
			return s.approve(iterDir, i, finalReviewRound{}), nil
		}

		// Inverted iteration order: review FIRST, fix SECOND.
		s.setReviewFixing(false)
		round := finalReviewRound{state: now}
		reviewStatus, feedback, verdicts, reviewErr := s.runReview(i, iterDir, plan)

		if reviewErr != nil {
			// A stop cancels the round: report the interrupt, never count it
			// as a failed round or start the next one.
			if isHelperInterruptedError(reviewErr) || isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
				return &FeatureFinalReviewResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}
			if isProviderUsageLimitError(reviewErr) {
				s.writeIterationMeta(iterDir, i, "FAILED", "review_failed", finalReviewRound{})
				return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: reviewErr.Error()}, nil
			}
			// A provider that cannot be reached will not heal by rerunning
			// the round: fail fast so the feature surfaces the outage.
			if isProviderUnavailableError(reviewErr) {
				s.writeIterationMeta(iterDir, i, "FAILED", "review_failed", finalReviewRound{})
				return &FeatureFinalReviewResult{
					FinalStatus: "failed",
					Iterations:  i,
					LastError:   fmt.Sprintf("final review could not run: %v; check provider connectivity and credentials, then retry", reviewErr),
				}, nil
			}
			if isProtocolViolationError(reviewErr) {
				consecutiveFailures++
				s.writeIterationMeta(iterDir, i, agentStatusProtocolViolation, "review_failed", finalReviewRound{})
				if consecutiveFailures >= maxConsecFails {
					return &FeatureFinalReviewResult{
						FinalStatus: BoundedHelperStatusProtocolViolation,
						Iterations:  i,
						LastError:   reviewErr.Error(),
					}, nil
				}
				continue
			}
			consecutiveFailures++
			s.writeIterationMeta(iterDir, i, "", "review_failed", finalReviewRound{})
			if consecutiveFailures >= maxConsecFails {
				return &FeatureFinalReviewResult{FinalStatus: "safety_rail", Iterations: i, LastError: reviewErr.Error()}, nil
			}
			continue
		}

		round.verdicts = verdicts
		s.history.recordReview(i, verdicts, round.state)

		switch reviewStatus {
		case ReviewApproved:
			if len(s.history.planRound(s.axes, round.state)) > 0 {
				// Earlier approvals predate later code: they confirm next round.
				s.writeIterationMeta(iterDir, i, "", "approved", round)
				owed = true
				continue
			}
			return s.approve(iterDir, i, round), nil

		case reviewChangesRequested:
			roundCommits.changesRequested++
			if i > cfg.MaxIterations {
				s.writeIterationMeta(iterDir, i, "", frMetaChangesRequested, round)
				return s.maxIterationsResult(i), nil
			}
			var done *FeatureFinalReviewResult
			if axis, stalled := s.history.stalledAxis(s.axes); stalled {
				reason := fmt.Sprintf("The %s axis requested changes in %d consecutive rounds.", axis.Name, axisStallLimit)
				done, owed = s.raiseHandoff(i, iterDir, feedback, reviewHandoff{Reason: reason, Findings: feedback, FixerNote: s.latestFixerNote(i)}, round, roundCommits.changesRequested)
			} else {
				done, owed = s.fixStage(i, iterDir, feedback, round, roundCommits.changesRequested, &consecutiveFailures)
			}
			if done != nil {
				return done, nil
			}
			continue

		default:
			s.writeIterationMeta(iterDir, i, "", "unclear", round)
			continue
		}
	}

	return s.maxIterationsResult(cfg.MaxIterations), nil
}

func (s *featureFinalReviewLoopState) maxIterationsResult(iterations int) *FeatureFinalReviewResult {
	return &FeatureFinalReviewResult{
		FinalStatus: "max_iterations",
		Iterations:  iterations,
		LastError:   fmt.Sprintf("final review hit MaxIterations=%d without APPROVED verdict", s.cfg.MaxIterations),
	}
}

// resumePending runs the fix or handoff a resumed iteration still owes; its
// review already ran.
func (s *featureFinalReviewLoopState) resumePending(iteration int, iterDir string, meta iterationMeta, fixNumber int, consecutiveFailures *int) (*FeatureFinalReviewResult, bool) {
	feedback := ""
	if data, err := os.ReadFile(filepath.Join(iterDir, "review-feedback.md")); err == nil {
		feedback = strings.TrimSpace(string(data))
	}
	round := roundFromMeta(meta)
	switch meta.Pending {
	case pendingFix:
		return s.fixStage(iteration, iterDir, feedback, round, fixNumber, consecutiveFailures)
	case pendingHandoff:
		if prompt := loadHandoffPrompt(filepath.Join(iterDir, handoffDirName)); prompt != nil {
			return s.handOff(iteration, iterDir, feedback, round, fixNumber, prompt)
		}
	}
	return nil, false
}

// fixStage runs the fix after a changes-requested review. The round is
// recorded as owing its fix first, so an interrupted fix resumes. A nil
// result means the loop continues; owed reports a round the loop owes.
func (s *featureFinalReviewLoopState) fixStage(iteration int, iterDir, feedback string, round finalReviewRound, fixNumber int, consecutiveFailures *int) (*FeatureFinalReviewResult, bool) {
	round.pending = pendingFix
	s.writeIterationMeta(iterDir, iteration, "", frMetaChangesRequested, round)
	round.pending = ""

	s.setReviewFixing(true)
	fix := s.runFix(finalFixRun{
		Iteration:    iteration,
		Dir:          iterDir,
		Role:         "fix",
		Feedback:     feedback,
		FeedbackPath: filepath.Join(iterDir, "review-feedback.md"),
	})
	decided := s.recordDecisions(fmt.Sprintf("Iteration %d fix", iteration), reviewDecisionPairs(fix.QA, ""))
	failed := fix.Err != nil || (fix.Status != agentStatusSuccess && fix.Status != "")

	// The fix round's work commits immediately, whatever the session's
	// outcome. FR has no later absorbing round before the next review, so a
	// FAILED or protocol-violating fixer's partial edits must not strand the
	// worktree dirty: the next review reads that state, and an approval over
	// dirt fails the feature. A fix session that never started (dispatch
	// error) left the tree untouched, so the hook no-ops on the clean
	// worktree.
	if err := s.commitFixRound(iteration, fixNumber); err != nil {
		return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: iteration, LastError: err.Error()}, false
	}
	if s.interruptedAfter(failed) {
		return &FeatureFinalReviewResult{FinalStatus: "interrupted", Iterations: iteration - 1}, false
	}
	round.changed = reposChangedSince(s.workspace.RepoPaths, round.state)
	s.history.recordFix(iteration, round.changed)

	if fix.Status == agentStatusProtocolViolation {
		violations := protocolViolationsFromError(fix.Err)
		return s.recordFixProtocolViolation(iterDir, iteration, violations, consecutiveFailures, round), false
	}

	if failed {
		*consecutiveFailures = *consecutiveFailures + 1
		agentStatus := "FAILED"
		if fix.Err == nil {
			agentStatus = fix.Status
		}
		s.writeIterationMeta(iterDir, iteration, agentStatus, frMetaChangesRequested, round)
		if *consecutiveFailures >= s.maxConsecFails() {
			return &FeatureFinalReviewResult{
				FinalStatus: "safety_rail",
				Iterations:  iteration,
				LastError:   fmt.Sprintf("consecutive-failure rail tripped after %d iterations", iteration),
			}, false
		}
		return nil, false
	}

	outcome, violations, validateErr := Validate(feature.PhaseReview, roleFinalReviewFixer, iterDir)
	if validateErr != nil {
		return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: iteration, LastError: validateErr.Error()}, false
	}
	if !outcome.OK {
		return s.recordFixProtocolViolation(iterDir, iteration, violations, consecutiveFailures, round), false
	}

	*consecutiveFailures = 0
	if !round.changed && !decided {
		reason := fmt.Sprintf("Fix round %d made no code changes, so another review round would repeat the same findings.", fixNumber)
		return s.raiseHandoff(iteration, iterDir, feedback, reviewHandoff{Reason: reason, Findings: feedback, FixerNote: fix.Note}, round, fixNumber)
	}
	s.writeIterationMeta(iterDir, iteration, "", frMetaChangesRequested, round)
	return nil, false
}

// interruptedAfter reports a pause-stop, or a shutdown that ended a session.
func (s *featureFinalReviewLoopState) interruptedAfter(sessionFailed bool) bool {
	if isFeatureInterrupted(s.cfg.FeatureStore, s.cfg.Feature.ID) {
		return true
	}
	return sessionFailed && s.sm != nil && waitForShutdownIntent(s.sm, shutdownDetectionGrace)
}

// approve ends the loop with an approval. Review-approved code must already
// be committed code: every fix round commits through RoundCommitHook, so a
// dirty worktree at approval is a fixer bug, not a sweep condition. Fail
// loudly instead of silently stranding changes past CodeReady.
func (s *featureFinalReviewLoopState) approve(iterDir string, iteration int, round finalReviewRound) *FeatureFinalReviewResult {
	if err := s.rejectDirtyWorktreesAtApproval(); err != nil {
		return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: iteration, LastError: err.Error()}
	}
	s.writeIterationMeta(iterDir, iteration, "", "approved", round)
	return &FeatureFinalReviewResult{FinalStatus: finalStatusReviewPassed, Iterations: iteration}
}

// raiseHandoff writes the handoff for iteration and asks it.
func (s *featureFinalReviewLoopState) raiseHandoff(iteration int, iterDir, feedback string, h reviewHandoff, round finalReviewRound, fixNumber int) (*FeatureFinalReviewResult, bool) {
	dir := filepath.Join(iterDir, handoffDirName)
	prompt, err := prepareHandoff(dir, iteration, h, implementationReviewGateLabel(implementationReviewGateFinal), handoffAcceptLabel, "the feature moves on to Publish")
	if err == nil {
		err = saveHandoffPrompt(dir, prompt)
	}
	if err != nil {
		return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: iteration, LastError: err.Error()}, false
	}
	return s.handOff(iteration, iterDir, feedback, round, fixNumber, prompt)
}

// handOff asks the user how Final Review should proceed once it stopped
// converging. The question runs in a fix session so it travels the existing
// ask-user path; with guidance, that session also applies it. The question
// stays pending across restarts and session errors until answered. A nil
// result means the loop continues; owed reports the round that reviews the
// guidance.
func (s *featureFinalReviewLoopState) handOff(iteration int, iterDir, feedback string, round finalReviewRound, fixNumber int, prompt *reviewHandoffPromptInput) (*FeatureFinalReviewResult, bool) {
	handoffDir := filepath.Join(iterDir, handoffDirName)
	round.pending = pendingHandoff
	s.writeIterationMeta(iterDir, iteration, "", frMetaChangesRequested, round)
	round.pending = ""

	detail := ""
	if prompt.Answer == "" || classifyHandoffAnswer(prompt.Answer) == handoffContinue {
		s.setReviewFixing(true)
		fix := s.runFix(finalFixRun{
			Iteration:    iteration,
			Dir:          handoffDir,
			Role:         handoffDirName,
			Feedback:     feedback,
			FeedbackPath: filepath.Join(iterDir, "review-feedback.md"),
			Handoff:      prompt,
		})
		if answer, ok := handoffAnswer(fix.QA, prompt.Question); ok && prompt.Answer == "" {
			prompt.Answer = answer
			if err := saveHandoffPrompt(handoffDir, prompt); err != nil {
				log.Printf("final review: %v", err)
			}
		}
		s.recordDecisions(fmt.Sprintf("Iteration %d handoff", iteration), reviewDecisionPairs(fix.QA, prompt.Question))
		failed := fix.Err != nil || (fix.Status != agentStatusSuccess && fix.Status != "")
		if err := s.commitFixRound(iteration, fixNumber); err != nil {
			return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: iteration, LastError: err.Error()}, false
		}
		if s.interruptedAfter(failed) {
			return &FeatureFinalReviewResult{FinalStatus: "interrupted", Iterations: iteration - 1}, false
		}
		if failed {
			if fix.Err != nil {
				detail = fmt.Sprintf(" (the handoff session failed: %v)", fix.Err)
			} else {
				detail = fmt.Sprintf(" (the handoff session ended %s)", fix.Status)
			}
			if prompt.Answer != "" && classifyHandoffAnswer(prompt.Answer) == handoffContinue {
				return s.parkHandoff(iteration, prompt, detail), false
			}
		}
	}

	switch classifyHandoffAnswer(prompt.Answer) {
	case handoffAccept:
		round.handoff = reviewHandoffAccepted
		return s.approve(iterDir, iteration, round), false
	case handoffStop:
		round.handoff = reviewHandoffStopped
		s.writeIterationMeta(iterDir, iteration, "", finalStatusStopped, round)
		return &FeatureFinalReviewResult{FinalStatus: finalStatusStopped, Iterations: iteration, LastError: prompt.Reason}, false
	case handoffContinue:
		round.handoff = reviewHandoffContinued
		round.changed = round.changed || reposChangedSince(s.workspace.RepoPaths, round.state)
		s.history.recordFix(iteration, round.changed)
		s.history.resetStreaks()
		s.writeIterationMeta(iterDir, iteration, "", frMetaChangesRequested, round)
		return nil, true
	default:
		return s.parkHandoff(iteration, prompt, detail), false
	}
}

// parkHandoff stops the loop with the handoff still pending, so resuming
// asks again; it is never a failure.
func (s *featureFinalReviewLoopState) parkHandoff(iteration int, prompt *reviewHandoffPromptInput, detail string) *FeatureFinalReviewResult {
	return &FeatureFinalReviewResult{
		FinalStatus: finalStatusStopped,
		Iterations:  iteration,
		LastError:   fmt.Sprintf("Final Review needs your decision, and the handoff question has no answer yet%s: %s Resume the feature to ask again; see %s.", detail, prompt.Reason, prompt.NotePath),
	}
}

func (s *featureFinalReviewLoopState) commitFixRound(iteration, fixNumber int) error {
	if s.cfg.RoundCommitHook == nil {
		return nil
	}
	if err := s.cfg.RoundCommitHook(RoundCommitInput{
		FeatureID: s.cfg.Feature.ID,
		Iteration: iteration,
		Kind:      RoundCommitFinalReviewFix,
		FixNumber: fixNumber,
		Repos:     s.workspace.RepoPaths,
	}); err != nil {
		return fmt.Errorf("committing final review fix round %d: %w", fixNumber, err)
	}
	return nil
}

func (s *featureFinalReviewLoopState) decisionsPath() string {
	return filepath.Join(s.artifactDir, reviewDecisionsFile)
}

// recordDecisions records a session's decisions in the decisions file and
// reports whether any were recorded.
func (s *featureFinalReviewLoopState) recordDecisions(heading string, qa []ports.QAPair) bool {
	recorded, err := appendReviewDecisions(s.decisionsPath(), heading, qa)
	if err != nil {
		log.Printf("final review: %v", err)
	}
	return recorded
}

// latestFixerNote returns the most recent fixer note before iteration.
func (s *featureFinalReviewLoopState) latestFixerNote(iteration int) string {
	paths := fixNotePaths(s.artifactDir, 1, iteration)
	if len(paths) == 0 {
		return ""
	}
	data, err := os.ReadFile(paths[len(paths)-1])
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *featureFinalReviewLoopState) finalReviewAxes() []implementationReviewAxis {
	profile := feature.PipelineMoonshot
	if s.cfg.Feature != nil {
		profile = s.cfg.Feature.EffectivePipeline()
	}
	return implementationReviewAxesForGate(implementationReviewGateFinal, implementationReviewAxisSelection{
		Profile:          profile,
		AnyPhaseFrontend: s.cfg.Feature.AnyRoadmapPhaseFrontend(),
	})
}

// runReview launches the round's review axes for iteration i. Cwd at the
// active run dir, --add-dir for every Feature.Repos worktree. Each axis
// writes its own review-feedback.md; the aggregate lands at iterDir.
func (s *featureFinalReviewLoopState) runReview(iteration int, iterDir string, plan []reviewAxisRun) (reviewStatus, string, map[string]string, error) {
	sessionConfig, err := resolveOrchestratorSessionConfig(s.cfg, llm.PhaseReview)
	if err != nil {
		return reviewFailed, "", nil, fmt.Errorf("resolving final review session configuration for iteration %d: %w", iteration, err)
	}
	cfg := s.cfg
	cfg.ReviewModel = sessionConfig.Model
	cfg.ReviewEffectiveEffort = sessionConfig.EffectiveEffort
	cfg.ReviewEffortSource = sessionConfig.EffortSource
	cfg.AskingClause = sessionConfig.AskingClause
	cfg.CompletionTool = sessionConfig.CompletionTool

	feedbackPath := filepath.Join(iterDir, "review-feedback.md")
	_ = os.Remove(feedbackPath)
	removeCompletionReceipt(iterDir)

	status, feedback, verdicts, err := s.runFinalReviewAxes(iteration, iterDir, cfg, plan)
	if err != nil {
		return status, feedback, nil, err
	}
	if err := os.WriteFile(feedbackPath, []byte(feedback), 0o644); err != nil {
		return reviewFailed, "", nil, fmt.Errorf("writing final review aggregate feedback: %w", err)
	}
	return status, feedback, verdicts, nil
}

func (s *featureFinalReviewLoopState) runFinalReviewAxes(iteration int, iterDir string, cfg OrchestratorConfig, plan []reviewAxisRun) (reviewStatus, string, map[string]string, error) {
	if err := RecordReadOnlyRepoBaseline(context.Background(), cfg.CommandRunner, cfg.Feature, iterDir); err != nil {
		return reviewFailed, "", nil, fmt.Errorf("record final review axes read-only repo baseline: %w", err)
	}

	axisNames := make([]string, 0, len(plan))
	scheduled := make(map[string]bool, len(plan))
	for _, run := range plan {
		axisNames = append(axisNames, run.Axis.Name)
		scheduled[run.Axis.Slug] = true
	}
	setMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID, axisNames)
	for _, axis := range s.axes {
		if !scheduled[axis.Slug] && s.history.approved(axis.Slug) {
			setMultiAxisValidatorState(cfg.FeatureStore, cfg.Feature.ID, axis.Name, ReviewApproved.String(), "")
		}
	}
	defer clearMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID)

	// The round's context cancels on pause-stop (feature interrupted) and when
	// any validator hits a non-retryable provider failure, so no validator
	// keeps running or retrying after the round is decided.
	roundCtx, cancelOnInterrupt := verificationContext(cfg.FeatureStore, cfg.Feature.ID)
	defer cancelOnInterrupt()
	roundCtx, cancelRound := context.WithCancel(roundCtx)
	defer cancelRound()

	validationCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun).Child()
	validationStart := time.Now()
	cfg.Observer.ValidationStarted(validationCtx, "final_review", len(plan))

	results := make([]reviewAxisResult, len(plan))
	runMultiAxisReviews(len(plan), func(i int) {
		run := plan[i]
		axisCtx := validationCtx.Child()
		axisStart := time.Now()
		cfg.Observer.ValidatorStarted(axisCtx, run.Axis.Name)

		status, feedback, err := s.runFinalReviewAxis(roundCtx, iteration, iterDir, run, axisCtx, cfg)
		results[i] = reviewAxisResult{Axis: run.Axis.Name, Status: status, Feedback: feedback, Error: err}
		if isProviderUnavailableError(err) || isProviderUsageLimitError(err) {
			cancelRound()
		}

		verdict := status.String()
		switch {
		case isHelperInterruptedError(err):
			verdict = validatorStateInterrupted
		case err != nil:
			verdict = "error"
		}
		cfg.Observer.ValidatorCompleted(axisCtx, run.Axis.Name, verdict, time.Since(axisStart))
		updateMultiAxisValidatorStatus(cfg.FeatureStore, cfg.Feature.ID, run.Axis.Name, status, err)
	})
	if roundErr := finalReviewRoundAbort(cfg, results); roundErr != nil {
		verdict := "error"
		if isHelperInterruptedError(roundErr) {
			verdict = validatorStateInterrupted
		}
		cfg.Observer.ValidationCompleted(validationCtx, "final_review", verdict, time.Since(validationStart), len(plan))
		return reviewFailed, "", nil, roundErr
	}
	if !isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
		violations, guardErr := EnforceReadOnlyRepoMutations(context.Background(), cfg.CommandRunner, cfg.Feature, feature.PhaseFinalReview, iterDir)
		if guardErr != nil {
			return reviewFailed, "", nil, fmt.Errorf("enforce final review axes read-only repo guard: %w", guardErr)
		}
		if len(violations) > 0 {
			return reviewFailed, "", nil, newProtocolViolationError(roleImplementationReviewCleanliness, iterDir, violations)
		}
	}

	status, feedback, err := composeMultiAxisReviewFeedback("Multi-Axis Final Review", results, len(plan))
	verdict := status.String()
	if err != nil {
		verdict = "error"
	}
	cfg.Observer.ValidationCompleted(validationCtx, "final_review", verdict, time.Since(validationStart), len(plan))
	if err != nil {
		return status, feedback, nil, err
	}
	verdicts := make(map[string]string, len(plan))
	for i, run := range plan {
		verdicts[run.Axis.Slug] = results[i].Status.String()
	}
	return status, feedback, verdicts, nil
}

// finalReviewRoundAbort returns the error that decides a round without a
// verdict: a non-retryable provider failure (which also cancelled its
// siblings) wins over the interrupt it caused; otherwise a stopped feature
// or interrupted validator aborts the round as interrupted.
func finalReviewRoundAbort(cfg OrchestratorConfig, results []reviewAxisResult) error {
	for _, result := range results {
		if isProviderUsageLimitError(result.Error) {
			return fmt.Errorf("%s validator: %w", result.Axis, result.Error)
		}
	}
	for _, result := range results {
		if isProviderUnavailableError(result.Error) {
			return fmt.Errorf("%s validator: %w", result.Axis, result.Error)
		}
	}
	for _, result := range results {
		if isHelperInterruptedError(result.Error) {
			return fmt.Errorf("%s validator: %w", result.Axis, result.Error)
		}
	}
	if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
		return fmt.Errorf("final review round: %w", errHelperInterrupted)
	}
	return nil
}

func (s *featureFinalReviewLoopState) runFinalReviewAxis(ctx context.Context, iteration int, iterDir string, run reviewAxisRun, parentCtx observe.SpanContext, cfg OrchestratorConfig) (reviewStatus, string, error) {
	axis := run.Axis
	axisSlug := axis.Slug
	axisDir := filepath.Join(iterDir, axisSlug)
	if err := os.MkdirAll(axisDir, 0o755); err != nil {
		return reviewFailed, "", fmt.Errorf("creating %s final review helper directory: %w", axis.Name, err)
	}
	removeCompletionReceipt(axisDir)

	feedbackPath := filepath.Join(axisDir, "review-feedback.md")
	_ = os.Remove(feedbackPath)

	diffBase := featureDefaultDiffBase(cfg.Feature)
	intent := resolvePromptIntent(cfg.Feature)
	prompt := buildImplementationReviewAxisPromptWithOpts(implementationReviewAxisPromptOpts{
		Gate:                  implementationReviewGateFinal,
		AxisLabel:             axis.Name,
		FeatureDescription:    intent.Description,
		DesignArtifactPath:    cfg.Feature.DesignArtifactPath(),
		LiveRunAxis:           axis.ExecutionPosture == reviewPostureLiveRun,
		ExitCriteria:          intent.ExitCriteria,
		AcceptanceClause:      intent.AcceptanceClause,
		DiffBase:              diffBase,
		RefactorPassForkPoint: refactorPassForkPoint(cfg.Feature),
		Iteration:             iteration,
		RoadmapPath:           finalReviewArtifactPath(s.stateDir, cfg.Feature, "roadmap"),
		PlanPath:              finalReviewArtifactPath(s.stateDir, cfg.Feature, "plan"),
		PhaseType:             cfg.Feature.RoadmapPhaseType,
		IterDir:               iterDir,
		FeedbackPath:          feedbackPath,
	})
	prompt += "\n" + buildReviewRoundSection(s.reviewRoundInput(iteration, run))
	if cfg.Feature != nil {
		if block := visualReferencesSection(cfg.Feature.Images, "conducting this final review"); block != "" {
			prompt = block + prompt
		}
	}

	// Only the active run is mounted; the containing feature state directory
	// also contains sealed predecessor runs.
	additionalDirs := append([]string{s.workspace.Cwd}, additionalDirsExcludingStateDir(s.workspace, s.stateDir)...)
	additionalDirs = append(additionalDirs, guidelineAdditionalDirs(cfg.GuidelinesDir)...)
	// Mount the active run first; the state dir stays available so the agent can
	// navigate ./<run>/<phase>/... to read prior artifacts.
	additionalDirs = append([]string{s.workspace.Cwd}, additionalDirs...)

	helper := &PhaseRunner{
		SessionManager: s.sm,
		FeatureStore:   cfg.FeatureStore,
		StateDir:       cfg.StateDir,
		SkillsDir:      cfg.SkillsDir,
		GuidelinesDir:  cfg.GuidelinesDir,
		Observer:       cfg.Observer,
		BuildSessionFn: cfg.BuildSession,
	}
	helperCfg := reviewHelperConfig{
		SessionID:              s.featureFinalReviewSessionID(axisSlug, iteration),
		FeatureID:              cfg.Feature.ID,
		Phase:                  feature.PhaseFinalReview,
		ContractPhase:          feature.PhaseReview,
		ParentSpanCtx:          parentCtx,
		Model:                  cfg.ReviewModel,
		Prompt:                 prompt,
		PromptPath:             filepath.Join(axisDir, "review-prompt.md"),
		FeedbackPath:           feedbackPath,
		HelperIterDir:          axisDir,
		Role:                   axis.Role,
		WorkDir:                s.workspace.Cwd,
		AdditionalDirs:         additionalDirs,
		LogPath:                filepath.Join(axisDir, "review-output.txt"),
		SystemPromptPrefix:     "final-review-" + axisSlug,
		CompletionAskingClause: cfg.AskingClause,
		CompletionTool:         cfg.CompletionTool,
		EffortLevel:            cfg.ReviewEffectiveEffort,
		EffectiveEffort:        cfg.ReviewEffectiveEffort,
		EffortSource:           cfg.ReviewEffortSource,
		Kind:                   ports.KindValidator,
		Label:                  axis.Name,
		Posture:                axis.ExecutionPosture,
		OnRetry: func(_ int, err error) {
			markMultiAxisValidatorRetrying(cfg.FeatureStore, cfg.Feature.ID, axis.Name, err)
		},
	}
	helperResult, err := helper.runReviewHelper(ctx, helperCfg)
	if err != nil {
		feedback := ""
		if helperResult != nil {
			feedback = helperResult.Feedback
		}
		if isHelperInterruptedError(err) {
			return reviewFailed, feedback, err
		}
		if _, statErr := os.Stat(feedbackPath); os.IsNotExist(statErr) {
			stub := formatStructuredReviewFeedback(
				fmt.Sprintf("%s Final Review — Helper Failed", axis.Name),
				fmt.Sprintf("- **Critical**: %s final review axis terminated before writing review-feedback.md: %v", axis.Name, err),
				"",
				reviewChangesRequested,
			)
			_ = os.WriteFile(feedbackPath, []byte(stub), 0o644)
			feedback = stub
		}
		return reviewChangesRequested, feedback, err
	}
	return helperResult.Status, helperResult.Feedback, nil
}

// reviewRoundInput scopes an axis's round: the decisions that outrank the
// design's acceptance criteria and, after its first review, its open
// findings or approval plus the fix delta since then.
func (s *featureFinalReviewLoopState) reviewRoundInput(iteration int, run reviewAxisRun) reviewRoundUserInput {
	in := reviewRoundUserInput{
		GateLabel:        implementationReviewGateLabel(implementationReviewGateFinal),
		AcceptanceSource: "the design's acceptance criteria",
		Mode:             run.Mode,
		LiveRun:          run.Axis.ExecutionPosture == reviewPostureLiveRun,
		RoadmapPath:      finalReviewArtifactPath(s.stateDir, s.cfg.Feature, "roadmap"),
		UserAnswerPaths:  userAnswerPaths(filepath.Dir(s.artifactDir)),
		DecisionsPath:    existingPath(s.decisionsPath()),
	}
	if run.Mode == reviewRoundFull {
		return in
	}
	in.Since = run.Since
	in.PriorFeedbackPath = filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", run.Since), run.Axis.Slug, "review-feedback.md")
	in.Deltas = reviewRoundDeltas(s.workspace.RepoPaths, run.SinceHeads)
	in.FixNotePaths = fixNotePaths(s.artifactDir, run.Since, iteration)
	return in
}

// rejectDirtyWorktreesAtApproval enforces the invariant that a Final Review
// approval is only emitted over committed code. Every fix round commits
// through RoundCommitHook; a fixer that edits files and ends its turn without
// the round commit landing would strand the feature at CodeReady with a
// dirty worktree, blocking every aftercare follow-up that demands a clean
// tree (rebase and refactor passes). Known untracked root orchestration
// artifacts (scrubbed by the publish path) are tolerated. No-op when no
// RoundCommitHook is wired (per-round commits disabled).
func (s *featureFinalReviewLoopState) rejectDirtyWorktreesAtApproval() error {
	if s.cfg.RoundCommitHook == nil {
		return nil
	}
	var dirty []string
	for name, path := range s.workspace.RepoPaths {
		if path == "" {
			continue
		}
		dirtyNow, err := git.HasUncommittedChangesExcludingUntracked(path, FinalReviewRootOrchestrationArtifacts...)
		if err != nil {
			// An indeterminate worktree must never read as clean at an
			// approval gate.
			return fmt.Errorf("final review approval cleanliness check failed for repo %s: %w", name, err)
		}
		if dirtyNow {
			dirty = append(dirty, name)
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	sort.Strings(dirty)
	return fmt.Errorf(
		"final review approved but repo(s) still have uncommitted changes — the fix round failed to commit its work: %s",
		strings.Join(dirty, ", "))
}

// finalFixRun is one fix session: a regular fix round, or the handoff that
// first asks the user how to proceed.
type finalFixRun struct {
	Iteration    int
	Dir          string
	Role         string
	Feedback     string
	FeedbackPath string
	Handoff      *reviewHandoffPromptInput
}

type finalFixResult struct {
	Status string
	Err    error
	QA     []ports.QAPair
	Note   string
}

// runFix launches one feature-level fix session. Same --add-dir set as the
// implementer (every Feature.Repos worktree) so the fix agent can address
// review findings in any repo. The session's user answers and closing note
// are returned for the decisions file and the next round.
func (s *featureFinalReviewLoopState) runFix(run finalFixRun) finalFixResult {
	cfg := s.cfg
	sessionConfig, err := resolveOrchestratorSessionConfig(cfg, llm.PhaseImplementation)
	if err != nil {
		return finalFixResult{Err: fmt.Errorf("resolving final review fix session configuration for iteration %d: %w", run.Iteration, err)}
	}
	cfg.Model = sessionConfig.Model
	cfg.ImplEffectiveEffort = sessionConfig.EffectiveEffort
	cfg.ImplEffortSource = sessionConfig.EffortSource
	cfg.AskingClause = sessionConfig.AskingClause
	cfg.CompletionTool = sessionConfig.CompletionTool

	intent := resolvePromptIntent(cfg.Feature)
	prompt := buildFinalFixPrompt(finalFixPromptOpts{
		Feedback:              run.Feedback,
		FeedbackPath:          run.FeedbackPath,
		ExitCriteria:          intent.ExitCriteria,
		AcceptanceClause:      intent.AcceptanceClause,
		Iteration:             run.Iteration,
		Publishable:           cfg.Feature.IsPublishable(),
		Images:                cfg.Feature.Images,
		RefactorPassForkPoint: refactorPassForkPoint(cfg.Feature),
		RoadmapPath:           finalReviewArtifactPath(s.stateDir, cfg.Feature, "roadmap"),
		UserAnswerPaths:       userAnswerPaths(filepath.Dir(s.artifactDir)),
		DecisionsPath:         existingPath(s.decisionsPath()),
		Handoff:               run.Handoff,
	})

	_ = os.WriteFile(filepath.Join(run.Dir, "fix-prompt.md"), []byte(prompt), 0o644)

	systemPrompt := buildRoleSystemPrompt(roleSystemPromptInput{
		Spec:           finalReviewFixerRoleSpec,
		IterationDir:   run.Dir,
		SkillsDir:      cfg.SkillsDir,
		GuidelinesDir:  cfg.GuidelinesDir,
		AskingClause:   cfg.AskingClause,
		CompletionTool: cfg.CompletionTool,
	})

	removeCompletionReceipt(run.Dir)

	additionalDirs := append([]string{s.workspace.Cwd}, additionalDirsExcludingStateDir(s.workspace, s.stateDir)...)
	additionalDirs = append(additionalDirs, guidelineAdditionalDirs(cfg.GuidelinesDir)...)

	command, env, sessOpts, buildErr := cfg.BuildSession(BuildSessionOpts{
		Model:              cfg.Model,
		Prompt:             prompt,
		SystemPrompt:       systemPrompt,
		AdditionalDirs:     additionalDirs,
		AgentNames:         []string{},
		PIDDir:             s.stateDir,
		PermHandler:        permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, ""),
		WorkDir:            s.workspace.Cwd,
		EffortLevel:        cfg.ImplEffectiveEffort,
		Phase:              feature.PhaseReview,
		CompletionProtocol: true,
	})
	if buildErr != nil {
		return finalFixResult{Err: fmt.Errorf("building feature fix agent session: %w", buildErr)}
	}
	sessOpts = enableTurnContinuation(sessOpts)
	if cfg.ImplEffectiveEffort != "" {
		sessOpts.EffectiveEffort = cfg.ImplEffectiveEffort
		sessOpts.EffortSource = cfg.ImplEffortSource
	}
	sessOpts.RunNumber = cfg.Feature.ActiveRun
	writeDebugPrompts(run.Dir, sessOpts.DebugSystemPrompt, prompt)

	sessionID := s.featureFinalReviewSessionID(run.Role, run.Iteration)

	sess, startErr := s.sm.StartSession(sessionID, cfg.Feature.ID, feature.PhaseFinalReview, command, s.workspace.Cwd, env, sessOpts)
	if startErr != nil {
		if errors.Is(startErr, ports.ErrSessionShuttingDown) {
			return finalFixResult{Err: fmt.Errorf("session manager shutting down")}
		}
		return finalFixResult{Err: fmt.Errorf("starting feature fix agent session: %w", startErr)}
	}
	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	sessionCtx, sessionStart, observed := s.observeFinalReviewSession(sess, sessionID, providerName, cfg.Model)
	defer func() {
		cost := extractSessionCost(sess)
		_ = accumulateSessionCostToFeatureKey(cfg.FeatureStore, cfg.Feature.ID, feature.PhaseFinalReview.DirName(), cost, sessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: feature.PhaseFinalReview.String(),
		})
		if observed {
			cfg.Observer.SessionEnded(sessionCtx, feature.PhaseFinalReview.String(), sessionID, "", toSessionUsage(cost, sess), time.Since(sessionStart), sessionErrFromStatus(sess))
		}
	}()

	logFile, logErr := os.Create(filepath.Join(run.Dir, "fix-output.txt"))
	if logErr != nil {
		return finalFixResult{Err: fmt.Errorf("creating fix log file: %w", logErr)}
	}
	sess.SetLogFile(logFile)

	waitResult := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{
		CommitOutcome: func(intent llm.CompletionIntent) ([]ProtocolViolation, error) {
			_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{
				Phase:       feature.PhaseReview,
				Role:        roleFinalReviewFixer,
				ArtifactDir: run.Dir,
				SessionID:   sessionID,
				Intent:      intent,
			})
			return violations, err
		},
	})
	result := finalFixResult{Status: waitResult.Status, Err: waitResult.Err, QA: sess.QALog(), Note: sessionClosingNote(sess)}
	if result.Note != "" {
		_ = os.WriteFile(filepath.Join(run.Dir, fixNotesFile), []byte(result.Note+"\n"), 0o644)
	}
	if result.Err == nil && result.Status == agentStatusProtocolViolation {
		result.Err = newProtocolViolationError(roleFinalReviewFixer, run.Dir, waitResult.ProtocolViolations)
	}
	return result
}

func (s *featureFinalReviewLoopState) recordFixProtocolViolation(iterDir string, iteration int, violations []ProtocolViolation, consecutiveFailures *int, round finalReviewRound) *FeatureFinalReviewResult {
	*consecutiveFailures = *consecutiveFailures + 1
	lastErr := formatProtocolViolationError(roleFinalReviewFixer, iterDir, violations)
	s.writeIterationMeta(iterDir, iteration, agentStatusProtocolViolation, frMetaChangesRequested, round)
	if *consecutiveFailures >= s.maxConsecFails() {
		return &FeatureFinalReviewResult{
			FinalStatus: BoundedHelperStatusProtocolViolation,
			Iterations:  iteration,
			LastError:   lastErr,
		}
	}
	return nil
}

func (s *featureFinalReviewLoopState) maxConsecFails() int {
	if s.cfg.MaxConsecFails != 0 {
		return s.cfg.MaxConsecFails
	}
	return 3
}

// featureFinalReviewSessionID returns the session ID stem for a feature-
// level FR iteration. Format: <featureID>-<role>-<NN>. The legacy per-repo
// suffix collapses since the FR session owns every repo at once.
func (s *featureFinalReviewLoopState) featureFinalReviewSessionID(role string, iteration int) string {
	return fmt.Sprintf("%s-%s-%02d", s.cfg.Feature.ID, role, iteration)
}

func (s *featureFinalReviewLoopState) writeIterationMeta(iterDir string, iteration int, agentStatus, reviewStatus string, round finalReviewRound) {
	meta := iterationMeta{
		Iteration:    iteration,
		AgentStatus:  agentStatus,
		ReviewStatus: reviewStatus,
		StartedAt:    time.Now(),
		MadeProgress: round.changed,
		AxisVerdicts: round.verdicts,
		ReviewHeads:  round.state.Heads,
		ReviewDirt:   round.state.Dirt,
		Handoff:      round.handoff,
		Pending:      round.pending,
	}
	am := &artifactManager{}
	_ = am.writeMeta(iterDir, meta)
}

func (s *featureFinalReviewLoopState) setReviewFixing(fixing bool) {
	if s.cfg.FeatureStore == nil {
		return
	}
	_ = s.cfg.FeatureStore.Modify(s.cfg.Feature.ID, func(f *feature.Feature) error {
		f.ReviewFixing = fixing
		return nil
	})
}

func (s *featureFinalReviewLoopState) observeFinalReviewSession(sess ports.SessionHandle, sessionID, providerName, model string) (observe.SpanContext, time.Time, bool) {
	cfg := s.cfg
	if cfg.Observer == nil {
		return observe.SpanContext{}, time.Time{}, false
	}
	sessionCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun).Child()
	cfg.Observer.SessionStarted(sessionCtx, feature.PhaseFinalReview.String(), sessionID, providerName, model, "", string(cfg.ImplEffectiveEffort), string(cfg.ImplEffortSource))
	(&contextReadTracker{
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		Observer:      cfg.Observer,
	}).install(sess, sessionCtx, feature.PhaseFinalReview.String(), sessionID)
	return sessionCtx, time.Now(), true
}

// featureDefaultDiffBase returns the diff base the feature-level FR
// reviewer should use. Defaults to the first repo's BaseBranch, falling
// back to "main" when none is set. Per-repo BaseBranch overrides are
// rare in multi-repo features and deferred to a follow-up if production
// data shows a need; the cumulative-diff review semantics imply one
// canonical base.
func featureDefaultDiffBase(f *feature.Feature) string {
	if f != nil {
		for _, r := range f.Repos {
			if strings.TrimSpace(r.BaseBranch) != "" {
				return r.BaseBranch
			}
		}
	}
	return "main"
}

// touchedReposFresh reads persisted state and returns repo names whose new
// RepoState carries Touched=true, sorted for deterministic atomic stamping.
// Wraps Feature.TouchedRepos in a fresh-load so the FR loop sees the latest
// committed dual-write rather than an in-memory shadow that may predate a
// crash-recovery resume.
func touchedReposFresh(store FeatureStore, f *feature.Feature) []string {
	fresh := f
	if store != nil {
		if loaded, err := store.Load(f.ID); err == nil && loaded != nil {
			fresh = loaded
		}
	}
	return fresh.TouchedRepos()
}
