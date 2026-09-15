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
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// commitRound is the agent.RoundCommitHook implementation the orchestrator
// installs on the PhaseRunner (see New). The implementation and final-review
// loops report each completed round here; this layer owns the git commit:
// only repos the round actually dirtied are committed, each with the
// round-specific message. A clean worktree is a no-op (and costs no feature
// load or message render). Known untracked root orchestration artifacts are
// scrubbed before staging so `git add -A` can never track them. Commit
// failures fail the round loudly — the loops surface the error and fail
// their phase — rather than silently stranding a dirty worktree mid-phase.
//
// Final Review fix rounds on a stacked feature go further: the fixer's
// manifest partitions each repository's dirty paths by stack layer, one
// commit is created per target layer, the top layer's live tip is recorded,
// and every non-top commit is relocated into its layer through the restack
// primitive with the fallback-upward policy. Rounds of any other kind, and
// repositories without lower-layer entries, commit exactly as above; a
// stacked feature still gets its top layer's tip recorded live.
//
// Review-feedback children whose parent has a stack run their own mode for
// every round kind: the round's fix manifest partitions each repository's
// dirty paths against the parent's stack, one commit is created per target
// layer in ascending order, and each commit message carries a Stack-Layer
// trailer. Nothing is relocated and no parent ref or worktree is touched;
// see round_commits_child.go. Rounds of every other feature kind behave
// exactly as above.
func (o *Orchestrator) commitRound(input agent.RoundCommitInput) error {
	if len(input.Repos) == 0 {
		return nil
	}

	names := make([]string, 0, len(input.Repos))
	for name := range input.Repos {
		names = append(names, name)
	}
	sort.Strings(names)

	// Pass 1: resolve which repos still carry uncommitted round work after
	// scrubbing known orchestration artifacts. Typically empty — a clean
	// round pays only the status probe, no feature load or roadmap parse.
	var pending []pendingRepoCommit
	for _, name := range names {
		path := input.Repos[name]
		if path == "" || !git.HasUncommittedChanges(path) {
			continue
		}
		if err := o.scrubFinalReviewRootArtifacts(context.Background(), path); err != nil {
			return fmt.Errorf("scrubbing round-commit artifacts in repo %s: %w", name, err)
		}
		if git.HasUncommittedChanges(path) {
			pending = append(pending, pendingRepoCommit{name: name, path: path})
		}
	}
	if len(pending) == 0 {
		return nil
	}

	f, err := o.deps.Lifecycle.Get(input.FeatureID)
	if err != nil {
		return fmt.Errorf("load feature %s for round commit: %w", input.FeatureID, err)
	}
	msg := o.roundCommitMessage(f, input)

	// Review-feedback children whose parent has a stack run the child mode:
	// every round partitions its dirty paths against the parent's stack and
	// tags each commit with its target layer. Every other feature keeps the
	// paths below.
	if parentStack := o.reviewFeedbackParentStack(f); len(parentStack) > 0 {
		return o.commitReviewFeedbackChildRound(input, f, parentStack, pending, msg)
	}

	// The stack-aware path runs only for Final Review fix rounds carrying
	// the fixer's iteration directory on a feature with more than one layer.
	relocate := input.Kind == agent.RoundCommitFinalReviewFix &&
		input.FixIterationDir != "" && len(f.Stack) > 1
	var manifest agent.FixManifest
	preRoundTopTip := make(map[string]string)
	if relocate {
		var diagnostic string
		manifest, diagnostic = agent.ReadFixManifest(fixManifestPath(input.FixIterationDir))
		if diagnostic != "" {
			o.emitManifestIgnoredWarning(input.FeatureID, "", 0, "", manifestReasonUnparsable, diagnostic)
		}
		// Entries naming a repository the feature does not record are
		// ignored with a warning, once per entry.
		for _, entry := range manifest.Entries {
			if entry.Repository != "" && featureRepoByName(f, entry.Repository) == nil {
				o.emitManifestIgnoredWarning(input.FeatureID, entry.Repository, entry.Layer, "", manifestReasonUnknownRepo, "")
			}
		}
		// The top layer's pre-round tip anchors the relocation cut points;
		// capture it before the first commit moves the checked-out branch.
		if o.deps.Worktrees != nil {
			top := topStackLayer(f.Stack)
			for _, pc := range pending {
				if tip, err := o.deps.Worktrees.RefSHA(pc.path, top.Branch); err == nil {
					preRoundTopTip[pc.name] = tip
				}
			}
		}
	}

	commits := make(map[string][]roundCommitTarget)
	var failed []string
	for _, pc := range pending {
		var layerPaths map[int][]string
		hasLower := false
		var partitionErr error
		if relocate {
			layerPaths, hasLower, partitionErr = o.partitionRoundPaths(f, input.FeatureID, manifest, pc.name, pc.path)
			if partitionErr != nil {
				// Listing the dirty paths failed: degrade to a single
				// top-layer commit with the warning, exactly like any other
				// git failure.
				if requested := lowestRequestedLowerLayer(f, manifest, pc.name); requested > 0 {
					o.warnFixStayedAtTop(input.FeatureID, pc.name, requested, partitionErr.Error())
				}
			}
		}
		if !relocate || partitionErr != nil || !hasLower {
			if _, err := git.CommitAllAndGetHead(pc.path, msg); err != nil {
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
		// One commit per target layer, in ascending layer order, with the
		// unchanged round-commit message.
		positions := make([]int, 0, len(layerPaths))
		for pos := range layerPaths {
			positions = append(positions, pos)
		}
		sort.Ints(positions)
		roundFailed := false
		for _, pos := range positions {
			sha, err := git.CommitPathsAndGetHead(pc.path, msg, layerPaths[pos])
			if err != nil {
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
			commits[pc.name] = append(commits[pc.name], roundCommitTarget{SHA: sha, Layer: pos})
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

	// Live top-layer tip maintenance for every stacked feature, after every
	// round commit of any kind.
	if len(f.Stack) > 0 {
		o.recordRoundTopLayerTips(input.FeatureID, f.Stack, pending)
	}

	// Relocate each repository's non-top commits, one repository at a time.
	// Repositories are independent; nothing here fails the round.
	if relocate {
		top := topStackLayer(f.Stack)
		for _, pc := range pending {
			var nonTop []roundCommitTarget
			for _, c := range commits[pc.name] {
				if c.Layer != top.Position {
					nonTop = append(nonTop, c)
				}
			}
			if len(nonTop) == 0 {
				continue
			}
			o.relocateFinalReviewFixes(input, pc.name, pc.path, preRoundTopTip[pc.name], nonTop)
		}
	}
	return nil
}

// pendingRepoCommit is one repository the round dirtied, with its worktree
// path.
type pendingRepoCommit struct {
	name string
	path string
}

// roundCommitMessage renders the commit message for a completed round.
//
// Roadmap features carry the historical phase prefix; the phase's first
// implementation commit keeps the unlabeled "Phase N/M (type): Name" subject
// so the happy-path log reads like the former single-commit-per-phase flow,
// while every later round is labeled:
//
//	Phase 1/3 (tracer-bullet): Tracer
//	Phase 1/3 (tracer-bullet): Tracer - implementation round 2
//	Phase 1/3 (tracer-bullet): Tracer - fix round 1 (address review feedback)
//
// Final Review fixes are feature-level and use their own counter:
//
//	Final review fix 2 (address review feedback)
//
// Non-roadmap features have no phase prefix, so every round is labeled.
func (o *Orchestrator) roundCommitMessage(f *feature.Feature, input agent.RoundCommitInput) string {
	featureLine := "\n\nFeature: " + f.Slug
	if input.Kind == agent.RoundCommitFinalReviewFix {
		return fmt.Sprintf("Final review fix %d (address review feedback)%s", input.FixNumber, featureLine)
	}
	if input.PhaseNumber <= 0 {
		if input.Kind == agent.RoundCommitFix {
			return fmt.Sprintf("Fix round %d (address review feedback)%s", input.FixNumber, featureLine)
		}
		return fmt.Sprintf("Implementation round %d%s", input.Iteration, featureLine)
	}
	base := fmt.Sprintf("Phase %d/%d (%s)", input.PhaseNumber, input.TotalPhases, input.PhaseType)
	if phaseName := o.lookupRoadmapPhaseName(f, input.PhaseNumber); phaseName != "" {
		base += ": " + phaseName
	}
	switch {
	case input.Kind == agent.RoundCommitFix:
		base += fmt.Sprintf(" - fix round %d (address review feedback)", input.FixNumber)
	case !input.FirstImplementCommit:
		base += fmt.Sprintf(" - implementation round %d", input.Iteration)
	}
	return base + featureLine
}

// roundCommitEventLabel renders the human-facing round label used in
// RepoStatusChanged event messages.
func roundCommitEventLabel(input agent.RoundCommitInput) string {
	switch input.Kind {
	case agent.RoundCommitFinalReviewFix:
		return fmt.Sprintf("final review fix round %d", input.FixNumber)
	case agent.RoundCommitFix:
		return fmt.Sprintf("fix round %d", input.FixNumber)
	default:
		return fmt.Sprintf("implementation round %d", input.Iteration)
	}
}
