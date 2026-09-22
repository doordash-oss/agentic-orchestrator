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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestDeleteRemoteBranch_RemovesOnlyPushedLayerBranch(t *testing.T) {
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	layer1 := "feature/wslug/1-first"
	testutil.CreateBranch(t, repo, layer1)
	layer1SHA := testutil.CommitFile(t, repo, "layer1.txt", "layer 1\n", "layer 1")
	testutil.SimulatePush(t, repo, bare, layer1, layer1)

	runRewriteGit(t, repo, "checkout", "main")
	layer2 := "feature/wslug/2-second"
	testutil.CreateBranch(t, repo, layer2)
	layer2SHA := testutil.CommitFile(t, repo, "layer2.txt", "layer 2\n", "layer 2")
	testutil.SimulatePush(t, repo, bare, layer2, layer2)

	if err := DeleteRemoteBranch(repo, layer2); err != nil {
		t.Fatalf("DeleteRemoteBranch() error = %v; want pushed layer branch deleted", err)
	}

	remoteHeads := runRewriteGit(t, repo, "ls-remote", "--heads", "origin")
	if strings.Contains(remoteHeads, "refs/heads/"+layer2) {
		t.Fatalf("remote heads after delete = %q; want %q gone", remoteHeads, layer2)
	}
	for _, untouched := range []string{"refs/heads/" + layer1, "refs/heads/main"} {
		if !strings.Contains(remoteHeads, untouched) {
			t.Fatalf("remote heads after delete = %q; want %q untouched", remoteHeads, untouched)
		}
	}

	if got := runRewriteGit(t, repo, "rev-parse", "refs/heads/"+layer2); got != layer2SHA {
		t.Fatalf("local branch %s = %s; want %s untouched", layer2, got, layer2SHA)
	}
	if got := remoteBranchSHA(t, bare, layer1); got != layer1SHA {
		t.Fatalf("remote branch %s = %s; want %s untouched", layer1, got, layer1SHA)
	}
}

func TestDeleteRemoteBranch_AbsentRemoteBranchIsSuccess(t *testing.T) {
	repo, _ := testutil.InitPublishReadyGitRepo(t)
	branch := "feature/wslug/3-never-pushed"
	testutil.CreateBranch(t, repo, branch)
	localSHA := testutil.CommitFile(t, repo, "local.txt", "local\n", "local branch")

	if err := DeleteRemoteBranch(repo, branch); err != nil {
		t.Fatalf("DeleteRemoteBranch() error = %v; want absent remote ref treated as success", err)
	}
	if got := runRewriteGit(t, repo, "rev-parse", "refs/heads/"+branch); got != localSHA {
		t.Fatalf("local branch %s = %s; want %s untouched", branch, got, localSHA)
	}
}

func TestDeleteRemoteBranch_WithoutOriginRemoteFails(t *testing.T) {
	repo := testutil.InitGitRepo(t)

	err := DeleteRemoteBranch(repo, "feature/wslug/1-first")
	if err == nil {
		t.Fatal("DeleteRemoteBranch() error = nil; want missing origin remote rejected")
	}
	if !strings.Contains(err.Error(), `the "origin" remote does not exist`) {
		t.Fatalf("DeleteRemoteBranch() error = %v; want error naming the missing origin remote", err)
	}
}
