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

// Real-git coverage of the review-feedback integration tail: the
// journal-driven republish through the stack publish walk with leased
// layer pushes, per-layer reply SHAs resolved from the relocated commits,
// and the diverged-remote failure that blocks one repository's replies
// without touching the others.

package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type tailLayerPush struct {
	repoPath      string
	branch        string
	localSHA      string
	lastPushedSHA string
}

// tailRecordingRemoteOps keeps the leased layer pushes genuine (real bare
// remotes) while recording every PushLayerBranch call and answering pull
// request state lookups as open.
type tailRecordingRemoteOps struct {
	mu     sync.Mutex
	pushes []tailLayerPush
}

func (r *tailRecordingRemoteOps) Push(worktreePath, branch string) error {
	return git.Push(worktreePath, branch)
}

func (r *tailRecordingRemoteOps) PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA string) (string, error) {
	r.mu.Lock()
	r.pushes = append(r.pushes, tailLayerPush{repoPath: repoPath, branch: branch, localSHA: localSHA, lastPushedSHA: lastPushedSHA})
	r.mu.Unlock()
	return git.PushLayerBranch(repoPath, branch, localSHA, lastPushedSHA)
}

func (r *tailRecordingRemoteOps) CreatePR(string, string, string, string, string, bool) (string, error) {
	return "", errors.New("CreatePR is not used by this test")
}

func (r *tailRecordingRemoteOps) PRBaseBranch(string, string) string { return "" }

func (r *tailRecordingRemoteOps) PRState(string, string) (string, error) {
	return git.PRStateOpen, nil
}

func (r *tailRecordingRemoteOps) GetPRBody(prURL string) (string, error) {
	return git.GetPRBody(prURL)
}

func (r *tailRecordingRemoteOps) UpdatePRBody(prURL, body string) error {
	return git.UpdatePRBody(prURL, body)
}

func (r *tailRecordingRemoteOps) UpdatePRBase(prURL, base string) error {
	return git.UpdatePRBaseBranch(prURL, base)
}

func (r *tailRecordingRemoteOps) ReopenPullRequest(repoPath, branch, prURL string) error {
	return git.ReopenPullRequest(repoPath, branch, prURL)
}

func (r *tailRecordingRemoteOps) layerPushes() []tailLayerPush {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tailLayerPush(nil), r.pushes...)
}

type reviewFeedbackTailFixture struct {
	t                                *testing.T
	store                            *feature.Store
	mgr                              *feature.Manager
	parentID, childID                string
	repoA, repoB                     string
	remote                           *tailRecordingRemoteOps
	fake                             *testutil.FakeGitHubAPI
	tip1A, tip2A, newTip1A, newTip2A string
	tip1B, tip2B                     string
	fixLayer1A, fixLayer2A           string
	childCommit1, childCommit2       string
}

func newReviewFeedbackTailFixture(t *testing.T) *reviewFeedbackTailFixture {
	t.Helper()
	fx := &reviewFeedbackTailFixture{t: t}

	// repoA: a two-layer stack whose layer-1 range the child's fixes
	// rewrote (both layer branches carry replayed commits, so both pushes
	// are leased rewrites, not fast-forwards).
	repoA := testutil.InitGitRepo(t)
	bareA := testutil.PairWithBareRemote(t, repoA, "main", "main")
	testutil.CreateBranch(t, repoA, "layer-1")
	fx.tip1A = testutil.CommitFile(t, repoA, "layer1.txt", "one\n", "layer one work")
	testutil.CreateBranch(t, repoA, "layer-2")
	fx.tip2A = testutil.CommitFile(t, repoA, "layer2.txt", "two\n", "layer two work")
	testutil.SimulatePush(t, repoA, bareA, "layer-1", "layer-1")
	testutil.SimulatePush(t, repoA, bareA, "layer-2", "layer-2")

	// The landed chain after integration: the layer-1 fix sits under the
	// replayed layer-1 commit, the layer-2 fix above the replayed layer-2
	// commit, each fix tagged with its Stack-Layer trailer.
	tailGit(t, repoA, "checkout", "-b", "rebuilt-1", "main")
	fx.fixLayer1A = testutil.CommitFile(t, repoA, "fix1.txt", "fix one\n", "fix one\n\nStack-Layer: 1")
	tailGit(t, repoA, "cherry-pick", fx.tip1A)
	fx.newTip1A = tailGit(t, repoA, "rev-parse", "HEAD")
	tailGit(t, repoA, "cherry-pick", fx.tip2A)
	fx.fixLayer2A = testutil.CommitFile(t, repoA, "fix2.txt", "fix two\n", "fix two\n\nStack-Layer: 2")
	fx.newTip2A = tailGit(t, repoA, "rev-parse", "HEAD")
	tailGit(t, repoA, "branch", "-f", "layer-1", fx.newTip1A)
	tailGit(t, repoA, "branch", "-f", "layer-2", fx.newTip2A)
	tailGit(t, repoA, "checkout", "layer-2")

	// repoB: the same two-layer stack, untouched by the child (a
	// pass-through journal entry: every ref's candidate equals its anchor).
	repoB := testutil.InitGitRepo(t)
	bareB := testutil.PairWithBareRemote(t, repoB, "main", "main")
	testutil.CreateBranch(t, repoB, "layer-1")
	fx.tip1B = testutil.CommitFile(t, repoB, "layer1.txt", "one\n", "layer one work")
	testutil.CreateBranch(t, repoB, "layer-2")
	fx.tip2B = testutil.CommitFile(t, repoB, "layer2.txt", "two\n", "layer two work")
	testutil.SimulatePush(t, repoB, bareB, "layer-1", "layer-1")
	testutil.SimulatePush(t, repoB, bareB, "layer-2", "layer-2")
	tailGit(t, repoB, "checkout", "layer-2")
	fx.repoA, fx.repoB = repoA, repoB

	// Stand-in child commit SHAs: the child worktree is gone by tail time;
	// only the relocated values need to resolve on the parent chain.
	fx.childCommit1 = strings.ReplaceAll(strings.Repeat("a1", 20), " ", "")
	fx.childCommit2 = strings.ReplaceAll(strings.Repeat("b2", 20), " ", "")

	publishable := true
	parent := &feature.Feature{
		ID:            "tail-parent",
		Name:          "Tail parent",
		Slug:          "tail-parent",
		Status:        feature.StatusPublished,
		CurrentPhase:  feature.PhasePublish,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: repoA, WorktreePath: repoA, Branch: "layer-2", BaseBranch: "main", Publishable: &publishable},
			{Name: "repoB", Path: repoB, WorktreePath: repoB, Branch: "layer-2", BaseBranch: "main", Publishable: &publishable},
		},
		RepoStates: map[string]*feature.RepoState{
			"repoA": {Touched: true},
			// repoB never had a phase touch it: the publish walk skips it as
			// untouched, and the tail must never flip its touched flag.
			"repoB": {Touched: false},
		},
		Stack: []feature.StackLayer{
			{
				Position: 1, Title: "Layer one", Branch: "layer-1",
				Repos: map[string]feature.StackRepoEntry{
					"repoA": {PRURL: "https://github.com/example/repoa/pull/1", PRState: feature.StackPRStateOpen, TipSHA: fx.newTip1A, LastPushedSHA: fx.tip1A},
					"repoB": {PRURL: "https://github.com/example/repob/pull/1", PRState: feature.StackPRStateOpen, TipSHA: fx.tip1B, LastPushedSHA: fx.tip1B},
				},
			},
			{
				Position: 2, Title: "Layer two", Branch: "layer-2",
				Repos: map[string]feature.StackRepoEntry{
					"repoA": {PRURL: "https://github.com/example/repoa/pull/2", PRState: feature.StackPRStateOpen, TipSHA: fx.newTip2A, LastPushedSHA: fx.tip2A},
					"repoB": {PRURL: "https://github.com/example/repob/pull/2", PRState: feature.StackPRStateOpen, TipSHA: fx.tip2B, LastPushedSHA: fx.tip2B},
				},
			},
		},
	}

	child := &feature.Feature{
		ID:            "tail-child",
		Name:          "Tail child",
		Slug:          "tail-child",
		Status:        feature.StatusReviewPassed,
		CurrentPhase:  feature.PhaseFinalReview,
		Pipeline:      feature.PipelineMedium,
		Created:       time.Now().UTC().Truncate(time.Second),
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: repoA, Branch: "layer-2", BaseBranch: "main"},
			{Name: "repoB", Path: repoB, Branch: "layer-2", BaseBranch: "main"},
		},
		Parent: &feature.ChildRelationship{
			ParentID:     parent.ID,
			Kind:         feature.ChildKindReviewFeedback,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			Transaction: &feature.TransactionJournal{
				Phase: feature.TransactionPhaseMerged,
				Entries: []feature.RepoTransactionEntry{
					{
						Repo: "repoA",
						Refs: []feature.RepoTransactionRef{
							{Branch: "layer-1", Layer: 1, AnchorSHA: fx.tip1A, CandidateSHA: fx.newTip1A},
							{Branch: "layer-2", Layer: 2, AnchorSHA: fx.tip2A, CandidateSHA: fx.newTip2A},
						},
						Relocated: map[string]string{
							fx.childCommit1: fx.fixLayer1A,
							fx.childCommit2: fx.fixLayer2A,
						},
					},
					{
						Repo: "repoB",
						Refs: []feature.RepoTransactionRef{
							{Branch: "layer-2", Layer: 2, AnchorSHA: fx.tip2B, CandidateSHA: fx.tip2B},
						},
					},
				},
			},
		},
		ReviewFeedback: []feature.ReviewFeedbackComment{
			{Repo: "repoA", ID: 11, Type: git.CommentTypeReview, Body: "fix the handler", PRURL: "https://github.com/example/repoa/pull/2", PRNumber: 2, LayerPosition: 2, LayerTitle: "Layer two"},
			{Repo: "repoA", ID: 12, Type: git.CommentTypeIssue, Body: "fix the docs", PRURL: "https://github.com/example/repoa/pull/1", PRNumber: 1, LayerPosition: 1, LayerTitle: "Layer one"},
			{Repo: "repoB", ID: 21, Type: git.CommentTypeReview, Body: "add a test", PRURL: "https://github.com/example/repob/pull/2", PRNumber: 2, LayerPosition: 2, LayerTitle: "Layer two"},
		},
	}

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	mgr := feature.NewManager(store, config.NewDefault())

	fx.store = store
	fx.mgr = mgr
	fx.parentID, fx.childID = parent.ID, child.ID
	fx.remote = &tailRecordingRemoteOps{}
	fx.fake = installReviewFeedbackTailFakeAPI(t)
	return fx
}

func (fx *reviewFeedbackTailFixture) runTail() error {
	fx.t.Helper()
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("load child: %v", err)
	}
	parent, err := fx.store.Load(fx.parentID)
	if err != nil {
		fx.t.Fatalf("load parent: %v", err)
	}
	o := New(Deps{
		Lifecycle: fx.mgr,
		Store:     fx.store,
		Remote:    fx.remote,
	}, Hooks{})
	return o.reviewFeedbackIntegrationTail(child, parent)
}

func (fx *reviewFeedbackTailFixture) reloadChild() *feature.Feature {
	fx.t.Helper()
	child, err := fx.store.Load(fx.childID)
	if err != nil {
		fx.t.Fatalf("reload child: %v", err)
	}
	return child
}

// installReviewFeedbackTailFakeAPI serves the reply endpoints, the issue
// comment endpoint, and per-PR GraphQL thread maps and resolutions. The
// thread map is resolution-stateful: a thread the resolve mutation landed on
// is reported resolved afterwards, so a retried tail's thread-map fetch
// filters it out exactly like the real API.
func installReviewFeedbackTailFakeAPI(t *testing.T) *testutil.FakeGitHubAPI {
	t.Helper()
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.HandleJSON("/repos/example/repoa/pulls/2/comments/11/replies", http.StatusCreated, `{}`)
	fake.HandleJSON("/repos/example/repoa/issues/1/comments", http.StatusCreated, `{}`)
	fake.HandleJSON("/repos/example/repob/pulls/2/comments/21/replies", http.StatusCreated, `{}`)
	resolved := make(map[string]bool)
	var mu sync.Mutex
	threadMap := func(thread string, commentID int) string {
		mu.Lock()
		isResolved := resolved[thread]
		mu.Unlock()
		return fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":%q,"isResolved":%t,"comments":{"nodes":[{"databaseId":%d}]}}]}}}}}`,
			thread, isResolved, commentID)
	}
	fake.Mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(body), "resolveReviewThread"):
			for _, thread := range []string{"thread-11", "thread-21"} {
				if strings.Contains(string(body), thread) {
					mu.Lock()
					resolved[thread] = true
					mu.Unlock()
				}
			}
			fmt.Fprint(w, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
		case strings.Contains(string(body), `"name":"repoa"`):
			fmt.Fprint(w, threadMap("thread-11", 11))
		case strings.Contains(string(body), `"name":"repob"`):
			fmt.Fprint(w, threadMap("thread-21", 21))
		default:
			fmt.Fprint(w, `{"data":{}}`)
		}
	})
	return fake
}

// TestReviewFeedbackTailRepublishesAndRepliesWithLayerSHAs proves the tail
// publishes only the journal's repositories, pushes exactly the rewritten
// layers with the previous pushed SHAs as leases, replies to each comment
// with the relocated SHA recorded for its layer, and leaves pass-through
// repositories replied without any push.
func TestReviewFeedbackTailRepublishesAndRepliesWithLayerSHAs(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newReviewFeedbackTailFixture(t)

	if err := fx.runTail(); err != nil {
		t.Fatalf("reviewFeedbackIntegrationTail() error = %v", err)
	}

	// Only repoA's rewritten layers pushed, each leased on its previous
	// pushed SHA; repoB (pass-through) pushed nothing.
	pushes := fx.remote.layerPushes()
	if len(pushes) != 2 {
		t.Fatalf("layer pushes = %+v, want repoA layer-1 and layer-2 only", pushes)
	}
	wantPushes := []tailLayerPush{
		{repoPath: fx.repoA, branch: "layer-1", localSHA: fx.newTip1A, lastPushedSHA: fx.tip1A},
		{repoPath: fx.repoA, branch: "layer-2", localSHA: fx.newTip2A, lastPushedSHA: fx.tip2A},
	}
	for i, want := range wantPushes {
		if pushes[i] != want {
			t.Errorf("layer push %d = %+v, want %+v", i, pushes[i], want)
		}
	}

	// Each reply names the relocated SHA recorded for the comment's layer;
	// the pass-through repository's reply cites its unchanged top tip.
	for _, tc := range []struct {
		substr string
		sha    string
	}{
		{substr: "/repos/example/repoa/pulls/2/comments/11/replies", sha: fx.fixLayer2A},
		{substr: "/repos/example/repoa/issues/1/comments", sha: fx.fixLayer1A},
		{substr: "/repos/example/repob/pulls/2/comments/21/replies", sha: fx.tip2B},
	} {
		found := false
		for _, line := range fx.fake.Requests() {
			if strings.Contains(line, tc.substr) {
				found = true
				if !strings.Contains(line, tc.sha) {
					t.Errorf("reply %s body missing relocated SHA %s: %s", tc.substr, tc.sha, line)
				}
			}
		}
		if !found {
			t.Errorf("no reply request matching %s", tc.substr)
		}
	}

	// Both inline threads resolved.
	if got := fx.fake.RequestCount("resolveReviewThread"); got != 2 {
		t.Errorf("thread resolutions = %d, want 2 (comments 11 and 21)", got)
	}

	// The addressed ledger recorded every replied comment.
	addressedA, err := fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoA")
	if err != nil {
		t.Fatalf("load addressed IDs repoA: %v", err)
	}
	if !addressedA[11] || !addressedA[12] {
		t.Errorf("addressed IDs repoA = %v, want 11 and 12", addressedA)
	}
	addressedB, err := fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoB")
	if err != nil {
		t.Fatalf("load addressed IDs repoB: %v", err)
	}
	if !addressedB[21] {
		t.Errorf("addressed IDs repoB = %v, want 21", addressedB)
	}

	// The parent ends Published and the tail settles with no warnings.
	parent, err := fx.mgr.Get(fx.parentID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if parent.Status != feature.StatusPublished {
		t.Errorf("parent status = %s, want Published", parent.Status)
	}
	// The untouched repository the walk skipped never had its touched flag
	// flipped by the tail.
	if state := parent.RepoStates["repoB"]; state == nil || state.Touched {
		t.Errorf("repoB touched = %v, want false (the walk skipped the untouched repository)", state)
	}
	child := fx.reloadChild()
	if child.Parent.Transaction == nil || !child.Parent.Transaction.TailSettled {
		t.Fatalf("tail-settled marker = %v, want true", child.Parent.Transaction)
	}
	for _, entry := range child.Parent.Transaction.Entries {
		if entry.Tail != nil {
			t.Errorf("entry %s tail warning = %+v, want none", entry.Repo, entry.Tail)
		}
	}
}

// TestReviewFeedbackTailDivergedRepublishBlocksReplies proves the
// unsettled-then-settled progression for a repository whose republish fails
// with a diverged remote: the failed attempt records the tail-incomplete
// warning, posts no replies, keeps its comments out of the addressed ledger,
// and leaves the tail unsettled — with the stored publish record naming the
// divergence — while the other repository is replied to and the parent still
// ends Published. Restoring the remote and re-running the tail retracts
// nothing: the ledger keeps repoB's reply from repeating, the retry pushes
// only the diverged layer leased on its recorded last pushed SHA, repoA's
// replies land exactly once citing the relocated layer SHAs, and the tail
// settles with both records cleared.
func TestReviewFeedbackTailDivergedRepublishBlocksReplies(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newReviewFeedbackTailFixture(t)

	// Move repoA's remote layer-2 branch away from the recorded last pushed
	// SHA: the leased rewrite push must report the divergence.
	tailGit(t, fx.repoA, "checkout", "-b", "diverge", fx.tip2A)
	testutil.CommitFile(t, fx.repoA, "remote-side.txt", "remote change\n", "remote-side change")
	bareA := tailGit(t, fx.repoA, "remote", "get-url", "origin")
	testutil.SimulatePush(t, fx.repoA, bareA, "diverge", "layer-2")
	divergedTip := tailGit(t, fx.repoA, "rev-parse", "origin/layer-2")
	tailGit(t, fx.repoA, "checkout", "layer-2")

	if err := fx.runTail(); err != nil {
		t.Fatalf("reviewFeedbackIntegrationTail() error = %v", err)
	}

	// repoA's layer-1 lease push succeeded before layer-2 diverged; repoB
	// pushed nothing. The diverged layer-2 lease push was attempted and
	// rejected — the remote branch still holds the diverged tip.
	pushes := fx.remote.layerPushes()
	if len(pushes) != 2 {
		t.Fatalf("layer pushes = %+v, want repoA's two leased pushes only", pushes)
	}
	wantPushes := []tailLayerPush{
		{repoPath: fx.repoA, branch: "layer-1", localSHA: fx.newTip1A, lastPushedSHA: fx.tip1A},
		{repoPath: fx.repoA, branch: "layer-2", localSHA: fx.newTip2A, lastPushedSHA: fx.tip2A},
	}
	for i, want := range wantPushes {
		if pushes[i] != want {
			t.Errorf("layer push %d = %+v, want %+v", i, pushes[i], want)
		}
	}
	for _, push := range pushes {
		if push.repoPath == fx.repoB {
			t.Errorf("repoB layer push = %+v, want none", push)
		}
	}
	if got := tailGit(t, fx.repoA, "rev-parse", "origin/layer-2"); got != divergedTip {
		t.Errorf("remote layer-2 = %s, want the diverged tip %s (the leased push must be rejected)", got, divergedTip)
	}
	if got := tailGit(t, fx.repoA, "rev-parse", "origin/layer-1"); got != fx.newTip1A {
		t.Errorf("remote layer-1 = %s, want the pushed new tip %s", got, fx.newTip1A)
	}

	// No reply for repoA: neither the inline nor the issue comment endpoint
	// was hit for that repository.
	for _, line := range fx.fake.Requests() {
		if strings.Contains(line, "example/repoa") && (strings.Contains(line, "replies") || strings.Contains(line, "POST /repos/example/repoa/issues")) {
			t.Errorf("repoA received a reply despite the failed republish: %s", line)
		}
	}
	// repoB was replied to as usual.
	if fx.fake.RequestCount("/repos/example/repob/pulls/2/comments/21/replies") != 1 {
		t.Errorf("repoB reply requests = %d, want 1", fx.fake.RequestCount("/repos/example/repob/pulls/2/comments/21/replies"))
	}

	// repoA's comments stay absent from the addressed ledger; repoB's are
	// recorded.
	addressedA, err := fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoA")
	if err != nil {
		t.Fatalf("load addressed IDs repoA: %v", err)
	}
	if len(addressedA) != 0 {
		t.Errorf("addressed IDs repoA = %v, want empty after failed republish", addressedA)
	}
	addressedB, err := fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoB")
	if err != nil {
		t.Fatalf("load addressed IDs repoB: %v", err)
	}
	if !addressedB[21] {
		t.Errorf("addressed IDs repoB = %v, want 21", addressedB)
	}

	// The tail warning names the failure on repoA's entry only.
	child := fx.reloadChild()
	entryA := child.Parent.Transaction.EntryByRepo("repoA")
	if entryA == nil || entryA.Tail == nil {
		t.Fatalf("repoA tail warning = %+v, want a record", entryA)
	}
	if entryA.Tail.Code != errcat.ReviewFeedbackTailIncomplete {
		t.Errorf("repoA tail warning code = %q, want %q", entryA.Tail.Code, errcat.ReviewFeedbackTailIncomplete)
	}
	if !strings.Contains(entryA.Tail.Diagnostics, "republish failed") {
		t.Errorf("repoA tail warning diagnostics = %q, want the republish failure named", entryA.Tail.Diagnostics)
	}
	entryB := child.Parent.Transaction.EntryByRepo("repoB")
	if entryB == nil || entryB.Tail != nil {
		t.Fatalf("repoB tail warning = %+v, want none", entryB)
	}

	// The parent's stored publish record for repoA classifies the
	// divergence.
	parent, err := fx.mgr.Get(fx.parentID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if record := parent.RepoStates["repoA"].Error; record == nil || record.Code != errcat.PublishRemoteDiverged {
		t.Errorf("repoA publish record = %+v, want publish_remote_diverged", parent.RepoStates["repoA"].Error)
	}

	// The parent still ends Published, but the failed attempt leaves the
	// tail unsettled so recovery or a re-integration retries it.
	if parent.Status != feature.StatusPublished {
		t.Errorf("parent status = %s, want Published", parent.Status)
	}
	if child.Parent.Transaction.TailSettled {
		t.Errorf("tail-settled marker = true, want false (the failed attempt must stay retryable)")
	}

	// Retry: restore the bare remote's layer-2 branch to the recorded last
	// pushed SHA and run the tail again on the reloaded child and parent.
	tailGit(t, fx.repoA, "branch", "-f", "restore-layer-2", fx.tip2A)
	testutil.SimulatePush(t, fx.repoA, bareA, "restore-layer-2", "layer-2")
	pushesBeforeRetry := len(fx.remote.layerPushes())

	if err := fx.runTail(); err != nil {
		t.Fatalf("reviewFeedbackIntegrationTail() retry error = %v", err)
	}

	// The retry pushes only repoA's layer-2, leased on its recorded last
	// pushed SHA; layer-1's tip already equals its recorded pushed SHA, so
	// it is skipped.
	pushes = fx.remote.layerPushes()
	if got := len(pushes) - pushesBeforeRetry; got != 1 {
		t.Fatalf("layer pushes in the retry = %d, want exactly 1 (repoA layer-2): %+v", got, pushes[pushesBeforeRetry:])
	}
	if push := pushes[len(pushes)-1]; push.repoPath != fx.repoA || push.branch != "layer-2" ||
		push.localSHA != fx.newTip2A || push.lastPushedSHA != fx.tip2A {
		t.Fatalf("retry layer push = %+v, want repoA layer-2 leased on %s", push, fx.tip2A)
	}

	// repoA's two replies were posted exactly once each, citing the
	// relocated layer SHAs; repoB's comment 21 received no second reply.
	for _, tc := range []struct {
		substr string
		sha    string
	}{
		{substr: "/repos/example/repoa/pulls/2/comments/11/replies", sha: fx.fixLayer2A},
		{substr: "/repos/example/repoa/issues/1/comments", sha: fx.fixLayer1A},
	} {
		count := 0
		for _, line := range fx.fake.Requests() {
			if !strings.Contains(line, tc.substr) {
				continue
			}
			count++
			if !strings.Contains(line, tc.sha) {
				t.Errorf("reply %s body missing relocated SHA %s: %s", tc.substr, tc.sha, line)
			}
		}
		if count != 1 {
			t.Errorf("reply requests for %s = %d, want exactly 1", tc.substr, count)
		}
	}
	if got := fx.fake.RequestCount("/repos/example/repob/pulls/2/comments/21/replies"); got != 1 {
		t.Errorf("repoB reply requests after retry = %d, want 1 (the ledger keeps replies from repeating)", got)
	}
	// Comment 21's thread was resolved once, on the first attempt; the
	// retry resolves comment 11's thread, bringing the total to two.
	if got := fx.fake.RequestCount("resolveReviewThread"); got != 2 {
		t.Errorf("thread resolutions = %d, want 2 (comments 11 and 21, once each)", got)
	}

	// The addressed ledger holds every comment: 11 and 12 for repoA, 21
	// for repoB.
	addressedA, err = fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoA")
	if err != nil {
		t.Fatalf("load addressed IDs repoA after retry: %v", err)
	}
	if !addressedA[11] || !addressedA[12] {
		t.Errorf("addressed IDs repoA after retry = %v, want 11 and 12", addressedA)
	}
	addressedB, err = fx.store.LoadAddressedReviewFeedbackIDs(fx.parentID, "repoB")
	if err != nil {
		t.Fatalf("load addressed IDs repoB after retry: %v", err)
	}
	if !addressedB[21] {
		t.Errorf("addressed IDs repoB after retry = %v, want 21", addressedB)
	}

	// Both of repoA's records — the tail warning and the stored publish
	// failure — are cleared, the parent is Published, and the tail settles.
	child = fx.reloadChild()
	if entryA = child.Parent.Transaction.EntryByRepo("repoA"); entryA == nil || entryA.Tail != nil {
		t.Fatalf("repoA tail warning after retry = %+v, want cleared", entryA)
	}
	parent, err = fx.mgr.Get(fx.parentID)
	if err != nil {
		t.Fatalf("reload parent after retry: %v", err)
	}
	if record := parent.RepoStates["repoA"].Error; record != nil {
		t.Fatalf("repoA publish record after retry = %+v, want cleared", record)
	}
	if parent.Status != feature.StatusPublished {
		t.Errorf("parent status after retry = %s, want Published", parent.Status)
	}
	if !child.Parent.Transaction.TailSettled {
		t.Errorf("tail-settled marker after retry = false, want true")
	}
}

// TestReviewFeedbackTailAttemptBoundaryCoversCommentlessJournalRepos pins
// the attempt-boundary bookkeeping for a journal repository with refs but no
// selected comments: its republish failure records the tail warning and a
// stored publish failure, and a later successful attempt must clear both —
// even though the comments loop, where the clearing used to live, never runs
// for it. The untouched repository the walk skipped keeps its touched flag.
func TestReviewFeedbackTailAttemptBoundaryCoversCommentlessJournalRepos(t *testing.T) {
	if testing.Short() {
		t.Skip("real-git integration test")
	}
	fx := newReviewFeedbackTailFixture(t)

	// No selected comments: the tail is the republish walk only.
	if err := fx.store.Modify(fx.childID, func(f *feature.Feature) error {
		f.ReviewFeedback = nil
		return nil
	}); err != nil {
		t.Fatalf("clear review feedback: %v", err)
	}

	// Diverge repoA's remote layer-2 branch away from the recorded last
	// pushed SHA so the leased rewrite push is rejected.
	tailGit(t, fx.repoA, "checkout", "-b", "diverge", fx.tip2A)
	testutil.CommitFile(t, fx.repoA, "remote-side.txt", "remote change\n", "remote-side change")
	bareA := tailGit(t, fx.repoA, "remote", "get-url", "origin")
	testutil.SimulatePush(t, fx.repoA, bareA, "diverge", "layer-2")
	tailGit(t, fx.repoA, "checkout", "layer-2")

	if err := fx.runTail(); err != nil {
		t.Fatalf("reviewFeedbackIntegrationTail() error = %v", err)
	}

	// The failed attempt leaves the tail warning, the stored publish
	// failure, and an unsettled tail.
	child := fx.reloadChild()
	entryA := child.Parent.Transaction.EntryByRepo("repoA")
	if entryA == nil || entryA.Tail == nil {
		t.Fatalf("repoA tail warning = %+v, want a record after the failed republish", entryA)
	}
	if !strings.Contains(entryA.Tail.Diagnostics, "republish failed") {
		t.Fatalf("repoA tail warning diagnostics = %q, want the republish failure named", entryA.Tail.Diagnostics)
	}
	if child.Parent.Transaction.TailSettled {
		t.Fatal("tail-settled marker = true after the failed attempt, want false")
	}
	parent, err := fx.mgr.Get(fx.parentID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if record := parent.RepoStates["repoA"].Error; record == nil || record.Code != errcat.PublishRemoteDiverged {
		t.Fatalf("repoA publish record = %+v, want publish_remote_diverged", parent.RepoStates["repoA"].Error)
	}

	// Restore the remote's layer-2 branch to the recorded last pushed SHA
	// and retry the tail on the reloaded child and parent.
	tailGit(t, fx.repoA, "branch", "-f", "restore-layer-2", fx.tip2A)
	testutil.SimulatePush(t, fx.repoA, bareA, "restore-layer-2", "layer-2")
	pushesBeforeRetry := len(fx.remote.layerPushes())

	if err := fx.runTail(); err != nil {
		t.Fatalf("reviewFeedbackIntegrationTail() retry error = %v", err)
	}

	// The successful attempt clears both records, pushes only repoA's
	// layer-2 (layer-1's tip already equals its recorded pushed SHA), and
	// settles the tail.
	child = fx.reloadChild()
	entryA = child.Parent.Transaction.EntryByRepo("repoA")
	if entryA == nil || entryA.Tail != nil {
		t.Fatalf("repoA tail warning after retry = %+v, want cleared", entryA)
	}
	if !child.Parent.Transaction.TailSettled {
		t.Fatal("tail-settled marker after retry = false, want true")
	}
	parent, err = fx.mgr.Get(fx.parentID)
	if err != nil {
		t.Fatalf("reload parent after retry: %v", err)
	}
	if record := parent.RepoStates["repoA"].Error; record != nil {
		t.Fatalf("repoA publish record after retry = %+v, want cleared", record)
	}
	if state := parent.RepoStates["repoB"]; state == nil || state.Touched {
		t.Fatalf("repoB touched = %v, want false (the walk skipped the untouched repository)", state)
	}
	pushes := fx.remote.layerPushes()
	if got := len(pushes) - pushesBeforeRetry; got != 1 {
		t.Fatalf("layer pushes in the retry = %d, want exactly 1 (repoA layer-2): %+v", got, pushes[pushesBeforeRetry:])
	}
	if push := pushes[len(pushes)-1]; push.branch != "layer-2" || push.localSHA != fx.newTip2A || push.lastPushedSHA != fx.tip2A {
		t.Fatalf("retry layer push = %+v, want repoA layer-2 leased on %s", push, fx.tip2A)
	}
}

// tailGit runs a git command in dir and returns its trimmed stdout.
func tailGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := execCommandGit(dir, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
