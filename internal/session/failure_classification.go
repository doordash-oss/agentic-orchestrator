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
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const maxProviderRetryHint = 5 * time.Minute

var transientHTTPStatus = regexp.MustCompile(`\b(?:429|5[0-2][0-9])\b`)

// FailureTier is the recovery disposition for a failed provider session.
type FailureTier int

const (
	// Permanent failures must not resume the same provider conversation.
	Permanent FailureTier = iota
	// TransientRetryable failures may recover by resuming the conversation.
	TransientRetryable
	// BudgetExhausted failures require a human to wait for limits to reset.
	BudgetExhausted
)

// FailureClassification is a provider-independent recovery decision.
type FailureClassification struct {
	Tier      FailureTier
	RetryHint time.Duration
	Reason    string
}

// ClassifyFailure maps a failed session's normalized provider signals to a
// recovery tier. Unknown provider text is permanent; a death without provider
// error text is transient.
func ClassifyFailure(sess ports.SessionView) FailureClassification {
	if sess == nil {
		return permanentFailure("missing failed session")
	}
	result := sess.Cost()
	hint := failureRetryHint(sess)
	if hint > maxProviderRetryHint {
		return FailureClassification{
			Tier:   BudgetExhausted,
			Reason: "provider retry hint exceeds the automatic recovery ceiling",
		}
	}
	if result != nil && result.Failure != nil && result.Failure.Watchdog {
		return transientFailure(hint, "provider session watchdog detected a stall")
	}

	provider := strings.ToLower(strings.TrimSpace(sess.ProviderName()))
	text := failureProviderText(sess, result)
	metadata := failureMetadata(result)
	if result != nil {
		switch provider {
		case "claude":
			switch strings.ToLower(result.Subtype) {
			case "max_budget":
				return budgetFailure("Claude maximum budget was exhausted")
			case "max_turns":
				return permanentFailure("Claude maximum turns cannot be recovered by resuming")
			}
		case "codex":
			switch strings.ToLower(metadata.Type) {
			case "usagelimitexceeded":
				return budgetFailure("Codex usage limit was exhausted")
			case "serveroverloaded":
				return transientFailure(hint, "Codex server is overloaded")
			case "contextwindowexceeded":
				return permanentFailure("Codex context window was exhausted")
			}
		case "opencode":
			if strings.EqualFold(result.StopReason, "refusal") ||
				strings.EqualFold(result.StopReason, "cancelled") {
				return permanentFailure("OpenCode refused or cancelled the request")
			}
			if metadata.Retryable != nil && *metadata.Retryable {
				return transientFailure(hint, "OpenCode marked the provider error retryable")
			}
		}
	}

	if metadata.StatusCode == 429 || metadata.StatusCode >= 500 && metadata.StatusCode <= 529 {
		return transientFailure(hint, fmt.Sprintf("provider returned retryable status %d", metadata.StatusCode))
	}
	if metadata.StatusCode == 401 || metadata.StatusCode == 403 {
		return permanentFailure(fmt.Sprintf("provider returned authentication status %d", metadata.StatusCode))
	}
	if strings.TrimSpace(text) == "" {
		return transientFailure(hint, "provider process ended without error text")
	}
	if textIsTransient(text) {
		return transientFailure(hint, "provider reported a transient service failure")
	}
	if textIsPermanent(text) {
		return permanentFailure("provider reported a permanent authentication or refusal failure")
	}
	return permanentFailure("provider reported an unrecognized error")
}

func failureMetadata(result *llm.ResultMessage) llm.FailureMetadata {
	if result == nil || result.Failure == nil {
		return llm.FailureMetadata{}
	}
	return *result.Failure
}

func failureProviderText(sess ports.SessionView, result *llm.ResultMessage) string {
	var parts []string
	if result != nil {
		parts = append(parts, result.Result)
	}
	if log := sess.MessageLog(); log != nil {
		parts = append(parts, log.LastErrorDetail())
	}
	parts = append(parts, sess.ErrorDetail())
	return strings.ToLower(strings.TrimSpace(strings.Join(parts, "\n")))
}

func failureRetryHint(sess ports.SessionView) time.Duration {
	if sess == nil || sess.MessageLog() == nil {
		return 0
	}
	var hint time.Duration
	for _, msg := range sess.MessageLog().Messages() {
		if msg.RateLimit == nil || msg.RateLimit.RetryMS <= 0 {
			continue
		}
		candidate := time.Duration(msg.RateLimit.RetryMS * float64(time.Millisecond))
		if candidate > hint {
			hint = candidate
		}
	}
	return hint
}

func textIsTransient(text string) bool {
	return containsFragment(text,
		"rate limit", "overloaded", "temporarily unavailable", "timeout",
		"timed out", "network", "connection reset", "connection refused",
		"gateway", "service unavailable",
	) || transientHTTPStatus.MatchString(text)
}

func textIsPermanent(text string) bool {
	return containsFragment(text,
		"authentication", "unauthorized", "forbidden", "invalid api key",
		"permission denied", "refusal", "refused", "cancelled",
	)
}

func containsFragment(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

func transientFailure(hint time.Duration, reason string) FailureClassification {
	return FailureClassification{Tier: TransientRetryable, RetryHint: hint, Reason: reason}
}

func budgetFailure(reason string) FailureClassification {
	return FailureClassification{Tier: BudgetExhausted, Reason: reason}
}

func permanentFailure(reason string) FailureClassification {
	return FailureClassification{Tier: Permanent, Reason: reason}
}
