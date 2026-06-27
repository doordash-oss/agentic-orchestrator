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

// Provider implements llm.LLMProvider for the Codex CLI (app-server mode).
type Provider struct {
	mu      sync.RWMutex
	catalog []llm.ModelInfo
	runner  clirun.CommandRunner
}

func (p *Provider) Name() string { return "codex" }

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
	_, err := exec.LookPath("codex")
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

// MapEffortLevel maps a provider-agnostic EffortLevel to Codex's model_reasoning_effort value.
// Codex accepts: low, medium, high, xhigh.
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

func (p *Provider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	// Interactive: app-server mode. Model/prompt delivered via JSON-RPC.
	args := []string{"codex", "app-server"}
	if opts.EffortLevel != "" {
		args = append(args, "-c", "model_reasoning_effort="+MapEffortLevel(opts.EffortLevel))
	}
	if window := p.contextWindowOverrideForModel(opts.Model); window > 0 {
		args = append(args, "-c", fmt.Sprintf("model_context_window=%d", window))
	}
	// Enable live web search so agents can fetch current web pages.
	args = append(args, "-c", "web_search=live")

	if err := p.prepareRealHome(); err != nil {
		return nil, nil, fmt.Errorf("preparing codex home: %w", err)
	}
	return args, nil, nil
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
	return computeCost(model, int(inputTokens), 0, int(outputTokens))
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
	out, err := exec.Command("codex", "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run 'codex --version': %w", err)
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
	out, err := runner(ctx, "codex", []string{"login", "status"}, nil)
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
// defaults when nothing has been set, so callers (TUI, registry) always see
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

// CLIVersion returns the installed Codex CLI version.
func (p *Provider) CLIVersion() (string, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	out, err := runner(context.Background(), "codex", []string{"--version"}, nil)
	if err != nil {
		return "", fmt.Errorf("running codex --version: %w", err)
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
		{ID: "gpt-5.5[272K]", DisplayName: "GPT-5.5 (272K)", ContextWindow: 272_000, Aliases: []string{"gpt-5.5"}, Category: "capable"},
		{ID: "gpt-5.4[272K]", DisplayName: "GPT-5.4 (272K)", ContextWindow: 272_000, Aliases: []string{"gpt-5.4"}, Category: "balanced"},
		{ID: "gpt-5.4[1M]", DisplayName: "GPT-5.4 (1M)", ContextWindow: 1_000_000, Category: "capable"},
		{ID: "gpt-5.4-mini[400K]", DisplayName: "GPT-5.4 Mini (400K)", ContextWindow: 400_000, Aliases: []string{"gpt-5.4-mini"}, Category: "balanced"},
		{ID: "gpt-5.3-codex[400K]", DisplayName: "GPT-5.3 Codex (400K)", Aliases: []string{"gpt-5.3-codex"}, ContextWindow: 400_000, Category: "balanced"},
		{ID: "gpt-5.2[400K]", DisplayName: "GPT-5.2 (400K)", ContextWindow: 400_000, Aliases: []string{"gpt-5.2"}},
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
