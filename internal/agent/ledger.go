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
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ledgerUnitStatusPending / ledgerUnitStatusDone are the only two legal values
// of a ledger unit's status field.
const (
	ledgerUnitStatusPending = "pending"
	ledgerUnitStatusDone    = "done"
)

// ledgerIDRE is the stable-slug rule for unit IDs: alphanumerics and hyphens.
var ledgerIDRE = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// LedgerUnit is one row of the agent-maintained `## Ledger` YAML block. It is
// the atomic unit of semantic progress for a phase (a research question, an
// open clarification, a design decision, a KB category, a validator axis).
type LedgerUnit struct {
	ID       string `yaml:"id"`                 // stable agent-assigned slug, never renamed
	Status   string `yaml:"status"`             // "pending" | "done"
	Decision string `yaml:"decision,omitempty"` // design/plan only; <= ~2 sentences when done
}

// ParsedLedger is the decoded `## Ledger` block. A nil *ParsedLedger means the
// block was absent (legacy / fresh handoff); the methods are nil-safe.
type ParsedLedger struct {
	Units []LedgerUnit
}

// ledgerYAMLDoc is the on-the-wire shape. Units is a pointer so an entirely
// missing `units:` key is distinguishable from an empty list.
type ledgerYAMLDoc struct {
	Units *[]LedgerUnit `yaml:"units"`
}

// PendingCount returns the number of units whose status is pending.
func (l *ParsedLedger) PendingCount() int {
	if l == nil {
		return 0
	}
	n := 0
	for _, u := range l.Units {
		if u.Status == ledgerUnitStatusPending {
			n++
		}
	}
	return n
}

// PendingIDs returns the stable IDs of pending units, in ledger order.
func (l *ParsedLedger) PendingIDs() []string {
	if l == nil {
		return nil
	}
	var ids []string
	for _, u := range l.Units {
		if u.Status == ledgerUnitStatusPending {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

// DoneDecisionsSummary renders the decisions-so-far summary: one `[id] decision`
// line per done unit that carries a non-empty decision, joined by newlines.
// Returns "" when no done unit carries a decision.
func (l *ParsedLedger) DoneDecisionsSummary() string {
	if l == nil {
		return ""
	}
	var lines []string
	for _, u := range l.Units {
		if u.Status == ledgerUnitStatusDone && strings.TrimSpace(u.Decision) != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", u.ID, strings.TrimSpace(u.Decision)))
		}
	}
	return strings.Join(lines, "\n")
}

// parseLedgerBlock extracts and decodes the fenced YAML inside a `## Ledger`
// section body. Contract:
//
//	(nil, nil)            sectionBody is empty (caller treats the block as absent).
//	(parsed, nil)         success.
//	(partial, violations) the block decoded but unit-level validation failed.
//	(nil, violations)     the block could not be decoded at all.
//
// requireDecision is true for design/plan (a done unit must carry a decision).
// completeState reflects the handoff's `## Handoff State` (COMPLETE implies zero
// pending). Returned violations are plain strings appended to the handoff's
// ProtocolViolations in the existing style.
func parseLedgerBlock(sectionBody string, requireDecision, completeState bool) (*ParsedLedger, []string) {
	if strings.TrimSpace(sectionBody) == "" {
		return nil, nil
	}
	yamlBody, ok := extractFencedYAML(sectionBody)
	if !ok {
		return nil, []string{"`## Ledger` body must contain a single fenced YAML code block (```yaml ... ```)"}
	}

	var doc ledgerYAMLDoc
	dec := yaml.NewDecoder(strings.NewReader(yamlBody))
	// Intentionally NOT strict (no KnownFields(true)): models reliably add
	// benign annotations to ledger units (e.g. a `topic:` field) and rejecting
	// the whole handoff over an unrecognized key is too brittle — an otherwise
	// complete, correct ledger should not hard-fail the phase. The real
	// contract (id slug, valid status, uniqueness, decision rules, pending
	// counts) is enforced explicitly below, so typos in known fields still
	// surface as violations rather than passing silently.
	if err := dec.Decode(&doc); err != nil && err != io.EOF {
		return nil, []string{fmt.Sprintf("`## Ledger` YAML failed to parse: %v", err)}
	}
	if doc.Units == nil {
		return nil, []string{"`## Ledger` YAML must include a `units:` key (use `units: []` when empty)"}
	}

	parsed := &ParsedLedger{Units: *doc.Units}
	var violations []string
	seen := map[string]struct{}{}
	pending := 0
	for i, u := range parsed.Units {
		id := strings.TrimSpace(u.ID)
		if id == "" {
			violations = append(violations, fmt.Sprintf("`## Ledger` units[%d] is missing a non-empty `id`", i))
		} else {
			if !ledgerIDRE.MatchString(id) {
				violations = append(violations, fmt.Sprintf("`## Ledger` unit id %q must be a slug (letters, digits, hyphens)", id))
			}
			if _, dup := seen[id]; dup {
				violations = append(violations, fmt.Sprintf("`## Ledger` unit id %q is duplicated; ids must be unique", id))
			}
			seen[id] = struct{}{}
		}
		switch u.Status {
		case ledgerUnitStatusPending:
			pending++
		case ledgerUnitStatusDone:
			if requireDecision && strings.TrimSpace(u.Decision) == "" {
				violations = append(violations, fmt.Sprintf("`## Ledger` unit %q is done but missing the required `decision` field", id))
			}
		default:
			violations = append(violations, fmt.Sprintf("`## Ledger` unit %q has status %q; must be one of {pending, done}", id, u.Status))
		}
	}
	if completeState && pending > 0 {
		violations = append(violations, fmt.Sprintf("`## Handoff State` is COMPLETE but the `## Ledger` reports %d pending unit(s); resolve them or write CONTINUE", pending))
	}
	// A CONTINUE handoff with zero units would auto-complete on the very first
	// iteration (pending==0 → done), silently declaring the phase finished with
	// no work tracked. Require at least one unit on CONTINUE so a not-yet-
	// enumerated ledger is a protocol violation, not a premature success.
	if !completeState && len(parsed.Units) == 0 {
		violations = append(violations, "`## Ledger` has no units on a CONTINUE handoff; register at least one pending unit, or write COMPLETE if there is genuinely nothing to do")
	}
	return parsed, violations
}

// LedgerProgressStrategy is the blocking-loop ProgressStrategy: it derives the
// pending count, decisions summary, and pending IDs from a handoff's `## Ledger`
// block via a phase-specific parser.
type LedgerProgressStrategy struct {
	Parse func(path string) (*ParsedHelperHandoff, error)
}

func (s *LedgerProgressStrategy) CountPending(path string) (int, error) {
	parsed, err := s.Parse(path)
	if err != nil {
		return 0, err
	}
	if parsed == nil || parsed.Ledger == nil {
		return LedgerAbsent, nil
	}
	return parsed.Ledger.PendingCount(), nil
}

func (s *LedgerProgressStrategy) SummarizeDecisions(path string) (string, error) {
	parsed, err := s.Parse(path)
	if err != nil {
		return "", err
	}
	if parsed == nil || parsed.Ledger == nil {
		return "", nil
	}
	return parsed.Ledger.DoneDecisionsSummary(), nil
}

func (s *LedgerProgressStrategy) PendingIDs(path string) ([]string, error) {
	parsed, err := s.Parse(path)
	if err != nil {
		return nil, err
	}
	if parsed == nil || parsed.Ledger == nil {
		return nil, nil
	}
	return parsed.Ledger.PendingIDs(), nil
}

// PlanningLedgerProgressStrategy adapts the planning handoff's `## Ledger` to
// the shared ProgressStrategy interface, so the within-attempt planning
// continuation loop uses the same net-pending + auto-complete mechanics as the
// blocking-loop phases. Planning is design-like, so done units carry decisions.
type PlanningLedgerProgressStrategy struct{}

func (PlanningLedgerProgressStrategy) ledgerOf(path string) (*ParsedLedger, error) {
	parsed, err := ParsePlanningHandoffMd(path)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, nil
	}
	return parsed.Ledger, nil
}

func (s PlanningLedgerProgressStrategy) CountPending(path string) (int, error) {
	l, err := s.ledgerOf(path)
	if err != nil {
		return 0, err
	}
	if l == nil {
		return LedgerAbsent, nil
	}
	return l.PendingCount(), nil
}

func (s PlanningLedgerProgressStrategy) SummarizeDecisions(path string) (string, error) {
	l, err := s.ledgerOf(path)
	if err != nil {
		return "", err
	}
	if l == nil {
		return "", nil
	}
	return l.DoneDecisionsSummary(), nil
}

func (s PlanningLedgerProgressStrategy) PendingIDs(path string) ([]string, error) {
	l, err := s.ledgerOf(path)
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, nil
	}
	return l.PendingIDs(), nil
}

// LedgerResumeStrategy builds the compact `## Resume Context` block. It embeds
// the concrete *LedgerProgressStrategy so it can reach pending IDs and decisions
// from one parse. WithDecisions is true for design/plan, false otherwise.
type LedgerResumeStrategy struct {
	Progress      *LedgerProgressStrategy
	WithDecisions bool
}

func (s *LedgerResumeStrategy) Build(iteration int, priorHandoffPath, deliverablePath string) (string, error) {
	if iteration <= 1 || s.Progress == nil {
		return "", nil
	}
	pendingIDs, err := s.Progress.PendingIDs(priorHandoffPath)
	if err != nil {
		return "", err
	}
	var decisions string
	if s.WithDecisions {
		if decisions, err = s.Progress.SummarizeDecisions(priorHandoffPath); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	b.WriteString("## Resume Context\n\n")
	if len(pendingIDs) > 0 {
		fmt.Fprintf(&b, "Pending units: %s\n\n", strings.Join(pendingIDs, ", "))
	}
	if strings.TrimSpace(decisions) != "" {
		fmt.Fprintf(&b, "Decisions so far (binding; do not relitigate):\n%s\n\n", decisions)
	}
	if strings.TrimSpace(deliverablePath) != "" {
		fmt.Fprintf(&b, "Read the deliverable on demand at: %s\nDo NOT re-inject its full prose.\n", deliverablePath)
	}
	return b.String(), nil
}
