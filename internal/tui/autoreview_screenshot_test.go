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
// generates the two required 1440x900 visual-evidence screenshots by
// rendering the real workspace config editor and capturing via headless
// Chrome. The ANSI-to-HTML renderer and Chrome launcher live in
// screenshot_helpers_test.go so they can be reused by future visual-evidence
// tests without another bespoke terminal renderer per feature.
//
// Run with:
//
//	go test ./internal/tui/ -tags=autoreview_screenshots -run TestGenerateAutoReviewScreenshots -v
package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestGenerateAutoReviewScreenshots(t *testing.T) {
	outDir := os.Getenv("AUTOREVIEW_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = "."
	}

	cat := PhaseModelCatalog{
		Fields:         append([]string(nil), globalModelFields...),
		ProviderModels: map[string][]string{"claude": {"claude/haiku[200K]", "claude/sonnet-4-6", "claude/opus-4-7"}},
		ProviderModelInfos: map[string][]llm.ModelInfo{"claude": {
			{ID: "claude/haiku[200K]", DisplayName: "Claude Haiku", ContextWindow: 200000, Category: "cheap", Aliases: []string{"haiku"}},
			{ID: "claude/sonnet-4-6", DisplayName: "Claude Sonnet 4.6", ContextWindow: 200000, Category: "balanced"},
			{ID: "claude/opus-4-7", DisplayName: "Claude Opus 4.7", ContextWindow: 200000, Category: "capable"},
		}},
		ProviderOrder:       []string{"claude"},
		PhaseDefaults:       map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{},
	}
	cfg := &config.Config{Defaults: config.DefaultsConfig{
		Models:                 config.ModelConfig{AutomaticReview: ""},
		AutomaticReviewEnabled: false,
		Inquireness:            "high",
		Pipeline:               "large",
	}}

	// Behavior tab: focus the Automatic Review setting so the Details panel
	// shows it as "off" (disabled), with the always-visible scope hint.
	behaviorM := NewWorkspaceEditConfigModel(cfg, cat)
	behaviorM.activeTab = tabBehavior
	behaviorM.focus = configFocusBody
	behaviorM.behaviorCursor = len(behaviorM.behaviorSettings()) - 1
	behaviorText := behaviorM.View()
	behaviorPath := filepath.Join(outDir, "screenshots", "workspace-behavior-tab-with-automatic-review-disabled-and-the-always-visible-new-1440x900.png")
	if err := renderScreenshot(behaviorText, behaviorPath); err != nil {
		t.Fatalf("behavior screenshot: %v", err)
	}
	t.Logf("wrote %s", behaviorPath)

	// Models tab: focus the Automatic Review row with its Claude-only picker
	// open, set to Automatic, while disabled.
	modelsM := NewWorkspaceEditConfigModel(cfg, cat)
	modelsM.activeTab = tabModels
	modelsM.focus = configFocusModelList
	modelsM.editor.rowCursor = modelsM.editor.modelsCount() - 1
	modelsM.editor.activeModelCell = modelCellModel
	modelsText := modelsM.View()
	modelsPath := filepath.Join(outDir, "screenshots", "workspace-models-tab-with-automatic-review-set-to-automatic-its-claude-only-pick-1440x900.png")
	if err := renderScreenshot(modelsText, modelsPath); err != nil {
		t.Fatalf("models screenshot: %v", err)
	}
	t.Logf("wrote %s", modelsPath)
}
