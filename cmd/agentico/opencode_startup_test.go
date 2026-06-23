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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// TestStartupCopyListsOpenCodeAsSupportedProvider is the startup-copy golden:
// it pins that the usage/help text presents OpenCode as a co-equal selectable
// provider, and that the no-ready-provider startup message surfaces a missing
// OpenCode CLI with its install hint — the user-facing copy that documents the
// supported OpenCode provider path at launch.
func TestStartupCopyListsOpenCodeAsSupportedProvider(t *testing.T) {
	var usage strings.Builder
	printUsage(&usage)
	for _, want := range []string{
		"--providers",
		"Available: claude, codex, opencode",
	} {
		if !strings.Contains(usage.String(), want) {
			t.Errorf("usage text missing %q:\n%s", want, usage.String())
		}
	}

	claude := &stubProvider{name: "claude", hasCLI: true, hasReadiness: true, readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "not logged in.",
		Remedy: "Run 'claude auth login'",
	}}
	openCode := &stubProvider{name: "opencode", hasCLI: false, installHint: "curl -fsSL https://opencode.ai/install | bash"}

	msg := formatNoReadyProviderMessage(
		[]llm.LLMProvider{claude, openCode},
		[]providerReadinessIssue{{provider: claude, status: claude.readiness}},
	)
	for _, want := range []string{
		"opencode",
		"opencode.ai/install",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("no-ready-provider message missing OpenCode token %q:\n%s", want, msg)
		}
	}
}

// TestProviderFxModules_DefaultIncludesOpenCode proves OpenCode is now a normal
// member of the default provider set: a launch without an explicit --providers
// filter registers Claude, Codex, and OpenCode together, before readiness
// filtering. No config signal or auto-registration gate is required.
func TestProviderFxModules_DefaultIncludesOpenCode(t *testing.T) {
	if got := len(providerFxModules(nil)); got != 3 {
		t.Fatalf("providerFxModules(nil) = %d modules, want 3 (claude+codex+opencode)", got)
	}
}

// TestProviderFxModules_ExplicitProvidersFlagHonorsOpenCode proves the
// --providers opt-in selects exactly the named providers, including OpenCode
// alone or alongside the others.
func TestProviderFxModules_ExplicitProvidersFlagHonorsOpenCode(t *testing.T) {
	if got := len(providerFxModules([]string{"opencode"})); got != 1 {
		t.Fatalf("providerFxModules([opencode]) = %d modules, want 1", got)
	}
	if got := len(providerFxModules([]string{"claude", "codex", "opencode"})); got != 3 {
		t.Fatalf("providerFxModules([claude codex opencode]) = %d modules, want 3", got)
	}
}

// TestProviderFxModules_MixedListWithOpenCodeAnyPositionTrimmed proves a mixed
// --providers list can place OpenCode in any position and tolerates surrounding
// whitespace.
func TestProviderFxModules_MixedListWithOpenCodeAnyPositionTrimmed(t *testing.T) {
	if got := len(providerFxModules([]string{" opencode ", " claude "})); got != 2 {
		t.Fatalf("providerFxModules([ opencode , claude ]) = %d modules, want 2 (trimmed, opencode leading)", got)
	}
	if got := len(providerFxModules([]string{"codex", "opencode"})); got != 2 {
		t.Fatalf("providerFxModules([codex opencode]) = %d modules, want 2", got)
	}
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
