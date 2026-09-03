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
// dir, --add-dir mounts every Feature.Repos worktree, and the latest
// harness-generated verification evidence is available as review context.
// The review-first, fix-second iteration order is expressed in the loop
// body, not by parameterising the phase-implement kernel.
package agent

import (
	"context"
	"fmt"
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

// finalReviewEffortLevel returns the effort level to pass to BuildSessionOpts:
// the resolved effective effort when set, otherwise the pipeline-driven level.
func finalReviewEffortLevel(cfg OrchestratorConfig) llm.EffortLevel {
	if cfg.EffectiveEffort != "" {
		return cfg.EffectiveEffort
	}
	return cfg.EffortLevel
}

// finalReviewAxisEffortLevel returns the effort level for final review axes
// (Review model): the resolved Review-role effort when set, falling back to
// EffectiveEffort then EffortLevel for legacy callers.
func finalReviewAxisEffortLevel(cfg OrchestratorConfig) llm.EffortLevel {
	if cfg.ReviewEffectiveEffort != "" {
		return cfg.ReviewEffectiveEffort
	}
	return finalReviewEffortLevel(cfg)
}

// finalReviewAxisEffectiveEffort returns the resolved Review-role effective
// effort for final review axes, falling back to EffectiveEffort for legacy.
func finalReviewAxisEffectiveEffort(cfg OrchestratorConfig) llm.EffortLevel {
	if cfg.ReviewEffectiveEffort != "" {
		return cfg.ReviewEffectiveEffort
	}
	return cfg.EffectiveEffort
}

// finalReviewAxisEffortSource returns the resolved Review-role effort source
// for final review axes, falling back to EffortSource for legacy.
func finalReviewAxisEffortSource(cfg OrchestratorConfig) llm.EffortSource {
	if cfg.ReviewEffectiveEffort != "" {
		return cfg.ReviewEffortSource
	}
	return cfg.EffortSource
}

// finalReviewFixEffortLevel returns the effort level for the fix agent
// (Implementation model): the resolved Implementation-role effort when set,
// falling back to EffectiveEffort then EffortLevel.
func finalReviewFixEffortLevel(cfg OrchestratorConfig) llm.EffortLevel {
	if cfg.ImplEffectiveEffort != "" {
		return cfg.ImplEffectiveEffort
	}
	return finalReviewEffortLevel(cfg)
}

// finalReviewFixEffectiveEffort returns the resolved Implementation-role
// effective effort for the fix agent, falling back to EffectiveEffort.
func finalReviewFixEffectiveEffort(cfg OrchestratorConfig) llm.EffortLevel {
	if cfg.ImplEffectiveEffort != "" {
		return cfg.ImplEffectiveEffort
	}
	return cfg.EffectiveEffort
}

// finalReviewFixEffortSource returns the resolved Implementation-role effort
// source for the fix agent, falling back to EffortSource.
func finalReviewFixEffortSource(cfg OrchestratorConfig) llm.EffortSource {
	if cfg.ImplEffectiveEffort != "" {
		return cfg.ImplEffortSource
	}
	return cfg.EffortSource
}

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
// reviewer requests changes. This inverted order lives in this loop
// function rather than as a kernel parameter so the control flow stays
// legible.
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
		return &FeatureFinalReviewResult{FinalStatus: finalStatusReviewPassed}, nil
	}

	// Build the cross-repo workspace. Cwd at the active run dir, with
	// --add-dir for every Feature.Repos worktree (and the active run).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
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

// featureFinalReviewLoopState carries the per-call state for the FR
// iteration loop. The outer RunFeatureFinalReviewLoop derives invariants
// once (workspace, contract path, staged repos); the run() method drives
// the iteration cursor.
type featureFinalReviewLoopState struct {
	cfg         OrchestratorConfig
	sm          ports.SessionManager
	workspace   WorkspaceSetup
	stateDir    string
	artifactDir string
	stagedRepos []string
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

	// Fix-round counter recovered from persisted iteration metas so a
	// crash-resumed loop numbers fix rounds identically to the interrupted
	// run: the fix following the Nth changes-requested review is "fix N".
	roundCommits := newFRRoundCommitTracker(am, s.artifactDir, startIter)

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

		// Inverted iteration order: review FIRST, fix SECOND.
		s.setReviewFixing(false)
		reviewStatus, feedback, reviewErr := s.runReview(i, iterDir)

		if reviewErr != nil {
			if isProtocolViolationError(reviewErr) {
				consecutiveFailures++
				s.writeIterationMetaWithAgent(iterDir, i, agentStatusProtocolViolation, "review_failed")
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
			s.writeIterationMetaWithAgent(iterDir, i, "", "review_failed")
			if consecutiveFailures >= maxConsecFails {
				return &FeatureFinalReviewResult{FinalStatus: "safety_rail", Iterations: i, LastError: reviewErr.Error()}, nil
			}
			continue
		}

		switch reviewStatus {
		case ReviewApproved:
			// Review-approved code must already be committed code: every fix
			// round commits through RoundCommitHook, so a dirty worktree at
			// approval is a fixer bug, not a sweep condition. Fail loudly
			// instead of silently stranding changes past CodeReady.
			if err := s.rejectDirtyWorktreesAtApproval(); err != nil {
				return &FeatureFinalReviewResult{FinalStatus: "failed", Iterations: i, LastError: err.Error()}, nil
			}
			s.writeIterationMeta(iterDir, i, "approved")
			return &FeatureFinalReviewResult{FinalStatus: finalStatusReviewPassed, Iterations: i}, nil

		case ReviewChangesRequested:
			roundCommits.changesRequested++
			s.setReviewFixing(true)
			fixStatus, fixErr := s.runFix(i, iterDir, feedback)

			// The fix round's work commits immediately, whatever the
			// session's outcome. FR has no later absorbing round before the
			// next review, so a FAILED or protocol-violating fixer's partial
			// edits must not strand the worktree dirty: the next review
			// reads that state, and an approval over dirt fails the feature.
			// A fix session that never started (dispatch error) left the
			// tree untouched, so the hook no-ops on the clean worktree.
			if s.cfg.RoundCommitHook != nil {
				if err := s.cfg.RoundCommitHook(RoundCommitInput{
					FeatureID: cfg.Feature.ID,
					Iteration: i,
					Kind:      RoundCommitFinalReviewFix,
					FixNumber: roundCommits.changesRequested,
					Repos:     s.workspace.RepoPaths,
				}); err != nil {
					return &FeatureFinalReviewResult{
						FinalStatus: "failed",
						Iterations:  i,
						LastError:   fmt.Sprintf("committing final review fix round %d: %v", roundCommits.changesRequested, err),
					}, nil
				}
			}

			if fixStatus == agentStatusProtocolViolation {
				violations := protocolViolationsFromError(fixErr)
				if done := s.recordFixProtocolViolation(iterDir, i, violations, &consecutiveFailures); done != nil {
					return done, nil
				}
				continue
			}

			if fixErr != nil || (fixStatus != agentStatusSuccess && fixStatus != "") {
				consecutiveFailures++
				agentStatus := "FAILED"
				if fixErr == nil {
					agentStatus = fixStatus
				}
				s.writeIterationMetaWithAgent(iterDir, i, agentStatus, frMetaChangesRequested)
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
			s.writeIterationMeta(iterDir, i, frMetaChangesRequested)
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
// Cwd at the active run dir, --add-dir for every Feature.Repos
// worktree. The reviewer reads the cumulative diff across all repos and
// writes review-feedback.md at iterDir.
func (s *featureFinalReviewLoopState) runReview(iteration int, iterDir string) (ReviewStatus, string, error) {
	sessionConfig, err := resolveOrchestratorSessionConfig(s.cfg, llm.PhaseReview)
	if err != nil {
		return ReviewFailed, "", fmt.Errorf("resolving final review session configuration for iteration %d: %w", iteration, err)
	}
	cfg := s.cfg
	cfg.ReviewModel = sessionConfig.Model
	cfg.ReviewEffectiveEffort = sessionConfig.EffectiveEffort
	cfg.ReviewEffortSource = sessionConfig.EffortSource
	cfg.AskingClause = sessionConfig.AskingClause

	feedbackPath := filepath.Join(iterDir, "review-feedback.md")
	_ = os.Remove(feedbackPath)
	RemoveCompletionReceipt(iterDir)

	status, feedback, err := s.runFinalReviewAxes(iteration, iterDir, cfg)
	if err != nil {
		return status, feedback, err
	}
	if err := os.WriteFile(feedbackPath, []byte(feedback), 0o644); err != nil {
		return ReviewFailed, "", fmt.Errorf("writing final review aggregate feedback: %w", err)
	}
	return status, feedback, nil
}

func (s *featureFinalReviewLoopState) runFinalReviewAxes(iteration int, iterDir string, cfg OrchestratorConfig) (ReviewStatus, string, error) {
	if err := RecordReadOnlyRepoBaseline(context.Background(), cfg.CommandRunner, cfg.Feature, iterDir); err != nil {
		return ReviewFailed, "", fmt.Errorf("record final review axes read-only repo baseline: %w", err)
	}
	profile := feature.PipelineMoonshot
	if cfg.Feature != nil {
		profile = cfg.Feature.EffectivePipeline()
	}
	axes := implementationReviewAxesForGate(implementationReviewGateFinal, implementationReviewAxisSelection{
		Profile:          profile,
		AnyPhaseFrontend: cfg.Feature.AnyRoadmapPhaseFrontend(),
	})
	if len(axes) == 0 {
		feedback := FormatStructuredReviewFeedback("Multi-Axis Final Review", "", "", ReviewApproved)
		return ReviewApproved, feedback, nil
	}

	axisNames := make([]string, 0, len(axes))
	for _, axis := range axes {
		axisNames = append(axisNames, axis.Name)
	}
	setMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID, axisNames)
	defer clearMultiAxisValidatorStatuses(cfg.FeatureStore, cfg.Feature.ID)

	validationCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun).Child()
	validationStart := time.Now()
	cfg.Observer.ValidationStarted(validationCtx, "final_review", len(axes))

	results := make([]reviewAxisResult, len(axes))
	runMultiAxisReviews(len(axes), func(i int) {
		axis := axes[i]
		axisCtx := validationCtx.Child()
		axisStart := time.Now()
		cfg.Observer.ValidatorStarted(axisCtx, axis.Name)

		status, feedback, err := s.runFinalReviewAxis(iteration, iterDir, axis, axisCtx, cfg)
		results[i] = reviewAxisResult{Axis: axis.Name, Status: status, Feedback: feedback, Error: err}

		verdict := status.String()
		if err != nil {
			verdict = "error"
		}
		cfg.Observer.ValidatorCompleted(axisCtx, axis.Name, verdict, time.Since(axisStart))
		updateMultiAxisValidatorStatus(cfg.FeatureStore, cfg.Feature.ID, axis.Name, status, err)
	})
	if !isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
		violations, guardErr := EnforceReadOnlyRepoMutations(context.Background(), cfg.CommandRunner, cfg.Feature, feature.PhaseFinalReview, iterDir)
		if guardErr != nil {
			return ReviewFailed, "", fmt.Errorf("enforce final review axes read-only repo guard: %w", guardErr)
		}
		if len(violations) > 0 {
			return ReviewFailed, "", newProtocolViolationError(RoleImplementationReviewCleanliness, iterDir, violations)
		}
	}

	status, feedback, err := composeMultiAxisReviewFeedback("Multi-Axis Final Review", results, len(axes))
	verdict := status.String()
	if err != nil {
		verdict = "error"
	}
	cfg.Observer.ValidationCompleted(validationCtx, "final_review", verdict, time.Since(validationStart), len(axes))
	return status, feedback, err
}

func (s *featureFinalReviewLoopState) runFinalReviewAxis(iteration int, iterDir string, axis implementationReviewAxis, parentCtx observe.SpanContext, cfg OrchestratorConfig) (ReviewStatus, string, error) {
	axisSlug := implementationReviewAxisSlug(axis.Name)
	axisDir := filepath.Join(iterDir, axisSlug)
	if err := os.MkdirAll(axisDir, 0o755); err != nil {
		return ReviewFailed, "", fmt.Errorf("creating %s final review helper directory: %w", axis.Name, err)
	}
	feedbackPath := filepath.Join(axisDir, "review-feedback.md")
	// Stop/restart resume: preserve a completed clean verdict and only
	// redispatch axes whose local handoff is incomplete or invalid.
	// Axis completion is decided by committed outcome receipts (axis helpers
	// commit Phase=PhaseReview, Role=axis.Role): a prior receipt survives to
	// short-circuit a completed axis, and any stale or invalid receipt is
	// removed before redispatch.
	if HasCommittedPhaseOutcome(axisDir, feature.PhaseReview, axis.Role) {
		if cached, err := ParseReviewFeedback(feedbackPath); err == nil && len(cached.ProtocolViolations) == 0 {
			return cached.Verdict, cached.Body, nil
		}
	}
	RemoveCompletionReceipt(axisDir)
	_ = os.Remove(feedbackPath)

	diffBase := featureDefaultDiffBase(cfg.Feature)
	priorEvidence := priorImplementationEvidenceContextForRun(filepath.Dir(s.artifactDir))
	intent := resolvePromptIntent(cfg.Feature)
	prompt := BuildImplementationReviewAxisPromptWithOpts(ImplementationReviewAxisPromptOpts{
		Gate:                                 implementationReviewGateFinal,
		AxisLabel:                            axis.Name,
		FeatureDescription:                   intent.Description,
		DesignArtifactPath:                   cfg.Feature.DesignArtifactPath(),
		LiveRunAxis:                          axis.ExecutionPosture == implementationReviewPostureLiveRun,
		ExitCriteria:                         intent.ExitCriteria,
		AcceptanceClause:                     intent.AcceptanceClause,
		DiffBase:                             diffBase,
		RefactorPassForkPoint:                refactorPassForkPoint(cfg.Feature),
		PreviousFeedback:                     s.previousAggregateFeedback(iteration),
		Iteration:                            iteration,
		RoadmapPath:                          finalReviewArtifactPath(s.stateDir, cfg.Feature, "roadmap"),
		PlanPath:                             finalReviewArtifactPath(s.stateDir, cfg.Feature, "plan"),
		PhaseType:                            cfg.Feature.RoadmapPhaseType,
		IterDir:                              iterDir,
		FeedbackPath:                         feedbackPath,
		PriorImplementationReportPaths:       priorEvidence.ReportPaths,
		PriorImplementationEvidenceRootDirs:  priorEvidence.EvidenceRootDirs,
		PriorImplementationEvidenceArtifacts: priorEvidence.EvidenceArtifactPaths,
	})
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
		SessionManager:   s.sm,
		FeatureStore:     cfg.FeatureStore,
		Registry:         cfg.Registry,
		StateDir:         cfg.StateDir,
		SkillsDir:        cfg.SkillsDir,
		GuidelinesDir:    cfg.GuidelinesDir,
		Observer:         cfg.Observer,
		OnFeatureResumed: cfg.OnFeatureResumed,
		BuildSessionFn:   cfg.BuildSession,
	}
	helperCfg := ReviewHelperConfig{
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
		EffortLevel:            finalReviewAxisEffortLevel(cfg),
		EffectiveEffort:        finalReviewAxisEffectiveEffort(cfg),
		EffortSource:           finalReviewAxisEffortSource(cfg),
		Kind:                   ports.KindValidator,
		Label:                  axis.Name,
		ResumeFeature:          cfg.Feature,
		ResumeParent: ResumeParentContext{
			PhaseKey:  feature.PhaseFinalReview.DirName(),
			Iteration: iteration,
		},
		ResumeChildKey:     axisSlug,
		ResumePhaseContext: fmt.Sprintf("You were mid the %s final-review axis for iteration %d.", axis.Name, iteration),
	}
	var helperResult *ReviewHelperResult
	var err error
	switch axis.ExecutionPosture {
	case implementationReviewPostureLiveRun:
		helperResult, err = helper.RunLiveRunReviewHelper(context.Background(), helperCfg)
	default:
		helperResult, err = helper.RunReadOnlyReviewHelper(context.Background(), helperCfg)
	}
	if err != nil {
		feedback := ""
		if helperResult != nil {
			feedback = helperResult.Feedback
		}
		if _, statErr := os.Stat(feedbackPath); os.IsNotExist(statErr) {
			stub := FormatStructuredReviewFeedback(
				fmt.Sprintf("%s Final Review — Helper Failed", axis.Name),
				fmt.Sprintf("- **Critical**: %s final review axis terminated before writing review-feedback.md: %v", axis.Name, err),
				"",
				ReviewChangesRequested,
			)
			_ = os.WriteFile(feedbackPath, []byte(stub), 0o644)
			feedback = stub
		}
		return ReviewChangesRequested, feedback, err
	}
	return helperResult.Status, helperResult.Feedback, nil
}

func (s *featureFinalReviewLoopState) previousAggregateFeedback(iteration int) string {
	if iteration <= 1 {
		return ""
	}
	prevIterDir := filepath.Join(s.artifactDir, fmt.Sprintf("iteration-%02d", iteration-1))
	data, err := os.ReadFile(filepath.Join(prevIterDir, "review-feedback.md"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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

// runFix launches one feature-level fix session for iteration i. Same
// --add-dir set as the implementer (every Feature.Repos worktree) so the
// fix agent can address review findings in any repo.
func (s *featureFinalReviewLoopState) runFix(iteration int, iterDir, feedback string) (string, error) {
	cfg := s.cfg
	fixDir := filepath.Join(iterDir, "fix")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		return "", fmt.Errorf("creating final-review fix helper directory: %w", err)
	}
	// Stop/restart resume: a completed fix session committed its receipt into
	// fixDir (Phase=PhaseReview, Role=RoleFinalReviewFixer). Reuse it and
	// republish the receipt into the parent iteration directory so
	// receipt-based consumers see the completed unit.
	if HasCommittedPhaseOutcome(fixDir, feature.PhaseReview, RoleFinalReviewFixer) {
		if outcome, _, err := Validate(feature.PhaseReview, RoleFinalReviewFixer, fixDir); err == nil && outcome.OK {
			if err := republishCompletionReceipt(fixDir, iterDir); err != nil {
				return "", fmt.Errorf("restoring final-review iteration completion receipt: %w", err)
			}
			return agentStatusSuccess, nil
		}
	}
	sessionConfig, err := resolveOrchestratorSessionConfig(cfg, llm.PhaseImplementation)
	if err != nil {
		return "", fmt.Errorf("resolving final review fix session configuration for iteration %d: %w", iteration, err)
	}
	cfg.Model = sessionConfig.Model
	cfg.ImplEffectiveEffort = sessionConfig.EffectiveEffort
	cfg.ImplEffortSource = sessionConfig.EffortSource
	cfg.AskingClause = sessionConfig.AskingClause

	feedbackPath := filepath.Join(iterDir, "review-feedback.md")
	intent := resolvePromptIntent(cfg.Feature)
	prompt := BuildFinalFixPrompt(FinalFixPromptOpts{
		Feedback:              feedback,
		FeedbackPath:          feedbackPath,
		ExitCriteria:          intent.ExitCriteria,
		AcceptanceClause:      intent.AcceptanceClause,
		Iteration:             iteration,
		Publishable:           cfg.Feature.IsPublishable(),
		DesignArtifactPath:    cfg.Feature.DesignArtifactPath(),
		Images:                cfg.Feature.Images,
		RefactorPassForkPoint: refactorPassForkPoint(cfg.Feature),
	})

	_ = os.WriteFile(filepath.Join(fixDir, "fix-prompt.md"), []byte(prompt), 0o644)

	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          FinalReviewFixerRoleSpec(),
		IterationDir:  fixDir,
		SkillsDir:     cfg.SkillsDir,
		GuidelinesDir: cfg.GuidelinesDir,
		KBInfos:       cfg.KBInfos,
		AskingClause:  cfg.AskingClause,
	})

	RemoveCompletionReceipt(iterDir)
	RemoveCompletionReceipt(fixDir)

	additionalDirs := append([]string{s.workspace.Cwd}, additionalDirsExcludingStateDir(s.workspace, s.stateDir)...)
	additionalDirs = append(additionalDirs, guidelineAdditionalDirs(cfg.GuidelinesDir)...)
	helper := &PhaseRunner{
		SessionManager:   s.sm,
		FeatureStore:     cfg.FeatureStore,
		Registry:         cfg.Registry,
		StateDir:         cfg.StateDir,
		SkillsDir:        cfg.SkillsDir,
		GuidelinesDir:    cfg.GuidelinesDir,
		Observer:         cfg.Observer,
		OnFeatureResumed: cfg.OnFeatureResumed,
		BuildSessionFn:   cfg.BuildSession,
	}
	result, err := helper.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:                      s.featureFinalReviewSessionID("fix", iteration),
		FeatureID:                      cfg.Feature.ID,
		Phase:                          feature.PhaseFinalReview,
		Label:                          "final-review fixer",
		ObserverPhase:                  feature.PhaseFinalReview.DirName(),
		Model:                          cfg.Model,
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		WorkDir:                        s.workspace.Cwd,
		AdditionalDirs:                 additionalDirs,
		LogPath:                        filepath.Join(fixDir, "fix-output.txt"),
		EffortLevel:                    finalReviewFixEffortLevel(cfg),
		EffectiveEffort:                finalReviewFixEffectiveEffort(cfg),
		EffortSource:                   finalReviewFixEffortSource(cfg),
		PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, ""),
		CompletionDir:                  fixDir,
		ContractPhase:                  feature.PhaseReview,
		ContractRole:                   RoleFinalReviewFixer,
		SystemPromptHasUsefulResources: true,
		ResumeFeature:                  cfg.Feature,
		ResumeParent: ResumeParentContext{
			PhaseKey:  feature.PhaseFinalReview.DirName(),
			Iteration: iteration,
		},
		ResumeChildKey:     string(RoleFinalReviewFixer),
		ResumePhaseContext: fmt.Sprintf("You were mid the final-review fixer for iteration %d.", iteration),
		// The fixer replaced an interactive pipeline: user questions and
		// unhandled permission requests must pause for the desktop to answer,
		// not hard-fail the fix round.
		RouteUserInput: true,
	})
	// RunBoundedHelper owns the fix session's commit validation; a protocol
	// violation error carries the structured violations the loop needs to
	// report, so it is returned alongside the status instead of swallowed.
	if err != nil {
		if isProtocolViolationError(err) {
			return agentStatusProtocolViolation, err
		}
		return "", err
	}
	if result == nil || result.Status != BoundedHelperStatusCompleted {
		if result == nil {
			return "", fmt.Errorf("final-review fixer returned no result")
		}
		return result.Status, nil
	}
	// The helper committed its receipt into fixDir; republish it into the
	// parent iteration directory so receipt-based consumers see the completed
	// fix without re-running preflight against iterDir.
	if err := republishCompletionReceipt(fixDir, iterDir); err != nil {
		return "", fmt.Errorf("writing final-review iteration completion receipt: %w", err)
	}
	return agentStatusSuccess, nil
}

func (s *featureFinalReviewLoopState) recordFixProtocolViolation(iterDir string, iteration int, violations []ProtocolViolation, consecutiveFailures *int) *FeatureFinalReviewResult {
	*consecutiveFailures = *consecutiveFailures + 1
	lastErr := formatProtocolViolationError(RoleFinalReviewFixer, iterDir, violations)
	s.writeIterationMetaWithAgent(iterDir, iteration, agentStatusProtocolViolation, frMetaChangesRequested)
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
	effort := finalReviewFixEffectiveEffort(cfg)
	effortSource := finalReviewFixEffortSource(cfg)
	cfg.Observer.SessionStarted(sessionCtx, feature.PhaseFinalReview.String(), sessionID, providerName, model, "", string(effort), string(effortSource))
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
