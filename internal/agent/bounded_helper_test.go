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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunBoundedHelper_SuccessWithoutPhaseComplete(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		mocks.AssistantTextMessage("Research complete"),
		{
			Type: testResultMessageType,
			Result: &llm.ResultMessage{
				Type:         testResultMessageType,
				Subtype:      testResultSuccessValue,
				Result:       testResultSuccessValue,
				StopReason:   testStopReasonEndTurn,
				TotalCostUSD: 0.12,
				Usage: &llm.Usage{
					InputTokens:  11,
					OutputTokens: 7,
				},
			},
		},
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:     "feature-scout-1",
		FeatureID:     "feature-1",
		Phase:         feature.PhaseResearch,
		Model:         "test-model",
		Prompt:        "Summarize the provided files.",
		SystemPrompt:  "You are a bounded research helper.",
		WorkDir:       workDir,
		Timeout:       2 * time.Second,
		EffortLevel:   llm.EffortMedium,
		PermHandler:   &permission.ReadOnlyHandler{},
		RequireOutput: true,
	})
	if err != nil {
		t.Fatalf("RunBoundedHelper() error = %v", err)
	}
	if result.Status != BoundedHelperStatusCompleted {
		t.Fatalf("result.Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
	}
	if result.Output != "Research complete" {
		t.Errorf("result.Output = %q, want %q", result.Output, "Research complete")
	}
	if result.Result == nil || result.Result.StopReason != testStopReasonEndTurn {
		t.Fatalf("result.Result = %#v, want end_turn success result", result.Result)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 {
		t.Errorf("result.Usage = %+v, want input=11 output=7", result.Usage)
	}
}

func TestRunBoundedHelperRecordsIndividualSessionCost(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		mocks.AssistantTextMessage("Review complete"),
		{
			Type: testResultMessageType,
			Result: &llm.ResultMessage{
				Type:         testResultMessageType,
				Subtype:      testResultSuccessValue,
				Result:       testResultSuccessValue,
				StopReason:   testStopReasonEndTurn,
				TotalCostUSD: 0.12,
			},
		},
	})
	defer cleanup()

	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:              "feature-1",
		Name:            "feature-1",
		Status:          feature.StatusPlanning,
		ActiveTimingKey: "phase-1-plan",
		SchemaVersion:   feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}
	pr.FeatureStore = store

	_, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:     "feature-1-planreview-architecture-01",
		FeatureID:     "feature-1",
		Phase:         feature.PhasePlan,
		Label:         "review helper",
		ObserverPhase: "review",
		Model:         "test-model",
		Prompt:        "Review the plan.",
		SystemPrompt:  "You are a bounded plan reviewer.",
		WorkDir:       workDir,
		RepoName:      "repo-a",
		Timeout:       2 * time.Second,
		EffortLevel:   llm.EffortMedium,
		PermHandler:   &permission.ReadOnlyHandler{},
	})
	if err != nil {
		t.Fatalf("RunBoundedHelper() error = %v", err)
	}

	updated, err := store.Load("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.PhaseCost("phase-1-plan"); got != 0.12 {
		t.Errorf("PhaseCost(phase-1-plan) = %v, want 0.12", got)
	}
	if len(updated.SessionCosts) != 1 {
		t.Fatalf("len(SessionCosts) = %d, want 1", len(updated.SessionCosts))
	}
	got := updated.SessionCosts[0]
	if got.SessionID != "feature-1-planreview-architecture-01" {
		t.Errorf("SessionID = %q, want feature-1-planreview-architecture-01", got.SessionID)
	}
	if got.PhaseKey != "phase-1-plan" {
		t.Errorf("PhaseKey = %q, want phase-1-plan", got.PhaseKey)
	}
	if got.ObserverPhase != "review" {
		t.Errorf("ObserverPhase = %q, want review", got.ObserverPhase)
	}
	if got.RepoName != "repo-a" {
		t.Errorf("RepoName = %q, want repo-a", got.RepoName)
	}
	if got.CostUSD != 0.12 {
		t.Errorf("CostUSD = %v, want 0.12", got.CostUSD)
	}
}

func TestRunBoundedHelper_FailsOnPendingAskUserQuestion(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		askUserControlRequest("ask-1"),
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:    "feature-scout-ask",
		FeatureID:    "feature-1",
		Phase:        feature.PhaseResearch,
		Model:        "test-model",
		Prompt:       "Ask if more detail is needed.",
		SystemPrompt: "You are a bounded research helper.",
		WorkDir:      workDir,
		Timeout:      2 * time.Second,
		EffortLevel:  llm.EffortMedium,
		PermHandler:  &permission.ReadOnlyHandler{},
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want AskUserQuestion failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusAskedUser {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusAskedUser)
	}
	if !strings.Contains(err.Error(), "asked for user input") {
		t.Errorf("error = %q, want AskUserQuestion context", err)
	}
}

func TestRunBoundedHelper_FailsOnPermissionRequest(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		mocks.ControlRequestMsg("perm-1", "Bash"),
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:    "feature-scout-perm",
		FeatureID:    "feature-1",
		Phase:        feature.PhaseResearch,
		Model:        "test-model",
		Prompt:       "Run a shell command.",
		SystemPrompt: "You are a bounded research helper.",
		WorkDir:      workDir,
		Timeout:      2 * time.Second,
		EffortLevel:  llm.EffortMedium,
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want permission failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusPermissionRequired {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusPermissionRequired)
	}
	if !strings.Contains(err.Error(), "requested tool permission") {
		t.Errorf("error = %q, want permission context", err)
	}
}

func TestRunBoundedHelper_FailsOnEmptyRequiredOutput(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		{
			Type: testResultMessageType,
			Result: &llm.ResultMessage{
				Type:       testResultMessageType,
				Subtype:    testResultSuccessValue,
				StopReason: testStopReasonEndTurn,
				Usage: &llm.Usage{
					InputTokens:  3,
					OutputTokens: 0,
				},
			},
		},
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:     "feature-scout-empty",
		FeatureID:     "feature-1",
		Phase:         feature.PhaseResearch,
		Model:         "test-model",
		Prompt:        "Return nothing.",
		SystemPrompt:  "You are a bounded research helper.",
		WorkDir:       workDir,
		Timeout:       2 * time.Second,
		EffortLevel:   llm.EffortMedium,
		PermHandler:   &permission.ReadOnlyHandler{},
		RequireOutput: true,
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want empty output failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusEmptyOutput {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusEmptyOutput)
	}
	if !strings.Contains(err.Error(), "without output") {
		t.Errorf("error = %q, want empty output context", err)
	}
}

// boundedNudgeSession is a SessionHandle test double for the bounded-helper
// statusCh nudge arm. It exposes a controllable statusCh and a never-closed
// Done channel, records nudge messages, and returns a settable result.
type boundedNudgeSession struct {
	*utilityTestSession
	statusC   chan string
	doneC     chan struct{}
	result    *llm.ResultMessage
	nudges    chan string
	stopCalls int
}

func newBoundedNudgeSession(result *llm.ResultMessage) *boundedNudgeSession {
	return &boundedNudgeSession{
		utilityTestSession: newUtilityTestSession(),
		statusC:            make(chan string, 4),
		doneC:              make(chan struct{}),
		result:             result,
		nudges:             make(chan string, 4),
	}
}

func (s *boundedNudgeSession) StatusCh() <-chan string  { return s.statusC }
func (s *boundedNudgeSession) Done() <-chan struct{}    { return s.doneC }
func (s *boundedNudgeSession) Cost() *llm.ResultMessage { return s.result }
func (s *boundedNudgeSession) SendUserMessage(text string) error {
	s.nudges <- text
	return nil
}
func (s *boundedNudgeSession) Stop() error { s.stopCalls++; return nil }

// TestRunBoundedHelper_NudgesOnMissingArtifactsThenFinalizes proves the bounded
// helper's statusCh arm nudges the same live session when it ends a turn without
// phase_complete (capability armed), then finalizes as completed once the nudged
// turn writes the marker.
func TestRunBoundedHelper_NudgesOnMissingArtifactsThenFinalizes(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:            "helper-nudge",
			workDir:              t.TempDir(),
			phaseCompleteDir:     phaseDir,
			finishOrViolateNudge: true,
		})
		resultCh <- result
	}()

	// First turn ends without phase_complete → expect a nudge.
	sess.statusC <- agentStatusSuccess
	select {
	case <-sess.nudges:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a finish-or-violate nudge on missing phase_complete")
	}

	// The nudged turn writes the marker, then ends its turn again.
	if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	sess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
}

// TestRunBoundedHelper_NudgeWritesContractArtifactsThenCompletes proves the
// nudge handles the full contract scenario: with a contractRole set, the nudged
// turn writing only phase_complete is not enough — once it also writes the
// required review-feedback artifact, finalization is BoundedHelperStatusCompleted
// rather than a protocol violation.
func TestRunBoundedHelper_NudgeWritesContractArtifactsThenCompletes(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:            "helper-contract",
			workDir:              t.TempDir(),
			phaseCompleteDir:     phaseDir,
			contractPhase:        feature.PhaseReview,
			contractRole:         RoleImplementationReviewCraft,
			finishOrViolateNudge: true,
		})
		resultCh <- result
	}()

	// First turn ends without the marker → expect a nudge.
	sess.statusC <- agentStatusSuccess
	select {
	case <-sess.nudges:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a finish-or-violate nudge on missing phase_complete")
	}

	// The nudged turn writes BOTH the required contract artifact and the
	// marker, then ends its turn.
	writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
	if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	sess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
}

// TestRunBoundedHelper_DoneBranchDoesNotNudge proves the Done() arm never
// nudges: a session that exits without the marker finalizes immediately as a
// protocol violation, even with the capability armed.
func TestRunBoundedHelper_DoneBranchDoesNotNudge(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:            "helper-done",
			workDir:              t.TempDir(),
			phaseCompleteDir:     phaseDir,
			contractRole:         RoleImplementationReviewCraft,
			finishOrViolateNudge: true,
		})
		resultCh <- result
	}()

	// Process exits without the marker — the Done arm must finalize, not nudge.
	close(sess.doneC)

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusProtocolViolation {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusProtocolViolation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize on Done")
	}

	select {
	case <-sess.nudges:
		t.Fatal("Done arm must never send a nudge")
	default:
	}

	// The Done arm must finalize via the single deferred Stop, never an
	// eager Stop inside the loop. A regression that routed Done through the
	// nudge/decide path would change this count.
	if sess.stopCalls != 1 {
		t.Errorf("stopCalls = %d, want 1 (single deferred Stop)", sess.stopCalls)
	}
}

func TestRunBoundedHelper_RetriesEarlyInfrastructureFailure(t *testing.T) {
	phaseDir := t.TempDir()
	workDir := t.TempDir()
	var sessionIDs []string

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sessionIDs = append(sessionIDs, id)
		if len(sessionIDs) == 1 {
			return newTerminalStatusTestSession(ports.SessionFailed), nil
		}
		writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
		if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		sess := newUtilityTestSession()
		sess.result = &llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn}
		sess.statusCh <- agentStatusSuccess
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID:        "review-helper",
		workDir:          workDir,
		phaseCompleteDir: phaseDir,
		contractPhase:    feature.PhaseReview,
		contractRole:     RoleImplementationReviewCraft,
	})
	if err != nil {
		t.Fatalf("runBoundedHelperSession() error = %v", err)
	}
	if result.Status != BoundedHelperStatusCompleted {
		t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
	}
	wantSessionIDs := []string{"review-helper", "review-helper-retry-02"}
	if !reflect.DeepEqual(sessionIDs, wantSessionIDs) {
		t.Fatalf("session IDs = %#v, want %#v", sessionIDs, wantSessionIDs)
	}
}

// TestRunBoundedHelper_NudgeCapThenViolation proves the nudge budget is bounded:
// after maxFinishOrViolateNudges nudges with the marker still missing, the run
// finalizes as a protocol violation.
func TestRunBoundedHelper_NudgeCapThenViolation(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:            "helper-cap",
			workDir:              t.TempDir(),
			phaseCompleteDir:     phaseDir,
			contractRole:         RoleImplementationReviewCraft,
			finishOrViolateNudge: true,
		})
		resultCh <- result
	}()

	// Each end_turn without the marker, up to the cap, draws a nudge.
	for i := 0; i < maxFinishOrViolateNudges; i++ {
		sess.statusC <- agentStatusSuccess
		select {
		case <-sess.nudges:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected nudge %d", i+1)
		}
	}

	// One more end_turn beyond the cap finalizes as a protocol violation.
	sess.statusC <- agentStatusSuccess
	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusProtocolViolation {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusProtocolViolation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for protocol violation after nudge cap")
	}

	select {
	case <-sess.nudges:
		t.Fatal("no extra nudge expected beyond the cap")
	default:
	}
}

// boundedBgTaskSession extends boundedNudgeSession with test-controlled
// live-background-task count and stdout-activity timestamp, mirroring a
// session whose agent spawned background Task subagents.
type boundedBgTaskSession struct {
	*boundedNudgeSession
	liveTasks    atomic.Int32
	lastStdoutNs atomic.Int64
}

func newBoundedBgTaskSession(result *llm.ResultMessage) *boundedBgTaskSession {
	s := &boundedBgTaskSession{boundedNudgeSession: newBoundedNudgeSession(result)}
	s.lastStdoutNs.Store(time.Now().UnixNano())
	return s
}

func (s *boundedBgTaskSession) LiveBackgroundTaskCount() int { return int(s.liveTasks.Load()) }
func (s *boundedBgTaskSession) LastStdoutAt() time.Time      { return time.Unix(0, s.lastStdoutNs.Load()) }

// TestRunBoundedHelper_DefersOnLiveBackgroundTasks proves the bounded helper
// waiter defers instead of finalizing when the helper ends its turn without
// phase_complete while its background subagents are still running — the CLI
// re-invokes the agent when they complete. Nudge capability is unarmed,
// matching providers where the yield-for-tasks pattern showed up.
func TestRunBoundedHelper_DefersOnLiveBackgroundTasks(t *testing.T) {
	withBackgroundTaskPollInterval(t, 5*time.Millisecond)

	phaseDir := t.TempDir()
	sess := newBoundedBgTaskSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})
	sess.liveTasks.Store(3)

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-bg-defer",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
	}()

	// First turn ends without the marker while tasks are live → defer.
	sess.statusC <- agentStatusSuccess
	select {
	case result := <-resultCh:
		t.Fatalf("runBoundedHelperSession() finalized %q; want it to defer to background tasks", result.Status)
	case msg := <-sess.nudges:
		t.Fatalf("unexpected user message %q while background tasks run", msg)
	case <-time.After(100 * time.Millisecond):
	}
	if sess.stopCalls != 0 {
		t.Fatal("session was stopped while background tasks were running")
	}

	// Tasks finish; the re-invoked agent writes the contract artifacts and
	// ends its turn again.
	sess.liveTasks.Store(0)
	writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
	if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	sess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
	select {
	case msg := <-sess.nudges:
		t.Fatalf("unexpected user message %q; completion needed no nudge", msg)
	default:
	}
}

// TestRunBoundedHelper_BackgroundTasksFinishQuietlyAutoResumes proves the
// bgTicker fallback: when the tasks finish but the CLI never re-invokes the
// agent, the waiter sends the auto-resume continuation instead of hanging or
// violating.
func TestRunBoundedHelper_BackgroundTasksFinishQuietlyAutoResumes(t *testing.T) {
	withBackgroundTaskPollInterval(t, 5*time.Millisecond)

	phaseDir := t.TempDir()
	sess := newBoundedBgTaskSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})
	sess.liveTasks.Store(1)

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-bg-resume",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
	}()

	sess.statusC <- agentStatusSuccess

	// Give the waiter a moment to defer, then finish the tasks with a stale
	// stdout stamp: the CLI never re-invoked the agent.
	time.Sleep(20 * time.Millisecond)
	sess.lastStdoutNs.Store(time.Now().Add(-time.Hour).UnixNano())
	sess.liveTasks.Store(0)

	select {
	case msg := <-sess.nudges:
		if !strings.Contains(msg, "Continue where you left off") {
			t.Fatalf("SendUserMessage() = %q, want auto-resume message", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auto-resume after tasks finished")
	}

	// The resumed turn writes the contract artifacts and ends cleanly.
	writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
	if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	sess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
}

// TestRunBoundedHelper_RetriesOnErroredResult proves a session whose CLI
// process ran and then reported an error result (llm.TermErrored — e.g. a
// transient provider/API failure) gets a fresh session retry instead of
// immediately failing the whole review gate, mirroring the early-crash-loop
// retry already in place for sessions that produce no result at all.
func TestRunBoundedHelper_RetriesOnErroredResult(t *testing.T) {
	phaseDir := t.TempDir()
	erroredSess := newBoundedNudgeSession(&llm.ResultMessage{
		Type: testResultMessageType, Subtype: "error", IsError: true, StopReason: testStopReasonEndTurn,
	})
	okSess := newBoundedNudgeSession(&llm.ResultMessage{
		Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn,
	})

	var startCalls int
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startCalls++
		if startCalls == 1 {
			return erroredSess, nil
		}
		// The retry attempt removes the stale marker before starting
		// (mirrors a real re-dispatched helper); simulate the resumed
		// helper writing its contract artifacts before ending its turn.
		writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
		if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		return okSess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-errored-retry",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
		errCh <- err
	}()

	erroredSess.statusC <- agentStatusSuccess
	okSess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q (err: %v)", result.Status, BoundedHelperStatusCompleted, <-errCh)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize after an errored-result retry")
	}
	if startCalls != 2 {
		t.Fatalf("StartSession called %d times, want 2 (one retry after the errored result)", startCalls)
	}
}

// TestRunBoundedHelper_ErroredResultRetryExhausts proves the retry budget for
// errored results is bounded: a session that keeps erroring finalizes as a
// failure once retryableInfrastructureSessionMaxAttempts is reached, rather
// than retrying forever.
func TestRunBoundedHelper_ErroredResultRetryExhausts(t *testing.T) {
	phaseDir := t.TempDir()

	var startCalls int
	sm := mocks.NewMockSessionManager()
	// Each attempt's session reports its status synchronously right after
	// construction (buffered statusC), driving the retry loop without
	// relying on an external goroutine to push per attempt.
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startCalls++
		sess := newBoundedNudgeSession(&llm.ResultMessage{
			Type: testResultMessageType, Subtype: "error", IsError: true, StopReason: testStopReasonEndTurn,
		})
		sess.statusC <- agentStatusSuccess
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID:        "helper-errored-exhaust",
		workDir:          t.TempDir(),
		phaseCompleteDir: phaseDir,
		contractPhase:    feature.PhaseReview,
		contractRole:     RoleImplementationReviewCraft,
	})

	if result == nil || result.Status != BoundedHelperStatusFailed {
		status := "<nil>"
		if result != nil {
			status = result.Status
		}
		t.Fatalf("Status = %q, want %q", status, BoundedHelperStatusFailed)
	}
	if err == nil || !strings.Contains(err.Error(), "helper returned an error result") {
		t.Fatalf("err = %v, want an error-result failure", err)
	}
	if startCalls != retryableInfrastructureSessionMaxAttempts {
		t.Fatalf("StartSession called %d times, want %d", startCalls, retryableInfrastructureSessionMaxAttempts)
	}
}

// TestArchiveExistingLog proves a session log from a prior attempt is
// preserved (renamed aside), not silently overwritten, before the next
// attempt's os.Create at the same path.
func TestArchiveExistingLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "review-output.txt")

	// No-op when there is nothing to preserve yet.
	archiveExistingLog(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file to be created, stat err = %v", err)
	}

	if err := os.WriteFile(path, []byte("attempt 1 transcript"), 0o644); err != nil {
		t.Fatal(err)
	}
	archiveExistingLog(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be renamed away, stat err = %v", path, err)
	}
	matches, err := filepath.Glob(path + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("glob %s.*.bak = %v, want exactly one archived file", path, matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "attempt 1 transcript" {
		t.Fatalf("archived content = %q, want the prior attempt's transcript", data)
	}
}

// TestRunBoundedHelper_TruncatedTurnAutoResumes proves the statusCh arm
// resumes a CLI-truncated turn (stop_reason tool_use — e.g. the helper yielded
// on a scheduled wakeup) instead of finalizing the run as a failure.
func TestRunBoundedHelper_TruncatedTurnAutoResumes(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: "tool_use"})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-truncated-resume",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
	}()

	// The truncated turn triggers the auto-resume continuation, not a failure.
	sess.statusC <- agentStatusSuccess
	select {
	case msg := <-sess.nudges:
		if !strings.Contains(msg, "Continue where you left off") {
			t.Fatalf("SendUserMessage() = %q, want auto-resume message", msg)
		}
	case result := <-resultCh:
		t.Fatalf("runBoundedHelperSession() finalized %q; want auto-resume of the truncated turn", result.Status)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for auto-resume after truncated turn")
	}

	// The resumed turn writes the contract artifacts and ends cleanly.
	writeReviewFeedbackFile(t, filepath.Join(phaseDir, "review-feedback.md"), testutil.StructuredReviewFeedback("", "", agentStatusApproved))
	if err := os.WriteFile(filepath.Join(phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	sess.statusC <- agentStatusSuccess

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusCompleted {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
}

// TestRunBoundedHelper_TruncatedTurnResumeCapThenFails proves the truncation
// retry budget is bounded: a session that keeps truncating without finishing
// finalizes as a failure after maxAutoResumeAttempts continuations.
func TestRunBoundedHelper_TruncatedTurnResumeCapThenFails(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: "tool_use"})

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-truncated-cap",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
		errCh <- err
	}()

	for i := 0; i < maxAutoResumeAttempts; i++ {
		sess.statusC <- agentStatusSuccess
		select {
		case msg := <-sess.nudges:
			if !strings.Contains(msg, "Continue where you left off") {
				t.Fatalf("SendUserMessage() = %q, want auto-resume message", msg)
			}
		case result := <-resultCh:
			t.Fatalf("runBoundedHelperSession() finalized %q after %d resumes; want %d", result.Status, i, maxAutoResumeAttempts)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for auto-resume")
		}
	}

	// Budget exhausted: the next truncated turn finalizes as a failure.
	sess.statusC <- agentStatusSuccess
	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusFailed {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusFailed)
		}
		err := <-errCh
		if err == nil || !strings.Contains(err.Error(), "turn ended before completion") {
			t.Fatalf("err = %v, want turn-ended-before-completion failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for bounded helper to finalize")
	}
	select {
	case msg := <-sess.nudges:
		t.Fatalf("unexpected user message %q after the resume budget was exhausted", msg)
	default:
	}
}

// TestRunBoundedHelper_BackgroundTaskStallFinalizesViolation proves the stall
// backstop: a wedged stream with perpetually-"live" tasks is bounded, and the
// run finalizes as a protocol violation instead of waiting forever.
func TestRunBoundedHelper_BackgroundTaskStallFinalizesViolation(t *testing.T) {
	withBackgroundTaskPollInterval(t, 5*time.Millisecond)
	prev := backgroundTaskStallTimeout
	backgroundTaskStallTimeout = 30 * time.Millisecond
	t.Cleanup(func() { backgroundTaskStallTimeout = prev })

	phaseDir := t.TempDir()
	sess := newBoundedBgTaskSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})
	sess.liveTasks.Store(1)

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-bg-stall",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractPhase:    feature.PhaseReview,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
	}()

	sess.statusC <- agentStatusSuccess

	// The waiter must defer first (not finalize on the spot) …
	select {
	case result := <-resultCh:
		t.Fatalf("runBoundedHelperSession() finalized %q immediately; want deferral before the stall backstop", result.Status)
	case <-time.After(15 * time.Millisecond):
	}

	// … then the silent wedged stream trips the backstop.
	sess.lastStdoutNs.Store(time.Now().Add(-time.Hour).UnixNano())
	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusProtocolViolation {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusProtocolViolation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stall backstop")
	}
}

// TestRunBoundedHelper_DoneArmIgnoresStaleBackgroundTasks proves a dead
// process never defers to its stale task set: the Done arm finalizes
// immediately even when the counter still reports live tasks.
func TestRunBoundedHelper_DoneArmIgnoresStaleBackgroundTasks(t *testing.T) {
	phaseDir := t.TempDir()
	sess := newBoundedBgTaskSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})
	sess.liveTasks.Store(2)

	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	resultCh := make(chan *BoundedHelperResult, 1)
	go func() {
		result, _ := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
			sessionID:        "helper-bg-done",
			workDir:          t.TempDir(),
			phaseCompleteDir: phaseDir,
			contractRole:     RoleImplementationReviewCraft,
		})
		resultCh <- result
	}()

	close(sess.doneC)

	select {
	case result := <-resultCh:
		if result.Status != BoundedHelperStatusProtocolViolation {
			t.Fatalf("Status = %q, want %q", result.Status, BoundedHelperStatusProtocolViolation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runBoundedHelperSession() hung on a dead session with stale background tasks")
	}
}

// TestRunBoundedHelper_ArmsKeepAliveForMarkeredHelpers proves helpers with
// phase_complete semantics start their session with KeepAliveOnTruncatedResult
// so the session layer keeps the CLI up after an end_turn result while
// background tasks are live. Markerless helpers keep the old behavior.
func TestRunBoundedHelper_ArmsKeepAliveForMarkeredHelpers(t *testing.T) {
	for _, tc := range []struct {
		name          string
		phaseDir      string
		wantKeepAlive bool
	}{
		{name: "markered helper arms keep-alive", phaseDir: t.TempDir(), wantKeepAlive: true},
		{name: "markerless helper does not", phaseDir: "", wantKeepAlive: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotOpts *session.SessionOpts
			sm := mocks.NewMockSessionManager()
			sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
				if len(opts) > 0 {
					gotOpts = opts[0]
				}
				sess := newUtilityTestSession()
				sess.result = &llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, Result: "done", StopReason: testStopReasonEndTurn}
				if tc.phaseDir != "" {
					if err := os.WriteFile(filepath.Join(tc.phaseDir, PhaseCompleteFile), nil, 0o644); err != nil {
						t.Fatalf("write marker: %v", err)
					}
				}
				sess.statusCh <- agentStatusSuccess
				return sess, nil
			}
			pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

			_, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
				sessionID:        "helper-keepalive",
				workDir:          t.TempDir(),
				phaseCompleteDir: tc.phaseDir,
			})
			if err != nil {
				t.Fatalf("runBoundedHelperSession() error = %v", err)
			}
			gotKeepAlive := gotOpts != nil && gotOpts.KeepAliveOnTruncatedResult
			if gotKeepAlive != tc.wantKeepAlive {
				t.Errorf("KeepAliveOnTruncatedResult = %v, want %v", gotKeepAlive, tc.wantKeepAlive)
			}
		})
	}
}

func newMockBoundedHelperRunner(t *testing.T, messages []llm.SDKMessage) (*PhaseRunner, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	for _, dir := range []string{workDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	mockProv := &mocks.MockProvider{
		ProviderName: testMockIdentifier,
		Models:       []string{"test-model"},
		CLIDetected:  true,
		CommandArgs:  []string{"cat"},
		Protocol:     mocks.NewMockProtocol(messages...),
	}

	registry := llm.NewRegistry()
	registry.Register(mockProv)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		Registry:       registry,
	}

	return pr, func() {
		sm.Shutdown()
	}
}

func askUserControlRequest(requestID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
			},
		},
	}
}
