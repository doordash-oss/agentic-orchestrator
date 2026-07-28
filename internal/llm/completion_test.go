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

import "testing"

func TestParseCompletionIntent_RequiresExactlyOneStructuredOutcome(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantStatus CompletionIntentStatus
		wantValid  bool
		wantFound  bool
	}{
		{
			name:       "success",
			text:       "Work is complete.\n<agentico-outcome>{\"status\":\"success\"}</agentico-outcome>",
			wantStatus: CompletionIntentSuccess,
			wantValid:  true,
			wantFound:  true,
		},
		{
			name:       "retry",
			text:       "<agentico-outcome>{\"status\":\"retry\"}</agentico-outcome>",
			wantStatus: CompletionIntentRetry,
			wantValid:  true,
			wantFound:  true,
		},
		{
			name: "ordinary text has no intent",
			text: "I finished the work.",
		},
		{
			name:      "unknown status is invalid",
			text:      "<agentico-outcome>{\"status\":\"need_user_input\"}</agentico-outcome>",
			wantFound: true,
		},
		{
			name:      "malformed json is invalid",
			text:      "<agentico-outcome>{status:success}</agentico-outcome>",
			wantFound: true,
		},
		{
			name:      "trailing json value is invalid",
			text:      "<agentico-outcome>{\"status\":\"success\"} {\"status\":\"retry\"}</agentico-outcome>",
			wantFound: true,
		},
		{
			name: "multiple outcomes are invalid",
			text: "<agentico-outcome>{\"status\":\"success\"}</agentico-outcome>\n" +
				"<agentico-outcome>{\"status\":\"retry\"}</agentico-outcome>",
			wantFound: true,
		},
		{
			name:      "outcome must end the root response",
			text:      "<agentico-outcome>{\"status\":\"success\"}</agentico-outcome>\nMore work follows.",
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCompletionIntent(tt.text)
			if got.Status != tt.wantStatus || got.Valid() != tt.wantValid || got.Found != tt.wantFound {
				t.Fatalf("ParseCompletionIntent() = %+v, want status=%q valid=%v found=%v", got, tt.wantStatus, tt.wantValid, tt.wantFound)
			}
			if tt.wantFound && !tt.wantValid && got.Error == "" {
				t.Fatal("invalid found outcome has empty Error")
			}
		})
	}
}

func TestClassifyTurn_SeparatesRootIntentQuestionsTasksAndViolations(t *testing.T) {
	success := &ResultMessage{Subtype: "success", StopReason: "end_turn"}
	validSuccess := CompletionIntent{Found: true, Status: CompletionIntentSuccess}
	validRetry := CompletionIntent{Found: true, Status: CompletionIntentRetry}

	tests := []struct {
		name string
		in   TurnSignals
		want TurnDisposition
	}{
		{
			name: "root success intent is committable",
			in:   TurnSignals{Result: success, RootIntent: validSuccess},
			want: TurnCommitSuccess,
		},
		{
			name: "root retry intent is committable",
			in:   TurnSignals{Result: success, RootIntent: validRetry},
			want: TurnCommitRetry,
		},
		{
			name: "root question wins over completion intent",
			in: TurnSignals{
				Result:              success,
				RootIntent:          validSuccess,
				RootQuestionPending: true,
			},
			want: TurnAwaitingUser,
		},
		{
			name: "live task wins over completion intent",
			in: TurnSignals{
				Result:       success,
				RootIntent:   validSuccess,
				TasksRunning: true,
			},
			want: TurnAwaitingTasks,
		},
		{
			name: "provider truncation resumes instead of committing",
			in: TurnSignals{
				Result:     &ResultMessage{Subtype: "success", StopReason: "tool_use"},
				RootIntent: validSuccess,
			},
			want: TurnTruncated,
		},
		{
			name: "clean end without structured intent is a violation",
			in:   TurnSignals{Result: success},
			want: TurnProtocolViolation,
		},
		{
			name: "malformed intent is a violation",
			in: TurnSignals{
				Result:     success,
				RootIntent: CompletionIntent{Found: true, Error: "bad payload"},
			},
			want: TurnProtocolViolation,
		},
		{
			name: "provider error wins over all other signals",
			in: TurnSignals{
				Result:              &ResultMessage{Subtype: "error", IsError: true},
				RootIntent:          validSuccess,
				RootQuestionPending: true,
				TasksRunning:        true,
			},
			want: TurnErrored,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyTurn(tt.in); got != tt.want {
				t.Fatalf("ClassifyTurn() = %s, want %s", got, tt.want)
			}
		})
	}
}
