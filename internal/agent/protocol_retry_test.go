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
	"time"
)

func TestProtocolRetrySidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := ProtocolRetrySidecar{
		Role:          RoleResearcher,
		ActiveRun:     7,
		Consecutive:   2,
		LastViolation: "agentico-outcome: root agent did not emit a valid structured outcome",
		UpdatedAt:     time.Date(2026, 5, 18, 12, 34, 56, 0, time.UTC),
	}

	if err := WriteProtocolRetrySidecar(dir, want); err != nil {
		t.Fatalf("WriteProtocolRetrySidecar() error = %v", err)
	}

	got, err := ReadProtocolRetrySidecar(dir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar() error = %v", err)
	}
	if got == nil {
		t.Fatal("ReadProtocolRetrySidecar() = nil, want sidecar")
	}
	if got.Role != want.Role {
		t.Errorf("Role = %q, want %q", got.Role, want.Role)
	}
	if got.ActiveRun != want.ActiveRun {
		t.Errorf("ActiveRun = %d, want %d", got.ActiveRun, want.ActiveRun)
	}
	if got.Consecutive != want.Consecutive {
		t.Errorf("Consecutive = %d, want %d", got.Consecutive, want.Consecutive)
	}
	if got.LastViolation != want.LastViolation {
		t.Errorf("LastViolation = %q, want %q", got.LastViolation, want.LastViolation)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt = %s, want %s", got.UpdatedAt.Format(time.RFC3339), want.UpdatedAt.Format(time.RFC3339))
	}
}

func TestReadProtocolRetrySidecarMissingAndMalformed(t *testing.T) {
	dir := t.TempDir()

	got, err := ReadProtocolRetrySidecar(dir)
	if err != nil {
		t.Fatalf("ReadProtocolRetrySidecar(missing) error = %v", err)
	}
	if got != nil {
		t.Fatalf("ReadProtocolRetrySidecar(missing) = %#v, want nil", got)
	}

	if err := os.WriteFile(filepath.Join(dir, ProtocolRetrySidecarFile), []byte("role: ["), 0o644); err != nil {
		t.Fatalf("write malformed sidecar: %v", err)
	}
	if _, err := ReadProtocolRetrySidecar(dir); err == nil {
		t.Fatal("ReadProtocolRetrySidecar(malformed) error = nil, want error")
	}
}

func TestRemoveCompletionReceiptOnlyRemovesReceipt(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		PhaseCompleteFile:        "",
		"artifact.md":            "# artifact\n",
		ProtocolRetrySidecarFile: "role: researcher\n",
		"qa-answers.md":          "Q: x\nA: y\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if err := RemoveCompletionReceipt(dir); err != nil {
		t.Fatalf("RemoveCompletionReceipt() error = %v", err)
	}
	if err := RemoveCompletionReceipt(dir); err != nil {
		t.Fatalf("RemoveCompletionReceipt() second call error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, PhaseCompleteFile)); !os.IsNotExist(err) {
		t.Fatalf("phase_complete stat err = %v, want not exist", err)
	}
	for _, name := range []string{"artifact.md", ProtocolRetrySidecarFile, "qa-answers.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s was removed, want preserved: %v", name, err)
		}
	}
}

func TestProtocolRetryDecision(t *testing.T) {
	violations := []ProtocolViolation{{
		Artifact: "agentico-outcome",
		Reason:   "root agent did not emit a valid structured outcome",
	}}
	dir := filepath.Join(t.TempDir(), "research")
	formatted := FormatSingleShotProtocolViolationError(RoleResearcher, dir, violations)

	tests := []struct {
		name                 string
		violations           []ProtocolViolation
		sidecar              *ProtocolRetrySidecar
		activeRun            int
		maxConsecutive       int
		wantAction           ProtocolRetryAction
		wantConsecutive      int
		wantNewSidecar       bool
		wantFormattedError   string
		wantSidecarActiveRun int
	}{
		{
			name:            "empty_violations_succeed_default_cap",
			activeRun:       1,
			wantAction:      ProtocolRetryActionSucceed,
			wantConsecutive: 0,
		},
		{
			name:                 "first_violation_retries_default_cap",
			violations:           violations,
			activeRun:            1,
			wantAction:           ProtocolRetryActionRetry,
			wantConsecutive:      1,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
		{
			name:       "consecutive_violation_retries_default_cap",
			violations: violations,
			sidecar: &ProtocolRetrySidecar{
				Role:        RoleResearcher,
				ActiveRun:   1,
				Consecutive: 1,
			},
			activeRun:            1,
			wantAction:           ProtocolRetryActionRetry,
			wantConsecutive:      2,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
		{
			name:       "cross_run_stale_sidecar_resets_default_cap",
			violations: violations,
			sidecar: &ProtocolRetrySidecar{
				Role:        RoleResearcher,
				ActiveRun:   2,
				Consecutive: 2,
			},
			activeRun:            3,
			wantAction:           ProtocolRetryActionRetry,
			wantConsecutive:      1,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 3,
		},
		{
			name:       "role_mismatch_sidecar_resets_default_cap",
			violations: violations,
			sidecar: &ProtocolRetrySidecar{
				Role:        RoleDesigner,
				ActiveRun:   1,
				Consecutive: 2,
			},
			activeRun:            1,
			wantAction:           ProtocolRetryActionRetry,
			wantConsecutive:      1,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
		{
			name:       "exhausted_default_cap",
			violations: violations,
			sidecar: &ProtocolRetrySidecar{
				Role:        RoleResearcher,
				ActiveRun:   1,
				Consecutive: 2,
			},
			activeRun:            1,
			wantAction:           ProtocolRetryActionTerminal,
			wantConsecutive:      DefaultMaxConsecutiveProtocolViolations,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
		{
			name:                 "first_violation_retries_injected_cap",
			violations:           violations,
			activeRun:            1,
			maxConsecutive:       2,
			wantAction:           ProtocolRetryActionRetry,
			wantConsecutive:      1,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
		{
			name:       "exhausted_injected_cap",
			violations: violations,
			sidecar: &ProtocolRetrySidecar{
				Role:        RoleResearcher,
				ActiveRun:   1,
				Consecutive: 1,
			},
			activeRun:            1,
			maxConsecutive:       2,
			wantAction:           ProtocolRetryActionTerminal,
			wantConsecutive:      2,
			wantNewSidecar:       true,
			wantFormattedError:   formatted,
			wantSidecarActiveRun: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideProtocolRetry(RoleResearcher, dir, tt.activeRun, tt.sidecar, tt.violations, tt.maxConsecutive)
			if got.Action != tt.wantAction {
				t.Errorf("DecideProtocolRetry().Action = %v, want %v", got.Action, tt.wantAction)
			}
			if got.Consecutive != tt.wantConsecutive {
				t.Errorf("DecideProtocolRetry().Consecutive = %d, want %d", got.Consecutive, tt.wantConsecutive)
			}
			if got.FormattedError != tt.wantFormattedError {
				t.Errorf("DecideProtocolRetry().FormattedError = %q, want %q", got.FormattedError, tt.wantFormattedError)
			}
			if (got.NewSidecar != nil) != tt.wantNewSidecar {
				t.Fatalf("DecideProtocolRetry().NewSidecar nil = %v, want populated %v", got.NewSidecar == nil, tt.wantNewSidecar)
			}
			if got.NewSidecar == nil {
				return
			}
			if got.NewSidecar.Role != RoleResearcher {
				t.Errorf("NewSidecar.Role = %q, want %q", got.NewSidecar.Role, RoleResearcher)
			}
			if got.NewSidecar.ActiveRun != tt.wantSidecarActiveRun {
				t.Errorf("NewSidecar.ActiveRun = %d, want %d", got.NewSidecar.ActiveRun, tt.wantSidecarActiveRun)
			}
			if got.NewSidecar.Consecutive != tt.wantConsecutive {
				t.Errorf("NewSidecar.Consecutive = %d, want %d", got.NewSidecar.Consecutive, tt.wantConsecutive)
			}
			if got.NewSidecar.LastViolation != JoinProtocolViolations(tt.violations) {
				t.Errorf("NewSidecar.LastViolation = %q, want %q", got.NewSidecar.LastViolation, JoinProtocolViolations(tt.violations))
			}
			if got.NewSidecar.UpdatedAt.IsZero() {
				t.Error("NewSidecar.UpdatedAt is zero")
			}
		})
	}
}

func TestProtocolRetrySidecarExcludedFromArtifactSelection(t *testing.T) {
	if !IsArtifactExcluded(ProtocolRetrySidecarFile) {
		t.Fatalf("IsArtifactExcluded(%q) = false, want true", ProtocolRetrySidecarFile)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProtocolRetrySidecarFile), []byte("# not an artifact\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "research.md"), []byte("# research\n"), 0o644); err != nil {
		t.Fatalf("write markdown: %v", err)
	}
	if got := newestPhaseMarkdownArtifact(dir); !strings.HasSuffix(got, "research.md") {
		t.Fatalf("newestPhaseMarkdownArtifact() = %q, want research.md", got)
	}
}
