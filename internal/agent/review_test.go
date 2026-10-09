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
	"testing"
)

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
				AxisApproved:   testAxisGrounding,
				FrozenSections: []string{testGroundingSectionHeading, "## Changes Required"},
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
