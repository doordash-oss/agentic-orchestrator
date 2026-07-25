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

// Package autoreview implements the default-off automatic Bash reviewer: one
// isolated, tool-less, fully ephemeral hidden classification through Claude,
// OpenCode, or Codex. It resolves one deterministic reviewer from the active
// catalogs and runs a single conservative ALLOW/DEFER classification over a
// minimal execution context, bypassing the session manager so the reviewer is
// absent from session listings, attach streams, recovery state, durable
// message logs, PID files, and raw provider output logs.
package autoreview

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Decision is the result of one hidden classification. Only exact ALLOW and
// DEFER tokens are successful classifications; every other outcome is a
// review failure that the caller converts to ordinary human deferral.
type Decision string

const (
	// Allow means the reviewer approved automatic execution.
	Allow Decision = "ALLOW"
	// Defer means the reviewer deferred to human approval.
	Defer Decision = "DEFER"
)

// defaultTimeout bounds the whole hidden attempt — launch, handshake,
// response, cancellation, and cleanup — to one low-effort turn. Tests inject
// a shorter value via ClassifyRequest.Timeout so concurrent sessions stay
// independent and no mutable package-global state is needed.
const defaultTimeout = 10 * time.Second

// gracePeriod is the SIGTERM-to-SIGKILL escalation window. It is part of the
// overall timeout budget, not additional to it: when the deadline context
// expires the escalation skips straight to SIGKILL.
const gracePeriod = 2 * time.Second

// Reviewer is a resolved automatic reviewer: the ready provider plus the
// canonical bare model id to pass to BuildCommand.
type Reviewer struct {
	Provider llm.LLMProvider
	Model    string
}

// ResolveReviewer resolves the workspace automatic-review selection against
// active, catalog-present providers that explicitly attest native tool-less
// execution. Automatic selection follows the fixed Claude, OpenCode, Codex
// order and deterministic provider-local cheap-model preferences. Explicit
// selection resolves only the configured provider/model (or a uniquely
// resolvable bare selector) and never substitutes.
func ResolveReviewer(registry *llm.Registry, model string) (Reviewer, bool) {
	if registry == nil {
		return Reviewer{}, false
	}
	selector := strings.TrimSpace(model)
	if selector == "" {
		return resolveAutomaticReviewer(registry)
	}
	return resolveExplicitReviewer(registry, selector)
}

var automaticProviderOrder = []string{"claude", "opencode", "codex"}

type reviewCandidate struct {
	model llm.ModelInfo
	band  int
}

func resolveAutomaticReviewer(registry *llm.Registry) (Reviewer, bool) {
	providers := eligibleReviewerProviders(registry)
	for _, name := range automaticProviderOrder {
		provider := providers[name]
		if provider == nil {
			continue
		}
		catalog := reviewerCatalog(provider)
		candidates := make([]reviewCandidate, 0, len(catalog))
		for _, model := range catalog {
			if band, ok := automaticPreferenceBand(name, model); ok {
				candidates = append(candidates, reviewCandidate{model: model, band: band})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if a.band != b.band {
				return a.band < b.band
			}
			aKnown, bKnown := a.model.ContextWindow > 0, b.model.ContextWindow > 0
			if aKnown != bKnown {
				return aKnown
			}
			if aKnown && a.model.ContextWindow != b.model.ContextWindow {
				return a.model.ContextWindow < b.model.ContextWindow
			}
			return strings.ToLower(a.model.ID) < strings.ToLower(b.model.ID)
		})
		return Reviewer{Provider: provider, Model: candidates[0].model.ID}, true
	}
	return Reviewer{}, false
}

func resolveExplicitReviewer(registry *llm.Registry, selector string) (Reviewer, bool) {
	providers := eligibleReviewerProviders(registry)
	if idx := strings.IndexByte(selector, ':'); idx > 0 {
		if provider := providers[selector[:idx]]; provider != nil {
			if canonical, ok := catalogModel(provider, selector[idx+1:]); ok {
				return Reviewer{Provider: provider, Model: canonical}, true
			}
			return Reviewer{}, false
		}
	}

	type match struct {
		provider llm.LLMProvider
		model    string
	}
	matches := make(map[string]match)
	for _, provider := range providers {
		if canonical, ok := catalogModel(provider, selector); ok {
			key := provider.Name() + "\x00" + strings.ToLower(canonical)
			matches[key] = match{provider: provider, model: canonical}
		}
	}
	if len(matches) != 1 {
		return Reviewer{}, false
	}
	for _, matched := range matches {
		return Reviewer{Provider: matched.provider, Model: matched.model}, true
	}
	return Reviewer{}, false
}

func eligibleReviewerProviders(registry *llm.Registry) map[string]llm.LLMProvider {
	result := make(map[string]llm.LLMProvider, len(automaticProviderOrder))
	for _, provider := range registry.DetectedProviders() {
		if !isBuiltinReviewerProvider(provider.Name()) {
			continue
		}
		capability, ok := provider.(llm.NativeToollessReviewer)
		if !ok || !capability.SupportsNativeToollessReview() || len(reviewerCatalog(provider)) == 0 {
			continue
		}
		if provider.Name() == "claude" {
			if checker, ok := provider.(llm.BareAuthChecker); ok && !checker.CheckBareAuth() {
				continue
			}
		}
		result[provider.Name()] = provider
	}
	return result
}

func isBuiltinReviewerProvider(name string) bool {
	for _, candidate := range automaticProviderOrder {
		if name == candidate {
			return true
		}
	}
	return false
}

func reviewerCatalog(provider llm.LLMProvider) []llm.ModelInfo {
	catalogProvider, ok := provider.(llm.CatalogProvider)
	if !ok {
		return nil
	}
	return catalogProvider.ModelCatalog()
}

func catalogModel(provider llm.LLMProvider, selector string) (string, bool) {
	var found string
	for _, model := range reviewerCatalog(provider) {
		matches := strings.EqualFold(model.ID, selector)
		for _, alias := range model.Aliases {
			matches = matches || strings.EqualFold(alias, selector)
		}
		if !matches {
			continue
		}
		if found != "" && !strings.EqualFold(found, model.ID) {
			return "", false
		}
		found = model.ID
	}
	return found, found != ""
}

func automaticPreferenceBand(provider string, model llm.ModelInfo) (int, bool) {
	haiku := modelMatchesHint(model, "haiku")
	flash := modelMatchesHint(model, "flash")
	mini := modelMatchesHint(model, "mini") || modelMatchesHint(model, "shrink")
	switch provider {
	case "claude":
		if haiku {
			return 0, true
		}
		if model.Category == "cheap" {
			return 1, true
		}
	case "opencode":
		if haiku {
			return 0, true
		}
		if flash {
			return 1, true
		}
		if model.Category == "cheap" {
			return 2, true
		}
	case "codex":
		if model.Category == "cheap" {
			return 0, true
		}
		if mini {
			return 1, true
		}
	}
	return 0, false
}

func modelMatchesHint(model llm.ModelInfo, hint string) bool {
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

// RestoreReviewer reconstructs a Reviewer from a snapshotted provider name
// and bare model id. It looks up the provider by name in the registry and
// returns a Reviewer with the snapshotted model. If the provider is no longer
// available, the registry is nil, or either identity field is empty, it
// returns an empty Reviewer (the caller's decorator will defer to the human
// prompt, matching the original session's no-reviewer behavior). This is used
// by crash-resume so the resumed session retains the original session's
// resolved reviewer rather than re-resolving against the current (possibly
// changed) provider/catalog state.
func RestoreReviewer(registry *llm.Registry, providerName, model string) Reviewer {
	if registry == nil || providerName == "" || model == "" {
		return Reviewer{}
	}
	for _, p := range registry.DetectedProviders() {
		if p.Name() == providerName {
			return Reviewer{Provider: p, Model: model}
		}
	}
	return Reviewer{}
}

// ReviewerIdentity returns the provider name and bare model id of a resolved
// reviewer, suitable for storing in a snapshot. Returns empty strings when
// no reviewer was resolved.
func (r Reviewer) Identity() (providerName, model string) {
	if r.Provider == nil {
		return "", ""
	}
	return r.Provider.Name(), r.Model
}

// ClassifyRequest is the minimal execution context for one classification. It
// carries exactly the canonical tool name, exact command, working directory,
// and declared writable roots — no conversation history, role prompt,
// environment values, file contents, or repository metadata. Timeout, when
// positive, overrides the default whole-attempt bound; zero uses
// defaultTimeout.
type ClassifyRequest struct {
	ToolName      string
	Command       string
	WorkDir       string
	WritableRoots []string
	Timeout       time.Duration
}

// attemptOutcome is the terminal outcome of a classification attempt,
// produced solely by the reader goroutine and communicated through a
// channel. The caller owns the final decision: a handshake error is
// terminal and overrides any reader result.
type attemptOutcome int

const (
	outcomeFailure attemptOutcome = iota
	outcomeSuccess
)

// readResult is the reader goroutine's terminal output: the outcome and
// the accumulated assistant text. It is sent through a buffered channel so
// the reader never blocks, and the caller selects on that channel or the
// deadline — no shared mutable state crosses the goroutine boundary.
type readResult struct {
	outcome attemptOutcome
	text    string
}

// Classify runs one isolated, tool-less, fully ephemeral hidden classification.
// It launches the resolved provider CLI directly (bypassing the session
// manager), wires a throwaway native protocol, sends the static review policy
// plus the minimal execution context, and reads exactly one low-effort turn.
//
// The whole attempt is bounded by the request timeout (or defaultTimeout).
// Every control_request (tool permission, hook, question, or any other
// interaction) is denied through the defensive deny-all boundary and fails
// the review immediately. The handshake error and any non-success result
// (error, refusal, truncation, max_turns) also fail the review. After
// trimming only surrounding whitespace, exactly case-sensitive ALLOW or
// DEFER is a successful classification; alternate casing, prose, fences,
// empty output, refusal, truncation, timeout, cancellation, and malformed
// output all fail. Raw reviewer output is discarded after parsing. On any
// termination path the subprocess is reaped synchronously. Returns ok=false
// for every failure; the caller converts that to ordinary human deferral.
func Classify(ctx context.Context, reviewer Reviewer, req ClassifyRequest) (Decision, bool) {
	if reviewer.Provider == nil || reviewer.Model == "" {
		return "", false
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args, env, cleanup, err := buildReviewCommand(reviewer, req)
	if err != nil || len(args) == 0 {
		if cleanup != nil {
			cleanup()
		}
		return "", false
	}
	defer cleanup()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = req.WorkDir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false
	}
	if err := cmd.Start(); err != nil {
		return "", false
	}

	proto := reviewer.Provider.NewProtocol(llm.ProtocolOpts{
		Model:                reviewer.Model,
		WorkDir:              req.WorkDir,
		InitialPrompt:        reviewPrompt(req),
		WritableRoots:        req.WritableRoots,
		NativeToollessReview: true,
	})
	proto.SetStdin(stdin)

	// The reader goroutine parses stdout and sends its terminal result
	// through a buffered channel. readDone signals that all stdout reads
	// have completed, which terminateAndWait needs before reaping the
	// process. No shared mutable state crosses the goroutine boundary.
	readCh := make(chan readResult, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		result := readResult{outcome: outcomeFailure}
		var assistantText strings.Builder
		defer func() {
			result.text = assistantText.String()
			readCh <- result
		}()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			msgs, perr := proto.ParseLine(scanner.Bytes())
			if perr != nil {
				return
			}
			for _, msg := range msgs {
				if unexpectedReviewActivity(msg) {
					return
				}
				// Defensive deny-all boundary: any control_request (tool
				// permission, hook callback, AskUserQuestion, or other
				// interaction) is denied and immediately fails the review.
				// The reader returns, so a subsequent ALLOW can never
				// bypass the deny-all boundary.
				if msg.ControlRequest != nil {
					_ = proto.RespondToControl(msg.ControlRequest.RequestID, false, nil, "denied: automatic review exposes no tools")
					return
				}
				if msg.Assistant != nil {
					appendAssistantText(&assistantText, msg.Assistant)
				}
				if msg.Result != nil {
					if msg.Result.IsSuccess() && !msg.Result.IsTurnTruncated() && msg.Result.StopReason != "refusal" {
						result.outcome = outcomeSuccess
					}
					return
				}
			}
		}
	}()

	// Send the initialize request and review prompt. A handshake error is
	// a terminal caller-owned result: it cannot be overwritten by a later
	// reader result, preventing a fail-open restoration.
	handshakeErr := proto.Handshake(deadlineCtx)

	// Wait for the reader to complete or the deadline to fire.
	var rr readResult
	select {
	case rr = <-readCh:
	case <-deadlineCtx.Done():
	}

	_ = stdin.Close()
	terminateAndWait(deadlineCtx, cmd, readDone)

	// A handshake error takes precedence over any reader result.
	if handshakeErr != nil {
		rr.outcome = outcomeFailure
	}

	if rr.outcome != outcomeSuccess {
		return "", false
	}
	return parseDecision(rr.text)
}

func unexpectedReviewActivity(msg llm.SDKMessage) bool {
	if msg.User != nil ||
		msg.ToolProgress != nil ||
		msg.HookStarted != nil ||
		msg.HookProgress != nil ||
		msg.HookResponse != nil ||
		msg.Compact != nil ||
		msg.TaskStarted != nil ||
		msg.TaskProgress != nil ||
		msg.TaskNotification != nil ||
		len(msg.FileReads) > 0 ||
		len(msg.FileChanges) > 0 ||
		msg.StreamDeltaType == "input_json" {
		return true
	}
	if msg.Assistant == nil {
		return false
	}
	for _, block := range msg.Assistant.Message.Content {
		if !block.IsText() && !block.IsThinking() {
			return true
		}
	}
	return false
}

// buildReviewCommand constructs the native CLI command with zero tools, no
// session persistence, and no customization injection. Each provider maps
// those options to its audited boundary. An isolated config directory prevents
// user-level Claude configuration from loading; OpenCode and Codex provide
// their own isolated environment entries and cleanup through BuildCommand.
func buildReviewCommand(reviewer Reviewer, req ClassifyRequest) (args []string, env []string, cleanup func(), err error) {
	configDir, mErr := os.MkdirTemp("", "autoreview-claude-config-*")
	if mErr != nil {
		return nil, nil, nil, mErr
	}
	cleanup = func() { _ = os.RemoveAll(configDir) }

	cmdArgs, cmdEnv, bErr := reviewer.Provider.BuildCommand(llm.CommandBuildOpts{
		Model:                reviewer.Model,
		EffortLevel:          llm.EffortLow,
		ZeroTools:            true,
		NoSessionPersistence: true,
		NoCustomization:      true,
		StateDir:             configDir,
		WorkDir:              req.WorkDir,
		WritableRoots:        req.WritableRoots,
	})
	if bErr != nil || len(cmdArgs) == 0 {
		cleanup()
		return nil, nil, nil, fmt.Errorf("build command: %w", bErr)
	}

	// Build an isolated environment: inherit the parent environment minus
	// provider-excluded vars, then set CLAUDE_CONFIG_DIR to the temp dir so
	// the reviewer ignores ~/.claude settings, CLAUDE.md project files, and
	// user customizations.
	env = buildIsolatedEnv(reviewer.Provider, configDir, cmdEnv)
	return cmdArgs, env, cleanup, nil
}

// buildIsolatedEnv returns the subprocess environment for the hidden
// reviewer: the parent environment with provider-excluded prefixes stripped
// and CLAUDE_CONFIG_DIR redirected to an empty temp directory so no
// project/user Claude configuration is loaded.
func buildIsolatedEnv(provider llm.LLMProvider, configDir string, providerEnv []string) []string {
	excludePrefixes := provider.EnvVarsToExclude()
	overrides := make(map[string]struct{}, len(providerEnv)+1)
	overrides["CLAUDE_CONFIG_DIR"] = struct{}{}
	for _, kv := range providerEnv {
		if key, _, ok := strings.Cut(kv, "="); ok && key != "" {
			overrides[key] = struct{}{}
		}
	}
	base := os.Environ()
	env := make([]string, 0, len(base)+len(providerEnv)+1)
	for _, kv := range base {
		if hasExcludedPrefix(kv, excludePrefixes) {
			continue
		}
		key, _, _ := strings.Cut(kv, "=")
		if _, overridden := overrides[key]; overridden {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	env = append(env, providerEnv...)
	return env
}

func hasExcludedPrefix(kv string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}

// parseDecision trims only surrounding whitespace and accepts exactly
// case-sensitive ALLOW or DEFER. Everything else fails.
func parseDecision(text string) (Decision, bool) {
	trimmed := strings.TrimSpace(text)
	switch trimmed {
	case "ALLOW":
		return Allow, true
	case "DEFER":
		return Defer, true
	default:
		return "", false
	}
}

func appendAssistantText(b *strings.Builder, msg *llm.AssistantMessage) {
	if msg == nil {
		return
	}
	for _, block := range msg.Message.Content {
		if block.Type == "text" && block.Text != "" {
			b.WriteString(block.Text)
		}
	}
}

// reviewPrompt builds the static safety policy plus exactly the canonical tool
// name, exact command, working directory, and declared writable roots.
func reviewPrompt(req ClassifyRequest) string {
	var b strings.Builder
	b.WriteString("You are a conservative command-safety reviewer. Decide whether a single tool command should run automatically without human approval.\n\n")
	b.WriteString("Reply with exactly one token on one line: ALLOW or DEFER.\n")
	b.WriteString("- ALLOW: the command is safe to run automatically.\n")
	b.WriteString("- DEFER: a human should approve it.\n")
	b.WriteString("Do not use any tools. Do not explain. Output only the token.\n\n")
	fmt.Fprintf(&b, "Tool: %s\n", req.ToolName)
	fmt.Fprintf(&b, "Command: %s\n", req.Command)
	fmt.Fprintf(&b, "Working directory: %s\n", req.WorkDir)
	fmt.Fprintf(&b, "Writable roots: %s\n", strings.Join(req.WritableRoots, ", "))
	return b.String()
}

// terminateAndWait terminates and reaps the hidden subprocess synchronously.
// The reviewer is launched with Setpgid so the negative pid targets the whole
// group. SIGTERM is sent immediately; if the process has not exited within
// the grace period (or the overall deadline expires, whichever comes first),
// SIGKILL is sent.
//
// Process completion is tracked separately from stdout-drain completion.
// A dedicated goroutine owns the single cmd.Wait call: it waits for readDone
// (all pipe reads done), then calls cmd.Wait (which reaps the process and
// closes pipes), then signals waitDone. The select escalates against actual
// process completion, not stdout drain — so a child that closes stdout but
// stays alive cannot bypass the grace/deadline escalation, and SIGKILL is
// always sent while the PID is still valid (before cmd.Wait reaps it).
func terminateAndWait(ctx context.Context, cmd *exec.Cmd, readDone <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	waitDone := make(chan struct{})
	go func() {
		<-readDone
		_ = cmd.Wait()
		close(waitDone)
	}()

	timer := time.NewTimer(gracePeriod)
	defer timer.Stop()

	select {
	case <-waitDone:
	case <-timer.C:
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitDone
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-waitDone
	}
}
