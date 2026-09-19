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
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// reopenLayerRepo builds a repository whose origin is a bare remote that
// carries the layer branch, mirroring a published stack layer.
func reopenLayerRepo(t *testing.T, branch string) (repoPath, barePath string) {
	t.Helper()
	repo, bare := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repo, branch)
	testutil.CommitFile(t, repo, "layer.txt", "layer work\n", "layer commit")
	testutil.SimulatePush(t, repo, bare, branch, branch)
	return repo, bare
}

// seedReopenPull creates pull request #1 on the fake pull store through its
// create endpoint, exactly as the cross-reference tests seed the store.
func seedReopenPull(t *testing.T, fake *testutil.FakeGitHubAPI) {
	t.Helper()
	resp, err := http.Post(fake.URL+"/repos/acme/widgets/pulls", "application/json",
		strings.NewReader(`{"title":"Ship","head":"feature/layer-2","base":"main","body":"B"}`))
	if err != nil {
		t.Fatalf("seeding pull request: %v", err)
	}
	resp.Body.Close()
}

func TestReopenPullRequestReopensClosedPR(t *testing.T) {
	repo, _ := reopenLayerRepo(t, "feature/layer-2")

	fake := testutil.InstallFakeGitHubAPI(t)
	store := testutil.NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	seedReopenPull(t, fake)
	if !store.MarkClosed("widgets", 1) {
		t.Fatal("MarkClosed(widgets, 1) = false, want the seeded pull request")
	}

	if err := ReopenPullRequest(repo, "feature/layer-2", store.URL("widgets", 1)); err != nil {
		t.Fatalf("ReopenPullRequest() error = %v", err)
	}
	if fake.RequestCount(`PATCH /repos/acme/widgets/pulls/1 {"state":"open"}`) != 1 {
		t.Fatalf("requests = %v; want exactly one reopen PATCH", fake.Requests())
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "open" {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record open after reopen", pr, ok)
	}
	if store.PatchedCount() != 1 {
		t.Fatalf("PatchedCount() = %d; want 1", store.PatchedCount())
	}
}

func TestReopenPullRequestReportsMissingHeadBranchWithoutAPICall(t *testing.T) {
	repo, bare := reopenLayerRepo(t, "feature/layer-2")
	// The layer branch is gone from the remote: an external delete removed
	// the ref from the bare origin.
	runGit(t, bare, "update-ref", "-d", "refs/heads/feature/layer-2")

	fake := testutil.InstallFakeGitHubAPI(t)
	store := testutil.NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	seedReopenPull(t, fake)
	store.MarkClosed("widgets", 1)

	err := ReopenPullRequest(repo, "feature/layer-2", store.URL("widgets", 1))
	if !errors.Is(err, ErrPRHeadBranchMissing) {
		t.Fatalf("ReopenPullRequest() error = %v; want ErrPRHeadBranchMissing", err)
	}
	if fake.RequestCount("/repos/acme/widgets/pulls/1") != 0 {
		t.Fatalf("requests = %v; want no API call when the layer branch is absent", fake.Requests())
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "closed" {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record still closed", pr, ok)
	}
}

func TestReopenPullRequestMapsHeadDeleted422ToMissingHeadBranch(t *testing.T) {
	repo, _ := reopenLayerRepo(t, "feature/layer-2")

	fake := testutil.InstallFakeGitHubAPI(t)
	store := testutil.NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	seedReopenPull(t, fake)
	store.MarkClosed("widgets", 1)
	store.MarkHeadDeleted("widgets", 1)

	err := ReopenPullRequest(repo, "feature/layer-2", store.URL("widgets", 1))
	if !errors.Is(err, ErrPRHeadBranchMissing) {
		t.Fatalf("ReopenPullRequest() error = %v; want ErrPRHeadBranchMissing via the 422 fallback", err)
	}
	if fake.RequestCount(`PATCH /repos/acme/widgets/pulls/1 {"state":"open"}`) != 1 {
		t.Fatalf("requests = %v; want the fallback to call the API once", fake.Requests())
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "closed" {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record still closed after the refusal", pr, ok)
	}
}

func TestReopenPullRequestReturnsMergedRefusalWithAPIText(t *testing.T) {
	repo, _ := reopenLayerRepo(t, "feature/layer-2")

	fake := testutil.InstallFakeGitHubAPI(t)
	store := testutil.NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	seedReopenPull(t, fake)
	store.MarkMerged("widgets", 1)

	err := ReopenPullRequest(repo, "feature/layer-2", store.URL("widgets", 1))
	if err == nil {
		t.Fatal("ReopenPullRequest() = nil error, want the merged-PR refusal")
	}
	if !strings.Contains(err.Error(), "cannot reopen a merged pull request") {
		t.Fatalf("ReopenPullRequest() error = %v; want the API's 422 text carried through", err)
	}
	if errors.Is(err, ErrPRHeadBranchMissing) {
		t.Fatalf("ReopenPullRequest() error = %v; a merged PR is not a missing head branch", err)
	}
}
