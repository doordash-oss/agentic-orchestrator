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

// Package orchestrator — preflight.go owns side-effect-free, server-authored
// preflight helpers. The desktop never reconstructs lifecycle or Git rules:
// it renders the preview, and execution rejects a stale source_revision
// before any mutation.
package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const (
	preflightFreshnessBehind     = "behind"
	preflightFreshnessUpToDate   = "up_to_date"
	preflightFreshnessUnknown    = "unknown"
	preflightBlockerNoTarget     = "rebase target not found"
	preflightBlockerNoWorktree   = "worktree not available"
	preflightBlockerRebaseInProg = "rebase already in progress"
)

// repoFreshnessAndBlocker is the single source of truth for the
// side-effect-free freshness/blocker decision shared by CompletionPreflight.
// It inspects only local remote-tracking refs and never mutates a worktree. It
// returns the freshness label, a non-empty blocker when the repo cannot be
// safely operated on, and the behind flag.
func (o *Orchestrator) repoFreshnessAndBlocker(out RebaseRepoFreshnessInput) (freshness, blocker string, behind bool) {
	switch {
	case out.WorktreePath == "":
		return preflightFreshnessUnknown, preflightBlockerNoWorktree, false
	case out.RebaseTarget == "":
		return preflightFreshnessUnknown, preflightBlockerNoTarget, false
	default:
		if git.RebaseInProgress(out.WorktreePath) {
			return preflightFreshnessUnknown, preflightBlockerRebaseInProg, false
		}
		if out.Publishable {
			b := git.IsBehindRemote(out.WorktreePath, out.RebaseTarget)
			if b {
				return preflightFreshnessBehind, "", true
			}
			return preflightFreshnessUpToDate, "", false
		}
		b := git.IsBehindLocal(out.WorktreePath, out.RebaseTarget)
		if b {
			return preflightFreshnessBehind, "", true
		}
		return preflightFreshnessUpToDate, "", false
	}
}

// collectPreflightFingerprints folds every repository's worktree fingerprint
// into the stable list the stale-preflight guard hashes. repo.Name is the
// single name source for both the preview and the execution-time guard, so
// the two paths cannot drift.
func (o *Orchestrator) collectPreflightFingerprints(f *feature.Feature) []string {
	var fingerprints []string
	for _, repo := range f.Repos {
		if fp := o.rebaseWorktreeFingerprint(repo); fp != "" {
			fingerprints = append(fingerprints, repo.Name+"\n"+fp)
		}
	}
	return fingerprints
}

// preflightRevision folds the per-repository worktree fingerprints into one
// stable revision string. The exact value is opaque; only equality matters for
// the stale guard.
func preflightRevision(fingerprints []string) string {
	if len(fingerprints) == 0 {
		return ""
	}
	return revisionHash(fingerprints)
}

// revisionHash returns a short, stable hex digest of the joined inputs.
func revisionHash(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ErrStalePreflight is returned by cycle start paths when the carried source
// revision no longer matches the authoritative repository state.
var ErrStalePreflight = errors.New("preflight is stale: repository state changed since the preview, refresh and try again")

// completionDestinationRef is the ref a repository's work is delivered to: the
// remote branch behind its pull request, or the local base branch a merge
// targets.
func completionDestinationRef(f *feature.Feature, repo feature.FeatureRepo, publishable bool) string {
	if !publishable {
		return repo.BaseBranch
	}
	if branch := repoBranch(f, repo); branch != "" {
		return "origin/" + branch
	}
	return ""
}

// applyPendingDelivery folds undelivered-work measurements into a repository's
// preflight result and distinguishes a stale pull request or base branch from a
// delivered one. An unresolvable destination leaves the result untouched.
func (o *Orchestrator) applyPendingDelivery(f *feature.Feature, repo feature.FeatureRepo, result CompletionRepoResult) CompletionRepoResult {
	dest := completionDestinationRef(f, repo, result.Publishable)
	work, ok := git.PendingAgainst(repoWorkDir(repo), dest)
	if !ok {
		return result
	}
	result.PendingCommits = work.Commits
	result.PendingDirty = work.Dirty
	// A preflight that cannot enumerate must not claim there are no files:
	// when Worktrees is unset or InspectCleanliness errors, both fields stay
	// zero-valued and PendingDirty alone carries the signal.
	if work.Dirty && o.deps.Worktrees != nil {
		if report, err := o.deps.Worktrees.InspectCleanliness(repoWorkDir(repo), feature.DefaultDirtyPathLimit); err == nil && report != nil {
			all := append(append(append([]string{}, report.Staged...), report.Unstaged...), report.Untracked...)
			// A path staged and further modified (MM) is reported by both
			// categories; dedupe so it is neither listed nor counted twice.
			deduped := dedupePreservingOrder(all)
			result.PendingDirtyFiles = deduped
			total := report.StagedTotal + report.UnstagedTotal + report.UntrackedTotal
			result.PendingDirtyFileTotal = total - (len(all) - len(deduped))
		}
	}
	if result.Publishable && result.PRURL != "" {
		result.PushMode = completionPushModeFastForward
		if work.DestinationAhead > 0 {
			result.PushMode = completionPushModeRewrite
		}
	}
	if !work.Pending() {
		return result
	}
	switch result.Status {
	case completionStatusAlreadyPublished:
		result.Status = completionStatusUnpublishedChanges
	case completionStatusCompleted:
		if result.Publishable {
			result.Status = completionStatusUnpublishedChanges
		} else {
			result.Status = completionStatusUnmergedChanges
		}
	}
	return result
}

// dedupePreservingOrder drops repeated entries, keeping each one's first
// occurrence position — used because a staged-and-further-modified (MM) path
// is reported by both the staged and unstaged categories.
func dedupePreservingOrder(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
