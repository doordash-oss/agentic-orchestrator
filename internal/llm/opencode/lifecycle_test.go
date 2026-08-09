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

package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// --- usage normalization (Task 1) ---
//
// These tests use the REAL OpenCode ACP wire shapes (verified against
// packages/opencode/src/acp/{service,usage}.ts on the dev branch and the ACP
// session-usage / end-turn-token-usage RFDs):
//   - session/update "usage_update": {used, size, cost:{amount,currency}} —
//     tokens currently in context, the model context window, and the cumulative
//     session cost. Carries NO input/output/cache split.
//   - session/prompt result "usage": {totalTokens, inputTokens, outputTokens,
//     thoughtTokens?, cachedReadTokens?, cachedWriteTokens?} — the end-turn
//     token accounting; the only carrier of the input/output/cache breakdown.

// usageUpdateLine builds a session/update notification carrying an ACP
// usage_update (context fill, context window, and cumulative cost).
func usageUpdateLine(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	fields["sessionUpdate"] = "usage_update"
	return notificationLine(t, "session/update", map[string]any{
		"sessionId": "ses_x",
		"update":    fields,
	})
}

// promptResultLine builds a session/prompt response with the given stop reason
// and (optional) end-turn token usage.
func promptResultLine(t *testing.T, id int, stopReason string, usage map[string]any) []byte {
	t.Helper()
	result := map[string]any{"stopReason": stopReason}
	if usage != nil {
		result["usage"] = usage
	}
	return responseLine(t, id, result)
}

func lastUsageUpdate(t *testing.T, msgs []llm.SDKMessage) *llm.Usage {
	t.Helper()
	var u *llm.Usage
	for _, m := range msgs {
		if m.UsageUpdate != nil {
			u = m.UsageUpdate
		}
	}
	if u == nil {
		t.Fatalf("no usage_update in %+v", msgs)
	}
	return u
}

func terminalResult(t *testing.T, msgs []llm.SDKMessage) *llm.ResultMessage {
	t.Helper()
	for _, m := range msgs {
		if m.Result != nil {
			return m.Result
		}
	}
	t.Fatalf("no terminal result in %+v", msgs)
	return nil
}

// TestUsageUpdateCarriesContextAndCost proves a usage_update is normalized into a
// usage update SDK message whose context fill is `used` and context window is
// `size`, with the model's cumulative cost captured for the terminal result.
func TestUsageUpdateCarriesContextAndCost(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)

	u := lastUsageUpdate(t, mustParse(t, p, usageUpdateLine(t, map[string]any{
		"used": 51200, "size": 200000,
		"cost": map[string]any{"amount": 0.0425, "currency": "USD"},
	})))
	// Context fill is `used` directly (tokens currently in context), reported as a
	// total-token snapshot against the `size` window so the percentage is backed
	// by provider metadata, not a guess.
	if u.ContextTotalTokens != 51200 {
		t.Fatalf("context fill = %d, want 51200 (used)", u.ContextTotalTokens)
	}
	if u.ContextWindow != 200000 {
		t.Fatalf("context window = %d, want 200000 (size)", u.ContextWindow)
	}
	if u.ContextBaseline != 0 {
		t.Fatalf("context baseline = %d, want 0 (OpenCode has no fixed overhead)", u.ContextBaseline)
	}
	if u.CostUSD != 0.0425 {
		t.Fatalf("running cost = %v, want 0.0425", u.CostUSD)
	}

	// The cost rides through to the terminal result's TotalCostUSD.
	r := terminalResult(t, mustParse(t, p, promptResultLine(t, promptID, "end_turn", nil)))
	if !r.IsSuccess() {
		t.Fatalf("end_turn produced %+v, want success", r)
	}
	if r.TotalCostUSD != 0.0425 {
		t.Fatalf("terminal cost = %v, want 0.0425 (carried from usage_update.cost.amount)", r.TotalCostUSD)
	}
}

// TestResultUsageAccumulatesInputOutputCache proves the end-turn token accounting
// on the prompt result (the only carrier of the token split) is normalized onto
// the terminal result and emitted as a cumulative usage update so the session can
// accumulate it.
func TestResultUsageAccumulatesInputOutputCache(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, promptResultLine(t, promptID, "end_turn", map[string]any{
		"totalTokens": 4736, "inputTokens": 4096, "outputTokens": 512,
		"thoughtTokens": 128, "cachedReadTokens": 200, "cachedWriteTokens": 64,
	}))
	r := terminalResult(t, msgs)
	if !r.IsSuccess() {
		t.Fatalf("end_turn produced %+v, want success", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 4096 || r.Usage.OutputTokens != 512 {
		t.Fatalf("terminal usage = %+v, want 4096/512 from the prompt result usage", r.Usage)
	}
	if r.Usage.CacheReadInputTokens != 200 || r.Usage.CacheCreationInputTokens != 64 {
		t.Fatalf("terminal cache = read %d write %d, want 200/64", r.Usage.CacheReadInputTokens, r.Usage.CacheCreationInputTokens)
	}
	// A cumulative usage update is emitted alongside the result so the session's
	// SET-semantics accumulation receives the input/output/cache split (the
	// streamed usage_update carries no split).
	u := lastUsageUpdate(t, msgs)
	if u.InputTokens != 4096 || u.OutputTokens != 512 {
		t.Fatalf("emitted usage update = in %d out %d, want 4096/512", u.InputTokens, u.OutputTokens)
	}
	if r.TotalCostUSD != 0 {
		t.Fatalf("terminal cost = %v, want 0 (no cost emitted)", r.TotalCostUSD)
	}
}

// TestUsageUpdateThenResultCombinesContextCostAndTokens proves a streamed
// usage_update (context + cost) followed by a prompt result (token split) yields
// a terminal result that carries BOTH the accumulated tokens and the cost — the
// result token data is not lost just because a usage_update was already seen.
func TestUsageUpdateThenResultCombinesContextCostAndTokens(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)

	mustParse(t, p, usageUpdateLine(t, map[string]any{
		"used": 51200, "size": 200000,
		"cost": map[string]any{"amount": 0.07, "currency": "USD"},
	}))
	msgs := mustParse(t, p, promptResultLine(t, promptID, "end_turn", map[string]any{
		"totalTokens": 51200, "inputTokens": 50000, "outputTokens": 1000,
	}))
	r := terminalResult(t, msgs)
	if r.Usage == nil || r.Usage.InputTokens != 50000 || r.Usage.OutputTokens != 1000 {
		t.Fatalf("terminal usage = %+v, want 50000/1000 (result tokens preserved after usage_update)", r.Usage)
	}
	if r.Usage.ContextWindow != 200000 || r.Usage.ContextTotalTokens != 51200 {
		t.Fatalf("terminal context = total %d window %d, want 51200/200000 (from usage_update)", r.Usage.ContextTotalTokens, r.Usage.ContextWindow)
	}
	if r.TotalCostUSD != 0.07 {
		t.Fatalf("terminal cost = %v, want 0.07", r.TotalCostUSD)
	}
}

// TestLateDuplicateResultDoesNotDoubleCountUsage proves the first terminal seals
// the outcome and folds its usage once; a late/duplicate terminal result is
// suppressed and its usage is NOT re-folded, so the sealed token totals cannot be
// inflated. (Legitimate multi-turn summing happens across non-sealing question
// pauses, not across repeated sealing terminals.)
func TestLateDuplicateResultDoesNotDoubleCountUsage(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)

	first := lastUsageUpdate(t, mustParse(t, p, promptResultLine(t, promptID, "end_turn", map[string]any{
		"inputTokens": 100, "outputTokens": 50, "cachedReadTokens": 10, "cachedWriteTokens": 5,
	})))
	if first.InputTokens != 100 || first.OutputTokens != 50 {
		t.Fatalf("first cumulative = in %d out %d, want 100/50", first.InputTokens, first.OutputTokens)
	}

	// A late/duplicate terminal produces no messages and must not re-fold usage.
	if late := mustParse(t, p, promptResultLine(t, promptID, "end_turn", map[string]any{
		"inputTokens": 200, "outputTokens": 80,
	})); len(late) != 0 {
		t.Fatalf("late duplicate result produced %+v, want no messages", late)
	}

	// A subsequent usage_update reflects the still-unchanged cumulative totals,
	// proving the duplicate's tokens were not added.
	after := lastUsageUpdate(t, mustParse(t, p, usageUpdateLine(t, map[string]any{"used": 160, "size": 200000})))
	if after.InputTokens != 100 || after.OutputTokens != 50 {
		t.Fatalf("cumulative after duplicate = in %d out %d, want 100/50 (no double count)", after.InputTokens, after.OutputTokens)
	}
	if after.CacheReadInputTokens != 10 || after.CacheCreationInputTokens != 5 {
		t.Fatalf("cumulative cache = read %d write %d, want 10/5", after.CacheReadInputTokens, after.CacheCreationInputTokens)
	}
}

// TestTerminalResultZeroCostWithNoUsage proves that when OpenCode emits no token
// usage and no cost, the terminal result carries no fabricated usage block and
// zero cost — the zero-cost fallback the plan requires.
func TestTerminalResultZeroCostWithNoUsage(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	msgs := mustParse(t, p, promptResultLine(t, promptID, "end_turn", nil))
	if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsSuccess() {
		t.Fatalf("end_turn produced %+v, want a single success result", msgs)
	}
	if msgs[0].Result.Usage != nil {
		t.Fatalf("terminal usage = %+v, want nil (no usage emitted)", msgs[0].Result.Usage)
	}
	if msgs[0].Result.TotalCostUSD != 0 {
		t.Fatalf("terminal cost = %v, want 0", msgs[0].Result.TotalCostUSD)
	}
}

// TestMissingCostLeavesZeroWhilePreservingTokens proves that when a usage_update
// omits cost (the backend has no pricing) but the result carries tokens, the
// tokens are preserved while cost stays at the zero-cost fallback.
func TestMissingCostLeavesZeroWhilePreservingTokens(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	mustParse(t, p, usageUpdateLine(t, map[string]any{"used": 8000, "size": 128000}))
	r := terminalResult(t, mustParse(t, p, promptResultLine(t, promptID, "end_turn", map[string]any{
		"inputTokens": 7000, "outputTokens": 900,
	})))
	if r.Usage == nil || r.Usage.InputTokens != 7000 || r.Usage.OutputTokens != 900 {
		t.Fatalf("terminal usage = %+v, want 7000/900 preserved", r.Usage)
	}
	if r.TotalCostUSD != 0 {
		t.Fatalf("terminal cost = %v, want 0 (no pricing → zero-cost fallback)", r.TotalCostUSD)
	}
}

// TestNoContextMetadataLeavesContextUnavailable proves a usage_update with only
// cost (no used/size) leaves the context window zero so context stays
// unavailable rather than fabricated.
func TestNoContextMetadataLeavesContextUnavailable(t *testing.T) {
	p, _, _ := newPostHandshakeProtocol(t)
	u := lastUsageUpdate(t, mustParse(t, p, usageUpdateLine(t, map[string]any{
		"cost": map[string]any{"amount": 0.01, "currency": "USD"},
	})))
	if u.ContextWindow != 0 {
		t.Fatalf("context window = %d, want 0 (no metadata → unavailable)", u.ContextWindow)
	}
}

// TestUsagePreservedOnErrorResult proves a failed turn still reports the tokens
// it spent (usage attached) and the cost observed before the failure.
func TestUsagePreservedOnErrorResult(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	mustParse(t, p, usageUpdateLine(t, map[string]any{
		"used": 1000, "size": 100000,
		"cost": map[string]any{"amount": 0.002, "currency": "USD"},
	}))
	msgs := mustParse(t, p, promptResultLine(t, promptID, "refusal", map[string]any{
		"inputTokens": 700, "outputTokens": 300,
	}))
	r := terminalResult(t, msgs)
	if r.IsSuccess() {
		t.Fatalf("refusal produced %+v, want a non-success result", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 700 || r.Usage.OutputTokens != 300 {
		t.Fatalf("error result usage = %+v, want 700/300 preserved", r.Usage)
	}
	if r.TotalCostUSD != 0.002 {
		t.Fatalf("error result cost = %v, want 0.002 (observed before failure)", r.TotalCostUSD)
	}
}

// --- resume / session identity (Task 2) ---

// TestSessionNewEmitsInitMessageWithSessionID proves the session/new response
// produces a SystemInit SDK message carrying the captured ACP session id. The
// session layer's PID-file session-id update is gated on a provider init
// message, so without this the OpenCode session id never reaches the PID file.
func TestSessionNewEmitsInitMessageWithSessionID(t *testing.T) {
	buf := &syncBuffer{}
	p := NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})
	p.SetStdin(buf)
	const sessionNewID = 201
	p.SetRequestIDsForTest(0, sessionNewID, 0, 0)

	msgs := mustParse(t, p, responseLine(t, sessionNewID, map[string]any{"sessionId": "ses_new"}))
	if len(msgs) != 1 || msgs[0].Init == nil {
		t.Fatalf("session/new produced %+v, want one system-init message", msgs)
	}
	if msgs[0].Init.SessionID != "ses_new" {
		t.Fatalf("init session id = %q, want ses_new", msgs[0].Init.SessionID)
	}
	// The backend model rides the init message so the session view reports it.
	if msgs[0].Init.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("init model = %q, want anthropic/claude-sonnet-4-5", msgs[0].Init.Model)
	}
	if got := p.SessionID(); got != "ses_new" {
		t.Fatalf("SessionID() = %q, want ses_new", got)
	}
}

// TestSessionLoadEmitsInitMessageWithResumedID proves a resumed session also
// emits an init message carrying the resumed session id, so a resumed run's PID
// file records the identity the next prompt is delivered to.
func TestSessionLoadEmitsInitMessageWithResumedID(t *testing.T) {
	buf := &syncBuffer{}
	p := NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5", ResumeSessionID: "ses_prior"})
	p.SetStdin(buf)
	const sessionLoadID = 202
	p.SetRequestIDsForTest(0, 0, sessionLoadID, 0)

	msgs := mustParse(t, p, responseLine(t, sessionLoadID, map[string]any{}))
	if len(msgs) != 1 || msgs[0].Init == nil {
		t.Fatalf("session/load produced %+v, want one system-init message", msgs)
	}
	if msgs[0].Init.SessionID != "ses_prior" {
		t.Fatalf("init session id = %q, want ses_prior (resumed identity)", msgs[0].Init.SessionID)
	}
}

// initResultWithLoadSession builds an initialize response advertising (or not)
// the loadSession capability.
func initResultWithLoadSession(loadSession bool) map[string]any {
	return map[string]any{
		"protocolVersion":   1,
		"agentInfo":         map[string]any{"name": "OpenCode", "version": "1.17.9"},
		"agentCapabilities": map[string]any{"loadSession": loadSession},
	}
}

// TestHandshake_ResumeUsesSessionLoad proves that a resume request resumes the
// prior ACP session via session/load (not session/new) and delivers the next
// prompt to the resumed session identity.
func TestHandshake_ResumeUsesSessionLoad(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{
		Model:           "opencode:anthropic/claude-sonnet-4-5",
		WorkDir:         "/work/dir",
		InitialPrompt:   "RESUMED PHASE PROMPT",
		ResumeSessionID: "ses_prior",
	})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	if initReq.Method != "initialize" {
		t.Fatalf("first request = %q, want initialize", initReq.Method)
	}
	h.feed(t, responseLine(t, mustID(t, initReq.ID), initResultWithLoadSession(true)))

	loadReq := h.nextRequest(t)
	if loadReq.Method != sessionLoadMethod {
		t.Fatalf("second request = %q, want session/load", loadReq.Method)
	}
	var lp SessionLoadParams
	if err := json.Unmarshal(loadReq.Params, &lp); err != nil {
		t.Fatalf("unmarshal session/load params: %v", err)
	}
	if lp.SessionID != "ses_prior" || lp.Cwd != "/work/dir" {
		t.Fatalf("session/load params = %+v, want sessionId ses_prior cwd /work/dir", lp)
	}
	h.feed(t, responseLine(t, mustID(t, loadReq.ID), map[string]any{}))

	promptReq := h.nextRequest(t)
	if promptReq.Method != "session/prompt" {
		t.Fatalf("third request = %q, want session/prompt", promptReq.Method)
	}
	var pp PromptParams
	if err := json.Unmarshal(promptReq.Params, &pp); err != nil {
		t.Fatalf("unmarshal session/prompt params: %v", err)
	}
	if pp.SessionID != "ses_prior" {
		t.Fatalf("resumed prompt sessionId = %q, want ses_prior (no hidden new session)", pp.SessionID)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Handshake() error: %v", err)
	}
	if got := h.p.SessionID(); got != "ses_prior" {
		t.Fatalf("SessionID() = %q, want ses_prior", got)
	}
}

// TestHandshake_ResumeFailsClearlyWhenLoadSessionUnsupported proves that a
// resume request against an agent that does not advertise loadSession fails
// clearly before any prompt runs, rather than silently starting a new session.
func TestHandshake_ResumeFailsClearlyWhenLoadSessionUnsupported(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{
		WorkDir:         "/w",
		InitialPrompt:   "p",
		ResumeSessionID: "ses_prior",
	})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	h.feed(t, responseLine(t, mustID(t, initReq.ID), initResultWithLoadSession(false)))

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "loadSession") {
		t.Fatalf("Handshake() error = %v, want a clear loadSession-unsupported failure", err)
	}
	if !strings.Contains(err.Error(), "ses_prior") {
		t.Fatalf("Handshake() error = %v, want it to name the session it could not resume", err)
	}
	// No session was established and no prompt was sent.
	assertNoFurtherRequest(t, h)
	if got := h.p.SessionID(); got != "" {
		t.Fatalf("SessionID() = %q, want empty (resume failed, no session)", got)
	}
}

// TestHandshake_ResumeFailsClearlyWhenLoadErrors proves that a session/load that
// errors fails the handshake clearly (naming the session) and sends no prompt.
func TestHandshake_ResumeFailsClearlyWhenLoadErrors(t *testing.T) {
	h := newHandshakeHarness(t, llm.ProtocolOpts{
		WorkDir:         "/w",
		InitialPrompt:   "p",
		ResumeSessionID: "ses_gone",
	})
	errCh := make(chan error, 1)
	go func() { errCh <- h.p.Handshake(context.Background()) }()

	initReq := h.nextRequest(t)
	h.feed(t, responseLine(t, mustID(t, initReq.ID), initResultWithLoadSession(true)))

	loadReq := h.nextRequest(t)
	if loadReq.Method != sessionLoadMethod {
		t.Fatalf("request = %q, want session/load", loadReq.Method)
	}
	h.feed(t, errorResponseLine(t, mustID(t, loadReq.ID), -32603, "session not found"))

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "session/load failed") {
		t.Fatalf("Handshake() error = %v, want a clear session/load failure", err)
	}
	if !strings.Contains(err.Error(), "ses_gone") {
		t.Fatalf("Handshake() error = %v, want it to name the session", err)
	}
	assertNoFurtherRequest(t, h)
}

// assertNoFurtherRequest verifies the protocol sent no additional outbound
// request (e.g. a prompt) after a clear resume failure.
func assertNoFurtherRequest(t *testing.T, h *handshakeHarness) {
	t.Helper()
	select {
	case b, ok := <-h.lines:
		if ok {
			t.Fatalf("unexpected further outbound request after resume failure: %q", b)
		}
	case <-time.After(100 * time.Millisecond):
		// No further request — the resume failed before prompt execution.
	}
}

// --- interruption (Task 3) ---

// TestInterrupt_SendsSessionCancel proves protocol interruption uses ACP
// session/cancel (cancellation IS supported), targeting the active session.
func TestInterrupt_SendsSessionCancel(t *testing.T) {
	p, buf, _ := newPostHandshakeProtocol(t)
	if err := p.Interrupt(); err != nil {
		t.Fatalf("Interrupt() = %v, want nil (ACP cancellation supported)", err)
	}
	var note struct {
		Method string `json:"method"`
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
		ID *json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(buf.lastLine(t), &note); err != nil {
		t.Fatalf("cancel line not JSON-RPC: %v (raw %q)", err, buf.String())
	}
	if note.Method != sessionCancelMethod {
		t.Fatalf("interrupt method = %q, want session/cancel", note.Method)
	}
	if note.Params.SessionID != "ses_x" {
		t.Fatalf("cancel sessionId = %q, want ses_x", note.Params.SessionID)
	}
	if note.ID != nil {
		t.Fatalf("session/cancel must be a notification (no id), got id %s", string(*note.ID))
	}
}

// TestInterrupt_CancelledPromptIsNonSuccessAndSticky proves a cancelled prompt
// outcome after an interrupt is a sealed non-success result that a later
// end_turn cannot flip into a success.
func TestInterrupt_CancelledPromptIsNonSuccessAndSticky(t *testing.T) {
	p, _, promptID := newPostHandshakeProtocol(t)
	if err := p.Interrupt(); err != nil {
		t.Fatalf("Interrupt() = %v, want nil", err)
	}

	cancelled := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "cancelled"}))
	if len(cancelled) != 1 || cancelled[0].Result == nil || cancelled[0].Result.IsSuccess() {
		t.Fatalf("cancelled prompt produced %+v, want a non-success result", cancelled)
	}
	intent := llm.CompletionIntent{Found: true, Status: llm.CompletionIntentSuccess}
	if got := llm.ClassifyTurn(llm.TurnSignals{Result: cancelled[0].Result, RootIntent: intent}); got != llm.TurnErrored {
		t.Fatalf("cancelled result classified as %v, want Errored", got)
	}

	// A late end_turn after the sealed cancellation must not emit a success.
	if late := mustParse(t, p, responseLine(t, promptID, map[string]any{"stopReason": "end_turn"})); len(late) != 0 {
		t.Fatalf("late end_turn after cancellation produced %+v, want no messages", late)
	}
}
