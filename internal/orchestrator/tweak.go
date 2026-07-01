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

// Package orchestrator wires the feature-level interactive tweak cycle. One
// Claude session is opened with --add-dir for every Feature.Repos worktree,
// the agent does not commit, and the orchestrator commits every modified repo
// at session end. Auto-publish publishable features also pull-rebase and push;
// manual-publish or local-only features stop after local commits. Pull-rebase
// conflicts on any repo surface a structured PublishConflictError so the TUI
// can route the affected repo into a rebase cycle.
//
// Lifecycle:
//
//   - StartTweak(featureID) — stamps Feature.ActiveCycle = {Type: tweak,
//     Status: running}, increments TweakCount, dispatches the session
//     via PhaseRunner.RunTweakSession with the feature-level workspace.
//   - CompleteTweakCommit(featureID) — iterates every Feature.Repos and
//     commits any worktree with uncommitted changes.
//   - CompleteTweakFinish(featureID, hadChanges) — commits any review-fix
//     leftovers, then either completes locally (manual/local-only) or
//     pull-rebases and pushes each modified branch (auto-publish). A
//     PullRebaseConflict in any repo surfaces a *PublishConflictError for
//     that repo and routes to a rebase cycle.
//   - FailTweakSession(featureID) — marks the cycle failed (session-die
//     mid-tweak path).
//   - RestoreTweakFromReview(featureID) — Esc on the post-commit Final
//     Review modal: clears Feature.ActiveCycle without pushing.
package orchestrator

import (
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// StartTweak launches a feature-level interactive tweak session. One
// Claude PTY session is opened with cwd at the feature state dir and
// --add-dir for every Feature.Repos worktree. There is no iteration
// counter, no review gate, and no testing contract — the user drives.
//
// Stamps Feature.ActiveCycle = {Type: tweak, Status: running}, increments
// TweakCount, and dispatches the session via PhaseRunner.RunTweakSession.
// Per-repo cycle entries are also opened so the TUI rendering surface
// (RepoCycles[name].Status == "running") stays in sync while the feature-level
// session runs.
//
// Returns the session ID on success. Returns ("", err) on dispatch
// failure; the caller does NOT need to clean up state — Failures route
// through the TweakSession deep module's MarkFailed and the
// per-repo FailRepoCycle entries.
func (o *Orchestrator) StartTweak(featureID string) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	pr := o.deps.PhaseRunner
	if pr == nil {
		return "", errors.New("phase runner not configured")
	}

	ts := o.tweakSession()

	// Bump TweakCount + stamp ActiveCycle synchronously so the TUI sees
	// the running cycle the moment this method returns. The deep module's
	// MarkRunning is idempotent on retry: a re-entrant call against an
	// already-running tweak short-circuits without double-bumping.
	updated, err := ts.MarkRunning(featureID)
	if err != nil {
		return "", fmt.Errorf("mark running: %w", err)
	}
	if updated != nil {
		f = updated
	}

	// Open per-repo cycle entries for every Feature.Repos. The tweak session is
	// feature-level, but TUI rendering still reads RepoCycles[name].Status while
	// the session is running.
	for i := range f.Repos {
		_ = o.deps.Lifecycle.StartRepoCycle(featureID, f.Repos[i].Name, feature.CycleTweak)
	}
	// Reload so per-repo cycle Counts reflect the StartRepoCycle writes.
	f, err = o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("reload feature: %w", err)
	}

	// Resolve the workspace via the WorkspaceSetup deep module. Cwd is
	// the feature state dir; AdditionalDirs covers every Feature.Repos
	// worktree. This matches what the rebase / refactor / final-review
	// loops do, and is the canonical source of truth for unified-flow
	// session bootstrap.
	workspace, err := agent.BuildWorkspace(f, o.stateDir())
	if err != nil {
		_ = ts.MarkFailed(featureID, "workspace setup: "+err.Error())
		return "", fmt.Errorf("build workspace: %w", err)
	}

	// AdditionalDirs[0] is the cwd; the session command's --add-dir set
	// must skip the cwd (BuildSession adds it implicitly).
	additionalDirs := workspace.AdditionalDirs
	if len(additionalDirs) > 0 && additionalDirs[0] == workspace.Cwd {
		additionalDirs = additionalDirs[1:]
	}

	sessionID, err := pr.RunTweakSession(f, agent.TweakSessionConfig{
		WorkDir:        workspace.Cwd,
		AdditionalDirs: additionalDirs,
	})
	if err != nil {
		_ = ts.MarkFailed(featureID, "run session: "+err.Error())
		// Mirror the per-repo FailRepoCycle for legacy TUI rendering.
		for i := range f.Repos {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, "tweak run session: "+err.Error())
		}
		return "", fmt.Errorf("run tweak session: %w", err)
	}
	if sm := o.deps.Sessions; sm != nil {
		if sess := sm.GetSession(sessionID); sess == nil {
			errMsg := "tweak session not found after start"
			_ = ts.MarkFailed(featureID, errMsg)
			for i := range f.Repos {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, errMsg)
			}
			return "", errors.New(errMsg)
		}
	}
	return sessionID, nil
}

// CompleteTweakCommit iterates every Feature.Repos and commits any
// worktree with uncommitted changes. Returns hadChanges == true when at
// least one repo's worktree had uncommitted changes that were committed.
// The orchestrator uses the boolean to decide whether the post-session
// flow needs the push step (CompleteTweakFinish).
//
// On commit failure: the deep module's MarkFailed records the error on
// Feature.ActiveCycle and per-repo FailRepoCycle clears the legacy TUI
// rendering surface. The caller (TUI) sees the error and presents the
// failed cycle; no partial completion path opens.
func (o *Orchestrator) CompleteTweakCommit(featureID string) (bool, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		_ = o.tweakSession().MarkFailed(featureID, "complete tweak commit load: "+err.Error())
		return false, fmt.Errorf("loading feature: %w", err)
	}

	ts := o.tweakSession()
	repoChanges, err := ts.CommitAll(f)
	if err != nil {
		_ = ts.MarkFailed(featureID, "tweak commit: "+err.Error())
		// Mirror the per-repo FailRepoCycle for legacy TUI rendering.
		for i := range f.Repos {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, "tweak commit: "+err.Error())
		}
		return false, fmt.Errorf("commit: %w", err)
	}

	hadChanges := false
	for _, v := range repoChanges {
		if v {
			hadChanges = true
			break
		}
	}
	return hadChanges, nil
}

// CompleteTweakFinish finalizes a feature-level tweak. When hadChanges
// is true (CompleteTweakCommit recorded at least one modified repo),
// commits any review-fix leftovers. Manual-publish or local-only features
// then complete locally; auto-publish publishable features continue through
// the pull-rebase + push chain.
//
// A PullRebase conflict in any repo surfaces a *PublishConflictError for
// the FIRST conflicted repo (in alphabetical order), so the TUI's
// existing handleRebaseRepoCycleResult routes that repo into a fresh
// CycleRebase. Other clean-pushed repos are committed and pushed
// independently. The cycle entry for the conflicted repo is marked
// failed so a follow-up CycleRebase can take over without StartRepoCycle
// blocking on a still-running entry.
//
// On full success: clears Feature.ActiveCycle and CompleteRepoCycle for
// every repo. On any push failure: deep module records the error on
// Feature.ActiveCycle and the per-repo FailRepoCycle clears the legacy
// TUI rendering surface for the affected repos.
func (o *Orchestrator) CompleteTweakFinish(featureID string, hadChanges bool) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		_ = o.tweakSession().MarkFailed(featureID, "complete tweak finish: "+err.Error())
		return fmt.Errorf("load feature: %w", err)
	}

	ts := o.tweakSession()

	// Step 1: when the post-commit Final Review fix path leaves
	// uncommitted changes (rare — the fix agent usually commits via the
	// implement-loop pathway), commit them in every repo. We re-use
	// CommitAll here because the iteration shape matches step-1 commit
	// at session end.
	repoChanges := map[string]bool{}
	if hadChanges {
		repoChanges, err = ts.CommitAll(f)
		if err != nil {
			_ = ts.MarkFailed(featureID, "tweak review commit: "+err.Error())
			for i := range f.Repos {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, "tweak review commit: "+err.Error())
			}
			return fmt.Errorf("commit review fixes: %w", err)
		}
		// hadChanges=true is the input gate; if no repo had uncommitted
		// changes at this step (the commit happened in step 1 already),
		// PushAll still needs the per-repo "modified" list. Preserve the
		// legacy behavior: when hadChanges=true, every repo with a non-
		// empty worktree is a candidate for push (the commit may have
		// happened in step 1).
		for i := range f.Repos {
			repo := &f.Repos[i]
			worktree := repo.WorktreePath
			if worktree == "" {
				worktree = repo.Path
			}
			if worktree == "" {
				continue
			}
			repoChanges[repo.Name] = true
		}
	}

	if !f.Checkpoints.AutoPublish() || !f.IsPublishable() {
		return o.completeTweakCycle(featureID, f, ts)
	}

	// Step 2: pull-rebase + push every modified repo. A pull-rebase
	// conflict on any repo surfaces a *PublishConflictError so the TUI
	// can route that repo into a fresh CycleRebase. Non-conflict failures
	// surface as plain errors per repo.
	pushErr := ts.PushAll(f, repoChanges)
	if pushErr != nil {
		var ftpe *agent.FeatureTweakPushError
		if errors.As(pushErr, &ftpe) {
			// Conflicts: surface the FIRST conflicted repo as a
			// PublishConflictError so the TUI routes it through the
			// rebase-resolution UX. Mark the cycle failed for every
			// conflicted/failed repo so a follow-up CycleRebase /
			// retry path can take over without StartRepoCycle blocking
			// on a still-running entry.
			for _, c := range ftpe.Conflicts {
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, c.RepoName, "tweak pull-rebase conflict")
			}
			for _, fl := range ftpe.Failures {
				errMsg := "tweak push: "
				if fl.Err != nil {
					errMsg += fl.Err.Error()
				}
				_ = o.deps.Lifecycle.FailRepoCycle(featureID, fl.RepoName, errMsg)
			}
			_ = ts.MarkFailed(featureID, ftpe.Error())
			if len(ftpe.Conflicts) > 0 {
				c := ftpe.Conflicts[0]
				return &PublishConflictError{
					RepoName:     c.RepoName,
					Branch:       c.Branch,
					RebaseTarget: c.RebaseTarget,
				}
			}
			// No conflicts but at least one non-conflict failure: surface
			// the first failure's wrapped error.
			if len(ftpe.Failures) > 0 {
				return ftpe.Failures[0].Err
			}
		}
		_ = ts.MarkFailed(featureID, pushErr.Error())
		return pushErr
	}

	// Step 3: every modified repo pushed cleanly. Clear ActiveCycle and
	// per-repo cycle entries so the feature returns to its steady state.
	return o.completeTweakCycle(featureID, f, ts)
}

func (o *Orchestrator) completeTweakCycle(featureID string, f *feature.Feature, ts *agent.TweakSession) error {
	for i := range f.Repos {
		if err := o.deps.Lifecycle.CompleteRepoCycle(featureID, f.Repos[i].Name); err != nil {
			_ = ts.MarkFailed(featureID, "tweak complete repo cycle: "+err.Error())
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, "tweak complete repo cycle: "+err.Error())
			return fmt.Errorf("complete repo cycle: %w", err)
		}
	}
	if err := ts.ClearActiveCycle(featureID); err != nil {
		return fmt.Errorf("clear active cycle: %w", err)
	}
	return nil
}

// FailTweakSession is the orchestrator-level entry the TUI calls when a
// tweak PTY session exits unsuccessfully (process-died-mid-tweak path).
// Marks Feature.ActiveCycle as failed via the deep module and clears
// every per-repo cycle entry so the TUI presents the failed cycle.
func (o *Orchestrator) FailTweakSession(featureID string) error {
	f, _ := o.deps.Lifecycle.Get(featureID)
	if err := o.tweakSession().MarkFailed(featureID, "tweak session failed"); err != nil {
		return err
	}
	if f != nil {
		for i := range f.Repos {
			_ = o.deps.Lifecycle.FailRepoCycle(featureID, f.Repos[i].Name, "tweak session failed")
		}
	}
	return nil
}

// RestoreTweakFromReview is the Esc path on the post-commit Final Review
// modal. The user pressed Esc instead of y/n, meaning they want to
// abandon the modal without pushing. Clear Feature.ActiveCycle so the
// feature returns to its steady state; the per-repo cycle entries are
// removed via the deep module's ClearActiveCycle + per-repo
// RemoveRepoCycle.
func (o *Orchestrator) RestoreTweakFromReview(featureID string) error {
	f, _ := o.deps.Lifecycle.Get(featureID)
	if f != nil {
		for i := range f.Repos {
			_ = o.deps.Lifecycle.RemoveRepoCycle(featureID, f.Repos[i].Name)
		}
	}
	return o.tweakSession().ClearActiveCycle(featureID)
}

// tweakSession constructs a TweakSession with the orchestrator's wired
// Deps. Each call returns a fresh value-typed instance — the deep module
// has no internal mutable state.
func (o *Orchestrator) tweakSession() *agent.TweakSession {
	return &agent.TweakSession{
		Store:               o.deps.Store,
		Publisher:           o.deps.Publisher,
		Rebaser:             o.deps.Rebaser,
		ResolveRebaseTarget: o.resolveRebaseTarget,
	}
}
