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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
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

// publishRepo publishes a single repo's branch as a PR. It runs synchronously,
// using concrete git helpers locally and RemoteOps for remote operations.
//
// The caller is expected to have already verified IsPublishable + AutoPublish
// conditions (startPublish for manual flows). Per-repo failures call
// SetRepoPublishError on the lifecycle; on success SetRepoPublished is
// called and the PR URL is returned.
//
// Returns a *PublishConflictError on pull-rebase conflicts so callers can
// distinguish them with errors.Is(err, ErrPublishConflict) / errors.As.
func (o *Orchestrator) publishRepo(featureID, repoName string) (string, error) {
	return o.publishRepoWithOptions(featureID, repoName, PublishOptions{})
}

func (o *Orchestrator) publishRepoWithOptions(featureID, repoName string, opts PublishOptions) (string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	repo, ok := findRepo(f, repoName)
	if !ok {
		return "", fmt.Errorf("repo %q not found in feature %s", repoName, featureID)
	}

	workDir := repoWorkDir(repo)
	branch := repoBranch(f, repo)

	// A repository that already has a pull request is re-pushed, never
	// re-described: CreatePR cannot update an existing PR's body, so
	// generating one would spend an agent call on discarded text.
	if state := f.RepoStates[repoName]; state != nil && state.PRURL != "" {
		return o.republishRepo(f, repo, state.PRURL)
	}

	// Commit any uncommitted changes.
	if git.HasUncommittedChanges(workDir) {
		if commitErr := git.CommitAll(workDir, f.Name); commitErr != nil {
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, commitErr.Error())
			return "", fmt.Errorf("commit failed: %w", commitErr)
		}
	}

	// Build a lean PR context (commit bodies + diff stat) and hand it to the
	// description-generation agent. The raw diff is intentionally excluded —
	// for large features it overflows the CLI prompt budget.
	prCtx := o.buildPRContext(f, workDir, repo.BaseBranch)
	title := strings.TrimSpace(opts.Title)
	body := strings.TrimSpace(opts.Body)
	if title == "" || body == "" {
		generatedTitle, generatedBody, generateErr := o.generatePRDescription(f, prCtx)
		if generateErr != nil {
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, generateErr.Error())
			return "", fmt.Errorf("generate PR description: %w", generateErr)
		}
		if title == "" {
			title = generatedTitle
		}
		if body == "" {
			body = generatedBody
		}
	}

	leasePush := publishRequiresLeasePush(f)

	// Pull-rebase before a regular push. CodeReady publish is the explicit
	// post-review path and may follow a rebase child pass that rewrote the feature
	// branch; rebasing that branch back onto origin/<branch> would undo the
	// intended direction of sync.
	if !leasePush {
		res := git.PullRebase(workDir, branch)
		switch res.Outcome {
		case git.PullRebaseConflict:
			errMsg := fmt.Sprintf("pull-rebase conflict in repo %s", repoName)
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, errMsg)
			return "", &PublishConflictError{
				RepoName:     repoName,
				Branch:       branch,
				RebaseTarget: o.resolveRebaseTarget(f, &repo),
			}
		case git.PullRebaseFailure:
			reason := "pull-rebase failed"
			if res.Err != nil {
				reason = res.Err.Error()
			}
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, reason)
			return "", fmt.Errorf("pull-rebase failed: %s", reason)
		}
	}

	// Push branch.
	if leasePush {
		if err := o.deps.Remote.ForcePush(workDir, branch); err != nil {
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, err.Error())
			return "", fmt.Errorf("force push failed: %w", err)
		}
	} else if err := o.deps.Remote.Push(workDir, branch); err != nil {
		_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, err.Error())
		return "", fmt.Errorf("push failed: %w", err)
	}

	// Create PR.
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = workDir
	}
	prURL, err := o.deps.Remote.CreatePR(repoPath, branch, title, body, repo.BaseBranch, f.Checkpoints.DraftPublish)
	if err != nil {
		_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, err.Error())
		return "", fmt.Errorf("PR creation failed: %w", err)
	}

	// Record per-repo success.
	if err := o.deps.Lifecycle.SetRepoPublished(featureID, repoName, prURL); err != nil {
		return prURL, fmt.Errorf("set repo published: %w", err)
	}

	// Apply cross-refs — best-effort; cross-ref failures do not fail the publish.
	if freshF, getErr := o.deps.Lifecycle.Get(featureID); getErr == nil {
		o.applyCrossRefs(freshF, repoName, prURL)
	}

	return prURL, nil
}

// republishRepo pushes new local work to a repository that already has a pull
// request and re-records the existing URL.
func (o *Orchestrator) republishRepo(f *feature.Feature, repo feature.FeatureRepo, prURL string) (string, error) {
	workDir := repoWorkDir(repo)
	branch := repoBranch(f, repo)
	if err := o.assertPRAcceptsUpdates(f, repo, prURL); err != nil {
		return "", err
	}
	if git.HasUncommittedChanges(workDir) {
		if err := git.CommitAll(workDir, f.Name); err != nil {
			_ = o.deps.Lifecycle.SetRepoPublishError(f.ID, repo.Name, err.Error())
			return "", fmt.Errorf("commit failed: %w", err)
		}
	}
	if err := o.pushRepublish(workDir, branch); err != nil {
		_ = o.deps.Lifecycle.SetRepoPublishError(f.ID, repo.Name, err.Error())
		return "", err
	}
	if err := o.deps.Lifecycle.SetRepoPublished(f.ID, repo.Name, prURL); err != nil {
		return prURL, fmt.Errorf("set repo published: %w", err)
	}
	return prURL, nil
}

// assertPRAcceptsUpdates refuses a republish whose pull request is no longer
// open. Pushing to a merged or closed PR's branch delivers commits nowhere
// reviewable — GitHub does not reopen a merged PR — while the feature would
// still be recorded as Published. This is the only network read in the
// republish path; CompletionPreflight cannot make it, so it cannot tell an
// unpublished-changes repository from a dead one.
//
// An indeterminate answer proceeds: a transient API or auth failure must not
// block a legitimate republish.
func (o *Orchestrator) assertPRAcceptsUpdates(f *feature.Feature, repo feature.FeatureRepo, prURL string) error {
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = repoWorkDir(repo)
	}
	state, err := o.deps.Remote.PRState(repoPath, prURL)
	if err != nil || state == "" || state == git.PRStateOpen {
		return nil
	}
	reason := fmt.Sprintf(
		"pull request %s is %s; new commits cannot be delivered to it", prURL, state)
	_ = o.deps.Lifecycle.SetRepoPublishError(f.ID, repo.Name, reason)
	return errors.New(reason)
}

// pushRepublish fast-forwards when the remote branch is an ancestor of HEAD.
// Otherwise the branch was rewritten locally and needs a lease push. We never
// fetch first: --force-with-lease derives its expectation from the
// remote-tracking ref, so refreshing that ref right before the push would
// make the lease compare the remote against itself and silently overwrite
// commits this clone never saw.
func (o *Orchestrator) pushRepublish(workDir, branch string) error {
	if git.IsAncestor(workDir, "origin/"+branch, "HEAD") {
		if err := o.deps.Remote.Push(workDir, branch); err != nil {
			return fmt.Errorf("push failed: %w", err)
		}
		return nil
	}
	if err := o.deps.Remote.ForcePush(workDir, branch); err != nil {
		return fmt.Errorf("force push failed: %w", err)
	}
	return nil
}

func publishRequiresLeasePush(f *feature.Feature) bool {
	return f != nil && f.Status == feature.StatusCodeReady && !f.Checkpoints.AutoPublish()
}

// generatePRDescription produces a PR title/body from a structured PRContext
// using the description-generation agent. Generation errors are logged to the
// feature-scoped publish error log and returned so publishing cannot proceed
// with synthetic fallback content.
func (o *Orchestrator) generatePRDescription(f *feature.Feature, prCtx agent.PRContext) (string, string, error) {
	if o.deps.PhaseRunner == nil {
		return "", "", errors.New("description generation agent is unavailable")
	}
	model := f.Models.Planning
	if model == "" {
		model = "sonnet"
	}
	title, body, err := o.deps.PhaseRunner.RunDescriptionGeneration(
		context.Background(),
		f.ID,
		model,
		prCtx,
	)
	if err != nil {
		agent.LogPhaseError(o.deps.PhaseRunner.StateDir, f, "publish", "description generation: "+err.Error())
		return "", "", err
	}
	return title, body, nil
}

type PublishDescriptionOptions struct {
	Repos []string
}

// GeneratePublishDescription derives a shared editable PR title/body from
// server-owned feature metadata plus bounded git summaries for the selected
// publish set. The renderer may name repository identities; it never supplies
// roadmap text, commit logs, diff stats, or fallback content.
func (o *Orchestrator) GeneratePublishDescription(featureID string, opts PublishDescriptionOptions) (string, string, error) {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", "", fmt.Errorf("load feature: %w", err)
	}
	requestedRepos, err := publishRepoSelection(f, opts.Repos)
	if err != nil {
		return "", "", err
	}
	hasSelection := len(requestedRepos) > 0
	prCtx := agent.PRContext{
		FeatureName:        f.Name,
		FeatureDescription: f.Description,
		Roadmap:            o.readPhaseArtifact(f, "plan"),
	}
	var commitSections []string
	var diffSections []string
	selectedCount := 0
	for _, repo := range f.Repos {
		if hasSelection {
			if !requestedRepos[repo.Name] {
				continue
			}
		} else {
			state := f.RepoStates[repo.Name]
			publishable := repoPublishable(repo)
			if !publishable || state == nil || !state.Touched || state.PRURL != "" {
				continue
			}
		}
		selectedCount++
		repoCtx := o.buildPRContext(f, repoWorkDir(repo), repo.BaseBranch)
		if strings.TrimSpace(repoCtx.CommitBodies) != "" {
			commitSections = append(commitSections, "## "+repo.Name+"\n"+strings.TrimSpace(repoCtx.CommitBodies))
		}
		if strings.TrimSpace(repoCtx.DiffStat) != "" {
			diffSections = append(diffSections, "## "+repo.Name+"\n"+strings.TrimSpace(repoCtx.DiffStat))
		}
	}
	if selectedCount == 0 {
		return "", "", errors.New("publish description: no eligible repositories")
	}
	prCtx.CommitBodies = strings.Join(commitSections, "\n\n")
	prCtx.DiffStat = strings.Join(diffSections, "\n\n")
	return o.generatePRDescription(f, prCtx)
}

// buildPRContext assembles the lean PRContext from the feature metadata and
// git introspection (commit bodies + diff stat). Individual fetch failures
// degrade gracefully — empty fields are acceptable inputs to the prompt and
// generator.
func (o *Orchestrator) buildPRContext(f *feature.Feature, workDir, baseBranch string) agent.PRContext {
	prCtx := agent.PRContext{
		FeatureName:        f.Name,
		FeatureDescription: f.Description,
		Roadmap:            o.readPhaseArtifact(f, "plan"),
	}
	if workDir != "" {
		if bodies, err := git.CommitBodies(workDir, baseBranch); err == nil {
			prCtx.CommitBodies = bodies
		}
		if stat, err := git.DiffStat(workDir, baseBranch); err == nil {
			prCtx.DiffStat = stat
		}
	}
	return prCtx
}

// readPhaseArtifact reads the textual content of a phase artifact, if
// recorded on f.Artifacts. Empty string on any resolution failure.
func (o *Orchestrator) readPhaseArtifact(f *feature.Feature, phase string) string {
	path := o.resolveArtifactPath(f, phase)
	if path == "" {
		return ""
	}
	data, err := readFileSafe(path)
	if err != nil {
		return ""
	}
	return data
}

// applyCrossRefs injects the multi-repo cross-reference section into the
// just-created PR body and retroactively updates earlier PRs. No-op when
// there is only one repo in the feature.
func (o *Orchestrator) applyCrossRefs(f *feature.Feature, justPublishedRepo, justPublishedURL string) {
	if len(f.Repos) <= 1 {
		return
	}
	entries := buildCrossRefEntries(f, justPublishedRepo, justPublishedURL)
	if len(entries) <= 1 {
		return
	}
	section := git.BuildCrossReferenceSection(f.Name, entries)
	if section == "" {
		return
	}
	// Update the just-created PR.
	if currentBody, getErr := git.GetPRBody(justPublishedURL); getErr == nil {
		updated := git.InjectCrossReferenceSection(currentBody, section)
		_ = git.UpdatePRBody(justPublishedURL, updated)
	}
	// Retroactively update earlier PRs. Errors are advisory.
	_ = git.RetroactivelyUpdateCrossRefs(f.Name, entries, justPublishedRepo)
}

// buildCrossRefEntries builds per-repo CrossRefEntry values for the current
// feature state, substituting the just-published URL for its own repo.
func buildCrossRefEntries(f *feature.Feature, justPublishedRepo, justPublishedURL string) []git.CrossRefEntry {
	var entries []git.CrossRefEntry
	for _, repo := range f.Repos {
		// Untouched repos contributed no work in any phase — omit them from
		// cross-reference sections so the just-published PR doesn't link to
		// branches that have nothing to merge.
		state, hasState := f.RepoStates[repo.Name]
		if hasState && state != nil && !state.Touched {
			if repo.Name != justPublishedRepo {
				continue
			}
		}
		entry := git.CrossRefEntry{
			RepoName: repo.Name,
			Branch:   repo.Branch,
		}
		if repo.Name == justPublishedRepo {
			entry.PRURL = justPublishedURL
		} else if hasState && state != nil {
			entry.PRURL = state.PRURL
			if state.LastError != "" {
				entry.PRURL = "(failed)"
			}
		}
		entries = append(entries, entry)
	}
	return entries
}
