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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlan creates a plan file in dir containing the given Grounding section
// body — anything between `## Grounding\n` and the next top-level heading.
// Test helper, not exported.
func writePlan(t *testing.T, dir, groundingBody string) string {
	t.Helper()
	plan := "# Phase 1\n\n## Changes Required\nstub\n\n## Grounding\n" + groundingBody + "\n## Testing Strategy\nstub\n"
	path := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

// touchFile creates an empty file (and any parent dirs) to act as a present
// path for EXISTS row checks.
func touchFile(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestCheckGroundingTable_MissingSection(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("# Phase 1\n\n## Changes Required\nstub\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	res := CheckGroundingTable(plan, dir)
	if res.HasSection {
		t.Fatal("HasSection should be false when ## Grounding is absent")
	}
	if res.OK() {
		t.Fatal("OK() should be false when section is missing")
	}
}

// TestCheckGroundingTable_ExistsRowsAreNotChecked locks in the design
// decision that EXISTS file-existence is left to the LLM judge. A row
// classified EXISTS whose path does not exist must NOT trip the gate —
// the hand-coded path parser was structurally brittle to repo-prefixed
// References and produced false positives that corrupted planner
// revisions. See checkGroundingRow's comment.
func TestCheckGroundingTable_ExistsRowsAreNotChecked(t *testing.T) {
	dir := t.TempDir()
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`internal/feature/feature.go`" + ` | EXISTS | line 1 |
`
	plan := writePlan(t, dir, body)
	// feature.go intentionally NOT created — gate must still pass.
	res := CheckGroundingTable(plan, dir)
	if !res.OK() {
		t.Fatalf("EXISTS rows must not be path-checked anymore, got findings: %+v", res.Findings)
	}
}

func TestCheckGroundingTable_IllegalClassification(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, dir, "x/y.go")
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`x/y.go`" + ` | EXISTS-POST-PHASE-2 | line 1 |
| ` + "`x/y.go`" + ` | MAYBE | line 1 |
`
	plan := writePlan(t, dir, body)
	res := CheckGroundingTable(plan, dir)
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 illegal-classification findings, got %d: %+v", len(res.Findings), res.Findings)
	}
	for i, f := range res.Findings {
		if !strings.Contains(f.Reason, "not one of {EXISTS, WILL-BE-CREATED}") {
			t.Errorf("finding %d: reason = %q, want classification rejection", i, f.Reason)
		}
	}
}

func TestCheckGroundingTable_WillBeCreatedButAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	touchFile(t, dir, "internal/agent/grounding_check.go")
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`internal/agent/grounding_check.go`" + ` | WILL-BE-CREATED | new file |
`
	plan := writePlan(t, dir, body)
	res := CheckGroundingTable(plan, dir)
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 contradiction finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	if !strings.Contains(res.Findings[0].Reason, "already exists") {
		t.Errorf("reason = %q, want it to mention already-exists contradiction", res.Findings[0].Reason)
	}
}

func TestCheckGroundingTable_WillBeCreatedSymbolInExistingFile(t *testing.T) {
	// A WILL-BE-CREATED row whose Reference is a symbol (not a path) and
	// whose Evidence cites an existing file is a legitimate "new symbol in
	// existing file" pattern. Must not be flagged — leave it to the LLM.
	dir := t.TempDir()
	touchFile(t, dir, "internal/feature/feature.go")
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`Feature.SchemaVersion`" + ` | WILL-BE-CREATED | new field in ` + "`internal/feature/feature.go`" + ` |
`
	plan := writePlan(t, dir, body)
	res := CheckGroundingTable(plan, dir)
	if !res.OK() {
		t.Fatalf("expected clean check (symbol-in-existing-file is legitimate), got: %+v", res.Findings)
	}
}

func TestCheckGroundingTable_PathPatternIgnored(t *testing.T) {
	// Path patterns with template variables can't be statted. The pre-check
	// must skip them entirely so the LLM judge can apply fuzzy reasoning.
	dir := t.TempDir()
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`<runDir>/implement/<repoName>/iter-NN/`" + ` | EXISTS | path convention |
`
	plan := writePlan(t, dir, body)
	res := CheckGroundingTable(plan, dir)
	if !res.OK() {
		t.Fatalf("path patterns must be ignored by pre-check, got findings: %+v", res.Findings)
	}
}

func TestCheckGroundingTable_MultipleDefectsExhaustive(t *testing.T) {
	// Both defect classes must surface in one pass: WBC contradiction
	// and bad classification. EXISTS missing-file is intentionally NOT
	// checked.
	dir := t.TempDir()
	touchFile(t, dir, "exists/a.go")
	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`exists/a.go`" + ` | EXISTS | ok |
| ` + "`missing/b.go`" + ` | EXISTS | not checked anymore |
| ` + "`exists/a.go`" + ` | WILL-BE-CREATED | contradiction |
| ` + "`missing/c.go`" + ` | MAYBE | bad classification |
`
	plan := writePlan(t, dir, body)
	res := CheckGroundingTable(plan, dir)
	if len(res.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (WBC contradiction + bad classification): %+v", len(res.Findings), res.Findings)
	}
	wantRows := map[int]bool{3: true, 4: true}
	for _, f := range res.Findings {
		if !wantRows[f.RowNumber] {
			t.Errorf("unexpected row number %d in finding: %+v", f.RowNumber, f)
		}
	}
}

func TestFormatGroundingPreCheckFeedback_Shape(t *testing.T) {
	res := GroundingCheckResult{
		HasSection: true,
		Findings: []GroundingFinding{
			{RowNumber: 2, Reference: "`x/y.go`", Classification: "WHATEVER", Reason: "classification \"WHATEVER\" is not one of {EXISTS, WILL-BE-CREATED} — see validate-phase-plan-grounding"},
		},
	}
	out := FormatGroundingPreCheckFeedback(res, "abc1234", "feature/x")
	checks := []string{
		"## Pre-flight",
		"abc1234",
		"feature/x",
		"## Findings",
		"Mechanical row checks failed",
		"Row 2:",
		"## Verdict\nCHANGES_REQUESTED",
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("feedback missing %q\n--- output:\n%s", want, out)
		}
	}
	// Critical: must NOT emit a sticky approval marker. A short-circuit
	// rejection must never grant sticky approval — would silently skip the
	// LLM judge on subsequent attempts even after the reviser fixes the
	// table.
	if strings.Contains(out, "## Sticky Approval") {
		t.Errorf("feedback must not emit ## Sticky Approval on rejection:\n%s", out)
	}
}

func TestFormatGroundingPreCheckFeedback_MissingSection(t *testing.T) {
	out := FormatGroundingPreCheckFeedback(GroundingCheckResult{HasSection: false}, "abc", "main")
	if !strings.Contains(out, "no `## Grounding` section") {
		t.Errorf("missing-section feedback should explain absence:\n%s", out)
	}
	if !strings.Contains(out, "## Verdict\nCHANGES_REQUESTED") {
		t.Errorf("missing-section feedback must end with ## Verdict\\nCHANGES_REQUESTED block:\n%s", out)
	}
}

// TestCheckGroundingTableRepos_RepoPrefixRoutesContradiction locks in that
// the multi-repo prefix routing still works for the WBC contradiction
// check — `payments:internal/foo.go` declared WILL-BE-CREATED must trip the
// contradiction when foo.go exists in payments's worktree (not agentic's).
func TestCheckGroundingTableRepos_RepoPrefixRoutesContradiction(t *testing.T) {
	parent := t.TempDir()
	agentic := filepath.Join(parent, "agentic")
	payments := filepath.Join(parent, "payments")
	for _, d := range []string{agentic, payments} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	touchFile(t, payments, "internal/foo.go")

	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`payments:internal/foo.go`" + ` | WILL-BE-CREATED | new file |
| ` + "`agentic:internal/foo.go`" + ` | WILL-BE-CREATED | new file |
`
	plan := writePlan(t, parent, body)

	res := CheckGroundingTableRepos(plan, []GroundingRoot{
		{Name: "agentic", Worktree: agentic},
		{Name: "payments", Worktree: payments},
	})
	if len(res.Findings) != 1 || res.Findings[0].RowNumber != 1 {
		t.Fatalf("expected 1 contradiction on row 1 (payments), got: %+v", res.Findings)
	}
	if !strings.Contains(res.Findings[0].Reason, "already exists") {
		t.Errorf("reason = %q, want already-exists contradiction", res.Findings[0].Reason)
	}
}

// TestCheckGroundingTableRepos_UnknownPrefixIsNotStripped guards against
// the slash-form strip chewing the leading segment of a genuine bare path.
func TestCheckGroundingTableRepos_UnknownPrefixIsNotStripped(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "agentic")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	touchFile(t, repo, "internal/foo.go")

	body := `| Reference | Classification | Evidence |
|-----------|----------------|----------|
| ` + "`internal/foo.go`" + ` | WILL-BE-CREATED | new file |
`
	plan := writePlan(t, parent, body)

	res := CheckGroundingTableRepos(plan, []GroundingRoot{
		{Name: "agentic", Worktree: repo},
	})
	// `internal/foo.go` exists under agentic, so WBC must contradict.
	// If `internal` were wrongly stripped, the gate would stat
	// `agentic/foo.go` (missing) and silently pass — which would mean
	// the prefix-strip is over-aggressive.
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Reason, "already exists") {
		t.Fatalf("expected WBC contradiction (proves no false strip), got: %+v", res.Findings)
	}
}
