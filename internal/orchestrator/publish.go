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
	"os"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
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

// publishRepo publishes a single repo's branch as a PR. Mirrors
// app.go:5022-5154 (autoPublishRepoCmd) but runs synchronously and consumes
// port interfaces instead of the package-level git helpers.
//
// The caller is expected to have already verified IsPublishable + AutoPublish
// conditions (startPublish for manual flows). Per-repo failures call
// SetRepoPublishError on the lifecycle; on success SetRepoPublished is
// called and the PR URL is returned.
//
// Returns a *PublishConflictError on pull-rebase conflicts so callers can
// distinguish them with errors.Is(err, ErrPublishConflict) / errors.As.
func (o *Orchestrator) publishRepo(featureID, repoName string) (string, error) {
	if o.deps.Publisher == nil {
		return "", fmt.Errorf("publishRepo: Publisher port is nil")
	}

	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return "", fmt.Errorf("load feature: %w", err)
	}

	repo, ok := findRepo(f, repoName)
	if !ok {
		return "", fmt.Errorf("repo %q not found in feature %s", repoName, featureID)
	}

	workDir := repo.WorktreePath
	if workDir == "" {
		workDir = repo.Path
	}
	branch := repo.Branch
	if branch == "" {
		branch = "feature/" + f.Slug
	}

	// Commit any uncommitted changes.
	hasChanges, err := o.deps.Publisher.HasUncommittedChanges(workDir)
	if err == nil && hasChanges {
		if commitErr := o.deps.Publisher.CommitAll(workDir, f.Name); commitErr != nil {
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, commitErr.Error())
			return "", fmt.Errorf("commit failed: %w", commitErr)
		}
	}

	// Build a lean PR context (commit bodies + diff stat) and hand it to the
	// description-generation agent. The raw diff is intentionally excluded —
	// for large features it overflows the CLI prompt budget.
	prCtx := o.buildPRContext(f, workDir, repo.BaseBranch)
	title, body := o.generatePRDescription(f, prCtx)

	// Pull-rebase before push. A conflict surfaces as *PublishConflictError
	// so callers can route to conflict-resolution flows with errors.Is.
	if o.deps.Rebaser != nil {
		res := o.deps.Rebaser.PullRebase(workDir, branch)
		switch res.Outcome {
		case ports.PullRebaseConflict:
			errMsg := fmt.Sprintf("pull-rebase conflict in repo %s", repoName)
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, errMsg)
			return "", &PublishConflictError{
				RepoName:     repoName,
				Branch:       branch,
				RebaseTarget: o.resolveRebaseTarget(f, &repo),
			}
		case ports.PullRebaseFailure:
			reason := "pull-rebase failed"
			if res.Err != nil {
				reason = res.Err.Error()
			}
			_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, reason)
			return "", fmt.Errorf("pull-rebase failed: %s", reason)
		}
	}

	// Push branch.
	if err := o.deps.Publisher.Push(workDir, branch); err != nil {
		_ = o.deps.Lifecycle.SetRepoPublishError(featureID, repoName, err.Error())
		return "", fmt.Errorf("push failed: %w", err)
	}

	// Create PR.
	repoPath := repo.Path
	if repoPath == "" {
		repoPath = workDir
	}
	prURL, err := o.deps.Publisher.CreatePR(repoPath, branch, title, body, repo.BaseBranch)
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

// generatePRDescription produces a PR title/body from a structured PRContext
// using the description-generation agent. When the agent is unavailable the
// deterministic fallback is used. Generation errors are logged to the
// feature-scoped publish error log so auto-publish failures are diagnosable.
func (o *Orchestrator) generatePRDescription(f *feature.Feature, prCtx agent.PRContext) (string, string) {
	if o.deps.PhaseRunner == nil {
		return agent.BuildPRDescriptionFallback(prCtx)
	}
	model := f.Models.Planning
	if model == "" {
		model = "sonnet"
	}
	title, body, err := o.deps.PhaseRunner.RunDescriptionGeneration(
		context.Background(),
		model,
		prCtx,
	)
	if err != nil {
		agent.LogPhaseError(o.deps.PhaseRunner.StateDir, f, "publish", "description generation: "+err.Error())
	}
	return title, body
}

// buildPRContext assembles the lean PRContext from the feature metadata and
// git introspection (commit bodies + diff stat). Individual fetch failures
// degrade gracefully — empty fields are acceptable inputs to the prompt and
// fallback builders.
func (o *Orchestrator) buildPRContext(f *feature.Feature, workDir, baseBranch string) agent.PRContext {
	prCtx := agent.PRContext{
		FeatureName:        f.Name,
		FeatureDescription: f.Description,
		Roadmap:            o.readPhaseArtifact(f, "plan"),
	}
	if o.deps.Publisher != nil && workDir != "" {
		if bodies, err := o.deps.Publisher.CommitBodies(workDir, baseBranch); err == nil {
			prCtx.CommitBodies = bodies
		}
		if stat, err := o.deps.Publisher.DiffStat(workDir, baseBranch); err == nil {
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
// CrossRef port is nil or there is only one repo in the feature.
func (o *Orchestrator) applyCrossRefs(f *feature.Feature, justPublishedRepo, justPublishedURL string) {
	if o.deps.CrossRef == nil || len(f.Repos) <= 1 {
		return
	}
	entries := buildCrossRefEntries(f, justPublishedRepo, justPublishedURL)
	if len(entries) <= 1 {
		return
	}
	section := o.deps.CrossRef.BuildCrossReferenceSection(f.Name, entries)
	if section == "" {
		return
	}
	// Update the just-created PR.
	if currentBody, getErr := o.deps.CrossRef.GetPRBody(justPublishedURL); getErr == nil {
		updated := o.deps.CrossRef.InjectCrossReferenceSection(currentBody, section)
		_ = o.deps.CrossRef.UpdatePRBody(justPublishedURL, updated)
	}
	// Retroactively update earlier PRs. Errors are advisory.
	_ = o.deps.CrossRef.RetroactivelyUpdateCrossRefs(f.Name, entries, justPublishedRepo)
}

// buildCrossRefEntries builds per-repo CrossRefEntry values for the current
// feature state, substituting the just-published URL for its own repo.
// Mirrors app.go:5187-5207.
func buildCrossRefEntries(f *feature.Feature, justPublishedRepo, justPublishedURL string) []ports.CrossRefEntry {
	var entries []ports.CrossRefEntry
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
		entry := ports.CrossRefEntry{
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
