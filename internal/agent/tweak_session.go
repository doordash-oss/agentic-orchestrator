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

// Package agent contains the feature-level TweakSession module. It opens one
// interactive Claude session that mounts every Feature.Repos worktree via
// --add-dir, so the user can drive cross-repo edits in one session.
//
// TweakSession encapsulates the four lifecycle steps of a feature-level
// tweak:
//
//  1. Start — open one Claude PTY session at the feature state dir, with
//     --add-dir for every Feature.Repos worktree. No iteration counter, no
//     review gate, no testing contract; the user drives.
//  2. CommitAll — after the user ends the session, iterate every
//     Feature.Repos and commit any worktree with uncommitted changes (the
//     skill prompt forbids the agent from committing).
//  3. PushAll — pull-rebase + push every modified repo's branch. A
//     PullRebaseConflict surfaces a structured FeatureTweakPushError carrying
//     the per-repo conflict so the orchestrator can route the affected repo
//     into a rebase cycle.
//  4. Mark transitions on Feature.ActiveCycle: Status: running while the
//     session is alive; cleared on success; transitioned to Status: interrupted
//     on session-die-mid-tweak (crash recovery).
//
// Construction is pure-input/pure-output. Side effects (commit, push,
// rebase, store mutation) flow through injected port interfaces:
//   - Publisher: HasUncommittedChanges, CommitAll, Push
//   - Rebaser:   PullRebase
//   - SessionManager: StartSession (forwarded by the PhaseRunner)
//   - FeatureStore:   ActiveCycle / TweakCount transitions
package agent

import (
	"errors"
	"fmt"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// FeatureTweakPushError carries the set of per-repo push failures produced
// by TweakSession.PushAll. The orchestrator inspects Conflicts to route
// each conflicted repo into a rebase cycle.
//
// Only Conflicts (PullRebase outcome == PullRebaseConflict) and Failures
// (any other push/pull-rebase error) appear; clean repos are not surfaced.
// At least one of Conflicts / Failures is non-empty when this error is
// returned.
type FeatureTweakPushError struct {
	Conflicts []TweakRepoConflict
	Failures  []TweakRepoFailure
}

// TweakRepoConflict identifies a repo whose pull-rebase produced a conflict
// during PushAll. The orchestrator forwards Branch + RebaseTarget into the
// PublishConflictError it surfaces to the TUI; the TUI dispatches a fresh
// CycleRebase against the affected repo.
type TweakRepoConflict struct {
	RepoName     string
	Branch       string
	RebaseTarget string
}

// TweakRepoFailure carries a non-conflict push/pull-rebase error scoped to
// a specific repo. The orchestrator surfaces these as plain errors to the
// caller; the TUI shows them as failed cycles.
type TweakRepoFailure struct {
	RepoName string
	Branch   string
	Err      error
}

// Error implements error so callers can errors.As against
// *FeatureTweakPushError.
func (e *FeatureTweakPushError) Error() string {
	return fmt.Sprintf(
		"feature tweak push: %d conflicts, %d failures",
		len(e.Conflicts), len(e.Failures),
	)
}

// TweakSession is the unified feature-level tweak deep module. Instances
// are constructed per call from the orchestrator's wired Deps.
//
// The session does not own a cwd or session ID; the harness's PhaseRunner
// owns those. TweakSession owns the lifecycle stamps on
// Feature.ActiveCycle, the per-repo commit/push fan-out, and the mapping
// from PullRebase outcomes to FeatureTweakPushError.
type TweakSession struct {
	Store     ports.FeatureStore
	Publisher ports.Publisher
	Rebaser   ports.RebaseOperator

	// ResolveRebaseTarget returns the rebase target ref for a repo on a
	// pull-rebase conflict. Mirrors Orchestrator.resolveRebaseTarget; the
	// orchestrator wires its own resolver in. May be nil — the conflict
	// then surfaces with an empty RebaseTarget and callers fall back to
	// the legacy "feature branch" assumption.
	ResolveRebaseTarget func(f *feature.Feature, repo *feature.FeatureRepo) string
}

// MarkRunning stamps Feature.ActiveCycle = {Type: tweak, Status: running}
// and increments TweakCount. Returns the post-modify Feature so callers
// can read the new TweakCount without reloading.
//
// MarkRunning is idempotent on retry: a second call against an already-
// running tweak short-circuits to keep the existing cycle entry; the
// TweakCount is NOT double-bumped.
func (ts *TweakSession) MarkRunning(featureID string) (*feature.Feature, error) {
	if ts.Store == nil {
		return nil, errors.New("tweak session: store not configured")
	}
	if err := ts.Store.Modify(featureID, func(f *feature.Feature) error {
		// Idempotent: a re-entrant MarkRunning on an already-running tweak
		// preserves the existing entry so TweakCount is not double-bumped.
		if f.ActiveCycle != nil &&
			f.ActiveCycle.Type == feature.CycleTweak &&
			f.ActiveCycle.Status == feature.RepoCycleRunning {
			return nil
		}
		f.SetTweakCount(f.TweakCount() + 1)
		f.SetActiveCycleType(feature.CycleTweak)
		f.ActiveCycle = &feature.CycleState{
			Type:   feature.CycleTweak,
			Status: feature.RepoCycleRunning,
			Count:  f.TweakCount(),
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("mark running: %w", err)
	}
	return ts.Store.Load(featureID)
}

// MarkInterrupted transitions ActiveCycle.Status = "interrupted". Used by
// crash recovery when the harness sees Status: running with no live
// session. The tweak entry remains in place so the TUI can offer "resume
// tweak" → fresh session.
func (ts *TweakSession) MarkInterrupted(featureID string) error {
	if ts.Store == nil {
		return errors.New("tweak session: store not configured")
	}
	return ts.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle == nil || f.ActiveCycle.Type != feature.CycleTweak {
			return nil
		}
		f.ActiveCycle.Status = "interrupted"
		return nil
	})
}

// MarkFailed records a session-end failure on Feature.ActiveCycle. The
// entry is preserved (Status: failed) so the TUI can present the failed
// cycle and the user can retry. ClearActiveCycle wipes the entry.
func (ts *TweakSession) MarkFailed(featureID, errMsg string) error {
	if ts.Store == nil {
		return errors.New("tweak session: store not configured")
	}
	return ts.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle == nil || f.ActiveCycle.Type != feature.CycleTweak {
			return nil
		}
		f.ActiveCycle.Status = feature.RepoCycleFailed
		f.ActiveCycle.LastError = errMsg
		return nil
	})
}

// ClearActiveCycle wipes Feature.ActiveCycle on successful completion or
// when the user cancels the tweak via Esc. The TweakCount remains so the
// next tweak's artifact dir is enumerated correctly.
func (ts *TweakSession) ClearActiveCycle(featureID string) error {
	if ts.Store == nil {
		return errors.New("tweak session: store not configured")
	}
	return ts.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.ActiveCycle != nil && f.ActiveCycle.Type != feature.CycleTweak {
			return nil
		}
		f.ActiveCycle = nil
		f.SetActiveCycleType("")
		return nil
	})
}

// CommitAll iterates every Feature.Repos and commits any worktree with
// uncommitted changes. Returns the per-repo "had changes" map keyed by
// repo name; downstream PushAll uses the map to decide which repos need
// pulling/pushing.
//
// Repos missing both WorktreePath and Path (mis-wired test fixtures) are
// silently skipped with hadChanges == false. A commit failure on any repo
// returns the first error and stops the iteration; partial commits across
// preceding repos remain on disk (the user can re-run). Any errors are
// recorded on Feature.ActiveCycle via MarkFailed by the caller.
func (ts *TweakSession) CommitAll(f *feature.Feature) (map[string]bool, error) {
	if f == nil {
		return nil, errors.New("tweak commit-all: nil feature")
	}
	if ts.Publisher == nil {
		return map[string]bool{}, nil
	}
	hadChanges := make(map[string]bool, len(f.Repos))
	for i := range f.Repos {
		repo := &f.Repos[i]
		worktree := repo.WorktreePath
		if worktree == "" {
			worktree = repo.Path
		}
		if worktree == "" {
			continue
		}
		dirty, err := ts.Publisher.HasUncommittedChanges(worktree)
		if err != nil {
			return hadChanges, fmt.Errorf("repo %q check uncommitted: %w", repo.Name, err)
		}
		if !dirty {
			continue
		}
		if err := ts.Publisher.CommitAll(worktree, "Apply tweak changes"); err != nil {
			return hadChanges, fmt.Errorf("repo %q commit: %w", repo.Name, err)
		}
		hadChanges[repo.Name] = true
	}
	return hadChanges, nil
}

// PushAll pull-rebases + pushes the branch for every repo whose name maps
// to true in modifiedRepos. Repos absent from the map (no changes
// committed in CommitAll) are skipped. A pull-rebase conflict on any repo
// is collected into FeatureTweakPushError.Conflicts; other errors land in
// FeatureTweakPushError.Failures. The push for a conflicted repo is
// short-circuited so the unrebased work is not pushed.
//
// PushAll returns nil when every modified repo pushed cleanly. When at
// least one repo failed, the returned error is *FeatureTweakPushError and
// the iteration is non-fail-fast: every modified repo gets a push attempt
// so the orchestrator knows which repos need follow-on rebase cycles.
func (ts *TweakSession) PushAll(f *feature.Feature, modifiedRepos map[string]bool) error {
	if f == nil {
		return errors.New("tweak push-all: nil feature")
	}
	if len(modifiedRepos) == 0 {
		return nil
	}

	pushErr := &FeatureTweakPushError{}

	// Iterate in deterministic order (alphabetical by repo name) so test
	// output is stable.
	names := make([]string, 0, len(f.Repos))
	for i := range f.Repos {
		if modifiedRepos[f.Repos[i].Name] {
			names = append(names, f.Repos[i].Name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		repo := findRepoForTweak(f, name)
		if repo == nil {
			continue
		}
		worktree := repo.WorktreePath
		if worktree == "" {
			worktree = repo.Path
		}
		if worktree == "" {
			continue
		}
		branch := repo.Branch
		if branch == "" {
			branch = "feature/" + f.Slug
		}

		// Pull-rebase first. A conflict short-circuits the push so the
		// pre-rebase head is not pushed.
		if ts.Rebaser != nil {
			result := ts.Rebaser.PullRebase(worktree, branch)
			switch result.Outcome {
			case ports.PullRebaseConflict:
				rebaseTarget := ""
				if ts.ResolveRebaseTarget != nil {
					rebaseTarget = ts.ResolveRebaseTarget(f, repo)
				}
				pushErr.Conflicts = append(pushErr.Conflicts, TweakRepoConflict{
					RepoName:     name,
					Branch:       branch,
					RebaseTarget: rebaseTarget,
				})
				continue
			case ports.PullRebaseFailure:
				pushErr.Failures = append(pushErr.Failures, TweakRepoFailure{
					RepoName: name,
					Branch:   branch,
					Err:      fmt.Errorf("pull rebase: %w", result.Err),
				})
				continue
			}
		}

		if ts.Publisher != nil {
			if err := ts.Publisher.Push(worktree, branch); err != nil {
				pushErr.Failures = append(pushErr.Failures, TweakRepoFailure{
					RepoName: name,
					Branch:   branch,
					Err:      fmt.Errorf("push: %w", err),
				})
				continue
			}
		}
	}

	if len(pushErr.Conflicts) == 0 && len(pushErr.Failures) == 0 {
		return nil
	}
	return pushErr
}

// findRepoForTweak returns a pointer to the named FeatureRepo, or nil if
// the name does not match any entry. Local helper to avoid importing the
// orchestrator's findRepo (which lives in a different package).
func findRepoForTweak(f *feature.Feature, name string) *feature.FeatureRepo {
	for i := range f.Repos {
		if f.Repos[i].Name == name {
			return &f.Repos[i]
		}
	}
	return nil
}
