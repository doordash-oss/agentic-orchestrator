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

// A layer branch that is not checked out anywhere — the worktree has moved on
// to a higher layer branch — is still deliverable by ref name from the shared
// repository, and delivering it must not disturb the checked-out branch.
func TestPushLayerBranch_CreatesAbsentRemoteBranchWhileWorktreeOnHigherBranch(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	layer := "feature/layer-one"
	testutil.CreateBranch(t, repo, layer)
	layerSHA := testutil.CommitFile(t, repo, "layer.txt", "layer\n", "layer one commit")

	runRewriteGit(t, repo, "checkout", "-b", "feature/layer-two")
	topSHA := testutil.CommitFile(t, repo, "top.txt", "top\n", "layer two commit")

	pushedSHA, err := PushLayerBranch(repo, layer, layerSHA, "")
	if err != nil {
		t.Fatalf("PushLayerBranch() error = %v; want absent remote branch created from an unchecked-out layer", err)
	}
	if pushedSHA != layerSHA {
		t.Fatalf("PushLayerBranch() = %s; want layer tip %s", pushedSHA, layerSHA)
	}
	if got := remoteBranchSHA(t, bare, layer); got != layerSHA {
		t.Fatalf("remote tip = %s; want layer tip %s", got, layerSHA)
	}
	if got := localHeadSHA(t, repo); got != topSHA {
		t.Fatalf("worktree HEAD = %s; want untouched higher layer tip %s", got, topSHA)
	}
	if got := runRewriteGit(t, repo, "branch", "--show-current"); got != "feature/layer-two" {
		t.Fatalf("checked-out branch = %q; want the higher layer branch", got)
	}

	// An unchanged SHA is a plain no-op push: the remote tip is an ancestor of
	// (here, equal to) the SHA being delivered.
	if _, err := PushLayerBranch(repo, layer, layerSHA, layerSHA); err != nil {
		t.Fatalf("PushLayerBranch() unchanged error = %v; want plain no-op push", err)
	}
	if got := remoteBranchSHA(t, bare, layer); got != layerSHA {
		t.Fatalf("remote tip after no-op push = %s; want layer tip %s", got, layerSHA)
	}
}

// A rewritten local layer ref replaces the previously pushed SHA through a
// lease pinned to that SHA with no further proof. The remote-only commit is
// an ordinary commit, so success here proves the last-pushed fast path was
// taken: the redundant-merge proof would have refused it.
func TestPushLayerBranch_LeasePushesRewrittenLayerOverLastPushedSHA(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/layer-lease"
	testutil.CreateBranch(t, repo, branch)
	firstSHA := testutil.CommitFile(t, repo, "first.txt", "first\n", "first layer commit")
	testutil.SimulatePush(t, repo, bare, branch, branch)

	runRewriteGit(t, repo, "reset", "--hard", "HEAD~1")
	rewrittenSHA := testutil.CommitFile(t, repo, "rewritten.txt", "rewritten\n", "rewritten layer commit")
	if rewrittenSHA == firstSHA {
		t.Fatal("rewritten layer tip equals the first push; fixture did not rewrite history")
	}

	pushedSHA, err := PushLayerBranch(repo, branch, rewrittenSHA, firstSHA)
	if err != nil {
		t.Fatalf("PushLayerBranch() error = %v; want lease push over the SHA Agentico pushed", err)
	}
	if pushedSHA != rewrittenSHA {
		t.Fatalf("PushLayerBranch() = %s; want rewritten layer tip %s", pushedSHA, rewrittenSHA)
	}
	if got := remoteBranchSHA(t, bare, branch); got != rewrittenSHA {
		t.Fatalf("remote tip = %s; want rewritten layer tip %s", got, rewrittenSHA)
	}
}

// A remote that moves between the last-pushed inspection and the lease push
// is rejected with the remote-changed kind, preserving the mover's work.
func TestPushLayerBranch_LeaseOverLastPushedSHARejectsRemoteMoveAfterInspection(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/layer-lease-move"
	testutil.CreateBranch(t, repo, branch)
	firstSHA := testutil.CommitFile(t, repo, "first.txt", "first\n", "first layer commit")
	testutil.SimulatePush(t, repo, bare, branch, branch)

	runRewriteGit(t, repo, "reset", "--hard", "HEAD~1")
	rewrittenSHA := testutil.CommitFile(t, repo, "rewritten.txt", "rewritten\n", "rewritten layer commit")

	other := cloneFreshnessRepo(t, bare)
	runRewriteGit(t, other, "checkout", branch)

	var movedSHA string
	_, err := pushLayerBranch(repo, branch, rewrittenSHA, firstSHA, func() {
		movedSHA = testutil.CommitFile(t, other, "moved.txt", "moved\n", "remote moved after inspection")
		testutil.SimulatePush(t, other, bare, branch, branch)
	})
	assertRewritePushError(t, err, RewritePushRemoteChanged, branch, 0)
	if got := remoteBranchSHA(t, bare, branch); got != movedSHA {
		t.Fatalf("remote tip = %s; want post-inspection move %s preserved", got, movedSHA)
	}
}
