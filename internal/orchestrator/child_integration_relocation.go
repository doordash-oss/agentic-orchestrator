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
	"sort"
	"strconv"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// This file implements review-feedback children's stack-aware candidate
// preparation: instead of a merge candidate on the parent's checked-out
// branch, the child's commits — each tagged with its target parent layer by
// the round-commit hook's Stack-Layer trailer — are relocated one at a time
// into the parent's layer chain through the restack primitive, and the
// journal entry records one ref update per parent layer at or above the
// lowest changed layer. Refactor and rebase children keep the merge-candidate
// and pass-through preparation in child_transaction.go.

// childRelocationCommit is one child commit tracked across the relocation
// ladder: the original SHA above the launch base, the current SHA on the
// virtual chain (replays renumber it), the requested target layer resolved
// from the Stack-Layer trailer, and the layer the commit finally landed in.
type childRelocationCommit struct {
	original string
	sha      string
	target   int
	landedAt int
	order    int
}

// childRelocationState is one repository's virtual chain across the ladder:
// the phase anchors and mutable layer tips the cut points are built from, the
// tracked child commits above the top layer's tip, the accumulated
// old-to-new commit map, the current virtual top, and the child head's tree
// every attempt's new top must reproduce.
type childRelocationState struct {
	repo      string
	repoPath  string
	childHead string
	headTree  string
	base      string
	topPos    int
	anchors   map[int]map[string]string
	stack     []feature.StackLayer
	layerTips map[int]string
	refTips   map[int]string
	commits   []childRelocationCommit
	head      string
	commitMap map[string]string
}

// prepareReviewFeedbackRefs stages one repository's review-feedback
// candidates: relocate the child's commits into the parent's layer chain and
// record the resulting ref list, remap, and relocated-commit map on the
// journal entry. A git failure while reading the range, trailers, refs, or
// trees is returned as a candidate-failed finding so the caller parks the
// transaction with the existing code; conflicts inside the ladder are not
// failures — they drive the fallback-upward policy.
func (o *Orchestrator) prepareReviewFeedbackRefs(child, parent *feature.Feature, entry *feature.RepoTransactionEntry, parentRepo *feature.FeatureRepo, parentTip string) (integrationFinding, error) {
	if o.deps.Worktrees == nil {
		return integrationFinding{}, errors.New("transaction: worktree operations are not configured")
	}
	top := topStackLayer(parent.Stack)
	state := &childRelocationState{
		repo:      entry.Repo,
		repoPath:  parentRepo.Path,
		childHead: entry.ChildHeadSHA,
		topPos:    top.Position,
		anchors:   parent.Run().RoadmapPhaseCommitAnchors,
		stack:     parent.Stack,
		layerTips: make(map[int]string),
		refTips:   make(map[int]string),
		commitMap: make(map[string]string),
	}

	// The relocation chain is anchored at the child's launch base: the top
	// layer's tip as it stood when the child branched. A top ref that no
	// longer matches the base (an acknowledged drift retry, or a worktree on
	// another branch) cannot be linearized with the child's commits, so the
	// caller falls back to merge-candidate preparation for this repository.
	base := child.BaseSHA(entry.Repo)
	if base == "" {
		base = parentTip
	}
	state.base = base
	topTip, err := o.deps.Worktrees.RefSHA(parentRepo.Path, "refs/heads/"+top.Branch)
	if err != nil {
		return entryFinding(entry, errcat.IntegrationCandidateFailed,
			fmt.Sprintf("reading the top layer ref %q for repo %s: %v", top.Branch, entry.Repo, err)), nil
	}
	if topTip != base {
		return integrationFinding{}, errReviewFeedbackFallbackToMerge
	}

	for _, layer := range parent.Stack {
		tip, err := o.deps.Worktrees.RefSHA(parentRepo.Path, "refs/heads/"+layer.Branch)
		if err != nil {
			return entryFinding(entry, errcat.IntegrationCandidateFailed,
				fmt.Sprintf("reading layer %d ref %q for repo %s: %v", layer.Position, layer.Branch, entry.Repo, err)), nil
		}
		state.refTips[layer.Position] = tip
		state.layerTips[layer.Position] = tip
	}

	// The child head's tree is the invariant every attempt's new top must
	// reproduce: the relocated chain delivers exactly the child's content.
	headTree, err := o.deps.Worktrees.CommitTreeSHA(parentRepo.Path, entry.ChildHeadSHA)
	if err != nil {
		return entryFinding(entry, errcat.IntegrationCandidateFailed,
			fmt.Sprintf("reading the child head tree for repo %s: %v", entry.Repo, err)), nil
	}
	state.headTree = headTree

	listed, err := git.CommitsBetweenWithStackLayer(parentRepo.Path, base, entry.ChildHeadSHA)
	if err != nil {
		return entryFinding(entry, errcat.IntegrationCandidateFailed,
			fmt.Sprintf("listing the child commits for repo %s: %v", entry.Repo, err)), nil
	}
	for i, c := range listed {
		state.commits = append(state.commits, childRelocationCommit{
			original: c.SHA,
			sha:      c.SHA,
			target:   state.trailerLayer(c.Layer),
			order:    i,
		})
	}
	state.head = entry.ChildHeadSHA

	// Relocate one commit at a time in ascending target-layer order (and
	// commit order within a layer), each with its own fallback-upward
	// policy; an attempt at the top layer is a no-op that leaves the commit
	// where it is, so the ladder always terminates without git.
	ordered := append([]childRelocationCommit(nil), state.commits...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].target != ordered[j].target {
			return ordered[i].target < ordered[j].target
		}
		return ordered[i].order < ordered[j].order
	})
	for i := range ordered {
		if err := o.relocateChildCommit(parent, state, &ordered[i]); err != nil {
			return entryFinding(entry, errcat.IntegrationCandidateFailed, err.Error()), nil
		}
	}

	entry.Refs = state.refUpdates()
	entry.Remap = state.remap()
	entry.Relocated = state.relocatedMap()
	return integrationFinding{}, nil
}

// errReviewFeedbackFallbackToMerge signals that a repository's chain cannot
// be linearized with the child's commits (the top ref moved away from the
// launch base), so the caller stages a merge candidate instead.
var errReviewFeedbackFallbackToMerge = errors.New("review feedback relocation falls back to a merge candidate")

// trailerLayer resolves one commit's Stack-Layer trailer value to a target
// parent layer: a valid position on the stack targets that layer; an absent,
// unparsable, or unknown value targets the top layer.
func (s *childRelocationState) trailerLayer(value string) int {
	pos, err := strconv.Atoi(value)
	if err != nil {
		return s.topPos
	}
	if _, ok := stackLayerByPosition(s.stack, pos); !ok || pos < 1 {
		return s.topPos
	}
	return pos
}

// relocateChildCommit runs one commit's fallback ladder: attempt the
// requested layer, then each layer above, until one lands or the top layer's
// no-op leaves the commit above the top. A landing above the request emits
// the relocated-above-requested-layer warning on the parent. A non-conflict
// git failure aborts the ladder and is returned so the caller parks the
// transaction with the candidate-failed code.
func (o *Orchestrator) relocateChildCommit(parent *feature.Feature, state *childRelocationState, commit *childRelocationCommit) error {
	if commit.target >= state.topPos {
		// The top layer is a no-op: the commit stays where it is, above the
		// top layer's tip, inside the top layer's ref.
		return nil
	}
	var lastDiagnostics string
	for pos := commit.target; pos < state.topPos; pos++ {
		landed, diagnostics, err := o.attemptChildRelocationAtLayer(state, commit, pos)
		if err != nil {
			return err
		}
		if landed {
			commit.landedAt = pos
			if pos != commit.target {
				o.emitFixRelocatedWarning(parent, state.repo, commit.target, pos, lastDiagnostics)
			}
			return nil
		}
		lastDiagnostics = diagnostics
	}
	// The ladder exhausted: the commit stays above the top layer, which is
	// still above its request.
	o.emitFixRelocatedWarning(parent, state.repo, commit.target, state.topPos, lastDiagnostics)
	return nil
}

// attemptChildRelocationAtLayer attempts one commit's relocation into one
// layer: build the labelled cut points from the virtual chain, insert the
// commit after that layer's tip and drop it from its current position
// through the restack primitive, and require the new top's tree to equal the
// child head's tree. landed reports success; diagnostics carries the reason
// the attempt must retry one layer higher. A non-conflict git failure is
// returned as an error so the caller parks the transaction.
func (o *Orchestrator) attemptChildRelocationAtLayer(state *childRelocationState, commit *childRelocationCommit, pos int) (landed bool, diagnostics string, err error) {
	cps, err := o.buildChildRelocationCutPoints(state, commit.sha)
	if err != nil {
		return false, err.Error(), nil
	}
	target, ok := cps.LayerLabel[pos]
	if !ok {
		return false, fmt.Sprintf("layer %d has no cut point on the chain", pos), nil
	}
	result, err := o.deps.Worktrees.RestackChain(state.repoPath, cps.CutPoints, []git.RestackOp{
		{Kind: git.RestackInsertAfter, CutPointLabel: target, CommitSHAs: []string{commit.sha}},
		{Kind: git.RestackDropSegment, CutPointLabel: cps.FixLabel},
	})
	if err != nil {
		return false, err.Error(), nil
	}
	newTree, err := o.deps.Worktrees.CommitTreeSHA(state.repoPath, result.HeadSHA)
	if err != nil {
		return false, "", fmt.Errorf("reading the replayed top tree for repo %s: %w", state.repo, err)
	}
	if newTree != state.headTree {
		return false, "the replayed top tree differs from the child head's tree, so the relocation would not deliver the reviewed content", nil
	}
	// Landed virtually: merge the result into the state so the next attempt
	// builds its cut points from the rewritten chain.
	for old, newSHA := range result.CommitMap {
		state.commitMap[old] = newSHA
	}
	state.head = result.HeadSHA
	for _, layer := range state.stack {
		// The layer's cut point may be a merged group whose label is the
		// phase anchor's (a layer's tip usually is its last phase's
		// completion anchor), so resolve the label through the cut points
		// this attempt was built from — never a synthesized "layer:N" the
		// group may not carry. InsertAfter makes the labelled cut point's
		// new SHA the inserted commit, which is the layer's new tip.
		label := cps.LayerLabel[layer.Position]
		if label == "" {
			continue
		}
		if newSHA := cutPointNewSHA(result, label); newSHA != "" {
			state.layerTips[layer.Position] = newSHA
		}
	}
	for i := range state.commits {
		if next, ok := state.commitMap[state.commits[i].sha]; ok {
			state.commits[i].sha = next
		}
	}
	return true, "", nil
}

// childRelocationCutPoints is the labelled virtual chain one attempt
// rewrites: the cut points passed to the primitive, the label each stack
// layer's tip resolved to, and the label of the cut point sitting on the
// commit being relocated.
type childRelocationCutPoints struct {
	CutPoints  []git.RestackCutPoint
	LayerLabel map[int]string
	FixLabel   string
}

// buildChildRelocationCutPoints builds the labelled cut points of one attempt
// from the state's virtual chain: the parent's roadmap-phase anchors, the
// mutable layer tips, and the tracked child commits above the top layer's
// tip — each its own cut point, so a drop-segment removes exactly the commit
// being relocated. Consecutive cut points on the same commit merge into one
// labelled group; a layer tip aliasing an earlier group adopts its label.
func (o *Orchestrator) buildChildRelocationCutPoints(state *childRelocationState, fixSHA string) (*childRelocationCutPoints, error) {
	if o.deps.Worktrees == nil {
		return nil, errors.New("worktree operations are not configured")
	}
	type candidate struct {
		sortKey int
		label   string
		sha     string
		layer   int
		isFix   bool
	}
	var candidates []candidate
	phases := make([]int, 0, len(state.anchors))
	for phase := range state.anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	for _, phase := range phases {
		sha := state.anchors[phase][state.repo]
		if sha == "" {
			continue
		}
		candidates = append(candidates, candidate{sortKey: phase, label: fmt.Sprintf("phase:%d", phase), sha: sha})
	}
	for _, layer := range state.stack {
		tip := state.layerTips[layer.Position]
		if tip == "" {
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
		candidates = append(candidates, candidate{
			sortKey: key, label: fmt.Sprintf("layer:%d", layer.Position), sha: tip, layer: layer.Position,
		})
	}
	for i := range state.commits {
		candidates = append(candidates, candidate{
			sortKey: 200000 + state.commits[i].order, label: fmt.Sprintf("fix:%d", state.commits[i].order),
			sha: state.commits[i].sha, isFix: true,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].sortKey < candidates[j].sortKey })

	cps := &childRelocationCutPoints{LayerLabel: make(map[int]string)}
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
		return nil, errors.New("the commit being relocated is not on the chain above the top layer's tip")
	}
	return cps, nil
}

// candidate returns the ref candidate for one layer position: lower layers
// map to their cut point's new SHA; the top layer's ref owns everything up
// to the virtual head, including commits that stayed above the top.
func (s *childRelocationState) candidate(pos int) string {
	if pos == s.topPos {
		return s.head
	}
	return s.layerTips[pos]
}

// anchor returns the layer's original ref SHA captured before preparation.
func (s *childRelocationState) anchor(pos int) string {
	return s.refTips[pos]
}

// refUpdates records one ref update per parent layer at or above the lowest
// changed layer; a repository whose commits all stay above the top records a
// single ref for the top layer, and a repository with no child commits
// records a pass-through top ref whose candidate equals its anchor.
func (s *childRelocationState) refUpdates() []feature.RepoTransactionRef {
	lowest := s.topPos
	for _, layer := range s.stack {
		pos := layer.Position
		if s.candidate(pos) != s.anchor(pos) && pos < lowest {
			lowest = pos
		}
	}
	var refs []feature.RepoTransactionRef
	for _, layer := range s.stack {
		if layer.Position < lowest {
			continue
		}
		refs = append(refs, feature.RepoTransactionRef{
			Branch:       layer.Branch,
			Layer:        layer.Position,
			AnchorSHA:    s.anchor(layer.Position),
			CandidateSHA: s.candidate(layer.Position),
		})
	}
	return refs
}

// remap computes the anchor and tip remap persisted on the parent run at
// closure: every roadmap-phase anchor whose SHA was replayed maps to its new
// SHA, and every layer at or above the lowest changed layer maps its tip to
// the layer's new candidate.
func (s *childRelocationState) remap() feature.RestackRemap {
	remap := feature.RestackRemap{Anchors: map[int]string{}, Tips: map[int]string{}}
	for phase, byRepo := range s.anchors {
		if newSHA, ok := s.commitMap[byRepo[s.repo]]; ok {
			remap.Anchors[phase] = newSHA
		}
	}
	refs := s.refUpdates()
	for _, ref := range refs {
		remap.Tips[ref.Layer] = ref.CandidateSHA
	}
	return remap
}

// relocatedMap maps every child commit's original SHA to the SHA it was
// relocated to on the rewritten chain; commits that were never replayed map
// to themselves, so every entry resolves on the landed parent refs.
func (s *childRelocationState) relocatedMap() map[string]string {
	if len(s.commits) == 0 {
		return nil
	}
	relocated := make(map[string]string, len(s.commits))
	for _, c := range s.commits {
		relocated[c.original] = c.sha
	}
	return relocated
}
