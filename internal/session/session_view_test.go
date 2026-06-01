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
