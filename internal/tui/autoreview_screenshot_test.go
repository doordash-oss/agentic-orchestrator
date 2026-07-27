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

//go:build autoreview_screenshots

// This file is excluded from normal builds and tests by the build tag. It
// generates automatic-review visual evidence by rendering real TUI surfaces
// and capturing them via headless Chrome. The ANSI-to-HTML renderer and Chrome
// launcher live in screenshot_helpers_test.go so they can be reused by future
// visual-evidence tests without another bespoke terminal renderer per feature.
//
// Run with:
//
//	go test ./internal/tui/ -tags=autoreview_screenshots -run 'TestGenerate(AutoReview|AutomaticReviewTransparency)Screenshots' -v
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

func TestGenerateAutomaticReviewTransparencyScreenshots(t *testing.T) {
	outDir := os.Getenv("AUTOREVIEW_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = "."
	}

	const (
		command    = "go test ./internal/permission -run TestAutomaticReviewStatusLineSanitizesAndBoundsCommand -count=1"
		statusText = "Auto-approved Bash: " + command
	)
	messages := []llm.SDKMessage{
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{{Type: "text", Text: "I’ll run the focused sanitizer regression test, then continue with the surrounding suite."}},
			}},
		},
		{Type: "status", Status: &llm.StatusMessage{Type: "status", Message: statusText}},
		{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
				Content: []llm.ContentBlock{{
					Type:  "tool_use",
					ID:    "toolu_auto_review",
					Name:  "Bash",
					Input: json.RawMessage(`{"command":"` + command + `"}`),
				}},
			}},
		},
	}

	liveFeature := &feature.Feature{
		ID:               "c20e8f6293287450",
		Slug:             "automatic-permission-review",
		Status:           feature.StatusImplementing,
		CurrentPhase:     feature.PhaseImplement,
		CurrentIteration: 1,
		Repos:            []feature.FeatureRepo{{Name: "agentic-orchestrator"}},
	}
	liveSession := newLivePreviewSessionWithIteration(
		"session-transparent-live",
		feature.PhaseImplement,
		1,
		messages...,
	)
	liveView := newLivePreviewModel(liveFeature).
		withSession(liveSession).
		withHeight(38).
		ViewCompact(108)
	livePath := filepath.Join(
		outDir,
		"screenshots",
		"live-preview-showing-a-redacted-automatic-bash-approval-status-immediately-befor-1440x900.png",
	)
	if err := renderScreenshot(liveView, livePath); err != nil {
		t.Fatalf("live-preview screenshot: %v", err)
	}
	t.Logf("wrote %s", livePath)

	completedSession := session.NewSession("session-transparent-completed", liveFeature.ID, feature.PhaseImplement)
	completedSession.SetStatus(session.SessionDone)
	completedSession.SetIteration(1)
	for _, message := range messages {
		completedSession.MessageLog().Append(message)
	}
	completedSession.MessageLog().Append(llm.SDKMessage{
		Type: "user",
		User: &llm.UserMessage{Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{
				Type:      "tool_result",
				ToolUseID: "toolu_auto_review",
				Content:   json.RawMessage(`"ok  github.com/doordash-oss/agentic-orchestrator/internal/permission  0.142s"`),
			}},
		}},
	})
	completedSession.MessageLog().Append(llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
			Content: []llm.ContentBlock{{Type: "text", Text: "The focused test passed. The same approval status remains in completed-session history."}},
		}},
	})
	completedSession.MessageLog().Append(llm.SDKMessage{
		Type:   "result",
		Result: &llm.ResultMessage{Subtype: "success"},
	})

	attach := testAttachModel(completedSession, 108, 40, nil, 0)
	attach.readOnly = true
	attach.updateViewport()
	attachView := attach.View()
	attachPath := filepath.Join(
		outDir,
		"screenshots",
		"completed-session-attach-history-showing-the-same-durable-automatic-bash-approva-1440x900.png",
	)
	if err := renderScreenshot(attachView, attachPath); err != nil {
		t.Fatalf("completed attach screenshot: %v", err)
	}
	t.Logf("wrote %s", attachPath)
}

func TestGenerateAutoReviewScreenshots(t *testing.T) {
	outDir := os.Getenv("AUTOREVIEW_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = "."
	}

	cat := PhaseModelCatalog{
		Fields: append([]string(nil), globalModelFields...),
		ProviderModels: map[string][]string{
			"claude":   {"claude/haiku[200K]", "claude/sonnet-4-6"},
			"opencode": {"anthropic/claude-haiku", "google/gemini-2.5-flash"},
			"codex":    {"gpt-5.4-mini", "gpt-5.4"},
		},
		ProviderModelInfos: map[string][]llm.ModelInfo{
			"claude": {
				{ID: "claude/haiku[200K]", DisplayName: "Claude Haiku", ContextWindow: 200000, Category: "cheap", Aliases: []string{"haiku"}},
				{ID: "claude/sonnet-4-6", DisplayName: "Claude Sonnet 4.6", ContextWindow: 200000, Category: "balanced"},
			},
			"opencode": {
				{ID: "anthropic/claude-haiku", DisplayName: "Claude Haiku via OpenCode", ContextWindow: 200000, Category: "cheap"},
				{ID: "google/gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", ContextWindow: 1000000, Category: "cheap"},
			},
			"codex": {
				{ID: "gpt-5.4-mini", DisplayName: "GPT-5.4 mini", ContextWindow: 272000, Category: "cheap"},
				{ID: "gpt-5.4", DisplayName: "GPT-5.4", ContextWindow: 272000, Category: "capable"},
			},
		},
		ProviderOrder: []string{"claude", "opencode", "codex"},
		PhaseDefaults: map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{
			automaticReviewField: {
				"claude":   {"claude/haiku[200K]", "claude/sonnet-4-6"},
				"opencode": {"anthropic/claude-haiku", "google/gemini-2.5-flash"},
				"codex":    {"gpt-5.4-mini", "gpt-5.4"},
			},
		},
	}
	cfg := &config.Config{Defaults: config.DefaultsConfig{
		Models:                 config.ModelConfig{AutomaticReview: ""},
		AutomaticReviewEnabled: false,
		Inquireness:            "high",
		Pipeline:               "large",
	}}

	// Automatic mode remains editable while disabled and exposes all eligible
	// provider groups in the dedicated reviewer role.
	modelsM := NewWorkspaceEditConfigModel(cfg, cat)
	modelsM.activeTab = tabModels
	modelsM.focus = configFocusAgentList
	modelsM.editor.rowCursor = modelsM.editor.modelsCount() - 1
	modelsText := modelsM.View()
	modelsPath := filepath.Join(outDir, "screenshots", "workspace-models-tab-with-automatic-review-set-to-automatic-and-the-provider-sel-1440x900.png")
	if err := renderScreenshot(modelsText, modelsPath); err != nil {
		t.Fatalf("models screenshot: %v", err)
	}
	t.Logf("wrote %s", modelsPath)

	// Explicit OpenCode mode shows its provider-local catalog while retaining
	// the always-visible new-session lifecycle hint.
	openCodeCfg := *cfg
	openCodeCfg.Defaults.Models.AutomaticReview = "opencode:anthropic/claude-haiku"
	openCodeM := NewWorkspaceEditConfigModel(&openCodeCfg, cat)
	openCodeM.activeTab = tabModels
	openCodeM.focus = configFocusModelList
	openCodeM.editor.rowCursor = openCodeM.editor.modelsCount() - 1
	openCodeM.editor.activeModelCell = modelCellModel
	openCodeText := openCodeM.View()
	openCodePath := filepath.Join(outDir, "screenshots", "workspace-models-tab-with-an-explicit-opencode-reviewer-selected-its-provider-lo-1440x900.png")
	if err := renderScreenshot(openCodeText, openCodePath); err != nil {
		t.Fatalf("OpenCode models screenshot: %v", err)
	}
	t.Logf("wrote %s", openCodePath)
}
