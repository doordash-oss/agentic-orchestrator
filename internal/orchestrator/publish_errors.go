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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// PublishRemoteDivergedError reports remote work that cannot safely be
// replaced by the workspace's rewritten pull-request branch. LayerPosition
// and LayerTitle name the stack layer whose branch refused the push; a zero
// position means the failure predates the layered flow.
type PublishRemoteDivergedError struct {
	RepoName          string
	Branch            string
	RemoteOnlyCommits int
	LayerPosition     int
	LayerTitle        string
}

func (e *PublishRemoteDivergedError) Error() string {
	return "pull-request branch contains remote work that is not in this workspace"
}

// PublishRemoteChangedError reports a remote branch that moved after its
// safety state was inspected for a rewritten push.
type PublishRemoteChangedError struct {
	RepoName      string
	Branch        string
	LayerPosition int
	LayerTitle    string
}

func (e *PublishRemoteChangedError) Error() string {
	return "pull-request branch changed while Agentico was publishing"
}

// PublishStackClosedError reports a stack layer whose recorded pull request
// is closed without merge and can no longer receive commits. The layer's
// position and title and the pull-request URL travel in the error so the
// stored canonical record can name them.
type PublishStackClosedError struct {
	RepoName      string
	Branch        string
	LayerPosition int
	LayerTitle    string
	PRURL         string
	State         string
}

func (e *PublishStackClosedError) Error() string {
	return fmt.Sprintf("stack pull request %s for layer %d (%s) is %s; new commits cannot be delivered to it", e.PRURL, e.LayerPosition, e.LayerTitle, e.State)
}

// PublishStackMissingError reports a run that reached publish without an
// approved pull-request stack. Every publishable repository fails closed
// with it until the roadmap is rewound and re-approved with a valid Pull
// Requests table.
type PublishStackMissingError struct {
	RepoName string
	Branch   string
}

func (e *PublishStackMissingError) Error() string {
	return fmt.Sprintf("publish for repo %s has no approved pull-request stack", e.RepoName)
}

// PublishPRCreateError reports a failed pull-request creation. The layer
// fields name the stack layer whose pull request could not be opened.
type PublishPRCreateError struct {
	RepoName      string
	LayerPosition int
	LayerTitle    string
	Err           error
}

func (e *PublishPRCreateError) Error() string {
	return fmt.Sprintf("PR creation failed: %v", e.Err)
}

func (e *PublishPRCreateError) Unwrap() error { return e.Err }

// PublishDescriptionError reports a failed pull-request description
// generation. Publishing cannot proceed with synthetic fallback content.
// The layer fields name the stack layer whose description session failed.
type PublishDescriptionError struct {
	RepoName      string
	LayerPosition int
	LayerTitle    string
	Err           error
}

func (e *PublishDescriptionError) Error() string {
	return fmt.Sprintf("generate PR description: %v", e.Err)
}

func (e *PublishDescriptionError) Unwrap() error { return e.Err }

// PublishPushError reports a layer push failure that is neither a
// remote-diverged nor a remote-changed refusal. The layer fields name the
// stack layer whose branch could not be delivered.
type PublishPushError struct {
	RepoName      string
	Branch        string
	LayerPosition int
	LayerTitle    string
	Err           error
}

func (e *PublishPushError) Error() string {
	return fmt.Sprintf("push failed: %v", e.Err)
}

func (e *PublishPushError) Unwrap() error { return e.Err }

// PublishDispatchError marks every error returned by the completion
// dispatch's auto-publish call. Publish failures are owned by repository
// records and are never terminal, so the dispatch surface must not translate
// them into a run-level failure.
type PublishDispatchError struct {
	Err error
}

func (e *PublishDispatchError) Error() string { return e.Err.Error() }

func (e *PublishDispatchError) Unwrap() error { return e.Err }

// PublishReopenFailedError reports a reopen attempt the remote refused for
// a reason other than a missing head branch. The layer fields and the
// pull-request URL travel in the error so the stored canonical record can
// name them.
type PublishReopenFailedError struct {
	RepoName      string
	Branch        string
	LayerPosition int
	LayerTitle    string
	PRURL         string
	Err           error
}

func (e *PublishReopenFailedError) Error() string {
	return fmt.Sprintf("reopening stack pull request %s for layer %d (%s) failed: %v", e.PRURL, e.LayerPosition, e.LayerTitle, e.Err)
}

func (e *PublishReopenFailedError) Unwrap() error { return e.Err }

// PublishHeadBranchMissingError reports a closed stack pull request whose
// head branch no longer exists on the remote, so reopening cannot restore
// it; only recreation can resolve the condition.
type PublishHeadBranchMissingError struct {
	RepoName      string
	Branch        string
	LayerPosition int
	LayerTitle    string
	PRURL         string
}

func (e *PublishHeadBranchMissingError) Error() string {
	return fmt.Sprintf("stack pull request %s for layer %d (%s) has no head branch %q on the remote; it cannot be reopened", e.PRURL, e.LayerPosition, e.LayerTitle, e.Branch)
}

// PublishRecreateFailedError reports a failed pull-request recreation: the
// layer branch pushed but the replacement pull request could not be opened.
type PublishRecreateFailedError struct {
	RepoName      string
	LayerPosition int
	LayerTitle    string
	PRURL         string
	Err           error
}

func (e *PublishRecreateFailedError) Error() string {
	return fmt.Sprintf("recreating stack pull request for layer %d (%s) failed: %v", e.LayerPosition, e.LayerTitle, e.Err)
}

func (e *PublishRecreateFailedError) Unwrap() error { return e.Err }

// PublishRecreateMootError reports a recreate attempt whose pull request is
// live open or merged, so there is nothing to recreate. The live state was
// recorded before the refusal.
type PublishRecreateMootError struct {
	RepoName      string
	LayerPosition int
	State         string
}

func (e *PublishRecreateMootError) Error() string {
	return fmt.Sprintf("layer %d's pull request for repo %s is %s; there is nothing to recreate", e.LayerPosition, e.RepoName, e.State)
}

// PublishStateWriteError reports a failed durable write of a layer's publish
// state — the pull-request record, its pushed SHA, its live state, or the
// repository's published mark — after the remote operation it records
// succeeded. The layer fields, the operation, and any pull request the
// remote already created travel in the error so the stored canonical record
// can name them; the remote change itself stands, and retrying the action
// re-records the state.
type PublishStateWriteError struct {
	RepoName      string
	LayerPosition int
	LayerTitle    string
	Operation     string
	PRURL         string
	Err           error
}

func (e *PublishStateWriteError) Error() string {
	subject := fmt.Sprintf("layer %d (%s) of repo %q", e.LayerPosition, e.LayerTitle, e.RepoName)
	if e.PRURL != "" {
		subject += " with pull request " + e.PRURL
	}
	return fmt.Sprintf("%s for %s failed: %v", e.Operation, subject, e.Err)
}

func (e *PublishStateWriteError) Unwrap() error { return e.Err }

// stackStateWriteError wraps a failed durable stack-state write with the
// repository, layer, operation, and the pull request the write records.
func stackStateWriteError(repoName string, layer feature.StackLayer, operation, prURL string, err error) *PublishStateWriteError {
	return &PublishStateWriteError{
		RepoName:      repoName,
		LayerPosition: layer.Position,
		LayerTitle:    layer.Title,
		Operation:     operation,
		PRURL:         prURL,
		Err:           err,
	}
}
