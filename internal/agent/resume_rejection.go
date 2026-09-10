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

package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const resumeEstablishmentWindow = 15 * time.Second

// ResumeRejectionVerdict classifies a provider's early refusal to restore a
// native session. Ordinary early process failures are intentionally excluded.
type ResumeRejectionVerdict struct {
	Rejected bool
	Reason   string
}

type resumeRejectionError struct {
	reason string
}

func (e *resumeRejectionError) Error() string {
	return e.reason
}

func detectResumeRejection(sess ports.SessionView, elapsed time.Duration) ResumeRejectionVerdict {
	if sess == nil || elapsed > resumeEstablishmentWindow || resumeSessionDidWork(sess) {
		return ResumeRejectionVerdict{}
	}
	cost := ExtractSessionCost(sess)
	if cost.TotalCostUSD != 0 || resumeUsageHasTokens(cost.Usage) {
		return ResumeRejectionVerdict{}
	}

	details := []string{sess.ErrorDetail(), sess.ExitCodeDetail()}
	if log := sess.MessageLog(); log != nil {
		details = append(details, log.LastErrorDetail(), log.Text())
	}
	return detectResumeRejectionDetail(sess.ProviderName(), strings.Join(details, "\n"))
}

func detectResumeStartRejection(provider string, err error, elapsed time.Duration) ResumeRejectionVerdict {
	if err == nil || elapsed > resumeEstablishmentWindow {
		return ResumeRejectionVerdict{}
	}
	return detectResumeRejectionDetail(provider, fmt.Sprintf("%v", err))
}

func detectResumeRejectionDetail(provider, detail string) ResumeRejectionVerdict {
	detail = strings.ToLower(detail)
	provider = strings.ToLower(strings.TrimSpace(provider))
	rejected := false
	switch provider {
	case "claude":
		rejected = containsAny(detail,
			"no conversation found",
			"conversation not found",
			"invalid session id",
			"session has expired",
		)
	case "codex":
		rejected = containsAny(detail,
			"thread/resume") && containsAny(detail, "error", "not found", "expired", "invalid")
		rejected = rejected || containsAny(detail, "failed to resume thread", "thread not found")
	case "opencode":
		// The provider handshake failure is "opencode session/load failed for
		// session %q: <rpc detail>" and the rpc detail carries no guaranteed
		// keyword, so session/load alone must establish the rejection. An
		// undetected rejection would hard-fail the phase instead of
		// dispatching the fresh-session fallback.
		rejected = containsAny(detail, "session/load")
	}
	if !rejected {
		return ResumeRejectionVerdict{}
	}
	return ResumeRejectionVerdict{
		Rejected: true,
		Reason:   "provider session was not found or has expired; retry starts a fresh session",
	}
}

func resumeSessionDidWork(sess ports.SessionView) bool {
	if sess == nil || sess.MessageLog() == nil {
		return false
	}
	for _, msg := range sess.MessageLog().Messages() {
		if resumeMessageIsProductive(msg) {
			return true
		}
	}
	return false
}

// ResumeSessionMadeProgress applies the shared observable-progress semantics
// used to reset consecutive idle resume attempts.
func ResumeSessionMadeProgress(sess ports.SessionView) bool {
	if sess == nil {
		return false
	}
	if resumeUsageHasTokens(sess.AccumulatedUsage()) {
		return true
	}
	if sess.MessageLog() == nil {
		return false
	}
	for _, msg := range sess.MessageLog().Messages() {
		if msg.Assistant != nil ||
			msg.ToolProgress != nil ||
			msg.TaskStarted != nil ||
			msg.TaskProgress != nil ||
			msg.TaskNotification != nil ||
			len(msg.FileReads) > 0 ||
			len(msg.FileChanges) > 0 {
			return true
		}
	}
	return false
}

func resumeMessageIsProductive(msg llm.SDKMessage) bool {
	return msg.Init != nil ||
		msg.Assistant != nil ||
		msg.ToolProgress != nil ||
		msg.TaskStarted != nil ||
		msg.TaskProgress != nil ||
		msg.TaskNotification != nil ||
		len(msg.FileReads) > 0 ||
		len(msg.FileChanges) > 0
}

func resumeUsageHasTokens(usage llm.Usage) bool {
	return usage.InputTokens != 0 ||
		usage.OutputTokens != 0 ||
		usage.CacheReadInputTokens != 0 ||
		usage.CacheCreationInputTokens != 0
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
