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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestBuildSummaryPrompt(t *testing.T) {
	prompt := BuildSummaryPrompt("caching-layer", "Add Redis caching to the API gateway for frequently accessed endpoints")
	if !strings.Contains(prompt, "caching-layer") {
		t.Error("expected feature name in prompt")
	}
	if !strings.Contains(prompt, "Redis caching") {
		t.Error("expected description in prompt")
	}
	if !strings.Contains(prompt, "1-2 concise sentences") {
		t.Error("expected summarization instruction in prompt")
	}
}

func TestParseSummary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"clean output",
			"Add a Redis caching layer to improve API response times.",
			"Add a Redis caching layer to improve API response times.",
		},
		{
			"whitespace padding",
			"  \n  Summary text here.  \n  ",
			"Summary text here.",
		},
		{
			"quoted output",
			`"Add caching to speed up the API."`,
			"Add caching to speed up the API.",
		},
		{
			"empty output",
			"",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSummary(tt.input)
			if got != tt.want {
				t.Errorf("ParseSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPhaseRunnerRunSummaryGeneration_UsesUtilitySession(t *testing.T) {
	sess := newUtilityTestSession()
	sess.msgLog.Append(mocks.AssistantTextMessage("A concise summary of the feature."))
	sess.result = &llm.ResultMessage{
		Type:       "result",
		Subtype:    "success",
		Result:     "done",
		StopReason: "end_turn",
	}
	sess.statusCh <- "SUCCESS"

	runner := newUtilityTestPhaseRunner(t, sess)
	summary, err := runner.pr.RunSummaryGeneration(context.Background(), "test-feature", "test description")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "A concise summary of the feature." {
		t.Errorf("summary = %q, want %q", summary, "A concise summary of the feature.")
	}
	if len(runner.capturedOpts) != 1 {
		t.Fatalf("captured opts = %d, want 1", len(runner.capturedOpts))
	}
	opts := runner.capturedOpts[0]
	if opts.Model != summaryModel {
		t.Errorf("BuildSessionOpts.Model = %q, want %q", opts.Model, summaryModel)
	}
	if opts.Phase != feature.PhaseResearch {
		t.Errorf("BuildSessionOpts.Phase = %v, want %v", opts.Phase, feature.PhaseResearch)
	}
	if !strings.Contains(opts.Prompt, "test-feature") || !strings.Contains(opts.Prompt, "test description") {
		t.Errorf("BuildSessionOpts.Prompt = %q, want feature name and description", opts.Prompt)
	}
}

func TestPhaseRunnerRunSummaryGeneration_PreservesTimeout(t *testing.T) {
	oldTimeout := summaryTimeout
	summaryTimeout = 10 * time.Millisecond
	t.Cleanup(func() { summaryTimeout = oldTimeout })

	sess := newUtilityTestSession()
	runner := newUtilityTestPhaseRunner(t, sess)

	_, err := runner.pr.RunSummaryGeneration(context.Background(), "test-feature", "test description")
	if err == nil {
		t.Fatal("RunSummaryGeneration() error = nil, want timeout")
	}
	if !strings.Contains(err.Error(), "timed out after 10ms") {
		t.Errorf("RunSummaryGeneration() error = %q, want timeout context", err)
	}
}
