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

// Guards live docs contract: TUI-only launch guidance across user-facing docs.
func TestUserFacingDocsDescribeTUIOnlyLaunchSurface(t *testing.T) {
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
		"--refresh-models",
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
