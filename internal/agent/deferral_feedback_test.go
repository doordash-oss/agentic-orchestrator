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

// TestFormatGateFeedback_UnclosedDeferralRendersSection is the primary
// regression guard for the "empty feedback file" bug. When
// ValidateDeferralLedger produced a KindDeferralUnclosed finding, the old
// FormatGateFeedback dropped it silently and wrote only the header +
// status line. The implementer then retried without the needed context. This test locks in
// that the finding actually appears in the rendered markdown.
func TestFormatGateFeedback_UnclosedDeferralRendersSection(t *testing.T) {
	result := ReportGateResult{
		Rejected: true,
		Findings: []ReportGateFinding{{
			Category: GateCategoryDeferral,
			Kind:     KindDeferralUnclosed,
			Detail:   "deferral D-a3f1c0 (\"Install Tailwind\"; created in phase 1) is due by this phase (3) and is still open.",
		}},
	}
	out := FormatGateFeedback(result)
	if !strings.Contains(out, "Cross-phase deferrals") {
		t.Errorf("missing Cross-phase deferrals section header; got:\n%s", out)
	}
	if !strings.Contains(out, "D-a3f1c0") {
		t.Errorf("missing deferral ID citation; got:\n%s", out)
	}
	if !strings.Contains(out, "due this phase is still open") {
		t.Errorf("missing actionable guidance; got:\n%s", out)
	}
	// Contract: the structured ## Verdict section must terminate the body
	// with the canonical CHANGES_REQUESTED token (file-based handoff).
	if !strings.HasSuffix(strings.TrimSpace(out), "## Verdict\nCHANGES_REQUESTED") {
		t.Errorf("missing terminal ## Verdict block; got:\n%s", out)
	}
	// The bug: output length was 318 bytes (header + intro + status).
	// A rendered finding pushes past that. Cheap sanity floor.
	if len(out) < 500 {
		t.Errorf("feedback suspiciously short (%d bytes) — finding likely dropped:\n%s", len(out), out)
	}
}

func TestFormatGateFeedback_ProseHedgeRendersSection(t *testing.T) {
	result := ReportGateResult{
		Rejected: true,
		Findings: []ReportGateFinding{{
			Category: GateCategoryDeferral,
			Kind:     KindDeferralProseHedge,
			Detail:   "report prose mentions cross-phase deferral(s) (\"lands in Phase 3\") but the structured `deferrals:` block is empty",
		}},
	}
	out := FormatGateFeedback(result)
	if !strings.Contains(out, "prose hedge") {
		t.Errorf("missing prose-hedge section header; got:\n%s", out)
	}
	if !strings.Contains(out, "lands in Phase 3") {
		t.Errorf("missing matched phrase citation; got:\n%s", out)
	}
}

func TestFormatGateFeedback_MissingReasonRendersSection(t *testing.T) {
	result := ReportGateResult{
		Rejected: true,
		Findings: []ReportGateFinding{{
			Category: GateCategoryDeferral,
			Kind:     KindDeferralMissingReason,
			Detail:   "deferrals entry with description \"Migrate session store\" and due_by_phase=5 has an empty `reason`",
		}},
	}
	out := FormatGateFeedback(result)
	if !strings.Contains(out, "missing a `reason`") {
		t.Errorf("missing reason-gap section header; got:\n%s", out)
	}
	if !strings.Contains(out, "Migrate session store") {
		t.Errorf("missing offending entry description; got:\n%s", out)
	}
}

// TestFormatGateFeedback_MixedCategoriesAllRender exercises the realistic
// case where the gate rejects for schema + hedge + deferral findings at
// once. All three sections must appear in output.
func TestFormatGateFeedback_MixedCategoriesAllRender(t *testing.T) {
	result := ReportGateResult{
		Rejected: true,
		Findings: []ReportGateFinding{
			{
				CheckName: "build",
				Category:  GateCategorySchema,
				Kind:      KindEmptyEvidence,
				Detail:    "status is passed but evidence is empty",
			},
			{
				CheckName: "tests",
				Category:  GateCategoryHedge,
				Kind:      KindHedgePhrase,
				Detail:    "status is passed but evidence text contains hedge phrases that describe failure",
			},
			{
				Category: GateCategoryDeferral,
				Kind:     KindDeferralUnclosed,
				Detail:   "deferral D-abc123 is due by this phase (2) and is still open",
			},
		},
	}
	out := FormatGateFeedback(result)
	for _, want := range []string{
		"Schema integrity",
		"Pass claims contradicted by their own evidence",
		"Cross-phase deferrals",
		"## Verdict\nCHANGES_REQUESTED",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in mixed-category feedback; got:\n%s", want, out)
		}
	}
}

// TestFormatGateFeedback_NoDeferralFindingsNoSection — backwards compat:
// a pure schema rejection should not carry an empty Cross-phase deferrals
// section.
func TestFormatGateFeedback_NoDeferralFindingsNoSection(t *testing.T) {
	result := ReportGateResult{
		Rejected: true,
		Findings: []ReportGateFinding{{
			CheckName: "build",
			Category:  GateCategorySchema,
			Kind:      KindEmptyEvidence,
			Detail:    "status is passed but evidence is empty",
		}},
	}
	out := FormatGateFeedback(result)
	if strings.Contains(out, "Cross-phase deferrals") {
		t.Errorf("unexpected Cross-phase section when no deferral findings; got:\n%s", out)
	}
}
