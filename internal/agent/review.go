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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/roles"
)

type ReviewStatus int

const (
	ReviewApproved ReviewStatus = iota
	ReviewChangesRequested
	ReviewFailed
)

// IsApproved reports whether the status is an exit-allowed verdict. Both
// ReviewApproved let the loop exit; only
// ReviewChangesRequested and ReviewFailed continue iterating.
func (s ReviewStatus) IsApproved() bool {
	return s == ReviewApproved
}

func (s ReviewStatus) String() string {
	switch s {
	case ReviewApproved:
		return agentStatusApproved
	case ReviewChangesRequested:
		return agentStatusChangesRequested
	case ReviewFailed:
		return "FAILED"
	default:
		return fmt.Sprintf("ReviewStatus(%d)", int(s))
	}
}

// BuildReviewPrompt constructs the prompt for the review gate.
// Large artifacts (plan, roadmap, progress, verification report) are referenced
// by path so the reviewer reads them via tool use, keeping the prompt compact.
// Per-phase implementation review now uses axis prompts; this helper remains
// for the generic review prompt contract and tests.
//
// feedbackPath is the absolute path to the structured `review-feedback.md` the
// reviewer must write under the Handoff Contract. Pass "" when the caller is
// only exercising the prompt body (e.g. unit tests for optional sections).
//
// The prose lives in internal/agent/prompts/templates/review.user.tmpl.
func BuildReviewPrompt(planPath, exitCriteria, progressPath, iterDir, contractPath, verificationReportPath string, iteration int, requiredVerification []RequiredVerificationItem, roadmapPath, phaseType, feedbackPath string) string {
	required := make([]roles.VerificationItemView, 0, len(requiredVerification))
	for _, item := range requiredVerification {
		required = append(required, roles.VerificationItemView{
			Name:        item.Name,
			Requirement: item.Requirement,
		})
	}

	return roles.BuildReviewPrompt(roles.ReviewUserInput{
		Iteration:              iteration,
		IterDir:                iterDir,
		RoadmapPath:            roadmapPath,
		PlanPath:               planPath,
		ExitCriteria:           exitCriteria,
		VerificationReportPath: verificationReportPath,
		ContractPath:           contractPath,
		RequiredVerification:   required,
		ProgressPath:           progressPath,
		PhaseType:              phaseType,
		FeedbackPath:           feedbackPath,
	})
}

// ImplementationReviewAxisPromptOpts carries the shared prompt context for an
// implementation review axis. The per-phase gate populates the phase-scoped
// fields; the Final Review gate also supplies cumulative-diff and prior
// implementation evidence context.
type ImplementationReviewAxisPromptOpts struct {
	Gate      implementationReviewGate
	AxisLabel string

	FeatureDescription string
	DesignArtifactPath string
	LiveRunAxis        bool
	ExitCriteria       string
	AcceptanceClause   string
	DiffBase           string
	PreviousFeedback   string
	// PriorAxisReport carries this axis's own verbatim round N-1
	// review-feedback.md for Final Review round N>1.
	PriorAxisReport string
	// RepoDeltas carries the per-repo incremental diff blocks for Final
	// Review round N>1.
	RepoDeltas []roles.RepoDeltaBlock
	// RefactorPassForkPoint resolves the spec's "fork point" references for a
	// refactor child ("repo @ sha"). Empty for top-level features.
	RefactorPassForkPoint string

	ProgressPath           string
	IterDir                string
	ContractPath           string
	VerificationReportPath string
	Iteration              int
	RequiredVerification   []RequiredVerificationItem
	RoadmapPath            string
	PlanPath               string
	PhaseType              string
	FeedbackPath           string

	PriorImplementationReportPaths       []string
	PriorImplementationEvidenceRootDirs  []string
	PriorImplementationEvidenceArtifacts []string
}

// BuildImplementationReviewAxisPrompt constructs the prompt for one
// implementation review axis. It carries the same review context as the legacy
// single-reviewer prompt plus the selected axis label.
func BuildImplementationReviewAxisPrompt(planPath, exitCriteria, progressPath, iterDir, contractPath, verificationReportPath string, iteration int, requiredVerification []RequiredVerificationItem, roadmapPath, phaseType, feedbackPath, axisLabel string) string {
	return BuildImplementationReviewAxisPromptWithOpts(ImplementationReviewAxisPromptOpts{
		Gate:                   implementationReviewGatePerPhase,
		AxisLabel:              axisLabel,
		PlanPath:               planPath,
		ExitCriteria:           exitCriteria,
		ProgressPath:           progressPath,
		IterDir:                iterDir,
		ContractPath:           contractPath,
		VerificationReportPath: verificationReportPath,
		Iteration:              iteration,
		RequiredVerification:   requiredVerification,
		RoadmapPath:            roadmapPath,
		PhaseType:              phaseType,
		FeedbackPath:           feedbackPath,
	})
}

// BuildImplementationReviewAxisPromptWithOpts renders one gate-aware
// implementation review axis prompt.
func BuildImplementationReviewAxisPromptWithOpts(opts ImplementationReviewAxisPromptOpts) string {
	required := make([]roles.VerificationItemView, 0, len(opts.RequiredVerification))
	for _, item := range opts.RequiredVerification {
		required = append(required, roles.VerificationItemView{
			Name:        item.Name,
			Requirement: item.Requirement,
		})
	}

	phaseType := opts.PhaseType
	if opts.Gate == implementationReviewGateFinal {
		phaseType = ""
	}

	return roles.BuildImplementationReviewAxisPrompt(roles.ImplementationReviewAxisUserInput{
		ReviewUserInput: roles.ReviewUserInput{
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
			PriorAxisReport:                      opts.PriorAxisReport,
			RepoDeltas:                           opts.RepoDeltas,
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

// reviewScopeHeading is the ATX heading for the review-scope section. When
// requireReviewScope is true the parser treats this as a mandatory section
// between `## Suggestions` and `## Verdict`; the first non-blank line is the
// scope token (targeted|full) and the remaining non-blank lines are the
// justification text.
const reviewScopeHeading = "## Review Scope"

// reviewScopeTokens is the closed set of valid scope tokens for the
// `## Review Scope` section.
var reviewScopeTokens = map[string]bool{
	"targeted": true,
	"full":     true,
}

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

// reviewRequiredSectionsWithScope is the required-sections list when the
// axis role must declare a review scope. `## Review Scope` is inserted
// between `## Suggestions` and `## Verdict`.
var reviewRequiredSectionsWithScope = []string{
	"## Findings",
	"## Suggestions",
	reviewScopeHeading,
	reviewVerdictHeading,
}

// reviewVerdictTokens is the closed set of strings the harness will route on.
// Anything else under `## Verdict` is a protocol violation.
var reviewVerdictTokens = map[string]ReviewStatus{
	agentStatusApproved:         ReviewApproved,
	agentStatusChangesRequested: ReviewChangesRequested,
}

// reviewStatusSkipped marks IterationMeta.ReviewStatus when the per-iteration
// review gate was intentionally not run (e.g. SkipIterationReview).
const reviewStatusSkipped = "skipped"

// ParsedReviewFeedback is the structured view of a review-feedback.md the
// harness uses for routing the bounded review/validation helper. Body holds
// the entire file contents verbatim so callers can re-emit it as the next
// implement iteration's feedback block. Markers carries the optional
// sticky-approval signal validators emit under `## Sticky Approval`.
//
// On parse failure each defect is recorded in ProtocolViolations and the
// typed Verdict / Markers fields are left zero. Callers MUST inspect
// ProtocolViolations before trusting Verdict.
type ParsedReviewFeedback struct {
	Verdict            ReviewStatus
	Body               string
	Findings           string
	Suggestions        string
	ReviewScope        string
	ReviewScopeJustification string
	Markers            ValidatorMarkers
	ProtocolViolations []string
}

// OK reports whether the feedback file satisfies the handoff contract.
// Callers route on Verdict only when OK() is true; otherwise they
// short-circuit to a structured CHANGES_REQUESTED.
func (p *ParsedReviewFeedback) OK() bool {
	return p != nil && p.Verdict != ReviewFailed && len(p.ProtocolViolations) == 0
}

// ParseReviewFeedback reads review-feedback.md at path and validates it
// against the handoff contract spelled out in the review skills. The
// function is conservative: every defect it can name deterministically
// becomes a separate ProtocolViolations entry — same shape as
// ParseProgressMd — so the synthesized feedback enumerates all of them in
// one pass. Callers MUST inspect ProtocolViolations before trusting Verdict.
//
// When requireReviewScope is true the parser additionally enforces a
// mandatory `## Review Scope` section between `## Suggestions` and
// `## Verdict`. This is set for implementation-review and final-review axis
// roles only; plan-review validators that share the review_feedback artifact
// type are unaffected.
//
// A missing file is not an error from the function's perspective: the
// returned ParsedReviewFeedback flags it as a protocol violation so the
// short-circuit feedback path can surface a useful message to the next
// iteration.
func ParseReviewFeedback(path string, requireReviewScope bool) (*ParsedReviewFeedback, error) {
	if path == "" {
		return nil, fmt.Errorf("ParseReviewFeedback: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ParsedReviewFeedback{
				Verdict: ReviewFailed,
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
	parsed := &ParsedReviewFeedback{Body: string(data), Verdict: ReviewFailed}
	body := parsed.Body

	sections := reviewRequiredSections
	if requireReviewScope {
		sections = reviewRequiredSectionsWithScope
	}

	headingPositions := findSectionHeadings(body, sections)
	missingOrUnordered := false
	lastPos := -1
	for _, h := range sections {
		pos, ok := headingPositions[h]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("review-feedback.md missing required section %q — the required `## ` sections must all be present, in order", h))
			missingOrUnordered = true
			continue
		}
		if pos < lastPos {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("review-feedback.md section %q appears out of order — required order is: %s", h, strings.Join(sections, ", ")))
			missingOrUnordered = true
		}
		lastPos = pos
	}

	parsed.Findings = strings.TrimSpace(extractMarkdownSection(body, "## Findings"))
	parsed.Suggestions = strings.TrimSpace(extractMarkdownSection(body, "## Suggestions"))

	if requireReviewScope {
		scopeBody := extractMarkdownSection(body, reviewScopeHeading)
		if scopeBody != "" {
			scopeToken := firstNonBlankLine(scopeBody)
			if !reviewScopeTokens[scopeToken] {
				parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
					"review-feedback.md `%s` first line must be exactly one of {targeted, full}; got %q", reviewScopeHeading, scopeToken))
			} else {
				parsed.ReviewScope = scopeToken
			}
			justification := strings.TrimSpace(strings.TrimPrefix(scopeBody, scopeToken))
			if justification == "" {
				parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
					"review-feedback.md `%s` justification is empty — after the scope token, explain what was reviewed or deliberately skipped", reviewScopeHeading))
			} else {
				parsed.ReviewScopeJustification = justification
			}
		} else if !missingOrUnordered {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
				"review-feedback.md `%s` section is empty — emit the scope token (targeted or full) on its own line followed by a non-empty justification", reviewScopeHeading))
		}
	}

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

// FormatReviewProtocolViolationFeedback renders a deterministic
// CHANGES_REQUESTED feedback document for a malformed review-feedback.md,
// shaped to satisfy the handoff contract on its own (so a downstream caller
// that re-parses the synthesized file gets a clean ParseReviewFeedback). The
// output mirrors FormatProtocolViolationFeedback for progress.md.
//
// When requireReviewScope is true the repair instructions and emitted
// feedback include the `## Review Scope` section.
func FormatReviewProtocolViolationFeedback(parsed *ParsedReviewFeedback, requireReviewScope bool) string {
	var b strings.Builder
	b.WriteString("# Implementation Review — Handoff Protocol Violation\n\n")
	b.WriteString("This iteration's `review-feedback.md` did not satisfy the handoff contract spelled out in the review skill. The deterministic handoff parser ran ahead of the LLM reviewer's downstream consumers; every defect listed below was decided mechanically (no model judgment), so the list is exhaustive and fixing every item will let the next review handoff parse cleanly.\n\n")
	b.WriteString("## Findings\n")
	if parsed == nil || len(parsed.ProtocolViolations) == 0 {
		b.WriteString("- (none)\n\n")
	} else {
		for _, v := range parsed.ProtocolViolations {
			fmt.Fprintf(&b, "- **Critical**: %s\n", v)
		}
		b.WriteString("\n")
	}
	if requireReviewScope {
		b.WriteString("Re-emit `review-feedback.md` with all four sections in order:\n")
		b.WriteString("1. `## Findings` (severity-prefixed bullets, or `- (none)`)\n")
		b.WriteString("2. `## Suggestions` (non-blocking improvements, or `- (none)`)\n")
		b.WriteString("3. `## Review Scope` (scope token `targeted` or `full` on its own line, followed by a non-empty justification)\n")
		b.WriteString("4. `## Verdict` (one of `APPROVED`, `CHANGES_REQUESTED` on its own line)\n\n")
	} else {
		b.WriteString("Re-emit `review-feedback.md` with all three sections in order:\n")
		b.WriteString("1. `## Findings` (severity-prefixed bullets, or `- (none)`)\n")
		b.WriteString("2. `## Suggestions` (non-blocking improvements, or `- (none)`)\n")
		b.WriteString("3. `## Verdict` (one of `APPROVED`, `CHANGES_REQUESTED` on its own line)\n\n")
	}
	b.WriteString("## Suggestions\n- (none)\n\n")
	if requireReviewScope {
		b.WriteString("## Review Scope\nfull\nNo axis round ran — the protocol violation was synthesized by the harness.\n\n")
	}
	b.WriteString("## Verdict\nCHANGES_REQUESTED\n")
	return b.String()
}

// FormatStructuredReviewFeedback wraps a free-form feedback body in the
// canonical `## Findings` / `## Suggestions` / `## Verdict` schema. Used by
// deterministic gates (Report Integrity, deferral ledger) that synthesize a
// CHANGES_REQUESTED verdict programmatically.
//
// findings, suggestions, and verdict are emitted verbatim under their
// section headings. When findings or suggestions are empty, the canonical
// `- (none)` placeholder is used so the file still parses cleanly.
func FormatStructuredReviewFeedback(title, findings, suggestions string, verdict ReviewStatus) string {
	return FormatStructuredReviewFeedbackWithScope(title, findings, suggestions, verdict, "", "")
}

// FormatStructuredReviewFeedbackWithScope is FormatStructuredReviewFeedback
// plus a `## Review Scope` section emitted between `## Suggestions` and
// `## Verdict`. When scope is empty the section is omitted (identical to
// FormatStructuredReviewFeedback). Deterministic gates that synthesize
// feedback for implementation-review or final-review axis roles should pass
// scope="full" with a justification explaining no axis round ran.
func FormatStructuredReviewFeedbackWithScope(title, findings, suggestions string, verdict ReviewStatus, scope, scopeJustification string) string {
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
	if scope != "" {
		b.WriteString(reviewScopeHeading)
		b.WriteByte('\n')
		b.WriteString(scope)
		b.WriteByte('\n')
		if strings.TrimSpace(scopeJustification) != "" {
			fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(scopeJustification, "\n"))
		} else {
			b.WriteByte('\n')
		}
	}
	b.WriteString(reviewVerdictHeading)
	b.WriteByte('\n')
	switch verdict {
	case ReviewApproved:
		b.WriteString("APPROVED\n")
	case ReviewChangesRequested:
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
func parseStickyApprovalSection(section string) ValidatorMarkers {
	var m ValidatorMarkers
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

// ValidatorMarkers carries the sticky-approval signal a per-axis validator
// emits in the `## Sticky Approval` section of its review-feedback file.
// AxisApproved is the axis name (e.g. "architecture", "scope"); FrozenSections
// is the byte-equal set of artifact headings the next revise attempt must
// preserve verbatim — see the "Sticky Approval Respect" procedure in
// skills/revise-roadmap/SKILL.md.
type ValidatorMarkers struct {
	AxisApproved   string
	FrozenSections []string
}
