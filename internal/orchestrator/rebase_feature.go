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

// Package orchestrator wires the feature-level rebase cycle. One call to
// startFeatureRebase enumerates every behind branch in Feature.Repos, launches
// agent.RunRebaseLoop, and translates the loop outcome into the per-repo
// CompleteRepoCycle / FailRepoCycle surface used by downstream listeners.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// startFeatureRebase launches the unified rebase cycle. Resolves the
// behind subset by inspecting every Feature.Repos branch via the Rebaser
// port, builds the agent.RebaseLoopConfig, and runs RunRebaseLoop in a
// background goroutine tracked by cycleWG. The hintRepoName +
// hintConflictFiles arguments come from the TUI's per-repo conflict
// callback (the legacy entry shape) and are folded into the matching
// behind-subset target so the rebase plan emits the
// "rebase-already-in-progress" template for the conflicted repo.
//
// hintRebaseTarget overrides the target ref for the hint repo when set
// (the TUI resolves it before dispatching). Other behind repos use
// resolveRebaseTarget for their target.
//
// Returns "" + nil on successful dispatch (no stable session ID; the
// inner loop owns per-iteration IDs). Returns ("", err) on dispatch
// failure (no behind repos, repo lookup error, etc.).
func (o *Orchestrator) startFeatureRebase(
	featureID, hintRepoName, hintRebaseTarget string,
	hintConflictFiles []string,
) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}
	if activeRepoCycleOfType(f, hintRepoName, feature.CycleRebase) {
		return "", nil
	}

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	// Enumerate the behind subset. The hint repo (from the TUI) is forced
	// in unconditionally — the TUI already detected it as behind via its
	// own preflight rebase attempt — and gets the hint conflict files.
	// Other Feature.Repos branches are tested via the Rebaser port.
	behind := o.resolveBehindSubset(f, hintRepoName, hintRebaseTarget, hintConflictFiles)
	if len(behind) == 0 {
		// Nothing to rebase. This can happen when the hint repo's branch
		// actually advanced between detection and dispatch (rare race).
		// Surface as a clean no-op via the legacy event channel so the
		// TUI clears its "starting rebase" status.
		return "", fmt.Errorf("rebase: no behind repos for feature %s", featureID)
	}

	// Open per-repo cycle entries for every behind repo so existing TUI
	// rendering paths (RepoCycles[name].Status == "running") light up while
	// the feature-level rebase loop runs.
	for _, t := range behind {
		if err := o.deps.Lifecycle.StartRepoCycle(featureID, t.RepoName, feature.CycleRebase); err != nil {
			freshF, getErr := o.deps.Lifecycle.Get(featureID)
			if getErr == nil && activeRepoCycleOfType(freshF, t.RepoName, feature.CycleRebase) {
				return "", nil
			}
			return "", fmt.Errorf("start rebase cycle for %s: %w", t.RepoName, err)
		}
	}
	// Reload the feature so RebaseCount and per-repo cycle Counts reflect
	// the StartRepoCycle writes.
	f, err = o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("reload feature: %w", err)
	}

	// Stage the rebase plan synchronously so callers (TUI, tests) can read
	// it immediately after this method returns. The async loop's plan
	// write below will overwrite with identical content — the operation
	// is idempotent.
	if err := o.stageRebasePlanArtifacts(f, behind); err != nil {
		return "", fmt.Errorf("stage rebase plan: %w", err)
	}

	cfg := agent.RebaseLoopConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		StateDir:                   o.stateDir(),
		BehindRepos:                behind,
		Model:                      f.Models.Implementation,
		ReviewModel:                f.Models.Review,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		KBInfos:                    o.computeKBInfos(f),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		Observer:                   pr.Observer,
	}

	sm := o.deps.Sessions
	o.cycleWG.Go(func() {
		runRebaseLoop := o.runRebaseLoopFn
		if runRebaseLoop == nil {
			runRebaseLoop = agent.RunRebaseLoop
		}
		result, loopErr := runRebaseLoop(cfg, sm)
		o.handleFeatureRebaseDone(featureID, behind, result, loopErr)
	})

	return "", nil
}

// stageRebasePlanArtifacts writes the rebase-plan.md synchronously so
// the orchestrator's caller can read it before the async loop runs. The
// loop overwrites with the same content; we reuse the agent helper so
// the format stays in one place.
//
// The artifact dir is `runs/run-N/rebase-N+1/` — the loop bumps
// RebaseCount inside its own goroutine, so this method computes the
// next rebase-N name by speculating one-ahead. That speculation is
// safe because RebaseCount is monotonic and only the rebase loop bumps
// it; no other code path can race ahead of us between this stage write
// and the loop's bump.
func (o *Orchestrator) stageRebasePlanArtifacts(
	f *feature.Feature,
	behind []agent.RebaseRepoTarget,
) error {
	nextRebaseDir := filepath.Join(
		agent.ActiveRunDir(o.stateDir(), f),
		fmt.Sprintf("rebase-%d", f.RebaseCount()+1),
	)
	if err := os.MkdirAll(nextRebaseDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", nextRebaseDir, err)
	}
	planPath := filepath.Join(nextRebaseDir, "rebase-plan.md")
	if err := os.WriteFile(planPath, []byte(agent.BuildMultiRepoRebasePlan(behind)), 0o644); err != nil {
		return fmt.Errorf("write rebase-plan.md: %w", err)
	}
	return nil
}

// resolveBehindSubset enumerates every Feature.Repos branch behind its
// base branch, building the RebaseRepoTarget slice the loop expects. The
// hint repo is included unconditionally (TUI already detected it behind);
// other repos are probed via the RebaseOperator. Repos that are up-to-
// date are skipped silently. The Rebaser dependency may be nil in tests
// that don't wire git adapters; in that case only the hint repo is
// included.
func (o *Orchestrator) resolveBehindSubset(
	f *feature.Feature,
	hintRepoName, hintRebaseTarget string,
	hintConflictFiles []string,
) []agent.RebaseRepoTarget {
	var out []agent.RebaseRepoTarget
	prURLs := f.PRURLs()

	for i := range f.Repos {
		repo := &f.Repos[i]
		worktreeDir := repo.WorktreePath
		if worktreeDir == "" {
			worktreeDir = repo.Path
		}

		// The hint repo enters unconditionally with the TUI-resolved
		// target + conflict files.
		if repo.Name == hintRepoName {
			target := hintRebaseTarget
			if target == "" {
				target = o.resolveRebaseTarget(f, repo)
			}
			out = append(out, agent.RebaseRepoTarget{
				RepoName:      repo.Name,
				RebaseTarget:  target,
				ConflictFiles: hintConflictFiles,
				PRURL:         prURLs[repo.Name],
			})
			continue
		}

		// Probe other repos via the Rebaser port. Skip when the dep is
		// not wired (tests) — we rely on the TUI's hint to be
		// authoritative in that case.
		if o.deps.Rebaser == nil {
			continue
		}
		target := o.resolveRebaseTarget(f, repo)
		if target == "" {
			continue
		}

		// Best-effort fetch so the IsBehind* probe sees fresh refs. A
		// Fetch failure should not block the rebase cycle dispatch —
		// the inner agent will retry git operations itself.
		_ = o.deps.Rebaser.Fetch(worktreeDir)

		var behind bool
		if f.IsPublishable() {
			behind, _ = o.deps.Rebaser.IsBehindRemote(worktreeDir, target)
		} else {
			behind, _ = o.deps.Rebaser.IsBehindLocal(worktreeDir, target)
		}
		if !behind {
			continue
		}
		out = append(out, agent.RebaseRepoTarget{
			RepoName:     repo.Name,
			RebaseTarget: target,
			PRURL:        prURLs[repo.Name],
		})
	}
	return out
}

// handleFeatureRebaseDone routes the unified rebase loop's result back
// into the per-repo legacy plumbing so the TUI's existing event chain
// (RebaseRepoCycleResultMsg, RepoCycleLoopDoneMsg, etc.) keeps working.
// On success: per-repo CompleteRepoCycle clears each cycle entry. On
// failure: per-repo FailRepoCycle records the conflict for the user.
func (o *Orchestrator) handleFeatureRebaseDone(
	featureID string,
	behind []agent.RebaseRepoTarget,
	result *agent.RebaseLoopResult,
	loopErr error,
) {
	// Dispatch error or no-result → fail every staged repo with the
	// dispatch error.
	if loopErr != nil || result == nil {
		errMsg := "rebase: dispatch failed"
		if loopErr != nil {
			errMsg = "rebase: " + loopErr.Error()
		}
		for _, t := range behind {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, t.RepoName, errMsg)
		}
		return
	}

	switch result.FinalStatus {
	case "review_passed":
		// Cycle complete: clear any stale LastError on each rebased repo
		// and clear each per-repo cycle entry. The agent already
		// force-pushed each rebased branch inside its session, so the
		// orchestrator does NOT need to commit/push here.
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			for _, t := range behind {
				if st, ok := ff.RepoStates[t.RepoName]; ok && st != nil {
					st.LastError = ""
				}
			}
			return nil
		})
		for _, t := range behind {
			_ = o.deps.Lifecycle.CompleteRepoCycle(featureID, t.RepoName)
		}
		o.resumePublishAfterPrePRRebase(featureID)
		return

	case "interrupted", "no_op":
		// Interrupted: persisted state preserved for restart. No-op:
		// nothing to do. Either way, leave the per-repo cycle entries
		// in place; the harness recovery / next user action handles
		// them.
		return

	case "need_user_input":
		gate := &agent.LoopResult{
			FinalStatus:       "need_user_input",
			Iterations:        result.Iterations,
			LastError:         result.LastError,
			NeedUserInputPath: result.NeedUserInputPath,
		}
		for _, t := range behind {
			o.recordRepoCycleNeedUserInput(featureID, t.RepoName, feature.CycleRebase, gate)
		}
		if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
			if f.PendingNeedUserInputPath == result.NeedUserInputPath {
				f.PendingNeedUserInputPath = ""
			}
			return nil
		}); err != nil {
			for _, t := range behind {
				o.failRepoCycleGatePersistence(featureID, t.RepoName,
					fmt.Errorf("rebase: clear stale feature-level need-user-input gate: %w", err))
			}
		}
		return

	default:
		// max_iterations / safety_rail / failed: surface conflict per
		// repo so the TUI can present the failed cycle.
		errMsg := "rebase: " + result.FinalStatus
		if result.LastError != "" {
			errMsg = "rebase: " + result.LastError
		}
		for _, t := range behind {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, t.RepoName, errMsg)
		}
		return
	}
}

func activeRepoCycleOfType(f *feature.Feature, repoName string, cycleType feature.RepoCycleType) bool {
	if f == nil || repoName == "" {
		return false
	}
	rc, ok := f.RepoCycles[repoName]
	if !ok || rc == nil || rc.Type != cycleType {
		return false
	}
	switch rc.Status {
	case feature.RepoCycleRunning, feature.RepoCycleReviewing, feature.RepoCycleNeedUserInput:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) resumePublishAfterPrePRRebase(featureID string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil {
		return
	}
	if f.Status != feature.StatusCodeReady || !f.IsPublishable() || !f.Checkpoints.AutoPublish() {
		return
	}
	if f.HasActiveRepoCycles() || f.AllReposPublished() {
		return
	}
	publishFn := o.publishFn
	if publishFn == nil {
		publishFn = o.Publish
	}
	_ = publishFn(featureID)
}
