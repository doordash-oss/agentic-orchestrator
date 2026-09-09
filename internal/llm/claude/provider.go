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

// defaultBinary is the executable name Agentico invokes for Claude when no
// override is configured.
const defaultBinary = "claude"

// Provider implements llm.LLMProvider for Claude Code CLI.
type Provider struct {
	mu      sync.RWMutex
	catalog []llm.ModelInfo
	runner  clirun.CommandRunner
	// binary overrides the CLI executable name; empty means defaultBinary.
	binary string
}

func (p *Provider) Name() string { return "claude" }

// SupportsSessionResume reports that a prior CLI session can be resumed via
// CommandBuildOpts.ResumeSessionID (the --resume flag).
func (p *Provider) SupportsSessionResume() bool { return true }

// cliBinary returns the configured CLI executable name, falling back to the
// provider default when no override is set.
func (p *Provider) cliBinary() string {
	if p.binary != "" {
		return p.binary
	}
	return defaultBinary
}

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
	_, err := exec.LookPath(p.cliBinary())
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
	opts.Model = p.claudeCLIModel(opts.Model)
	args := buildInteractiveCommand(p.cliBinary(), opts)
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
	bin := p.cliBinary()
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run '%s --version': %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Provider) MinVersion() [3]int { return [3]int{2, 1, 81} }

func (p *Provider) EnvVarsToExclude() []string { return []string{"CLAUDECODE"} }

func (p *Provider) SupportsNativeToollessReview() bool { return true }

// EnablesPendingToolWatchdog opts Claude into the generic session watchdog.
// The adapter emits no tool lifecycle updates, so the watchdog only times a
// window when one is armed explicitly (e.g. an answered AskUserQuestion that
// still owes a tool_result).
func (p *Provider) EnablesPendingToolWatchdog() bool { return true }

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
	out, err := runner(ctx, p.cliBinary(), []string{"auth", "status", "--json"}, nil)

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
// defaults when nothing has been set, so callers (desktop app, registry) always see
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

// RecommendedModels nominates the balanced Claude family as the default for
// every role. Provider-local family knowledge stays out of shared selection
// code; configured model_recommendations override these nominations.
func (p *Provider) RecommendedModels(llm.PhaseRole) []string {
	return []string{"sonnet[200K]", "sonnet"}
}

// ReviewPreferenceBand ranks Claude review models without leaking model-family
// naming into shared automatic-review code.
func (p *Provider) ReviewPreferenceBand(model llm.ModelInfo) (int, bool) {
	if reviewModelMatchesHint(model, "haiku") {
		return 0, true
	}
	if model.Category == "cheap" {
		return 1, true
	}
	return 0, false
}

func reviewModelMatchesHint(model llm.ModelInfo, hint string) bool {
	if strings.Contains(strings.ToLower(model.ID), hint) {
		return true
	}
	for _, alias := range model.Aliases {
		if strings.Contains(strings.ToLower(alias), hint) {
			return true
		}
	}
	return false
}

// CLIVersion returns the installed Claude CLI version.
func (p *Provider) CLIVersion() (string, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	bin := p.cliBinary()
	out, err := runner(context.Background(), bin, []string{"--version"}, nil)
	if err != nil {
		return "", fmt.Errorf("running %s --version: %w", bin, err)
	}
	return clirun.ParseVersionOutput(out)
}

// defaultModelInfos supplies offline routing defaults when initialization and
// the persisted catalog are unavailable. Discovery never filters through this
// list, and these hints never claim to be CLI-reported capabilities.
func (p *Provider) defaultModelInfos() []llm.ModelInfo {
	return []llm.ModelInfo{
		{ID: "fable[1M]", DisplayName: "Claude Fable (1M)", ContextWindow: 1_000_000, Aliases: []string{"fable"}, Category: "capable"},
		{ID: "opus[200K]", DisplayName: "Claude Opus (200K)", ContextWindow: 200_000, Aliases: []string{"opus"}, Category: "capable"},
		{ID: "opus[1M]", DisplayName: "Claude Opus (1M)", ContextWindow: 1_000_000, Aliases: []string{"opus[1m]"}, Category: "capable"},
		{ID: "sonnet[200K]", DisplayName: "Claude Sonnet (200K)", ContextWindow: 200_000, Aliases: []string{"sonnet"}, Category: "balanced"},
		{ID: "sonnet[1M]", DisplayName: "Claude Sonnet (1M)", ContextWindow: 1_000_000, Aliases: []string{"sonnet[1m]"}, Category: "balanced"},
		{ID: "haiku[200K]", DisplayName: "Claude Haiku (200K)", ContextWindow: 200_000, Aliases: []string{"haiku"}, Category: "cheap"},
	}
}

// --- Command building helpers ---

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

func buildInteractiveCommand(binary string, opts llm.CommandBuildOpts) []string {
	args := []string{binary,
		"--model", opts.Model,
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
	}
	if opts.EffortLevel != "" {
		args = append(args, "--effort", llm.MapStandardEffortLevel(opts.EffortLevel))
	}
	args = append(args, "--include-partial-messages")
	args = applyStreamingOpts(args, opts)

	// --safe-mode disables CLAUDE.md, skills, plugins, hooks, MCP servers,
	// agents, and other customizations while retaining normal authentication.
	// It ensures the hidden reviewer's context contains only the static review
	// policy and declared execution-context fields.
	if opts.NoCustomization {
		args = InsertAfterBinary(args, "--safe-mode")
	}

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

func (p *Provider) claudeCLIModel(model string) string {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		cat = p.defaultModelInfos()
	}
	for _, entry := range cat {
		if strings.EqualFold(entry.ID, model) {
			return claudeCLIModelForCatalogEntry(entry)
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) {
				return claudeCLIModelForCatalogEntry(entry)
			}
		}
	}
	return model
}

func claudeCLIModelForCatalogEntry(entry llm.ModelInfo) string {
	if len(entry.Aliases) > 0 {
		return entry.Aliases[0]
	}
	return entry.ID
}

func applyStreamingOpts(args []string, opts llm.CommandBuildOpts) []string {
	if opts.AgentsJSON != "" {
		args = append(args, "--agents", opts.AgentsJSON)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.ZeroTools {
		args = append(args, "--tools", "")
	} else if len(opts.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(opts.DisallowedTools, ","))
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}
	if opts.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	return args
}
