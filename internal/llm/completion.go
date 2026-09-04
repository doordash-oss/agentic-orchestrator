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

package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	completionIntentOpen  = "<agentico-outcome>"
	completionIntentClose = "</agentico-outcome>"
)

var completionIntentPattern = regexp.MustCompile(`(?s)<agentico-outcome>\s*(.*?)\s*</agentico-outcome>`)

// CompletionIntentStatus is the root agent's semantic decision for the
// current iteration. Asking the user is intentionally not represented here:
// it is a first-class control request, not a terminal outcome.
type CompletionIntentStatus string

const (
	CompletionIntentSuccess CompletionIntentStatus = "success"
	CompletionIntentRetry   CompletionIntentStatus = "retry"
)

// CompletionIntent is the root agent's requested outcome. Providers carry it
// through either a structured tool request or final root-assistant text.
type CompletionIntent struct {
	Found   bool                   `json:"-"`
	Status  CompletionIntentStatus `json:"status,omitempty"`
	Summary string                 `json:"summary,omitempty"`
	Error   string                 `json:"-"`
}

// PhaseCompletionRequest asks the harness to validate and commit a phase.
// Receiving a request alone never authorizes completion.
type PhaseCompletionRequest struct {
	RequestID string
	Intent    CompletionIntent
}

// StructuredCompletionProtocol exposes provider-native completion requests.
// The harness answers each request after checking the phase contract.
type StructuredCompletionProtocol interface {
	UsesStructuredCompletion() bool
	RespondToCompletion(requestID string, accepted bool, message string) error
}

// Harness-owned structured tool names shared by providers that expose them
// and by the prompts and nudges that reference them.
const (
	AskUserToolName       = "ask_user"
	CompletePhaseToolName = "complete_phase"
)

// CompletionToolProvider is implemented by providers whose sessions commit
// phases through a structured tool call instead of prose outcome tags.
type CompletionToolProvider interface {
	CompletionToolName() string
}

// TurnDisposition is the harness's provider-neutral interpretation of one
// root turn ending.
type TurnDisposition int

const (
	TurnUnknown TurnDisposition = iota
	TurnCommitSuccess
	TurnCommitRetry
	TurnAwaitingUser
	TurnAwaitingTasks
	TurnTruncated
	TurnRefused
	TurnErrored
	TurnProtocolViolation
)

func (d TurnDisposition) String() string {
	switch d {
	case TurnCommitSuccess:
		return "CommitSuccess"
	case TurnCommitRetry:
		return "CommitRetry"
	case TurnAwaitingUser:
		return "AwaitingUser"
	case TurnAwaitingTasks:
		return "AwaitingTasks"
	case TurnTruncated:
		return "Truncated"
	case TurnRefused:
		return "Refused"
	case TurnErrored:
		return "Errored"
	case TurnProtocolViolation:
		return "ProtocolViolation"
	default:
		return "Unknown"
	}
}

// TurnSignals are the independent facts needed to classify a provider turn.
// Filesystem markers are deliberately absent: phase_complete is a receipt
// written after TurnCommitSuccess/TurnCommitRetry, never an input.
type TurnSignals struct {
	Result              *ResultMessage
	RootIntent          CompletionIntent
	RootQuestionPending bool
	TasksRunning        bool
}

// ClassifyTurn separates provider termination, root-agent intent, user input,
// and delegated-task liveness. The order prevents conflicting or child-owned
// output from committing a phase.
func ClassifyTurn(in TurnSignals) TurnDisposition {
	if in.Result == nil {
		return TurnUnknown
	}
	if in.Result.IsError || in.Result.Subtype == "error" {
		return TurnErrored
	}
	if in.RootQuestionPending {
		return TurnAwaitingUser
	}
	if in.TasksRunning {
		return TurnAwaitingTasks
	}
	switch in.Result.StopReason {
	case "refusal":
		return TurnRefused
	case "tool_use", "max_tokens", "pause_turn":
		return TurnTruncated
	}
	if in.RootIntent.Valid() {
		if in.RootIntent.Status == CompletionIntentRetry {
			return TurnCommitRetry
		}
		return TurnCommitSuccess
	}
	return TurnProtocolViolation
}

// Valid reports whether the provider supplied a recognized outcome without
// a parsing or protocol error.
func (i CompletionIntent) Valid() bool {
	return i.Found && i.Error == "" &&
		(i.Status == CompletionIntentSuccess || i.Status == CompletionIntentRetry)
}

// ParseCompletionIntent extracts the one allowed structured outcome from a
// root assistant message. Free-form prose may surround the tag, but the JSON
// payload is strict and duplicate outcomes are rejected.
func ParseCompletionIntent(text string) CompletionIntent {
	matches := completionIntentPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		if strings.Contains(text, completionIntentOpen) || strings.Contains(text, completionIntentClose) {
			return CompletionIntent{Found: true, Error: "malformed agentico-outcome tag"}
		}
		return CompletionIntent{}
	}
	if len(matches) != 1 {
		return CompletionIntent{Found: true, Error: "expected exactly one agentico-outcome"}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), completionIntentClose) {
		return CompletionIntent{Found: true, Error: "agentico-outcome must be the final content in the root response"}
	}

	var payload struct {
		Status  CompletionIntentStatus `json:"status"`
		Summary string                 `json:"summary,omitempty"`
	}
	decoder := json.NewDecoder(strings.NewReader(matches[0][1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return CompletionIntent{Found: true, Error: fmt.Sprintf("invalid agentico-outcome JSON: %v", err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return CompletionIntent{Found: true, Error: "invalid agentico-outcome JSON: multiple JSON values"}
		}
		return CompletionIntent{Found: true, Error: fmt.Sprintf("invalid agentico-outcome JSON: %v", err)}
	}
	switch payload.Status {
	case CompletionIntentSuccess, CompletionIntentRetry:
		return CompletionIntent{
			Found:   true,
			Status:  payload.Status,
			Summary: strings.TrimSpace(payload.Summary),
		}
	default:
		return CompletionIntent{
			Found: true,
			Error: fmt.Sprintf("unsupported agentico-outcome status %q", payload.Status),
		}
	}
}
