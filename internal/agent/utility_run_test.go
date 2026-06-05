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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

type utilityTestSession struct {
	id          string
	featureID   string
	phase       feature.Phase
	repoName    string
	done        chan struct{}
	statusCh    chan string
	attachCh    chan llm.SDKMessage
	msgLog      *session.MessageLog
	result      *llm.ResultMessage
	usage       llm.Usage
	lastControl *llm.ControlRequestMessage
	pendingAsk  bool
	contextFill int
	threshold   int
}

func newUtilityTestSession() *utilityTestSession {
	return &utilityTestSession{
		done:        make(chan struct{}),
		statusCh:    make(chan string, 1),
		attachCh:    make(chan llm.SDKMessage, 1),
		msgLog:      session.NewMessageLog(),
		contextFill: -1,
		threshold:   llm.DefaultSmartZoneThresholdTokens,
	}
}

func (s *utilityTestSession) ID() string              { return s.id }
func (s *utilityTestSession) FeatureID() string       { return s.featureID }
func (s *utilityTestSession) Phase() feature.Phase    { return s.phase }
func (s *utilityTestSession) RepoName() string        { return s.repoName }
func (s *utilityTestSession) PermCacheScope() string  { return "" }
func (s *utilityTestSession) Kind() ports.SessionKind { return ports.KindPhase }
func (s *utilityTestSession) Label() string           { return "" }
func (s *utilityTestSession) Status() session.SessionStatus {
	return session.SessionRunning
}
func (s *utilityTestSession) IsActive() bool        { return true }
func (s *utilityTestSession) Iteration() int        { return 0 }
func (s *utilityTestSession) StartedAt() time.Time  { return time.Time{} }
func (s *utilityTestSession) InitialPrompt() string { return "" }
func (s *utilityTestSession) ProviderName() string  { return "" }
func (s *utilityTestSession) Model() string         { return "" }
func (s *utilityTestSession) WorkDir() string       { return "" }
func (s *utilityTestSession) MessageLog() ports.MessageLog {
	return s.msgLog
}
func (s *utilityTestSession) Cost() *llm.ResultMessage    { return s.result }
func (s *utilityTestSession) LatestUsage() *llm.Usage     { return nil }
func (s *utilityTestSession) AccumulatedUsage() llm.Usage { return s.usage }
func (s *utilityTestSession) LastControlRequest() *llm.ControlRequestMessage {
	return s.lastControl
}
func (s *utilityTestSession) PendingControlRequests() []*llm.ControlRequestMessage {
	if s.lastControl == nil {
		return nil
	}
	return []*llm.ControlRequestMessage{s.lastControl}
}
func (s *utilityTestSession) QALog() []session.QAPair            { return nil }
func (s *utilityTestSession) LogFilePath() string                { return "" }
func (s *utilityTestSession) ContextHandoffThresholdTokens() int { return s.threshold }
func (s *utilityTestSession) ContextFillTokens() int             { return s.contextFill }
func (s *utilityTestSession) ContextWindowTokens() int           { return 0 }
func (s *utilityTestSession) ContextPercentage() int             { return 0 }
func (s *utilityTestSession) ActiveSubAgentCount() int           { return 0 }
func (s *utilityTestSession) MaxActiveSubAgentFillTokens() int   { return 0 }
func (s *utilityTestSession) ErrorDetail() string                { return "" }
func (s *utilityTestSession) ExitCodeDetail() string             { return "" }
func (s *utilityTestSession) LastStdoutAt() time.Time            { return time.Time{} }
func (s *utilityTestSession) StatusCh() <-chan string            { return s.statusCh }
func (s *utilityTestSession) AttachCh() <-chan llm.SDKMessage    { return s.attachCh }
func (s *utilityTestSession) Done() <-chan struct{}              { return s.done }
func (s *utilityTestSession) HasPendingAskUserQuestion() bool {
	return s.pendingAsk
}
func (s *utilityTestSession) SendUserMessage(text string) error { return nil }
func (s *utilityTestSession) RespondToControl(requestID string, allow bool, reason string) error {
	return nil
}
func (s *utilityTestSession) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	return nil
}
func (s *utilityTestSession) ClearPendingQuestion(requestID string)  {}
func (s *utilityTestSession) ResetWaitingStatus()                    {}
func (s *utilityTestSession) Stop() error                            { return nil }
func (s *utilityTestSession) Interrupt() error                       { return nil }
func (s *utilityTestSession) Wait()                                  {}
func (s *utilityTestSession) SetStatus(status session.SessionStatus) {}
func (s *utilityTestSession) SetLogFile(f *os.File)                  {}
func (s *utilityTestSession) AddCleanupFunc(fn func())               {}
func (s *utilityTestSession) SetHasUnansweredQuestion(v bool)        {}
func (s *utilityTestSession) CloseStdin()                            {}
func (s *utilityTestSession) SetOnToolAllowed(fn func(toolName string, input json.RawMessage)) {
}
func (s *utilityTestSession) SetOnFileRead(fn func(read llm.FileReadEvent))         {}
func (s *utilityTestSession) SetOnSubagentEvent(fn func(msg llm.SDKMessage))        {}
func (s *utilityTestSession) SetOnSubagentContext(fn func(sub llm.SubAgentContext)) {}

var _ session.SessionHandle = (*utilityTestSession)(nil)

type utilityTestPhaseRunner struct {
	pr           *PhaseRunner
	capturedOpts []BuildSessionOpts
}

func newUtilityTestPhaseRunner(t *testing.T, sess *utilityTestSession) *utilityTestPhaseRunner {
	t.Helper()

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess.id = id
		sess.featureID = featureID
		sess.phase = phase
		if len(opts) > 0 && opts[0] != nil {
			sess.repoName = opts[0].RepoName
		}
		return sess, nil
	}

	runner := &utilityTestPhaseRunner{}
	runner.pr = &PhaseRunner{
		SessionManager: sm,
		StateDir:       t.TempDir(),
	}
	runner.pr.BuildSessionFn = func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		runner.capturedOpts = append(runner.capturedOpts, opts)
		return []string{"mock"}, nil, &ports.SessionOpts{
			PIDDir:      opts.PIDDir,
			RepoName:    opts.RepoName,
			PermHandler: opts.PermHandler,
		}, nil
	}
	return runner
}

func TestPhaseRunnerRunUtilitySession_Success(t *testing.T) {
	sess := newUtilityTestSession()
	sess.msgLog.Append(mocks.AssistantTextMessage("Utility output"))
	sess.result = &llm.ResultMessage{
		Type:       "result",
		Subtype:    "success",
		Result:     "done",
		StopReason: "end_turn",
	}
	sess.usage = llm.Usage{InputTokens: 9, OutputTokens: 4}
	sess.statusCh <- "SUCCESS"

	runner := newUtilityTestPhaseRunner(t, sess)
	result, err := runner.pr.RunUtilitySession(context.Background(), UtilityRunConfig{
		FeatureID:   "feat-1",
		SessionID:   "summary-1",
		Label:       "summary generation",
		Model:       "haiku",
		Prompt:      "summarize",
		WorkDir:     "/tmp/work",
		Timeout:     time.Second,
		Phase:       feature.PhaseResearch,
		RequireText: true,
	})
	if err != nil {
		t.Fatalf("RunUtilitySession() error = %v", err)
	}
	if result.Text != "Utility output" {
		t.Errorf("result.Text = %q, want %q", result.Text, "Utility output")
	}
	if result.Status != BoundedHelperStatusCompleted {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
	}
	if result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 4 {
		t.Errorf("result.Usage = %+v, want input=9 output=4", result.Usage)
	}
	if len(runner.capturedOpts) != 1 {
		t.Fatalf("captured opts = %d, want 1", len(runner.capturedOpts))
	}
	opts := runner.capturedOpts[0]
	if opts.WorkDir != "/tmp/work" {
		t.Errorf("BuildSessionOpts.WorkDir = %q, want %q", opts.WorkDir, "/tmp/work")
	}
	if opts.Phase != feature.PhaseResearch {
		t.Errorf("BuildSessionOpts.Phase = %v, want %v", opts.Phase, feature.PhaseResearch)
	}
}

func TestPhaseRunnerRunUtilitySession_TimesOut(t *testing.T) {
	sess := newUtilityTestSession()
	runner := newUtilityTestPhaseRunner(t, sess)

	result, err := runner.pr.RunUtilitySession(context.Background(), UtilityRunConfig{
		SessionID: "summary-timeout",
		Label:     "summary generation",
		Model:     "haiku",
		Prompt:    "summarize",
		Timeout:   10 * time.Millisecond,
		Phase:     feature.PhaseResearch,
	})
	if err == nil {
		t.Fatal("RunUtilitySession() error = nil, want timeout")
	}
	if !contextDeadlineExceeded(err) {
		t.Errorf("RunUtilitySession() error = %v, want wrapped deadline exceeded", err)
	}
	if result == nil {
		t.Fatal("RunUtilitySession() result = nil, want timeout snapshot")
	}
	if result.Status != BoundedHelperStatusTimedOut {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusTimedOut)
	}
}

func TestPhaseRunnerRunUtilitySession_RequiresText(t *testing.T) {
	sess := newUtilityTestSession()
	sess.result = &llm.ResultMessage{
		Type:       "result",
		Subtype:    "success",
		Result:     "done",
		StopReason: "end_turn",
	}
	sess.statusCh <- "SUCCESS"

	runner := newUtilityTestPhaseRunner(t, sess)
	result, err := runner.pr.RunUtilitySession(context.Background(), UtilityRunConfig{
		SessionID:   "summary-empty",
		Label:       "summary generation",
		Model:       "haiku",
		Prompt:      "summarize",
		Timeout:     time.Second,
		Phase:       feature.PhaseResearch,
		RequireText: true,
	})
	if err == nil {
		t.Fatal("RunUtilitySession() error = nil, want empty output failure")
	}
	if result == nil {
		t.Fatal("RunUtilitySession() result = nil, want empty-output snapshot")
	}
	if result.Status != BoundedHelperStatusEmptyOutput {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusEmptyOutput)
	}
}

func TestPhaseRunnerRunUtilitySession_FailsOnPermissionRequest(t *testing.T) {
	sess := newUtilityTestSession()
	sess.attachCh <- mocks.ControlRequestMsg("perm-1", "Bash")

	runner := newUtilityTestPhaseRunner(t, sess)
	result, err := runner.pr.RunUtilitySession(context.Background(), UtilityRunConfig{
		SessionID: "summary-perm",
		Label:     "summary generation",
		Model:     "haiku",
		Prompt:    "summarize",
		Timeout:   time.Second,
		Phase:     feature.PhaseResearch,
	})
	if err == nil {
		t.Fatal("RunUtilitySession() error = nil, want permission failure")
	}
	if result == nil {
		t.Fatal("RunUtilitySession() result = nil, want permission snapshot")
	}
	if result.Status != BoundedHelperStatusPermissionRequired {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusPermissionRequired)
	}
}

func contextDeadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
