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
	"regexp"
	"sort"
	"strings"
)

// PlanTask is one `### Task N:` block parsed out of a phase-plan markdown.
// Heading is the literal heading line (e.g. "### Task 1: Wire the helper").
// Repos contains every `**Repo:** <name>` tag in the task. Valid multi-repo
// phase plans declare exactly one; retaining every tag
// lets validation reject ambiguous ownership instead of silently choosing one.
// Body is the slice of source lines that fall under this task — including
// the Repo tag line, blanks, and trailing content — up to (but not
// including) the next `### ` or `## ` heading. Lines preserve trailing
// whitespace exactly as they appeared in the source so re-emission is
// byte-stable.
type PlanTask struct {
	Heading string
	Repos   []string
	Body    []string
}

// taskHeadingRE matches a Task heading line: `### Task N:` (any number),
// optionally followed by a name. The match is anchored to the start of a
// line to avoid sub-headers like `#### Task helper` inside a body.
var taskHeadingRE = regexp.MustCompile(`^###\s+Task\s+\d+\b`)

// repoTagRE matches `**Repo:** <name>` where the name may be wrapped in
// backticks. Case-insensitive on the label so `**repo:**` is tolerated.
// Captures the bare repo name in group 1.
var repoTagRE = regexp.MustCompile("(?i)^\\*\\*Repo:\\*\\*\\s*`?([\\w.\\-]+)`?\\s*$")

// frontendMetadataRE matches the plan-level `**Frontend:** true|false`
// metadata field. Values are interpreted case-insitively by
// ParsePhasePlanFrontend.
var frontendMetadataRE = regexp.MustCompile("(?i)^\\*\\*Frontend:\\*\\*\\s*`?([^`\\s]+)`?\\s*$")

// ParsePlanTasks scans a phase-plan markdown and returns every Task block
// in the `## Tasks` section (the first such section; any later one is
// ignored). Tasks without a `**Repo:**` tag get an empty Repos slice.
//
// Section-end detection, task-heading detection, and Repo-tag matching all
// honor fenced code blocks: ` ``` ` / ` ~~~ ` / ` ```` ` openers suppress
// header recognition until the matching closer, so plans that embed
// translated markdown (e.g. README content inside a ` ```` `markdown block)
// don't get truncated at an inner `## ` heading.
//
// Returns nil when the plan has no `## Tasks` section or no Task headings.
func ParsePlanTasks(planMarkdown string) []PlanTask {
	lines := strings.Split(planMarkdown, "\n")

	// Find the `## Tasks` section. Treat the first match as canonical.
	start := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "## Tasks" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}

	// Determine the section end: the next top-level `## ` heading after start
	// that is NOT inside a fenced code block.
	var fence fenceState
	end := len(lines)
	for i := start; i < len(lines); i++ {
		ln := lines[i]
		if fence.update(ln) {
			continue
		}
		if fence.inside() {
			continue
		}
		if strings.HasPrefix(ln, "## ") && !strings.HasPrefix(ln, "### ") {
			end = i
			break
		}
	}

	fence = fenceState{}
	var tasks []PlanTask
	var current *PlanTask
	for i := start; i < end; i++ {
		ln := lines[i]
		if fence.update(ln) {
			if current != nil {
				current.Body = append(current.Body, ln)
			}
			continue
		}
		if !fence.inside() && taskHeadingRE.MatchString(ln) {
			if current != nil {
				tasks = append(tasks, *current)
			}
			current = &PlanTask{Heading: ln}
			continue
		}
		if current == nil {
			// Pre-amble before the first Task heading — ignore.
			continue
		}
		current.Body = append(current.Body, ln)
		if !fence.inside() {
			if m := repoTagRE.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
				current.Repos = append(current.Repos, m[1])
			}
		}
	}
	if current != nil {
		tasks = append(tasks, *current)
	}
	return tasks
}

// fenceState tracks whether a line iterator is currently inside a fenced
// code block. A fence opens on a line of 3+ matching backticks or tildes
// at column 0 and closes on a line of the same character whose run length
// is at least the opener's (CommonMark §4.5).
type fenceState struct {
	char byte
	n    int
}

// update advances the fence state for one line. Returns true when the line
// itself is a fence marker (open or close), so callers can decide how to
// treat it.
func (f *fenceState) update(ln string) bool {
	c, n := detectFenceMarker(ln)
	if c == 0 {
		return false
	}
	switch {
	case f.char == 0:
		f.char, f.n = c, n
	case c == f.char && n >= f.n:
		f.char, f.n = 0, 0
	}
	return true
}

func (f *fenceState) inside() bool { return f.char != 0 }

// detectFenceMarker returns the fence character and run length when ln is a
// fence opener/closer; otherwise (0, 0). Leading indentation is not honored
// — agent-generated plans place fences at column 0.
func detectFenceMarker(ln string) (byte, int) {
	if len(ln) < 3 {
		return 0, 0
	}
	c := ln[0]
	if c != '`' && c != '~' {
		return 0, 0
	}
	n := 0
	for n < len(ln) && ln[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0
	}
	return c, n
}

// PlanTaskRepos returns the sorted, deduplicated set of repo names tagged
// across the plan's Tasks section. Untagged tasks contribute nothing.
// Returns nil when the plan has no Tasks or no Repo tags.
func PlanTaskRepos(planMarkdown string) []string {
	tasks := ParsePlanTasks(planMarkdown)
	seen := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		for _, repo := range t.Repos {
			seen[repo] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ParsePhasePlanFrontend reads the plan-level frontend metadata flag.
// Missing legacy metadata, a missing field, or an unrecognized value all
// resolve to false for backward compatibility with already-authored plans.
func ParsePhasePlanFrontend(planMarkdown string) bool {
	metadata := extractMarkdownSection(planMarkdown, "## Metadata")
	if metadata == "" {
		return false
	}
	lines := strings.Split(metadata, "\n")
	for _, line := range lines {
		m := frontendMetadataRE.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(m[1])) {
		case "true":
			return true
		case "false":
			return false
		default:
			return false
		}
	}
	return false
}
