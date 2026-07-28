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
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"gopkg.in/yaml.v3"
)

// Fingerprint computes SHA256 of a file's contents.
func Fingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading file for fingerprint: %w", err)
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// ProgressFingerprint computes a stable fingerprint over progress.md. The
// fingerprint covers just the `## Iteration Handoff` section — the agent's claimed
// completed/remaining/where-stopped narrative — which is what should
// converge across iterations when no real progress is being made.
//
// Falls back to whole-file hashing when the file cannot be parsed (e.g.,
// pre-handoff-contract progress.md that lacks the structured section); a
// full-file fingerprint is still better than no detection.
func ProgressFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading progress file: %w", err)
	}
	if section := extractMarkdownSection(string(data), "## Iteration Handoff"); section != "" {
		h := sha256.Sum256([]byte(section))
		return fmt.Sprintf("%x", h), nil
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// ProgressTracker tracks consecutive no-progress iterations.
type ProgressTracker struct {
	lastFingerprint string
	hasChecked      bool
	noProgressCount int
	hasOutcome      bool
	bestBlockers    int
	// Verified stall-evidence state: the last worktree fingerprint seen by
	// ObserveVerifiedOutcomeWithWorktree.
	lastVerifiedWorktree string
	// RETRY stall-evidence state: the last handoff-narrative and worktree
	// fingerprints seen by ObserveRetryOutcome.
	hasRetryObservation bool
	lastRetryNarrative  string
	lastRetryWorktree   string
}

func NewProgressTracker() *ProgressTracker {
	return &ProgressTracker{}
}

// Check compares the current progress file fingerprint with the last one.
// Returns true if progress was made (fingerprint changed). Uses the
// handoff-narrative-scoped fingerprint so iteration-specific paths in
// progress.md don't mask a stalled agent.
func (pt *ProgressTracker) Check(progressPath string) (bool, error) {
	fp, err := ProgressFingerprint(progressPath)
	if err != nil {
		return false, err
	}

	if !pt.hasChecked {
		pt.lastFingerprint = fp
		pt.hasChecked = true
		return true, nil
	}

	if fp == pt.lastFingerprint {
		pt.noProgressCount++
		return false, nil
	}

	pt.lastFingerprint = fp
	pt.noProgressCount = 0
	return true, nil
}

// NoProgressCount returns the number of consecutive no-progress iterations.
func (pt *ProgressTracker) NoProgressCount() int {
	return pt.noProgressCount
}

// ObserveVerifiedOutcome records a deterministic outcome when no repository
// fingerprint is available. Progress is credited when the verified blocker
// count reaches a new low.
func (pt *ProgressTracker) ObserveVerifiedOutcome(blockers int) bool {
	return pt.ObserveVerifiedOutcomeWithWorktree(blockers, "")
}

// ObserveVerifiedOutcomeWithWorktree records a deterministic verifier/reviewer
// outcome together with the repository state that produced it. Fewer blockers
// or a changed worktree is progress; an empty fingerprint cannot disarm the
// safety rail.
func (pt *ProgressTracker) ObserveVerifiedOutcomeWithWorktree(blockers int, worktreeFP string) bool {
	if blockers < 0 {
		blockers = 0
	}
	worktreeChanged := worktreeFP != "" &&
		pt.lastVerifiedWorktree != "" &&
		worktreeFP != pt.lastVerifiedWorktree
	if worktreeFP != "" {
		pt.lastVerifiedWorktree = worktreeFP
	}
	if !pt.hasOutcome {
		pt.hasOutcome = true
		pt.bestBlockers = blockers
		return true
	}
	if blockers < pt.bestBlockers || worktreeChanged {
		if blockers < pt.bestBlockers {
			pt.bestBlockers = blockers
		}
		pt.noProgressCount = 0
		return true
	}
	pt.noProgressCount++
	return false
}

// ObserveUnverifiedOutcome records an iteration that did not produce a
// verifier/reviewer outcome, such as a protocol violation.
func (pt *ProgressTracker) ObserveUnverifiedOutcome() bool {
	pt.noProgressCount++
	return false
}

// ObserveRetryOutcome records a self-declared partial (RETRY) iteration.
// A RETRY produces no review verdict, but absence of a verdict is not
// evidence of a stall: a short-turn model legitimately splits one task
// across many partial iterations. The iteration counts as no-progress only
// when neither the handoff narrative nor the worktree state changed since
// the previous RETRY observation. An empty fingerprint (unreadable
// progress.md, git failure) is treated as unchanged so a broken signal
// cannot disarm the rail. A progressing RETRY does not reset the counter
// accumulated by non-improving verified outcomes — it only declines to add
// to it — so review churn without fewer blockers still trips the rail.
func (pt *ProgressTracker) ObserveRetryOutcome(narrativeFP, worktreeFP string) bool {
	first := !pt.hasRetryObservation
	narrativeChanged := narrativeFP != "" && narrativeFP != pt.lastRetryNarrative
	worktreeChanged := worktreeFP != "" && worktreeFP != pt.lastRetryWorktree
	pt.hasRetryObservation = true
	pt.lastRetryNarrative = narrativeFP
	pt.lastRetryWorktree = worktreeFP
	if first || narrativeChanged || worktreeChanged {
		return true
	}
	pt.noProgressCount++
	return false
}

// WorktreeStateFingerprint hashes each repo worktree's HEAD, porcelain
// status, tracked diff, and untracked-file stats (path, size, mtime — new
// files stay untracked for the whole phase, so their edits never appear in
// `git diff HEAD`). Returns "" when no signal could be gathered so callers
// treat it as unchanged. A nil runner falls back to direct execution.
func WorktreeStateFingerprint(ctx context.Context, runner ports.CommandRunner, paths []string) string {
	h := sha256.New()
	gotSignal := false
	for _, p := range paths {
		for _, args := range [][]string{
			{"rev-parse", "HEAD"},
			{"status", "--porcelain", "--untracked-files=all"},
			{"diff", "HEAD"},
		} {
			out, err := gitOutput(ctx, runner, p, args...)
			if err != nil {
				continue
			}
			gotSignal = true
			h.Write(out)
		}
		out, err := gitOutput(ctx, runner, p, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			continue
		}
		gotSignal = true
		for _, rel := range strings.Split(string(out), "\x00") {
			if rel == "" {
				continue
			}
			if fi, err := os.Stat(filepath.Join(p, rel)); err == nil {
				fmt.Fprintf(h, "%s|%d|%d\n", rel, fi.Size(), fi.ModTime().UnixNano())
			}
		}
	}
	if !gotSignal {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

var blockingReviewFindingRE = regexp.MustCompile(`(?im)^\s*[-*]\s+\*\*\[?(critical|high)(?:\]|\b)`)

func CountBlockingReviewFindings(feedback string) int {
	return len(blockingReviewFindingRE.FindAllString(feedback, -1))
}

func CountOpenVerificationBlockers(report *VerificationReport) int {
	if report == nil {
		return 0
	}
	count := 0
	for _, result := range report.Results {
		switch NormalizeStatus(result.Status) {
		case VerificationStatusPassed, VerificationStatusWaived:
			continue
		default:
			count++
		}
	}
	return count
}

func CountOpenVerificationOutcomeBlockers(out *VerificationExecutionOutcome) int {
	if out == nil {
		return 0
	}
	return CountOpenVerificationBlockers(out.Report)
}

// IterationState is the harness-recognised terminal state the agent declares
// in progress.md's `## Iteration State` section. StateInvalid is reserved for
// cases where parsing failed or the body did not match one of the two
// canonical tokens — callers must treat StateInvalid as a protocol violation,
// not as one of the valid routes.
type IterationState int

const (
	StateInvalid IterationState = iota
	StateSuccess
	StateRetry
)

func (s IterationState) String() string {
	switch s {
	case StateSuccess:
		return agentStatusSuccess
	case StateRetry:
		return "RETRY"
	default:
		return "INVALID"
	}
}

// ParsedProgress is the structured view of a progress.md the implement
// harness uses for routing. Fields populated only when the corresponding
// section parsed cleanly; on failure the defect is appended to
// ProtocolViolations and the field is left zero. Callers MUST check
// ProtocolViolations before trusting any non-State field — a malformed
// Deferrals YAML, for example, leaves Deferrals nil but the whole struct
// otherwise looks valid.
type ParsedProgress struct {
	State              IterationState
	Deferrals          []feature.IncomingDeferral
	ClosedDeferrals    []string
	HandoffSections    map[string]string // sub-section heading -> body (Iteration Handoff children)
	ProtocolViolations []string
}

// OK reports whether the parsed progress.md satisfies the contract: a valid
// terminal state, all required sections present, deferrals YAML parsed, and
// no other protocol violations. Callers route on State only when OK() is
// true; otherwise they short-circuit to a structured CHANGES_REQUESTED.
func (p *ParsedProgress) OK() bool {
	return p != nil && p.State != StateInvalid && len(p.ProtocolViolations) == 0
}

// progressRequiredSections lists the three `## ` sections the agent MUST emit
// in this exact order. The parser enforces presence and order; missing or
// out-of-order headings are protocol violations.
var progressRequiredSections = []string{
	"## Iteration Handoff",
	"## Deferrals",
	"## Iteration State",
}

// progressHandoffSubsections lists the four `### ` sub-sections under
// `## Iteration Handoff`. They are not strictly required (free-form prose
// is allowed), but the parser captures their bodies so the next iteration's
// agent can pick up cleanly. Missing sub-sections are surfaced as warnings
// but do NOT count as protocol violations — the agent gets latitude here
// because the section is for the next-iteration human-readable handoff,
// not for harness-driven routing.
var progressHandoffSubsections = []string{
	"### Completed this iteration",
	"### Remaining from the plan",
	"### Where I stopped",
	"### Gotchas / blockers / in-flight decisions",
}

// validIterationStateTokens is the closed set of strings the harness will
// route on. Anything else under `## Iteration State` is a protocol
// violation — the canonical token must appear on its own line as the first
// non-blank content of the section.
var validIterationStateTokens = map[string]IterationState{
	agentStatusSuccess: StateSuccess,
	"RETRY":            StateRetry,
}

// progressYAMLBlockRE matches a fenced YAML code block; capture group 1 is
// the body. Used to extract the structured Deferrals payload. Tolerant of
// `~~~` fences in addition to backticks since some agents emit the
// alternate fence under tilde-fence Markdown rules.
var progressYAMLBlockRE = regexp.MustCompile("(?s)(?:```|~~~)\\s*ya?ml\\s*\\n(.*?)\\n(?:```|~~~)")

// deferralsYAMLDoc is the on-the-wire shape inside the Deferrals fenced YAML
// block. We accept the empty form (`deferrals: []`, `closed_deferrals: []`)
// but require both keys to be present so the agent's intent is unambiguous —
// implicit absence is rejected.
type deferralsYAMLDoc struct {
	Deferrals       *[]feature.IncomingDeferral `yaml:"deferrals"`
	ClosedDeferrals *[]string                   `yaml:"closed_deferrals"`
}

// ParseProgressMd parses progress.md at path under the iteration-handoff
// contract spelled out in skills/implement/SKILL.md.
// The function is conservative: every defect it can name deterministically
// becomes a separate ProtocolViolations entry so the synthesized feedback
// enumerates all of them in one pass — same shape as the grounding gate.
// Callers MUST inspect ProtocolViolations before trusting State.
func ParseProgressMd(path string) (*ParsedProgress, error) {
	if path == "" {
		return nil, fmt.Errorf("ParseProgressMd: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ParsedProgress{
				ProtocolViolations: []string{
					fmt.Sprintf("progress.md not found at %s — the implement iteration must emit it before declaring a completion outcome", path),
				},
			}, nil
		}
		return nil, fmt.Errorf("reading progress.md at %s: %w", path, err)
	}

	parsed := &ParsedProgress{HandoffSections: map[string]string{}}
	body := string(data)

	// Required-section presence + order. Missing or reordered headings are
	// the most common way agents drift from the contract, so name them
	// explicitly in the feedback rather than fail at the first downstream
	// parse error.
	headingPositions := findSectionHeadings(body, progressRequiredSections)
	missingOrUnordered := false
	lastPos := -1
	for _, h := range progressRequiredSections {
		pos, ok := headingPositions[h]
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("progress.md missing required section %q — the three `## ` sections must all be present, in order", h))
			missingOrUnordered = true
			continue
		}
		if pos < lastPos {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				fmt.Sprintf("progress.md section %q appears out of order — required order is: %s", h, strings.Join(progressRequiredSections, ", ")))
			missingOrUnordered = true
		}
		lastPos = pos
	}

	// Iteration Handoff: capture sub-section bodies for the next iteration.
	if handoffBody := extractMarkdownSection(body, "## Iteration Handoff"); handoffBody != "" {
		for _, sub := range progressHandoffSubsections {
			if v := extractMarkdownSection(handoffBody, sub); v != "" {
				parsed.HandoffSections[strings.TrimPrefix(sub, "### ")] = strings.TrimSpace(v)
			}
		}
	}

	// Deferrals: required fenced YAML block with both keys.
	if deferralsBody := extractMarkdownSection(body, "## Deferrals"); deferralsBody != "" {
		yamlBody, ok := extractFencedYAML(deferralsBody)
		if !ok {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations,
				"progress.md `## Deferrals` body must contain a single fenced YAML code block (```yaml ... ```)")
		} else {
			doc, err := parseDeferralsYAML(yamlBody)
			if err != nil {
				parsed.ProtocolViolations = append(parsed.ProtocolViolations,
					fmt.Sprintf("progress.md Deferrals YAML failed to parse: %v", err))
			} else {
				if doc.Deferrals == nil {
					parsed.ProtocolViolations = append(parsed.ProtocolViolations,
						"progress.md Deferrals YAML must include a `deferrals:` key (use `deferrals: []` when empty)")
				} else {
					parsed.Deferrals = *doc.Deferrals
				}
				if doc.ClosedDeferrals == nil {
					parsed.ProtocolViolations = append(parsed.ProtocolViolations,
						"progress.md Deferrals YAML must include a `closed_deferrals:` key (use `closed_deferrals: []` when empty)")
				} else {
					parsed.ClosedDeferrals = *doc.ClosedDeferrals
				}
			}
		}
	}

	// Iteration State: exactly one of the two tokens on its own line.
	if stateBody := extractMarkdownSection(body, "## Iteration State"); stateBody != "" {
		token, note := splitStateTokenAndNote(stateBody)
		if state, ok := validIterationStateTokens[token]; ok {
			parsed.State = state
			if strings.TrimSpace(note) != "" {
				parsed.ProtocolViolations = append(parsed.ProtocolViolations,
					"progress.md `## Iteration State` must contain only the state token on its own line")
			}
		} else {
			parsed.ProtocolViolations = append(parsed.ProtocolViolations, fmt.Sprintf(
				"progress.md `## Iteration State` body must be exactly one of {SUCCESS, RETRY} on its own line; got %q", token))
		}
	} else if !missingOrUnordered {
		// Section present in heading scan but body empty (e.g., heading
		// followed immediately by EOF). Treat as a protocol violation.
		// When the heading was missing we already emitted a finding.
		parsed.ProtocolViolations = append(parsed.ProtocolViolations,
			"progress.md `## Iteration State` section is empty — emit one of {SUCCESS, RETRY} on its own line")
	}

	if _, present := findSectionHeadings(body, []string{"## Questions for User"})["## Questions for User"]; present {
		parsed.ProtocolViolations = append(parsed.ProtocolViolations,
			"progress.md must not contain `## Questions for User`; use the formal AskUserQuestion control before declaring an outcome")
	}

	return parsed, nil
}

// FormatProtocolViolationFeedback renders a deterministic CHANGES_REQUESTED
// feedback document for a malformed progress.md, conforming to the file-based
// review handoff schema (## Findings / ## Suggestions / ## Verdict) so
// ParseReviewFeedback can re-parse it cleanly when the implement loop short-
// circuits ahead of the LLM reviewer. Callers persist this to
// review-feedback.md.
func FormatProtocolViolationFeedback(parsed *ParsedProgress) string {
	var b strings.Builder
	b.WriteString("This iteration's `progress.md` did not satisfy the harness contract spelled out in `skills/implement/SKILL.md`. The deterministic handoff parser ran ahead of the LLM reviewer; every defect listed below was decided mechanically (no model judgment), so the list is exhaustive and fixing every item will let the next iteration's handoff parse cleanly.\n\n")
	if parsed != nil {
		for _, v := range parsed.ProtocolViolations {
			fmt.Fprintf(&b, "- **Critical**: %s\n", v)
		}
	}
	if parsed == nil || len(parsed.ProtocolViolations) == 0 {
		b.WriteString("- (none)\n")
	}
	b.WriteString("\nRe-emit `progress.md` with these sections in order:\n")
	b.WriteString("1. `## Iteration Handoff` (with the four `### ` sub-sections)\n")
	b.WriteString("2. `## Deferrals` (fenced YAML block with `deferrals:` and `closed_deferrals:` keys)\n")
	b.WriteString("3. `## Iteration State` (one of `SUCCESS`, `RETRY` on its own line)\n")
	return FormatStructuredReviewFeedback(
		"Implementation Review — Iteration Handoff Protocol Violation",
		strings.TrimRight(b.String(), "\n"),
		"",
		ReviewChangesRequested,
	)
}

// findSectionHeadings returns the byte offset of each heading in the
// supplied list (only entries actually present are included). Order is
// determined by document position, not by the input list — the caller is
// responsible for cross-checking expected ordering. Headings are matched on
// trimmed lines so trailing whitespace doesn't break detection.
func findSectionHeadings(body string, headings []string) map[string]int {
	want := map[string]struct{}{}
	for _, h := range headings {
		want[h] = struct{}{}
	}
	out := map[string]int{}
	pos := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if _, ok := want[trimmed]; ok {
			if _, dup := out[trimmed]; !dup {
				out[trimmed] = pos
			}
		}
		pos += len(line) + 1
	}
	return out
}

// extractMarkdownSection returns the body of the section whose heading line
// exactly matches heading. Body starts at the line after the heading and
// ends before the next heading at the same or higher level (e.g. for a
// `## ` heading, the next `## ` or `# ` ends the section; for a `### `
// heading, the next `### ` / `## ` / `# ` ends it). Returns "" when the
// section is absent.
//
// Fenced code blocks (``` and ~~~) suspend heading detection: a `# comment`
// line inside a YAML or Markdown fence is content, not a heading. Without
// this, the structured `## Deferrals` YAML body — which legitimately
// contains `# comment` lines explaining the deferrals contract — would
// truncate the section right after the opening fence.
func extractMarkdownSection(body, heading string) string {
	level := headingLevel(heading)
	if level == 0 {
		return ""
	}
	lines := strings.Split(body, "\n")
	var out []string
	inSection := false
	inFence := false
	var fenceMarker string
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Toggle fence state on opening/closing fence lines. Markdown
		// allows ``` and ~~~; either may carry a language hint after.
		if !inFence {
			if strings.HasPrefix(trimmed, "```") {
				inFence = true
				fenceMarker = "```"
			} else if strings.HasPrefix(trimmed, "~~~") {
				inFence = true
				fenceMarker = "~~~"
			}
		} else if strings.HasPrefix(trimmed, fenceMarker) {
			inFence = false
		}

		if !inSection {
			if trimmed == heading {
				inSection = true
			}
			continue
		}
		if !inFence {
			if l := headingLevel(trimmed); l > 0 && l <= level {
				break
			}
		}
		out = append(out, line)
	}
	if !inSection {
		return ""
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// headingLevel returns 1 for `# `, 2 for `## `, 3 for `### `, etc.; 0 for
// non-heading lines. The trailing space is required (Markdown's ATX rule).
func headingLevel(line string) int {
	n := 0
	for _, r := range line {
		if r != '#' {
			break
		}
		n++
	}
	if n == 0 {
		return 0
	}
	if len(line) <= n || line[n] != ' ' {
		return 0
	}
	return n
}

// extractFencedYAML pulls the body of the first ```yaml ... ``` block in s.
// Returns ("", false) when no block is present. Tolerant of `yml` and
// alternate `~~~` fences for the rare agent that emits them.
func extractFencedYAML(s string) (string, bool) {
	m := progressYAMLBlockRE.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// parseDeferralsYAML decodes the Deferrals fenced block. Returns
// validation-friendly errors (no internal yaml.v3 wrappers) so the
// synthesized feedback is readable.
func parseDeferralsYAML(yamlBody string) (*deferralsYAMLDoc, error) {
	var doc deferralsYAMLDoc
	dec := yaml.NewDecoder(strings.NewReader(yamlBody))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return &doc, nil
		}
		return nil, fmt.Errorf("invalid YAML: %v", err)
	}
	if doc.Deferrals != nil {
		for i, d := range *doc.Deferrals {
			if strings.TrimSpace(d.Description) == "" {
				return nil, fmt.Errorf("deferrals[%d].description is required", i)
			}
			if d.DueByPhase <= 0 {
				return nil, fmt.Errorf("deferrals[%d].due_by_phase must be a positive roadmap phase number", i)
			}
			if strings.TrimSpace(d.Reason) == "" {
				return nil, fmt.Errorf("deferrals[%d].reason is required", i)
			}
		}
	}
	return &doc, nil
}

// splitStateTokenAndNote extracts the first non-blank line as the state
// token and the remaining body as a free-text note. Trailing whitespace and
// surrounding fences (some agents wrap the token in backticks) are
// tolerated. Note is trimmed; empty when no body follows.
func splitStateTokenAndNote(body string) (token, note string) {
	lines := strings.Split(body, "\n")
	tokenIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		token = strings.Trim(trimmed, "`*_ ")
		tokenIdx = i
		break
	}
	if tokenIdx < 0 {
		return "", ""
	}
	if tokenIdx+1 >= len(lines) {
		return token, ""
	}
	note = strings.TrimSpace(strings.Join(lines[tokenIdx+1:], "\n"))
	return token, note
}
