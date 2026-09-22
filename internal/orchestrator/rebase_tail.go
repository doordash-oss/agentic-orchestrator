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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the rebase child's closure tail. It runs after the shared
// transactional integration closed the child: every repository whose journal
// entry lists refs and which already carries at least one pull request first
// has its kept layers' open pull requests retargeted — the branch of the
// nearest lower kept layer with an open pull request, else the repository's
// base branch — and then walks the stack publish restricted to that
// repository, in full under auto-publish and in update-only mode otherwise,
// so rebased layers are force-pushed with their lease and every open pull
// request's stack section refreshed. Repositories without any pull request
// are left to the user's Publish action or the auto-publish handoff, exactly
// as before the rebase pass. Retarget and republish failures are terminal
// warnings and stored records: the tail always continues with the next
// repository and always settles.

// rebaseIntegrationTail is the rebase child's ending after the shared
// transactional integration closed the child. The tail is guarded by the
// journal's settled marker: re-entering a settled tail replays nothing, so
// historical children trigger no pushes, no retargets, and no journal churn
// on later startups. The unconditional publish-completion check and the
// parent-settled event stay with the closure tail's shared ending.
func (o *Orchestrator) rebaseIntegrationTail(child, parent *feature.Feature) error {
	if child.Parent.Transaction == nil {
		return nil
	}
	if o.deps.Remote == nil {
		// Mirror the review-feedback tail's fail-closed guard: without
		// remote operations nothing here can run, and the walk would
		// otherwise fail per repository anyway. The warning is terminal;
		// the tail still settles.
		for _, repo := range parent.Repos {
			if entry := child.Parent.Transaction.EntryByRepo(repo.Name); entry != nil && len(entry.Refs) > 0 {
				o.recordRebaseTailWarning(child.ID, repo.Name, errcat.StackBaseRetargetFailed, nil, "remote operations not configured")
			}
		}
	} else {
		layers := orderedStackLayers(parent)
		autoPublish := parent.Checkpoints.AutoPublish()
		for _, repo := range parent.Repos {
			entry := child.Parent.Transaction.EntryByRepo(repo.Name)
			if entry == nil || len(entry.Refs) == 0 {
				continue
			}
			if !rebaseRepoHasAnyPullRequest(layers, repo.Name) {
				// No pull request in this repository: nothing to
				// retarget or republish here — the user's Publish
				// action or the auto-publish handoff owns it,
				// exactly as before the rebase pass.
				continue
			}
			o.retargetRebaseRepoPRBases(child, parent, repo, layers, entry)
			opts := PublishOptions{Repos: []string{repo.Name}}
			if !autoPublish {
				// Existing pull requests are republished regardless
				// of the checkpoint; creating pull requests for
				// never-published layers still requires
				// auto-publish or the user's Publish action, so the
				// walk runs update-only.
				opts.UpdateOnly = true
			}
			// The walk stores the canonical record (remote diverged,
			// stack closed, …) on the failing repository and stops
			// that repository only; the tail continues with the next
			// one. Each walk's ending runs the publish-completion
			// check for the repositories it touched.
			_ = o.publishWithOptionsLocked(parent.ID, opts)
		}
	}

	// The unconditional publish-completion check: repositories the walks
	// touched were already checked by each walk's ending, and a tail that
	// walked nothing still owes the feature the completion evaluation. A
	// failure is advisory — the stored records own the conditions.
	if _, err := o.tryCompleteAndEmit(parent.ID); err != nil {
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: parent.ID,
			Message:   "rebase tail publish-completion check failed: " + err.Error(),
		})
	}

	// Persist the durable tail-settled marker regardless of warnings: the
	// retarget and republish failures are terminal, and the stored records
	// own the retry path.
	if err := o.deps.Store.Modify(child.ID, func(f *feature.Feature) error {
		if f.Parent.Transaction != nil {
			f.Parent.Transaction.TailSettled = true
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persist rebase tail-settled marker: %w", err)
	}
	return nil
}

// rebaseRepoHasAnyPullRequest reports whether any stack layer entry of the
// repository carries a pull-request URL.
func rebaseRepoHasAnyPullRequest(layers []feature.StackLayer, repoName string) bool {
	for _, layer := range layers {
		if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
			return true
		}
	}
	return false
}

// retargetRebaseRepoPRBases patches the bases of one repository's kept
// layers' open pull requests: the desired base is the branch of the nearest
// lower kept layer with an open pull request in that repository, else the
// repository's base branch — the same resolution a fresh layer's pull
// request would be created with. Only a differing current base is patched; a
// base that cannot be read (indeterminate) is left alone, matching the
// transient-failure tolerance of the state lookups. A failed patch records
// the stack-base-retarget warning on the journal entry's tail record and the
// walk continues: the pull request keeps its current base and the layer's
// push is unaffected.
func (o *Orchestrator) retargetRebaseRepoPRBases(child *feature.Feature, parent *feature.Feature, repo feature.FeatureRepo, layers []feature.StackLayer, entry *feature.RepoTransactionEntry) {
	dropped := make(map[int]bool, len(entry.Refs))
	for i := range entry.Refs {
		if entry.Refs[i].RefKind() == feature.RepoRefKindDelete {
			dropped[entry.Refs[i].Layer] = true
		}
	}
	entries := make(map[int]feature.StackRepoEntry, len(layers))
	for _, layer := range layers {
		entries[layer.Position] = layer.Repos[repo.Name]
	}
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = repoWorkDir(repo)
	}
	for i, layer := range layers {
		stackEntry := entries[layer.Position]
		if stackEntry.PRURL == "" || dropped[layer.Position] {
			continue
		}
		if stackEntry.PRState == feature.StackPRStateMerged || stackEntry.PRState == feature.StackPRStateClosed {
			continue
		}
		desired := stackLayerBaseBranch(layers, entries, i, repo.BaseBranch)
		if desired == "" {
			continue
		}
		current := o.deps.Remote.PRBaseBranch(repoPath, stackEntry.PRURL)
		if current == "" || current == desired {
			continue
		}
		if err := o.deps.Remote.UpdatePRBase(stackEntry.PRURL, desired); err != nil {
			o.recordRebaseTailWarning(child.ID, repo.Name, errcat.StackBaseRetargetFailed, &errcat.CodeRepository{
				Name:           repo.Name,
				Branch:         layer.Branch,
				LayerPosition:  layer.Position,
				LayerTitle:     layer.Title,
				PullRequestURL: stackEntry.PRURL,
			}, fmt.Sprintf("retarget layer %d (%q) pull request %s to base %q: %v", layer.Position, layer.Title, stackEntry.PRURL, desired, err))
		}
	}
}

// recordRebaseTailWarning durably records a rebase tail warning for a
// repository on the transaction journal entry's stored tail record. The first
// failure for a repository creates the record with the typed repositories
// block; every further failure appends one raw diagnostics line. The warning
// is terminal — it never blocks the remaining layers or repositories and the
// tail still settles.
func (o *Orchestrator) recordRebaseTailWarning(childID, repoName string, code errcat.Code, repo *errcat.CodeRepository, cause string) {
	branch := ""
	if repo != nil {
		branch = repo.Branch
	}
	if err := o.deps.Store.Modify(childID, func(f *feature.Feature) error {
		if f.Parent.Transaction == nil {
			return nil
		}
		for i := range f.Parent.Transaction.Entries {
			entry := &f.Parent.Transaction.Entries[i]
			if entry.Repo != repoName {
				continue
			}
			if entry.Tail == nil {
				record := &errcat.FailureRecord{Code: code, Diagnostics: cause}
				if repo != nil {
					r := *repo
					record.Context = &errcat.RecordContext{Repositories: []errcat.CodeRepository{r}}
				} else {
					record.Context = &errcat.RecordContext{Repositories: []errcat.CodeRepository{{Name: repoName, Branch: branch}}}
				}
				entry.Tail = record
			} else if cause != "" {
				if entry.Tail.Diagnostics != "" {
					entry.Tail.Diagnostics += "\n"
				}
				entry.Tail.Diagnostics += cause
			}
			return nil
		}
		return nil
	}); err != nil {
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: childID,
			RepoName:  repoName,
			Message:   fmt.Sprintf("failed to record rebase tail warning for %s: %v", repoName, err),
		})
	}
}
