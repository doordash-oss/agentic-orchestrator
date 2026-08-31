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

// MatchAskUserOptionLabel returns the option label an AskUserQuestion answer
// selects, or ok=false when it matches none. Exact match is tried first, then
// a "(Recommended)"-suffix-insensitive match, then a display-truncation match
// for answers ending in an ellipsis (display layers truncate long labels).
func MatchAskUserOptionLabel(labels []string, answer string) (string, bool) {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return "", false
	}
	for _, label := range labels {
		if strings.TrimSpace(label) == trimmed {
			return label, true
		}
	}
	stripped := trimRecommendedSuffix(trimmed)
	if stripped != "" {
		for _, label := range labels {
			if trimRecommendedSuffix(strings.TrimSpace(label)) == stripped {
				return label, true
			}
		}
	}
	if prefix, ok := trimEllipsisSuffix(trimmed); ok && prefix != "" {
		var match string
		count := 0
		for _, label := range labels {
			if strings.HasPrefix(strings.TrimSpace(label), prefix) {
				match = label
				count++
			}
		}
		if count == 1 {
			return match, true
		}
	}
	return "", false
}

func trimRecommendedSuffix(s string) string {
	return strings.TrimSpace(strings.TrimSuffix(s, "(Recommended)"))
}

func trimEllipsisSuffix(s string) (string, bool) {
	for _, suffix := range []string{"...", "…"} {
		if rest, ok := strings.CutSuffix(s, suffix); ok {
			return rest, true
		}
	}
	return s, false
}
