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
	"reflect"
	"strings"
	"testing"
)

func TestBuildReviewPrompt(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "all tests pass", "/tmp/progress.md", "/tmp/artifacts/iteration-03", "", "/tmp/artifacts/iteration-03/verification-report.yaml", 3, nil, "", "", "")

	checks := []string{
		"/tmp/plan.md",
		"all tests pass",
		"/tmp/progress.md",
		"/tmp/artifacts/iteration-03",
		"Iteration under review: 3",
		"Iteration artifacts directory:",
		"Read the implementation plan",
		"Read the current progress",
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected review prompt to contain %q", c)
		}
	}
}

func TestBuildReviewPromptWithoutOptionalFields(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "", "", "/tmp/iter", "", "", 1, nil, "", "", "")

	if strings.Contains(prompt, "Exit Criteria") {
		t.Error("expected no exit criteria section when empty")
	}
	if !strings.Contains(prompt, "/tmp/plan.md") {
		t.Error("expected plan path in prompt")
	}
	if strings.Contains(prompt, "Read the current progress") {
		t.Error("expected no progress section when empty")
	}
}

func TestBuildReviewPromptWithTestingContract(t *testing.T) {
	prompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"all tests pass",
		"/tmp/progress.md",
		"/tmp/iter-01",
		"/tmp/phase-01/testing-contract.yaml",
		"/tmp/iter-01/verification-report.yaml",
		1,
		nil,
		"",
		"",
		"",
	)

	checks := []string{
		"Testing Contract",
		"/tmp/phase-01/testing-contract.yaml",
		"Read the verification report",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
}

func TestBuildReviewPromptLeavesOutputContractToRoleSpec(t *testing.T) {
	feedbackPath := "/tmp/iter-01/review/review-feedback.md"
	prompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"all tests pass",
		"/tmp/progress.md",
		"/tmp/iter-01",
		"/tmp/phase-01/testing-contract.yaml",
		"/tmp/iter-01/verification-report.yaml",
		1,
		nil,
		"",
		"",
		feedbackPath,
	)

	if strings.Contains(prompt, feedbackPath) {
		t.Fatalf("BuildReviewPrompt() rendered RoleSpec-owned feedback path %q:\n%s", feedbackPath, prompt)
	}
	if strings.Contains(prompt, "Handoff Contract") || strings.Contains(prompt, "Feedback output file") {
		t.Fatalf("BuildReviewPrompt() rendered output handoff prose:\n%s", prompt)
	}
}

func TestBuildReviewPromptWithLegacyVerificationRequirements(t *testing.T) {
	required := []RequiredVerificationItem{
		{Name: "Unit tests pass", Requirement: "go test ./..."},
		{Name: "Lint check", Requirement: "golangci-lint run"},
		{Requirement: "make build"},
	}
	prompt := BuildReviewPrompt(
		"/tmp/plan.md",
		"all tests pass",
		"/tmp/progress.md",
		"/tmp/iter-01",
		"",
		"/tmp/iter-01/verification-report.yaml",
		1,
		required,
		"",
		"",
		"",
	)

	checks := []string{
		"Required Verification Items",
		"go test ./...",
		"golangci-lint run",
		"make build",
		"Unit tests pass",
		"Lint check",
		"Review Rules (With Verification Items)",
	}
	for _, c := range checks {
		if !strings.Contains(prompt, c) {
			t.Errorf("expected prompt to contain %q", c)
		}
	}
}

func TestBuildReviewPromptWithoutVerificationSteps(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "", "", "/tmp/iter", "", "", 1, nil, "", "", "")

	if strings.Contains(prompt, "Testing Contract") {
		t.Error("expected no testing contract section when path empty")
	}
	if !strings.Contains(prompt, "Review Rules (Without Verification Items)") {
		t.Error("expected without-verification rules reference")
	}
}

func TestBuildReviewPromptTracerBullet(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "tests pass", "", "/tmp/iter", "", "", 1, nil, "", "tracer-bullet", "")
	if !strings.Contains(prompt, "tracer-bullet") {
		t.Error("expected tracer-bullet phase type in prompt")
	}
	if !strings.Contains(prompt, "Phase Type") {
		t.Error("expected Phase Type section")
	}
}

func TestBuildReviewPromptTDDFillIn(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "tests pass", "", "/tmp/iter", "", "", 1, nil, "", "tdd-fill-in", "")
	if !strings.Contains(prompt, "tdd-fill-in") {
		t.Error("expected tdd-fill-in phase type in prompt")
	}
}

func TestBuildReviewPromptCollapsed(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "tests pass", "", "/tmp/iter", "", "", 1, nil, "", "collapsed", "")
	if !strings.Contains(prompt, "collapsed") {
		t.Error("expected collapsed phase type in prompt")
	}
}

func TestBuildReviewPromptEmptyPhaseType(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "tests pass", "", "/tmp/iter", "", "", 1, nil, "", "", "")
	if strings.Contains(prompt, "Phase Type") {
		t.Error("expected no phase type section for empty phase type")
	}
}

func TestBuildReviewPromptRoadmapLinked(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "", "", "/tmp/iter", "", "", 1, nil, "/tmp/roadmap.md", "", "")
	if !strings.Contains(prompt, "Read the approved roadmap at: /tmp/roadmap.md") {
		t.Error("expected roadmap path reference")
	}
	if !strings.Contains(prompt, "Scope guidance") {
		t.Error("expected scope guidance when roadmap present")
	}
}

func TestBuildReviewPromptNoRoadmap(t *testing.T) {
	prompt := BuildReviewPrompt("/tmp/plan.md", "", "", "/tmp/iter", "", "", 1, nil, "", "", "")
	if strings.Contains(prompt, "Approved Roadmap") {
		t.Error("expected no roadmap section when path empty")
	}
}

func TestParseReviewFeedback(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantStat       ReviewStatus
		wantViolations bool
		wantMarkers    ValidatorMarkers
	}{
		{
			name:     "approved with no findings",
			body:     "## Findings\n- (none)\n\n## Suggestions\n- (none)\n\n## Verdict\nAPPROVED\n",
			wantStat: ReviewApproved,
		},
		{
			name:     "changes_requested with prose findings",
			body:     "## Findings\n- **High**: Fix error handling in auth.go\n\n## Suggestions\n- Consider adding docs\n\n## Verdict\nCHANGES_REQUESTED\n",
			wantStat: ReviewChangesRequested,
		},
		{
			name:     "approved with sticky approval block",
			body:     "## Findings\n- (none)\n\n## Suggestions\n- (none)\n\n## Sticky Approval\n\naxis: architecture\nfrozen_sections:\n- Phase 3: Wire the dispatcher\n- Architecture Approach\n\n## Verdict\nAPPROVED\n",
			wantStat: ReviewApproved,
			wantMarkers: ValidatorMarkers{
				AxisApproved:   "architecture",
				FrozenSections: []string{"Phase 3: Wire the dispatcher", "Architecture Approach"},
			},
		},
		{
			name:           "missing required section is a protocol violation",
			body:           "## Findings\n- (none)\n\n## Verdict\nAPPROVED\n",
			wantStat:       ReviewApproved,
			wantViolations: true,
		},
		{
			name:           "unknown verdict token is a protocol violation",
			body:           "## Findings\n- (none)\n\n## Suggestions\n- (none)\n\n## Verdict\nMAYBE\n",
			wantStat:       ReviewFailed,
			wantViolations: true,
		},
		{
			name:           "empty file is a protocol violation",
			body:           "",
			wantStat:       ReviewFailed,
			wantViolations: true,
		},
		{
			name:     "sticky approval skips angle-bracket placeholder bullets",
			body:     "## Findings\n- (none)\n\n## Suggestions\n- (none)\n\n## Sticky Approval\n\naxis: grounding\nfrozen_sections:\n- ## Grounding\n- <any other section whose references you spot-checked>\n- ## Changes Required\n\n## Verdict\nAPPROVED\n",
			wantStat: ReviewApproved,
			wantMarkers: ValidatorMarkers{
				AxisApproved:   "grounding",
				FrozenSections: []string{"## Grounding", "## Changes Required"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review-feedback.md")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			parsed, err := ParseReviewFeedback(path)
			if err != nil {
				t.Fatalf("ParseReviewFeedback: %v", err)
			}
			if parsed.Verdict != tt.wantStat {
				t.Errorf("Verdict = %v, want %v (violations=%v)", parsed.Verdict, tt.wantStat, parsed.ProtocolViolations)
			}
			gotViolations := len(parsed.ProtocolViolations) > 0
			if gotViolations != tt.wantViolations {
				t.Errorf("ProtocolViolations present = %v, want %v: %v", gotViolations, tt.wantViolations, parsed.ProtocolViolations)
			}
			if tt.wantMarkers.AxisApproved != "" {
				if parsed.Markers.AxisApproved != tt.wantMarkers.AxisApproved {
					t.Errorf("Markers.AxisApproved = %q, want %q", parsed.Markers.AxisApproved, tt.wantMarkers.AxisApproved)
				}
				if !reflect.DeepEqual(parsed.Markers.FrozenSections, tt.wantMarkers.FrozenSections) {
					t.Errorf("Markers.FrozenSections = %#v, want %#v", parsed.Markers.FrozenSections, tt.wantMarkers.FrozenSections)
				}
			}
		})
	}
}

func TestParseReviewFeedback_MissingFile(t *testing.T) {
	parsed, err := ParseReviewFeedback(filepath.Join(t.TempDir(), "does-not-exist.md"))
	if err != nil {
		t.Fatalf("ParseReviewFeedback: %v", err)
	}
	if parsed.Verdict != ReviewFailed {
		t.Errorf("Verdict = %v, want ReviewFailed for missing file", parsed.Verdict)
	}
	if len(parsed.ProtocolViolations) == 0 {
		t.Errorf("expected a protocol violation for the missing file")
	}
}
