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
// loop. One Claude session per iteration reads the cumulative diff across
// every Feature.Repos worktree. One verdict gates overall feature quality;
// FR atomicity (every staged repo records the same outcome, or all
// stay/fail together) is enforced via AtomicPhaseStamp.
//
// The loop intentionally does NOT carry RepoName: cwd is the feature state
// dir, --add-dir mounts every Feature.Repos worktree, and the testing
// contract is a feature-level artifact with `repo:`-tagged baseline rows.
// Cycle-specific divergence vs phase implement (review-first, fix-second
// instead of fix-first, review-second) is expressed in the loop body, not
// by parameterising the phase-implement kernel.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

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
//   - "failed":          dispatch error before iteration began.
//
// Repos is the deduplicated, sorted list of touched repos at loop entry —
// the canonical "FR-staged subset" the AtomicPhaseStamp wrote to.
type FeatureFinalReviewResult struct {
	FinalStatus          string
	Iterations           int
	LastError            string
	Repos                []string
	PlanRevisionFeedback string
}

// RunFeatureFinalReviewLoop runs one feature-level Final Review session per
// iteration. Cwd is the feature state dir; --add-dir mounts every
// Feature.Repos worktree. The reviewer reads the cumulative diff across all
// repos and emits one APPROVED / CHANGES_REQUESTED verdict.
//
// Iteration order is INVERTED relative to phase implement: review runs
// FIRST inside the iteration, and the fix agent runs SECOND when the
// reviewer requests changes. This is the cycle-specific divergence the
// PRD names; it lives in this loop function rather than as a kernel
// parameter so the control flow stays legible.
//
// FR atomicity guarantee: AtomicPhaseStamp commits the outcome in one
// FeatureStore.Modify write at the end of the loop. Either every staged
// repo records the FR-pass outcome (success) or every staged repo records
// the failure (max_iterations / safety_rail / dispatch error) — no
// partial FR shipment.
//
// Crash recovery: re-runs the interrupted unit (iteration N's review, or
// iteration N's fix) from scratch. Durable state on disk (verification
// reports, prior reviewer feedback, prior iteration meta) is the resume
// scaffolding. ArtifactManager.LatestIteration() picks up where the
// previous run stopped.
func RunFeatureFinalReviewLoop(cfg OrchestratorConfig, sm ports.SessionManager) (*FeatureFinalReviewResult, error) {
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
		return &FeatureFinalReviewResult{FinalStatus: "review_passed"}, nil
	}

	// Build the cross-repo workspace. Cwd at the feature state dir, with
	// --add-dir for every Feature.Repos worktree (and the state dir).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("feature final review loop: workspace setup: %w", err)
	}

	// FR artifact dir: feature-level under runs/run-NNN/review/. The
	// per-repo subdir collapses under the unified flow.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, feature.PhaseReview.DirName())

	// Compile and persist the feature-level FR testing contract once at
	// loop entry. The compiler emits per-repo baseline rows tagged with
	// `repo: <name>` plus any cross-repo verification items declared on
	// the feature. Plan-less mode: FR has no phase plan to inherit from.
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	if err := writeFeatureFinalReviewContract(contractPath, stagedRepos, cfg.Feature); err != nil {
		return nil, fmt.Errorf("feature final review loop: testing contract: %w", err)
	}

	// Mark mid-flight phase status at the feature level so observers can
	// surface "final reviewing" without per-repo lying.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "final_reviewing")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	loopState := &featureFinalReviewLoopState{
		cfg:          cfg,
		sm:           sm,
		workspace:    workspace,
		stateDir:     stateDir,
		artifactDir:  artifactDir,
		contractPath: contractPath,
		stagedRepos:  stagedRepos,
	}

	result, runErr := loopState.run()

	// Translate the loop outcome into atomic state transitions.
	switch {
	case runErr != nil:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: runErr.Error(),
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
	case "review_passed":
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

	case "interrupted":
		// No atomic stamp on interrupt; persisted state is left
		// untouched so the next start picks up the loop.
		result.Repos = stagedRepos
		return result, nil

	default:
		// max_iterations / safety_rail map to a feature-level FR failure.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: result.LastError,
		})
		result.Repos = stagedRepos
		return result, nil
	}
}

// RunFeatureCycleFinalReviewLoop runs one feature-level Final Review session
// per iteration for a post-publish cycle (e.g., post-tweak "review changes?
// y/n" modal "y" path). Unlike RunFeatureFinalReviewLoop, this entry:
//
//   - Reviews every Feature.Repos worktree (the feature's full repo set)
//     rather than the touched-only staged subset, because post-publish
//     cycles operate on already-shipped repos.
//   - Skips the AtomicPhaseStamp on success/failure: post-publish repo
//     state is unchanged by the FR's verdict; the surrounding cycle
//     (e.g., tweak commit/push chain) owns the post-FR transitions.
//   - Resolves the cycle artifact dir under f.CyclePrefix() so artifacts
//     live at runs/run-N/<cycle>-N/review/iteration-NN/.
//
// The iteration loop body (review FIRST, fix SECOND on CHANGES_REQUESTED,
// --add-dir to every Feature.Repos worktree, one verification report per
// iteration) is identical to RunFeatureFinalReviewLoop. This entry exists
// purely to elide the atomic-stamp wrapper for post-publish cycles.
//
// Cumulative-diff review semantics align with the unification principle:
// the post-tweak FR reviews every Feature.Repos cumulative diff, not just
// the repos the tweak modified. If the tweak only touched one repo, this
// is the degenerate len(Feature.Repos) == 1 case.
func RunFeatureCycleFinalReviewLoop(cfg OrchestratorConfig, sm ports.SessionManager) (*FeatureFinalReviewResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("feature cycle final review loop: feature is nil")
	}
	if cfg.FeatureStore == nil {
		return nil, fmt.Errorf("feature cycle final review loop: feature store is nil")
	}

	// Every Feature.Repos is in scope. The feature is post-publish; per-repo
	// state (Touched + PRURL) is preserved regardless of FR outcome.
	repos := make([]string, 0, len(cfg.Feature.Repos))
	for _, r := range cfg.Feature.Repos {
		repos = append(repos, r.Name)
	}
	sort.Strings(repos)
	if len(repos) == 0 {
		// Degenerate "no repos" case — nothing to review.
		return &FeatureFinalReviewResult{FinalStatus: "review_passed"}, nil
	}

	// Build the cross-repo workspace. Cwd at the feature state dir, with
	// --add-dir for every Feature.Repos worktree (and the state dir).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("feature cycle final review loop: workspace setup: %w", err)
	}

	// Cycle artifact dir: feature-level under runs/run-NNN/<cycle>-N/review/.
	// Falls back to runs/run-NNN/review/ when no active cycle is set (a
	// programming error for the post-cycle entry, but kept safe for tests).
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, feature.PhaseReview.DirName())
	if prefix := cfg.Feature.CyclePrefix(); prefix != "" {
		artifactDir = filepath.Join(runDir, prefix, feature.PhaseReview.DirName())
	}

	// Compile and persist a fresh feature-level FR testing contract at
	// loop entry. Plan-less mode emits per-repo baseline rows tagged
	// `repo: <name>` for every Feature.Repos.
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	if err := writeFeatureFinalReviewContract(contractPath, repos, cfg.Feature); err != nil {
		return nil, fmt.Errorf("feature cycle final review loop: testing contract: %w", err)
	}

	// Mark mid-flight phase status at the feature level so observers can
	// surface "final reviewing" without per-repo lying.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "final_reviewing")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	loopState := &featureFinalReviewLoopState{
		cfg:          cfg,
		sm:           sm,
		workspace:    workspace,
		stateDir:     stateDir,
		artifactDir:  artifactDir,
		contractPath: contractPath,
		stagedRepos:  repos,
	}

	result, runErr := loopState.run()

	// Post-cycle FR does NOT call AtomicPhaseStamp. The surrounding cycle
	// (CompleteTweakFinish for tweak; rebase / review-comments for those
	// cycles) owns the post-FR transitions on success; on failure the
	// cycle entry's FailRepoCycle path handles state cleanup.
	if runErr != nil {
		return &FeatureFinalReviewResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			Repos:       repos,
			Iterations:  result.Iterations,
		}, runErr
	}
	if result != nil {
		result.Repos = repos
	}
	return result, nil
}

// featureFinalReviewLoopState carries the per-call state for the FR
// iteration loop. The outer RunFeatureFinalReviewLoop derives invariants
// once (workspace, contract path, staged repos); the run() method drives
// the iteration cursor.
type featureFinalReviewLoopState struct {
	cfg          OrchestratorConfig
	sm           ports.SessionManager
	workspace    WorkspaceSetup
	stateDir     string
	artifactDir  string
	contractPath string
	stagedRepos  []string
}

// run executes the FR iteration loop. Returns a result with FinalStatus
// set; never returns a wrapped error because per-iteration failures are
// translated into safety-rail trips by the consecutive-failure counter.
func (s *featureFinalReviewLoopState) run() (*FeatureFinalReviewResult, error) {
	cfg := s.cfg

	// Recover from prior iterations on disk.
	am := NewArtifactManager(s.artifactDir)
	startIter := am.LatestIteration()

	consecutiveFailures := 0
	if startIter > 0 {
		for j := startIter; j >= 1; j-- {
			jDir := filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", j))
			meta, err := am.ReadMeta(jDir)
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

	maxConsecFails := cfg.MaxConsecFails
	if maxConsecFails == 0 {
		maxConsecFails = 3
	}

	for i := startIter + 1; i <= cfg.MaxIterations; i++ {
		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			return &FeatureFinalReviewResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
		}

		if cfg.FeatureStore != nil {
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.ReviewIteration = i
				return nil
			})
		}

		iterDir, mkdirErr := am.CreateIterationDir(i)
		if mkdirErr != nil {
			return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: mkdirErr.Error()}, nil
		}

		// Seed the iteration's verification report. On iter-1 we synthesize
		// a contract-shaped stub; on iter-N>1 we copy the prior iteration's
		// report so manual-verification attestation accumulates.
		if err := s.seedVerificationReport(iterDir); err != nil {
			return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: err.Error()}, nil
		}

		// Inverted iteration order: review FIRST, fix SECOND.
		s.setReviewFixing(false)
		reviewStatus, feedback, reviewErr := s.runReview(i, iterDir)

		if reviewErr != nil {
			if isProtocolViolationError(reviewErr) {
				consecutiveFailures++
				s.writeIterationMetaWithAgent(iterDir, i, agentStatusProtocolViolation, "review_failed")
				if consecutiveFailures >= maxConsecFails {
					return &FeatureFinalReviewResult{
						FinalStatus: "protocol_violation",
						Iterations:  i,
						LastError:   reviewErr.Error(),
					}, nil
				}
				continue
			}
			consecutiveFailures++
			s.writeIterationMetaWithAgent(iterDir, i, "", "review_failed")
			if consecutiveFailures >= maxConsecFails {
				return &FeatureFinalReviewResult{FinalStatus: "safety_rail", Iterations: i, LastError: reviewErr.Error()}, nil
			}
			continue
		}

		switch reviewStatus {
		case ReviewApproved:
			s.writeIterationMeta(iterDir, i, "approved")
			return &FeatureFinalReviewResult{FinalStatus: "review_passed", Iterations: i}, nil

		case ReviewChangesRequested:
			s.setReviewFixing(true)
			fixStatus, fixErr := s.runFix(i, iterDir, feedback)

			if fixStatus == agentStatusMissingMarker {
				violations := []ProtocolViolation{{
					Artifact: PhaseCompleteFile,
					Reason:   "SDK reported success but phase_complete was not present",
				}}
				if done := s.recordFixProtocolViolation(iterDir, i, violations, &consecutiveFailures); done != nil {
					return done, nil
				}
				continue
			}

			if fixErr != nil || (fixStatus != "SUCCESS" && fixStatus != "") {
				consecutiveFailures++
				agentStatus := "FAILED"
				if fixErr == nil {
					agentStatus = fixStatus
				}
				s.writeIterationMetaWithAgent(iterDir, i, agentStatus, "changes_requested")
				if consecutiveFailures >= maxConsecFails {
					return &FeatureFinalReviewResult{
						FinalStatus: "safety_rail",
						Iterations:  i,
						LastError:   fmt.Sprintf("consecutive-failure rail tripped after %d iterations", i),
					}, nil
				}
				continue
			}

			outcome, violations, validateErr := Validate(feature.PhaseReview, RoleFinalReviewFixer, iterDir)
			if validateErr != nil {
				return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: validateErr.Error()}, nil
			}
			if !outcome.OK {
				if done := s.recordFixProtocolViolation(iterDir, i, violations, &consecutiveFailures); done != nil {
					return done, nil
				}
				continue
			}

			consecutiveFailures = 0
			s.writeIterationMeta(iterDir, i, "changes_requested")
			continue

		default:
			s.writeIterationMeta(iterDir, i, "unclear")
			continue
		}
	}

	return &FeatureFinalReviewResult{
		FinalStatus: "max_iterations",
		Iterations:  cfg.MaxIterations,
		LastError:   fmt.Sprintf("final review hit MaxIterations=%d without APPROVED verdict", cfg.MaxIterations),
	}, nil
}

// runReview launches one feature-level review session for iteration i.
// Cwd at the feature state dir, --add-dir for every Feature.Repos
// worktree. The reviewer reads the cumulative diff across all repos and
// writes review-feedback.md at iterDir.
func (s *featureFinalReviewLoopState) runReview(iteration int, iterDir string) (ReviewStatus, string, error) {
	cfg := s.cfg

	// Read previous iteration's feedback (re-review).
	previousFeedback := ""
	if iteration > 1 {
		prevIterDir := filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", iteration-1))
		if data, err := os.ReadFile(filepath.Join(prevIterDir, "review-feedback.md")); err == nil {
			previousFeedback = strings.TrimSpace(string(data))
		}
	}

	feedbackPath := filepath.Join(iterDir, "review-feedback.md")
	_ = os.Remove(feedbackPath)
	RemovePhaseComplete(iterDir)

	verificationPath := filepath.Join(iterDir, "verification-report.yaml")

	// Use the configured diff-base from the FR-driver feature; the
	// feature-level FR uses the first repo's BaseBranch as a reasonable
	// default, since per-repo BaseBranch is normally identical
	// ("main"/"master") across a feature's repos. Callers may want a
	// more sophisticated rule later, but consolidating to one base
	// matches the "one verdict over the cumulative diff" semantics.
	diffBase := featureDefaultDiffBase(cfg.Feature)
	priorEvidence := priorImplementationEvidenceContextForRun(filepath.Dir(s.artifactDir))

	prompt := BuildFinalReviewPrompt(FinalReviewPromptOpts{
		FeatureDescription:                   cfg.Feature.Description,
		ExitCriteria:                         cfg.Feature.ExitCriteria,
		DiffBase:                             diffBase,
		WorkDir:                              s.workspace.Cwd,
		VerificationPath:                     verificationPath,
		TestingContractPath:                  s.contractPath,
		PreviousFeedback:                     previousFeedback,
		Iteration:                            iteration,
		RoadmapPath:                          cfg.Feature.Artifacts["roadmap"],
		DesignArtifactPath:                   cfg.Feature.DesignArtifactPath(),
		Images:                               cfg.Feature.Images,
		PhaseType:                            cfg.Feature.RoadmapPhaseType,
		FeedbackPath:                         feedbackPath,
		Publishable:                          cfg.Feature.IsPublishable(),
		PriorImplementationPlanPaths:         priorEvidence.PlanPaths,
		PriorImplementationContractPaths:     priorEvidence.ContractPaths,
		PriorImplementationReportPaths:       priorEvidence.ReportPaths,
		PriorImplementationEvidenceRootDirs:  priorEvidence.EvidenceRootDirs,
		PriorImplementationEvidenceArtifacts: priorEvidence.EvidenceArtifactPaths,
	})

	_ = os.WriteFile(filepath.Join(iterDir, "review-prompt.md"), []byte(prompt), 0o644)

	additionalDirs := append([]string(nil), additionalDirsExcludingStateDir(s.workspace, s.stateDir)...)
	additionalDirs = append(additionalDirs, guidelineAdditionalDirs(cfg.GuidelinesDir)...)
	// State dir always present so the agent can navigate ./<run>/<phase>/...
	additionalDirs = append([]string{s.stateDir}, additionalDirs...)

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          FinalReviewerRoleSpec(),
		IterationDir:  iterDir,
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		KBInfos:       cfg.KBInfos,
		AskingClause:  cfg.AskingClause,
	})

	command, env, sessOpts, buildErr := cfg.BuildSession(BuildSessionOpts{
		Model:                          cfg.ReviewModel,
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 additionalDirs,
		AgentNames:                     explorationAgentNames(),
		PIDDir:                         s.stateDir,
		PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.Config != nil && cfg.Config.AutoReview, cfg.PermissionCache, "", cfg.Classify, autoReviewDecisionHook(cfg.Observer, cfg.Feature)),
		WorkDir:                        s.workspace.Cwd,
		EffortLevel:                    cfg.EffortLevel,
		Phase:                          feature.PhaseReview,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(iterDir, PhaseCompleteFile),
	})
	if buildErr != nil {
		return ReviewFailed, "", fmt.Errorf("building feature final review session: %w", buildErr)
	}
	sessOpts = enableTruncatedTurnAutoResume(sessOpts)
	if cfg.FinishOrViolateNudge {
		sessOpts.TurnMode = ports.TurnModeInteractive
	}
	WriteDebugPrompts(iterDir, sessOpts.DebugSystemPrompt, prompt)

	sessionID := s.featureFinalReviewSessionID("final-review", iteration)

	sess, err := s.sm.StartSession(sessionID, cfg.Feature.ID, feature.PhaseReview, command, s.workspace.Cwd, env, sessOpts)
	if err != nil {
		if errors.Is(err, ports.ErrSessionShuttingDown) {
			return ReviewFailed, "", fmt.Errorf("session manager shutting down")
		}
		return ReviewFailed, "", fmt.Errorf("starting feature final review session: %w", err)
	}
	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	sessionCtx, sessionStart, observed := s.observeFinalReviewSession(sess, sessionID, providerName, cfg.ReviewModel)
	defer func() {
		cost := ExtractSessionCost(sess)
		_ = accumulateSessionCostToFeatureKey(cfg.FeatureStore, cfg.Feature.ID, feature.PhaseFinalReview.DirName(), cost, SessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: feature.PhaseFinalReview.String(),
		})
		if observed {
			cfg.Observer.SessionEnded(sessionCtx, feature.PhaseFinalReview.String(), sessionID, "", toSessionUsage(cost), time.Since(sessionStart), sessionErrFromStatus(sess))
		}
	}()

	logFile, err := os.Create(filepath.Join(iterDir, "review-response.txt"))
	if err != nil {
		return ReviewFailed, "", fmt.Errorf("creating review log file: %w", err)
	}
	sess.SetLogFile(logFile)

	agentStatus := waitForStatusDetailed(sess, s.sm, sessionID, waitForStatusOptions{
		ReadyCheck: func() bool {
			if HasPhaseComplete(iterDir) {
				sess.SetHasUnansweredQuestion(false)
				return true
			}
			return false
		},
		FinishOrViolateNudge: cfg.FinishOrViolateNudge,
		MissingArtifacts:     []string{"review-feedback.md"},
	}).Status

	if agentStatus == agentStatusMissingMarker {
		return ReviewFailed, "", newProtocolViolationError(RoleFinalReviewer, iterDir, []ProtocolViolation{{
			Artifact: PhaseCompleteFile,
			Reason:   "SDK reported success but phase_complete was not present",
		}})
	}
	if agentStatus != agentStatusSuccess {
		return ReviewFailed, "", fmt.Errorf("feature final review session did not complete successfully (status: %s)", agentStatus)
	}

	outcome, violations, validateErr := Validate(feature.PhaseReview, RoleFinalReviewer, iterDir)
	if validateErr != nil {
		return ReviewFailed, "", fmt.Errorf("validating final reviewer contract: %w", validateErr)
	}
	if !outcome.OK {
		if outcome.ReviewFeedback != nil && !outcome.ReviewFeedback.OK() {
			synth := FormatReviewProtocolViolationFeedback(outcome.ReviewFeedback)
			_ = os.WriteFile(feedbackPath, []byte(synth), 0o644)
		}
		return ReviewFailed, "", newProtocolViolationError(RoleFinalReviewer, iterDir, violations)
	}
	if outcome.ReviewFeedback == nil {
		return ReviewFailed, "", fmt.Errorf("validating final reviewer contract: validated without review feedback")
	}
	return outcome.ReviewFeedback.Verdict, strings.TrimSpace(outcome.ReviewFeedback.Body), nil
}

// runFix launches one feature-level fix session for iteration i. Same
// --add-dir set as the implementer (every Feature.Repos worktree) so the
// fix agent can address review findings in any repo.
func (s *featureFinalReviewLoopState) runFix(iteration int, iterDir, feedback string) (string, error) {
	cfg := s.cfg

	verificationReportPath := filepath.Join(iterDir, "verification-report.yaml")
	feedbackPath := filepath.Join(iterDir, "review-feedback.md")
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:               feedback,
		FeedbackPath:           feedbackPath,
		ExitCriteria:           cfg.Feature.ExitCriteria,
		IterDir:                iterDir,
		VerificationReportPath: verificationReportPath,
		Iteration:              iteration,
		Publishable:            cfg.Feature.IsPublishable(),
		DesignArtifactPath:     cfg.Feature.DesignArtifactPath(),
		Images:                 cfg.Feature.Images,
	})

	_ = os.WriteFile(filepath.Join(iterDir, "fix-prompt.md"), []byte(prompt), 0o644)

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          FinalReviewFixerRoleSpec(),
		IterationDir:  iterDir,
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		KBInfos:       cfg.KBInfos,
		AskingClause:  cfg.AskingClause,
	})

	RemovePhaseComplete(iterDir)

	additionalDirs := append([]string{s.stateDir}, additionalDirsExcludingStateDir(s.workspace, s.stateDir)...)
	additionalDirs = append(additionalDirs, guidelineAdditionalDirs(cfg.GuidelinesDir)...)

	command, env, sessOpts, buildErr := cfg.BuildSession(BuildSessionOpts{
		Model:                          cfg.Model,
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 additionalDirs,
		AgentNames:                     []string{},
		PIDDir:                         s.stateDir,
		PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.Config != nil && cfg.Config.AutoReview, cfg.PermissionCache, "", cfg.Classify, autoReviewDecisionHook(cfg.Observer, cfg.Feature)),
		WorkDir:                        s.workspace.Cwd,
		EffortLevel:                    cfg.EffortLevel,
		Phase:                          feature.PhaseReview,
		SystemPromptHasUsefulResources: true,
		MarkerPath:                     filepath.Join(iterDir, PhaseCompleteFile),
	})
	if buildErr != nil {
		return "", fmt.Errorf("building feature fix agent session: %w", buildErr)
	}
	sessOpts = enableTruncatedTurnAutoResume(sessOpts)
	if cfg.FinishOrViolateNudge {
		sessOpts.TurnMode = ports.TurnModeInteractive
	}
	WriteDebugPrompts(iterDir, sessOpts.DebugSystemPrompt, prompt)

	sessionID := s.featureFinalReviewSessionID("fix", iteration)

	sess, startErr := s.sm.StartSession(sessionID, cfg.Feature.ID, feature.PhaseReview, command, s.workspace.Cwd, env, sessOpts)
	if startErr != nil {
		if errors.Is(startErr, ports.ErrSessionShuttingDown) {
			return "", fmt.Errorf("session manager shutting down")
		}
		return "", fmt.Errorf("starting feature fix agent session: %w", startErr)
	}
	providerName := ""
	if sessOpts != nil {
		providerName = sessOpts.ProviderName
	}
	sessionCtx, sessionStart, observed := s.observeFinalReviewSession(sess, sessionID, providerName, cfg.Model)
	defer func() {
		cost := ExtractSessionCost(sess)
		_ = accumulateSessionCostToFeatureKey(cfg.FeatureStore, cfg.Feature.ID, feature.PhaseFinalReview.DirName(), cost, SessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: feature.PhaseFinalReview.String(),
		})
		if observed {
			cfg.Observer.SessionEnded(sessionCtx, feature.PhaseFinalReview.String(), sessionID, "", toSessionUsage(cost), time.Since(sessionStart), sessionErrFromStatus(sess))
		}
	}()

	logFile, logErr := os.Create(filepath.Join(iterDir, "fix-output.txt"))
	if logErr != nil {
		return "", fmt.Errorf("creating fix log file: %w", logErr)
	}
	sess.SetLogFile(logFile)

	agentStatus := waitForStatusDetailed(sess, s.sm, sessionID, waitForStatusOptions{
		ReadyCheck: func() bool {
			if HasPhaseComplete(iterDir) {
				sess.SetHasUnansweredQuestion(false)
				return true
			}
			return false
		},
		FinishOrViolateNudge: cfg.FinishOrViolateNudge,
		MissingArtifacts:     []string{"verification-report.yaml"},
	}).Status
	return agentStatus, nil
}

func (s *featureFinalReviewLoopState) recordFixProtocolViolation(iterDir string, iteration int, violations []ProtocolViolation, consecutiveFailures *int) *FeatureFinalReviewResult {
	*consecutiveFailures = *consecutiveFailures + 1
	lastErr := formatProtocolViolationError(RoleFinalReviewFixer, iterDir, violations)
	s.writeIterationMetaWithAgent(iterDir, iteration, agentStatusProtocolViolation, "changes_requested")
	if *consecutiveFailures >= s.maxConsecFails() {
		return &FeatureFinalReviewResult{
			FinalStatus: "protocol_violation",
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

// seedVerificationReport pre-populates iterDir/verification-report.yaml
// from the prior iteration when present, otherwise from the contract.
// Mirrors writeFinalReviewArtifacts but keyed off the feature-level
// contract path rather than the per-repo cycle resolver.
func (s *featureFinalReviewLoopState) seedVerificationReport(iterDir string) error {
	target := filepath.Join(iterDir, "verification-report.yaml")
	if seed := priorIterationFinalReviewReportPath(s.artifactDir, iterDir); seed != "" {
		if err := copyFile(seed, target); err == nil {
			return nil
		}
	}
	contract, err := ReadTestingContract(s.contractPath)
	if err != nil {
		return fmt.Errorf("reading feature FR testing contract: %w", err)
	}
	if err := WriteVerificationReport(target, BuildContractVerificationReportStub(contract, s.contractPath)); err != nil {
		return fmt.Errorf("writing feature FR verification report stub: %w", err)
	}
	return nil
}

func (s *featureFinalReviewLoopState) writeIterationMeta(iterDir string, iteration int, reviewStatus string) {
	meta := IterationMeta{
		Iteration:    iteration,
		ReviewStatus: reviewStatus,
		StartedAt:    time.Now(),
	}
	am := &ArtifactManager{}
	_ = am.WriteMeta(iterDir, meta)
}

func (s *featureFinalReviewLoopState) writeIterationMetaWithAgent(iterDir string, iteration int, agentStatus, reviewStatus string) {
	meta := IterationMeta{
		Iteration:    iteration,
		AgentStatus:  agentStatus,
		ReviewStatus: reviewStatus,
		StartedAt:    time.Now(),
	}
	am := &ArtifactManager{}
	_ = am.WriteMeta(iterDir, meta)
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
	cfg.Observer.SessionStarted(sessionCtx, feature.PhaseFinalReview.String(), sessionID, providerName, model, "")
	(&ContextReadTracker{
		KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		Observer:      cfg.Observer,
	}).Install(sess, sessionCtx, feature.PhaseFinalReview.String(), sessionID)
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
func touchedReposFresh(store ports.FeatureStore, f *feature.Feature) []string {
	fresh := f
	if store != nil {
		if loaded, err := store.Load(f.ID); err == nil && loaded != nil {
			fresh = loaded
		}
	}
	return fresh.TouchedRepos()
}

// writeFeatureFinalReviewContract compiles and persists the feature-level
// FR testing contract: per-repo baseline rows tagged `repo: <name>` plus
// any cross-repo verification items declared on the feature. Plan-less
// because FR has no phase plan to inherit from — implementation-phase
// plans already gated their own per-iteration reviews.
func writeFeatureFinalReviewContract(path string, repos []string, f *feature.Feature) error {
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    repos,
		PlanLess: true,
		PlanPath: path,
	})
	if err := WriteTestingContract(path, contract); err != nil {
		return fmt.Errorf("writing feature FR testing contract: %w", err)
	}
	return nil
}
