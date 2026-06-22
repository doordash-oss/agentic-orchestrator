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

// Package opencode implements an explicit-only llm.LLMProvider for the
// OpenCode CLI driven over the Agent Client Protocol (ACP). Phase 1 is a
// tracer bullet: OpenCode only resolves through the explicit "opencode:"
// routing prefix and is deliberately kept out of catalog defaults, the setup
// picker, and ordinary user-selectable model lists. The backend model is
// expressed in OpenCode's native "provider/model" form once the routing prefix
// is stripped.
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

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
// it lets the tracer pin the backend model for one launch without writing to or
// mutating the user's global OpenCode configuration.
const configContentEnvVar = "OPENCODE_CONFIG_CONTENT"

// Provider implements llm.LLMProvider for the OpenCode CLI (ACP stdio mode).
//
// runner is injectable so version/readiness probes can be exercised in tests
// without a real OpenCode binary; production code falls back to clirun helpers.
type Provider struct {
	runner clirun.CommandRunner
}

// New returns a Provider with the default (production) command runner.
func New() *Provider { return &Provider{} }

func (p *Provider) Name() string { return providerName }

// MatchesModel reports whether this provider handles the given model string.
//
// Phase 1 is explicit-only: OpenCode matches a model string ONLY when it
// carries the explicit "opencode:" routing prefix AND names a backend model
// after it. Bare model strings never resolve to OpenCode, which keeps it out of
// automatic defaults and ordinary model selection until a later roadmap phase
// opts it in; the bare prefix with no backend ("opencode:") is not a valid
// selection and does not match.
func (p *Provider) MatchesModel(model string) bool {
	return strings.HasPrefix(model, RoutingPrefix) && BackendModel(model) != ""
}

// DetectCLI reports whether the OpenCode CLI binary is available in PATH.
func (p *Provider) DetectCLI() bool {
	_, err := exec.LookPath("opencode")
	return err == nil
}

// AvailableModels returns nil. OpenCode contributes no entries to catalog
// defaults, the setup picker, or user-selectable model lists during Phase 1;
// it is reachable only through an explicit "opencode:" selection.
func (p *Provider) AvailableModels() []string { return nil }

// BuildCommand returns the args and environment needed to launch OpenCode in
// ACP stdio mode with the requested backend model selected.
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
// for the protocol reader. The validated backend model is selected via the
// OPENCODE_CONFIG_CONTENT environment variable (inline config) rather than a CLI
// flag — `opencode acp` rejects `-m` — and rather than editing the user's global
// config file.
func (p *Provider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	backend := BackendModel(opts.Model)
	if err := validateBackendModel(backend); err != nil {
		return nil, nil, err
	}

	args := []string{"opencode", "acp"}

	env, err := configContentEnv(backend, opts.DangerouslySkipPerms)
	if err != nil {
		return nil, nil, err
	}
	return args, env, nil
}

// sessionConfig is the inline OpenCode configuration the tracer pins for one
// managed launch. It carries the validated backend model and the session-scoped
// permission decisions; delivered via OPENCODE_CONFIG_CONTENT, it never mutates
// the user's global OpenCode configuration file.
type sessionConfig struct {
	Model      string            `json:"model"`
	Permission map[string]string `json:"permission"`
}

// configContentEnv builds the OPENCODE_CONFIG_CONTENT entry that pins the given
// validated backend model and the Phase 2 permission decisions for a managed
// launch. Callers must validate the backend with validateBackendModel first.
func configContentEnv(backend string, dangerouslySkipPerms bool) ([]string, error) {
	content, err := json.Marshal(sessionConfig{
		Model:      backend,
		Permission: permissionConfig(dangerouslySkipPerms),
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling OpenCode config content: %w", err)
	}
	return []string{configContentEnvVar + "=" + string(content)}, nil
}

// permissionGatedTools are the OpenCode permission keys for the tool surfaces
// Phase 2 routes through Agentico's permission flow: shell, file edits, web
// fetch/search, and external-directory access.
var permissionGatedTools = []string{"bash", "edit", "webfetch", "websearch", "external_directory"}

// questionPermissionKey is the OpenCode permission key governing user-facing
// questions. It always stays "ask" so questions surface as AskUserQuestion
// pauses rather than being auto-answered — even in dangerous-skip mode.
const questionPermissionKey = "question"

// permissionConfig returns the session-scoped OpenCode permission map. In normal
// mode every gated surface asks, so Agentico pauses for the user (its caching
// and deny decisions then apply after normalization). In dangerous-skip mode the
// tool surfaces are allowed noninteractively, but questions still ask so an
// AskUserQuestion is never silently auto-approved.
func permissionConfig(dangerouslySkipPerms bool) map[string]string {
	toolDecision := "ask"
	if dangerouslySkipPerms {
		toolDecision = "allow"
	}
	perm := make(map[string]string, len(permissionGatedTools)+1)
	for _, key := range permissionGatedTools {
		perm[key] = toolDecision
	}
	perm[questionPermissionKey] = "ask"
	return perm
}

// validateBackendModel reports whether a stripped OpenCode backend model is a
// safe, usable data value at the provider/subprocess boundary. OpenCode receives
// the model as inline-config data (never through a shell), but the tracer still
// fails closed on anything that is not a plain slash-form model id so a
// malformed or hostile selection cannot reach command construction:
//   - empty (or whitespace-only) selections are rejected so OpenCode never
//     silently falls back to its default model;
//   - a leading "-" is rejected because it would read as a CLI flag;
//   - any character outside the model-id allowlist (every shell metacharacter
//     and all whitespace) is rejected as requiring shell interpolation.
//
// Valid "provider/model" ids and any OpenCode-meaningful suffix pass unchanged.
func validateBackendModel(backend string) error {
	if backend == "" {
		return fmt.Errorf("empty OpenCode model selection: an explicit provider/model backend is required")
	}
	if strings.HasPrefix(backend, "-") {
		return fmt.Errorf("invalid OpenCode model %q: a leading %q looks like a CLI flag", backend, "-")
	}
	for _, r := range backend {
		if !isSafeModelRune(r) {
			return fmt.Errorf("invalid OpenCode model %q: unsupported character %q requires shell interpolation", backend, string(r))
		}
	}
	return nil
}

// isSafeModelRune reports whether r may appear in an OpenCode backend
// "provider/model" identifier. The allowlist covers slash-form ids, version and
// variant tokens, and tag separators while excluding whitespace and every shell
// metacharacter, so a backend that passes never needs shell quoting.
func isSafeModelRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '/', '-', '_', '.', ':':
		return true
	default:
		return false
	}
}

// BackendModel returns the OpenCode-native "provider/model" string for a model
// selection. The Agentico "opencode:" routing prefix is stripped exactly once;
// the backend slash-form id and any suffix meaningful to OpenCode are otherwise
// preserved verbatim.
func BackendModel(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, RoutingPrefix)
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

// VersionInfo runs `opencode --version` and returns the raw version string.
func (p *Provider) VersionInfo() (string, error) {
	out, err := exec.Command("opencode", "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run 'opencode --version': %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MinVersion returns the minimum OpenCode CLI version required. 1.17.9 is the
// earliest version against which every Phase 1 ACP behavior (initialize with
// protocolVersion 1, session/new, session/prompt, session/update streaming)
// has been verified.
func (p *Provider) MinVersion() [3]int { return [3]int{1, 17, 9} }

// EnforcesMinVersion reports that OpenCode must meet MinVersion() to be ready at
// startup. Because the Phase 1 ACP wire behavior is only verified at or above
// 1.17.9, a too-old CLI is filtered out of the ready provider set (and thus out
// of explicit "opencode:" routing) rather than left selectable with a warning.
func (p *Provider) EnforcesMinVersion() bool { return true }

// EnvVarsToExclude returns nil — OpenCode needs no env stripping.
func (p *Provider) EnvVarsToExclude() []string { return nil }

// ComputeCost returns 0. OpenCode pricing is unknown during Phase 1 and is
// treated as zero rather than guessed; cost accounting arrives in a later phase.
func (p *Provider) ComputeCost(model string, inputTokens, outputTokens int64) float64 {
	return 0
}

// ContextWindowForModel returns 0. The backend context window is not discovered
// during Phase 1; callers treat zero as "unknown" without corrupting behavior.
func (p *Provider) ContextWindowForModel(model string) int { return 0 }

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

	out, err := runner(ctx, "opencode", []string{"models"}, nil)
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
