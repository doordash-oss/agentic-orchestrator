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
	"fmt"
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

// liveStackPRStates reads every stack layer pull request's live state for one
// publishable repository through the remote operations — the completion
// preflight's status refresh. A determinate merged or closed state is
// persisted onto the run's stack through the lifecycle, monotonically: only
// merged or closed is ever written, so a recorded merged or closed entry can
// never be downgraded, and the write is a no-op when the entry already records
// it. The returned map carries the live state (open, merged, or closed) for
// every layer whose lookup was determinate; an indeterminate lookup (error or
// unrecognised state) is omitted so the recorded state stands. A
// closed-unmerged pull request found here is observed and persisted only —
// blocker parking for it stays with Phase 15.
func (o *Orchestrator) liveStackPRStates(f *feature.Feature, repo feature.FeatureRepo, publishable bool) map[int]feature.StackPRState {
	if !publishable || len(f.Stack) == 0 {
		return nil
	}
	workDir := repoWorkDir(repo)
	if workDir == "" {
		return nil
	}
	var live map[int]feature.StackPRState
	for _, layer := range orderedStackLayers(f) {
		entry, hasEntry := layer.Repos[repo.Name]
		if !hasEntry || entry.PRURL == "" {
			continue
		}
		state, err := o.deps.Remote.PRState(workDir, entry.PRURL)
		if err != nil {
			// Indeterminate: the recorded state stands and no hint is
			// derived from this lookup.
			continue
		}
		switch state {
		case git.PRStateMerged:
			if entry.PRState != feature.StackPRStateMerged {
				_ = o.deps.Lifecycle.SetStackLayerPRState(f.ID, repo.Name, layer.Position, feature.StackPRStateMerged)
			}
		case git.PRStateClosed:
			if entry.PRState != feature.StackPRStateClosed {
				_ = o.deps.Lifecycle.SetStackLayerPRState(f.ID, repo.Name, layer.Position, feature.StackPRStateClosed)
			}
		case git.PRStateOpen:
			// Open never downgrades a recorded merged or closed entry.
		default:
			continue
		}
		if live == nil {
			live = make(map[int]feature.StackPRState)
		}
		live[layer.Position] = feature.StackPRState(state)
	}
	return live
}

// applyPendingDelivery folds undelivered-work measurements into a repository's
// preflight result and distinguishes a stale pull request or base branch from a
// delivered one. A publishable repository on a stacked run is measured per
// delivery layer by applyStackPendingDelivery, which also fills the
// per-layer pull-request entries with their push modes; every other shape
// (merge delivery, pre-stack runs) keeps the single-destination measurement
// and carries no entries. An unresolvable destination leaves the result
// untouched.
func (o *Orchestrator) applyPendingDelivery(f *feature.Feature, repo feature.FeatureRepo, result CompletionRepoResult, liveStates map[int]feature.StackPRState) CompletionRepoResult {
	if result.Publishable && len(f.Stack) > 0 {
		return o.applyStackPendingDelivery(f, repo, result, liveStates)
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
// work layer by layer and fills the per-layer pull-request entries with
// their push modes. A layer "has commits" when its recorded tip reaches
// past the lower cut point — the previous layer's tip, or the resolved base
// for layer 1 (remote-tracking base preferred, as today); the same rule
// decides the live no-commits marker, mirroring the publish walk's
// emptiness rule so the preview is exact. Each entry's state is the live
// pull-request state when the lookup was determinate (a recorded merged or
// closed entry is never downgraded to open), else the recorded state. The
// pending count sums each unpushed layer's commits (the full layer range
// when it never pushed); the repository-level push mode is rewrite when any
// layer's remote branch holds commits its tip does not contain, else
// fast_forward, present only when some layer carries a pull request. A
// merged layer whose entry still holds a tip below a kept layer with commits
// sets the rebase hint naming that merged layer. An unresolvable base
// mirrors the legacy unresolved-destination contract: the pending fields and
// entries stay untouched rather than guessing.
func (o *Orchestrator) applyStackPendingDelivery(f *feature.Feature, repo feature.FeatureRepo, result CompletionRepoResult, liveStates map[int]feature.StackPRState) CompletionRepoResult {
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
	anyPR := false
	preflightEntries := make([]CompletionPullRequestEntry, 0, len(layers))
	// Per-layer shape for the rebase hint: a merged layer whose entry still
	// holds a tip, and a kept (not merged, not closed) layer with commits.
	mergedWithTip := make([]bool, len(layers))
	keptWithCommits := make([]bool, len(layers))
	for i, layer := range layers {
		entry := entries[layer.Position]
		cut := baseSHA
		if i > 0 {
			cut = entries[layers[i-1].Position].TipSHA
		}
		// The live no-commits marker uses the publish walk's rule — the
		// tip does not reach past the lower cut point — so a not-yet-
		// published empty layer is already marked here.
		hasCommits := git.HasCommitsBeyond(workDir, entry.TipSHA, cut)
		live, liveKnown := liveStates[layer.Position]
		state := stackPreflightPRStateWithLive(entry, live, liveKnown)
		pushedUpToDate := entry.PRURL != "" && entry.TipSHA == entry.LastPushedSHA
		pushMode := completionPushModeNone
		switch {
		case !hasCommits:
			pushMode = completionPushModeNone
		case entry.PRURL == "":
			pushMode = completionPushModeCreate
		case state == string(feature.StackPRStateMerged) || state == string(feature.StackPRStateClosed):
			pushMode = completionPushModeNone
		case entry.TipSHA == entry.LastPushedSHA:
			pushMode = completionPushModeNone
		default:
			pushMode = completionPushModeFastForward
			if remoteSHA, readErr := git.ReadRefSHA(workDir, "refs/remotes/origin/"+layer.Branch); readErr == nil &&
				remoteSHA != "" && !git.IsAncestor(workDir, remoteSHA, entry.TipSHA) {
				pushMode = completionPushModeRewrite
			}
		}
		if entry.PRURL != "" {
			anyPR = true
			if pushMode == completionPushModeRewrite {
				rewrite = true
			}
		}
		if hasCommits && (entry.PRURL == "" || entry.TipSHA != entry.LastPushedSHA) {
			pending = true
			lowerBound := entry.LastPushedSHA
			if lowerBound == "" {
				lowerBound = cut
			}
			pendingCommits += stackRangeCommitCount(workDir, lowerBound, entry.TipSHA)
		}
		preflightEntries = append(preflightEntries, CompletionPullRequestEntry{
			Position:       layer.Position,
			Title:          layer.Title,
			Branch:         layer.Branch,
			URL:            entry.PRURL,
			State:          state,
			NoCommits:      !hasCommits,
			PushedUpToDate: pushedUpToDate,
			PushMode:       pushMode,
		})
		mergedWithTip[i] = state == string(feature.StackPRStateMerged) && entry.TipSHA != ""
		keptWithCommits[i] = state != string(feature.StackPRStateMerged) &&
			state != string(feature.StackPRStateClosed) && hasCommits
	}
	// The rebase hint: the lowest merged layer whose entry still holds a tip
	// with kept work with commits above it — the chain above a merged base
	// must be restacked onto that base, so the repository reads behind even
	// when the local remote-tracking comparison reports up to date.
	for i, layer := range layers {
		if !mergedWithTip[i] {
			continue
		}
		keptAbove := false
		for j := i + 1; j < len(layers); j++ {
			if keptWithCommits[j] {
				keptAbove = true
				break
			}
		}
		if keptAbove {
			result.RebaseHint = fmt.Sprintf(
				"Layer %d (%s) is merged below kept work — run the rebase pass to restack the layers above.",
				layer.Position, layer.Title)
			break
		}
	}
	result.PullRequests = preflightEntries
	result.PendingCommits = pendingCommits
	result.PendingDirty = dirty
	if dirty {
		result = o.enumeratePendingDirtyFiles(workDir, result)
	}
	if anyPR {
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

// stackPreflightPRState normalizes a recorded layer entry's pull-request
// state for the preflight entries, matching the feature read model: no
// pull request URL means "none", and a URL without a recorded state reads
// as open.
func stackPreflightPRState(entry feature.StackRepoEntry) string {
	if entry.PRURL == "" {
		return string(feature.StackPRStateNone)
	}
	switch entry.PRState {
	case feature.StackPRStateOpen, feature.StackPRStateMerged, feature.StackPRStateClosed:
		return string(entry.PRState)
	default:
		return string(feature.StackPRStateOpen)
	}
}

// stackPreflightPRStateWithLive folds a determinate live lookup into a layer
// entry's preflight state. The live state wins except that a live open never
// downgrades a recorded merged or closed entry — merged and closed are
// monotonic remote facts. An indeterminate lookup (ok false) keeps the
// recorded state.
func stackPreflightPRStateWithLive(entry feature.StackRepoEntry, live feature.StackPRState, liveKnown bool) string {
	if entry.PRURL == "" {
		return string(feature.StackPRStateNone)
	}
	if liveKnown {
		switch live {
		case feature.StackPRStateMerged, feature.StackPRStateClosed:
			return string(live)
		case feature.StackPRStateOpen:
			if entry.PRState != feature.StackPRStateMerged && entry.PRState != feature.StackPRStateClosed {
				return string(feature.StackPRStateOpen)
			}
		}
	}
	return stackPreflightPRState(entry)
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
// A run without a stack is never settled: publish fails closed for it, so
// its repositories can never deliver.
func repoStackSettled(f *feature.Feature, repoName string) bool {
	if f == nil || len(f.Stack) == 0 {
		return false
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
