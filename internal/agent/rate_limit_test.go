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

import "testing"

func TestIsRateLimitError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "claude_rate_limit_429", text: "API rate limit exceeded (429)", want: true},
		{name: "http_429_too_many_requests", text: "HTTP 429 Too Many Requests", want: true},
		{name: "underscore_marker", text: "[rate_limit] resets in 12s", want: true},
		{name: "spaced_lowercase", text: "the model hit a rate limit, retrying", want: true},
		{name: "mixed_case_ratelimit", text: "RateLimit reached for org", want: true},
		{name: "codex_usage_limit", text: "Usage limit exceeded", want: true},
		{name: "codex_usage_limit_mixed_case", text: "USAGE LIMIT EXCEEDED for account", want: true},
		{name: "server_overloaded", text: "Server overloaded (529)", want: true},
		{name: "quota", text: "quota exceeded for this project", want: true},
		{name: "digits_429_substring_matches", text: "error code 4291", want: true},

		{name: "empty", text: "", want: false},
		{name: "whitespace_only", text: "   \n\t ", want: false},
		{name: "missing_phase_complete", text: "phase_complete was missing", want: false},
		{name: "contract_failed", text: "contract validation failed", want: false},
		{name: "file_not_found", text: "open research.md: no such file or directory", want: false},
		{name: "generic_500", text: "internal server error 500", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRateLimitError(tt.text); got != tt.want {
				t.Errorf("IsRateLimitError(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// TestRateLimitSignaturesExtensible guards the "keep it open for future codes/
// messages" requirement without mutating the shared package global (which
// would race the parallel table test above): the signature set is a non-empty
// exported var and every entry — including any future addition — is honored by
// the classifier, case-insensitively and as a substring.
func TestRateLimitSignaturesExtensible(t *testing.T) {
	t.Parallel()
	if len(RateLimitSignatures) == 0 {
		t.Fatal("RateLimitSignatures must not be empty")
	}
	for _, sig := range RateLimitSignatures {
		if sig == "" {
			t.Error("RateLimitSignatures must not contain empty entries")
			continue
		}
		if !IsRateLimitError("upstream said: " + sig + " (retry later)") {
			t.Errorf("signature %q not honored by IsRateLimitError", sig)
		}
	}
}
