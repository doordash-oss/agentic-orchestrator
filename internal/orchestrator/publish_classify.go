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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// publishFailureRecord classifies a repository publish failure by inspecting
// the error chain and builds the canonical record the repository state
// stores: the repositories block names the repository with its branch and,
// where known, the rebase target or remote-only commit count, and the raw
// error becomes diagnostics. One code per distinct manual remediation:
//
//   - pull-rebase conflict            → publish_rebase_conflict
//   - rewritten-push diverged         → publish_remote_diverged
//   - rewritten-push changed          → publish_remote_changed
//   - closed or merged pull request   → publish_pull_request_closed
//   - pull-request creation           → publish_pull_request_failed
//   - description generation          → publish_description_failed
//   - commit, non-conflict pull-rebase, push, artifact scrub
//     → publish_push_failed
func publishFailureRecord(repoName, branch string, err error) errcat.FailureRecord {
	code := errcat.PublishPushFailed
	block := errcat.CodeRepository{Name: repoName, Branch: branch}
	var conflict *PublishConflictError
	var diverged *PublishRemoteDivergedError
	var changed *PublishRemoteChangedError
	var closed *PublishPRClosedError
	var created *PublishPRCreateError
	var described *PublishDescriptionError
	switch {
	case errors.As(err, &conflict):
		code = errcat.PublishRebaseConflict
		if conflict.Branch != "" {
			block.Branch = conflict.Branch
		}
		block.RebaseTarget = conflict.RebaseTarget
	case errors.As(err, &diverged):
		code = errcat.PublishRemoteDiverged
		if diverged.Branch != "" {
			block.Branch = diverged.Branch
		}
		block.RemoteOnlyCommits = diverged.RemoteOnlyCommits
	case errors.As(err, &changed):
		code = errcat.PublishRemoteChanged
		if changed.Branch != "" {
			block.Branch = changed.Branch
		}
	case errors.As(err, &closed):
		code = errcat.PublishPullRequestClosed
	case errors.As(err, &created):
		code = errcat.PublishPullRequestFailed
	case errors.As(err, &described):
		code = errcat.PublishDescriptionFailed
	}
	return errcat.FailureRecord{
		Code:        code,
		Context:     &errcat.RecordContext{Repositories: []errcat.CodeRepository{block}},
		Diagnostics: err.Error(),
	}
}

// PublishConflictRecord classifies a conflict-class publish mutation error
// (pull-rebase conflict, remote diverged, remote changed) into the canonical
// record the repository state stores, so the HTTP envelope and the stored
// record agree. It reports false for every other error.
func PublishConflictRecord(err error) (errcat.FailureRecord, bool) {
	var conflict *PublishConflictError
	var diverged *PublishRemoteDivergedError
	var changed *PublishRemoteChangedError
	switch {
	case errors.As(err, &conflict):
		return publishFailureRecord(conflict.RepoName, conflict.Branch, err), true
	case errors.As(err, &diverged):
		return publishFailureRecord(diverged.RepoName, diverged.Branch, err), true
	case errors.As(err, &changed):
		return publishFailureRecord(changed.RepoName, changed.Branch, err), true
	}
	return errcat.FailureRecord{}, false
}

// storePublishFailure classifies err at the publish boundary and stores the
// canonical record on the repository state. It is never terminal: no
// run-level failure is written for a publish failure.
func (o *Orchestrator) storePublishFailure(f *feature.Feature, repoName string, err error) {
	branch := ""
	if repo, ok := findRepo(f, repoName); ok {
		branch = repoBranch(f, repo)
	}
	_ = o.deps.Lifecycle.SetRepoPublishError(f.ID, repoName, publishFailureRecord(repoName, branch, err))
}

// firstFailedRepoError renders the stored record of the first repository
// carrying one, for failure-carrying publish events. It returns false when
// no repository carries a record.
func firstFailedRepoError(f *feature.Feature) (errcat.Error, bool) {
	for _, repo := range f.Repos {
		state, ok := f.RepoStates[repo.Name]
		if !ok || state == nil || state.Error == nil {
			continue
		}
		return errcat.RenderRecord(*state.Error), true
	}
	return errcat.Error{}, false
}
