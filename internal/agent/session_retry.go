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

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	retryableInfrastructureSessionMaxAttempts = 3
	retryableInfrastructureSessionMaxDuration = 30 * time.Second
)

func retrySessionID(base string, sessionAttempt int) string {
	if sessionAttempt <= 1 {
		return base
	}
	return fmt.Sprintf("%s-retry-%02d", base, sessionAttempt)
}

func shouldRetryPlanInfrastructureSession(agentStatus string, sess ports.SessionView, cost SessionCost, duration time.Duration, sessionAttempt int) bool {
	if sessionAttempt >= retryableInfrastructureSessionMaxAttempts {
		return false
	}
	if agentStatus != agentStatusFailed {
		return false
	}
	return isRetryableInfrastructureSessionFailure(sess, cost, duration)
}

func isRetryableInfrastructureSessionFailure(sess ports.SessionView, cost SessionCost, duration time.Duration) bool {
	if sess == nil {
		return false
	}
	if sess.Status() != ports.SessionFailed {
		return false
	}
	if duration > retryableInfrastructureSessionMaxDuration {
		return false
	}
	if sess.Cost() != nil {
		return false
	}
	if cost.TotalCostUSD != 0 {
		return false
	}
	if cost.Usage.InputTokens != 0 ||
		cost.Usage.OutputTokens != 0 ||
		cost.Usage.CacheReadInputTokens != 0 ||
		cost.Usage.CacheCreationInputTokens != 0 {
		return false
	}
	if log := sess.MessageLog(); log != nil && strings.TrimSpace(log.Text()) != "" {
		return false
	}
	return true
}
