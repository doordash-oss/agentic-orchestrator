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

// Package orchestrator — rebase.go owns feature-level rebase helpers.
package orchestrator

import (
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type HarnessRebaseRepoOutcome struct {
	RepoName      string
	Status        feature.RebaseRepoStatus
	RebaseTarget  string
	ConflictFiles []string
	Changed       bool
	Err           error
	Publishable   bool
	WorktreePath  string
	Branch        string
}

// resolveRebaseTarget picks the base ref a follow-up rebase should target
// for the given repo. The order matches the auto-rebase path so conflict
// recovery and the proactive rebase agree on the target:
//
//  1. The PR's base branch on GitHub (when a PRURL is recorded), via the
//     RebaseOperator. This is authoritative for published features.
//  2. repo.BaseBranch from the feature manifest.
//  3. The repo's default branch (origin/HEAD).
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
	if target == "" {
		target = git.DefaultBranch(repo.Path)
	}
	return target
}

func (o *Orchestrator) runHarnessRebaseRepo(f *feature.Feature, repo feature.FeatureRepo) HarnessRebaseRepoOutcome {
	out := o.harnessRebaseOutcomeForRepo(f, repo)
	if out.RebaseTarget == "" {
		out.Status = feature.RebaseRepoStatusFailed
		out.Err = errors.New("rebase target not found")
		return out
	}
	if o.deps.Rebaser == nil {
		out.Status = feature.RebaseRepoStatusFailed
		out.Err = errors.New("rebase operator not configured")
		return out
	}

	if out.Publishable {
		if err := o.deps.Rebaser.Fetch(out.WorktreePath); err != nil {
			out.Status = feature.RebaseRepoStatusFailed
			out.Err = fmt.Errorf("fetch failed: %w", err)
			return out
		}

		behind, err := o.deps.Rebaser.IsBehindRemote(out.WorktreePath, out.RebaseTarget)
		if err != nil {
			out.Status = feature.RebaseRepoStatusFailed
			out.Err = fmt.Errorf("behind remote check failed: %w", err)
			return out
		}
		if !behind {
			out.Status = feature.RebaseRepoStatusUpToDate
			return out
		}

		return harnessOutcomeFromRebaseResult(out, o.deps.Rebaser.RebaseOnto(out.WorktreePath, "origin/"+out.RebaseTarget))
	}

	behind, err := o.deps.Rebaser.IsBehindLocal(out.WorktreePath, out.RebaseTarget)
	if err != nil {
		out.Status = feature.RebaseRepoStatusFailed
		out.Err = fmt.Errorf("behind local check failed: %w", err)
		return out
	}
	if !behind {
		out.Status = feature.RebaseRepoStatusUpToDate
		return out
	}

	return harnessOutcomeFromRebaseResult(out, o.deps.Rebaser.RebaseOnto(out.WorktreePath, out.RebaseTarget))
}

func harnessOutcomeFromRebaseResult(out HarnessRebaseRepoOutcome, result ports.RebaseResult) HarnessRebaseRepoOutcome {
	switch result.Outcome {
	case ports.RebaseSuccess:
		out.Status = feature.RebaseRepoStatusChanged
		out.Changed = true
		return out
	case ports.RebaseConflict:
		out.Status = feature.RebaseRepoStatusConflict
		out.ConflictFiles = append([]string(nil), result.ConflictFiles...)
		return out
	case ports.RebaseFailed:
		out.Status = feature.RebaseRepoStatusFailed
		if result.Err != nil {
			out.Err = result.Err
		} else {
			out.Err = errors.New("rebase failed")
		}
		return out
	default:
		out.Status = feature.RebaseRepoStatusFailed
		out.Err = fmt.Errorf("unknown rebase outcome %d", result.Outcome)
		return out
	}
}
