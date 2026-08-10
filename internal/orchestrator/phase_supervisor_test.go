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

package orchestrator

import (
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Fixture session ID and status values used by the single-shot and loop
// supervision tests below.
const (
	inquireSessionID          = "feat-inquire"
	planStatusApproved        = "approved"
	statusAwaitingFinalReview = "awaiting_final_review"
)

func TestPhaseSupervisorSingleShotCompletesOnSessionResultBeforeProcessExit(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	sess := mocks.NewMockSessionView(inquireSessionID, "feat")
	sess.PhaseVal = feature.PhaseInquire
	configureSuccessfulRootTurn(sess)
	sm.GetSessionFn = func(id string) ports.SessionView {
		if id != inquireSessionID {
			t.Fatalf("GetSession(%q), want feat-inquire", id)
		}
		return sess
	}

	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion:    sink,
		Sessions:      sm,
		CommitOutcome: commitAllOutcomes,
	})
	supervisor.superviseSingleShotSession("feat", inquireSessionID, feature.PhaseInquire)

	sess.StatusChVal <- "SUCCESS"

	call := sink.wait(t)
	if call.featureID != "feat" {
		t.Fatalf("featureID = %q, want feat", call.featureID)
	}
	if call.input.Phase != feature.PhaseInquire || call.input.SessionID != inquireSessionID || !call.input.Success {
		t.Fatalf("completion input = %+v, want inquire success", call.input)
	}
	if sess.StopCalled != 1 {
		t.Fatalf("session Stop calls = %d, want 1", sess.StopCalled)
	}
}

func TestPhaseSupervisorSingleShotDedupe(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	sess := mocks.NewMockSessionView(inquireSessionID, "feat")
	sess.PhaseVal = feature.PhaseInquire
	configureSuccessfulRootTurn(sess)
	sm.GetSessionFn = func(string) ports.SessionView { return sess }

	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion:    sink,
		Sessions:      sm,
		CommitOutcome: commitAllOutcomes,
	})
	supervisor.superviseSingleShotSession("feat", inquireSessionID, feature.PhaseInquire)
	supervisor.superviseSingleShotSession("feat", inquireSessionID, feature.PhaseInquire)

	sess.StatusChVal <- "SUCCESS"

	call := sink.wait(t)
	if call.input.Phase != feature.PhaseInquire || !call.input.Success {
		t.Fatalf("completion input = %+v, want inquire success", call.input)
	}
	sink.expectNoCall(t)
	if sess.StopCalled != 1 {
		t.Fatalf("session Stop calls = %d, want 1", sess.StopCalled)
	}
}

func TestPhaseSupervisorSingleShotAllowsReentrantRetryForSameSessionID(t *testing.T) {
	sm := mocks.NewMockSessionManager()
	first := mocks.NewMockSessionView(inquireSessionID, "feat")
	first.PhaseVal = feature.PhaseInquire
	configureSuccessfulRootTurn(first)
	retry := mocks.NewMockSessionView(inquireSessionID, "feat")
	retry.PhaseVal = feature.PhaseInquire
	configureSuccessfulRootTurn(retry)

	var getCount atomic.Int32
	sm.GetSessionFn = func(id string) ports.SessionView {
		if id != inquireSessionID {
			t.Fatalf("GetSession(%q), want feat-inquire", id)
		}
		if getCount.Add(1) == 1 {
			return first
		}
		return retry
	}

	var supervisor *phaseSupervisor
	sink := newReentrantRetryCompletionSink(func() {
		supervisor.superviseSingleShotSession("feat", inquireSessionID, feature.PhaseInquire)
		retry.StatusChVal <- "SUCCESS"
	})
	supervisor = newPhaseSupervisor(phaseSupervisorConfig{
		Completion:    sink,
		Sessions:      sm,
		CommitOutcome: commitAllOutcomes,
	})
	supervisor.superviseSingleShotSession("feat", inquireSessionID, feature.PhaseInquire)

	first.StatusChVal <- "SUCCESS"

	firstCall := sink.wait(t)
	if firstCall.input.SessionID != inquireSessionID || !firstCall.input.Success {
		t.Fatalf("first completion = %+v, want successful inquire completion", firstCall.input)
	}
	retryCall := sink.wait(t)
	if retryCall.input.SessionID != inquireSessionID || !retryCall.input.Success {
		t.Fatalf("retry completion = %+v, want successful retry completion", retryCall.input)
	}
	if got := getCount.Load(); got != 2 {
		t.Fatalf("GetSession calls = %d, want 2 so retried session was supervised", got)
	}
	if retry.StopCalled != 1 {
		t.Fatalf("retry Stop calls = %d, want 1", retry.StopCalled)
	}
}

func TestPhaseSupervisorSingleShotFailsWhenSessionExitsWithoutResult(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	sess := mocks.NewMockSessionView("feat-research", "feat")
	sess.PhaseVal = feature.PhaseResearch
	sess.ErrorDetailVal = "process exited unexpectedly"
	sm.GetSessionFn = func(string) ports.SessionView { return sess }

	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink,
		Sessions:   sm,
	})
	supervisor.superviseSingleShotSession("feat", "feat-research", feature.PhaseResearch)

	close(sess.DoneChVal)

	call := sink.wait(t)
	if call.input.Phase != feature.PhaseResearch || call.input.SessionID != "feat-research" || call.input.Success {
		t.Fatalf("completion input = %+v, want research failure", call.input)
	}
	if call.input.ErrorDetail != "process exited unexpectedly" {
		t.Fatalf("ErrorDetail = %q, want session detail", call.input.ErrorDetail)
	}
}

func TestPhaseSupervisorSingleShotReportsTerminalOutcomeViolation(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	sess := mocks.NewMockSessionView("feat-kbv2-my-service-0123456789ab", "feat")
	sess.PhaseVal = feature.PhaseKnowledgeBase
	sess.LogFilePathVal = filepath.Join(t.TempDir(), "output.txt")
	sess.CostVal = &llm.ResultMessage{Subtype: "success", StopReason: "end_turn"}
	sm.GetSessionFn = func(string) ports.SessionView { return sess }

	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink,
		Sessions:   sm,
	})
	supervisor.superviseSingleShotSession("feat", sess.ID(), feature.PhaseKnowledgeBase)

	// The first two clean turns receive bounded correction nudges. The third
	// missing outcome is terminal and must be reported as a protocol failure.
	for range 3 {
		sess.StatusChVal <- phaseSupervisorStatusSuccess
	}

	call := sink.wait(t)
	if call.input.FailureType != feature.FailureProtocolViolation {
		t.Errorf("FailureType = %q, want %q", call.input.FailureType, feature.FailureProtocolViolation)
	}
	if !strings.Contains(call.input.ErrorDetail, string(agent.RoleKnowledgeBaseBuilder)) {
		t.Errorf("ErrorDetail = %q, want knowledge base role", call.input.ErrorDetail)
	}
	if want := filepath.Dir(sess.LogFilePathVal); !strings.Contains(call.input.ErrorDetail, want) {
		t.Errorf("ErrorDetail = %q, want artifact dir %q", call.input.ErrorDetail, want)
	}
	if strings.Contains(call.input.ErrorDetail, "<unresolved>") {
		t.Errorf("ErrorDetail = %q, want resolved artifact dir", call.input.ErrorDetail)
	}
	if got := len(sess.SentMessages); got != 2 {
		t.Errorf("completion correction nudges = %d, want 2", got)
	}
}

func TestPhaseSupervisorPlanLoopRoutesPlanResult(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{Completion: sink})
	resultCh := make(chan *agent.PlanLoopResult, 1)
	want := &agent.PlanLoopResult{FinalStatus: planStatusApproved, Iterations: 2}

	supervisor.supervisePlanLoop("feat-plan", resultCh)
	resultCh <- want

	call := sink.wait(t)
	if call.featureID != "feat-plan" || call.input.Phase != feature.PhasePlan || call.input.PlanResult != want {
		t.Fatalf("completion call = %+v, want plan result", call)
	}
}

func TestPhaseSupervisorImplementationLoopRoutesFirstTerminalResult(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{Completion: sink})
	resultCh := make(chan *agent.OrchestratorResult, 2)
	want := &agent.OrchestratorResult{FinalStatus: statusAwaitingFinalReview}

	supervisor.superviseImplementationLoop("feat-impl", resultCh)
	resultCh <- &agent.OrchestratorResult{FinalStatus: "still_running"}
	resultCh <- want

	call := sink.wait(t)
	if call.featureID != "feat-impl" || call.input.Phase != feature.PhaseImplement || call.input.MultiRepoResult != want {
		t.Fatalf("completion call = %+v, want implement terminal result", call)
	}
}

func TestPhaseSupervisorSurfacesCompletionErrors(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sink.err = errors.New("completion failed")
	errCh := make(chan error, 1)
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink,
		OnCompletionError: func(_ string, err error) {
			errCh <- err
		},
	})
	resultCh := make(chan *agent.PlanLoopResult, 1)

	supervisor.supervisePlanLoop("feat-plan", resultCh)
	resultCh <- &agent.PlanLoopResult{FinalStatus: finalStatusFailed}
	_ = sink.wait(t)

	select {
	case err := <-errCh:
		if !errors.Is(err, sink.err) {
			t.Fatalf("completion error = %v, want %v", err, sink.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for surfaced completion error")
	}
}

type recordingPhaseCompletionSink struct {
	calls chan recordedPhaseCompletionCall
	err   error
}

type recordedPhaseCompletionCall struct {
	featureID string
	input     PhaseCompletionInput
}

func newRecordingPhaseCompletionSink() *recordingPhaseCompletionSink {
	return &recordingPhaseCompletionSink{calls: make(chan recordedPhaseCompletionCall, 4)}
}

type reentrantRetryCompletionSink struct {
	*recordingPhaseCompletionSink
	onFirst func()
	calls   atomic.Int32
}

func newReentrantRetryCompletionSink(onFirst func()) *reentrantRetryCompletionSink {
	return &reentrantRetryCompletionSink{
		recordingPhaseCompletionSink: newRecordingPhaseCompletionSink(),
		onFirst:                      onFirst,
	}
}

func (r *reentrantRetryCompletionSink) HandlePhaseCompletion(featureID string, input PhaseCompletionInput) error {
	callNum := r.calls.Add(1)
	r.recordingPhaseCompletionSink.calls <- recordedPhaseCompletionCall{featureID: featureID, input: input}
	if callNum == 1 && r.onFirst != nil {
		r.onFirst()
	}
	return r.err
}

func (r *recordingPhaseCompletionSink) HandlePhaseCompletion(featureID string, input PhaseCompletionInput) error {
	r.calls <- recordedPhaseCompletionCall{featureID: featureID, input: input}
	return r.err
}

func (r *recordingPhaseCompletionSink) wait(t *testing.T) recordedPhaseCompletionCall {
	t.Helper()
	select {
	case call := <-r.calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for phase completion call")
		return recordedPhaseCompletionCall{}
	}
}

func (r *recordingPhaseCompletionSink) expectNoCall(t *testing.T) {
	t.Helper()
	select {
	case call := <-r.calls:
		t.Fatalf("unexpected phase completion call: %+v", call)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestPhaseSupervisorSingleShotClassifiesDoneResultCost(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	sess := mocks.NewMockSessionView("feat-design", "feat")
	sess.PhaseVal = feature.PhaseDesign
	configureSuccessfulRootTurn(sess)
	sm.GetSessionFn = func(string) ports.SessionView { return sess }

	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion:    sink,
		Sessions:      sm,
		CommitOutcome: commitAllOutcomes,
	})
	supervisor.superviseSingleShotSession("feat", "feat-design", feature.PhaseDesign)

	close(sess.DoneChVal)

	call := sink.wait(t)
	if call.input.Phase != feature.PhaseDesign || !call.input.Success {
		t.Fatalf("completion input = %+v, want design success from result cost", call.input)
	}
	if sess.StopCalled != 1 {
		t.Fatalf("session Stop calls = %d, want 1", sess.StopCalled)
	}
}

func configureSuccessfulRootTurn(sess *mocks.MockSessionView) {
	sess.RootCompletionIntentVal = llm.CompletionIntent{
		Found:  true,
		Status: llm.CompletionIntentSuccess,
	}
	sess.CostVal = &llm.ResultMessage{Subtype: "success", StopReason: "end_turn"}
}

func commitAllOutcomes(string, string, feature.Phase, llm.CompletionIntent) ([]agent.ProtocolViolation, error) {
	return nil, nil
}
