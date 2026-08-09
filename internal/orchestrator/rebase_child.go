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

package orchestrator

import (
	"fmt"
	"os"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// RebaseChildPreflightResult carries the creation-time resolved per-repo
// targets, the behind set, and the captured parent tip SHAs produced by the
// orchestrator preflight.
type RebaseChildPreflightResult struct {
	Bases   []feature.ChildRepoBase
	Targets []feature.RebaseRepoTarget
	Behind  []string
}

// RebaseChildPreflight performs the orchestrator-owned preflight for a rebase
// child launch: it loads the parent, checks every worktree for dirty state,
// resolves each repo's merge target (PR base branch → recorded base branch →
// repository default branch), fetches (for publishable repos), and computes
// behind-ness against the remote-tracking target ref for publishable repos and
// the local target branch otherwise. If any repo fails target resolution or
// fetch, the whole preflight fails atomically with a typed error naming the
// repo. If every repo is up to date, it returns a RebaseAlreadyUpToDateError.
func (o *Orchestrator) RebaseChildPreflight(parentID string) (*RebaseChildPreflightResult, error) {
	parent, err := o.deps.Store.Load(parentID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", feature.ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := feature.ValidateRefactorParent(parent, nil); err != nil {
		return nil, err
	}

	bases, err := o.preflightCleanWorktreesAndCaptureSHAs(parent)
	if err != nil {
		return nil, err
	}

	targets := make([]feature.RebaseRepoTarget, 0, len(parent.Repos))
	var behind []string
	for i := range parent.Repos {
		repo := &parent.Repos[i]
		worktreePath := repo.WorktreePath
		if worktreePath == "" {
			worktreePath = repo.Path
		}
		publishable := repo.Publishable == nil || *repo.Publishable

		target := o.resolveRebaseTarget(parent, repo)
		if target == "" {
			return nil, &feature.RebaseTargetResolutionError{Repo: repo.Name}
		}

		var ref string
		if publishable {
			ref = "origin/" + target
			if err := git.Fetch(worktreePath); err != nil {
				return nil, &feature.RebaseFetchError{Repo: repo.Name, Err: err}
			}
			if git.IsBehindRemote(worktreePath, target) {
				behind = append(behind, repo.Name)
			}
		} else {
			ref = target
			if git.IsBehindLocal(worktreePath, target) {
				behind = append(behind, repo.Name)
			}
		}

		// Capture the target commit SHA as it stood at the creation-time
		// fetch, resolving the same ref behind-ness was computed against. The
		// mechanical integration gate reads this SHA (never re-resolving the
		// ref) so a target that moves after creation does not change what the
		// gate checks. A resolution failure is a creation-time failure
		// surfaced through the existing typed target-resolution error path.
		targetSHA, err := git.ReadRefSHA(worktreePath, ref)
		if err != nil {
			return nil, &feature.RebaseTargetResolutionError{Repo: repo.Name, Err: err}
		}

		targets = append(targets, feature.RebaseRepoTarget{
			Repo:        repo.Name,
			Target:      target,
			Ref:         ref,
			Publishable: publishable,
			TargetSHA:   targetSHA,
		})
	}

	if len(behind) == 0 {
		return nil, &feature.RebaseAlreadyUpToDateError{Targets: targets}
	}

	return &RebaseChildPreflightResult{
		Bases:   bases,
		Targets: targets,
		Behind:  behind,
	}, nil
}

// preflightCleanWorktreesAndCaptureSHAs inspects every parent worktree for
// dirty state and captures each repository's full HEAD SHA. Any dirty
// repository rejects the whole preflight with categorized diagnostics.
func (o *Orchestrator) preflightCleanWorktreesAndCaptureSHAs(parent *feature.Feature) ([]feature.ChildRepoBase, error) {
	if o.deps.Worktrees == nil {
		return nil, fmt.Errorf("cleanliness inspection is not configured")
	}
	var bases []feature.ChildRepoBase
	var dirty []feature.RepoDirtyDiagnostics
	for _, repo := range parent.Repos {
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		report, err := o.deps.Worktrees.InspectCleanliness(path, feature.DefaultDirtyPathLimit)
		if err != nil {
			return nil, fmt.Errorf("inspecting parent worktree %s: %w", repo.Name, err)
		}
		if report.Dirty() {
			dirty = append(dirty, feature.RepoDirtyDiagnostics{
				Repo:           repo.Name,
				Path:           path,
				Staged:         report.Staged,
				Unstaged:       report.Unstaged,
				Untracked:      report.Untracked,
				StagedTotal:    report.StagedTotal,
				UnstagedTotal:  report.UnstagedTotal,
				UntrackedTotal: report.UntrackedTotal,
			})
			continue
		}
		sha, err := o.deps.Worktrees.CurrentHeadSHA(path)
		if err != nil {
			return nil, fmt.Errorf("capturing HEAD of parent repo %s: %w", repo.Name, err)
		}
		bases = append(bases, feature.ChildRepoBase{Repo: repo.Name, SHA: sha, ParentBranch: repo.Branch})
	}
	if len(dirty) > 0 {
		return nil, &feature.ParentWorktreesDirtyError{Repos: dirty}
	}
	return bases, nil
}
