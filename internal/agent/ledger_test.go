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
	"strings"
	"testing"
)

func ledgerSection(yaml string) string {
	return "## Ledger\n```yaml\n" + yaml + "\n```\n"
}

func TestParseLedgerBlock_Valid(t *testing.T) {
	body := ledgerSection("units:\n  - id: Q-001\n    status: done\n  - id: Q-002\n    status: pending")
	parsed, violations := parseLedgerBlock(body, false, false)
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	if parsed == nil {
		t.Fatal("parsed == nil")
	}
	if got := parsed.PendingCount(); got != 1 {
		t.Fatalf("PendingCount = %d, want 1", got)
	}
	if ids := parsed.PendingIDs(); len(ids) != 1 || ids[0] != "Q-002" {
		t.Fatalf("PendingIDs = %v, want [Q-002]", ids)
	}
}

func TestParseLedgerBlock_AbsentBodyReturnsNil(t *testing.T) {
	parsed, violations := parseLedgerBlock("", false, false)
	if parsed != nil || violations != nil {
		t.Fatalf("got (%v, %v), want (nil, nil) for absent ledger", parsed, violations)
	}
}

func TestParseLedgerBlock_MissingFenceIsViolation(t *testing.T) {
	parsed, violations := parseLedgerBlock("no yaml here", false, false)
	if parsed != nil {
		t.Fatalf("parsed = %v, want nil", parsed)
	}
	if len(violations) == 0 || !strings.Contains(violations[0], "fenced YAML") {
		t.Fatalf("violations = %v, want a fenced-YAML violation", violations)
	}
}

func TestParseLedgerBlock_MissingUnitsKeyIsViolation(t *testing.T) {
	body := ledgerSection("# intentionally no units key")
	parsed, violations := parseLedgerBlock(body, false, false)
	if parsed != nil {
		t.Fatalf("parsed = %v, want nil", parsed)
	}
	if len(violations) == 0 || !strings.Contains(violations[0], "units:") {
		t.Fatalf("violations = %v, want a units-key violation", violations)
	}
}

// TestParseLedgerBlock_UnknownUnitFieldTolerated reproduces the run-018 failure
// mode: the model added a `topic:` annotation to every ledger unit. Unknown
// fields must be ignored, not hard-fail the handoff, while the known fields are
// still parsed and validated.
func TestParseLedgerBlock_UnknownUnitFieldTolerated(t *testing.T) {
	body := ledgerSection("units:\n  - id: Q-001\n    status: done\n    topic: startup entry point\n  - id: Q-002\n    status: pending\n    topic: shutdown behavior")
	parsed, violations := parseLedgerBlock(body, false, false)
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none (unknown unit field should be tolerated)", violations)
	}
	if parsed == nil {
		t.Fatal("parsed == nil")
	}
	if got := parsed.PendingCount(); got != 1 {
		t.Fatalf("PendingCount = %d, want 1", got)
	}
	if ids := parsed.PendingIDs(); len(ids) != 1 || ids[0] != "Q-002" {
		t.Fatalf("PendingIDs = %v, want [Q-002]", ids)
	}
}

func TestParseLedgerBlock_EmptyUnitsOnCompleteAllowed(t *testing.T) {
	body := ledgerSection("units: []")
	parsed, violations := parseLedgerBlock(body, false, true) // completeState=true
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none for empty units on COMPLETE", violations)
	}
	if parsed == nil || parsed.PendingCount() != 0 {
		t.Fatalf("parsed = %v, want present ledger with 0 pending", parsed)
	}
}

func TestParseLedgerBlock_EmptyUnitsOnContinueIsViolation(t *testing.T) {
	body := ledgerSection("units: []")
	_, violations := parseLedgerBlock(body, false, false) // completeState=false (CONTINUE)
	if !hasSubstr(violations, "no units on a CONTINUE handoff") {
		t.Fatalf("violations = %v, want an empty-units-on-CONTINUE violation", violations)
	}
}

func TestParseLedgerBlock_DuplicateIDIsViolation(t *testing.T) {
	body := ledgerSection("units:\n  - id: dup\n    status: pending\n  - id: dup\n    status: pending")
	_, violations := parseLedgerBlock(body, false, false)
	if !hasSubstr(violations, "duplicated") {
		t.Fatalf("violations = %v, want a duplicate-id violation", violations)
	}
}

func TestParseLedgerBlock_BadStatusIsViolation(t *testing.T) {
	body := ledgerSection("units:\n  - id: u1\n    status: cooking")
	_, violations := parseLedgerBlock(body, false, false)
	if !hasSubstr(violations, "must be one of {pending, done}") {
		t.Fatalf("violations = %v, want a bad-status violation", violations)
	}
}

func TestParseLedgerBlock_BadIDSlugIsViolation(t *testing.T) {
	body := ledgerSection("units:\n  - id: 'has space'\n    status: pending")
	_, violations := parseLedgerBlock(body, false, false)
	if !hasSubstr(violations, "must be a slug") {
		t.Fatalf("violations = %v, want a slug violation", violations)
	}
}

func TestParseLedgerBlock_DoneRequiresDecisionWhenRequired(t *testing.T) {
	body := ledgerSection("units:\n  - id: data-model\n    status: done")
	_, violations := parseLedgerBlock(body, true, false)
	if !hasSubstr(violations, "missing the required `decision`") {
		t.Fatalf("violations = %v, want a missing-decision violation", violations)
	}

	// Same ledger, decision NOT required → no violation.
	if _, v := parseLedgerBlock(body, false, false); len(v) != 0 {
		t.Fatalf("violations = %v, want none when decision not required", v)
	}
}

func TestParseLedgerBlock_CompleteWithPendingIsViolation(t *testing.T) {
	body := ledgerSection("units:\n  - id: u1\n    status: pending")
	_, violations := parseLedgerBlock(body, false, true)
	if !hasSubstr(violations, "COMPLETE but the `## Ledger` reports") {
		t.Fatalf("violations = %v, want a complete-with-pending violation", violations)
	}
}

func TestDoneDecisionsSummary(t *testing.T) {
	body := ledgerSection("units:\n  - id: a\n    status: done\n    decision: chose X\n  - id: b\n    status: pending\n  - id: c\n    status: done\n    decision: chose Y")
	parsed, violations := parseLedgerBlock(body, false, false)
	if len(violations) != 0 {
		t.Fatalf("violations = %v", violations)
	}
	summary := parsed.DoneDecisionsSummary()
	if !strings.Contains(summary, "[a] chose X") || !strings.Contains(summary, "[c] chose Y") {
		t.Fatalf("summary = %q, want both done decisions", summary)
	}
	if strings.Contains(summary, "[b]") {
		t.Fatalf("summary = %q, must not include pending unit b", summary)
	}
}

func TestParseResearchProgressHandoff_EmptyLedgerBodyIsViolation(t *testing.T) {
	// A present `## Ledger` heading with an empty body must be a protocol
	// violation, not a silent LedgerAbsent.
	handoff := "# Research Progress\n\n## Completed Findings\n- x\n\n## Remaining Areas\n- y\n\n## Where I Stopped\nnext\n\n## Gotchas\n- none\n\n## Ledger\n\n## Handoff State\nCONTINUE\n"
	path := writeHelperHandoff(t, t.TempDir(), ResearchProgressHandoffFilename, handoff)
	parsed, err := ParseResearchProgressHandoffMd(path)
	if err != nil {
		t.Fatalf("ParseResearchProgressHandoffMd error = %v", err)
	}
	if parsed.OK() {
		t.Fatalf("expected empty `## Ledger` body to be a protocol violation, got OK")
	}
	if !containsViolation(parsed.ProtocolViolations, "`## Ledger` section is empty") {
		t.Fatalf("violations = %v, want an empty-ledger violation", parsed.ProtocolViolations)
	}
}

func hasSubstr(violations []string, want string) bool {
	for _, v := range violations {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}
