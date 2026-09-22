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
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
)

// readFileSafe reads a file and returns its contents trimmed of leading/trailing
// whitespace. Errors are surfaced to the caller.
func readFileSafe(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// publishRepoWithOptions walks one repository's stack layers bottom-up:
// layers without commits in the repository are marked empty, layers with a
// recorded pull request get their live state refreshed and new work pushed,
// and layers without a pull request get a generated description, a lease
// push, and a PR based on the nearest lower layer's non-closed PR. A failure
// at any layer stores the canonical record — naming the layer — on the
// repository and stops that repository; other repositories continue. The
// durable record of every created pull request lives on the stack's
// per-layer entries.
func (o *Orchestrator) publishRepoWithOptions(featureID, repoName string, opts PublishOptions) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature: %w", err)
	}

	repo, ok := findRepo(f, repoName)
	if !ok {
		return fmt.Errorf("repo %q not found in feature %s", repoName, featureID)
	}

	workDir := repoWorkDir(repo)
	branch := repo.Branch

	// Fail closed: a run approved before the `## Pull Requests` table has
	// no layer composition to deliver, and inventing a single-PR shape
	// would silently bypass the stack contract.
	if len(f.Stack) == 0 {
		missingErr := &PublishStackMissingError{RepoName: repoName, Branch: branch}
		o.storePublishFailure(f, repoName, missingErr)
		return missingErr
	}
	if branch == "" {
		// No fabricated name: a repository without a recorded branch cannot
		// be published, and the error names the repository so the user can
		// fix the record.
		err := fmt.Errorf("repo %q has no feature branch recorded", repoName)
		o.storePublishFailure(f, repoName, err)
		return err
	}

	// Commit any uncommitted changes onto the checked-out (top) layer.
	if git.HasUncommittedChanges(workDir) {
		if commitErr := git.CommitAll(workDir, f.Name); commitErr != nil {
			err := fmt.Errorf("commit failed: %w", commitErr)
			o.storePublishFailure(f, repoName, err)
			return err
		}
	}

	// Refresh the top layer's recorded tip from the checked-out branch: the
	// boundary snapshot is deliberately stale between boundaries (Final
	// Review fix rounds keep committing on the ref), so publish re-reads it
	// before walking. The snapshot handed to the walk must be taken after
	// this write: a layer whose Repos map was absent gets a fresh map here,
	// and a stale copy would keep the nil map and lose the tip.
	headSHA, headErr := git.CurrentHeadSHA(workDir)
	if headErr != nil {
		tipErr := fmt.Errorf("resolving %s's checked-out tip for publish: %w", repoName, headErr)
		o.storePublishFailure(f, repoName, tipErr)
		return tipErr
	}

	// The checked-out branch owns the refreshed tip. The highest-position
	// layer is the checked-out one in every pre-rebase shape, but a rebase
	// pass that dropped the top layer leaves the worktree on the highest
	// kept layer's branch while the dropped layer's merged entry keeps its
	// cleared tip — writing the new top's SHA there would resurrect the
	// dropped layer as a walk base. A layer dropped in this repository is
	// therefore skipped; every repository's layers merged away leaves the
	// position at zero and the write a no-op.
	topPosition := 0
	for _, layer := range f.Stack {
		entry := layer.Repos[repoName]
		if entry.PRState == feature.StackPRStateMerged && entry.TipSHA == "" {
			continue
		}
		if layer.Position > topPosition {
			topPosition = layer.Position
		}
	}
	applyStackRepoTip(f, topPosition, repoName, headSHA)
	if o.deps.Store != nil {
		if modErr := o.deps.Store.Modify(featureID, func(ff *feature.Feature) error {
			applyStackRepoTip(ff, topPosition, repoName, headSHA)
			return nil
		}); modErr != nil {
			return fmt.Errorf("record top layer tip: %w", modErr)
		}
	}

	layers := orderedStackLayers(f)

	baseSHA, baseErr := resolveBaseCutSHA(workDir, repo.BaseBranch)
	if baseErr != nil {
		o.storePublishFailure(f, repoName, baseErr)
		return baseErr
	}

	walkErr := o.walkStackLayers(f, repo, layers, baseSHA, opts.UpdateOnly)
	// The stack-section refresh runs whether or not the walk stopped early:
	// pull requests created earlier in the pass still deserve current links.
	o.reinjectStackSections(featureID, repoName)
	return walkErr
}

// walkStackLayers delivers one repository's stack, ascending by position.
// entries is the walk's local view of the repository's per-layer entries; it
// is updated as the walk records outcomes so later layers decide (base
// branch, stack section) against what this pass just did. updateOnly
// restricts the walk to layers that already carry a pull request: layers
// without one are left untouched for a later full publish.
func (o *Orchestrator) walkStackLayers(f *feature.Feature, repo feature.FeatureRepo, layers []feature.StackLayer, baseSHA string, updateOnly bool) error {
	workDir := repoWorkDir(repo)
	repoName := repo.Name
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = workDir
	}

	entries := make(map[int]feature.StackRepoEntry, len(layers))
	for _, layer := range layers {
		entries[layer.Position] = layer.Repos[repoName]
	}
	// A merged entry whose tip was cleared is transparent to the walk: its
	// local ref is gone, so it owns neither commits, nor a lower cut point,
	// nor a base branch. A merged entry that still holds a tip keeps the
	// Phase 7 behavior — its branch is still a valid base and cut point,
	// because the rebase pass has not dropped it yet.
	absentForWalk := func(entry feature.StackRepoEntry) bool {
		return entry.PRState == feature.StackPRStateMerged && entry.TipSHA == ""
	}
	lowerCut := func(i int) string {
		for j := i - 1; j >= 0; j-- {
			entry := entries[layers[j].Position]
			if absentForWalk(entry) {
				continue
			}
			return entry.TipSHA
		}
		return baseSHA
	}

	// Precompute which layers carry commits in this repository: the
	// creation-time stack section must predict pull requests that do not
	// exist yet, and the walk reuses the same decision.
	hasCommits := make(map[int]bool, len(layers))
	predictedPRs := 0
	for i, layer := range layers {
		if absentForWalk(entries[layer.Position]) {
			continue
		}
		if git.HasCommitsBeyond(workDir, entries[layer.Position].TipSHA, lowerCut(i)) {
			hasCommits[layer.Position] = true
			predictedPRs++
		}
	}

	for i, layer := range layers {
		entry := entries[layer.Position]
		if absentForWalk(entry) {
			// Dropped by a merged rebase pass: nothing to deliver and no
			// branch to publish; the entry already records the merged pull
			// request, so the all-published check counts it settled.
			continue
		}
		if !hasCommits[layer.Position] {
			// Nothing to deliver for this layer here: mark the entry so the
			// all-published check counts it as settled. The write is durable
			// state, not an advisory notification — a failed save stops the
			// repository's walk instead of leaving the layer pending on disk.
			if err := o.deps.Lifecycle.MarkStackLayerNoCommits(f.ID, repoName, layer.Position); err != nil {
				stateErr := stackStateWriteError(repoName, layer, "mark the layer without commits", "", err)
				o.storePublishFailure(f, repoName, stateErr)
				return stateErr
			}
			entry.NoCommits = true
			entries[layer.Position] = entry
			continue
		}
		if layer.Branch == "" {
			err := &PublishPushError{
				RepoName:      repoName,
				LayerPosition: layer.Position,
				LayerTitle:    layer.Title,
				Err:           errors.New("stack layer has no branch recorded"),
			}
			o.storePublishFailure(f, repoName, err)
			return err
		}

		if entry.PRURL != "" {
			stopErr := o.refreshExistingLayerPR(f, repoName, repoPath, layer, &entry)
			entries[layer.Position] = entry
			if stopErr != nil {
				// A closed pull request blocks the layer; every other refusal
				// from the refresh — a failed durable state write — failed the
				// publish step instead.
				action := observe.LayerPublishActionFailed
				var closed *PublishStackClosedError
				if errors.As(stopErr, &closed) {
					action = observe.LayerPublishActionBlocked
				}
				o.emitLayerPublishEvent(f, repoName, layer, entry, action)
				return stopErr
			}
			if entry.PRState == feature.StackPRStateMerged {
				// The layer's work has landed; the branch is left alone.
				o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionMerged)
				continue
			}
			if entry.TipSHA != entry.LastPushedSHA {
				action := observe.LayerPublishActionPushed
				if layerPushRewrites(workDir, layer.Branch, entry) {
					action = observe.LayerPublishActionRewritten
				}
				pushedSHA, pushErr := o.pushStackLayer(repoName, layer, entry, workDir)
				if pushErr != nil {
					o.storePublishFailure(f, repoName, pushErr)
					o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
					return pushErr
				}
				if err := o.deps.Lifecycle.RecordStackLayerPushedSHA(f.ID, repoName, layer.Position, pushedSHA); err != nil {
					stateErr := stackStateWriteError(repoName, layer, "record the pushed SHA", entry.PRURL, err)
					o.storePublishFailure(f, repoName, stateErr)
					o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
					return stateErr
				}
				entry.LastPushedSHA = pushedSHA
				entries[layer.Position] = entry
				o.emitLayerPublishEvent(f, repoName, layer, entry, action)
			}
			continue
		}

		// No pull request yet. An update-only walk (a manual-publish
		// rebase tail republishing existing pull requests) leaves the
		// layer for a later full publish: creating pull requests for
		// never-published layers still requires auto-publish or the
		// user's Publish action.
		if updateOnly {
			continue
		}

		// The description is generated first so a failed session pushes nothing.
		prCtx := o.buildLayerPRContext(f, repo, layer, lowerCut(i), entry.TipSHA)
		body, generateErr := o.generatePRDescription(f, prCtx)
		if generateErr != nil {
			err := &PublishDescriptionError{
				RepoName:      repoName,
				LayerPosition: layer.Position,
				LayerTitle:    layer.Title,
				Err:           generateErr,
			}
			o.storePublishFailure(f, repoName, err)
			o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
			return err
		}

		pushedSHA := entry.LastPushedSHA
		if entry.TipSHA != entry.LastPushedSHA {
			var pushErr error
			pushedSHA, pushErr = o.pushStackLayer(repoName, layer, entry, workDir)
			if pushErr != nil {
				o.storePublishFailure(f, repoName, pushErr)
				o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
				return pushErr
			}
			if err := o.deps.Lifecycle.RecordStackLayerPushedSHA(f.ID, repoName, layer.Position, pushedSHA); err != nil {
				stateErr := stackStateWriteError(repoName, layer, "record the pushed SHA", "", err)
				o.storePublishFailure(f, repoName, stateErr)
				o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
				return stateErr
			}
			entry.LastPushedSHA = pushedSHA
		}

		if section := creationStackSection(layers, entries, i, predictedPRs); section != "" {
			body = git.InjectStackSection(body, section)
		}
		baseBranch := stackLayerBaseBranch(layers, entries, i, repo.BaseBranch)
		prURL, createErr := o.deps.Remote.CreatePR(repoPath, layer.Branch, layer.Title, body, baseBranch, f.Checkpoints.DraftPublish)
		if createErr != nil {
			err := &PublishPRCreateError{
				RepoName:      repoName,
				LayerPosition: layer.Position,
				LayerTitle:    layer.Title,
				Err:           createErr,
			}
			o.storePublishFailure(f, repoName, err)
			o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
			return err
		}
		// The durable records land before the layer reports success: the
		// pull request exists on the remote, so a repository whose stack
		// entry or published mark cannot be saved stops here with the
		// already-created URL named instead of continuing to higher layers.
		entry.PRURL = prURL
		entry.PRState = feature.StackPRStateOpen
		entries[layer.Position] = entry
		if err := o.deps.Lifecycle.RecordStackLayerPR(f.ID, repoName, layer.Position, prURL, pushedSHA); err != nil {
			stateErr := stackStateWriteError(repoName, layer, "record the created pull request", prURL, err)
			o.storePublishFailure(f, repoName, stateErr)
			o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
			return stateErr
		}
		if err := o.deps.Lifecycle.SetRepoPublished(f.ID, repoName); err != nil {
			stateErr := stackStateWriteError(repoName, layer, "mark the repository published", prURL, err)
			o.storePublishFailure(f, repoName, stateErr)
			o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionFailed)
			return stateErr
		}
		o.applyLayerCrossRefs(f, layer, repoName, prURL)
		o.emitLayerPublishEvent(f, repoName, layer, entry, observe.LayerPublishActionCreated)
	}
	return nil
}

// emitLayerPublishEvent reports one layer publish outcome through the
// OnStackLayerPublished hook, following the layer-boundary event pattern.
// The hook is nil-safe and the observer no-ops when disabled.
func (o *Orchestrator) emitLayerPublishEvent(f *feature.Feature, repoName string, layer feature.StackLayer, entry feature.StackRepoEntry, action observe.LayerPublishAction) {
	if o == nil || o.hooks.OnStackLayerPublished == nil {
		return
	}
	state := entry.PRState
	if entry.PRURL == "" {
		state = feature.StackPRStateNone
	} else if state == "" {
		state = feature.StackPRStateOpen
	}
	o.hooks.OnStackLayerPublished(f.ID, observe.LayerPublishEvent{
		Repository: repoName,
		Position:   layer.Position,
		Title:      layer.Title,
		Branch:     layer.Branch,
		PRURL:      entry.PRURL,
		State:      string(state),
		Action:     action,
	})
}

// layerPushRewrites reports whether pushing entry's tip onto the layer's
// remote branch would rewrite remote history: the remote branch resolves
// and is not an ancestor of the tip.
func layerPushRewrites(workDir, branch string, entry feature.StackRepoEntry) bool {
	remoteSHA, err := git.ReadRefSHA(workDir, "refs/remotes/origin/"+branch)
	if err != nil || remoteSHA == "" {
		return false
	}
	return !git.IsAncestor(workDir, remoteSHA, entry.TipSHA)
}

// refreshExistingLayerPR reads a layer's recorded pull request live state.
// Merged leaves the branch alone (the work has landed); closed without merge
// fails the repository with the stack-closed canonical record; open or
// indeterminate proceeds — a transient API failure must not block a
// legitimate push, matching the retired republish contract.
func (o *Orchestrator) refreshExistingLayerPR(f *feature.Feature, repoName, repoPath string, layer feature.StackLayer, entry *feature.StackRepoEntry) error {
	state, stateErr := o.deps.Remote.PRState(repoPath, entry.PRURL)
	switch {
	case stateErr == nil && state == git.PRStateMerged:
		if err := o.deps.Lifecycle.SetStackLayerPRState(f.ID, repoName, layer.Position, feature.StackPRStateMerged); err != nil {
			stateWriteErr := stackStateWriteError(repoName, layer, "record the merged pull-request state", entry.PRURL, err)
			o.storePublishFailure(f, repoName, stateWriteErr)
			return stateWriteErr
		}
		entry.PRState = feature.StackPRStateMerged
	case stateErr == nil && state == git.PRStateClosed:
		closedErr := &PublishStackClosedError{
			RepoName:      repoName,
			Branch:        layer.Branch,
			LayerPosition: layer.Position,
			LayerTitle:    layer.Title,
			PRURL:         entry.PRURL,
			State:         state,
		}
		o.storePublishFailure(f, repoName, closedErr)
		return closedErr
	case stateErr == nil && state == git.PRStateOpen:
		if err := o.deps.Lifecycle.SetStackLayerPRState(f.ID, repoName, layer.Position, feature.StackPRStateOpen); err != nil {
			stateWriteErr := stackStateWriteError(repoName, layer, "record the open pull-request state", entry.PRURL, err)
			o.storePublishFailure(f, repoName, stateWriteErr)
			return stateWriteErr
		}
		entry.PRState = feature.StackPRStateOpen
	}
	// An indeterminate answer (lookup error or unrecognised state) is
	// treated as open: the entry keeps its recorded state.
	return nil
}

// pushStackLayer delivers one layer's branch through the guarded push,
// mapping the refusal kinds onto the layer-aware publish errors so the
// stored record names the layer.
func (o *Orchestrator) pushStackLayer(repoName string, layer feature.StackLayer, entry feature.StackRepoEntry, workDir string) (string, error) {
	pushedSHA, err := o.deps.Remote.PushLayerBranch(workDir, layer.Branch, entry.TipSHA, entry.LastPushedSHA)
	if err == nil {
		return pushedSHA, nil
	}
	var pushErr *git.RewritePushError
	if errors.As(err, &pushErr) {
		switch pushErr.Kind {
		case git.RewritePushRemoteDiverged:
			return "", &PublishRemoteDivergedError{
				RepoName:          repoName,
				Branch:            pushErr.Branch,
				RemoteOnlyCommits: pushErr.RemoteOnlyCommits,
				LayerPosition:     layer.Position,
				LayerTitle:        layer.Title,
			}
		case git.RewritePushRemoteChanged:
			return "", &PublishRemoteChangedError{
				RepoName:      repoName,
				Branch:        pushErr.Branch,
				LayerPosition: layer.Position,
				LayerTitle:    layer.Title,
			}
		}
	}
	return "", &PublishPushError{
		RepoName:      repoName,
		Branch:        layer.Branch,
		LayerPosition: layer.Position,
		LayerTitle:    layer.Title,
		Err:           err,
	}
}

// generatePRDescription produces one stack layer's PR body from a structured
// PRContext using the description-generation agent. Generation errors are
// returned so publishing cannot proceed with synthetic fallback content; the
// publish boundary classifies and stores them on the repository state.
func (o *Orchestrator) generatePRDescription(f *feature.Feature, prCtx agent.PRContext) (string, error) {
	if o.deps.PhaseRunner == nil {
		return "", errors.New("description generation agent is unavailable")
	}
	model := f.Models.Planning
	if model == "" {
		model = "sonnet"
	}
	return o.deps.PhaseRunner.RunDescriptionGeneration(
		context.Background(),
		f.ID,
		model,
		prCtx,
	)
}

// buildLayerPRContext assembles the per-layer PRContext: the feature
// metadata, the layer's roadmap row (position, title, phases, rationale
// re-read from the roadmap on disk best-effort), the whole stack view, and
// the layer's own commit bodies and diff stat between the lower cut point
// and the layer tip. A roadmap-derived layer is described from the run's
// own feature and roadmap; a layer with an origin (an appended layer) is
// described from the origin feature instead — its name, description, and
// the roadmap row at the origin position — while the position and title
// stay the parent's persisted layer. Individual fetch failures degrade
// gracefully — empty fields are acceptable inputs to the prompt and
// generator.
func (o *Orchestrator) buildLayerPRContext(f *feature.Feature, repo feature.FeatureRepo, layer feature.StackLayer, lowerCutSHA, tipSHA string) agent.PRContext {
	prCtx := agent.PRContext{
		LayerPosition: layer.Position,
		LayerTitle:    layer.Title,
	}
	if layer.Origin != nil {
		origin := o.loadOriginLayerContext(*layer.Origin)
		prCtx.FeatureName = origin.name
		prCtx.FeatureDescription = origin.description
		prCtx.LayerPhases = origin.phases
		prCtx.LayerRationale = origin.rationale
	} else {
		prCtx.FeatureName = f.Name
		prCtx.FeatureDescription = f.Description
		prCtx.LayerPhases = layer.Phases
		prCtx.LayerRationale = o.stackLayerRationale(f, layer.Position)
	}
	for _, l := range orderedStackLayers(f) {
		prCtx.Stack = append(prCtx.Stack, prompts.PRStackLayerView{
			Position: l.Position,
			Title:    l.Title,
			Phases:   l.Phases,
			Branch:   l.Branch,
		})
	}
	workDir := repoWorkDir(repo)
	if workDir != "" && lowerCutSHA != "" && tipSHA != "" {
		if bodies, err := git.CommitBodiesRange(workDir, lowerCutSHA, tipSHA); err == nil {
			prCtx.CommitBodies = bodies
		}
		if stat, err := git.DiffStatRange(workDir, lowerCutSHA, tipSHA); err == nil {
			prCtx.DiffStat = stat
		}
	}
	return prCtx
}

// originLayerContext is the origin-aware feature context for an appended
// layer's PR body: the origin (child) feature's name and description plus
// the phases and rationale of the origin roadmap's row at the origin
// position.
type originLayerContext struct {
	name        string
	description string
	phases      []int
	rationale   string
}

// loadOriginLayerContext resolves an appended layer's feature context from
// its origin, best-effort — a re-read exactly like the layer rationale
// re-read, never a publish failure. A missing origin feature or an
// unreadable origin roadmap leaves every field empty; a readable roadmap
// whose table carries no row at the origin position still names the layer
// from the loaded origin feature while the phases and rationale stay empty.
func (o *Orchestrator) loadOriginLayerContext(origin feature.StackLayerOrigin) originLayerContext {
	if origin.SourceFeatureID == "" || o.deps.Lifecycle == nil {
		return originLayerContext{}
	}
	originF, err := o.deps.Lifecycle.Get(origin.SourceFeatureID)
	if err != nil || originF == nil {
		return originLayerContext{}
	}
	row, readable := o.stackLayerRoadmapRow(originF, origin.SourceLayerPosition)
	if !readable {
		return originLayerContext{}
	}
	ctx := originLayerContext{name: originF.Name, description: originF.Description}
	if row != nil {
		ctx.phases = row.Phases
		ctx.rationale = row.Rationale
	}
	return ctx
}

// stackLayerRationale re-reads the layer's rationale row from the roadmap on
// disk, best-effort: the run's stack snapshot deliberately carries no
// rationale, and an unavailable or edited roadmap yields an empty string the
// description prompt omits.
func (o *Orchestrator) stackLayerRationale(f *feature.Feature, layerPosition int) string {
	row, _ := o.stackLayerRoadmapRow(f, layerPosition)
	if row == nil {
		return ""
	}
	return row.Rationale
}

// stackLayerRoadmapRow re-reads one row of the roadmap's `## Pull Requests`
// table from the feature's roadmap artifact on disk, best-effort. readable
// is false when the roadmap cannot be resolved or read; a readable roadmap
// whose table carries no row at layerPosition returns readable with a nil
// row.
func (o *Orchestrator) stackLayerRoadmapRow(f *feature.Feature, layerPosition int) (row *agent.RoadmapPullRequest, readable bool) {
	roadmapPath := o.resolveArtifactPath(f, "roadmap")
	if roadmapPath == "" {
		return nil, false
	}
	data, err := os.ReadFile(roadmapPath)
	if err != nil {
		return nil, false
	}
	rows, _ := agent.ParseRoadmapPullRequests(string(data))
	for i := range rows {
		if rows[i].Position == layerPosition {
			return &rows[i], true
		}
	}
	return nil, true
}

// applyLayerCrossRefs injects the per-layer cross-repo section into the
// just-created PR and its sibling PRs of the same layer. No-op for a
// single-repo feature; failures are advisory.
func (o *Orchestrator) applyLayerCrossRefs(f *feature.Feature, layer feature.StackLayer, repoName, prURL string) {
	if len(f.Repos) <= 1 {
		return
	}
	fresh := f
	if freshF, getErr := o.deps.Lifecycle.Get(f.ID); getErr == nil {
		fresh = freshF
	}
	var entries []git.CrossRefEntry
	type siblingPull struct {
		repoName string
		prURL    string
	}
	var siblings []siblingPull
	for _, repo := range fresh.Repos {
		entry := stackRepoEntryFor(fresh, layer.Position, repo.Name)
		crossRef := git.CrossRefEntry{RepoName: repo.Name, Branch: layer.Branch, PRURL: entry.PRURL}
		switch {
		case repo.Name == repoName:
			crossRef.PRURL = prURL
		case entry.PRURL != "":
			siblings = append(siblings, siblingPull{repoName: repo.Name, prURL: entry.PRURL})
		}
		entries = append(entries, crossRef)
	}
	// The new pull request links every sibling that already has one.
	if section := git.BuildLayerCrossReferenceSection(f.Name, entries, repoName); section != "" {
		if body, getErr := git.GetPRBody(prURL); getErr == nil {
			_ = git.UpdatePRBody(prURL, git.InjectCrossReferenceSection(body, section))
		}
	}
	// Each sibling's body links the layer's pull requests in the other
	// repositories — including this one — so its section is rendered with
	// that sibling as the current repository rather than reused from ours.
	for _, sibling := range siblings {
		section := git.BuildLayerCrossReferenceSection(f.Name, entries, sibling.repoName)
		if section == "" {
			continue
		}
		_ = git.UpdatePRBodiesWithSection([]string{sibling.prURL}, section)
	}
}

// reinjectStackSections re-renders the stack section for one repository's
// pass — current links and states, the marker on the repository's highest
// layer with a pull request — and injects it into every open pull request of
// that repository. Best-effort: body read/update failures are advisory.
func (o *Orchestrator) reinjectStackSections(featureID, repoName string) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil || f == nil || len(f.Stack) == 0 {
		return
	}
	layers := orderedStackLayers(f)
	sectionLayers := make([]git.StackSectionLayer, 0, len(layers))
	currentPos := 0
	var openURLs []string
	for _, layer := range layers {
		entry := layer.Repos[repoName]
		sectionLayers = append(sectionLayers, git.StackSectionLayer{
			Position: layer.Position,
			Title:    layer.Title,
			PRURL:    entry.PRURL,
			PRState:  string(entry.PRState),
		})
		if entry.PRURL != "" && layer.Position > currentPos {
			currentPos = layer.Position
		}
		if entry.PRURL != "" && entry.PRState != feature.StackPRStateMerged && entry.PRState != feature.StackPRStateClosed {
			openURLs = append(openURLs, entry.PRURL)
		}
	}
	if currentPos == 0 {
		return
	}
	for i := range sectionLayers {
		if sectionLayers[i].Position == currentPos {
			sectionLayers[i].Current = true
		}
	}
	_ = git.UpdatePRBodiesWithStackSection(openURLs, git.BuildStackSection(sectionLayers))
}

// creationStackSection renders the stack section injected into a layer's PR
// body before CreatePR: known lower pull requests link with their states,
// the current layer is marked, and higher layers render a line without a
// link. It returns empty when the repository will carry at most one pull
// request — a single-layer delivery has no stack context worth showing.
func creationStackSection(layers []feature.StackLayer, entries map[int]feature.StackRepoEntry, currentIndex, predictedPRs int) string {
	if predictedPRs <= 1 {
		return ""
	}
	sectionLayers := make([]git.StackSectionLayer, len(layers))
	for j, layer := range layers {
		entry := entries[layer.Position]
		view := git.StackSectionLayer{Position: layer.Position, Title: layer.Title}
		if j < currentIndex && entry.PRURL != "" {
			view.PRURL = entry.PRURL
			view.PRState = string(entry.PRState)
		}
		if j == currentIndex {
			view.Current = true
		}
		sectionLayers[j] = view
	}
	return git.BuildStackSection(sectionLayers)
}

// stackLayerBaseBranch resolves the base branch for a new layer PR: the
// branch of the nearest lower layer whose pull request in this repository is
// in a non-closed state (open or merged — a merged lower layer's branch is
// still the review base) and whose tip has not been cleared by a rebase
// pass (a dropped layer's branch no longer exists), else the repository's
// base branch.
func stackLayerBaseBranch(layers []feature.StackLayer, entries map[int]feature.StackRepoEntry, currentIndex int, repoBaseBranch string) string {
	for j := currentIndex - 1; j >= 0; j-- {
		entry := entries[layers[j].Position]
		if entry.PRURL == "" || entry.PRState == feature.StackPRStateClosed {
			continue
		}
		if entry.PRState == feature.StackPRStateMerged && entry.TipSHA == "" {
			continue
		}
		return layers[j].Branch
	}
	return repoBaseBranch
}

// orderedStackLayers returns the run's stack layers sorted ascending by
// position, copied so callers can mutate the slice freely.
func orderedStackLayers(f *feature.Feature) []feature.StackLayer {
	layers := append([]feature.StackLayer(nil), f.Stack...)
	sort.SliceStable(layers, func(i, j int) bool {
		return layers[i].Position < layers[j].Position
	})
	return layers
}

// applyStackRepoTip writes one repository's tip onto the layer at
// layerPosition, preserving the push and pull-request fields an earlier
// boundary or publish pass recorded.
func applyStackRepoTip(f *feature.Feature, layerPosition int, repoName, sha string) {
	for i := range f.Stack {
		if f.Stack[i].Position != layerPosition {
			continue
		}
		if f.Stack[i].Repos == nil {
			f.Stack[i].Repos = make(map[string]feature.StackRepoEntry)
		}
		entry := f.Stack[i].Repos[repoName]
		entry.TipSHA = sha
		f.Stack[i].Repos[repoName] = entry
	}
}

// stackRepoEntryFor reads one repository's entry on the layer at
// layerPosition from the feature's stack.
func stackRepoEntryFor(f *feature.Feature, layerPosition int, repoName string) feature.StackRepoEntry {
	for _, layer := range f.Stack {
		if layer.Position == layerPosition {
			return layer.Repos[repoName]
		}
	}
	return feature.StackRepoEntry{}
}

// resolveBaseCutSHA resolves the cut point a layer-1 delivery is measured
// against: the remote-tracking base branch when it exists (publish describes
// what a PR against the remote base would contain), else the local base
// branch. An unresolvable base fails closed instead of letting every layer
// read as empty.
func resolveBaseCutSHA(workDir, baseBranch string) (string, error) {
	base := strings.TrimSpace(baseBranch)
	if base == "" {
		base = git.DefaultBranch(workDir)
	}
	if base == "" {
		return "", fmt.Errorf("repo at %s has no base branch recorded and no default branch detected", workDir)
	}
	if sha, err := git.ReadRefSHA(workDir, "refs/remotes/origin/"+base); err == nil {
		return sha, nil
	}
	if sha, err := git.ReadRefSHA(workDir, "refs/heads/"+base); err == nil {
		return sha, nil
	}
	return "", fmt.Errorf("base branch %q does not resolve in %s", base, workDir)
}

// stackRepoEntriesSnapshot captures one repository's per-layer entries so a
// publish pass can tell whether the walk changed them.
func stackRepoEntriesSnapshot(f *feature.Feature, repoName string) map[int]feature.StackRepoEntry {
	out := make(map[int]feature.StackRepoEntry, len(f.Stack))
	for _, layer := range f.Stack {
		out[layer.Position] = layer.Repos[repoName]
	}
	return out
}

// stackRepoEntriesChanged reports whether two snapshots of a repository's
// per-layer entries differ. StackRepoEntry is comparable, so a plain value
// comparison is exact.
func stackRepoEntriesChanged(before, after map[int]feature.StackRepoEntry) bool {
	if len(before) != len(after) {
		return true
	}
	for pos, entry := range before {
		other, ok := after[pos]
		if !ok || entry != other {
			return true
		}
	}
	return false
}
