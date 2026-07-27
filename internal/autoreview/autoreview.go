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
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	agentprompts "github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
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

// Outcome is the stable terminal classification for an automatic-review
// decision, including deterministic fast-path approvals that run no model.
type Outcome string

const (
	OutcomeFastPath              Outcome = "fast_path"
	OutcomeAllow                 Outcome = "allow"
	OutcomeDefer                 Outcome = "defer"
	OutcomeTimeout               Outcome = "timeout"
	OutcomeMalformedResponse     Outcome = "malformed_response"
	OutcomeProviderError         Outcome = "provider_error"
	OutcomeUnexpectedInteraction Outcome = "unexpected_interaction"
	OutcomeCanceled              Outcome = "canceled"
)

// Result is the typed, bounded-input-free result of one actual review attempt.
// FailureReason is a generic classification reason and never contains model
// output, prompt content, command text, environment values, or file contents.
type Result struct {
	Decision      Decision
	Outcome       Outcome
	FailureReason string
	Timing        Timing
}

// Timing records cumulative, provider-neutral milestones for one review
// attempt. Zero means the attempt did not reach that milestone.
type Timing struct {
	Launch      time.Duration
	FirstOutput time.Duration
	Completion  time.Duration
}

// defaultTimeout bounds the whole hidden attempt — launch, handshake,
// response, cancellation, and cleanup — to one low-effort turn. Tests inject
// a shorter value via ClassifyRequest.Timeout so concurrent sessions stay
// independent and no mutable package-global state is needed.
const defaultTimeout = 30 * time.Second

const maxProviderAttempts = 2

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
func ResolveReviewer(registry *llm.Registry, model string) (Reviewer, bool, string) {
	if registry == nil {
		return Reviewer{}, false, "provider registry unavailable"
	}
	selector := strings.TrimSpace(model)
	if selector == "" {
		return resolveAutomaticReviewer(registry)
	}
	selector = llm.StripModelContextWindow(selector)
	return resolveExplicitReviewer(registry, selector)
}

var automaticProviderOrder = []string{"claude", "opencode", "codex"}

type reviewCandidate struct {
	model llm.ModelInfo
	band  int
}

func resolveAutomaticReviewer(registry *llm.Registry) (Reviewer, bool, string) {
	providers, eligibility := eligibleReviewerProviders(registry)
	for _, name := range automaticProviderOrder {
		provider := providers[name]
		if provider == nil {
			continue
		}
		catalog := reviewerCatalog(provider)
		candidates := make([]reviewCandidate, 0, len(catalog))
		for _, model := range catalog {
			if band, ok := reviewPreferenceBand(provider, model); ok {
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
		return Reviewer{Provider: provider, Model: candidates[0].model.ID}, true, ""
	}
	switch {
	case eligibility.detected == 0:
		return Reviewer{}, false, "no supported reviewer provider is available"
	case len(providers) == 0:
		return Reviewer{}, false, "no detected provider supports isolated tool-less review"
	default:
		return Reviewer{}, false, "eligible reviewer catalogs contain no preferred review model"
	}
}

func resolveExplicitReviewer(registry *llm.Registry, selector string) (Reviewer, bool, string) {
	selector = llm.StripModelContextWindow(strings.TrimSpace(selector))
	providers, eligibility := eligibleReviewerProviders(registry)
	if idx := strings.IndexByte(selector, ':'); idx > 0 {
		providerName := selector[:idx]
		if provider := providers[providerName]; provider != nil {
			if canonical, ok := catalogModel(provider, selector[idx+1:]); ok {
				return Reviewer{Provider: provider, Model: canonical}, true, ""
			}
			return Reviewer{}, false, fmt.Sprintf("configured automatic-review model %q is unknown for provider %q", selector[idx+1:], providerName)
		}
		if rejection := eligibility.rejections[providerName]; rejection != "" {
			return Reviewer{}, false, fmt.Sprintf("automatic-review provider %q unavailable: %s", providerName, rejection)
		}
		return Reviewer{}, false, fmt.Sprintf("configured automatic-review provider %q is unavailable", providerName)
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
		if len(matches) == 0 {
			return Reviewer{}, false, fmt.Sprintf("configured automatic-review model %q is unavailable", selector)
		}
		return Reviewer{}, false, fmt.Sprintf("configured automatic-review model %q is ambiguous across providers", selector)
	}
	for _, matched := range matches {
		return Reviewer{Provider: matched.provider, Model: matched.model}, true, ""
	}
	return Reviewer{}, false, fmt.Sprintf("configured automatic-review model %q is unavailable", selector)
}

type reviewerEligibility struct {
	detected   int
	rejections map[string]string
}

func eligibleReviewerProviders(registry *llm.Registry) (map[string]llm.LLMProvider, reviewerEligibility) {
	result := make(map[string]llm.LLMProvider, len(automaticProviderOrder))
	eligibility := reviewerEligibility{rejections: make(map[string]string)}
	for _, provider := range registry.DetectedProviders() {
		if !isBuiltinReviewerProvider(provider.Name()) {
			continue
		}
		eligibility.detected++
		capability, ok := provider.(llm.NativeToollessReviewer)
		if !ok || !capability.SupportsNativeToollessReview() {
			eligibility.rejections[provider.Name()] = "isolated tool-less review is not supported"
			continue
		}
		if len(reviewerCatalog(provider)) == 0 {
			eligibility.rejections[provider.Name()] = "reviewer model catalog is empty"
			continue
		}
		result[provider.Name()] = provider
	}
	return result, eligibility
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

func reviewPreferenceBand(provider llm.LLMProvider, model llm.ModelInfo) (int, bool) {
	if ranker, ok := provider.(llm.ReviewModelRanker); ok {
		return ranker.ReviewPreferenceBand(model)
	}
	if model.Category == "cheap" {
		return 0, true
	}
	return 0, false
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

// readResult is the reader goroutine's terminal output: the outcome and
// the accumulated assistant text. It is sent through a buffered channel so
// the reader never blocks, and the caller selects on that channel or the
// deadline — no shared mutable state crosses the goroutine boundary.
type readResult struct {
	outcome     Outcome
	reason      string
	retryClass  string
	text        string
	firstOutput time.Duration
	successful  bool
}

type attemptResult struct {
	result    Result
	retryable bool
}

// Classify runs one isolated, tool-less, fully ephemeral hidden classification.
// It launches the resolved provider CLI directly (bypassing the session
// manager), wires a throwaway native protocol, sends the static review policy
// plus the minimal execution context, and reads exactly one low-effort turn.
//
// One transient launch, handshake, transport, rate-limit, or server failure is
// retried after a short jitter. Both attempts share the request timeout (or
// defaultTimeout), so retry never extends the human-facing wait bound.
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
func ClassifyDetailed(ctx context.Context, reviewer Reviewer, req ClassifyRequest) (result Result) {
	startedAt := time.Now()
	var timing Timing
	defer func() {
		timing.Completion = time.Since(startedAt)
		result.Timing = timing
	}()

	if reviewer.Provider == nil || reviewer.Model == "" {
		return failedResult(OutcomeProviderError, "reviewer unavailable")
	}
	if err := ctx.Err(); err != nil {
		return contextFailureResult(err)
	}
	prompt, err := reviewPrompt(req)
	if err != nil {
		return failedResult(OutcomeProviderError, "review prompt nonce generation failed")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for attempt := 1; attempt <= maxProviderAttempts; attempt++ {
		attempted := classifyAttempt(ctx, deadlineCtx, reviewer, req, prompt, startedAt)
		mergeTiming(&timing, attempted.result.Timing)
		result = attempted.result
		if !attempted.retryable || attempt == maxProviderAttempts {
			return result
		}
		if !waitForProviderRetry(deadlineCtx) {
			if err := ctx.Err(); err != nil {
				return contextFailureResult(err)
			}
			return failedResult(OutcomeTimeout, "review attempt timed out")
		}
	}
	return result
}

func classifyAttempt(
	parentCtx context.Context,
	deadlineCtx context.Context,
	reviewer Reviewer,
	req ClassifyRequest,
	prompt string,
	startedAt time.Time,
) attemptResult {
	args, env, cleanup, err := buildReviewCommand(reviewer, req)
	if err != nil || len(args) == 0 {
		if cleanup != nil {
			cleanup()
		}
		return attemptResult{result: failedResult(OutcomeProviderError, "provider command build failed")}
	}
	defer cleanup()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = req.WorkDir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return attemptResult{result: failedResult(OutcomeProviderError, "provider stdin setup failed")}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return attemptResult{result: failedResult(OutcomeProviderError, "provider stdout setup failed")}
	}
	if err := cmd.Start(); err != nil {
		return attemptResult{
			result:    failedResult(OutcomeProviderError, "provider launch failed"),
			retryable: retryableLaunchError(err),
		}
	}
	timing := Timing{Launch: time.Since(startedAt)}

	proto := reviewer.Provider.NewProtocol(llm.ProtocolOpts{
		Model:                reviewer.Model,
		WorkDir:              req.WorkDir,
		InitialPrompt:        prompt,
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
		result := readResult{outcome: OutcomeProviderError, reason: "provider ended without a successful result"}
		var assistantText strings.Builder
		defer func() {
			result.text = assistantText.String()
			readCh <- result
		}()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			if result.firstOutput == 0 {
				result.firstOutput = time.Since(startedAt)
			}
			msgs, perr := proto.ParseLine(scanner.Bytes())
			if perr != nil {
				result.outcome = OutcomeMalformedResponse
				result.reason = "provider protocol response was malformed"
				return
			}
			for _, msg := range msgs {
				if unexpectedReviewActivity(msg) {
					result.outcome = OutcomeUnexpectedInteraction
					result.reason = "reviewer attempted an unexpected interaction"
					return
				}
				// Defensive deny-all boundary: any control_request (tool
				// permission, hook callback, AskUserQuestion, or other
				// interaction) is denied and immediately fails the review.
				// The reader returns, so a subsequent ALLOW can never
				// bypass the deny-all boundary.
				if msg.ControlRequest != nil {
					_ = proto.RespondToControl(msg.ControlRequest.RequestID, false, nil, "denied: automatic review exposes no tools")
					result.outcome = OutcomeUnexpectedInteraction
					result.reason = "reviewer requested an interaction"
					return
				}
				if msg.Assistant != nil {
					appendAssistantText(&assistantText, msg.Assistant)
				}
				if msg.Result != nil {
					if msg.Result.IsSuccess() && !msg.Result.IsTurnTruncated() && msg.Result.StopReason != "refusal" {
						result.successful = true
						result.reason = ""
					} else {
						result.retryClass = retryableProviderFailureClass(msg.Result.Result)
						if result.retryClass != "" {
							result.reason = "provider returned a retryable " + result.retryClass + " failure"
						} else {
							result.reason = "provider returned an unsuccessful review result"
						}
					}
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			result.outcome = OutcomeMalformedResponse
			result.reason = "provider response stream was malformed"
		}
	}()

	// Send the initialize request and review prompt. A handshake error is
	// a terminal caller-owned result: it cannot be overwritten by a later
	// reader result, preventing a fail-open restoration.
	handshakeErr := proto.Handshake(deadlineCtx)

	// Wait for the reader to complete or the deadline to fire.
	var rr readResult
	timedOut := false
	select {
	case rr = <-readCh:
	case <-deadlineCtx.Done():
		timedOut = true
	}

	_ = stdin.Close()
	terminateAndWait(deadlineCtx, cmd, readDone)
	if timedOut {
		rr = <-readCh
	}
	timing.FirstOutput = rr.firstOutput

	// A handshake error takes precedence over any reader result.
	if err := parentCtx.Err(); err != nil {
		return attemptResult{result: contextFailureResult(err)}
	}
	if timedOut || deadlineCtx.Err() == context.DeadlineExceeded {
		return attemptResult{result: Result{
			Outcome:       OutcomeTimeout,
			FailureReason: "review attempt timed out",
			Timing:        timing,
		}}
	}
	if handshakeErr != nil {
		return attemptResult{
			result: Result{
				Outcome:       OutcomeProviderError,
				FailureReason: "provider handshake failed",
				Timing:        timing,
			},
			retryable: retryableHandshakeError(handshakeErr),
		}
	}
	if !rr.successful {
		return attemptResult{
			result: Result{
				Outcome:       rr.outcome,
				FailureReason: rr.reason,
				Timing:        timing,
			},
			retryable: rr.outcome == OutcomeProviderError &&
				(rr.retryClass != "" || rr.reason == "provider ended without a successful result"),
		}
	}
	decision, ok := parseDecision(rr.text)
	if !ok {
		return attemptResult{result: Result{
			Outcome:       OutcomeMalformedResponse,
			FailureReason: "reviewer response was not an exact decision token",
			Timing:        timing,
		}}
	}
	if decision == Allow {
		return attemptResult{result: Result{Decision: decision, Outcome: OutcomeAllow, Timing: timing}}
	}
	return attemptResult{result: Result{Decision: decision, Outcome: OutcomeDefer, Timing: timing}}
}

// Classify preserves the original decision/bool API for callers that do not
// need the typed operational outcome.
func Classify(ctx context.Context, reviewer Reviewer, req ClassifyRequest) (Decision, bool) {
	result := ClassifyDetailed(ctx, reviewer, req)
	return result.Decision, result.Outcome == OutcomeAllow || result.Outcome == OutcomeDefer
}

func mergeTiming(dst *Timing, src Timing) {
	if dst.Launch == 0 && src.Launch > 0 {
		dst.Launch = src.Launch
	}
	if dst.FirstOutput == 0 && src.FirstOutput > 0 {
		dst.FirstOutput = src.FirstOutput
	}
}

func waitForProviderRetry(ctx context.Context) bool {
	timer := time.NewTimer(providerRetryDelay())
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func providerRetryDelay() time.Duration {
	const (
		minDelay = 250 * time.Millisecond
		jitter   = 100 * time.Millisecond
	)
	var sample [1]byte
	if _, err := cryptorand.Read(sample[:]); err != nil {
		return minDelay + jitter/2
	}
	return minDelay + time.Duration(sample[0])*jitter/255
}

func retryableLaunchError(err error) bool {
	return err != nil &&
		!errors.Is(err, os.ErrNotExist) &&
		!errors.Is(err, os.ErrPermission) &&
		!errors.Is(err, syscall.ENOEXEC) &&
		!errors.Is(err, syscall.ENOTDIR)
}

func retryableHandshakeError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"authentication",
		"credential",
		"unauthorized",
		"forbidden",
		"login",
		"approval policy",
		"configuration",
		"config",
		"invalid params",
		"invalid request",
		"protocol version",
		"not supported",
		"unsupported",
	} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	// Handshakes primarily perform local pipe writes and wait for the provider
	// to become ready. Unknown failures get one bounded retry; deterministic
	// auth, configuration, policy, and protocol errors are filtered above.
	return true
}

func retryableProviderFailureClass(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return ""
	}
	if containsStatusCode(lower, "401", "403") {
		return ""
	}
	for _, marker := range []string{
		"authentication",
		"credential",
		"unauthorized",
		"forbidden",
		"login",
		"quota exhausted",
		"approval policy",
		"configuration",
		"invalid config",
		"invalid model",
		"model not found",
		"invalid params",
		"invalid request",
		"not supported",
		"unsupported",
	} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	if containsStatusCode(lower, "429") {
		return "rate-limit"
	}
	for _, marker := range []string{"rate limit", "too many requests"} {
		if strings.Contains(lower, marker) {
			return "rate-limit"
		}
	}
	if containsStatusCode(lower, "500", "502", "503", "504", "529") {
		return "server"
	}
	for _, marker := range []string{
		"internal server error",
		"service unavailable",
		"server error",
		"overloaded",
		"temporarily unavailable",
	} {
		if strings.Contains(lower, marker) {
			return "server"
		}
	}
	for _, marker := range []string{
		"connection",
		"transport",
		"network",
		"broken pipe",
		"reset by peer",
		"unexpected eof",
		"timed out",
		"timeout",
	} {
		if strings.Contains(lower, marker) {
			return "transport"
		}
	}
	return ""
}

func containsStatusCode(text string, codes ...string) bool {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return r < '0' || r > '9'
	}) {
		for _, code := range codes {
			if field == code {
				return true
			}
		}
	}
	return false
}

func failedResult(outcome Outcome, reason string) Result {
	return Result{Outcome: outcome, FailureReason: reason}
}

func contextFailureResult(err error) Result {
	if err == context.DeadlineExceeded {
		return failedResult(OutcomeTimeout, "review attempt timed out")
	}
	return failedResult(OutcomeCanceled, "review attempt canceled")
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
// those options to its audited boundary. A provider-scoped state directory
// gives each provider a private location for its isolated configuration and
// credentials.
func buildReviewCommand(reviewer Reviewer, req ClassifyRequest) (args []string, env []string, cleanup func(), err error) {
	configDir, mErr := os.MkdirTemp("", "autoreview-"+providerTempKey(reviewer.Provider.Name())+"-*")
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
	if bErr != nil {
		cleanup()
		return nil, nil, nil, fmt.Errorf("build command: %w", bErr)
	}
	if len(cmdArgs) == 0 {
		cleanup()
		return nil, nil, nil, errors.New("build command: provider returned no command")
	}

	// Inherit the parent environment minus provider-excluded vars. OpenCode and
	// Codex add their provider-specific isolated state through BuildCommand;
	// Claude safe mode isolates customization while retaining its auth state.
	env = buildIsolatedEnv(reviewer.Provider, cmdEnv)
	return cmdArgs, env, cleanup, nil
}

func providerTempKey(name string) string {
	var key strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			key.WriteRune(r)
		default:
			key.WriteByte('-')
		}
	}
	if key.Len() == 0 {
		return "provider"
	}
	return key.String()
}

// buildIsolatedEnv returns the subprocess environment for the hidden reviewer:
// the parent environment with provider-excluded prefixes stripped and
// provider-specific overrides applied. Provider launch flags enforce the
// no-customization boundary without replacing authentication state.
func buildIsolatedEnv(provider llm.LLMProvider, providerEnv []string) []string {
	excludePrefixes := provider.EnvVarsToExclude()
	overrides := make(map[string]struct{}, len(providerEnv))
	for _, kv := range providerEnv {
		if key, _, ok := strings.Cut(kv, "="); ok && key != "" {
			overrides[key] = struct{}{}
		}
	}
	base := os.Environ()
	env := make([]string, 0, len(base)+len(providerEnv))
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
// name, command, working directory, and declared writable roots. A fresh
// cryptographic nonce fences the sanitized request data so command text cannot
// forge the surrounding prompt structure.
func reviewPrompt(req ClassifyRequest) (string, error) {
	var nonceBytes [16]byte
	if _, err := cryptorand.Read(nonceBytes[:]); err != nil {
		return "", err
	}
	return reviewPromptWithNonce(req, hex.EncodeToString(nonceBytes[:])), nil
}

func reviewPromptWithNonce(req ClassifyRequest, nonce string) string {
	toolName := sanitizeReviewPromptField(req.ToolName)
	command := sanitizeReviewPromptField(req.Command)
	workDir := sanitizeReviewPromptField(req.WorkDir)
	writableRoots := make([]string, 0, len(req.WritableRoots))
	for _, root := range req.WritableRoots {
		writableRoots = append(writableRoots, sanitizeReviewPromptField(root))
	}
	writableRootsSummary := strings.Join(writableRoots, ", ")
	if writableRootsSummary == "" {
		writableRootsSummary = "none (writes are not authorized; reads are not limited by this field)"
	} else {
		writableRootsSummary += " (writes may target only these roots; reads are not limited by this field)"
	}

	return agentprompts.AutoReviewUserPrompt(agentprompts.AutoReviewUserInput{
		Nonce:                nonce,
		ToolName:             toolName,
		Command:              command,
		WorkDir:              workDir,
		WritableRootsSummary: writableRootsSummary,
	})
}

func sanitizeReviewPromptField(value string) string {
	value = permission.StripUnsafeControlContent(value)
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) {
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
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
