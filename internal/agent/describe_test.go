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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestBuildPRDescriptionPrompt(t *testing.T) {
	prompt := BuildPRDescriptionPrompt(PRContext{
		FeatureName:        "my-feature",
		FeatureDescription: "feature desc",
		Roadmap:            "plan content",
		CommitBodies:       "commit body content",
		DiffStat:           " internal/foo.go | 42 ++++++++",
	})
	wantContains := []string{
		"my-feature",
		"feature desc",
		"plan content",
		"commit body content",
		"internal/foo.go",
		"TITLE:",
		"BODY:",
	}
	for _, s := range wantContains {
		if !strings.Contains(prompt, s) {
			t.Errorf("prompt missing %q", s)
		}
	}
	if strings.Contains(prompt, "```diff") {
		t.Error("prompt should no longer embed a raw diff block")
	}
}

func TestBuildPRDescriptionPrompt_EmitsOnlyPopulatedSections(t *testing.T) {
	prompt := BuildPRDescriptionPrompt(PRContext{Roadmap: "plan only"})
	if !strings.Contains(prompt, "plan only") {
		t.Error("expected roadmap content in prompt")
	}
	for _, s := range []string{"## Feature", "## Commit Messages", "## Changes (file stats)"} {
		if strings.Contains(prompt, s) {
			t.Errorf("empty section %q should have been omitted", s)
		}
	}
}

func TestBuildPRDescriptionFallback(t *testing.T) {
	ctx := PRContext{
		FeatureName:        "Refactor auth middleware",
		FeatureDescription: "Rip out legacy session storage.",
		CommitBodies:       "Refactor auth middleware\n\nlong body\n---commit---\nUpdate tests\n---commit---\n",
		DiffStat:           " internal/auth.go | 20 ++++++\n",
	}
	title, body := BuildPRDescriptionFallback(ctx)
	if title != "Refactor auth middleware" {
		t.Errorf("title = %q, want feature name", title)
	}
	for _, s := range []string{"## Summary", "Rip out legacy", "## Commits", "Refactor auth middleware", "Update tests", "## Changes", "internal/auth.go", "## Test plan"} {
		if !strings.Contains(body, s) {
			t.Errorf("body missing %q\nbody:\n%s", s, body)
		}
	}
}

func TestBuildPRDescriptionFallback_EmptyContext(t *testing.T) {
	title, body := BuildPRDescriptionFallback(PRContext{})
	if title != "Feature implementation" {
		t.Errorf("empty-context title = %q, want 'Feature implementation'", title)
	}
	if !strings.Contains(body, "## Summary") || !strings.Contains(body, "## Test plan") {
		t.Errorf("empty-context body missing required sections:\n%s", body)
	}
}

func TestFallbackTitle_TruncatesLongStrings(t *testing.T) {
	long := strings.Repeat("x", 100)
	title := fallbackTitle(PRContext{FeatureName: long})
	if runes := []rune(title); len(runes) > 70 {
		t.Errorf("title rune length = %d, want <= 70", len(runes))
	}
}

func TestRunDescriptionGenerationCommand(t *testing.T) {
	// We can't run the actual claude CLI in tests, but we can verify the
	// prompt construction and parsing work end-to-end with known output.
	sampleOutput := "TITLE: Add feature X\nBODY:\n## Summary\n\n- Added X\n\n## Test plan\n\n- [ ] Test X\n"
	title, body := ParsePRDescription(sampleOutput)
	if title != "Add feature X" {
		t.Errorf("title = %q, want 'Add feature X'", title)
	}
	if !strings.Contains(body, "Added X") {
		t.Error("expected body to contain 'Added X'")
	}
	if !strings.Contains(body, "Test plan") {
		t.Error("expected body to contain 'Test plan'")
	}
}

func TestParsePRDescription(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "full marked output",
			output:    "TITLE: Fix authentication bug\nBODY:\n## Summary\n\n- Fixed auth\n",
			wantTitle: "Fix authentication bug",
			wantBody:  "## Summary\n\n- Fixed auth",
		},
		{
			name:      "marked without BODY tag",
			output:    "TITLE: Quick patch\n\nSome description continues here.\n",
			wantTitle: "Quick patch",
			wantBody:  "Some description continues here.",
		},
		{
			name:      "unmarked with heading",
			output:    "# Refactor auth\n\nBody line.\n",
			wantTitle: "Refactor auth",
			wantBody:  "Body line.",
		},
		{
			name:      "unmarked plain text",
			output:    "First line as title\nrest of body\n",
			wantTitle: "First line as title",
			wantBody:  "rest of body",
		},
		{
			name:      "empty output returns empty",
			output:    "",
			wantTitle: "",
			wantBody:  "",
		},
		{
			name:      "title only",
			output:    "TITLE: Simple fix\nBODY:\n",
			wantTitle: "Simple fix",
			wantBody:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, body := ParsePRDescription(tt.output)
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
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
			output: `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"TITLE: Fix bug"}]}}` + "\n" + `{"type":"result","subtype":"success","session_id":"s1","total_cost_usd":0.01}`,
			want:   "TITLE: Fix bug",
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
	sess.msgLog.Append(mocks.AssistantTextMessage("TITLE: Test PR\nBODY:\n## Summary\n- Test change"))
	sess.result = &llm.ResultMessage{
		Type:       "result",
		Subtype:    "success",
		Result:     "done",
		StopReason: "end_turn",
	}
	sess.statusCh <- "SUCCESS"

	runner := newUtilityTestPhaseRunner(t, sess)
	prCtx := PRContext{FeatureName: "test", Roadmap: "plan content"}
	title, body, err := runner.pr.RunDescriptionGeneration(context.Background(), "sonnet", prCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "Test PR" {
		t.Errorf("expected title 'Test PR', got %q", title)
	}
	if body == "" {
		t.Error("expected non-empty body")
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
}

func TestPhaseRunnerRunDescriptionGeneration_FallsBackOnHelperError(t *testing.T) {
	sess := newUtilityTestSession()
	sess.attachCh <- mocks.ControlRequestMsg("perm-1", "Bash")

	runner := newUtilityTestPhaseRunner(t, sess)
	prCtx := PRContext{
		FeatureName:        "test",
		FeatureDescription: "feature desc",
	}

	title, body, err := runner.pr.RunDescriptionGeneration(context.Background(), "sonnet", prCtx)
	if err == nil {
		t.Fatal("RunDescriptionGeneration() error = nil, want fallback error")
	}
	if title != "test" {
		t.Errorf("title = %q, want fallback title %q", title, "test")
	}
	if !strings.Contains(body, "## Summary") {
		t.Errorf("body = %q, want fallback body", body)
	}
}
