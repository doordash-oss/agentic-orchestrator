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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Category G — Publish pipeline
// ---------------------------------------------------------------------------

// Publish happy path: multiple publishable repos, no conflicts, no already-
// published skips. Emits PublishStarted + PublishCompleted, fans out per-repo,
// and delegates FeatureCompleted emission to tryCompleteAndEmit (which fires
// because TryCompletePublish returns true on the first call).
func TestOrchestrator_Publish_HappyPath_MultiRepo(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-happy",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	var publishedCount int
	var publishedFeatureID string
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{
		OnFeatureCompleted: func(id string, fv *feature.Feature) {
			publishedCount++
			publishedFeatureID = id
		},
	})

	publishRepoCalls := make(map[string]int)
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		publishRepoCalls[repo]++
		return "https://github.com/org/" + repo + "/pull/1", nil
	})

	if err := o.Publish("feat-pub-happy"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if publishRepoCalls["r1"] != 1 {
		t.Errorf("publishRepo calls for r1 = %d, want 1", publishRepoCalls["r1"])
	}
	if publishRepoCalls["r2"] != 1 {
		t.Errorf("publishRepo calls for r2 = %d, want 1", publishRepoCalls["r2"])
	}

	assertLifecycleCall(t, lc, "TryCompletePublish")
	if publishedCount != 1 {
		t.Errorf("OnFeatureCompleted fired %d times, want 1", publishedCount)
	}
	if publishedFeatureID != "feat-pub-happy" {
		t.Errorf("OnFeatureCompleted featureID = %q, want feat-pub-happy", publishedFeatureID)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PublishStarted) {
		t.Error("expected PublishStarted event")
	}
	if !hasEventType(events, ports.PublishCompleted) {
		t.Error("expected PublishCompleted event")
	}
	if !hasEventType(events, ports.FeatureCompleted) {
		t.Error("expected FeatureCompleted event")
	}
}

// Publish is a no-op for non-publishable features (explicitly-false flag).
func TestOrchestrator_Publish_NotPublishable_NoOp(t *testing.T) {
	unpub := false
	f := &feature.Feature{
		ID:     "feat-pub-np",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", Publishable: &unpub},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	calls := 0
	o.SetPublishRepoFn(func(id, repo string) (string, error) { calls++; return "", nil })

	if err := o.Publish("feat-pub-np"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if calls != 0 {
		t.Errorf("publishRepo should not fire for non-publishable feature; got %d", calls)
	}
	events := drainEvents(o)
	if hasEventType(events, ports.PublishStarted) {
		t.Error("PublishStarted should NOT fire for non-publishable")
	}
}

// Publish skips repos that have already been published (RepoImpl[repo].PRURL set).
func TestOrchestrator_Publish_SkipsAlreadyPublished(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-skip",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
		RepoStates: map[string]*feature.RepoState{
			"r1": {PRURL: "https://github.com/org/r1/pull/42"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	calls := make(map[string]int)
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		calls[repo]++
		return "https://github.com/org/" + repo + "/pull/99", nil
	})

	if err := o.Publish("feat-pub-skip"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if calls["r1"] != 0 {
		t.Errorf("r1 already published; publishRepo should not be called for r1; got %d", calls["r1"])
	}
	if calls["r2"] != 1 {
		t.Errorf("r2 publishRepo should be called once; got %d", calls["r2"])
	}
}

// Publish surfaces *PublishConflictError on conflict; final error satisfies
// errors.Is(err, ErrPublishConflict) and errors.As extracts repo info.
func TestOrchestrator_Publish_ConflictError_SurfacedAsSentinel(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-conflict",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	wantConflict := &orchestrator.PublishConflictError{RepoName: "r1", Branch: "feature/x"}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		return "", wantConflict
	})

	err := o.Publish("feat-pub-conflict")
	if err == nil {
		t.Fatal("expected error from conflict, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("errors.Is(err, ErrPublishConflict) = false; err = %v", err)
	}
	var ce *orchestrator.PublishConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As failed: %v", err)
	}
	if ce.RepoName != "r1" || ce.Branch != "feature/x" {
		t.Errorf("conflict details lost: %+v", ce)
	}

	events := drainEvents(o)
	if !hasEventType(events, ports.PublishCompleted) {
		t.Error("expected PublishCompleted event even on conflict")
	}
}

// When both a conflict and a plain error occur in the same publish pass, the
// conflict sentinel wins (conflict gets preferential surfacing).
func TestOrchestrator_Publish_Conflict_Takes_Priority(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-mix",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1"},
			{Name: "r2", Path: "/tmp/r2"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	plainErr := errors.New("push boom")
	conflict := &orchestrator.PublishConflictError{RepoName: "r2", Branch: "feature/x"}
	o.SetPublishRepoFn(func(id, repo string) (string, error) {
		switch repo {
		case "r1":
			return "", plainErr
		case "r2":
			return "", conflict
		}
		return "", nil
	})

	err := o.Publish("feat-pub-mix")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("conflict should take priority; err = %v", err)
	}
}

// Publish with no repos returns an explicit error (nothing to publish is a
// caller bug, not silent success).
func TestOrchestrator_Publish_NoRepos_Errors(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-empty",
		Status: feature.StatusReviewPassed,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})
	err := o.Publish("feat-pub-empty")
	if err == nil {
		t.Fatal("expected error for empty-repos feature, got nil")
	}
}

// ---------------------------------------------------------------------------
// publishRepo (internal) — exercised via o.Publish with real port mocks.
// ---------------------------------------------------------------------------

// publishRepo commits uncommitted changes, skips pull-rebase when Rebaser nil,
// pushes, creates PR, records success on Lifecycle. End-to-end via o.Publish.
func TestOrchestrator_PublishRepo_EndToEnd_NoRebaser(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pubrepo",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(path string) (bool, error) { return true, nil }
	pub.CommitAllFn = func(path, msg string) error { return nil }
	pub.CommitBodiesFn = func(path, base string) (string, error) { return "commit bodies", nil }
	pub.DiffStatFn = func(path, base string) (string, error) { return "stat", nil }
	pub.PushFn = func(path, branch string) error { return nil }
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string) (string, error) {
		return "https://github.com/org/r1/pull/1", nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
	}, orchestrator.Hooks{})

	if err := o.Publish("feat-pubrepo"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Publisher calls: HasUncommittedChanges, CommitAll, CommitBodies, DiffStat, Push, CreatePR.
	got := make(map[string]int)
	for _, c := range pub.Calls {
		got[c.Method]++
	}
	for _, want := range []string{"HasUncommittedChanges", "CommitAll", "CommitBodies", "DiffStat", "Push", "CreatePR"} {
		if got[want] == 0 {
			t.Errorf("expected Publisher.%s to be called", want)
		}
	}

	assertLifecycleCall(t, lc, "SetRepoPublished")
}

// publishRepo surfaces a pull-rebase conflict as *PublishConflictError and
// records SetRepoPublishError on the lifecycle.
func TestOrchestrator_PublishRepo_PullRebaseConflict_Sentinel(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pubrepo-conflict",
		Name:   "x",
		Slug:   "x",
		Status: feature.StatusReviewPassed,
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishErrorFn = func(id, repo, msg string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return false, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(path string) (bool, error) { return false, nil }
	pub.DiffSummaryFn = func(path, base string) (string, error) { return "", nil }

	reb := mocks.NewMockRebaseOperator()
	reb.PullRebaseFn = func(worktreePath, branch string) git.PullRebaseResult {
		return git.PullRebaseResult{Outcome: git.PullRebaseConflict}
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Publisher: pub,
		Rebaser:   reb,
	}, orchestrator.Hooks{})

	err := o.Publish("feat-pubrepo-conflict")
	if err == nil {
		t.Fatal("expected conflict error, got nil")
	}
	if !errors.Is(err, orchestrator.ErrPublishConflict) {
		t.Errorf("errors.Is(err, ErrPublishConflict) = false; err = %v", err)
	}

	assertLifecycleCall(t, lc, "SetRepoPublishError")
	// Push and CreatePR should NOT have been called because we bailed on rebase.
	for _, c := range pub.Calls {
		if c.Method == "Push" || c.Method == "CreatePR" {
			t.Errorf("%s should not have been called after rebase conflict", c.Method)
		}
	}
}

func TestOrchestrator_PublishRepo_UsesPhaseRunnerDescriptionGeneration(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-desc",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(path string) (bool, error) { return false, nil }
	pub.CommitBodiesFn = func(path, base string) (string, error) { return "commit bodies", nil }
	pub.DiffStatFn = func(path, base string) (string, error) { return "stat", nil }
	pub.PushFn = func(path, branch string) error { return nil }

	var gotTitle, gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string) (string, error) {
		gotTitle, gotBody = title, body
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "TITLE: Session Title\nBODY:\n## Summary\n\nGenerated body", false)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Publisher:   pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish("feat-pub-desc"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if gotTitle != "Session Title" {
		t.Errorf("CreatePR title = %q, want %q", gotTitle, "Session Title")
	}
	if !strings.Contains(gotBody, "Generated body") {
		t.Errorf("CreatePR body = %q, want generated session output", gotBody)
	}
}

func TestOrchestrator_PublishRepo_FallsBackAndLogsDescriptionGenerationErrors(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-pub-fallback",
		Name:   "cool-feature",
		Slug:   "cool-feature",
		Status: feature.StatusReviewPassed,
		Models: config.ModelConfig{Planning: "sonnet"},
		Repos: []feature.FeatureRepo{
			{Name: "r1", Path: "/tmp/r1", WorktreePath: "/tmp/wt-r1", BaseBranch: "main"},
		},
	}
	lc := lifecycleForFeature(f)
	lc.SetRepoPublishedFn = func(id, repo, url string) error { return nil }
	lc.TryCompletePublishFn = func(id string) (bool, error) { return true, nil }
	fs := newFeatureStore(f)

	pub := mocks.NewMockPublisher()
	pub.HasUncommittedChangesFn = func(path string) (bool, error) { return false, nil }
	pub.CommitBodiesFn = func(path, base string) (string, error) { return "commit bodies", nil }
	pub.DiffStatFn = func(path, base string) (string, error) { return "stat", nil }
	pub.PushFn = func(path, branch string) error { return nil }

	var gotTitle, gotBody string
	pub.CreatePRFn = func(repoPath, branch, title, body, baseBranch string) (string, error) {
		gotTitle, gotBody = title, body
		return "https://github.com/org/r1/pull/1", nil
	}

	pr := newPublishDescriptionPhaseRunner(t, "", true)
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Publisher:   pub,
		PhaseRunner: pr,
	}, orchestrator.Hooks{})

	if err := o.Publish("feat-pub-fallback"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if gotTitle != "cool-feature" {
		t.Errorf("CreatePR title = %q, want fallback title %q", gotTitle, "cool-feature")
	}
	if !strings.Contains(gotBody, "## Summary") {
		t.Errorf("CreatePR body = %q, want fallback body", gotBody)
	}

	logPath := filepath.Join(agent.ActiveRunDir(pr.StateDir, f), "publish", "error.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", logPath, err)
	}
	if !strings.Contains(string(data), "description generation: generating description:") {
		t.Errorf("error log = %q, want description generation context", string(data))
	}
}

type publishDescriptionSessionHandle struct {
	id          string
	featureID   string
	phase       feature.Phase
	done        chan struct{}
	statusCh    chan string
	attachCh    chan llm.SDKMessage
	msgLog      *session.MessageLog
	result      *llm.ResultMessage
	lastControl *llm.ControlRequestMessage
}

func newPublishDescriptionSessionHandle() *publishDescriptionSessionHandle {
	return &publishDescriptionSessionHandle{
		done:     make(chan struct{}),
		statusCh: make(chan string, 1),
		attachCh: make(chan llm.SDKMessage, 1),
		msgLog:   session.NewMessageLog(),
	}
}

func (s *publishDescriptionSessionHandle) ID() string              { return s.id }
func (s *publishDescriptionSessionHandle) FeatureID() string       { return s.featureID }
func (s *publishDescriptionSessionHandle) Phase() feature.Phase    { return s.phase }
func (s *publishDescriptionSessionHandle) RepoName() string        { return "" }
func (s *publishDescriptionSessionHandle) PermCacheScope() string  { return "" }
func (s *publishDescriptionSessionHandle) Kind() ports.SessionKind { return ports.KindPhase }
func (s *publishDescriptionSessionHandle) Label() string           { return "" }
func (s *publishDescriptionSessionHandle) Status() session.SessionStatus {
	return session.SessionRunning
}
func (s *publishDescriptionSessionHandle) IsActive() bool        { return true }
func (s *publishDescriptionSessionHandle) Iteration() int        { return 0 }
func (s *publishDescriptionSessionHandle) StartedAt() time.Time  { return time.Time{} }
func (s *publishDescriptionSessionHandle) InitialPrompt() string { return "" }
func (s *publishDescriptionSessionHandle) ProviderName() string  { return "" }
func (s *publishDescriptionSessionHandle) Model() string         { return "" }
func (s *publishDescriptionSessionHandle) WorkDir() string       { return "" }
func (s *publishDescriptionSessionHandle) MessageLog() ports.MessageLog {
	return s.msgLog
}
func (s *publishDescriptionSessionHandle) Cost() *llm.ResultMessage { return s.result }
func (s *publishDescriptionSessionHandle) LatestUsage() *llm.Usage  { return nil }
func (s *publishDescriptionSessionHandle) AccumulatedUsage() llm.Usage {
	return llm.Usage{}
}
func (s *publishDescriptionSessionHandle) LastControlRequest() *llm.ControlRequestMessage {
	return s.lastControl
}
func (s *publishDescriptionSessionHandle) PendingControlRequests() []*llm.ControlRequestMessage {
	if s.lastControl == nil {
		return nil
	}
	return []*llm.ControlRequestMessage{s.lastControl}
}
func (s *publishDescriptionSessionHandle) QALog() []session.QAPair { return nil }
func (s *publishDescriptionSessionHandle) LogFilePath() string     { return "" }
func (s *publishDescriptionSessionHandle) ContextHandoffThresholdTokens() int {
	return llm.DefaultSmartZoneThresholdTokens
}
func (s *publishDescriptionSessionHandle) ContextFillTokens() int           { return -1 }
func (s *publishDescriptionSessionHandle) ContextWindowTokens() int         { return 0 }
func (s *publishDescriptionSessionHandle) ContextPercentage() int           { return 0 }
func (s *publishDescriptionSessionHandle) ActiveSubAgentCount() int         { return 0 }
func (s *publishDescriptionSessionHandle) MaxActiveSubAgentFillTokens() int { return 0 }
func (s *publishDescriptionSessionHandle) ErrorDetail() string              { return "" }
func (s *publishDescriptionSessionHandle) ExitCodeDetail() string           { return "" }
func (s *publishDescriptionSessionHandle) LastStdoutAt() time.Time          { return time.Time{} }
func (s *publishDescriptionSessionHandle) StatusCh() <-chan string          { return s.statusCh }
func (s *publishDescriptionSessionHandle) AttachCh() <-chan llm.SDKMessage  { return s.attachCh }
func (s *publishDescriptionSessionHandle) Done() <-chan struct{}            { return s.done }
func (s *publishDescriptionSessionHandle) HasPendingAskUserQuestion() bool {
	return false
}
func (s *publishDescriptionSessionHandle) SendUserMessage(text string) error { return nil }
func (s *publishDescriptionSessionHandle) RespondToControl(requestID string, allow bool, reason string) error {
	return nil
}
func (s *publishDescriptionSessionHandle) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return nil
}
func (s *publishDescriptionSessionHandle) ClearPendingQuestion(requestID string)  {}
func (s *publishDescriptionSessionHandle) ResetWaitingStatus()                    {}
func (s *publishDescriptionSessionHandle) Stop() error                            { return nil }
func (s *publishDescriptionSessionHandle) Interrupt() error                       { return nil }
func (s *publishDescriptionSessionHandle) Wait()                                  {}
func (s *publishDescriptionSessionHandle) SetStatus(status session.SessionStatus) {}
func (s *publishDescriptionSessionHandle) SetLogFile(f *os.File)                  {}
func (s *publishDescriptionSessionHandle) AddCleanupFunc(fn func())               {}
func (s *publishDescriptionSessionHandle) SetHasUnansweredQuestion(v bool)        {}
func (s *publishDescriptionSessionHandle) CloseStdin()                            {}
func (s *publishDescriptionSessionHandle) SetOnToolAllowed(fn func(toolName string, input json.RawMessage)) {
}
func (s *publishDescriptionSessionHandle) SetOnFileRead(fn func(read llm.FileReadEvent))  {}
func (s *publishDescriptionSessionHandle) SetOnSubagentEvent(fn func(msg llm.SDKMessage)) {}
func (s *publishDescriptionSessionHandle) SetOnSubagentContext(fn func(sub llm.SubAgentContext)) {
}

var _ session.SessionHandle = (*publishDescriptionSessionHandle)(nil)

func newPublishDescriptionPhaseRunner(t *testing.T, output string, permissionFailure bool) *agent.PhaseRunner {
	t.Helper()

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

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
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
	return pr
}
