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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunBoundedHelper_SuccessWithoutPhaseComplete(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		mocks.AssistantTextMessage("Research complete"),
		{
			Type: "result",
			Result: &llm.ResultMessage{
				Type:         "result",
				Subtype:      "success",
				Result:       "success",
				StopReason:   "end_turn",
				TotalCostUSD: 0.12,
				Usage: &llm.Usage{
					InputTokens:  11,
					OutputTokens: 7,
				},
			},
		},
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:     "feature-scout-1",
		FeatureID:     "feature-1",
		Phase:         feature.PhaseResearch,
		Model:         "test-model",
		Prompt:        "Summarize the provided files.",
		SystemPrompt:  "You are a bounded research helper.",
		WorkDir:       workDir,
		Timeout:       2 * time.Second,
		EffortLevel:   llm.EffortMedium,
		PermHandler:   &permission.ReadOnlyHandler{},
		RequireOutput: true,
	})
	if err != nil {
		t.Fatalf("RunBoundedHelper() error = %v", err)
	}
	if result.Status != BoundedHelperStatusCompleted {
		t.Fatalf("result.Status = %q, want %q", result.Status, BoundedHelperStatusCompleted)
	}
	if result.Output != "Research complete" {
		t.Errorf("result.Output = %q, want %q", result.Output, "Research complete")
	}
	if result.Result == nil || result.Result.StopReason != "end_turn" {
		t.Fatalf("result.Result = %#v, want end_turn success result", result.Result)
	}
	if result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 7 {
		t.Errorf("result.Usage = %+v, want input=11 output=7", result.Usage)
	}
}

func TestRunBoundedHelper_FailsOnPendingAskUserQuestion(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		askUserControlRequest("ask-1"),
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:    "feature-scout-ask",
		FeatureID:    "feature-1",
		Phase:        feature.PhaseResearch,
		Model:        "test-model",
		Prompt:       "Ask if more detail is needed.",
		SystemPrompt: "You are a bounded research helper.",
		WorkDir:      workDir,
		Timeout:      2 * time.Second,
		EffortLevel:  llm.EffortMedium,
		PermHandler:  &permission.ReadOnlyHandler{},
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want AskUserQuestion failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusAskedUser {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusAskedUser)
	}
	if !strings.Contains(err.Error(), "asked for user input") {
		t.Errorf("error = %q, want AskUserQuestion context", err)
	}
}

func TestRunBoundedHelper_FailsOnPermissionRequest(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		mocks.ControlRequestMsg("perm-1", "Bash"),
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:    "feature-scout-perm",
		FeatureID:    "feature-1",
		Phase:        feature.PhaseResearch,
		Model:        "test-model",
		Prompt:       "Run a shell command.",
		SystemPrompt: "You are a bounded research helper.",
		WorkDir:      workDir,
		Timeout:      2 * time.Second,
		EffortLevel:  llm.EffortMedium,
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want permission failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusPermissionRequired {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusPermissionRequired)
	}
	if !strings.Contains(err.Error(), "requested tool permission") {
		t.Errorf("error = %q, want permission context", err)
	}
}

func TestRunBoundedHelper_FailsOnEmptyRequiredOutput(t *testing.T) {
	workDir := t.TempDir()
	pr, cleanup := newMockBoundedHelperRunner(t, []llm.SDKMessage{
		mocks.InitMessage(),
		{
			Type: "result",
			Result: &llm.ResultMessage{
				Type:       "result",
				Subtype:    "success",
				StopReason: "end_turn",
				Usage: &llm.Usage{
					InputTokens:  3,
					OutputTokens: 0,
				},
			},
		},
	})
	defer cleanup()

	result, err := pr.RunBoundedHelper(context.Background(), BoundedHelperConfig{
		SessionID:     "feature-scout-empty",
		FeatureID:     "feature-1",
		Phase:         feature.PhaseResearch,
		Model:         "test-model",
		Prompt:        "Return nothing.",
		SystemPrompt:  "You are a bounded research helper.",
		WorkDir:       workDir,
		Timeout:       2 * time.Second,
		EffortLevel:   llm.EffortMedium,
		PermHandler:   &permission.ReadOnlyHandler{},
		RequireOutput: true,
	})
	if err == nil {
		t.Fatal("RunBoundedHelper() error = nil, want empty output failure")
	}
	if result == nil {
		t.Fatal("RunBoundedHelper() result = nil, want status snapshot")
	}
	if result.Status != BoundedHelperStatusEmptyOutput {
		t.Errorf("result.Status = %q, want %q", result.Status, BoundedHelperStatusEmptyOutput)
	}
	if !strings.Contains(err.Error(), "without output") {
		t.Errorf("error = %q, want empty output context", err)
	}
}

func newMockBoundedHelperRunner(t *testing.T, messages []llm.SDKMessage) (*PhaseRunner, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	workDir := filepath.Join(tmpDir, "work")
	stateDir := filepath.Join(tmpDir, "state")
	for _, dir := range []string{workDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	mockProv := &mocks.MockProvider{
		ProviderName: "mock",
		Models:       []string{"test-model"},
		CLIDetected:  true,
		CommandArgs:  []string{"cat"},
		Protocol:     mocks.NewMockProtocol(messages...),
	}

	registry := llm.NewRegistry()
	registry.Register(mockProv)

	eventCh := make(chan interface{}, 100)
	sm := session.NewManager(eventCh)

	pr := &PhaseRunner{
		SessionManager: sm,
		StateDir:       stateDir,
		Registry:       registry,
	}

	return pr, func() {
		sm.Shutdown()
	}
}

func askUserControlRequest(requestID string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "control_request",
		ControlRequest: &llm.ControlRequestMessage{
			Type:      "control_request",
			RequestID: requestID,
			Request: llm.ControlRequest{
				Subtype:  "can_use_tool",
				ToolName: "AskUserQuestion",
			},
		},
	}
}
