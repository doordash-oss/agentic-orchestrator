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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// twoLayerStack is the shared two-layer stack fixture: layer 2 is the top.
func twoLayerStack() []prompts.PRStackLayerView {
	return []prompts.PRStackLayerView{
		{Position: 1, Title: "Foundations", Phases: []int{1, 2}, Branch: "feature/feat-x-1/foundations"},
		{Position: 2, Title: "Review loop", Phases: []int{3}, Branch: "feature/feat-x-2/review-loop"},
	}
}

func TestBuildPRDescriptionPrompt_LayerOfStack(t *testing.T) {
	prompt := BuildPRDescriptionPrompt(PRContext{
		FeatureName:        "my-feature",
		FeatureDescription: "feature desc",
		LayerPosition:      2,
		LayerTitle:         "Review loop",
		LayerPhases:        []int{3},
		LayerRationale:     "Keeps review feedback inside one pull request.",
		Stack:              twoLayerStack(),
		CommitBodies:       "commit body content",
		DiffStat:           " internal/foo.go | 42 ++++++++",
	})
	wantContains := []string{
		"my-feature",
		"feature desc",
		"Position: Layer 2 of 2",
		"Title: Review loop",
		"Phases: [3]",
		"Rationale: Keeps review feedback inside one pull request.",
		"- Layer 1: Foundations — phases [1 2], branch feature/feat-x-1/foundations",
		"- Layer 2: Review loop (top layer) — phases [3], branch feature/feat-x-2/review-loop",
		"commit body content",
		"internal/foo.go",
		"Describe only this layer's changes",
		"Do not request or invoke tools",
		"Output the body in markdown only. Do not include a pull request title.",
	}
	for _, s := range wantContains {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	for _, s := range []string{"TITLE:", "BODY:", "## Roadmap", "concise title"} {
		if strings.Contains(prompt, s) {
			t.Errorf("prompt must not contain %q", s)
		}
	}
	if strings.Contains(prompt, "```diff") {
		t.Error("prompt should no longer embed a raw diff block")
	}
}

func TestBuildPRDescriptionPrompt_EmitsOnlyPopulatedSections(t *testing.T) {
	prompt := BuildPRDescriptionPrompt(PRContext{FeatureName: "my-feature", LayerPosition: 1})
	if !strings.Contains(prompt, "Name: my-feature") {
		t.Error("expected feature name in prompt")
	}
	if !strings.Contains(prompt, "Position: Layer 1") {
		t.Error("expected layer position in prompt")
	}
	for _, s := range []string{"## Commit Messages", "## Changes (file stats)", "## Delivery Stack"} {
		if strings.Contains(prompt, s) {
			t.Errorf("empty section %q should have been omitted", s)
		}
	}
}

func TestParsePRDescription(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		wantBody string
	}{
		{
			name:     "body-only reply is the body",
			output:   "## Summary\n\n- Added X\n\n## Test plan\n\n- [ ] Test X\n",
			wantBody: "## Summary\n\n- Added X\n\n## Test plan\n\n- [ ] Test X",
		},
		{
			name:     "legacy marked output strips title and body markers",
			output:   "TITLE: Fix authentication bug\nBODY:\n## Summary\n\n- Fixed auth\n",
			wantBody: "## Summary\n\n- Fixed auth",
		},
		{
			name:     "legacy marked output without BODY marker",
			output:   "TITLE: Quick patch\n\nSome description continues here.\n",
			wantBody: "Some description continues here.",
		},
		{
			name:     "unmarked single-paragraph reply stays whole",
			output:   "First line is body\nrest of body\n",
			wantBody: "First line is body\nrest of body",
		},
		{
			name:     "empty output returns empty body",
			output:   "",
			wantBody: "",
		},
		{
			name:     "whitespace-only output returns empty body",
			output:   "  \n\n",
			wantBody: "",
		},
		{
			name:     "title-only legacy reply has no body",
			output:   "TITLE: Simple fix\nBODY:\n",
			wantBody: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := ParsePRDescription(tt.output)
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestExtractTextFromStreamJSON(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name:   "assistant text blocks",
			output: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"## Summary"}]}}` + "\n" + `{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}`,
			want:   "## Summary",
		},
		{
			name:   "result text fallback",
			output: `{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01,"result":"Final answer"}`,
			want:   "Final answer",
		},
		{
			name:   "plain text fallback",
			output: "Not JSON at all",
			want:   "Not JSON at all",
		},
		{
			name:   "empty",
			output: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTextFromStreamJSON(tt.output)
			if got != tt.want {
				t.Errorf("extractTextFromStreamJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPhaseRunnerRunDescriptionGeneration_UsesUtilitySession(t *testing.T) {
	sess := newUtilityTestSession()
	sess.msgLog.Append(mocks.AssistantTextMessage("## Summary\n- Test change"))
	sess.result = &llm.ResultMessage{
		Type:       testResultMessageType,
		Subtype:    testResultSuccessValue,
		Result:     "done",
		StopReason: testStopReasonEndTurn,
	}
	sess.statusCh <- agentStatusSuccess

	runner := newUtilityTestPhaseRunner(t, sess)
	prCtx := PRContext{
		FeatureName:   "test",
		LayerPosition: 1,
		LayerTitle:    "Only layer",
		Stack:         twoLayerStack()[:1],
	}
	body, err := runner.pr.RunDescriptionGeneration(context.Background(), "feat-publish", "sonnet", prCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(body, "Test change") {
		t.Errorf("expected generated body, got %q", body)
	}
	if len(runner.capturedOpts) != 1 {
		t.Fatalf("captured opts = %d, want 1", len(runner.capturedOpts))
	}
	opts := runner.capturedOpts[0]
	if opts.Model != "sonnet" {
		t.Errorf("BuildSessionOpts.Model = %q, want %q", opts.Model, "sonnet")
	}
	if opts.Phase != feature.PhasePublish {
		t.Errorf("BuildSessionOpts.Phase = %v, want %v", opts.Phase, feature.PhasePublish)
	}
	if !strings.Contains(opts.SystemPrompt, "complete using only the context supplied") {
		t.Errorf("BuildSessionOpts.SystemPrompt missing tool-free completion instruction: %q", opts.SystemPrompt)
	}
	if opts.PermHandler == nil {
		t.Fatal("BuildSessionOpts.PermHandler = nil, want PR-description deny-all handler")
	}
	for _, toolName := range []string{"Bash", "Read", "Write", "WebSearch", "Agent", "FutureTool"} {
		decision, decisionErr := opts.PermHandler.CanUseTool(ports.ToolPermissionRequest{
			ToolName: toolName,
			Input:    `{}`,
		})
		if decisionErr != nil {
			t.Fatalf("CanUseTool(%q): %v", toolName, decisionErr)
		}
		if decision.Behavior != "deny" {
			t.Errorf("CanUseTool(%q) behavior = %q, want deny", toolName, decision.Behavior)
		}
		if !strings.Contains(decision.Reason, "context supplied") {
			t.Errorf("CanUseTool(%q) reason = %q, want supplied-context direction", toolName, decision.Reason)
		}
	}
	if sess.featureID != "feat-publish" {
		t.Errorf("session featureID = %q, want %q", sess.featureID, "feat-publish")
	}
	if sess.phase != feature.PhasePublish {
		t.Errorf("session phase = %v, want %v", sess.phase, feature.PhasePublish)
	}
}

func TestPhaseRunnerRunDescriptionGeneration_ReturnsHelperErrorWithoutFallback(t *testing.T) {
	sess := newUtilityTestSession()
	sess.attachCh <- mocks.ControlRequestMsg("perm-1", "Bash")

	runner := newUtilityTestPhaseRunner(t, sess)
	prCtx := PRContext{
		FeatureName:        "test",
		FeatureDescription: "feature desc",
	}

	body, err := runner.pr.RunDescriptionGeneration(context.Background(), "feat-publish", "sonnet", prCtx)
	if err == nil {
		t.Fatal("RunDescriptionGeneration() error = nil, want helper error")
	}
	if body != "" {
		t.Errorf("RunDescriptionGeneration() = %q, want empty output on error", body)
	}
}

func TestPhaseRunnerRunDescriptionGeneration_RejectsEmptyBody(t *testing.T) {
	sess := newUtilityTestSession()
	sess.msgLog.Append(mocks.AssistantTextMessage("   "))
	sess.result = &llm.ResultMessage{
		Type:       testResultMessageType,
		Subtype:    testResultSuccessValue,
		Result:     "done",
		StopReason: testStopReasonEndTurn,
	}
	sess.statusCh <- agentStatusSuccess

	runner := newUtilityTestPhaseRunner(t, sess)
	body, err := runner.pr.RunDescriptionGeneration(
		context.Background(),
		"feat-publish",
		"sonnet",
		PRContext{FeatureName: "test", LayerPosition: 1},
	)
	if err == nil {
		t.Fatal("RunDescriptionGeneration() error = nil, want empty-body error")
	}
	if body != "" {
		t.Errorf("RunDescriptionGeneration() = %q, want empty output on incomplete result", body)
	}
}
