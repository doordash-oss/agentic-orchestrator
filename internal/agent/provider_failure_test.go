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
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// openCodeCannotConnect is the terminal error an opencode session reports
// when its provider gateway is unreachable.
const openCodeCannotConnect = "OpenCode prompt failed: code -32603: Internal error: Cannot connect to API: Unable to connect. Is the computer able to access the url?"

func TestClassifyProviderFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		text string
		want providerFailureClass
	}{
		{openCodeCannotConnect, providerFailureHard},
		{"API Error: Unable to connect to API (ENOTFOUND)", providerFailureHard},
		{"dial tcp 127.0.0.1:443: connect: connection refused", providerFailureHard},
		{"ECONNREFUSED", providerFailureHard},
		{"API Error: 401 Unauthorized", providerFailureHard},
		{"Invalid API key · Please run /login", providerFailureHard},
		{`exec: "opencode": executable file not found in $PATH`, providerFailureHard},
		{"OpenCode prompt failed: stream_incomplete", providerFailureTransient},
		{"API Error: 429 rate limit exceeded", providerFailureTransient},
		{"Overloaded", providerFailureTransient},
		{"protocol handshake: opencode initialize timeout: context deadline exceeded", providerFailureTransient},
		{"read: ECONNRESET", providerFailureTransient},
		{"helper returned an error result", providerFailureUnknown},
		{"", providerFailureUnknown},
	}
	for _, tt := range tests {
		if got := classifyProviderFailure(tt.text); got != tt.want {
			t.Errorf("classifyProviderFailure(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestClassifyStartSessionError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want providerFailureClass
	}{
		{"missing executable", fmt.Errorf("starting: %w", exec.ErrNotFound), providerFailureHard},
		{"handshake timeout", errors.New("protocol handshake: opencode initialize timeout: context deadline exceeded"), providerFailureTransient},
		{"shutting down", fmt.Errorf("start: %w", ports.ErrSessionShuttingDown), providerFailureUnknown},
		{"unrecognized", errors.New("session id already in use"), providerFailureUnknown},
	}
	for _, tt := range tests {
		if got := classifyStartSessionError(tt.err); got != tt.want {
			t.Errorf("%s: classifyStartSessionError(%v) = %d, want %d", tt.name, tt.err, got, tt.want)
		}
	}
}

func erroredResultSessionManager(resultText string, startCalls *int) *mocks.MockSessionManager {
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		*startCalls++
		sess := newBoundedNudgeSession(&llm.ResultMessage{
			Type: testResultMessageType, Subtype: "error", IsError: true, StopReason: testStopReasonEndTurn,
			Result: resultText,
		})
		sess.statusC <- agentStatusSuccess
		return sess, nil
	}
	return sm
}

// TestRunBoundedHelper_HardProviderFailureFailsFast proves an unreachable
// provider is not retried: one session, a provider-unavailable error that
// names the provider diagnostic, and no retry callback.
func TestRunBoundedHelper_HardProviderFailureFailsFast(t *testing.T) {
	var startCalls, retries int
	pr := &PhaseRunner{SessionManager: erroredResultSessionManager(openCodeCannotConnect, &startCalls), StateDir: t.TempDir()}

	result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID:     "helper-cannot-connect",
		workDir:       t.TempDir(),
		completionDir: t.TempDir(),
		contractPhase: feature.PhaseReview,
		contractRole:  RoleImplementationReviewCraft,
		onRetry:       func(int, error) { retries++ },
	})
	if startCalls != 1 {
		t.Fatalf("StartSession called %d times, want 1 (hard failures are not retried)", startCalls)
	}
	if retries != 0 {
		t.Fatalf("onRetry called %d times, want 0", retries)
	}
	if !isProviderUnavailableError(err) {
		t.Fatalf("err = %v, want provider-unavailable", err)
	}
	if !errors.Is(err, errHelperReturnedErrorResult) {
		t.Fatalf("err = %v, want it to keep wrapping the error-result sentinel", err)
	}
	if !strings.Contains(err.Error(), "Cannot connect to API") {
		t.Fatalf("err = %q, want the provider diagnostic", err)
	}
	if result == nil || result.Status != boundedHelperStatusFailed {
		t.Fatalf("result = %+v, want failed status", result)
	}
}

// TestRunBoundedHelper_TransientProviderFailureStillRetries proves transient
// provider errors (opencode stream_incomplete) keep the bounded retry.
func TestRunBoundedHelper_TransientProviderFailureStillRetries(t *testing.T) {
	var startCalls, retries int
	pr := &PhaseRunner{SessionManager: erroredResultSessionManager("OpenCode prompt failed: stream_incomplete", &startCalls), StateDir: t.TempDir()}

	_, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID:     "helper-stream-incomplete",
		workDir:       t.TempDir(),
		completionDir: t.TempDir(),
		contractPhase: feature.PhaseReview,
		contractRole:  RoleImplementationReviewCraft,
		onRetry:       func(int, error) { retries++ },
	})
	if startCalls != retryableInfrastructureSessionMaxAttempts {
		t.Fatalf("StartSession called %d times, want %d", startCalls, retryableInfrastructureSessionMaxAttempts)
	}
	if retries != retryableInfrastructureSessionMaxAttempts-1 {
		t.Fatalf("onRetry called %d times, want %d", retries, retryableInfrastructureSessionMaxAttempts-1)
	}
	if isProviderUnavailableError(err) {
		t.Fatalf("err = %v, transient failures must not be provider-unavailable", err)
	}
}

// TestRunBoundedHelper_StartSessionHandshakeTimeoutRetries proves a session
// that never started because the provider handshake timed out is retried
// like any other transient helper failure instead of failing at once.
func TestRunBoundedHelper_StartSessionHandshakeTimeoutRetries(t *testing.T) {
	var sessionIDs []string
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sessionIDs = append(sessionIDs, id)
		if len(sessionIDs) == 1 {
			return nil, errors.New("protocol handshake: opencode initialize timeout: context deadline exceeded")
		}
		sess := newBoundedNudgeSession(&llm.ResultMessage{Type: testResultMessageType, Subtype: testResultSuccessValue, StopReason: testStopReasonEndTurn})
		sess.statusC <- agentStatusSuccess
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID: "helper-handshake",
		workDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("runBoundedHelperSession() error = %v", err)
	}
	if result.Status != boundedHelperStatusCompleted {
		t.Fatalf("Status = %q, want completed", result.Status)
	}
	if want := []string{"helper-handshake", "helper-handshake-retry-02"}; strings.Join(sessionIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("session IDs = %v, want %v", sessionIDs, want)
	}
}

// TestRunBoundedHelper_StartSessionMissingExecutableFailsFast proves a
// provider CLI that cannot boot fails fast as provider-unavailable.
func TestRunBoundedHelper_StartSessionMissingExecutableFailsFast(t *testing.T) {
	var startCalls int
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startCalls++
		return nil, &exec.Error{Name: "opencode", Err: exec.ErrNotFound}
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}

	_, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{sessionID: "helper-no-cli", workDir: t.TempDir()})
	if startCalls != 1 {
		t.Fatalf("StartSession called %d times, want 1", startCalls)
	}
	if !isProviderUnavailableError(err) {
		t.Fatalf("err = %v, want provider-unavailable", err)
	}
}

// TestRunBoundedHelper_InterruptedFeatureIsNotRetried proves a session killed
// by pause-stop (same footprint as an early infrastructure failure) is
// recorded as interrupted and never relaunched as a -retry-02 session.
func TestRunBoundedHelper_InterruptedFeatureIsNotRetried(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	if err := store.Save(&feature.Feature{
		ID: "feature-stopped", Name: "feature-stopped", Status: feature.StatusInterrupted,
		SchemaVersion: feature.SchemaVersionCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	var startCalls, retries int
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startCalls++
		return newTerminalStatusTestSession(ports.SessionFailed), nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir(), FeatureStore: store}

	result, err := pr.runBoundedHelperSession(context.Background(), boundedHelperRunConfig{
		sessionID:     "helper-stopped",
		featureID:     "feature-stopped",
		workDir:       t.TempDir(),
		completionDir: t.TempDir(),
		contractPhase: feature.PhaseReview,
		contractRole:  RoleImplementationReviewCraft,
		onRetry:       func(int, error) { retries++ },
	})
	if startCalls != 1 || retries != 0 {
		t.Fatalf("StartSession calls = %d, retries = %d; want 1 and 0", startCalls, retries)
	}
	if !isHelperInterruptedError(err) {
		t.Fatalf("err = %v, want interrupted", err)
	}
	if isHelperInfrastructureError(err) {
		t.Fatalf("err = %v, an interrupt must not read as a retryable infrastructure failure", err)
	}
	if result == nil || result.Status != boundedHelperStatusInterrupted {
		t.Fatalf("result = %+v, want interrupted status", result)
	}
}

// TestRunBoundedHelper_CancelledContextIsInterrupted proves cancelling the
// round's context stops a running helper and records it as interrupted.
func TestRunBoundedHelper_CancelledContextIsInterrupted(t *testing.T) {
	var startCalls int
	sess := newBoundedNudgeSession(nil)
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		startCalls++
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm, StateDir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := pr.runBoundedHelperSession(ctx, boundedHelperRunConfig{sessionID: "helper-cancelled", workDir: t.TempDir()})
	if startCalls != 1 {
		t.Fatalf("StartSession called %d times, want 1", startCalls)
	}
	if !isHelperInterruptedError(err) {
		t.Fatalf("err = %v, want interrupted", err)
	}
	if result == nil || result.Status != boundedHelperStatusInterrupted {
		t.Fatalf("result = %+v, want interrupted status", result)
	}
	if sess.stopCalls == 0 {
		t.Fatal("cancelled helper session was not stopped")
	}
}

// TestMultiAxisValidatorStatusRecordsRetriesAndErrors proves the live review
// gate shows retrying/failed validators with their last error, and that a
// stopped validator reads as interrupted rather than running.
func TestMultiAxisValidatorStatusRecordsRetriesAndErrors(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	if err := store.Save(&feature.Feature{
		ID: "feature-validators", Name: "feature-validators", Status: feature.StatusFinalReviewing,
		SchemaVersion: feature.SchemaVersionCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	id := "feature-validators"
	setMultiAxisValidatorStatuses(store, id, []string{"QA", "Craft", "Cleanliness"})
	markMultiAxisValidatorRetrying(store, id, "QA", errors.New("opencode initialize timeout"))
	updateMultiAxisValidatorStatus(store, id, "Cleanliness", reviewFailed, newProviderUnavailableError(openCodeCannotConnect, errHelperReturnedErrorResult))
	updateMultiAxisValidatorStatus(store, id, "Craft", reviewFailed, fmt.Errorf("running review helper: %w", errHelperInterrupted))

	f, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ValidatorStatuses["QA"]; got != validatorStateRetrying {
		t.Errorf("QA status = %q, want retrying", got)
	}
	if got := f.ValidatorErrors["QA"]; got != "opencode initialize timeout" {
		t.Errorf("QA last error = %q", got)
	}
	if got := f.ValidatorStatuses["Cleanliness"]; got != validatorStateError {
		t.Errorf("Cleanliness status = %q, want error", got)
	}
	if got := f.ValidatorErrors["Cleanliness"]; !strings.Contains(got, "Cannot connect to API") {
		t.Errorf("Cleanliness last error = %q, want provider diagnostic", got)
	}
	if got := f.ValidatorStatuses["Craft"]; got != validatorStateInterrupted {
		t.Errorf("Craft status = %q, want interrupted", got)
	}

	// A later approval clears that validator's error.
	updateMultiAxisValidatorStatus(store, id, "QA", ReviewApproved, nil)
	f, _ = store.Load(id)
	if _, ok := f.ValidatorErrors["QA"]; ok {
		t.Errorf("QA error retained after approval: %v", f.ValidatorErrors)
	}

	clearMultiAxisValidatorStatuses(store, id)
	f, _ = store.Load(id)
	if f.ValidatorStatuses != nil || f.ValidatorErrors != nil {
		t.Errorf("statuses/errors not cleared: %v %v", f.ValidatorStatuses, f.ValidatorErrors)
	}
}
