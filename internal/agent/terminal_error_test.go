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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const terminalImageError = "Model only supports text input; received unsupported content type 'image_url'."

func TestWaitForPhaseOutcome_TerminalProviderError(t *testing.T) {
	// Exercise both normal delivery and the fallback tick with a live transport.
	// The tick interval is package state, so these cases must remain serial.
	withPhaseOutcomeReclassifyInterval(t, time.Millisecond)
	for _, delivery := range []string{"status", "reclassify", "exited"} {
		t.Run(delivery, func(t *testing.T) {
			sess := newBgTaskSession()
			sess.result = &llm.ResultMessage{Subtype: "error", IsError: true, Result: terminalImageError}
			// Even a success intent and outstanding work cannot override a
			// terminal provider failure or defer it into an input/task wait.
			sess.setRootIntent(validSuccessCompletionIntent())
			sess.setRootAsk(true)
			sess.liveTasks.Store(1)
			if delivery == "status" {
				sess.statusCh <- agentStatusAPIError
			}
			if delivery == "exited" {
				close(sess.done)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{
				Ctx: ctx,
				CommitOutcome: func(llm.CompletionIntent) ([]ProtocolViolation, error) {
					t.Fatal("committed an outcome after a terminal provider error")
					return nil, nil
				},
			})
			if got.Status != agentStatusFailed || got.Err == nil || !strings.Contains(got.Err.Error(), terminalImageError) {
				t.Fatalf("WaitForPhaseOutcome() = %+v, want failure carrying the provider diagnostic", got)
			}
			if !sess.stopped.Load() {
				t.Fatal("terminal provider error did not stop the session")
			}
			if len(sess.userMessages) != 0 {
				t.Fatal("terminal provider error prompted another turn")
			}
		})
	}
}

func TestImplementLoop_TerminalProviderErrorDoesNotCrashResume(t *testing.T) {
	featureID := "terminal-provider-error"
	artifactDir, stateDir, workDir, observeDir := crashResumeTestDirs(t, featureID)
	cfg := newCrashResumeLoopConfig(t, artifactDir, stateDir, workDir, observeDir, featureID)
	builds := 0
	cfg.BuildSession = func(BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		builds++
		return []string{"mock-agent"}, nil, &session.SessionOpts{SupportsSessionResume: true}, nil
	}
	cfg.SessionStartFunc = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		sess := &crashResumeTestSession{utilityTestSession: newUtilityTestSession(), providerSessionID: "native-failed-turn"}
		sess.result = &llm.ResultMessage{Subtype: "error", IsError: true, Result: terminalImageError}
		// A closed process also exposes the retry bug without hanging the test.
		close(sess.done)
		return sess, nil
	}
	_, err := RunImplementationLoop(cfg, nil)
	if err == nil || !strings.Contains(err.Error(), terminalImageError) {
		t.Fatalf("RunImplementationLoop() error = %v, want provider diagnostic", err)
	}
	if builds != 1 {
		t.Fatalf("BuildSession calls = %d, want one (no replay of failed input)", builds)
	}
	meta, err := NewArtifactManager(artifactDir).ReadMeta(filepath.Join(artifactDir, "iteration-01"))
	if err != nil || meta.AgentStatus != agentStatusFailed {
		t.Fatalf("iteration metadata = %+v, err = %v, want durable FAILED outcome", meta, err)
	}
	events := readObserveEvents(t, observeDir, featureID)
	if got := len(filterEventsByType(events, "iteration.ended")); got != 1 {
		t.Fatalf("iteration.ended events = %d, want one", got)
	}
}

func TestWaitForPhaseOutcome_TerminalErrorShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *llm.ResultMessage
		want   string
	}{
		{"subtype only", &llm.ResultMessage{Subtype: "error", Result: terminalImageError}, terminalImageError},
		{"error flag", &llm.ResultMessage{Subtype: "success", IsError: true, Result: terminalImageError}, terminalImageError},
		{"empty diagnostic", &llm.ResultMessage{Subtype: "error"}, "provider returned a terminal error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newBgTaskSession()
			sess.result = tc.result
			sess.statusCh <- statusFromResult(tc.result)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx})
			if got.Status != agentStatusFailed || got.Err == nil || got.Err.Error() != tc.want || !sess.stopped.Load() {
				t.Fatalf("WaitForPhaseOutcome() = %+v, stopped = %v, want failure %q", got, sess.stopped.Load(), tc.want)
			}
		})
	}
}
