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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const grillMeSmokeStaleQAFile = `# User Q&A — Phase Clarifications

## Q: What does the user want?
**A:** Option A

## Q: How confident are we in the recommended option?
**A:** Recommended option B
_(auto-picked, confidence: 0.85)_
`

const grillMeSmokeHarnessQAFile = `# User Q&A — Phase Clarifications

## Q: Q1

**A:** A1

`

const grillMeAutoPickSmokeHarnessQAFile = `# User Q&A — Phase Clarifications

## Q: Which planning path?

**A:** Focused (Recommended)

_(auto-picked, confidence: 0.85)_

`

// TestInquirePhase_GrillMe_SmokeEndToEnd is the Phase 1 tracer-bullet smoke
// test. It exercises a single end-to-end path: directive flows through
// builder → prompt → session Q&A capture → TUI persistence gate
// (handleSessionDone) → orchestrator persistence gate (HandlePhaseCompletion
// → onArtifactPhaseCompleted). The gates must write the harness-owned Q&A
// transcript and surface it to downstream consumers via collectQAFilePaths.
func TestInquirePhase_GrillMe_SmokeEndToEnd(t *testing.T) {
	cases := []struct {
		name        string
		inquireness feature.Inquireness
	}{
		{
			name:        "medium",
			inquireness: feature.InquirenessMedium,
		},
		{
			name:        "high",
			inquireness: feature.InquirenessHigh,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, fm := newTestAppModel(t)

			f, err := fm.Create("Grill-Me Smoke", "exercise the [grill-me] directive end-to-end", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_ = fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
				ff.Status = feature.StatusInquiring
				ff.CurrentPhase = feature.PhaseInquire
				ff.Inquireness = tc.inquireness
				// Gate every downstream advance so the smoke test does not
				// touch a real PhaseRunner / model registry once the inquire
				// gate lands.
				ff.Checkpoints = feature.Checkpoints{
					InquiryReview:  true,
					ResearchReview: true,
					DesignReview:   true,
					PlanReview:     true,
				}
				return nil
			})

			f, _ = fm.Get(f.ID)

			// (a) Directive flows through builder → prompt.
			prompt := agent.BuildInquirePrompt(f, "")
			if !strings.Contains(prompt, "## Ambiguity Resolution [grill-me]") {
				t.Errorf("inquire prompt missing [grill-me] header for inquireness=%q", tc.inquireness)
			}
			for _, forbidden := range []string{
				"strictly greater than",
				"auto-pick",
				"auto-resolve",
				"silent",
				"qa-answers.md",
				"threshold",
			} {
				if strings.Contains(strings.ToLower(prompt), forbidden) {
					t.Errorf("inquire prompt unexpectedly contains policy/authorship term %q for inquireness=%q\n--- prompt tail ---\n%s", forbidden, tc.inquireness, promptTail(prompt))
				}
			}

			// (b) Seed a stale qa-answers.md in the inquire phase dir before
			// the session-done event fires. The harness-owned session log
			// should replace it.
			phaseDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "inquire")
			if err := os.MkdirAll(phaseDir, 0o755); err != nil {
				t.Fatalf("mkdir phaseDir: %v", err)
			}
			artifactPath := filepath.Join(phaseDir, "inquire.md")
			if err := os.WriteFile(artifactPath, []byte("# inquire\n"), 0o644); err != nil {
				t.Fatalf("write artifact: %v", err)
			}
			if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
				t.Fatalf("write phase_complete: %v", err)
			}
			qaPath := filepath.Join(phaseDir, "qa-answers.md")
			if err := os.WriteFile(qaPath, []byte(grillMeSmokeStaleQAFile), 0o644); err != nil {
				t.Fatalf("pre-write stale qa-answers.md: %v", err)
			}

			// (c) Wire a session whose QALog is non-empty so both gates'
			// session-side path is exercised with realistic input.
			sm := session.NewManager(nil)
			sess := makeSessionWithQALog(t, f.ID+"-inquire", f.ID, feature.PhaseInquire)
			sess.SendStatus("SUCCESS")
			sm.RegisterTestSession(sess)
			app.sessionManager = sm

			// (d) Drive handleSessionDone → TUI gate fires.
			doneMsg := SessionDoneTUIMsg{
				Done: session.SessionDoneMsg{
					SessionID: sess.ID(),
					FeatureID: f.ID,
					Phase:     feature.PhaseInquire,
					Status:    session.SessionDone,
				},
			}
			_, _ = app.handleSessionDone(doneMsg)

			// (e) Both gates honored: the transcript is written from the
			// session Q&A log, not from stale agent-authored bytes.
			got, err := os.ReadFile(qaPath)
			if err != nil {
				t.Fatalf("read qa-answers.md after both gates fired: %v", err)
			}
			if string(got) != grillMeSmokeHarnessQAFile {
				t.Errorf("qa-answers.md was not written from session QALog.\n--- got ---\n%s\n--- want ---\n%s", got, grillMeSmokeHarnessQAFile)
			}

			// (g) Downstream Design consumes the path via
			// collectQAFilePaths (refPrefix is empty for non-refactor
			// features). HandlePhaseCompletion routed through the
			// orchestrator's stateDir helper, so the same store/PhaseRunner
			// wiring the TUI uses must surface the inquire path.
			//
			// We re-derive expectations using the same agent.ActiveRunDir
			// helper the gate uses, so the assertion is robust to non-default
			// run-dir layouts.
			expected := qaPath
			// We cannot reach the orchestrator's unexported collectQAFilePaths
			// from this package; instead we confirm the file is at the
			// canonical path that collectQAFilePaths probes for the inquire
			// phase. Companion test
			// orchestrator.TestInquirePhase_WritesHarnessOwnedQAFile
			// pins the collectQAFilePaths reachability on the orchestrator
			// side; the smoke test pins the path-derivation contract here.
			if _, err := os.Stat(expected); err != nil {
				t.Fatalf("inquire qa file missing at canonical path %q: %v", expected, err)
			}
		})
	}
}

func TestInquirePhase_GrillMe_AutoPickSmokeEndToEnd(t *testing.T) {
	app, fm := newTestAppModel(t)
	eventCh := make(chan interface{}, 20)
	sm := session.NewManager(eventCh)
	t.Cleanup(sm.Shutdown)
	app.sessionManager = sm
	app.phaseRunner.SessionManager = sm
	app.phaseRunner.FeatureStore = fm.Store
	app.orchestrator = orchestrator.New(orchestrator.Deps{
		Lifecycle:   fm,
		Store:       fm.Store,
		Sessions:    sm,
		PhaseRunner: app.phaseRunner,
		CmdRunner:   app.phaseRunner.CommandRunner,
	}, orchestrator.Hooks{})

	f, err := fm.Create("Grill-Me Auto Pick Smoke", "exercise the [grill-me] auto-pick path", []string{"test-repo"}, fm.Config.Defaults.Models, "", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := fm.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusInquiring
		ff.CurrentPhase = feature.PhaseInquire
		ff.Inquireness = feature.InquirenessMedium
		ff.Checkpoints = feature.Checkpoints{InquiryReview: true}
		return nil
	}); err != nil {
		t.Fatalf("modify feature: %v", err)
	}
	f, err = fm.Get(f.ID)
	if err != nil {
		t.Fatalf("reload feature: %v", err)
	}

	phaseDir := filepath.Join(agent.ActiveRunDir(fm.Store.BaseDir, f), "inquire")
	if err := os.MkdirAll(phaseDir, 0o755); err != nil {
		t.Fatalf("mkdir phaseDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, "inquire.md"), []byte("# inquire\n"), 0o644); err != nil {
		t.Fatalf("write inquire artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phaseDir, agent.PhaseCompleteFile), nil, 0o644); err != nil {
		t.Fatalf("write phase_complete: %v", err)
	}
	qaPath := filepath.Join(phaseDir, "qa-answers.md")
	if err := os.WriteFile(qaPath, []byte(grillMeSmokeStaleQAFile), 0o644); err != nil {
		t.Fatalf("pre-write stale qa-answers.md: %v", err)
	}

	script := filepath.Join(t.TempDir(), "autopick-smoke.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 1 init_request || true
echo '{"type":"control_request","request_id":"ask_auto","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which planning path?","options":[{"label":"Focused (Recommended)","confidence":0.85},{"label":"Broad","confidence":0.2}]}]}}}'
if read -t 2 response; then
  case "$response" in
    *"Focused (Recommended)"*) echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"AUTO_PICK_RECEIVED"}]}}' ;;
    *) echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"WRONG_RESPONSE"}]}}' ;;
  esac
else
  echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"NO_AUTO_PICK_RESPONSE"}]}}'
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	sess, err := sm.StartSession(f.ID+"-inquire", f.ID, feature.PhaseInquire, []string{"bash", script}, t.TempDir(), nil, &session.SessionOpts{
		AskUserAutoPick: &ports.AskUserAutoPickConfig{
			Purpose: ports.AskUserAutoPickPurposeInquire,
			LoadInquireness: func() (feature.Inquireness, error) {
				return feature.InquirenessMedium, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for auto-pick smoke session")
	}

	if got := sess.MessageLog().AssistantText(); !strings.Contains(got, "AUTO_PICK_RECEIVED") {
		t.Fatalf("assistant transcript = %q, want AUTO_PICK_RECEIVED", got)
	}
	qa := sess.QALog()
	if len(qa) != 1 || qa[0].Question != "Which planning path?" || qa[0].Answer != "Focused (Recommended)" || !qa[0].AutoPicked || qa[0].Confidence != 0.85 {
		t.Fatalf("QALog() = %+v, want one auto-picked planning answer", qa)
	}
	if pending := sess.PendingControlRequests(); len(pending) != 0 {
		t.Fatalf("PendingControlRequests() = %+v, want none for auto-picked bundle", pending)
	}
	if sess.HasPendingAskUserQuestion() {
		t.Fatal("HasPendingAskUserQuestion() = true, want false for auto-picked bundle")
	}
	for {
		select {
		case evt := <-eventCh:
			if sdkEvt, ok := evt.(session.SDKEventMsg); ok && sdkEvt.Message.ControlRequest != nil {
				t.Fatalf("event channel surfaced auto-picked control_request: %+v", sdkEvt.Message.ControlRequest)
			}
		default:
			goto drained
		}
	}
drained:

	attach := attachModelFromSession(sess, 120, 40)
	if attach.hasActiveQuestion() || attach.awaitingInput || len(attach.pendingAskQueue) != 0 || attach.showPermMenu {
		t.Fatalf("attach model surfaced auto-picked bundle: active=%v awaiting=%v queue=%v perm=%v", attach.hasActiveQuestion(), attach.awaitingInput, attach.pendingAskQueue, attach.showPermMenu)
	}
	stale := llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "ask_auto",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    json.RawMessage(`{"questions":[{"question":"Which planning path?","options":[{"label":"Focused (Recommended)","confidence":0.85},{"label":"Broad","confidence":0.2}]}]}`),
			},
		},
	}
	attach, _ = attach.Update(attachMsgsMsg{generation: attach.tabGeneration, messages: []llm.SDKMessage{stale}})
	if attach.hasActiveQuestion() || len(attach.pendingAskQueue) != 0 {
		t.Fatalf("stale replay of auto-picked bundle surfaced in attach: active=%v queue=%v", attach.hasActiveQuestion(), attach.pendingAskQueue)
	}

	_, _ = app.handleSessionDone(SessionDoneTUIMsg{
		Done: session.SessionDoneMsg{
			SessionID: sess.ID(),
			FeatureID: f.ID,
			Phase:     feature.PhaseInquire,
			Status:    session.SessionDone,
		},
	})
	got, err := os.ReadFile(qaPath)
	if err != nil {
		t.Fatalf("read qa-answers.md: %v", err)
	}
	if string(got) != grillMeAutoPickSmokeHarnessQAFile {
		t.Fatalf("qa-answers.md mismatch\n--- got ---\n%s\n--- want ---\n%s", got, grillMeAutoPickSmokeHarnessQAFile)
	}
}

// promptTail returns the last 1500 bytes of a prompt for diagnostic display
// when the directive substring assertion fails.
func promptTail(prompt string) string {
	const tailBytes = 1500
	if len(prompt) <= tailBytes {
		return prompt
	}
	return "…" + prompt[len(prompt)-tailBytes:]
}
