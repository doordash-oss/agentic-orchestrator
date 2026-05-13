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

package tui

import (
	"regexp"
	"strings"
)

// toolLineRegex matches lines that look like actual tool usage output from Claude CLI.
// These are the meaningful lines we want to surface.
var toolLineRegex = regexp.MustCompile(`(?i)^(Read|Write|Edit|Glob|Grep|Bash|Agent|WebFetch|WebSearch|Skill|LS)\b`)

// meaningfulPrefixes are line prefixes that indicate meaningful output.
var meaningfulPrefixes = []string{
	"Read ", "Write ", "Edit ", "Glob ", "Grep ", "Bash ", "Agent ",
	"Reading ", "Writing ", "Editing ", "Creating ", "Running ",
	"Found ", "Wrote ", "Created ", "Updated ", "Deleted ",
	"$ ",                                  // shell commands
	"ok ", "FAIL", "PASS", "--- ", "=== ", // test output
	"Error:", "error:", "Warning:", "warning:",
	"diff ", "patch ",
	"commit ", "push ",
}

// extractActivityLines extracts the last N meaningful lines from session output.
// With the JSON protocol, input is already clean text from MessageLog.
func extractActivityLines(raw string, n int) []string {
	// Split by newlines
	lines := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r'
	})

	var meaningful []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < 4 {
			continue
		}

		// Accept lines that match tool output patterns
		if !isMeaningfulLine(trimmed) {
			continue
		}

		// Truncate long lines
		if len(trimmed) > 100 {
			trimmed = trimmed[:97] + "..."
		}

		meaningful = append(meaningful, trimmed)
	}

	// Deduplicate consecutive identical lines
	meaningful = dedup(meaningful)

	// Return last n lines
	if len(meaningful) > n {
		meaningful = meaningful[len(meaningful)-n:]
	}
	return meaningful
}

// isMeaningfulLine checks if a line looks like actual tool/agent output.
func isMeaningfulLine(s string) bool {
	// Tool output lines
	if toolLineRegex.MatchString(s) {
		return true
	}

	// Lines with meaningful prefixes
	for _, p := range meaningfulPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}

	// Lines that contain file paths (contain / or .go, .py, etc)
	if strings.Contains(s, "/") && (strings.Contains(s, ".go") || strings.Contains(s, ".py") ||
		strings.Contains(s, ".ts") || strings.Contains(s, ".js") ||
		strings.Contains(s, ".md") || strings.Contains(s, ".yaml") ||
		strings.Contains(s, ".json") || strings.Contains(s, ".toml")) {
		return true
	}

	// Test output lines
	if strings.HasPrefix(s, "ok ") || strings.HasPrefix(s, "FAIL") || strings.HasPrefix(s, "PASS") {
		return true
	}

	return false
}

// dedup removes consecutive duplicate lines.
func dedup(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	result := []string{lines[0]}
	for i := 1; i < len(lines); i++ {
		if lines[i] != lines[i-1] {
			result = append(result, lines[i])
		}
	}
	return result
}
