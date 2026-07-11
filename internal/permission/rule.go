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

import "strings"

// toolNameBash is the tool name Codex/Claude report for shell-command tool calls.
const toolNameBash = "Bash"

// toolNameEdit is the tool name Codex/Claude report for file-edit tool calls.
const toolNameEdit = "Edit"

// toolNameNotebookEdit is the tool name Codex/Claude report for notebook-cell edits.
const toolNameNotebookEdit = "NotebookEdit"

// toolNameWrite is the tool name Codex/Claude report for file-write tool calls.
const toolNameWrite = "Write"

// Bash tool-pattern literals shared between default rules and tests.
const (
	patternBashAny     = "Bash(*)"
	patternBashLSExact = "Bash(ls -la)"
	patternBashLS      = "Bash(ls *)"
	patternBashRm      = "Bash(rm *)"
	patternBashEcho    = "Bash(echo *)"
	patternBashGitDiff = "Bash(git diff *)"
	patternBashNpmTest = "Bash(npm test *)"
	patternBashGoTest  = "Bash(go test *)"
)

// Rule represents a cached permission rule.
type Rule struct {
	ToolPattern string // e.g. "Bash(npm test *)", "Bash", "Edit(/path/to/file)"
	Effect      string // "allow" or "deny"
	RepoName    string // scope: "" = global, otherwise per-repo
}

// Match checks if this rule applies to the given tool request.
// Supports three match modes:
//   - Tool-name-only: "Bash" matches any input for that tool
//   - Prefix wildcard: "Bash(npm test *)" matches "npm test", "npm test --coverage", etc.
//   - Exact match: "Bash(ls -la)" matches only "ls -la"
//
// For Bash tools the toolInput may be a raw JSON object (e.g. {"command":"ls"});
// the command field is extracted automatically before matching.
func (r Rule) Match(toolName, toolInput string) bool {
	openParen := strings.Index(r.ToolPattern, "(")
	if openParen < 0 {
		// Tool-name-only: match if tool names are equal
		return r.ToolPattern == toolName
	}

	// Extract tool name from pattern
	patternTool := r.ToolPattern[:openParen]
	if patternTool != toolName {
		return false
	}

	// Normalize: extract command from JSON and strip shell chaining
	input := toolInput
	if toolName == toolNameBash {
		input = normalizeBashCommand(extractBashCommand(toolInput))
	}

	// Extract inner pattern (between parens)
	closeParen := strings.LastIndex(r.ToolPattern, ")")
	if closeParen <= openParen {
		return false // malformed pattern
	}
	inner := r.ToolPattern[openParen+1 : closeParen]

	// Wildcard-only: matches any input
	if inner == "*" {
		return true
	}

	// Prefix wildcard: "prefix *" matches input == prefix or input starts with "prefix "
	if strings.HasSuffix(inner, " *") {
		prefix := inner[:len(inner)-2] // strip " *"
		return input == prefix || strings.HasPrefix(input, prefix+" ")
	}

	// Exact match
	return inner == input
}
