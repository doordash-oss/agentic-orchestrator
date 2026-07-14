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

import "testing"

// TestReconcileNeedUserInputGate covers the gate-questionnaire reconciliation:
// a well-formed agent-authored record is preserved, while blank fields fall
// back to the validated progress.md handoff so the TUI never shows an empty
// gate.
func TestReconcileNeedUserInputGate(t *testing.T) {
	progress := &ParsedProgress{
		StateNote: "Plan contradicts the worktree; pick an ordering.",
		Questions: []string{
			"Should the pre-flight run before or after the fetch?",
			"Is aborting an already-current install acceptable?",
		},
	}

	tests := []struct {
		name        string
		agentRec    *NeedUserInputRecord
		progress    *ParsedProgress
		wantSummary string
		wantPrompts []string
	}{
		{
			name: "well-formed agent gate preserved verbatim",
			agentRec: &NeedUserInputRecord{
				Summary: "Concise gate summary distinct from the note.",
				Questions: []NeedUserInputQuestion{
					{Index: 1, Prompt: "Question one?"},
					{Index: 2, Prompt: "Question two?"},
				},
			},
			progress:    progress,
			wantSummary: "Concise gate summary distinct from the note.",
			wantPrompts: []string{"Question one?", "Question two?"},
		},
		{
			name: "blank stub falls back to progress.md",
			agentRec: &NeedUserInputRecord{
				Summary:   "",
				Questions: []NeedUserInputQuestion{{Index: 0, Prompt: ""}},
			},
			progress:    progress,
			wantSummary: "Plan contradicts the worktree; pick an ordering.",
			wantPrompts: []string{
				"Should the pre-flight run before or after the fetch?",
				"Is aborting an already-current install acceptable?",
			},
		},
		{
			name: "summary present but questions blank backfills questions only",
			agentRec: &NeedUserInputRecord{
				Summary:   "Agent summary stays.",
				Questions: nil,
			},
			progress:    progress,
			wantSummary: "Agent summary stays.",
			wantPrompts: []string{
				"Should the pre-flight run before or after the fetch?",
				"Is aborting an already-current install acceptable?",
			},
		},
		{
			name:        "nil agent record builds entirely from progress.md",
			agentRec:    nil,
			progress:    progress,
			wantSummary: "Plan contradicts the worktree; pick an ordering.",
			wantPrompts: []string{
				"Should the pre-flight run before or after the fetch?",
				"Is aborting an already-current install acceptable?",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := reconcileNeedUserInputGate(tt.agentRec, tt.progress, 3)
			if rec.Iteration != 3 {
				t.Errorf("Iteration = %d, want 3", rec.Iteration)
			}
			if rec.Summary != tt.wantSummary {
				t.Errorf("Summary = %q, want %q", rec.Summary, tt.wantSummary)
			}
			if len(rec.Questions) != len(tt.wantPrompts) {
				t.Fatalf("question count = %d, want %d", len(rec.Questions), len(tt.wantPrompts))
			}
			for i, want := range tt.wantPrompts {
				if rec.Questions[i].Prompt != want {
					t.Errorf("Q%d prompt = %q, want %q", i+1, rec.Questions[i].Prompt, want)
				}
				if rec.Questions[i].Index != i+1 {
					t.Errorf("Q%d index = %d, want %d", i+1, rec.Questions[i].Index, i+1)
				}
			}
		})
	}
}

func TestRetryNeedsUserInput(t *testing.T) {
	failed := VerificationCheckResult{
		Name:   "Integration tests pass",
		Status: VerificationStatusFailed,
	}
	blocked := VerificationCheckResult{
		ItemID: "plan_docker",
		Name:   "Container tests pass",
		Status: VerificationStatusBlocked,
	}
	passed := VerificationCheckResult{
		Name:   "Build succeeds",
		Status: VerificationStatusPassed,
	}
	notRun := VerificationCheckResult{
		Name:   "Tests remain to run",
		Status: VerificationStatusNotRun,
	}

	tests := []struct {
		name     string
		progress *ParsedProgress
		report   *VerificationReport
		want     bool
	}{
		{
			name: "complete retry with failed legacy check escalates",
			progress: &ParsedProgress{
				State: StateRetry,
				HandoffSections: map[string]string{
					"Where I stopped": "Complete for implementation; Docker is unavailable.",
				},
			},
			report: &VerificationReport{RequiredChecks: []VerificationCheckResult{failed}},
			want:   true,
		},
		{
			name: "retry with only blocked contract results escalates",
			progress: &ParsedProgress{
				State: StateRetry,
				HandoffSections: map[string]string{
					"Where I stopped": "Waiting for external infrastructure.",
				},
			},
			report: &VerificationReport{Results: []VerificationCheckResult{passed, blocked}},
			want:   true,
		},
		{
			name: "actionable retry stays in loop",
			progress: &ParsedProgress{
				State: StateRetry,
				HandoffSections: map[string]string{
					"Where I stopped": "Fix the failing parser test next.",
				},
			},
			report: &VerificationReport{RequiredChecks: []VerificationCheckResult{failed}},
		},
		{
			name: "complete retry with only not-run work stays in loop",
			progress: &ParsedProgress{
				State: StateRetry,
				HandoffSections: map[string]string{
					"Where I stopped": "Complete with the first task; continue with the second.",
				},
			},
			report: &VerificationReport{RequiredChecks: []VerificationCheckResult{notRun}},
		},
		{
			name: "success state never escalates",
			progress: &ParsedProgress{
				State: StateSuccess,
				HandoffSections: map[string]string{
					"Where I stopped": "Complete",
				},
			},
			report: &VerificationReport{RequiredChecks: []VerificationCheckResult{failed}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryNeedsUserInput(tt.progress, tt.report); got != tt.want {
				t.Errorf("retryNeedsUserInput() = %v, want %v", got, tt.want)
			}
		})
	}
}
