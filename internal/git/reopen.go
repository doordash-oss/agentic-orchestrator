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

package git

import (
	"errors"
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// ErrPRHeadBranchMissing reports that the head branch of a closed stack
// pull request no longer exists on the remote, so the pull request cannot
// be reopened; only recreating it — pushing the branch again and opening a
// fresh pull request — can resolve the condition.
var ErrPRHeadBranchMissing = errors.New("pull-request head branch no longer exists on the remote")

// ReopenPullRequest sets the closed pull request at prURL back to open on
// GitHub. The layer branch must still exist on origin: the remote ref is
// probed first and a missing branch answers ErrPRHeadBranchMissing without
// any API call. GitHub's 422 "branch has been deleted" refusal maps to the
// same error as a fallback; every other failure is returned with the API
// text for diagnostics.
func ReopenPullRequest(repoPath, branch, prURL string) error {
	remoteRef := "refs/heads/" + branch
	if _, present, err := rewritePushRemoteBranchState(repoPath, remoteRef); err != nil {
		return fmt.Errorf("checking layer branch %q on origin: %w", branch, err)
	} else if !present {
		return fmt.Errorf("%w: branch %q", ErrPRHeadBranchMissing, branch)
	}
	owner, repo, number, err := ParsePRURL(prURL)
	if err != nil {
		return err
	}
	client, err := github.ForHost(prURLHost(prURL))
	if err != nil {
		return err
	}
	if err := client.ReopenPR(owner, repo, number); err != nil {
		if errors.Is(err, github.ErrHeadBranchDeleted) {
			return fmt.Errorf("%w: branch %q", ErrPRHeadBranchMissing, branch)
		}
		return err
	}
	return nil
}
