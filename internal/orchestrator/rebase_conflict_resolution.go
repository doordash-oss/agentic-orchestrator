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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the policy half of rebase conflict resolution: the attempt
// budget, the per-attempt prompt and verification, the scope revert, the
// feedback loop, and the progress events. The mechanics (the paused pick,
// the marker re-check, staging, the non-interactive continuation) live in
// the git package's restack primitive.

// rebaseConflictResolutionAttempts is the resolution budget every
// conflicting commit gets: three bounded agent sessions, then the pass
// parks. A package constant by decision — no configuration key, no
// runtime-defaults field, no desktop control.
const rebaseConflictResolutionAttempts = 3

// errRebasePassStopped aborts a resolution engagement whose pass was stopped
// or discarded mid-attempt. It is an error, never a failed attempt: the
// loop exits without parking and the child stays at Created.
var errRebasePassStopped = errors.New("the rebase pass was stopped")

// rebaseRestackLoopHandle tracks one in-flight asynchronous restack loop.
// Its presence in the orchestrator's loop map is the in-flight guard that
// refuses a second start with the feature-busy error; its stopped flag is
// set by stop or discard so a running resolution attempt turns into an
// abort instead of a retryable failure.
type rebaseRestackLoopHandle struct {
	mu      sync.Mutex
	stopped bool
}

// stop marks the loop as stopping; the running attempt aborts on its next
// checkpoint instead of counting as a failed, retryable attempt.
func (h *rebaseRestackLoopHandle) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = true
}

// isStopped reports whether stop was requested.
func (h *rebaseRestackLoopHandle) isStopped() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopped
}

// rebaseConflictResolver is the per-repository resolver the restack loop
// hands to the git primitive. It closes over the pass's bookkeeping (the
// plan's groups and tips, the target, the loop handle) and accumulates the
// resolutions it lands so the loop can persist them on the restack result.
type rebaseConflictResolver struct {
	o        *Orchestrator
	parent   *feature.Feature
	child    *feature.Feature
	repoName string
	// worktree is the repository's child worktree — where commit ancestry
	// is read and where the chain being replayed lives.
	worktree string
	plan     *rebaseRestackPlan
	target   feature.RebaseRepoTarget
	handle   *rebaseRestackLoopHandle
	// resolved accumulates one record per agent-resolved conflict; the loop
	// decorates each with the primitive's dropped verdict afterwards.
	resolved []feature.RebaseResolvedConflict
}

// newRebaseConflictResolver builds one repository's resolver.
func (o *Orchestrator) newRebaseConflictResolver(parent, child *feature.Feature, repoName, worktree string, plan *rebaseRestackPlan, target feature.RebaseRepoTarget, handle *rebaseRestackLoopHandle) *rebaseConflictResolver {
	return &rebaseConflictResolver{
		o:        o,
		parent:   parent,
		child:    child,
		repoName: repoName,
		worktree: worktree,
		plan:     plan,
		target:   target,
		handle:   handle,
	}
}

// ResolveConflict runs the attempt budget for one conflicting cherry-pick.
// Every attempt: build the prompt (with the previous attempt's feedback),
// run one bounded session in the paused pick's temporary worktree, then
// verify — revert any change outside the conflicted set, scan the conflicted
// files for markers, and combine with the session outcome. A clean attempt
// resolves; a failed one records feedback and emits a transient progress
// event; the third failure exhausts the budget.
func (r *rebaseConflictResolver) ResolveConflict(input git.RestackResolverInput) (git.RestackResolverResult, error) {
	commitRoot := filepath.Join(input.AttemptRoot, shortRebaseCommit(input.CommitSHA))
	var feedback, lastFailure, lastAttemptDir string
	for attempt := 1; attempt <= rebaseConflictResolutionAttempts; attempt++ {
		if r.passStopped() {
			return git.RestackResolverResult{}, errRebasePassStopped
		}
		attemptDir := filepath.Join(commitRoot, fmt.Sprintf("attempt-%02d", attempt))
		prompt, err := r.buildPrompt(input, feedback)
		if err != nil {
			return git.RestackResolverResult{}, err
		}
		// The harness writes the attempt's prompt itself so the attempt
		// directory is complete even when the phase-runner entry point is
		// scripted away; the entry point rewrites the identical content.
		if err := os.MkdirAll(attemptDir, 0o755); err != nil {
			return git.RestackResolverResult{}, fmt.Errorf("preparing resolution attempt dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "user-prompt.md"), []byte(prompt), 0o644); err != nil {
			return git.RestackResolverResult{}, fmt.Errorf("writing the resolution attempt prompt: %w", err)
		}

		result, err := r.o.runConflictResolution(context.Background(), agent.ConflictResolutionRequest{
			FeatureID:     r.child.ID,
			SessionID:     fmt.Sprintf("%s-rebase-resolve-%s-%02d", r.child.ID, shortRebaseCommit(input.CommitSHA), attempt),
			RunNumber:     r.child.ActiveRun,
			RepoName:      r.repoName,
			WorkDir:       input.WorktreePath,
			AttemptDir:    attemptDir,
			Prompt:        prompt,
			ConflictPaths: input.ConflictFiles,
		})
		if err != nil {
			return git.RestackResolverResult{}, fmt.Errorf("resolution attempt %d for commit %s: %w", attempt, shortRebaseCommit(input.CommitSHA), err)
		}

		failure := r.verifyAttempt(input, result)
		if failure == "" {
			r.recordResolution(input, attempt)
			r.emitAttemptEvent(input, attempt, "resolved")
			return git.RestackResolverResult{Resolution: git.RestackResolutionResolved, Attempts: attempt}, nil
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "feedback.md"), []byte(failure+"\n"), 0o644); err != nil {
			return git.RestackResolverResult{}, fmt.Errorf("recording resolution feedback for attempt %d: %w", attempt, err)
		}
		r.emitAttemptEvent(input, attempt, "failed: "+failure)
		feedback, lastFailure, lastAttemptDir = failure, failure, attemptDir
	}
	return git.RestackResolverResult{
		Resolution:  git.RestackResolutionExhausted,
		Attempts:    rebaseConflictResolutionAttempts,
		LastFailure: lastFailure,
		AttemptDir:  lastAttemptDir,
	}, nil
}

// verifyAttempt inspects the temporary worktree after one session: changes
// outside the conflicted set are reverted (tracked files restored, new files
// removed) and named, the conflicted files are scanned for markers, and the
// session's own outcome is combined. An empty return means the attempt is
// clean; the conflicted files themselves stay exactly as the session left
// them, marker-free or not, so the next prompt describes reality.
func (r *rebaseConflictResolver) verifyAttempt(input git.RestackResolverInput, result *agent.ConflictResolutionResult) string {
	var problems []string
	if result != nil && result.Status != agent.ConflictResolutionCompleted {
		problems = append(problems, result.Reason)
	}
	outside, err := git.ChangedPathsOutsideSet(input.WorktreePath, input.ConflictFiles)
	if err != nil {
		return fmt.Sprintf("verifying the session's scope failed: %v", err)
	}
	if len(outside) > 0 {
		if err := git.RevertPaths(input.WorktreePath, outside); err != nil {
			return fmt.Sprintf("reverting the session's out-of-scope changes failed: %v", err)
		}
		problems = append(problems, "files outside the conflicted set were changed and reverted: "+strings.Join(outside, ", "))
	}
	if marked, err := git.ConflictMarkerFilesInPaths(input.WorktreePath, input.ConflictFiles); err != nil {
		return fmt.Sprintf("scanning the conflicted files for markers failed: %v", err)
	} else if len(marked) > 0 {
		problems = append(problems, "conflict markers remain in: "+strings.Join(marked, ", "))
	}
	return strings.Join(problems, "; ")
}

// buildPrompt renders one attempt's user prompt from the restack plan's
// bookkeeping and the previous attempt's feedback.
func (r *rebaseConflictResolver) buildPrompt(input git.RestackResolverInput, feedback string) (string, error) {
	message, err := git.CommitMessage(input.MainRepo, input.CommitSHA)
	if err != nil {
		return "", err
	}
	patch, err := git.CommitPatch(input.MainRepo, input.CommitSHA)
	if err != nil {
		return "", err
	}
	upstreamDiff, err := git.DiffPathsBetween(input.MainRepo, r.segmentBaseSHA(input.SegmentFrom), r.target.TargetSHA, input.ConflictFiles)
	if err != nil {
		return "", err
	}
	layerPosition, layerTitle := r.owningLayer(input.CommitSHA)
	roadmapPhase := r.owningRoadmapPhase(input.CommitSHA)
	// A conflicting foreign commit belongs to the diverged layer it was
	// scheduled onto, not to whatever layer the local chain's geometry
	// suggests: the plan pinned the adoption's owning layer at build time.
	if pos, ok := r.plan.foreignBySHA[input.CommitSHA]; ok {
		layerPosition = pos
		layerTitle = r.layerTitle(pos)
		roadmapPhase = r.layerLastRoadmapPhase(pos)
	}
	return roles.BuildConflictResolutionPrompt(roles.ConflictResolutionUserInput{
		FeatureName:        r.parent.Name,
		FeatureDescription: r.parent.Description,
		LayerPosition:      layerPosition,
		LayerTitle:         layerTitle,
		RoadmapPhase:       roadmapPhase,
		TargetBranch:       r.targetBranch(),
		TargetSHA:          r.target.TargetSHA,
		CommitMessage:      message,
		CommitPatch:        patch,
		ConflictFiles:      input.ConflictFiles,
		UpstreamDiff:       upstreamDiff,
		Feedback:           feedback,
	}), nil
}

// segmentBaseSHA resolves the cut point below the conflicting segment — the
// base the conflicted commit was authored on. The upstream diff runs from
// there to the target, showing exactly the movement the commit must
// integrate with.
func (r *rebaseConflictResolver) segmentBaseSHA(segmentFrom string) string {
	for _, group := range r.plan.groups {
		if group.label == segmentFrom {
			return group.sha
		}
	}
	if len(r.plan.groups) > 0 {
		return r.plan.groups[0].sha
	}
	return r.target.TargetSHA
}

// owningLayer finds the lowest stack layer whose tip sits at or above the
// conflicted commit — the layer the commit belongs to.
func (r *rebaseConflictResolver) owningLayer(commitSHA string) (int, string) {
	for _, tip := range r.plan.tips {
		if tip.sha == commitSHA || git.IsAncestor(r.worktree, tip.sha, commitSHA) {
			for _, layer := range r.parent.OrderedStackLayers() {
				if layer.Position == tip.position {
					return layer.Position, layer.Title
				}
			}
			return tip.position, ""
		}
	}
	return 0, ""
}

// owningRoadmapPhase finds the highest roadmap phase whose commit anchor sits
// at or below the conflicted commit — the phase the commit belongs to.
func (r *rebaseConflictResolver) owningRoadmapPhase(commitSHA string) int {
	anchors := r.parent.Run().RoadmapPhaseCommitAnchors
	phases := make([]int, 0, len(anchors))
	for phase := range anchors {
		phases = append(phases, phase)
	}
	sort.Ints(phases)
	owner := 0
	for _, phase := range phases {
		sha := anchors[phase][r.repoName]
		if sha != "" && (sha == commitSHA || git.IsAncestor(r.worktree, sha, commitSHA)) {
			owner = phase
		}
	}
	return owner
}

// layerTitle resolves one stack layer position to its title.
func (r *rebaseConflictResolver) layerTitle(position int) string {
	for _, layer := range r.parent.OrderedStackLayers() {
		if layer.Position == position {
			return layer.Title
		}
	}
	return ""
}

// layerLastRoadmapPhase resolves one stack layer position to its last
// roadmap phase — the highest phase the layer covers.
func (r *rebaseConflictResolver) layerLastRoadmapPhase(position int) int {
	phase := 0
	for _, layer := range r.parent.OrderedStackLayers() {
		if layer.Position != position {
			continue
		}
		for _, p := range layer.Phases {
			if p > phase {
				phase = p
			}
		}
	}
	return phase
}

// targetBranch renders the rebase target's human-readable name.
func (r *rebaseConflictResolver) targetBranch() string {
	if r.target.Ref != "" {
		return r.target.Ref
	}
	return r.target.Target
}

// recordResolution accumulates one resolution record; the loop decorates it
// with the primitive's dropped verdict after the continuation lands.
func (r *rebaseConflictResolver) recordResolution(input git.RestackResolverInput, attempts int) {
	r.resolved = append(r.resolved, feature.RebaseResolvedConflict{
		Commit:   input.CommitSHA,
		Segment:  input.SegmentFrom + ".." + input.SegmentTo,
		Files:    append([]string(nil), input.ConflictFiles...),
		Attempts: attempts,
	})
}

// emitAttemptEvent emits the transient per-attempt progress event: a plain
// message naming the repository, the commit, the attempt number, and the
// outcome. No canonical error, no persisted warning, no read-model change.
func (r *rebaseConflictResolver) emitAttemptEvent(input git.RestackResolverInput, attempt int, outcome string) {
	r.o.emitEvent(ports.Event{
		Type:      ports.RepoStatusChanged,
		FeatureID: r.child.ID,
		RepoName:  r.repoName,
		Message: fmt.Sprintf("rebase conflict resolution attempt %d of %d for commit %s %s",
			attempt, rebaseConflictResolutionAttempts, shortRebaseCommit(input.CommitSHA), outcome),
	})
}

// passStopped reports whether the pass was stopped or discarded: an explicit
// stop request on the loop handle, or the child no longer being an active
// rebase child at Created (discard closes the relationship).
func (r *rebaseConflictResolver) passStopped() bool {
	if r.handle != nil && r.handle.isStopped() {
		return true
	}
	child, err := r.o.deps.Lifecycle.Get(r.child.ID)
	if err != nil {
		return true
	}
	return child == nil || child.Parent == nil || child.Parent.Kind != feature.ChildKindRebase ||
		child.Status != feature.StatusCreated || child.Parent.CloseOutcome != ""
}

// shortRebaseCommit abbreviates a commit SHA for session ids, events, and
// directory names.
func shortRebaseCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
