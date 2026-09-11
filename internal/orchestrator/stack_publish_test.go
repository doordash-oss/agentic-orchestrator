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

package orchestrator_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Stack publish — fixtures
// ---------------------------------------------------------------------------

// newStackedPublishRepo builds a real git repository wired to a bare origin
// with three layer branches: layer 1 always commits, layer 2 commits only
// when layer2HasCommits is set, and layer 3 always commits. The worktree is
// left on layer 3's branch, matching the final layer boundary.
func newStackedPublishRepo(t *testing.T, branches [3]string, layer2HasCommits bool) (string, [3]string) {
	t.Helper()
	repoPath, _ := testutil.InitPublishReadyGitRepo(t)
	testutil.CreateBranch(t, repoPath, branches[0])
	tip1 := testutil.CommitFile(t, repoPath, "one.txt", "one\n", "layer one")
	testutil.CreateBranch(t, repoPath, branches[1])
	tip2 := tip1
	if layer2HasCommits {
		tip2 = testutil.CommitFile(t, repoPath, "two.txt", "two\n", "layer two")
	}
	testutil.CreateBranch(t, repoPath, branches[2])
	tip3 := testutil.CommitFile(t, repoPath, "three.txt", "three\n", "layer three")
	return repoPath, [3]string{tip1, tip2, tip3}
}

// threeLayerStack mirrors an approved three-row Pull Requests table with the
// layer boundary's per-repository tip snapshots recorded for layers 1 and 2;
// the top layer's tip is refreshed from HEAD during publish. tipsByRepo
// carries each repository's per-layer tips — a stack layer shares one branch
// name across repositories.
func threeLayerStack(branches [3]string, tipsByRepo map[string][3]string) []feature.StackLayer {
	layer := func(position int, title, slug string, phases int) feature.StackLayer {
		repos := make(map[string]feature.StackRepoEntry, len(tipsByRepo))
		for repoName, tips := range tipsByRepo {
			repos[repoName] = feature.StackRepoEntry{TipSHA: tips[position-1]}
		}
		return feature.StackLayer{
			Position: position, Title: title, Slug: slug, Phases: []int{phases},
			Branch: branches[position-1], Repos: repos,
		}
	}
	return []feature.StackLayer{
		layer(1, "Foundation", "foundation", 1),
		layer(2, "Fix auth", "fix-auth", 2),
		layer(3, "Top polish", "top-polish", 3),
	}
}

// stackPublishLifecycle wires a MockFeatureLifecycle that mirrors the real
// manager's stack writes against the shared feature object, so change
// detection, the all-published check, and the legacy URL projection observe
// the same state the production lifecycle would persist.
func stackPublishLifecycle(f *feature.Feature) *mocks.MockFeatureLifecycle {
	lc := lifecycleForFeature(f)
	lc.RecordStackLayerPRFn = func(id, repo string, layerPosition int, prURL, pushedSHA string) error {
		testModifyStackEntry(f, repo, layerPosition, func(e *feature.StackRepoEntry) {
			e.PRURL = prURL
			e.PRState = feature.StackPRStateOpen
			e.LastPushedSHA = pushedSHA
		})
		return nil
	}
	lc.RecordStackLayerPushedSHAFn = func(id, repo string, layerPosition int, sha string) error {
		testModifyStackEntry(f, repo, layerPosition, func(e *feature.StackRepoEntry) { e.LastPushedSHA = sha })
		return nil
	}
	lc.SetStackLayerPRStateFn = func(id, repo string, layerPosition int, state feature.StackPRState) error {
		testModifyStackEntry(f, repo, layerPosition, func(e *feature.StackRepoEntry) { e.PRState = state })
		return nil
	}
	lc.MarkStackLayerNoCommitsFn = func(id, repo string, layerPosition int) error {
		testModifyStackEntry(f, repo, layerPosition, func(e *feature.StackRepoEntry) { e.NoCommits = true })
		return nil
	}
	lc.SetRepoPublishedFn = func(id, repo string) error {
		if f.RepoStates == nil {
			f.RepoStates = make(map[string]*feature.RepoState)
		}
		st := f.RepoStates[repo]
		if st == nil {
			st = &feature.RepoState{}
			f.RepoStates[repo] = st
		}
		st.Touched = true
		st.Error = nil
		if url := testHighestLayerPRURL(f, repo); url != "" {
			st.PRURL = url
		}
		return nil
	}
	lc.SetRepoPublishErrorFn = func(id, repo string, record errcat.FailureRecord) error {
		if f.RepoStates == nil {
			return nil
		}
		if st := f.RepoStates[repo]; st != nil {
			stored := record
			st.Error = &stored
		}
		return nil
	}
	lc.TryCompletePublishFn = func(id string) (bool, error) {
		if !f.AllReposPublished() {
			return false, nil
		}
		f.Status = feature.StatusPublished
		return true, nil
	}
	return lc
}

func testModifyStackEntry(f *feature.Feature, repoName string, layerPosition int, mutate func(*feature.StackRepoEntry)) {
	for i := range f.Stack {
		if f.Stack[i].Position != layerPosition {
			continue
		}
		if f.Stack[i].Repos == nil {
			f.Stack[i].Repos = make(map[string]feature.StackRepoEntry)
		}
		entry := f.Stack[i].Repos[repoName]
		mutate(&entry)
		f.Stack[i].Repos[repoName] = entry
	}
}

func testHighestLayerPRURL(f *feature.Feature, repoName string) string {
	url, best := "", 0
	for _, layer := range f.Stack {
		if layer.Position > best {
			if entry, ok := layer.Repos[repoName]; ok && entry.PRURL != "" {
				url, best = entry.PRURL, layer.Position
			}
		}
	}
	return url
}

// newScriptedDescriptionPhaseRunner scripts the description session per call
// index (1-based, one session per layer per repository).
func newScriptedDescriptionPhaseRunner(t *testing.T, script func(callIndex int) (output string, permissionFailure bool)) (*agent.PhaseRunner, *int) {
	t.Helper()
	sessions := 0
	newSession := func() *publishDescriptionSessionHandle {
		sessions++
		output, permissionFailure := script(sessions)
		sess := newPublishDescriptionSessionHandle()
		if output != "" {
			sess.msgLog.Append(mocks.AssistantTextMessage(output))
			sess.result = &llm.ResultMessage{
				Type:       "result",
				Subtype:    "success",
				Result:     "done",
				StopReason: "end_turn",
			}
			sess.statusCh <- "SUCCESS"
		} else if permissionFailure {
			req := mocks.ControlRequestMsg("perm-1", "Bash").ControlRequest
			sess.lastControl = req
			sess.attachCh <- llm.SDKMessage{Type: "control_request", ControlRequest: req}
		}
		return sess
	}

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess := newSession()
		sess.id = id
		sess.featureID = featureID
		sess.phase = phase
		return sess, nil
	}

	pr := &agent.PhaseRunner{
		SessionManager: sm,
		StateDir:       t.TempDir(),
	}
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		return []string{"mock"}, nil, &ports.SessionOpts{RepoName: opts.RepoName}, nil
	}
	return pr, &sessions
}

func stackRemoteCalls(pub *mocks.MockRemoteOps, method string) []mocks.MockCall {
	var out []mocks.MockCall
	for _, c := range pub.Calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// installStackPublishFakeGitHub routes the PR-body read/update operations at
// a fake API so the stack-section and cross-ref rewrites stay hermetic. Pull
// requests answer open with an empty body.
func installStackPublishFakeGitHub(t *testing.T) *testutil.FakeGitHubAPI {
	t.Helper()
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/org/r1/pulls/1", 200, `{"body":"","state":"open"}`)
	fake.HandleJSON("/repos/org/r1/pulls/2", 200, `{"body":"","state":"open"}`)
	fake.HandleJSON("/repos/org/r1/pulls/3", 200, `{"body":"","state":"open"}`)
	fake.HandleJSON("/repos/org/r2/pulls/1", 200, `{"body":"","state":"open"}`)
	fake.HandleJSON("/repos/org/r2/pulls/2", 200, `{"body":"","state":"open"}`)
	fake.HandleJSON("/repos/org/r2/pulls/3", 200, `{"body":"","state":"open"}`)
	return fake
}

// ---------------------------------------------------------------------------
// Stack publish — the walk
// ---------------------------------------------------------------------------

// A three-layer stack where layer 2 has no commits in the repository: PRs
// are created for layers 1 and 3 in that order, layer 3 bases on layer 1's
// branch, both carry the checkpoint's draft flag, titles equal the table
// titles, bodies come from the scripted session, and layer 2's entry is
// marked empty with no push or PR call.
func TestPublishStackWalkCreatesLayerPRsInOrderWithEmptyMiddleLayer(t *testing.T) {
	branches := [3]string{"feature/stack-f/1-foundation", "feature/stack-f/2-fix-auth", "feature/stack-f/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, false)
	f := &feature.Feature{
		ID:          "feat-stack-walk",
		Name:        "stack walk",
		Slug:        "stack-walk",
		Status:      feature.StatusReviewPassed,
		Checkpoints: feature.Checkpoints{DraftPublish: true},
		Stack:       threeLayerStack(branches, map[string][3]string{"r1": tips}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	fake := installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushFn = func(path, branch string) error {
		t.Fatalf("stack publish must deliver through PushLayerBranch, not a plain push of %s", branch)
		return nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBranches, gotTitles, gotBases, gotBodies []string
	var gotDrafts []bool
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotBranches = append(gotBranches, branch)
		gotTitles = append(gotTitles, title)
		gotBases = append(gotBases, baseBranch)
		gotBodies = append(gotBodies, body)
		gotDrafts = append(gotDrafts, draft)
		switch branch {
		case branches[0]:
			return "https://github.com/org/r1/pull/1", nil
		case branches[2]:
			return "https://github.com/org/r1/pull/3", nil
		}
		t.Fatalf("unexpected CreatePR branch %q", branch)
		return "", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(gotBranches) != 2 || gotBranches[0] != branches[0] || gotBranches[1] != branches[2] {
		t.Fatalf("CreatePR branches = %v, want [%s %s] in order", gotBranches, branches[0], branches[2])
	}
	if len(gotTitles) != 2 || gotTitles[0] != "Foundation" || gotTitles[1] != "Top polish" {
		t.Fatalf("CreatePR titles = %v, want the table titles [Foundation Top polish]", gotTitles)
	}
	if len(gotBases) != 2 || gotBases[0] != mainBranch || gotBases[1] != branches[0] {
		t.Fatalf("CreatePR bases = %v, want [%s %s] — layer 3 bases on layer 1's branch", gotBases, mainBranch, branches[0])
	}
	for i, draft := range gotDrafts {
		if !draft {
			t.Errorf("CreatePR draft[%d] = false, want the checkpoint's DraftPublish flag", i)
		}
	}
	for i, body := range gotBodies {
		if body != "## Summary\n\nGenerated body" {
			t.Errorf("CreatePR body[%d] = %q, want the scripted session body", i, body)
		}
	}

	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 2 {
		t.Fatalf("PushLayerBranch calls = %d, want 2 (layers 1 and 3)", len(pushes))
	}
	if pushes[0].Args[1] != branches[0] || pushes[0].Args[2] != tips[0] || pushes[0].Args[3] != "" {
		t.Errorf("layer 1 push args = %v, want branch %s local %s lease empty", pushes[0].Args, branches[0], tips[0])
	}
	if pushes[1].Args[1] != branches[2] || pushes[1].Args[2] != tips[2] || pushes[1].Args[3] != "" {
		t.Errorf("layer 3 push args = %v, want branch %s local %s lease empty", pushes[1].Args, branches[2], tips[2])
	}

	entry2 := f.Stack[1].Repos["r1"]
	if !entry2.NoCommits || entry2.PRURL != "" {
		t.Fatalf("layer 2 entry = %+v, want the no-commits marker and no pull request", entry2)
	}
	assertLifecycleCallArgs(t, lc, "MarkStackLayerNoCommits", "r1", 2)

	entry1 := f.Stack[0].Repos["r1"]
	if entry1.PRURL != "https://github.com/org/r1/pull/1" || entry1.LastPushedSHA != tips[0] || entry1.PRState != feature.StackPRStateOpen {
		t.Fatalf("layer 1 entry = %+v, want the PR, pushed SHA, and open state", entry1)
	}
	entry3 := f.Stack[2].Repos["r1"]
	if entry3.PRURL != "https://github.com/org/r1/pull/3" || entry3.LastPushedSHA != tips[2] {
		t.Fatalf("layer 3 entry = %+v, want the PR and pushed SHA", entry3)
	}

	// The feature completes only when every layer is settled, and the legacy
	// per-repository URL equals the highest layer's PR.
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published (AllReposPublished wired through TryCompletePublish)", f.Status)
	}
	if got := f.RepoStates["r1"].PRURL; got != "https://github.com/org/r1/pull/3" {
		t.Fatalf("legacy per-repo PRURL = %q, want the highest layer's PR", got)
	}

	// One repository status event for the repository whose entries changed.
	statusEvents := 0
	for _, ev := range drainEvents(o) {
		if ev.Type == ports.RepoStatusChanged && ev.RepoName == "r1" {
			statusEvents++
		}
	}
	if statusEvents != 1 {
		t.Fatalf("RepoStatusChanged events for r1 = %d, want 1", statusEvents)
	}

	// The pass-end stack section lands on both open PRs.
	if got := fake.RequestCount("## Stack"); got < 2 {
		t.Errorf("stack-section body writes = %d, want the section injected into both open PRs", got)
	}
}

// assertLifecycleCallArgs asserts the mock lifecycle recorded a call to the
// named method with the given arguments after the leading feature ID.
func assertLifecycleCallArgs(t *testing.T, lc *mocks.MockFeatureLifecycle, method string, wantArgs ...any) {
	t.Helper()
	for _, c := range lc.Calls {
		if c.Method != method {
			continue
		}
		for i, want := range wantArgs {
			if i+1 >= len(c.Args) || c.Args[i+1] != want {
				t.Errorf("%s call args = %v, want %v at position %d", method, c.Args, want, i+1)
			}
		}
		return
	}
	t.Errorf("expected lifecycle call %q with args %v; got calls: %v", method, wantArgs, lifecycleCallNames(lc))
}

// Re-running publish after a failure injected at layer 2's PR creation
// pushes nothing for layer 1 (tip == LastPushedSHA → no push call), creates
// only layer 2's PR (and layer 3's), and records last-pushed SHAs.
func TestPublishStackRetryAfterPRCreateFailure(t *testing.T) {
	branches := [3]string{"feature/stack-r/1-foundation", "feature/stack-r/2-fix-auth", "feature/stack-r/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	f := &feature.Feature{
		ID:     "feat-stack-retry",
		Name:   "stack retry",
		Slug:   "stack-retry",
		Status: feature.StatusReviewPassed,
		Stack:  threeLayerStack(branches, map[string][3]string{"r1": tips}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	urlForBranch := map[string]string{
		branches[0]: "https://github.com/org/r1/pull/1",
		branches[1]: "https://github.com/org/r1/pull/2",
		branches[2]: "https://github.com/org/r1/pull/3",
	}
	failLayer2Create := true
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if branch == branches[1] && failLayer2Create {
			return "", errors.New("POST /repos/org/r1/pulls: 502 Bad Gateway")
		}
		return urlForBranch[branch], nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	// First pass: layer 1 lands, layer 2's PR creation fails.
	err := o.Publish(f.ID)
	var create *orchestrator.PublishPRCreateError
	if !errors.As(err, &create) {
		t.Fatalf("Publish error = %T %v; want PublishPRCreateError", err, err)
	}
	if create.LayerPosition != 2 || create.LayerTitle != "Fix auth" {
		t.Fatalf("PublishPRCreateError layer = %d (%q), want 2 (Fix auth)", create.LayerPosition, create.LayerTitle)
	}
	record := f.RepoStates["r1"].Error
	if record == nil || record.Code != errcat.PublishPullRequestFailed {
		t.Fatalf("stored record = %+v, want publish_pull_request_failed", record)
	}
	if record.Context.Repositories[0].LayerPosition != 2 || record.Context.Repositories[0].LayerTitle != "Fix auth" {
		t.Fatalf("stored record layer = %+v, want layer 2 named", record.Context.Repositories[0])
	}
	if got := len(stackRemoteCalls(pub, "PushLayerBranch")); got != 2 {
		t.Fatalf("first pass PushLayerBranch calls = %d, want 2 (layers 1 and 2 only)", got)
	}

	// Second pass: layer 1 is a no-op (open PR, tip == LastPushedSHA), so
	// only layer 3 is pushed; layers 2 and 3 get their PRs.
	failLayer2Create = false
	pub.Calls = nil
	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 1 || pushes[0].Args[1] != branches[2] {
		t.Fatalf("retry PushLayerBranch calls = %+v, want exactly one push for layer 3", pushes)
	}
	created := stackRemoteCalls(pub, "CreatePR")
	if len(created) != 2 || created[0].Args[1] != branches[1] || created[1].Args[1] != branches[2] {
		t.Fatalf("retry CreatePR calls = %+v, want layers 2 and 3 in order", created)
	}
	for pos, want := range map[int]string{1: tips[0], 2: tips[1], 3: tips[2]} {
		var layer feature.StackLayer
		for _, l := range f.Stack {
			if l.Position == pos {
				layer = l
			}
		}
		if entry := layer.Repos["r1"]; entry.LastPushedSHA != want {
			t.Errorf("layer %d LastPushedSHA = %q, want %q", pos, entry.LastPushedSHA, want)
		}
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published after the retry", f.Status)
	}
}

// A rewritten layer produces exactly one push carrying the previous
// last-pushed SHA as the lease anchor.
func TestPublishStackRewrittenLayerPushesOnceWithLease(t *testing.T) {
	branches := [3]string{"feature/stack-l/1-foundation", "feature/stack-l/2-fix-auth", "feature/stack-l/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	f := &feature.Feature{
		ID:     "feat-stack-lease",
		Name:   "stack lease",
		Slug:   "stack-lease",
		Status: feature.StatusReviewPassed,
		Stack:  threeLayerStack(branches, map[string][3]string{"r1": tips}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/" + map[string]string{
			branches[0]: "1", branches[1]: "2", branches[2]: "3",
		}[branch], nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("initial Publish: %v", err)
	}

	// Rewrite layer 1's branch (as a restack would) and record the new tip
	// while keeping the previously pushed SHA.
	runPublishGit(t, repoPath, "checkout", branches[0])
	runPublishGit(t, repoPath, "commit", "--amend", "-m", "layer one rewritten")
	rewritten := runPublishGitOutput(t, repoPath, "rev-parse", "HEAD")
	runPublishGit(t, repoPath, "checkout", branches[2])
	testModifyStackEntry(f, "r1", 1, func(e *feature.StackRepoEntry) { e.TipSHA = rewritten })

	pub.Calls = nil
	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("rewritten Publish: %v", err)
	}
	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 1 {
		t.Fatalf("PushLayerBranch calls = %d, want exactly one (the rewritten layer)", len(pushes))
	}
	if pushes[0].Args[1] != branches[0] || pushes[0].Args[2] != rewritten || pushes[0].Args[3] != tips[0] {
		t.Fatalf("rewritten push args = %v, want branch %s local %s lease %s", pushes[0].Args, branches[0], rewritten, tips[0])
	}
}

// A layer whose PR reads as merged is recorded merged with no push; a PR
// above it bases on the merged layer's branch.
func TestPublishStackMergedLayerRecordsStateAndBasesUpperPR(t *testing.T) {
	branches := [3]string{"feature/stack-m/1-foundation", "feature/stack-m/2-fix-auth", "feature/stack-m/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	stack := threeLayerStack(branches, map[string][3]string{"r1": tips})
	stack[0].Repos["r1"] = feature.StackRepoEntry{
		TipSHA:        tips[0],
		PRURL:         "https://github.com/org/r1/pull/1",
		LastPushedSHA: tips[0],
	}
	f := &feature.Feature{
		ID:     "feat-stack-merged",
		Name:   "stack merged",
		Slug:   "stack-merged",
		Status: feature.StatusReviewPassed,
		Stack:  stack,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		if prURL == "https://github.com/org/r1/pull/1" {
			return git.PRStateMerged, nil
		}
		return git.PRStateOpen, nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBases []string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotBases = append(gotBases, baseBranch)
		return "https://github.com/org/r1/pull/" + map[string]string{
			branches[1]: "2", branches[2]: "3",
		}[branch], nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if entry := f.Stack[0].Repos["r1"]; entry.PRState != feature.StackPRStateMerged {
		t.Fatalf("layer 1 PRState = %q, want merged", entry.PRState)
	}
	for _, push := range stackRemoteCalls(pub, "PushLayerBranch") {
		if push.Args[1] == branches[0] {
			t.Fatalf("merged layer was pushed: %+v; the branch must be left alone", push)
		}
	}
	if len(gotBases) != 2 || gotBases[0] != branches[0] || gotBases[1] != branches[1] {
		t.Fatalf("CreatePR bases = %v, want layer 2 on the merged layer's branch and layer 3 on layer 2's", gotBases)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}

// A layer whose PR reads as closed stores the closed-stack code naming the
// layer and PR and stops that repository while a second repository
// publishes fully.
func TestPublishStackClosedLayerStopsRepositoryOnly(t *testing.T) {
	// One stack, one set of shared layer branch names, two repositories.
	branches := [3]string{"feature/stack-c/1-foundation", "feature/stack-c/2-fix-auth", "feature/stack-c/3-top-polish"}
	repoA, tipsA := newStackedPublishRepo(t, branches, true)
	repoB, tipsB := newStackedPublishRepo(t, branches, true)

	stack := threeLayerStack(branches, map[string][3]string{"r1": tipsA, "r2": tipsB})
	stack[0].Repos["r1"] = feature.StackRepoEntry{
		TipSHA:        tipsA[0],
		PRURL:         "https://github.com/org/r1/pull/1",
		LastPushedSHA: tipsA[0],
	}

	f := &feature.Feature{
		ID:     "feat-stack-closed",
		Name:   "stack closed",
		Slug:   "stack-closed",
		Status: feature.StatusReviewPassed,
		Stack:  stack,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoA, WorktreePath: repoA, Branch: branches[2], BaseBranch: mainBranch},
			{Name: "r2", Path: repoB, WorktreePath: repoB, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
			"r2": {Touched: true},
		},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PRStateFn = func(repoPath, prURL string) (string, error) {
		if prURL == "https://github.com/org/r1/pull/1" {
			return git.PRStateClosed, nil
		}
		return git.PRStateOpen, nil
	}
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if repoPath == repoA {
			t.Fatalf("CreatePR called for the stopped repository (%s)", repoPath)
		}
		switch branch {
		case branches[0]:
			return "https://github.com/org/r2/pull/1", nil
		case branches[1]:
			return "https://github.com/org/r2/pull/2", nil
		case branches[2]:
			return "https://github.com/org/r2/pull/3", nil
		}
		t.Fatalf("unexpected CreatePR branch %q", branch)
		return "", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	err := o.Publish(f.ID)
	var closed *orchestrator.PublishStackClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("Publish error = %T %v; want PublishStackClosedError", err, err)
	}
	if closed.LayerPosition != 1 || closed.LayerTitle != "Foundation" || closed.PRURL != "https://github.com/org/r1/pull/1" {
		t.Fatalf("PublishStackClosedError = %+v, want layer 1 (Foundation) and the closed PR", closed)
	}

	record := f.RepoStates["r1"].Error
	if record == nil || record.Code != errcat.PublishStackPullRequestClosed {
		t.Fatalf("r1 stored record = %+v, want publish_stack_pull_request_closed", record)
	}
	repo := record.Context.Repositories[0]
	if repo.Name != "r1" || repo.LayerPosition != 1 || repo.LayerTitle != "Foundation" || repo.PullRequestURL != "https://github.com/org/r1/pull/1" {
		t.Fatalf("r1 record block = %+v, want the repository, layer, and PR named", repo)
	}
	for _, call := range pub.Calls {
		if call.Method == "PushLayerBranch" || call.Method == "CreatePR" {
			if call.Args[0] == repoA {
				t.Fatalf("%s called for the stopped repository: %+v", call.Method, call)
			}
		}
	}

	// The sibling repository published fully and owns no record.
	if f.RepoStates["r2"].Error != nil {
		t.Fatalf("r2 record = %+v, want none (the sibling published fully)", f.RepoStates["r2"].Error)
	}
	for pos, wantURL := range map[int]string{1: "pull/1", 2: "pull/2", 3: "pull/3"} {
		var layer feature.StackLayer
		for _, l := range f.Stack {
			if l.Position == pos {
				layer = l
			}
		}
		if entry := layer.Repos["r2"]; !strings.HasSuffix(entry.PRURL, wantURL) {
			t.Errorf("r2 layer %d PRURL = %q, want %s", pos, entry.PRURL, wantURL)
		}
	}
	// r1 is unsettled, so the feature-level publish cannot complete.
	if f.Status == feature.StatusPublished {
		t.Fatal("feature published despite the closed-stack repository")
	}
}

// A push failing with the diverged kind stores remote-diverged (rebase-pass
// remediation comes from the catalog) and stops the repository.
func TestPublishStackDivergedPushStoresRecordAndStopsRepository(t *testing.T) {
	branches := [3]string{"feature/stack-v/1-foundation", "feature/stack-v/2-fix-auth", "feature/stack-v/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	f := &feature.Feature{
		ID:     "feat-stack-diverged",
		Name:   "stack diverged",
		Slug:   "stack-diverged",
		Status: feature.StatusReviewPassed,
		Stack:  threeLayerStack(branches, map[string][3]string{"r1": tips}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return "", &git.RewritePushError{
			Kind:              git.RewritePushRemoteDiverged,
			Branch:            branch,
			RemoteOnlyCommits: 2,
		}
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		t.Fatal("CreatePR called; the diverged repository must stop at its first layer")
		return "", nil
	}

	pr, sessionCount := newScriptedDescriptionPhaseRunner(t, func(callIndex int) (string, bool) {
		return "## Summary\n\nGenerated body", false
	})
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	err := o.Publish(f.ID)
	var diverged *orchestrator.PublishRemoteDivergedError
	if !errors.As(err, &diverged) {
		t.Fatalf("Publish error = %T %v; want PublishRemoteDivergedError", err, err)
	}
	if diverged.LayerPosition != 1 || diverged.LayerTitle != "Foundation" {
		t.Fatalf("PublishRemoteDivergedError = %+v, want layer 1 (Foundation) named", diverged)
	}

	record := f.RepoStates["r1"].Error
	if record == nil || record.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("stored record = %+v, want publish_remote_diverged", record)
	}
	repo := record.Context.Repositories[0]
	if repo.LayerPosition != 1 || repo.LayerTitle != "Foundation" || repo.RemoteOnlyCommits != 2 {
		t.Fatalf("stored record block = %+v, want the layer and remote-only count", repo)
	}
	rendered := errcat.RenderRecord(*record)
	if rendered.Remediation == nil || !strings.Contains(rendered.Remediation.Hint, "rebase") {
		t.Fatalf("rendered remediation = %+v, want the catalog's rebase-pass remediation", rendered.Remediation)
	}
	// The repository stopped at its first layer: exactly one description
	// session ran and no PR was created.
	if got := *sessionCount; got != 1 {
		t.Fatalf("description sessions = %d, want 1 (the walk stops at the refused push)", got)
	}
}

// A run without a stack stores the no-stack code on every publishable
// repository and makes no remote call.
func TestPublishWithoutStackFailsClosedOnEveryRepository(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-no-stack",
		Name:   "no stack",
		Slug:   "no-stack",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: wtR1Path, Branch: "feature/no-stack", BaseBranch: mainBranch},
			{Name: "r2", Path: "/tmp/r2", WorktreePath: "/tmp/wt-r2", Branch: "feature/no-stack", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
			"r2": {Touched: true},
		},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		t.Fatal("PushLayerBranch called; a stackless run must make no remote call")
		return "", nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		t.Fatal("CreatePR called; a stackless run must make no remote call")
		return "", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	err := o.Publish(f.ID)
	var missing *orchestrator.PublishStackMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("Publish error = %T %v; want PublishStackMissingError", err, err)
	}
	for _, repo := range []string{"r1", "r2"} {
		record := f.RepoStates[repo].Error
		if record == nil || record.Code != errcat.PublishStackMissing {
			t.Fatalf("%s stored record = %+v, want publish_stack_missing", repo, record)
		}
	}
	if len(pub.Calls) != 0 {
		t.Fatalf("remote calls = %+v, want none", pub.Calls)
	}
	if f.Status == feature.StatusPublished {
		t.Fatal("feature published despite the missing stack")
	}
}

// A single-layer (single feature) stack produces one PR per touched
// repository with no stack section and no PR-body rewrite.
func TestPublishSingleLayerStackHasNoStackSection(t *testing.T) {
	repoPath := newPublishReadyBranch(t, "feature/single-layer")
	testutil.CommitFile(t, repoPath, "change.txt", "change\n", "publish change")
	f := &feature.Feature{
		ID:     "feat-single-layer",
		Name:   "single layer",
		Slug:   "single-layer",
		Status: feature.StatusReviewPassed,
		Stack:  singleLayerStack(1, "feature/single-layer"),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: "feature/single-layer", BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	fake := installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	var gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		gotBody = body
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if got := len(stackRemoteCalls(pub, "CreatePR")); got != 1 {
		t.Fatalf("CreatePR calls = %d, want exactly one", got)
	}
	if gotBody != "## Summary\n\nGenerated body" {
		t.Fatalf("CreatePR body = %q, want the session body with no stack section", gotBody)
	}
	if got := fake.RequestCount("pulls"); got != 0 {
		t.Fatalf("PR-body requests = %d, want none (no stack section for a single layer)", got)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published", f.Status)
	}
}

// The description-failed record names the layer, nothing is pushed for that
// layer, and retrying resumes there.
func TestPublishStackDescriptionFailureNamesLayerAndRetryResumes(t *testing.T) {
	branches := [3]string{"feature/stack-df/1-foundation", "feature/stack-df/2-fix-auth", "feature/stack-df/3-top-polish"}
	repoPath, tips := newStackedPublishRepo(t, branches, true)
	f := &feature.Feature{
		ID:     "feat-stack-desc-fail",
		Name:   "stack desc fail",
		Slug:   "stack-desc-fail",
		Status: feature.StatusReviewPassed,
		Stack:  threeLayerStack(branches, map[string][3]string{"r1": tips}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoPath, WorktreePath: repoPath, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{"r1": {Touched: true}},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		return "https://github.com/org/r1/pull/" + map[string]string{
			branches[0]: "1", branches[1]: "2", branches[2]: "3",
		}[branch], nil
	}

	failSecondSession := true
	pr, _ := newScriptedDescriptionPhaseRunner(t, func(callIndex int) (string, bool) {
		if callIndex == 2 && failSecondSession {
			return "", true
		}
		return "## Summary\n\nGenerated body", false
	})
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	// First pass: layer 1 lands; layer 2's description session fails before
	// anything is pushed for it.
	err := o.Publish(f.ID)
	var described *orchestrator.PublishDescriptionError
	if !errors.As(err, &described) {
		t.Fatalf("Publish error = %T %v; want PublishDescriptionError", err, err)
	}
	if described.LayerPosition != 2 || described.LayerTitle != "Fix auth" {
		t.Fatalf("PublishDescriptionError layer = %d (%q), want 2 (Fix auth)", described.LayerPosition, described.LayerTitle)
	}
	record := f.RepoStates["r1"].Error
	if record == nil || record.Code != errcat.PublishDescriptionFailed {
		t.Fatalf("stored record = %+v, want publish_description_failed", record)
	}
	if repo := record.Context.Repositories[0]; repo.LayerPosition != 2 || repo.LayerTitle != "Fix auth" {
		t.Fatalf("stored record layer = %+v, want layer 2 named", repo)
	}
	pushes := stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 1 || pushes[0].Args[1] != branches[0] {
		t.Fatalf("first pass PushLayerBranch calls = %+v, want only layer 1 (nothing pushed for the failed layer)", pushes)
	}

	// Retry: layer 1 is a no-op, layer 2's session reruns and its PR is
	// created, then layer 3 completes the stack.
	failSecondSession = false
	pub.Calls = nil
	if err := o.Publish(f.ID); err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	pushes = stackRemoteCalls(pub, "PushLayerBranch")
	if len(pushes) != 2 || pushes[0].Args[1] != branches[1] || pushes[1].Args[1] != branches[2] {
		t.Fatalf("retry PushLayerBranch calls = %+v, want layers 2 and 3 only", pushes)
	}
	if got := len(stackRemoteCalls(pub, "CreatePR")); got != 2 {
		t.Fatalf("retry CreatePR calls = %d, want 2 (layers 2 and 3)", got)
	}
	if f.Status != feature.StatusPublished {
		t.Fatalf("feature status = %s, want Published after the retry", f.Status)
	}
}

// Repository selection still limits the walk: an explicit Repos list walks
// only the selected touched repository.
func TestPublishStackRepoSelectionLimitsWalk(t *testing.T) {
	branches := [3]string{"feature/stack-s/1-foundation", "feature/stack-s/2-fix-auth", "feature/stack-s/3-top-polish"}
	repoA, tipsA := newStackedPublishRepo(t, branches, true)
	repoB, tipsB := newStackedPublishRepo(t, branches, true)

	f := &feature.Feature{
		ID:     "feat-stack-selection",
		Name:   "stack selection",
		Slug:   "stack-selection",
		Status: feature.StatusReviewPassed,
		Stack:  threeLayerStack(branches, map[string][3]string{"r1": tipsA, "r2": tipsB}),
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: repoA, WorktreePath: repoA, Branch: branches[2], BaseBranch: mainBranch},
			{Name: "r2", Path: repoB, WorktreePath: repoB, Branch: branches[2], BaseBranch: mainBranch},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {Touched: true},
			"r2": {Touched: true},
		},
	}
	lc := stackPublishLifecycle(f)
	fs := newFeatureStore(f)
	installStackPublishFakeGitHub(t)

	pub := mocks.NewMockRemoteOps()
	pub.PushLayerBranchFn = func(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
		return localSHA, nil
	}
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string, draft bool) (string, error) {
		if repoPath != repoA {
			t.Fatalf("CreatePR called for unselected repository path %q", repoPath)
		}
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Remote:      pub,
		PhaseRunner: newPublishDescriptionPhaseRunner(t, "## Summary\n\nGenerated body", false),
	}, orchestrator.Hooks{})

	if err := o.PublishWithOptions(f.ID, orchestrator.PublishOptions{Repos: []string{"r1"}}); err != nil {
		t.Fatalf("PublishWithOptions: %v", err)
	}
	for _, call := range pub.Calls {
		if call.Method == "PushLayerBranch" || call.Method == "CreatePR" {
			if call.Args[0] != repoA {
				t.Fatalf("%s called for unselected repository: %+v", call.Method, call)
			}
		}
	}
	if entry := f.Stack[0].Repos["r2"]; entry.PRURL != "" || entry.NoCommits {
		t.Fatalf("r2 layer 1 entry = %+v, want untouched by the selected walk", entry)
	}
}
