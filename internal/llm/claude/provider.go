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
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

// Provider implements llm.LLMProvider for Claude Code CLI.
type Provider struct {
	mu      sync.RWMutex
	catalog []llm.ModelInfo
	runner  clirun.CommandRunner
}

func (p *Provider) Name() string { return "claude" }

func (p *Provider) MatchesModel(model string) bool {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		cat = p.defaultModelInfos()
	}
	m := strings.ToLower(model)
	for _, entry := range cat {
		if strings.EqualFold(entry.ID, m) {
			return true
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, m) {
				return true
			}
		}
	}
	return false
}

func (p *Provider) DetectCLI() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

func (p *Provider) AvailableModels() []string {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		cat = p.defaultModelInfos()
	}
	ids := make([]string, len(cat))
	for i, m := range cat {
		ids[i] = m.ID
	}
	return ids
}

func (p *Provider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	args := buildInteractiveCommand(opts)
	return args, nil, nil
}

func (p *Provider) AskingQuestionsClause() string {
	return `## Asking Questions

If you need to ask the user a question or require clarification, use the AskUserQuestion tool. The orchestration system will surface your question to the user and deliver their answer back to you via the tool result.

When using AskUserQuestion, follow this contract:
- Ask one question at a time.
- Prefer structured multiple choice. Provide 2-4 mutually exclusive options whenever the answer can reasonably be categorized.
- Every option should include a short description explaining the tradeoff.
- Mark your recommended option by appending " (Recommended)" to its label.
- Include a numeric "confidence" field on every multiple-choice option.
- Only use a free-text question with no options when the missing value is inherently unconstrained user input, such as an exact version string, free-form name, or arbitrary identifier.
- Do not combine a free-text value request with a separate scope decision in one question. Ask the scope decision first as multiple choice, then ask for the exact value in a follow-up free-text question if still needed.

` + llm.RecommendationConfidenceClause + `

Match the AskUserQuestion structure closely:
- "header": short category label (2-4 words)
- "question": the actual question shown to the user
- "multiSelect": almost always false
- "options": array of objects with "label", "description", and "confidence"

Preferred multiple-choice shape:
{"questions":[{"header":"Scope","question":"What should be updated?","multiSelect":false,"options":[{"label":"Only displayed version (Recommended)","description":"Update the Agentic version shown in the product UI/CLI only.","confidence":0.86},{"label":"Displayed version and release references","description":"Also update packaging, docs, and release-facing references that must stay in sync.","confidence":0.31}]}]}

Preferred free-text shape when the value is inherently unconstrained:
{"questions":[{"header":"Version","question":"What exact version should Agentic be bumped to?","multiSelect":false,"options":[]}]} `
}

func (p *Provider) ComputeCost(_ string, _, _ int64) float64 {
	// Claude CLI reports cost in the result message TotalCostUSD field.
	return 0
}

func (p *Provider) ContextWindowForModel(model string) int {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		cat = p.defaultModelInfos()
	}
	for _, entry := range cat {
		if strings.EqualFold(entry.ID, model) {
			if entry.ContextWindow > 0 {
				return entry.ContextWindow
			}
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) {
				if entry.ContextWindow > 0 {
					return entry.ContextWindow
				}
			}
		}
	}
	return 0
}

func (p *Provider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return NewProtocol(opts)
}

func (p *Provider) InstallHint() string {
	return "npm install -g @anthropic-ai/claude-code"
}

func (p *Provider) VersionInfo() (string, error) {
	out, err := exec.Command("claude", "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run 'claude --version': %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Provider) MinVersion() [3]int { return [3]int{2, 1, 81} }

func (p *Provider) EnvVarsToExclude() []string { return []string{"CLAUDECODE"} }

type authStatus struct {
	LoggedIn     bool   `json:"loggedIn"`
	AuthMethod   string `json:"authMethod"`
	APIProvider  string `json:"apiProvider"`
	Email        string `json:"email"`
	APIKeySource string `json:"apiKeySource"`
}

func (p *Provider) CheckReadiness(ctx context.Context) llm.ProviderReadiness {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	out, err := runner(ctx, "claude", []string{"auth", "status", "--json"}, nil)

	var status authStatus
	if parseErr := json.Unmarshal(out, &status); parseErr == nil {
		if status.LoggedIn {
			return llm.ProviderReadiness{
				Ready:  true,
				Detail: formatReadyAuthDetail(status),
			}
		}
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: "not authenticated",
			Remedy: "Run 'claude auth login' or set ANTHROPIC_API_KEY",
		}
	}

	if err != nil {
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: fmt.Sprintf("could not check authentication: %v", err),
			Remedy: "Run 'claude auth status' to inspect Claude Code authentication",
		}
	}
	return llm.ProviderReadiness{
		Ready:  false,
		Detail: fmt.Sprintf("could not parse 'claude auth status --json' output: %q", strings.TrimSpace(string(out))),
		Remedy: "Run 'claude auth login'",
	}
}

func formatReadyAuthDetail(status authStatus) string {
	switch {
	case status.AuthMethod == "api_key" && status.APIKeySource != "":
		return "authenticated with API key from " + status.APIKeySource
	case status.Email != "":
		return "authenticated as " + status.Email
	case status.AuthMethod != "":
		return "authenticated with " + status.AuthMethod
	default:
		return "authenticated"
	}
}

// PhaseDefaults returns Claude's recommended models per phase.
// SetModelCatalog sets the discovered model catalog.
func (p *Provider) SetModelCatalog(models []llm.ModelInfo) {
	p.mu.Lock()
	p.catalog = models
	p.mu.Unlock()
}

// ModelCatalog returns the current catalog. Falls back to the hardcoded
// defaults when nothing has been set, so callers (TUI, registry) always see
// a populated catalog without needing an explicit seeding step.
func (p *Provider) ModelCatalog() []llm.ModelInfo {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		return p.defaultModelInfos()
	}
	return cat
}

// CLIVersion returns the installed Claude CLI version.
func (p *Provider) CLIVersion() (string, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	out, err := runner(context.Background(), "claude", []string{"--version"}, nil)
	if err != nil {
		return "", fmt.Errorf("running claude --version: %w", err)
	}
	return clirun.ParseVersionOutput(out)
}

// defaultModelInfos returns Agentic's curated Claude Code model catalog.
//
// Runtime startup tries to refresh these with DiscoverModelCatalog; they are
// the offline fallback when probing fails. Entries carry no concrete-version
// alias (e.g. claude-opus-4-8) because what an alias resolves to is
// provider-dependent (Anthropic API vs Bedrock/Vertex/Foundry) and drifts over
// time — a hardcoded version is a frequently-wrong guess. The probe sets the
// resolved model when it runs. Base and `[1m]` variants stay as separate
// entries because `--model opus` (200K) and `--model opus[1m]` (1M) are
// distinct CLI inputs.
//
// Context-window sources (verified 2026-06-05):
//   - https://platform.claude.com/docs/en/about-claude/models/overview
//   - https://code.claude.com/docs/en/model-config
//
// The 200K base windows for opus/sonnet were confirmed empirically from
// auto-compact thresholds observed in live Claude Code sessions
// (compaction at ~167K on --model opus ≈ 83% of 200K).
func (p *Provider) defaultModelInfos() []llm.ModelInfo {
	return []llm.ModelInfo{
		{ID: "opus", DisplayName: "Claude Opus", ContextWindow: 200_000, SmartZoneTokens: 100_000, Category: "capable"},
		{ID: "opus[1m]", DisplayName: "Claude Opus (1M)", ContextWindow: 1_000_000, SmartZoneTokens: 100_000, Category: "capable"},
		{ID: "sonnet", DisplayName: "Claude Sonnet", ContextWindow: 200_000, SmartZoneTokens: 80_000, Category: "balanced"},
		{ID: "sonnet[1m]", DisplayName: "Claude Sonnet (1M)", ContextWindow: 1_000_000, SmartZoneTokens: 80_000, Category: "balanced"},
		{ID: "haiku", DisplayName: "Claude Haiku", ContextWindow: 200_000, SmartZoneTokens: 40_000, Category: "cheap"},
	}
}

// --- Command building helpers ---

// MapEffortLevel maps a provider-agnostic EffortLevel to Claude CLI's --effort value.
// Claude CLI accepts: low, medium, high, xhigh, max.
func MapEffortLevel(level llm.EffortLevel) string {
	switch level {
	case llm.EffortMedium:
		return "medium"
	case llm.EffortHigh:
		return "high"
	case llm.EffortMax:
		return "xhigh"
	default:
		return "high" // safe default
	}
}

// InsertAfterBinary inserts flags right after the binary name (args[0]).
func InsertAfterBinary(cmd []string, flags ...string) []string {
	if len(cmd) == 0 {
		return cmd
	}
	result := make([]string, 0, len(cmd)+len(flags))
	result = append(result, cmd[0])
	result = append(result, flags...)
	result = append(result, cmd[1:]...)
	return result
}

func buildInteractiveCommand(opts llm.CommandBuildOpts) []string {
	args := []string{"claude",
		"--model", opts.Model,
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
	}
	if opts.EffortLevel != "" {
		args = append(args, "--effort", MapEffortLevel(opts.EffortLevel))
	}
	args = append(args, "--include-partial-messages")
	args = applyStreamingOpts(args, opts)

	if opts.DangerouslySkipPerms {
		args = InsertAfterBinary(args, "--dangerously-skip-permissions")
	}

	// Pin the session permission mode when the caller asks for a specific one.
	// Otherwise Claude inherits ~/.claude/settings.json's defaultMode, which
	// can include "auto" — auto mode injects a "work without stopping for
	// clarifying questions" system-reminder that silently overrides
	// grilling-phase prompts ([grill-me]).
	if opts.PermissionMode != "" {
		args = InsertAfterBinary(args, "--permission-mode", opts.PermissionMode)
	}

	// Activate the control_request protocol for AskUserQuestion and tool permissions
	tool := opts.PermissionPromptTool
	if tool == "" {
		tool = "stdio"
	}
	args = InsertAfterBinary(args, "--permission-prompt-tool", tool)

	for _, dir := range opts.AdditionalDirs {
		if dir != "" {
			args = InsertAfterBinary(args, "--add-dir", dir)
		}
	}
	return args
}

func applyStreamingOpts(args []string, opts llm.CommandBuildOpts) []string {
	if opts.AgentsJSON != "" {
		args = append(args, "--agents", opts.AgentsJSON)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	return args
}
