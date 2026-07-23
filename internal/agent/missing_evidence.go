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
		if kind != testingContractVisualSource && kind != testingContractBehavioralSource {
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

const insufficientEvidenceMarker = "INSUFFICIENT_EVIDENCE_REQUIREMENT"

// InsufficientEvidenceRequirement is one reviewer-authored request to
// restructure an existing evidence row whose scope is unsatisfiable as
// written (e.g. one row contracting an entire capture matrix). Repair is a
// phase-plan revision, not another implementer lap against the same row.
type InsufficientEvidenceRequirement struct {
	ItemID      string
	Requirement string
}

// InsufficientEvidenceRequirements extracts structured markers of the form:
//
//	INSUFFICIENT_EVIDENCE_REQUIREMENT <item_id>: <revised requirement>
//
// Only visual_/behavioral_ item IDs are accepted — command rows already have
// their own contract-error revision path.
func InsufficientEvidenceRequirements(feedback string) []InsufficientEvidenceRequirement {
	var out []InsufficientEvidenceRequirement
	for _, line := range strings.Split(feedback, "\n") {
		idx := strings.Index(line, insufficientEvidenceMarker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(line[idx+len(insufficientEvidenceMarker):])
		head, requirement, ok := strings.Cut(rest, ":")
		if !ok {
			continue
		}
		itemID := strings.Trim(strings.TrimSpace(head), "`")
		requirement = strings.TrimSpace(requirement)
		if requirement == "" || !isEvidenceContractItemID(itemID) {
			continue
		}
		out = append(out, InsufficientEvidenceRequirement{ItemID: itemID, Requirement: requirement})
	}
	return out
}

func isEvidenceContractItemID(itemID string) bool {
	return strings.HasPrefix(itemID, testingContractVisualSource+"_") ||
		strings.HasPrefix(itemID, testingContractBehavioralSource+"_")
}

// EvidencePlanRevisionFeedback renders one combined plan-revision request for
// both marker kinds so the planner repairs coverage and row structure in a
// single pass.
func EvidencePlanRevisionFeedback(missing []MissingEvidenceRequirement, insufficient []InsufficientEvidenceRequirement) string {
	if len(insufficient) == 0 {
		return MissingEvidencePlanRevisionFeedback(missing)
	}
	var b strings.Builder
	if len(missing) > 0 {
		b.WriteString(MissingEvidencePlanRevisionFeedback(missing))
		b.WriteString("\n")
	}
	b.WriteString("# Phase Plan Revision Required: Restructure Evidence Rows\n\n")
	b.WriteString("The implementation reviewer found existing evidence rows whose scope cannot be satisfied or audited as written. Replace each row's plan bullet(s) under `### Visual Evidence` / `### Behavioral Evidence` per the revised requirement below. Split matrix-shaped visual requirements into one checklist item per surface/state/size/theme cell using `[size: WxH]` tags; give each behavioral journey its own checklist item ending with its packaged executable command in backticks.\n\n")
	b.WriteString("## Findings\n")
	for _, req := range insufficient {
		fmt.Fprintf(&b, "- **High**: %s `%s`: %s\n", insufficientEvidenceMarker, req.ItemID, req.Requirement)
	}
	b.WriteString("\n## Invalid Repair Paths\n")
	b.WriteString("- Do not edit verification-report.yaml or testing-contract.yaml directly; rows recompile from the revised plan.\n")
	return b.String()
}
