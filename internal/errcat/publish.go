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
	PublishStackPullRequestClosed Code = "publish_stack_pull_request_closed"
	PublishStackMissing           Code = "publish_stack_missing"
	PublishPullRequestFailed      Code = "publish_pull_request_failed"
	PublishDescriptionFailed      Code = "publish_description_failed"
	PublishPushFailed             Code = "publish_push_failed"
)

// publishFailureCodes is the closed set of codes a repository publish
// failure can carry. Every predicate that needs to know whether a failure
// is a publish failure keys on IsPublishFailure instead of comparing codes
// directly.
var publishFailureCodes = map[Code]bool{
	PublishStackPullRequestClosed: true,
	PublishRemoteDiverged:         true,
	PublishRemoteChanged:          true,
	PublishStackMissing:           true,
	PublishPullRequestFailed:      true,
	PublishDescriptionFailed:      true,
	PublishPushFailed:             true,
}

// IsPublishFailure reports whether code is one of the publish failure codes
// a repository state's stored record can carry.
func IsPublishFailure(code Code) bool {
	return publishFailureCodes[code]
}

// PublishRepoParams carries the repository name, branch, rebase target,
// remote-only commit count, stack-layer identity, and pull-request URL a
// publish-failure summary interpolates. RenderRecord derives them from a
// stored record's repositories block; the static summary applies when the
// record carries no repositories.
type PublishRepoParams struct {
	Repo              string
	Branch            string
	RebaseTarget      string
	RemoteOnlyCommits int
	LayerPosition     int
	LayerTitle        string
	PullRequestURL    string
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

// publishLayerPhrase renders the stack-layer clause a publish summary
// appends to the repository name when the failing repository occupies a
// known stack layer. A zero layer position means the publish was not
// stack-layered, so the phrase stays empty and the summary keeps its
// historical shape.
func publishLayerPhrase(params PublishRepoParams) string {
	if params.LayerPosition <= 0 {
		return ""
	}
	if params.LayerTitle == "" {
		return fmt.Sprintf(" at layer %d", params.LayerPosition)
	}
	return fmt.Sprintf(" at layer %d (%s)", params.LayerPosition, params.LayerTitle)
}

// publishRepoName renders the quoted repository name with the stack-layer
// clause appended when one applies.
func publishRepoName(params PublishRepoParams) string {
	return fmt.Sprintf("%q", params.Repo) + publishLayerPhrase(params)
}

// publishDivergedSummary names the repository and remote-only commit count
// of the diverged pull-request branch.
func publishDivergedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return fmt.Sprintf(
		"The pull-request branch for %s contains %d remote %s that are not in this workspace.",
		publishRepoName(params), params.RemoteOnlyCommits, commitWord(params.RemoteOnlyCommits),
	)
}

// publishChangedSummary names the repository whose pull-request branch
// changed while it was being published.
func publishChangedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return "The pull-request branch for " + publishRepoName(params) + " changed while Agentico was publishing."
}

// publishStackPullRequestClosedSummary names the repository, stack layer,
// and pull request of a stack layer whose pull request was found closed
// without merge.
func publishStackPullRequestClosedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	subject := publishRepoName(params)
	if params.PullRequestURL != "" {
		subject += fmt.Sprintf(" (%s)", params.PullRequestURL)
	}
	return "The stack pull request for repository " + subject + " is closed without merge and cannot receive new commits."
}

// publishStackMissingSummary names the repository of a run that reached
// publish without an approved stack of pull requests.
func publishStackMissingSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return "The run reached publish for repository " + publishRepoName(params) + " without an approved stack of pull requests."
}

// publishPullRequestFailedSummary names the repository whose pull-request
// creation failed.
func publishPullRequestFailedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return "Creating the pull request for repository " + publishRepoName(params) + " failed."
}

// publishDescriptionFailedSummary names the repository whose pull-request
// description generation failed.
func publishDescriptionFailedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	return "Generating the pull-request description for repository " + publishRepoName(params) + " failed."
}

// publishPushFailedSummary names the repository (and branch when known) whose
// publish push failed.
func publishPushFailedSummary(p Params) string {
	params, ok := publishRepoParams(p)
	if !ok {
		return ""
	}
	if params.Branch != "" {
		return fmt.Sprintf("Publishing repository %s (branch %q) failed.", publishRepoName(params), params.Branch)
	}
	return "Publishing repository " + publishRepoName(params) + " failed."
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
				LayerPosition:     repo.LayerPosition,
				LayerTitle:        repo.LayerTitle,
				PullRequestURL:    repo.PullRequestURL,
			}
		}
	}
	return PublishRepoParams{}
}
