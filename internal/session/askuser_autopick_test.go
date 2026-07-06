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
	"encoding/json"
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
)

func TestDecideAskUserAutoPick(t *testing.T) {
	tests := []struct {
		name        string
		purpose     ports.AskUserAutoPickPurpose
		inquireness feature.Inquireness
		input       string
		wantPick    bool
		wantAnswer  string
		wantConf    float64
	}{
		{
			name:        "none threshold picks above threshold",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo","confidence":0.51},{"label":"All","confidence":0.2}]}`),
			wantPick:    true,
			wantAnswer:  "Repo",
			wantConf:    0.51,
		},
		{
			name:        "none threshold equal picks",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo","confidence":0.5},{"label":"All","confidence":0.2}]}`),
			wantPick:    true,
			wantAnswer:  "Repo",
			wantConf:    0.5,
		},
		{
			name:        "none threshold below declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo (Recommended)","confidence":0.49},{"label":"All","confidence":0.2}]}`),
		},
		{
			name:        "medium threshold picks above threshold",
			purpose:     ports.AskUserAutoPickPurposeDesign,
			inquireness: feature.InquirenessMedium,
			input:       askInput(`{"question":"Design?","options":[{"label":"A","confidence":0.71},{"label":"B","confidence":0.2}]}`),
			wantPick:    true,
			wantAnswer:  "A",
			wantConf:    0.71,
		},
		{
			name:        "medium threshold equal picks",
			purpose:     ports.AskUserAutoPickPurposeDesign,
			inquireness: feature.InquirenessMedium,
			input:       askInput(`{"question":"Design?","options":[{"label":"A","confidence":0.7},{"label":"B","confidence":0.2}]}`),
			wantPick:    true,
			wantAnswer:  "A",
			wantConf:    0.7,
		},
		{
			name:        "medium threshold below declines",
			purpose:     ports.AskUserAutoPickPurposeDesign,
			inquireness: feature.InquirenessMedium,
			input:       askInput(`{"question":"Design?","options":[{"label":"A (Recommended)","confidence":0.69},{"label":"B","confidence":0.2}]}`),
		},
		{
			name:        "high inquireness disabled",
			purpose:     ports.AskUserAutoPickPurposeRoadmapCreator,
			inquireness: feature.InquirenessHigh,
			input:       askInput(`{"question":"Roadmap?","options":[{"label":"A (Recommended)","confidence":1.0},{"label":"B","confidence":0.2}]}`),
		},
		{
			name:        "phase plan creator uses none threshold override",
			purpose:     ports.AskUserAutoPickPurposePhasePlanCreator,
			inquireness: feature.InquirenessHigh,
			input:       askInput(`{"question":"Plan slice?","options":[{"label":"Tracer","confidence":0.51},{"label":"Full","confidence":0.2}]}`),
			wantPick:    true,
			wantAnswer:  "Tracer",
			wantConf:    0.51,
		},
		{
			name:        "disallowed purpose declines",
			purpose:     ports.AskUserAutoPickPurposeResearch,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Research?","options":[{"label":"A (Recommended)","confidence":0.9},{"label":"B","confidence":0.1}]}`),
		},
		{
			name:        "free text declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Explain the API shape","options":[]}`),
		},
		{
			name:        "multi select picks threshold-qualified options",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Pick any","multiSelect":true,"options":[{"label":"A (Recommended)","confidence":0.9},{"label":"B","confidence":0.1}]}`),
			wantPick:    true,
			wantAnswer:  "A (Recommended)",
			wantConf:    0.9,
		},
		{
			name:        "multi select records lowest selected confidence",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Pick any","multiSelect":true,"options":[{"label":"A","confidence":0.9},{"label":"B","confidence":0.5},{"label":"C","confidence":0.1}]}`),
			wantPick:    true,
			wantAnswer:  "A, B",
			wantConf:    0.5,
		},
		{
			name:        "multi select declines when no option meets threshold",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Pick any","multiSelect":true,"options":[{"label":"A","confidence":0.49},{"label":"B","confidence":0.1}]}`),
		},
		{
			name:        "single select picks threshold-qualified option without recommended suffix",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo","confidence":0.9},{"label":"All","confidence":0.1}]}`),
			wantPick:    true,
			wantAnswer:  "Repo",
			wantConf:    0.9,
		},
		{
			name:        "single select chooses highest confidence regardless of recommended suffix",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo (Recommended)","confidence":0.8},{"label":"All","confidence":0.9}]}`),
			wantPick:    true,
			wantAnswer:  "All",
			wantConf:    0.9,
		},
		{
			name:        "missing confidence declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo (Recommended)"},{"label":"All","confidence":0.1}]}`),
		},
		{
			name:        "out of range confidence declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo (Recommended)","confidence":1.1},{"label":"All","confidence":0.1}]}`),
		},
		{
			name:        "invalid confidence declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Scope?","options":[{"label":"Repo (Recommended)","confidence":"0.9"},{"label":"All","confidence":0.1}]}`),
		},
		{
			name:        "partially pickable bundle declines",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input: askInput(
				`{"question":"Scope?","options":[{"label":"Repo (Recommended)","confidence":0.9},{"label":"All","confidence":0.1}]}`,
				`{"question":"Deployment?","options":[]}`,
			),
		},
		{
			name:        "numbered options use current TUI label parsing",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"Which scope?\n1. Repository-first (Recommended): cover repo code. [confidence: 0.81]\n2. Everything: include adjacent state. [confidence: 0.34]\nReply with 1 or 2.","options":[]}`),
			wantPick:    true,
			wantAnswer:  "Repository-first (Recommended)",
			wantConf:    0.81,
		},
		{
			name:        "numbered options tolerate trailing stem and recommended after confidence",
			purpose:     ports.AskUserAutoPickPurposeInquire,
			inquireness: feature.InquirenessNone,
			input:       askInput(`{"question":"I read the research.\n\n1. Italian tech terms where established: Follow normal Italian developer-doc practice. [confidence: 0.85] (Recommended)\n2. Keep all English tech terms untranslated: Simpler for developers. [confidence: 0.45]\n3. Fully Italianize everything possible: Risks awkward calques. [confidence: 0.25]\n\nWhich technical-term strategy should the translation use?","options":[]}`),
			wantPick:    true,
			wantAnswer:  "Italian tech terms where established (Recommended)",
			wantConf:    0.85,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideAskUserAutoPick(json.RawMessage(tt.input), askUserAutoPickDecisionContext{
				Purpose:     tt.purpose,
				Inquireness: tt.inquireness,
			})
			if got.Pickable != tt.wantPick {
				t.Fatalf("decideAskUserAutoPick(%s).Pickable = %v, want %v (reason=%s)", tt.name, got.Pickable, tt.wantPick, got.Reason)
			}
			if !tt.wantPick {
				if len(got.Answers) != 0 {
					t.Errorf("decideAskUserAutoPick(%s).Answers = %v, want empty", tt.name, got.Answers)
				}
				return
			}
			if got.Answers["Scope?"] != "" && got.Answers["Scope?"] != tt.wantAnswer {
				t.Errorf("decideAskUserAutoPick(%s) Scope answer = %q, want %q", tt.name, got.Answers["Scope?"], tt.wantAnswer)
			}
			if got.Answers["Design?"] != "" && got.Answers["Design?"] != tt.wantAnswer {
				t.Errorf("decideAskUserAutoPick(%s) Design answer = %q, want %q", tt.name, got.Answers["Design?"], tt.wantAnswer)
			}
			if len(got.Selections) != 1 {
				t.Fatalf("decideAskUserAutoPick(%s) len(Selections) = %d, want 1", tt.name, len(got.Selections))
			}
			if got.Selections[0].Answer != tt.wantAnswer {
				t.Errorf("decideAskUserAutoPick(%s) Answer = %q, want %q", tt.name, got.Selections[0].Answer, tt.wantAnswer)
			}
			if got.Selections[0].Confidence != tt.wantConf {
				t.Errorf("decideAskUserAutoPick(%s) Confidence = %v, want %v", tt.name, got.Selections[0].Confidence, tt.wantConf)
			}
		})
	}
}

func TestAskUserAutoPickPurposeCanPickAllowlist(t *testing.T) {
	tests := []struct {
		purpose ports.AskUserAutoPickPurpose
		want    bool
	}{
		{ports.AskUserAutoPickPurposeInquire, true},
		{ports.AskUserAutoPickPurposeDesign, true},
		{ports.AskUserAutoPickPurposeRoadmapCreator, true},
		{ports.AskUserAutoPickPurposePhasePlanCreator, true},
		{ports.AskUserAutoPickPurposeNone, false},
		{ports.AskUserAutoPickPurposeResearch, false},
		{ports.AskUserAutoPickPurposeImplement, false},
		{ports.AskUserAutoPickPurposeReview, false},
		{ports.AskUserAutoPickPurposeKBBuild, false},
		{ports.AskUserAutoPickPurposeChat, false},
		{ports.AskUserAutoPickPurposeTweak, false},
		{ports.AskUserAutoPickPurposeFinalReview, false},
		{ports.AskUserAutoPickPurposeValidator, false},
		{ports.AskUserAutoPickPurposeRoadmapReviser, false},
		{ports.AskUserAutoPickPurposePhasePlanReviser, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.purpose), func(t *testing.T) {
			if got := askUserAutoPickPurposeCanPick(tt.purpose); got != tt.want {
				t.Errorf("askUserAutoPickPurposeCanPick(%q) = %v, want %v", tt.purpose, got, tt.want)
			}
		})
	}
}

func TestManagerAutoPick_AnswersAndSuppressesAskUserQuestion(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "autopick.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 1 init_request
echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Color?","options":[{"label":"Red (Recommended)","confidence":0.9},{"label":"Blue","confidence":0.1}]}]}}}'
read -t 2 response
if [ -n "$response" ]; then
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"AUTO_PICKED\"}]}}"
else
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"BLOCKED\"}]}}"
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	eventCh := make(chan interface{}, 20)
	mgr := NewManager(eventCh)
	var observed []string
	sess, err := mgr.StartSession("autopick-test", "feat-1", feature.PhaseInquire,
		[]string{"bash", script}, dir, nil, &SessionOpts{
			AskUserAutoPick: &ports.AskUserAutoPickConfig{
				Purpose: ports.AskUserAutoPickPurposeInquire,
				LoadInquireness: func() (feature.Inquireness, error) {
					return feature.InquirenessNone, nil
				},
				OnQuestionAutoPicked: func(question, answer string, confidence float64) {
					observed = append(observed, fmt.Sprintf("%s=%s:%.2f", question, answer, confidence))
				},
			},
		})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session")
	}

	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "AUTO_PICKED") {
		t.Fatalf("assistant output = %q, want AUTO_PICKED", output)
	}
	if got := sess.PendingControlRequests(); len(got) != 0 {
		t.Fatalf("PendingControlRequests() = %v, want empty", got)
	}
	if sess.Status() == SessionWaitingHelp {
		t.Fatal("Status() = SessionWaitingHelp, want non-waiting")
	}
	if len(observed) != 1 || observed[0] != "Color?=Red (Recommended):0.90" {
		t.Fatalf("observed auto-pick callbacks = %v, want Color? callback", observed)
	}

	qa := sess.QALog()
	if len(qa) != 1 {
		t.Fatalf("len(QALog()) = %d, want 1", len(qa))
	}
	if qa[0].Question != "Color?" || qa[0].Answer != "Red (Recommended)" || !qa[0].AutoPicked || qa[0].Confidence != 0.9 {
		t.Fatalf("QALog()[0] = %+v, want auto-picked Color? metadata", qa[0])
	}
	var renderedAutoPick bool
	for _, msg := range sess.MessageLog().Messages() {
		if msg.User != nil && msg.LocallyAppended && msg.AutoPicked && msg.AutoPickConfidence == 0.9 {
			for _, block := range msg.User.Message.Content {
				if block.IsText() && block.Text == "Red (Recommended)" {
					renderedAutoPick = true
				}
			}
		}
	}
	if !renderedAutoPick {
		t.Fatalf("MessageLog() missing auto-picked local user message: %+v", sess.MessageLog().Messages())
	}

	for {
		select {
		case evt := <-eventCh:
			if sdkEvt, ok := evt.(SDKEventMsg); ok && sdkEvt.Message.ControlRequest != nil {
				t.Fatalf("event channel received auto-picked control_request: %+v", sdkEvt.Message.ControlRequest)
			}
		default:
			return
		}
	}
}

func TestManagerAutoPick_UsesAssistantToolUseConfidenceWhenControlRequestStripsIt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "autopick-stripped-confidence.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 1 init_request
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ask_1","name":"AskUserQuestion","input":{"questions":[{"question":"Color?","options":[{"label":"Red (Recommended)","description":"Pick red.","confidence":0.9},{"label":"Blue","description":"Pick blue.","confidence":0.1}]}]}}]}}'
echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Color?","options":[{"label":"Red (Recommended)","description":"Pick red."},{"label":"Blue","description":"Pick blue."}]}]}}}'
read -t 2 response
if [ -n "$response" ]; then
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"AUTO_PICKED\"}]}}"
else
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"BLOCKED\"}]}}"
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	eventCh := make(chan interface{}, 20)
	mgr := NewManager(eventCh)
	sess, err := mgr.StartSession("autopick-stripped-confidence-test", "feat-1", feature.PhaseInquire,
		[]string{"bash", script}, dir, nil, &SessionOpts{
			AskUserAutoPick: &ports.AskUserAutoPickConfig{
				Purpose: ports.AskUserAutoPickPurposeInquire,
				LoadInquireness: func() (feature.Inquireness, error) {
					return feature.InquirenessNone, nil
				},
			},
		})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session")
	}

	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "AUTO_PICKED") {
		t.Fatalf("assistant output = %q, want AUTO_PICKED", output)
	}
	if got := sess.PendingControlRequests(); len(got) != 0 {
		t.Fatalf("PendingControlRequests() = %v, want empty", got)
	}
	qa := sess.QALog()
	if len(qa) != 1 {
		t.Fatalf("len(QALog()) = %d, want 1", len(qa))
	}
	if qa[0].Question != "Color?" || qa[0].Answer != "Red (Recommended)" || !qa[0].AutoPicked || qa[0].Confidence != 0.9 {
		t.Fatalf("QALog()[0] = %+v, want auto-picked Color? metadata from tool_use input", qa[0])
	}
}

func TestManagerAutoPick_MultiSelectUsesAssistantToolUseConfidenceWhenControlRequestStripsIt(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "autopick-multiselect-stripped-confidence.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 1 init_request
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ask_1","name":"AskUserQuestion","input":{"questions":[{"question":"Aids?","multiSelect":true,"options":[{"label":"Legal move highlights","description":"Show legal moves.","confidence":0.9},{"label":"Last move highlight","description":"Show the previous move.","confidence":0.85},{"label":"Hints","description":"Suggest a move.","confidence":0.2}]}]}}]}}'
echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Aids?","multiSelect":true,"options":[{"label":"Legal move highlights","description":"Show legal moves."},{"label":"Last move highlight","description":"Show the previous move."},{"label":"Hints","description":"Suggest a move."}]}]}}}'
read -t 2 response
if [ -n "$response" ]; then
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"AUTO_PICKED\"}]}}"
else
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"BLOCKED\"}]}}"
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	eventCh := make(chan interface{}, 20)
	mgr := NewManager(eventCh)
	sess, err := mgr.StartSession("autopick-multiselect-stripped-confidence-test", "feat-1", feature.PhaseInquire,
		[]string{"bash", script}, dir, nil, &SessionOpts{
			AskUserAutoPick: &ports.AskUserAutoPickConfig{
				Purpose: ports.AskUserAutoPickPurposeInquire,
				LoadInquireness: func() (feature.Inquireness, error) {
					return feature.InquirenessNone, nil
				},
			},
		})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for session")
	}

	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "AUTO_PICKED") {
		t.Fatalf("assistant output = %q, want AUTO_PICKED", output)
	}
	if got := sess.PendingControlRequests(); len(got) != 0 {
		t.Fatalf("PendingControlRequests() = %v, want empty", got)
	}
	qa := sess.QALog()
	if len(qa) != 1 {
		t.Fatalf("len(QALog()) = %d, want 1", len(qa))
	}
	if qa[0].Question != "Aids?" || qa[0].Answer != "Legal move highlights, Last move highlight" || !qa[0].AutoPicked || qa[0].Confidence != 0.85 {
		t.Fatalf("QALog()[0] = %+v, want multi-select auto-picked metadata from tool_use input", qa[0])
	}
}

func TestTryHandleControlRequest_AutoPickLoadFailureFailsClosed(t *testing.T) {
	s := NewSession("autopick-load-failure", "feat-1", feature.PhaseInquire)
	s.askUserAutoPick = &ports.AskUserAutoPickConfig{
		Purpose: ports.AskUserAutoPickPurposeInquire,
		LoadInquireness: func() (feature.Inquireness, error) {
			return "", errors.New("feature store unavailable")
		},
	}

	handled := s.tryHandleControlRequest(llm.SDKMessage{
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: "ask_1",
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
				Input:    json.RawMessage(askInput(`{"question":"Color?","options":[{"label":"Red (Recommended)","confidence":0.9},{"label":"Blue","confidence":0.1}]}`)),
			},
		},
	})
	if handled {
		t.Fatal("tryHandleControlRequest() = true, want false on inquireness load failure")
	}
	if len(s.QALog()) != 0 {
		t.Fatalf("QALog() = %+v, want empty", s.QALog())
	}
}

func TestManagerAutoPick_LoadsInquirenessForEachBundleAndKeepsNonPickablePending(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "autopick-dynamic.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"type":"system","subtype":"init","session_id":"s1","model":"test"}'
read -t 1 init_request
echo '{"type":"control_request","request_id":"ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"First?","options":[{"label":"A (Recommended)","confidence":0.9},{"label":"B","confidence":0.1}]}]}}}'
read -t 1 first_response
echo '{"type":"control_request","request_id":"ask_2","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Second?","options":[{"label":"C (Recommended)","confidence":0.9},{"label":"D","confidence":0.1}]}]}}}'
read -t 2 second_response
if [ -z "$first_response" ] && [ -n "$second_response" ]; then
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"FIRST_PENDING_SECOND_PICKED\"}]}}"
else
  echo "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"UNEXPECTED_ROUTING\"}]}}"
fi
echo '{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0}'
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	eventCh := make(chan interface{}, 20)
	mgr := NewManager(eventCh)
	loads := 0
	sess, err := mgr.StartSession("autopick-dynamic-test", "feat-1", feature.PhaseInquire,
		[]string{"bash", script}, dir, nil, &SessionOpts{
			AskUserAutoPick: &ports.AskUserAutoPickConfig{
				Purpose: ports.AskUserAutoPickPurposeInquire,
				LoadInquireness: func() (feature.Inquireness, error) {
					loads++
					if loads == 1 {
						return feature.InquirenessHigh, nil
					}
					return feature.InquirenessNone, nil
				},
			},
		})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("timeout waiting for session")
	}

	if loads != 2 {
		t.Fatalf("LoadInquireness calls = %d, want 2", loads)
	}
	output := sess.MessageLog().AssistantText()
	if !strings.Contains(output, "FIRST_PENDING_SECOND_PICKED") {
		t.Fatalf("assistant output = %q, want FIRST_PENDING_SECOND_PICKED", output)
	}
	pending := sess.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "ask_1" {
		t.Fatalf("PendingControlRequests() = %+v, want only ask_1", pending)
	}
	qa := sess.QALog()
	if len(qa) != 1 || qa[0].Question != "Second?" || qa[0].Answer != "C (Recommended)" {
		t.Fatalf("QALog() = %+v, want only auto-picked second question", qa)
	}

	controlEvents := 0
	for {
		select {
		case evt := <-eventCh:
			if sdkEvt, ok := evt.(SDKEventMsg); ok && sdkEvt.Message.ControlRequest != nil {
				controlEvents++
				if sdkEvt.Message.ControlRequest.RequestID != "ask_1" {
					t.Fatalf("control event requestID = %q, want only ask_1", sdkEvt.Message.ControlRequest.RequestID)
				}
			}
		default:
			if controlEvents != 1 {
				t.Fatalf("control event count = %d, want 1", controlEvents)
			}
			return
		}
	}
}

func TestRespondToAskUserAutoPicked_PreservesWaitingHelpForOtherPendingQuestion(t *testing.T) {
	s := NewSession("autopick-parallel-state", "feat-1", feature.PhaseInquire)
	s.protocol = &stubProtocol{}
	s.mu.Lock()
	s.status = SessionWaitingHelp
	s.hasUnansweredQuestion = true
	s.recordPendingControlRequestLocked(&llm.ControlRequestMessage{
		RequestID: "ask_1",
		Request: llm.ControlRequest{
			Subtype:  "can_use_tool",
			ToolName: "AskUserQuestion",
			Input:    json.RawMessage(askInput(`{"question":"First?","options":[]}`)),
		},
	})
	s.mu.Unlock()

	err := s.respondToAskUserAutoPicked(
		"ask_2",
		json.RawMessage(askInput(`{"question":"Second?","options":[{"label":"C (Recommended)","confidence":0.9},{"label":"D","confidence":0.1}]}`)),
		map[string]string{"Second?": "C (Recommended)"},
		map[string]float64{"Second?": 0.9},
	)
	if err != nil {
		t.Fatalf("respondToAskUserAutoPicked(): %v", err)
	}

	if got := s.Status(); got != SessionWaitingHelp {
		t.Fatalf("Status() = %s, want %s while ask_1 remains pending", got, SessionWaitingHelp)
	}
	pending := s.PendingControlRequests()
	if len(pending) != 1 || pending[0].RequestID != "ask_1" {
		t.Fatalf("PendingControlRequests() = %+v, want only ask_1", pending)
	}
}

func askInput(questions ...string) string {
	return `{"questions":[` + strings.Join(questions, ",") + `]}`
}
