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

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// TestProviderFxModules_DefaultExcludesOpenCode proves OpenCode stays out of the
// default provider set during Phase 1 when no explicit opencode: model is
// configured, keeping it out of automatic defaults, the setup picker, and
// ordinary model lists.
func TestProviderFxModules_DefaultExcludesOpenCode(t *testing.T) {
	if got := len(providerFxModules(nil, false)); got != 2 {
		t.Fatalf("providerFxModules(nil, false) = %d modules, want 2 (claude+codex, opencode excluded)", got)
	}
}

// TestProviderFxModules_AutoRegistersOpenCodeForExplicitConfig proves that when
// the config selects an explicit opencode: model, OpenCode joins the default
// provider set so config-driven routing can resolve under normal startup.
func TestProviderFxModules_AutoRegistersOpenCodeForExplicitConfig(t *testing.T) {
	if got := len(providerFxModules(nil, true)); got != 3 {
		t.Fatalf("providerFxModules(nil, true) = %d modules, want 3 (claude+codex+opencode)", got)
	}
}

// TestProviderFxModules_ExplicitProvidersFlagHonorsOpenCode proves the
// --providers opt-in still selects exactly the named providers regardless of the
// auto-registration signal.
func TestProviderFxModules_ExplicitProvidersFlagHonorsOpenCode(t *testing.T) {
	if got := len(providerFxModules([]string{"opencode"}, false)); got != 1 {
		t.Fatalf("providerFxModules([opencode]) = %d modules, want 1", got)
	}
	if got := len(providerFxModules([]string{"claude", "codex", "opencode"}, false)); got != 3 {
		t.Fatalf("providerFxModules([claude codex opencode]) = %d modules, want 3", got)
	}
}

// TestConfigSelectsOpenCode proves the config scan recognizes an explicit
// opencode: model in the default model selections or a pipeline preference, and
// reports false for missing files and configs that never reference OpenCode.
func TestConfigSelectsOpenCode(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if configSelectsOpenCode(filepath.Join(t.TempDir(), "nonexistent.yaml")) {
			t.Fatal("configSelectsOpenCode(missing) = true, want false")
		}
	})

	t.Run("no opencode model", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		cfg := config.NewDefault()
		cfg.Defaults.Models.Implementation = "claude:opus"
		if err := config.Save(path, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
		if configSelectsOpenCode(path) {
			t.Fatal("configSelectsOpenCode(no opencode) = true, want false")
		}
	})

	t.Run("opencode model in defaults", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		cfg := config.NewDefault()
		cfg.Defaults.Models.Implementation = "opencode:anthropic/claude-sonnet-4-5"
		if err := config.Save(path, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
		if !configSelectsOpenCode(path) {
			t.Fatal("configSelectsOpenCode(opencode default) = false, want true")
		}
	})

	t.Run("opencode model in pipeline preference", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		cfg := config.NewDefault()
		cfg.Defaults.PipelinePreferences = map[string]config.PipelinePreference{
			"default": {Models: config.ModelConfig{Planning: "opencode:anthropic/claude-sonnet-4-5"}},
		}
		if err := config.Save(path, cfg); err != nil {
			t.Fatalf("save config: %v", err)
		}
		if !configSelectsOpenCode(path) {
			t.Fatal("configSelectsOpenCode(opencode pipeline preference) = false, want true")
		}
	})
}

// TestCheckRequiredProviders_OpenCodeUnreadyFallsBackToReadyProvider proves
// startup proceeds on a ready provider (Claude) when OpenCode is detected but
// not ready, filtering OpenCode out of routing.
func TestCheckRequiredProviders_OpenCodeUnreadyFallsBackToReadyProvider(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude", hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true}})
	r.Register(&stubProvider{name: "opencode", hasCLI: true, installHint: "curl -fsSL https://opencode.ai/install | bash", hasReadiness: true, readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "no provider is configured",
		Remedy: "Run 'opencode auth login'",
	}})

	detected, _, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("detected = %v, want only claude", providerNames(detected))
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "opencode") || !strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("notices = %v, want an opencode startup notice", notices)
	}
	// After filtering, an explicit opencode selection no longer resolves.
	if _, _, err := r.ResolveModel("opencode:anthropic/claude-sonnet-4-5"); err == nil {
		t.Fatal("ResolveModel(opencode:...) succeeded after opencode was filtered out")
	}
}

// TestCheckRequiredProviders_OpenCodeOnlyUnreadyFails proves the existing
// no-ready-provider failure shape applies when OpenCode is the only selected
// provider and is not ready.
func TestCheckRequiredProviders_OpenCodeOnlyUnreadyFails(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "opencode", hasCLI: true, installHint: "curl -fsSL https://opencode.ai/install | bash", hasReadiness: true, readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "no provider is configured",
		Remedy: "Run 'opencode auth login'",
	}})

	_, _, _, filtered, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected no-ready-provider error when opencode is the only selected provider and unready")
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("error = %q, want it to reference opencode", err.Error())
	}
}

// TestCheckRequiredProviders_OpenCodeTooOldFallsBackToReadyProvider proves a
// detected OpenCode whose CLI is below the enforced minimum version is filtered
// out of routing before an ACP session can launch — startup proceeds on the
// ready provider (Claude) instead. The stub's readiness probe would otherwise
// report Ready, so this isolates the version gate as the cause of exclusion.
func TestCheckRequiredProviders_OpenCodeTooOldFallsBackToReadyProvider(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true}})
	r.Register(&stubProvider{
		name: "opencode", hasCLI: true, installHint: "curl -fsSL https://opencode.ai/install | bash",
		hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true},
		version: "1.17.8", minVersion: [3]int{1, 17, 9}, enforceMinVersion: true,
	})

	detected, _, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("detected = %v, want only claude", providerNames(detected))
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "opencode") ||
		!strings.Contains(notices[0], "1.17.8") || !strings.Contains(notices[0], "1.17.9") ||
		!strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("notices = %v, want a too-old opencode startup notice citing the installed and minimum versions", notices)
	}
	// After filtering, an explicit opencode selection no longer resolves to a
	// launchable provider.
	if _, _, err := r.ResolveModel("opencode:anthropic/claude-sonnet-4-5"); err == nil {
		t.Fatal("ResolveModel(opencode:...) succeeded after too-old opencode was filtered out")
	}
}

// TestCheckRequiredProviders_OpenCodeOnlyTooOldFails proves the existing
// no-ready-provider failure shape applies when OpenCode is the only selected
// provider and its CLI is below the enforced minimum version — startup fails
// rather than launching an ACP session against a too-old CLI.
func TestCheckRequiredProviders_OpenCodeOnlyTooOldFails(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{
		name: "opencode", hasCLI: true, installHint: "curl -fsSL https://opencode.ai/install | bash",
		hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true},
		version: "1.17.8", minVersion: [3]int{1, 17, 9}, enforceMinVersion: true,
	})

	_, _, _, filtered, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected no-ready-provider error when opencode is the only selected provider and too old")
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if !strings.Contains(err.Error(), "opencode") || !strings.Contains(err.Error(), "1.17.9") {
		t.Fatalf("error = %q, want it to reference opencode and the minimum version", err.Error())
	}
}

// TestCheckRequiredProviders_OpenCodeReadyAtMinVersionIncluded proves a ready
// OpenCode whose CLI is exactly at the enforced minimum version is NOT filtered
// by the version gate — it stays in the ready set and remains routable for an
// explicit opencode: selection. This is the boundary complement to the too-old
// cases above.
func TestCheckRequiredProviders_OpenCodeReadyAtMinVersionIncluded(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{
		name: "opencode", hasCLI: true, installHint: "curl -fsSL https://opencode.ai/install | bash",
		hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true},
		version: "1.17.9", minVersion: [3]int{1, 17, 9}, enforceMinVersion: true,
	})

	detected, _, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filtered {
		t.Fatal("filtered = true, want false (the only provider is ready and at the minimum version)")
	}
	if len(detected) != 1 || detected[0].Name() != "opencode" {
		t.Fatalf("detected = %v, want opencode included", providerNames(detected))
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v, want none for a single ready provider", notices)
	}
	if _, _, err := r.ResolveModel("opencode:anthropic/claude-sonnet-4-5"); err != nil {
		t.Fatalf("ResolveModel(opencode:...) failed for a ready, at-minimum opencode: %v", err)
	}
}

// TestCheckRequiredProviders_OpenCodeMissingFallsBack proves startup proceeds
// on a ready provider when the OpenCode CLI is missing entirely.
func TestCheckRequiredProviders_OpenCodeMissingFallsBack(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true}})
	r.Register(&stubProvider{name: "opencode", hasCLI: false, installHint: "curl -fsSL https://opencode.ai/install | bash"})

	detected, _, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("detected = %v, want only claude", providerNames(detected))
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "opencode") || !strings.Contains(notices[0], "opencode.ai/install") {
		t.Fatalf("notices = %v, want a missing-opencode install notice", notices)
	}
}
