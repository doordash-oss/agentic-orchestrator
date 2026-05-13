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

package permission

import (
	"encoding/json"
	"strings"
)

// extractBashCommand extracts the command string from a Bash tool input.
// In production, the input is JSON like {"command":"ls -la"}.
// For plain strings (e.g. in tests), the input is returned as-is.
func extractBashCommand(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return input
	}
	var payload struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return input // not valid JSON, use as-is
	}
	return payload.Command
}

// normalizeBashCommand strips shell chaining constructs so that the core
// command can be matched against permission patterns.
//
//   - Chains: splits on "&&", takes the last segment
//     "cd /path && npm test --coverage" → "npm test --coverage"
//   - Pipes: takes content before the first "|"
//     "npm test 2>&1 | tee log" → "npm test 2>&1"
//
// Returns the trimmed result, or "" if nothing meaningful remains.
func normalizeBashCommand(input string) string {
	// Handle chained commands: split on "&&", take the last segment.
	if parts := strings.Split(input, "&&"); len(parts) > 1 {
		input = strings.TrimSpace(parts[len(parts)-1])
	}
	// Handle pipes: take content before first "|".
	if idx := strings.Index(input, "|"); idx >= 0 {
		input = strings.TrimSpace(input[:idx])
	}
	return strings.TrimSpace(input)
}

// InferBashPattern generates a Tool(pattern) string from a tool name and raw input.
// For Bash commands, extracts binary + subcommand and appends a wildcard:
//   - "npm test --coverage" → "Bash(npm test *)"
//   - "ls -la"              → "Bash(ls *)"      (flag, not subcommand)
//   - "cd /path && npm test" → "Bash(npm test *)" (strips cd chain prefix)
//   - ""                    → "Bash(*)"
//
// The input may be a raw JSON object (e.g. {"command":"ls"}) as received from
// the Claude CLI wire protocol; the command field is extracted automatically.
//
// For non-Bash tools, returns the exact "ToolName(input)" pattern.
func InferBashPattern(toolName, toolInput string) string {
	if toolName != "Bash" {
		return toolName + "(" + toolInput + ")"
	}

	input := normalizeBashCommand(strings.TrimSpace(extractBashCommand(toolInput)))
	if input == "" {
		return "Bash(*)"
	}

	tokens := strings.Fields(input)
	if len(tokens) == 0 {
		return "Bash(*)"
	}

	binary := tokens[0]

	// Single token: "ls" → "Bash(ls *)"
	if len(tokens) == 1 {
		return "Bash(" + binary + " *)"
	}

	// If second token starts with "-", it's a flag → just "binary *"
	// e.g. "ls -la /tmp" → "Bash(ls *)"
	if strings.HasPrefix(tokens[1], "-") {
		return "Bash(" + binary + " *)"
	}

	// Second token is a subcommand → "binary subcommand *"
	// e.g. "npm test --coverage" → "Bash(npm test *)"
	return "Bash(" + binary + " " + tokens[1] + " *)"
}
