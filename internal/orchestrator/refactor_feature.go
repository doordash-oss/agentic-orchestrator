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

// Package orchestrator wires the feature-level refactor cycle. One call to
// startFeatureRefactor invokes agent.RunRefactorFeatureLoop with --add-dir for
// every Feature.Repos worktree, runs the refactor-plan step, then drives the
// iterative implement loop. Cross-repo Tasks are first-class.
//
// The legacy per-repo lifecycle plumbing (StartRepoCycle / FailRepoCycle /
// CompleteRepoCycle / RefactorPrompt) is preserved as a façade so the TUI
// event chain keeps working unchanged.
package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// startFeatureRefactor launches the unified refactor cycle. The repoName
// argument from the legacy per-repo TUI dispatch is treated as a hint
// only — the loop mounts every Feature.Repos worktree and stages the
// repos that the refactor-plan's `**Repo:** <name>` tags select. Returns
// "" + nil on successful dispatch (no stable session ID; the inner loop
// owns per-iteration IDs); returns ("", err) on dispatch failure.
//
// Mirrors startFeatureRebase / startFeatureReviewComments in shape: the
// orchestrator stamps RefactorCount + RefactorPrompt synchronously, opens
// per-repo cycle entries for the legacy TUI rendering paths, then runs
// the agent loop in a background goroutine tracked by cycleWG.
func (o *Orchestrator) startFeatureRefactor(
	featureID, hintRepoName, prompt string,
) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	// Defensive: refactor cycles are valid once final review has passed.
	// CodeReady is the manual-publish steady state; Published is the
	// auto-publish / already-published steady state.
	if f.Status != feature.StatusCodeReady && f.Status != feature.StatusPublished {
		return "", fmt.Errorf("feature not in code-ready or published state (status=%s)", f.Status)
	}

	if prompt == "" {
		return "", errors.New("refactor prompt is empty")
	}

	// Block concurrent refactor cycles on the same feature. Paused cycles
	// (need_user_input) count as active because they preserve the shared
	// feature-level RefactorPrompt that a second refactor would clobber
	// before the first gate is resolved.
	if hasRunningRefactor(f) {
		return "", errors.New("another refactor cycle is already running on this feature")
	}

	// Bump RefactorCount synchronously, stash the prompt, and clear any
	// stale plan artifact pointer in one Modify so the TUI sees the
	// updated state immediately when this method returns. The loop's own
	// Modify (inside RunRefactorFeatureLoop) re-reads the count; the
	// double-bump is avoided by pre-incrementing here and having the
	// loop adopt the on-disk value as its working count.
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		ff.SetRefactorCount(ff.RefactorCount() + 1)
		ff.RefactorPrompt = prompt
		if ff.Artifacts != nil {
			delete(ff.Artifacts, "plan")
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("stash refactor prompt: %w", err)
	}

	// Open per-repo cycle entries for every Feature.Repos. The refactor loop
	// is feature-level, but TUI rendering still reads RepoCycles[name].Status
	// while the loop is running.
	for i := range f.Repos {
		_ = o.deps.Lifecycle.StartRepoCycle(featureID, f.Repos[i].Name, feature.CycleRefactor)
	}
	// Reload so RefactorCount and per-repo cycle Counts reflect the
	// StartRepoCycle writes.
	f, err = o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("reload feature: %w", err)
	}

	// Stage the artifact dir synchronously so callers can see the dir
	// immediately after this method returns. The async loop will reuse
	// it — the operation is idempotent. Layout is flat
	// (refactor-N/refactor-prompt.md, no per-repo subdir).
	stagedArtifactDir := filepath.Join(
		agent.ActiveRunDir(o.stateDir(), f),
		fmt.Sprintf("refactor-%d", f.RefactorCount()),
	)
	if err := os.MkdirAll(stagedArtifactDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir refactor dir: %w", err)
	}
	// Persist a "refactor-prompt.md" alongside the staged dir so a
	// crashed run can read the prompt back even if the persisted feature
	// state was lost.
	promptPath := filepath.Join(stagedArtifactDir, "refactor-prompt.md")
	_ = os.WriteFile(promptPath, []byte("# Refactor\n\n"+prompt+"\n"), 0o644)

	cfg := agent.RefactorFeatureLoopConfig{
		Feature:                    f,
		FeatureStore:               o.deps.Store,
		StateDir:                   o.stateDir(),
		Prompt:                     prompt,
		Model:                      f.Models.Implementation,
		ReviewModel:                f.Models.Review,
		PlanningModel:              f.Models.Planning,
		MaxIterations:              f.MaxIterations,
		MaxConsecFails:             3,
		MaxConsecNoProgress:        3,
		KBInfos:                    o.computeKBInfos(f),
		DangerouslySkipPermissions: pr.DangerouslySkipPermissions,
		PermissionCache:            pr.PermissionCache,
		BuildSession:               pr.BuildSession,
		AskingClause:               pr.AskingClauseForModel(f.Models.Implementation),
		AskingClauseForModel:       pr.AskingClauseForModel,
		EffortLevel:                f.EffectivePipeline().EffortLevel(),
		SkillsDir:                  pr.SkillsDir,
		GuidelinesDir:              pr.GuidelinesDir,
		FinishOrViolateNudge: pr.FinishOrViolateNudgeForModel(f.Models.Implementation) &&
			pr.FinishOrViolateNudgeForModel(f.Models.Planning) &&
			pr.FinishOrViolateNudgeForModel(f.Models.Review),
		Observer: pr.Observer,
	}
	_ = hintRepoName // diagnostic only under the unified flow.

	repoNames := make([]string, 0, len(f.Repos))
	for i := range f.Repos {
		repoNames = append(repoNames, f.Repos[i].Name)
	}

	sm := o.deps.Sessions
	o.cycleWG.Go(func() {
		result, loopErr := agent.RunRefactorFeatureLoop(cfg, sm)
		o.handleFeatureRefactorDone(featureID, repoNames, result, loopErr)
	})

	return "", nil
}

// handleFeatureRefactorDone routes the unified refactor loop's result
// back into the per-repo legacy plumbing so the TUI's existing event
// chain (RefactorCycleLoopDoneMsg via the shared HandleRefactorCycleLoopDone
// pathway) keeps working. On success: per-repo CompleteRefactorRepoCycle
// commits + pushes each touched repo's branch and clears the cycle entry.
// On failure: per-repo FailRepoCycle records the error.
//
// repoNames is the feature's full Feature.Repos set as it stood at
// dispatch — every repo had a per-repo cycle entry opened, so every entry
// must be closed (or failed) here.
func (o *Orchestrator) handleFeatureRefactorDone(
	featureID string,
	repoNames []string,
	result *agent.RefactorFeatureLoopResult,
	loopErr error,
) {
	if loopErr != nil || result == nil {
		errMsg := "refactor: dispatch failed"
		if loopErr != nil {
			errMsg = "refactor: " + loopErr.Error()
		}
		for _, name := range repoNames {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, name, errMsg)
		}
		// Clear the refactor prompt on failure so a follow-up refactor
		// doesn't re-trigger the same broken plan.
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.RefactorPrompt = ""
			return nil
		})
		return
	}

	switch result.FinalStatus {
	case "review_passed":
		// Cycle complete: every staged repo's Touched flag was already set
		// by AtomicPhaseStamp. Run the per-repo completion finalisation
		// (commit+push) then clear each per-repo cycle entry.
		stagedSet := make(map[string]bool, len(result.Repos))
		for _, n := range result.Repos {
			stagedSet[n] = true
		}
		// Clear any stale LastError on the staged subset and reset the
		// refactor prompt. Repos NOT in the staged subset (the plan didn't
		// touch them) just get their per-repo cycle entry cleared below.
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			for _, name := range result.Repos {
				if st, ok := ff.RepoStates[name]; ok && st != nil {
					st.LastError = ""
				}
			}
			ff.RefactorPrompt = ""
			return nil
		})
		for _, name := range repoNames {
			if stagedSet[name] {
				if err := o.completeRefactorRepoFinalize(featureID, name); err != nil {
					_ = o.deps.Lifecycle.FailRepoCycle(featureID, name, err.Error())
					continue
				}
			}
			_ = o.deps.Lifecycle.CompleteRepoCycle(featureID, name)
		}
		return

	case "interrupted", "no_op":
		// Persisted state preserved for restart. Leave per-repo cycle
		// entries in place; the harness recovery / next user action
		// handles them.
		return

	case "need_user_input":
		// Surface the gate via the legacy per-repo NEED_USER_INPUT
		// pathway for the staged subset. Repos outside the staged subset
		// stay as RepoCycleRunning until the gate resolves.
		stagedSet := make(map[string]bool, len(result.Repos))
		for _, n := range result.Repos {
			stagedSet[n] = true
		}
		gate := &agent.LoopResult{
			FinalStatus:       "need_user_input",
			LastError:         result.LastError,
			NeedUserInputPath: result.NeedUserInputPath,
		}
		for _, name := range repoNames {
			if stagedSet[name] {
				_ = o.onRepoCycleNeedUserInput(featureID, name, feature.CycleRefactor, gate)
			}
		}
		return

	default:
		// max_iterations / safety_rail / failed: surface error per repo
		// so the TUI can present the failed cycle.
		errMsg := "refactor: " + result.FinalStatus
		if result.LastError != "" {
			errMsg = "refactor: " + result.LastError
		}
		for _, name := range repoNames {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, name, errMsg)
		}
		// Clear the refactor prompt on failure so a follow-up refactor
		// doesn't re-trigger the same broken plan.
		_ = o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			ff.RefactorPrompt = ""
			return nil
		})
		return
	}
}

// completeRefactorRepoFinalize runs the post-loop finalisation for a
// single staged repo: commit any uncommitted refactor changes,
// pull-rebase, and push. Mirrors the legacy CompleteRefactorRepoCycle
// commit+push chain so per-repo branch propagation behavior is preserved
// under the unified flow.
func (o *Orchestrator) completeRefactorRepoFinalize(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}
	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return fmt.Errorf("repo %q not found in feature", repoName)
	}

	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}
	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	if o.deps.Publisher != nil {
		_ = o.deps.Publisher.CommitAll(workDir, "Apply refactor changes")
	}
	if o.deps.Rebaser != nil {
		_ = o.deps.Rebaser.PullRebase(workDir, branch)
	}
	if o.deps.Publisher != nil {
		if err := o.deps.Publisher.Push(workDir, branch); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	}
	return nil
}
