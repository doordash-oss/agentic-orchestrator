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
// recovery tier. A death without provider error text — and text that matches
// no recognized vocabulary — is transient: the pre-classification crash-resume
// path guaranteed a continuation, and false permanence costs a full phase
// restart where a cheap retry would have recovered. Only recognized
// authentication/refusal signals and provider-structured budget/limit errors
// are permanent or budget-exhausted.
func ClassifyFailure(sess ports.SessionView) FailureClassification {
	if sess == nil {
		return permanentFailure("missing failed session")
	}
	result := sess.Cost()
	text := failureProviderText(sess, result)
	hint := failureRetryHint(sess)
	if hint > maxProviderRetryHint && !textConfirmsRateLimit(text) {
		// A long hint the failure text does not corroborate (e.g. a tail-end
		// quota snapshot) is not a wait instruction; drop it so the failure
		// falls back to the normal backoff instead of parking for hours.
		hint = 0
	}
	if result != nil && result.Failure != nil && result.Failure.Watchdog {
		return transientFailure(hint, "provider session watchdog detected a stall")
	}

	provider := strings.ToLower(strings.TrimSpace(sess.ProviderName()))
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
		if hint > maxProviderRetryHint && textConfirmsRateLimit(text) {
			return budgetFailure("provider rate/quota window will not reset soon enough for automatic recovery")
		}
		return transientFailure(hint, "provider reported a transient service failure")
	}
	if textIsPermanent(text) {
		return permanentFailure("provider reported a permanent authentication or refusal failure")
	}
	return transientFailure(hint, "provider reported an unrecognized error")
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

// failureRetryHint derives the provider's retry hint from the failed turn's
// tail. A rate-limit entry only counts while nothing productive happened
// after it: the session kept working past the message, the limit was not
// actually blocking. Routine quota snapshots (e.g. Codex account/rateLimits
// updates whose RetryMS is the time until the quota window resets — often
// hours) are thereby excluded once any later activity supersedes them, so a
// stale snapshot cannot reclassify an unrelated crash as budget exhaustion.
func failureRetryHint(sess ports.SessionView) time.Duration {
	if sess == nil || sess.MessageLog() == nil {
		return 0
	}
	var hint time.Duration
	superseded := false
	messages := sess.MessageLog().Messages()
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.RateLimit != nil && msg.RateLimit.RetryMS > 0 {
			if !superseded {
				if candidate := time.Duration(msg.RateLimit.RetryMS * float64(time.Millisecond)); candidate > hint {
					hint = candidate
				}
			}
			continue
		}
		if messageShowsProductiveActivity(msg) {
			superseded = true
		}
	}
	return hint
}

// messageShowsProductiveActivity reports whether a log entry demonstrates the
// session was still working (and therefore not blocked on a rate limit).
// Terminal failure results are excluded: they are the crash being classified,
// not evidence the limit was stale.
func messageShowsProductiveActivity(msg llm.SDKMessage) bool {
	if msg.Result != nil && !msg.Result.IsSuccess() {
		return false
	}
	return msg.Assistant != nil ||
		msg.Result != nil ||
		msg.ToolProgress != nil ||
		msg.TaskStarted != nil ||
		msg.TaskProgress != nil ||
		len(msg.FileReads) > 0 ||
		len(msg.FileChanges) > 0
}

func textIsTransient(text string) bool {
	return containsFragment(text,
		"rate limit", "overloaded", "temporarily unavailable", "timeout",
		"timed out", "network", "connection reset", "connection refused",
		"gateway", "service unavailable",
		// errno-style transport failures as providers spell them
		// (e.g. "read ECONNRESET"), kept in sync with the bounded-helper
		// retry vocabulary in internal/agent.
		"econnreset", "econnrefused", "econnaborted", "epipe",
		"enotfound", "eai_again", "etimedout",
		"unable to connect to api", "socket hang up",
	) || transientHTTPStatus.MatchString(text)
}

// textConfirmsRateLimit reports whether the failure text itself describes a
// rate/quota limit. Only then may a long retry hint upgrade a transient
// failure to BudgetExhausted; a hint alone — which may come from a routine
// quota snapshot — never can.
func textConfirmsRateLimit(text string) bool {
	return containsFragment(text, "rate limit", "quota", "usage limit", "rate_limit")
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
