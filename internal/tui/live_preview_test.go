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
	"image/color"
	"strings"
	"testing"
	"time"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// testLPToolUseIDBash and testLPToolUseIDRead are fixture tool_use IDs paired
// with a specific tool name, reused across this file's transcript-summary
// test table.
const (
	testLPToolUseIDBash = "toolu_1"
	testLPToolUseIDRead = "toolu_2"
)

// testToolProgressDataDownload30 and testHookCallbackSubtype are shared
// ToolProgressMessage.Data / ControlRequest.Subtype fixture values used
// across this file's noise-filtering assertions.
const (
	testToolProgressDataDownload30 = "download 30%"
	testHookCallbackSubtype        = "hook_callback"
)

// testToolNameWebFetch is a fixture tool name reused across this file's
// truncation tests.
const testToolNameWebFetch = "WebFetch"

func TestLivePreviewEligible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    *feature.Feature
		want bool
	}{
		{
			name: "running phase",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			want: true,
		},
		{
			name: "created startup",
			f:    &feature.Feature{Status: feature.StatusCreated, CurrentPhase: feature.PhaseImplement},
			want: true,
		},
		{
			name: "active post publish cycle",
			f: &feature.Feature{
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning},
				},
			},
			want: true,
		},
		{
			name: "pending permission",
			f: &feature.Feature{
				Status: feature.StatusFailed,
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: toolNameBash, Pending: true},
				},
			},
			want: true,
		},
		{
			name: "interrupted stale ask user",
			f: &feature.Feature{
				Status:    feature.StatusInterrupted,
				HelpQueue: []feature.HelpRequest{{Question: testQuestionNeedInput, Pending: true}},
			},
			want: false,
		},
		{
			name: "needs review",
			f:    &feature.Feature{Status: feature.StatusPlanNeedsReview},
			want: false,
		},
		{
			name: "need user input",
			f:    &feature.Feature{Status: feature.StatusNeedUserInput, PendingNeedUserInputPath: "/tmp/need-user-input.yaml"},
			want: true,
		},
		{
			name: "done",
			f:    &feature.Feature{Status: feature.StatusDone},
			want: false,
		},
		{
			name: "failed",
			f:    &feature.Feature{Status: feature.StatusFailed},
			want: false,
		},
		{
			name: "interrupted",
			f:    &feature.Feature{Status: feature.StatusInterrupted},
			want: false,
		},
		{
			name: "published without active cycle",
			f:    &feature.Feature{Status: feature.StatusPublished},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLivePreviewEligible(tt.f); got != tt.want {
				t.Errorf("isLivePreviewEligible(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestContextualAActionHint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		f        *feature.Feature
		wantHint string
		wantLead bool
	}{
		{
			name:     "ordinary live state watches",
			f:        &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			wantHint: "[a] Watch",
		},
		{
			name:     "review behavior preserved",
			f:        &feature.Feature{Status: feature.StatusPlanNeedsReview},
			wantHint: "[a] Review",
			wantLead: true,
		},
		{
			name:     "answer behavior preserved",
			f:        &feature.Feature{Status: feature.StatusNeedUserInput},
			wantHint: "[a] Answer",
			wantLead: true,
		},
		{
			name: "waiting permission advertises approval",
			f: &feature.Feature{
				Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: toolNameBash, Pending: true},
				},
			},
			wantHint: "[a] Approve",
			wantLead: true,
		},
		{
			name:     "static published has no a action",
			f:        &feature.Feature{Status: feature.StatusPublished},
			wantHint: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHint, gotLead := contextualAActionHintFor(tt.f, nil)
			if gotHint != tt.wantHint || gotLead != tt.wantLead {
				t.Errorf("contextualAActionHintFor(%s) = (%q, %v), want (%q, %v)", tt.name, gotHint, gotLead, tt.wantHint, tt.wantLead)
			}
		})
	}
}

func TestLivePreviewRendersAttentionBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		feature *feature.Feature
		sess    session.SessionView
		width   int
		height  int
		want    []string
		notWant []string
	}{
		{
			name: "permission",
			feature: &feature.Feature{
				Status: feature.StatusImplementing,
				PermissionsQueue: []feature.PermissionRequest{
					{Tool: toolNameBash, Args: `{"command":"go test ./internal/tui"}`, Pending: true}, //nolint:goconst // shared raw-JSON test fixture; not constant-ized per raw-string-fixture policy
				},
			},
			width:  96,
			height: 20,
			want:   []string{attentionTypeLabelPermission, "Bash: go test ./internal/tui", "[a] Approve", "Waiting for approval"},
		},
		{
			name: "ask user",
			feature: &feature.Feature{
				Status:    feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Which API should we keep?", Pending: true}},
			},
			width:  96,
			height: 20,
			want:   []string{"Question", "Which API should we keep?", "press ", "[a]", " to Answer", "Waiting for an answer"},
		},
		{
			name:    "review",
			feature: &feature.Feature{Status: feature.StatusPlanNeedsReview, CurrentRoadmapPhase: 1, TotalRoadmapPhases: 3},
			width:   96,
			height:  20,
			want:    []string{"Review Required", "Phase 1 plan needs review", "[a] Review"},
		},
		{
			name: "need user input",
			feature: &feature.Feature{
				Status:                   feature.StatusNeedUserInput,
				PendingNeedUserInputPath: "/tmp/need-user-input.yaml",
			},
			width:  96,
			height: 20,
			want:   []string{"Input Required", "Feature-level input gate", "press ", "[a]", " to Answer"},
		},
		{
			name: "narrow keeps attention and drops transcript",
			feature: &feature.Feature{
				Status:    feature.StatusImplementing,
				HelpQueue: []feature.HelpRequest{{Question: "Narrow question?", Pending: true}},
			},
			sess: newLivePreviewSession("narrow", feature.PhaseImplement,
				assistantMessage(llm.ContentBlock{Type: "text", Text: "roomy transcript context"})),
			width:   50,
			height:  10,
			want:    []string{"Question", "Narrow question?", "press ", "[a]", " to Answer"},
			notWant: []string{"roomy transcript context"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newLivePreviewModel(tt.feature).withSession(tt.sess).withHeight(tt.height)
			view := m.ViewCompact(tt.width)
			for _, want := range tt.want {
				if !strings.Contains(view, want) {
					t.Errorf("LivePreviewModel.ViewCompact(%s) missing %q in:\n%s", tt.name, want, view)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(view, notWant) {
					t.Errorf("LivePreviewModel.ViewCompact(%s) contains %q unexpectedly in:\n%s", tt.name, notWant, view)
				}
			}
		})
	}
}

func TestLivePreviewActivityLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    *feature.Feature
		sess session.SessionView
		want string
	}{
		{
			name: "nil session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			want: "Working on Implement...",
		},
		{
			name: "empty session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("empty", feature.PhaseImplement),
			want: "Working on Implement...",
		},
		{
			name: "idle running session",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("idle", feature.PhaseImplement,
				llm.SDKMessage{Type: "system", Init: &llm.SystemInitMessage{SessionID: "idle"}}),
			want: "Working on Implement...",
		},
		{
			name: "assistant thinking",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("thinking", feature.PhaseImplement,
				assistantMessage(llm.ContentBlock{Type: "thinking", Thinking: "checking"})),
			want: thinkingLineText,
		},
		{
			name: "active tool use",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("tool", feature.PhaseImplement,
				assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, Name: toolNameBash})),
			want: testUsingBashActivity,
		},
		{
			name: "tool progress",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("tool-progress", feature.PhaseImplement,
				llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: "Edit"}}),
			want: "Using Edit...",
		},
		{
			name: "completed turn fallback",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("result", feature.PhaseImplement,
				llm.SDKMessage{Type: "result", Result: &llm.ResultMessage{Subtype: "success"}}),
			want: "Working on Implement...",
		},
		{
			name: "created startup",
			f:    &feature.Feature{Status: feature.StatusCreated, CurrentPhase: feature.PhasePlan},
			want: "Starting Plan...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := livePreviewActivityLine(tt.f, tt.sess); got != tt.want {
				t.Errorf("livePreviewActivityLine(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestLivePreviewTranscriptSummaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		messages []llm.SDKMessage
		want     []string
		notWant  []string
	}{
		{
			name: "assistant text",
			messages: []llm.SDKMessage{
				assistantMessage(llm.ContentBlock{Type: "text", Text: "Finished repo scan\nready for implementation."}),
			},
			want: []string{"Finished repo scan", "ready for implementation."},
		},
		{
			name: "tool use with arguments",
			messages: []llm.SDKMessage{
				assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: testLPToolUseIDBash, Name: toolNameBash, Input: rawJSON(`{"command":"go test ./internal/tui -run LivePreview"}`)}),
			},
			want: []string{"Bash: go test ./internal/tui -run LivePreview"},
		},
		{
			name: "agent tool use summarizes description",
			messages: []llm.SDKMessage{
				assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: "toolu_agent", Name: toolNameAgent, Input: rawJSON(`{"description":"Explore KB completion handler","prompt":"This long delegated prompt should not render in the dashboard preview tail."}`)}),
			},
			want:    []string{testAgentToolLabel},
			notWant: []string{"long delegated prompt", `"prompt"`},
		},
		{
			name: "successful tool result",
			messages: []llm.SDKMessage{
				assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: testLPToolUseIDRead, Name: toolNameRead, Input: rawJSON(`{"file_path":"internal/tui/live_preview.go"}`)}),
				userMessage(llm.ContentBlock{Type: blockTypeToolResult, ToolUseID: testLPToolUseIDRead, Content: rawJSON(`"file loaded"`)}),
			},
			want: []string{"Read: internal/tui/live_preview.go", "Read result: file loaded"},
		},
		{
			name: "failed tool result",
			messages: []llm.SDKMessage{
				userMessage(llm.ContentBlock{Type: blockTypeToolResult, ToolUseID: "toolu_3", Content: rawJSON(`"permission denied"`), IsError: true}),
			},
			want: []string{"Tool failed: permission denied"},
		},
		{
			name: "permission request",
			messages: []llm.SDKMessage{
				controlRequest("perm_1", toolNameBash, rawJSON(`{"command":"rm -rf /tmp/nope"}`)),
			},
			want: []string{"Permission: Bash: rm -rf /tmp/nope"},
		},
		{
			name: "AskUser control request",
			messages: []llm.SDKMessage{
				controlRequest("ask_1", toolNameAskUserQuestion, rawJSON(`{"questions":[{"question":"Which implementation path?"}]}`)),
			},
			want: []string{"AskUser: Which implementation path?"},
		},
		{
			name: "task lifecycle",
			messages: []llm.SDKMessage{
				{Type: "system", Subtype: taskSubtypeTaskStarted, TaskStarted: &llm.TaskStartedMessage{Description: "reading files", TaskType: taskTypeLocalAgent}},
				{Type: "system", Subtype: taskSubtypeTaskProgress, TaskProgress: &llm.TaskProgressMessage{Description: "reading files", LastToolName: toolNameRead}},
				{Type: "system", Subtype: taskSubtypeTaskNotification, TaskNotification: &llm.TaskNotificationMessage{Status: taskNotificationStatusCompleted, Summary: "wrote summary"}},
			},
			want: []string{"Task started: reading files", "Task progress: reading files via Read", "Task completed: wrote summary"},
		},
		{
			name: "turn result",
			messages: []llm.SDKMessage{
				{Type: "result", Result: &llm.ResultMessage{Subtype: "success", TotalCostUSD: 0.12, Result: "phase_complete created"}},
				{Type: "result", Result: &llm.ResultMessage{Subtype: "error", Result: "context too large"}},
			},
			want: []string{"Turn complete: phase_complete created", "Turn failed: context too large"},
		},
		{
			name: "compaction",
			messages: []llm.SDKMessage{
				{Type: "system", Subtype: "compact_boundary", Compact: &llm.CompactBoundaryMessage{}},
			},
			want: []string{"Context compacted"},
		},
		{
			name: "noisy messages",
			messages: []llm.SDKMessage{
				{Type: "stream_event", StreamDeltaType: "text"},
				{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"}},
				{Type: "status", Status: &llm.StatusMessage{Message: "routine status"}},
				{Type: "hook_progress", HookProgress: &llm.HookProgressMessage{HookName: "PreToolUse", Data: "checking"}},
				{Type: "hook_response", HookResponse: &llm.HookResponseMessage{HookName: "PreToolUse", Result: "ok"}},
				{Type: "rate_limit", RateLimit: &llm.RateLimitMessage{Message: "slow down"}},
				{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: toolNameBash, Data: testToolProgressDataDownload30}},
				{Type: msgTypeControlRequest, ControlRequest: &llm.ControlRequestMessage{RequestID: "hook_1", Request: llm.ControlRequest{Subtype: testHookCallbackSubtype, ToolName: toolNameBash}}},
				assistantMessage(llm.ContentBlock{Type: "text", Text: "visible after noise"}),
			},
			want:    []string{"visible after noise"},
			notWant: []string{"routine status", "PreToolUse", "slow down", testToolProgressDataDownload30, testHookCallbackSubtype, "session=s1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newLivePreviewSession("tail-"+tt.name, feature.PhaseImplement, tt.messages...)
			got := stripANSI(newLivePreviewModel(&feature.Feature{
				Status:       feature.StatusImplementing,
				CurrentPhase: feature.PhaseImplement,
			}).withSession(sess).ViewCompact(100))
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("LivePreviewModel.ViewCompact(%s) missing %q in:\n%s", tt.name, want, got)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(got, notWant) {
					t.Errorf("LivePreviewModel.ViewCompact(%s) contained noisy %q in:\n%s", tt.name, notWant, got)
				}
			}
		})
	}
}

func TestLivePreviewToolProgressRendersCompactToolRows(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	longResult := strings.Repeat("tool-output-", 20)
	sess := streamingLivePreviewSession("streaming-tools", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: "text", Text: "checking files"}),
		llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: toolNameBash, Data: "PASS\nok ./..."}},
		llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: toolNameBash, Data: longResult}},
		llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: toolNameWrite, Data: "A README.scn.md"}},
	)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))

	for _, want := range []string{"$ Bash", "Bash result: PASS ok ./...", "Bash result: tool-output-", "[...]", "$ Write", "Write result: A README.scn.md"} {
		if !strings.Contains(view, want) {
			t.Fatalf("live preview missing %q in:\n%s", want, view)
		}
	}
	if strings.Count(view, "$ Bash") != 1 {
		t.Fatalf("live preview should render one Bash tool-use row, got:\n%s", view)
	}
}

func TestLivePreviewTranscriptEmphasis(t *testing.T) {
	t.Parallel()

	assertForeground(t, livePreviewTranscriptStyle(livePreviewTranscriptAssistant), colorText)
	assertFaintForeground(t, livePreviewTranscriptStyle(livePreviewTranscriptTool), colorWarning)
	assertFaintForeground(t, livePreviewTranscriptStyle(livePreviewTranscriptResult), colorSuccess)

	if livePreviewTranscriptStyle(livePreviewTranscriptAssistant).GetFaint() {
		t.Fatal("assistant transcript rows should remain high contrast")
	}
}

func assertForeground(t *testing.T, style lipgloss.Style, want color.Color) {
	t.Helper()
	if !sameColor(style.GetForeground(), want) {
		t.Fatalf("foreground = %v, want %v", style.GetForeground(), want)
	}
}

func assertFaintForeground(t *testing.T, style lipgloss.Style, want color.Color) {
	t.Helper()
	assertForeground(t, style, want)
	if !style.GetFaint() {
		t.Fatalf("foreground = %v, want faint style", style.GetForeground())
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestLivePreviewTailBannerLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    *feature.Feature
		sess session.SessionView
		want string
	}{
		{
			name: "active session phase",
			f:    &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess: newLivePreviewSession("reviewing", feature.PhaseReview),
			want: "Current: Review",
		},
		{
			name: "feature phase fallback",
			f:    &feature.Feature{Status: feature.StatusPlanning, CurrentPhase: feature.PhasePlan},
			want: "Current: Plan",
		},
		{
			name: "roadmap implementation context",
			f: &feature.Feature{
				Status:              feature.StatusImplementing,
				CurrentPhase:        feature.PhaseImplement,
				CurrentIteration:    4,
				CurrentRoadmapPhase: 2,
				TotalRoadmapPhases:  3,
			},
			sess: newLivePreviewSessionWithIteration("implementing", feature.PhaseImplement, 5),
			want: "Current: Implement · Phase 2/3 · Iteration 5",
		},
		{
			name: "post publish cycle context",
			f: &feature.Feature{
				Status:           feature.StatusPublished,
				CurrentPhase:     feature.PhaseImplement,
				CurrentIteration: 2,
				Repos:            []feature.FeatureRepo{{Name: "api"}},
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning},
				},
			},
			sess: newLivePreviewSession("cycle", feature.PhaseImplement),
			want: "Current: Rebasing [2]",
		},
		{
			name: "feature rebase cycle context",
			f: &feature.Feature{
				Status:       feature.StatusCodeReady,
				CurrentPhase: feature.PhasePublish,
				ActiveCycle: &feature.CycleState{
					Type:      feature.CycleRebase,
					Status:    feature.RepoCycleRunning,
					Iteration: 3,
				},
			},
			want: "Current: Rebasing [3]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSI(livePreviewTailBannerLabel(tt.f, tt.sess)); got != tt.want {
				t.Errorf("livePreviewTailBannerLabel(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestLivePreviewValidationContextShowsAggregateAndSelectedValidator(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status:              feature.StatusPlanning,
		CurrentPhase:        feature.PhasePlan,
		ValidatingPlan:      true,
		CurrentRoadmapPhase: 1,
		ValidatorStatuses: map[string]string{
			"Architecture": validatorStatusApproved,         //nolint:goconst // validator category label; matches production's inline literal, not a reusable test-owned constant
			"Scope":        validatorStatusChangesRequested, //nolint:goconst // validator category label; matches production's inline literal, not a reusable test-owned constant
			"Testing":      "running",
		},
	}
	sess := validatorLivePreviewSession("scope-validator", "Scope",
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: "toolu_read", Name: toolNameBash, Input: rawJSON(`{"command":"go test ./..."}`)}),
	)

	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))
	for _, want := range []string{
		"Status", "Validating Phase 1 plan",
		"Validators", "Arch ✓", "Test ⟳", "Scope ✗",
		"Current: Validating Phase 1 plan", "1 ✓", "1 ✗", "1 running", "Showing Scope", testUsingBashActivity,
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("validation live preview missing %q in:\n%s", want, view)
		}
	}
}

func TestLivePreviewValidatorStatusStyles(t *testing.T) {
	t.Parallel()

	assertForeground(t, livePreviewValidatorStatusStyle(validatorStatusApproved), colorSuccess)
	assertForeground(t, livePreviewValidatorStatusStyle(validatorStatusChangesRequested), colorError)
	assertForeground(t, livePreviewValidatorStatusStyle("FAILED"), colorError)
	assertForeground(t, livePreviewValidatorStatusStyle("error"), colorError)
	assertForeground(t, livePreviewValidatorStatusStyle("running"), colorOverlay)
}

func TestLivePreviewFinalReviewingUsesLifecyclePhase(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{
		ID:           "feat-final-review",
		Status:       feature.StatusFinalReviewing,
		CurrentPhase: feature.PhaseFinalReview,
	}
	sess := newLivePreviewSession("feat-final-review-final-review-01", feature.PhaseReview,
		assistantMessage(llm.ContentBlock{Type: "text", Text: "reviewing cumulative diff"}),
	)

	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(100))
	for _, want := range []string{"Phase", feature.PhaseFinalReview.String(), "Current: " + feature.PhaseFinalReview.String(), "reviewing cumulative diff"} { //nolint:goconst // "Phase" is a generic UI-label assertion, not a reusable test concept
		if !strings.Contains(view, want) {
			t.Fatalf("final review live preview missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"Phase        Review", "Current: Review", "Current: " + feature.PhaseImplement.String()} {
		if strings.Contains(view, notWant) {
			t.Fatalf("final review live preview should not show %q in:\n%s", notWant, view)
		}
	}
}

func TestLivePreviewTranscriptTailCollapsesWhenConstrained(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	sess := newLivePreviewSession("constrained", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: testLPToolUseIDBash, Name: toolNameBash, Input: rawJSON(`{"command":"go test ./internal/tui"}`)}),
		assistantMessage(llm.ContentBlock{Type: "text", Text: "visible transcript row"}),
	)

	wide := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(100))
	if !strings.Contains(wide, "Current: "+feature.PhaseImplement.String()) || !strings.Contains(wide, "visible transcript row") {
		t.Fatalf("wide live preview should show banner and tail, got:\n%s", wide)
	}

	narrow := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(50))
	if strings.Contains(narrow, "visible transcript row") {
		t.Fatalf("narrow live preview should collapse transcript tail, got:\n%s", narrow)
	}
	for _, want := range []string{"Live Preview", "Current: Implement · ⟳ Using Bash..."} {
		if !strings.Contains(narrow, want) {
			t.Fatalf("narrow live preview missing %q in:\n%s", want, narrow)
		}
	}
}

func TestLivePreviewRendersTitledSectionBoxes(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	sess := newLivePreviewSession("boxed", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: "text", Text: "visible transcript row"}),
	)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(100))

	for _, want := range []string{"╭─ Live Preview", "╭─ Current: Implement", "visible transcript row"} {
		if !strings.Contains(view, want) {
			t.Fatalf("boxed live preview missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\n  - Current:") {
		t.Fatalf("current label should be rendered in the lower box title, got:\n%s", view)
	}
}

func TestLivePreviewActivityRendersInPhasePreviewTitle(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseDesign}
	sess := newLivePreviewSession("activity-title", feature.PhaseDesign,
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: "toolu_read", Name: toolNameRead, Input: rawJSON(`{"file_path":"README.md"}`)}),
	)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(100))

	if !strings.Contains(view, "Current: Design · ⟳ Using Read...") {
		t.Fatalf("activity should render in phase preview title, got:\n%s", view)
	}
	if strings.Count(view, "Using Read...") != 1 {
		t.Fatalf("activity should not also render inside the upper metadata box, got:\n%s", view)
	}
}

func TestLivePreviewPhaseTitleHandlesStyledLongActivity(t *testing.T) {
	t.Parallel()

	f := &feature.Feature{
		ID:                  "feat-long-activity",
		Status:              feature.StatusImplementing,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 2,
		TotalRoadmapPhases:  2,
	}
	longStatus := "Running 12:32:37 session ab1d1ed7fe2af11e-final-review-02: dropped critical SDK message (type=result) after 5s on full attachCh"
	sess := newLivePreviewSession("long-activity", feature.PhaseImplement,
		llm.SDKMessage{Type: "status", Status: &llm.StatusMessage{Type: "status", Message: longStatus}},
	)
	model := newLivePreviewModel(f).
		withSession(sess).
		withHeight(24)
	model.spinnerView = lipgloss.NewStyle().Foreground(colorInfo).Render("⠴")
	view := model.ViewCompact(100)

	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if got := lipgloss.Width(line); got > 96 {
			t.Fatalf("live preview line width = %d, want <= 96; line=%q", got, stripANSI(line))
		}
	}
	plain := stripANSI(view)
	for _, leaked := range []string{"38;2", "38:2", ":::20"} {
		if strings.Contains(plain, leaked) {
			t.Fatalf("live preview leaked ANSI fragment %q in:\n%s", leaked, plain)
		}
	}
}

func TestLivePreviewAttentionRendersQuestionBox(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Status:    feature.StatusImplementing,
		HelpQueue: []feature.HelpRequest{{Question: "Which API should we keep?", Pending: true}},
	}
	view := stripANSI(newLivePreviewModel(f).withHeight(20).ViewCompact(100))

	for _, want := range []string{"╭─ ? Question", "Which API should we keep?", "press [a] to Answer", "Waiting for an answer"} {
		if !strings.Contains(view, want) {
			t.Fatalf("attention question box missing %q in:\n%s", want, view)
		}
	}
}

func TestLivePreviewUpperMetadataShowsReposFeatureIDAndLinkedPRs(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-meta",
		Status:       feature.StatusPublished,
		CurrentPhase: feature.PhaseImplement,
		Models:       config.ModelConfig{Implementation: "agent-model"},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a"},
			{Name: "repo-b"},
		},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/repo-a/pull/42"},
			"repo-b": {Touched: true, PRURL: "https://github.com/org/repo-b/pull/43"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"repo-a": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning},
		},
	}
	raw := newLivePreviewModel(f).withHeight(24).ViewCompact(100)
	view := stripANSI(raw)

	for _, want := range []string{labelFeatureID, "feat-meta", "Repos", "repo-a, repo-b", "PRs", "#42", "#43", "Phase", "Implement", "Status", "Rebasing", "Phase Model", "agent-model", "Elapsed", labelCost} { //nolint:goconst // "Elapsed" is a generic UI-label assertion, not a reusable test concept
		if !strings.Contains(view, want) {
			t.Fatalf("live preview metadata missing %q in:\n%s", want, view)
		}
	}
	for _, url := range []string{"https://github.com/org/repo-a/pull/42", "https://github.com/org/repo-b/pull/43"} {
		if !strings.Contains(raw, url) {
			t.Fatalf("raw live preview should include hyperlink target %q in:\n%q", url, raw)
		}
		if strings.Contains(view, url) {
			t.Fatalf("plain live preview should show compact PR numbers, not full URL %q in:\n%s", url, view)
		}
	}
}

func TestLivePreviewUpperMetadataUsesShortPhaseModelName(t *testing.T) {
	t.Parallel()
	const routedModel = "gateway:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"
	f := &feature.Feature{
		ID:           "feat-short-model",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Models:       config.ModelConfig{KBBuild: routedModel},
	}

	view := stripANSI(newLivePreviewModel(f).withHeight(24).ViewCompact(120))
	if strings.Contains(view, "portkey/@fireworks/accounts/fireworks/models") {
		t.Fatalf("live preview rendered routed model ID, want compact model name:\n%s", view)
	}
	if strings.Contains(view, "gateway:glm-5p2[1.04M]") {
		t.Fatalf("live preview phase model should omit context window suffix:\n%s", view)
	}
	if !strings.Contains(view, "Phase Model") || !strings.Contains(view, "gateway:glm-5p2") {
		t.Fatalf("live preview missing compact phase model:\n%s", view)
	}

	sess := session.NewSession("feat-short-model-kb", f.ID, feature.PhaseKnowledgeBase)
	sess.SetModel(routedModel)
	withSession := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))
	if strings.Contains(withSession, "portkey/@fireworks/accounts/fireworks/models") {
		t.Fatalf("live preview session model rendered routed model ID, want compact model name:\n%s", withSession)
	}
	if strings.Contains(withSession, "gateway:glm-5p2[1.04M]") {
		t.Fatalf("live preview session phase model should omit context window suffix:\n%s", withSession)
	}
	if !strings.Contains(withSession, "gateway:glm-5p2") {
		t.Fatalf("live preview session model missing compact phase model:\n%s", withSession)
	}
}

func TestLivePreviewConfiguredPhaseModelUsesInquiryForInquire(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		Models: config.ModelConfig{
			Inquiry:  "claude:clarify-model",
			Research: "claude:research-model",
			Planning: "claude:planning-model",
		},
	}

	if got := livePreviewConfiguredPhaseModel(f, feature.PhaseInquire); got != "claude:clarify-model" {
		t.Errorf("PhaseInquire model = %q, want inquiry model", got)
	}
	if got := livePreviewConfiguredPhaseModel(f, feature.PhaseResearch); got != "claude:research-model" {
		t.Errorf("PhaseResearch model = %q, want research model", got)
	}
	if got := livePreviewConfiguredPhaseModel(f, feature.PhaseDesign); got != "claude:planning-model" {
		t.Errorf("PhaseDesign model = %q, want planning model", got)
	}
}

func TestLivePreviewUpperMetadataKeepsConfiguredProviderForBackendSessionModel(t *testing.T) {
	t.Parallel()
	const routedModel = "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"
	const backendModel = "portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"
	f := &feature.Feature{
		ID:           "feat-opencode-model",
		Status:       feature.StatusResearching,
		CurrentPhase: feature.PhaseResearch,
		Models:       config.ModelConfig{Research: routedModel},
	}

	sess := session.NewSession("feat-opencode-model-research", f.ID, feature.PhaseResearch)
	sess.SetModel(backendModel)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(120))
	if strings.Contains(view, "portkey/@fireworks/accounts/fireworks/models") {
		t.Fatalf("live preview session model rendered routed model ID, want compact model name:\n%s", view)
	}
	if strings.Contains(view, "opencode:glm-5p2[1.04M]") {
		t.Fatalf("live preview session phase model should omit context window suffix:\n%s", view)
	}
	if !strings.Contains(view, "Phase Model") || !strings.Contains(view, "opencode:glm-5p2") {
		t.Fatalf("live preview session model missing configured provider prefix:\n%s", view)
	}
}

func TestLivePreviewTranscriptRowsWrapToContentWidth(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	longText := strings.Repeat("longword", 20)
	sess := newLivePreviewSession("truncate", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: "text", Text: longText}),
	)
	view := newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(80)
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if w := lipgloss.Width(line); w > 76 {
			t.Fatalf("LivePreviewModel.ViewCompact line width = %d, want <= 76; line=%q", w, stripANSI(line))
		}
	}
	if strings.Contains(stripANSI(view), "…") {
		t.Fatalf("wrapped live preview should not ellipsize rows, got:\n%s", stripANSI(view))
	}
	letters := strings.Builder{}
	for _, r := range stripANSI(view) {
		if unicode.IsLetter(r) {
			letters.WriteRune(r)
		}
	}
	if !strings.Contains(letters.String(), longText) {
		t.Fatalf("wrapped live preview lost transcript text, got:\n%s", stripANSI(view))
	}
}

func TestLivePreviewToolResultsTruncateToSingleLine(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	longResult := strings.Repeat("tool-result-body-", 20)
	sess := newLivePreviewSession("truncate-tool-result", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: "toolu_trunc", Name: testToolNameWebFetch, Input: rawJSON(`{"url":"https://example.com/really/long/path"}`)}),
		userMessage(llm.ContentBlock{Type: blockTypeToolResult, ToolUseID: "toolu_trunc", Content: rawJSON(`"` + longResult + `"`)}),
	)
	view := newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(80)
	plain := stripANSI(view)

	if strings.Count(plain, "WebFetch result:") != 1 {
		t.Fatalf("tool result should render as one row, got:\n%s", plain)
	}
	if !strings.Contains(plain, "[...]") {
		t.Fatalf("truncated tool result should contain fixed truncation marker, got:\n%s", plain)
	}
	if strings.Contains(plain, longResult) {
		t.Fatalf("tool result should be truncated, got:\n%s", plain)
	}
	for _, line := range strings.Split(strings.TrimRight(view, "\n"), "\n") {
		if w := lipgloss.Width(line); w > 76 {
			t.Fatalf("LivePreviewModel.ViewCompact line width = %d, want <= 76; line=%q", w, stripANSI(line))
		}
	}
}

func TestLivePreviewToolResultsUseFixedCharacterCap(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement}
	longResult := strings.Repeat("tool-result-body-", 20)
	sess := newLivePreviewSession("fixed-tool-result", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: "toolu_fixed", Name: testToolNameWebFetch, Input: rawJSON(`{"url":"https://example.com/really/long/path"}`)}),
		userMessage(llm.ContentBlock{Type: blockTypeToolResult, ToolUseID: "toolu_fixed", Content: rawJSON(`"` + longResult + `"`)}),
	)
	view := stripANSI(newLivePreviewModel(f).withSession(sess).withHeight(24).ViewCompact(180))

	var resultLine string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "WebFetch result:") {
			resultLine = strings.TrimSpace(strings.Trim(line, "│ "))
			break
		}
	}
	if resultLine == "" {
		t.Fatalf("tool result row missing in:\n%s", view)
	}
	if !strings.HasSuffix(resultLine, "[...]") {
		t.Fatalf("tool result row should end with fixed truncation marker, got %q", resultLine)
	}
	text := strings.TrimPrefix(resultLine, "= ")
	if got := len([]rune(text)); got != livePreviewToolResultMaxChars {
		t.Fatalf("tool result text length = %d, want %d; row=%q", got, livePreviewToolResultMaxChars, resultLine)
	}
}

func TestDashboardRendersLivePreviewForEligibleFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:               "feat-live",
		Name:             "Live Feature",
		Slug:             "live-feature",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 2,
		Created:          time.Now(),
		Repos:            []feature.FeatureRepo{{Name: "api"}},
		PhaseTimings: map[string]time.Duration{
			"implement": 12 * time.Minute,
		},
		PhaseCosts: map[string]float64{
			"implement": 0.42,
		},
	}
	sess := newLivePreviewSession("feat-live-impl", feature.PhaseImplement,
		llm.SDKMessage{Type: "system", Subtype: "init", Init: &llm.SystemInitMessage{SessionID: "s1", Model: "opus"}},
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: testLPToolUseIDBash, Name: toolNameBash, Input: rawJSON(`{"command":"go test ./internal/tui"}`)}),
		assistantMessage(llm.ContentBlock{Type: "text", Text: testReadyToPatchText}),
		llm.SDKMessage{Type: "status", Status: &llm.StatusMessage{Message: "routine status"}},
		llm.SDKMessage{Type: "tool_progress", ToolProgress: &llm.ToolProgressMessage{ToolName: toolNameBash, Data: testToolProgressDataDownload30}},
		controlRequest("ask_1", toolNameAskUserQuestion, rawJSON(`{"questions":[{"question":"Proceed with patch?"}]}`)),
	)
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"
	m.livePreview.contextPct = 42
	m.livePreview.session = sess

	view := m.View()
	for _, want := range []string{"Live Preview", labelFeatureID, "feat-live", "Repos", "api", "Phase", "Implement", "Status", "Implementing", "Context", "42% used", "Elapsed", "12m", labelCost, "$0.42", testUsingBashActivity, "Current: " + feature.PhaseImplement.String(), testReadyToPatchText, "AskUser: Proceed with patch?", "[a] Watch"} { //nolint:goconst // "Context" is a generic UI-label assertion, not a reusable test concept
		if !strings.Contains(view, want) {
			t.Fatalf("dashboard live preview missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{"routine status", testToolProgressDataDownload30, "session=s1"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("dashboard live preview contained noisy %q in:\n%s", notWant, view)
		}
	}
	if strings.Contains(view, "Phase Progress") {
		t.Fatalf("live preview should replace static detail phase progress, got:\n%s", view)
	}
}

func TestDashboardRendersLivePreviewForFeatureRebase(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-rebase",
		Name:         "Feature Rebase",
		Slug:         "feature-rebase",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		Repos:        []feature.FeatureRepo{{Name: "api"}},
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleRebase,
			Status: feature.RepoCycleRunning,
			Count:  1,
		},
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"

	view := stripANSI(m.View())
	for _, want := range []string{"Live Preview", "Status", "Rebasing [1]", "Current: Rebasing [1]", "[a] Watch"} {
		if !strings.Contains(view, want) {
			t.Fatalf("feature rebase live preview missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Phase Progress") {
		t.Fatalf("feature rebase should render live preview instead of static detail, got:\n%s", view)
	}
}

func TestLivePreviewContextMetadata(t *testing.T) {
	t.Parallel()

	sessionWithUsage := session.NewSession("feat-context-impl", "feat-context", feature.PhaseImplement)
	sessionWithUsage.SetLatestUsage(&llm.Usage{InputTokens: 20_000, ContextWindow: 200_000})

	tests := []struct {
		name       string
		f          *feature.Feature
		sess       session.SessionView
		contextPct int
		want       string
		notWant    string
	}{
		{
			name:       "active session shows context window and used percentage",
			f:          &feature.Feature{ID: "feat-context", Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			sess:       sessionWithUsage,
			contextPct: 10,
			want:       "200K (10% used)",
			notWant:    "Calculating",
		},
		{
			name: "configured model suffix supplies context window",
			f: &feature.Feature{
				ID:           "feat-context",
				Status:       feature.StatusImplementing,
				CurrentPhase: feature.PhaseImplement,
				Models:       config.ModelConfig{Implementation: "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]"},
			},
			contextPct: 2,
			want:       "1.04M (2% used)",
			notWant:    "Calculating",
		},
		{
			name:       "active session without usage shows calculating",
			f:          &feature.Feature{ID: "feat-context", Status: feature.StatusImplementing, CurrentPhase: feature.PhaseImplement},
			contextPct: -1,
			want:       "Calculating",
		},
		{
			name:       "review gate without session omits context",
			f:          &feature.Feature{ID: "feat-review", Status: feature.StatusPlanNeedsReview, CurrentPhase: feature.PhasePlan},
			contextPct: 44,
			notWant:    "Context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := stripANSI(newLivePreviewModel(tt.f).withSession(tt.sess).withContextPct(tt.contextPct).withHeight(24).ViewCompact(100))
			if tt.want != "" && !strings.Contains(view, tt.want) {
				t.Fatalf("live preview context metadata missing %q in:\n%s", tt.want, view)
			}
			if tt.notWant != "" && strings.Contains(view, tt.notWant) {
				t.Fatalf("live preview context metadata contained %q in:\n%s", tt.notWant, view)
			}
		})
	}
}

func TestDashboardOverviewModeUsesCompactDetailForEligibleFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-overview",
		Name:         "Overview Feature",
		Slug:         "overview-feature",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Created:      time.Now(),
		Repos:        []feature.FeatureRepo{{Name: "api"}},
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.rightPanelMode = dashboardRightPanelOverview

	view := stripANSI(m.View())
	for _, want := range []string{"Info", "Phase Progress", "[l] Live Preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overview mode missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{labelFeatureID, "Current: " + feature.PhaseImplement.String(), "[o] Overview"} { //nolint:goconst // "[o] Overview" is a generic UI-label assertion, not a reusable test concept
		if strings.Contains(view, notWant) {
			t.Fatalf("overview mode contained live-preview copy %q in:\n%s", notWant, view)
		}
	}
}

func TestDashboardOverviewModeShowsRefactorCycleSubphase(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-refactor",
		Name:         "Refactor Feature",
		Slug:         "refactor-feature",
		Status:       feature.StatusCodeReady,
		CurrentPhase: feature.PhasePublish,
		Created:      time.Now(),
		Repos:        []feature.FeatureRepo{{Name: "api"}},
		RepoCycles: map[string]*feature.RepoCycleState{
			"api": {Type: feature.CycleRefactor, Status: feature.RepoCycleRunning},
		},
		ActiveCycle: &feature.CycleState{
			Type:   feature.CycleRefactor,
			Status: feature.RepoCycleRunning,
			Count:  1,
		},
		PhaseTimings: map[string]time.Duration{
			"implement": 5 * time.Minute,
		},
	}
	f.SetRefactorCount(1)
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.rightPanelMode = dashboardRightPanelOverview
	m.spinnerView = "spin"

	view := stripANSI(m.View())
	for _, want := range []string{"Info", "Phase Progress", "Refactor #1", "in progress", "[l] Live Preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("refactor cycle overview missing %q in:\n%s", want, view)
		}
	}
	for _, notWant := range []string{labelFeatureID, "Current: Refactoring", "[o] Overview"} {
		if strings.Contains(view, notWant) {
			t.Fatalf("refactor cycle overview contained live-preview copy %q in:\n%s", notWant, view)
		}
	}
}

func TestDashboardLivePreviewConstrainedCollapseKeepsFooter(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-narrow",
		Name:         "Narrow Feature",
		Slug:         "narrow-feature",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		Created:      time.Now(),
	}
	sess := newLivePreviewSession("feat-narrow-impl", feature.PhaseImplement,
		assistantMessage(llm.ContentBlock{Type: blockTypeToolUse, ID: testLPToolUseIDBash, Name: toolNameBash, Input: rawJSON(`{"command":"go test ./internal/tui"}`)}),
		assistantMessage(llm.ContentBlock{Type: "text", Text: "this transcript should collapse"}),
	)
	m := dashboardWithSelectedFeature(f)
	m.width = 80
	m.height = 14
	m.focusPanel = 1
	m.spinnerView = "spin"
	m.livePreview.session = sess

	view := stripANSI(m.View())
	if strings.Contains(view, "this transcript should collapse") {
		t.Fatalf("constrained dashboard should hide transcript tail, got:\n%s", view)
	}
	for _, want := range []string{"Live Preview", labelFeatureID, "feat-narrow", "Current: Implement · spin Using Bash...", "[a] Watch"} {
		if !strings.Contains(view, want) {
			t.Fatalf("constrained dashboard missing %q in:\n%s", want, view)
		}
	}
}

func TestDashboardRendersStartupLivePreviewWithoutSession(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-created",
		Name:         "Created Feature",
		Slug:         "created-feature",
		Status:       feature.StatusCreated,
		CurrentPhase: feature.PhaseResearch,
		Created:      time.Now(),
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"

	view := m.View()
	if !strings.Contains(view, "Live Preview") || !strings.Contains(view, "Starting Research...") {
		t.Fatalf("created feature should render startup live preview, got:\n%s", view)
	}
	if strings.Contains(view, "No transcript") {
		t.Fatalf("created startup preview must not render an empty transcript placeholder, got:\n%s", view)
	}
}

func TestDashboardRendersStaticDetailForFallbackFeature(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-static",
		Name:         "Static Feature",
		Slug:         "static-feature",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement,
		Created:      time.Now(),
	}
	m := dashboardWithSelectedFeature(f)
	m.focusPanel = 1
	m.spinnerView = "spin"

	view := m.View()
	if strings.Contains(view, "Live Preview") {
		t.Fatalf("failed feature should render static detail, got:\n%s", view)
	}
	if !strings.Contains(view, "Phase Progress") {
		t.Fatalf("failed feature should retain static detail panel, got:\n%s", view)
	}
}

func TestDashboardFeatureRowSpinnerUsesLivePreviewPredicate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		f        *feature.Feature
		wantSpin bool
	}{
		{
			name:     "running",
			f:        &feature.Feature{ID: "running", Slug: "running", Status: feature.StatusImplementing},
			wantSpin: true,
		},
		{
			name:     "created",
			f:        &feature.Feature{ID: "created", Slug: "created", Status: feature.StatusCreated},
			wantSpin: true,
		},
		{
			name: "active cycle",
			f: &feature.Feature{
				ID:     "cycle",
				Slug:   "cycle",
				Status: feature.StatusPublished,
				RepoCycles: map[string]*feature.RepoCycleState{
					"api": {Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning},
				},
			},
			wantSpin: true,
		},
		{
			name: "active feature cycle",
			f: &feature.Feature{
				ID:     "feature-cycle",
				Slug:   "feature-cycle",
				Status: feature.StatusCodeReady,
				ActiveCycle: &feature.CycleState{
					Type:   feature.CycleRebase,
					Status: feature.RepoCycleRunning,
					Count:  1,
				},
			},
			wantSpin: true,
		},
		{
			name:     "failed",
			f:        &feature.Feature{ID: "failed", Slug: "failed", Status: feature.StatusFailed},
			wantSpin: false,
		},
		{
			name:     "idle published",
			f:        &feature.Feature{ID: "published", Slug: "published", Status: feature.StatusPublished},
			wantSpin: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewDashboardModel([]*feature.Feature{tt.f}, "")
			m.spinnerView = "spin"
			row := m.renderFeatureRowCompact(tt.f, false)
			hasSpin := strings.Contains(row, "spin")
			if hasSpin != tt.wantSpin {
				t.Errorf("renderFeatureRowCompact(%s) spinner = %v, want %v; row=%q", tt.name, hasSpin, tt.wantSpin, row)
			}
		})
	}
}

func newLivePreviewSession(id string, phase feature.Phase, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", phase)
	sess.SetStatus(session.SessionRunning)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func newLivePreviewSessionWithIteration(id string, phase feature.Phase, iteration int, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", phase)
	sess.SetStatus(session.SessionRunning)
	sess.SetIteration(iteration)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func streamingLivePreviewSession(id string, phase feature.Phase, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", phase)
	sess.SetProviderName("streaming")
	sess.SetStatus(session.SessionRunning)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func validatorLivePreviewSession(id, label string, messages ...llm.SDKMessage) session.SessionView {
	sess := session.NewSession(id, "feat-live", feature.PhasePlan)
	sess.SetKind(ports.KindValidator)
	sess.SetLabel(label)
	sess.SetStatus(session.SessionRunning)
	for _, msg := range messages {
		sess.MessageLog().Append(msg)
	}
	return sess
}

func assistantMessage(blocks ...llm.ContentBlock) llm.SDKMessage {
	return llm.SDKMessage{
		Type: roleAssistant,
		Assistant: &llm.AssistantMessage{
			Message: llm.ConversationMsg{
				Role:    roleAssistant,
				Content: blocks,
			},
		},
	}
}

func userMessage(blocks ...llm.ContentBlock) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "user",
		User: &llm.UserMessage{
			Message: llm.ConversationMsg{
				Role:    "user",
				Content: blocks,
			},
		},
	}
}

func controlRequest(requestID, toolName string, input json.RawMessage) llm.SDKMessage {
	return llm.SDKMessage{
		Type: msgTypeControlRequest,
		ControlRequest: &llm.ControlRequestMessage{
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  controlRequestSubtypeCanUseTool,
				ToolName: toolName,
				Input:    input,
			},
		},
	}
}

func rawJSON(s string) json.RawMessage {
	return json.RawMessage(s)
}

func (m LivePreviewModel) withSession(sess session.SessionView) LivePreviewModel {
	m.session = sess
	return m
}

func (m LivePreviewModel) withContextPct(contextPct int) LivePreviewModel {
	m.contextPct = contextPct
	return m
}

func (m LivePreviewModel) withHeight(height int) LivePreviewModel {
	m.height = height
	return m
}

func dashboardWithSelectedFeature(f *feature.Feature) DashboardModel {
	m := NewDashboardModel([]*feature.Feature{f}, "")
	m.width = 120
	m.height = 30
	m.cursor = 1
	m.syncPreview()
	return m
}
