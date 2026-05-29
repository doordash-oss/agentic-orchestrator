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

package llm_test

import (
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
)

func TestClaudeProvider_Name(t *testing.T) {
	p := &claude.Provider{}
	if p.Name() != "claude" {
		t.Errorf("got %q, want %q", p.Name(), "claude")
	}
}

func TestCodexProvider_Name(t *testing.T) {
	p := &codex.Provider{}
	if p.Name() != "codex" {
		t.Errorf("got %q, want %q", p.Name(), "codex")
	}
}

func TestRecommendationConfidenceClause_RequiresRecommendedHighestConfidence(t *testing.T) {
	if !strings.Contains(llm.RecommendationConfidenceClause, "single highest-confidence option") {
		t.Fatalf("RecommendationConfidenceClause must require the recommended option to be the highest-confidence option:\n%s", llm.RecommendationConfidenceClause)
	}
}

func TestProviderAskingQuestionsClausesIncludeRecommendationConfidenceContract(t *testing.T) {
	providers := map[string]string{
		"claude": (&claude.Provider{}).AskingQuestionsClause(),
		"codex":  (&codex.Provider{}).AskingQuestionsClause(),
	}
	for name, clause := range providers {
		if !strings.Contains(clause, "single highest-confidence option") {
			t.Fatalf("%s AskingQuestionsClause missing highest-confidence recommendation contract:\n%s", name, clause)
		}
	}
}

func TestRegistrySmartZoneThresholdTokens_DefaultCatalog(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&claude.Provider{})
	r.Register(&codex.Provider{})

	tests := []struct {
		name     string
		provider string
		model    string
		want     int
	}{
		{"claude_opus", "claude", "opus", 100_000},
		{"claude_opus_1m", "claude", "opus[1m]", 100_000},
		{"claude_sonnet", "claude", "sonnet", 80_000},
		{"claude_sonnet_1m", "claude", "sonnet[1m]", 80_000},
		{"claude_haiku", "claude", "haiku", 40_000},
		{"codex_gpt_5_5", "codex", "gpt-5.5", 100_000},
		{"codex_gpt_5_4", "codex", "gpt-5.4", 90_000},
		{"codex_gpt_5_4_mini", "codex", "gpt-5.4-mini", 50_000},
		{"codex_gpt_5_3_codex", "codex", "gpt-5.3-codex", 80_000},
		{"codex_alias", "codex", "codex", 80_000},
		{"codex_gpt_5_2", "codex", "gpt-5.2", 80_000},
		{"unknown_model", "codex", "unknown-model", 40_000},
		{"unknown_provider", "unknown", "whatever", 40_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.SmartZoneThresholdTokens(tt.provider, tt.model, config.SmartZoneConfig{})
			if got != tt.want {
				t.Errorf("SmartZoneThresholdTokens(%q, %q) = %d, want %d", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestRegistrySmartZoneThresholdTokens_OverridesCanonicalizedAliases(t *testing.T) {
	r := llm.NewRegistry()
	claudeProvider := &claude.Provider{}
	claudeProvider.SetModelCatalog([]llm.ModelInfo{
		{
			ID:              "opus[1m]",
			Aliases:         []string{"claude-opus-4-8[1m]"},
			SmartZoneTokens: 100_000,
		},
	})
	r.Register(claudeProvider)
	r.Register(&codex.Provider{})

	smartZone := config.SmartZoneConfig{
		Thresholds: map[string]map[string]int{
			"claude": {
				"claude-opus-4-8[1m]": 123_456,
			},
			"codex": {
				"codex": 77_777,
			},
		},
	}

	if got := r.SmartZoneThresholdTokens("claude", "opus[1m]", smartZone); got != 123_456 {
		t.Errorf("SmartZoneThresholdTokens() alias override = %d, want 123456", got)
	}
	if got := r.SmartZoneThresholdTokens("codex", "gpt-5.3-codex", smartZone); got != 77_777 {
		t.Errorf("SmartZoneThresholdTokens() codex alias override = %d, want 77777", got)
	}
	if got := r.SmartZoneThresholdTokens("codex", "gpt-5.4", smartZone); got != 90_000 {
		t.Errorf("SmartZoneThresholdTokens() catalog fallback = %d, want 90000", got)
	}
}
