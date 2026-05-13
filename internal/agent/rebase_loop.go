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

// Package agent — rebase_loop.go is the unified feature-level rebase cycle
// loop under SchemaVersionCurrent = 4. The legacy per-repo rebase entry
// (Orchestrator.StartRebase + per-repo StartRepoCycleImplement(CycleRebase))
// is replaced by RunRebaseLoop: a single iterative loop that rebases every
// `Feature.Repos` branch behind master in one Claude session.
//
// Topology:
//
//   - Triggered when any `Feature.Repos` branch is behind its base branch.
//     Participating repos = the behind subset of `Feature.Repos`; the loop
//     mounts only those worktrees via WorkspaceForRepos.
//   - Plan-less: TestingContractCompiler runs in plan-less mode (only
//     baseline + rebase-specific items, no plan-source items).
//   - Cycle-specific verification — post-rebase `git status` cleanliness and
//     a rebuild — is expressed in the loop body via the per-repo baseline
//     contract rows; `clean working tree` and `no rebase in progress`
//     verification items live in the rebase plan markdown so the agent
//     attests them through the standard verification report.
//   - One Claude session per iteration with `--add-dir` for every
//     behind-subset repo (and the state dir).
//   - Iteration loop reuses RunImplementationLoop for the iteration
//     mechanics (handoff parser, review-feedback walker, retry/safety-rail,
//     crash recovery), with cycle-specific divergence (plan-less compiler,
//     ActiveCycle stamping, RebaseCount increment, flat artifact dirs)
//     expressed inline so the kernel stays unparameterized.
//   - On success: AtomicPhaseStamp transitions every behind-subset repo's
//     RepoImpl entry; ActiveCycle clears.
//   - On failure: surface conflict via LastError, leave for human
//     resolution (no atomic stamp; the feature stays at its prior cycle
//     state).
//   - Crash recovery: re-runs the interrupted iteration from scratch with a
//     fresh session. Durable state (progress.md, plan checkmarks, working
//     tree) is the resume scaffolding.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// RebaseLoopConfig holds configuration for the unified rebase cycle loop.
// Mirrors OrchestratorConfig in shape but adds rebase-specific inputs:
// the behind-subset of repos with their resolved rebase targets.
type RebaseLoopConfig struct {
	Feature      *feature.Feature
	FeatureStore ports.FeatureStore
	StateDir     string

	// BehindRepos is the resolved subset of Feature.Repos whose feature
	// branch is behind its base. Only these repos are mounted in the
	// session workspace and stamped on cycle outcome. The orchestrator
	// computes this via the Rebaser port before invoking the loop.
	//
	// Each entry carries the rebase target ref (e.g. "master") so the
	// rebase plan markdown can name it accurately. ConflictFiles, when
	// non-empty, signals the rebase is mid-flight (a previous attempt
	// stopped at a conflict and the worktree still has unresolved
	// markers); the rebase plan switches to the
	// "rebase-already-in-progress" template in that case.
	BehindRepos []RebaseRepoTarget

	Model               string
	ReviewModel         string
	MaxIterations       int
	MaxConsecFails      int
	MaxConsecNoProgress int

	KBInfos []KBInfo

	DangerouslySkipPermissions bool
	PermissionCache            *permission.Cache
	BuildSession               func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error)
	AskingClause               string
	EffortLevel                llm.EffortLevel
	SkillsDir                  string
	GuidelinesDir              string
	Observer                   *observe.Observer

	// RunImplementFn is a test seam: when non-nil, RunRebaseLoop calls
	// this instead of RunImplementationLoop so unit tests can drive the
	// outer loop's state-machine transitions without launching a real
	// Claude session.
	RunImplementFn func(ImplementConfig, ports.SessionManager) (*LoopResult, error)
}

// RebaseRepoTarget describes one behind-subset repo entering the rebase
// cycle. RebaseTarget is the base ref the agent will rebase onto (e.g.
// "master" or "main"); ConflictFiles is non-empty only when the worktree
// is already mid-rebase from a prior attempt.
type RebaseRepoTarget struct {
	RepoName      string
	RebaseTarget  string
	ConflictFiles []string
	PRURL         string
}

// RebaseLoopResult is the outcome of a unified rebase cycle.
//
// FinalStatus values:
//   - "review_passed":    every behind-subset repo's rebase succeeded; the
//     loop's review gate APPROVED the implementation. AtomicPhaseStamp
//     transitioned every repo to Touched (staged for FR).
//   - "max_iterations":   hit MaxIterations without a passing review.
//     AtomicPhaseStamp wrote failed (LastError set) (conflict surfaced for human).
//   - "safety_rail":      consecutive-failure / no-progress rail tripped.
//     AtomicPhaseStamp wrote failed (LastError set).
//   - "interrupted":      shutdown / feature stopped mid-loop. No atomic
//     stamp; persisted state preserved for restart.
//   - "no_op":            no behind repos at loop entry; nothing to do.
//   - "failed":           dispatch error before iteration began.
//
// Repos is the deduplicated, sorted list of behind-subset repo names — the
// canonical "rebase-staged subset" the AtomicPhaseStamp wrote to.
type RebaseLoopResult struct {
	FinalStatus string
	Iterations  int
	LastError   string
	Repos       []string
}

// RunRebaseLoop drives the unified rebase cycle. Cwd at the feature state
// dir; --add-dir mounts every behind-subset Feature.Repos worktree (and
// the state dir). The agent does the actual `git fetch`/`git rebase`/
// conflict resolution/`git push --force` inside its session — the loop
// owns iteration mechanics, plan-less contract compilation, ActiveCycle
// state, RebaseCount increment, and atomic outcome stamping.
//
// The loop sets `Feature.ActiveCycle = {Type: rebase, Status: running}` at
// entry (via FeatureStore.Modify) and clears it on success. RebaseCount is
// incremented per invocation so the artifact dir layout is
// `runs/run-N/rebase-N/iteration-NN/` with no per-repo subdir.
func RunRebaseLoop(cfg RebaseLoopConfig, sm ports.SessionManager) (*RebaseLoopResult, error) {
	if cfg.Feature == nil {
		return nil, fmt.Errorf("rebase loop: feature is nil")
	}
	if cfg.FeatureStore == nil {
		return nil, fmt.Errorf("rebase loop: feature store is nil")
	}

	// Degenerate "nothing behind" case — the orchestrator detected at least
	// one behind repo before invoking, but a stale read can race. Treat as
	// a no-op so the orchestrator can short-circuit without surfacing an
	// error.
	if len(cfg.BehindRepos) == 0 {
		return &RebaseLoopResult{FinalStatus: "no_op"}, nil
	}

	// Sorted, deduplicated repo names form the canonical staged subset.
	repoNames := make([]string, 0, len(cfg.BehindRepos))
	seen := map[string]bool{}
	for _, t := range cfg.BehindRepos {
		if t.RepoName == "" || seen[t.RepoName] {
			continue
		}
		seen[t.RepoName] = true
		repoNames = append(repoNames, t.RepoName)
	}
	sort.Strings(repoNames)

	// Build the cycle workspace. State dir is per-feature; the workspace
	// filter is the behind subset only — every Feature.Repos worktree
	// outside that subset is intentionally NOT mounted, both to keep the
	// agent's edit boundary tight and to make the AtomicPhaseStamp
	// staged-subset unambiguous.
	stateDir := filepath.Join(cfg.StateDir, cfg.Feature.ID)
	workspace, err := WorkspaceForRepos(cfg.Feature, stateDir, repoNames)
	if err != nil {
		return nil, fmt.Errorf("rebase loop: workspace setup: %w", err)
	}

	// Increment RebaseCount and set ActiveCycle = {Type: rebase, Status:
	// running} at loop entry. Both are persisted in the same Modify so a
	// crash between the two (e.g. another goroutine reads partial state)
	// cannot observe one without the other.
	rebaseCount := 0
	if err := cfg.FeatureStore.Modify(cfg.Feature.ID, func(f *feature.Feature) error {
		f.SetRebaseCount(f.RebaseCount() + 1)
		f.SetActiveCycleType(feature.CycleRebase)
		f.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  f.RebaseCount(),
		}
		rebaseCount = f.RebaseCount()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("rebase loop: stamping cycle entry: %w", err)
	}

	// Reload so subsequent reads see the new RebaseCount and ActiveCycle.
	if loaded, lerr := cfg.FeatureStore.Load(cfg.Feature.ID); lerr == nil && loaded != nil {
		cfg.Feature = loaded
	}

	// Flat artifact layout: runs/run-N/rebase-N/iteration-NN/ — no per-repo subdir.
	runDir := ActiveRunDir(cfg.StateDir, cfg.Feature)
	artifactDir := filepath.Join(runDir, fmt.Sprintf("rebase-%d", rebaseCount))
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("rebase loop: mkdir %s: %w", artifactDir, err)
	}

	// Compile and persist the plan-less testing contract once at loop
	// entry. Per-repo baseline rows (build/test/lint) are emitted; no
	// plan-source items. The contract is the binding the implement loop
	// reads to seed each iteration's verification-report.yaml.
	contractPath := filepath.Join(artifactDir, "testing-contract.yaml")
	if err := writeRebaseTestingContract(contractPath, repoNames, cfg.Feature); err != nil {
		return nil, fmt.Errorf("rebase loop: testing contract: %w", err)
	}

	// Author the rebase plan markdown. The implementer reads this as the
	// "plan" input even though the cycle is plan-less in the contract
	// sense — it carries the per-repo rebase target, conflict status, and
	// step-by-step git commands the agent must run.
	planPath := filepath.Join(artifactDir, "rebase-plan.md")
	if err := os.WriteFile(planPath, []byte(BuildMultiRepoRebasePlan(cfg.BehindRepos)), 0o644); err != nil {
		return nil, fmt.Errorf("rebase loop: writing rebase plan: %w", err)
	}

	// Mid-flight phase status surfaced to the TUI (mirrors phase-implement /
	// FR loop conventions). Cleared via defer so a panic still resets it.
	setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "rebasing")
	defer setCurrentPhaseStatus(cfg.FeatureStore, cfg.Feature.ID, "")

	runImpl := RunImplementationLoop
	if cfg.RunImplementFn != nil {
		runImpl = cfg.RunImplementFn
	}

	// Build the inner ImplementConfig. RepoName intentionally LEFT EMPTY:
	// under the unified flow the main agent owns every behind-subset repo
	// in the workspace; per-repo Task fan-out is handled by the rebase
	// skill prompt.
	implCfg := ImplementConfig{
		Feature:                    cfg.Feature,
		FeatureStore:               cfg.FeatureStore,
		WorkDir:                    workspace.Cwd,
		PlanPath:                   planPath,
		MaxIterations:              cfg.MaxIterations,
		MaxConsecFails:             cfg.MaxConsecFails,
		MaxConsecNoProgress:        cfg.MaxConsecNoProgress,
		ExitCriteria:               rebaseExitCriteria(cfg.Feature),
		Model:                      cfg.Model,
		ReviewModel:                cfg.ReviewModel,
		ArtifactDir:                artifactDir,
		StateDir:                   stateDir,
		AdditionalDirs:             additionalDirsExcludingStateDir(workspace, stateDir),
		KBInfos:                    cfg.KBInfos,
		BrainstormArtifactPath:     cfg.Feature.Artifacts["brainstorm"],
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
		PermissionCache:            cfg.PermissionCache,
		BuildSession:               cfg.BuildSession,
		AskingClause:               cfg.AskingClause,
		EffortLevel:                cfg.EffortLevel,
		SkillsDir:                  cfg.SkillsDir,
		GuidelinesDir:              cfg.GuidelinesDir,
		Observer:                   cfg.Observer,
		// Rebase cycles always run the per-iteration review gate so the
		// reviewer can attest the rebase landed cleanly (no conflict
		// markers, branches up-to-date) before we stamp the staged subset
		// AwaitingFinalReview. Skipping iteration review here would let a
		// half-resolved rebase ship.
		SkipIterationReview: false,
	}

	loopResult, runErr := runImpl(implCfg, sm)

	// Translate the inner LoopResult into the outer RebaseLoopResult and
	// commit the cycle outcome.
	switch {
	case runErr != nil:
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: runErr.Error(),
		})
		_ = markActiveCycleFailed(cfg.FeatureStore, cfg.Feature.ID, runErr.Error())
		return &RebaseLoopResult{
			FinalStatus: "failed",
			LastError:   runErr.Error(),
			Repos:       repoNames,
		}, runErr
	}

	if loopResult == nil {
		// Defensive: a nil result with nil error should not happen, but if
		// the inner loop returns one, treat it as a generic dispatch
		// failure rather than panicking.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: "rebase loop: nil result from implement loop",
		})
		_ = markActiveCycleFailed(cfg.FeatureStore, cfg.Feature.ID, "nil result from implement loop")
		return &RebaseLoopResult{
			FinalStatus: "failed",
			LastError:   "nil result from implement loop",
			Repos:       repoNames,
		}, nil
	}

	switch loopResult.FinalStatus {
	case "review_passed":
		// Cycle success: every behind-subset repo transitions to
		// Touched (staged for FR) atomically; ActiveCycle clears.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeReviewPassed,
		})
		_ = clearActiveCycle(cfg.FeatureStore, cfg.Feature.ID)
		return &RebaseLoopResult{
			FinalStatus: "review_passed",
			Iterations:  loopResult.Iterations,
			Repos:       repoNames,
		}, nil

	case "interrupted":
		// No atomic stamp on interrupt — persisted state preserved so the
		// next start picks up the loop. ActiveCycle stays at "running" so
		// the harness can resume.
		return &RebaseLoopResult{
			FinalStatus: "interrupted",
			Iterations:  loopResult.Iterations,
			Repos:       repoNames,
		}, nil

	default:
		// max_iterations / safety_rail / failed all map to a cycle failure:
		// surface conflict (LastError carries it) and leave for human
		// resolution. Every staged repo transitions to failed (LastError set); the
		// feature stays at its prior cycle entry but ActiveCycle.Status
		// flips to "failed" so the TUI surfaces the rebase row in red.
		_ = AtomicPhaseStamp(cfg.FeatureStore, AtomicPhaseStampInput{
			FeatureID: cfg.Feature.ID,
			Repos:     repoNames,
			Outcome:   PhaseOutcomeFailed,
			LastError: loopResult.LastError,
		})
		_ = markActiveCycleFailed(cfg.FeatureStore, cfg.Feature.ID, loopResult.LastError)
		return &RebaseLoopResult{
			FinalStatus: loopResult.FinalStatus,
			Iterations:  loopResult.Iterations,
			LastError:   loopResult.LastError,
			Repos:       repoNames,
		}, nil
	}
}

// writeRebaseTestingContract compiles and persists the plan-less rebase
// testing contract: per-repo baseline rows tagged `repo: <name>`. No
// plan-source items — the rebase cycle has no phase plan to inherit from.
// The reviewer audits the verification report against these baseline rows
// to confirm post-rebase build/test/lint pass.
func writeRebaseTestingContract(path string, repos []string, _ *feature.Feature) error {
	contract := CompileTestingContractMultiRepo(MultiRepoContractInput{
		Repos:    repos,
		PlanLess: true,
		PlanPath: path,
	})
	if err := WriteTestingContract(path, contract); err != nil {
		return fmt.Errorf("writing rebase testing contract: %w", err)
	}
	return nil
}

// rebaseExitCriteria returns the rebase cycle's exit criteria. The agent
// reads this from the implement prompt's `## Exit Criteria` section to
// know when it can emit SUCCESS. Cycle-specific verification (post-rebase
// `git status` clean, no rebase in progress, build passes) lives here so
// the iteration loop can enforce it via the reviewer's audit.
func rebaseExitCriteria(_ *feature.Feature) string {
	return "Every behind-subset repo's feature branch is rebased onto its base, " +
		"the worktree has no rebase in progress, no conflict markers remain, " +
		"the project's build, test, and lint commands pass, and the rebased " +
		"branch is force-pushed to the remote PR branch (when the feature is " +
		"publishable). No commit history beyond what was on the branch before " +
		"the rebase + the upstream commits being incorporated."
}

// BuildMultiRepoRebasePlan composes the rebase plan markdown for the
// behind-subset repos. For each repo it inlines a per-repo section
// derived from BuildRebasePlan; the agent sees one plan document but
// dispatches per-repo work via the rebase skill prompt.
func BuildMultiRepoRebasePlan(repos []RebaseRepoTarget) string {
	if len(repos) == 0 {
		return "# Rebase Cycle Plan\n\nNo behind repos detected. This file is empty.\n"
	}

	// Sort by repo name for deterministic output.
	sorted := append([]RebaseRepoTarget(nil), repos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RepoName < sorted[j].RepoName })

	out := "# Rebase Cycle Plan\n\n"
	out += "This rebase cycle covers the following repos. Each repo's branch is\n"
	out += "behind its base; rebase the branch, resolve any conflicts, run the\n"
	out += "project verification commands, and (when publishable) force-push the\n"
	out += "rebased branch to its remote PR branch.\n\n"
	out += "## Repos in this cycle\n\n"
	for _, r := range sorted {
		out += fmt.Sprintf("- `%s` — base `%s`", r.RepoName, r.RebaseTarget)
		if r.PRURL != "" {
			out += fmt.Sprintf(" — PR %s", r.PRURL)
		}
		if len(r.ConflictFiles) > 0 {
			out += " — rebase already in progress"
		}
		out += "\n"
	}
	out += "\n"

	for _, r := range sorted {
		out += fmt.Sprintf("---\n\n## Repo: `%s`\n\n", r.RepoName)
		out += "**Repo:** " + r.RepoName + "\n\n"
		// Reuse the per-repo template — the agent dispatches each section
		// via a Task sub-agent prompt-scoped to that repo's worktree.
		section := BuildRebasePlan(r.RebaseTarget, r.PRURL, r.ConflictFiles)
		// Strip the duplicate top-level title so the inlined section
		// reads as a sub-heading under the per-repo section.
		section = stripFirstHeading(section)
		out += section
		out += "\n"
	}
	return out
}

// stripFirstHeading drops the first H1 ("# ...") heading line from md.
// Used to inline BuildRebasePlan output under a higher-level heading
// without leaving a stray duplicate title.
func stripFirstHeading(md string) string {
	if len(md) == 0 || md[0] != '#' {
		return md
	}
	for i := 0; i < len(md); i++ {
		if md[i] == '\n' {
			return md[i+1:]
		}
	}
	return md
}

// clearActiveCycle clears Feature.ActiveCycle on cycle success. The
// AtomicPhaseStamp has already transitioned per-repo statuses; this just
// removes the feature-level cycle pointer.
func clearActiveCycle(store ports.FeatureStore, featureID string) error {
	if store == nil {
		return fmt.Errorf("clear active cycle: store is nil")
	}
	return store.Modify(featureID, func(f *feature.Feature) error {
		f.ActiveCycle = nil
		f.SetActiveCycleType("")
		return nil
	})
}

// markActiveCycleFailed flips Feature.ActiveCycle.Status to "failed" so
// the TUI surfaces the rebase row in red. ActiveCycle stays populated so
// the user can see which cycle failed and trigger a restart.
func markActiveCycleFailed(store ports.FeatureStore, featureID, lastError string) error {
	if store == nil {
		return fmt.Errorf("mark active cycle failed: store is nil")
	}
	return store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle == nil {
			f.ActiveCycle = &feature.CycleState{Type: feature.CycleRebase}
		}
		f.ActiveCycle.Status = feature.RepoCycleFailed
		if lastError != "" {
			f.ActiveCycle.LastError = lastError
		}
		return nil
	})
}
