// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// pullRequestResolutionTarget is the validated repository-and-layer target
// of a reopen or recreate action.
type pullRequestResolutionTarget struct {
	f        *feature.Feature
	repo     feature.FeatureRepo
	layer    feature.StackLayer
	entry    feature.StackRepoEntry
	workDir  string
	repoPath string
}

// resolutionTarget loads the feature and validates the named repository and
// stack layer: the repository must exist, be publishable, and carry a
// worktree, the stack must have a layer at the position, and that layer's
// entry for the repository must record a pull request. Validation failures
// are plain errors the action surface maps to bad requests; no remote call
// has happened when one is returned.
func (o *Orchestrator) resolutionTarget(featureID, repoName string, layerPosition int) (pullRequestResolutionTarget, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return pullRequestResolutionTarget{}, fmt.Errorf("load feature: %w", err)
	}
	repo, ok := findRepo(f, repoName)
	if !ok {
		return pullRequestResolutionTarget{}, fmt.Errorf("unknown repository %q for feature %s", repoName, featureID)
	}
	if repo.Publishable != nil && !*repo.Publishable {
		return pullRequestResolutionTarget{}, fmt.Errorf("repository %q is local-only and has no pull requests to resolve", repoName)
	}
	workDir := repoWorkDir(repo)
	if workDir == "" {
		return pullRequestResolutionTarget{}, fmt.Errorf("repository %q has no worktree", repoName)
	}
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = workDir
	}
	var layer feature.StackLayer
	found := false
	for _, l := range f.Stack {
		if l.Position == layerPosition {
			layer, found = l, true
			break
		}
	}
	if !found {
		return pullRequestResolutionTarget{}, fmt.Errorf("feature %s has no stack layer at position %d", featureID, layerPosition)
	}
	entry := layer.Repos[repoName]
	if entry.PRURL == "" {
		return pullRequestResolutionTarget{}, fmt.Errorf("layer %d of repository %q has no recorded pull request", layerPosition, repoName)
	}
	return pullRequestResolutionTarget{
		f:        f,
		repo:     repo,
		layer:    layer,
		entry:    entry,
		workDir:  workDir,
		repoPath: repoPath,
	}, nil
}

// livePullRequestState reads the layer's pull request live state. An
// indeterminate answer (lookup error or unrecognised state) proceeds on the
// recorded state, exactly as the publish walk treats it.
func (o *Orchestrator) livePullRequestState(target pullRequestResolutionTarget) string {
	state, err := o.deps.Remote.PRState(target.repoPath, target.entry.PRURL)
	if err != nil {
		return ""
	}
	return state
}

// emitPullRequestResolutionEvents reports one resolution outcome: a
// repository status event naming the layer and the per-layer publish
// observer event with the resolution action value.
func (o *Orchestrator) emitPullRequestResolutionEvents(target pullRequestResolutionTarget, entry feature.StackRepoEntry, action observe.LayerPublishAction, message string) {
	o.emitEvent(ports.Event{
		Type:          ports.RepoStatusChanged,
		FeatureID:     target.f.ID,
		RepoName:      target.repo.Name,
		Branch:        target.repo.Branch,
		LayerPosition: target.layer.Position,
		Message:       message,
	})
	o.emitLayerPublishEvent(target.f, target.repo.Name, target.layer, entry, action)
}

// ReopenPullRequest resolves a repository's closed stack pull request by
// reopening it on GitHub. The action is idempotent: a live-open pull request
// is recorded open — which clears the stored repository record — without a
// remote write, and a live-merged one is recorded merged the same way. A
// closed or indeterminate live state calls the reopen operation with the
// layer branch; success records the layer open, re-injects the stack
// section into the repository's open pull requests, and emits the
// repository status and layer publish events. A missing head branch stores
// the head-branch-missing record offering only Recreate; any other refusal
// stores the reopen-failed record naming the layer and the pull request.
func (o *Orchestrator) ReopenPullRequest(featureID, repoName string, layerPosition int) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationPublish); err != nil {
		return err
	}
	target, err := o.resolutionTarget(featureID, repoName, layerPosition)
	if err != nil {
		return err
	}

	switch o.livePullRequestState(target) {
	case git.PRStateOpen:
		_ = o.deps.Lifecycle.SetStackLayerPRState(featureID, repoName, layerPosition, feature.StackPRStateOpen)
		return nil
	case git.PRStateMerged:
		_ = o.deps.Lifecycle.SetStackLayerPRState(featureID, repoName, layerPosition, feature.StackPRStateMerged)
		return nil
	}

	if err := o.deps.Remote.ReopenPullRequest(target.repoPath, target.layer.Branch, target.entry.PRURL); err != nil {
		var resolutionErr error
		if errors.Is(err, git.ErrPRHeadBranchMissing) {
			resolutionErr = &PublishHeadBranchMissingError{
				RepoName:      repoName,
				Branch:        target.layer.Branch,
				LayerPosition: target.layer.Position,
				LayerTitle:    target.layer.Title,
				PRURL:         target.entry.PRURL,
			}
		} else {
			resolutionErr = &PublishReopenFailedError{
				RepoName:      repoName,
				Branch:        target.layer.Branch,
				LayerPosition: target.layer.Position,
				LayerTitle:    target.layer.Title,
				PRURL:         target.entry.PRURL,
				Err:           err,
			}
		}
		o.storePublishFailure(target.f, repoName, resolutionErr)
		return resolutionErr
	}

	entry := target.entry
	entry.PRState = feature.StackPRStateOpen
	_ = o.deps.Lifecycle.SetStackLayerPRState(featureID, repoName, layerPosition, feature.StackPRStateOpen)
	o.reinjectStackSections(featureID, repoName)
	o.emitPullRequestResolutionEvents(target, entry, observe.LayerPublishActionReopened, "reopen pull request updated repository layer entry")
	return nil
}

// RecreatePullRequest resolves a repository's closed stack pull request by
// pushing the layer branch again and opening a fresh pull request for it.
// A live-open or live-merged pull request is recorded with that state and
// refused as a conflict — there is nothing to recreate. A closed or
// indeterminate state pushes the layer branch through the layer-aware push
// with the entry's last-pushed SHA as the lease, reuses the closed pull
// request's body with the harness-owned sections stripped and re-injected —
// falling back to the body-only description session when the read fails —
// and creates the pull request with the layer's table title, the publish
// walk's base rule, and the draft checkpoint. The new pull request is
// recorded open with the pushed SHA, cross-references and stack sections
// are re-injected, and the old closed pull request is left untouched.
func (o *Orchestrator) RecreatePullRequest(featureID, repoName string, layerPosition int) error {
	o.relationshipMu.RLock()
	defer o.relationshipMu.RUnlock()
	if err := o.RelationshipGuard(featureID, MutationPublish); err != nil {
		return err
	}
	target, err := o.resolutionTarget(featureID, repoName, layerPosition)
	if err != nil {
		return err
	}

	switch state := o.livePullRequestState(target); state {
	case git.PRStateOpen:
		_ = o.deps.Lifecycle.SetStackLayerPRState(featureID, repoName, layerPosition, feature.StackPRStateOpen)
		return &PublishRecreateMootError{RepoName: repoName, LayerPosition: layerPosition, State: state}
	case git.PRStateMerged:
		_ = o.deps.Lifecycle.SetStackLayerPRState(featureID, repoName, layerPosition, feature.StackPRStateMerged)
		return &PublishRecreateMootError{RepoName: repoName, LayerPosition: layerPosition, State: state}
	}

	// Push first, through the Phase 7 layer-aware primitive: an absent
	// remote branch gets a plain push, a remote at the last-pushed SHA gets
	// a no-op or lease push, and anything else fails with the existing
	// remote-diverged record naming the rebase pass.
	pushedSHA, pushErr := o.pushStackLayer(repoName, target.layer, target.entry, target.workDir)
	if pushErr != nil {
		o.storePublishFailure(target.f, repoName, pushErr)
		return pushErr
	}
	_ = o.deps.Lifecycle.RecordStackLayerPushedSHA(featureID, repoName, layerPosition, pushedSHA)

	body, bodyErr := o.deps.Remote.GetPRBody(target.entry.PRURL)
	if bodyErr != nil {
		// Only when the closed pull request's body cannot be read does the
		// recreation run the Phase 7 body-only description session.
		prCtx := o.buildLayerPRContext(target.f, target.repo, target.layer, o.recreateLowerCut(target), target.entry.TipSHA)
		generated, generateErr := o.generatePRDescription(target.f, prCtx)
		if generateErr != nil {
			descriptionErr := &PublishDescriptionError{
				RepoName:      repoName,
				LayerPosition: target.layer.Position,
				LayerTitle:    target.layer.Title,
				Err:           generateErr,
			}
			o.storePublishFailure(target.f, repoName, descriptionErr)
			return descriptionErr
		}
		body = generated
	} else {
		body = git.StripHarnessSections(body)
	}

	layers := orderedStackLayers(target.f)
	entries := make(map[int]feature.StackRepoEntry, len(layers))
	layerIndex := -1
	for i, layer := range layers {
		entries[layer.Position] = layer.Repos[repoName]
		if layer.Position == layerPosition {
			layerIndex = i
		}
	}
	entry := target.entry
	entry.LastPushedSHA = pushedSHA
	entries[layerPosition] = entry
	baseBranch := stackLayerBaseBranch(layers, entries, layerIndex, target.repo.BaseBranch)

	prURL, createErr := o.deps.Remote.CreatePR(target.repoPath, target.layer.Branch, target.layer.Title, body, baseBranch, target.f.Checkpoints.DraftPublish)
	if createErr != nil {
		recreateErr := &PublishRecreateFailedError{
			RepoName:      repoName,
			LayerPosition: target.layer.Position,
			LayerTitle:    target.layer.Title,
			PRURL:         target.entry.PRURL,
			Err:           createErr,
		}
		o.storePublishFailure(target.f, repoName, recreateErr)
		return recreateErr
	}

	entry.PRURL = prURL
	entry.PRState = feature.StackPRStateOpen
	_ = o.deps.Lifecycle.RecordStackLayerPR(featureID, repoName, layerPosition, prURL, pushedSHA)
	_ = o.deps.Lifecycle.SetRepoPublished(featureID, repoName)
	o.applyLayerCrossRefs(target.f, target.layer, repoName, prURL)
	o.reinjectStackSections(featureID, repoName)
	o.emitPullRequestResolutionEvents(target, entry, observe.LayerPublishActionRecreated, "recreate pull request updated repository layer entry")
	return nil
}

// recreateLowerCut resolves the cut point a recreated layer's description
// session is measured against: the nearest lower layer's recorded tip whose
// entry was not dropped by a rebase pass, else the repository's base cut.
func (o *Orchestrator) recreateLowerCut(target pullRequestResolutionTarget) string {
	layers := orderedStackLayers(target.f)
	for i := len(layers) - 1; i >= 0; i-- {
		if layers[i].Position >= target.layer.Position {
			continue
		}
		entry := layers[i].Repos[target.repo.Name]
		if entry.PRState == feature.StackPRStateMerged && entry.TipSHA == "" {
			continue
		}
		if entry.TipSHA != "" {
			return entry.TipSHA
		}
	}
	if baseSHA, err := resolveBaseCutSHA(target.workDir, target.repo.BaseBranch); err == nil {
		return baseSHA
	}
	return ""
}
