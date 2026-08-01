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
	"fmt"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// PhaseImplementLoopResult is the unified phase-implement loop's outcome.
//
// FinalStatus values:
//   - "review_passed":    every phase-declared repo passed the loop's review
//     gate atomically (AtomicPhaseStamp wrote
//     Touched (staged for FR) for every PhaseRepos entry).
//   - "max_iterations":   safety-rail trip — hit cfg.MaxIterations without a
//     passing review for the whole phase. AtomicPhaseStamp wrote
//     failed (LastError set) for every PhaseRepos entry.
//   - "safety_rail":      no-progress / consecutive-failure rail tripped.
//   - "interrupted":      shutdown / feature stopped while running.
//   - "need_user_input":  harness verification requires a user decision;
//     NeedUserInputPath points to the harness-owned gate artifact at the
//     phase-iteration dir.
//   - "plan_revision_required": implementation found a phase-plan contract
//     defect that must be repaired before another implementation attempt.
//
// PhaseRepos is the deduplicated, sorted list of repo names declared by the
// phase plan's `**Repo:** <name>` Task tags (single-repo phases get
// `[repo[0].Name]`). It is the canonical "phase-declared subset" the
// AtomicPhaseStamp wrote to.
type PhaseImplementLoopResult struct {
	FinalStatus          string
	Iterations           int
	LastError            string
	PhaseRepos           []string
	NeedUserInputPath    string
	PlanRevisionFeedback string
}

// RunPhaseImplementLoop runs the unified phase-implement loop for the feature.
//
// Single Claude session per iteration. Cwd at the active run dir; --add-dir
// for every Feature.Repos worktree (and the active run). Per-repo Task
// sub-agents are dispatched by prompt scope, not cwd — the existing
// main-vs-sub split (≤3 files / ≤50 lines stays in main; everything else
// delegated) is preserved by the implement skill prompt.
//
// Phase atomicity: every phase-declared repo passes review or all fail
// together. AtomicPhaseStamp commits the outcome in one Modify write at the
// end of the loop.
//
// RETRY is phase-scoped. Harness-created verification gates live at the
// phase-iteration artifact dir; the orchestrator surfaces them via the
// feature-level pending-need-user-input pointer.
//
// Crash recovery: re-runs the interrupted unit (iteration N's implement, or
// iteration N's review) from scratch. Durable state on disk
// (progress.md, plan checkmarks, working tree, prior reviewer feedback) is
// the resume scaffolding. No --resume integration.
func RunPhaseImplementLoop(cfg OrchestratorConfig, sm ports.SessionManager) (*PhaseImplementLoopResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("phase implement loop: feature is nil")
	}
	if cfg.PlanPath == "" {
		return nil, fmt.Errorf("phase implement loop: plan path is empty")
	}

	// PhaseScope replaces LoadExecutionPlan + ParseExecutionOrder +
	// ValidateExecutionOrder. For single-repo features (no `**Repo:**` tags) the result's Repos
	// slice contains the lone repo; for multi-repo features it's the
	// deduplicated set of tagged repos. PhaseScope failures (no Tasks
	// section, malformed tags) fall back to every Feature.Repos entry so
	// placeholder plans during early-phase wiring still launch a session.
	scope, err := PhaseScope(cfg.Feature, cfg.PlanPath)
	if err != nil {
		return nil, fmt.Errorf("phase implement loop: scoping plan: %w", err)
	}

	phaseRepos := scope.Repos
	if len(phaseRepos) == 0 {
		// Empty / placeholder plan: every Feature.Repos entry is the
		// implementer's edit boundary by default. The implement skill
		// prompt enforces tag discipline at the agent level.
		phaseRepos = make([]string, 0, len(cfg.Feature.Repos))
		for _, r := range cfg.Feature.Repos {
			phaseRepos = append(phaseRepos, r.Name)
		}
	}
	if len(phaseRepos) == 0 {
		// Truly empty (no plan tasks AND no Feature.Repos) — nothing to do.
		return &PhaseImplementLoopResult{
			FinalStatus: finalStatusReviewPassed,
			PhaseRepos:  nil,
		}, nil
	}

	// Build the cross-repo workspace. Cwd at the active run dir, with
	// --add-dir for every Feature.Repos worktree (and the active run).
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := BuildWorkspace(cfg.Feature, stateDir)
	if err != nil {
		return nil, fmt.Errorf("phase implement loop: workspace setup: %w", err)
	}

	// Phase-iteration artifact dir is the phase-implement root with no
	// per-repo subdir under the unified schema. Cycle paths still set
	// CyclePrefix and continue to use resolveImplementArtifactDirForRepo;
	// those paths will migrate to their own unified loops in slices 3-7.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := resolveUnifiedPhaseImplementDir(cfg.Feature, runDir)

	// Resolve the implement loop fn so tests can inject a stub.
	runImpl := RunImplementationLoop
	if cfg.RunImplementFn != nil {
		runImpl = cfg.RunImplementFn
	}

	// Build the inner ImplementConfig. RepoName intentionally LEFT EMPTY:
	// under the unified flow the main agent owns every repo in the
	// workspace; per-repo Task fan-out is handled by the implement skill
	// prompt via the Task tool. The existing single-repo code path in
	// RunImplementationLoop is the degenerate case.
	implCfg := ImplementConfig{
		Feature:                    cfg.Feature,
		FeatureStore:               cfg.FeatureStore,
		WorkDir:                    workspace.Cwd,
		PlanPath:                   cfg.PlanPath,
		MaxIterations:              cfg.MaxIterations,
		MaxConsecFails:             cfg.MaxConsecFails,
		MaxConsecNoProgress:        cfg.MaxConsecNoProgress,
		ExitCriteria:               resolvePromptIntent(cfg.Feature).ExitCriteria,
		Model:                      cfg.Model,
		ReviewModel:                cfg.ReviewModel,
		ResolveSessionConfig:       cfg.ResolveSessionConfig,
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		RunDir:                     runDir,
		AdditionalDirs:             additionalDirsExcludingStateDir(workspace, stateDir),
		KBInfos:                    cfg.KBInfos,
		PhaseType:                  cfg.Feature.RoadmapPhaseType,
		RoadmapPath:                cfg.Feature.Artifacts["roadmap"],
		DesignArtifactPath:         cfg.Feature.DesignArtifactPath(),
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
		PermissionCache:            cfg.PermissionCache,
		CommandRunner:              cfg.CommandRunner,
		BuildSession:               cfg.BuildSession,
		AskingClause:               cfg.AskingClause,
		EffortLevel:                cfg.EffortLevel,
		EffectiveEffort:            cfg.EffectiveEffort,
		EffortSource:               cfg.EffortSource,
		ReviewEffectiveEffort:      cfg.ReviewEffectiveEffort,
		ReviewEffortSource:         cfg.ReviewEffortSource,
		SkillsDir:                  cfg.SkillsDir,
		GuidelinesDir:              cfg.GuidelinesDir,
		Observer:                   cfg.Observer,
		OnVerificationProgress:     cfg.OnVerificationProgress,
		// Per-iteration review stays on the same gating policy as the
		// per-repo path historically used: Medium/Large skip per-
		// iteration review and rely on Final Review for quality gating;
		// Moonshot runs the per-iteration gate.
		SkipIterationReview: cfg.Feature.EffectivePipeline().ShouldSkipIterationReview(),
	}

	// Mark mid-flight phase status at the feature level so observers and
	// the desktop app can surface "implementing" without per-repo lying.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "implementing")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	loopResult, runErr := runImpl(implCfg, sm)
	if runErr != nil {
		// Atomic failure stamp: every phase-declared repo transitions to
		// failed (LastError set) in one write. This preserves "phase atomicity":
		// no partial-phase shipment.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     phaseRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: runErr.Error(),
		})
		return &PhaseImplementLoopResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			PhaseRepos:  phaseRepos,
		}, runErr
	}

	switch loopResult.FinalStatus {
	case finalStatusReviewPassed:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     phaseRepos,
			Outcome:   PhaseOutcomeReviewPassed,
		})
		return &PhaseImplementLoopResult{
			FinalStatus: finalStatusReviewPassed,
			Iterations:  loopResult.Iterations,
			PhaseRepos:  phaseRepos,
		}, nil

	case "need_user_input":
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     phaseRepos,
			Outcome:   PhaseOutcomeNeedUserInput,
			GatePath:  loopResult.NeedUserInputPath,
		})
		return &PhaseImplementLoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        loopResult.Iterations,
			LastError:         loopResult.LastError,
			NeedUserInputPath: loopResult.NeedUserInputPath,
			PhaseRepos:        phaseRepos,
		}, nil

	case "plan_revision_required":
		return &PhaseImplementLoopResult{
			FinalStatus:          "plan_revision_required",
			Iterations:           loopResult.Iterations,
			PhaseRepos:           phaseRepos,
			PlanRevisionFeedback: loopResult.PlanRevisionFeedback,
		}, nil

	case "interrupted":
		// Crash recovery re-runs from scratch — no atomic stamp on
		// interrupt; the persisted state stays at whatever the prior
		// iterations committed (typically untouched).
		return &PhaseImplementLoopResult{
			FinalStatus: "interrupted",
			Iterations:  loopResult.Iterations,
			PhaseRepos:  phaseRepos,
		}, nil

	default:
		// max_iterations / safety_rail / failed all map to a phase failure.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     phaseRepos,
			Outcome:   PhaseOutcomeFailed,
			LastError: loopResult.LastError,
		})
		return &PhaseImplementLoopResult{
			FinalStatus: loopResult.FinalStatus,
			Iterations:  loopResult.Iterations,
			LastError:   loopResult.LastError,
			PhaseRepos:  phaseRepos,
		}, nil
	}
}

// resolveUnifiedPhaseImplementDir returns the unified-flow phase-iteration
// artifact dir: one progress.md, one verification-report.yaml, one
// review-feedback.md per iteration at this dir level (no per-repo subdir).
//
// For roadmap-phase features the dir is `runs/run-NNN/phase-NN/implement/`.
// For non-roadmap features it's `runs/run-NNN/implement/`.
func resolveUnifiedPhaseImplementDir(f *feature.Feature, runDir string) string {
	base := runDir
	if f.CurrentRoadmapPhase > 0 {
		return filepath.Join(base, fmt.Sprintf("phase-%02d", f.CurrentRoadmapPhase), "implement")
	}
	return filepath.Join(base, "implement")
}

// additionalDirsExcludingStateDir filters out the workspace cwd entry that
// BuildWorkspace puts at AdditionalDirs[0]. The active run is passed separately
// through ImplementConfig.RunDir; without this filter the agent would see it
// twice in --add-dir. The legacy stateDir comparison keeps manually constructed
// WorkspaceSetup values compatible.
func additionalDirsExcludingStateDir(ws WorkspaceSetup, stateDir string) []string {
	out := make([]string, 0, len(ws.AdditionalDirs))
	abs, _ := filepath.Abs(stateDir)
	for _, d := range ws.AdditionalDirs {
		if d == ws.Cwd || d == abs {
			continue
		}
		out = append(out, d)
	}
	return out
}

// setCurrentPhaseStatus writes the mid-flight phase status onto the feature.
// Used by the unified loop to surface "implementing" / "" without lying about
// per-repo state (which is now durable end-of-phase only).
func setCurrentPhaseStatus(store ports.FeatureStore, featureID, status string) {
	if store == nil {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.CurrentPhaseStatus = status
		return nil
	})
}
