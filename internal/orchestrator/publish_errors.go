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

import "fmt"

// PublishRemoteDivergedError reports remote work that cannot safely be
// replaced by the workspace's rewritten pull-request branch.
type PublishRemoteDivergedError struct {
	RepoName          string
	Branch            string
	RemoteOnlyCommits int
}

func (e *PublishRemoteDivergedError) Error() string {
	return "pull-request branch contains remote work that is not in this workspace"
}

// PublishRemoteChangedError reports a remote branch that moved after its
// safety state was inspected for a rewritten push.
type PublishRemoteChangedError struct {
	RepoName string
	Branch   string
}

func (e *PublishRemoteChangedError) Error() string {
	return "pull-request branch changed while Agentico was publishing"
}

// PublishPRClosedError reports a republish whose existing pull request is
// closed or merged and can no longer receive commits. The pull-request URL
// travels in the error text, which becomes the stored record's diagnostics.
type PublishPRClosedError struct {
	RepoName string
	PRURL    string
	State    string
}

func (e *PublishPRClosedError) Error() string {
	return fmt.Sprintf("pull request %s is %s; new commits cannot be delivered to it", e.PRURL, e.State)
}

// PublishPRCreateError reports a failed pull-request creation.
type PublishPRCreateError struct {
	RepoName string
	Err      error
}

func (e *PublishPRCreateError) Error() string {
	return fmt.Sprintf("PR creation failed: %v", e.Err)
}

func (e *PublishPRCreateError) Unwrap() error { return e.Err }

// PublishDescriptionError reports a failed pull-request description
// generation. Publishing cannot proceed with synthetic fallback content.
type PublishDescriptionError struct {
	RepoName string
	Err      error
}

func (e *PublishDescriptionError) Error() string {
	return fmt.Sprintf("generate PR description: %v", e.Err)
}

func (e *PublishDescriptionError) Unwrap() error { return e.Err }

// PublishDispatchError marks every error returned by the completion
// dispatch's auto-publish call. Publish failures are owned by repository
// records and are never terminal, so the dispatch surface must not translate
// them into a run-level failure.
type PublishDispatchError struct {
	Err error
}

func (e *PublishDispatchError) Error() string { return e.Err.Error() }

func (e *PublishDispatchError) Unwrap() error { return e.Err }
