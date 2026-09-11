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

package git

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// StackSectionHeader is the markdown header for the stack section.
const StackSectionHeader = "## Stack"

// Markers rendered on individual stack layer lines.
const (
	stackNoChangesText = "no changes in this repository"
	stackCurrentMarker = "(this pull request)"
)

// StackSectionLayer describes one layer of a stack as it applies to a single
// repository: the layer's position and roadmap title, the PR opened for it in
// this repository (empty when the layer has no changes here), that PR's state,
// and whether this layer's PR is the one whose body is being rendered.
type StackSectionLayer struct {
	Position int
	Title    string
	PRURL    string
	PRState  string
	Current  bool
}

// BuildStackSection renders the stack section listing every layer of the run's
// stack in position order: a layer with a PR in this repository links it with
// its state, a layer without one shows its title and a no-changes note, and the
// current layer's line is marked. It returns empty when the repository has a
// single PR in the stack (or none) — there is no stack context worth showing.
func BuildStackSection(layers []StackSectionLayer) string {
	prCount := 0
	for _, l := range layers {
		if l.PRURL != "" {
			prCount++
		}
	}
	if prCount <= 1 {
		return ""
	}

	ordered := make([]StackSectionLayer, len(layers))
	copy(ordered, layers)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Position < ordered[j].Position
	})

	var sb strings.Builder
	sb.WriteString(StackSectionHeader)
	sb.WriteString("\n\n")
	for i, l := range ordered {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(renderStackLayerLine(l))
	}
	return sb.String()
}

// renderStackLayerLine renders one layer line: position and title, then either
// the PR link and state or the no-changes note, with the current-PR marker last.
func renderStackLayerLine(l StackSectionLayer) string {
	line := fmt.Sprintf("%d. %s", l.Position, l.Title)
	if l.PRURL == "" {
		return line + " - " + stackNoChangesText
	}
	link := fmt.Sprintf("[PR](%s)", l.PRURL)
	if num := path.Base(l.PRURL); num != "" && num != "." && num != "/" {
		link = fmt.Sprintf("[#%s](%s)", num, l.PRURL)
	}
	line += " - " + link
	if l.PRState != "" {
		line += " (" + l.PRState + ")"
	}
	if l.Current {
		line += " " + stackCurrentMarker
	}
	return line
}

// InjectStackSection inserts the stack section into a PR body, replacing any
// existing stack section by heading. A body that already carries exactly this
// section text is returned unchanged. The section sits directly above the
// related-PRs section when present, else above the PR signature, else at the
// end of the body.
func InjectStackSection(body, section string) string {
	if section == "" {
		return body
	}
	// No-op when the rendered text is unchanged.
	if strings.Contains(body, section) {
		return body
	}
	if strings.Contains(body, StackSectionHeader) {
		body = removeSectionByHeader(body, StackSectionHeader)
	}
	if strings.Contains(body, CrossRefSectionHeader) {
		idx := strings.Index(body, CrossRefSectionHeader)
		before := body[:idx]
		switch {
		case before == "":
		case strings.HasSuffix(before, "\n\n"):
		case strings.HasSuffix(before, "\n"):
			before += "\n"
		default:
			before += "\n\n"
		}
		return before + section + "\n\n" + body[idx:]
	}
	if strings.Contains(body, PRSignature) {
		idx := strings.Index(body, PRSignature)
		before := body[:idx]
		if before == "" {
			return section + body[idx:]
		}
		return before + "\n\n" + section + body[idx:]
	}
	if body != "" {
		return body + "\n\n" + section
	}
	return section
}

// ExtractStackSection extracts the stack section from a PR body, including the
// header and trimmed of trailing whitespace. Returns empty string when no stack
// section is found.
func ExtractStackSection(body string) string {
	return extractSectionByHeader(body, StackSectionHeader)
}

// RemoveStackSection removes the stack section from a PR body.
// Cleans up extra whitespace left behind.
func RemoveStackSection(body string) string {
	return removeSectionByHeader(body, StackSectionHeader)
}

// UpdatePRBodiesWithStackSection injects a rendered stack section into every
// given PR body through the remote PR-body read/update operations. Bodies the
// injection leaves unchanged are not written back. Errors are collected per PR
// rather than aborting the set.
func UpdatePRBodiesWithStackSection(prURLs []string, section string) []error {
	if section == "" {
		return nil
	}
	return updatePRBodies(prURLs, func(body string) string {
		return InjectStackSection(body, section)
	})
}
