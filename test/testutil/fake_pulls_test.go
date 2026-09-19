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

package testutil

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// createFakePull seeds pull request #1 for the widgets repository through
// the store's create endpoint, exactly as feature tests do.
func createFakePull(t *testing.T, fake *FakeGitHubAPI) {
	t.Helper()
	resp, err := http.Post(fake.URL+"/repos/acme/widgets/pulls", "application/json",
		strings.NewReader(`{"title":"T","head":"feature/x","base":"main","body":"B"}`))
	if err != nil {
		t.Fatalf("seeding pull request: %v", err)
	}
	resp.Body.Close()
}

// patchFakePull PATCHes one pull request on the fake's HTTP surface and
// returns the response status and body.
func patchFakePull(t *testing.T, fake *FakeGitHubAPI, path, payload string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPatch, fake.URL+path, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("building PATCH %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading PATCH %s response: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

func TestFakePullStorePatchReopensClosedPull(t *testing.T) {
	fake := InstallFakeGitHubAPI(t)
	store := NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	createFakePull(t, fake)
	if !store.MarkClosed("widgets", 1) {
		t.Fatal("MarkClosed(widgets, 1) = false, want the seeded pull request")
	}

	status, _ := patchFakePull(t, fake, "/repos/acme/widgets/pulls/1", `{"state":"open"}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH state=open status = %d, want 200", status)
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "open" {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record open", pr, ok)
	}
	patches := store.Patches()
	if len(patches) != 1 || patches[0].State == nil || *patches[0].State != "open" {
		t.Fatalf("Patches() = %+v; want one state patch to open", patches)
	}
}

func TestFakePullStorePatchRefusesMergedReopen(t *testing.T) {
	fake := InstallFakeGitHubAPI(t)
	store := NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	createFakePull(t, fake)
	store.MarkMerged("widgets", 1)

	status, body := patchFakePull(t, fake, "/repos/acme/widgets/pulls/1", `{"state":"open"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH state=open status = %d body = %s, want 422", status, body)
	}
	if !strings.Contains(body, "cannot reopen a merged pull request") {
		t.Fatalf("PATCH body = %s; want GitHub's merged-reopen refusal", body)
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "closed" || !pr.Merged {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record still closed and merged", pr, ok)
	}
	if store.PatchedCount() != 0 {
		t.Fatalf("PatchedCount() = %d; want 0 for the refused patch", store.PatchedCount())
	}
}

func TestFakePullStorePatchAnswersBranchDeleted422WhenHeadDeleted(t *testing.T) {
	fake := InstallFakeGitHubAPI(t)
	store := NewFakePullStore("acme")
	store.Install(t, fake, "widgets")
	createFakePull(t, fake)
	store.MarkClosed("widgets", 1)
	store.MarkHeadDeleted("widgets", 1)

	status, body := patchFakePull(t, fake, "/repos/acme/widgets/pulls/1", `{"state":"open"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH state=open status = %d body = %s, want 422", status, body)
	}
	if !strings.Contains(body, "branch has been deleted") {
		t.Fatalf("PATCH body = %s; want GitHub's branch-deleted refusal", body)
	}
	pr, ok := store.Pull("widgets", 1)
	if !ok || pr.State != "closed" {
		t.Fatalf("Pull(widgets, 1) = %+v, %v; want the record still closed after the refusal", pr, ok)
	}
	if store.PatchedCount() != 0 {
		t.Fatalf("PatchedCount() = %d; want 0 for the refused patch", store.PatchedCount())
	}
}
