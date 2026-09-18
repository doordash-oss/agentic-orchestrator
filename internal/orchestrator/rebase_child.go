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
	"os"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// RebaseChildPreflightResult carries the creation-time resolved per-repo
// targets, the per-layer classifications, the behind set, the work list, and
// the captured parent tip SHAs produced by the orchestrator preflight.
type RebaseChildPreflightResult struct {
	Bases   []feature.ChildRepoBase
	Targets []feature.RebaseRepoTarget
	// LayerStates is the per-layer classification (kept/merged/closed) per
	// repository, as observed live at preflight.
	LayerStates []feature.RebaseLayerClassification
	// Behind is the set of repositories behind their resolved target;
	// computed for every repository.
	Behind []string
	// WorkRepos is the list of repositories the pass must reconcile: each
	// has at least one kept layer with commits and is either behind its
	// target, has a merged layer whose entry still holds a tip, or has a
	// diverged layer whose remote branch held commits Agentico never
	// pushed.
	WorkRepos []string
}

// RebaseChildPreflight performs the orchestrator-owned preflight for a rebase
// child launch: it loads the parent, checks every worktree for dirty state,
// resolves each repo's merge target (PR base branch → recorded base branch →
// repository default branch), fetches origin and every layer branch for
// publishable repositories, computes behind-ness for every repository, and
// classifies every stack layer of a publishable repository by reading its
// pull request's live state through the remote operations. Merged and closed
// states observed live are persisted on the parent's stack (monotonically —
// never downgrading a recorded merged/closed entry back to open/none). Any
// closed-unmerged pull request refuses the launch with the typed stack-closed
// error naming the layer, its title, and its URL. Every kept layer of a
// publishable repository whose entry records a last-pushed SHA or a pull
// request URL is also inspected against its freshly fetched remote-tracking
// tip: a remote tip that differs from the last-pushed SHA and is not an
// ancestor of the local tip classifies the layer as diverged, with the
// observed remote tip and the ordered adoptable (non-merge) foreign commits
// pinned on the classification. A repository has work when it has at least
// one kept layer with commits and is either behind its target, has a merged
// layer whose entry still holds a tip, or has a diverged layer; when no
// repository has work, the preflight fails with the already-up-to-date
// error. If any repo fails target resolution or fetch, the whole preflight
// fails atomically with a typed error naming the repo.
func (o *Orchestrator) RebaseChildPreflight(parentID string) (*RebaseChildPreflightResult, error) {
	parent, err := o.deps.Store.Load(parentID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", feature.ErrRefactorParentNotFound, parentID)
		}
		return nil, fmt.Errorf("loading parent feature: %w", err)
	}
	if err := feature.ValidateRefactorParent(parent, nil); err != nil {
		return nil, err
	}

	bases, err := o.preflightCleanWorktreesAndCaptureSHAs(parent)
	if err != nil {
		return nil, err
	}

	targets := make([]feature.RebaseRepoTarget, 0, len(parent.Repos))
	var layerStates []feature.RebaseLayerClassification
	var behind []string
	var workRepos []string
	for i := range parent.Repos {
		repo := &parent.Repos[i]
		worktreePath := repo.WorktreePath
		if worktreePath == "" {
			worktreePath = repo.Path
		}
		publishable := repo.Publishable == nil || *repo.Publishable

		target := o.resolveRebaseTarget(parent, repo)
		if target == "" {
			return nil, &feature.RebaseTargetResolutionError{Repo: repo.Name}
		}

		var ref string
		if publishable {
			ref = "origin/" + target
			if err := git.Fetch(worktreePath); err != nil {
				return nil, &feature.RebaseFetchError{Repo: repo.Name, Err: err}
			}
			// Refresh every layer branch's remote-tracking ref so
			// classification and later restack work sees the live branch
			// positions. A layer branch the remote no longer carries (deleted
			// after its pull request merged, for example) must not fail the
			// launch: the fetch is best-effort by design.
			for _, layer := range parent.OrderedStackLayers() {
				if layer.Branch == "" {
					continue
				}
				if _, ok := layer.Repos[repo.Name]; !ok {
					continue
				}
				_ = git.FetchBranch(worktreePath, layer.Branch)
			}
			if git.IsBehindRemote(worktreePath, target) {
				behind = append(behind, repo.Name)
			}
		} else {
			ref = target
			if git.IsBehindLocal(worktreePath, target) {
				behind = append(behind, repo.Name)
			}
		}

		// Capture the target commit SHA as it stood at the creation-time
		// fetch, resolving the same ref behind-ness was computed against. The
		// mechanical integration gate reads this SHA (never re-resolving the
		// ref) so a target that moves after creation does not change what the
		// gate checks. A resolution failure is a creation-time failure
		// surfaced through the existing typed target-resolution error path.
		targetSHA, err := git.ReadRefSHA(worktreePath, ref)
		if err != nil {
			return nil, &feature.RebaseTargetResolutionError{Repo: repo.Name, Err: err}
		}

		targets = append(targets, feature.RebaseRepoTarget{
			Repo:        repo.Name,
			Target:      target,
			Ref:         ref,
			Publishable: publishable,
			TargetSHA:   targetSHA,
		})

		classification, hasKeptLayerWithCommits, hasMergedLayerWithTip, hasDivergedLayer, classifyErr := o.classifyRebaseRepoLayers(parent, repo, publishable, worktreePath)
		if classifyErr != nil {
			return nil, classifyErr
		}
		layerStates = append(layerStates, classification...)

		repoBehind := false
		for _, name := range behind {
			if name == repo.Name {
				repoBehind = true
				break
			}
		}
		// A repository has work when it still owns a chain to rebuild (at
		// least one kept layer with commits) and either its target moved
		// (behind), a merged layer's entry still holds a tip — the chain
		// above a merged base must be restacked — or a layer's remote branch
		// held commits Agentico never pushed (diverged) — the pass must
		// adopt them before republishing. A repository whose every layer
		// with commits is merged is a pass-through.
		if hasKeptLayerWithCommits && (repoBehind || hasMergedLayerWithTip || hasDivergedLayer) {
			workRepos = append(workRepos, repo.Name)
		}
	}

	if len(workRepos) == 0 {
		return nil, &feature.RebaseAlreadyUpToDateError{Targets: targets}
	}

	return &RebaseChildPreflightResult{
		Bases:       bases,
		Targets:     targets,
		LayerStates: layerStates,
		Behind:      behind,
		WorkRepos:   workRepos,
	}, nil
}

// classifyRebaseRepoLayers classifies every stack layer entry of one
// repository — kept, merged, or closed — by reading each layer pull
// request's live state through the remote operations. Only publishable
// repositories read remote state; a local-only repository classifies by
// behind-ness alone and every layer reads kept. Every kept layer of a
// publishable repository whose entry records a last-pushed SHA or a pull
// request URL is also inspected for divergence against the layer branch's
// freshly fetched remote-tracking tip. It also reports whether the
// repository has at least one kept layer with commits (the chain to
// rebuild), whether a merged layer's entry still holds a tip (the drop-work
// trigger), and whether any layer diverged (the adoption trigger). A
// closed-unmerged pull request returns the typed stack-closed error naming
// the layer, its title, and its URL.
func (o *Orchestrator) classifyRebaseRepoLayers(parent *feature.Feature, repo *feature.FeatureRepo, publishable bool, workDir string) ([]feature.RebaseLayerClassification, bool, bool, bool, error) {
	var states []feature.RebaseLayerClassification
	hasKeptLayerWithCommits := false
	hasMergedLayerWithTip := false
	hasDivergedLayer := false
	for _, layer := range parent.OrderedStackLayers() {
		entry, hasEntry := layer.Repos[repo.Name]
		if !hasEntry {
			continue
		}
		state := feature.RebaseLayerStateKept
		if publishable && entry.PRURL != "" {
			live, liveErr := o.deps.Remote.PRState(workDir, entry.PRURL)
			switch {
			case liveErr == nil && live == git.PRStateMerged:
				state = feature.RebaseLayerStateMerged
				// The one preflight write: record the live remote fact on
				// the parent's stack, monotonically. Only a merged/closed
				// state is ever written, so a recorded merged or closed entry
				// can never be downgraded to open/none; the change check
				// keeps the write a no-op when the entry already records it.
				if entry.PRState != feature.StackPRStateMerged {
					_ = o.deps.Lifecycle.SetStackLayerPRState(parent.ID, repo.Name, layer.Position, feature.StackPRStateMerged)
				}
			case liveErr == nil && live == git.PRStateClosed:
				if entry.PRState != feature.StackPRStateClosed {
					_ = o.deps.Lifecycle.SetStackLayerPRState(parent.ID, repo.Name, layer.Position, feature.StackPRStateClosed)
				}
				return nil, false, false, false, &PublishStackClosedError{
					RepoName:      repo.Name,
					Branch:        layer.Branch,
					LayerPosition: layer.Position,
					LayerTitle:    layer.Title,
					PRURL:         entry.PRURL,
					State:         live,
				}
			default:
				// Open or indeterminate (lookup error, unrecognised state)
				// reads as kept: a transient API failure must not block a
				// legitimate launch, matching publish's treatment.
			}
		}
		classification := feature.RebaseLayerClassification{
			Repo:          repo.Name,
			LayerPosition: layer.Position,
			LayerTitle:    layer.Title,
			Branch:        layer.Branch,
			State:         state,
		}
		if state == feature.RebaseLayerStateMerged && entry.TipSHA != "" {
			hasMergedLayerWithTip = true
		}
		if state == feature.RebaseLayerStateKept && !entry.NoCommits {
			hasKeptLayerWithCommits = true
		}
		// Divergence inspection: a kept layer of a publishable repository
		// whose entry records a last-pushed SHA or a pull request URL is
		// checked against the layer branch's remote-tracking ref, which the
		// preflight's fetch just refreshed. The inspection is best-effort,
		// matching the fetch: a read that fails or an absent remote branch
		// leaves the layer not diverged, and the push lease still protects
		// the remote at the tail.
		if publishable && state == feature.RebaseLayerStateKept && layer.Branch != "" &&
			(entry.LastPushedSHA != "" || entry.PRURL != "") {
			if o.classifyLayerDivergence(workDir, layer.Branch, entry, &classification) {
				hasDivergedLayer = true
			}
		}
		states = append(states, classification)
	}
	return states, hasKeptLayerWithCommits, hasMergedLayerWithTip, hasDivergedLayer, nil
}

// classifyLayerDivergence inspects one kept layer's branch for reviewer work
// the local chain lacks, recording the diverged flag, the observed remote
// tip, the ordered adoptable foreign commits, and the remote-only count on
// the classification. Reports whether the layer diverged. The local tip is
// the stack entry's recorded tip, falling back to the local branch ref; a
// layer with no resolvable local tip is never diverged.
func (o *Orchestrator) classifyLayerDivergence(workDir, branch string, entry feature.StackRepoEntry, classification *feature.RebaseLayerClassification) bool {
	remoteTip, absent, err := git.ReadRefSHAOrAbsent(workDir, "refs/remotes/origin/"+branch)
	if err != nil || absent || remoteTip == "" {
		return false
	}
	localTip := entry.TipSHA
	if localTip == "" {
		localTip, err = git.ReadRefSHA(workDir, "refs/heads/"+branch)
		if err != nil || localTip == "" {
			return false
		}
	}
	report, err := git.InspectRemoteDivergence(workDir, remoteTip, localTip, entry.LastPushedSHA)
	if err != nil || !report.Diverged {
		return false
	}
	classification.Diverged = true
	classification.RemoteTip = report.RemoteTip
	classification.RemoteOnlyCommits = report.RemoteOnlyCommits
	for _, c := range report.ForeignCommits {
		classification.ForeignCommits = append(classification.ForeignCommits, feature.RebaseForeignCommit{
			SHA:     c.SHA,
			Subject: c.Subject,
			Author:  c.Author,
		})
	}
	return true
}

// preflightCleanWorktreesAndCaptureSHAs inspects every parent worktree for
// dirty state and captures each repository's full HEAD SHA. Any dirty
// repository rejects the whole preflight with categorized diagnostics.
func (o *Orchestrator) preflightCleanWorktreesAndCaptureSHAs(parent *feature.Feature) ([]feature.ChildRepoBase, error) {
	if o.deps.Worktrees == nil {
		return nil, fmt.Errorf("cleanliness inspection is not configured")
	}
	var bases []feature.ChildRepoBase
	var dirty []feature.RepoDirtyDiagnostics
	for _, repo := range parent.Repos {
		path := repo.WorktreePath
		if path == "" {
			path = repo.Path
		}
		report, err := o.deps.Worktrees.InspectCleanliness(path, feature.DefaultDirtyPathLimit)
		if err != nil {
			return nil, fmt.Errorf("inspecting parent worktree %s: %w", repo.Name, err)
		}
		if report.Dirty() {
			dirty = append(dirty, feature.RepoDirtyDiagnostics{
				Repo:           repo.Name,
				Path:           path,
				Staged:         report.Staged,
				Unstaged:       report.Unstaged,
				Untracked:      report.Untracked,
				StagedTotal:    report.StagedTotal,
				UnstagedTotal:  report.UnstagedTotal,
				UntrackedTotal: report.UntrackedTotal,
			})
			continue
		}
		sha, err := o.deps.Worktrees.CurrentHeadSHA(path)
		if err != nil {
			return nil, fmt.Errorf("capturing HEAD of parent repo %s: %w", repo.Name, err)
		}
		bases = append(bases, feature.ChildRepoBase{Repo: repo.Name, SHA: sha, ParentBranch: repo.Branch})
	}
	if len(dirty) > 0 {
		return nil, &feature.ParentWorktreesDirtyError{Repos: dirty}
	}
	return bases, nil
}
