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

// findSectionHeadings returns the byte offset of each heading in the
// supplied list (only entries actually present are included). Order is
// determined by document position, not by the input list — the caller is
// responsible for cross-checking expected ordering. Headings are matched on
// trimmed lines so trailing whitespace doesn't break detection.
func findSectionHeadings(body string, headings []string) map[string]int {
	want := map[string]struct{}{}
	for _, h := range headings {
		want[h] = struct{}{}
	}
	out := map[string]int{}
	pos := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if _, ok := want[trimmed]; ok {
			if _, dup := out[trimmed]; !dup {
				out[trimmed] = pos
			}
		}
		pos += len(line) + 1
	}
	return out
}

// extractMarkdownSection returns the body of the section whose heading line
// exactly matches heading. Body starts at the line after the heading and
// ends before the next heading at the same or higher level (e.g. for a
// `## ` heading, the next `## ` or `# ` ends the section; for a `### `
// heading, the next `### ` / `## ` / `# ` ends it). Returns "" when the
// section is absent.
//
// Fenced code blocks (``` and ~~~) suspend heading detection: a `# comment`
// line inside a fence is content, not a heading.
func extractMarkdownSection(body, heading string) string {
	level := headingLevel(heading)
	if level == 0 {
		return ""
	}
	lines := strings.Split(body, "\n")
	var out []string
	inSection := false
	inFence := false
	var fenceMarker string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Toggle fence state on opening/closing fence lines. Markdown
		// allows ``` and ~~~; either may carry a language hint after.
		if !inFence {
			if strings.HasPrefix(trimmed, "```") {
				inFence = true
				fenceMarker = "```"
			} else if strings.HasPrefix(trimmed, "~~~") {
				inFence = true
				fenceMarker = "~~~"
			}
		} else if strings.HasPrefix(trimmed, fenceMarker) {
			inFence = false
		}

		if !inSection {
			if trimmed == heading {
				inSection = true
			}
			continue
		}
		if !inFence {
			if l := headingLevel(trimmed); l > 0 && l <= level {
				break
			}
		}
		out = append(out, line)
	}
	if !inSection {
		return ""
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// headingLevel returns 1 for `# `, 2 for `## `, 3 for `### `, etc.; 0 for
// non-heading lines. The trailing space is required (Markdown's ATX rule).
func headingLevel(line string) int {
	n := 0
	for _, r := range line {
		if r != '#' {
			break
		}
		n++
	}
	if n == 0 {
		return 0
	}
	if len(line) <= n || line[n] != ' ' {
		return 0
	}
	return n
}
