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
	"encoding/json"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

func TestClaudeCatalogHasEffortCapabilities(t *testing.T) {
	p := &claude.Provider{}
	catalog := p.ModelCatalog()
	if len(catalog) == 0 {
		t.Fatal("expected non-empty Claude catalog")
	}
	for _, m := range catalog {
		if len(m.EffortCapabilities) == 0 {
			t.Errorf("model %s: expected non-empty EffortCapabilities", m.ID)
		}
		want := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}
		if !equalEffortLevels(m.EffortCapabilities, want) {
			t.Errorf("model %s: got %v, want %v", m.ID, m.EffortCapabilities, want)
		}
	}
}

func TestCodexCatalogHasEffortCapabilities(t *testing.T) {
	p := &codex.Provider{}
	catalog := p.ModelCatalog()
	if len(catalog) == 0 {
		t.Fatal("expected non-empty Codex catalog")
	}
	for _, m := range catalog {
		if len(m.EffortCapabilities) == 0 {
			t.Errorf("model %s: expected non-empty EffortCapabilities", m.ID)
		}
		want := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}
		if llm.StripModelContextWindow(m.ID) == "gpt-6-astra" {
			want = append(want, llm.EffortUltra)
		}
		if !equalEffortLevels(m.EffortCapabilities, want) {
			t.Errorf("model %s: got %v, want %v", m.ID, m.EffortCapabilities, want)
		}
	}
}

func TestOpenCodeFallbackCatalogEffortCapabilities(t *testing.T) {
	p := opencode.New()
	catalog := p.ModelCatalog()
	if len(catalog) == 0 {
		t.Fatal("expected non-empty OpenCode fallback catalog")
	}
	for _, m := range catalog {
		backendID := llm.StripModelContextWindow(m.ID)
		provider, _, ok := splitBackend(backendID)
		if !ok {
			continue
		}
		if provider == "openai" {
			want := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
			if !equalEffortLevels(m.EffortCapabilities, want) {
				t.Errorf("openai model %s: got %v, want %v (max collapsed to high)", m.ID, m.EffortCapabilities, want)
			}
		} else {
			if len(m.EffortCapabilities) != 0 {
				t.Errorf("non-openai model %s: expected empty EffortCapabilities (Auto-only), got %v", m.ID, m.EffortCapabilities)
			}
		}
	}
}

func TestOpenCodeMaxCollapsedToHigh(t *testing.T) {
	p := opencode.New()
	catalog := p.ModelCatalog()
	found := false
	for _, m := range catalog {
		backendID := llm.StripModelContextWindow(m.ID)
		provider, _, ok := splitBackend(backendID)
		if ok && provider == "openai" {
			found = true
			for _, cap := range m.EffortCapabilities {
				if cap == llm.EffortMax {
					t.Errorf("openai model %s: EffortMax should be collapsed (executes identically to high), but was advertised", m.ID)
				}
			}
		}
	}
	if !found {
		t.Fatal("expected at least one openai model in fallback catalog")
	}
}

func TestEffortCapabilitiesForModelViaRegistry(t *testing.T) {
	reg := llm.NewRegistry()
	claudeProv := &claude.Provider{}
	codexProv := &codex.Provider{}
	opencodeProv := opencode.New()
	reg.Register(claudeProv)
	reg.Register(codexProv)
	reg.Register(opencodeProv)

	caps := llm.EffortCapabilitiesForModel(claudeProv, "sonnet[200K]")
	if len(caps) != 5 {
		t.Errorf("claude sonnet[200K]: expected 5 capabilities, got %d", len(caps))
	}

	caps = llm.EffortCapabilitiesForModel(codexProv, "gpt-5.4[272K]")
	if len(caps) != 5 {
		t.Errorf("codex gpt-5.4[272K]: expected 5 capabilities, got %d", len(caps))
	}

	caps = llm.EffortCapabilitiesForModel(opencodeProv, "openai/gpt-5[400K]")
	if len(caps) != 3 {
		t.Errorf("opencode openai/gpt-5[400K]: expected 3 capabilities (max collapsed), got %d", len(caps))
	}

	caps = llm.EffortCapabilitiesForModel(opencodeProv, "anthropic/claude-sonnet-4-5[200K]")
	if len(caps) != 0 {
		t.Errorf("opencode anthropic model: expected 0 capabilities (Auto-only), got %d", len(caps))
	}

	caps = llm.EffortCapabilitiesForModel(claudeProv, "nonexistent-model")
	if len(caps) != 0 {
		t.Errorf("unknown model: expected 0 capabilities, got %d", len(caps))
	}

	caps = llm.EffortCapabilitiesForModel(nil, "sonnet")
	if len(caps) != 0 {
		t.Errorf("nil provider: expected 0 capabilities, got %d", len(caps))
	}
}

func TestEffortCapabilitiesForModelViaAlias(t *testing.T) {
	p := &codex.Provider{}
	caps := llm.EffortCapabilitiesForModel(p, "gpt-5.4")
	if len(caps) != 5 {
		t.Errorf("alias gpt-5.4: expected 5 capabilities, got %d", len(caps))
	}
}

func TestEffortCapabilitySupported(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	if !llm.EffortCapabilitySupported(caps, llm.EffortHigh) {
		t.Error("expected EffortHigh to be supported")
	}
	if llm.EffortCapabilitySupported(caps, llm.EffortMax) {
		t.Error("expected EffortMax to NOT be supported")
	}
	if llm.EffortCapabilitySupported(nil, llm.EffortLow) {
		t.Error("expected nil capabilities to support nothing")
	}
}

func TestIsValidExplicitEffort(t *testing.T) {
	valid := []llm.EffortLevel{llm.EffortAuto, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax, llm.EffortUltra}
	for _, level := range valid {
		if !llm.IsValidExplicitEffort(level) {
			t.Errorf("expected %q to be valid", level)
		}
	}
	invalid := []llm.EffortLevel{"", "invalid", "extreme"}
	for _, level := range invalid {
		if llm.IsValidExplicitEffort(level) {
			t.Errorf("expected %q to be invalid", level)
		}
	}
}

func TestEffortCapabilitiesPreservedThroughCacheRoundtrip(t *testing.T) {
	models := []llm.ModelInfo{
		{ID: "test-model", DisplayName: "Test", ContextWindow: 200_000, Category: "balanced",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortHigh}},
	}
	data, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []llm.ModelInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 model, got %d", len(decoded))
	}
	if !equalEffortLevels(decoded[0].EffortCapabilities, []llm.EffortLevel{llm.EffortLow, llm.EffortHigh}) {
		t.Errorf("capabilities not preserved through JSON roundtrip: got %v", decoded[0].EffortCapabilities)
	}
}

func equalEffortLevels(a, b []llm.EffortLevel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func splitBackend(backendID string) (string, string, bool) {
	for i, r := range backendID {
		if r == '/' {
			return backendID[:i], backendID[i+1:], true
		}
	}
	return "", "", false
}
