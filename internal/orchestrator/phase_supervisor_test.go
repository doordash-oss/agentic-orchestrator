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
	"fmt"
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

// PHASE2(session-view alias): main dropped the session.SessionView alias;
// feature resume tests rewire onto ports.SessionView.
func TestPhaseSupervisorSingleShotTransientFailureResumesThenCompletes(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	failed := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-inquire", "feat"), providerID: "provider-1"}
	failed.PhaseVal = feature.PhaseInquire
	failed.ErrorDetailVal = "service temporarily unavailable"
	resumed := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-inquire-resume-01", "feat"), providerID: "provider-1"}
	resumed.StatusChVal <- phaseSupervisorStatusSuccess
	driver := &fakeSingleShotResumeDriver{
		supports: true,
		dispatch: func(previous, resumeID string, ordinal int, fresh bool) (*agent.SingleShotResumeResult, error) {
			if previous != "feat-inquire" || resumeID != "provider-1" || ordinal != 1 || fresh {
				t.Fatalf("dispatch = %q %q %d %v", previous, resumeID, ordinal, fresh)
			}
			return &agent.SingleShotResumeResult{SessionID: resumed.ID(), Session: resumed}, nil
		},
	}
	sm.GetSessionFn = func(string) ports.SessionView { return failed }
	var waits []time.Duration
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion:        sink,
		Sessions:          sm,
		SingleShotResumer: driver,
		AutoResumeWait: func(wait time.Duration) bool {
			waits = append(waits, wait)
			return true
		},
	})
	supervisor.superviseSingleShotSession("feat", failed.ID(), feature.PhaseInquire)
	close(failed.DoneChVal)

	call := sink.wait(t)
	if !call.input.Success || call.input.SessionID != resumed.ID() {
		t.Fatalf("completion = %+v", call.input)
	}
	if len(waits) != 1 || waits[0] != 5*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if driver.resumed != 1 || driver.completed != 1 {
		t.Fatalf("resumed/completed = %d/%d", driver.resumed, driver.completed)
	}
}

func TestPhaseSupervisorSingleShotTransientCapCompletesFailure(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	initial := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-design", "feat"), providerID: "provider"}
	initial.ErrorDetailVal = "service temporarily unavailable"
	driver := &fakeSingleShotResumeDriver{supports: true}
	driver.dispatch = func(_ string, _ string, ordinal int, _ bool) (*agent.SingleShotResumeResult, error) {
		next := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView(fmt.Sprintf("resume-%d", ordinal), "feat"), providerID: "provider"}
		next.ErrorDetailVal = "service temporarily unavailable"
		close(next.DoneChVal)
		return &agent.SingleShotResumeResult{SessionID: next.ID(), Session: next}, nil
	}
	sm.GetSessionFn = func(string) ports.SessionView { return initial }
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink, Sessions: sm, SingleShotResumer: driver,
		AutoResumeWait: func(time.Duration) bool { return true },
	})
	supervisor.superviseSingleShotSession("feat", initial.ID(), feature.PhaseDesign)
	close(initial.DoneChVal)

	call := sink.wait(t)
	if call.input.Success {
		t.Fatalf("completion = %+v, want failure", call.input)
	}
	if driver.dispatched != 3 {
		t.Fatalf("dispatch count = %d, want 3", driver.dispatched)
	}
}

func TestPhaseSupervisorSingleShotProgressResetsConsecutiveUntilAbsoluteCap(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	initial := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-research", "feat"), providerID: "provider"}
	initial.ErrorDetailVal = "service temporarily unavailable"
	driver := &fakeSingleShotResumeDriver{supports: true}
	driver.dispatch = func(_ string, _ string, ordinal int, _ bool) (*agent.SingleShotResumeResult, error) {
		next := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView(fmt.Sprintf("resume-%d", ordinal), "feat"), providerID: "provider"}
		next.ErrorDetailVal = "service temporarily unavailable"
		next.AccumulatedUsageVal.InputTokens = 1
		close(next.DoneChVal)
		return &agent.SingleShotResumeResult{SessionID: next.ID(), Session: next}, nil
	}
	sm.GetSessionFn = func(string) ports.SessionView { return initial }
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink, Sessions: sm, SingleShotResumer: driver,
		AutoResumeWait: func(time.Duration) bool { return true },
	})
	supervisor.superviseSingleShotSession("feat", initial.ID(), feature.PhaseResearch)
	close(initial.DoneChVal)

	call := sink.wait(t)
	if call.input.Success {
		t.Fatalf("completion = %+v, want failure", call.input)
	}
	if driver.dispatched != 10 {
		t.Fatalf("dispatch count = %d, want absolute cap 10", driver.dispatched)
	}
}

func TestPhaseSupervisorSingleShotRetryHintRaisesBackoffFloor(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	initial := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-inquire", "feat"), providerID: "provider"}
	initial.ErrorDetailVal = "rate limit"
	initial.MessageLogVal.Append(llm.SDKMessage{RateLimit: &llm.RateLimitMessage{RetryMS: 90_000}})
	resumed := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("resume-1", "feat"), providerID: "provider"}
	resumed.StatusChVal <- phaseSupervisorStatusSuccess
	driver := &fakeSingleShotResumeDriver{
		supports: true,
		dispatch: func(string, string, int, bool) (*agent.SingleShotResumeResult, error) {
			return &agent.SingleShotResumeResult{SessionID: resumed.ID(), Session: resumed}, nil
		},
	}
	sm.GetSessionFn = func(string) ports.SessionView { return initial }
	var gotWait time.Duration
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink, Sessions: sm, SingleShotResumer: driver,
		AutoResumeWait: func(wait time.Duration) bool {
			gotWait = wait
			return true
		},
	})
	supervisor.superviseSingleShotSession("feat", initial.ID(), feature.PhaseInquire)
	close(initial.DoneChVal)

	if call := sink.wait(t); !call.input.Success {
		t.Fatalf("completion = %+v, want success", call.input)
	}
	if gotWait != 90*time.Second {
		t.Fatalf("wait = %v, want provider retry hint 1m30s", gotWait)
	}
}

func TestPhaseSupervisorSingleShotBackoffStopsForInterruptedFeature(t *testing.T) {
	driver := &fakeSingleShotResumeDriver{interrupted: true}
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		SingleShotResumer: driver,
	})
	startedAt := time.Now()
	if supervisor.waitForSingleShotResume("feat-design", time.Second) {
		t.Fatal("waitForSingleShotResume() = true, want interrupted feature to cancel dispatch")
	}
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		t.Errorf("interrupted backoff took %v, want prompt cancellation", elapsed)
	}
}

func TestPhaseSupervisorSingleShotRejectedEstablishmentFallsBackFresh(t *testing.T) {
	sink := newRecordingPhaseCompletionSink()
	sm := mocks.NewMockSessionManager()
	rejected := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-inquire-resume-01", "feat"), providerID: "provider"}
	rejected.ErrorDetailVal = "session not found"
	fresh := &singleShotProviderSession{MockSessionView: mocks.NewMockSessionView("feat-inquire-fresh-01", "feat"), providerID: "provider-fresh"}
	fresh.StatusChVal <- phaseSupervisorStatusSuccess
	driver := &fakeSingleShotResumeDriver{supports: true, pending: true, rejectOnce: true}
	driver.dispatch = func(_ string, _ string, ordinal int, freshFallback bool) (*agent.SingleShotResumeResult, error) {
		if ordinal != 1 || !freshFallback {
			t.Fatalf("fresh dispatch ordinal/fresh = %d/%v", ordinal, freshFallback)
		}
		driver.pending = false
		return &agent.SingleShotResumeResult{SessionID: fresh.ID(), Session: fresh}, nil
	}
	sm.GetSessionFn = func(string) ports.SessionView { return rejected }
	supervisor := newPhaseSupervisor(phaseSupervisorConfig{
		Completion: sink, Sessions: sm, SingleShotResumer: driver,
		AutoResumeWait: func(time.Duration) bool { t.Fatal("rejection must not consume transient retry"); return false },
	})
	supervisor.superviseSingleShotSession("feat", rejected.ID(), feature.PhaseInquire)
	close(rejected.DoneChVal)

	call := sink.wait(t)
	if !call.input.Success || call.input.SessionID != fresh.ID() {
		t.Fatalf("completion = %+v", call.input)
	}
	if driver.dispatched != 1 || driver.resumed != 0 || driver.completed != 1 {
		t.Fatalf("dispatch/resumed/completed = %d/%d/%d", driver.dispatched, driver.resumed, driver.completed)
	}
}

type singleShotProviderSession struct {
	*mocks.MockSessionView
	providerID string
}

func (s *singleShotProviderSession) SessionID() string { return s.providerID }

type fakeSingleShotResumeDriver struct {
	supports    bool
	pending     bool
	dispatched  int
	resumed     int
	completed   int
	retired     int
	rejectOnce  bool
	interrupted bool
	dispatch    func(string, string, int, bool) (*agent.SingleShotResumeResult, error)
}

func (d *fakeSingleShotResumeDriver) SingleShotPhaseComplete(string) bool { return false }
func (d *fakeSingleShotResumeDriver) SingleShotSupportsResume(string) bool {
	return d.supports
}
func (d *fakeSingleShotResumeDriver) SingleShotInterrupted(string) bool { return d.interrupted }
func (d *fakeSingleShotResumeDriver) SingleShotNeedsEstablishment(string) bool {
	return d.pending
}
func (d *fakeSingleShotResumeDriver) CaptureSingleShotProviderSnapshot(string, ports.SessionView) error {
	return nil
}
func (d *fakeSingleShotResumeDriver) DispatchSingleShotContinuation(previous, resumeID string, ordinal int, fresh bool) (*agent.SingleShotResumeResult, error) {
	d.dispatched++
	return d.dispatch(previous, resumeID, ordinal, fresh)
}
func (d *fakeSingleShotResumeDriver) CompleteSingleShotResumeEstablishment(string, ports.SessionView, time.Duration) (bool, error) {
	if d.rejectOnce {
		d.rejectOnce = false
		return false, nil
	}
	d.resumed++
	return true, nil
}
func (d *fakeSingleShotResumeDriver) MarkSingleShotCompleted(string) error {
	d.completed++
	return nil
}
func (d *fakeSingleShotResumeDriver) RetireSingleShotResume(string) { d.retired++ }

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
