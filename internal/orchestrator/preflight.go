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
	"strings"

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
// targets. A publishable repository without a recorded branch has no
// destination: the caller reports an error naming the repository instead of
// fabricating a name.
func completionDestinationRef(repo feature.FeatureRepo, publishable bool) string {
	if !publishable {
		return repo.BaseBranch
	}
	if repo.Branch == "" {
		return ""
	}
	return "origin/" + repo.Branch
}

// commitBodiesRangeSeparator mirrors the per-commit separator of
// git.CommitBodiesRange's format string. Counting its occurrences counts the
// commits in a range without a dedicated rev-list helper in the git package.
const commitBodiesRangeSeparator = "---commit---"

// applyPendingDelivery folds undelivered-work measurements into a repository's
// preflight result and distinguishes a stale pull request or base branch from a
// delivered one. A publishable repository on a stacked run is measured per
// delivery layer by applyStackPendingDelivery; every other shape (merge
// delivery, pre-stack runs) keeps the single-destination measurement. An
// unresolvable destination leaves the result untouched.
func (o *Orchestrator) applyPendingDelivery(f *feature.Feature, repo feature.FeatureRepo, result CompletionRepoResult) CompletionRepoResult {
	if result.Publishable && len(f.Stack) > 0 {
		return o.applyStackPendingDelivery(f, repo, result)
	}
	dest := completionDestinationRef(repo, result.Publishable)
	work, ok := git.PendingAgainst(repoWorkDir(repo), dest)
	if !ok {
		return result
	}
	result.PendingCommits = work.Commits
	result.PendingDirty = work.Dirty
	if work.Dirty {
		result = o.enumeratePendingDirtyFiles(repoWorkDir(repo), result)
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

// applyStackPendingDelivery measures a publishable repository's undelivered
// work layer by layer. A layer "has commits" when its recorded tip reaches
// past the lower cut point — the previous layer's tip, or the resolved base
// for layer 1 (remote-tracking base preferred, as today); layers marked
// NoCommits or without a recorded tip are settled, mirroring the publish
// walk's emptiness rule so the two readers never disagree. The pending count
// sums each unpushed layer's commits (the full layer range when it never
// pushed); the push mode is rewrite when any layer's remote branch holds
// commits its tip does not contain. The PR URL keeps coming from the legacy
// projection the caller already filled in. An unresolvable base mirrors the
// legacy unresolved-destination contract: the pending fields stay untouched
// rather than guessing.
func (o *Orchestrator) applyStackPendingDelivery(f *feature.Feature, repo feature.FeatureRepo, result CompletionRepoResult) CompletionRepoResult {
	workDir := repoWorkDir(repo)
	if workDir == "" {
		return result
	}
	baseSHA, baseErr := resolveBaseCutSHA(workDir, repo.BaseBranch)
	if baseErr != nil {
		return result
	}
	dirty := git.HasUncommittedChanges(workDir)
	layers := orderedStackLayers(f)
	entries := make(map[int]feature.StackRepoEntry, len(layers))
	for _, layer := range layers {
		entries[layer.Position] = layer.Repos[repo.Name]
	}
	pending := false
	pendingCommits := 0
	rewrite := false
	for i, layer := range layers {
		entry := entries[layer.Position]
		if entry.NoCommits || entry.TipSHA == "" {
			continue
		}
		cut := baseSHA
		if i > 0 {
			cut = entries[layers[i-1].Position].TipSHA
		}
		if cut == "" || !git.HasCommitsBeyond(workDir, entry.TipSHA, cut) {
			continue
		}
		if entry.PRURL == "" || entry.TipSHA != entry.LastPushedSHA {
			pending = true
			lowerBound := entry.LastPushedSHA
			if lowerBound == "" {
				lowerBound = cut
			}
			pendingCommits += stackRangeCommitCount(workDir, lowerBound, entry.TipSHA)
		}
		if remoteSHA, readErr := git.ReadRefSHA(workDir, "refs/remotes/origin/"+layer.Branch); readErr == nil &&
			remoteSHA != "" && !git.IsAncestor(workDir, remoteSHA, entry.TipSHA) {
			rewrite = true
		}
	}
	result.PendingCommits = pendingCommits
	result.PendingDirty = dirty
	if dirty {
		result = o.enumeratePendingDirtyFiles(workDir, result)
	}
	if result.PRURL != "" {
		result.PushMode = completionPushModeFastForward
		if rewrite {
			result.PushMode = completionPushModeRewrite
		}
	}
	if !pending && !dirty {
		return result
	}
	switch result.Status {
	case completionStatusAlreadyPublished:
		result.Status = completionStatusUnpublishedChanges
	case completionStatusCompleted:
		result.Status = completionStatusUnpublishedChanges
	}
	return result
}

// stackRangeCommitCount counts the commits in lower..tip through the ranged
// commit-body helper: every commit renders exactly one separator, so the
// separator count is the commit count. A failed range read counts as zero —
// the pending signal itself comes from the entry fields, only the count
// degrades.
func stackRangeCommitCount(workDir, lower, tip string) int {
	if lower == "" || tip == "" || lower == tip {
		return 0
	}
	bodies, err := git.CommitBodiesRange(workDir, lower, tip)
	if err != nil {
		return 0
	}
	return strings.Count(bodies, commitBodiesRangeSeparator)
}

// enumeratePendingDirtyFiles fills the bounded dirty-file sample and true
// total. A preflight that cannot enumerate must not claim there are no files:
// when Worktrees is unset or InspectCleanliness errors, both fields stay
// zero-valued and PendingDirty alone carries the signal.
func (o *Orchestrator) enumeratePendingDirtyFiles(workDir string, result CompletionRepoResult) CompletionRepoResult {
	if o.deps.Worktrees == nil {
		return result
	}
	report, err := o.deps.Worktrees.InspectCleanliness(workDir, feature.DefaultDirtyPathLimit)
	if err != nil || report == nil {
		return result
	}
	all := append(append(append([]string{}, report.Staged...), report.Unstaged...), report.Untracked...)
	// A path staged and further modified (MM) is reported by both
	// categories; dedupe so it is neither listed nor counted twice.
	deduped := dedupePreservingOrder(all)
	result.PendingDirtyFiles = deduped
	total := report.StagedTotal + report.UnstagedTotal + report.UntrackedTotal
	result.PendingDirtyFileTotal = total - (len(all) - len(deduped))
	return result
}

// repoStackSettled reports whether one repository's delivery is settled under
// the run's stack — every layer's entry either carries a pull request or is
// marked NoCommits, the per-repository slice of Feature.AllReposPublished.
// Runs without a stack (approved before the `## Pull Requests` table) keep the
// legacy rule: the per-repo PR URL alone settles the repository.
func repoStackSettled(f *feature.Feature, repoName, legacyPRURL string) bool {
	if f == nil || len(f.Stack) == 0 {
		return legacyPRURL != ""
	}
	for _, layer := range f.Stack {
		entry, ok := layer.Repos[repoName]
		if !ok || (entry.PRURL == "" && !entry.NoCommits) {
			return false
		}
	}
	return true
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
