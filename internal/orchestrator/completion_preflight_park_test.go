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

// Real-git coverage of the completion preflight's closed-PR blocker parking
// (parkClosedStackPullRequest): a repository whose live refresh observes a
// closed-unmerged pull request is parked on the same needs-action record the
// publish walk stores, naming the lowest closed layer, and the park store is
// the last write of the pass — a per-layer state write clears the stored
// record, so the record surviving the pass proves the ordering. A second
// preflight over the same state leaves exactly that record; a live-open
// pull request stores nothing new and the reopen resolution clears a
// previously parked record; a repository with no closed pull request stores
// nothing.

package orchestrator

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// completionParkPRURLs are the fixture stack's recorded pull requests, one
// per layer, matching hintPreflightStack.
var completionParkPRURLs = [3]string{
	"https://github.example/repo-a/pull/1",
	"https://github.example/repo-a/pull/2",
	"https://github.example/repo-a/pull/3",
}

// countingLifecycle wraps a ports.FeatureLifecycle and counts
// SetRepoPublishError calls, so a test can prove the park store wrote
// exactly once — or not at all.
type countingLifecycle struct {
	ports.FeatureLifecycle
	parkStores int
}

func (c *countingLifecycle) SetRepoPublishError(featureID, repoName string, record errcat.FailureRecord) error {
	c.parkStores++
	return c.FeatureLifecycle.SetRepoPublishError(featureID, repoName, record)
}

// completionParkHarness carries the fixture's knobs: the live pull-request
// states the scripted remote serves, the counting lifecycle, the store the
// parked records land in, and the shared mock for remote-call assertions.
type completionParkHarness struct {
	states map[string]string
	lc     *countingLifecycle
	store  *feature.Store
	remote *mocks.MockRemoteOps
}

// newCompletionParkFixture wires the shared closed-park fixture: the
// three-layer delivered stack of hintPreflightRepo and hintPreflightStack,
// the real manager-backed lifecycle behind the counting wrapper, and the
// shared MockRemoteOps serving whatever pull-request states the test scripts
// into the harness — every unscripted pull request reads open.
func newCompletionParkFixture(t *testing.T) (*Orchestrator, *completionParkHarness) {
	t.Helper()
	repoPath, tip1, tip2, tip3 := hintPreflightRepo(t)
	store := feature.NewStore(t.TempDir())
	manager := feature.NewManager(store, config.NewDefault())
	f := &feature.Feature{
		ID:            "feat-closed-park",
		Name:          "Closed park",
		Slug:          "closed-park",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Stack:         hintPreflightStack(tip1, tip2, tip3),
		Repos: []feature.FeatureRepo{{
			Name:         "repo-a",
			Path:         repoPath,
			WorktreePath: repoPath,
			Branch:       "feature/l1",
			BaseBranch:   "main",
			Publishable:  boolPtr(true),
		}},
		RepoStates: map[string]*feature.RepoState{"repo-a": {Touched: true}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	harness := &completionParkHarness{
		states: map[string]string{},
		lc:     &countingLifecycle{FeatureLifecycle: manager},
		store:  store,
		remote: mocks.NewMockRemoteOps(),
	}
	harness.remote.PRStateFn = func(_, prURL string) (string, error) {
		if state, ok := harness.states[prURL]; ok {
			return state, nil
		}
		return git.PRStateOpen, nil
	}
	o := New(Deps{Lifecycle: harness.lc, Store: store, Remote: harness.remote}, Hooks{})
	return o, harness
}

// parkPreflightRepoResult runs the completion preflight and returns the
// fixture's single repository result.
func parkPreflightRepoResult(t *testing.T, o *Orchestrator) CompletionRepoResult {
	t.Helper()
	result, err := o.CompletionPreflight("feat-closed-park")
	if err != nil {
		t.Fatalf("CompletionPreflight: %v", err)
	}
	if len(result.Repos) != 1 {
		t.Fatalf("repos len = %d; want 1", len(result.Repos))
	}
	return result.Repos[0]
}

// reload re-reads the fixture feature from the store, so a test asserts the
// durable state after the preflight returned.
func (h *completionParkHarness) reload(t *testing.T) *feature.Feature {
	t.Helper()
	f, err := h.store.Load("feat-closed-park")
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}
	return f
}

// assertClosedParkRecord pins a reported or stored record as the canonical
// closed-pull-request record naming the repository, the layer, its title,
// and the pull request.
func assertClosedParkRecord(t *testing.T, record *errcat.FailureRecord, wantLayer int, wantTitle, wantURL string) {
	t.Helper()
	if record == nil || record.Code != errcat.PublishStackPullRequestClosed {
		t.Fatalf("record = %+v, want publish_stack_pull_request_closed", record)
	}
	if record.Context == nil || len(record.Context.Repositories) != 1 {
		t.Fatalf("record context = %+v, want exactly one repositories block", record.Context)
	}
	block := record.Context.Repositories[0]
	if block.Name != "repo-a" || block.LayerPosition != wantLayer || block.LayerTitle != wantTitle || block.PullRequestURL != wantURL {
		t.Fatalf("record block = %+v, want repo-a layer %d (%s) at %s", block, wantLayer, wantTitle, wantURL)
	}
}

// A repository whose layer 2 pull request reads closed live is parked on the
// closed record: the preflight reports the closed layer entry, carries the
// parked record, persists the live closed state on the stack, and — because
// a per-layer state write clears the stored record — the record still in the
// store after the preflight returned proves the park store was the last
// write of the pass.
func TestCompletionPreflightClosedLayerParksRepositoryRecord(t *testing.T) {
	t.Parallel()
	o, harness := newCompletionParkFixture(t)
	harness.states[completionParkPRURLs[1]] = git.PRStateClosed

	got := parkPreflightRepoResult(t, o)
	if len(got.PullRequests) != 3 {
		t.Fatalf("pull requests len = %d; want one entry per layer", len(got.PullRequests))
	}
	if got.PullRequests[1].URL != completionParkPRURLs[1] || got.PullRequests[1].State != string(feature.StackPRStateClosed) {
		t.Fatalf("layer 2 entry = %+v, want the closed live state at %s", got.PullRequests[1], completionParkPRURLs[1])
	}
	assertClosedParkRecord(t, got.Error, 2, "Fix auth", completionParkPRURLs[1])
	if parkStores := harness.lc.parkStores; parkStores != 1 {
		t.Fatalf("park stores = %d, want exactly 1", parkStores)
	}

	persisted := harness.reload(t)
	assertClosedParkRecord(t, persisted.RepoStates["repo-a"].Error, 2, "Fix auth", completionParkPRURLs[1])
	if entry := stackRepoEntryFor(persisted, 2, "repo-a"); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("persisted layer 2 PR state = %q, want closed", entry.PRState)
	}
}

// A second preflight over the same closed state leaves exactly the same
// parked record: the entry already records closed so no per-layer state
// write runs, and the park store is skipped because the repository already
// stores the closed code for that layer.
func TestCompletionPreflightSecondPassLeavesSameParkedRecord(t *testing.T) {
	t.Parallel()
	o, harness := newCompletionParkFixture(t)
	harness.states[completionParkPRURLs[1]] = git.PRStateClosed
	parkPreflightRepoResult(t, o)
	if parkStores := harness.lc.parkStores; parkStores != 1 {
		t.Fatalf("park stores after the first pass = %d, want 1", parkStores)
	}

	second := parkPreflightRepoResult(t, o)
	if parkStores := harness.lc.parkStores; parkStores != 1 {
		t.Fatalf("park stores after the second pass = %d, want still 1 (the write is skipped)", parkStores)
	}
	assertClosedParkRecord(t, second.Error, 2, "Fix auth", completionParkPRURLs[1])

	persisted := harness.reload(t)
	assertClosedParkRecord(t, persisted.RepoStates["repo-a"].Error, 2, "Fix auth", completionParkPRURLs[1])
}

// A repository whose closed pull request was reopened on the remote stores
// nothing new — a live open state writes no per-layer state and parks no
// record — and the reopen resolution then clears the previously parked
// record by recording the layer open, without any remote write.
func TestCompletionPreflightReopenedPullRequestStoresNothingAndReopenClearsRecord(t *testing.T) {
	t.Parallel()
	o, harness := newCompletionParkFixture(t)
	harness.states[completionParkPRURLs[1]] = git.PRStateClosed
	parkPreflightRepoResult(t, o)

	// The pull request was reopened on the remote since the park; the
	// unscripted URL now reads open.
	delete(harness.states, completionParkPRURLs[1])
	reopened := parkPreflightRepoResult(t, o)
	if parkStores := harness.lc.parkStores; parkStores != 1 {
		t.Fatalf("park stores with the live state open = %d, want still 1 (nothing new stored)", parkStores)
	}
	if reopened.PullRequests[1].State != string(feature.StackPRStateClosed) {
		t.Errorf("layer 2 entry state = %q, want closed kept from the recorded entry (open never downgrades)", reopened.PullRequests[1].State)
	}

	if err := o.ReopenPullRequest("feat-closed-park", "repo-a", 2); err != nil {
		t.Fatalf("ReopenPullRequest: %v", err)
	}
	for _, call := range harness.remote.Calls {
		if call.Method == "ReopenPullRequest" {
			t.Fatalf("remote reopen call = %+v, want none for a live-open pull request", call)
		}
	}
	persisted := harness.reload(t)
	if record := persisted.RepoStates["repo-a"].Error; record != nil {
		t.Fatalf("stored record = %+v, want cleared by the reopen", record)
	}
	if entry := stackRepoEntryFor(persisted, 2, "repo-a"); entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("persisted layer 2 PR state = %q, want open recorded by the reopen", entry.PRState)
	}
}

// A repository whose every pull request reads open stores no record: the
// preflight parks nothing and the repository state carries no error.
func TestCompletionPreflightWithoutClosedPullRequestStoresNothing(t *testing.T) {
	t.Parallel()
	o, harness := newCompletionParkFixture(t)

	got := parkPreflightRepoResult(t, o)
	if got.Error != nil {
		t.Fatalf("repo result error = %+v, want none", got.Error)
	}
	if parkStores := harness.lc.parkStores; parkStores != 0 {
		t.Fatalf("park stores = %d, want none without a closed pull request", parkStores)
	}
	persisted := harness.reload(t)
	if record := persisted.RepoStates["repo-a"].Error; record != nil {
		t.Fatalf("stored record = %+v, want none", record)
	}
}

// When several layers read closed, the parked record names the lowest closed
// layer: with layers 2 and 3 closed — layer 1 open — the record names layer
// 2, its title, and its pull request.
func TestCompletionPreflightParksLowestClosedLayer(t *testing.T) {
	t.Parallel()
	o, harness := newCompletionParkFixture(t)
	harness.states[completionParkPRURLs[1]] = git.PRStateClosed
	harness.states[completionParkPRURLs[2]] = git.PRStateClosed

	got := parkPreflightRepoResult(t, o)
	assertClosedParkRecord(t, got.Error, 2, "Fix auth", completionParkPRURLs[1])

	persisted := harness.reload(t)
	assertClosedParkRecord(t, persisted.RepoStates["repo-a"].Error, 2, "Fix auth", completionParkPRURLs[1])
}
