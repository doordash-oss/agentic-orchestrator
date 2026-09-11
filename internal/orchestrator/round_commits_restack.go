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
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// roundCommitTarget is one commit the round-commit hook created, and the
// stack layer position it was created for.
type roundCommitTarget struct {
	SHA   string
	Layer int
}

// manifestEntryIgnoredReasons are the canonical reasons a fix manifest entry
// is ignored, surfaced through the FixManifestEntryIgnored warning.
const (
	manifestReasonInvalidPosition = "invalid position"
	manifestReasonUnknownRepo     = "unknown repository"
	manifestReasonPathNotChanged  = "path not changed"
	manifestReasonDuplicatePath   = "duplicate path"
	manifestReasonUnparsable      = "unparsable manifest"
)

// partitionRoundPaths assigns one repository's dirty paths to stack layers
// using the fixer's manifest: for each valid entry naming this repository
// and an existing layer, the listed paths that are dirty go to that layer;
// invalid positions, non-dirty paths, and lower duplicates are reported
// through the FixManifestEntryIgnored warning, and every unassigned dirty
// path — including the second half of a renamed path whose pair was listed —
// goes to the top layer. The returned map carries one path list per target
// layer position; hasLower reports whether any path landed below the top.
func (o *Orchestrator) partitionRoundPaths(f *feature.Feature, featureID string, manifest agent.FixManifest, repoName, worktreePath string) (map[int][]string, bool, error) {
	dirty, counterparts, err := git.UncommittedPaths(worktreePath)
	if err != nil {
		return nil, false, err
	}
	dirtySet := make(map[string]bool, len(dirty))
	for _, p := range dirty {
		dirtySet[p] = true
	}
	assign := make(map[string]int)
	for _, entry := range manifest.Entries {
		if entry.Repository != repoName {
			continue
		}
		if _, ok := stackLayerByPosition(f.Stack, entry.Layer); !ok {
			o.emitManifestIgnoredWarning(featureID, repoName, entry.Layer, "", manifestReasonInvalidPosition, "")
			continue
		}
		for _, p := range entry.Paths {
			if !dirtySet[p] {
				o.emitManifestIgnoredWarning(featureID, repoName, entry.Layer, p, manifestReasonPathNotChanged, "")
				continue
			}
			if prev, exists := assign[p]; exists {
				switch {
				case entry.Layer > prev:
					// The highest listed valid layer wins.
					assign[p] = entry.Layer
					o.emitManifestIgnoredWarning(featureID, repoName, prev, p, manifestReasonDuplicatePath, "")
				case entry.Layer < prev:
					o.emitManifestIgnoredWarning(featureID, repoName, entry.Layer, p, manifestReasonDuplicatePath, "")
				}
				continue
			}
			assign[p] = entry.Layer
		}
	}
	// A rename's unlisted half follows its pair so a relocated rename
	// carries both halves in one commit.
	for _, p := range dirty {
		if _, exists := assign[p]; exists {
			continue
		}
		if pair, ok := counterparts[p]; ok {
			if pos, assigned := assign[pair]; assigned {
				assign[p] = pos
			}
		}
	}
	topPos := topStackLayer(f.Stack).Position
	byLayer := make(map[int][]string)
	hasLower := false
	for _, p := range dirty {
		pos := topPos
		if assigned, ok := assign[p]; ok {
			pos = assigned
		}
		byLayer[pos] = append(byLayer[pos], p)
		if pos != topPos {
			hasLower = true
		}
	}
	return byLayer, hasLower, nil
}

// lowestRequestedLowerLayer returns the lowest layer position the manifest
// validly requests below the top layer for one repository, or 0 when no
// lower layer is requested.
func lowestRequestedLowerLayer(f *feature.Feature, manifest agent.FixManifest, repoName string) int {
	topPos := topStackLayer(f.Stack).Position
	lowest := 0
	for _, entry := range manifest.Entries {
		if entry.Repository != repoName {
			continue
		}
		if _, ok := stackLayerByPosition(f.Stack, entry.Layer); !ok || entry.Layer >= topPos {
			continue
		}
		if lowest == 0 || entry.Layer < lowest {
			lowest = entry.Layer
		}
	}
	return lowest
}

// recordRoundTopLayerTips records the top layer's live tip per repository
// after a round commit of any kind, taking over live tip maintenance from
// the boundary snapshot so the persisted stack stays current for publish.
// Best effort: unreadable refs are skipped and a persistence failure is
// surfaced as a repository-status event, never as a round failure.
func (o *Orchestrator) recordRoundTopLayerTips(featureID string, stack []feature.StackLayer, pending []pendingRepoCommit) {
	if o.deps.Worktrees == nil || len(stack) == 0 {
		return
	}
	top := topStackLayer(stack)
	var splits []repoLayerSplit
	for _, pc := range pending {
		tip, err := o.deps.Worktrees.RefSHA(pc.path, top.Branch)
		if err != nil {
			continue
		}
		splits = append(splits, repoLayerSplit{Repo: pc.name, Tip: tip})
	}
	if len(splits) == 0 {
		return
	}
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		applyLayerTips(ff, top.Position, splits)
		return nil
	}); err != nil {
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: featureID,
			Message:   fmt.Sprintf("recording the top layer's tips after a round commit failed: %v", err),
			Error:     err,
		})
	}
}

// repoRelocation tracks one repository's in-flight fix relocation across
// the per-commit attempts: the anchor is the top layer's tip as it stood
// before this round's commits (remapped after every landing), and the
// remaining commits' SHAs are remapped the same way.
type repoRelocation struct {
	featureID string
	repo      string
	worktree  string
	anchor    string
	commits   []roundCommitTarget
	preTree   string
}

// relocateFinalReviewFixes relocates one repository's non-top fix commits
// into their requested layers, one commit at a time in ascending layer
// order, each with its own independent fallback-upward policy: on a
// structured conflict, any other primitive error, or a top-tree mismatch,
// the attempt retries one layer higher until the top layer, which is a
// no-op. Best effort throughout — nothing here fails the round.
func (o *Orchestrator) relocateFinalReviewFixes(input agent.RoundCommitInput, repoName, worktreePath, anchor string, commits []roundCommitTarget) {
	r := &repoRelocation{
		featureID: input.FeatureID,
		repo:      repoName,
		worktree:  worktreePath,
		anchor:    anchor,
		commits:   commits,
	}
	if o.deps.Worktrees == nil {
		for _, c := range r.commits {
			o.warnFixStayedAtTop(input.FeatureID, repoName, c.Layer, "worktree operations are not configured")
		}
		return
	}
	head, err := o.deps.Worktrees.CurrentHeadSHA(worktreePath)
	if err != nil {
		for _, c := range r.commits {
			o.warnFixStayedAtTop(input.FeatureID, repoName, c.Layer, fmt.Sprintf("reading the pre-relocation top: %v", err))
		}
		return
	}
	preTree, err := o.deps.Worktrees.CommitTreeSHA(worktreePath, head)
	if err != nil {
		for _, c := range r.commits {
			o.warnFixStayedAtTop(input.FeatureID, repoName, c.Layer, fmt.Sprintf("reading the pre-relocation top tree: %v", err))
		}
		return
	}
	r.preTree = preTree
	for _, c := range r.commits {
		f, err := o.deps.Lifecycle.Get(input.FeatureID)
		if err != nil {
			o.warnFixStayedAtTop(input.FeatureID, repoName, c.Layer, fmt.Sprintf("reloading the feature for relocation: %v", err))
			continue
		}
		o.relocateOneCommit(f, r, c)
	}
}

// relocateOneCommit runs the fallback ladder for one fix commit: attempt the
// requested layer, then each layer above, until one lands or the top layer's
// no-op leaves the commit in the top layer's commit range.
func (o *Orchestrator) relocateOneCommit(f *feature.Feature, r *repoRelocation, commit roundCommitTarget) {
	top := topStackLayer(f.Stack)
	requested := commit.Layer
	var lastDiagnostics string
	for pos := requested; pos < top.Position; pos++ {
		outcome, diagnostics := o.attemptRelocateAtLayer(f, r, commit, pos)
		switch outcome {
		case relocationLanded:
			if pos != requested {
				// The landing succeeded above the requested layer: the
				// diagnostics say why the requested layer could not hold it.
				o.emitFixRelocatedWarning(f, r.repo, requested, pos, lastDiagnostics)
			}
			return
		case relocationStayAtTop:
			o.warnFixStayedAtTop(f.ID, r.repo, requested, diagnostics)
			return
		default:
			lastDiagnostics = diagnostics
		}
	}
	// The top layer is a no-op that always succeeds; the commit stays in
	// the top layer's commit range.
	o.warnFixStayedAtTop(f.ID, r.repo, requested, lastDiagnostics)
}

// relocationOutcome classifies one relocation attempt.
type relocationOutcome int

const (
	// relocationRetryHigher: a conflict, another primitive error, or a
	// top-tree mismatch — the attempt retries one layer higher.
	relocationRetryHigher relocationOutcome = iota
	// relocationStayAtTop: a landing failure (journal write, ref
	// transaction, remap persistence) — the commits stay on the top branch
	// and the fix is not retried higher.
	relocationStayAtTop
	// relocationLanded: the rewritten chain is landed and the worktree
	// synced (or left pending sync for recovery).
	relocationLanded
)

// attemptRelocateAtLayer attempts one fix commit's relocation into a single
// layer: build the labelled cut points, insert the commit after that
// layer's tip and drop it from its current position through the restack
// primitive, verify the new top's tree is byte-identical to the
// pre-relocation top tree, and land the chain through the journal, the
// multi-ref transaction, the remap, and the worktree reset.
func (o *Orchestrator) attemptRelocateAtLayer(f *feature.Feature, r *repoRelocation, commit roundCommitTarget, pos int) (relocationOutcome, string) {
	cps, err := o.buildRestackCutPoints(f, r, commit.SHA)
	if err != nil {
		return relocationRetryHigher, err.Error()
	}
	target, ok := cps.LayerLabel[pos]
	if !ok {
		return relocationRetryHigher, fmt.Sprintf("layer %d has no cut point on the chain", pos)
	}
	result, err := o.deps.Worktrees.RestackChain(r.worktree, cps.CutPoints, []git.RestackOp{
		{Kind: git.RestackInsertAfter, CutPointLabel: target, CommitSHAs: []string{commit.SHA}},
		{Kind: git.RestackDropSegment, CutPointLabel: cps.FixLabel},
	})
	if err != nil {
		return relocationRetryHigher, err.Error()
	}
	newTree, err := o.deps.Worktrees.CommitTreeSHA(r.worktree, result.HeadSHA)
	if err != nil {
		return relocationRetryHigher, fmt.Sprintf("reading the replayed top tree: %v", err)
	}
	if newTree != r.preTree {
		return relocationRetryHigher, "the replayed top tree differs from the tree before relocation, so the fix would not have survived the replay"
	}
	return o.landRelocation(f, r, commit, pos, result, cps)
}

// landRelocation persists one computed chain: the journal entry first, then
// the multi-ref transaction, then the remap, then the worktree reset. A
// journal-write or transaction failure leaves the commits on the top branch
// (stay at top); a remap failure keeps the entry for startup recovery; a
// failed reset leaves the entry applied with pending sync.
func (o *Orchestrator) landRelocation(f *feature.Feature, r *repoRelocation, commit roundCommitTarget, pos int, result *git.RestackResult, cps *restackCutPoints) (relocationOutcome, string) {
	lifecycle, ok := o.deps.Lifecycle.(restackJournalLifecycle)
	if !ok {
		return relocationStayAtTop, "the feature lifecycle does not support restack journaling"
	}
	top := topStackLayer(f.Stack)

	var updates []git.RefUpdate
	for layer := pos; layer <= top.Position; layer++ {
		if cps.LayerLabel[layer] == "" && layer != top.Position {
			return relocationStayAtTop, fmt.Sprintf("layer %d has no cut point on the chain", layer)
		}
		stackLayer, ok := stackLayerByPosition(f.Stack, layer)
		if !ok {
			return relocationStayAtTop, fmt.Sprintf("layer %d is not on the approved stack", layer)
		}
		oldSHA, err := o.deps.Worktrees.RefSHA(r.worktree, "refs/heads/"+stackLayer.Branch)
		if err != nil {
			return relocationStayAtTop, fmt.Sprintf("reading the current value of ref %q: %v", stackLayer.Branch, err)
		}
		newSHA := result.HeadSHA
		if layer != top.Position {
			newSHA = cutPointNewSHA(result, cps.LayerLabel[layer])
		}
		updates = append(updates, git.RefUpdate{Ref: "refs/heads/" + stackLayer.Branch, OldSHA: oldSHA, NewSHA: newSHA})
	}

	entry := feature.RestackJournalEntry{
		Repository:     r.repo,
		Updates:        journalUpdatesFromGit(updates),
		State:          feature.RestackJournalPrepared,
		RequestedLayer: commit.Layer,
		NewTopSHA:      result.HeadSHA,
		Remap:          restackRemap(f, r.repo, pos, top.Position, result, cps),
	}
	if err := lifecycle.SetRestackJournalEntry(f.ID, r.repo, entry); err != nil {
		return relocationStayAtTop, fmt.Sprintf("writing the restack journal entry: %v", err)
	}
	if err := o.deps.Worktrees.UpdateRefsTransaction(r.worktree, updates); err != nil {
		_ = lifecycle.DropRestackJournalEntry(f.ID, r.repo)
		return relocationStayAtTop, fmt.Sprintf("landing the rewritten layer refs: %v", err)
	}
	applyErr := lifecycle.ApplyRestackJournalEntry(f.ID, r.repo)
	resetErr := o.deps.Worktrees.ResetToCommit(r.worktree, result.HeadSHA)
	if resetErr != nil {
		if applyErr != nil {
			// The refs landed but the remap did not persist; the prepared
			// entry stays for startup recovery to finish, and the worktree
			// reset failed on top of that.
			return relocationLanded, fmt.Sprintf("persisting the remap failed (%v) and resetting the worktree failed (%v); startup recovery will finish the landing", applyErr, resetErr)
		}
		pending := entry
		pending.State = feature.RestackJournalApplied
		pending.PendingSync = true
		if err := lifecycle.SetRestackJournalEntry(f.ID, r.repo, pending); err != nil {
			return relocationLanded, fmt.Sprintf("marking the restack journal entry pending sync failed: %v; startup recovery will still finish the landing", err)
		}
		return relocationLanded, fmt.Sprintf("resetting the worktree to the new top failed (%v); startup recovery will retry the sync", resetErr)
	}
	if applyErr != nil {
		// The refs landed and the worktree is synced, but the remap did not
		// persist; the prepared entry stays for startup recovery to finish.
		return relocationLanded, fmt.Sprintf("persisting the remap failed: %v; startup recovery will finish the landing", applyErr)
	}
	// Everything landed: remap the tracked anchor and the remaining commits'
	// SHAs onto the rewritten chain.
	r.remapAfterLanding(result)
	o.emitEvent(ports.Event{
		Type:          ports.RepoStatusChanged,
		FeatureID:     r.featureID,
		RepoName:      r.repo,
		Branch:        top.Branch,
		LayerPosition: pos,
		Message:       fmt.Sprintf("relocated a final review fix into layer %d", pos),
	})
	return relocationLanded, ""
}

// remapAfterLanding moves the relocation's tracked anchor and remaining
// commit SHAs onto the rewritten chain, so the next commit's attempt builds
// its cut points from current state.
func (r *repoRelocation) remapAfterLanding(result *git.RestackResult) {
	if next, ok := result.CommitMap[r.anchor]; ok {
		r.anchor = next
	}
	for i := range r.commits {
		if next, ok := result.CommitMap[r.commits[i].SHA]; ok {
			r.commits[i].SHA = next
		}
	}
}

// journalUpdatesFromGit converts git ref updates into the journal's durable
// shape.
func journalUpdatesFromGit(updates []git.RefUpdate) []feature.RestackRefUpdate {
	out := make([]feature.RestackRefUpdate, 0, len(updates))
	for _, u := range updates {
		out = append(out, feature.RestackRefUpdate{Ref: u.Ref, OldSHA: u.OldSHA, NewSHA: u.NewSHA})
	}
	return out
}

// restackRemap computes the anchor and tip remap one landed relocation
// persists: every roadmap-phase anchor whose old SHA was replayed maps to
// its new SHA, and every layer at or above the landed layer maps its tip —
// the landed layer's tip to the inserted commit, the top layer's tip to the
// rewritten chain's top.
func restackRemap(f *feature.Feature, repoName string, landedPos, topPos int, result *git.RestackResult, cps *restackCutPoints) feature.RestackRemap {
	remap := feature.RestackRemap{Anchors: map[int]string{}, Tips: map[int]string{}}
	for phase, byRepo := range f.Run().RoadmapPhaseCommitAnchors {
		if newSHA, ok := result.CommitMap[byRepo[repoName]]; ok {
			remap.Anchors[phase] = newSHA
		}
	}
	for pos := landedPos; pos <= topPos; pos++ {
		if pos == topPos {
			remap.Tips[pos] = result.HeadSHA
			continue
		}
		remap.Tips[pos] = cutPointNewSHA(result, cps.LayerLabel[pos])
	}
	return remap
}

// restackCutPoints is the labelled chain one relocation attempt rewrites:
// the cut points passed to the primitive, the label each stack layer's tip
// resolved to, and the label of the cut point sitting on the fix commit
// being relocated.
type restackCutPoints struct {
	CutPoints  []git.RestackCutPoint
	LayerLabel map[int]string
	FixLabel   string
}

// restackCutCandidate is one labelled position on the chain, ordered by
// sortKey: roadmap-phase anchors by phase number, layer tips at their
// layer's highest phase, and every commit above the anchor (earlier
// fallback commits and this round's fix commits) above everything else.
type restackCutCandidate struct {
	sortKey int
	label   string
	sha     string
	layer   int
	isFix   bool
}

// buildRestackCutPoints builds the labelled cut points of one relocation
// attempt from the feature's recorded anchors and layer tips, the
// relocation's tracked anchor (the top layer's tip before this round's
// commits, remapped after every landing), and every commit above it — each
// its own cut point, so a drop-segment removes exactly the fix commit being
// relocated. Consecutive cut points on the same commit merge into one
// labelled group; a layer tip aliasing an earlier group adopts its label.
func (o *Orchestrator) buildRestackCutPoints(f *feature.Feature, r *repoRelocation, fixSHA string) (*restackCutPoints, error) {
	if o.deps.Worktrees == nil {
		return nil, errors.New("worktree operations are not configured")
	}
	top := topStackLayer(f.Stack)
	var candidates []restackCutCandidate
	anchors := f.Run().RoadmapPhaseCommitAnchors
	phases := make([]int, 0, len(anchors))
	for phase := range anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	for _, phase := range phases {
		sha := anchors[phase][r.repo]
		if sha == "" {
			continue
		}
		candidates = append(candidates, restackCutCandidate{sortKey: phase, label: fmt.Sprintf("phase:%d", phase), sha: sha})
	}
	for _, layer := range f.Stack {
		if layer.Position == top.Position {
			// The top layer's cut point is the relocation's tracked anchor:
			// the recorded tip already includes this round's commits.
			if r.anchor == "" {
				return nil, errors.New("the top layer's pre-round tip is unknown")
			}
			candidates = append(candidates, restackCutCandidate{sortKey: 100000 + layer.Position, label: fmt.Sprintf("layer:%d", layer.Position), sha: r.anchor, layer: layer.Position})
			continue
		}
		tip := layer.Repos[r.repo].TipSHA
		if tip == "" {
			refTip, err := o.deps.Worktrees.RefSHA(r.worktree, layer.Branch)
			if err != nil {
				return nil, fmt.Errorf("resolving the tip of layer %d from ref %q: %w", layer.Position, layer.Branch, err)
			}
			tip = refTip
		}
		key := 0
		for _, p := range layer.Phases {
			if p > key {
				key = p
			}
		}
		if key == 0 {
			key = 100000 + layer.Position
		}
		candidates = append(candidates, restackCutCandidate{sortKey: key, label: fmt.Sprintf("layer:%d", layer.Position), sha: tip, layer: layer.Position})
	}
	above, err := git.CommitsBetween(r.worktree, r.anchor, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("listing the commits above the top layer's tip: %w", err)
	}
	for i, sha := range above {
		candidates = append(candidates, restackCutCandidate{sortKey: 200000 + i, label: fmt.Sprintf("fix:%d", i), sha: sha, isFix: true})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].sortKey < candidates[j].sortKey })

	cps := &restackCutPoints{LayerLabel: make(map[int]string)}
	for _, c := range candidates {
		if len(cps.CutPoints) > 0 && cps.CutPoints[len(cps.CutPoints)-1].SHA == c.sha {
			label := cps.CutPoints[len(cps.CutPoints)-1].Label
			if c.layer > 0 {
				cps.LayerLabel[c.layer] = label
			}
			if c.isFix && c.sha == fixSHA {
				cps.FixLabel = label
			}
			continue
		}
		cps.CutPoints = append(cps.CutPoints, git.RestackCutPoint{Label: c.label, SHA: c.sha})
		if c.layer > 0 {
			cps.LayerLabel[c.layer] = c.label
		}
		if c.isFix && c.sha == fixSHA {
			cps.FixLabel = c.label
		}
	}
	if cps.FixLabel == "" {
		return nil, errors.New("the fix commit is not on the chain above the top layer's tip")
	}
	return cps, nil
}

// cutPointNewSHA resolves a labelled cut point's new SHA from a restack
// result.
func cutPointNewSHA(result *git.RestackResult, label string) string {
	for _, cp := range result.CutPoints {
		if cp.Label == label {
			return cp.SHA
		}
	}
	return ""
}

// stackLayerByPosition resolves the stack layer with the given position.
func stackLayerByPosition(stack []feature.StackLayer, position int) (feature.StackLayer, bool) {
	for _, layer := range stack {
		if layer.Position == position {
			return layer, true
		}
	}
	return feature.StackLayer{}, false
}

// warnFixStayedAtTop reports a fix that stayed in the top layer — either
// because every relocation attempt failed or because a landing failure left
// the commits on the top branch.
func (o *Orchestrator) warnFixStayedAtTop(featureID, repo string, requestedPos int, diagnostics string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return
	}
	o.emitFixRelocatedWarning(f, repo, requestedPos, topStackLayer(f.Stack).Position, diagnostics)
}

// emitFixRelocatedWarning reports, through the canonical warning code and
// the observer trail, that a repository's final review fix landed above its
// requested stack layer.
func (o *Orchestrator) emitFixRelocatedWarning(f *feature.Feature, repository string, requestedPos, actualPos int, diagnostics string) {
	stack := f.Run().Stack
	requestedTitle := ""
	actualTitle := ""
	actualBranch := ""
	for _, layer := range stack {
		switch layer.Position {
		case requestedPos:
			requestedTitle = layer.Title
		case actualPos:
			actualTitle = layer.Title
			actualBranch = layer.Branch
		}
	}
	repositories := []errcat.CodeRepository{{Name: repository, Branch: actualBranch}}
	warning := errcat.New(
		errcat.FixRelocatedAboveLayer,
		errcat.WithRepositories(repositories...),
		errcat.WithParams(errcat.WarningFixRelocatedParams{
			Repositories:   repositories,
			RequestedLayer: requestedPos,
			RequestedTitle: requestedTitle,
			ActualLayer:    actualPos,
			ActualTitle:    actualTitle,
		}),
		errcat.WithDiagnostics(diagnostics),
	)
	o.emitEvent(ports.Event{
		Type:           ports.RepoStatusChanged,
		FeatureID:      f.ID,
		RepoName:       repository,
		Branch:         actualBranch,
		LayerPosition:  actualPos,
		Message:        warning.Summary,
		CanonicalError: &warning,
	})
	if o.hooks.OnRestackWarning != nil {
		o.hooks.OnRestackWarning(f.ID, observe.RestackWarningEvent{
			Code:           string(errcat.FixRelocatedAboveLayer),
			Repository:     repository,
			RequestedLayer: requestedPos,
			RequestedTitle: requestedTitle,
			ActualLayer:    actualPos,
			ActualTitle:    actualTitle,
			Diagnostics:    diagnostics,
		})
	}
}

// emitManifestIgnoredWarning reports, through the canonical warning code and
// the observer trail, one ignored fix manifest entry.
func (o *Orchestrator) emitManifestIgnoredWarning(featureID, repository string, layer int, path, reason, diagnostics string) {
	var repositories []errcat.CodeRepository
	if repository != "" {
		repositories = append(repositories, errcat.CodeRepository{Name: repository})
	}
	warning := errcat.New(
		errcat.FixManifestEntryIgnored,
		errcat.WithRepositories(repositories...),
		errcat.WithParams(errcat.WarningManifestIgnoredParams{
			Repositories: repositories,
			Layer:        layer,
			Path:         path,
			Reason:       reason,
		}),
		errcat.WithDiagnostics(diagnostics),
	)
	o.emitEvent(ports.Event{
		Type:           ports.RepoStatusChanged,
		FeatureID:      featureID,
		RepoName:       repository,
		LayerPosition:  layer,
		Message:        warning.Summary,
		CanonicalError: &warning,
	})
	if o.hooks.OnRestackWarning != nil {
		o.hooks.OnRestackWarning(featureID, observe.RestackWarningEvent{
			Code:        string(errcat.FixManifestEntryIgnored),
			Repository:  repository,
			Layer:       layer,
			Path:        path,
			Reason:      reason,
			Diagnostics: diagnostics,
		})
	}
}

// fixManifestPath resolves the fix manifest's path inside a fixer iteration
// directory.
func fixManifestPath(iterationDir string) string {
	return filepath.Join(iterationDir, agent.FixManifestFilename)
}
