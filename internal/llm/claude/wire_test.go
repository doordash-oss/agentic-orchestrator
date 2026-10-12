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

package claude

import (
	"encoding/json"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// --- Wire format tests ---
// These verify that the JSON sent to the CLI matches the protocol the Python/TypeScript
// Agent SDKs use (request_id nested inside response, not at top level).

func TestControlResponseWireFormat_Allow(t *testing.T) {
	resp := newAllowResponse("req_42")
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Parse back as generic map to verify wire format
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if wire["type"] != "control_response" {
		t.Errorf("type = %v, want control_response", wire["type"])
	}

	// request_id must NOT be at top level
	if _, ok := wire["request_id"]; ok {
		t.Error("request_id must not be at top level — it belongs inside response")
	}

	// request_id must be inside response
	response, ok := wire["response"].(map[string]any)
	if !ok {
		t.Fatal("response must be an object")
	}
	if response["request_id"] != "req_42" {
		t.Errorf("response.request_id = %v, want req_42", response["request_id"])
	}
	if response["subtype"] != "success" {
		t.Errorf("response.subtype = %v, want success", response["subtype"])
	}

	inner, ok := response["response"].(map[string]any)
	if !ok {
		t.Fatal("response.response must be an object")
	}
	if inner["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", inner["behavior"])
	}
}

func TestControlResponseWireFormat_Deny(t *testing.T) {
	resp := newDenyResponse("req_99", "too dangerous")
	data, _ := json.Marshal(resp)

	var wire map[string]any
	json.Unmarshal(data, &wire)

	response := wire["response"].(map[string]any)
	if response["request_id"] != "req_99" {
		t.Errorf("response.request_id = %v, want req_99", response["request_id"])
	}

	inner := response["response"].(map[string]any)
	if inner["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", inner["behavior"])
	}
	if inner["message"] != "too dangerous" {
		t.Errorf("message = %v, want 'too dangerous'", inner["message"])
	}
}

func TestControlResponseWireFormat_HookContinue(t *testing.T) {
	resp := newHookContinueResponse("hook_abc")
	data, _ := json.Marshal(resp)

	var wire map[string]any
	json.Unmarshal(data, &wire)

	response := wire["response"].(map[string]any)
	if response["request_id"] != "hook_abc" {
		t.Errorf("response.request_id = %v, want hook_abc", response["request_id"])
	}

	inner := response["response"].(map[string]any)
	if inner["continue"] != true {
		t.Errorf("response.response.continue = %v, want true", inner["continue"])
	}
}

func TestControlResponseWireFormat_AskUser(t *testing.T) {
	// Production callers pass the entire AskUserQuestion tool input —
	// {"questions":[...]} — straight from cr.Request.Input. The wire
	// format must unwrap the envelope so updatedInput.questions is the
	// inner array; otherwise the CLI throws "H.map is not a function"
	// on its response handler.
	cases := []struct {
		name      string
		questions json.RawMessage
	}{
		{
			name:      "production envelope",
			questions: json.RawMessage(`{"questions":[{"question":"Pick a color?","options":[{"label":"Red"},{"label":"Blue"}]}]}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle, err := askuser.Parse(tc.questions)
			if err != nil {
				t.Fatalf("parse ask-user input: %v (%s)", err, tc.questions)
			}
			resolved, err := bundle.Resolve(map[string]string{"1": "Red"})
			if err != nil {
				t.Fatalf("resolve ask-user answers: %v", err)
			}
			answers := map[string]string{"Pick a color?": "Red"}
			resp := newAskUserResponse("ask_1", resolved.Bundle, answers)
			data, _ := json.Marshal(resp)

			var wire map[string]any
			json.Unmarshal(data, &wire)

			response := wire["response"].(map[string]any)
			if response["request_id"] != "ask_1" {
				t.Errorf("response.request_id = %v, want ask_1", response["request_id"])
			}

			inner := response["response"].(map[string]any)
			if inner["behavior"] != "allow" {
				t.Errorf("behavior = %v, want allow", inner["behavior"])
			}

			updated, ok := inner["updatedInput"].(map[string]any)
			if !ok {
				t.Fatal("updatedInput must be an object")
			}
			questionsArr, ok := updated["questions"].([]any)
			if !ok {
				t.Fatalf("updatedInput.questions must be an array, got %T", updated["questions"])
			}
			if len(questionsArr) != 1 {
				t.Fatalf("updatedInput.questions length = %d, want 1", len(questionsArr))
			}
			q0, ok := questionsArr[0].(map[string]any)
			if !ok {
				t.Fatalf("questions[0] must be an object, got %T", questionsArr[0])
			}
			if q0["question"] != "Pick a color?" {
				t.Errorf("questions[0].question = %v, want Pick a color?", q0["question"])
			}
			answersMap, ok := updated["answers"].(map[string]any)
			if !ok {
				t.Fatal("updatedInput.answers must be an object")
			}
			if answersMap["Pick a color?"] != "Red" {
				t.Errorf("answer = %v, want Red", answersMap["Pick a color?"])
			}
		})
	}
}

func TestInitializeRequestWireFormat(t *testing.T) {
	msg := newInitializeRequest()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wire map[string]any
	json.Unmarshal(data, &wire)

	if wire["type"] != "control_request" {
		t.Errorf("type = %v, want control_request", wire["type"])
	}
	if wire["request_id"] == "" {
		t.Error("request_id must be set")
	}

	request, ok := wire["request"].(map[string]any)
	if !ok {
		t.Fatal("request must be an object")
	}
	if request["subtype"] != "initialize" {
		t.Errorf("subtype = %v, want initialize", request["subtype"])
	}

	hooks, ok := request["hooks"].(map[string]any)
	if !ok {
		t.Fatal("hooks must be an object")
	}
	preToolUse, ok := hooks["PreToolUse"].([]any)
	if !ok || len(preToolUse) == 0 {
		t.Fatal("PreToolUse must be a non-empty array")
	}
	first, ok := preToolUse[0].(map[string]any)
	if !ok {
		t.Fatal("first PreToolUse entry must be an object")
	}
	ids, ok := first["hookCallbackIds"].([]any)
	if !ok || len(ids) == 0 {
		t.Fatal("hookCallbackIds must be a non-empty array")
	}
}

func TestNewAllowResponse(t *testing.T) {
	resp := newAllowResponse("req_1")
	if resp.Type != "control_response" {
		t.Errorf("type = %q, want control_response", resp.Type)
	}
	if resp.Response.RequestID != "req_1" {
		t.Errorf("request_id = %q, want req_1", resp.Response.RequestID)
	}
	if resp.Response.Response["behavior"] != "allow" {
		t.Errorf("behavior = %v, want allow", resp.Response.Response["behavior"])
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}
}

func TestNewDenyResponse(t *testing.T) {
	resp := newDenyResponse("req_2", "dangerous command")
	if resp.Response.Response["behavior"] != "deny" {
		t.Errorf("behavior = %v, want deny", resp.Response.Response["behavior"])
	}
	if resp.Response.Response["message"] != "dangerous command" {
		t.Errorf("message = %v, want 'dangerous command'", resp.Response.Response["message"])
	}
}

func TestNewUserInput(t *testing.T) {
	input := newUserInput("Hello, Claude!")
	if input.Type != "user" {
		t.Errorf("type = %q, want user", input.Type)
	}
	if input.Message.Role != "user" {
		t.Errorf("role = %q, want user", input.Message.Role)
	}
	if input.Message.Content != "Hello, Claude!" {
		t.Errorf("content = %q, want 'Hello, Claude!'", input.Message.Content)
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !json.Valid(data) {
		t.Error("invalid JSON output")
	}
}
