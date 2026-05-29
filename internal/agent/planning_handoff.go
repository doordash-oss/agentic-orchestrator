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
	"crypto/sha256"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// PlanningHandoffFilename is the rolling scratch artifact used by planning
// Smart Zone continuations inside a single attempt or refactor-plan step.
const PlanningHandoffFilename = "planning-handoff.md"

// PlanningHandoffState is the continuation routing token parsed from
// planning-handoff.md.
type PlanningHandoffState int

const (
	PlanningHandoffInvalid PlanningHandoffState = iota
	PlanningHandoffContinue
	PlanningHandoffComplete
)

func (s PlanningHandoffState) String() string {
	switch s {
	case PlanningHandoffContinue:
		return "CONTINUE"
	case PlanningHandoffComplete:
		return "COMPLETE"
	default:
		return "INVALID"
	}
}

// ParsedPlanningHandoff is the deterministic parse result for the shared
// planning-handoff.md artifact.
type ParsedPlanningHandoff struct {
	State              PlanningHandoffState
	ProgressRegion     string
	ProtocolViolations []string
}

// OK reports whether the handoff satisfies the planning continuation contract.
func (p *ParsedPlanningHandoff) OK() bool {
	return p != nil && p.State != PlanningHandoffInvalid && len(p.ProtocolViolations) == 0
}

var planningHandoffRequiredSections = []string{
	"## Understanding",
	"## Plan Progress",
	"## Handoff State",
}

var planningHandoffProgressSubsections = []string{
	"### Drafted",
	"### Remaining",
	"### Where I stopped",
}

var validPlanningHandoffStateTokens = map[string]PlanningHandoffState{
	"CONTINUE": PlanningHandoffContinue,
	"COMPLETE": PlanningHandoffComplete,
}

// ParsePlanningHandoffMd parses the shared planning Smart Zone handoff
// artifact. It returns validation-friendly protocol violations instead of
// failing fast so callers can surface all deterministic defects at once.
func ParsePlanningHandoffMd(path string) (*ParsedPlanningHandoff, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("ParsePlanningHandoffMd: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ParsedPlanningHandoff{
				ProtocolViolations: []string{
					fmt.Sprintf("%s not found at %s", PlanningHandoffFilename, path),
				},
			}, nil
		}
		return nil, fmt.Errorf("reading %s at %s: %w", PlanningHandoffFilename, path, err)
	}

	body := string(data)
	parsed := &ParsedPlanningHandoff{}

	positions := findSectionHeadings(body, planningHandoffRequiredSections)
	lastPos := -1
	for _, heading := range planningHandoffRequiredSections {
		pos, ok := positions[heading]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s missing required section %q", PlanningHandoffFilename, heading))
			continue
		}
		if pos < lastPos {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s section %q appears out of order; required order is: %s",
					PlanningHandoffFilename, heading, strings.Join(planningHandoffRequiredSections, ", ")))
		}
		lastPos = pos
	}

	progressBody := extractMarkdownSection(body, "## Plan Progress")
	for _, heading := range planningHandoffProgressSubsections {
		if _, ok := findSectionHeadings(progressBody, []string{heading})[heading]; !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s `## Plan Progress` missing required subsection %q", PlanningHandoffFilename, heading))
		}
	}

	if stateBody := extractMarkdownSection(body, "## Handoff State"); stateBody != "" {
		token, _ := splitStateTokenAndNote(stateBody)
		if state, ok := validPlanningHandoffStateTokens[token]; ok {
			parsed.State = state
		} else {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
				"%s `## Handoff State` body must be exactly one of {CONTINUE, COMPLETE} on its own line; got %q",
				PlanningHandoffFilename, token))
		}
	} else {
		if _, ok := positions["## Handoff State"]; ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("%s `## Handoff State` section is empty; emit CONTINUE or COMPLETE", PlanningHandoffFilename))
		}
	}

	understanding := extractMarkdownSection(body, "## Understanding")
	if progressBody != "" || understanding != "" {
		parsed.ProgressRegion = strings.TrimSpace("## Understanding\n" + strings.TrimSpace(understanding) + "\n\n## Plan Progress\n" + strings.TrimSpace(progressBody))
	}
	return parsed, nil
}

// PlanningHandoffFingerprint computes a stable fingerprint over the handoff
// narrative, excluding per-attempt paths and validation-feedback filenames.
func PlanningHandoffFingerprint(path string) (string, error) {
	parsed, err := ParsePlanningHandoffMd(path)
	if err != nil {
		return "", err
	}
	if parsed == nil {
		return "", nil
	}
	region := normalizePlanningHandoffProgressRegion(parsed.ProgressRegion)
	h := sha256.Sum256([]byte(region))
	return fmt.Sprintf("%x", h), nil
}

var (
	planningAbsolutePathRE     = regexp.MustCompile(`(?:/[^/\s` + "`" + `]+)+/(?:attempt-\d+|phase-\d+|run-\d+|plan|roadmap|refactor-\d+|[^/\s` + "`" + `]+)`)
	planningAttemptDirRE       = regexp.MustCompile(`attempt-\d+`)
	planningRunDirRE           = regexp.MustCompile(`run-\d+`)
	planningFeedbackFilenameRE = regexp.MustCompile(`validation(?:-[a-z0-9_]+)*-feedback\.md`)
)

func normalizePlanningHandoffProgressRegion(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = planningAbsolutePathRE.ReplaceAllString(s, "<path>")
	s = planningAttemptDirRE.ReplaceAllString(s, "attempt-NN")
	s = planningRunDirRE.ReplaceAllString(s, "run-NNN")
	s = planningFeedbackFilenameRE.ReplaceAllString(s, "validation-feedback.md")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
