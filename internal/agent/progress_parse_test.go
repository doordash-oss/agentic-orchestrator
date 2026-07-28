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

// progressMdHappy returns a minimal valid progress.md body for the given
// state. iterDir is interpolated into the Verification Report path bullet
// so the cross-check against the expected runtime path passes when the
// caller passes filepath.Join(iterDir, "verification-report.yaml").
func progressMdHappy(state, iterDir, stateNote string) string {
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
		name  string
		state string
		want  IterationState
	}{
		{testResultSuccessValue, agentStatusSuccess, StateSuccess},
		{"retry", "RETRY", StateRetry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			iterDir := filepath.Join(dir, "iteration-01")
			_ = os.MkdirAll(iterDir, 0o755)
			path := filepath.Join(dir, "progress.md")
			body := progressMdHappy(tt.state, iterDir, "")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			parsed, err := ParseProgressMd(path)
			if err != nil {
				t.Fatalf("ParseProgressMd: %v", err)
			}
			if !parsed.OK() {
				t.Fatalf("expected OK; violations=%v", parsed.ProtocolViolations)
			}
			if parsed.State != tt.want {
				t.Errorf("State = %v, want %v", parsed.State, tt.want)
			}
		})
	}
}

func TestParseProgressMd_MissingFile(t *testing.T) {
	parsed, err := ParseProgressMd(filepath.Join(t.TempDir(), "nope.md"))
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
	parsed, err := ParseProgressMd(path)
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
	parsed, _ := ParseProgressMd(path)
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
	parsed, _ := ParseProgressMd(path)
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
	parsed, _ := ParseProgressMd(path)
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
	parsed, _ := ParseProgressMd(path)
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
	parsed, _ := ParseProgressMd(path)
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

func TestParseProgressMd_AllowsFuturePhaseProse(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	_ = os.MkdirAll(iterDir, 0o755)
	body := `# Iteration Progress

## Iteration Handoff

### Completed this iteration
- Fixed prior feedback: progress.md used to mention "lands in Phase 8" while deferrals were empty.

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
	parsed, _ := ParseProgressMd(path)
	if !parsed.OK() {
		t.Fatalf("expected future-phase prose to stay out of protocol validation; got %v", parsed.ProtocolViolations)
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

func TestParseProgressMd_RejectsAgentAuthoredQuestionGate(t *testing.T) {
	dir := t.TempDir()
	iterDir := filepath.Join(dir, "iteration-01")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(progressMdHappy(agentStatusSuccess, iterDir, ""), "## Iteration State", "## Questions for User\n\n1. Which option?\n\n## Iteration State", 1)
	path := filepath.Join(dir, "progress.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseProgressMd(path)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.OK() || !strings.Contains(strings.Join(parsed.ProtocolViolations, "\n"), "AskUserQuestion") {
		t.Fatalf("violations = %v, want formal AskUserQuestion guidance", parsed.ProtocolViolations)
	}
}

func TestProgressFingerprint_StableAcrossIterationDirs(t *testing.T) {
	// Two progress.md files identical except for metadata outside the handoff.
	// The fingerprint should match because only the handoff is hashed — otherwise the
	// no-progress safety rail breaks (see TestImplementLoopSafetyRailNoProgress).
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.md")
	pathB := filepath.Join(dir, "b.md")
	_ = os.WriteFile(pathA, []byte(progressMdHappy(agentStatusSuccess, "/iter/01", "")), 0o644)
	_ = os.WriteFile(pathB, []byte(progressMdHappy(agentStatusSuccess, "/iter/02", "")), 0o644)
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
	bodyA := progressMdHappy(agentStatusSuccess, "/iter/01", "")
	bodyB := strings.Replace(bodyA, "did the thing", "did a different thing", 1)
	_ = os.WriteFile(pathA, []byte(bodyA), 0o644)
	_ = os.WriteFile(pathB, []byte(bodyB), 0o644)
	fpA, _ := ProgressFingerprint(pathA)
	fpB, _ := ProgressFingerprint(pathB)
	if fpA == fpB {
		t.Errorf("fingerprints should differ when Iteration Handoff body changes")
	}
}
