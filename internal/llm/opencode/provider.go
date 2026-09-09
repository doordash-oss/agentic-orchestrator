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

// Package opencode implements an llm.LLMProvider for the OpenCode CLI driven
// over the Agent Client Protocol (ACP). A ready OpenCode contributes a live
// model catalog discovered from the local CLI through the same catalog,
// discovery, context-window, and cost interfaces the other providers use, so
// its backend models participate in routing, provider-grouped model lists, and
// as a co-equal default selection. A failed refresh can use a previously
// discovered cache with a warning; no accessible backend is invented. Models
// are always expressed in OpenCode's native "provider/model" form: the
// "opencode:" routing prefix and Agentico's "[<window>]" context-window suffix
// are both stripped before a model string is handed to the CLI.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/clirun"
)

// providerName is the Agentico routing identifier for OpenCode.
const providerName = "opencode"

// RoutingPrefix is the Agentico-only model prefix that explicitly selects the
// OpenCode provider, e.g. "opencode:anthropic/claude-sonnet-4-5". It is purely
// a routing artifact: the prefix is stripped before the backend model string
// is handed to OpenCode.
const RoutingPrefix = providerName + ":"

// configContentEnvVar is OpenCode's inline-config environment variable. Setting
// it lets the provider pin the backend model for one launch without writing to
// or mutating the user's global OpenCode configuration.
const configContentEnvVar = "OPENCODE_CONFIG_CONTENT"

// Provider implements llm.LLMProvider for the OpenCode CLI (ACP stdio mode).
//
// runner is injectable so version/readiness/discovery probes can be exercised in
// tests without a real OpenCode binary; production code falls back to clirun
// helpers. catalog holds the model catalog discovered at startup (empty until
// discovery runs); rates holds the per-model pricing parsed alongside it. Both
// are guarded by mu because discovery, startup enrichment, and routing lookups
// touch them from different goroutines.
type Provider struct {
	mu      sync.RWMutex
	catalog []llm.ModelInfo
	rates   map[string]modelRate
	runner  clirun.CommandRunner
	// binary overrides the CLI executable name; empty means defaultBinary.
	binary string
}

// defaultBinary is the executable name Agentico invokes for OpenCode when no
// override is configured.
const defaultBinary = "opencode"

// cliBinary returns the configured CLI executable name, falling back to the
// provider default when no override is set.
func (p *Provider) cliBinary() string {
	if p.binary != "" {
		return p.binary
	}
	return defaultBinary
}

// modelRate holds the per-million-token pricing discovered for one backend
// model. Pricing is not part of llm.ModelInfo (and so is not persisted in the
// version-keyed catalog cache); it lives here only for the lifetime of a
// discovered catalog and drives ComputeCost when present.
type modelRate struct {
	inputPerMToken  float64
	outputPerMToken float64
}

// New returns a Provider with the default (production) command runner and the
// default CLI binary name.
func New() *Provider { return &Provider{} }

// NewWithBinary returns a Provider that invokes the supplied CLI binary name
// (or the default when empty). It is the constructor used by the FX module to
// honor the providers.opencode.cli config override.
func NewWithBinary(binary string) *Provider { return &Provider{binary: binary} }

// NewWithRunner returns a Provider that uses the supplied command runner for its
// version, readiness, and model-discovery probes instead of shelling out to the
// real OpenCode binary. It is a dependency-injection seam for tests that drive
// the provider through the shared startup discovery and cache path; production
// code uses New.
func NewWithRunner(runner clirun.CommandRunner) *Provider {
	return &Provider{runner: runner}
}

func (p *Provider) Name() string { return providerName }

// EnablesPendingToolWatchdog opts this adapter into the generic session
// watchdog for providers that can report tool lifecycle updates without
// necessarily completing the enclosing turn or surfacing a permission request.
// The historical capability name is retained for compatibility; it enables
// both the running-tool and post-tool turn-completion safety rails.
func (p *Provider) EnablesPendingToolWatchdog() bool { return true }

// UsesBoundedHelperSandbox opts bounded helper sessions into OS-level worktree
// sandboxing so helper shell probes fail as process errors rather than provider
// permission denials.
func (p *Provider) UsesBoundedHelperSandbox() bool { return true }

// SupportsSessionResume reports that a prior ACP session can be resumed via
// ProtocolOpts.ResumeSessionID (session/load). Agents that do not advertise
// the loadSession capability fail the resume handshake with a clear error,
// which callers treat as "resume unavailable" and fall back.
func (p *Provider) SupportsSessionResume() bool { return true }

// SupportsNativeToollessReview attests that the provider's isolated managed
// configuration and ACP protocol implement Agentico's complete hidden-review
// contract. BuildCommand activates that boundary only when all three native
// isolation options are requested; ordinary OpenCode sessions are unchanged.
func (p *Provider) SupportsNativeToollessReview() bool { return true }

// MatchesModel reports whether this provider handles the given model string.
//
// An explicit "opencode:" routing prefix always matches when a backend model
// follows it (the bare prefix "opencode:" with no backend is not a valid
// selection). A bare model string matches only when it names an entry in the
// effective catalog — a discovered or cached catalog — by canonical id or alias. Catalog IDs
// are slash-form "provider/model" values, so a ready OpenCode never captures a
// bare name (e.g. "sonnet", "gpt-5.4") meant for another provider.
func (p *Provider) MatchesModel(model string) bool {
	if strings.HasPrefix(model, RoutingPrefix) {
		return BackendModel(model) != ""
	}
	return p.catalogContains(model)
}

// catalogContains reports whether model names an effective-catalog entry by its
// canonical id or one of its aliases (case-insensitive).
func (p *Provider) catalogContains(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, entry := range p.catalogSnapshot() {
		if strings.EqualFold(entry.ID, model) {
			return true
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) {
				return true
			}
		}
	}
	return false
}

// DetectCLI reports whether the OpenCode CLI binary is available in PATH.
func (p *Provider) DetectCLI() bool {
	_, err := exec.LookPath(p.cliBinary())
	return err == nil
}

// AvailableModels returns only discovered or cached model IDs.
func (p *Provider) AvailableModels() []string {
	cat := p.catalogSnapshot()
	ids := make([]string, len(cat))
	for i, m := range cat {
		ids[i] = m.ID
	}
	return ids
}

// BuildCommand returns the args and environment needed to launch OpenCode in
// ACP stdio mode with a deterministic, Agentico-owned managed session
// configuration.
//
// The backend model is validated as a data value before any command is
// constructed: empty selections, CLI-flag-shaped values, and values carrying
// shell/interpolation metacharacters fail closed so a malformed or hostile
// selection can never reach a launchable command (and an empty selection never
// silently falls back to OpenCode's default model). Valid slash-form
// "provider/model" ids pass through unchanged.
//
// The command itself is the bare `opencode acp`: it speaks newline-delimited
// JSON-RPC on stdout while sending its own logs to stderr, so stdout stays valid
// for the protocol reader. Managed configuration is generated under the
// provider-managed state directory and delivered through the OPENCODE_CONFIG file
// plus the highest-precedence OPENCODE_CONFIG_CONTENT inline channel, while
// inherited compatibility/config sources are scrubbed through environment flags —
// none of which mutates the user's global OpenCode configuration. Any build
// failure aborts before a launchable command exists. See buildManagedSession.
func (p *Provider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	return buildManagedSession(p.cliBinary(), opts, p.effortOptions(opts.Model, opts.EffortLevel))
}

// validateBackendModel reports whether a stripped OpenCode backend model is a
// safe, usable data value at the provider/subprocess boundary. OpenCode receives
// the model as inline-config data (never through a shell), but the provider
// still fails closed on anything that is not a plain slash-form model id so a
// malformed or hostile selection cannot reach command construction:
//   - empty (or whitespace-only) selections are rejected so OpenCode never
//     silently falls back to its default model;
//   - credential-shaped ids are rejected before any diagnostic can echo them;
//   - a leading "-" is rejected because it would read as a CLI flag;
//   - any character outside the model-id allowlist (every shell metacharacter
//     and all whitespace) is rejected as requiring shell interpolation;
//   - malformed slash-form ids are rejected so the launch boundary matches
//     discovery's model-id safety rules.
//
// Valid "provider/model" ids and any OpenCode-meaningful suffix pass unchanged.
func validateBackendModel(backend string) error {
	if backend == "" {
		return fmt.Errorf("empty OpenCode model selection: an explicit provider/model backend is required")
	}
	if tokenPrefixPattern.MatchString(backend) {
		return fmt.Errorf("invalid OpenCode model: credential-like content is not allowed in backend model selection")
	}
	if strings.HasPrefix(backend, "-") {
		return fmt.Errorf("invalid OpenCode model %q: a leading %q looks like a CLI flag", backend, "-")
	}
	for _, r := range backend {
		if !isSafeModelRune(r) {
			return fmt.Errorf("invalid OpenCode model %q: unsupported character %q requires shell interpolation", backend, string(r))
		}
	}
	provider, model, ok := strings.Cut(backend, "/")
	if !ok || provider == "" || model == "" {
		return fmt.Errorf("invalid OpenCode model: expected non-empty provider/model backend")
	}
	return nil
}

// isSafeModelRune reports whether r may appear in an OpenCode backend
// "provider/model" identifier. The allowlist covers slash-form ids, version and
// variant tokens, tag separators, and provider path prefixes such as
// Portkey's "@fireworks/..." model namespace while excluding whitespace and
// every shell metacharacter, so a backend that passes never needs shell quoting.
func isSafeModelRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '/', '-', '_', '.', ':', '@':
		return true
	default:
		return false
	}
}

// BackendModel returns the OpenCode-native "provider/model" string for a model
// selection. It strips the two Agentico-only routing artifacts so OpenCode never
// sees them: the "opencode:" routing prefix (removed exactly once) and the
// trailing "[<window>]" context-window suffix (selection metadata, never part of
// a backend model name). The remaining slash-form id — including any colon-form
// tag such as an ollama "model:tag" — is preserved verbatim.
func BackendModel(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, RoutingPrefix)
	model = strings.TrimSpace(model)
	model = llm.StripModelContextWindow(model)
	return strings.TrimSpace(model)
}

// NewProtocol creates a per-session ACP protocol handler.
func (p *Provider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol {
	return NewProtocol(opts)
}

// InstallHint returns the supported OpenCode install command.
func (p *Provider) InstallHint() string {
	return "curl -fsSL https://opencode.ai/install | bash"
}

// VersionInfo runs `opencode --version` (through the injectable runner) and
// returns the parsed semver string (e.g. "1.17.9"), matching the Claude and Codex
// providers. Parsing — rather than returning the raw command output — guarantees
// the value the startup discovery path uses as the catalog cache key, cache
// filename, and persisted cache metadata is a clean version token that cannot
// carry trailing credential-like or terminal-control content from a malformed or
// hostile `opencode --version` line. On unparseable output it returns a generic
// error that does not echo the raw output, so no untrusted content reaches the
// startup version diagnostic either. It shares the same runner seam as discovery.
func (p *Provider) VersionInfo() (string, error) {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}
	bin := p.cliBinary()
	out, err := runner(context.Background(), bin, []string{"--version"}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to run '%s --version': %w", bin, err)
	}
	version, err := clirun.ParseVersionOutput(out)
	if err != nil {
		// Deliberately do not wrap the parser error: it echoes the raw output,
		// which an attacker-influenced version line could use to inject
		// credential-like or terminal-control content into the version diagnostic.
		return "", errors.New("'opencode --version' returned no recognizable version")
	}
	return version, nil
}

// MinVersion returns the minimum OpenCode CLI version required. 1.17.9 is the
// earliest version against which the OpenCode ACP behavior (initialize with
// protocolVersion 1, session/new, session/prompt, session/update streaming) and
// the `opencode models` discovery surface have been verified.
func (p *Provider) MinVersion() [3]int { return [3]int{1, 17, 9} }

// EnforcesMinVersion reports that OpenCode must meet MinVersion() to be ready at
// startup. Because its ACP wire behavior is only verified at or above 1.17.9, a
// too-old CLI is filtered out of the ready provider set — and thus out of
// routing, catalog discovery, and model lists — rather than left selectable with
// only a warning.
func (p *Provider) EnforcesMinVersion() bool { return true }

// EnvVarsToExclude returns nil — OpenCode needs no env stripping.
func (p *Provider) EnvVarsToExclude() []string { return nil }

// ComputeCost computes token cost from the pricing parsed during catalog
// discovery. When pricing was exposed for the model in a stable numeric shape it
// is applied (per million tokens); otherwise — unknown model, no pricing, or a
// a catalog without advertised pricing — it returns 0 so the
// session falls back to the cost OpenCode reports over ACP without corrupting
// usage summaries.
func (p *Provider) ComputeCost(model string, inputTokens, outputTokens int64) float64 {
	rate, ok := p.lookupRate(model)
	if !ok {
		return 0
	}
	return (float64(inputTokens)/1_000_000)*rate.inputPerMToken +
		(float64(outputTokens)/1_000_000)*rate.outputPerMToken
}

// lookupRate returns the discovered pricing for a model, trying the selection as
// given and then with the Agentico context-window suffix removed (rates are
// keyed under both forms during discovery).
func (p *Provider) lookupRate(model string) (modelRate, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return modelRate{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if r, ok := p.rates[strings.ToLower(model)]; ok {
		return r, true
	}
	if stripped := llm.StripModelContextWindow(model); stripped != model {
		if r, ok := p.rates[strings.ToLower(stripped)]; ok {
			return r, true
		}
	}
	return modelRate{}, false
}

// ContextWindowForModel returns the context window for a model from the
// discovered or cached catalog, matching by canonical id or alias (so suffixed IDs, unsuffixed
// aliases, and the canonicalized form of an explicit "opencode:" selection all
// resolve). It returns 0 only when no catalog metadata exists for the model,
// which callers treat as "unknown" without corrupting behavior.
func (p *Provider) ContextWindowForModel(model string) int {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, RoutingPrefix)
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	suffixWindow := llm.ParseModelContextWindow(model)
	strippedModel := strings.TrimSpace(llm.StripModelContextWindow(model))
	for _, entry := range p.catalogSnapshot() {
		if entry.ContextWindow <= 0 {
			continue
		}
		if strings.EqualFold(entry.ID, model) || strings.EqualFold(entry.ID, strippedModel) {
			return entry.ContextWindow
		}
		for _, alias := range entry.Aliases {
			if strings.EqualFold(alias, model) || strings.EqualFold(alias, strippedModel) {
				return entry.ContextWindow
			}
		}
	}
	if suffixWindow > 0 {
		return suffixWindow
	}
	return 0
}

// ModelCatalog returns an isolated snapshot of discovered or cached metadata.
// No models or capabilities are fabricated when discovery is unavailable.
func (p *Provider) ModelCatalog() []llm.ModelInfo {
	return p.catalogSnapshot()
}

// ReviewPreferenceBand admits discovered text models for native toolless
// review without guessing quality or cost from family names.
func (p *Provider) ReviewPreferenceBand(model llm.ModelInfo) (int, bool) {
	return 0, model.Capabilities == nil || model.Capabilities.TextOutput == nil || *model.Capabilities.TextOutput
}

func (p *Provider) catalogSnapshot() []llm.ModelInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneCatalog(p.catalog)
}

// RefreshCatalogOnStartup prevents a CLI-version cache from hiding changes to
// user-defined provider models or variants. A failed refresh can use a stale
// cache with the startup warning, but no model catalog is fabricated.
func (p *Provider) RefreshCatalogOnStartup() bool { return true }

// SetModelCatalog installs an isolated metadata snapshot and restores its
// advertised pricing, including when the catalog was loaded from cache.
func (p *Provider) SetModelCatalog(models []llm.ModelInfo) {
	p.mu.Lock()
	p.catalog = cloneCatalog(models)
	p.rates = make(map[string]modelRate)
	for _, m := range models {
		if m.Cost != nil {
			p.rates[strings.ToLower(m.ID)] = modelRate{inputPerMToken: m.Cost.Input, outputPerMToken: m.Cost.Output}
			p.rates[strings.ToLower(BackendModel(m.ID))] = modelRate{inputPerMToken: m.Cost.Input, outputPerMToken: m.Cost.Output}
		}
	}
	p.mu.Unlock()
}

// setRates replaces the discovered pricing table. Called by discovery before the
// catalog is installed via SetModelCatalog.
func (p *Provider) setRates(rates map[string]modelRate) {
	p.mu.Lock()
	p.rates = rates
	p.mu.Unlock()
}

// CheckReadiness probes whether OpenCode is configured with usable provider
// access, beyond mere binary presence. It runs the non-interactive
// `opencode models`, which lists the models reachable from configured
// providers, and distinguishes ready / installed-but-unconfigured /
// command-failed / timed-out states with actionable remedies.
func (p *Provider) CheckReadiness(ctx context.Context) llm.ProviderReadiness {
	runner := p.runner
	if runner == nil {
		runner = clirun.DefaultRunner()
	}

	out, err := runner(ctx, p.cliBinary(), []string{"models"}, nil)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return llm.ProviderReadiness{
				Ready:  false,
				Detail: "timed out listing OpenCode models",
				Remedy: "Check the OpenCode installation and retry",
			}
		}
		detail := fmt.Sprintf("could not list OpenCode models: %v", err)
		if text := strings.TrimSpace(string(out)); text != "" {
			detail = fmt.Sprintf("could not list OpenCode models: %s", text)
		}
		// `opencode models` output or the command error can echo provider config,
		// auth tokens, or API keys on failure; redact them before the detail is
		// surfaced at startup or persisted.
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: sanitizeDiagnostic(detail),
			Remedy: "Run 'opencode auth login' to configure a provider",
		}
	}

	if countModelLines(out) == 0 {
		return llm.ProviderReadiness{
			Ready:  false,
			Detail: "no OpenCode models available; no provider is configured",
			Remedy: "Run 'opencode auth login' to configure a provider",
		}
	}

	return llm.ProviderReadiness{
		Ready:  true,
		Detail: "OpenCode provider access configured",
	}
}

// countModelLines counts the non-empty lines OpenCode prints for `opencode
// models` (one "provider/model" per line).
func countModelLines(out []byte) int {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// AskingQuestionsClause provides the provider-specific asking-questions prompt
// section. OpenCode questions surface through Agentico's shared AskUserQuestion
// flow: the protocol parses the agent's numbered text alternatives into a formal
// AskUserQuestion that pauses the session for the user, so the clause directs the
// agent to express any question in exactly that numbered, confidence-qualified
// format (the structure the plain-text parser reads). A question phrased this way
// becomes a help-waiting pause, not a phase completion.
func (p *Provider) AskingQuestionsClause() string {
	return `## Asking Questions

If you need to ask the user a question or require clarification, you MUST structure your question with numbered alternatives. The orchestration system parses your text and presents the options as selectable choices to the user.

IMPORTANT: NEVER ask open-ended, free-form questions. ALWAYS propose exactly 3 concrete alternatives and recommend one. This rule applies on every turn.

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
1. Tracked source files only (Recommended): Faster, focuses on committed code. [confidence: 0.88]
2. Source files and generated artifacts: Also covers build outputs, but may include noise. [confidence: 0.42]
3. All files in worktree: Comprehensive but includes temporary files. [confidence: 0.19]`
}
