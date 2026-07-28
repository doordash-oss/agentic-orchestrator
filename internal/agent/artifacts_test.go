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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestArtifactManagerCreateDir(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	iterDir, err := am.CreateIterationDir(1)
	if err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := os.Stat(iterDir); err != nil {
		t.Fatalf("expected iteration dir to exist: %v", err)
	}
}

func TestArtifactManagerWriteFiles(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	iterDir, _ := am.CreateIterationDir(1)

	if err := am.WriteResponse(iterDir, "test response"); err != nil {
		t.Fatalf("write response: %v", err)
	}
	content, _ := os.ReadFile(filepath.Join(iterDir, "response.txt"))
	if string(content) != "test response" {
		t.Errorf("response content = %q", content)
	}

	meta := IterationMeta{
		Iteration:   1,
		StartedAt:   time.Now(),
		Duration:    5 * time.Second,
		AgentStatus: agentStatusSuccess,
	}
	if err := am.WriteMeta(iterDir, meta); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(iterDir, "meta.yaml")); err != nil {
		t.Fatalf("expected meta file: %v", err)
	}
}

func TestArtifactManagerWriteSummary(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)
	summaryPath := filepath.Join(dir, "summary.log")

	meta1 := IterationMeta{Iteration: 1, AgentStatus: "RETRY", MadeProgress: true, Duration: 3 * time.Second}
	meta2 := IterationMeta{
		Iteration:    2,
		AgentStatus:  agentStatusSuccess,
		MadeProgress: true,
		Duration:     5 * time.Second,
		Context: &ContextMeta{
			ThresholdPct:     80,
			FinalPct:         64,
			TotalTokens:      171_278,
			WindowTokens:     258_400,
			HandoffTriggered: true,
		},
	}

	am.WriteSummary(summaryPath, meta1)
	am.WriteSummary(summaryPath, meta2)

	content, _ := os.ReadFile(summaryPath)
	lines := string(content)
	if !containsStr(lines, "iteration=1") || !containsStr(lines, "iteration=2") {
		t.Errorf("expected both iterations in summary, got: %s", lines)
	}
	if !containsStr(lines, "context=64%/80% tokens=171278/258400 handoff=true") {
		t.Errorf("expected context telemetry in summary, got: %s", lines)
	}
}

func TestWriteReviewFiles(t *testing.T) {
	dir := t.TempDir()

	reviewPrompt := "Review the implementation against the plan"
	reviewFeedback := "LGTM, approved"

	_ = os.WriteFile(filepath.Join(dir, "review-prompt.md"), []byte(reviewPrompt), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "review-feedback.md"), []byte(reviewFeedback), 0o644)

	promptData, err := os.ReadFile(filepath.Join(dir, "review-prompt.md"))
	if err != nil {
		t.Fatalf("reading review-prompt.md: %v", err)
	}
	if string(promptData) != reviewPrompt {
		t.Errorf("review prompt = %q, want %q", promptData, reviewPrompt)
	}

	feedbackData, err := os.ReadFile(filepath.Join(dir, "review-feedback.md"))
	if err != nil {
		t.Fatalf("reading review-feedback.md: %v", err)
	}
	if string(feedbackData) != reviewFeedback {
		t.Errorf("review feedback = %q, want %q", feedbackData, reviewFeedback)
	}
}

func TestWriteDebugPrompts(t *testing.T) {
	dir := t.TempDir()
	WriteDebugPrompts(dir, "test system prompt", "test user prompt")

	sysData, err := os.ReadFile(filepath.Join(dir, "system-prompt.md"))
	if err != nil {
		t.Fatalf("reading system-prompt.md: %v", err)
	}
	if string(sysData) != "test system prompt" {
		t.Errorf("system prompt = %q, want %q", sysData, "test system prompt")
	}

	userData, err := os.ReadFile(filepath.Join(dir, "user-prompt.md"))
	if err != nil {
		t.Fatalf("reading user-prompt.md: %v", err)
	}
	if string(userData) != "test user prompt" {
		t.Errorf("user prompt = %q, want %q", userData, "test user prompt")
	}
}

func TestArtifactManagerLatestIteration(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	if got := am.LatestIteration(); got != 0 {
		t.Errorf("empty dir: expected 0, got %d", got)
	}

	// Directories without meta.yaml are incomplete and should be ignored
	am.CreateIterationDir(1)
	am.CreateIterationDir(3)
	am.CreateIterationDir(2)

	if got := am.LatestIteration(); got != 0 {
		t.Errorf("dirs without meta: expected 0, got %d", got)
	}

	// Only completed iterations (with meta.yaml) count
	iterDir1, _ := am.CreateIterationDir(1)
	am.WriteMeta(iterDir1, IterationMeta{Iteration: 1, AgentStatus: "RETRY"})
	iterDir3, _ := am.CreateIterationDir(3)
	am.WriteMeta(iterDir3, IterationMeta{Iteration: 3, AgentStatus: agentStatusSuccess})

	if got := am.LatestIteration(); got != 3 {
		t.Errorf("expected 3, got %d", got)
	}

	// Incomplete iteration-02 (no meta.yaml) between completed ones is skipped
	if got := am.LatestIteration(); got != 3 {
		t.Errorf("expected 3 with gap, got %d", got)
	}
}

// TestRestartIterationResolution documents the three restart scenarios the
// implement loop must handle correctly (see implement.go:162-174). The state
// of iteration-N on disk dictates where the next run picks up:
//
//   - implement unfinished (no completion receipt, no meta.yaml) → resume impl at N
//   - impl done, review unfinished (receipt present, no meta.yaml) → resume review at N
//   - both done (meta.yaml written after review) → advance to N+1
//
// The bug fixed alongside this test: if review was interrupted by app
// shutdown AND meta.yaml had already been written with ReviewStatus=FAILED,
// LatestIteration would return N and restart would advance to N+1, skipping
// the unfinished review. The fix (implement.go early-return-on-shutdown
// before WriteMeta) keeps scenario (2)'s on-disk shape intact: a valid receipt
// is present and meta.yaml is absent.
func TestRestartIterationResolution(t *testing.T) {
	type scenario struct {
		name              string
		writePhaseDone    bool
		writeMeta         bool
		reviewStatus      string
		wantLatestIter    int
		wantSkipImplement bool
	}
	cases := []scenario{
		{
			name:              "implement unfinished: no receipt, no meta → resume impl at N",
			writePhaseDone:    false,
			writeMeta:         false,
			wantLatestIter:    0, // treat as N-1; only iter-1 exists on disk and is incomplete
			wantSkipImplement: false,
		},
		{
			name:              "impl done, review unfinished: receipt present, no meta → skip impl, resume review at N",
			writePhaseDone:    true,
			writeMeta:         false,
			wantLatestIter:    0,
			wantSkipImplement: true,
		},
		{
			name:              "fully done: meta.yaml written with ReviewStatus=APPROVED → advance to N+1",
			writePhaseDone:    true,
			writeMeta:         true,
			reviewStatus:      agentStatusApproved,
			wantLatestIter:    1,
			wantSkipImplement: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			am := NewArtifactManager(dir)
			it1, err := am.CreateIterationDir(1)
			if err != nil {
				t.Fatalf("create iter dir: %v", err)
			}
			if tc.writePhaseDone {
				writeTestCompletionReceiptFor(t, it1, feature.PhaseImplement, RoleImplementer)
				testutil.WriteImplementHandoffFiles(t, dir, it1, agentStatusSuccess)
			}
			if tc.writeMeta {
				if err := am.WriteMeta(it1, IterationMeta{Iteration: 1, AgentStatus: agentStatusSuccess, ReviewStatus: tc.reviewStatus}); err != nil {
					t.Fatalf("write meta: %v", err)
				}
			}

			// Mirror the restart-detection logic in implement.go:162-174.
			startIter := am.LatestIteration()
			nextIter := startIter + 1
			nextIterDir := filepath.Join(dir, fmt.Sprintf("iteration-%02d", nextIter))
			skipImplement := HasCommittedPhaseOutcome(nextIterDir, feature.PhaseImplement, RoleImplementer)

			if startIter != tc.wantLatestIter {
				t.Errorf("LatestIteration = %d, want %d", startIter, tc.wantLatestIter)
			}
			if skipImplement != tc.wantSkipImplement {
				t.Errorf("skipImplement = %v, want %v", skipImplement, tc.wantSkipImplement)
			}
		})
	}
}

// fakeShutdownSM is a tiny shutdownChecker for waitForShutdownIntent tests.
// `shutdownAt` is measured relative to `started`; IsShuttingDown() flips true
// once that much time has elapsed. Zero-value = never shuts down.
type fakeShutdownSM struct {
	started    time.Time
	shutdownAt time.Duration
}

func (f *fakeShutdownSM) IsShuttingDown() bool {
	if f.started.IsZero() {
		return false
	}
	return time.Since(f.started) >= f.shutdownAt
}

func TestWaitForShutdownIntent(t *testing.T) {
	t.Run("returns immediately when already shutting down", func(t *testing.T) {
		sm := &fakeShutdownSM{started: time.Now().Add(-time.Hour), shutdownAt: 0}
		// Force IsShuttingDown=true by making shutdownAt fire immediately.
		sm.shutdownAt = 1 * time.Nanosecond
		start := time.Now()
		if !waitForShutdownIntent(sm, 75*time.Millisecond) {
			t.Fatal("expected shutdown intent, got false")
		}
		if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
			t.Errorf("should return ~instantly; took %s", elapsed)
		}
	})

	t.Run("returns true if shutdown arrives within grace window", func(t *testing.T) {
		sm := &fakeShutdownSM{started: time.Now(), shutdownAt: 10 * time.Millisecond}
		start := time.Now()
		if !waitForShutdownIntent(sm, 75*time.Millisecond) {
			t.Fatal("expected shutdown intent after grace wait")
		}
		elapsed := time.Since(start)
		if elapsed < 10*time.Millisecond {
			t.Errorf("should have waited at least for the delayed shutdown; took %s", elapsed)
		}
		if elapsed > 75*time.Millisecond {
			t.Errorf("should not have exceeded grace; took %s", elapsed)
		}
	})

	t.Run("returns false if shutdown never arrives", func(t *testing.T) {
		sm := &fakeShutdownSM{}
		start := time.Now()
		if waitForShutdownIntent(sm, 50*time.Millisecond) {
			t.Error("expected no shutdown intent")
		}
		if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
			t.Errorf("should have waited the full grace; took %s", elapsed)
		}
	})

	t.Run("nil session manager returns false", func(t *testing.T) {
		if waitForShutdownIntent(nil, 75*time.Millisecond) {
			t.Error("expected false for nil SM")
		}
	})
}

func TestArtifactManagerReadMeta(t *testing.T) {
	dir := t.TempDir()
	am := NewArtifactManager(dir)

	iterDir, _ := am.CreateIterationDir(1)
	meta := IterationMeta{
		Iteration:    1,
		AgentStatus:  "FAILED",
		ReviewStatus: agentStatusChangesRequested,
		MadeProgress: true,
		Duration:     3 * time.Second,
		Context: &ContextMeta{
			Provider:           "codex",
			ThresholdPct:       80,
			FinalPct:           64,
			TotalTokens:        171_278,
			WindowTokens:       258_400,
			BaselineTokens:     12_000,
			HandoffTriggered:   true,
			HandoffPct:         81,
			HandoffTotalTokens: 211_000,
		},
	}
	if err := am.WriteMeta(iterDir, meta); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	got, err := am.ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if got.Iteration != 1 || got.AgentStatus != "FAILED" || got.ReviewStatus != agentStatusChangesRequested {
		t.Errorf("read meta mismatch: %+v", got)
	}
	if got.Context == nil || got.Context.Provider != "codex" || got.Context.HandoffPct != 81 {
		t.Errorf("read context meta mismatch: %+v", got.Context)
	}

	// Non-existent directory returns error
	_, err = am.ReadMeta(filepath.Join(dir, "no-such-dir"))
	if err == nil {
		t.Error("expected error for non-existent dir")
	}
}

func TestPhaseDir(t *testing.T) {
	tests := []struct {
		stateDir  string
		featureID string
		phase     int
		want      string
	}{
		{"/tmp/state", "abc123", 1, "/tmp/state/abc123/runs/run-001/phase-01"},
		{"/tmp/state", "abc123", 10, "/tmp/state/abc123/runs/run-001/phase-10"},
		{"/tmp/state", "xyz", 3, "/tmp/state/xyz/runs/run-001/phase-03"},
	}
	for _, tt := range tests {
		f := &feature.Feature{ID: tt.featureID, ActiveRun: 1}
		got := PhaseDir(tt.stateDir, f, tt.phase)
		if got != tt.want {
			t.Errorf("PhaseDir(%q, %q, %d) = %q, want %q", tt.stateDir, tt.featureID, tt.phase, got, tt.want)
		}
	}
}

func TestPhasePlanDir(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	got := PhasePlanDir("/tmp/state", f, 2)
	want := "/tmp/state/feat1/runs/run-001/phase-02/plan"
	if got != want {
		t.Errorf("PhasePlanDir = %q, want %q", got, want)
	}
}

func TestPhaseImplementDir(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	got := PhaseImplementDir("/tmp/state", f, 3)
	want := "/tmp/state/feat1/runs/run-001/phase-03/implement"
	if got != want {
		t.Errorf("PhaseImplementDir = %q, want %q", got, want)
	}
}

func TestPhaseTestingContractDir(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	got := PhaseTestingContractDir("/tmp/state", f, 2)
	want := "/tmp/state/feat1/runs/run-001/phase-02"
	if got != want {
		t.Errorf("PhaseTestingContractDir = %q, want %q", got, want)
	}
}

func TestPhaseTestingContractPath(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	got := PhaseTestingContractPath("/tmp/state", f, 2)
	want := "/tmp/state/feat1/runs/run-001/phase-02/testing-contract.yaml"
	if got != want {
		t.Errorf("PhaseTestingContractPath = %q, want %q", got, want)
	}
}

func TestCycleTestingContractPath(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	f.SetRebaseCount(2)
	got := CycleTestingContractPath("/tmp/state", f, "", feature.CycleRebase)
	want := "/tmp/state/feat1/runs/run-001/rebase-2/testing-contract.yaml"
	if got != want {
		t.Errorf("CycleTestingContractPath = %q, want %q", got, want)
	}
}

func TestCycleTestingContractPath_PerRepo(t *testing.T) {
	f := &feature.Feature{
		ID:        "feat1",
		ActiveRun: 1,
		RepoCycles: map[string]*feature.RepoCycleState{
			testRepoNameWeb: {Type: feature.CycleReviewComments, Count: 3},
		},
	}
	got := CycleTestingContractPath("/tmp/state", f, testRepoNameWeb, feature.CycleReviewComments)
	want := "/tmp/state/feat1/runs/run-001/review-comments-3/web/testing-contract.yaml"
	if got != want {
		t.Errorf("CycleTestingContractPath = %q, want %q", got, want)
	}
}

func TestLatestCycleImplementationVerificationReportPath(t *testing.T) {
	stateDir := t.TempDir()
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	f.SetRebaseCount(1)
	iterDir := filepath.Join(stateDir, "feat1", "runs", "run-001", "rebase-1", "implement", "iteration-02")
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(iterDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, "meta.yaml"), []byte("iteration: 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(meta.yaml): %v", err)
	}

	got := LatestCycleImplementationVerificationReportPath(stateDir, f, "", feature.CycleRebase)
	want := filepath.Join(iterDir, "verification-report.yaml")
	if got != want {
		t.Errorf("LatestCycleImplementationVerificationReportPath = %q, want %q", got, want)
	}
}

func TestRoadmapDir(t *testing.T) {
	f := &feature.Feature{ID: "feat1", ActiveRun: 1}
	got := RoadmapDir("/tmp/state", f)
	want := "/tmp/state/feat1/runs/run-001/roadmap"
	if got != want {
		t.Errorf("RoadmapDir = %q, want %q", got, want)
	}
}

func TestRefactorBaseDir(t *testing.T) {
	tests := []struct {
		name      string
		stateDir  string
		featureID string
		n         int
		want      string
	}{
		{
			name:      "first refactor cycle",
			stateDir:  "/tmp/state",
			featureID: "feat-abc",
			n:         1,
			want:      filepath.Join("/tmp/state", "feat-abc", "runs", "run-001", "refactor-1"),
		},
		{
			name:      "second refactor cycle",
			stateDir:  "/tmp/state",
			featureID: "feat-abc",
			n:         2,
			want:      filepath.Join("/tmp/state", "feat-abc", "runs", "run-001", "refactor-2"),
		},
		{
			name:      "different state dir and feature",
			stateDir:  "/var/data/features",
			featureID: "xyz-123",
			n:         5,
			want:      filepath.Join("/var/data/features", "xyz-123", "runs", "run-001", "refactor-5"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{ID: tt.featureID, ActiveRun: 1}
			got := RefactorBaseDir(tt.stateDir, f, tt.n)
			if got != tt.want {
				t.Errorf("RefactorBaseDir(%q, %q, %d) = %q, want %q",
					tt.stateDir, tt.featureID, tt.n, got, tt.want)
			}
		})
	}
}

// TestActiveRunDir verifies ActiveRunDir returns the zero-padded run dir path
// and falls back to run-001 for an unset ActiveRun (shadow-fields tolerance).
func TestActiveRunDir(t *testing.T) {
	tests := []struct {
		name    string
		feature *feature.Feature
		want    string
	}{
		{"active run 1", &feature.Feature{ID: "f1", ActiveRun: 1}, "/tmp/state/f1/runs/run-001"},
		{"active run 9", &feature.Feature{ID: "f1", ActiveRun: 9}, "/tmp/state/f1/runs/run-009"},
		{"active run 1000 (no truncation)", &feature.Feature{ID: "f1", ActiveRun: 1000}, "/tmp/state/f1/runs/run-1000"},
		{"zero active run defaults to 1", &feature.Feature{ID: "f1"}, "/tmp/state/f1/runs/run-001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ActiveRunDir("/tmp/state", tt.feature); got != tt.want {
				t.Errorf("ActiveRunDir = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunDir verifies the zero-padded run directory path regardless of feature.
func TestRunDir(t *testing.T) {
	if got := RunDir("/s", "x", 9); got != "/s/x/runs/run-009" {
		t.Errorf("RunDir 9 = %q", got)
	}
	if got := RunDir("/s", "x", 1000); got != "/s/x/runs/run-1000" {
		t.Errorf("RunDir 1000 = %q", got)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && findStr(s, substr)
}

func findStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
