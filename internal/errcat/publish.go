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

package errcat

import (
	"fmt"
	"strings"
)

// Publish failure codes. Every condition that fails a repository publish
// classifies into exactly one of these at the publish boundary; all are
// needs_action preconditions that reference the publish action and declare
// only the repositories block. A publish failure never marks a run Failed:
// the repository's stored record is the sole owner.
const (
	PublishRebaseConflict    Code = "publish_rebase_conflict"
	PublishPullRequestClosed Code = "publish_pull_request_closed"
	PublishPullRequestFailed Code = "publish_pull_request_failed"
	PublishDescriptionFailed Code = "publish_description_failed"
	PublishPushFailed        Code = "publish_push_failed"
)

// publishFailureCodes is the closed set of codes a repository publish
// failure can carry. Every predicate that needs to know whether a failure
// is a publish failure keys on IsPublishFailure instead of comparing codes
// directly.
var publishFailureCodes = map[Code]bool{
	PublishRebaseConflict:    true,
	PublishRemoteDiverged:    true,
	PublishRemoteChanged:     true,
	PublishPullRequestClosed: true,
	PublishPullRequestFailed: true,
	PublishDescriptionFailed: true,
	PublishPushFailed:        true,
}

// IsPublishFailure reports whether code is one of the publish failure codes
// a repository state's stored record can carry.
func IsPublishFailure(code Code) bool {
	return publishFailureCodes[code]
}

// PublishRepoParams carries the repository name, branch, rebase target, and
// remote-only commit count a publish-failure summary interpolates.
// RenderRecord derives them from a stored record's repositories block; the
// static summary applies when the record carries no repositories.
type PublishRepoParams struct {
	Repo              string
	Branch            string
	RebaseTarget      string
	RemoteOnlyCommits int
}

func (PublishRepoParams) params() {}

// publishRepoParams extracts the publish summary params from p, reporting
// whether the template may render.
func publishRepoParams(p Params) (PublishRepoParams, bool) {
	params, ok := p.(PublishRepoParams)
	if !ok || strings.TrimSpace(params.Repo) == "" {
		return PublishRepoParams{}, false
	}
	return params, true
}

// publishNamedRepo renders the repository-keyed sentence via format when the
// params name a repository, or "" for the static fallback.
func publishNamedRepo(p Params, format string) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return fmt.Sprintf(format, params.Repo)
}

// publishRebaseConflictSummary names the repository, branch, and rebase
// target of the conflicted pull rebase.
func publishRebaseConflictSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	if params.Branch != "" && params.RebaseTarget != "" {
		return fmt.Sprintf(
			"The pull rebase for repository %q (branch %q) onto %q conflicted.",
			params.Repo, params.Branch, params.RebaseTarget,
		)
	}
	return fmt.Sprintf("The pull rebase for repository %q conflicted with its target branch.", params.Repo)
}

// publishDivergedSummary names the repository and remote-only commit count
// of the diverged pull-request branch.
func publishDivergedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return fmt.Sprintf(
		"The pull-request branch for %q contains %d remote %s that are not in this workspace.",
		params.Repo, params.RemoteOnlyCommits, commitWord(params.RemoteOnlyCommits),
	)
}

// publishChangedSummary names the repository whose pull-request branch
// changed while it was being published.
func publishChangedSummary(p Params) string {
	return publishNamedRepo(p, "The pull-request branch for %q changed while Agentico was publishing.")
}

// publishPullRequestClosedSummary names the repository whose pull request is
// closed or merged and cannot receive new commits.
func publishPullRequestClosedSummary(p Params) string {
	return publishNamedRepo(p, "The pull request for repository %q is closed or merged and cannot receive new commits.")
}

// publishPullRequestFailedSummary names the repository whose pull-request
// creation failed.
func publishPullRequestFailedSummary(p Params) string {
	return publishNamedRepo(p, "Creating the pull request for repository %q failed.")
}

// publishDescriptionFailedSummary names the repository whose pull-request
// description generation failed.
func publishDescriptionFailedSummary(p Params) string {
	return publishNamedRepo(p, "Generating the pull-request description for repository %q failed.")
}

// publishPushFailedSummary names the repository (and branch when known) whose
// publish push failed.
func publishPushFailedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	if params.Branch != "" {
		return fmt.Sprintf("Publishing repository %q (branch %q) failed.", params.Repo, params.Branch)
	}
	return fmt.Sprintf("Publishing repository %q failed.", params.Repo)
}

// publishRepoSummaryParams derives the publish summary params from the first
// named repository of a stored record's repositories block. Publish records
// are per-repository, so the block carries exactly one repository; a block
// with no named repository yields the zero params and the static summary.
func publishRepoSummaryParams(repos []CodeRepository) PublishRepoParams {
	for _, repo := range repos {
		if name := strings.TrimSpace(repo.Name); name != "" {
			return PublishRepoParams{
				Repo:              name,
				Branch:            repo.Branch,
				RebaseTarget:      repo.RebaseTarget,
				RemoteOnlyCommits: repo.RemoteOnlyCommits,
			}
		}
	}
	return PublishRepoParams{}
}
