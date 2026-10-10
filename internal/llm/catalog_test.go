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
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/claude"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

// catalogSurface is the catalog-facing slice of the provider interface the
// shared-catalog behavior is pinned against, at the provider interface.
type catalogSurface interface {
	llm.LLMProvider
}

// defaultCatalogProviders returns the two hardcoded-defaults providers,
// constructed through their constructors (the embedded catalog is bound at
// construction).
func defaultCatalogProviders() map[string]catalogSurface {
	return map[string]catalogSurface{
		"claude": claude.NewProvider(""),
		"codex":  codex.NewProvider(""),
	}
}

func TestProviderCatalogMatchesModelAgainstDefaults(t *testing.T) {
	matchCases := map[string][]string{
		"claude": {
			// By canonical id.
			"sonnet[200K]", "fable[1M]", "haiku[200K]",
			// By alias.
			"sonnet", "opus", "haiku", "opus[1m]",
			// Case-insensitive id and alias.
			"SONNET[200K]", "Opus[1M]", "HAIKU",
		},
		"codex": {
			"gpt-5.4[272K]", "gpt-6-astra[872K]",
			"gpt-5.4", "gpt-6-astra", "gpt-5.3-codex",
			"GPT-5.4", "GPT-6-ASTRA[272K]",
		},
	}
	noMatchCases := map[string][]string{
		"claude": {
			// Other providers' names are never captured.
			"gpt-5.4", "gpt-6-astra",
			"unknown-model",
			// Matching is not trimmed: surrounding whitespace is a mismatch.
			" sonnet", "sonnet ", " opus",
		},
		"codex": {
			"sonnet", "opus", "haiku", "unknown-model", " sonnet", "gpt-5.4 ",
		},
	}
	for name, p := range defaultCatalogProviders() {
		for _, m := range matchCases[name] {
			if !p.MatchesModel(m) {
				t.Errorf("%s.MatchesModel(%q) = false, want true against the hardcoded defaults", name, m)
			}
		}
		for _, m := range noMatchCases[name] {
			if p.MatchesModel(m) {
				t.Errorf("%s.MatchesModel(%q) = true, want false", name, m)
			}
		}
	}
}

func TestProviderCatalogContextWindowsAgainstDefaults(t *testing.T) {
	wantWindows := map[string]map[string]int{
		"claude": {
			"fable":        1_000_000,
			"fable[1M]":    1_000_000,
			"opus":         200_000,
			"opus[200K]":   200_000,
			"opus[1m]":     1_000_000,
			"opus[1M]":     1_000_000,
			"sonnet":       200_000,
			"sonnet[1m]":   1_000_000,
			"sonnet[1M]":   1_000_000,
			"sonnet[200K]": 200_000,
			"haiku[200K]":  200_000,
			"haiku":        200_000,
			"SONNET":       200_000,
			"unknown":      0,
			" sonnet":      0,
		},
		"codex": {
			"gpt-5.5":             272_000,
			"gpt-5.5[272K]":       272_000,
			"gpt-5.4":             272_000,
			"gpt-5.4[272K]":       272_000,
			"gpt-5.4[1M]":         1_000_000,
			"gpt-5.4-mini":        400_000,
			"gpt-5.4-mini[400K]":  400_000,
			"gpt-5.3-codex":       400_000,
			"gpt-5.3-codex[400K]": 400_000,
			"gpt-5.2":             400_000,
			"gpt-5.2[400K]":       400_000,
			"gpt-6-astra":         272_000,
			"gpt-6-astra[272K]":   272_000,
			"gpt-6-astra[872K]":   872_000,
			"GPT-5.4":             272_000,
			"unknown":             0,
		},
	}
	for name, p := range defaultCatalogProviders() {
		for model, want := range wantWindows[name] {
			if got := p.ContextWindowForModel(model); got != want {
				t.Errorf("%s.ContextWindowForModel(%q) = %d, want %d", name, model, got, want)
			}
		}
	}
}

func TestProviderCatalogAvailableModelsAgainstDefaults(t *testing.T) {
	wantFirst := map[string]string{
		"claude": "fable[1M]",
		"codex":  "gpt-6-astra[272K]",
	}
	for name, p := range defaultCatalogProviders() {
		got := p.AvailableModels()
		if len(got) == 0 {
			t.Fatalf("%s.AvailableModels() empty, want hardcoded defaults", name)
		}
		if got[0] != wantFirst[name] {
			t.Errorf("%s.AvailableModels()[0] = %q, want %q (catalog order)", name, got[0], wantFirst[name])
		}
	}
}

func TestProviderCatalogSetThenReadRoundTrip(t *testing.T) {
	custom := []llm.ModelInfo{
		{ID: "custom/capable[1M]", DisplayName: "Custom (1M)", ContextWindow: 1_000_000, Aliases: []string{"custom/capable"}, Category: "capable"},
		{ID: "custom/balanced[200K]", ContextWindow: 200_000, Aliases: []string{"custom/balanced"}, Category: "balanced"},
	}
	for name, p := range defaultCatalogProviders() {
		p.SetModelCatalog(custom)
		if got := p.AvailableModels(); !slices.Equal(got, []string{"custom/capable[1M]", "custom/balanced[200K]"}) {
			t.Errorf("%s.AvailableModels() = %v, want the set catalog IDs", name, got)
		}
		for _, m := range []string{"custom/capable[1M]", "custom/capable", "CUSTOM/BALANCED[200K]"} {
			if !p.MatchesModel(m) {
				t.Errorf("%s.MatchesModel(%q) = false after SetModelCatalog", name, m)
			}
		}
		// A stored catalog replaces the defaults entirely.
		if p.MatchesModel("sonnet") || p.MatchesModel("gpt-5.4") {
			t.Errorf("%s still matches its default model after SetModelCatalog", name)
		}
		if got := p.ContextWindowForModel("custom/capable"); got != 1_000_000 {
			t.Errorf("%s.ContextWindowForModel(custom/capable) = %d, want 1000000", name, got)
		}
		if got := p.ModelCatalog(); len(got) != 2 || got[0].ID != "custom/capable[1M]" || got[0].ContextWindow != 1_000_000 {
			t.Errorf("%s.ModelCatalog() = %+v, want the set entries", name, got)
		}

		// Set stores a deep copy: mutating the input afterwards cannot leak in.
		custom[0].ID = "mutated-input"
		custom[0].ContextWindow = -1
		if got := p.ModelCatalog(); got[0].ID != "custom/capable[1M]" || got[0].ContextWindow != 1_000_000 {
			t.Errorf("%s.ModelCatalog() leaked input mutations: %+v", name, got[0])
		}
		custom[0].ID, custom[0].ContextWindow = "custom/capable[1M]", 1_000_000

		// Read returns a deep copy: mutating the returned slice cannot leak in.
		got := p.ModelCatalog()
		got[0].ID = "mutated-read"
		got[1].Aliases[0] = "mutated-read"
		if again := p.ModelCatalog(); again[0].ID != "custom/capable[1M]" || again[1].Aliases[0] != "custom/balanced" {
			t.Errorf("%s.ModelCatalog() leaked read mutations: %+v", name, again)
		}
	}
}

func TestProviderCatalogNilSetRestoresDefaults(t *testing.T) {
	for name, p := range defaultCatalogProviders() {
		p.SetModelCatalog([]llm.ModelInfo{{ID: "custom/only[100K]", ContextWindow: 100_000}})
		if !p.MatchesModel("custom/only[100K]") {
			t.Fatalf("%s.MatchesModel(custom/only[100K]) = false after set", name)
		}
		p.SetModelCatalog(nil)
		if name == "claude" && !p.MatchesModel("sonnet") {
			t.Error("claude.MatchesModel(sonnet) = false after nil set, want defaults restored")
		}
		if name == "codex" && !p.MatchesModel("gpt-5.4") {
			t.Error("codex.MatchesModel(gpt-5.4) = false after nil set, want defaults restored")
		}
		if got := p.ModelCatalog(); len(got) == 0 {
			t.Errorf("%s.ModelCatalog() empty after nil set, want defaults restored", name)
		}
	}
}

func TestProviderCatalogContextWindowSkipsZeroWindowEntries(t *testing.T) {
	for name, p := range defaultCatalogProviders() {
		p.SetModelCatalog([]llm.ModelInfo{
			{ID: "dup", ContextWindow: 0},
			{ID: "holder", Aliases: []string{"dup"}, ContextWindow: 12345},
		})
		if got := p.ContextWindowForModel("dup"); got != 12345 {
			t.Errorf("%s.ContextWindowForModel(dup) = %d, want 12345 (zero-window first match skipped, later alias match wins)", name, got)
		}
		p.SetModelCatalog([]llm.ModelInfo{{ID: "zero-window", ContextWindow: 0}})
		if got := p.ContextWindowForModel("zero-window"); got != 0 {
			t.Errorf("%s.ContextWindowForModel(zero-window) = %d, want 0 (no usable window)", name, got)
		}
	}
}

func TestProviderCatalogDetectCLIUsesConstructorBinary(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"claude", "codex", "opencode", "agentico-fake-provider-cli"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("writing stub %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	if !claude.NewProvider("").DetectCLI() {
		t.Error(`claude.NewProvider("").DetectCLI() = false, want the resolved default binary`)
	}
	if !codex.NewProvider("").DetectCLI() {
		t.Error(`codex.NewProvider("").DetectCLI() = false, want the resolved default binary`)
	}
	if !opencode.New("", nil).DetectCLI() {
		t.Error(`opencode.New("", nil).DetectCLI() = false, want the resolved default binary`)
	}
	const fake = "agentico-fake-provider-cli"
	if !claude.NewProvider(fake).DetectCLI() || !codex.NewProvider(fake).DetectCLI() || !opencode.New(fake, nil).DetectCLI() {
		t.Error("DetectCLI() = false for a binary present in PATH, want the constructor override honored")
	}
	if claude.NewProvider("agentico-no-such-cli").DetectCLI() || opencode.New("agentico-no-such-cli", nil).DetectCLI() {
		t.Error("DetectCLI() = true for a binary absent from PATH, want false")
	}
}

// --- OpenCode: the same catalog surface pinned where its rules differ ---

func TestOpenCodeCatalogMatchesModelAtInterface(t *testing.T) {
	p := opencode.New("", nil)
	for _, m := range []string{
		// An explicit "opencode:" prefix always matches, in or out of the catalog.
		"opencode:anthropic/claude-sonnet-4-5",
		"opencode:vendor/custom-model",
	} {
		if !p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = false, want true", m)
		}
	}
	// A bare name that has not been discovered must not resolve to OpenCode:
	// it never captures a bare name (e.g. "sonnet", "gpt-5.4") meant for
	// another provider, nor an unknown slash-form id. The bare routing prefix
	// with no backend model is not a valid selection either.
	for _, m := range []string{
		"sonnet",
		"gpt-5.4",
		"anthropic/claude-opus",
		"openai/gpt-4",
		"openai:gpt-5",
		"",
		" opencode:foo", // leading space is not the routing prefix
		"opencode:",     // routing prefix with no backend model
		"opencode:   ",  // routing prefix with whitespace-only backend
	} {
		if p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = true, want false", m)
		}
	}

	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 200_000},
		{ID: "openai/gpt-5"},
	})
	for _, m := range []string{
		"anthropic/claude-sonnet-4-5[200K]", // canonical id
		"anthropic/claude-sonnet-4-5",       // unsuffixed alias
		"ANTHROPIC/CLAUDE-SONNET-4-5",       // case-insensitive
		"openai/gpt-5",
		"opencode:openai/gpt-5", // explicit prefix still matches
		// Unlike Claude and Codex, a bare OpenCode selection is trimmed.
		"  anthropic/claude-sonnet-4-5  ",
	} {
		if !p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = false, want true after discovery", m)
		}
	}
	for _, m := range []string{"sonnet", "anthropic/claude-opus", "openai/gpt-4", "sonnet "} {
		if p.MatchesModel(m) {
			t.Errorf("MatchesModel(%q) = true, want false (not in catalog)", m)
		}
	}

	// No defaults are fabricated: an empty (or cleared) catalog stays empty.
	p.SetModelCatalog(nil)
	if len(p.ModelCatalog()) != 0 || len(p.AvailableModels()) != 0 {
		t.Fatal("clearing a catalog must not invent fallbacks")
	}
	if p.MatchesModel("vendor/custom") {
		t.Fatal("a cleared catalog must not match bare names")
	}
}

func TestOpenCodeCatalogContextWindowsAtInterface(t *testing.T) {
	p := opencode.New("", nil)
	p.SetModelCatalog([]llm.ModelInfo{
		{ID: "anthropic/claude-sonnet-4-5[200K]", Aliases: []string{"anthropic/claude-sonnet-4-5"}, ContextWindow: 200_000},
		{ID: "portkey/@fireworks/accounts/fireworks/models/glm-5p2"},
		{ID: "vendor/no-window"},
	})
	for model, want := range map[string]int{
		"anthropic/claude-sonnet-4-5[200K]":                                    200_000,   // suffixed id
		"anthropic/claude-sonnet-4-5":                                          200_000,   // unsuffixed alias
		"opencode:anthropic/claude-sonnet-4-5[200K]":                           200_000,   // explicit prefix
		"portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]":          1_040_000, // suffix-window fallback
		"opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]": 1_040_000,
		"vendor/no-window[200K]":                                               200_000, // zero-window entry skipped, suffix wins
		"vendor/unknown-model":                                                 0,       // unknown metadata stays unknown
		"vendor/no-window":                                                     0,
	} {
		if got := p.ContextWindowForModel(model); got != want {
			t.Errorf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
		}
	}

	// Reads return isolated deep copies.
	got := p.ModelCatalog()
	got[0].ID = "mutated"
	if again := p.ModelCatalog(); again[0].ID != "anthropic/claude-sonnet-4-5[200K]" {
		t.Errorf("ModelCatalog() leaked read mutations: %q", again[0].ID)
	}
}
