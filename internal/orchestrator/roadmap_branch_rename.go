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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// stackLayersWithBranches fills in every layer's branch name from the
// workspace slug, the position, and the layer slug. Derivation happens at
// approval for all layers; layer 1 is the only one that exists as a git ref
// after this phase.
func stackLayersWithBranches(layers []feature.StackLayer, workspaceSlug string) []feature.StackLayer {
	if len(layers) == 0 {
		return layers
	}
	out := make([]feature.StackLayer, len(layers))
	for i, layer := range layers {
		layer.Branch = git.LayerBranchName(workspaceSlug, layer.Position, layer.Slug)
		out[i] = layer
	}
	return out
}

// repoBranchRename is one repository's outcome of the approval rename. Branch
// is the name the repository ends up recorded on: the approved layer-1 name
// after a rename or a no-op, or the branch still checked out after a failure.
type repoBranchRename struct {
	Repo    string
	Branch  string
	Renamed bool
	Err     error
}

// renameWorktreeBranchesToLayerOne renames the checked-out branch in every
// repository worktree from the recorded branch to layer 1's approved branch.
// Per repository: already on the target is a skip; on the recorded branch is
// an in-place rename; anything else — or a target ref that already exists —
// is a failure naming the repository, the current branch, and the expected
// branch. A repository without a worktree path is skipped and its record
// left untouched. All renames run before the caller's single persistence
// write, so a crash between the two is healed by the retry: the retry sees
// already-renamed repositories on the target and skips them.
func (o *Orchestrator) renameWorktreeBranchesToLayerOne(f *feature.Feature, layers []feature.StackLayer) []repoBranchRename {
	if o.deps.Worktrees == nil || f == nil || len(layers) == 0 {
		return nil
	}
	target := layers[0].Branch
	if target == "" {
		return nil
	}
	var renames []repoBranchRename
	for i := range f.Repos {
		repo := &f.Repos[i]
		if repo.WorktreePath == "" {
			continue
		}
		current := o.deps.Worktrees.CurrentBranch(repo.WorktreePath)
		switch {
		case current == target:
			renames = append(renames, repoBranchRename{Repo: repo.Name, Branch: target})
		case repo.Branch == "":
			renames = append(renames, repoBranchRename{
				Repo:   repo.Name,
				Branch: current,
				Err:    fmt.Errorf("repo %q has no feature branch recorded (worktree is on %q)", repo.Name, current),
			})
		case current == repo.Branch:
			if err := o.deps.Worktrees.RenameBranch(repo.WorktreePath, repo.Branch, target); err != nil {
				renames = append(renames, repoBranchRename{
					Repo:   repo.Name,
					Branch: current,
					Err:    fmt.Errorf("repo %q: renaming branch %q to %q: %w", repo.Name, repo.Branch, target, err),
				})
			} else {
				renames = append(renames, repoBranchRename{Repo: repo.Name, Branch: target, Renamed: true})
			}
		default:
			renames = append(renames, repoBranchRename{
				Repo:   repo.Name,
				Branch: current,
				Err: fmt.Errorf("repo %q is on branch %q, want it on the recorded %q so it can be renamed to %q",
					repo.Name, current, repo.Branch, target),
			})
		}
	}
	return renames
}

// joinRenameFailures renders every failed rename, one per line.
func joinRenameFailures(renames []repoBranchRename) error {
	var msgs []string
	for _, rename := range renames {
		if rename.Err != nil {
			msgs = append(msgs, rename.Err.Error())
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	return errors.New(strings.Join(msgs, "\n"))
}

// emitRoadmapBranchRenameWarning emits the per-repository warning event for a
// rename failure on the no-gate auto-approval path: the run still advances,
// and that repository's record stays on the branch actually checked out.
func (o *Orchestrator) emitRoadmapBranchRenameWarning(featureID string, rename repoBranchRename) {
	repositories := []errcat.CodeRepository{{Name: rename.Repo, Branch: rename.Branch}}
	warning := errcat.New(
		errcat.RoadmapBranchRenameFailed,
		errcat.WithRepositories(repositories...),
		errcat.WithParams(errcat.WarningRepoParams{Repositories: repositories}),
		errcat.WithDiagnostics(rename.Err.Error()),
	)
	o.emitEvent(ports.Event{
		Type:           ports.RepoStatusChanged,
		FeatureID:      featureID,
		RepoName:       rename.Repo,
		Branch:         rename.Branch,
		Message:        warning.Summary,
		CanonicalError: &warning,
	})
}

// applyApprovedStack writes the approval outcome onto the feature in one
// pass, inside the caller's single Store.Modify write: the phase count, the
// stack with branch names, and — per renamed or already-placed repository —
// the repository record's and worktree setup task's branch. Repositories
// without a worktree path keep their recorded branch. The layer-1 entry
// keeps the approved name unless every worktree-holding repository failed to
// reach it, in which case it records the branch those worktrees are on.
func applyApprovedStack(f *feature.Feature, phaseCount int, layers []feature.StackLayer, renames []repoBranchRename) {
	f.TotalRoadmapPhases = phaseCount
	f.Stack = layers
	if len(renames) == 0 {
		return
	}
	anyOnTarget := false
	for _, rename := range renames {
		if rename.Branch == layers[0].Branch {
			anyOnTarget = true
		}
	}
	if !anyOnTarget {
		// Every worktree stayed on its recorded branch: the layer-1 entry
		// must equal the branch the worktrees are actually on.
		fallback := append([]feature.StackLayer(nil), layers...)
		fallback[0].Branch = renames[0].Branch
		f.Stack = fallback
	}
	for i := range f.Repos {
		for _, rename := range renames {
			if rename.Repo != f.Repos[i].Name {
				continue
			}
			f.Repos[i].Branch = rename.Branch
			if setup := f.Run().Setup; setup != nil {
				key := "worktree:" + f.Repos[i].Name
				if task, ok := setup.Tasks[key]; ok && task.Kind == feature.SetupTaskWorktree {
					task.Branch = rename.Branch
					setup.Tasks[key] = task
				}
			}
		}
	}
}

// deriveApprovedStack re-reads the roadmap from disk — so edits made at the
// review gate are honored — and derives the phase count plus every layer's
// definition and branch name. A roadmap that cannot yield a stack returns an
// error; nothing has been persisted at that point.
func (o *Orchestrator) deriveApprovedStack(f *feature.Feature) (int, []feature.StackLayer, error) {
	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return 0, nil, fmt.Errorf("roadmap artifact is missing, so the pull-request stack cannot be derived")
	}
	data, err := readFileSafe(roadmapPath)
	if err != nil {
		return 0, nil, fmt.Errorf("reading roadmap to derive the pull-request stack: %w", err)
	}
	phases, err := agent.ParseRoadmap(data)
	if err != nil {
		return 0, nil, fmt.Errorf("parsing roadmap to derive the pull-request stack: %w", err)
	}
	rows, problems := agent.ValidateRoadmapPullRequestsTable(data, phases)
	if len(problems) > 0 {
		return 0, nil, fmt.Errorf("roadmap ## Pull Requests table is invalid: %s", strings.Join(problems, "; "))
	}
	if deliveryProblems := agent.ValidateRoadmapPullRequestsDeliveryMode(f.EffectiveDeliveryMode(), rows); len(deliveryProblems) > 0 {
		return 0, nil, fmt.Errorf("roadmap ## Pull Requests table violates the single-pull-request delivery mode: %s", strings.Join(deliveryProblems, "; "))
	}
	layers := stackLayersWithBranches(agent.DeriveStackLayers(rows), f.WorkspaceSlug())
	return len(phases), layers, nil
}
