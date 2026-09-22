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

package git

import (
	"errors"
	"fmt"
	"os/exec"
)

// DeleteRemoteBranch deletes branch from the repository's origin remote by
// explicit ref name (refs/heads/<branch>). A remote ref that no longer
// exists is success so retried rewinds stay idempotent; the local branch and
// every other remote branch are untouched.
func DeleteRemoteBranch(repoPath, branch string) error {
	mu := worktreeMutationLock(repoPath)
	mu.Lock()
	defer mu.Unlock()

	if !HasOriginRemote(repoPath) {
		return fmt.Errorf("deleting remote branch %q: the %q remote does not exist", branch, "origin")
	}

	remoteRef := "refs/heads/" + branch
	_, remoteExists, err := rewritePushRemoteBranchState(repoPath, remoteRef)
	if err != nil {
		return fmt.Errorf("deleting remote branch %q: %w", branch, err)
	}
	if !remoteExists {
		return nil
	}

	if _, err := runGitMutationWithLockRetry(repoPath, "push", "origin", "--delete", remoteRef); err != nil {
		return fmt.Errorf("deleting remote branch %q: %w", branch, sanitizeRemoteDeletePushError(err))
	}
	return nil
}

// sanitizeRemoteDeletePushError reports a rejected delete push with a fixed
// operation label and no raw git output. Non-exit failures are typed errors
// (git lock contention) whose text is already safe.
func sanitizeRemoteDeletePushError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("git push deleting remote branch: %w", exitErr)
	}
	return fmt.Errorf("git push deleting remote branch: %w", err)
}
