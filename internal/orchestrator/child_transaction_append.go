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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// This file implements refactor-child append preparation: the child's own
// stack — built in its own branch namespace through roadmap approval, layer
// boundary splits, and round-commit tip recording — is mapped onto the
// parent's stack as appended layers. Child layer j of M becomes parent
// position n+j, where n is the parent's current top position, named with the
// parent's workspace slug and numbering and the child's layer slug; the
// child's recorded layer tip in each repository is that layer's candidate
// SHA. Because the child's history sits directly on the parent tip captured
// at launch, no replay is needed: the apply-step ref transaction creates the
// new refs expecting absence while atomically verifying the parent's top ref
// is still at its anchor. Existing parent layers are never rewritten.

// refactorAppendChild reports whether this is a refactor child, whose
// integration appends the child's layers onto the parent's stack. Rebase
// children keep merge-candidate and pass-through preparation; review-feedback
// children keep the relocation ladder (stacked parents) or the merge
// candidate (stackless parents).
func refactorAppendChild(child *feature.Feature) bool {
	return child != nil &&
		child.Parent != nil &&
		child.Parent.Kind == feature.ChildKindRefactor
}

// prepareAppendedLayerCandidates fills every journal entry of a refactor
// child with the appended layer refs: the created refs for each appended
// layer, the repository's previous top, and the journal-level appended layer
// definitions. It runs after the commit-remaining-changes, parent-tip drift,
// dirty-parent, and repository-mapping gates, and parks — with the
// candidate-failed or ref-race attention — when either feature records no
// delivery stack, when the candidates do not form one ascending ancestry
// chain from the parent tip, or when a parent-facing branch name already
// exists locally. No ref is created and no parent ref moves.
func (o *Orchestrator) prepareAppendedLayerCandidates(child, parent *feature.Feature, journal *feature.TransactionJournal) (ok bool, err error) {
	if len(journal.Entries) == 0 {
		return false, fmt.Errorf("transaction: no repository entries to append layers onto")
	}

	// A parent with no persisted stack, or a child with no persisted stack,
	// parks with the candidate-failed attention naming the missing stack; no
	// one-layer stack is synthesized.
	if len(parent.Stack) == 0 || len(child.Stack) == 0 {
		missing := "child"
		if len(parent.Stack) == 0 {
			missing = "parent"
		}
		journal.Phase = feature.TransactionPhaseAttention
		finding := entryFinding(&journal.Entries[0], errcat.IntegrationCandidateFailed,
			fmt.Sprintf("%s feature records no delivery stack; refactor integration appends the child's layers onto the parent's stack, and a one-layer stack is never synthesized", missing))
		return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
	}

	topPosition := parentTopLayerPosition(parent)
	parentTop, ok := stackLayerByPosition(parent.Stack, topPosition)
	if !ok || parentTop.Branch == "" {
		journal.Phase = feature.TransactionPhaseAttention
		finding := entryFinding(&journal.Entries[0], errcat.IntegrationCandidateFailed,
			fmt.Sprintf("parent feature records no branch for its top stack layer at position %d", topPosition))
		return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
	}
	childLayers := childStackLayerCount(child)

	// The appended layer definitions, recorded once on the journal so closure
	// and recovery can persist them without the child record: position n+j,
	// the child's title and slug, a branch named from the parent's workspace
	// slug and numbering with the child's layer slug, and the origin pointing
	// at the child and its layer position.
	defs := make([]feature.AppendedLayer, 0, childLayers)
	for j := 1; j <= childLayers; j++ {
		layer, ok := stackLayerByPosition(child.Stack, j)
		if !ok {
			journal.Phase = feature.TransactionPhaseAttention
			finding := entryFinding(&journal.Entries[0], errcat.IntegrationCandidateFailed,
				fmt.Sprintf("child feature records no stack layer at position %d; positions must be contiguous from 1", j))
			return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
		}
		defs = append(defs, feature.AppendedLayer{
			Position: topPosition + j,
			Title:    layer.Title,
			Slug:     layer.Slug,
			Branch:   git.LayerBranchName(parent.WorkspaceSlug(), topPosition+j, layer.Slug),
			Origin: &feature.StackLayerOrigin{
				SourceFeatureID:     child.ID,
				SourceLayerPosition: j,
			},
		})
	}
	journal.AppendedLayers = defs

	for i := range journal.Entries {
		entry := &journal.Entries[i]
		parentRepo := featureRepoByName(parent, entry.Repo)
		if parentRepo == nil {
			entry.PrepState = feature.RepoPrepFailed
			journal.Phase = feature.TransactionPhaseAttention
			finding := entryFinding(entry, errcat.IntegrationRepositoryMissing,
				fmt.Sprintf("parent no longer has repository %s", entry.Repo))
			return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
		}
		if entry.PreviousTop == nil {
			entry.PrepState = feature.RepoPrepFailed
			journal.Phase = feature.TransactionPhaseAttention
			finding := entryFinding(entry, errcat.IntegrationCandidateFailed,
				fmt.Sprintf("repo %s records no previous top; the parent tip was not captured during preparation", entry.Repo))
			return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
		}

		// Per repository, the candidate of appended layer j is the child's
		// recorded tip for layer j, or the tip below it when none is recorded;
		// the top layer's candidate is the child head after remaining changes
		// were committed. A repository the child did not touch still receives
		// created refs at the parent tip so positions never skip.
		below := entry.PreviousTop.TipSHA
		entry.Refs = make([]feature.RepoTransactionRef, 0, len(defs))
		for _, def := range defs {
			candidate := below
			if def.Position == topPosition+childLayers {
				candidate = entry.ChildHeadSHA
			} else if childLayer, ok := stackLayerByPosition(child.Stack, def.Origin.SourceLayerPosition); ok {
				if tip := childLayer.Repos[entry.Repo].TipSHA; tip != "" {
					candidate = tip
				}
			}
			entry.Refs = append(entry.Refs, feature.RepoTransactionRef{
				Kind:         feature.RepoRefKindCreate,
				Branch:       def.Branch,
				Layer:        def.Position,
				CandidateSHA: candidate,
			})
		}

		// The candidates and the parent tip must form one ascending ancestry
		// chain: every candidate is the tip below it or a descendant of it.
		lower := entry.PreviousTop.TipSHA
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			if ref.CandidateSHA == "" {
				entry.PrepState = feature.RepoPrepFailed
				journal.Phase = feature.TransactionPhaseAttention
				finding := entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("repo %s records no candidate for appended layer %d", entry.Repo, ref.Layer))
				return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
			}
			if ref.CandidateSHA != lower && !git.IsAncestor(parentRepo.Path, lower, ref.CandidateSHA) {
				entry.PrepState = feature.RepoPrepFailed
				journal.Phase = feature.TransactionPhaseAttention
				finding := entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("repo %s: appended layer %d's candidate %s is not a descendant of the tip below it (%s); the candidates must form one ascending ancestry chain from the parent tip",
						entry.Repo, ref.Layer, ref.CandidateSHA, lower))
				return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
			}
			lower = ref.CandidateSHA
		}

		// A parent-facing branch name that already exists locally parks with
		// the ref-race attention naming the branch; nothing is renamed or
		// suffixed silently. The create-expecting-absence transaction refuses
		// it again at apply.
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			refName := "refs/heads/" + ref.Branch
			_, absent, err := o.deps.Worktrees.RefSHAOrAbsent(parentRepo.Path, refName)
			if err != nil {
				entry.PrepState = feature.RepoPrepFailed
				journal.Phase = feature.TransactionPhaseAttention
				finding := entryFinding(entry, errcat.IntegrationCandidateFailed,
					fmt.Sprintf("reading ref %s: %v", refName, err))
				return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
			}
			if !absent {
				entry.PrepState = feature.RepoPrepFailed
				journal.Phase = feature.TransactionPhaseAttention
				finding := entryFinding(entry, errcat.IntegrationRefRace,
					fmt.Sprintf("branch %s already exists locally in repo %s; appended layer branches must not exist before integration", ref.Branch, entry.Repo))
				return false, o.parkIntegrationAttention(child, journal, []integrationFinding{finding})
			}
		}

		entry.PrepState = feature.RepoPrepPrepared
	}
	return true, nil
}

// childStackLayerCount returns the child's highest stack position, the number
// of layers the child's roadmap derived.
func childStackLayerCount(child *feature.Feature) int {
	if child == nil {
		return 0
	}
	return parentTopLayerPosition(child)
}

// appendRefUpdates builds one repository's ref transaction: an atomic
// verify of the previous top ref at its recorded tip for entries that carry
// one, plus a create — expecting absence — for every created ref, a delete —
// expecting the anchor — for every deleted ref, and an anchor-to-candidate
// move for every rewrite ref. Entries without a previous-top record (rebase
// and review-feedback shapes) get their plain ref updates only.
func appendRefUpdates(entry *feature.RepoTransactionEntry) []git.RefUpdate {
	updates := make([]git.RefUpdate, 0, len(entry.Refs)+1)
	// A previous top the transaction deletes skips the verify line: the
	// delete line on the same ref is a compare-and-swap expecting the
	// anchor, which already refuses the whole batch unless the ref sits
	// where the journal recorded it.
	if entry.PreviousTop != nil && !previousTopDeleted(entry) {
		// Equal old and new SHAs produce a verify: the previous top ref must
		// still sit at the tip captured at preparation, or the whole batch —
		// including every create — is refused.
		updates = append(updates, git.RefUpdate{
			Ref:    "refs/heads/" + entry.PreviousTop.Branch,
			OldSHA: entry.PreviousTop.TipSHA,
			NewSHA: entry.PreviousTop.TipSHA,
		})
	}
	for j := range entry.Refs {
		ref := &entry.Refs[j]
		if ref.RefKind() == feature.RepoRefKindCreate {
			// An empty old SHA means the ref must be absent: the create is
			// refused when anything — the operator or another process —
			// already created the branch.
			updates = append(updates, git.RefUpdate{
				Ref:    "refs/heads/" + ref.Branch,
				NewSHA: ref.CandidateSHA,
			})
			continue
		}
		if ref.RefKind() == feature.RepoRefKindDelete {
			// An empty new SHA deletes the ref expecting it to sit at its
			// anchor: the delete is refused — atomically with every other
			// line — when the ref moved or is already gone.
			updates = append(updates, git.RefUpdate{
				Ref:    "refs/heads/" + ref.Branch,
				OldSHA: ref.AnchorSHA,
			})
			continue
		}
		updates = append(updates, git.RefUpdate{
			Ref:    "refs/heads/" + ref.Branch,
			OldSHA: ref.AnchorSHA,
			NewSHA: ref.CandidateSHA,
		})
	}
	return updates
}

// rollbackRefUpdateFor maps one at-candidate ref onto its rollback update:
// a created ref is deleted — an empty new SHA expecting the candidate — a
// deleted ref is recreated at its anchor — an empty old SHA expecting
// absence, the mirror of the create rollback's delete — and a rewrite ref is
// restored to its anchor.
func rollbackRefUpdateFor(ref *feature.RepoTransactionRef) git.RefUpdate {
	refName := "refs/heads/" + ref.Branch
	if ref.RefKind() == feature.RepoRefKindCreate {
		return git.RefUpdate{Ref: refName, OldSHA: ref.CandidateSHA}
	}
	if ref.RefKind() == feature.RepoRefKindDelete {
		return git.RefUpdate{Ref: refName, NewSHA: ref.AnchorSHA}
	}
	return git.RefUpdate{Ref: refName, OldSHA: ref.CandidateSHA, NewSHA: ref.AnchorSHA}
}

// setParentRepoBranchRecord points one repository's record — the repo entry
// and its worktree setup task — at the given branch inside the caller's
// Store.Modify write, mirroring the layer boundary split's branch rewrite.
func setParentRepoBranchRecord(f *feature.Feature, repoName, branch string) {
	for i := range f.Repos {
		if f.Repos[i].Name != repoName {
			continue
		}
		f.Repos[i].Branch = branch
	}
	if setup := f.Run().Setup; setup != nil {
		key := "worktree:" + repoName
		if task, ok := setup.Tasks[key]; ok && task.Kind == feature.SetupTaskWorktree {
			task.Branch = branch
			setup.Tasks[key] = task
		}
	}
}

// moveParentRepoBranch durably points the parent's repository record and
// worktree setup task at the given branch. Idempotent; used by apply
// (forward to the new top layer), rollback, and discard (back to the
// previous top).
func (o *Orchestrator) moveParentRepoBranch(parentID, repoName, branch string) error {
	return o.deps.Store.Modify(parentID, func(f *feature.Feature) error {
		setParentRepoBranchRecord(f, repoName, branch)
		return nil
	})
}

// applyAppendedLayers persists the appended layer definitions with
// per-repository tips equal to the candidates onto the parent run, inside
// the closure write that applies the remap. Idempotent: a position already
// present with the same branch is left alone, and the per-repository tips
// are absolute SHAs, so a crash before this write is repaired by the
// merged-phase re-entry. The repository records stay on the new top layer's
// branch, repairing a crash between the apply transaction and the record
// write.
func applyAppendedLayers(f *feature.Feature, journal *feature.TransactionJournal) {
	for _, def := range journal.AppendedLayers {
		found := false
		for i := range f.Stack {
			if f.Stack[i].Position != def.Position {
				continue
			}
			found = true
			if f.Stack[i].Branch != def.Branch {
				f.Stack[i].Title = def.Title
				f.Stack[i].Slug = def.Slug
				f.Stack[i].Branch = def.Branch
				f.Stack[i].Origin = def.Origin
			}
			break
		}
		if !found {
			f.Stack = append(f.Stack, feature.StackLayer{
				Position: def.Position,
				Title:    def.Title,
				Slug:     def.Slug,
				Branch:   def.Branch,
				Origin:   def.Origin,
			})
		}
	}
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		for j := range entry.Refs {
			ref := &entry.Refs[j]
			if ref.RefKind() != feature.RepoRefKindCreate || ref.CandidateSHA == "" {
				continue
			}
			for k := range f.Stack {
				if f.Stack[k].Position != ref.Layer {
					continue
				}
				if f.Stack[k].Repos == nil {
					f.Stack[k].Repos = make(map[string]feature.StackRepoEntry)
				}
				layerEntry := f.Stack[k].Repos[entry.Repo]
				layerEntry.TipSHA = ref.CandidateSHA
				f.Stack[k].Repos[entry.Repo] = layerEntry
			}
		}
		if entry.PreviousTop != nil {
			if top := entry.TopRef(); top != nil {
				setParentRepoBranchRecord(f, entry.Repo, top.Branch)
			}
		}
	}
}
