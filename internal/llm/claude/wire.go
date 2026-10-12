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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// controlResponse is the wire format for responding to control_request messages.
type controlResponse struct {
	Type     string               `json:"type"`
	Response controlResponseInner `json:"response"`
}

// controlResponseInner is the inner payload of a control response.
type controlResponseInner struct {
	Subtype   string         `json:"subtype"`
	RequestID string         `json:"request_id"`
	Response  map[string]any `json:"response"`
}

// newAllowResponse creates a control_response that allows a tool use.
func newAllowResponse(requestID string, originalInput ...json.RawMessage) controlResponse {
	resp := map[string]any{"behavior": "allow"}
	if len(originalInput) > 0 && len(originalInput[0]) > 0 {
		var inputVal any
		if err := json.Unmarshal(originalInput[0], &inputVal); err == nil {
			resp["updatedInput"] = inputVal
		}
	}
	return controlResponse{
		Type: "control_response",
		Response: controlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  resp,
		},
	}
}

// newDenyResponse creates a control_response that denies a tool use.
func newDenyResponse(requestID string, reason string) controlResponse {
	return controlResponse{
		Type: "control_response",
		Response: controlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  map[string]any{"behavior": "deny", "message": reason},
		},
	}
}

// newAskUserResponse creates a control_response that allows an AskUserQuestion
// tool use and supplies the user's answers keyed by question text. The bundle
// is sent as the SDK's updatedInput.questions array.
func newAskUserResponse(requestID string, bundle askuser.Bundle, answers map[string]string) controlResponse {
	questions := bundle.Questions
	if questions == nil {
		questions = []askuser.Question{}
	}
	return controlResponse{
		Type: "control_response",
		Response: controlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response: map[string]any{
				"behavior": "allow",
				"updatedInput": map[string]any{
					"questions": questions,
					"answers":   answers,
				},
			},
		},
	}
}

// initializeRequestMessage is the handshake sent to the CLI to activate the
// control_request protocol.
type initializeRequestMessage struct {
	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	Request   initializeRequestBody `json:"request"`
}

// initializeRequestBody is the inner payload of the initialize handshake.
type initializeRequestBody struct {
	Subtype string                         `json:"subtype"`
	Hooks   map[string][]initializeHookRef `json:"hooks"`
}

// initializeHookRef references hook callbacks by ID.
type initializeHookRef struct {
	HookCallbackIDs []string `json:"hookCallbackIds"`
}

// newInitializeRequest creates the SDK initialize handshake message.
func newInitializeRequest() initializeRequestMessage {
	return initializeRequestMessage{
		Type:      "control_request",
		RequestID: "agentic-init-1",
		Request: initializeRequestBody{
			Subtype: "initialize",
			Hooks: map[string][]initializeHookRef{
				"PreToolUse": {
					{HookCallbackIDs: []string{"agentic-hook-1"}},
				},
			},
		},
	}
}

// newHookContinueResponse creates a control_response that tells the CLI
// to continue with the tool execution (used for PreToolUse hook callbacks).
func newHookContinueResponse(requestID string) controlResponse {
	return controlResponse{
		Type: "control_response",
		Response: controlResponseInner{
			Subtype:   "success",
			RequestID: requestID,
			Response:  map[string]any{"continue": true},
		},
	}
}

// userInputMessage is sent to stdin to deliver a user message.
type userInputMessage struct {
	Type    string           `json:"type"`
	Message userInputContent `json:"message"`
}

// userInputContent is the inner content of a user input message.
type userInputContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// newUserInput creates a userInputMessage for sending text to the session.
func newUserInput(text string) userInputMessage {
	return userInputMessage{
		Type: "user",
		Message: userInputContent{
			Role:    "user",
			Content: text,
		},
	}
}
