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

package tui

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// noopWriteCloser fulfills the io.WriteCloser contract that
// Session.SetStdinForTest expects so RespondToAskUser's writeJSON call
// returns nil rather than "session stdin is closed".
type noopWriteCloser struct{}

func (noopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (noopWriteCloser) Close() error                { return nil }

var _ io.WriteCloser = noopWriteCloser{}

// makeSessionWithQALog returns a *session.Session with one Q&A pair captured
// in qaLog so QALog() returns a non-empty slice. Uses RespondToAskUser
// (the production qaLog mutator) so the test exercises the actual data path
// the gate is gating.
func makeSessionWithQALog(t *testing.T, sessionID, featureID string, phase feature.Phase) *session.Session {
	t.Helper()
	sess := session.NewSession(sessionID, featureID, phase)
	sess.SetStdinForTest(noopWriteCloser{})
	if err := sess.RespondToAskUser(
		"req-1",
		json.RawMessage(`[{"question":"Q1"}]`),
		map[string]string{"Q1": "A1"},
		nil,
	); err != nil {
		t.Fatalf("RespondToAskUser: %v", err)
	}
	if len(sess.QALog()) == 0 {
		t.Fatalf("expected QALog populated by RespondToAskUser, got empty")
	}
	return sess
}

// statusForPhase pairs each phase with the matching feature.Status the TUI
// success/handleSessionDone branches expect to see when this phase is
// "currently running". Using the wrong status (e.g. StatusCreated) trips
// guards like featureCanRecoverQuestion or hasAdvancedPast.
func statusForPhase(p feature.Phase) feature.Status {
	switch p {
	case feature.PhaseKnowledgeBase:
		return feature.StatusBuildingKB
	case feature.PhaseInquire:
		return feature.StatusInquiring
	case feature.PhaseResearch:
		return feature.StatusResearching
	case feature.PhaseBrainstorm:
		return feature.StatusBrainstorming
	}
	return feature.StatusCreated
}

// TestTUI_HandleSessionDone_QAWritesForInteractivePlanningPhases drives
// handleSessionDone for each of {Research, Inquire, Brainstorm, KnowledgeBase}
// and asserts qa-answers.md is written for phases whose completed sessions
// can carry user Q&A.
func TestTUI_HandleSessionDone_QAWritesForInteractivePlanningPhases(t *testing.T) {
	cases := []struct {
		name       string
		phase      feature.Phase
		wantQAFile bool
	}{
		{"research_writes", feature.PhaseResearch, true},
		{"inquire_writes", feature.PhaseInquire, true},
		{"brainstorm_writes", feature.PhaseBrainstorm, true},
		{"knowledge_base_skips", feature.PhaseKnowledgeBase, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)
			app.orchestrator = &fakeOrch{}
			t.Cleanup(func() {
				if waiter, ok := app.orchestrator.(interface{ WaitForCycles() }); ok {
					waiter.WaitForCycles()
				}
				app.sessionManager.Shutdown()
			})

			f, err := fm.Create("QA Gate Test", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = statusForPhase(tc.phase)
				ff.CurrentPhase = tc.phase
				ff.Checkpoints = feature.Checkpoints{
					InquiryReview:  true,
					ResearchReview: true,
					DesignReview:   true,
					PlanReview:     true,
				}
				return nil
			})

			sm := app.sessionManager
			sess := makeSessionWithQALog(t, f.ID+"-"+tc.phase.DirName(), f.ID, tc.phase)
			sess.SendStatus("SUCCESS")
			// Pre-write the completion marker and phase artifact so the
			// registry-owned completion contract passes for artifact phases.
			if tc.phase != feature.PhaseKnowledgeBase {
				artifactDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), tc.phase.DirName())
				if err := os.MkdirAll(artifactDir, 0o755); err != nil {
					t.Fatalf("mkdir artifact dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(artifactDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
					t.Fatalf("write phase_complete: %v", err)
				}
				artifact := filepath.Join(artifactDir, tc.phase.DirName()+".md")
				if err := os.WriteFile(artifact, []byte("# "+tc.phase.DirName()+"\n"), 0o644); err != nil {
					t.Fatalf("write artifact: %v", err)
				}
			}
			sm.RegisterTestSession(sess)

			doneMsg := SessionDoneTUIMsg{
				Done: session.SessionDoneMsg{
					SessionID: sess.ID(),
					FeatureID: f.ID,
					Phase:     tc.phase,
					Status:    session.SessionDone,
				},
			}
			_, _ = app.handleSessionDone(doneMsg)

			artifactDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), tc.phase.DirName())
			qaPath := filepath.Join(artifactDir, "qa-answers.md")
			_, err = os.Stat(qaPath)
			switch {
			case tc.wantQAFile && err != nil:
				t.Errorf("expected qa-answers.md to be written for phase %q, but stat err = %v", tc.phase, err)
			case !tc.wantQAFile && err == nil:
				t.Errorf("expected NO qa-answers.md to be written for phase %q, but the file exists", tc.phase)
			}
		})
	}
}

// TestTUI_ResultSuccess_ProtocolValidationFailureDoesNotWriteQA drives the
// SDK-Result success branch for registry-owned artifact phases without
// phase_complete. Protocol validation must fail before any TUI-side QA write.
func TestTUI_ResultSuccess_ProtocolValidationFailureDoesNotWriteQA(t *testing.T) {
	cases := []struct {
		name       string
		phase      feature.Phase
		wantQAFile bool
	}{
		{"research_skips", feature.PhaseResearch, false},
		{"inquire_skips", feature.PhaseInquire, false},
		{"brainstorm_skips", feature.PhaseBrainstorm, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)

			f, err := fm.Create("QA Gate Result", "desc", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = statusForPhase(tc.phase)
				ff.CurrentPhase = tc.phase
				return nil
			})

			sm := app.sessionManager
			sess := makeSessionWithQALog(t, f.ID+"-"+tc.phase.DirName(), f.ID, tc.phase)
			// Plain text (no '?') keeps the transcript unambiguous while the
			// SDK-result branch routes through registry protocol validation.
			sess.MessageLog().Append(llm.SDKMessage{
				Type: "assistant",
				Assistant: &llm.AssistantMessage{
					Message: llm.ConversationMsg{
						Role: "assistant",
						Content: []llm.ContentBlock{
							{Type: "text", Text: "## Analysis\n\nNo artifact written for this run."},
						},
					},
				},
			})
			sm.RegisterTestSession(sess)

			msg := SDKSessionEventMsg{
				Event: session.SDKEventMsg{
					SessionID: sess.ID(),
					FeatureID: f.ID,
					Phase:     tc.phase,
					Message: llm.SDKMessage{
						Type:    "result",
						Subtype: "success",
						Result: &llm.ResultMessage{
							Type:    "result",
							Subtype: "success",
						},
					},
				},
			}
			_, _ = app.handleSDKEvent(msg)

			artifactDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), tc.phase.DirName())
			qaPath := filepath.Join(artifactDir, "qa-answers.md")
			_, err = os.Stat(qaPath)
			switch {
			case tc.wantQAFile && err != nil:
				t.Errorf("expected qa-answers.md for phase %q; stat err = %v", tc.phase, err)
			case !tc.wantQAFile && err == nil:
				t.Errorf("expected NO qa-answers.md for phase %q on protocol-validation failure; file exists", tc.phase)
			}
		})
	}
}
