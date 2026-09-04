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
	"os"
	"regexp"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
)

func phaseMarkdownValidator(artifact RoleArtifactSpec) func(string, string, *Outcome) ([]ProtocolViolation, error) {
	return func(_ string, dir string, out *Outcome) ([]ProtocolViolation, error) {
		path := newestPhaseMarkdownArtifact(dir)
		if path == "" {
			return []ProtocolViolation{{Artifact: artifact.DisplayPath, Reason: missingArtifactReason(artifact.DisplayPath, dir)}}, nil
		}
		if artifact.Validate != roles.ValidatorPhaseMarkdown {
			body, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", artifact.DisplayPath, err)
			}
			var reasons []string
			switch artifact.Validate {
			case roles.ValidatorInquiryQuestions:
				reasons = inquiryQuestionsViolations(string(body))
			case roles.ValidatorDesignDocument:
				reasons = designDocumentViolations(string(body))
			}
			if len(reasons) > 0 {
				violations := make([]ProtocolViolation, 0, len(reasons))
				for _, reason := range reasons {
					violations = append(violations, ProtocolViolation{Artifact: artifact.DisplayPath, Reason: reason})
				}
				return violations, nil
			}
		}
		out.PhaseArtifactPath = path
		return nil, nil
	}
}

var numberedQuestionRE = regexp.MustCompile(`^[0-9]{1,9}[.)][ \t]+\S`)

// These checks enforce the documented artifact structure. Pending user
// decisions are tracked by the session protocol, not inferred from prose.
func inquiryQuestionsViolations(body string) []string {
	sections := phaseMarkdownSections(body, 1)
	_, single := sections["# Research Questions"]
	_, codebase := sections["# Codebase Research Questions"]
	_, web := sections["# Web Research Questions"]
	var headings []string
	switch {
	case single && !codebase && !web:
		headings = []string{"# Research Questions"}
	case !single && codebase && web:
		headings = []string{"# Codebase Research Questions", "# Web Research Questions"}
	default:
		return []string{"inquiry must contain either `# Research Questions` or both `# Codebase Research Questions` and `# Web Research Questions`"}
	}
	var reasons []string
	for _, heading := range headings {
		found := false
		for _, line := range sections[heading] {
			if numberedQuestionRE.MatchString(strings.TrimSpace(line)) {
				found = true
				break
			}
		}
		if !found {
			reasons = append(reasons, fmt.Sprintf("inquiry `%s` must contain a nonempty numbered list of research questions", heading))
		}
	}
	return reasons
}

func designDocumentViolations(body string) []string {
	sections := phaseMarkdownSections(body, 2)
	var reasons []string
	for _, heading := range []string{
		"## Problem Statement",
		"## Solution",
		"## User Stories",
		"## Implementation Decisions",
		"## Testing Decisions",
		"## Acceptance Criteria",
		"## Out of Scope",
		"## Further Notes",
	} {
		if strings.TrimSpace(strings.Join(sections[heading], "\n")) == "" {
			reasons = append(reasons, fmt.Sprintf("design must contain a nonempty `%s` section", heading))
		}
	}
	return reasons
}

// phaseMarkdownSections collects unfenced content under headings at the
// requested level. A parent heading ends the section; subheadings do not
// count as content. Fenced examples cannot satisfy a document contract.
func phaseMarkdownSections(body string, level int) map[string][]string {
	sections := make(map[string][]string)
	var fence fenceState
	current := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if fence.update(line) || fence.inside() {
			continue
		}
		if depth := headingLevel(line); depth > 0 {
			if depth <= level {
				current = ""
				if depth == level {
					current = line
					if _, exists := sections[current]; !exists {
						sections[current] = nil
					}
				}
			}
			continue
		}
		if current != "" {
			sections[current] = append(sections[current], line)
		}
	}
	return sections
}
