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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestClassifyFailureProviderSignals(t *testing.T) {
	retryable := true
	notRetryable := false
	tests := []struct {
		name       string
		provider   string
		result     *llm.ResultMessage
		messages   []llm.SDKMessage
		wantTier   FailureTier
		wantHint   time.Duration
		wantReason string
	}{
		{name: "claude max budget", provider: "claude", result: failureResult("max_budget", ""), wantTier: BudgetExhausted},
		{name: "claude 429 text", provider: "claude", result: failureResult("error", "API returned 429 rate limit"), wantTier: TransientRetryable},
		{name: "claude auth text", provider: "claude", result: failureResult("error", "authentication failed"), wantTier: Permanent},
		{name: "claude max turns", provider: "claude", result: failureResult("max_turns", ""), wantTier: Permanent},
		{name: "claude bare death", provider: "claude", wantTier: TransientRetryable},
		{
			name:     "codex usage limit",
			provider: "codex",
			result:   failureResultWithMetadata("error", "", &llm.FailureMetadata{Type: "UsageLimitExceeded"}),
			wantTier: BudgetExhausted,
		},
		{
			name:     "codex overloaded",
			provider: "codex",
			result:   failureResultWithMetadata("error", "", &llm.FailureMetadata{Type: "ServerOverloaded"}),
			wantTier: TransientRetryable,
		},
		{
			name:     "codex context",
			provider: "codex",
			result:   failureResultWithMetadata("error", "", &llm.FailureMetadata{Type: "ContextWindowExceeded"}),
			wantTier: Permanent,
		},
		{
			name:     "codex retry hint",
			provider: "codex",
			result:   failureResult("error", "rate limited"),
			messages: []llm.SDKMessage{rateLimitMessage(90 * time.Second)},
			wantTier: TransientRetryable,
			wantHint: 90 * time.Second,
		},
		{
			name:     "codex retry hint above ceiling",
			provider: "codex",
			result:   failureResult("error", "rate limited"),
			messages: []llm.SDKMessage{rateLimitMessage(5*time.Minute + time.Millisecond)},
			wantTier: BudgetExhausted,
		},
		{
			name:     "stale rate limit snapshot ignored when work followed it",
			provider: "codex",
			result:   failureResult("error", "an unfamiliar provider failure"),
			messages: []llm.SDKMessage{
				rateLimitMessage(6 * time.Hour),
				assistantTextMessage("still working on the refactor"),
			},
			wantTier: TransientRetryable,
			wantHint: 0,
		},
		{
			name:     "uncorroborated tail rate limit hint dropped",
			provider: "codex",
			result:   failureResult("error", "an unfamiliar provider failure"),
			messages: []llm.SDKMessage{rateLimitMessage(6 * time.Hour)},
			wantTier: TransientRetryable,
			wantHint: 0,
		},
		{
			name:     "codex econnreset errno text",
			provider: "codex",
			result:   failureResult("error", "read ECONNRESET"),
			wantTier: TransientRetryable,
		},
		{
			name:     "opencode retryable",
			provider: "opencode",
			result:   failureResultWithMetadata("error", "backend unavailable", &llm.FailureMetadata{Retryable: &retryable}),
			wantTier: TransientRetryable,
		},
		{
			name:     "opencode status 503",
			provider: "opencode",
			result:   failureResultWithMetadata("error", "backend unavailable", &llm.FailureMetadata{StatusCode: 503}),
			wantTier: TransientRetryable,
		},
		{
			name:     "opencode status 401",
			provider: "opencode",
			result:   failureResultWithMetadata("error", "unauthorized", &llm.FailureMetadata{StatusCode: 401, Retryable: &notRetryable}),
			wantTier: Permanent,
		},
		{
			name:     "opencode refusal",
			provider: "opencode",
			result:   &llm.ResultMessage{Subtype: "error", IsError: true, StopReason: "refusal"},
			wantTier: Permanent,
		},
		{
			name:     "watchdog",
			provider: "opencode",
			result:   failureResultWithMetadata("error", "opaque watchdog text", &llm.FailureMetadata{Watchdog: true}),
			wantTier: TransientRetryable,
		},
		// Unrecognized non-empty provider text must stay transient: the
		// pre-classification crash-resume path guaranteed one continuation,
		// and defaulting to permanent starved common mid-turn crashes of any
		// resume attempt.
		{name: "unknown provider text", provider: "claude", result: failureResult("error", "an unfamiliar provider failure"), wantTier: TransientRetryable},
		{name: "absence of provider text", provider: "codex", wantTier: TransientRetryable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := NewSession("test", "feature", feature.PhaseImplement)
			sess.SetProviderName(test.provider)
			sess.SetStatus(SessionFailed)
			if test.result != nil {
				sess.cost = test.result
				sess.messageLog.Append(llm.SDKMessage{Type: "result", Result: test.result})
			}
			for _, msg := range test.messages {
				sess.messageLog.Append(msg)
			}

			got := ClassifyFailure(sess)
			if got.Tier != test.wantTier || got.RetryHint != test.wantHint {
				t.Errorf("ClassifyFailure() = %#v, want tier=%v hint=%s", got, test.wantTier, test.wantHint)
			}
			if got.Reason == "" {
				t.Error("ClassifyFailure().Reason is empty")
			}
		})
	}
}

func failureResult(subtype, detail string) *llm.ResultMessage {
	return &llm.ResultMessage{Subtype: subtype, Result: detail, IsError: true}
}

func failureResultWithMetadata(subtype, detail string, metadata *llm.FailureMetadata) *llm.ResultMessage {
	result := failureResult(subtype, detail)
	result.Failure = metadata
	return result
}

func rateLimitMessage(hint time.Duration) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "rate_limit",
		RateLimit: &llm.RateLimitMessage{
			Type:    "rate_limit",
			RetryMS: float64(hint.Milliseconds()),
		},
	}
}

func assistantTextMessage(text string) llm.SDKMessage {
	return llm.SDKMessage{
		Type: "assistant",
		Assistant: &llm.AssistantMessage{
			Type: "assistant",
			Message: llm.ConversationMsg{
				Role:    "assistant",
				Content: []llm.ContentBlock{{Type: "text", Text: text}},
			},
		},
	}
}
