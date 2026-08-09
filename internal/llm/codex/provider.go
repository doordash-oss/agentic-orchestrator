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

package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/agentdef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

// defaultBinary is the executable name Agentico invokes for Codex when no
// override is configured.
const defaultBinary = "codex"

// Provider implements llm.LLMProvider for the Codex CLI (app-server mode).
type Provider struct {
	mu      sync.RWMutex
	catalog []llm.ModelInfo
	runner  clirun.CommandRunner
	// binary overrides the CLI executable name; empty means defaultBinary.
	binary string
}

func (p *Provider) Name() string { return "codex" }

// SupportsNativeToollessReview attests that Codex's isolated app-server launch
// and explicit zero-environment thread implement the complete hidden-review
// contract. Ordinary Codex sessions do not activate this boundary.
func (p *Provider) SupportsNativeToollessReview() bool { return true }

// SupportsSessionResume reports that a persisted thread can be resumed via
// ProtocolOpts.ResumeSessionID (the thread/resume handshake).
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
	nativeToolless, err := nativeToollessReviewRequested(opts)
	if err != nil {
		return nil, nil, err
	}

	// Interactive: app-server mode. Model/prompt delivered via JSON-RPC.
	args := []string{p.cliBinary(), "app-server"}
	if opts.EffortLevel != "" {
		args = append(args, "-c", "model_reasoning_effort="+llm.MapStandardEffortLevel(opts.EffortLevel))
	}
	if window := p.contextWindowOverrideForModel(opts.Model); window > 0 {
		args = append(args, "-c", fmt.Sprintf("model_context_window=%d", window))
	}
	if nativeToolless {
		for _, override := range nativeToollessConfigOverrides {
			args = append(args, "-c", override)
		}
		env, err := p.prepareNativeToollessHome(opts.StateDir)
		if err != nil {
			return nil, nil, err
		}
		return args, env, nil
	}
	// Enable live web search so agents can fetch current web pages.
	args = append(args, "-c", "web_search=live")

	if err := p.prepareRealHome(); err != nil {
		return nil, nil, fmt.Errorf("preparing codex home: %w", err)
	}
	return args, nil, nil
}

var nativeToollessConfigOverrides = []string{
	"web_search=disabled",
	"mcp_servers={}",
	"plugins={}",
	"features.shell_tool=false",
	"features.multi_agent=false",
	"features.apps=false",
	"features.plugins=false",
	"features.connectors=false",
	"features.web_search=false",
	"features.standalone_web_search=false",
	"features.web_search_request=false",
	"features.search_tool=false",
	"features.tool_search=false",
	"features.tool_suggest=false",
	"features.request_permissions_tool=false",
	"features.memory_tool=false",
	"features.goals=false",
	"features.image_generation=false",
	"features.computer_use=false",
	"features.browser_use=false",
	"features.in_app_browser=false",
	"features.js_repl=false",
	"features.code_mode=false",
	"tools.update_plan.enabled=false",
	"tools.experimental_request_user_input.enabled=false",
	"skills.bundled.enabled=false",
	"skills.include_instructions=false",
	"agents={}",
	"include_apps_instructions=false",
	"include_environment_context=false",
	"include_permissions_instructions=false",
	"include_collaboration_mode_instructions=false",
}

func nativeToollessReviewRequested(opts llm.CommandBuildOpts) (bool, error) {
	requested := opts.ZeroTools || opts.NoSessionPersistence || opts.NoCustomization
	if !requested {
		return false, nil
	}
	if !opts.ZeroTools || !opts.NoSessionPersistence || !opts.NoCustomization {
		return false, fmt.Errorf("Codex native tool-less review requires zero tools, no session persistence, and no customization")
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		return false, fmt.Errorf("Codex native tool-less review requires an ephemeral provider state directory")
	}
	return true, nil
}

func (p *Provider) prepareNativeToollessHome(stateDir string) ([]string, error) {
	realHome, err := p.resolveCodexHome()
	if err != nil {
		return nil, fmt.Errorf("resolving Codex auth home: %w", err)
	}
	isolatedHome := filepath.Join(stateDir, "codex-home")
	if err := os.MkdirAll(isolatedHome, 0o700); err != nil {
		return nil, fmt.Errorf("creating isolated Codex home: %w", err)
	}
	for _, name := range []string{"auth.json", ".credentials.json"} {
		source := filepath.Join(realHome, name)
		data, readErr := os.ReadFile(source)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			return nil, fmt.Errorf("reading Codex authentication %s: %w", name, readErr)
		}
		if err := os.WriteFile(filepath.Join(isolatedHome, name), data, 0o600); err != nil {
			return nil, fmt.Errorf("copying Codex authentication %s: %w", name, err)
		}
	}
	return []string{
		"CODEX_HOME=" + isolatedHome,
		"CODEX_SQLITE_HOME=" + isolatedHome,
	}, nil
}

func (p *Provider) AskingQuestionsClause() string {
	return `## Asking Questions

If you need to ask the user a question or require clarification, you MUST structure your question with numbered alternatives. The orchestration system will parse your text and present the options as selectable choices to the user.

IMPORTANT: NEVER ask open-ended, free-form questions. ALWAYS propose exactly 3 concrete alternatives and recommend one. This rule applies on every turn — not just the first question of a conversation.

Follow this format strictly:
- Write a brief question stem (1-2 sentences) ending with a question mark.
- List exactly 3 mutually exclusive options as a numbered list.
- Each option MUST have a short label followed by a colon and a description of the tradeoff.
- End each option with a user-visible confidence suffix in the exact form "[confidence: 0.00]".
- Mark exactly one option with "(Recommended)" in its label.
- Ask one question at a time. Do not bundle multiple decisions.

` + llm.RecommendationConfidenceClause + `

Example:

Which files should I document?
1. Tracked source files only (Recommended): Faster, focuses on committed code that other developers will see. [confidence: 0.88]
2. Source files and generated artifacts: Also covers build outputs and generated configs, but may include noise. [confidence: 0.42]
3. All files in worktree: Comprehensive but includes temporary and generated files that may change frequently. [confidence: 0.19]

Free-form exception (rare): ask a free-form question without numbered options ONLY when the answer is inherently unconstrained — e.g. an exact version string, a free-form name, or an arbitrary identifier. "I'm not sure what they prefer" is NOT a valid reason — if you can imagine 3 plausible answers, list them.

Confirmation traps to avoid. Open-ended confirmation prose disguised as a question is the most common failure mode and ends the phase as a protocol violation. NEVER end a turn with patterns like:
- "Is that the X you want?" / "Sound good?" / "Does that work for you?"
- "Shall I proceed with Y?" / "Want me to do Z?"
- "Let me know if this looks right."
If you want the user to confirm a recommendation, restructure as a 3-option choice where one option is "Yes, do X (Recommended)" and the other options are concrete alternatives (e.g. a different scope, a different approach, a stop-and-discuss option). The same rule applies on every turn of an interview — not just the first question. After the user answers one question and you do follow-up tool exploration, your next question still needs the full numbered-options format.

Self-check before sending every question. If any answer is "no" and the free-form exception above does not apply, rewrite before sending:
1. Does the stem end with a single "?" and ask one decision?
2. Are there exactly 3 numbered options, each formatted "Label: short tradeoff description [confidence: 0.00]"?
3. Is exactly one option marked "(Recommended)" in its label?
4. Are the options mutually exclusive and cover the realistic answer space?
5. Is the question a real choice between alternatives, not a confirmation of a single direction you already proposed?`
}

func (p *Provider) ComputeCost(model string, inputTokens, outputTokens int64) float64 {
	return computeCost(model, int(inputTokens), 0, 0, int(outputTokens))
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

func (p *Provider) contextWindowOverrideForModel(model string) int {
	if strings.TrimSpace(model) == "" || model == llm.StripModelContextWindow(model) {
		return 0
	}
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		cat = p.defaultModelInfos()
	}
	for _, entry := range cat {
		if strings.EqualFold(entry.ID, model) {
			return entry.ContextWindow
		}
	}
	return 0
}

func (p *Provider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return NewProtocol(opts)
}

func (p *Provider) InstallHint() string {
	return "npm install -g @openai/codex"
}

func (p *Provider) VersionInfo() (string, error) {
	bin := p.cliBinary()
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run '%s --version': %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Provider) MinVersion() [3]int { return [3]int{0, 116, 0} }

func (p *Provider) EnvVarsToExclude() []string { return nil }

func (p *Provider) CheckReadiness(ctx context.Context) llm.ProviderReadiness {
	if _, err := p.ensureCodexHomeForStatus(); err != nil {
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: fmt.Sprintf("could not prepare Codex home: %v", err),
			Remedy: "Fix CODEX_HOME or run 'codex login'",
		}
	}

	runner := p.runner
	if runner == nil {
		// codex login status prints "Logged in using ..." to stderr, so
		// capture combined output; DefaultRunner would drop it and make a
		// logged-in account look like empty status.
		runner = clirun.CombinedRunner()
	}
	out, err := runner(ctx, p.cliBinary(), []string{"login", "status"}, nil)
	text := strings.TrimSpace(string(out))
	lower := strings.ToLower(text)

	if err == nil && strings.Contains(lower, "logged in") && !strings.Contains(lower, "not logged in") {
		return llm.ProviderReadiness{
			Ready:  true,
			Detail: text,
		}
	}
	if strings.Contains(lower, "not logged in") {
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: "not logged in",
			Remedy: "Run 'codex login'",
		}
	}
	if err != nil {
		detail := fmt.Sprintf("could not check login status: %v", err)
		if text != "" {
			detail = fmt.Sprintf("could not check login status: %s", text)
		}
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: detail,
			Remedy: "Run 'codex login status' to inspect Codex authentication",
		}
	}
	if text == "" {
		text = "<empty output>"
	}
	return llm.ProviderReadiness{
		Ready:  false,
		Detail: fmt.Sprintf("login status did not report a logged-in account: %s", text),
		Remedy: "Run 'codex login'",
	}
}

// SetModelCatalog sets the discovered model catalog.
func (p *Provider) SetModelCatalog(models []llm.ModelInfo) {
	p.mu.Lock()
	p.catalog = models
	p.mu.Unlock()
}

// ModelCatalog returns the current catalog. Falls back to the hardcoded
// defaults when nothing has been set, so callers (desktop app, registry) always see
// a populated catalog without an explicit seeding step.
func (p *Provider) ModelCatalog() []llm.ModelInfo {
	p.mu.RLock()
	cat := p.catalog
	p.mu.RUnlock()
	if len(cat) == 0 {
		return p.defaultModelInfos()
	}
	return cat
}

// ReviewPreferenceBand ranks Codex review models without leaking model-family
// naming into shared automatic-review code.
func (p *Provider) ReviewPreferenceBand(model llm.ModelInfo) (int, bool) {
	if model.Category == "cheap" {
		return 0, true
	}
	if reviewModelMatchesHint(model, "mini") || reviewModelMatchesHint(model, "shrink") {
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

// CLIVersion returns the installed Codex CLI version.
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

// OutputPricePerMToken returns the output price per million tokens for a model.
func (p *Provider) OutputPricePerMToken(model string) float64 {
	r, ok := lookupRate(model)
	if !ok {
		return 0
	}
	return r.outputPerMToken
}

// defaultModelInfos returns Agentic's curated Codex/OpenAI model catalog.
//
// Runtime startup refreshes these values with `codex debug models`, which
// exposes Codex's raw catalog including context windows. These values remain
// as an offline fallback so the Session.ContextPercentage() signal works from
// session start even when discovery is unavailable.
//
// Sources (verified 2026-04-18):
//   - https://developers.openai.com/api/docs/models/gpt-5.5
//   - https://developers.openai.com/api/docs/models/gpt-5.4
//   - https://developers.openai.com/api/docs/models/gpt-5.4-mini
//   - https://developers.openai.com/api/docs/models/gpt-5.3-codex
//   - https://developers.openai.com/api/docs/models/gpt-5.2
func (p *Provider) defaultModelInfos() []llm.ModelInfo {
	return []llm.ModelInfo{
		{ID: "gpt-5.5[272K]", DisplayName: "GPT-5.5 (272K)", ContextWindow: 272_000, Aliases: []string{"gpt-5.5"}, Category: "capable",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "gpt-5.4[272K]", DisplayName: "GPT-5.4 (272K)", ContextWindow: 272_000, Aliases: []string{"gpt-5.4"}, Category: "balanced",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "gpt-5.4[1M]", DisplayName: "GPT-5.4 (1M)", ContextWindow: 1_000_000, Category: "capable",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "gpt-5.4-mini[400K]", DisplayName: "GPT-5.4 Mini (400K)", ContextWindow: 400_000, Aliases: []string{"gpt-5.4-mini"}, Category: "balanced",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "gpt-5.3-codex[400K]", DisplayName: "GPT-5.3 Codex (400K)", Aliases: []string{"gpt-5.3-codex"}, ContextWindow: 400_000, Category: "balanced",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
		{ID: "gpt-5.2[400K]", DisplayName: "GPT-5.2 (400K)", ContextWindow: 400_000, Aliases: []string{"gpt-5.2"},
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}},
	}
}

// --- Environment setup ---

func (p *Provider) resolveCodexHome() (string, error) {
	if raw := strings.TrimSpace(os.Getenv("CODEX_HOME")); raw != "" {
		expanded, err := expandHomeDir(raw)
		if err != nil {
			return "", err
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			return "", fmt.Errorf("resolving CODEX_HOME %q: %w", raw, err)
		}
		return abs, nil
	}
	home := defaultCodexHome()
	if home == "" {
		return "", fmt.Errorf("resolving Codex home: HOME is not set")
	}
	return home, nil
}

func (p *Provider) reconcileAgenticAgents(codexHome string) error {
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return fmt.Errorf("creating codex home: %w", err)
	}

	agentsDir := filepath.Join(codexHome, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("creating codex agents dir: %w", err)
	}

	defs, err := agentdef.ParseEmbedded()
	if err != nil {
		return fmt.Errorf("parsing embedded agents: %w", err)
	}

	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		def := defs[name]
		if err := writeAgentTOMLIfChanged(agentsDir, name, def); err != nil {
			return fmt.Errorf("writing agent TOML for %s: %w", name, err)
		}
	}
	return nil
}

func (p *Provider) prepareRealHome() error {
	codexHome, err := p.resolveCodexHome()
	if err != nil {
		return err
	}
	return p.reconcileAgenticAgents(codexHome)
}

func (p *Provider) ensureCodexHomeForStatus() (string, error) {
	codexHome, err := p.resolveCodexHome()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		return "", fmt.Errorf("creating codex home: %w", err)
	}
	return codexHome, nil
}

func defaultCodexHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func expandHomeDir(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding %q: %w", path, err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func writeAgentTOMLIfChanged(dir string, name string, def agentdef.AgentDef) error {
	content := []byte(agentTOMLContent(name, def))
	path := filepath.Join(dir, name+".toml")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, content) {
		return nil
	}

	tmp, err := os.CreateTemp(dir, name+".*.toml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp TOML: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("setting temp TOML permissions: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("writing temp TOML: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp TOML: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("renaming temp TOML: %w", err)
	}
	return nil
}

func agentTOMLContent(name string, def agentdef.AgentDef) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\n", name)
	fmt.Fprintf(&b, "description = %q\n", def.Description)
	escaped := strings.ReplaceAll(def.Prompt, `"""`, `\"\"\"`)
	fmt.Fprintf(&b, "developer_instructions = \"\"\"\n%s\n\"\"\"\n", escaped)
	return b.String()
}
