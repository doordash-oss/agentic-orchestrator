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

package session

import (
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

// These tests drive a REAL opencode.Protocol through the session's stdout
// reader with fake ACP lifecycle notifications/responses using the real OpenCode
// ACP wire shapes, proving OpenCode lifecycle data reaches Agentico's existing
// usage, cost, and context-reporting surfaces exactly like other providers —
// without live OpenCode credentials, network, or global config mutation (Task 4).
//
// Real shapes: a session/update "usage_update" carries {used, size, cost}
// (context fill, context window, cumulative cost); the session/prompt result
// carries "usage":{inputTokens,outputTokens,cachedReadTokens,cachedWriteTokens}
// (the only carrier of the token split).

// runOpenCodeSession replays fake ACP stdout lines through a real opencode
// protocol attached to a fresh session.
func runOpenCodeSession(t *testing.T, lines []string) *Session {
	t.Helper()
	s, _ := newOpenCodeSession(t)
	runSessionWithStdoutLines(t, s, lines, nil)
	return s
}

// newOpenCodeSession builds a fresh session backed by a real opencode protocol,
// returning both so a test can pin the protocol's request ids before driving
// prompt responses.
func newOpenCodeSession(t *testing.T) (*Session, *opencode.Protocol) {
	t.Helper()
	s := NewSession("oc-lifecycle", "feat-1", feature.PhaseImplement)
	p := opencode.NewProtocol(llm.ProtocolOpts{Model: "opencode:anthropic/claude-sonnet-4-5"})
	s.protocol = p
	return s, p
}

// ocUsageUpdate builds a session/update "usage_update" notification carrying the
// raw update body (e.g. {"used":..,"size":..,"cost":{..}}).
func ocUsageUpdate(update string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_x","update":{"sessionUpdate":"usage_update",` + update + `}}}`
}

// ocContentChunk builds an agent_message_chunk carrying assistant text.
func ocContentChunk(text string) string {
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses_x","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + text + `"}}}}`
}

// ocPromptResult builds a session/prompt JSON-RPC response with the given id,
// stop reason, and (optional) end-turn token usage body.
func ocPromptResult(id int, stopReason, usage string) string {
	result := `{"stopReason":"` + stopReason + `"`
	if usage != "" {
		result += `,"usage":` + usage
	}
	result += `}`
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"result":` + result + `}`
}

// TestOpenCodeSessionResultUsageReachesAccumulatedAfterStreamedUsage proves the
// core lifecycle path end-to-end: a streamed usage_update (context + cost)
// followed by the prompt result (token split) leaves the session reporting the
// result's input/output/cache in AccumulatedUsage (NOT dropped because a
// usage_update was already seen), the cumulative cost in Cost(), and the context
// percentage from the usage_update. It also guards the multi-message dispatch:
// the prompt-result line yields BOTH a usage update and a result, and dropping
// either (cost or tokens) fails this test.
func TestOpenCodeSessionResultUsageReachesAccumulatedAfterStreamedUsage(t *testing.T) {
	s, p := newOpenCodeSession(t)
	const promptID = 902
	p.SetRequestIDsForTest(0, 0, 0, promptID)

	runSessionWithStdoutLines(t, s, []string{
		ocUsageUpdate(`"used":51200,"size":200000,"cost":{"amount":0.07,"currency":"USD"}`),
		ocPromptResult(promptID, "end_turn", `{"inputTokens":50000,"outputTokens":1000,"cachedReadTokens":300,"cachedWriteTokens":40}`),
	}, nil)

	got := s.AccumulatedUsage()
	if got.InputTokens != 50000 || got.OutputTokens != 1000 {
		t.Errorf("AccumulatedUsage() = in %d out %d, want 50000/1000 (result tokens not dropped after streamed usage)", got.InputTokens, got.OutputTokens)
	}
	if got.CacheReadInputTokens != 300 || got.CacheCreationInputTokens != 40 {
		t.Errorf("AccumulatedUsage() cache = read %d write %d, want 300/40", got.CacheReadInputTokens, got.CacheCreationInputTokens)
	}
	if s.Cost() == nil || s.Cost().TotalCostUSD != 0.07 {
		t.Errorf("Cost() = %+v, want TotalCostUSD 0.07 (carried from usage_update.cost.amount)", s.Cost())
	}
	// fill = used = 51200; window = size = 200000; baseline 0 → 25%.
	if pct := s.ContextPercentage(); pct != 25 {
		t.Errorf("ContextPercentage() = %d, want 25 (backed by OpenCode usage_update)", pct)
	}
}

// TestOpenCodeSessionStreamsContentThenUsage proves assistant content and a
// separate usage_update both land: the text is captured and the context metadata
// drives the percentage.
func TestOpenCodeSessionStreamsContentThenUsage(t *testing.T) {
	s := runOpenCodeSession(t, []string{
		ocContentChunk("hello from opencode"),
		ocUsageUpdate(`"used":51200,"size":200000`),
	})

	if got := s.messageLog.AssistantText(); !strings.Contains(got, "hello from opencode") {
		t.Errorf("AssistantText() = %q, want it to contain the chunk's assistant text", got)
	}
	if pct := s.ContextPercentage(); pct != 25 {
		t.Errorf("ContextPercentage() = %d, want 25 (context metadata from usage_update)", pct)
	}
}

// TestOpenCodeSessionZeroCostWithoutPricing proves that when OpenCode supplies no
// cost (the backend has no pricing), the session invents none — token usage is
// still preserved but Cost stays at the zero-cost fallback.
func TestOpenCodeSessionZeroCostWithoutPricing(t *testing.T) {
	s, p := newOpenCodeSession(t)
	const promptID = 903
	p.SetRequestIDsForTest(0, 0, 0, promptID)

	runSessionWithStdoutLines(t, s, []string{
		ocUsageUpdate(`"used":8000,"size":128000`),
		ocPromptResult(promptID, "end_turn", `{"inputTokens":7000,"outputTokens":900}`),
	}, nil)

	got := s.AccumulatedUsage()
	if got.InputTokens != 7000 || got.OutputTokens != 900 {
		t.Errorf("AccumulatedUsage() = in %d out %d, want 7000/900", got.InputTokens, got.OutputTokens)
	}
	if s.Cost() != nil && s.Cost().TotalCostUSD != 0 {
		t.Errorf("Cost().TotalCostUSD = %v, want 0 (no pricing → zero-cost fallback)", s.Cost().TotalCostUSD)
	}
}

// TestOpenCodeSessionFallsBackToResultTokensWhenUsageUpdateUsedIsZero proves
// OpenCode backends that report usage_update.used=0 still leave Agentico with a
// non-zero context percentage once the prompt result supplies token usage.
func TestOpenCodeSessionFallsBackToResultTokensWhenUsageUpdateUsedIsZero(t *testing.T) {
	s, p := newOpenCodeSession(t)
	const promptID = 904
	p.SetRequestIDsForTest(0, 0, 0, promptID)

	runSessionWithStdoutLines(t, s, []string{
		ocUsageUpdate(`"used":0,"size":200000`),
		ocPromptResult(promptID, "end_turn", `{"inputTokens":50000,"outputTokens":1000}`),
	}, nil)

	if pct := s.ContextPercentage(); pct != 25 {
		t.Errorf("ContextPercentage() = %d, want 25 (fallback to result tokens when OpenCode used=0)", pct)
	}
}

// TestOpenCodeSessionEstimatesContextWhenOpenCodeReportsZeroTelemetry proves
// OpenCode backends that report both usage_update.used=0 and zero prompt-result
// tokens still move the context meter from the prompt text Agentico sent.
func TestOpenCodeSessionEstimatesContextWhenOpenCodeReportsZeroTelemetry(t *testing.T) {
	s, p := newOpenCodeSession(t)
	p.SetStdin(io.Discard)
	if err := p.SendUserMessage(strings.Repeat("abcd", 100)); err != nil {
		t.Fatalf("SendUserMessage() error = %v", err)
	}

	runSessionWithStdoutLines(t, s, []string{
		ocUsageUpdate(`"used":0,"size":2000`),
	}, nil)

	if pct := s.ContextPercentage(); pct != 5 {
		t.Errorf("ContextPercentage() = %d, want 5 (estimated 100 tokens / 2000 window)", pct)
	}
}

// TestOpenCodeSessionContextPercentageFromMetadata proves the active-session
// context display uses OpenCode-supplied context metadata when present.
func TestOpenCodeSessionContextPercentageFromMetadata(t *testing.T) {
	s := runOpenCodeSession(t, []string{
		ocUsageUpdate(`"used":51200,"size":200000`),
	})
	// fill = used = 51200; window = size = 200000; baseline 0 → 25%.
	if pct := s.ContextPercentage(); pct != 25 {
		t.Errorf("ContextPercentage() = %d, want 25 (backed by OpenCode metadata)", pct)
	}
}

// TestOpenCodeSessionContextUnavailableWithoutMetadata proves context display
// stays unavailable (calculating) when OpenCode omits context metadata, rather
// than fabricating a percentage from lifetime tokens.
func TestOpenCodeSessionContextUnavailableWithoutMetadata(t *testing.T) {
	s := runOpenCodeSession(t, []string{
		ocUsageUpdate(`"cost":{"amount":0.01,"currency":"USD"}`),
	})
	if pct := s.ContextPercentage(); pct != -1 {
		t.Errorf("ContextPercentage() = %d, want -1 (unavailable without context metadata)", pct)
	}
}
