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

package session

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// stubProtocol is a minimal llm.Protocol for testing session accessors.
type stubProtocol struct {
	sessionID      string
	transcriptPath string
}

func (p *stubProtocol) SetStdin(io.Writer)                                           {}
func (p *stubProtocol) Handshake(context.Context) error                              { return nil }
func (p *stubProtocol) ParseLine([]byte) ([]llm.SDKMessage, error)                   { return nil, nil }
func (p *stubProtocol) SendUserMessage(string) error                                 { return nil }
func (p *stubProtocol) RespondToControl(string, bool, json.RawMessage, string) error { return nil }
func (p *stubProtocol) RespondToHook(string) error                                   { return nil }
func (p *stubProtocol) Interrupt() error                                             { return llm.ErrNotSupported }
func (p *stubProtocol) RespondToAskUser(string, json.RawMessage, map[string]string, map[string]llm.AskUserAnnotation) error {
	return nil
}
func (p *stubProtocol) SessionID() string      { return p.sessionID }
func (p *stubProtocol) TranscriptPath() string { return p.transcriptPath }
func (p *stubProtocol) Close() error           { return nil }

func TestSessionImplementsSessionView(t *testing.T) {
	// Compile-time check is in session_view.go:
	//   var _ SessionView = (*Session)(nil)
	// This test exercises runtime behavior:
	var sv SessionView = NewSession("test-id", "feat-id", feature.PhaseResearch)
	if sv.ID() != "test-id" {
		t.Errorf("ID() = %q, want %q", sv.ID(), "test-id")
	}
	if sv.FeatureID() != "feat-id" {
		t.Errorf("FeatureID() = %q, want %q", sv.FeatureID(), "feat-id")
	}
	if sv.Phase() != feature.PhaseResearch {
		t.Errorf("Phase() = %v, want %v", sv.Phase(), feature.PhaseResearch)
	}
}

func TestSessionViewContextHandoffThresholdTokens(t *testing.T) {
	var sv SessionView = &Session{contextHandoffThresholdTokens: 123_456}
	if got := sv.ContextHandoffThresholdTokens(); got != 123_456 {
		t.Errorf("ContextHandoffThresholdTokens() = %d, want %d", got, 123_456)
	}

	var fallback SessionView = NewSession("id", "fid", feature.PhaseImplement)
	if got := fallback.ContextHandoffThresholdTokens(); got != llm.DefaultSmartZoneThresholdTokens {
		t.Errorf("ContextHandoffThresholdTokens() fallback = %d, want %d", got, llm.DefaultSmartZoneThresholdTokens)
	}
}

func TestStartSessionExposesContextHandoffThresholdWhenDisabled(t *testing.T) {
	eventCh := make(chan any, 10)
	m := NewManager(eventCh)
	sess, err := m.StartSession(
		"id",
		"fid",
		feature.PhaseImplement,
		[]string{"sh", "-c", `printf '{"type":"result","result":{"type":"result","subtype":"success","session_id":"sid"}}\n'`},
		t.TempDir(),
		nil,
		&SessionOpts{
			ContextHandoffThresholdTokens: 222_000,
			ContextHandoffDisabled:        true,
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	defer sess.Wait()

	if got := sess.ContextHandoffThresholdTokens(); got != 222_000 {
		t.Errorf("ContextHandoffThresholdTokens() = %d, want %d", got, 222_000)
	}
}

func TestSessionAccessors(t *testing.T) {
	s := NewSession("id", "fid", feature.PhaseImplement)

	// Verify constructor defaults
	if s.ID() != "id" {
		t.Errorf("ID() = %q, want %q", s.ID(), "id")
	}
	if s.FeatureID() != "fid" {
		t.Errorf("FeatureID() = %q, want %q", s.FeatureID(), "fid")
	}
	if s.Phase() != feature.PhaseImplement {
		t.Errorf("Phase() = %v, want %v", s.Phase(), feature.PhaseImplement)
	}
	if s.Status() != SessionRunning {
		t.Errorf("Status() = %v, want %v", s.Status(), SessionRunning)
	}
	if s.MessageLog() == nil {
		t.Error("MessageLog() should not be nil")
	}

	// Set fields internally (same package) and verify accessor output
	s.repoName = "my-repo"
	if s.RepoName() != "my-repo" {
		t.Errorf("RepoName() = %q, want %q", s.RepoName(), "my-repo")
	}

	s.iteration = 3
	if s.Iteration() != 3 {
		t.Errorf("Iteration() = %d, want 3", s.Iteration())
	}

	s.initialPrompt = "do the thing"
	if s.InitialPrompt() != "do the thing" {
		t.Errorf("InitialPrompt() = %q, want %q", s.InitialPrompt(), "do the thing")
	}

	s.providerName = "codex"
	if s.ProviderName() != "codex" {
		t.Errorf("ProviderName() = %q, want %q", s.ProviderName(), "codex")
	}

	s.model = "opus"
	if s.Model() != "opus" {
		t.Errorf("Model() = %q, want %q", s.Model(), "opus")
	}

	now := time.Now()
	s.startedAt = now
	if !s.StartedAt().Equal(now) {
		t.Errorf("StartedAt() = %v, want %v", s.StartedAt(), now)
	}

	s.protocol = &stubProtocol{sessionID: "cs-123"}
	if s.SessionID() != "cs-123" {
		t.Errorf("SessionID() = %q, want %q", s.SessionID(), "cs-123")
	}

	s.workDir = "/tmp/work"
	if s.WorkDir() != "/tmp/work" {
		t.Errorf("WorkDir() = %q, want %q", s.WorkDir(), "/tmp/work")
	}
}

func TestLogFilePath(t *testing.T) {
	s := NewSession("id", "fid", 0)

	// No log file set
	if s.LogFilePath() != "" {
		t.Errorf("LogFilePath() = %q, want empty", s.LogFilePath())
	}

	// Set a log file
	f, err := os.CreateTemp("", "test-log")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	s.SetLogFile(f)
	if s.LogFilePath() != f.Name() {
		t.Errorf("LogFilePath() = %q, want %q", s.LogFilePath(), f.Name())
	}
}

func TestStatusChReturnsReceiveOnly(t *testing.T) {
	s := NewSession("id", "fid", 0)
	ch := s.StatusCh() // <-chan string

	// Send via SendStatus (internal helper)
	go s.SendStatus("SUCCESS")

	select {
	case status := <-ch:
		if status != "SUCCESS" {
			t.Errorf("received %q from StatusCh, want %q", status, "SUCCESS")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for StatusCh")
	}
}

func TestSetterMethods(t *testing.T) {
	s := NewSession("id", "fid", 0)

	s.SetModel("opus")
	if s.Model() != "opus" {
		t.Errorf("Model() = %q after SetModel, want %q", s.Model(), "opus")
	}

	cost := &llm.ResultMessage{TotalCostUSD: 1.5}
	s.SetCost(cost)
	if s.Cost() == nil || s.Cost().TotalCostUSD != 1.5 {
		t.Errorf("Cost().TotalCostUSD = %v, want 1.5", s.Cost())
	}

	usage := &llm.Usage{InputTokens: 100, OutputTokens: 50}
	s.SetLatestUsage(usage)
	if s.LatestUsage() == nil || s.LatestUsage().InputTokens != 100 {
		t.Errorf("LatestUsage() = %v, want InputTokens=100", s.LatestUsage())
	}

	s.SetIteration(5)
	if s.Iteration() != 5 {
		t.Errorf("Iteration() = %d after SetIteration, want 5", s.Iteration())
	}

	s.SetRepoName("test-repo")
	if s.RepoName() != "test-repo" {
		t.Errorf("RepoName() = %q after SetRepoName, want %q", s.RepoName(), "test-repo")
	}

	cr := &llm.ControlRequestMessage{RequestID: "req-1"}
	s.SetLastControlRequest(cr)
	if s.LastControlRequest() == nil || s.LastControlRequest().RequestID != "req-1" {
		t.Errorf("LastControlRequest() = %v, want RequestID=req-1", s.LastControlRequest())
	}
}

func TestContextWindowTokens(t *testing.T) {
	s := NewSession("id", "fid", 0)
	if got := s.ContextWindowTokens(); got != 0 {
		t.Fatalf("ContextWindowTokens() before usage = %d, want 0", got)
	}
	s.SetLatestUsage(&llm.Usage{InputTokens: 100, ContextWindow: 200_000})
	if got := s.ContextWindowTokens(); got != 200_000 {
		t.Fatalf("ContextWindowTokens() = %d, want 200000", got)
	}
}

// TestSubAgentContextLifecycle verifies the per-sub-thread storage, the active
// count, and the max-active-fill getter, with activity tracked by lifecycle
// (a Done snapshot marks a sub-thread inactive). It also confirms the main
// Smart Zone fill is never moved by sub-agent snapshots.
func TestSubAgentContextLifecycle(t *testing.T) {
	s := NewSession("id", "fid", 0)
	s.SetLatestUsage(&llm.Usage{ContextTotalTokens: 40_000, ContextWindow: 200_000})
	mainFillBefore := s.ContextFillTokens()

	if got := s.ActiveSubAgentCount(); got != 0 {
		t.Fatalf("ActiveSubAgentCount() with no sub-agents = %d, want 0", got)
	}
	if got := s.MaxActiveSubAgentFillTokens(); got != 0 {
		t.Fatalf("MaxActiveSubAgentFillTokens() with no sub-agents = %d, want 0", got)
	}

	record := func(sub llm.SubAgentContext) {
		s.mu.Lock()
		s.recordSubAgentContextLocked(sub)
		s.mu.Unlock()
	}

	record(llm.SubAgentContext{ThreadID: "sub-1", FillTokens: 60_000, WindowTokens: 200_000})
	record(llm.SubAgentContext{ThreadID: "sub-2", FillTokens: 90_000, WindowTokens: 200_000})

	if got := s.ActiveSubAgentCount(); got != 2 {
		t.Fatalf("ActiveSubAgentCount() = %d, want 2", got)
	}
	if got := s.MaxActiveSubAgentFillTokens(); got != 90_000 {
		t.Fatalf("MaxActiveSubAgentFillTokens() = %d, want 90000", got)
	}

	// A Done snapshot (lifecycle) marks sub-2 inactive without removing its
	// last-known fill; the count drops and the max falls back to sub-1.
	record(llm.SubAgentContext{ThreadID: "sub-2", Done: true})
	if got := s.ActiveSubAgentCount(); got != 1 {
		t.Fatalf("ActiveSubAgentCount() after Done = %d, want 1", got)
	}
	if got := s.MaxActiveSubAgentFillTokens(); got != 60_000 {
		t.Fatalf("MaxActiveSubAgentFillTokens() after Done = %d, want 60000", got)
	}

	// The main-only Smart Zone fill must be untouched by sub-agent activity.
	if got := s.ContextFillTokens(); got != mainFillBefore {
		t.Fatalf("ContextFillTokens() moved by sub-agents: got %d, want %d", got, mainFillBefore)
	}
}

// TestSubAgentDoneIsStickyAgainstLateFill guards the re-activation race: some
// providers flush a final fill snapshot for a sub-thread AFTER its
// turn/completed. That late fill must NOT resurrect a sub-agent that lifecycle
// already retired, otherwise the active count never returns to 0 after a
// fan-out finishes.
func TestSubAgentDoneIsStickyAgainstLateFill(t *testing.T) {
	s := NewSession("id", "fid", 0)
	record := func(sub llm.SubAgentContext) {
		s.mu.Lock()
		s.recordSubAgentContextLocked(sub)
		s.mu.Unlock()
	}

	record(llm.SubAgentContext{ThreadID: "sub-1", FillTokens: 60_000, WindowTokens: 200_000})
	record(llm.SubAgentContext{ThreadID: "sub-1", Done: true})
	if got := s.ActiveSubAgentCount(); got != 0 {
		t.Fatalf("ActiveSubAgentCount() after Done = %d, want 0", got)
	}
	// Late fill snapshot arrives out of order — must stay inactive.
	record(llm.SubAgentContext{ThreadID: "sub-1", FillTokens: 70_000, WindowTokens: 200_000})
	if got := s.ActiveSubAgentCount(); got != 0 {
		t.Fatalf("ActiveSubAgentCount() after late fill following Done = %d, want 0 (must not resurrect)", got)
	}
	if got := s.MaxActiveSubAgentFillTokens(); got != 0 {
		t.Fatalf("MaxActiveSubAgentFillTokens() after late fill following Done = %d, want 0", got)
	}
}

// TestSubAgentRecencyBackstop verifies the recency TTL safety net: a sub-thread
// that emits a fill snapshot but never a Done (e.g. it was interrupted/killed)
// is treated as inactive once its last update goes stale, so the active count
// still falls to 0 even without a lifecycle signal.
func TestSubAgentRecencyBackstop(t *testing.T) {
	s := NewSession("id", "fid", 0)
	now := time.Unix(1_000_000, 0)
	s.mu.Lock()
	s.subAgentClock = func() time.Time { return now }
	s.recordSubAgentContextLocked(llm.SubAgentContext{ThreadID: "sub-1", FillTokens: 60_000, WindowTokens: 200_000})
	s.mu.Unlock()

	if got := s.ActiveSubAgentCount(); got != 1 {
		t.Fatalf("ActiveSubAgentCount() fresh = %d, want 1", got)
	}

	// Advance well past the TTL with no Done: the entry self-clears.
	now = now.Add(subAgentActiveTTL + time.Second)
	if got := s.ActiveSubAgentCount(); got != 0 {
		t.Fatalf("ActiveSubAgentCount() after TTL = %d, want 0", got)
	}
	if got := s.MaxActiveSubAgentFillTokens(); got != 0 {
		t.Fatalf("MaxActiveSubAgentFillTokens() after TTL = %d, want 0", got)
	}
}

func TestSubAgentContextFromTask(t *testing.T) {
	cases := []struct {
		name string
		msg  llm.SDKMessage
		want llm.SubAgentContext
		ok   bool
	}{
		{"started", llm.SDKMessage{TaskStarted: &llm.TaskStartedMessage{TaskID: "t1"}}, llm.SubAgentContext{ThreadID: "t1"}, true},
		{"progress", llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "t1", Usage: &llm.TaskUsage{TotalTokens: 34_000}}}, llm.SubAgentContext{ThreadID: "t1", FillTokens: 34_000}, true},
		{"progress no usage", llm.SDKMessage{TaskProgress: &llm.TaskProgressMessage{TaskID: "t1"}}, llm.SubAgentContext{ThreadID: "t1"}, true},
		{"notification", llm.SDKMessage{TaskNotification: &llm.TaskNotificationMessage{TaskID: "t1"}}, llm.SubAgentContext{ThreadID: "t1", Done: true}, true},
		{"missing id", llm.SDKMessage{TaskStarted: &llm.TaskStartedMessage{}}, llm.SubAgentContext{}, false},
		{"non-task", llm.SDKMessage{Type: "assistant"}, llm.SubAgentContext{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := subAgentContextFromTask(tc.msg)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("subAgentContextFromTask = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestClaudeTaskEventsFeedSubAgentGetters drives a started→progress→notification
// lifecycle through the real session pipeline and asserts the Task events reach
// the per-sub-thread store (so the Sub-agents line works for Claude) without
// moving the main Smart Zone fill.
func TestClaudeTaskEventsFeedSubAgentGetters(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"system","subtype":"task_started","task_id":"t1","tool_use_id":"u1","task_type":"local_agent","session_id":"s1"}`,
		`{"type":"system","subtype":"task_progress","task_id":"t1","tool_use_id":"u1","last_tool_name":"Grep","usage":{"total_tokens":12000,"tool_uses":2,"duration_ms":500},"session_id":"s1"}`,
		`{"type":"system","subtype":"task_progress","task_id":"t1","tool_use_id":"u1","last_tool_name":"Read","usage":{"total_tokens":34000,"tool_uses":5,"duration_ms":900},"session_id":"s1"}`,
		`{"type":"system","subtype":"task_notification","task_id":"t1","tool_use_id":"u1","status":"completed","usage":{"total_tokens":40000,"tool_uses":6,"duration_ms":1500},"session_id":"s1"}`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}`,
	}

	s := NewSession("claude-subagent-test", "feat-1", feature.PhaseImplement)
	var mu sync.Mutex
	var captured []llm.SubAgentContext
	s.SetOnSubagentContext(func(sub llm.SubAgentContext) {
		mu.Lock()
		captured = append(captured, sub)
		mu.Unlock()
	})

	runSessionWithStdoutLines(t, s, lines, nil)

	mu.Lock()
	defer mu.Unlock()
	want := []llm.SubAgentContext{
		{ThreadID: "t1"},
		{ThreadID: "t1", FillTokens: 12_000},
		{ThreadID: "t1", FillTokens: 34_000},
		{ThreadID: "t1", Done: true},
	}
	if len(captured) != len(want) {
		t.Fatalf("onSubagentContext fired %d times, want %d: %+v", len(captured), len(want), captured)
	}
	for i := range want {
		if captured[i] != want[i] {
			t.Errorf("captured[%d] = %+v, want %+v", i, captured[i], want[i])
		}
	}
	// Notification retired the only sub-agent, and no main usage_update arrived.
	if got := s.ActiveSubAgentCount(); got != 0 {
		t.Errorf("ActiveSubAgentCount() after notification = %d, want 0", got)
	}
	if got := s.ContextFillTokens(); got != -1 {
		t.Errorf("ContextFillTokens() = %d, want -1 (Task events must not establish main fill)", got)
	}
}

// TestClaudeSubAgentTurnDoesNotMoveMainFill: a sub-agent turn must not move the main fill.
func TestClaudeSubAgentTurnDoesNotMoveMainFill(t *testing.T) {
	t.Parallel()
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":1,"cache_read_input_tokens":44000,"cache_creation_input_tokens":1000},"content":[]},"session_id":"s1"}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_sub","message":{"role":"assistant","usage":{"input_tokens":3,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200},"content":[]},"session_id":"s1"}`,
		`{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}`,
	}

	s := NewSession("claude-mainfill-test", "feat-1", feature.PhaseImplement)
	runSessionWithStdoutLines(t, s, lines, nil)

	// Last turn was a sub-agent (fill 5200); without the gate it would pin Main there.
	if got := s.ContextFillTokens(); got != 45001 {
		t.Errorf("ContextFillTokens() = %d, want 45001 (sub-agent turn must not move main fill)", got)
	}
}

func TestManagerGetSessionReturnsSessionView(t *testing.T) {
	mgr := NewManager(nil)
	s := NewSession("s1", "f1", feature.PhaseResearch)
	mgr.RegisterTestSession(s)

	var sv SessionView = mgr.GetSession("s1")
	if sv == nil {
		t.Fatal("GetSession returned nil")
	}
	if sv.ID() != "s1" {
		t.Errorf("ID() = %q, want %q", sv.ID(), "s1")
	}
	if sv.FeatureID() != "f1" {
		t.Errorf("FeatureID() = %q, want %q", sv.FeatureID(), "f1")
	}
}

func TestManagerActiveSessionsReturnsSessionView(t *testing.T) {
	mgr := NewManager(nil)
	s := NewSession("s1", "f1", feature.PhaseResearch)
	s.status = SessionRunning
	mgr.RegisterTestSession(s)

	sessions := mgr.ActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("ActiveSessions() returned %d sessions, want 1", len(sessions))
	}
	var sv SessionView = sessions[0]
	if sv.ID() != "s1" {
		t.Errorf("ID() = %q, want %q", sv.ID(), "s1")
	}
}

func TestManagerFeatureSessionsReturnsSessionView(t *testing.T) {
	mgr := NewManager(nil)
	s1 := NewSession("s1", "f1", feature.PhaseResearch)
	s2 := NewSession("s2", "f2", feature.PhasePlan)
	mgr.RegisterTestSession(s1)
	mgr.RegisterTestSession(s2)

	sessions := mgr.FeatureSessions("f1")
	if len(sessions) != 1 {
		t.Fatalf("FeatureSessions('f1') returned %d sessions, want 1", len(sessions))
	}
	var sv SessionView = sessions[0]
	if sv.FeatureID() != "f1" {
		t.Errorf("FeatureID() = %q, want %q", sv.FeatureID(), "f1")
	}
}
