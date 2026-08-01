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

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ImplementConfig holds configuration for the implementation loop.
type ImplementConfig struct {
	Feature             *feature.Feature
	FeatureStore        ports.FeatureStore // for reading updated help answers
	WorkDir             string
	PlanPath            string // absolute path to the plan artifact; the agent reads it via tool use
	MaxIterations       int
	MaxConsecFails      int
	MaxConsecNoProgress int
	ExitCriteria        string
	Model               string
	ReviewModel         string
	// ResolveSessionConfig reloads the persisted feature configuration when a
	// new provider session is about to start. The currently running session
	// keeps its snapshot; later implementation iterations and review batches
	// receive edits made while the loop is active.
	ResolveSessionConfig func(llm.PhaseRole) (SessionRuntimeConfig, error)
	ArtifactDir          string // base dir for iteration artifacts
	StateDir             string // feature state directory for PID files
	RunDir               string // active run directory granted to the agent; empty derives from Feature.ActiveRun
	// SessionIDPrefix namespaces cycle sessions beneath the feature ID. For
	// example "rebase-2" yields "<featureID>-rebase-2-impl-01".
	SessionIDPrefix string

	// AdditionalDirs are extra directories to pass as --add-dir flags to the
	// claude CLI, giving the agent file-system access beyond WorkDir and StateDir.
	// Under the unified phase-implement flow this carries every repo in
	// Feature.Repos beyond the cwd state dir; the per-stage upstream context
	// injection of the legacy per-repo flow is gone.
	AdditionalDirs []string

	// KBInfos are repo knowledge base paths to include in prompts.
	KBInfos []KBInfo

	// PhaseType is the roadmap phase type ("tracer-bullet", "tdd-fill-in", "collapsed", or "").
	// Used to inject phase-aware review criteria.
	PhaseType string

	// RoadmapPath is the absolute path to the approved roadmap artifact.
	// Included in the review prompt so the reviewer has full context.
	RoadmapPath string

	// DesignArtifactPath is the absolute path to the design design
	// document, if one was produced. Retained for caller compatibility;
	// implementation prompts no longer re-inject it.
	DesignArtifactPath string

	// DangerouslySkipPermissions enables --dangerously-skip-permissions for
	// interactive sessions, replacing the default --permission-mode flag.
	DangerouslySkipPermissions bool

	// PermissionCache is the shared permission cache for auto-approving
	// previously remembered tool requests. Nil means no caching.
	PermissionCache *permission.Cache

	// CommandRunner executes harness-owned verification commands declared in
	// the testing contract. It is required whenever the contract contains a
	// harness-owned command.
	CommandRunner ports.CommandRunner

	// BuildSession creates CLI command args, env vars, and session opts
	// by routing through the provider registry. In tests, provide a mock
	// function. In production, set to PhaseRunner.BuildSession.
	BuildSession func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)

	// Observer is the observability facade for lifecycle event emission (nil = no-op).
	Observer *observe.Observer

	// OnVerificationProgress is called after each persisted harness
	// verification status transition.
	OnVerificationProgress func(featureID string)

	// RepoName is the repo name for session ID namespacing in multi-repo features.
	RepoName string

	// EffortLevel is the pipeline-driven effort level passed to providers.
	EffortLevel llm.EffortLevel

	// EffectiveEffort is the resolved provider-safe effort level for the
	// implementation agent sessions. When non-empty, it overrides EffortLevel
	// in BuildSessionOpts so the provider command receives the
	// capability-resolved level rather than the raw pipeline level. Empty
	// means no effort resolution was performed (tests, legacy callers) and
	// EffortLevel is used directly.
	EffectiveEffort llm.EffortLevel
	// EffortSource records whether EffectiveEffort was derived from the
	// pipeline (auto) or an explicit user configuration (explicit). Stored on
	// the session and emitted in session.started for tracing.
	EffortSource llm.EffortSource

	// ReviewEffectiveEffort is the resolved Review-role effort for the
	// per-iteration implementation review axes. Review axes select the
	// configured Review model, so their effort is coupled to the Review role
	// rather than the Implementation role. When non-empty, it overrides the
	// implementation effort in the review helper's BuildSessionOpts and is
	// recorded on the session for observability. Empty means no Review effort
	// resolution was performed and the implementation effort is used as
	// fallback.
	ReviewEffectiveEffort llm.EffortLevel
	// ReviewEffortSource records whether ReviewEffectiveEffort was
	// auto-derived or explicitly configured.
	ReviewEffortSource llm.EffortSource

	// SkillsDir is the path to the reconciled skills directory on disk.
	SkillsDir string

	// GuidelinesDir is the path to the reconciled guidelines directory on disk.
	GuidelinesDir string

	// OnReviewingChange is called when the implementation loop enters or exits
	// the review gate. This allows the orchestrator to update per-repo status
	// to "reviewing". The bool parameter is true when entering review, false
	// when exiting.
	OnReviewingChange func(reviewing bool)

	// SessionStartFunc overrides SessionManager.StartSession in tests.
	// When non-nil, called instead of sm.StartSession. Return
	// ports.ErrSessionShuttingDown to exit the loop cleanly.
	SessionStartFunc func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error)

	// AskingClause is the pre-resolved "Asking Questions" prompt section
	// from the PromptAdapter for the implementation model. Set by PhaseRunner
	// before launching the loop.
	AskingClause string

	// SkipIterationReview, when true, causes the implementation loop to skip
	// the per-iteration review gate on SUCCESS and immediately return
	// "review_passed". Medium and Large profiles set this to true in the
	// single-repo path, relying on the Final Review for quality gating.
	// Moonshot, multi-repo orchestration, and repo-scoped cycles keep
	// per-iteration review enabled.
	SkipIterationReview bool
}

// SessionRuntimeConfig is the role-specific configuration snapshot used to
// build one newly-created provider session.
type SessionRuntimeConfig struct {
	Model           string
	EffectiveEffort llm.EffortLevel
	EffortSource    llm.EffortSource
	AskingClause    string
}

func resolveImplementSessionConfig(cfg ImplementConfig, role llm.PhaseRole) (SessionRuntimeConfig, error) {
	if cfg.ResolveSessionConfig != nil {
		return cfg.ResolveSessionConfig(role)
	}
	if role == llm.PhaseReview {
		return SessionRuntimeConfig{
			Model:           cfg.ReviewModel,
			EffectiveEffort: cfg.ReviewEffectiveEffort,
			EffortSource:    cfg.ReviewEffortSource,
			AskingClause:    cfg.AskingClause,
		}, nil
	}
	return SessionRuntimeConfig{
		Model:           cfg.Model,
		EffectiveEffort: cfg.EffectiveEffort,
		EffortSource:    cfg.EffortSource,
		AskingClause:    cfg.AskingClause,
	}, nil
}

// LoopResult represents the outcome of the full implementation loop.
//
// FinalStatus values:
//   - "review_passed":     iteration emitted SUCCESS and the review gate APPROVED.
//   - "plan_revision_required": implementation found a phase-plan contract
//     defect with structured requirements that must go through phase-plan
//     revision.
//   - "max_iterations":    hit cfg.MaxIterations without a passing review.
//   - "safety_rail":       no-progress / consecutive-failure rail tripped.
//   - "interrupted":       shutdown / feature stopped while running.
//   - "need_user_input":   harness verification found a capability-protected
//     check that requires a user decision. NeedUserInputPath points to the
//     harness-owned gate artifact and Iterations identifies its iteration.
type LoopResult struct {
	FinalStatus string
	Iterations  int
	LastError   string

	// PlanRevisionFeedback carries phase-plan repair requirements when
	// FinalStatus == "plan_revision_required".
	PlanRevisionFeedback string

	// NeedUserInputPath is the absolute path of the harness-owned gate artifact
	// written when FinalStatus == "need_user_input". Empty otherwise.
	NeedUserInputPath string
}

// RunImplementationLoop manages the iterative agent loop.
// It creates sessions for each iteration, monitors progress, and handles
// the review gate. Returns when SUCCESS+APPROVED, max iterations reached,
// or safety rails triggered.
func RunImplementationLoop(cfg ImplementConfig, sm ports.SessionManager) (result *LoopResult, err error) {
	am := NewArtifactManager(cfg.ArtifactDir)
	pt := NewProgressTracker()
	progressPath := filepath.Join(cfg.ArtifactDir, "progress.md")
	summaryPath := filepath.Join(cfg.ArtifactDir, "summary.log")
	aggregateLogPath := filepath.Join(cfg.ArtifactDir, "output.txt")

	// Phase-level instrumentation
	featureCtx := observe.SpanContextForFeature(cfg.Feature.ID, cfg.Feature.TraceID, cfg.Feature.Name, cfg.Feature.FeatureSpanID).WithRun(cfg.Feature.ActiveRun)
	phaseCtx := featureCtx.Child()
	phaseStart := time.Now()
	cfg.Observer.PhaseStarted(phaseCtx, "implement")
	defer func() {
		var finalErr error
		if err != nil {
			finalErr = err
		} else if result != nil && result.FinalStatus != finalStatusReviewPassed {
			finalErr = fmt.Errorf("%s", result.FinalStatus)
			if result.LastError != "" {
				finalErr = fmt.Errorf("%s: %s", result.FinalStatus, result.LastError)
			}
		}
		cfg.Observer.PhaseCompleted(phaseCtx, "implement", time.Since(phaseStart), finalErr)
	}()

	// Resume from the latest completed iteration, if any.
	startIter := am.LatestIteration()
	var consecutiveFailures int
	var reviewerFeedback string
	// Iteration number of the most recent CHANGES_REQUESTED review. RETRY
	// iterations swap the full feedback for a pointer to this iteration's
	// review-feedback.md (see retryReviewFeedbackReminder).
	var lastChangesRequestedIter int

	// Mid-iteration restart: if the next iteration's dir already has a valid
	// harness completion receipt (but no meta.yaml, otherwise LatestIteration
	// would have advanced past it), the implement phase finished before the
	// prior run was interrupted — likely during the review gate. Skip the
	// implement session and jump straight to the review for that iteration.
	skipImplement := false
	if nextIter := startIter + 1; nextIter <= cfg.MaxIterations {
		nextIterDir := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", nextIter))
		skipImplement = HasCommittedPhaseOutcome(nextIterDir, feature.PhaseImplement, RoleImplementer)
	}

	if startIter > 0 {
		// Recover consecutiveFailures: count trailing iterations that
		// consumed the unified failure budget before restart.
		for j := startIter; j >= 1; j-- {
			iterDir := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", j))
			meta, err := am.ReadMeta(iterDir)
			if err != nil || !isFailureBudgetAgentStatus(meta.AgentStatus) {
				break
			}
			consecutiveFailures++
		}
		// Recover reviewer feedback: walk backwards to find the most recent
		// CHANGES_REQUESTED review. FAILED iterations (e.g. API errors) and
		// RETRY iterations sit between the last review and the restart point
		// and must be skipped. Feedback found beyond a RETRY is recovered as
		// the same pointer reminder the live loop uses, mirroring what the
		// interrupted process would have carried.
		passedRetry := false
		for j := startIter; j >= 1; j-- {
			jDir := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", j))
			jMeta, jErr := am.ReadMeta(jDir)
			if jErr != nil {
				continue
			}
			if jMeta.ReviewStatus == agentStatusChangesRequested {
				lastChangesRequestedIter = j
				if passedRetry {
					reviewerFeedback = retryReviewFeedbackReminder(cfg.ArtifactDir, j)
				} else if data, err := os.ReadFile(filepath.Join(jDir, "review-feedback.md")); err == nil {
					reviewerFeedback = strings.TrimSpace(string(data))
				}
				break
			}
			if jMeta.AgentStatus == "RETRY" {
				passedRetry = true
				continue
			}
			// Stop searching once we hit an iteration that had a successful
			// review (APPROVED) or a non-FAILED agent run — earlier feedback
			// would already have been addressed by that iteration.
			if !isFailureBudgetAgentStatus(jMeta.AgentStatus) {
				break
			}
		}
	}

	for i := startIter + 1; i <= cfg.MaxIterations; i++ {
		iterStart := time.Now()
		iterCtx := phaseCtx.Child()
		cfg.Observer.IterationStarted(iterCtx, i)

		if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
			cfg.Observer.IterationEnded(iterCtx, i, observe.SessionUsage{}, time.Since(iterStart), "interrupted")
			return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
		}

		// Update iteration counter so the desktop app dashboard reflects progress
		if cfg.FeatureStore != nil {
			_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
				f.CurrentIteration = i
				return nil
			})
		}

		var (
			iterDir             string
			sessionID           string
			agentStatus         string
			duration            time.Duration
			cost                SessionCost
			madeProgress        bool
			exitCode            int
			sess                ports.SessionHandle
			waitResult          PhaseOutcomeWaitResult
			testingContractPath string
			contractFingerprint string
		)
		planContent := readPlanContent(cfg.PlanPath)
		if violations := verificationScopeViolations(planContent); len(violations) > 0 {
			cfg.Observer.IterationEnded(iterCtx, i, observe.SessionUsage{}, time.Since(iterStart), "plan_revision_required")
			return &LoopResult{
				FinalStatus:          "plan_revision_required",
				Iterations:           i,
				PlanRevisionFeedback: verificationScopePlanRevisionFeedback(violations),
			}, nil
		}

		if skipImplement {
			// Resume point: implement phase already finished in the prior run.
			// Skip session creation and jump to the review gate. Cost was
			// already accounted at that run's line ~430, so leave zero here.
			skipImplement = false
			iterDir = filepath.Join(cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", i))
			agentStatus = agentStatusSuccess
			var prepareErr error
			testingContractPath, contractFingerprint, prepareErr = prepareImplementationTestingContract(cfg, planContent)
			if prepareErr != nil {
				return nil, prepareErr
			}
		} else {
			sessionConfig, resolveErr := resolveImplementSessionConfig(cfg, llm.PhaseImplementation)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolving session configuration for iteration %d: %w", i, resolveErr)
			}

			// Read help answers from the latest feature state
			helpAnswers := ""
			if cfg.FeatureStore != nil {
				if latestFeature, err := cfg.FeatureStore.Load(cfg.Feature.ID); err == nil {
					helpAnswers = buildHelpAnswers(latestFeature.HelpQueue)
				}
			}

			// Create iteration directory and write prompt
			var createErr error
			iterDir, createErr = am.CreateIterationDir(i)
			if createErr != nil {
				return nil, fmt.Errorf("creating iteration dir: %w", createErr)
			}
			testingContractPath, contractFingerprint, createErr = prepareImplementationTestingContract(cfg, planContent)
			if createErr != nil {
				return nil, createErr
			}
			// Build prompt
			prompt := BuildImplementPrompt(
				cfg.PlanPath,
				cfg.ExitCriteria,
				reviewerFeedback,
				helpAnswers,
				i,
			)
			// Re-inject user-attached visual references (mockups, design
			// comps, desired-state screenshots) on every implement
			// iteration. They communicate intent that text-only phase
			// plans can't carry; without this they die at Design.
			if cfg.Feature != nil {
				if block := visualReferencesSection(cfg.Feature.Images, "implementing this iteration"); block != "" {
					prompt = block + prompt
				}
			}
			// Surface cross-phase deferrals owed by the current phase so
			// the agent cannot silently drop prior-phase commitments.
			// Closing or re-deferring is enforced by the Report Integrity
			// Gate (see internal/agent/report_integrity.go).
			if cfg.Feature != nil {
				if run := cfg.Feature.Run(); run != nil {
					currentPhase := cfg.Feature.CurrentRoadmapPhase
					if block := deferralsDueThisPhaseSection(run.Deferrals, currentPhase, deferralPromptKindImplement, cfg.RepoName); block != "" {
						prompt = block + prompt
					}
				}
			}
			// Remove a stale harness receipt before starting a new root turn.
			RemoveCompletionReceipt(iterDir)

			// Build the RoleSpec-backed system prompt with the iteration-specific
			// completion protocol and output roots.
			implProtocol := BuildImplementSystemPrompt(BuildImplementSystemPromptInput{
				IterationDir:  iterDir,
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				KBInfos:       cfg.KBInfos,
				AskingClause:  sessionConfig.AskingClause,
				Frontend:      cfg.Feature != nil && cfg.Feature.RoadmapPhaseFrontend(cfg.Feature.CurrentRoadmapPhase),
			})

			// Derive repo name for permission scoping. cfg.RepoName is empty for
			// single-repo features (preserves legacy "session.pid" PID naming), but the
			// permission cache needs the actual repo name to scope rules per-repo.
			permRepoName := cfg.RepoName
			if permRepoName == "" && len(cfg.Feature.Repos) > 0 {
				permRepoName = cfg.Feature.Repos[0].Name
			}

			// Build command + session opts via BuildSession. Only the active run
			// state is granted; the containing feature state directory also holds
			// sealed predecessor runs and must remain orchestrator-private. The opts
			// are kept so a crash-resume attempt can rebuild the same session with
			// ResumeSessionID set.
			runDir := cfg.RunDir
			if runDir == "" {
				runNumber := cfg.Feature.ActiveRun
				if runNumber <= 0 {
					runNumber = 1
				}
				runDir = filepath.Join(cfg.StateDir, "runs", feature.RunDirName(runNumber))
			}
			dirs := append([]string{runDir}, cfg.AdditionalDirs...)
			implBuildOpts := BuildSessionOpts{
				Model:                          sessionConfig.Model,
				Prompt:                         prompt,
				SystemPrompt:                   implProtocol,
				AdditionalDirs:                 dirs,
				AgentNames:                     []string{},
				PIDDir:                         cfg.StateDir,
				PermHandler:                    permHandlerFor(cfg.DangerouslySkipPermissions, cfg.PermissionCache, permRepoName),
				RepoName:                       cfg.RepoName,
				WorkDir:                        cfg.WorkDir,
				EffortLevel:                    cfg.EffortLevel,
				Phase:                          feature.PhaseImplement,
				SystemPromptHasUsefulResources: true,
				CompletionProtocol:             true,
			}
			if sessionConfig.EffectiveEffort != "" {
				implBuildOpts.EffortLevel = sessionConfig.EffectiveEffort
			}
			command, env, sessOpts, buildErr := cfg.BuildSession(implBuildOpts)
			if buildErr != nil {
				return nil, fmt.Errorf("building session for iteration %d: %w", i, buildErr)
			}
			// Copy the auto-review snapshot from sessOpts into implBuildOpts
			// so crash-resume reuses the original values rather than reading
			// the current (possibly edited) workspace config.
			if sessOpts != nil && sessOpts.AutoReview.Enabled != nil {
				implBuildOpts.AutoReview = sessOpts.AutoReview
			}

			sessOpts = enableTurnContinuation(sessOpts)
			if sessionConfig.EffectiveEffort != "" {
				sessOpts.EffectiveEffort = sessionConfig.EffectiveEffort
				sessOpts.EffortSource = sessionConfig.EffortSource
			}
			WriteDebugPrompts(iterDir, sessOpts.DebugSystemPrompt, prompt)
			// Merge iteration-specific fields into session opts
			sessOpts.Iteration = i
			sessOpts.RunNumber = cfg.Feature.ActiveRun
			sessOpts.PermCacheScope = permRepoName
			// Capture provider stderr so silent process deaths are diagnosable.
			sessOpts.StderrPath = filepath.Join(iterDir, "stderr.log")

			// Start session in interactive mode
			if cfg.SessionIDPrefix != "" {
				sessionID = fmt.Sprintf("%s-%s-impl", cfg.Feature.ID, cfg.SessionIDPrefix)
			} else if cfg.Feature.CurrentRoadmapPhase > 0 {
				sessionID = fmt.Sprintf("%s-phase-%02d-impl", cfg.Feature.ID, cfg.Feature.CurrentRoadmapPhase)
			} else {
				sessionID = cfg.Feature.ID + "-impl"
			}
			sessionID += fmt.Sprintf("-%02d", i)
			startSession := resolveSessionStartFunc(cfg.SessionStartFunc, sm)
			sess, err = startSession(
				sessionID,
				cfg.Feature.ID,
				feature.PhaseImplement,
				command,
				cfg.WorkDir,
				env,
				sessOpts,
			)
			if err != nil {
				if errors.Is(err, ports.ErrSessionShuttingDown) {
					cfg.Observer.IterationEnded(iterCtx, i, observe.SessionUsage{}, time.Since(iterStart), "interrupted")
					return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
				}
				cfg.Observer.IterationEnded(iterCtx, i, observe.SessionUsage{}, time.Since(iterStart), "error")
				return nil, fmt.Errorf("starting session for iteration %d: %w", i, err)
			}

			// Emit session.started
			implSessionCtx := iterCtx.Child()
			implProvider := ""
			if sessOpts != nil {
				implProvider = sessOpts.ProviderName
			}
			cfg.Observer.SessionStarted(implSessionCtx, "implement", sessionID, implProvider, sessionConfig.Model, cfg.RepoName, string(sessionConfig.EffectiveEffort), string(sessionConfig.EffortSource))
			implTracker := &ContextReadTracker{
				KBBaseDir:     filepath.Join(filepath.Dir(cfg.StateDir), "knowledge-base"),
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				Observer:      cfg.Observer,
			}
			implTracker.Install(sess, implSessionCtx, "implement", sessionID)

			// Set up log file
			logPath := filepath.Join(iterDir, "response.txt")
			logFile, err := os.Create(logPath)
			if err != nil {
				return nil, fmt.Errorf("creating log file: %w", err)
			}
			sess.SetLogFile(logFile)

			// implWaitOptions builds the wait options for an implement session.
			// Shared between the initial session and a crash-resume attempt so
			// both waits apply the same readiness and handoff behavior.
			implWaitOptions := func(sess ports.SessionHandle, sessionCtx observe.SpanContext, sessionID string) PhaseOutcomeWaitOptions {
				return PhaseOutcomeWaitOptions{
					CommitOutcome: func(intent llm.CompletionIntent) ([]ProtocolViolation, error) {
						_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{
							Phase:       feature.PhaseImplement,
							Role:        RoleImplementer,
							ArtifactDir: iterDir,
							SessionID:   sessionID,
							Intent:      intent,
						})
						if err == nil && len(violations) == 0 {
							sess.SetHasUnansweredQuestion(false)
						}
						return violations, err
					},
					MissingArtifacts:     []string{"progress.md"},
					RetryOutcomeAllowed:  true,
					EnableContextHandoff: true,
					OnContextHandoff: func(snap contextSnapshot) {
						cfg.Observer.ContextHandoffTriggered(
							sessionCtx,
							"implement",
							sessionID,
							cfg.RepoName,
							sess.ProviderName(),
							i,
							snap.Pct,
							snap.ThresholdPct,
							snap.TotalTokens,
							snap.WindowTokens,
							snap.BaselineTokens,
						)
					},
				}
			}

			// Wait for a semantic root outcome or session exit. If the provider
			// reports a clean end without an outcome, the iteration records a
			// protocol violation instead of treating transport completion as
			// phase completion.
			waitResult = WaitForPhaseOutcome(sess, implWaitOptions(sess, implSessionCtx, sessionID))
			agentStatus = waitResult.Status

			// App shutdown should not serialize an in-flight iteration as FAILED.
			// Leaving the iteration incomplete (no meta.yaml) allows restart to replay
			// the same iteration number instead of incorrectly advancing to the next.
			//
			// Grace period: a bare `sm.IsShuttingDown()` check races against how
			// shutdown signals reach us. When the user presses Ctrl+C, SIGINT
			// propagates through the process group, killing the agent CLI
			// *before* the desktop app unwinds and main.go calls sm.Shutdown(). The
			// session dies, WaitForPhaseOutcome returns FAILED, and this check would
			// run with IsShuttingDown still false — committing a FAILED meta
			// that makes the next run advance to iteration N+1 instead of
			// replaying N. Pausing briefly lets the normal shutdown path land.
			if agentStatus == agentStatusFailed && sm != nil && waitForShutdownIntent(sm, shutdownDetectionGrace) {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}

			if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i}, nil
			}

			// Crash resume: the provider process died mid-turn without
			// completing the iteration (a completed one is reclassified
			// SUCCESS by the waiter). For providers that can resume their
			// native session, give the same conversation one fresh process to
			// finish or report state before charging the iteration as FAILED.
			if agentStatus == agentStatusFailed && sessOpts != nil && sessOpts.SupportsSessionResume &&
				!HasCommittedPhaseOutcome(iterDir, feature.PhaseImplement, RoleImplementer) {
				if resumeID := providerSessionID(sess); resumeID != "" {
					// Account the dead session before replacing it.
					deadCost := ExtractSessionCost(sess)
					cfg.Observer.SessionEnded(implSessionCtx, "implement", sessionID, cfg.RepoName, toSessionUsage(deadCost), time.Since(iterStart), sessionErrFromLogicalAgentStatus(agentStatus, sess))
					_ = accumulateSessionCostToFeature(cfg.FeatureStore, cfg.Feature.ID, "implement", deadCost, SessionCostMetadata{
						SessionID:     sessionID,
						ObserverPhase: "implement",
						RepoName:      cfg.RepoName,
					})
					appendIterationLog(aggregateLogPath, i, sess.MessageLog().Text())

					resumeSessionID := sessionID + "-resume"
					resumeSess, resumeOpts, resumeErr := startCrashResumeSession(cfg, sm, implBuildOpts, resumeID, resumeSessionID, i, iterDir, permRepoName)
					if resumeErr == nil {
						sess, sessOpts = resumeSess, resumeOpts
						sessionID = resumeSessionID
						implSessionCtx = iterCtx.Child()
						cfg.Observer.SessionStarted(implSessionCtx, "implement", sessionID, sessOpts.ProviderName, sessionConfig.Model, cfg.RepoName, string(sessionConfig.EffectiveEffort), string(sessionConfig.EffortSource))
						implTracker.Install(sess, implSessionCtx, "implement", sessionID)

						waitResult = WaitForPhaseOutcome(sess, implWaitOptions(sess, implSessionCtx, sessionID))
						agentStatus = waitResult.Status
						if agentStatus == agentStatusFailed && sm != nil && waitForShutdownIntent(sm, shutdownDetectionGrace) {
							return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
						}
						if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
							return &LoopResult{FinalStatus: "interrupted", Iterations: i}, nil
						}
					}
				}
			}

			duration = time.Since(iterStart)

			// Read cost from session's ResultMessage
			cost = ExtractSessionCost(sess)
			cfg.Observer.SessionEnded(implSessionCtx, "implement", sessionID, cfg.RepoName, toSessionUsage(cost), time.Since(iterStart), sessionErrFromLogicalAgentStatus(agentStatus, sess))
			emitLargeCodexCommandOutputEvents(cfg.Observer, implSessionCtx, "implement", sessionID, cfg.RepoName, sess.ProviderName(), i, logPath)

			// Read output from message log
			output := sess.MessageLog().Text()

			// Append iteration output to aggregate log so the desktop app log viewer works
			appendIterationLog(aggregateLogPath, i, output)

			// Build iteration metadata from the logical agent result, not
			// the process status after Stop(). Multi-turn providers such as
			// Codex can report SUCCESS and then exit non-zero during
			// intentional cleanup.
			exitCode = exitCodeFromAgentStatus(agentStatus)
		}
		meta := IterationMeta{
			Iteration:    i,
			StartedAt:    iterStart,
			Duration:     duration,
			ExitCode:     exitCode,
			AgentStatus:  agentStatus,
			MadeProgress: madeProgress,
			CostUSD:      cost.TotalCostUSD,
			Context:      iterationContextMeta(sess, waitResult.Handoff),
		}

		// Accumulate iteration cost into the latest active timing key rather
		// than the initial config snapshot, which may be stale after phase
		// transitions such as plan -> implement.
		_ = accumulateSessionCostToFeature(cfg.FeatureStore, cfg.Feature.ID, "implement", cost, SessionCostMetadata{
			SessionID:     sessionID,
			ObserverPhase: "implement",
			RepoName:      cfg.RepoName,
		})

		// Handle based on agent status
		if waitResult.Err != nil {
			return nil, fmt.Errorf("committing implementer outcome: %w", waitResult.Err)
		}
		if agentStatus == agentStatusProtocolViolation {
			violations := waitResult.ProtocolViolations
			if len(violations) == 0 {
				violations = completionIntentViolations(rootCompletionIntent(sess), []string{"progress.md"})
			}
			lastErr := formatProtocolViolationError(RoleImplementer, iterDir, violations)
			if done := recordProtocolViolationIteration(am, summaryPath, iterDir, &meta, i, cfg, iterCtx, cost, iterStart, violations, lastErr, &consecutiveFailures, &reviewerFeedback); done != nil {
				return done, nil
			}
			continue
		}

		if agentStatus == agentStatusSuccess {
			var harnessVerification *VerificationExecutionOutcome
			var verificationContract *TestingContract
			preliminaryProgress, _ := ParseProgressMd(progressPath)
			if preliminaryProgress != nil && preliminaryProgress.State == StateSuccess && strings.TrimSpace(testingContractPath) != "" {
				if contractFingerprint != "" {
					currentFingerprint, fingerprintErr := Fingerprint(testingContractPath)
					if fingerprintErr != nil || currentFingerprint != contractFingerprint {
						reason := "testing-contract.yaml was modified by the implementer; the contract is harness-owned"
						if fingerprintErr != nil {
							reason = fmt.Sprintf("testing-contract.yaml could not be verified after implementation: %v", fingerprintErr)
						}
						violations := []ProtocolViolation{{Artifact: "testing-contract.yaml", Reason: reason}}
						lastErr := formatProtocolViolationError(RoleImplementer, iterDir, violations)
						if done := recordProtocolViolationIteration(am, summaryPath, iterDir, &meta, i, cfg, iterCtx, cost, iterStart, violations, lastErr, &consecutiveFailures, &reviewerFeedback); done != nil {
							return done, nil
						}
						continue
					}
				}
				contract, readErr := ReadTestingContract(testingContractPath)
				if readErr != nil {
					return nil, fmt.Errorf("reading testing contract for harness verification: %w", readErr)
				}
				verificationContract = contract
				reportPath := filepath.Join(iterDir, "verification-report.yaml")
				report := BuildContractVerificationReportStub(contract, testingContractPath)
				verificationRepos := cfg.Feature.Repos
				if strings.TrimSpace(cfg.RepoName) != "" {
					verificationRepos = nil
					for _, repo := range cfg.Feature.Repos {
						if repo.Name == cfg.RepoName {
							verificationRepos = append(verificationRepos, repo)
						}
					}
				}
				if cached, cacheErr := ReadVerificationReport(reportPath); cacheErr == nil && cached != nil && cached.ContractRevision == contract.Revision {
					// Stop/restart resume: the harness already executed this
					// iteration's contract and persisted the report. Reuse
					// it so resuming mid-review never re-runs the commands.
					harnessVerification = ReconstructVerificationOutcome(cached)
				} else {
					verifyCtx, cancelVerify := verificationContext(cfg.FeatureStore, cfg.Feature.ID)
					beginVerificationStatuses(cfg.FeatureStore, cfg.Feature.ID, contract, cfg.OnVerificationProgress)
					verifyCtx = WithVerificationProgress(verifyCtx, func(name, state string) {
						updateVerificationStatus(cfg.FeatureStore, cfg.Feature.ID, name, state, cfg.OnVerificationProgress)
					})
					harnessVerification, readErr = ExecuteTestingContract(verifyCtx, cfg.CommandRunner, contract, &report, testingContractPath, iterDir, cfg.WorkDir, verificationRepos)
					cancelVerify()
					clearVerificationStatuses(cfg.FeatureStore, cfg.Feature.ID, cfg.OnVerificationProgress)
					if readErr != nil {
						if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
							cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "interrupted")
							return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
						}
						return nil, fmt.Errorf("executing testing contract: %w", readErr)
					}
					if readErr = WriteVerificationReport(reportPath, *harnessVerification.Report); readErr != nil {
						return nil, fmt.Errorf("writing harness verification report: %w", readErr)
					}
				}
			}
			outcome, violations, validateErr := Validate(feature.PhaseImplement, RoleImplementer, iterDir)
			if validateErr != nil {
				return nil, fmt.Errorf("validating implementer contract: %w", validateErr)
			}
			if !outcome.OK {
				lastErr := formatProtocolViolationError(RoleImplementer, iterDir, violations)
				if done := recordProtocolViolationIteration(am, summaryPath, iterDir, &meta, i, cfg, iterCtx, cost, iterStart, violations, lastErr, &consecutiveFailures, &reviewerFeedback); done != nil {
					return done, nil
				}
				continue
			}
			parsed := outcome.Progress
			if harnessVerification != nil && len(harnessVerification.ContractErrors) > 0 {
				consecutiveFailures = 0
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "plan_revision_required")
				return &LoopResult{
					FinalStatus:          "plan_revision_required",
					Iterations:           i,
					PlanRevisionFeedback: VerificationContractPlanRevisionFeedback(harnessVerification.ContractErrors),
				}, nil
			}
			if harnessVerification != nil {
				gate := ValidateVerificationReportWithContext(harnessVerification.Report, nil, true, VerificationReportValidationContext{
					IterationDir: iterDir,
					Contract:     verificationContract,
				})
				if gate.Rejected {
					gateViolations := reportGateViolations(gate)
					lastErr := formatProtocolViolationError(RoleImplementer, iterDir, gateViolations)
					if done := recordProtocolViolationIteration(am, summaryPath, iterDir, &meta, i, cfg, iterCtx, cost, iterStart, gateViolations, lastErr, &consecutiveFailures, &reviewerFeedback); done != nil {
						return done, nil
					}
					continue
				}
			}

			if harnessVerification != nil && len(harnessVerification.BlockedItems) > 0 {
				gatePath := NeedUserInputPath(iterDir)
				rec := SynthesizeVerificationNeedUserInputGateWithContext(
					testingContractPath,
					verificationContract,
					harnessVerification.Report,
					harnessVerification.BlockedItems,
					i,
				)
				if err := WriteNeedUserInputRecord(gatePath, rec); err != nil {
					return nil, fmt.Errorf("persisting verification capability gate: %w", err)
				}
				consecutiveFailures = 0
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "need_user_input")
				return &LoopResult{FinalStatus: "need_user_input", Iterations: i, LastError: rec.Summary, NeedUserInputPath: gatePath}, nil
			}

			// RETRY: skip the review gate entirely; the agent is telling
			// us the iteration is intentionally partial. The next loop
			// iteration starts fresh against the just-emitted progress.md
			// (no reviewer feedback — RETRY is not a rejection).
			if parsed.State == StateRetry {
				madeProgress = pt.ObserveRetryOutcome(retryProgressFingerprints(progressPath, cfg))
				meta.ReviewStatus = "skipped_retry"
				meta.AgentStatus = "RETRY"
				meta.MadeProgress = madeProgress
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				consecutiveFailures = 0
				// Don't re-inject the full feedback (the RETRY handoff may
				// have partially addressed it), but don't drop it either —
				// the progress.md handoff is not guaranteed to carry every
				// finding. Point the next iteration at the on-disk feedback
				// so it re-verifies what remains.
				reviewerFeedback = retryReviewFeedbackReminder(cfg.ArtifactDir, lastChangesRequestedIter)
				if pt.NoProgressCount() >= cfg.MaxConsecNoProgress {
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "retry")
					return &LoopResult{
						FinalStatus: "safety_rail",
						Iterations:  i,
						LastError:   fmt.Sprintf("no progress for %d consecutive iterations", pt.NoProgressCount()),
					}, nil
				}
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "retry")
				continue
			}

			// Fall through: SUCCESS — run the review gate.
			if cfg.SkipIterationReview {
				// Medium/Large: skip per-iteration review, rely on Final Review.
				// Deterministically classified regressions still route back to
				// the implementer — with no per-iteration reviewer there is
				// nobody else to act on a red harness report.
				if harnessVerification != nil && len(harnessVerification.RegressionItems) > 0 {
					meta.MadeProgress = observeVerifiedImplementationOutcome(pt, CountOpenVerificationOutcomeBlockers(harnessVerification), cfg)
					meta.ReviewStatus = ReviewChangesRequested.String()
					_ = am.WriteMeta(iterDir, meta)
					_ = am.WriteSummary(summaryPath, meta)
					consecutiveFailures = 0
					reviewerFeedback = fmt.Sprintf(
						"Harness verification detected regressions: %s. These commands pass at the contract base commit but fail with your changes. See verification-report.yaml in the iteration directory for evidence; fix the regressions before declaring SUCCESS.",
						strings.Join(harnessVerification.RegressionItems, ", "))
					if pt.NoProgressCount() >= cfg.MaxConsecNoProgress {
						cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "harness_regression")
						return &LoopResult{
							FinalStatus: "safety_rail",
							Iterations:  i,
							LastError:   fmt.Sprintf("no progress for %d consecutive iterations", pt.NoProgressCount()),
						}, nil
					}
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "harness_regression")
					continue
				}
				meta.MadeProgress = observeVerifiedImplementationOutcome(pt, 0, cfg)
				meta.ReviewStatus = reviewStatusSkipped
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				consecutiveFailures = 0
				return &LoopResult{
					FinalStatus: finalStatusReviewPassed,
					Iterations:  i,
				}, nil
			}

			// Moonshot: run per-iteration review gate.
			//
			// Before starting, re-check interruption state. The implement
			// session may have finished right as a shutdown was initiated;
			// starting a review session at that point wastes tokens and
			// races the shutdown, with the review typically coming back as
			// FAILED — which used to get serialized into meta.yaml and
			// advance the restart cursor to N+1. Re-check here and exit
			// cleanly instead. The harness receipt from the implement phase
			// stays on disk, so restart will route through the skipImplement
			// branch above and resume the review for
			// iteration N — exactly the expected behavior.
			if sm != nil && sm.IsShuttingDown() {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}
			if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}

			setReviewingGate(cfg, true)
			if cfg.OnReviewingChange != nil {
				cfg.OnReviewingChange(true)
			}

			// Run review gate with observability context
			reviewCtx := iterCtx.Child()
			cfg.Observer.ReviewStarted(reviewCtx, i)
			reviewStart := time.Now()
			reviewSessionConfig, resolveErr := resolveImplementSessionConfig(cfg, llm.PhaseReview)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolving review session configuration for iteration %d: %w", i, resolveErr)
			}
			reviewCfg := cfg
			reviewCfg.ReviewModel = reviewSessionConfig.Model
			reviewCfg.ReviewEffectiveEffort = reviewSessionConfig.EffectiveEffort
			reviewCfg.ReviewEffortSource = reviewSessionConfig.EffortSource
			reviewCfg.AskingClause = reviewSessionConfig.AskingClause
			reviewStatus, feedback, reviewErr := runReviewGate(reviewCfg, sm, i, iterDir, parsed, reviewCtx)
			cfg.Observer.ReviewCompleted(reviewCtx, i, reviewStatus.String(), time.Since(reviewStart))

			// Clear the reviewing flag
			setReviewingGate(cfg, false)
			if cfg.OnReviewingChange != nil {
				cfg.OnReviewingChange(false)
			}

			// App shutdown during review must NOT serialize iteration-N's
			// meta.yaml as FAILED (same rationale as the implement-phase
			// shutdown check above). Without this, restart would see
			// iteration-N as "complete with review failed" and advance to
			// N+1, even though the review never ran to completion. Leaving
			// meta.yaml unwritten lets LatestIteration stop at N-1 and the
			// skipImplement check above find the harness receipt, routing
			// restart back to iteration N for review only.
			// Uses the same grace-period detection as the implement path.
			if (reviewErr != nil || reviewStatus == ReviewFailed) && sm != nil && waitForShutdownIntent(sm, shutdownDetectionGrace) {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}
			if isFeatureInterrupted(cfg.FeatureStore, cfg.Feature.ID) {
				return &LoopResult{FinalStatus: "interrupted", Iterations: i - 1}, nil
			}

			if reviewErr != nil {
				if isProtocolViolationError(reviewErr) {
					consecutiveFailures++
					if strings.TrimSpace(feedback) == "" {
						feedback = fmt.Sprintf("Review helper protocol violation: %v", reviewErr)
					}
					reviewerFeedback = feedback
					lastChangesRequestedIter = i
					meta.AgentStatus = agentStatusProtocolViolation
					meta.ReviewStatus = ReviewChangesRequested.String()
					_ = am.WriteMeta(iterDir, meta)
					_ = am.WriteSummary(summaryPath, meta)
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), BoundedHelperStatusProtocolViolation)
					if consecutiveFailures >= cfg.MaxConsecFails {
						return &LoopResult{
							FinalStatus: BoundedHelperStatusProtocolViolation,
							Iterations:  i,
							LastError:   reviewErr.Error(),
						}, nil
					}
					continue
				}
				// The review never produced a verdict (helper/API failure) —
				// that is not reviewer feedback, and dispatching it to the
				// implementer would burn an iteration on unactionable text.
				// Leave meta.yaml unwritten: the completion receipt stays on
				// disk, so on resume LatestIteration stops at i-1 and the
				// skipImplement branch re-runs the review for this iteration.
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "review_error")
				return &LoopResult{
					FinalStatus: "review_error",
					Iterations:  i - 1,
					LastError:   fmt.Sprintf("review did not complete: %v; resume to re-run the review for iteration %d", reviewErr, i),
				}, nil
			}

			switch reviewStatus {
			case ReviewApproved:
				meta.MadeProgress = observeVerifiedImplementationOutcome(pt, 0, cfg)
				meta.ReviewStatus = reviewStatus.String()
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				consecutiveFailures = 0
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), finalStatusReviewPassed)
				return &LoopResult{
					FinalStatus: finalStatusReviewPassed,
					Iterations:  i,
				}, nil
			case ReviewChangesRequested:
				meta.MadeProgress = observeVerifiedImplementationOutcome(pt, CountBlockingReviewFindings(feedback)+CountOpenVerificationOutcomeBlockers(harnessVerification), cfg)
				meta.ReviewStatus = reviewStatus.String()
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				missingReqs := MissingEvidenceRequirements(feedback)
				insufficientReqs := InsufficientEvidenceRequirements(feedback)
				if len(missingReqs) > 0 || len(insufficientReqs) > 0 {
					consecutiveFailures = 0
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "plan_revision_required")
					return &LoopResult{
						FinalStatus:          "plan_revision_required",
						Iterations:           i,
						PlanRevisionFeedback: EvidencePlanRevisionFeedback(missingReqs, insufficientReqs),
					}, nil
				}
				consecutiveFailures = 0
				reviewerFeedback = feedback
				lastChangesRequestedIter = i
				// Check no-progress safety rail
				if pt.NoProgressCount() >= cfg.MaxConsecNoProgress {
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "changes_requested")
					return &LoopResult{
						FinalStatus: "safety_rail",
						Iterations:  i,
						LastError:   fmt.Sprintf("no progress for %d consecutive iterations", pt.NoProgressCount()),
					}, nil
				}
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "changes_requested")
				continue
			default:
				meta.MadeProgress = pt.ObserveUnverifiedOutcome()
				meta.ReviewStatus = reviewStatus.String()
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				consecutiveFailures = 0
				reviewerFeedback = "Review produced no clear result. Please verify your changes."
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "review_unclear")
				continue
			}
		} else {
			// Agent exited with error (API_ERROR or failure).
			// Preserve reviewerFeedback so the next iteration still sees
			// unaddressed review findings from the last successful review.
			consecutiveFailures++

			meta.AgentStatus = "FAILED"
			_ = am.WriteMeta(iterDir, meta)
			_ = am.WriteSummary(summaryPath, meta)

			// Check consecutive failure safety rail
			if consecutiveFailures >= cfg.MaxConsecFails {
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), agentStatus)
				return &LoopResult{
					FinalStatus: "safety_rail",
					Iterations:  i,
					LastError:   fmt.Sprintf("%d consecutive agent failures", consecutiveFailures),
				}, nil
			}
			cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), agentStatus)
		}
	}

	return &LoopResult{
		FinalStatus: "max_iterations",
		Iterations:  cfg.MaxIterations,
		LastError:   "reached maximum iteration count",
	}, nil
}

// crashResumeMessageFragment is the stable fragment tests and log scans match
// on; keep crashResumeMessage in sync with it.
const crashResumeMessageFragment = "process terminated unexpectedly mid-turn"

// crashResumeMessage is the prompt for a crash-resume session. The resumed
// conversation already carries the full iteration context; this only explains
// the process boundary and re-anchors the completion protocol.
const crashResumeMessage = "Your previous process terminated unexpectedly mid-turn; this session resumes that conversation. " +
	"Reassess the repository and your artifacts: if the iteration's work is already complete, write any missing " +
	"required artifacts, run the artifact preflight, and emit the structured root outcome; otherwise update progress " +
	"and continue from where you left off."

// providerSessionID returns the provider-native session identifier for a
// session handle, "" when the handle does not expose one. SessionID is not
// part of ports.SessionView, so it is probed as an optional interface.
func providerSessionID(sess ports.SessionView) string {
	if p, ok := sess.(interface{ SessionID() string }); ok {
		return p.SessionID()
	}
	return ""
}

// startCrashResumeSession starts a fresh provider process that resumes the
// provider-native session resumeID after the previous process died mid-turn.
// The iteration's response.txt is opened in append mode so the dead session's
// streamed output is preserved.
func startCrashResumeSession(cfg ImplementConfig, sm ports.SessionManager, buildOpts BuildSessionOpts, resumeID, sessionID string, iteration int, iterDir, permScope string) (ports.SessionHandle, *ports.SessionOpts, error) {
	buildOpts.ResumeSessionID = resumeID
	buildOpts.Prompt = crashResumeMessage
	command, env, sessOpts, err := cfg.BuildSession(buildOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("building crash-resume session: %w", err)
	}
	sessOpts = enableTurnContinuation(sessOpts)
	if cfg.EffectiveEffort != "" {
		sessOpts.EffectiveEffort = cfg.EffectiveEffort
		sessOpts.EffortSource = cfg.EffortSource
	}
	sessOpts.Iteration = iteration
	sessOpts.RunNumber = cfg.Feature.ActiveRun
	sessOpts.PermCacheScope = permScope
	sessOpts.StderrPath = filepath.Join(iterDir, "stderr-resume.log")

	startSession := resolveSessionStartFunc(cfg.SessionStartFunc, sm)
	sess, err := startSession(sessionID, cfg.Feature.ID, feature.PhaseImplement, command, cfg.WorkDir, env, sessOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("starting crash-resume session: %w", err)
	}
	if logFile, logErr := os.OpenFile(filepath.Join(iterDir, "response.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); logErr == nil {
		sess.SetLogFile(logFile)
	}
	return sess, sessOpts, nil
}

func formatProtocolViolationError(role Role, iterDir string, violations []ProtocolViolation) string {
	reason := JoinProtocolViolations(violations)
	if strings.TrimSpace(reason) == "" {
		reason = "root outcome or required artifacts were invalid"
	}
	return fmt.Sprintf("protocol violation: %s @ %s: %s", role, iterDir, reason)
}

func formatContractViolationFeedback(role Role, violations []ProtocolViolation) string {
	var b strings.Builder
	for _, v := range violations {
		fmt.Fprintf(&b, "- **Critical**: %s: %s\n", v.Artifact, v.Reason)
	}
	return FormatStructuredReviewFeedback(
		fmt.Sprintf("Implementation Review — %s contract violation", role),
		strings.TrimSpace(b.String()),
		"",
		ReviewChangesRequested,
	)
}

func recordProtocolViolationIteration(
	am *ArtifactManager,
	summaryPath string,
	iterDir string,
	meta *IterationMeta,
	iteration int,
	cfg ImplementConfig,
	iterCtx observe.SpanContext,
	cost SessionCost,
	iterStart time.Time,
	violations []ProtocolViolation,
	lastErr string,
	consecutiveFailures *int,
	reviewerFeedback *string,
) *LoopResult {
	*consecutiveFailures = *consecutiveFailures + 1
	feedback := formatContractViolationFeedback(RoleImplementer, violations)
	_ = os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(feedback), 0o644)
	meta.AgentStatus = "PROTOCOL_VIOLATION"
	meta.ReviewStatus = agentStatusChangesRequested
	_ = am.WriteMeta(iterDir, *meta)
	_ = am.WriteSummary(summaryPath, *meta)
	*reviewerFeedback = feedback
	cfg.Observer.IterationEnded(iterCtx, iteration, toSessionUsage(cost), time.Since(iterStart), BoundedHelperStatusProtocolViolation)
	if *consecutiveFailures >= cfg.MaxConsecFails {
		return &LoopResult{FinalStatus: BoundedHelperStatusProtocolViolation, Iterations: iteration, LastError: lastErr}
	}
	return nil
}

// retryReviewFeedbackReminder builds the reviewer-feedback text carried into
// iterations that follow a RETRY. Re-injecting the full CHANGES_REQUESTED
// feedback every iteration is wasteful — some findings may already be
// addressed — but dropping it entirely trusts the RETRY handoff in
// progress.md to have captured every finding, which is not guaranteed.
// Instead, point the implementer at the on-disk feedback so it re-reads and
// re-verifies what remains in its own context. Returns "" when no reviewed
// iteration precedes the RETRY or its feedback file is gone.
func retryReviewFeedbackReminder(artifactDir string, reviewedIter int) string {
	if reviewedIter <= 0 {
		return ""
	}
	path := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", reviewedIter), "review-feedback.md")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return fmt.Sprintf("The iteration %d review requested changes, and the intervening RETRY iteration(s) may not have addressed every finding. Re-read the full feedback at %s and verify each finding against the current tree; progress.md alone is not authoritative for what remains. Do not declare SUCCESS while any finding is unaddressed.", reviewedIter, path)
}

// retryProgressFingerprints gathers the stall-evidence signals for a RETRY
// iteration: the progress.md handoff-narrative fingerprint and a hash of
// every feature worktree's uncommitted state.
func retryProgressFingerprints(progressPath string, cfg ImplementConfig) (string, string) {
	narrativeFP, _ := ProgressFingerprint(progressPath)
	return narrativeFP, WorktreeStateFingerprint(context.Background(), cfg.CommandRunner, implementWorktreePaths(cfg))
}

func observeVerifiedImplementationOutcome(pt *ProgressTracker, blockers int, cfg ImplementConfig) bool {
	worktreeFP := WorktreeStateFingerprint(context.Background(), cfg.CommandRunner, implementWorktreePaths(cfg))
	return pt.ObserveVerifiedOutcomeWithWorktree(blockers, worktreeFP)
}

// implementWorktreePaths returns the repo paths whose git state evidences
// implementation progress: each feature repo's worktree (or checkout path),
// falling back to the session work dir for repo-less configurations.
func implementWorktreePaths(cfg ImplementConfig) []string {
	if cfg.Feature != nil && len(cfg.Feature.Repos) > 0 {
		paths := make([]string, 0, len(cfg.Feature.Repos))
		for _, r := range cfg.Feature.Repos {
			switch {
			case r.WorktreePath != "":
				paths = append(paths, r.WorktreePath)
			case r.Path != "":
				paths = append(paths, r.Path)
			}
		}
		return paths
	}
	if cfg.WorkDir != "" {
		return []string{cfg.WorkDir}
	}
	return nil
}

func isFailureBudgetAgentStatus(status string) bool {
	return status == agentStatusFailed || status == agentStatusProtocolViolation
}

func exitCodeFromAgentStatus(status string) int {
	if status == agentStatusSuccess {
		return 0
	}
	return 1
}

func sessionErrFromLogicalAgentStatus(status string, sess ports.SessionView) error {
	switch status {
	case agentStatusSuccess:
		return nil
	case agentStatusProtocolViolation:
		return sessionErrFromAgentStatus(status)
	}
	if err := sessionErrFromStatus(sess); err != nil {
		return err
	}
	return sessionErrFromAgentStatus(status)
}

// runReviewGate spawns a review agent to evaluate the implementation.
// The reviewer inspects the repository and iteration artifacts directly
// using its own tools rather than receiving the full diff inline.
//
// Before invoking the LLM reviewer, a deterministic Report Integrity Gate
// validates the harness-generated verification report against its contract.
// The deferral ledger gate runs against the parsed
// progress.md (passed in by the caller after harness routing), since
// deferrals now live in `## Deferrals` rather than the YAML report.
// When either gate rejects, the LLM is skipped entirely and a structured
// CHANGES_REQUESTED is returned.
func runReviewGate(cfg ImplementConfig, sm ports.SessionManager, iteration int, iterDir string, parsed *ParsedProgress, reviewCtx observe.SpanContext) (ReviewStatus, string, error) {
	progressPath := filepath.Join(cfg.ArtifactDir, "progress.md")
	verificationReportPath := filepath.Join(iterDir, "verification-report.yaml")
	contractPath := ""
	var contract *TestingContract
	if path, ok := resolveImplementationContractPath(filepath.Dir(cfg.StateDir), cfg.Feature, cfg.RepoName); ok {
		contractPath = path
		if loaded, err := ReadTestingContract(contractPath); err == nil {
			contract = loaded
		}
	}

	// Pull the deferral ledger context up front; both the gate and the
	// post-gate ingestion need it.
	var ledger []feature.Deferral
	currentPhase := 0
	if cfg.Feature != nil {
		currentPhase = cfg.Feature.CurrentRoadmapPhase
		if run := cfg.Feature.Run(); run != nil {
			ledger = run.Deferrals
		}
	}
	var parsedDeferrals []feature.IncomingDeferral
	var parsedClosedDeferrals []string
	if parsed != nil {
		parsedDeferrals = parsed.Deferrals
		parsedClosedDeferrals = parsed.ClosedDeferrals
	}

	// Report Integrity Gate: deterministic pre-review. The harness generates
	// verification-report.yaml, so a missing or malformed report rejects the
	// iteration before any review axis runs. The gate runs two passes —
	// schema against verification-report.yaml, and deferral-ledger against
	// the parsed progress.md — merged into a single Rejected verdict so the
	// agent sees all failure modes at once.
	var gateResult ReportGateResult
	if report, err := ReadVerificationReport(verificationReportPath); err == nil {
		boundContract := contract
		var schemaResult ReportGateResult
		if loaded, err := readBoundTestingContract(report); err != nil {
			schemaResult.Findings = append(schemaResult.Findings, ReportGateFinding{
				Category: GateCategorySchema,
				Kind:     KindMissingRequired,
				Detail:   fmt.Sprintf("testing contract referenced by verification-report.yaml is unreadable: %v", err),
			})
			schemaResult.Rejected = true
		} else if loaded != nil {
			boundContract = loaded
		}
		schemaResult = MergeGateResults(schemaResult, ValidateVerificationReportWithContext(report, nil, true, VerificationReportValidationContext{
			IterationDir: iterDir,
			Contract:     boundContract,
		}))
		deferralResult := ValidateDeferralLedger(parsedDeferrals, parsedClosedDeferrals, ledger, currentPhase, cfg.RepoName)
		gateResult = MergeGateResults(schemaResult, deferralResult)
		if gateResult.Rejected {
			feedback := FormatGateFeedback(gateResult)
			_ = os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(feedback), 0o644)
			cfg.Observer.ReviewCompleted(reviewCtx, iteration, "CHANGES_REQUESTED_GATE", 0)
			return ReviewChangesRequested, feedback, nil
		}
	} else {
		// A SUCCESS handoff must have a harness-generated report before review.
		gateResult.Findings = append(gateResult.Findings, ReportGateFinding{
			Category: GateCategorySchema,
			Kind:     KindMissingRequired,
			Detail:   "harness-generated verification-report.yaml is missing",
		})
		gateResult.Rejected = true
		deferralResult := ValidateDeferralLedger(parsedDeferrals, parsedClosedDeferrals, ledger, currentPhase, cfg.RepoName)
		gateResult = MergeGateResults(gateResult, deferralResult)
		if gateResult.Rejected {
			feedback := FormatGateFeedback(gateResult)
			_ = os.WriteFile(filepath.Join(iterDir, "review-feedback.md"), []byte(feedback), 0o644)
			cfg.Observer.ReviewCompleted(reviewCtx, iteration, "CHANGES_REQUESTED_GATE", 0)
			return ReviewChangesRequested, feedback, nil
		}
	}

	// Ingest the deferrals ledger from the parsed progress.md. New
	// entries are merged idempotently (stable IDs); closures transition
	// matching ledger entries to Status==closed. The deferral gate above
	// has already rejected reason-less entries and unclosed-due-this-phase
	// commitments, so any deferrals reaching this point are intentional.
	if cfg.FeatureStore != nil && (len(parsedDeferrals) > 0 || len(parsedClosedDeferrals) > 0) {
		now := time.Now()
		_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
			run := f.Run()
			if run == nil {
				return nil
			}
			run.Deferrals = feature.MergeDeferrals(run.Deferrals, parsedDeferrals, currentPhase, now)
			feature.CloseDeferrals(run.Deferrals, parsedClosedDeferrals, currentPhase, "implement", now)
			return nil
		})
	}

	reviewDir := filepath.Join(iterDir, "review")
	if err := os.MkdirAll(reviewDir, 0o755); err != nil {
		return ReviewFailed, "", fmt.Errorf("creating review helper directory: %w", err)
	}
	RemoveCompletionReceipt(reviewDir)

	parentFeedbackPath := filepath.Join(iterDir, "review-feedback.md")
	reviewStatus, feedback, err := runImplementationReviewAxes(cfg, sm, iteration, iterDir, reviewDir, reviewCtx, implementationReviewInput{
		ProgressPath:           progressPath,
		ContractPath:           contractPath,
		VerificationReportPath: verificationReportPath,
		KnownCaveatsGateResult: gateResult,
	})
	if err != nil {
		if strings.TrimSpace(feedback) != "" {
			_ = os.WriteFile(parentFeedbackPath, []byte(feedback), 0o644)
		}
		return reviewStatus, feedback, fmt.Errorf("running review gate: %w", err)
	}

	if strings.TrimSpace(feedback) != "" {
		_ = os.WriteFile(parentFeedbackPath, []byte(feedback), 0o644)
	}
	return reviewStatus, feedback, nil
}

func resolveImplementationContractPath(stateDir string, f *feature.Feature, repoName string) (string, bool) {
	if f == nil {
		return "", false
	}
	cycleType := resolveCycleTypeForRepo(f, repoName)
	if cycleType == "" {
		if f.CurrentRoadmapPhase > 0 {
			return PhaseTestingContractPath(stateDir, f, f.CurrentRoadmapPhase), true
		}
		return "", false
	}
	return CycleTestingContractPath(stateDir, f, repoName, cycleType), true
}

func prepareImplementationTestingContract(cfg ImplementConfig, planContent string) (string, string, error) {
	contractPath, ok := resolveImplementationContractPath(filepath.Dir(cfg.StateDir), cfg.Feature, cfg.RepoName)
	if !ok {
		return "", "", nil
	}
	if resolveCycleTypeForRepo(cfg.Feature, cfg.RepoName) == "" &&
		!cfg.Feature.EffectivePipeline().ShouldRunImplementationHarness() {
		// Medium/Large roadmap phases run no per-iteration harness; the
		// plan's automated verification is the implementer's to run and is
		// re-exercised live at Final Review. Cycle contracts keep the
		// harness for every profile. Remove any stale contract left by a
		// prior run/profile so the implementer's presence check sees none.
		if err := os.Remove(contractPath); err != nil && !os.IsNotExist(err) {
			return "", "", fmt.Errorf("removing stale testing contract: %w", err)
		}
		return "", "", nil
	}
	contract := compileImplementationTestingContract(cfg, planContent)
	if existing, err := ReadTestingContract(contractPath); err == nil {
		contract = ReconcileTestingContract(existing, contract)
	} else if !os.IsNotExist(err) {
		return "", "", fmt.Errorf("reading existing testing contract: %w", err)
	}
	if testingContractRequiresCommandRunner(&contract) && cfg.CommandRunner == nil {
		return "", "", errors.New("implementation testing contract contains harness-owned commands but CommandRunner is not configured")
	}
	if cfg.CommandRunner != nil && cfg.Feature != nil {
		resolver := func(worktreePath string) (string, error) {
			return resolveTestingContractWorktreeHEADWithRunner(cfg.CommandRunner, worktreePath)
		}
		if err := EnsureTestingContractBaseCommits(&contract, cfg.Feature.Repos, resolver); err != nil {
			return "", "", fmt.Errorf("anchoring testing contract baseline: %w", err)
		}
	}
	if err := WriteTestingContract(contractPath, contract); err != nil {
		return "", "", fmt.Errorf("writing testing contract: %w", err)
	}
	fingerprint, err := Fingerprint(contractPath)
	if err != nil {
		return "", "", fmt.Errorf("fingerprinting testing contract: %w", err)
	}
	return contractPath, fingerprint, nil
}

func verificationScopePlanRevisionFeedback(violations []ProtocolViolation) string {
	var b strings.Builder
	b.WriteString("## Verification Scope Errors\n\n")
	b.WriteString("Repair the phase plan's command scopes without changing implementation scope. Multi-repo commands require `[repo: <name>]` and run from that repository root.\n")
	for _, violation := range violations {
		fmt.Fprintf(&b, "\n- %s\n", violation.Reason)
	}
	return b.String()
}

func compileImplementationTestingContract(cfg ImplementConfig, planContent string) TestingContract {
	if cfg.Feature != nil && cfg.RepoName == "" &&
		(cfg.Feature.CurrentRoadmapPhase > 0 || resolveCycleTypeForRepo(cfg.Feature, "") != "") {
		repos := phaseReposForImplementationContract(cfg.Feature, cfg.PlanPath)
		return CompileTestingContractMultiRepo(MultiRepoContractInput{
			Repos:     repos,
			PlanText:  planContent,
			PlanPath:  cfg.PlanPath,
			PhaseType: cfg.PhaseType,
		})
	}
	return CompileTestingContract(planContent, cfg.PlanPath, cfg.PhaseType)
}

func phaseReposForImplementationContract(f *feature.Feature, planPath string) []string {
	if scope, err := PhaseScope(f, planPath); err == nil && scope.ScopeOK() && len(scope.Repos) > 0 {
		return scope.Repos
	}
	repos := make([]string, 0, len(f.Repos))
	for _, r := range f.Repos {
		repos = append(repos, r.Name)
	}
	return repos
}

// BuildImplementPrompt constructs the per-call prompt for an implementation
// iteration. Role-internal artifact paths, resource catalogs, and static
// verification discovery live in the RoleSpec-backed system prompt, the
// pre-seeded verification report, and skills/implement/SKILL.md.
func BuildImplementPrompt(planPath, exitCriteria, feedback, helpAnswers string, iteration int) string {
	return roles.BuildImplementPrompt(roles.ImplementUserInput{
		PlanPath:             planPath,
		ExitCriteria:         exitCriteria,
		Feedback:             feedback,
		PlanRevisionFeedback: implementationPlanRevisionFeedback(planPath, iteration),
		HelpAnswers:          helpAnswers,
		Iteration:            iteration,
	})
}

func implementationPlanRevisionFeedback(planPath string, iteration int) string {
	if iteration <= 1 || strings.TrimSpace(planPath) == "" {
		return ""
	}
	planDir := filepath.Dir(planPath)
	feedbackAttempt, feedback := latestPlanRevisionFeedbackAttempt(planDir)
	if feedbackAttempt == 0 || strings.TrimSpace(feedback) == "" {
		return ""
	}
	if len(MissingEvidenceRequirements(feedback)) == 0 && len(InsufficientEvidenceRequirements(feedback)) == 0 {
		return ""
	}
	if LatestCompletedPlanAttempt(planDir) <= feedbackAttempt {
		return ""
	}
	return feedback
}

// buildHelpAnswers formats answered help requests into a string for the prompt.
func buildHelpAnswers(queue []feature.HelpRequest) string {
	var answers []string
	for _, h := range queue {
		if !h.Pending && h.Answer != "" {
			answers = append(answers, fmt.Sprintf("Q: %s\nA: %s", h.Question, h.Answer))
		}
	}
	return strings.Join(answers, "\n\n")
}

// maxAutoResumeAttempts caps how many consecutive CLI-truncated results
// we auto-resume before treating the turn as a protocol violation.
const maxAutoResumeAttempts = 3

// autoResumeMessage is the user-facing continuation sent to the session
// when the CLI truncated an invocation mid-work. It reminds the agent of
// the completion protocol so a genuinely-finished agent can exit cleanly.
const autoResumeMessage = `Continue where you left off. If the task is complete, validate the required artifacts and finish with exactly one <agentico-outcome>{"status":"success"}</agentico-outcome> or <agentico-outcome>{"status":"retry"}</agentico-outcome> tag.`

// backgroundTaskPollInterval is how often the waiter re-checks a session that
// ended its turn while background subagents were still running. Declared as
// var (not const) so tests can override it.
var backgroundTaskPollInterval = 2 * time.Second

// backgroundTaskQuietGrace is how long a session whose background tasks have
// all finished may stay quiet (no stdout) before the waiter concludes the CLI
// did not re-invoke the agent on its own and sends the auto-resume
// continuation.
var backgroundTaskQuietGrace = 15 * time.Second

// liveBackgroundTaskCounter is the optional session capability that reports
// running background subagents. Provider sessions that do not track them
// (or test doubles that predate the capability) simply never defer.
// liveBackgroundTasks returns the session's running background-subagent count.
func liveBackgroundTasks(sess ports.SessionView) int {
	if sess == nil {
		return 0
	}
	return sess.LiveBackgroundTaskCount()
}

func rootCompletionIntent(sess ports.SessionView) llm.CompletionIntent {
	if sess == nil {
		return llm.CompletionIntent{}
	}
	return sess.RootCompletionIntent()
}

func hasPendingRootQuestion(sess ports.SessionView) bool {
	return sess != nil && sess.HasPendingRootAskUserQuestion()
}

func completionIntentViolations(intent llm.CompletionIntent, expectedArtifacts []string) []ProtocolViolation {
	if intent.Error != "" {
		return []ProtocolViolation{{Artifact: "agentico-outcome", Reason: intent.Error}}
	}
	if len(expectedArtifacts) == 0 {
		return []ProtocolViolation{{
			Artifact: "agentico-outcome",
			Reason:   "root agent ended the turn without exactly one structured completion outcome",
		}}
	}
	violations := make([]ProtocolViolation, 0, len(expectedArtifacts)+1)
	for _, artifact := range expectedArtifacts {
		if strings.TrimSpace(artifact) == "" {
			continue
		}
		violations = append(violations, ProtocolViolation{
			Artifact: artifact,
			Reason:   "required artifact was not committed by a valid root outcome",
		})
	}
	return append(violations, ProtocolViolation{
		Artifact: "agentico-outcome",
		Reason:   "root agent ended the turn without exactly one structured completion outcome",
	})
}

func protocolViolationArtifacts(violations []ProtocolViolation) []string {
	seen := make(map[string]struct{}, len(violations))
	artifacts := make([]string, 0, len(violations))
	for _, violation := range violations {
		artifact := strings.TrimSpace(violation.Artifact)
		if artifact == "" {
			continue
		}
		if _, ok := seen[artifact]; ok {
			continue
		}
		seen[artifact] = struct{}{}
		artifacts = append(artifacts, artifact)
	}
	return artifacts
}

// maxFinishOrViolateNudges caps how many times a session that ended its turn
// without a valid root outcome is nudged to finish on the same live session
// before the turn is counted as a protocol violation.
const maxFinishOrViolateNudges = 2

// finishOrViolateNudgeFragment is a stable substring present in every
// finish-or-violate nudge. Tests match on it to detect the nudge without
// coupling to the full prompt wording.
const finishOrViolateNudgeFragment = "do not re-investigate"

// formatFinishOrViolateNudge builds the single-purpose continuation sent when a
// session ended its turn without a committable root outcome.
func formatFinishOrViolateNudge(missing []string) string {
	if len(missing) == 0 {
		return `You ended your turn without a valid completion outcome. Finish the required artifacts, run the artifact preflight, then emit exactly one <agentico-outcome>{"status":"success"}</agentico-outcome> or <agentico-outcome>{"status":"retry"}</agentico-outcome> tag. Do not start new work, ` + finishOrViolateNudgeFragment + ", and use AskUserQuestion only if the task is genuinely blocked on the user."
	}
	return fmt.Sprintf(`You ended your turn without a committable completion outcome. Still invalid or missing: %s. Fix only those artifacts, rerun the artifact preflight, then emit exactly one <agentico-outcome>{"status":"success"}</agentico-outcome> or <agentico-outcome>{"status":"retry"}</agentico-outcome> tag. Do not start new work, %s, and use AskUserQuestion only if the task is genuinely blocked on the user.`,
		strings.Join(missing, ", "), finishOrViolateNudgeFragment)
}

// decideFinishOrViolate sends one bounded completion-protocol nudge to the
// same live session after a clean provider turn that carried no committable
// root outcome.
func decideFinishOrViolate(sess ports.SessionView, disposition llm.TurnDisposition, nudges *int, missing []string) bool {
	if disposition != llm.TurnProtocolViolation {
		return false
	}
	return sendCompletionNudge(sess, nudges, formatFinishOrViolateNudge(missing))
}

// sendCompletionNudge delivers one completion-protocol correction to the live
// session, bounded by the shared finish-or-violate budget.
func sendCompletionNudge(sess ports.SessionView, nudges *int, message string) bool {
	if *nudges >= maxFinishOrViolateNudges {
		return false
	}
	*nudges++
	if err := sess.SendUserMessage(message); err != nil {
		return false
	}
	return true
}

// formatCommitViolationNudge builds the correction sent when a parsed root
// outcome was rejected at commit time. Unlike the missing-outcome nudge it
// carries the rejection reasons verbatim and offers only the outcome verbs the
// role may actually use — re-offering "retry" to a role without iteration
// state would invite the same violation again.
func formatCommitViolationNudge(violations []ProtocolViolation, retryAllowed bool) string {
	var reasons strings.Builder
	for _, violation := range violations {
		artifact := strings.TrimSpace(violation.Artifact)
		if artifact == "" {
			artifact = "agentico-outcome"
		}
		fmt.Fprintf(&reasons, "- %s: %s\n", artifact, violation.Reason)
	}
	verbs := `<agentico-outcome>{"status":"success"}</agentico-outcome> or <agentico-outcome>{"status":"retry"}</agentico-outcome>`
	roleNote := ""
	if !retryAllowed {
		verbs = `<agentico-outcome>{"status":"success"}</agentico-outcome>`
		roleNote = ` "retry" is not a valid outcome for your role: record requested changes or findings in your role's artifacts (for reviewers, the feedback verdict), then emit success to complete your assignment.`
	}
	return fmt.Sprintf(`Your completion outcome was rejected:
%s
Fix only what is listed, rerun the artifact preflight, then emit exactly one %s tag.%s Do not start new work, %s, and use AskUserQuestion only if the task is genuinely blocked on the user.`,
		strings.TrimRight(reasons.String(), "\n"), verbs, roleNote, finishOrViolateNudgeFragment)
}

const (
	agentStatusSuccess           = "SUCCESS"
	agentStatusFailed            = "FAILED"
	agentStatusAPIError          = "API_ERROR"
	agentStatusProtocolViolation = "PROTOCOL_VIOLATION"
	agentStatusChangesRequested  = "CHANGES_REQUESTED"
	agentStatusApproved          = "APPROVED"
)

// minContextHandoffWindowTokens disables the implementation wind-down nudge
// for smaller context windows. Those models benefit less from a proactive
// handoff because the prompt itself consumes meaningful remaining context.
const minContextHandoffWindowTokens = 1_000_000

// defaultContextHandoffThresholdPct is the context-window utilization
// (percent) at which WaitForPhaseOutcome nudges the agent to wrap up cleanly for
// providers without a provider-specific override. The default is chosen well
// below the Claude CLI's auto-compact trigger (~85%) so the agent has headroom
// to write a handoff and emit a structured retry outcome before the CLI would
// otherwise compact and lose working memory.
const defaultContextHandoffThresholdPct = 60

const largeCommandOutputThresholdChars = 20_000

// contextHandoffPollInterval is how often the implementation waiter samples
// session.ContextPercentage() to decide whether to send the handoff message.
// Declared as var (not const) so tests can override it without a flag plumb.
var contextHandoffPollInterval = 2 * time.Second

// contextHandoffMessageBody is the user-facing instruction injected when the
// session's context utilization first crosses its provider-specific threshold.
// A fresh iteration will pick up from the updated progress.md with a clean
// context, so the agent should stop taking new work and leave a good handoff
// — and emit `## Iteration State: RETRY` so the harness skips the review
// gate and routes straight to the next iteration.
const contextHandoffMessageBody = `Wind this iteration down now so the next iteration can resume with a fresh context; the outer loop will spawn a new session seeded with progress.md.

Do this, in order:

1. Do NOT start new implementation work or new substantial tool calls.
2. Write progress.md per skills/implement/SKILL.md's section schema:
   - ` + "`" + `## Iteration Handoff` + "`" + ` (Completed / Remaining / Where I stopped / Gotchas).
   - ` + "`" + `## Deferrals` + "`" + ` (a fenced YAML block; ` + "`" + `deferrals: []` + "`" + ` and ` + "`" + `closed_deferrals: []` + "`" + ` if you have nothing to declare).
   - ` + "`" + `## Iteration State` + "`" + ` set to ` + "`" + `RETRY` + "`" + ` so the harness skips review and starts the next iteration with no reviewer feedback.
3. End with exactly ` + "`" + `<agentico-outcome>{"status":"retry","summary":"context handoff"}</agentico-outcome>` + "`" + `. Do not create or edit ` + "`" + `phase_complete` + "`" + `; the harness writes the receipt after validation.`

type contextSnapshot struct {
	Pct            int
	ThresholdPct   int
	TotalTokens    int
	WindowTokens   int
	BaselineTokens int
}

// PhaseOutcomeWaitOptions configures the provider-neutral autonomous-turn
// completion boundary.
type PhaseOutcomeWaitOptions struct {
	// CommitOutcome validates a root intent against the phase contract and
	// writes the harness-owned completion receipt. A nil callback is a
	// protocol error for a completed autonomous turn.
	CommitOutcome func(llm.CompletionIntent) ([]ProtocolViolation, error)
	// RetryOutcomeAllowed mirrors the role's iteration-state capability so a
	// commit-rejection nudge offers only the outcome verbs the role may use.
	// Leave false for every role without a progress artifact.
	RetryOutcomeAllowed bool
	// EnableContextHandoff arms the context-utilization wind-down nudge.
	// The handoff message references the implement progress.md schema, so
	// only Implementation-phase sessions should set this true. Plan,
	// roadmap, and final-review sessions produce different
	// artifacts and have heavy required-reading loads that would trip the
	// threshold before they could write any output.
	EnableContextHandoff bool
	OnContextHandoff     func(contextSnapshot)
	// ContextHandoffPollHook is a test hook for observing ticker progress
	// without sleeping. Production callers leave it nil.
	ContextHandoffPollHook func()
	// MissingArtifacts names the completion artifacts the session must produce.
	// It seeds the bounded protocol-nudge text before contract validation can
	// report more precise violations.
	MissingArtifacts []string
}

// PhaseOutcomeWaitResult reports the semantic result of an autonomous root
// turn, including any completion-contract failure.
type PhaseOutcomeWaitResult struct {
	Status             string
	Handoff            contextSnapshot
	ProtocolViolations []ProtocolViolation
	Err                error
}

func contextTotalTokens(usage *llm.Usage) int {
	if usage == nil {
		return 0
	}
	if usage.ContextTotalTokens > 0 {
		return usage.ContextTotalTokens
	}
	return usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
}

func currentContextSnapshot(sess ports.SessionView, thresholdPct int) contextSnapshot {
	if sess == nil {
		return contextSnapshot{Pct: -1, ThresholdPct: thresholdPct}
	}
	usage := sess.LatestUsage()
	snap := contextSnapshot{
		Pct:          sess.ContextPercentage(),
		ThresholdPct: thresholdPct,
	}
	if usage != nil {
		snap.TotalTokens = contextTotalTokens(usage)
		snap.WindowTokens = usage.ContextWindow
		snap.BaselineTokens = usage.ContextBaseline
	}
	return snap
}

func formatContextHandoffMessage(snap contextSnapshot) string {
	return fmt.Sprintf("Your context window is ~%d%% full, above Agentic's %d%% handoff threshold.\n\n%s",
		snap.Pct, snap.ThresholdPct, contextHandoffMessageBody)
}

func iterationContextMeta(sess ports.SessionView, handoff contextSnapshot) *ContextMeta {
	if sess == nil {
		return nil
	}
	threshold := defaultContextHandoffThresholdPct
	final := currentContextSnapshot(sess, threshold)
	if final.Pct < 0 || final.WindowTokens == 0 {
		return nil
	}
	meta := &ContextMeta{
		Provider:       sess.ProviderName(),
		ThresholdPct:   threshold,
		FinalPct:       final.Pct,
		TotalTokens:    final.TotalTokens,
		WindowTokens:   final.WindowTokens,
		BaselineTokens: final.BaselineTokens,
	}
	if handoff.Pct >= threshold {
		meta.HandoffTriggered = true
		meta.HandoffPct = handoff.Pct
		meta.HandoffTotalTokens = handoff.TotalTokens
	}
	return meta
}

func enableTurnContinuation(sessOpts *ports.SessionOpts) *ports.SessionOpts {
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	sessOpts.KeepAliveOnTurnResult = true
	return sessOpts
}

// WaitForPhaseOutcome waits for a provider turn, classifies the root-owned
// semantic outcome, and invokes the harness commit boundary. Provider process
// completion, AskUserQuestion, delegated-task liveness, and phase completion are
// deliberately independent signals.
func WaitForPhaseOutcome(sess ports.SessionView, opts PhaseOutcomeWaitOptions) PhaseOutcomeWaitResult {
	// Counts consecutive auto-resumes triggered by CLI truncation. Resets
	// whenever we observe a non-truncated result, so each fresh stall has
	// its own retry budget.
	autoResumeAttempts := 0

	// Counts bounded completion-protocol nudges sent after a clean provider
	// turn carried no committable root outcome.
	finishOrViolateNudges := 0

	// Periodically sample the session's context-window utilization and, on
	// first crossing of the provider-specific threshold, nudge the agent to
	// wrap up cleanly. Sending once is enough — the outer loop will restart
	// a fresh iteration from the updated progress.md.
	//
	// Gated on EnableContextHandoff: only Implementation-phase sessions
	// arm the nudge. When disabled, handoffSent starts true so the ticker
	// case is a no-op for plan, roadmap, and final-review.
	var handoffC <-chan time.Time
	var handoffTicker *time.Ticker
	if opts.EnableContextHandoff {
		handoffTicker = time.NewTicker(contextHandoffPollInterval)
		handoffC = handoffTicker.C
		defer handoffTicker.Stop()
	}
	handoffSent := false
	handoff := contextSnapshot{
		Pct:          -1,
		ThresholdPct: defaultContextHandoffThresholdPct,
	}

	// awaitingBackgroundTasks is set when the agent ended its turn while its
	// background subagents were still running. The waiter then defers: the CLI
	// re-invokes the agent when the tasks complete, so no nudge is sent and no
	// budget is consumed. The bgTicker below provides the fallback paths.
	awaitingBackgroundTasks := false
	bgTicker := time.NewTicker(backgroundTaskPollInterval)
	defer bgTicker.Stop()

	handleStatus := func(status string, sessionDone bool) (PhaseOutcomeWaitResult, bool) {
		if status == agentStatusAPIError {
			if sessionDone {
				return PhaseOutcomeWaitResult{Status: agentStatusFailed, Handoff: handoff}, true
			}
			return PhaseOutcomeWaitResult{}, false
		}
		if status != agentStatusSuccess {
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{Status: status, Handoff: handoff}, true
		}

		intent := rootCompletionIntent(sess)
		disposition := llm.ClassifyTurn(llm.TurnSignals{
			Result:              sess.Cost(),
			RootIntent:          intent,
			RootQuestionPending: hasPendingRootQuestion(sess),
			TasksRunning:        liveBackgroundTasks(sess) > 0,
		})
		switch disposition {
		case llm.TurnAwaitingUser:
			if sessionDone {
				return PhaseOutcomeWaitResult{
					Status:  agentStatusFailed,
					Handoff: handoff,
					Err:     errors.New("root agent requested user input after the provider session exited"),
				}, true
			}
			return PhaseOutcomeWaitResult{}, false

		case llm.TurnAwaitingTasks:
			if sessionDone {
				return PhaseOutcomeWaitResult{
					Status:  agentStatusProtocolViolation,
					Handoff: handoff,
					ProtocolViolations: []ProtocolViolation{{
						Artifact: "agentico-outcome",
						Reason:   "provider session exited while delegated tasks were still running",
					}},
				}, true
			}
			awaitingBackgroundTasks = true
			return PhaseOutcomeWaitResult{}, false

		case llm.TurnTruncated:
			awaitingBackgroundTasks = false
			if autoResumeAttempts < maxAutoResumeAttempts {
				autoResumeAttempts++
				if err := sess.SendUserMessage(autoResumeMessage); err == nil {
					return PhaseOutcomeWaitResult{}, false
				}
			}
			violations := []ProtocolViolation{{
				Artifact: "agentico-outcome",
				Reason:   "provider repeatedly truncated the root turn before a completion outcome",
			}}
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:             agentStatusProtocolViolation,
				Handoff:            handoff,
				ProtocolViolations: violations,
			}, true

		case llm.TurnCommitSuccess, llm.TurnCommitRetry:
			awaitingBackgroundTasks = false
			autoResumeAttempts = 0
			if opts.CommitOutcome == nil {
				violations := []ProtocolViolation{{
					Artifact: "agentico-outcome",
					Reason:   "harness completion committer is not configured",
				}}
				_ = sess.Stop()
				return PhaseOutcomeWaitResult{
					Status:             agentStatusProtocolViolation,
					Handoff:            handoff,
					ProtocolViolations: violations,
				}, true
			}
			violations, err := opts.CommitOutcome(intent)
			if err != nil {
				_ = sess.Stop()
				return PhaseOutcomeWaitResult{Status: agentStatusFailed, Handoff: handoff, Err: err}, true
			}
			if len(violations) == 0 {
				_ = sess.Stop()
				return PhaseOutcomeWaitResult{Status: agentStatusSuccess, Handoff: handoff}, true
			}
			if !sessionDone &&
				sendCompletionNudge(sess, &finishOrViolateNudges,
					formatCommitViolationNudge(violations, opts.RetryOutcomeAllowed)) {
				return PhaseOutcomeWaitResult{}, false
			}
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:             agentStatusProtocolViolation,
				Handoff:            handoff,
				ProtocolViolations: violations,
			}, true

		case llm.TurnProtocolViolation:
			awaitingBackgroundTasks = false
			autoResumeAttempts = 0
			violations := completionIntentViolations(intent, opts.MissingArtifacts)
			if !sessionDone && decideFinishOrViolate(sess, disposition, &finishOrViolateNudges, protocolViolationArtifacts(violations)) {
				return PhaseOutcomeWaitResult{}, false
			}
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:             agentStatusProtocolViolation,
				Handoff:            handoff,
				ProtocolViolations: violations,
			}, true

		case llm.TurnRefused:
			violations := []ProtocolViolation{{
				Artifact: "agentico-outcome",
				Reason:   "root model refused the phase assignment",
			}}
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:             agentStatusProtocolViolation,
				Handoff:            handoff,
				ProtocolViolations: violations,
			}, true

		case llm.TurnErrored:
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{Status: agentStatusFailed, Handoff: handoff}, true

		default:
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:  agentStatusFailed,
				Handoff: handoff,
				Err:     errors.New("provider ended without a classifiable root turn"),
			}, true
		}
	}

	doneCh := sess.Done()
	for {
		select {
		case <-handoffC:
			if opts.ContextHandoffPollHook != nil {
				opts.ContextHandoffPollHook()
			}
			if handoffSent {
				continue
			}
			threshold := defaultContextHandoffThresholdPct
			snap := currentContextSnapshot(sess, threshold)
			if snap.WindowTokens < minContextHandoffWindowTokens {
				continue
			}
			if snap.Pct < threshold {
				continue
			}
			if err := sess.SendUserMessage(formatContextHandoffMessage(snap)); err == nil {
				handoffSent = true
				handoff = snap
				if opts.OnContextHandoff != nil {
					opts.OnContextHandoff(snap)
				}
			}
			// On send error, leave handoffSent=false so a later tick retries.

		case <-bgTicker.C:
			if !awaitingBackgroundTasks {
				continue
			}
			if liveBackgroundTasks(sess) > 0 {
				continue
			}
			quiet := time.Since(sess.LastStdoutAt())
			// All tasks finished but no new result arrived: the CLI did not
			// re-invoke the agent on its own. Resume it explicitly, reusing
			// the truncation budget so a session that keeps yielding without
			// finishing still converges to a violation.
			if quiet < backgroundTaskQuietGrace {
				continue
			}
			awaitingBackgroundTasks = false
			if autoResumeAttempts < maxAutoResumeAttempts {
				autoResumeAttempts++
				if err := sess.SendUserMessage(autoResumeMessage); err == nil {
					continue
				}
			}
			_ = sess.Stop()
			return PhaseOutcomeWaitResult{
				Status:  agentStatusProtocolViolation,
				Handoff: handoff,
				ProtocolViolations: []ProtocolViolation{{
					Artifact: "agentico-outcome",
					Reason:   "delegated tasks completed but the root agent did not resume with a completion outcome",
				}},
			}

		case <-doneCh:
			// Session exited — drain StatusCh for any pending status
			select {
			case status := <-sess.StatusCh():
				if result, done := handleStatus(status, true); done {
					return result
				}
				// A truncated SUCCESS drained after Done() still gets the
				// same auto-resume behavior as the StatusCh branch. The
				// done channel is already closed, so disable this select
				// case while waiting for the resumed turn's next status.
				doneCh = nil
				continue
			default:
			}
			// The session message log and cost are updated before the
			// status channel is signaled. Under load, Done can still win
			// this select before the status send is observed. Preserve the
			// terminal result classification instead of demoting the turn
			// to a generic failure.
			if cost := sess.Cost(); cost != nil {
				if result, done := handleStatus(statusFromResult(cost), true); done {
					return result
				}
				doneCh = nil
				continue
			}
			// A provider process exit is transport state, not a semantic
			// completion signal. Without a Result there is nothing to commit.
			return PhaseOutcomeWaitResult{Status: agentStatusFailed, Handoff: handoff}

		case status := <-sess.StatusCh():
			if result, done := handleStatus(status, false); done {
				return result
			}
			// API error — the agent is stuck but still alive, or a
			// truncated turn was auto-resumed. Keep waiting for the next
			// terminal result.
			continue
		}
	}
}

func statusFromResult(result *llm.ResultMessage) string {
	switch {
	case result.Subtype == "error":
		return agentStatusAPIError
	case result.IsSuccess():
		return agentStatusSuccess
	default:
		return agentStatusFailed
	}
}

// appendIterationLog appends a single iteration's output to the aggregate log
// at the well-known path that the desktop app log viewer reads.
func appendIterationLog(path string, iteration int, output string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "\n=== Iteration %d ===\n\n", iteration)
	_, _ = f.WriteString(output)
}

func emitLargeCodexCommandOutputEvents(obs *observe.Observer, sc observe.SpanContext, phase, sessionID, repoName, provider string, iteration int, responsePath string) {
	if obs == nil || !strings.EqualFold(provider, "codex") || responsePath == "" {
		return
	}
	f, err := os.Open(responsePath)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 50*1024*1024)
	for scanner.Scan() {
		event, ok := parseLargeCodexCommandOutput(scanner.Bytes(), largeCommandOutputThresholdChars)
		if !ok {
			continue
		}
		obs.ContextLargeOutput(sc, phase, sessionID, repoName, provider, iteration,
			event.Command, event.OutputChars, largeCommandOutputThresholdChars, event.ExitCode, event.DurationMs)
	}
}

type largeCodexCommandOutput struct {
	Command     string
	OutputChars int
	ExitCode    *int
	DurationMs  *int64
}

func parseLargeCodexCommandOutput(line []byte, threshold int) (largeCodexCommandOutput, bool) {
	var raw struct {
		Method string `json:"method"`
		Params struct {
			Item struct {
				Type             string `json:"type"`
				Command          string `json:"command"`
				AggregatedOutput string `json:"aggregatedOutput"`
				ExitCode         *int   `json:"exitCode"`
				DurationMs       *int64 `json:"durationMs"`
			} `json:"item"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return largeCodexCommandOutput{}, false
	}
	if raw.Method != "item/completed" || raw.Params.Item.Type != "commandExecution" {
		return largeCodexCommandOutput{}, false
	}
	outputChars := len([]rune(raw.Params.Item.AggregatedOutput))
	if outputChars < threshold {
		return largeCodexCommandOutput{}, false
	}
	return largeCodexCommandOutput{
		Command:     raw.Params.Item.Command,
		OutputChars: outputChars,
		ExitCode:    raw.Params.Item.ExitCode,
		DurationMs:  raw.Params.Item.DurationMs,
	}, true
}

func readPlanContent(planPath string) string {
	if planPath == "" {
		return ""
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// setReviewingGate persists the ReviewingGate flag on the feature so the
// desktop app dashboard can reflect that the review gate is currently running.
func setReviewingGate(cfg ImplementConfig, reviewing bool) {
	if cfg.FeatureStore == nil {
		return
	}
	_ = cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.ReviewingGate = reviewing
		return nil
	})
}

// shutdownDetectionGrace bounds how long waitForShutdownIntent will wait for
// sm.IsShuttingDown() to flip true after a session died. When the user presses
// Ctrl+C, SIGINT hits the agent CLI child first (via the process group) and
// the session's Done() channel fires before desktop app has unwound and main.go
// has called sm.Shutdown(). A short wait here absorbs that race; anything
// longer would delay real agent failures (API errors, auth expiry, etc.).
// 500ms is empirically enough for the desktop/server shutdown path to call
// sm.Shutdown().
const shutdownDetectionGrace = 500 * time.Millisecond

// shutdownChecker is the narrow interface waitForShutdownIntent needs; any
// type with an IsShuttingDown() bool method satisfies it, which lets tests
// exercise the grace-period logic without implementing the full
// ports.SessionManager surface. Real callers pass a *session.Manager, which
// satisfies this trivially because ports.SessionManager already requires
// IsShuttingDown().
type shutdownChecker interface {
	IsShuttingDown() bool
}

// waitForShutdownIntent returns true if sm signals shutdown within grace,
// polling every 25ms. Returns false immediately when sm is nil, and as soon
// as the deadline passes without shutdown. This lets callers distinguish
// "session died because we're shutting down" from "session died for a real
// reason" without writing a premature FAILED state for the former.
func waitForShutdownIntent(sm shutdownChecker, grace time.Duration) bool {
	if sm == nil {
		return false
	}
	if sm.IsShuttingDown() {
		return true
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		if sm.IsShuttingDown() {
			return true
		}
	}
	return false
}

// isFeatureInterrupted checks the feature store to determine if the feature
// has been stopped by the user. Loops call this before starting new sessions
// to avoid zombie iterations after the user presses 's' to stop.
// verificationContext returns a context that cancels once the feature is
// interrupted or failed, so a hung verification command cannot outlive a user
// interrupt by its full declared timeout. The caller must call cancel.
func verificationContext(store ports.FeatureStore, featureID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if store == nil {
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if isFeatureInterrupted(store, featureID) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func isFeatureInterrupted(store ports.FeatureStore, featureID string) bool {
	if store == nil {
		return false
	}
	f, err := store.Load(featureID)
	if err != nil {
		return false
	}
	return f.Status == feature.StatusInterrupted || f.Status == feature.StatusFailed
}
