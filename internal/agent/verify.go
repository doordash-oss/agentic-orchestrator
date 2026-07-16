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
	"strings"
)

// VerificationStep represents a single automated verification command
// extracted from a plan's "Automated Verification" section.
type VerificationStep struct {
	Description  string // Human-readable description (e.g., "Unit tests pass")
	Command      string // Executable command (e.g., "go test ./...")
	Repo         string // Explicit [repo: <name>] scope for multi-repo phase commands.
	Capabilities []VerificationCapability
}

// VerificationCapability is explicit plan metadata for an environmental
// prerequisite. It is deliberately declared rather than inferred from test
// output so arbitrary failures cannot be mislabeled as authorization blocks.
type VerificationCapability struct {
	Name  string
	Probe string
}

// ManualVerificationStep represents one manual check extracted from a plan's
// "Manual Verification" section.
type ManualVerificationStep struct {
	Description string
}

// EvidenceRequirement represents one visual or behavioral artifact requirement
// extracted from a phase plan.
type EvidenceRequirement struct {
	Description string
}

// ParsePlanVerification extracts automated verification commands from a plan's
// markdown text. It looks for "#### Automated Verification:" sections and
// parses checklist items that contain backtick-delimited commands.
//
// Supported patterns:
//   - [ ] Description: `command`
//   - [x] Description: `command`
//   - [ ] `command`
//   - [ ] Description (`command`)
func ParsePlanVerification(planText string) []VerificationStep {
	return parseVerificationSection(planText, isAutomatedVerificationHeader, parseChecklistItem)
}

// ParseCrossRepoVerification extracts cross-repo verification commands from a
// plan's "Cross-Repo Verification" section. Accepts both checklist items
// ("- [ ] Description: `command`") and plain bullets ("- Description: `command`")
// so planner-authored ## Cross-Repo Verification blocks match production plans.
func ParseCrossRepoVerification(planText string) []VerificationStep {
	return parseVerificationSection(planText, isCrossRepoVerificationHeader, parseCrossRepoItem)
}

func parseVerificationSection(
	planText string,
	isSectionHeader func(string) bool,
	parseItem func(string) (VerificationStep, bool),
) []VerificationStep {
	var steps []VerificationStep
	forEachVerificationSectionLine(planText, isSectionHeader, func(trimmed string) {
		if step, ok := parseItem(trimmed); ok {
			steps = append(steps, step)
		}
	})
	return steps
}

// forEachVerificationSectionLine invokes fn for every non-fence line inside
// sections whose heading matches isSectionHeader. Fence and section-end rules
// are identical to extractMarkdownSection and countEvidenceChecklistItems
// (``` and ~~~ fences skipped; only a heading at the opener's level or higher
// ends the section) — the plan validator cross-checks their counts, so any
// divergence would reject valid plans.
func forEachVerificationSectionLine(planText string, isSectionHeader func(string) bool, fn func(trimmed string)) {
	var fence fenceState
	inSection := false
	sectionLevel := 0
	for _, line := range strings.Split(planText, "\n") {
		if fence.update(line) || fence.inside() {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if isSectionHeader(trimmed) {
			inSection = true
			sectionLevel = headingLevel(trimmed)
			continue
		}
		if inSection {
			if l := headingLevel(trimmed); l > 0 && l <= sectionLevel {
				inSection = false
			}
		}
		if inSection {
			fn(trimmed)
		}
	}
}

// ParsePlanManualVerification extracts manual verification checklist bullets
// from "Manual Verification" sections. A checklist item beginning with
// "None required:" is treated as an explicit absence marker and is not
// returned as a required manual check.
func ParsePlanManualVerification(planText string) []ManualVerificationStep {
	var steps []ManualVerificationStep
	forEachVerificationSectionLine(planText, isManualVerificationHeader, func(trimmed string) {
		if step, ok := parseManualChecklistItem(trimmed); ok {
			steps = append(steps, step)
		}
	})
	return steps
}

// ParsePlanVisualEvidence extracts visual evidence checklist bullets from
// "Visual Evidence" sections. A checklist item beginning with
// "None required:" is treated as an explicit absence marker and is not
// returned as a required evidence artifact.
func ParsePlanVisualEvidence(planText string) []EvidenceRequirement {
	return parsePlanEvidenceRequirements(planText, isVisualEvidenceHeader)
}

// ParsePlanBehavioralEvidence extracts behavioral evidence checklist bullets
// from "Behavioral Evidence" sections. A checklist item beginning with
// "None required:" is treated as an explicit absence marker and is not
// returned as a required evidence artifact.
func ParsePlanBehavioralEvidence(planText string) []EvidenceRequirement {
	return parsePlanEvidenceRequirements(planText, isBehavioralEvidenceHeader)
}

func parsePlanEvidenceRequirements(planText string, isSectionHeader func(string) bool) []EvidenceRequirement {
	var requirements []EvidenceRequirement
	forEachVerificationSectionLine(planText, isSectionHeader, func(trimmed string) {
		if requirement, ok := parseEvidenceChecklistItem(trimmed); ok {
			requirements = append(requirements, requirement)
		}
	})
	return requirements
}

// isAutomatedVerificationHeader returns true for headers like:
//
//	"#### Automated Verification:", "### Automated Verification:"
var automatedVerificationRe = regexp.MustCompile(`^#{2,5}\s+Automated Verification`)

func isAutomatedVerificationHeader(line string) bool {
	return automatedVerificationRe.MatchString(line)
}

// Cross-Repo Verification headers appear as ## / ### / #### with an optional
// trailing colon — matching both the struct docs ("#### Cross-Repo Verification:")
// and the multi-repo integration fixture ("## Cross-Repo Verification").
var crossRepoVerificationRe = regexp.MustCompile(`^#{2,5}\s+Cross-Repo Verification`)

func isCrossRepoVerificationHeader(line string) bool {
	return crossRepoVerificationRe.MatchString(line)
}

var manualVerificationRe = regexp.MustCompile(`^#{2,5}\s+Manual Verification`)

func isManualVerificationHeader(line string) bool {
	return manualVerificationRe.MatchString(line)
}

var visualEvidenceRe = regexp.MustCompile(`^#{2,5}\s+Visual Evidence`)

func isVisualEvidenceHeader(line string) bool {
	return visualEvidenceRe.MatchString(line)
}

var behavioralEvidenceRe = regexp.MustCompile(`^#{2,5}\s+Behavioral Evidence`)

func isBehavioralEvidenceHeader(line string) bool {
	return behavioralEvidenceRe.MatchString(line)
}

// parseChecklistItem implements a two-layer strategy:
//
//  1. Prefer backtick spans that look like executable commands — i.e. contain
//     whitespace, shell operators, or relative-path patterns. Bare identifier
//     spans (file.go, internal/app/, TestFoo, go.sum) are filtered out because
//     they're almost always inline code references in the description, not
//     the verification command. Among the command-shaped candidates, take the
//     LAST one (matches the canonical "Description: `command`" convention).
//  2. If no span looks command-shaped — e.g. the plan author used a single
//     backtick for a single-word command like `make` — fall back to the LAST
//     raw backtick span. This preserves the legacy behavior for simple cases.
//
// The canonical plan-phase format (see skills/plan-phase/SKILL.md) requires
// the executable command to be the final backtick on the line, so both
// layers land on the same span for a well-formatted plan. The filter step
// makes the parser robust to plans that include inline code references
// (paths, symbols) in the description alongside the real command.
//
// Span discovery uses a quote-aware scanner (see findBacktickSpans) rather
// than a regex so that backticks appearing inside shell quotes (e.g. the
// triple-backtick literal in `grep -cE '^` + "```" + `' README.md`) are
// treated as part of the command, not as span delimiters.
func parseChecklistItem(line string) (VerificationStep, bool) {
	// Must be a checklist item: "- [ ]" or "- [x]"
	if !strings.HasPrefix(line, "- [") {
		return VerificationStep{}, false
	}
	return parseBacktickVerificationItem(line)
}

// parseCrossRepoItem accepts checklist items and plain markdown bullets so
// Cross-Repo Verification sections authored either way yield VerificationSteps.
// Plain bullets require a command-shaped backtick span (no last-span fallback)
// so prose notes like `- Keep `service-a` and `service-b` schemas in sync`
// do not become fabricated contract rows.
func parseCrossRepoItem(line string) (VerificationStep, bool) {
	if !strings.HasPrefix(line, "- ") {
		return VerificationStep{}, false
	}
	if strings.HasPrefix(line, "- [") {
		return parseBacktickVerificationItem(line)
	}
	return parsePlainCrossRepoBullet(line)
}

func parseBacktickVerificationItem(line string) (VerificationStep, bool) {
	matches := findBacktickSpans(line)
	if len(matches) == 0 {
		return VerificationStep{}, false
	}

	chosen := pickCommandSpan(line, matches)
	if chosen == nil {
		return VerificationStep{}, false
	}
	return verificationStepFromSpan(line, chosen)
}

// parsePlainCrossRepoBullet parses "- description: `cmd`" lines. Unlike
// checklist parsing, it rejects lines whose backtick spans are only bare
// identifiers (no looksLikeShellCommand match).
func parsePlainCrossRepoBullet(line string) (VerificationStep, bool) {
	matches := findBacktickSpans(line)
	if len(matches) == 0 {
		return VerificationStep{}, false
	}
	for _, m := range matches {
		if looksLikeShellCommand(line[m[2]:m[3]]) {
			return verificationStepFromSpan(line, m)
		}
	}
	return VerificationStep{}, false
}

func verificationStepFromSpan(line string, chosen []int) (VerificationStep, bool) {
	command := strings.TrimSpace(line[chosen[2]:chosen[3]])
	if command == "" {
		return VerificationStep{}, false
	}

	commandWithBackticks := line[chosen[0]:chosen[1]]
	description := extractDescription(line, commandWithBackticks)
	description = strings.TrimSpace(verificationCapabilityRE.ReplaceAllString(description, ""))
	description = strings.TrimSpace(verificationRepoRE.ReplaceAllString(description, ""))
	description = strings.TrimSpace(strings.TrimSuffix(description, ":"))

	return VerificationStep{
		Description:  description,
		Command:      command,
		Repo:         parseVerificationRepo(line),
		Capabilities: parseVerificationCapabilities(line),
	}, true
}

var verificationCapabilityRE = regexp.MustCompile(`\[agentico capability:\s*([^;\]]+)\s*;\s*probe:\s*([^\]]+)\]`)
var verificationRepoRE = regexp.MustCompile(`(?i)\[repo:\s*([^\]]+)\]`)

func parseVerificationRepo(line string) string {
	match := verificationRepoRE.FindStringSubmatch(line)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(match[1], "`"))
}

func parseVerificationCapabilities(line string) []VerificationCapability {
	matches := verificationCapabilityRE.FindAllStringSubmatch(line, -1)
	out := make([]VerificationCapability, 0, len(matches))
	for _, match := range matches {
		name := strings.TrimSpace(match[1])
		probe := strings.TrimSpace(match[2])
		if name != "" && probe != "" {
			out = append(out, VerificationCapability{Name: name, Probe: probe})
		}
	}
	return out
}

func parseManualChecklistItem(line string) (ManualVerificationStep, bool) {
	description, ok := parseChecklistDescription(line)
	if !ok {
		return ManualVerificationStep{}, false
	}
	if isNoneRequiredDescription(description) {
		return ManualVerificationStep{}, false
	}
	return ManualVerificationStep{Description: description}, true
}

func parseEvidenceChecklistItem(line string) (EvidenceRequirement, bool) {
	description, ok := parseChecklistDescription(line)
	if !ok {
		return EvidenceRequirement{}, false
	}
	if isNoneRequiredDescription(description) {
		return EvidenceRequirement{}, false
	}
	return EvidenceRequirement{Description: description}, true
}

func parseChecklistDescription(line string) (string, bool) {
	if !strings.HasPrefix(line, "- [") {
		return "", false
	}
	closeIdx := strings.Index(line, "]")
	if closeIdx < 0 || closeIdx+1 >= len(line) {
		return "", false
	}
	description := strings.TrimSpace(line[closeIdx+1:])
	description = strings.TrimPrefix(description, "-")
	description = strings.TrimSpace(description)
	if description == "" {
		return "", false
	}
	return description, true
}

func isNoneRequiredDescription(description string) bool {
	marker, _ := noneRequiredMarker(description)
	return marker
}

func noneRequiredMarker(description string) (marker bool, valid bool) {
	normalized := strings.TrimSpace(description)
	normalized = strings.Trim(normalized, "`*_ ")
	const prefix = "none required:"
	if !strings.HasPrefix(strings.ToLower(normalized), prefix) {
		return false, false
	}
	return true, strings.TrimSpace(normalized[len(prefix):]) != ""
}

// findBacktickSpans scans a line for backtick-delimited spans, treating any
// backtick that appears inside matching shell quotes (`'…'` or `"…"`) as
// literal content rather than a span delimiter. This lets a verification
// command that itself contains literal backticks — e.g. an awk/grep
// invocation that searches for triple-backtick fences — survive parsing
// as a single span.
//
// The output mirrors regexp.FindAllStringSubmatchIndex's shape so callers
// (pickCommandSpan, parseChecklistItem) can use it interchangeably: each
// entry is [outer_start, outer_end, inner_start, inner_end] with positions
// in bytes. Empty spans (“ “ “) are skipped, matching the legacy regex.
//
// Backslash escaping inside double-quoted segments is intentionally not
// handled — it's vanishingly rare in plan bullets and the simple state
// machine stays correct for the common cases.
func findBacktickSpans(line string) [][]int {
	var spans [][]int
	inSpan := false
	var openIdx int
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if !inSpan {
			if c == '`' {
				inSpan = true
				openIdx = i
				quote = 0
			}
			continue
		}
		if quote == 0 {
			if c == '\'' || c == '"' {
				quote = c
				continue
			}
			if c == '`' {
				if i > openIdx+1 {
					spans = append(spans, []int{openIdx, i + 1, openIdx + 1, i})
				}
				inSpan = false
			}
			continue
		}
		if c == quote {
			quote = 0
		}
	}
	return spans
}

// pickCommandSpan selects which backtick span on a line is the verification
// command. See parseChecklistItem for rationale.
func pickCommandSpan(line string, matches [][]int) []int {
	// Prefer the FIRST shell-command-shaped span. Plans that deviate from
	// the canonical "description: `command`" format (e.g. "description:
	// `command` passes, including `extra context`, on `files`") still have
	// the real command as the first command-shaped span — later spans are
	// elaborations of its behavior, not alternatives.
	for _, m := range matches {
		if looksLikeShellCommand(line[m[2]:m[3]]) {
			return m
		}
	}
	// All spans looked like bare identifiers; fall back to last span so a
	// plan that uses a single-word command (`make`, `task`) still parses.
	return matches[len(matches)-1]
}

// looksLikeShellCommand returns true when s contains at least one signal of
// being an executable invocation rather than an inline code reference.
// Deliberately conservative — false negatives here mean we fall through to
// the legacy last-span behavior, so they're safe.
func looksLikeShellCommand(s string) bool {
	if strings.ContainsAny(s, " \t|&><$=();") {
		return true
	}
	// Relative-path invocations like `./bin/agentic` or `./scripts/foo.sh`.
	if strings.Contains(s, "./") {
		return true
	}
	// Script-path invocations without ./, e.g. `scripts/e2e.sh`.
	lower := strings.ToLower(s)
	if strings.HasSuffix(lower, ".sh") ||
		strings.HasSuffix(lower, ".bash") ||
		strings.HasSuffix(lower, ".py") ||
		strings.HasSuffix(lower, ".rb") {
		return true
	}
	return false
}

// extractDescription gets the human-readable part of a checklist item,
// stripping the checkbox prefix and backtick command.
//
// When the bullet is in "inverted format" — the command appears at the very
// start, immediately after the checkbox, with prose trailing — the leftover
// after stripping is almost always a sentence fragment ("succeeds.", "passes.",
// "shows exactly `X`."). In that case return "" so callers can fall back to
// using the command itself as the human-readable name (the testing-contract
// extractor in testing_contract.go does this via `if name == "" { name = command }`).
// The canonical format puts description before the command, so this only
// changes behavior for the inverted shape.
func extractDescription(line, commandWithBackticks string) string {
	// Remove checkbox prefix: "- [ ] " or "- [x] "
	desc := line
	if idx := strings.Index(desc, "] "); idx >= 0 {
		desc = desc[idx+2:]
	} else if strings.HasPrefix(desc, "- ") {
		// Plain markdown bullet (Cross-Repo Verification plans).
		desc = strings.TrimSpace(desc[2:])
	}
	// Inverted format: command is the first non-whitespace content.
	if strings.HasPrefix(strings.TrimLeft(desc, " \t"), commandWithBackticks) {
		return ""
	}
	// Remove the backtick command portion
	desc = strings.Replace(desc, commandWithBackticks, "", 1)
	// Clean up trailing/leading colons, parens, spaces
	desc = strings.TrimRight(desc, ": ()")
	desc = strings.TrimSpace(desc)
	return desc
}
