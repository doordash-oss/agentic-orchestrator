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
	"sort"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// stackLayerTrailerPrefix is the commit-message trailer line carrying the
// parent-stack layer position a review-feedback child commit targets.
const stackLayerTrailerPrefix = "Stack-Layer: "

// reviewFeedbackParentStack resolves the parent's stack for a
// review-feedback child. Every other feature — top-level, refactor, or
// rebase — and any parent that cannot be loaded or records no stack yields
// nil, and the round commits exactly as before.
func (o *Orchestrator) reviewFeedbackParentStack(f *feature.Feature) []feature.StackLayer {
	if f.Parent == nil || f.Parent.Kind != feature.ChildKindReviewFeedback || f.Parent.ParentID == "" {
		return nil
	}
	parent, err := o.deps.Lifecycle.Get(f.Parent.ParentID)
	if err != nil || parent == nil {
		return nil
	}
	return parent.Stack
}

// commitReviewFeedbackChildRound is the review-feedback child mode of the
// round-commit hook, running for every round kind of a child whose parent
// has a stack. Each dirty repository's paths are partitioned against the
// parent's stack by the round's fix manifest; one commit is created per
// target layer in ascending order, each carrying the unchanged round-commit
// message plus a Stack-Layer trailer and staging only that layer's paths
// (deletions included). Nothing is relocated and no parent ref or worktree
// is touched; the child's own stack keeps its live tip recording as for any
// other feature.
func (o *Orchestrator) commitReviewFeedbackChildRound(input agent.RoundCommitInput, f *feature.Feature, parentStack []feature.StackLayer, pending []pendingRepoCommit, msg string) error {
	// The manifest is loaded leniently from the round's iteration directory:
	// a missing file is silent, a malformed one warns once and is treated as
	// absent.
	iterationDir := input.IterationDir
	if iterationDir == "" {
		iterationDir = input.FixIterationDir
	}
	var manifest agent.FixManifest
	if iterationDir != "" {
		var diagnostic string
		manifest, diagnostic = agent.ReadFixManifest(fixManifestPath(iterationDir))
		if diagnostic != "" {
			o.emitManifestIgnoredWarning(input.FeatureID, "", 0, "", manifestReasonUnparsable, diagnostic)
		}
	}
	// Entries naming a repository the child does not record are ignored with
	// a warning, once per entry.
	for _, entry := range manifest.Entries {
		if entry.Repository != "" && featureRepoByName(f, entry.Repository) == nil {
			o.emitManifestIgnoredWarning(input.FeatureID, entry.Repository, entry.Layer, "", manifestReasonUnknownRepo, "")
		}
	}
	defaultPos := reviewFeedbackCommentedLayer(parentStack, f.ReviewFeedback)

	var failed []string
	for _, pc := range pending {
		layerPaths, partitionErr := o.partitionChildRoundPaths(input.FeatureID, pc.name, pc.path, parentStack, manifest, defaultPos)
		if partitionErr != nil {
			// Listing the dirty paths failed: degrade to one commit holding
			// everything, tagged for the default layer, exactly like any
			// other git failure.
			if _, err := git.CommitAllAndGetHead(pc.path, stackLayerCommitMessage(msg, defaultPos)); err != nil {
				failed = append(failed, pc.name)
				o.emitEvent(ports.Event{
					Type:      ports.RepoStatusChanged,
					FeatureID: input.FeatureID,
					RepoName:  pc.name,
					Error:     err,
					Message:   fmt.Sprintf("round commit failed (%s): %v", roundCommitEventLabel(input), err),
				})
				continue
			}
			o.emitEvent(ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: input.FeatureID,
				RepoName:  pc.name,
				Message:   "committed " + roundCommitEventLabel(input),
			})
			continue
		}
		// One commit per target layer, in ascending layer order, each
		// staging only that layer's paths.
		positions := make([]int, 0, len(layerPaths))
		for pos := range layerPaths {
			positions = append(positions, pos)
		}
		sort.Ints(positions)
		roundFailed := false
		for _, pos := range positions {
			if _, err := git.CommitPathsAndGetHead(pc.path, stackLayerCommitMessage(msg, pos), layerPaths[pos]); err != nil {
				failed = append(failed, pc.name)
				roundFailed = true
				o.emitEvent(ports.Event{
					Type:      ports.RepoStatusChanged,
					FeatureID: input.FeatureID,
					RepoName:  pc.name,
					Error:     err,
					Message:   fmt.Sprintf("round commit failed (%s): %v", roundCommitEventLabel(input), err),
				})
				break
			}
		}
		if roundFailed {
			continue
		}
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: input.FeatureID,
			RepoName:  pc.name,
			Message:   "committed " + roundCommitEventLabel(input),
		})
	}
	if len(failed) > 0 {
		return fmt.Errorf("round commit failed in repo(s) %s", strings.Join(failed, ", "))
	}

	// The child's own stack keeps its live tip recording; the parent's stack
	// is only read here, never written.
	if len(f.Stack) > 0 {
		o.recordRoundTopLayerTips(input.FeatureID, f.Stack, pending)
	}
	return nil
}

// partitionChildRoundPaths assigns one repository's dirty paths to the
// parent's stack layers for a review-feedback child round: each valid
// manifest entry naming this repository sends its dirty paths to the named
// layer; invalid positions, non-dirty paths, and lower duplicates are
// reported through the FixManifestEntryIgnored warning (the highest valid
// listing wins for a duplicated path); every unlisted path — including the
// second half of a renamed path whose pair was listed — goes to defaultPos,
// the commented layer when every selected comment sits on one layer and the
// parent's top layer otherwise.
func (o *Orchestrator) partitionChildRoundPaths(featureID, repoName, worktreePath string, parentStack []feature.StackLayer, manifest agent.FixManifest, defaultPos int) (map[int][]string, error) {
	dirty, counterparts, err := git.UncommittedPaths(worktreePath)
	if err != nil {
		return nil, err
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
		if _, ok := stackLayerByPosition(parentStack, entry.Layer); !ok {
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
	// A rename's unlisted half follows its pair so the commit carrying the
	// rename holds both halves.
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
	byLayer := make(map[int][]string)
	for _, p := range dirty {
		pos := defaultPos
		if assigned, ok := assign[p]; ok {
			pos = assigned
		}
		byLayer[pos] = append(byLayer[pos], p)
	}
	return byLayer, nil
}

// reviewFeedbackCommentedLayer resolves the layer that unlisted paths of a
// review-feedback child round default to: the single layer every selected
// comment sits on when they all share one, otherwise the parent's top layer.
// A comment whose layer cannot be resolved counts as spanning layers, so an
// unknown-layer payload defaults to the top.
func reviewFeedbackCommentedLayer(stack []feature.StackLayer, comments []feature.ReviewFeedbackComment) int {
	top := topStackLayer(stack).Position
	single := 0
	for _, pos := range commentLayerPositions(comments) {
		if pos <= 0 {
			return top
		}
		if _, ok := stackLayerByPosition(stack, pos); !ok {
			return top
		}
		if single == 0 {
			single = pos
			continue
		}
		if single != pos {
			return top
		}
	}
	if single == 0 {
		return top
	}
	return single
}

// commentLayerPositions resolves the parent-stack layer position each
// selected comment sits on, from the layer tagging recorded at fetch and
// launch time. A comment recorded before layer tagging existed carries
// position 0 (unknown), so the harness default still applies to it.
func commentLayerPositions(comments []feature.ReviewFeedbackComment) []int {
	positions := make([]int, len(comments))
	for i := range comments {
		positions[i] = comments[i].LayerPosition
	}
	return positions
}

// stackLayerCommitMessage appends the Stack-Layer trailer line for one
// target layer position to a round-commit message.
func stackLayerCommitMessage(msg string, pos int) string {
	return msg + "\n\n" + stackLayerTrailerPrefix + strconv.Itoa(pos)
}
