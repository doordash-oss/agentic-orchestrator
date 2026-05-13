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
	"regexp"
	"strings"
)

// ansiPattern matches all common ANSI escape sequences:
//   - CSI (Control Sequence Introducer): ESC [ <params> <final>
//   - OSC (Operating System Command): ESC ] <text> BEL or ESC ] <text> ST
//   - Simple two-byte escapes: ESC <char>
//   - Carriage return: used by Ink for line rewriting
var ansiPattern = regexp.MustCompile(
	`\x1b\[[0-9;?]*[A-Za-z]` + // CSI sequences
		`|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)` + // OSC sequences
		`|\x1b[()][A-Z0-9]` + // Character set selection
		`|\x1b[A-Za-z]` + // Two-byte escapes (e.g., ESC M, ESC 7, ESC 8)
		`|\r`, // Carriage return (Ink redraws use CR to overwrite lines)
)

// stripANSI removes all ANSI escape sequences from a string, returning
// only the visible text content. This is used to clean Ink TUI rendering
// output from codex's stderr.
func stripANSI(s string) string {
	cleaned := ansiPattern.ReplaceAllString(s, "")
	return strings.TrimSpace(cleaned)
}
