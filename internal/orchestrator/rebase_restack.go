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
	"log"
	"path/filepath"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the harness restack loop a rebase child runs when it is
// started at Created. The loop replaces the child's planning and implement
// phases: for every work repository it rebuilds the stack chain from the
// target SHA through the restack primitive — dropping merged layers'
// segments and replaying the kept layers in order, with bounded agent
// sessions resolving any conflicting cherry-pick — resets the child worktree
// to the rebuilt top, persists the result on the relationship, and leaves
// the child ready for its single verification round (the deferred Final
// Review).
//
// Because a resolution session runs for minutes, the start is asynchronous:
// the start request returns at once, the loop runs in a background goroutine
// tracked by the cycle wait group while the child stays at Created with no
// new status transitions, an in-memory in-flight guard refuses a second
// start with the feature-busy error, and stop or discard aborts the loop
// with the temporary worktree removed. The loop is idempotent: it recomputes
// from the parent's current refs and the creation-time persisted target SHA,
// so a crash after the worktree reset but before the result is persisted
// leaves the child at Created and a restart recomputes the same rebuilt
// tips. Exhausted resolution attempts park the pass: the child's transaction
// journal is created in the attention phase with the rebase conflict
// resolution code, no ref changes, and starting the pass again re-runs the
// loop.

// startRebaseRestackPass admits one asynchronous restack run for a rebase
// child at Created. The synchronous preflight (validation, the in-flight
// guard, clearing a previously parked journal) runs under the caller's
// relationship read lock; the loop itself runs in a background goroutine
// tracked by the cycle wait group, holding no relationship lock, and the
// start returns immediately with the child still at Created. Landing
// dispatches the Final Review from the goroutine through the same start path
// the server used.
func (o *Orchestrator) startRebaseRestackPass(featureID string) error {
	child, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("loading rebase child: %w", err)
	}
	if child == nil || child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase {
		return fmt.Errorf("feature %s is not a rebase child", featureID)
	}
	if child.Status != feature.StatusCreated {
		return fmt.Errorf("rebase child %s is %s, expected Created", featureID, child.Status)
	}
	if len(child.Parent.RebaseWorkRepos) == 0 {
		return fmt.Errorf("rebase child %s has no work repositories", featureID)
	}
	if _, err := o.deps.Lifecycle.Get(child.Parent.ParentID); err != nil {
		return fmt.Errorf("loading rebase parent %s: %w", child.Parent.ParentID, err)
	}
	if o.deps.Worktrees == nil {
		return errors.New("worktree operations are not configured")
	}

	handle := &rebaseRestackLoopHandle{}
	if _, loaded := o.rebaseRestackLoops.LoadOrStore(featureID, handle); loaded {
		return ErrFeatureBusy
	}

	// A re-run recomputes everything from the parent's refs; a journal left
	// by a previous exhausted-resolution park describes that attempt only,
	// so it is cleared before the loop runs.
	if child.Parent.Transaction != nil {
		if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
			if f.Parent != nil {
				f.Parent.Transaction = nil
			}
			return nil
		}); err != nil {
			o.rebaseRestackLoops.Delete(featureID)
			return fmt.Errorf("clearing the parked rebase journal: %w", err)
		}
	}

	o.cycleWG.Go(func() {
		defer o.rebaseRestackLoops.Delete(featureID)
		o.runRebaseRestackLoop(featureID, handle)
	})
	return nil
}

// runRebaseRestackLoop is the background body of the pass: per work
// repository, rebuild the chain with the conflict resolver attached; on
// exhaustion park the pass (a durable attention journal, an attention event
// emitted) and stop; on any other failure exit without parking, leaving the
// child at Created; on landing, persist the result, transition to
// ReviewPassed, extend the description and exit criteria with the resolved
// conflicts, and dispatch the Final Review through StartFeature — the same
// path the server's start used.
func (o *Orchestrator) runRebaseRestackLoop(featureID string, handle *rebaseRestackLoopHandle) {
	child, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		log.Printf("rebase restack loop for %s: loading the child failed: %v", featureID, err)
		return
	}
	if child == nil || child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase || child.Status != feature.StatusCreated {
		// Stopped or discarded between the start and the loop's first step.
		return
	}
	parent, err := o.deps.Lifecycle.Get(child.Parent.ParentID)
	if err != nil {
		o.emitRestackLoopError(featureID, fmt.Errorf("loading rebase parent %s: %w", child.Parent.ParentID, err))
		return
	}

	restacks := make([]feature.RebaseRepoRestack, 0, len(child.Parent.RebaseWorkRepos))
	for _, repoName := range child.Parent.RebaseWorkRepos {
		restack, conflict, err := o.rebaseRestackOneRepo(parent, child, repoName, handle)
		if err != nil {
			o.emitRestackLoopError(featureID, err)
			return
		}
		if conflict != nil {
			if err := o.parkRebaseReplayConflict(child, repoName, conflict); err != nil {
				o.emitRestackLoopError(featureID, err)
			}
			return
		}
		restacks = append(restacks, restack)
	}

	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if f.Parent == nil {
			return fmt.Errorf("feature %s lost its relationship", featureID)
		}
		f.Parent.RebaseRestacks = restacks
		// The single verification round stages exactly the repositories the
		// restack rebuilt; pass-through repositories never enter it.
		for _, repoName := range f.Parent.RebaseWorkRepos {
			if st := f.RepoStates[repoName]; st != nil {
				st.Touched = true
			}
		}
		if err := f.Transition(feature.StatusReviewPassed); err != nil {
			return fmt.Errorf("transitioning the rebase child to ReviewPassed: %w", err)
		}
		f.CurrentPhase = feature.PhaseFinalReview
		// The verification round scrutinizes the agent-resolved commits
		// and the adopted reviewer commits specifically: the description
		// and exit criteria name them.
		f.Description = feature.ExtendRebaseDescriptionWithResolutions(f.Description, restacks)
		f.ExitCriteria = feature.ExtendRebaseExitCriteriaWithResolutions(f.ExitCriteria, restacks)
		f.Description = feature.ExtendRebaseDescriptionWithAdoptions(f.Description, restacks)
		f.ExitCriteria = feature.ExtendRebaseExitCriteriaWithAdoptions(f.ExitCriteria, restacks)
		return nil
	}); err != nil {
		o.emitRestackLoopError(featureID, err)
		return
	}

	// Landing dispatches the deferred Final Review through the same start
	// path the server used: StartFeature on the ReviewPassed child routes to
	// the Final Review starter exactly as the pre-async flow's fall-through
	// did.
	if err := o.StartFeature(featureID); err != nil {
		o.emitRestackLoopError(featureID, fmt.Errorf("dispatching the rebase Final Review: %w", err))
	}
}

// emitRestackLoopError surfaces a non-park loop failure: the child stays at
// Created with no attention journal, so the transient event is the only
// progress surface.
func (o *Orchestrator) emitRestackLoopError(featureID string, err error) {
	log.Printf("rebase restack loop for %s: %v", featureID, err)
	o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: featureID,
		Message:   "rebase restack failed: " + err.Error(),
	})
}

// parkRebaseReplayConflict parks the pass on exhausted conflict resolution:
// the child's transaction journal is created in the attention phase carrying
// the rebase conflict resolution code, naming the repository, the segment,
// the commit, the conflicted files, the attempt count, the last failure, and
// the attempt directory. No ref and no worktree changed — the restack
// primitive aborts its replay before returning the conflict.
func (o *Orchestrator) parkRebaseReplayConflict(child *feature.Feature, repoName string, conflict *git.RestackConflictError) error {
	entry := feature.RepoTransactionEntry{
		Repo:      repoName,
		PrepState: feature.RepoPrepFailed,
	}
	detail := fmt.Sprintf(
		"resolving segment %s..%s commit %s exhausted %d attempts on: %s",
		conflict.SegmentFrom, conflict.SegmentTo, conflict.CommitSHA, conflict.Attempts, joinFileList(conflict.ConflictFiles))
	if conflict.LastFailure != "" {
		detail += "; last failure: " + conflict.LastFailure
	}
	if conflict.AttemptDir != "" {
		detail += "; attempt directory: " + conflict.AttemptDir
	}
	item := entryFinding(&entry, errcat.IntegrationRebaseConflict, detail)
	item.ctx.ConflictFiles = append([]string(nil), conflict.ConflictFiles...)
	item.ctx.CommitSHA = conflict.CommitSHA
	item.ctx.Attempts = conflict.Attempts
	journal := &feature.TransactionJournal{
		Phase:   feature.TransactionPhaseAttention,
		Entries: []feature.RepoTransactionEntry{entry},
	}
	return o.parkIntegrationAttention(child, journal, []integrationFinding{item})
}

// rebaseRestackOneRepo rebuilds one work repository's chain: it resolves the
// cut points, runs the restack primitive with replace-base to the persisted
// target SHA plus drop-segment for every cut point inside a merged layer and
// the per-repository conflict resolver attached, decorates the resolver's
// records with the primitive's dropped verdict, resets the repository's
// child worktree to the rebuilt top, and returns the
// relationship-persistable result. A structured conflict returns the
// conflict with a nil result and a nil error; every other failure returns an
// error with no ref touched.
func (o *Orchestrator) rebaseRestackOneRepo(parent *feature.Feature, child *feature.Feature, repoName string, handle *rebaseRestackLoopHandle) (feature.RebaseRepoRestack, *git.RestackConflictError, error) {
	restack := feature.RebaseRepoRestack{Repo: repoName}

	target, ok := child.RebaseTargetForRepo(repoName)
	if !ok || target.TargetSHA == "" {
		return restack, nil, fmt.Errorf("rebase child %s has no persisted target SHA for repository %s", child.ID, repoName)
	}
	childRepo := featureRepoByName(child, repoName)
	if childRepo == nil {
		return restack, nil, fmt.Errorf("rebase child %s has no repository %s", child.ID, repoName)
	}
	worktree := childRepo.WorktreePath
	if worktree == "" {
		worktree = childRepo.Path
	}
	if worktree == "" {
		return restack, nil, fmt.Errorf("rebase child %s has no worktree for repository %s", child.ID, repoName)
	}

	plan, err := o.buildRebaseRestackPlan(parent, child, repoName, worktree, target.TargetSHA)
	if err != nil {
		return restack, nil, err
	}
	resolver := o.newRebaseConflictResolver(parent, child, repoName, worktree, plan, target, handle)
	attemptsRoot := filepath.Join(agent.ActiveRunDir(o.stateDir(), child), "rebase-resolution", repoName)
	result, err := o.deps.Worktrees.RestackChainWithResolver(worktree, plan.cutPoints, plan.ops, resolver.ResolveConflict, attemptsRoot)
	if err != nil {
		var conflict *git.RestackConflictError
		if errors.As(err, &conflict) {
			return restack, conflict, nil
		}
		return restack, nil, fmt.Errorf("restacking repository %s: %w", repoName, err)
	}

	restack.RebuiltTop = result.HeadSHA
	restack.RebuiltTips = plan.rebuiltTips(result)
	restack.DroppedLayers = plan.droppedLayers
	restack.AnchorRemap = plan.anchorRemap(result, target.TargetSHA)
	restack.ResolvedConflicts = decorateResolvedConflicts(resolver.resolved, result.Dropped)
	restack.AdoptedCommits = plan.adoptedCommits(result)
	restack.SkippedMergeCommits = plan.skippedMergeCommits

	// The child branch is the top layer's carrier: reset the child
	// worktree's branch to the rebuilt top before the result is persisted,
	// so a crash in between leaves the child at Created and the re-run
	// recomputes identical tips (the layer refs never moved).
	if err := o.deps.Worktrees.ResetToCommit(worktree, result.HeadSHA); err != nil {
		return restack, nil, fmt.Errorf("resetting the child worktree of %s to the rebuilt top: %w", repoName, err)
	}
	return restack, nil, nil
}

// decorateResolvedConflicts marks every resolution whose continued
// cherry-pick turned out empty — the resolution took the target's side
// entirely — as dropped, exactly like a replay that became empty on its own.
func decorateResolvedConflicts(resolved []feature.RebaseResolvedConflict, dropped []string) []feature.RebaseResolvedConflict {
	if len(resolved) == 0 {
		return nil
	}
	droppedSet := make(map[string]bool, len(dropped))
	for _, sha := range dropped {
		droppedSet[sha] = true
	}
	out := make([]feature.RebaseResolvedConflict, 0, len(resolved))
	for _, rc := range resolved {
		rc.Dropped = droppedSet[rc.Commit]
		out = append(out, rc)
	}
	return out
}

// rebaseRestackPlan is one repository's computed restack request and the
// bookkeeping needed to translate the primitive's result into the
// relationship-persistable restack record.
type rebaseRestackPlan struct {
	// cutPoints are the labelled chain positions handed to the primitive.
	cutPoints []git.RestackCutPoint
	// ops are the restack operations: replace-base to the target SHA,
	// drop-segment for every cut point inside a merged layer, and
	// insert-after for every diverged layer's foreign commits.
	ops []git.RestackOp
	// groups are the merged cut-point groups with their owning layer and
	// anchor phases, in chain order (the base group first).
	groups []rebaseRestackGroup
	// layerTipGroup maps a layer position to the index of the group its tip
	// resolved to.
	layerTipGroup map[int]int
	// droppedLayers lists the merged layers whose branches the pass drops.
	droppedLayers []int
	// layerState mirrors the per-layer classification for tip translation.
	layerState map[int]feature.RebaseLayerState
	// tips lists the stack layers' resolved tips ascending, so the conflict
	// resolver can attribute a conflicted commit to its owning layer.
	tips []rebaseRestackTip
	// adoptions records every diverged layer's foreign commits scheduled
	// onto the cut point that owns the layer's tip, so the result can be
	// translated into the persisted adopted-commit records.
	adoptions []rebaseRestackAdoption
	// foreignBySHA maps an adopted foreign commit's original SHA to its
	// layer position, so the conflict resolver attributes a conflicting
	// adoption to the diverged layer it extends.
	foreignBySHA map[string]int
	// skippedMergeCommits counts the remote-only merge commits the
	// preflight observed on this repository's diverged layers.
	skippedMergeCommits int
}

// rebaseRestackAdoption is one diverged layer's foreign commits scheduled
// onto the cut point that owns the layer's tip: the commits cherry-pick
// onto the layer's replayed segment, after the layer's own commits and
// before the next layer's.
type rebaseRestackAdoption struct {
	layerPosition int
	label         string
	commits       []feature.RebaseForeignCommit
}

// rebaseRestackTip is one stack layer's resolved tip position on the chain.
type rebaseRestackTip struct {
	position int
	sha      string
}

// rebaseRestackGroup is one cut-point group on the chain: consecutive
// candidates sharing a SHA merge into one group whose label is the first
// candidate's. Owning layer is the lowest layer whose tip sits at or above
// the group; anchor phases are the roadmap phases whose anchors resolved to
// the group.
type rebaseRestackGroup struct {
	label       string
	sha         string
	ownerLayer  int
	anchorPhase []int
}

// rebaseRestackCandidate is one labelled position on the chain before
// same-SHA merging, ordered by sortKey exactly like the relocation path's
// builder: roadmap-phase anchors by phase number, layer tips at their
// layer's highest phase.
type rebaseRestackCandidate struct {
	sortKey int
	label   string
	sha     string
}

// buildRebaseRestackPlan computes one work repository's restack request:
// the cut points are the merge base of the target SHA and the lowest layer
// tip, then every roadmap-phase anchor and layer tip in ascending order; the
// operations are replace-base with the target SHA plus drop-segment for
// every cut point inside a merged layer. Cut points at or below the merge
// base are skipped — the base region already lives in the target.
func (o *Orchestrator) buildRebaseRestackPlan(parent *feature.Feature, child *feature.Feature, repoName, worktree, targetSHA string) (*rebaseRestackPlan, error) {
	layers := parent.OrderedStackLayers()
	stateByLayer := make(map[int]feature.RebaseLayerState)
	for _, c := range child.Parent.RebaseLayerStates {
		if c.Repo == repoName {
			stateByLayer[c.LayerPosition] = c.State
		}
	}

	var tips []rebaseRestackTip
	for _, layer := range layers {
		entry, hasEntry := layer.Repos[repoName]
		if !hasEntry || layer.Branch == "" {
			continue
		}
		tip := entry.TipSHA
		if tip == "" {
			refTip, err := o.deps.Worktrees.RefSHA(worktree, layer.Branch)
			if err != nil {
				return nil, fmt.Errorf("resolving the tip of layer %d branch %q in %s: %w", layer.Position, layer.Branch, repoName, err)
			}
			tip = refTip
		}
		if tip == "" {
			continue
		}
		tips = append(tips, rebaseRestackTip{position: layer.Position, sha: tip})
	}
	if len(tips) == 0 {
		return nil, fmt.Errorf("repository %s has no stack layer tip to restack", repoName)
	}

	base, err := git.MergeBaseSHA(worktree, targetSHA, tips[0].sha)
	if err != nil {
		return nil, fmt.Errorf("resolving the restack base of repository %s: %w", repoName, err)
	}

	var candidates []rebaseRestackCandidate
	candidates = append(candidates, rebaseRestackCandidate{sortKey: -1, label: "base", sha: base})
	anchors := parent.Run().RoadmapPhaseCommitAnchors
	phases := make([]int, 0, len(anchors))
	for phase := range anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	for _, phase := range phases {
		sha := anchors[phase][repoName]
		if sha == "" || sha == base || !git.IsAncestor(worktree, base, sha) {
			continue
		}
		candidates = append(candidates, rebaseRestackCandidate{sortKey: phase, label: fmt.Sprintf("phase:%d", phase), sha: sha})
	}
	for _, layer := range layers {
		entry, hasEntry := layer.Repos[repoName]
		if !hasEntry || layer.Branch == "" {
			continue
		}
		tip := entry.TipSHA
		if tip == "" {
			// Already resolved in the tips walk; reuse the same resolution
			// so the label and the tip bookkeeping cannot diverge.
			for _, t := range tips {
				if t.position == layer.Position {
					tip = t.sha
					break
				}
			}
		}
		if tip == "" || tip == base || !git.IsAncestor(worktree, base, tip) {
			continue
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
		candidates = append(candidates, rebaseRestackCandidate{sortKey: key, label: fmt.Sprintf("layer:%d", layer.Position), sha: tip})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].sortKey < candidates[j].sortKey })

	plan := &rebaseRestackPlan{layerTipGroup: make(map[int]int), layerState: stateByLayer, tips: tips, foreignBySHA: make(map[string]int)}
	var groups []rebaseRestackGroup
	for _, c := range candidates {
		if len(groups) > 0 && groups[len(groups)-1].sha == c.sha {
			continue
		}
		groups = append(groups, rebaseRestackGroup{label: c.label, sha: c.sha})
	}

	// The child worktree forked at the parent tip, which Phase 11 keeps at
	// the top layer's tip. Anything above the highest layer tip on the
	// forked HEAD would be silently dropped by the replay, so fail closed
	// instead of losing commits.
	if head, err := o.deps.Worktrees.CurrentHeadSHA(worktree); err == nil && head != "" {
		topTip := tips[len(tips)-1].sha
		if head != topTip && git.IsAncestor(worktree, topTip, head) {
			return nil, fmt.Errorf("repository %s's child worktree HEAD carries commits above the top layer tip; refusing to replay without them", repoName)
		}
	}

	for i := range groups {
		group := &groups[i]
		if i == 0 {
			continue
		}
		// Owning layer: the lowest layer whose tip sits at or above the
		// group — the layer whose commit range contains it.
		for _, t := range tips {
			if t.sha == group.sha || git.IsAncestor(worktree, group.sha, t.sha) {
				group.ownerLayer = t.position
				break
			}
		}
		for _, phase := range phases {
			if anchors[phase][repoName] == group.sha {
				group.anchorPhase = append(group.anchorPhase, phase)
			}
		}
	}
	for _, t := range tips {
		for i := range groups {
			if groups[i].sha == t.sha {
				plan.layerTipGroup[t.position] = i
				break
			}
		}
	}
	// Every merged layer that still resolves a tip is dropped: its branch
	// is deleted at integration even when its tip aliases another layer's
	// tip (no commits of its own) or the base region.
	for _, t := range tips {
		if stateByLayer[t.position] == feature.RebaseLayerStateMerged {
			plan.droppedLayers = append(plan.droppedLayers, t.position)
		}
	}

	// Foreign-commit adoption: every diverged layer with a non-empty
	// foreign list gets one insert-after operation on the cut point that
	// owns the layer's tip, so the reviewer's commits extend the layer's
	// replayed segment — after the layer's own commits (and its
	// roadmap-phase anchors) and before the next layer's — and the layer's
	// remapped tip covers them. A diverged layer whose tip resolved no cut
	// point fails closed: replaying without the reviewer's commits would
	// silently lose them. The foreign SHAs are pinned on the persisted
	// classification, so a re-run adopts the same commits; the preflight's
	// fetch left their objects in the shared store the primitive's temp
	// worktree cherry-picks from.
	var insertOps []git.RestackOp
	for _, c := range child.RebaseDivergedLayers(repoName) {
		plan.skippedMergeCommits += c.RemoteOnlyCommits - len(c.ForeignCommits)
		if len(c.ForeignCommits) == 0 {
			continue
		}
		groupIdx, ok := plan.layerTipGroup[c.LayerPosition]
		if !ok {
			return nil, fmt.Errorf("repository %s's diverged layer %d has no cut point on the restack chain; refusing to replay without its reviewer commits", repoName, c.LayerPosition)
		}
		shas := make([]string, 0, len(c.ForeignCommits))
		for _, fc := range c.ForeignCommits {
			shas = append(shas, fc.SHA)
			plan.foreignBySHA[fc.SHA] = c.LayerPosition
		}
		label := groups[groupIdx].label
		insertOps = append(insertOps, git.RestackOp{
			Kind:          git.RestackInsertAfter,
			CutPointLabel: label,
			CommitSHAs:    shas,
		})
		plan.adoptions = append(plan.adoptions, rebaseRestackAdoption{
			layerPosition: c.LayerPosition,
			label:         label,
			commits:       c.ForeignCommits,
		})
	}

	plan.groups = groups
	plan.cutPoints = make([]git.RestackCutPoint, len(groups))
	for i, group := range groups {
		plan.cutPoints[i] = git.RestackCutPoint{Label: group.label, SHA: group.sha}
	}
	plan.ops = []git.RestackOp{{
		Kind:           git.RestackReplaceBase,
		CutPointLabel:  groups[0].label,
		ReplaceBaseSHA: targetSHA,
	}}
	for i := range groups {
		if i == 0 || groups[i].ownerLayer == 0 {
			continue
		}
		if stateByLayer[groups[i].ownerLayer] == feature.RebaseLayerStateMerged {
			plan.ops = append(plan.ops, git.RestackOp{
				Kind:          git.RestackDropSegment,
				CutPointLabel: groups[i].label,
			})
		}
	}
	plan.ops = append(plan.ops, insertOps...)
	return plan, nil
}

// rebuiltTips translates the restack result into per-kept-layer rebuilt
// tips: the new SHA of the group each kept layer's tip resolved to. A kept
// layer whose tip aliases the base or a lower group keeps that group's new
// SHA — it owns no commits, so its tip is the boundary below it.
func (p *rebaseRestackPlan) rebuiltTips(result *git.RestackResult) map[int]string {
	tips := make(map[int]string)
	for position, groupIdx := range p.layerTipGroup {
		if p.layerState[position] != feature.RebaseLayerStateKept {
			continue
		}
		if sha := cutPointNewSHA(result, p.groups[groupIdx].label); sha != "" {
			tips[position] = sha
		}
	}
	return tips
}

// anchorRemap translates the restack result into the roadmap-phase anchor
// remap: every anchor phase mapped to its group's new SHA, except anchors
// inside dropped layers, which map to the target SHA — the base already
// contains that work, so a later rewind into those phases resets to the
// target.
func (p *rebaseRestackPlan) anchorRemap(result *git.RestackResult, targetSHA string) map[int]string {
	remap := make(map[int]string)
	for i := range p.groups {
		group := &p.groups[i]
		if len(group.anchorPhase) == 0 {
			continue
		}
		newSHA := cutPointNewSHA(result, group.label)
		if i > 0 && group.ownerLayer != 0 && p.layerState[group.ownerLayer] == feature.RebaseLayerStateMerged {
			newSHA = targetSHA
		}
		if newSHA == "" {
			continue
		}
		for _, phase := range group.anchorPhase {
			remap[phase] = newSHA
		}
	}
	return remap
}

// adoptedCommits translates the restack result into the persisted
// adopted-commit records: one record per foreign commit the plan scheduled,
// with the new SHA the replay produced and the dropped marker when the
// commit's change was already present on the replayed tip.
func (p *rebaseRestackPlan) adoptedCommits(result *git.RestackResult) []feature.RebaseAdoptedCommit {
	if len(p.adoptions) == 0 {
		return nil
	}
	droppedSet := make(map[string]bool, len(result.Dropped))
	for _, sha := range result.Dropped {
		droppedSet[sha] = true
	}
	var out []feature.RebaseAdoptedCommit
	for _, a := range p.adoptions {
		for _, fc := range a.commits {
			out = append(out, feature.RebaseAdoptedCommit{
				LayerPosition: a.layerPosition,
				OriginalSHA:   fc.SHA,
				NewSHA:        result.CommitMap[fc.SHA],
				Dropped:       droppedSet[fc.SHA],
				Subject:       fc.Subject,
				Author:        fc.Author,
			})
		}
	}
	return out
}

// prepareRebaseRestackRefs stages one work repository's integration entry
// from the persisted restack result instead of a merge candidate: one
// rewrite ref per kept layer (anchor the layer branch's current tip,
// candidate the rebuilt tip — the top kept layer's candidate is the child
// head, which must descend from the rebuilt top), one delete ref per dropped
// layer, the previous-top record when the parent's top layer is dropped, and
// the remap. Pure computation: no ref is read for update and no parent ref
// moves. Returns the zero finding and true when the entry is staged; a
// finding and false when the transaction must park at attention.
func (o *Orchestrator) prepareRebaseRestackRefs(child, parent *feature.Feature, entry *feature.RepoTransactionEntry) (integrationFinding, bool) {
	restack, ok := child.RebaseRestackForRepo(entry.Repo)
	if !ok || restack.RebuiltTop == "" {
		return entryFinding(entry, errcat.IntegrationCandidateFailed,
			fmt.Sprintf("repo %s has no persisted restack result; discard the pass and relaunch it", entry.Repo)), false
	}
	parentRepo := featureRepoByName(parent, entry.Repo)
	if parentRepo == nil {
		return entryFinding(entry, errcat.IntegrationRepositoryMissing,
			fmt.Sprintf("parent no longer has repository %s", entry.Repo)), false
	}
	repoPath := parentRepo.WorktreePath
	if repoPath == "" {
		repoPath = parentRepo.Path
	}
	childRepo := featureRepoByName(child, entry.Repo)
	if childRepo == nil {
		return entryFinding(entry, errcat.IntegrationRepositoryMissing,
			fmt.Sprintf("child no longer has repository %s", entry.Repo)), false
	}
	childWorktree := childRepo.WorktreePath
	if childWorktree == "" {
		childWorktree = childRepo.Path
	}

	dropped := make(map[int]bool, len(restack.DroppedLayers))
	for _, pos := range restack.DroppedLayers {
		dropped[pos] = true
	}
	kept := make([]int, 0, len(restack.RebuiltTips))
	for pos := range restack.RebuiltTips {
		kept = append(kept, pos)
	}
	sort.Ints(kept)
	if len(kept) == 0 {
		return entryFinding(entry, errcat.IntegrationCandidateFailed,
			fmt.Sprintf("repo %s records no kept layer tip in its restack result", entry.Repo)), false
	}
	topKept := kept[len(kept)-1]

	// readAnchor resolves one layer branch's current tip, reporting a
	// candidate-failure message instead of a SHA when the branch cannot be
	// read or does not exist locally (a kept layer's branch must exist to be
	// rewritten; a dropped layer's must exist to be deleted).
	readAnchor := func(branch string, delete bool) (sha, problem string) {
		current, absent, err := o.deps.Worktrees.RefSHAOrAbsent(repoPath, "refs/heads/"+branch)
		if err != nil {
			return "", fmt.Sprintf("reading repo %s's layer branch %q: %v", entry.Repo, branch, err)
		}
		if absent {
			verb := "kept layer"
			if delete {
				verb = "dropped layer"
			}
			return "", fmt.Sprintf("repo %s's %s branch %q does not exist locally", entry.Repo, verb, branch)
		}
		return current, ""
	}

	layers := parent.OrderedStackLayers()
	positions := make([]int, 0, len(kept)+len(dropped))
	positions = append(positions, kept...)
	for pos := range dropped {
		positions = append(positions, pos)
	}
	sort.Ints(positions)

	var refs []feature.RepoTransactionRef
	var previousTop *feature.RepoTransactionPreviousTop
	parentTopPosition := parentTopLayerPosition(parent)
	for _, pos := range positions {
		layer, hasLayer := stackLayerByPosition(layers, pos)
		if !hasLayer || layer.Branch == "" {
			return entryFinding(entry, errcat.IntegrationCandidateFailed,
				fmt.Sprintf("repo %s's restack result names layer %d, which the parent stack no longer defines", entry.Repo, pos)), false
		}
		anchor, problem := readAnchor(layer.Branch, dropped[pos])
		if problem != "" {
			return entryFinding(entry, errcat.IntegrationCandidateFailed, problem), false
		}
		if dropped[pos] {
			refs = append(refs, feature.RepoTransactionRef{
				Branch:    layer.Branch,
				Layer:     pos,
				Kind:      feature.RepoRefKindDelete,
				AnchorSHA: anchor,
			})
			if pos == parentTopPosition {
				previousTop = &feature.RepoTransactionPreviousTop{
					Branch: layer.Branch,
					Layer:  pos,
					TipSHA: anchor,
				}
			}
			continue
		}
		candidate := restack.RebuiltTips[pos]
		if pos == topKept {
			// The top kept layer's carrier is the child branch: its
			// candidate is the child head after the verification round's
			// fix commits, which must descend from the rebuilt top.
			if !git.IsAncestor(childWorktree, restack.RebuiltTop, entry.ChildHeadSHA) {
				return entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("repo %s's child head %s does not descend from the rebuilt top %s; the child branch left the restacked chain",
						entry.Repo, entry.ChildHeadSHA, restack.RebuiltTop)), false
			}
			candidate = entry.ChildHeadSHA
		}
		refs = append(refs, feature.RepoTransactionRef{
			Branch:       layer.Branch,
			Layer:        pos,
			AnchorSHA:    anchor,
			CandidateSHA: candidate,
		})
	}

	entry.Refs = refs
	entry.PreviousTop = previousTop
	entry.Remap = feature.RestackRemap{
		Anchors: restack.AnchorRemap,
		Tips:    make(map[int]string, len(refs)),
	}
	for _, ref := range refs {
		if ref.RefKind() == feature.RepoRefKindRewrite {
			entry.Remap.Tips[ref.Layer] = ref.CandidateSHA
		}
	}
	return integrationFinding{}, true
}
