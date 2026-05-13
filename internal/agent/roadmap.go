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
	"fmt"
	"regexp"
	"strings"
)

// RoadmapPhase represents a single phase extracted from a roadmap document.
type RoadmapPhase struct {
	Number        int
	Name          string
	Type          string // phase category: "tracer-bullet", "tdd-fill-in", or "collapsed"
	Goal          string
	StubsToRetire []string // optional roadmap-assigned retirements
}

// Matches "## Phase 1: Title", "## Phase 1/1 (Collapsed): Title", "## Phase 1/3: Title", etc.
var phaseHeaderRe = regexp.MustCompile(`(?m)^## Phase (\d+)(?:/\d+)?(?:\s*\([^)]*\))?:\s*(.+)`)

// ParseRoadmap extracts phase information from a roadmap markdown document.
// Scans for `## Phase N:` headers. Single phase = "collapsed", otherwise
// phase 1 = "tracer-bullet" and later phases = "tdd-fill-in".
func ParseRoadmap(roadmapText string) ([]RoadmapPhase, error) {
	matches := phaseHeaderRe.FindAllStringSubmatchIndex(roadmapText, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no phases found in roadmap (expected ## Phase N: headers)")
	}

	var phases []RoadmapPhase
	for i, match := range matches {
		var num int
		_, _ = fmt.Sscanf(roadmapText[match[2]:match[3]], "%d", &num)
		name := strings.TrimSpace(roadmapText[match[4]:match[5]])

		// Extract content between this header and the next phase header (or end)
		contentStart := match[1]
		contentEnd := len(roadmapText)
		if i+1 < len(matches) {
			contentEnd = matches[i+1][0]
		}
		content := roadmapText[contentStart:contentEnd]

		goal := extractSection(content, "Goal")
		stubs := extractListSection(content, "Retires Stubs")

		phase := RoadmapPhase{
			Number:        num,
			Name:          name,
			Goal:          goal,
			StubsToRetire: stubs,
		}
		phases = append(phases, phase)
	}

	// Assign types
	for i := range phases {
		if len(phases) == 1 {
			phases[i].Type = "collapsed"
		} else if phases[i].Number == 1 {
			phases[i].Type = "tracer-bullet"
		} else {
			phases[i].Type = "tdd-fill-in"
		}
	}

	return phases, nil
}

// extractSection extracts text content under a ### heading.
func extractSection(content, heading string) string {
	re := regexp.MustCompile(`(?mi)^###\s+` + regexp.QuoteMeta(heading) + `\s*\n`)
	loc := re.FindStringIndex(content)
	if loc == nil {
		return ""
	}
	start := loc[1]
	// Find the next heading or end of content
	nextHeading := regexp.MustCompile(`(?m)^##`)
	endLoc := nextHeading.FindStringIndex(content[start:])
	end := len(content)
	if endLoc != nil {
		end = start + endLoc[0]
	}
	return strings.TrimSpace(content[start:end])
}

// extractListSection extracts bullet items under a ### heading.
func extractListSection(content, heading string) []string {
	text := extractSection(content, heading)
	if text == "" {
		return nil
	}
	var items []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			items = append(items, strings.TrimSpace(line[2:]))
		}
	}
	return items
}
