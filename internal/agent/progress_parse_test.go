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
	"path/filepath"
	"strings"
	"testing"
)

// progressMdHappy returns a minimal valid progress.md body for the given
// state. iterDir is interpolated into the Verification Report path bullet
// so the cross-check against the expected runtime path passes when the
// caller passes filepath.Join(iterDir, "verification-report.yaml").
//
// For NEED_USER_INPUT the helper inlines a single placeholder question so
// the parser's gate-completeness validation passes; tests that need to
// drive missing/malformed question variants build the body from
// progressMdNeedUserInput directly.
func progressMdHappy(state, iterDir, stateNote string) string {
	var qBlock string
	if state == "NEED_USER_INPUT" {
		qBlock = "\n## Questions for User\n\n1. Should the harness pick option A or B?\n"
	}
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- did the thing

### Remaining from the plan

### Where I stopped
At the end of unit 4.

### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run
` + qBlock + `
## Iteration State

` + state + "\n"
	if stateNote != "" {
		body += stateNote + "\n"
	}
	return body
}

func writeTempProgress(t *testing.T, body string) (path, iterDir string) {
	t.Helper()
	dir := t.TempDir()
	iterDir = filepath.Join(dir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iter: %v", err)
	}
	path = filepath.Join(dir, "progress.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write progress.md: %v", err)
	}
	return path, iterDir
}

func TestParseProgressMd_RoundTripStates(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		want      IterationState
		stateNote string
	}{
		{"success", "SUCCESS", StateSuccess, ""},
		{"retry", "RETRY", StateRetry, ""},
		{"need_user_input", "NEED_USER_INPUT", StateNeedUserInput, "Plan contradicts worktree."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			iterDir := filepath.Join(dir, "iteration-01")
			_ = os.MkdirAll(iterDir, 0o755)
			path := filepath.Join(dir, "progress.md")
			body := progressMdHappy(tt.state, iterDir, tt.stateNote)
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			parsed, err := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
			if err != nil {
				t.Fatalf("ParseProgressMd: %v", err)
			}
			if !parsed.OK() {
				t.Fatalf("expected OK; violations=%v", parsed.ProtocolViolations)
			}
			if parsed.State != tt.want {
				t.Errorf("State = %v, want %v", parsed.State, tt.want)
			}
			if tt.stateNote != "" && !strings.Contains(parsed.StateNote, tt.stateNote) {
				t.Errorf("StateNote = %q, want it to contain %q", parsed.StateNote, tt.stateNote)
			}
		})
	}
}

func TestParseProgressMd_MissingFile(t *testing.T) {
	parsed, err := ParseProgressMd(filepath.Join(t.TempDir(), "nope.md"), "")
	if err != nil {
		t.Fatalf("expected nil err for missing file, got: %v", err)
	}
	if parsed.OK() {
		t.Errorf("expected protocol violation for missing file; got OK=true")
	}
	if len(parsed.ProtocolViolations) == 0 {
		t.Errorf("expected at least one violation for missing file")
	}
}

func TestParseProgressMd_RejectsMissingSection(t *testing.T) {
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- thing

## Verification Report

- **Path**: /tmp/iter/verification-report.yaml

## Iteration State

SUCCESS
`
	path, _ := writeTempProgress(t, body)
	parsed, err := ParseProgressMd(path, "")
	if err != nil {
		t.Fatalf("ParseProgressMd: %v", err)
	}
	if parsed.OK() {
		t.Fatalf("expected protocol violation for missing Deferrals section")
	}
	found := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "## Deferrals") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected violation citing missing Deferrals; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_RejectsInvalidStateToken(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := progressMdHappy("MAYBE", iterDir, "")
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, "")
	if parsed.OK() {
		t.Fatalf("expected violation for invalid state token")
	}
	if parsed.State != StateInvalid {
		t.Errorf("State = %v, want StateInvalid", parsed.State)
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "MAYBE") || strings.Contains(v, "Iteration State") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected violation about invalid Iteration State token; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_RejectsMalformedDeferralsYAML(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals:
  - description: bad
    due_by_phase: not-an-int
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Iteration State

SUCCESS
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation for malformed Deferrals YAML")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "Deferrals YAML") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected Deferrals YAML violation; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_RejectsManualFollowUpShapedDeferral(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals:
  - id: phase-01-deferral-001
    title: Manual GitHub repository rename
    owner: human repo admin
    due_by_phase: 3
    reason: External repository operation.
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Iteration State

SUCCESS
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation for manual-follow-up deferral shape")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "field title not found") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected unknown deferral field violation; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_RejectsIncompleteDeferralEntry(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals:
  - description: Install Tailwind
    due_by_phase: 0
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Iteration State

SUCCESS
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation for incomplete deferral entry")
	}
	hitPhase := false
	hitReason := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "due_by_phase must be a positive roadmap phase number") {
			hitPhase = true
		}
		if strings.Contains(v, "reason is required") {
			hitReason = true
		}
	}
	if !hitPhase && !hitReason {
		t.Errorf("expected deferral field validation violation; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_RequiresExplicitDeferralsKeys(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
# explicit empty form is required; no keys at all is rejected.
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Iteration State

SUCCESS
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation when both deferrals keys are absent")
	}
	hits := 0
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "deferrals:") || strings.Contains(v, "closed_deferrals:") {
			hits++
		}
	}
	if hits < 2 {
		t.Errorf("expected violations citing both deferrals: and closed_deferrals: keys; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_VerificationPathMismatch(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	// Cite a wrong path.
	body := progressMdHappy("SUCCESS", "/wrong/path", "")
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation for verification path mismatch")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "Verification Report") && strings.Contains(v, "does not match") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected Verification Report path mismatch violation; got %v", parsed.ProtocolViolations)
	}
}

func TestParseProgressMd_DetectsProseHedge(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- Base styles shipped; Tailwind lands in Phase 3.

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Iteration State

SUCCESS
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation for prose-only deferral hedge")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "lands in Phase 3") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected prose-hedge violation citing the matched phrase; got %v", parsed.ProtocolViolations)
	}
}

func TestFormatProtocolViolationFeedback_TerminatesWithStatusLine(t *testing.T) {
	parsed := &ParsedProgress{
		ProtocolViolations: []string{
			"progress.md missing required section \"## Deferrals\"",
		},
	}
	out := FormatProtocolViolationFeedback(parsed)
	if !strings.HasSuffix(strings.TrimSpace(out), "## Verdict\nCHANGES_REQUESTED") {
		t.Errorf("missing terminal ## Verdict block; got:\n%s", out)
	}
	if !strings.Contains(out, "missing required section") {
		t.Errorf("violation should be enumerated; got:\n%s", out)
	}
}

// TestParseProgressMd_NeedUserInputQuestions covers the happy-path
// `## Questions for User` parser: numbered prompts are captured into
// ParsedProgress.Questions for later persistence in the gate artifact.
func TestParseProgressMd_NeedUserInputQuestions(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- traced the gap

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Questions for User

1. Should the new path use the legacy library or the new one?
2. Is it acceptable to skip migration of historical sessions?

## Iteration State

NEED_USER_INPUT
Plan contradicts the worktree.
`
	path := filepath.Join(dir, "progress.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write progress.md: %v", err)
	}
	parsed, err := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if err != nil {
		t.Fatalf("ParseProgressMd: %v", err)
	}
	if !parsed.OK() {
		t.Fatalf("expected OK; violations=%v", parsed.ProtocolViolations)
	}
	if parsed.State != StateNeedUserInput {
		t.Fatalf("State = %v, want StateNeedUserInput", parsed.State)
	}
	if !strings.Contains(parsed.StateNote, "Plan contradicts") {
		t.Errorf("StateNote = %q, want it to contain summary", parsed.StateNote)
	}
	if len(parsed.Questions) != 2 {
		t.Fatalf("Questions count = %d, want 2: %v", len(parsed.Questions), parsed.Questions)
	}
	if !strings.Contains(parsed.Questions[0], "legacy library") {
		t.Errorf("Questions[0] = %q, want it to contain prompt text", parsed.Questions[0])
	}
	if !strings.Contains(parsed.Questions[1], "skip migration") {
		t.Errorf("Questions[1] = %q, want it to contain prompt text", parsed.Questions[1])
	}
}

// progressMdNeedUserInput renders a NEED_USER_INPUT progress.md with the
// requested summary note and questions list. iterDir is interpolated into
// the Verification Report path bullet so the cross-check passes when the
// caller wires the same iterDir into ParseProgressMd.
func progressMdNeedUserInput(iterDir, note string, questions []string) string {
	var qBlock string
	if questions != nil {
		qBlock = "\n## Questions for User\n\n"
		for i, q := range questions {
			qBlock += fmt.Sprintf("%d. %s\n", i+1, q)
		}
	}
	return `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- traced the gap

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run
` + qBlock + `
## Iteration State

NEED_USER_INPUT
` + note + "\n"
}

// TestParseProgressMd_NeedUserInputRejectsMissingQuestions covers the
// reviewer-flagged failure mode: a NEED_USER_INPUT handoff with no
// `## Questions for User` section is unrecoverable (the user has nothing
// to answer and resume's answer-completeness check rejects an empty set),
// so the parser must surface a protocol violation instead of letting the
// orchestrator persist a malformed gate.
func TestParseProgressMd_NeedUserInputRejectsMissingQuestions(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := progressMdNeedUserInput(iterDir, "Plan contradicts worktree.", nil)
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation when NEED_USER_INPUT lacks ## Questions for User")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "Questions for User") && strings.Contains(v, "missing") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected violation citing missing Questions for User; got %v", parsed.ProtocolViolations)
	}
}

// TestParseProgressMd_NeedUserInputRejectsEmptyStateNote ensures the gate
// summary (the body after the NEED_USER_INPUT token) is mandatory: without
// it the user has no description of why the gate opened.
func TestParseProgressMd_NeedUserInputRejectsEmptyStateNote(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := progressMdNeedUserInput(iterDir, "", []string{"What now?"})
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation when NEED_USER_INPUT has empty summary")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "summary") && strings.Contains(v, "NEED_USER_INPUT") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected violation citing missing summary; got %v", parsed.ProtocolViolations)
	}
}

// TestParseProgressMd_NeedUserInputRejectsHeadingWithoutPrompts covers the
// "heading present but body parses zero prompts" case: an unnumbered
// bullet list under `## Questions for User` is not a valid gate.
func TestParseProgressMd_NeedUserInputRejectsHeadingWithoutPrompts(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- traced the gap

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + iterDir + `/verification-report.yaml

## Questions for User

- not numbered, just a bullet
- another bullet

## Iteration State

NEED_USER_INPUT
Plan contradicts the worktree.
`
	path := filepath.Join(dir, "progress.md")
	_ = os.WriteFile(path, []byte(body), 0o644)
	parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if parsed.OK() {
		t.Fatalf("expected violation when Questions for User has zero numbered prompts")
	}
	hit := false
	for _, v := range parsed.ProtocolViolations {
		if strings.Contains(v, "Questions for User") && strings.Contains(v, "zero") {
			hit = true
		}
	}
	if !hit {
		t.Errorf("expected violation citing zero parsed prompts; got %v", parsed.ProtocolViolations)
	}
}

// TestParseProgressMd_RejectsQuestionsOnSuccessOrRetry guards against a
// stale `## Questions for User` block surviving into a non-gate iteration
// (e.g. the agent forgot to delete it after answering questions). Both
// SUCCESS and RETRY must reject the section.
func TestParseProgressMd_RejectsQuestionsOnSuccessOrRetry(t *testing.T) {
	for _, state := range []string{"SUCCESS", "RETRY"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			iterDir := filepath.Join(dir, "iteration-01")
			_ = os.MkdirAll(iterDir, 0o755)
			body := strings.Replace(
				progressMdHappy(state, iterDir, ""),
				"## Iteration State",
				"## Questions for User\n\n1. Stale question.\n\n## Iteration State",
				1,
			)
			path := filepath.Join(dir, "progress.md")
			_ = os.WriteFile(path, []byte(body), 0o644)
			parsed, _ := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
			if parsed.OK() {
				t.Fatalf("expected violation when state is %s but Questions for User is present", state)
			}
			hit := false
			for _, v := range parsed.ProtocolViolations {
				if strings.Contains(v, "Questions for User") && strings.Contains(v, state) {
					hit = true
				}
			}
			if !hit {
				t.Errorf("expected violation citing stale Questions for User on %s; got %v", state, parsed.ProtocolViolations)
			}
		})
	}
}

// TestParseProgressMd_RejectsQuestionsAfterIterationState locks in the
// section-ordering contract: `## Questions for User` MUST sit between
// `## Verification Report` and `## Iteration State`. A misplaced section
// (e.g., appended after `## Iteration State`) is a protocol violation so
// the deterministic retry path rejects the iteration before the gate
// artifact is persisted.
func TestParseProgressMd_RejectsQuestionsAfterIterationState(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iter: %v", err)
	}

	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- traced the blocker

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

` + "```yaml" + `
deferrals: []
closed_deferrals: []
` + "```" + `

## Verification Report

- **Path**: ` + filepath.Join(iterDir, "verification-report.yaml") + `
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Iteration State

NEED_USER_INPUT
Need a product choice before touching auth.

## Questions for User

1. Should implementation target the legacy auth path or the new auth service?
`
	path := filepath.Join(dir, "progress.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write progress.md: %v", err)
	}

	parsed, err := ParseProgressMd(path, filepath.Join(iterDir, "verification-report.yaml"))
	if err != nil {
		t.Fatalf("ParseProgressMd: %v", err)
	}
	if parsed.OK() {
		t.Fatalf("expected misplaced Questions for User section to be rejected")
	}
	if !strings.Contains(strings.Join(parsed.ProtocolViolations, "\n"), "between `## Verification Report` and `## Iteration State`") {
		t.Fatalf("violations = %v, want explicit ordering guidance", parsed.ProtocolViolations)
	}
}

func TestProgressFingerprint_StableAcrossIterationDirs(t *testing.T) {
	// Two progress.md files identical except for the verification-report
	// path inside `## Verification Report`. The fingerprint should match
	// because that section is excluded from the hash — otherwise the
	// no-progress safety rail breaks (see TestImplementLoopSafetyRailNoProgress).
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.md")
	pathB := filepath.Join(dir, "b.md")
	_ = os.WriteFile(pathA, []byte(progressMdHappy("SUCCESS", "/iter/01", "")), 0o644)
	_ = os.WriteFile(pathB, []byte(progressMdHappy("SUCCESS", "/iter/02", "")), 0o644)
	fpA, errA := ProgressFingerprint(pathA)
	fpB, errB := ProgressFingerprint(pathB)
	if errA != nil || errB != nil {
		t.Fatalf("fingerprint errors: %v / %v", errA, errB)
	}
	if fpA != fpB {
		t.Errorf("fingerprints differ across iteration dirs:\n  A=%s\n  B=%s", fpA, fpB)
	}
}

func TestProgressFingerprint_ChangesWhenHandoffChanges(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.md")
	pathB := filepath.Join(dir, "b.md")
	bodyA := progressMdHappy("SUCCESS", "/iter/01", "")
	bodyB := strings.Replace(bodyA, "did the thing", "did a different thing", 1)
	_ = os.WriteFile(pathA, []byte(bodyA), 0o644)
	_ = os.WriteFile(pathB, []byte(bodyB), 0o644)
	fpA, _ := ProgressFingerprint(pathA)
	fpB, _ := ProgressFingerprint(pathB)
	if fpA == fpB {
		t.Errorf("fingerprints should differ when Iteration Handoff body changes")
	}
}
