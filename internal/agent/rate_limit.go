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

import "strings"

// RateLimitSignatures is the case-insensitive substring set that marks an
// upstream rate-limit / capacity failure across the supported model CLIs
// (Claude "rate limit"/"429", Codex "Usage limit exceeded"/"Server
// overloaded", and the shared "[rate_limit]" transcript marker). It is an
// exported package var so new provider phrasings or HTTP codes can be added
// with a single-line append without touching the matcher.
var RateLimitSignatures = []string{
	"429",
	"rate limit",
	"ratelimit",
	"rate_limit",
	"too many requests",
	"overloaded",
	"usage limit exceeded",
	"quota",
}

// IsRateLimitError reports whether text contains any known rate-limit
// signature. Matching is case-insensitive and substring-based so it works on
// raw CLI error strings and full session transcripts alike.
func IsRateLimitError(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	lower := strings.ToLower(text)
	for _, sig := range RateLimitSignatures {
		if sig == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(sig)) {
			return true
		}
	}
	return false
}
