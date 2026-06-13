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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guards live docs contract: direct TUI plus foreground server launch guidance.
func TestUserFacingDocsDescribeLaunchSurface(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	docs := []string{"README.md"}
	userGuideDocs, err := filepath.Glob(filepath.Join(repoRoot, "skills", "chat", "user-guide", "*.md"))
	if err != nil {
		t.Fatalf("Glob user guide docs: %v", err)
	}
	for _, path := range userGuideDocs {
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			t.Fatalf("Rel(%q): %v", path, err)
		}
		docs = append(docs, rel)
	}

	banned := []string{
		"`agentico run`",
		"agentico run",
		"agentico [flags] [command]",
		"Commands:",
		"feature list       List all features",
		"feature create     Create a new feature",
		"agentico feature create",
		"`agentico feature create`",
		"--name <name>",
		"--repo <path>",
		"--jira <ticket>",
		"--checkpoint <gate>",
		"--auto-publish",
		"**From the CLI:**",
	}
	for _, rel := range docs {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, token := range banned {
			if strings.Contains(text, token) {
				t.Fatalf("%s still advertises removed command-era surface %q", rel, token)
			}
		}
	}

	for _, rel := range []string{
		"README.md",
		filepath.Join("skills", "chat", "user-guide", "configuration.md"),
	} {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, want := range []string{
			"agentico [flags]",
			"--config <path>",
			"--state-dir <path>",
			"--providers <list>",
			"--refresh-models",
			"--dangerously-skip-permissions",
			"--help",
			"--version",
			// Phase 1 update surface: docs must advertise the new subcommand
			// and its check-only flag alongside the retained launch flags.
			"agentico update",
			"--check",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing retained launch guidance %q", rel, want)
			}
		}
		if !strings.Contains(text, "agentico server") {
			t.Fatalf("%s missing server launch guidance", rel)
		}
	}
}

// Guards live docs contract: renamed product story across repository-facing docs.
func TestUserFacingDocsAdvertiseRenamedProduct(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	requiredByDoc := map[string][]string{
		"README.md": {
			"Agentic Orchestrator",
			"agentico",
			"github.com/doordash-oss/agentic-orchestrator/cmd/agentico",
			"go build -o bin/agentico ./cmd/agentico",
			"~/.agentic-orchestrator/",
		},
		filepath.Join("skills", "chat", "user-guide", "getting-started.md"): {
			"Agentic Orchestrator",
			"agentico",
			"github.com/doordash-oss/agentic-orchestrator/cmd/agentico",
			"go build -o bin/agentico ./cmd/agentico",
			"~/.agentic-orchestrator/",
		},
		filepath.Join("skills", "chat", "user-guide", "configuration.md"): {
			"Agentic Orchestrator",
			"agentico",
			"~/.agentic-orchestrator/",
			"agentico.log",
		},
		filepath.Join("skills", "chat", "user-guide", "permissions.md"): {
			"Agentic Orchestrator",
			"agentico",
			"~/.agentic-orchestrator/permissions/",
		},
		filepath.Join("skills", "chat", "user-guide", "index.md"): {
			"Agentic Orchestrator",
		},
		filepath.Join("skills", "chat", "SKILL.md"): {
			"Agentic Orchestrator",
			"agentico",
		},
		filepath.Join("agents", "verification-researcher.md"): {
			"bin/agentico",
			"./cmd/agentico",
		},
		"AGENTS.md": {
			"agentico",
			"agentico.log",
		},
		"CONTRIBUTING.md": {
			"Agentic Orchestrator",
			"agentico",
		},
		filepath.Join("docs", "keybindings.md"): {
			"Agentic Orchestrator Keybinding Reference",
		},
	}

	for rel, wants := range requiredByDoc {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing required renamed-product token %q", rel, want)
			}
		}
	}
}

// TestUserFacingDocsDescribeOpenCodeProviderPath guards the live docs contract
// that OpenCode is documented as a co-equal, supported provider path — install,
// authenticate, minimum version, model-ID semantics, permission mediation,
// managed-session isolation, zero-cost pricing fallback, and troubleshooting —
// and that the README and chat user guide no longer present Claude or Codex as
// the only supported provider path.
func TestUserFacingDocsDescribeOpenCodeProviderPath(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	requiredByDoc := map[string][]string{
		"README.md": {
			"OpenCode CLI >= 1.17.9",
			"opencode.ai/install",
			"opencode auth login",
			"opencode models",
			"opencode:anthropic/claude-sonnet-4-5",
			"--providers opencode",
			"global OpenCode configuration",
		},
		filepath.Join("skills", "chat", "user-guide", "configuration.md"): {
			"Provider Selection",
			"opencode:anthropic/claude-sonnet-4-5",
			"opencode models --verbose",
			"Managed-session isolation",
			"opencode auth login",
			"--providers opencode",
			"1.17.9",
			"zero cost",
		},
		filepath.Join("skills", "chat", "user-guide", "permissions.md"): {
			"OpenCode tool mediation",
			"session/request_permission",
			"--dangerously-skip-permissions",
			"still pauses for you",
			"managed per-session config",
		},
		filepath.Join("skills", "chat", "user-guide", "getting-started.md"): {
			"co-equal",
			"opencode` CLI >= 1.17.9",
			"opencode auth login",
		},
	}

	for rel, wants := range requiredByDoc {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing required OpenCode provider-path token %q", rel, want)
			}
		}
	}

	// The README chat description and getting-started prerequisites must not
	// frame a single provider as the sole/primary supported backend.
	bannedSoleProviderFraming := map[string][]string{
		"README.md": {
			"read-only Claude session",
		},
		filepath.Join("skills", "chat", "user-guide", "getting-started.md"): {
			"the primary AI agent backend",
		},
	}
	for rel, banned := range bannedSoleProviderFraming {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, token := range banned {
			if strings.Contains(text, token) {
				t.Errorf("%s still frames a single provider as the only supported path: %q", rel, token)
			}
		}
	}

	// Model-ID semantics must distinguish the three selection forms a user can
	// type: a plain alias (resolves to a native provider, never OpenCode), the
	// explicit opencode: prefix, and a bare slash-form backend id (resolves to
	// OpenCode and is what provider-neutral defaults persist when OpenCode is the
	// only ready provider). See main_test.go
	// (TestApplyCatalogModelDefaultsToConfig_OpenCodeOnlyPersistsBareBackendIDs)
	// and provider_test.go (TestFallbackRouting_RegistryLevel) for the behavior.
	requiredModelIDSemantics := map[string][]string{
		"README.md": {
			"plain alias",
			"bare slash-form",
			"provider-neutral per-phase defaults",
		},
		filepath.Join("skills", "chat", "user-guide", "configuration.md"): {
			"plain alias",
			"bare slash-form",
			"provider-neutral per-phase defaults",
		},
	}
	for rel, wants := range requiredModelIDSemantics {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing required OpenCode model-ID semantics token %q (must distinguish plain aliases from bare OpenCode backend ids and provider-neutral defaults)", rel, want)
			}
		}
	}

	// The docs must not reassert the inaccurate model-ID claims that OpenCode is
	// reachable only via the explicit prefix or that a bare id can never be an
	// OpenCode default — both contradict single-provider OpenCode configs and
	// bare slash-form resolution.
	bannedModelIDClaims := map[string][]string{
		"README.md": {
			"selected only through the explicit",
			"never silently routes to OpenCode",
		},
		filepath.Join("skills", "chat", "user-guide", "configuration.md"): {
			"selected only through the explicit",
			"never routes to OpenCode",
			"can never be picked silently",
		},
	}
	for rel, banned := range bannedModelIDClaims {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, token := range banned {
			if strings.Contains(text, token) {
				t.Errorf("%s still contains inaccurate OpenCode model-ID claim %q", rel, token)
			}
		}
	}
}

// Guards live smoke-shell launch path: renamed binary path and help text.
func TestSmokeScriptDocsRetainRenamedSurface(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(repoRoot, "test", "e2e", "smoke.sh"))
	if err != nil {
		t.Fatalf("ReadFile(smoke.sh): %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"./bin/agentico",
		"./cmd/agentico",
		"Agentic Orchestrator",
		"~/.agentic-orchestrator/",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("smoke.sh missing required renamed-surface token %q", want)
		}
	}
	for _, bad := range []string{
		"./bin/agentic ",
		"./bin/agentic\"",
		"./cmd/agentic ",
		"./cmd/agentic\"",
	} {
		if strings.Contains(text, bad) {
			t.Errorf("smoke.sh still references retired binary path %q", bad)
		}
	}
}

func TestAGENTSdocumentsTestIsolationRules(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(repoRoot, "AGENTS.md"))
	if err != nil {
		t.Fatalf("ReadFile(AGENTS.md): %v", err)
	}
	text := string(body)

	for _, want := range []string{
		"Test isolation and parallelism",
		"package-level mutable globals",
		"process environment or working directory",
		"global config paths or shared on-disk fixtures",
		"long-running subprocess or session state",
		"pure functions",
		"read-only fixtures",
		"independent t.TempDir() per test",
		"isolated table cases",
		"t.Setenv",
		"t.TempDir",
		"t.Cleanup",
		"observable conditions",
		"per-test option overrides",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("AGENTS.md missing test isolation guidance token %q", want)
		}
	}
}

func TestVerificationDocsDescribeFastSuiteContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	docs := []string{
		"AGENTS.md",
		"CONTRIBUTING.md",
		"README.md",
		filepath.Join("docs", "testing-baseline.md"),
		filepath.Join("skills", "chat", "user-guide", "verification.md"),
	}

	for _, rel := range docs {
		body, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", rel, err)
		}
		text := string(body)
		for _, want := range []string{
			"`make test-fast`",
			"23s, target <=30s",
			"TUI observability",
			"`go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1`",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s missing fast-suite contract token %q", rel, want)
			}
		}
		for _, banned := range []string{
			"115s, target <=30s",
			"29s, target <=30s",
			"26s, target <=30s",
			"21s, target <=30s",
			"19s, target <=30s",
			"13s, target <=30s",
			"core-package",
			"core packages",
			"FAST_CORE_PKGS",
			"GIT_FAST_RUN",
			"No build tags are required.",
			"go test ./cmd/agentic/... -count=1",
		} {
			if strings.Contains(text, banned) {
				t.Errorf("%s still contains stale fast-suite contract token %q", rel, banned)
			}
		}
	}

	userGuide := filepath.Join(repoRoot, "skills", "chat", "user-guide", "verification.md")
	body, err := os.ReadFile(userGuide)
	if err != nil {
		t.Fatalf("ReadFile(user guide verification): %v", err)
	}
	userGuideText := string(body)
	for _, want := range []string{
		"all packages in short mode without the race detector",
		"`go vet ./...` and `go build ./...` remain required",
	} {
		if !strings.Contains(userGuideText, want) {
			t.Errorf("user-guide verification missing fast-suite guidance %q", want)
		}
	}

	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("ReadFile(Makefile): %v", err)
	}
	makefileText := string(makefile)
	for _, want := range []string{
		"go list ./...",
		"go test $$core_pkgs -short -count=1 -parallel 32",
		"go test $$ui_test_pkgs -short -count=1 -parallel 32",
	} {
		if !strings.Contains(makefileText, want) {
			t.Fatalf("Makefile test-fast missing all-package sharded sweep token %q", want)
		}
	}
	for _, banned := range []string{
		"FAST_CORE_PKGS",
		"GIT_FAST_RUN",
	} {
		if strings.Contains(makefileText, banned) {
			t.Errorf("Makefile still contains curated fast-suite token %q", banned)
		}
	}
}
