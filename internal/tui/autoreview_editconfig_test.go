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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func autoreviewWorkspaceCatalog() PhaseModelCatalog {
	return PhaseModelCatalog{
		Fields: append([]string(nil), globalModelFields...),
		ProviderModels: map[string][]string{
			"claude":   {"claude/haiku[200K]", "claude/sonnet-4-6"},
			"opencode": {"anthropic/claude-haiku", "google/gemini-pro"},
			"codex":    {"gpt-5.4-mini", "gpt-5.4"},
		},
		ProviderOrder: []string{"claude", "opencode", "codex"},
		PhaseDefaults: map[string]string{},
		PhaseProviderModels: map[string]map[string][]string{
			automaticReviewField: {
				"claude":   {"claude/haiku[200K]", "claude/sonnet-4-6"},
				"opencode": {"anthropic/claude-haiku", "google/gemini-pro"},
				"codex":    {"gpt-5.4-mini", "gpt-5.4"},
			},
		},
	}
}

func autoreviewWorkspaceModel(t *testing.T, enabled bool, model string) EditConfigModel {
	t.Helper()
	cfg := &config.Config{Defaults: config.DefaultsConfig{
		Models:                 config.ModelConfig{AutomaticReview: model},
		AutomaticReviewEnabled: enabled,
		Inquireness:            "high",
		Pipeline:               "large",
	}}
	m := NewWorkspaceEditConfigModel(cfg, autoreviewWorkspaceCatalog())
	return m
}

func TestWorkspaceEditorShowsAutomaticReviewToggle(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabBehavior
	view := m.renderBehaviorPane()
	if !strings.Contains(view, "Automatic Review") {
		t.Errorf("workspace Behavior pane missing Automatic Review toggle:\n%s", view)
	}
}

func TestWorkspaceEditorShowsNewSessionScopeHint(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	view := m.View()
	if !strings.Contains(view, "Applies to new sessions") {
		t.Errorf("workspace editor missing 'Applies to new sessions' hint:\n%s", view)
	}
}

func TestWorkspaceModelsTabShowsAutomaticReviewAutomatic(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabModels
	m.focus = configFocusPhaseList
	// Cursor on the Automatic Review row (last field).
	m.editor.rowCursor = m.editor.modelsCount() - 1
	view := m.renderModelsWorkspace()
	if !strings.Contains(view, "Automatic Review (Automatic)") {
		t.Errorf("workspace Models tab missing 'Automatic Review (Automatic)':\n%s", view)
	}
}

func TestWorkspaceModelsTabAutomaticReviewRowWorkspaceOnly(t *testing.T) {
	// The Automatic Review row appears only in the workspace catalog, never in
	// the feature-scoped catalog (phaseCatalogFields).
	if containsString(phaseCatalogFields, automaticReviewField) {
		t.Errorf("phaseCatalogFields must not include %q", automaticReviewField)
	}
	if !containsString(globalModelFields, automaticReviewField) {
		t.Errorf("globalModelFields must include %q", automaticReviewField)
	}
}

func TestFeatureEditorDoesNotShowAutomaticReview(t *testing.T) {
	f := &feature.Feature{Name: "My Feature", Pipeline: feature.PipelineLarge}
	m := NewEditConfigModel(f, testCatalog(), true)
	view := m.View()
	if strings.Contains(view, "Automatic Review") {
		t.Errorf("feature-scoped editor must not show Automatic Review:\n%s", view)
	}
	if strings.Contains(view, "Applies to new sessions") {
		t.Errorf("feature-scoped editor must not show workspace scope hint:\n%s", view)
	}
}

func TestWorkspaceAutomaticReviewToggleCycles(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabBehavior
	m.focus = configFocusBody
	// Cursor on the Automatic Review setting (last behavior setting).
	m.behaviorCursor = len(m.behaviorSettings()) - 1
	if m.selectedBehaviorSetting() != behaviorSettingAutomaticReview {
		t.Fatalf("expected Automatic Review selected, got %v", m.selectedBehaviorSetting())
	}
	if m.automaticReviewEnabled {
		t.Fatalf("expected disabled initially")
	}
	m.cycleSelectedBehaviorValue(+1)
	if !m.automaticReviewEnabled {
		t.Errorf("toggle should enable Automatic Review")
	}
	if !m.HasChanges() {
		t.Errorf("HasChanges should be true after toggling Automatic Review")
	}
}

func TestWorkspaceAutomaticReviewModelSelectionCyclesProvidersAndAutomatic(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabModels
	m.focus = configFocusPhaseList
	m.editor.rowCursor = m.editor.modelsCount() - 1
	// Cycle forward from "Automatic" to the first Claude model.
	m.editor.cycleModelForward()
	if got := m.editor.models.AutomaticReview; got != "claude:claude/haiku[200K]" {
		t.Fatalf("first explicit reviewer = %q, want provider-qualified Claude model", got)
	}
	m.editor.cycleAgent(+1)
	if got := m.editor.models.AutomaticReview; got != "opencode:anthropic/claude-haiku" {
		t.Fatalf("OpenCode reviewer = %q, want provider-qualified canonical model", got)
	}
	m.editor.cycleAgent(+1)
	if got := m.editor.models.AutomaticReview; got != "codex:gpt-5.4-mini" {
		t.Fatalf("Codex reviewer = %q, want provider-qualified canonical model", got)
	}
	m.editor.cycleModelBackward()
	if got := m.editor.models.AutomaticReview; got != "" {
		t.Errorf("backward from first provider model = %q, want Automatic empty sentinel", got)
	}
}

func TestWorkspaceAutomaticReviewModelRowPermitsSelectionWhileDisabled(t *testing.T) {
	// The toggle is disabled but the model row must still be selectable.
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabModels
	m.focus = configFocusPhaseList
	m.editor.rowCursor = m.editor.modelsCount() - 1
	before := m.editor.models.AutomaticReview
	m.editor.cycleModelForward()
	if m.editor.models.AutomaticReview == before {
		t.Errorf("model selection should change even while toggle disabled")
	}
}

func TestWorkspaceAutomaticReviewProviderGroupsAreVisible(t *testing.T) {
	m := autoreviewWorkspaceModel(t, false, "")
	m.activeTab = tabModels
	m.focus = configFocusAgentList
	m.editor.rowCursor = m.editor.modelsCount() - 1
	view := m.renderModelsWorkspace()
	if !strings.Contains(view, "Providers") {
		t.Errorf("automatic-review selector must label Claude, OpenCode, and Codex as providers:\n%s", view)
	}
	if strings.Contains(view, "Models for claude") {
		t.Errorf("automatic reviewer must not appear scoped to the highlighted Claude provider:\n%s", view)
	}
	if !strings.Contains(view, "Automatic tries Claude → OpenCode → Codex") {
		t.Errorf("automatic reviewer must explain its cross-provider fallback order:\n%s", view)
	}
	for _, provider := range []string{"claude", "opencode", "codex"} {
		if !strings.Contains(view, provider) {
			t.Errorf("automatic-review provider picker missing %q:\n%s", provider, view)
		}
	}
}

func TestWorkspaceExplicitAutomaticReviewShowsProviderLocalModels(t *testing.T) {
	m := autoreviewWorkspaceModel(t, true, "opencode:anthropic/claude-haiku")
	m.activeTab = tabModels
	m.focus = configFocusModelList
	m.editor.rowCursor = m.editor.modelsCount() - 1

	view := m.renderModelsWorkspace()
	if !strings.Contains(view, "Providers") {
		t.Errorf("explicit reviewer selector must use provider terminology:\n%s", view)
	}
	if !strings.Contains(view, "Models for opencode") {
		t.Errorf("explicit reviewer must retain its provider-local model title:\n%s", view)
	}
	if strings.Contains(view, "Automatic tries Claude → OpenCode → Codex") {
		t.Errorf("explicit reviewer must not show automatic fallback copy:\n%s", view)
	}
	if strings.Contains(view, "opencode/opencode /") {
		t.Errorf("explicit reviewer summary must not repeat its provider identity:\n%s", view)
	}
}

func TestWorkspaceAutomaticReviewStaleValueRemainsVisibleAndUnchanged(t *testing.T) {
	const stale = "opencode:retired/reviewer"
	m := autoreviewWorkspaceModel(t, true, stale)
	m.activeTab = tabModels
	m.focus = configFocusPhaseList
	m.editor.rowCursor = m.editor.modelsCount() - 1

	view := m.renderModelsWorkspace()
	if !strings.Contains(view, "opencode/retired/reviewer (unavailable)") {
		t.Fatalf("stale reviewer is not visible as unavailable:\n%s", view)
	}
	if got := m.editor.Snapshot().Models.AutomaticReview; got != stale {
		t.Fatalf("stale reviewer changed during read/render = %q, want %q", got, stale)
	}

	m.editor.cycleModelForward()
	if got := m.editor.models.AutomaticReview; got != "" {
		t.Fatalf("editing stale reviewer = %q, want first eligible choice Automatic", got)
	}
}

func containsString(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
