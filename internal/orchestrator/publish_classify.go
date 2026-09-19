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
// where known, the stack layer the failure belongs to (position and title),
// the remote-only commit count, or the closed pull request's URL, and the
// raw error becomes diagnostics. One code per distinct manual remediation:
//
//   - stack pull request closed     → publish_stack_pull_request_closed
//   - run without an approved stack → publish_stack_missing
//   - rewritten-push diverged       → publish_remote_diverged
//   - rewritten-push changed        → publish_remote_changed
//   - pull-request creation         → publish_pull_request_failed
//   - description generation        → publish_description_failed
//   - reopen refusal                → publish_reopen_failed
//   - missing head branch           → publish_head_branch_missing
//   - pull-request recreation       → publish_recreate_failed
//   - commit, push, artifact scrub
//     → publish_push_failed
func publishFailureRecord(repoName, branch string, err error) errcat.FailureRecord {
	code := errcat.PublishPushFailed
	block := errcat.CodeRepository{Name: repoName, Branch: branch}
	var diverged *PublishRemoteDivergedError
	var changed *PublishRemoteChangedError
	var closed *PublishStackClosedError
	var missing *PublishStackMissingError
	var created *PublishPRCreateError
	var described *PublishDescriptionError
	var pushed *PublishPushError
	var reopenFailed *PublishReopenFailedError
	var headMissing *PublishHeadBranchMissingError
	var recreateFailed *PublishRecreateFailedError
	switch {
	case errors.As(err, &diverged):
		code = errcat.PublishRemoteDiverged
		if diverged.Branch != "" {
			block.Branch = diverged.Branch
		}
		block.RemoteOnlyCommits = diverged.RemoteOnlyCommits
		block.LayerPosition = diverged.LayerPosition
		block.LayerTitle = diverged.LayerTitle
	case errors.As(err, &changed):
		code = errcat.PublishRemoteChanged
		if changed.Branch != "" {
			block.Branch = changed.Branch
		}
		block.LayerPosition = changed.LayerPosition
		block.LayerTitle = changed.LayerTitle
	case errors.As(err, &closed):
		code = errcat.PublishStackPullRequestClosed
		if closed.Branch != "" {
			block.Branch = closed.Branch
		}
		block.LayerPosition = closed.LayerPosition
		block.LayerTitle = closed.LayerTitle
		block.PullRequestURL = closed.PRURL
	case errors.As(err, &missing):
		code = errcat.PublishStackMissing
	case errors.As(err, &created):
		code = errcat.PublishPullRequestFailed
		block.LayerPosition = created.LayerPosition
		block.LayerTitle = created.LayerTitle
	case errors.As(err, &described):
		code = errcat.PublishDescriptionFailed
		block.LayerPosition = described.LayerPosition
		block.LayerTitle = described.LayerTitle
	case errors.As(err, &pushed):
		code = errcat.PublishPushFailed
		if pushed.Branch != "" {
			block.Branch = pushed.Branch
		}
		block.LayerPosition = pushed.LayerPosition
		block.LayerTitle = pushed.LayerTitle
	case errors.As(err, &reopenFailed):
		code = errcat.PublishReopenFailed
		if reopenFailed.Branch != "" {
			block.Branch = reopenFailed.Branch
		}
		block.LayerPosition = reopenFailed.LayerPosition
		block.LayerTitle = reopenFailed.LayerTitle
		block.PullRequestURL = reopenFailed.PRURL
	case errors.As(err, &headMissing):
		code = errcat.PublishHeadBranchMissing
		if headMissing.Branch != "" {
			block.Branch = headMissing.Branch
		}
		block.LayerPosition = headMissing.LayerPosition
		block.LayerTitle = headMissing.LayerTitle
		block.PullRequestURL = headMissing.PRURL
	case errors.As(err, &recreateFailed):
		code = errcat.PublishRecreateFailed
		block.LayerPosition = recreateFailed.LayerPosition
		block.LayerTitle = recreateFailed.LayerTitle
		block.PullRequestURL = recreateFailed.PRURL
	}
	return errcat.FailureRecord{
		Code:        code,
		Context:     &errcat.RecordContext{Repositories: []errcat.CodeRepository{block}},
		Diagnostics: err.Error(),
	}
}

// PublishConflictRecord classifies a conflict-class publish mutation error
// (remote diverged, remote changed) into the canonical record the repository
// state stores, so the HTTP envelope and the stored record agree. It reports
// false for every other error.
func PublishConflictRecord(err error) (errcat.FailureRecord, bool) {
	var diverged *PublishRemoteDivergedError
	var changed *PublishRemoteChangedError
	switch {
	case errors.As(err, &diverged):
		return publishFailureRecord(diverged.RepoName, diverged.Branch, err), true
	case errors.As(err, &changed):
		return publishFailureRecord(changed.RepoName, changed.Branch, err), true
	}
	return errcat.FailureRecord{}, false
}

// StackClosedConflictRecord classifies a closed-stack-PR refusal — the
// rebase preflight's launch refusal — into the canonical record the
// repository state stores, so a preflight surface can reject with the same
// canonical code and context. It reports false for every other error.
func StackClosedConflictRecord(err error) (errcat.FailureRecord, bool) {
	var closed *PublishStackClosedError
	if errors.As(err, &closed) {
		return publishFailureRecord(closed.RepoName, closed.Branch, err), true
	}
	return errcat.FailureRecord{}, false
}

// PullRequestResolutionConflictRecord classifies a reopen or recreate
// mutation failure that stored a canonical record on the repository — the
// resolution refusals plus the push and description failures the actions
// reuse — so the HTTP envelope and the stored record agree. It reports
// false for validation and moot-state failures, which carry no record.
func PullRequestResolutionConflictRecord(err error) (errcat.FailureRecord, bool) {
	var reopenFailed *PublishReopenFailedError
	var headMissing *PublishHeadBranchMissingError
	var recreateFailed *PublishRecreateFailedError
	var pushed *PublishPushError
	var described *PublishDescriptionError
	switch {
	case errors.As(err, &reopenFailed):
		return publishFailureRecord(reopenFailed.RepoName, reopenFailed.Branch, err), true
	case errors.As(err, &headMissing):
		return publishFailureRecord(headMissing.RepoName, headMissing.Branch, err), true
	case errors.As(err, &recreateFailed):
		return publishFailureRecord(recreateFailed.RepoName, "", err), true
	case errors.As(err, &pushed):
		return publishFailureRecord(pushed.RepoName, pushed.Branch, err), true
	case errors.As(err, &described):
		return publishFailureRecord(described.RepoName, "", err), true
	}
	return PublishConflictRecord(err)
}

// storePublishFailure classifies err at the publish boundary and stores the
// canonical record on the repository state. It is never terminal: no
// run-level failure is written for a publish failure.
func (o *Orchestrator) storePublishFailure(f *feature.Feature, repoName string, err error) {
	branch := ""
	if repo, ok := findRepo(f, repoName); ok {
		branch = repo.Branch
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
