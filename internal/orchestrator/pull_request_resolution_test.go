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

package orchestrator_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Closed pull-request resolution — fixtures
// ---------------------------------------------------------------------------

// The shared three-layer stack the reopen and recreate actions resolve:
// layer 1's pull request is open, layer 2's is recorded closed with the
// repository parked on the stored closed record, and layer 3 has no pull
// request yet. resolutionTestNewPR is the URL the mock CreatePR hands back
// for a recreated layer 2, and resolutionTestSiblingPR2 is the sibling
// repository's layer-2 pull request the cross-reference rewrites target.
// Every remote operation goes through the shared mock; the PR-body reads
// and writes the production helpers issue directly (cross-references, stack
// sections) route at the fake GitHub API.
const (
	resolutionTestFeatureID  = "feat-pr-resolution"
	resolutionTestPR1        = "https://github.com/org/r1/pull/1"
	resolutionTestPR2        = "https://github.com/org/r1/pull/2"
	resolutionTestNewPR      = "https://github.com/org/r1/pull/4"
	resolutionTestSiblingPR2 = "https://github.com/org/r2/pull/2"
)

var resolutionTestBranches = [3]string{
	"feature/stack-x/1-foundation",
	"feature/stack-x/2-fix-auth",
	"feature/stack-x/3-top-polish",
}

// resolutionTestClosedBody is the closed layer-2 pull request's body as the
// remote reports it: authored content plus every harness-owned section.
func resolutionTestClosedBody() string {
	return "## Summary\n\nFix the auth token refresh path.\n\n" +
		"## Stack\n\n" +
		"1. Foundation - [#1](https://github.com/org/r1/pull/1) (open)\n" +
		"2. Fix auth - [#2](https://github.com/org/r1/pull/2) (closed) (this pull request)\n" +
		"3. Top polish\n\n" +
		"## Related PRs\n\n" +
		"This PR is part of the multi-repo feature **\"pr resolution\"**.\n\n" +
		"| Repository | Branch | PR |\n" +
		"|------------|--------|----|\n" +
		"| r2 | " + resolutionTestBranches[1] + " | [#2](https://github.com/org/r2/pull/2) |\n\n" +
		"---\n\n" +
		"*Generated with [agentic orchestrator](https://github.com/doordash-oss/agentic-orchestrator)*"
}

// resolutionFixtureOpts configures the shared resolution fixture.
type resolutionFixtureOpts struct {
	// withSibling adds a second repository (r2) whose layer-2 pull request
	// is open, so the recreate cross-reference rewrite has a sibling.
	withSibling bool
	// rewriteLayer2 amends layer 2's branch so its tip differs from the
	// last-pushed SHA, making the recreate push lease observable.
	rewriteLayer2 bool
	// layer1MergedNoTip records layer 1 as merged with its tip cleared by
	// a rebase pass, so the recreate base falls back to the repository's
	// base branch.
	layer1MergedNoTip bool
	// draft sets the feature's DraftPublish checkpoint.
	draft bool
	// liveState is the live pull-request state every PRState lookup
	// answers; empty means indeterminate, so the action proceeds on the
	// recorded closed state.
	liveState string
	// reopenErr is the reopen remote operation's refusal.
	reopenErr error
	// pushErr is the layer push's refusal.
	pushErr error
	// createErr is the pull-request creation's refusal.
	createErr error
	// bodyReadErr makes the closed pull request's body read fail, forcing
	// the body-only description session.
	bodyReadErr bool
	// descriptionScript, when set, replaces the default description phase
	// runner with a scripted one and exposes the session count.
	descriptionScript func(callIndex int) (output string, permissionFailure bool)
}

// resolutionTestFixture is the shared reopen/recreate stack fixture.
type resolutionTestFixture struct {
	f    *feature.Feature
	lc   *mocks.MockFeatureLifecycle
	pub  *mocks.MockRemoteOps
	fake *testutil.FakeGitHubAPI
	o    *orchestrator.Orchestrator

	branches [3]string
	repoPath string
	// tips are the per-layer boundary tips the stack snapshot recorded.
	tips [3]string
	// rewritten2 is layer 2's amended tip when rewriteLayer2 is set.
	rewritten2 string

	layerEvents         []observe.LayerPublishEvent
	descriptionSessions *int
}

// newResolutionTestFixture builds the three-layer stack with layer 2 closed
// and the repository parked on the stored closed record, exactly as a
// publish pass that found the closed pull request leaves it.
func newResolutionTestFixture(t *testing.T, opts resolutionFixtureOpts) *resolutionTestFixture {
	t.Helper()
	branches := resolutionTestBranches
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	fx := &resolutionTestFixture{branches: branches, repoPath: repoPath, tips: tips}

	tipsByRepo := map[string][3]string{"r1": tips}
	repos := []feature.FeatureRepo{
		{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
	}
	if opts.withSibling {
		siblingPath, siblingTips := newStackedPublishRepo(t, branches, true)
		tipsByRepo["r2"] = siblingTips
		repos = append(repos, feature.FeatureRepo{
			Name: "r2", Path: siblingPath, WorktreePath: siblingPath, Branch: branches[2], BaseBranch: mainBranch,
		})
	}

	stack := threeLayerStack(branches, tipsByRepo)
	layer1 := feature.StackRepoEntry{
		TipSHA: tips[0], PRURL: resolutionTestPR1,
		PRState: feature.StackPRStateOpen, LastPushedSHA: tips[0],
	}
	if opts.layer1MergedNoTip {
		layer1 = feature.StackRepoEntry{
			PRURL: resolutionTestPR1, PRState: feature.StackPRStateMerged, LastPushedSHA: tips[0],
		}
	}
	stack[0].Repos["r1"] = layer1

	layer2Tip := tips[1]
	if opts.rewriteLayer2 {
		runPublishGit(t, repoPath, "checkout", branches[1])
		runPublishGit(t, repoPath, "commit", "--amend", "-m", "layer two rewritten")
		layer2Tip = runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
		runPublishGit(t, repoPath, "checkout", branches[2])
		fx.rewritten2 = layer2Tip
	}
	stack[1].Repos["r1"] = feature.StackRepoEntry{
		TipSHA: layer2Tip, PRURL: resolutionTestPR2,
		PRState: feature.StackPRStateClosed, LastPushedSHA: tips[1],
	}
	if opts.withSibling {
		stack[1].Repos["r2"] = feature.StackRepoEntry{
			TipSHA: tipsByRepo["r2"][1], PRURL: resolutionTestSiblingPR2,
			PRState: feature.StackPRStateOpen, LastPushedSHA: tipsByRepo["r2"][1],
		}
	}

	f := &feature.Feature{
		ID:          resolutionTestFeatureID,
		Name:        "pr resolution",
		Slug:        "pr-resolution",
		Status:      feature.StatusReviewPassed,
		Checkpoints: feature.Checkpoints{DraftPublish: opts.draft},
		Stack:       stack,
		Repos:       repos,
		RepoStates:  map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	if opts.withSibling {
		f.RepoStates["r2"] = &feature.RepoState{Touched: true}
	}

	// The repository is parked on the stored closed record, as the publish
	// walk's closed-layer refusal stores it.
	closedRecord, ok := orchestrator.StackClosedConflictRecord(&orchestrator.PublishStackClosedError{
		RepoName: "r1", Branch: branches[1], LayerPosition: 2,
		LayerTitle: "Fix auth", PRURL: resolutionTestPR2, State: git.PRStateClosed,
	})
	if !ok {
		t.Fatalf("StackClosedConflictRecord did not classify the closed error")
	}
	stored := closedRecord
	f.RepoStates["r1"].Error = &stored
	fx.f = f

	lc := resolutionTestLifecycle(f)
	fake := installStackPublishFakeGitHub(t)
	fake.HandleJSON("/repos/org/r1/pulls/4", 200, `{"body":"","state":"open"}`)

	pub := mocks.NewMockRemoteOps()
	if opts.liveState != "" {
		pub.PRStateFn = func(repoPath, prURL string) (string, error) {
			return opts.liveState, nil
		}
	}
	pub.ReopenPullRequestFn = func(repoPath, branch, prURL string) error {
		return opts.reopenErr
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		if opts.pushErr != nil {
			return "", opts.pushErr
		}
		return localSHA, nil
	}
	pub.GetPRBodyFn = func(prURL string) (string, error) {
		if opts.bodyReadErr {
			return "", errors.New("GET /repos/org/r1/pulls/2: 502 Bad Gateway")
		}
		return resolutionTestClosedBody(), nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if opts.createErr != nil {
			return "", opts.createErr
		}
		return resolutionTestNewPR, nil
	}

	deps := orchestrator.Deps{Lifecycle: lc, Store: newFeatureStore(f), Remote: pub}
	if opts.descriptionScript != nil {
		scripted, sessions := newScriptedDescriptionPhaseRunner(t, opts.descriptionScript)
		deps.PhaseRunner = scripted
		fx.descriptionSessions = sessions
	} else {
		deps.PhaseRunner = newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false)
	}
	fx.lc, fx.pub, fx.fake = lc, pub, fake
	fx.o = orchestrator.New(deps, orchestrator.Hooks{
		OnStackLayerPublished: func(featureID string, outcome observe.LayerPublishEvent) {
			fx.layerEvents = append(fx.layerEvents, outcome)
		},
	})
	return fx
}

// resolutionTestLifecycle wires a MockFeatureLifecycle that mirrors the real
// manager's stack writes — including the bookkeeping every per-layer publish
// write shares: the repository's stored publish record is cleared — so the
// actions' record-clearing behavior observes the state the production
// lifecycle would persist.
func resolutionTestLifecycle(f *feature.Feature) *mocks.MockFeatureLifecycle {
	lc := lifecycleForFeature(f)
	writeEntry := func(repoName string, layerPosition int, mutate func(*feature.StackRepoEntry)) {
		testModifyStackEntry(f, repoName, layerPosition, mutate)
		if state := f.RepoStates[repoName]; state != nil {
			state.Error = nil
		}
	}
	lc.RecordStackLayerPRFn = func(id, repo string, layerPosition int, prURL, pushedSHA string) error {
		writeEntry(repo, layerPosition, func(e *feature.StackRepoEntry) {
			e.PRURL = prURL
			e.PRState = feature.StackPRStateOpen
			e.LastPushedSHA = pushedSHA
		})
		return nil
	}
	lc.RecordStackLayerPushedSHAFn = func(id, repo string, layerPosition int, sha string) error {
		writeEntry(repo, layerPosition, func(e *feature.StackRepoEntry) { e.LastPushedSHA = sha })
		return nil
	}
	lc.SetStackLayerPRStateFn = func(id, repo string, layerPosition int, state feature.StackPRState) error {
		writeEntry(repo, layerPosition, func(e *feature.StackRepoEntry) { e.PRState = state })
		return nil
	}
	lc.MarkStackLayerNoCommitsFn = func(id, repo string, layerPosition int) error {
		writeEntry(repo, layerPosition, func(e *feature.StackRepoEntry) { e.NoCommits = true })
		return nil
	}
	lc.SetRepoPublishedFn = func(id, repo string) error {
		if state := f.RepoStates[repo]; state != nil {
			state.Touched = true
			state.Error = nil
		}
		return nil
	}
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error {
		if state := f.RepoStates[repo]; state != nil {
			stored := record
			state.Error = &stored
		}
		return nil
	}
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	return lc
}

// layer2Entry returns r1's layer-2 stack entry.
func (fx *resolutionTestFixture) layer2Entry() feature.StackRepoEntry {
	return fx.f.Stack[1].Repos["r1"]
}

// storedRecord returns r1's stored publish-failure record, or nil when the
// repository is unparked.
func (fx *resolutionTestFixture) storedRecord() *errcat.FailureRecord {
	return fx.f.RepoStates["r1"].Error
}

// statusEvents drains the orchestrator's events and returns r1's repository
// status events.
func (fx *resolutionTestFixture) statusEvents() []ports.Event {
	var out []ports.Event
	for _, ev := range drainEvents(fx.o) {
		if ev.Type == ports.RepoStatusChanged && ev.RepoName == "r1" {
			out = append(out, ev)
		}
	}
	return out
}

// resolutionTestFakeRequestCount counts fake GitHub requests whose logged
// line contains every given substring.
func resolutionTestFakeRequestCount(fake *testutil.FakeGitHubAPI, substrs ...string) int {
	count := 0
	for _, line := range fake.Requests() {
		matches := true
		for _, substr := range substrs {
			if !strings.Contains(line, substr) {
				matches = false
				break
			}
		}
		if matches {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Reopen — Orchestrator.ReopenPullRequest
// ---------------------------------------------------------------------------

// Layer 2 recorded closed and the repository parked on the closed record:
// Reopen calls the reopen remote operation with layer 2's branch and pull
// request, records the entry open, clears the stored record, refreshes the
// stack section across the repository's open pull requests, and emits one
// repository status event naming layer 2 and one per-layer publish event
// whose action is "reopened".
func TestOrchestrator_ReopenPullRequest_ClosedLayerReopensRecordsOpenAndClearsRecord(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{})

	if err := fx.o.ReopenPullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("ReopenPullRequest: %v", err)
	}

	reopened := stackRemoteCalls(fx.pub, "ReopenPullRequest")
	if len(reopened) != 1 {
		t.Fatalf("ReopenPullRequest remote calls = %d, want exactly 1", len(reopened))
	}
	if args := reopened[0].Args; args[0] != fx.repoPath || args[1] != fx.branches[1] || args[2] != resolutionTestPR2 {
		t.Fatalf("reopen call args = %v, want repo path %s, layer 2 branch %s, pull request %s",
			args, fx.repoPath, fx.branches[1], resolutionTestPR2)
	}

	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 2 entry = %+v, want the pull request recorded open", entry)
	}
	if record := fx.storedRecord(); record != nil {
		t.Fatalf("stored record = %+v, want the closed record cleared", record)
	}

	// The stack-section update covers the repository's open pull requests:
	// layers 1 and 2 both carry one after the reopen.
	if got := resolutionTestFakeRequestCount(fx.fake, "## Stack"); got != 2 {
		t.Fatalf("stack-section body writes = %d, want 2 (every open pull request of r1)", got)
	}
	if got := resolutionTestFakeRequestCount(fx.fake, "## Stack", resolutionTestPR2); got < 1 {
		t.Fatalf("stack-section writes naming layer 2's pull request = %d, want at least 1", got)
	}

	statusEvents := fx.statusEvents()
	if len(statusEvents) != 1 || statusEvents[0].LayerPosition != 2 {
		t.Fatalf("repository status events = %+v, want exactly one naming layer 2", statusEvents)
	}
	if len(fx.layerEvents) != 1 {
		t.Fatalf("layer publish events = %d, want exactly 1", len(fx.layerEvents))
	}
	event := fx.layerEvents[0]
	if event.Action != observe.LayerPublishActionReopened || string(event.Action) != "reopened" {
		t.Fatalf("layer publish action = %q, want reopened", event.Action)
	}
	if event.Repository != "r1" || event.Position != 2 || event.PRURL != resolutionTestPR2 {
		t.Fatalf("layer publish event = %+v, want r1's layer 2 and its pull request", event)
	}
}

// A live-open pull request is recorded open — which clears the stored
// record — without any reopen remote write.
func TestOrchestrator_ReopenPullRequest_LiveOpenRecordsOpenWithoutRemoteWrite(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{liveState: git.PRStateOpen})

	if err := fx.o.ReopenPullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("ReopenPullRequest: %v", err)
	}
	if got := len(stackRemoteCalls(fx.pub, "ReopenPullRequest")); got != 0 {
		t.Fatalf("ReopenPullRequest remote calls = %d, want 0 (the live state already reads open)", got)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 2 entry = %+v, want the pull request recorded open", entry)
	}
	if record := fx.storedRecord(); record != nil {
		t.Fatalf("stored record = %+v, want the closed record cleared", record)
	}
}

// A live-merged pull request is recorded merged and the stored record is
// cleared the same way.
func TestOrchestrator_ReopenPullRequest_LiveMergedRecordsMergedAndClearsRecord(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{liveState: git.PRStateMerged})

	if err := fx.o.ReopenPullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("ReopenPullRequest: %v", err)
	}
	if got := len(stackRemoteCalls(fx.pub, "ReopenPullRequest")); got != 0 {
		t.Fatalf("ReopenPullRequest remote calls = %d, want 0 (the live state already reads merged)", got)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateMerged {
		t.Fatalf("layer 2 entry = %+v, want the pull request recorded merged", entry)
	}
	if record := fx.storedRecord(); record != nil {
		t.Fatalf("stored record = %+v, want the closed record cleared", record)
	}
}

// A generic reopen refusal stores the reopen-failed record naming layer 2
// and its pull request, and leaves the entry closed.
func TestOrchestrator_ReopenPullRequest_RemoteRefusalStoresReopenFailedRecord(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{
		reopenErr: errors.New("PATCH /repos/org/r1/pulls/2: 403 Forbidden"),
	})

	err := fx.o.ReopenPullRequest(fx.f.ID, "r1", 2)
	var reopenFailed *orchestrator.PublishReopenFailedError
	if !errors.As(err, &reopenFailed) {
		t.Fatalf("ReopenPullRequest error = %T %v; want PublishReopenFailedError", err, err)
	}
	if reopenFailed.RepoName != "r1" || reopenFailed.LayerPosition != 2 ||
		reopenFailed.LayerTitle != "Fix auth" || reopenFailed.PRURL != resolutionTestPR2 {
		t.Fatalf("PublishReopenFailedError = %+v, want r1's layer 2 (Fix auth) and its pull request", reopenFailed)
	}

	record := fx.storedRecord()
	if record == nil || record.Code != errcat.PublishReopenFailed {
		t.Fatalf("stored record = %+v, want publish_reopen_failed", record)
	}
	repo := record.Context.Repositories[0]
	if repo.Name != "r1" || repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" ||
		repo.PullRequestURL != resolutionTestPR2 {
		t.Fatalf("stored record block = %+v, want the repository, layer 2, and the pull request named", repo)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the pull request left closed", entry)
	}
}

// A reopen refusal carrying the typed head-branch-missing error stores the
// head-branch-missing record naming layer 2 and its pull request, and
// leaves the entry closed.
func TestOrchestrator_ReopenPullRequest_HeadBranchMissingStoresHeadBranchMissingRecord(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{
		reopenErr: fmt.Errorf("ls-remote origin %s: %w", resolutionTestBranches[1], git.ErrPRHeadBranchMissing),
	})

	err := fx.o.ReopenPullRequest(fx.f.ID, "r1", 2)
	var headMissing *orchestrator.PublishHeadBranchMissingError
	if !errors.As(err, &headMissing) {
		t.Fatalf("ReopenPullRequest error = %T %v; want PublishHeadBranchMissingError", err, err)
	}
	if headMissing.RepoName != "r1" || headMissing.LayerPosition != 2 ||
		headMissing.LayerTitle != "Fix auth" || headMissing.PRURL != resolutionTestPR2 {
		t.Fatalf("PublishHeadBranchMissingError = %+v, want r1's layer 2 (Fix auth) and its pull request", headMissing)
	}

	record := fx.storedRecord()
	if record == nil || record.Code != errcat.PublishHeadBranchMissing {
		t.Fatalf("stored record = %+v, want publish_head_branch_missing", record)
	}
	repo := record.Context.Repositories[0]
	if repo.Name != "r1" || repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" ||
		repo.PullRequestURL != resolutionTestPR2 {
		t.Fatalf("stored record block = %+v, want the repository, layer 2, and the pull request named", repo)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the pull request left closed", entry)
	}
}

// Unknown repository, local-only repository, unknown layer, and a layer
// without a recorded pull request are all rejected before any remote call.
func TestOrchestrator_ReopenPullRequest_ValidationRejectionsPrecedeRemoteCalls(t *testing.T) {
	unpublishable := false
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{})
	fx.f.Repos = append(fx.f.Repos, feature.FeatureRepo{
		Name: "rlocal", Path: "/tmp/rlocal", WorktreePath: "/tmp/wt-rlocal",
		Branch: "feature/local-only", BaseBranch: mainBranch, Publishable: &unpublishable,
	})

	cases := []struct {
		name    string
		repo    string
		layer   int
		wantMsg string
	}{
		{"unknown repository", "ghost", 2, `unknown repository "ghost"`},
		{"local-only repository", "rlocal", 2, "is local-only"},
		{"unknown layer", "r1", 9, "no stack layer at position 9"},
		{"layer without a recorded pull request", "r1", 3, "no recorded pull request"},
	}
	for _, tc := range cases {
		err := fx.o.ReopenPullRequest(fx.f.ID, tc.repo, tc.layer)
		if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: ReopenPullRequest error = %v, want a rejection mentioning %q", tc.name, err, tc.wantMsg)
		}
	}
	if got := len(fx.pub.Calls); got != 0 {
		t.Fatalf("remote calls = %+v, want none before validation passes", fx.pub.Calls)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the pull request left closed", entry)
	}
	if fx.storedRecord() == nil {
		t.Fatal("stored record cleared by a rejected reopen; want the closed record kept")
	}
}

// ---------------------------------------------------------------------------
// Recreate — Orchestrator.RecreatePullRequest
// ---------------------------------------------------------------------------

// Layer 2 closed with a rewritten branch in a two-repository stack: Recreate
// pushes layer 2 with its last-pushed SHA as the lease, creates a pull
// request with the table title, layer 1's branch as base, the draft
// checkpoint, and the closed pull request's body with the harness sections
// stripped, records the new URL as open with the pushed SHA, rewires the
// sibling repository's cross-reference, re-injects stack sections into every
// open pull request of the repository, emits "recreated", and leaves the old
// closed pull request untouched.
func TestOrchestrator_RecreatePullRequest_ClosedLayerPushesCreatesAndRewiresSibling(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{
		withSibling:   true,
		rewriteLayer2: true,
		draft:         true,
	})

	if err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("RecreatePullRequest: %v", err)
	}

	pushes := stackRemoteCalls(fx.pub, "PushLayerBranch")
	if len(pushes) != 1 {
		t.Fatalf("PushLayerBranch calls = %d, want exactly 1 (layer 2)", len(pushes))
	}
	if args := pushes[0].Args; args[0] != fx.repoPath || args[1] != fx.branches[1] ||
		args[2] != fx.rewritten2 || args[3] != fx.tips[1] {
		t.Fatalf("layer 2 push args = %v, want branch %s local %s lease %s (the last-pushed SHA)",
			args, fx.branches[1], fx.rewritten2, fx.tips[1])
	}

	created := stackRemoteCalls(fx.pub, "CreatePR")
	if len(created) != 1 {
		t.Fatalf("CreatePR calls = %d, want exactly 1", len(created))
	}
	args := created[0].Args
	if args[1] != fx.branches[1] {
		t.Fatalf("CreatePR branch = %v, want layer 2's branch %s", args[1], fx.branches[1])
	}
	if args[2] != "Fix auth" {
		t.Fatalf("CreatePR title = %v, want the layer table title %q", args[2], "Fix auth")
	}
	body, _ := args[3].(string)
	if want := git.StripHarnessSections(resolutionTestClosedBody()); body != want {
		t.Fatalf("CreatePR body = %q, want the closed body with the harness sections stripped", body)
	}
	if !strings.Contains(body, "Fix the auth token refresh path.") {
		t.Fatalf("CreatePR body = %q, want the closed pull request's authored content kept", body)
	}
	for _, banned := range []string{git.StackSectionHeader, git.CrossRefSectionHeader, git.PRSignature} {
		if strings.Contains(body, banned) {
			t.Fatalf("CreatePR body = %q, still carries the harness-owned %q section", body, banned)
		}
	}
	if args[4] != fx.branches[0] {
		t.Fatalf("CreatePR base = %v, want layer 1's branch %s", args[4], fx.branches[0])
	}
	if args[5] != true {
		t.Fatalf("CreatePR draft = %v, want the feature's DraftPublish checkpoint", args[5])
	}

	assertLifecycleCallArgs(t, fx.lc, "RecordStackLayerPR", "r1", 2, resolutionTestNewPR, fx.rewritten2)
	entry := fx.layer2Entry()
	if entry.PRURL != resolutionTestNewPR || entry.PRState != feature.StackPRStateOpen ||
		entry.LastPushedSHA != fx.rewritten2 {
		t.Fatalf("layer 2 entry = %+v, want the new pull request recorded open with the pushed SHA", entry)
	}
	if record := fx.storedRecord(); record != nil {
		t.Fatalf("stored record = %+v, want the closed record cleared", record)
	}
	assertLifecycleCall(t, fx.lc, "SetRepoPublished")

	// The sibling repository's layer-2 pull request learns the new URL.
	if got := resolutionTestFakeRequestCount(fx.fake, "/repos/org/r2/pulls/2", resolutionTestNewPR); got < 1 {
		t.Fatalf("sibling cross-reference writes naming the new pull request = %d, want at least 1", got)
	}
	// The new pull request carries the related-PRs section.
	if got := resolutionTestFakeRequestCount(fx.fake, "/repos/org/r1/pulls/4", "## Related PRs"); got < 1 {
		t.Fatalf("new pull request cross-reference writes = %d, want at least 1", got)
	}
	// Stack sections cover every open pull request of the repository —
	// layers 1 and 2 after the recreation.
	if got := resolutionTestFakeRequestCount(fx.fake, "## Stack"); got != 2 {
		t.Fatalf("stack-section body writes = %d, want 2 (every open pull request of r1)", got)
	}
	if got := resolutionTestFakeRequestCount(fx.fake, "## Stack", resolutionTestNewPR); got < 1 {
		t.Fatalf("stack-section writes naming the new pull request = %d, want at least 1", got)
	}
	// The old closed pull request is left untouched: no request reaches it.
	if got := resolutionTestFakeRequestCount(fx.fake, "/repos/org/r1/pulls/2"); got != 0 {
		t.Fatalf("requests to the old closed pull request = %d, want 0 (it is left untouched)", got)
	}

	statusEvents := fx.statusEvents()
	if len(statusEvents) != 1 || statusEvents[0].LayerPosition != 2 {
		t.Fatalf("repository status events = %+v, want exactly one naming layer 2", statusEvents)
	}
	if len(fx.layerEvents) != 1 {
		t.Fatalf("layer publish events = %d, want exactly 1", len(fx.layerEvents))
	}
	event := fx.layerEvents[0]
	if event.Action != observe.LayerPublishActionRecreated || string(event.Action) != "recreated" {
		t.Fatalf("layer publish action = %q, want recreated", event.Action)
	}
	if event.Repository != "r1" || event.Position != 2 || event.PRURL != resolutionTestNewPR {
		t.Fatalf("layer publish event = %+v, want r1's layer 2 and the new pull request", event)
	}
}

// A failed body read runs the body-only description session exactly once and
// uses its body for the recreated pull request.
func TestOrchestrator_RecreatePullRequest_BodyReadFailureRunsDescriptionSessionOnce(t *testing.T) {
	opts := resolutionFixtureOpts{rewriteLayer2: true, bodyReadErr: true}
	opts.descriptionScript = func(callIndex int) (string, bool) {
		return "## Summary\n\nGenerated session body", false
	}
	fx := newResolutionTestFixture(t, opts)

	if err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("RecreatePullRequest: %v", err)
	}
	if got := *fx.descriptionSessions; got != 1 {
		t.Fatalf("description sessions = %d, want exactly 1 (the body-only fallback)", got)
	}

	created := stackRemoteCalls(fx.pub, "CreatePR")
	if len(created) != 1 {
		t.Fatalf("CreatePR calls = %d, want exactly 1", len(created))
	}
	if body, _ := created[0].Args[3].(string); body != "## Summary\n\nGenerated session body" {
		t.Fatalf("CreatePR body = %q, want the description session's body", body)
	}

	pushes := stackRemoteCalls(fx.pub, "PushLayerBranch")
	if len(pushes) != 1 || pushes[0].Args[3] != fx.tips[1] {
		t.Fatalf("layer 2 push calls = %+v, want exactly one carrying the last-pushed SHA as the lease", pushes)
	}
}

// A lower layer merged with its tip cleared by a rebase pass is not a base
// candidate: the recreated pull request bases on the repository's base
// branch.
func TestOrchestrator_RecreatePullRequest_BaseFallsBackToRepoBaseWhenLowerLayerMergedWithoutTip(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{layer1MergedNoTip: true})

	if err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2); err != nil {
		t.Fatalf("RecreatePullRequest: %v", err)
	}
	created := stackRemoteCalls(fx.pub, "CreatePR")
	if len(created) != 1 {
		t.Fatalf("CreatePR calls = %d, want exactly 1", len(created))
	}
	if base := created[0].Args[4]; base != mainBranch {
		t.Fatalf("CreatePR base = %v, want the repository base branch %q (layer 1 merged with no tip)", base, mainBranch)
	}
}

// A diverged push stores the existing remote-diverged record and creates no
// pull request.
func TestOrchestrator_RecreatePullRequest_DivergedPushStoresRemoteDivergedAndCreatesNothing(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{
		pushErr: &git.RewritePushError{
			Kind: git.RewritePushRemoteDiverged, Branch: resolutionTestBranches[1], RemoteOnlyCommits: 2,
		},
	})

	err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2)
	var diverged *orchestrator.PublishRemoteDivergedError
	if !errors.As(err, &diverged) {
		t.Fatalf("RecreatePullRequest error = %T %v; want PublishRemoteDivergedError", err, err)
	}
	if diverged.LayerPosition != 2 || diverged.LayerTitle != "Fix auth" || diverged.RemoteOnlyCommits != 2 {
		t.Fatalf("PublishRemoteDivergedError = %+v, want layer 2 (Fix auth) with 2 remote-only commits", diverged)
	}

	record := fx.storedRecord()
	if record == nil || record.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("stored record = %+v, want publish_remote_diverged", record)
	}
	if repo := record.Context.Repositories[0]; repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" {
		t.Fatalf("stored record block = %+v, want layer 2 named", repo)
	}
	if got := len(stackRemoteCalls(fx.pub, "CreatePR")); got != 0 {
		t.Fatalf("CreatePR calls = %d, want 0 after a refused push", got)
	}
	if entry := fx.layer2Entry(); entry.PRURL != resolutionTestPR2 || entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the closed pull request kept", entry)
	}
}

// A pull-request creation failure stores the recreate-failed record naming
// layer 2 and the closed pull request.
func TestOrchestrator_RecreatePullRequest_PRCreateFailureStoresRecreateFailedRecord(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{
		createErr: errors.New("POST /repos/org/r1/pulls: 502 Bad Gateway"),
	})

	err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2)
	var recreateFailed *orchestrator.PublishRecreateFailedError
	if !errors.As(err, &recreateFailed) {
		t.Fatalf("RecreatePullRequest error = %T %v; want PublishRecreateFailedError", err, err)
	}
	if recreateFailed.RepoName != "r1" || recreateFailed.LayerPosition != 2 ||
		recreateFailed.LayerTitle != "Fix auth" || recreateFailed.PRURL != resolutionTestPR2 {
		t.Fatalf("PublishRecreateFailedError = %+v, want r1's layer 2 (Fix auth) and the closed pull request", recreateFailed)
	}

	record := fx.storedRecord()
	if record == nil || record.Code != errcat.PublishRecreateFailed {
		t.Fatalf("stored record = %+v, want publish_recreate_failed", record)
	}
	repo := record.Context.Repositories[0]
	if repo.Name != "r1" || repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" ||
		repo.PullRequestURL != resolutionTestPR2 {
		t.Fatalf("stored record block = %+v, want the repository, layer 2, and the closed pull request named", repo)
	}
	if got := len(stackRemoteCalls(fx.pub, "PushLayerBranch")); got != 1 {
		t.Fatalf("PushLayerBranch calls = %d, want 1 (the push precedes the creation)", got)
	}
	if entry := fx.layer2Entry(); entry.PRURL != resolutionTestPR2 || entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the closed pull request kept", entry)
	}
}

// A live-open pull request is recorded open and refused as a moot conflict
// without any remote write.
func TestOrchestrator_RecreatePullRequest_LiveOpenPRIsMootConflictWithoutRemoteWrite(t *testing.T) {
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{liveState: git.PRStateOpen})

	err := fx.o.RecreatePullRequest(fx.f.ID, "r1", 2)
	var moot *orchestrator.PublishRecreateMootError
	if !errors.As(err, &moot) {
		t.Fatalf("RecreatePullRequest error = %T %v; want PublishRecreateMootError", err, err)
	}
	if moot.RepoName != "r1" || moot.LayerPosition != 2 || moot.State != git.PRStateOpen {
		t.Fatalf("PublishRecreateMootError = %+v, want r1's layer 2 reported open", moot)
	}
	for _, call := range fx.pub.Calls {
		if call.Method != "PRState" {
			t.Fatalf("remote write %v happened; a moot recreate must not write anything", call)
		}
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 2 entry = %+v, want the live open state recorded", entry)
	}
	if record := fx.storedRecord(); record != nil {
		t.Fatalf("stored record = %+v, want the closed record cleared by the state write", record)
	}
}

// The same validation rejections as reopen: unknown repository, local-only
// repository, unknown layer, and a layer without a recorded pull request are
// all rejected before any remote call.
func TestOrchestrator_RecreatePullRequest_ValidationRejectionsPrecedeRemoteCalls(t *testing.T) {
	unpublishable := false
	fx := newResolutionTestFixture(t, resolutionFixtureOpts{})
	fx.f.Repos = append(fx.f.Repos, feature.FeatureRepo{
		Name: "rlocal", Path: "/tmp/rlocal", WorktreePath: "/tmp/wt-rlocal",
		Branch: "feature/local-only", BaseBranch: mainBranch, Publishable: &unpublishable,
	})

	cases := []struct {
		name    string
		repo    string
		layer   int
		wantMsg string
	}{
		{"unknown repository", "ghost", 2, `unknown repository "ghost"`},
		{"local-only repository", "rlocal", 2, "is local-only"},
		{"unknown layer", "r1", 9, "no stack layer at position 9"},
		{"layer without a recorded pull request", "r1", 3, "no recorded pull request"},
	}
	for _, tc := range cases {
		err := fx.o.RecreatePullRequest(fx.f.ID, tc.repo, tc.layer)
		if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: RecreatePullRequest error = %v, want a rejection mentioning %q", tc.name, err, tc.wantMsg)
		}
	}
	if got := len(fx.pub.Calls); got != 0 {
		t.Fatalf("remote calls = %+v, want none before validation passes", fx.pub.Calls)
	}
	if entry := fx.layer2Entry(); entry.PRState != feature.StackPRStateClosed {
		t.Fatalf("layer 2 entry = %+v, want the pull request left closed", entry)
	}
	if fx.storedRecord() == nil {
		t.Fatal("stored record cleared by a rejected recreate; want the closed record kept")
	}
}
