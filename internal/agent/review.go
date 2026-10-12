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
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
)

type reviewStatus int

const (
	ReviewApproved reviewStatus = iota
	reviewChangesRequested
	reviewFailed
)

// isApproved reports whether the status is an exit-allowed verdict. Both
// ReviewApproved let the loop exit; only
// ReviewChangesRequested and ReviewFailed continue iterating.
func (s reviewStatus) isApproved() bool {
	return s == ReviewApproved
}

func (s reviewStatus) String() string {
	switch s {
	case ReviewApproved:
		return agentStatusApproved
	case reviewChangesRequested:
		return agentStatusChangesRequested
	case reviewFailed:
		return "FAILED"
	default:
		return fmt.Sprintf("ReviewStatus(%d)", int(s))
	}
}

// verificationItemView projects a RequiredVerificationItem for review templates.
type verificationItemView struct {
	Name        string
	Requirement string
}

// reviewUserInput is the data passed to review.user.tmpl.
type reviewUserInput struct {
	Iteration int
	IterDir   string

	GateLabel          string
	FinalGate          bool
	LiveRunAxis        bool
	DiffBase           string
	FeatureDescription string
	DesignArtifactPath string
	PreviousFeedback   string
	// RefactorPassForkPoint names a refactor child's fork-point commits
	// ("repo @ sha"). It resolves the spec's "fork point" references and
	// attributes cumulative-diff hunks between parent and pass. Empty for
	// top-level features.
	RefactorPassForkPoint string

	RoadmapPath            string
	PlanPath               string
	ExitCriteria           string
	AcceptanceClause       string
	VerificationReportPath string

	ContractPath         string
	RequiredVerification []verificationItemView

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string

	ProgressPath string
	PhaseType    string

	FeedbackPath string
}

// implementationReviewAxisPromptOpts carries the shared prompt context for an
// implementation review axis. The per-phase gate populates the phase-scoped
// fields; the Final Review gate also supplies cumulative-diff and prior
// implementation evidence context.
type implementationReviewAxisPromptOpts struct {
	Gate      implementationReviewGate
	AxisLabel string

	FeatureDescription string
	DesignArtifactPath string
	LiveRunAxis        bool
	ExitCriteria       string
	AcceptanceClause   string
	DiffBase           string
	PreviousFeedback   string
	// RefactorPassForkPoint resolves the spec's "fork point" references for a
	// refactor child ("repo @ sha"). Empty for top-level features.
	RefactorPassForkPoint string

	ProgressPath           string
	IterDir                string
	ContractPath           string
	VerificationReportPath string
	Iteration              int
	RequiredVerification   []requiredVerificationItem
	RoadmapPath            string
	PlanPath               string
	PhaseType              string
	FeedbackPath           string

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string
}

// buildImplementationReviewAxisPromptWithOpts renders one gate-aware
// implementation review axis prompt.
func buildImplementationReviewAxisPromptWithOpts(opts implementationReviewAxisPromptOpts) string {
	required := make([]verificationItemView, 0, len(opts.RequiredVerification))
	for _, item := range opts.RequiredVerification {
		required = append(required, verificationItemView{
			Name:        item.Name,
			Requirement: item.Requirement,
		})
	}

	phaseType := opts.PhaseType
	if opts.Gate == implementationReviewGateFinal {
		phaseType = ""
	}

	return prompts.ImplementationReviewAxisUserPrompt(implementationReviewAxisUserInput{
		reviewUserInput: reviewUserInput{
			Iteration:                            opts.Iteration,
			IterDir:                              opts.IterDir,
			GateLabel:                            implementationReviewGateLabel(opts.Gate),
			FinalGate:                            opts.Gate == implementationReviewGateFinal,
			LiveRunAxis:                          opts.LiveRunAxis,
			DiffBase:                             opts.DiffBase,
			RefactorPassForkPoint:                opts.RefactorPassForkPoint,
			FeatureDescription:                   opts.FeatureDescription,
			DesignArtifactPath:                   opts.DesignArtifactPath,
			PreviousFeedback:                     opts.PreviousFeedback,
			RoadmapPath:                          opts.RoadmapPath,
			PlanPath:                             opts.PlanPath,
			ExitCriteria:                         opts.ExitCriteria,
			AcceptanceClause:                     opts.AcceptanceClause,
			VerificationReportPath:               opts.VerificationReportPath,
			ContractPath:                         opts.ContractPath,
			RequiredVerification:                 required,
			PriorImplementationReportPaths:       opts.PriorImplementationReportPaths,
			PriorImplementationEvidenceRootDirs:  opts.PriorImplementationEvidenceRootDirs,
			PriorImplementationEvidenceArtifacts: opts.PriorImplementationEvidenceArtifacts,
			ProgressPath:                         opts.ProgressPath,
			PhaseType:                            phaseType,
			FeedbackPath:                         opts.FeedbackPath,
		},
		AxisLabel: opts.AxisLabel,
	})
}

func implementationReviewGateLabel(gate implementationReviewGate) string {
	switch gate {
	case implementationReviewGateFinal:
		return "Final Review"
	default:
		return "Per-Phase Implementation Review"
	}
}

// reviewVerdictHeading is the ATX heading used by the review handoff
// protocol. The body of this section MUST be exactly one of
// reviewVerdictTokens on its own line.
const reviewVerdictHeading = "## Verdict"

// reviewRequiredSections is the closed list of `## ` sections the reviewer
// (LLM or deterministic gate) MUST emit, in order. Missing or out-of-order
// headings are protocol violations — the parser names each one separately so
// the synthesized feedback enumerates every defect at once (same shape as
// ParseProgressMd).
var reviewRequiredSections = []string{
	"## Findings",
	"## Suggestions",
	reviewVerdictHeading,
}

// reviewVerdictTokens is the closed set of strings the harness will route on.
// Anything else under `## Verdict` is a protocol violation.
var reviewVerdictTokens = map[string]reviewStatus{
	agentStatusApproved:         ReviewApproved,
	agentStatusChangesRequested: reviewChangesRequested,
}

// reviewStatusSkipped marks IterationMeta.ReviewStatus when the per-iteration
// review gate was intentionally not run (e.g. SkipIterationReview).
const reviewStatusSkipped = "skipped"

// parsedReviewFeedback is the structured view of a review-feedback.md the
// harness uses for routing the bounded review/validation helper. Body holds
// the entire file contents verbatim so callers can re-emit it as the next
// implement iteration's feedback block. Markers carries the optional
// sticky-approval signal validators emit under `## Sticky Approval`.
//
// On parse failure each defect is recorded in ProtocolViolations and the
// typed Verdict / Markers fields are left zero. Callers MUST inspect
// ProtocolViolations before trusting Verdict.
type parsedReviewFeedback struct {
	Verdict            reviewStatus
	Body               string
	Findings           string
	Suggestions        string
	Markers            validatorMarkers
	ProtocolViolations []string
}

// ok reports whether the feedback file satisfies the handoff contract.
// Callers route on Verdict only when ok() is true; otherwise they
// short-circuit to a structured CHANGES_REQUESTED.
func (p *parsedReviewFeedback) ok() bool {
	return p != nil && p.Verdict != reviewFailed && len(p.ProtocolViolations) == 0
}

// parseReviewFeedback reads review-feedback.md at path and validates it
// against the handoff contract spelled out in the review skills. The
// function is conservative: every defect it can name deterministically
// becomes a separate ProtocolViolations entry — same shape as
// ParseProgressMd — so the synthesized feedback enumerates all of them in
// one pass. Callers MUST inspect ProtocolViolations before trusting Verdict.
//
// A missing file is not an error from the function's perspective: the
// returned ParsedReviewFeedback flags it as a protocol violation so the
// short-circuit feedback path can surface a useful message to the next
// iteration.
func parseReviewFeedback(path string) (*parsedReviewFeedback, error) {
	if path == "" {
		return nil, fmt.Errorf("ParseReviewFeedback: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &parsedReviewFeedback{
				Verdict: reviewFailed,
				ProtocolViolations: []string{
					fmt.Sprintf("review-feedback.md not found at %s — the review helper must write the structured handoff file before ending its turn", path),
				},
			}, nil
		}
		return nil, fmt.Errorf("reading review-feedback.md at %s: %w", path, err)
	}

	// Default to ReviewFailed so callers that forget to inspect
	// ProtocolViolations cannot silently route a malformed file into the
	// success path (ReviewApproved is the iota-0 zero value).
	parsed := &parsedReviewFeedback{Body: string(data), Verdict: reviewFailed}
	body := parsed.Body

	headingPositions := findSectionHeadings(body, reviewRequiredSections)
	missingOrUnordered := false
	lastPos := -1
	for _, h := range reviewRequiredSections {
		pos, ok := headingPositions[h]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("review-feedback.md missing required section %q — the three `## ` sections must all be present, in order", h))
			missingOrUnordered = true
			continue
		}
		if pos < lastPos {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("review-feedback.md section %q appears out of order — required order is: %s", h, strings.Join(reviewRequiredSections, ", ")))
			missingOrUnordered = true
		}
		lastPos = pos
	}

	parsed.Findings = strings.TrimSpace(extractMarkdownSection(body, "## Findings"))
	parsed.Suggestions = strings.TrimSpace(extractMarkdownSection(body, "## Suggestions"))

	verdictBody := extractMarkdownSection(body, reviewVerdictHeading)
	if verdictBody == "" && !missingOrUnordered {
		parsed.ProtocolViolations = append(parsed.ProtocolViolations,
			fmt.Sprintf("review-feedback.md `%s` section is empty — emit one of {APPROVED, CHANGES_REQUESTED} on its own line", reviewVerdictHeading))
	} else if verdictBody != "" {
		token := firstNonBlankLine(verdictBody)
		state, ok := reviewVerdictTokens[token]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
				"review-feedback.md `%s` body must be exactly one of {APPROVED, CHANGES_REQUESTED} on its own line; got %q", reviewVerdictHeading, token))
		} else {
			parsed.Verdict = state
		}
	}

	// Optional `## Sticky Approval` section carries the per-axis
	// frozen-section signal validators emit so the next revise attempt
	// can preserve approved-axis sections byte-equal. Absence is not a
	// violation; the implementation reviewer never emits this section.
	if sticky := extractMarkdownSection(body, "## Sticky Approval"); sticky != "" {
		parsed.Markers = parseStickyApprovalSection(sticky)
	}

	return parsed, nil
}

// formatStructuredReviewFeedback wraps a free-form feedback body in the
// canonical `## Findings` / `## Suggestions` / `## Verdict` schema. Used by
// deterministic gates (Report Integrity, deferral ledger) that synthesize a
// CHANGES_REQUESTED verdict programmatically.
//
// findings, suggestions, and verdict are emitted verbatim under their
// section headings. When findings or suggestions are empty, the canonical
// `- (none)` placeholder is used so the file still parses cleanly.
func formatStructuredReviewFeedback(title, findings, suggestions string, verdict reviewStatus) string {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	b.WriteString("## Findings\n")
	if strings.TrimSpace(findings) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(findings, "\n"))
	}
	b.WriteString("## Suggestions\n")
	if strings.TrimSpace(suggestions) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(suggestions, "\n"))
	}
	b.WriteString(reviewVerdictHeading)
	b.WriteByte('\n')
	switch verdict {
	case ReviewApproved:
		b.WriteString("APPROVED\n")
	case reviewChangesRequested:
		b.WriteString("CHANGES_REQUESTED\n")
	default:
		// Fall back to CHANGES_REQUESTED so a downstream re-parse never
		// approves on an indeterminate input.
		b.WriteString("CHANGES_REQUESTED\n")
	}
	return b.String()
}

// firstNonBlankLine returns the first non-empty line in body, trimmed. Used
// to extract the canonical token from `## Verdict` / `## Iteration State`
// section bodies where free-form prose may follow.
func firstNonBlankLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// parseStickyApprovalSection extracts the axis name and frozen sections list
// from the `## Sticky Approval` section body. The expected format is:
//
//	axis: <axis>
//	frozen_sections:
//	  - <heading 1>
//	  - <heading 2>
//
// Returns a zero-value ValidatorMarkers when the section is empty or
// malformed; an empty axis disables sticky-approval propagation entirely.
func parseStickyApprovalSection(section string) validatorMarkers {
	var m validatorMarkers
	lines := strings.Split(section, "\n")
	inFrozen := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "axis:"); ok {
			m.AxisApproved = strings.TrimSpace(rest)
			inFrozen = false
			continue
		}
		if trimmed == "frozen_sections:" {
			inFrozen = true
			continue
		}
		if !inFrozen {
			continue
		}
		bullet, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "- ")
		if !ok {
			inFrozen = false
			continue
		}
		heading := strings.TrimSpace(bullet)
		if heading == "" {
			continue
		}
		// Drop SKILL-template placeholders that critics sometimes copy
		// verbatim instead of filling in (e.g. "<exact section heading 1>").
		// A bullet whose entire content is wrapped in angle brackets is
		// not a real section heading.
		if strings.HasPrefix(heading, "<") && strings.HasSuffix(heading, ">") {
			continue
		}
		m.FrozenSections = append(m.FrozenSections, heading)
	}
	return m
}

// validatorMarkers carries the sticky-approval signal a per-axis validator
// emits in the `## Sticky Approval` section of its review-feedback file.
// AxisApproved is the axis name (e.g. "architecture", "scope"); FrozenSections
// is the byte-equal set of artifact headings the next revise attempt must
// preserve verbatim — see the "Sticky Approval Respect" procedure in
// skills/revise-roadmap/SKILL.md.
type validatorMarkers struct {
	AxisApproved   string
	FrozenSections []string
}
