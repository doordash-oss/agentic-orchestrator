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

package agent

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// mockStartupProvider is a minimal LLMProvider for startup tests.
type mockStartupProvider struct {
	name        string
	installHint string
}

func (m *mockStartupProvider) Name() string                 { return m.name }
func (m *mockStartupProvider) VersionInfo() (string, error) { return "1.0.0", nil }
func (m *mockStartupProvider) InstallHint() string          { return m.installHint }
func (m *mockStartupProvider) MatchesModel(_ string) bool   { return false }
func (m *mockStartupProvider) DetectCLI() bool              { return true }
func (m *mockStartupProvider) AvailableModels() []string    { return nil }
func (m *mockStartupProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (m *mockStartupProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (m *mockStartupProvider) MinVersion() [3]int                          { return [3]int{0, 0, 0} }
func (m *mockStartupProvider) EnvVarsToExclude() []string                  { return nil }

// --- FormatNoCLIMessage tests ---

func TestFormatNoCLIMessage_TwoProviders(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockStartupProvider{name: "claude", installHint: "npm install -g @anthropic-ai/claude-code"},
		&mockStartupProvider{name: "codex", installHint: "npm install -g @openai/codex"},
	}
	msg := FormatNoCLIMessage(providers)

	if !strings.Contains(msg, "No AI coding assistant") {
		t.Error("missing friendly header")
	}
	if !strings.Contains(msg, "claude") {
		t.Error("missing claude provider name")
	}
	if !strings.Contains(msg, "codex") {
		t.Error("missing codex provider name")
	}
	if !strings.Contains(msg, "npm install -g @anthropic-ai/claude-code") {
		t.Error("missing claude install hint")
	}
	if !strings.Contains(msg, "npm install -g @openai/codex") {
		t.Error("missing codex install hint")
	}
	if !strings.Contains(msg, "Install one") {
		t.Error("missing install instruction")
	}
	if !strings.Contains(msg, "Agentic Orchestrator requires") {
		t.Error("missing renamed product guidance")
	}
	if !strings.Contains(msg, "'agentico' again") {
		t.Error("missing renamed launch command guidance")
	}
}

func TestFormatNoCLIMessage_SingleProvider(t *testing.T) {
	providers := []llm.LLMProvider{
		&mockStartupProvider{name: "claude", installHint: "npm install -g @anthropic-ai/claude-code"},
	}
	msg := FormatNoCLIMessage(providers)
	if !strings.Contains(msg, "claude") {
		t.Error("missing provider name for single provider")
	}
}

// --- ApplyStartupDefaults tests ---

func TestApplyStartupDefaults_FillsEmptyConfig(t *testing.T) {
	cfg := &config.Config{}
	defaults := map[string]string{
		"inquiry":        "sonnet",
		"research":       "opus",
		"planning":       "opus",
		"implementation": "opus",
		"review":         "gpt-5.4",
		"chat":           "sonnet",
		"kb_build":       "opus",
	}
	changed := ApplyStartupDefaults(cfg, defaults)
	if !changed {
		t.Error("expected changed=true when filling empty config")
	}
	if cfg.Defaults.Models.Inquiry != "sonnet" {
		t.Errorf("expected Inquiry='sonnet', got %q", cfg.Defaults.Models.Inquiry)
	}
	if cfg.Defaults.Models.Research != "opus" {
		t.Errorf("expected Research='opus', got %q", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Review != "gpt-5.4" {
		t.Errorf("expected Review='gpt-5.4', got %q", cfg.Defaults.Models.Review)
	}
}

func TestApplyStartupDefaults_PreservesUserValues(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.Models.Inquiry = "haiku"
	cfg.Defaults.Models.Research = "haiku"
	defaults := map[string]string{
		"inquiry":        "sonnet",
		"research":       "opus",
		"planning":       "opus",
		"implementation": "opus",
		"review":         "gpt-5.4",
		"chat":           "sonnet",
		"kb_build":       "opus",
	}
	changed := ApplyStartupDefaults(cfg, defaults)
	if !changed {
		t.Error("expected changed=true since other fields were empty")
	}
	if cfg.Defaults.Models.Inquiry != "haiku" {
		t.Errorf("expected Inquiry='haiku' preserved, got %q", cfg.Defaults.Models.Inquiry)
	}
	if cfg.Defaults.Models.Research != "haiku" {
		t.Errorf("expected Research='haiku' preserved, got %q", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Planning != "opus" {
		t.Errorf("expected Planning='opus' filled, got %q", cfg.Defaults.Models.Planning)
	}
}

// --- CheckRequiredTools tests ---

func TestCheckRequiredTools_GitPresent(t *testing.T) {
	// On any dev machine, git should be available.
	hard, _ := CheckRequiredTools()
	for _, issue := range hard {
		if issue.Code == errcat.MissingExecutable {
			t.Skip("git not in PATH on this machine")
		}
	}
}

func TestCheckRequiredToolsMissingGitIsStructured(t *testing.T) {
	isolateGitHubAuth(t)
	hard, soft := checkRequiredTools(func(name string) (string, error) {
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	})
	if len(hard) != 1 {
		t.Fatalf("hard = %#v; want exactly the missing git issue", hard)
	}
	if hard[0].Code != errcat.MissingExecutable {
		t.Errorf("missing git code = %q; want missing_executable", hard[0].Code)
	}
	if !strings.Contains(hard[0].Diagnostics, "git not found in PATH") ||
		!strings.Contains(hard[0].Diagnostics, "https://git-scm.com/downloads") {
		t.Errorf("missing git diagnostics = %q; want the install hint", hard[0].Diagnostics)
	}
	if len(soft) != 1 || soft[0].Code != errcat.GithubCredentialsMissing {
		t.Fatalf("soft = %#v; want the credentials warning issue", soft)
	}
	for _, issue := range append(append([]ToolIssue(nil), hard...), soft...) {
		for _, text := range []string{issue.Diagnostics} {
			if strings.HasPrefix(text, "Error: ") || strings.HasPrefix(text, "Warning: ") {
				t.Errorf("issue %q carries a pre-formatted prefix: %q", issue.Code, text)
			}
		}
	}
}

func TestApplyStartupDefaults_NoChange(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.Models.Inquiry = "z"
	cfg.Defaults.Models.Research = "a"
	cfg.Defaults.Models.Planning = "b"
	cfg.Defaults.Models.Implementation = "c"
	cfg.Defaults.Models.Review = "d"
	cfg.Defaults.Models.Utilities = "e"
	cfg.Defaults.Models.KBBuild = "f"

	defaults := map[string]string{
		"inquiry":        "sonnet",
		"research":       "opus",
		"planning":       "opus",
		"implementation": "opus",
		"review":         "codex",
		"chat":           "sonnet",
		"kb_build":       "opus",
	}
	changed := ApplyStartupDefaults(cfg, defaults)
	if changed {
		t.Error("expected changed=false when all fields already set")
	}
}
