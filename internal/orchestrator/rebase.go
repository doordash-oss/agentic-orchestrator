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

// Package orchestrator — rebase.go owns the per-repo rebase cycle lifecycle.
// Public entry points: StartRebase, HandleRebaseResult, ForcePushAfterConflict.
// All carry a repoName parameter.
package orchestrator

import (
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// RebaseResultInput describes the outcome of a rebase step, typically fed
// from the TUI after the user resolves conflicts or the auto-rebase finishes.
type RebaseResultInput struct {
	RepoName      string
	HasConflict   bool
	Aborted       bool
	Err           error
	RebaseTarget  string
	ConflictFiles []string
}

// RebaseConflictError is returned when a rebase encounters a merge conflict
// that needs interactive resolution. RebaseTarget carries the ref the rebase
// was being applied onto (e.g. "master", "main") so the conflict-resolution
// plan can name the correct base. ConflictFiles lists the files left with
// unmerged state in the worktree.
type RebaseConflictError struct {
	FeatureID     string
	RepoName      string
	Branch        string
	RebaseTarget  string
	ConflictFiles []string
}

// Error formats the rebase-conflict sentinel.
func (e *RebaseConflictError) Error() string {
	return fmt.Sprintf("rebase: conflict in feature %s repo %s on branch %s",
		e.FeatureID, e.RepoName, e.Branch)
}

// Is reports whether target is the rebase-conflict sentinel.
func (e *RebaseConflictError) Is(target error) bool {
	_, ok := target.(*RebaseConflictError)
	return ok
}

// ErrRebaseConflict is the sentinel used with errors.Is for rebase conflicts.
var ErrRebaseConflict = &RebaseConflictError{}

// ErrRebaseIncomplete is returned when CompleteRebase observes a worktree
// still mid-rebase (rebase-merge / rebase-apply dir present). The caller
// must not transition the feature past the rebase cycle — the branch
// pointer is still stale and a force-push would silently push nothing
// useful, leaving the PR in its pre-rebase conflict state.
var ErrRebaseIncomplete = errors.New("rebase: worktree still has an unfinished rebase (run `git rebase --continue` or `--abort` before completing)")

// resolveRebaseTarget picks the base ref a follow-up rebase should target
// for the given repo. The order matches the auto-rebase path so conflict
// recovery and the proactive rebase agree on the target:
//
//  1. The PR's base branch on GitHub (when a PRURL is recorded), via the
//     RebaseOperator. This is authoritative for published features.
//  2. repo.BaseBranch from the feature manifest.
//  3. The repo's default branch (origin/HEAD) via the BranchOperator.
//
// Returns "" only when every fallback fails — callers should treat that as
// a hard error rather than silently rebasing onto an empty target.
func (o *Orchestrator) resolveRebaseTarget(f *feature.Feature, repo *feature.FeatureRepo) string {
	target := ""
	if state, ok := f.RepoStates[repo.Name]; ok && state != nil && state.PRURL != "" && o.deps.Rebaser != nil {
		target, _ = o.deps.Rebaser.PRBaseBranch(repo.Path, state.PRURL)
	}
	if target == "" {
		target = repo.BaseBranch
	}
	if target == "" && o.deps.Branch != nil {
		target, _ = o.deps.Branch.DefaultBranch(repo.Path)
	}
	return target
}

// StartRebase performs a per-repo rebase for a published feature. The
// per-repo cycle is recorded via Lifecycle.StartRepoCycle elsewhere; this
// method runs the fetch/rebase/push sequence scoped to one repo and returns
// a *RebaseConflictError on merge conflict so callers can launch the
// conflict-resolution loop.
func (o *Orchestrator) StartRebase(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("rebase-repo load feature: %w", err)
	}

	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	worktreeDir := repo.WorktreePath
	if worktreeDir == "" {
		worktreeDir = repo.Path
	}

	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	if f.IsPublishable() {
		// Remote rebase: fetch + rebase onto origin/<base> + force push.
		if err := o.deps.Rebaser.Fetch(worktreeDir); err != nil {
			return fmt.Errorf("rebase-repo fetch failed: %w", err)
		}

		rebaseTarget := o.resolveRebaseTarget(f, repo)

		behind, _ := o.deps.Rebaser.IsBehindRemote(worktreeDir, rebaseTarget)
		if !behind {
			return fmt.Errorf("%s already up to date with %s", repoName, rebaseTarget)
		}

		result := o.deps.Rebaser.RebaseOnto(worktreeDir, "origin/"+rebaseTarget)
		switch result.Outcome {
		case ports.RebaseConflict:
			return &RebaseConflictError{
				FeatureID:     featureID,
				RepoName:      repoName,
				Branch:        branch,
				RebaseTarget:  rebaseTarget,
				ConflictFiles: result.ConflictFiles,
			}
		case ports.RebaseFailed:
			if result.Err != nil {
				return fmt.Errorf("rebase-repo onto origin/%s: %w", rebaseTarget, result.Err)
			}
			return fmt.Errorf("rebase-repo onto origin/%s failed", rebaseTarget)
		}

		if err := o.deps.Rebaser.ForcePush(worktreeDir, branch); err != nil {
			return fmt.Errorf("rebase-repo force push failed: %w", err)
		}
		return nil
	}

	// Local rebase: rebase onto local base branch (no fetch/push).
	rebaseTarget := repo.BaseBranch
	if rebaseTarget == "" && o.deps.Branch != nil {
		rebaseTarget, _ = o.deps.Branch.DefaultBranch(repo.Path)
	}

	behind, _ := o.deps.Rebaser.IsBehindLocal(worktreeDir, rebaseTarget)
	if !behind {
		return fmt.Errorf("%s already up to date with %s", repoName, rebaseTarget)
	}

	result := o.deps.Rebaser.RebaseOnto(worktreeDir, rebaseTarget)
	switch result.Outcome {
	case ports.RebaseConflict:
		return &RebaseConflictError{
			FeatureID:     featureID,
			RepoName:      repoName,
			Branch:        branch,
			RebaseTarget:  rebaseTarget,
			ConflictFiles: result.ConflictFiles,
		}
	case ports.RebaseFailed:
		if result.Err != nil {
			return fmt.Errorf("rebase-repo onto %s: %w", rebaseTarget, result.Err)
		}
		return fmt.Errorf("rebase-repo onto %s failed", rebaseTarget)
	}

	return nil
}

// HandleRebaseResult dispatches a per-repo rebase outcome. Conflict outcomes
// are no-ops here; the TUI's startRepoCycleImplementCmd handles the
// per-repo conflict-fix loop. Fatal errors call FailRepoCycle.
func (o *Orchestrator) HandleRebaseResult(featureID, repoName string, input RebaseResultInput) error {
	if input.HasConflict {
		// Per-repo conflict: the TUI's startRepoCycleImplementCmd handles the
		// per-repo conflict-fix loop. Mirror that entry by ensuring the cycle
		// is recorded; the concrete implementation loop dispatch remains with
		// the caller (TUI), which knows how to wire the agent plan content.
		return nil
	}
	if input.Aborted {
		return nil
	}
	if input.Err != nil {
		errMsg := "rebase-repo: " + input.Err.Error()
		if err := o.deps.Lifecycle.FailRepoCycle(featureID, repoName, errMsg); err != nil {
			return fmt.Errorf("fail repo cycle: %w", err)
		}
		return nil
	}
	return nil
}

// ForcePushAfterConflict force-pushes the feature branch of the given repo
// after the user has resolved conflicts manually. Uses the RebaseOperator's
// ForcePush method.
func (o *Orchestrator) ForcePushAfterConflict(featureID, repoName string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("force-push load feature: %w", err)
	}

	var repo *feature.FeatureRepo
	for i := range f.Repos {
		if f.Repos[i].Name == repoName {
			repo = &f.Repos[i]
			break
		}
	}
	if repo == nil {
		return fmt.Errorf("repo %s not found", repoName)
	}

	worktreeDir := repo.WorktreePath
	if worktreeDir == "" {
		worktreeDir = repo.Path
	}

	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	if err := o.deps.Rebaser.ForcePush(worktreeDir, branch); err != nil {
		return fmt.Errorf("force push: %w", err)
	}
	return nil
}
