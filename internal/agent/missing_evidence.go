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
	"strconv"
	"strings"
)

const missingEvidenceMarker = "MISSING_EVIDENCE_REQUIREMENT"

// MissingEvidenceRequirement is one reviewer-authored evidence row request.
type MissingEvidenceRequirement struct {
	Phase       int
	Kind        string
	Requirement string
}

// MissingEvidenceRequirements extracts structured missing-evidence markers
// from review feedback. The marker grammar is intentionally small so reviewer
// prose can vary while the harness still has a deterministic routing signal:
//
//	MISSING_EVIDENCE_REQUIREMENT visual|behavioral: <requirement>
//	MISSING_EVIDENCE_REQUIREMENT phase <n> visual|behavioral: <requirement>
func MissingEvidenceRequirements(feedback string) []MissingEvidenceRequirement {
	var out []MissingEvidenceRequirement
	for _, line := range strings.Split(feedback, "\n") {
		idx := strings.Index(line, missingEvidenceMarker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len(missingEvidenceMarker):])
		kind, requirement, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		phase, kind, ok := parseMissingEvidenceMarkerHead(kind)
		if !ok {
			continue
		}
		if kind != "visual" && kind != "behavioral" {
			continue
		}
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		out = append(out, MissingEvidenceRequirement{
			Phase:       phase,
			Kind:        kind,
			Requirement: requirement,
		})
	}
	return out
}

func parseMissingEvidenceMarkerHead(head string) (int, string, bool) {
	fields := strings.Fields(strings.TrimSpace(head))
	switch len(fields) {
	case 1:
		return 0, strings.ToLower(fields[0]), true
	case 2:
		phaseText, ok := strings.CutPrefix(strings.ToLower(fields[0]), "phase-")
		if !ok {
			return 0, "", false
		}
		phase, err := strconv.Atoi(phaseText)
		if err != nil || phase <= 0 {
			return 0, "", false
		}
		return phase, strings.ToLower(fields[1]), true
	case 3:
		if !strings.EqualFold(fields[0], "phase") {
			return 0, "", false
		}
		phase, err := strconv.Atoi(fields[1])
		if err != nil || phase <= 0 {
			return 0, "", false
		}
		return phase, strings.ToLower(fields[2]), true
	default:
		return 0, "", false
	}
}

// MissingEvidencePlanRevisionFeedback renders the phase-plan critic feedback
// used when review discovers a user-facing surface without contract-backed
// visual or behavioral evidence coverage.
func MissingEvidencePlanRevisionFeedback(requirements []MissingEvidenceRequirement) string {
	var b strings.Builder
	b.WriteString("# Phase Plan Revision Required: Missing Evidence Coverage\n\n")
	b.WriteString("The implementation reviewer found user-facing work without matching visual or behavioral testing-contract coverage. Revise the phase plan so the next implementation attempt recompiles normal contract rows from the plan instead of inventing report rows.\n\n")
	b.WriteString("## Findings\n")
	if len(requirements) == 0 {
		b.WriteString("- **High**: Missing visual or behavioral evidence coverage was reported, but no structured requirement was parsed.\n\n")
	} else {
		for _, req := range requirements {
			if req.Phase > 0 {
				fmt.Fprintf(&b, "- **High**: %s phase %d %s: %s\n", missingEvidenceMarker, req.Phase, req.Kind, req.Requirement)
			} else {
				fmt.Fprintf(&b, "- **High**: %s %s: %s\n", missingEvidenceMarker, req.Kind, req.Requirement)
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("## Required Phase-Plan Repair\n")
	b.WriteString("- Add each accepted visual requirement as a checklist item under `### Visual Evidence`.\n")
	b.WriteString("- Add each accepted behavioral requirement as a checklist item under `### Behavioral Evidence`.\n")
	b.WriteString("- If a requested evidence item is consciously declined, keep the section valid with exactly one `None required: <reason>` checklist entry that explains why no evidence is required.\n\n")
	b.WriteString("### Visual Evidence\n")
	b.WriteString("- [ ] <add visual requirements here, or `None required: <reason>`>\n\n")
	b.WriteString("### Behavioral Evidence\n")
	b.WriteString("- [ ] <add behavioral requirements here, or `None required: <reason>`>\n\n")
	b.WriteString("## Invalid Repair Paths\n")
	b.WriteString("- Do not add verification-report.yaml rows directly; reports may only contain rows compiled from the bound testing contract.\n")
	b.WriteString("- Do not use testing-contract.yaml Changes entries to create new evidence rows; Changes can only revise known item IDs.\n")
	return b.String()
}
