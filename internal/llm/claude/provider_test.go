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

package claude

import (
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestClaudeProvider_EnvVarsToExclude(t *testing.T) {
	p := &Provider{}
	got := p.EnvVarsToExclude()
	if len(got) != 1 || got[0] != "CLAUDECODE" {
		t.Errorf("EnvVarsToExclude() = %v, want [CLAUDECODE]", got)
	}
}

// TestClaudeProvider_DefaultCatalog_Invariants locks in the hardcoded
// catalog that replaced the former discovery pipeline. Whenever Anthropic
// ships new model variants, update this table deliberately — that's the
// point of hardcoding.
func TestClaudeProvider_DefaultCatalog_Invariants(t *testing.T) {
	p := &Provider{}
	cat := p.defaultModelInfos()

	byID := make(map[string]llm.ModelInfo, len(cat))
	for _, m := range cat {
		byID[m.ID] = m
	}

	wantWindows := map[string]int{
		"opus":       200_000,
		"opus[1m]":   1_000_000,
		"sonnet":     200_000,
		"sonnet[1m]": 1_000_000,
		"haiku":      200_000,
	}

	for id, want := range wantWindows {
		m, ok := byID[id]
		if !ok {
			t.Errorf("missing catalog entry for %q", id)
			continue
		}
		if m.ContextWindow != want {
			t.Errorf("%s.ContextWindow = %d, want %d", id, m.ContextWindow, want)
		}
	}

	// The `[1m]` variants must be distinct catalog entries, not aliases of
	// the base ID. This is load-bearing: canonicalModelForProvider rewrites
	// alias matches to the canonical ID, which would silently downgrade
	// `--model opus[1m]` (1M) to `--model opus` (200K).
	for _, m := range cat {
		for _, alias := range m.Aliases {
			if alias == m.ID+"[1m]" {
				t.Errorf("entry %q must not carry %q as an alias; %q must be its own catalog ID", m.ID, alias, alias)
			}
		}
	}
}

// TestClaudeProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed
// guards against regressions where ContextWindowForModel stopped falling
// back to defaultModelInfos when SetModelCatalog had not been called.
// Before the fix that path silently returned 0, which made the
// implement-loop 60% handoff ticker dead on fresh sessions.
func TestClaudeProvider_ContextWindowForModel_ReturnsHardcodedWithoutSeed(t *testing.T) {
	p := &Provider{}

	tests := map[string]int{
		"opus":       200_000,
		"opus[1m]":   1_000_000,
		"sonnet[1m]": 1_000_000,
		"haiku":      200_000,
	}
	for model, want := range tests {
		t.Run(model, func(t *testing.T) {
			if got := p.ContextWindowForModel(model); got != want {
				t.Errorf("ContextWindowForModel(%q) = %d, want %d", model, got, want)
			}
		})
	}
}

// TestClaudeProvider_ModelCatalog_FallsBackToHardcoded verifies the same
// fallback on the catalog-exposing API that the TUI and registry depend on.
func TestClaudeProvider_ModelCatalog_FallsBackToHardcoded(t *testing.T) {
	p := &Provider{}
	got := p.ModelCatalog()
	want := p.defaultModelInfos()
	if len(got) != len(want) {
		t.Fatalf("ModelCatalog() len = %d, want %d", len(got), len(want))
	}
}

// TestBuildInteractiveCommand_PermissionMode pins the wiring that prevents
// Claude Code's user-level "auto" defaultMode from silently overriding
// grilling-phase prompts. When the caller passes PermissionMode, the CLI
// receives an explicit `--permission-mode <value>` so the session is not
// launched under auto mode (which injects a "work without stopping for
// clarifying questions" system reminder that suppresses [grill-me]).
func TestBuildInteractiveCommand_PermissionMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "default mode emitted", mode: "default", want: true},
		{name: "plan mode emitted", mode: "plan", want: true},
		{name: "empty mode not emitted", mode: "", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := buildInteractiveCommand(llm.CommandBuildOpts{
				Model:          "opus",
				PermissionMode: tc.mode,
			})
			hasFlag := false
			for i, a := range args {
				if a == "--permission-mode" {
					hasFlag = true
					if i+1 >= len(args) || args[i+1] != tc.mode {
						t.Errorf("--permission-mode value = %q, want %q", args[i+1], tc.mode)
					}
				}
			}
			if hasFlag != tc.want {
				t.Errorf("--permission-mode present = %v, want %v; args=%v", hasFlag, tc.want, args)
			}
		})
	}
}

// TestBuildInteractiveCommand_ReadOnlyOutsideRoots pins the Claude-side
// enforcement for document-only roles: file-mutation tools must land in
// --disallowedTools (with no duplicates against any caller-supplied list)
// when the agent opts in via ReadOnlyOutsideRoots.
func TestBuildInteractiveCommand_ReadOnlyOutsideRoots(t *testing.T) {
	t.Run("appends mutation tools when flag set", func(t *testing.T) {
		args := buildInteractiveCommand(llm.CommandBuildOpts{
			Model:                "opus",
			ReadOnlyOutsideRoots: true,
		})
		got := extractFlagValue(args, "--disallowedTools")
		for _, want := range []string{"Edit", "MultiEdit", "Write", "NotebookEdit"} {
			if !strings.Contains(got, want) {
				t.Errorf("--disallowedTools = %q, missing %q", got, want)
			}
		}
	})

	t.Run("flag not set leaves disallowedTools empty", func(t *testing.T) {
		args := buildInteractiveCommand(llm.CommandBuildOpts{Model: "opus"})
		if slices.Contains(args, "--disallowedTools") {
			t.Errorf("--disallowedTools should not be emitted: %v", args)
		}
	})

	t.Run("preserves caller-supplied disallows and dedupes", func(t *testing.T) {
		args := buildInteractiveCommand(llm.CommandBuildOpts{
			Model:                "opus",
			DisallowedTools:      []string{"WebSearch", "Write"},
			ReadOnlyOutsideRoots: true,
		})
		got := extractFlagValue(args, "--disallowedTools")
		// WebSearch stays, Write isn't repeated.
		if !strings.Contains(got, "WebSearch") {
			t.Errorf("--disallowedTools = %q lost WebSearch", got)
		}
		if strings.Count(got, "Write") != 1 {
			t.Errorf("--disallowedTools = %q duplicates Write", got)
		}
	})
}

func extractFlagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestBuildInteractiveCommand_PermissionMode_CoexistsWithDSP verifies that
// --dangerously-skip-permissions and --permission-mode can both be emitted on
// the same invocation. Empirically, the Claude CLI accepts both: DSP wins for
// runtime permission handling, but --permission-mode still suppresses the
// auto-mode system-reminder.
func TestBuildInteractiveCommand_PermissionMode_CoexistsWithDSP(t *testing.T) {
	args := buildInteractiveCommand(llm.CommandBuildOpts{
		Model:                "opus",
		DangerouslySkipPerms: true,
		PermissionMode:       "default",
	})
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Errorf("missing --dangerously-skip-permissions:\n%v", args)
	}
	if !slices.Contains(args, "--permission-mode") {
		t.Errorf("missing --permission-mode:\n%v", args)
	}
}
