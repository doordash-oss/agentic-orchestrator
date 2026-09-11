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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// layerBoundaryError is the typed error every layer-boundary failure
// returns. surfaceDispatchCompletionError turns it into the feature's
// terminal failure record carrying the layer_boundary_failed canonical
// code; the repositories block names each failing repository with the
// branch its worktree is on, and the diagnostics name the repository, its
// current branch, and the expected branch. Because every git step runs
// before the single persistence write and is idempotent, a retry that
// re-enters the boundary skips repositories already on the next layer's
// branch and heals.
type layerBoundaryError struct {
	diagnostics string
	repos       []errcat.CodeRepository
}

func (e *layerBoundaryError) Error() string { return e.diagnostics }

// failureRecord builds the stored terminal record for the boundary failure.
func (e *layerBoundaryError) failureRecord() errcat.FailureRecord {
	record := failureRecord(errcat.LayerBoundaryFailed, feature.PhaseImplement, e.diagnostics)
	if len(e.repos) > 0 {
		record.Context.Repositories = append(record.Context.Repositories, e.repos...)
	}
	return record
}

// stackLayerForBoundary resolves the layer whose phase list contains the
// feature's current roadmap phase. A roadmap feature with no stack, or
// whose current phase belongs to no layer, fails closed: the schema
// version guarantees every loadable roadmap feature was approved with a
// stack, so reaching here means state no path should produce.
func (o *Orchestrator) stackLayerForBoundary(f *feature.Feature) (feature.StackLayer, error) {
	if len(f.Stack) == 0 {
		return feature.StackLayer{}, &layerBoundaryError{
			diagnostics: fmt.Sprintf("feature %q has no approved pull-request stack at the layer boundary of roadmap phase %d", f.ID, f.CurrentRoadmapPhase),
		}
	}
	layer, ok := f.StackLayerForPhase(f.CurrentRoadmapPhase)
	if !ok {
		return feature.StackLayer{}, &layerBoundaryError{
			diagnostics: fmt.Sprintf("roadmap phase %d belongs to no layer of the approved pull-request stack", f.CurrentRoadmapPhase),
		}
	}
	return layer, nil
}

// repoLayerSplit is one repository's outcome of the layer boundary: the tip
// read from the completed layer's ref, whether the worktree was already on
// the next layer's branch (a healed retry), the branch the worktree was on
// when it failed, and the failure when the split could not run.
type repoLayerSplit struct {
	Repo    string
	Tip     string
	Skipped bool
	Branch  string
	Err     error
}

// splitWorktreesToNextLayer records the completed layer's tip per repository
// — read from the layer's ref, never from HEAD, so a retry after a crash
// between the git steps and the persistence write reads the same SHA — and
// splits every repository worktree onto the next layer's branch. Per
// repository: already on the next layer's branch is a skip (a healed
// retry); on the recorded branch creates and checks out the next layer's
// branch at HEAD; anything else — or a target ref that already exists while
// not checked out — is a failure naming the repository, its current branch,
// and the expected branch. A repository without a worktree path is skipped
// and its record left untouched. All git steps run before the caller's
// single persistence write.
func (o *Orchestrator) splitWorktreesToNextLayer(f *feature.Feature, layer, next feature.StackLayer) []repoLayerSplit {
	var splits []repoLayerSplit
	for i := range f.Repos {
		repo := &f.Repos[i]
		if repo.WorktreePath == "" {
			continue
		}
		if o.deps.Worktrees == nil {
			splits = append(splits, repoLayerSplit{
				Repo: repo.Name,
				Err:  fmt.Errorf("repo %q: worktree operations are unavailable, so layer %d's boundary cannot split onto %q", repo.Name, next.Position, next.Branch),
			})
			continue
		}
		tip, err := o.deps.Worktrees.RefSHA(repo.WorktreePath, layer.Branch)
		if err != nil {
			splits = append(splits, repoLayerSplit{
				Repo:   repo.Name,
				Branch: o.deps.Worktrees.CurrentBranch(repo.WorktreePath),
				Err:    fmt.Errorf("repo %q: reading layer %d's tip from ref %q: %v", repo.Name, layer.Position, layer.Branch, err),
			})
			continue
		}
		current := o.deps.Worktrees.CurrentBranch(repo.WorktreePath)
		switch {
		case current == next.Branch:
			splits = append(splits, repoLayerSplit{Repo: repo.Name, Tip: tip, Skipped: true})
		case repo.Branch == "":
			splits = append(splits, repoLayerSplit{
				Repo:   repo.Name,
				Branch: current,
				Err:    fmt.Errorf("repo %q has no feature branch recorded (worktree is on %q), so layer %d's branch %q cannot be created", repo.Name, current, next.Position, next.Branch),
			})
		case current == repo.Branch:
			if _, err := o.deps.Worktrees.RefSHA(repo.WorktreePath, next.Branch); err == nil {
				splits = append(splits, repoLayerSplit{
					Repo:   repo.Name,
					Branch: current,
					Err: fmt.Errorf("repo %q: layer %d's branch %q already exists as a ref while the worktree is on %q (recorded %q)",
						repo.Name, next.Position, next.Branch, current, repo.Branch),
				})
				continue
			}
			if err := o.deps.Worktrees.CreateBranchAtHead(repo.WorktreePath, next.Branch); err != nil {
				splits = append(splits, repoLayerSplit{
					Repo:   repo.Name,
					Branch: current,
					Err:    fmt.Errorf("repo %q: creating layer %d's branch %q: %v", repo.Name, next.Position, next.Branch, err),
				})
				continue
			}
			splits = append(splits, repoLayerSplit{Repo: repo.Name, Tip: tip})
		default:
			splits = append(splits, repoLayerSplit{
				Repo:   repo.Name,
				Branch: current,
				Err: fmt.Errorf("repo %q is on branch %q, want it on the recorded %q so it can be split onto layer %d's branch %q",
					repo.Name, current, repo.Branch, next.Position, next.Branch),
			})
		}
	}
	return splits
}

// boundaryFailure renders the split failures as one layerBoundaryError.
// The repositories block names each failing repository with the branch its
// worktree is on; the expected branch lives in the diagnostics text.
func boundaryFailure(splits []repoLayerSplit) *layerBoundaryError {
	var msgs []string
	var repos []errcat.CodeRepository
	for _, split := range splits {
		if split.Err == nil {
			continue
		}
		msgs = append(msgs, split.Err.Error())
		repos = append(repos, errcat.CodeRepository{Name: split.Repo, Branch: split.Branch})
	}
	if len(msgs) == 0 {
		return nil
	}
	return &layerBoundaryError{diagnostics: strings.Join(msgs, "\n"), repos: repos}
}

// applyLayerSplit writes the boundary outcome onto the feature in one pass,
// inside the caller's single Store.Modify write: the per-repository tips on
// the completed layer's entries, and — per split or skipped repository —
// the repository record's and worktree setup task's branch set to the next
// layer's branch. Repositories without a worktree path keep their recorded
// branch.
func applyLayerSplit(f *feature.Feature, layerPosition int, nextBranch string, splits []repoLayerSplit) {
	applyLayerTips(f, layerPosition, splits)
	for i := range f.Repos {
		for _, split := range splits {
			if split.Repo != f.Repos[i].Name {
				continue
			}
			f.Repos[i].Branch = nextBranch
			if setup := f.Run().Setup; setup != nil {
				key := "worktree:" + f.Repos[i].Name
				if task, ok := setup.Tasks[key]; ok && task.Kind == feature.SetupTaskWorktree {
					task.Branch = nextBranch
					setup.Tasks[key] = task
				}
			}
		}
	}
}

// applyLayerTips writes the per-repository tips onto one layer's entries,
// preserving any push and pull-request fields an earlier boundary or round
// hook recorded.
func applyLayerTips(f *feature.Feature, layerPosition int, splits []repoLayerSplit) {
	for i := range f.Stack {
		if f.Stack[i].Position != layerPosition {
			continue
		}
		if f.Stack[i].Repos == nil {
			f.Stack[i].Repos = make(map[string]feature.StackRepoEntry, len(splits))
		}
		for _, split := range splits {
			entry := f.Stack[i].Repos[split.Repo]
			entry.TipSHA = split.Tip
			f.Stack[i].Repos[split.Repo] = entry
		}
	}
}

// emitLayerBoundaryEvents emits the boundary observability: one
// feature-scoped observer event carrying the completed layer, its
// per-repository tips, and — for a split — the next layer's position and
// branch, plus one repository status event per repository in changed — the
// repositories whose recorded branch the boundary rewrote — so desktop
// surfaces that display the branch refresh.
func (o *Orchestrator) emitLayerBoundaryEvents(featureID string, layer, next feature.StackLayer, splits []repoLayerSplit, changed []string) {
	tips := make(map[string]string, len(splits))
	for _, split := range splits {
		if split.Err == nil && split.Tip != "" {
			tips[split.Repo] = split.Tip
		}
	}
	if o.hooks.OnLayerBoundaryCrossed != nil {
		boundary := observe.LayerBoundaryEvent{
			LayerPosition: layer.Position,
			LayerTitle:    layer.Title,
			LayerBranch:   layer.Branch,
			RepoTips:      tips,
		}
		if next.Branch != "" {
			boundary.NextLayerPosition = next.Position
			boundary.NextLayerBranch = next.Branch
		}
		o.hooks.OnLayerBoundaryCrossed(featureID, boundary)
	}
	for _, name := range changed {
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: featureID,
			RepoName:  name,
			Branch:    next.Branch,
		})
	}
}

// crossLayerBoundary runs the mid-flight layer boundary for a completed
// roadmap phase: when the phase is the last of its layer and a higher layer
// exists, it records the completed layer's per-repository tips and splits
// every repository worktree onto the next layer's branch, persisting tips,
// repository branches, and worktree setup task branches in one write before
// the roadmap phase advances. A mid-layer phase records anchors only, as
// before the stack existed. Any failure aborts before the persistence
// write, so nothing is persisted, the roadmap phase does not advance, and
// the completion dispatcher marks the feature Failed with the
// layer_boundary_failed canonical code.
func (o *Orchestrator) crossLayerBoundary(featureID string, f *feature.Feature) error {
	layer, err := o.stackLayerForBoundary(f)
	if err != nil {
		return err
	}
	if !f.IsLastPhaseOfStackLayer(f.CurrentRoadmapPhase) {
		return nil
	}
	next, ok := f.StackLayerAbove(layer.Position)
	if !ok {
		return &layerBoundaryError{
			diagnostics: fmt.Sprintf("roadmap phase %d is the top pull-request layer's last phase but %d roadmap phases remain, so the stack cannot be crossed",
				f.CurrentRoadmapPhase, f.TotalRoadmapPhases),
		}
	}
	splits := o.splitWorktreesToNextLayer(f, layer, next)
	if len(splits) == 0 {
		return nil
	}
	if failure := boundaryFailure(splits); failure != nil {
		return failure
	}
	// The repositories whose recorded branch changes, computed before the
	// persistence write: a store that mutates the caller's feature in place
	// would otherwise see no change to report.
	changed := make([]string, 0, len(splits))
	for i := range f.Repos {
		repo := &f.Repos[i]
		if repo.WorktreePath != "" && repo.Branch != next.Branch {
			changed = append(changed, repo.Name)
		}
	}
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		applyLayerSplit(ff, layer.Position, next.Branch, splits)
		return nil
	}); err != nil {
		return fmt.Errorf("persisting layer boundary onto %q: %w", next.Branch, err)
	}
	o.emitLayerBoundaryEvents(featureID, layer, next, splits, changed)
	return nil
}

// recordTopLayerTips records the top layer's per-repository tips when the
// final roadmap phase completes: the worktrees stay on the top layer's
// branch and publish delivers that branch as one pull request, so only the
// tip snapshot is recorded. The stored tip is a boundary snapshot; Final
// Review fix rounds keep committing on the checked-out branch and the ref
// stays authoritative between boundaries.
func (o *Orchestrator) recordTopLayerTips(featureID string, f *feature.Feature) error {
	if _, err := o.stackLayerForBoundary(f); err != nil {
		return err
	}
	top := f.Stack[len(f.Stack)-1]
	// Without worktree operations there is nothing to snapshot; the checked
	// out branch ref stays authoritative, matching the approval rename's
	// tolerance for unwired test orchestrators.
	if o.deps.Worktrees == nil {
		return nil
	}
	var splits []repoLayerSplit
	for i := range f.Repos {
		repo := &f.Repos[i]
		if repo.WorktreePath == "" {
			continue
		}
		tip, err := o.deps.Worktrees.RefSHA(repo.WorktreePath, top.Branch)
		if err != nil {
			splits = append(splits, repoLayerSplit{
				Repo: repo.Name,
				Err:  fmt.Errorf("repo %q: reading the top layer's tip from ref %q: %v", repo.Name, top.Branch, err),
			})
			continue
		}
		splits = append(splits, repoLayerSplit{Repo: repo.Name, Tip: tip})
	}
	if len(splits) == 0 {
		return nil
	}
	if failure := boundaryFailure(splits); failure != nil {
		return failure
	}
	if err := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
		applyLayerTips(ff, top.Position, splits)
		return nil
	}); err != nil {
		return fmt.Errorf("persisting the top layer's tips: %w", err)
	}
	o.emitLayerBoundaryEvents(featureID, top, feature.StackLayer{}, splits, nil)
	return nil
}

// asLayerBoundaryError extracts the typed layer-boundary failure, if any.
func asLayerBoundaryError(err error) (*layerBoundaryError, bool) {
	var boundary *layerBoundaryError
	if errors.As(err, &boundary) {
		return boundary, true
	}
	return nil, false
}
