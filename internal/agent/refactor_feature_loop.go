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

// Package agent — refactor_feature_loop.go is the unified feature-level
// refactor cycle loop under SchemaVersionCurrent = 4. The legacy per-repo
// refactor entry (RunRefactorLoop sequencing inquire → research → design
// → roadmap → per-phase plans → implement, scoped to a single repo) is
// replaced by RunRefactorFeatureLoop: a single planned cycle that runs the
// refactor-plan step once and then drives the iterative implement loop with
// `--add-dir` for every Feature.Repos worktree, supporting cross-repo Tasks
// natively.
//
// Topology:
//
//   - Triggered by user prompt; the orchestrator stamps RefactorPrompt and
//     RefactorCount, then dispatches the loop.
//   - Planned: TestingContractCompiler runs in normal (planned) mode emitting
//     baseline + plan-source items + cross-repo items, every item tagged
//     with `repo:`.
//   - One Claude session per iteration with `--add-dir` for every
//     Feature.Repos worktree (and the state dir). Cross-repo edits are
//     first-class — a Task tagged `**Repo:** repo-a` and another tagged
//     `**Repo:** repo-b` are dispatched in the same iteration.
//   - Refactor-plan step runs once at loop entry, producing a phase-plan-style
//     markdown that may carry per-Task `**Repo:** <name>` tags. PhaseScope
//     parses the tags to determine the staged repo subset.
//   - On success: AtomicPhaseStamp transitions every staged repo's RepoImpl
//     entry to Touched (staged for FR); ActiveCycle clears.
//   - Crash recovery: re-runs the interrupted iteration from scratch with a
//     fresh session. Durable state (progress.md, plan checkmarks, working
//     tree, prior reviewer feedback, refactor-plan.md) is the resume
//     scaffolding.
package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// RefactorFeatureLoopConfig holds configuration for the unified refactor
// cycle loop. Mirrors RebaseLoopConfig / ReviewCommentsLoopConfig in shape
// but adds refactor-specific inputs: the user-supplied refactor prompt and
// the optional refactor-plan-fn test seam.
type RefactorFeatureLoopConfig struct {
	Feature      *feature.Feature
	FeatureStore ports.FeatureStore
	StateDir     string

	// Prompt is the user-supplied refactor prompt that the refactor-plan
	// step turns into a phase-plan-style markdown document. Empty means the
	// orchestrator did not stash one — the loop falls back to
	// Feature.RefactorPrompt.
	Prompt string

	Model               string
	ReviewModel         string
	PlanningModel       string // model for the refactor-plan step (defaults to Model when empty)
	MaxIterations       int
	MaxConsecFails      int
	MaxConsecNoProgress int

	KBInfos []KBInfo

	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	BuildSession               func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)
	AskingClause               string
	AskingClauseForModel       func(model string) string
	EffortLevel                llm.EffortLevel
	SkillsDir                  string
	GuidelinesDir              string
	Observer                   *observe.Observer

	// RunImplementFn is a test seam: when non-nil, RunRefactorFeatureLoop
	// calls this instead of RunImplementationLoop so unit tests can drive
	// the outer state-machine without launching a real Claude session.
	RunImplementFn func(ImplementConfig, ports.SessionManager) (*LoopResult, error)

	// RunRefactorPlanFn is a test seam: when non-nil, RunRefactorFeatureLoop
	// calls this instead of the production refactor-plan step. The function
	// returns the absolute path to the produced refactor-plan.md and any
	// error. Tests use this to inject a synthetic plan with the repo tags
	// they want to exercise without launching a real Claude session.
	RunRefactorPlanFn func(stagedDir string) (string, error)
}

// RefactorFeatureLoopResult is the outcome of a unified refactor cycle.
//
// FinalStatus values:
//   - "review_passed":    every plan-staged repo's edits passed review;
//     AtomicPhaseStamp transitioned every staged repo to
//     Touched (staged for FR).
//   - "max_iterations":   hit MaxIterations without a passing review.
//     AtomicPhaseStamp wrote failed (LastError set).
//   - "safety_rail":      consecutive-failure / no-progress rail tripped.
//   - "interrupted":      shutdown / feature stopped mid-loop. No atomic
//     stamp; persisted state preserved for restart.
//   - "need_user_input":  iteration emitted NEED_USER_INPUT — feature-level
//     pause gate; NeedUserInputPath points to the persisted gate artifact.
//   - "failed":           dispatch error before iteration began (or the
//     refactor-plan step failed).
//   - "protocol_violation": the refactor-plan step repeatedly violated its
//     completion contract.
//
// Repos is the deduplicated, sorted list of repo names declared by the
// refactor-plan's `**Repo:** <name>` tags — the canonical staged subset
// the AtomicPhaseStamp wrote to.
type RefactorFeatureLoopResult struct {
	FinalStatus       string
	Iterations        int
	LastError         string
	Repos             []string
	NeedUserInputPath string
	// ArtifactDir is the flat cycle artifact dir
	// (`runs/run-N/refactor-N/`). The refactor-plan, testing-contract, and
	// per-iteration dirs all live underneath.
	ArtifactDir string
}

// RunRefactorFeatureLoop drives the unified refactor cycle. Cwd at the
// feature state dir; --add-dir mounts every Feature.Repos worktree (and
// the state dir). The agent reads the refactor-plan, dispatches per-repo
// Task fan-out for the edits (cross-repo Tasks are first-class), runs
// build/test/lint against each touched repo, commits, and emits the
// standard handoff.
//
// The loop sets `Feature.ActiveCycle = {Type: refactor, Status: running}`
// at entry and clears it on success. RefactorCount is incremented per
// invocation so the artifact dir layout is
// `runs/run-N/refactor-N/iteration-NN/` with no per-repo subdir.
func RunRefactorFeatureLoop(cfg RefactorFeatureLoopConfig, sm ports.SessionManager) (*RefactorFeatureLoopResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("refactor feature loop: feature is nil")
	}
	if cfg.FeatureStore == nil {
		return nil, fmt.Errorf("refactor feature loop: feature store is nil")
	}
	if len(cfg.Feature.Repos) == 0 {
		return nil, fmt.Errorf("refactor feature loop: feature has no repos")
	}

	// Resolve the prompt. Prefer the explicit config; fall back to the
	// feature-level RefactorPrompt the orchestrator stashed before the
	// loop launched.
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = cfg.Feature.RefactorPrompt
	}
	if prompt == "" {
		return nil, fmt.Errorf("refactor feature loop: prompt is empty")
	}

	// Build the cross-repo workspace. Cwd at the feature state dir, with
	// --add-dir for every Feature.Repos worktree (refactors are cross-repo
	// by design — even when the plan ends up scoped to one repo, the agent
	// needs full visibility to judge cross-repo impact).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("refactor feature loop: workspace setup: %w", err)
	}

	// Stamp ActiveCycle = {Type: refactor, Status: running} so the TUI
	// can render the active cycle row. Persist the prompt too so a
	// crashed/interrupted run can be resumed by simply re-launching
	// against the same feature record.
	//
	// RefactorCount is NOT incremented here under the unified flow: the
	// orchestrator's startFeatureRefactor entry bumped it synchronously
	// (so the TUI sees the new count when its dispatch returns). The
	// loop adopts the on-disk count as its working count. When invoked
	// without an orchestrator pre-bump (e.g. unit tests), the count
	// starts at 0 and we bump it here so the loop still produces a
	// stable refactor-N artifact dir.
	refactorCount := 0
	if err := cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		if f.RefactorCount() == 0 {
			f.SetRefactorCount(1)
		}
		if f.RefactorPrompt == "" {
			f.RefactorPrompt = prompt
		}
		f.SetActiveCycleType(feature.CycleRefactor)
		f.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRefactor,
			Status: feature.RepoCycleRunning,
			Count:  f.RefactorCount(),
		}
		refactorCount = f.RefactorCount()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("refactor feature loop: stamping cycle entry: %w", err)
	}

	// Reload so subsequent reads see the new count and ActiveCycle.
	if loaded, lerr := cfg.FeatureStore.Load(cfg.Feature.ID); lerr == nil && loaded != nil {
		cfg.Feature = loaded
	}

	// Flat artifact layout: runs/run-N/refactor-N/iteration-NN/ — no
	// per-repo subdir under the unified flow.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, fmt.Sprintf("refactor-%d", refactorCount))
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("refactor feature loop: mkdir %s: %w", artifactDir, err)
	}

	// Mid-flight phase status surfaced to the TUI (mirrors phase-implement
	// / FR / rebase / review-comments loop conventions). Cleared via defer
	// so a panic still resets it.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "refactor-planning")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	// Step 1 — refactor-plan. Produces a phase-plan-style markdown with
	// per-Task `**Repo:** <name>` tags. Tests inject RunRefactorPlanFn.
	planRunner := cfg.RunRefactorPlanFn
	if planRunner == nil {
		planRunner = func(stagedDir string) (string, error) {
			return runRefactorPlanStep(refactorPlanStepInput{
				Feature:                    cfg.Feature,
				FeatureStore:               cfg.FeatureStore,
				StateDir:                   cfg.StateDir,
				ArtifactDir:                stagedDir,
				Workspace:                  workspace,
				Prompt:                     prompt,
				PlanningModel:              cfg.PlanningModel,
				ImplementModel:             cfg.Model,
				DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
				PermissionCache:            cfg.PermissionCache,
				BuildSession:               cfg.BuildSession,
				AskingClauseForModel:       cfg.AskingClauseForModel,
				EffortLevel:                cfg.EffortLevel,
				KBInfos:                    cfg.KBInfos,
				SkillsDir:                  cfg.SkillsDir,
				GuidelinesDir:              cfg.GuidelinesDir,
			}, sm)
		}
	}

	maxPlanFailures := cfg.MaxConsecFails
	if maxPlanFailures <= 0 {
		maxPlanFailures = 3
	}

	var planPath string
	for attempt := 1; attempt <= maxPlanFailures; attempt++ {
		if err := clearRefactorPlanAttemptArtifacts(artifactDir); err != nil {
			_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, err.Error())
			return &RefactorFeatureLoopResult{
				FinalStatus: "failed",
				LastError:   err.Error(),
				ArtifactDir: artifactDir,
			}, fmt.Errorf("refactor feature loop: prepare plan attempt: %w", err)
		}
		candidate, planErr := planRunner(artifactDir)
		if planErr != nil {
			if isProtocolViolationError(planErr) {
				errMsg := planErr.Error()
				if attempt >= maxPlanFailures {
					_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, errMsg)
					return &RefactorFeatureLoopResult{
						FinalStatus: "protocol_violation",
						LastError:   errMsg,
						ArtifactDir: artifactDir,
					}, nil
				}
				continue
			}
			_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, planErr.Error())
			return &RefactorFeatureLoopResult{
				FinalStatus: "failed",
				LastError:   planErr.Error(),
				ArtifactDir: artifactDir,
			}, fmt.Errorf("refactor feature loop: plan step: %w", planErr)
		}
		planPath = candidate
		break
	}
	if planPath == "" {
		// Defensive: planner returned an empty path with no error. Treat
		// as a dispatch failure so the cycle surfaces the issue rather
		// than running an iteration with no plan.
		errMsg := "refactor feature loop: plan step returned empty path"
		_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, errMsg)
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   errMsg,
			ArtifactDir: artifactDir,
		}, fmt.Errorf("%s", errMsg)
	}

	// Step 2 — derive the staged repo subset from the produced plan via
	// PhaseScope. Empty Repos slice means "every Feature.Repos entry"
	// (placeholder plan / single-repo untagged).
	scope, err := PhaseScope(cfg.Feature, planPath)
	if err != nil {
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   err.Error(),
			ArtifactDir: artifactDir,
		}, fmt.Errorf("refactor feature loop: scoping plan: %w", err)
	}
	stagedRepos := scope.Repos
	if len(stagedRepos) == 0 {
		stagedRepos = make([]string, 0, len(cfg.Feature.Repos))
		for _, r := range cfg.Feature.Repos {
			stagedRepos = append(stagedRepos, r.Name)
		}
		sort.Strings(stagedRepos)
	}

	// Read plan text once for testing-contract compilation.
	planBytes, readErr := os.ReadFile(planPath)
	if readErr != nil {
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   readErr.Error(),
			ArtifactDir: artifactDir,
		}, fmt.Errorf("refactor feature loop: reading plan: %w", readErr)
	}

	// Step 3 — compile and persist the planned testing contract. Per-repo
	// baseline rows + plan-source rows tagged `repo:` for each staged repo,
	// plus any cross-repo verification rows extracted from the plan.
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:     stagedRepos,
		PlanText:  string(planBytes),
		PlanPath:  planPath,
		PhaseType: cfg.Feature.RoadmapPhaseType,
		PlanLess:  false,
	})
	if err := WriteTestingContract(contractPath, contract); err != nil {
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   err.Error(),
			ArtifactDir: artifactDir,
		}, fmt.Errorf("refactor feature loop: writing testing contract: %w", err)
	}

	// Step 4 — drive the iterative implement loop. The rest of the loop's
	// state-machine (handoff parser, review gate, retry/safety-rail,
	// crash recovery) is owned by RunImplementationLoop; the refactor loop
	// just translates outcomes into the cycle-level result.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "refactoring")

	runImpl := RunImplementationLoop
	if cfg.RunImplementFn != nil {
		runImpl = cfg.RunImplementFn
	}

	implCfg := ImplementConfig{
		Feature:                    cfg.Feature,
		FeatureStore:               cfg.FeatureStore,
		WorkDir:                    workspace.Cwd,
		PlanPath:                   planPath,
		MaxIterations:              cfg.MaxIterations,
		MaxConsecFails:             cfg.MaxConsecFails,
		MaxConsecNoProgress:        cfg.MaxConsecNoProgress,
		ExitCriteria:               refactorExitCriteria(cfg.Feature),
		Model:                      cfg.Model,
		ReviewModel:                cfg.ReviewModel,
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		AdditionalDirs:             additionalDirsExcludingStateDir(workspace, stateDir),
		KBInfos:                    cfg.KBInfos,
		PhaseType:                  cfg.Feature.RoadmapPhaseType,
		DesignArtifactPath:     cfg.Feature.DesignArtifactPath(),
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
		PermissionCache:            cfg.PermissionCache,
		BuildSession:               cfg.BuildSession,
		AskingClause:               cfg.AskingClause,
		EffortLevel:                cfg.EffortLevel,
		SkillsDir:                  cfg.SkillsDir,
		GuidelinesDir:              cfg.GuidelinesDir,
		Observer:                   cfg.Observer,
		// Refactor cycles run the per-iteration review gate so the
		// reviewer can attest the cross-repo edits landed cleanly before
		// AtomicPhaseStamp marks the staged subset AwaitingFinalReview.
		// Skipping iteration review here would let half-done refactors
		// ship.
		SkipIterationReview: false,
	}

	loopResult, runErr := runImpl(implCfg, sm)

	// Translate the inner LoopResult into the outer RefactorFeatureLoopResult
	// and commit the cycle outcome.
	switch {
	case runErr != nil:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: runErr.Error(),
		})
		_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, runErr.Error())
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			Repos:       stagedRepos,
			ArtifactDir: artifactDir,
		}, runErr
	}

	if loopResult == nil {
		errMsg := "refactor feature loop: nil result from implement loop"
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: errMsg,
		})
		_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, errMsg)
		return &RefactorFeatureLoopResult{
			FinalStatus: "failed",
			LastError:   errMsg,
			Repos:       stagedRepos,
			ArtifactDir: artifactDir,
		}, nil
	}

	switch loopResult.FinalStatus {
	case "review_passed":
		// Cycle success: every staged repo transitions to
		// Touched (staged for FR) atomically; ActiveCycle clears.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeReviewPassed,
		})
		_ = clearActiveCycle(cfg.FeatureStore, cfg.Feature.ID)
		return &RefactorFeatureLoopResult{
			FinalStatus: "review_passed",
			Iterations:  loopResult.Iterations,
			Repos:       stagedRepos,
			ArtifactDir: artifactDir,
		}, nil

	case "need_user_input":
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeNeedUserInput,
			GatePath:  loopResult.NeedUserInputPath,
		})
		// Leave ActiveCycle at "running" so the harness sees the gate
		// when surfacing the pause; resume drives back into this loop.
		return &RefactorFeatureLoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        loopResult.Iterations,
			LastError:         loopResult.LastError,
			NeedUserInputPath: loopResult.NeedUserInputPath,
			Repos:             stagedRepos,
			ArtifactDir:       artifactDir,
		}, nil

	case "interrupted":
		// No atomic stamp on interrupt — persisted state preserved so the
		// next start picks up the loop. ActiveCycle stays at "running" so
		// the harness can resume.
		return &RefactorFeatureLoopResult{
			FinalStatus: "interrupted",
			Iterations:  loopResult.Iterations,
			Repos:       stagedRepos,
			ArtifactDir: artifactDir,
		}, nil

	default:
		// max_iterations / safety_rail / failed all map to a cycle
		// failure: every staged repo transitions to failed (LastError set); the
		// feature stays at its prior cycle entry but ActiveCycle.Status
		// flips to "failed" so the TUI surfaces the cycle row in red.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     stagedRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: loopResult.LastError,
		})
		_ = markActiveCycleFailedRefactor(cfg.FeatureStore, cfg.Feature.ID, loopResult.LastError)
		return &RefactorFeatureLoopResult{
			FinalStatus: loopResult.FinalStatus,
			Iterations:  loopResult.Iterations,
			LastError:   loopResult.LastError,
			Repos:       stagedRepos,
			ArtifactDir: artifactDir,
		}, nil
	}
}

// refactorExitCriteria returns the refactor cycle's exit criteria. The
// agent reads this from the implement prompt's `## Exit Criteria` section
// to know when it can emit SUCCESS. Cycle-specific verification — every
// plan-staged Task addressed, build/test/lint passes on every touched repo,
// and any `cross-repo` verification commands all pass — lives here so the
// iteration loop's reviewer can attest it via the verification report.
func refactorExitCriteria(f *feature.Feature) string {
	base := "Every Task in the refactor plan is addressed (file edits committed " +
		"or explicitly dismissed in the plan), the build, test, and lint commands " +
		"pass on every touched repo, the per-repo baseline rows in the testing " +
		"contract are attested in `verification-report.yaml`, and any " +
		"`cross-repo` verification rows pass. No commit history beyond what " +
		"the plan declares plus follow-on import / wiring updates."
	if f != nil && f.ExitCriteria != "" {
		// Honor a feature-level override if the user/profile set one;
		// most refactor cycles will pick up the default above.
		return f.ExitCriteria
	}
	return base
}

// markActiveCycleFailedRefactor flips Feature.ActiveCycle.Status to
// "failed" so the TUI surfaces the cycle row in red. ActiveCycle stays
// populated so the user can see which cycle failed and trigger a restart.
func markActiveCycleFailedRefactor(store ports.FeatureStore, featureID, lastError string) error {
	if store == nil {
		return fmt.Errorf("mark active refactor cycle failed: store is nil")
	}
	return store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle == nil {
			f.ActiveCycle = &feature.CycleState{Type: feature.CycleRefactor}
		}
		f.ActiveCycle.Status = feature.RepoCycleFailed
		if lastError != "" {
			f.ActiveCycle.LastError = lastError
		}
		return nil
	})
}

// refactorPlanStepInput carries the inputs to runRefactorPlanStep. The
// shape is internal to refactor_feature_loop.go; the function is the
// production refactor-plan step factored out so RunRefactorPlanFn can be
// injected by tests without re-implementing the I/O plumbing.
type refactorPlanStepInput struct {
	Feature                    *feature.Feature
	FeatureStore               ports.FeatureStore
	StateDir                   string
	ArtifactDir                string
	Workspace                  WorkspaceSetup
	Prompt                     string
	PlanningModel              string
	ImplementModel             string // fallback when PlanningModel is empty
	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	BuildSession               func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)
	AskingClauseForModel       func(model string) string
	EffortLevel                llm.EffortLevel
	KBInfos                    []KBInfo
	SkillsDir                  string
	GuidelinesDir              string
}

// runRefactorPlanStep launches a single Claude session to author the
// refactor plan markdown and waits for `phase_complete`. The plan markdown
// is expected to follow the phase-plan format (per-Task `**Repo:** <name>`
// tags, `## Tasks` section, optional cross-repo verification block) so
// PhaseScope and CompileTestingContractMultiRepo can consume it directly.
//
// On success it returns the absolute path to refactor-plan.md inside
// ArtifactDir.
func runRefactorPlanStep(in refactorPlanStepInput, sm ports.SessionManager) (string, error) {
	if err := os.MkdirAll(in.ArtifactDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir refactor-plan dir: %w", err)
	}

	model := in.PlanningModel
	if model == "" {
		model = in.ImplementModel
	}

	prompt := buildRefactorPlanPrompt(in.Feature, in.Workspace, in.Prompt, in.SkillsDir, in.GuidelinesDir, in.KBInfos)

	asking := ""
	if in.AskingClauseForModel != nil {
		asking = in.AskingClauseForModel(model)
	}
	systemPrompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{
		Spec:          RefactorPlanRoleSpec(),
		IterationDir:  in.ArtifactDir,
		SkillsDir:     in.SkillsDir,
		GuidelinesDir: in.GuidelinesDir,
		KBInfos:       in.KBInfos,
		AskingClause:  asking,
	})

	// Clear stale completion and plan artifacts BEFORE spawning so every
	// validated plan belongs to this marker-bearing attempt.
	if err := clearRefactorPlanAttemptArtifacts(in.ArtifactDir); err != nil {
		return "", err
	}

	cmd, env, sessOpts, err := in.BuildSession(BuildSessionOpts{
		Model:                          model,
		Prompt:                         prompt,
		SystemPrompt:                   systemPrompt,
		AdditionalDirs:                 in.Workspace.AdditionalDirs,
		PIDDir:                         filepath.Join(in.StateDir, in.Feature.ID),
		PermHandler:                    permHandlerFor(in.DangerouslySkipPermissions, in.PermissionCache, ""),
		WorkDir:                        in.Workspace.Cwd,
		EffortLevel:                    in.EffortLevel,
		Phase:                          feature.PhasePlan,
		SystemPromptHasUsefulResources: true,
	})
	if err != nil {
		return "", fmt.Errorf("building refactor-plan session: %w", err)
	}
	sessOpts = enableTruncatedTurnAutoResume(sessOpts)
	WriteDebugPrompts(in.ArtifactDir, sessOpts.DebugSystemPrompt, prompt)

	sessID := fmt.Sprintf("%s-refactor-plan", in.Feature.ID)
	sess, err := sm.StartSession(sessID, in.Feature.ID, feature.PhasePlan, cmd, in.Workspace.Cwd, env, sessOpts)
	if err != nil {
		return "", fmt.Errorf("starting refactor-plan session: %w", err)
	}

	logPath := filepath.Join(in.ArtifactDir, "output.txt")
	if logFile, ferr := os.Create(logPath); ferr == nil {
		sess.SetLogFile(logFile)
	}

	agentStatus := waitForStatus(sess, sm, sessID, func() bool {
		if HasPhaseComplete(in.ArtifactDir) {
			sess.SetHasUnansweredQuestion(false)
			return true
		}
		return false
	})

	output := sess.MessageLog().Text()
	_ = os.WriteFile(logPath, []byte(output), 0o644)

	if agentStatus == agentStatusMissingMarker {
		return "", newProtocolViolationError(RoleRefactorPlanStep, in.ArtifactDir, []ProtocolViolation{{
			Artifact: PhaseCompleteFile,
			Reason:   "SDK reported success but phase_complete was not present",
		}})
	}
	if agentStatus != agentStatusSuccess {
		return "", fmt.Errorf("refactor-plan session did not complete successfully (status: %s)", agentStatus)
	}

	outcome, violations, validateErr := Validate(feature.PhasePlan, RoleRefactorPlanStep, in.ArtifactDir)
	if validateErr != nil {
		return "", fmt.Errorf("validating refactor plan step contract: %w", validateErr)
	}
	if !outcome.OK {
		return "", newProtocolViolationError(RoleRefactorPlanStep, in.ArtifactDir, violations)
	}
	if outcome.PlanMarkdownPath == "" {
		return "", fmt.Errorf("validating refactor plan step contract: validated without plan path")
	}
	return outcome.PlanMarkdownPath, nil
}

func clearRefactorPlanAttemptArtifacts(artifactDir string) error {
	if err := os.Remove(filepath.Join(artifactDir, PhaseCompleteFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale %s: %w", PhaseCompleteFile, err)
	}

	matches, err := filepath.Glob(filepath.Join(artifactDir, "*.md"))
	if err != nil {
		return fmt.Errorf("glob refactor-plan artifacts: %w", err)
	}
	for _, match := range matches {
		if IsArtifactExcluded(filepath.Base(match)) {
			continue
		}
		if err := os.Remove(match); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale refactor-plan artifact %s: %w", match, err)
		}
	}
	return nil
}

// buildRefactorPlanPrompt composes the user prompt for the refactor-plan
// step. The prompt carries only invocation arguments; output format and
// planning-only rules live in skills/refactor/SKILL.md.
func buildRefactorPlanPrompt(f *feature.Feature, ws WorkspaceSetup, refactorPrompt, skillsDir, guidelinesDir string, _ []KBInfo) string {
	names := make([]string, 0, len(ws.RepoPaths))
	for name := range ws.RepoPaths {
		names = append(names, name)
	}
	sort.Strings(names)

	repos := make([]prompts.RepoView, 0, len(names))
	for _, name := range names {
		repos = append(repos, prompts.RepoView{Name: name, Path: ws.RepoPaths[name]})
	}

	context := ""
	if f != nil {
		context = f.EffectiveDescription()
	}
	return roles.BuildRefactorPlanPrompt(roles.RefactorPlanUserInput{
		Request:        refactorPrompt,
		FeatureContext: context,
		Repos:          repos,
	})
}
