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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

type completionToolSession struct {
	*nudgeRecorderSession
	requests chan llm.PhaseCompletionRequest
	respond  func(string, bool, string) error
	tasks    int
	stopped  bool
}

func newCompletionToolSession() *completionToolSession {
	return &completionToolSession{nudgeRecorderSession: newNudgeRecorderSession(nil), requests: make(chan llm.PhaseCompletionRequest, 1)}
}
func (s *completionToolSession) UsesStructuredCompletion() bool { return true }
func (s *completionToolSession) PhaseCompletionRequests() <-chan llm.PhaseCompletionRequest {
	return s.requests
}
func (s *completionToolSession) RespondToPhaseCompletion(id string, accepted bool, reason string) error {
	return s.respond(id, accepted, reason)
}
func (s *completionToolSession) LiveBackgroundTaskCount() int { return s.tasks }
func (s *completionToolSession) Stop() error                  { s.stopped = true; return s.utilityTestSession.Stop() }

func TestStructuredCompletionRejectsIncompleteInquiryThenCommitsRepairedArtifact(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "questions.md")
	if err := os.WriteFile(artifact, []byte("# Inquiry\n\nThe question is still awaiting an answer.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := newCompletionToolSession()
	request := llm.PhaseCompletionRequest{RequestID: "first", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
	sess.requests <- request
	responses := 0
	sess.respond = func(id string, accepted bool, reason string) error {
		responses++
		if sess.stopped {
			t.Fatal("stopped provider before returning tool response")
		}
		if responses == 1 {
			if accepted || reason == "" {
				t.Fatalf("unfinished artifact accepted: %q", reason)
			}
			if _, err := os.Stat(filepath.Join(dir, PhaseCompleteFile)); !os.IsNotExist(err) {
				t.Fatalf("rejected request wrote receipt: %v", err)
			}
			if err := os.WriteFile(artifact, []byte("# Research Questions\n\n1. Which component initializes repositories?\n"), 0600); err != nil {
				return err
			}
			request.RequestID = "repaired"
			sess.requests <- request
		} else if !accepted {
			t.Fatalf("repaired artifact rejected: %s", reason)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx, CommitOutcome: func(intent llm.CompletionIntent) ([]ProtocolViolation, error) {
		_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{Phase: feature.PhaseInquire, Role: RoleInquirer, ArtifactDir: dir, SessionID: "root", Intent: intent})
		return violations, err
	}})
	if result.Status != agentStatusSuccess || responses != 2 || !sess.stopped {
		t.Fatalf("result=%+v responses=%d stopped=%v", result, responses, sess.stopped)
	}
	if !HasCommittedPhaseOutcome(dir, feature.PhaseInquire, RoleInquirer) {
		t.Fatal("missing valid receipt")
	}
	if len(sess.messages) > 0 {
		t.Fatalf("completion correction became user messages: %v", sess.messages)
	}
}

func TestStructuredCompletionRejectsUnansweredQuestionAndLiveTasks(t *testing.T) {
	for _, scenario := range []string{"question", "tasks"} {
		t.Run(scenario, func(t *testing.T) {
			sess := newCompletionToolSession()
			if scenario == "question" {
				sess.rootAsk = true
			} else {
				sess.tasks = 1
			}
			responded := false
			sess.respond = func(_ string, accepted bool, reason string) error {
				responded = true
				if accepted || reason == "" {
					t.Fatalf("accepted unresolved work: %q", reason)
				}
				return nil
			}
			resolution, err := resolvePhaseCompletion(sess, llm.PhaseCompletionRequest{RequestID: "call", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}, func(llm.CompletionIntent) ([]ProtocolViolation, error) {
				t.Fatal("committer called before liveness validation")
				return nil, nil
			})
			if resolution.Accepted || !resolution.Deferred || err != nil || !responded {
				t.Fatalf("resolution=%+v err=%v responded=%v", resolution, err, responded)
			}
		})
	}
}

func TestStructuredCompletionRejectedThenPlainTurnReportsCommitViolations(t *testing.T) {
	sess := newCompletionToolSession()
	sess.requests <- llm.PhaseCompletionRequest{RequestID: "first", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
	sess.respond = func(_ string, accepted bool, _ string) error {
		if accepted {
			t.Fatal("defective artifact accepted")
		}
		// The model gives up and ends the turn in prose.
		sess.result = newEndedAfterTextResult()
		sess.statusCh <- agentStatusSuccess
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx, CommitOutcome: func(llm.CompletionIntent) ([]ProtocolViolation, error) {
		return []ProtocolViolation{{Artifact: "design.md", Reason: "design must contain a nonempty `## Acceptance Criteria` section"}}, nil
	}})
	if result.Status != agentStatusProtocolViolation {
		t.Fatalf("result=%+v", result)
	}
	if got := JoinProtocolViolations(result.ProtocolViolations); !strings.Contains(got, "Acceptance Criteria") || strings.Contains(got, "without exactly one structured completion outcome") {
		t.Fatalf("violations lost the rejected completion's reasons: %s", got)
	}
}

func TestStructuredCompletionDeferredRejectionsDoNotConsumeBudget(t *testing.T) {
	sess := newCompletionToolSession()
	sess.tasks = 1
	attempts := 0
	sess.respond = func(_ string, accepted bool, reason string) error {
		if accepted {
			if sess.tasks > 0 {
				t.Fatal("completion accepted while delegated tasks were running")
			}
			return nil
		}
		attempts++
		if attempts <= maxFinishOrViolateNudges+2 {
			// The model keeps retrying while its tasks run.
			sess.requests <- llm.PhaseCompletionRequest{RequestID: fmt.Sprintf("retry-%d", attempts), Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
			return nil
		}
		sess.tasks = 0
		sess.requests <- llm.PhaseCompletionRequest{RequestID: "final", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
		return nil
	}
	sess.requests <- llm.PhaseCompletionRequest{RequestID: "first", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx, CommitOutcome: func(llm.CompletionIntent) ([]ProtocolViolation, error) {
		return nil, nil
	}})
	if result.Status != agentStatusSuccess || attempts <= maxFinishOrViolateNudges {
		t.Fatalf("result=%+v attempts=%d: waiting on live tasks was charged as protocol violations", result, attempts)
	}
}

func TestStructuredCompletionPlainTurnFailsWithoutSuccessNudge(t *testing.T) {
	sess := newCompletionToolSession()
	sess.result = newEndedAfterTextResult()
	sess.statusCh <- agentStatusSuccess
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx, CommitOutcome: func(llm.CompletionIntent) ([]ProtocolViolation, error) {
		t.Fatal("plain turn committed")
		return nil, nil
	}})
	if result.Status != agentStatusProtocolViolation || len(sess.messages) != 0 {
		t.Fatalf("result=%+v nudges=%v", result, sess.messages)
	}
}

func TestStructuredCompletionPromptAndHandoffUseTools(t *testing.T) {
	prompt := BuildRoleSystemPrompt(BuildRoleSystemPromptInput{Spec: InquirerRoleSpec(), IterationDir: t.TempDir(), CompletionTool: "complete_phase"})
	if strings.Contains(prompt, "<agentico-outcome>") || !strings.Contains(prompt, "call `complete_phase`") {
		t.Fatalf("wrong completion transport: %s", prompt)
	}
	if strings.Contains(prompt, `{"status":"retry"}`) {
		t.Fatal("inquiry advertised unsupported retry")
	}
	continuation := autoResumeMessageForSession(newCompletionToolSession())
	if strings.Contains(continuation, "<agentico-outcome>") || !strings.Contains(continuation, "`complete_phase`") {
		t.Fatalf("wrong continuation transport: %s", continuation)
	}
	handoff := contextHandoffMessageForSession(newCompletionToolSession(), contextSnapshot{Pct: 80, ThresholdPct: 75})
	if strings.Contains(handoff, "<agentico-outcome>") || !strings.Contains(handoff, "Call `complete_phase`") {
		t.Fatalf("wrong handoff transport: %s", handoff)
	}
}

func TestStructuredCompletionAcknowledgmentFailurePreservesCommittedOutcome(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "questions.md"), []byte("# Research Questions\n\n1. Where is configuration loaded?\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := newCompletionToolSession()
	sess.requests <- llm.PhaseCompletionRequest{RequestID: "complete", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
	sess.respond = func(_ string, accepted bool, _ string) error {
		if !accepted || !HasCommittedPhaseOutcome(dir, feature.PhaseInquire, RoleInquirer) {
			t.Fatal("completion response preceded durable commit")
		}
		return errors.New("provider pipe closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := WaitForPhaseOutcome(sess, PhaseOutcomeWaitOptions{Ctx: ctx, CommitOutcome: func(intent llm.CompletionIntent) ([]ProtocolViolation, error) {
		_, _, violations, err := CommitPhaseOutcome(CompletionCommitInput{Phase: feature.PhaseInquire, Role: RoleInquirer, ArtifactDir: dir, SessionID: "root", Intent: intent})
		return violations, err
	}})
	if result.Status != agentStatusSuccess || !sess.stopped {
		t.Fatalf("committed phase reported failure: %+v", result)
	}
}

func TestStructuredBoundedHelperValidatesOutputBeforeCommitWithoutTerminalResult(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "questions.md"), []byte("# Research Questions\n\n1. Where is configuration loaded?\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(t.TempDir(), "response.txt")
	sess := newCompletionToolSession()
	sess.id = "helper"
	request := llm.PhaseCompletionRequest{RequestID: "empty-output", Intent: llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}}
	sess.requests <- request
	responses := 0
	sess.respond = func(_ string, accepted bool, reason string) error {
		responses++
		if responses == 1 {
			if accepted || !strings.Contains(reason, "without output") || HasCommittedPhaseOutcome(dir, feature.PhaseInquire, RoleInquirer) {
				t.Fatalf("empty output was committed: accepted=%v reason=%s", accepted, reason)
			}
			if err := os.WriteFile(outputPath, []byte("Research questions prepared."), 0600); err != nil {
				return err
			}
			request.RequestID = "repaired-output"
			sess.requests <- request
		} else if !accepted || !HasCommittedPhaseOutcome(dir, feature.PhaseInquire, RoleInquirer) {
			t.Fatalf("valid output not committed before response: %s", reason)
		}
		return nil
	}
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(string, string, feature.Phase, []string, string, []string, ...*ports.SessionOpts) (ports.SessionHandle, error) {
		return sess, nil
	}
	pr := &PhaseRunner{SessionManager: sm}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err, retryable := pr.runBoundedHelperSessionOnce(ctx, boundedHelperRunConfig{
		sessionID: "helper", responsePath: outputPath, requireOutput: true,
		completionDir: dir, contractPhase: feature.PhaseInquire, contractRole: RoleInquirer,
	}, "structured helper")
	if err != nil || retryable || result.Status != BoundedHelperStatusCompleted || result.Result != nil || responses != 2 || !sess.stopped {
		t.Fatalf("result=%+v err=%v retryable=%v responses=%d stopped=%v", result, err, retryable, responses, sess.stopped)
	}
}
