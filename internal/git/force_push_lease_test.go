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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// ForcePush's --force-with-lease has no explicit <expect> value, so its
// guarantee depends entirely on nobody having refreshed the remote-tracking
// ref just before the push. This test proves the lease rejects a push when
// the remote moved and this clone never fetched to find out — the exact
// property a stray git.Fetch before ForcePush would silently destroy.
func TestForcePush_RejectsWhenRemoteMovedWithoutFetch(t *testing.T) {
	t.Parallel()

	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, "feature/lease")
	testutil.CommitFile(t, repo, "first.txt", "first\n", "first commit")
	testutil.SimulatePush(t, repo, bare, "feature/lease", "feature/lease")

	// A second clone pushes a commit this clone never fetches.
	other := cloneFreshnessRepo(t, bare)
	runFreshnessGit(t, other, "checkout", "feature/lease")
	testutil.CommitFile(t, other, "other.txt", "other\n", "other's commit")
	testutil.SimulatePush(t, other, bare, "feature/lease", "feature/lease")

	// This clone rewrites its own branch locally without ever fetching, so
	// its stale origin/feature/lease tracking ref is not an ancestor of HEAD.
	runFreshnessGit(t, repo, "reset", "--hard", "HEAD~1")
	testutil.CommitFile(t, repo, "rewritten.txt", "rewritten\n", "rewritten commit")

	if err := ForcePush(repo, "feature/lease"); err == nil {
		t.Fatal("ForcePush() = nil, want an error: the remote moved and this clone never fetched")
	}
}
