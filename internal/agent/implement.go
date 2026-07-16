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
	ArtifactDir         string // base dir for iteration artifacts
	StateDir            string // feature state directory for PID files
	RunDir              string // active run directory granted to the agent; empty derives from Feature.ActiveRun

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

	// RepoName is the repo name for session ID namespacing in multi-repo features.
	RepoName string

	// EffortLevel is the pipeline-driven effort level passed to providers.
	EffortLevel llm.EffortLevel

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
	// Moonshot, multi-repo orchestration, repo-scoped cycles, and refactor loops
	// keep per-iteration review enabled.
	SkipIterationReview bool

	// FinishOrViolateNudge arms the finish-or-violate auto-continuation retry
	// for this loop's sessions: the session runs in interactive turn mode and,
	// on a deliberate end_turn without the completion marker, is nudged to
	// finish before a protocol violation is recorded. Resolved per-model from
	// the provider capability, so only capability-positive providers opt in.
	FinishOrViolateNudge bool
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
//   - "need_user_input":   iteration emitted NEED_USER_INPUT — single-repo
//     pause gate. NeedUserInputPath points to the persisted gate artifact
//     and Iterations carries the iteration that emitted the gate so the
//     orchestrator can resume at iteration N+1.
type LoopResult struct {
	FinalStatus string
	Iterations  int
	LastError   string

	// PlanRevisionFeedback carries phase-plan repair requirements when
	// FinalStatus == "plan_revision_required".
	PlanRevisionFeedback string

	// NeedUserInputPath is the absolute path of the persisted gate
	// artifact written when FinalStatus == "need_user_input". Empty for
	// every other status.
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
	// implProtocol is built per-iteration below with the correct iterDir
	// so the agent writes phase_complete to the iteration directory.

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

	// Mid-iteration restart: if the next iteration's dir already has
	// phase_complete (but no meta.yaml, otherwise LatestIteration would have
	// advanced past it), the implement phase finished before the prior run
	// was interrupted — likely during the review gate. Skip the implement
	// session and jump straight to the review for that iteration.
	skipImplement := false
	if nextIter := startIter + 1; nextIter <= cfg.MaxIterations {
		nextIterDir := filepath.Join(cfg.ArtifactDir, fmt.Sprintf("iteration-%02d", nextIter))
		skipImplement = HasPhaseComplete(nextIterDir)
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

		// Update iteration counter so the TUI dashboard reflects progress
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
			waitResult          waitForStatusResult
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
			madeProgress, _ = pt.Check(progressPath)
			var prepareErr error
			testingContractPath, contractFingerprint, prepareErr = prepareImplementationTestingContract(cfg, planContent)
			if prepareErr != nil {
				return nil, prepareErr
			}
		} else {
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
				progressPath,
				"",
				testingContractPath,
				reviewerFeedback,
				helpAnswers,
				buildPriorUserInputAnswers(cfg.ArtifactDir),
				cfg.SkillsDir,
				cfg.GuidelinesDir,
				nil,
				i,
				cfg.KBInfos...,
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
			// Remove stale phase_complete signal from previous turns
			RemovePhaseComplete(iterDir)

			// Build the RoleSpec-backed system prompt with the iteration-specific
			// completion marker and output roots.
			implProtocol := BuildImplementSystemPrompt(BuildImplementSystemPromptInput{
				IterationDir:  iterDir,
				SkillsDir:     cfg.SkillsDir,
				GuidelinesDir: cfg.GuidelinesDir,
				KBInfos:       cfg.KBInfos,
				AskingClause:  cfg.AskingClause,
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
				Model:                          cfg.Model,
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
				MarkerPath:                     filepath.Join(iterDir, PhaseCompleteFile),
			}
			command, env, sessOpts, buildErr := cfg.BuildSession(implBuildOpts)
			if buildErr != nil {
				return nil, fmt.Errorf("building session for iteration %d: %w", i, buildErr)
			}

			sessOpts = enableTruncatedTurnAutoResume(sessOpts)
			if cfg.FinishOrViolateNudge {
				sessOpts.TurnMode = ports.TurnModeInteractive
			}
			WriteDebugPrompts(iterDir, sessOpts.DebugSystemPrompt, prompt)
			// Merge iteration-specific fields into session opts
			sessOpts.Iteration = i
			sessOpts.PermCacheScope = permRepoName
			// Capture provider stderr so silent process deaths are diagnosable.
			sessOpts.StderrPath = filepath.Join(iterDir, "stderr.log")

			// Start session in interactive mode
			if cfg.Feature.CurrentRoadmapPhase > 0 {
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
			cfg.Observer.SessionStarted(implSessionCtx, "implement", sessionID, implProvider, cfg.Model, cfg.RepoName)
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
			implWaitOptions := func(sess ports.SessionHandle, sessionCtx observe.SpanContext, sessionID string) waitForStatusOptions {
				return waitForStatusOptions{
					ReadyCheck: func() bool {
						if HasPhaseComplete(iterDir) {
							// Agent completed its work — clear any stale question flag
							// so we don't block on a question the agent already moved past.
							sess.SetHasUnansweredQuestion(false)
							return true
						}
						// No phase_complete yet — the agent is likely waiting for user input.
						return false
					},
					FinishOrViolateNudge: cfg.FinishOrViolateNudge,
					MissingArtifacts:     []string{"progress.md"},
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

			// Wait for status marker or session exit. If the SDK reports a
			// non-truncated success without phase_complete, the iteration below
			// records a protocol violation instead of waiting for user input.
			waitResult = waitForStatusDetailed(sess, sm, sessionID, implWaitOptions(sess, implSessionCtx, sessionID))
			agentStatus = waitResult.Status

			// App shutdown should not serialize an in-flight iteration as FAILED.
			// Leaving the iteration incomplete (no meta.yaml) allows restart to replay
			// the same iteration number instead of incorrectly advancing to the next.
			//
			// Grace period: a bare `sm.IsShuttingDown()` check races against how
			// shutdown signals reach us. When the user presses Ctrl+C, SIGINT
			// propagates through the process group, killing the agent CLI
			// *before* the TUI unwinds and main.go calls sm.Shutdown(). The
			// session dies, waitForStatus returns FAILED, and this check would
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
			if agentStatus == agentStatusFailed && sessOpts != nil && sessOpts.SupportsSessionResume && !HasPhaseComplete(iterDir) {
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
						cfg.Observer.SessionStarted(implSessionCtx, "implement", sessionID, sessOpts.ProviderName, cfg.Model, cfg.RepoName)
						implTracker.Install(sess, implSessionCtx, "implement", sessionID)

						waitResult = waitForStatusDetailed(sess, sm, sessionID, implWaitOptions(sess, implSessionCtx, sessionID))
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

			// Append iteration output to aggregate log so the TUI log viewer works
			appendIterationLog(aggregateLogPath, i, output)

			// Check progress
			madeProgress, _ = pt.Check(progressPath)

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
		if agentStatus == agentStatusMissingMarker {
			violations := []ProtocolViolation{{
				Artifact: PhaseCompleteFile,
				Reason:   "SDK reported success but phase_complete was not present",
			}}
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
				verifyCtx, cancelVerify := verificationContext(cfg.FeatureStore, cfg.Feature.ID)
				harnessVerification, readErr = ExecuteTestingContract(verifyCtx, cfg.CommandRunner, contract, &report, testingContractPath, iterDir, cfg.WorkDir, verificationRepos)
				cancelVerify()
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
				rec := SynthesizeVerificationNeedUserInputGate(testingContractPath, harnessVerification.Report.ContractRevision, harnessVerification.BlockedItems, i)
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
				meta.ReviewStatus = "skipped_retry"
				meta.AgentStatus = "RETRY"
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

			// NEED_USER_INPUT: consume the agent-authored gate artifact and return a
			// non-terminal loop result. The orchestrator transitions the
			// feature into StatusNeedUserInput; the iteration's meta.yaml
			// is committed so LatestIteration() advances past it and a
			// later resume runs iteration N+1.
			if parsed.State == StateNeedUserInput {
				gatePath := NeedUserInputPath(iterDir)
				rec := reconcileNeedUserInputGate(outcome.NeedUserInput, parsed, i)
				if err := WriteNeedUserInputRecord(gatePath, rec); err != nil {
					return nil, fmt.Errorf("persisting need-user-input gate: %w", err)
				}
				meta.AgentStatus = "NEED_USER_INPUT"
				meta.ReviewStatus = "skipped_need_user_input"
				_ = am.WriteMeta(iterDir, meta)
				_ = am.WriteSummary(summaryPath, meta)
				consecutiveFailures = 0
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "need_user_input")
				return &LoopResult{
					FinalStatus:       "need_user_input",
					Iterations:        i,
					LastError:         strings.TrimSpace(rec.Summary),
					NeedUserInputPath: gatePath,
				}, nil
			}

			// Fall through: SUCCESS — run the review gate.
			if cfg.SkipIterationReview {
				// Medium/Large: skip per-iteration review, rely on Final Review.
				// Deterministically classified regressions still route back to
				// the implementer — with no per-iteration reviewer there is
				// nobody else to act on a red harness report.
				if harnessVerification != nil && len(harnessVerification.RegressionItems) > 0 {
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
			// cleanly instead. The phase_complete marker from the implement
			// phase stays on disk, so restart will route through the
			// skipImplement branch at line 172 and resume the review for
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
			reviewStatus, feedback, reviewErr := runReviewGate(cfg, sm, i, iterDir, parsed, reviewCtx)
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
			// skipImplement check at line 174 find the phase_complete
			// marker, routing restart back to iteration N for review only.
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
				// Leave meta.yaml unwritten: phase_complete stays on disk, so
				// on resume LatestIteration stops at i-1 and the skipImplement
				// branch re-runs the review for this same iteration only.
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "review_error")
				return &LoopResult{
					FinalStatus: "review_error",
					Iterations:  i - 1,
					LastError:   fmt.Sprintf("review did not complete: %v; resume to re-run the review for iteration %d", reviewErr, i),
				}, nil
			}

			meta.ReviewStatus = reviewStatus.String()
			_ = am.WriteMeta(iterDir, meta)
			_ = am.WriteSummary(summaryPath, meta)

			switch reviewStatus {
			case ReviewApproved:
				consecutiveFailures = 0
				cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), finalStatusReviewPassed)
				return &LoopResult{
					FinalStatus: finalStatusReviewPassed,
					Iterations:  i,
				}, nil
			case ReviewChangesRequested:
				if reqs := MissingEvidenceRequirements(feedback); len(reqs) > 0 {
					consecutiveFailures = 0
					cfg.Observer.IterationEnded(iterCtx, i, toSessionUsage(cost), time.Since(iterStart), "plan_revision_required")
					return &LoopResult{
						FinalStatus:          "plan_revision_required",
						Iterations:           i,
						PlanRevisionFeedback: MissingEvidencePlanRevisionFeedback(reqs),
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
	"required artifacts and the completion marker per your instructions; otherwise update progress and continue " +
	"from where you left off."

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
	sessOpts = enableTruncatedTurnAutoResume(sessOpts)
	if cfg.FinishOrViolateNudge {
		sessOpts.TurnMode = ports.TurnModeInteractive
	}
	sessOpts.Iteration = iteration
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
		reason = "phase_complete was missing"
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
	case agentStatusMissingMarker:
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
	RemovePhaseComplete(reviewDir)

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
	if cfg.Feature != nil && cfg.Feature.CurrentRoadmapPhase > 0 && cfg.RepoName == "" {
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
func BuildImplementPrompt(planPath, exitCriteria, progressPath, verificationReportPath, testingContractPath, feedback, helpAnswers, priorUserInputAnswers, skillsDir, guidelinesDir string, _ []RequiredVerificationItem, iteration int, kbInfos ...KBInfo) string {
	_ = progressPath
	_ = verificationReportPath
	_ = testingContractPath
	_ = skillsDir
	_ = guidelinesDir
	_ = kbInfos

	return roles.BuildImplementPrompt(roles.ImplementUserInput{
		PlanPath:              planPath,
		ExitCriteria:          exitCriteria,
		Feedback:              feedback,
		PlanRevisionFeedback:  implementationPlanRevisionFeedback(planPath, iteration),
		HelpAnswers:           helpAnswers,
		PriorUserInputAnswers: priorUserInputAnswers,
		Iteration:             iteration,
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
	if len(MissingEvidenceRequirements(feedback)) == 0 {
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
// we auto-resume before treating a missing completion marker as a protocol
// violation.
const maxAutoResumeAttempts = 3

// autoResumeMessage is the user-facing continuation sent to the session
// when the CLI truncated an invocation mid-work. It reminds the agent of
// the completion protocol so a genuinely-finished agent can exit cleanly.
const autoResumeMessage = "Continue where you left off. If you have already finished the task, write the phase_complete marker file now."

// backgroundTaskPollInterval is how often the waiter re-checks a session that
// ended its turn while background subagents were still running. Declared as
// var (not const) so tests can override it.
var backgroundTaskPollInterval = 2 * time.Second

// backgroundTaskQuietGrace is how long a session whose background tasks have
// all finished may stay quiet (no stdout) before the waiter concludes the CLI
// did not re-invoke the agent on its own and sends the auto-resume
// continuation.
var backgroundTaskQuietGrace = 15 * time.Second

// backgroundTaskStallTimeout bounds how long the waiter defers to
// still-"live" background tasks with zero stdout activity. Running subagents
// emit periodic task_progress lines, so a totally silent stream this long
// means the CLI wedged; give up instead of waiting forever.
var backgroundTaskStallTimeout = 10 * time.Minute

// liveBackgroundTaskCounter is the optional session capability that reports
// running background subagents. Provider sessions that do not track them
// (or test doubles that predate the capability) simply never defer.
type liveBackgroundTaskCounter interface {
	LiveBackgroundTaskCount() int
}

// liveBackgroundTasks returns the session's running background-subagent
// count, or 0 when the session does not expose the capability.
func liveBackgroundTasks(sess ports.SessionView) int {
	if c, ok := sess.(liveBackgroundTaskCounter); ok {
		return c.LiveBackgroundTaskCount()
	}
	return 0
}

// maxFinishOrViolateNudges caps how many times a session that ended its turn
// without writing the required completion artifacts is nudged to finish on the
// same live session before the turn is counted as a protocol violation.
const maxFinishOrViolateNudges = 2

// finishOrViolateNudgeFragment is a stable substring present in every
// finish-or-violate nudge. Tests match on it to detect the nudge without
// coupling to the full prompt wording.
const finishOrViolateNudgeFragment = "do not re-investigate"

// formatFinishOrViolateNudge builds the single-purpose continuation sent when a
// session ended its turn without producing its required completion artifacts. It
// names the missing artifacts (when known) so the agent finishes exactly that
// work and nothing else.
func formatFinishOrViolateNudge(missing []string) string {
	if len(missing) == 0 {
		return "You ended your turn but the required completion artifacts and the `phase_complete` marker are missing; write them now and nothing else. Do not start new work, " + finishOrViolateNudgeFragment + ", do not ask a question."
	}
	return fmt.Sprintf("You ended your turn without completing the required outputs. Still missing: %s. Do exactly this now and nothing else: write those artifacts to your output directory, then create the `phase_complete` marker. Do not start new work, %s, do not ask a question.",
		strings.Join(missing, ", "), finishOrViolateNudgeFragment)
}

// decideFinishOrViolate sends one finish-or-violate nudge to the same live
// session when it ended its turn (TermEndedAfterText) without writing its
// completion artifacts, up to maxFinishOrViolateNudges times. It returns true
// only when a nudge was sent and the caller should keep waiting on the session;
// it returns false for any other termination class, once the budget is
// exhausted, or when the send fails (stdin closed) so the caller falls through
// to the protocol-violation path. The caller gates this on the provider
// capability; the function itself is provider-agnostic.
func decideFinishOrViolate(sess ports.SessionHandle, class llm.TerminationClass, nudges *int, missing []string) bool {
	if class != llm.TermEndedAfterText {
		return false
	}
	if *nudges >= maxFinishOrViolateNudges {
		return false
	}
	*nudges++
	if err := sess.SendUserMessage(formatFinishOrViolateNudge(missing)); err != nil {
		return false
	}
	return true
}

const (
	agentStatusSuccess           = "SUCCESS"
	agentStatusFailed            = "FAILED"
	agentStatusAPIError          = "API_ERROR"
	agentStatusMissingMarker     = "MISSING_PHASE_COMPLETE"
	agentStatusProtocolViolation = "PROTOCOL_VIOLATION"
	agentStatusChangesRequested  = "CHANGES_REQUESTED"
	agentStatusApproved          = "APPROVED"
)

// minContextHandoffWindowTokens disables the implementation wind-down nudge
// for smaller context windows. Those models benefit less from a proactive
// handoff because the prompt itself consumes meaningful remaining context.
const minContextHandoffWindowTokens = 1_000_000

// defaultContextHandoffThresholdPct is the context-window utilization
// (percent) at which waitForStatus nudges the agent to wrap up cleanly for
// providers without a provider-specific override. The default is chosen well
// below the Claude CLI's auto-compact trigger (~85%) so the agent has headroom
// to write a handoff and the phase_complete marker before the CLI would
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

   (You are emitting RETRY here, so do NOT include the conditional ` + "`" + `## Questions for User` + "`" + ` section — it is reserved for ` + "`" + `NEED_USER_INPUT` + "`" + ` and must sit between ` + "`" + `## Deferrals` + "`" + ` and ` + "`" + `## Iteration State` + "`" + ` only when used.)
3. Touch the phase_complete marker (per your system prompt) as your very last action and end your turn.`

type contextSnapshot struct {
	Pct            int
	ThresholdPct   int
	TotalTokens    int
	WindowTokens   int
	BaselineTokens int
}

type waitForStatusOptions struct {
	ReadyCheck func() bool
	// EnableContextHandoff arms the context-utilization wind-down nudge.
	// The handoff message references the implement progress.md schema, so
	// only Implementation-phase sessions should set this true. Plan,
	// roadmap, final-review, and refactor sessions produce different
	// artifacts and have heavy required-reading loads that would trip the
	// threshold before they could write any output.
	EnableContextHandoff bool
	OnContextHandoff     func(contextSnapshot)
	// ContextHandoffPollHook is a test hook for observing ticker progress
	// without sleeping. Production callers leave it nil.
	ContextHandoffPollHook func()
	// FinishOrViolateNudge arms the finish-or-violate auto-continuation
	// retry: when a session ends its turn without writing the required
	// completion artifacts, it is nudged to finish on the same live session
	// before the turn is counted as a protocol violation. Resolved per-model
	// from the provider capability, so only capability-positive sessions opt
	// in.
	FinishOrViolateNudge bool
	// MissingArtifacts names the completion artifacts the session must
	// produce. It is used only to build the nudge text, never for control
	// flow.
	MissingArtifacts []string
}

type waitForStatusResult struct {
	Status  string
	Handoff contextSnapshot
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

// waitForStatus waits for the session to produce a result via StatusCh or exit.
// Returns the SDK-derived session status: "SUCCESS", "FAILED", or
// "API_ERROR". The harness's iteration state (SUCCESS / RETRY /
// NEED_USER_INPUT) is parsed from progress.md by ParseProgressMd
// AFTER this function returns, not from this string.
//
// If readyCheck is non-nil, it is called when SUCCESS is received. If it
// returns false, the agent ended its turn without completing the work.
// The termination is classified via llm.ClassifyTermination:
//   - TermTurnTruncated (CLI ended mid-tool-use / max_tokens / pause_turn)
//     triggers an automatic continuation message, up to
//     maxAutoResumeAttempts consecutive times. This covers the common case
//     where the claude CLI ends an invocation while the agent is still
//     working, without the agent having asked anything.
//   - TermAwaitingBackgroundTasks (turn ended while background subagents
//     were still running) defers without sending anything: the CLI
//     re-invokes the agent when its tasks complete. If the tasks finish and
//     no re-invocation arrives, the waiter falls back to the auto-resume
//     continuation; a fully wedged stream is bounded by
//     backgroundTaskStallTimeout.
//   - Anything else (EndedAfterText, Refused, Errored), or an exhausted
//     truncation retry budget, returns MISSING_PHASE_COMPLETE so the caller
//     can count the turn as a protocol violation.
func waitForStatus(sess ports.SessionHandle, sm ports.SessionManager, sessionID string, readyCheck ...func() bool) string {
	opts := waitForStatusOptions{}
	if len(readyCheck) > 0 {
		opts.ReadyCheck = readyCheck[0]
	}
	return waitForStatusDetailed(sess, sm, sessionID, opts).Status
}

func waitForImplementationStatus(sess ports.SessionHandle, sm ports.SessionManager, sessionID string, readyCheck ...func() bool) string {
	opts := waitForStatusOptions{EnableContextHandoff: true}
	if len(readyCheck) > 0 {
		opts.ReadyCheck = readyCheck[0]
	}
	return waitForStatusDetailed(sess, sm, sessionID, opts).Status
}

func enableTruncatedTurnAutoResume(sessOpts *ports.SessionOpts) *ports.SessionOpts {
	if sessOpts == nil {
		sessOpts = &ports.SessionOpts{}
	}
	sessOpts.KeepAliveOnTruncatedResult = true
	return sessOpts
}

func waitForStatusDetailed(sess ports.SessionHandle, _ ports.SessionManager, _ string, opts waitForStatusOptions) waitForStatusResult {
	isReady := opts.ReadyCheck

	// Counts consecutive auto-resumes triggered by CLI truncation. Resets
	// whenever we observe a non-truncated result, so each fresh stall has
	// its own retry budget.
	autoResumeAttempts := 0

	// Counts finish-or-violate nudges sent when the session ended its turn
	// without writing the completion marker. Bounded by
	// maxFinishOrViolateNudges; armed only when opts.FinishOrViolateNudge is
	// set (capability-positive providers).
	finishOrViolateNudges := 0

	// Periodically sample the session's context-window utilization and, on
	// first crossing of the provider-specific threshold, nudge the agent to
	// wrap up cleanly. Sending once is enough — the outer loop will restart
	// a fresh iteration from the updated progress.md.
	//
	// Gated on EnableContextHandoff: only Implementation-phase sessions
	// arm the nudge. When disabled, handoffSent starts true so the ticker
	// case is a no-op for plan, roadmap, final-review, and refactor.
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

	handleStatus := func(status string, sessionDone bool) (string, bool) {
		// A session that died or errored after completing its work product is
		// a logical success: the completion marker is the protocol's "done"
		// signal, and contract validation plus the review gate still arbitrate
		// the artifacts. This salvages iterations whose provider process dies
		// between writing the marker and exiting cleanly.
		if isReady != nil && (status == agentStatusFailed || (status == agentStatusAPIError && sessionDone)) && isReady() {
			_ = sess.Stop()
			return agentStatusSuccess, true
		}
		if status == agentStatusAPIError {
			if sessionDone {
				return agentStatusFailed, true
			}
			return "", false
		}
		// If a readiness check is provided and fails, the agent
		// ended its turn without completing the work. Classify why
		// so CLI-truncated invocations are auto-resumed instead of
		// escalating to the user.
		if status == agentStatusSuccess && isReady != nil && !isReady() {
			inputs := llm.TerminationInputs{
				Result:                 sess.Cost(),
				PhaseCompleteExists:    false, // isReady just returned false
				AskUserQuestionPending: sess.HasPendingAskUserQuestion(),
				// A dead process cannot deliver task notifications; never
				// defer to a stale task set.
				BackgroundTasksRunning: !sessionDone && liveBackgroundTasks(sess) > 0,
			}
			class := llm.ClassifyTermination(inputs)

			// The agent yielded while its background subagents are still
			// running. Keep the session alive and wait: the CLI re-invokes
			// the agent when the tasks complete. The bgTicker case handles
			// the CLI failing to do so.
			if class == llm.TermAwaitingBackgroundTasks {
				awaitingBackgroundTasks = true
				return "", false
			}
			awaitingBackgroundTasks = false

			if class == llm.TermTurnTruncated && autoResumeAttempts < maxAutoResumeAttempts {
				autoResumeAttempts++
				if err := sess.SendUserMessage(autoResumeMessage); err != nil {
					_ = sess.Stop()
					return agentStatusMissingMarker, true
				}
				return "", false
			}

			// The session deliberately ended its turn without writing the
			// completion marker. For capability-positive providers, nudge the
			// same live session to finish before escalating. On nudge, give the
			// resumed turn a fresh truncation budget and keep waiting WITHOUT
			// stopping the session.
			if opts.FinishOrViolateNudge && decideFinishOrViolate(sess, class, &finishOrViolateNudges, opts.MissingArtifacts) {
				autoResumeAttempts = 0
				return "", false
			}

			// Either not truncated, or the retry cap was reached.
			autoResumeAttempts = 0
			_ = sess.Stop()
			return agentStatusMissingMarker, true
		}
		// Got a real status — stop the session.
		_ = sess.Stop()
		return status, true
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
			quiet := time.Since(sess.LastStdoutAt())
			if liveBackgroundTasks(sess) > 0 {
				// Running subagents emit periodic task_progress lines; a
				// stream this quiet means the CLI wedged. Give up rather
				// than wait forever.
				if quiet >= backgroundTaskStallTimeout {
					_ = sess.Stop()
					return waitForStatusResult{Status: agentStatusMissingMarker, Handoff: handoff}
				}
				continue
			}
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
			return waitForStatusResult{Status: agentStatusMissingMarker, Handoff: handoff}

		case <-doneCh:
			// Session exited — drain StatusCh for any pending status
			select {
			case status := <-sess.StatusCh():
				if result, done := handleStatus(status, true); done {
					return waitForStatusResult{Status: result, Handoff: handoff}
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
					return waitForStatusResult{Status: result, Handoff: handoff}
				}
				doneCh = nil
				continue
			}
			// Session exited without any status — treat as failure unless the
			// completion marker landed (same logical-success rule as
			// handleStatus). A well-behaved agent emits a result message
			// before exiting; absence of one means something went wrong.
			if isReady != nil && isReady() {
				return waitForStatusResult{Status: agentStatusSuccess, Handoff: handoff}
			}
			return waitForStatusResult{Status: agentStatusFailed, Handoff: handoff}

		case status := <-sess.StatusCh():
			if result, done := handleStatus(status, false); done {
				return waitForStatusResult{Status: result, Handoff: handoff}
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
// at the well-known path that the TUI log viewer reads.
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
// TUI dashboard can reflect that the review gate is currently running.
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
// the session's Done() channel fires before bubbletea has unwound and main.go
// has called sm.Shutdown(). A short wait here absorbs that race; anything
// longer would delay real agent failures (API errors, auth expiry, etc.).
// 500ms is empirically enough for tea.Quit → main.go → sm.Shutdown() to land.
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
