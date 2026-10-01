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

package llm

import "strings"

// ReviewFailure is an allowlisted diagnostic from a native tool-less adapter.
// It is never decoded from provider JSON; raw errors can contain credentials.
type ReviewFailure string

const (
	ReviewFailureProvider    ReviewFailure = "provider_error"
	ReviewFailureAuth        ReviewFailure = "authentication"
	ReviewFailureModel       ReviewFailure = "model_rejected"
	ReviewFailureTransport   ReviewFailure = "transport"
	ReviewFailureRateLimit   ReviewFailure = "rate_limit"
	ReviewFailureServer      ReviewFailure = "server_error"
	ReviewFailureDecision    ReviewFailure = "invalid_decision"
	ReviewFailureProtocol    ReviewFailure = "protocol_violation"
	ReviewFailureInteraction ReviewFailure = "unexpected_interaction"
	ReviewFailureTurn        ReviewFailure = "turn_failed"
)

// Reason returns only fixed strings, including for unknown enum values.
func (f ReviewFailure) Reason() string {
	switch f {
	case ReviewFailureAuth:
		return "reviewer authentication or authorization failed"
	case ReviewFailureModel:
		return "reviewer model or request configuration was rejected"
	case ReviewFailureTransport:
		return "reviewer transport failed"
	case ReviewFailureRateLimit:
		return "reviewer rate limit exceeded"
	case ReviewFailureServer:
		return "reviewer service failed"
	case ReviewFailureDecision:
		return "reviewer response was not an exact decision token"
	case ReviewFailureProtocol:
		return "reviewer protocol validation failed"
	case ReviewFailureInteraction:
		return "reviewer attempted an unexpected interaction"
	case ReviewFailureTurn:
		return "reviewer turn failed"
	default:
		return "provider returned an unsuccessful review result"
	}
}

// ClassifyReviewFailure inspects an ephemeral error without retaining any text.
func ClassifyReviewFailure(message string) ReviewFailure {
	text := strings.ToLower(message)
	codes := strings.FieldsFunc(text, func(r rune) bool { return r < '0' || r > '9' })
	for _, group := range []struct {
		kind    ReviewFailure
		markers []string
	}{
		{ReviewFailureAuth, []string{"unauthorized", "forbidden", "authentication", "credential", "api key", "401", "403"}},
		{ReviewFailureModel, []string{"invalid model", "model not found", "does not exist", "not supported", "unsupported", "invalid request", "invalid params", "invalid config", "400", "404"}},
		{ReviewFailureRateLimit, []string{"rate limit", "too many requests", "429"}},
		{ReviewFailureServer, []string{"internal server error", "service unavailable", "overloaded", "500", "502", "503", "504", "529"}},
		{ReviewFailureTransport, []string{"transport", "connection", "network", "disconnected", "reconnecting", "timeout", "timed out", "unexpected eof"}},
	} {
		for _, marker := range group.markers {
			matches := strings.Contains(text, marker)
			if len(marker) == 3 && marker[0] >= '0' && marker[0] <= '9' {
				matches = false
				for _, code := range codes {
					if code == marker {
						matches = true
						break
					}
				}
			}
			if matches {
				return group.kind
			}
		}
	}
	return ReviewFailureProvider
}
