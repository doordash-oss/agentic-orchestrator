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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestResumeSidecarRoundTripAndLifecycleMutations(t *testing.T) {
	dir := t.TempDir()
	createdAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	record := ResumeRecord{
		ProviderSessionID:     "provider-session-123",
		Provider:              "codex",
		ResolvedModel:         "gpt-5.6-codex",
		PhaseKey:              "phase-01-implement",
		Iteration:             2,
		RunNumber:             3,
		OrchestratorSessionID: "feat-1-phase-01-impl-02",
		FreshFallbackCount:    2,
		FreshFallbackReason:   "model_changed",
		CreatedAt:             createdAt,
		UpdatedAt:             createdAt,
	}

	if err := WriteResumeRecord(dir, record); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	got, err := ReadResumeRecord(dir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if got == nil {
		t.Fatal("ReadResumeRecord() = nil, want record")
	}
	assertResumeIdentity(t, got, record)

	resumedAt := createdAt.Add(time.Minute)
	got.MarkResumed(resumedAt)
	completedAt := resumedAt.Add(time.Minute)
	got.MarkCompleted(completedAt)
	rejectedAt := completedAt.Add(time.Minute)
	got.MarkRejected("provider session expired", rejectedAt)
	if err := WriteResumeRecord(dir, *got); err != nil {
		t.Fatalf("WriteResumeRecord(mutated) error = %v", err)
	}

	mutated, err := ReadResumeRecord(dir)
	if err != nil {
		t.Fatalf("ReadResumeRecord(mutated) error = %v", err)
	}
	if mutated == nil {
		t.Fatal("ReadResumeRecord(mutated) = nil, want record")
	}
	assertResumeIdentity(t, mutated, record)
	if !mutated.Resumed || mutated.ResumeCount != 1 {
		t.Errorf("resume markers = (%v, %d), want (true, 1)", mutated.Resumed, mutated.ResumeCount)
	}
	if !mutated.Completed || mutated.CompletedAt == nil || !mutated.CompletedAt.Equal(completedAt) {
		t.Errorf("completion stamp = (%v, %v), want (%v, %v)", mutated.Completed, mutated.CompletedAt, true, completedAt)
	}
	if !mutated.Rejected || mutated.RejectionReason != "provider session expired" ||
		mutated.RejectedAt == nil || !mutated.RejectedAt.Equal(rejectedAt) {
		t.Errorf("rejection stamp = (%v, %q, %v), want (true, %q, %v)",
			mutated.Rejected, mutated.RejectionReason, mutated.RejectedAt, "provider session expired", rejectedAt)
	}
	if !mutated.UpdatedAt.Equal(rejectedAt) {
		t.Errorf("UpdatedAt = %v, want %v", mutated.UpdatedAt, rejectedAt)
	}
	if mutated.FreshFallbackCount != 2 || mutated.FreshFallbackReason != "model_changed" {
		t.Errorf("fresh fallback lineage = (%d, %q), want (2, %q)",
			mutated.FreshFallbackCount, mutated.FreshFallbackReason, "model_changed")
	}
}

func TestResumeCoordinatorInitializePreservesFreshFallbackLineage(t *testing.T) {
	tests := []struct {
		name          string
		fallbacks     int
		successResume bool
	}{
		{name: "single fallback", fallbacks: 1},
		{name: "repeated fallbacks", fallbacks: 3},
		{name: "fallback after successful resume", fallbacks: 2, successResume: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := NewResumeCoordinator(t.TempDir())
			coordinator.Initialize(resumeTestRecord())
			if test.successResume {
				coordinator.MarkResumed(time.Now())
			}
			for i := 0; i < test.fallbacks; i++ {
				coordinator.MarkFreshFallback("model_changed", time.Now())
				next := resumeTestRecord()
				next.ProviderSessionID = fmt.Sprintf("provider-session-%d", i+2)
				next.OrchestratorSessionID = fmt.Sprintf("orchestrator-session-%d", i+2)
				coordinator.Initialize(next)
			}

			got := coordinator.Snapshot()
			if got == nil {
				t.Fatal("Snapshot() = nil")
			}
			if got.FreshFallbackCount != test.fallbacks ||
				got.FreshFallbackReason != "model_changed" {
				t.Errorf("fresh fallback lineage = (%d, %q), want (%d, %q)",
					got.FreshFallbackCount, got.FreshFallbackReason, test.fallbacks, "model_changed")
			}
			if got.Resumed || got.ResumeCount != 0 {
				t.Errorf("new provider lineage resume state = (%v, %d), want (false, 0)", got.Resumed, got.ResumeCount)
			}
		})
	}
}

func TestReadResumeSidecarMissingAndMalformedIsNoRecord(t *testing.T) {
	dir := t.TempDir()

	for _, setup := range []struct {
		name string
		fn   func()
	}{
		{name: "missing", fn: func() {}},
		{name: "malformed", fn: func() {
			if err := os.WriteFile(filepath.Join(dir, ResumeSidecarFile), []byte("provider: ["), 0o644); err != nil {
				t.Fatalf("write malformed resume sidecar: %v", err)
			}
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			setup.fn()
			got, err := ReadResumeRecord(dir)
			if err != nil {
				t.Fatalf("ReadResumeRecord() error = %v, want nil", err)
			}
			if got != nil {
				t.Fatalf("ReadResumeRecord() = %#v, want nil", got)
			}
		})
	}
}

func TestWriteResumeSidecarAtomicallyReplacesAndCleansTempFile(t *testing.T) {
	dir := t.TempDir()
	first := ResumeRecord{ProviderSessionID: "first", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	second := first
	second.ProviderSessionID = "second"

	if err := WriteResumeRecord(dir, first); err != nil {
		t.Fatalf("WriteResumeRecord(first) error = %v", err)
	}
	if err := WriteResumeRecord(dir, second); err != nil {
		t.Fatalf("WriteResumeRecord(second) error = %v", err)
	}
	got, err := ReadResumeRecord(dir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if got == nil || got.ProviderSessionID != "second" {
		t.Fatalf("ReadResumeRecord() = %#v, want replacement record", got)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".resume-*.yaml.tmp"))
	if err != nil {
		t.Fatalf("Glob(resume temp files) error = %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("resume temp files = %v, want none", matches)
	}
}

func TestCrashResumePromptTemplateMatchesLegacyContent(t *testing.T) {
	want := "Your previous process terminated unexpectedly mid-turn; this session resumes that conversation. " +
		"Reassess the repository and your artifacts: if the iteration's work is already complete, write any missing " +
		"required artifacts and the completion marker per your instructions; otherwise update progress and continue " +
		"from where you left off."
	if got := renderResumePrompt(implementResumeContext); got != want {
		t.Errorf("renderResumePrompt() = %q, want %q", got, want)
	}
}

func TestResumePhaseKeyDescribesCurrentResumableKind(t *testing.T) {
	tests := []struct {
		name    string
		current feature.Feature
		want    string
	}{
		{
			name:    "inquire",
			current: feature.Feature{CurrentPhase: feature.PhaseInquire},
			want:    "inquire",
		},
		{
			name:    "research",
			current: feature.Feature{CurrentPhase: feature.PhaseResearch},
			want:    "research",
		},
		{
			name:    "design",
			current: feature.Feature{CurrentPhase: feature.PhaseDesign},
			want:    "design",
		},
		{
			name:    "roadmap plan",
			current: feature.Feature{CurrentPhase: feature.PhasePlan},
			want:    "roadmap-plan",
		},
		{
			name: "roadmap phase plan",
			current: feature.Feature{
				CurrentPhase:        feature.PhasePlan,
				CurrentRoadmapPhase: 3,
				ActiveTimingKey:     "phase-3-plan",
			},
			want: "phase-3-plan",
		},
		{
			name: "roadmap phase implementation uses active timing identity",
			current: feature.Feature{
				CurrentPhase:        feature.PhaseImplement,
				CurrentRoadmapPhase: 3,
				ActiveTimingKey:     "phase-3-impl",
			},
			want: "phase-3-impl",
		},
		{
			name:    "unscoped implementation",
			current: feature.Feature{CurrentPhase: feature.PhaseImplement},
			want:    "implement",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResumePhaseKey(&test.current); got != test.want {
				t.Errorf("ResumePhaseKey() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResumeUnitDirResolvesCurrentUnit(t *testing.T) {
	stateDir := t.TempDir()
	baseFeature := feature.Feature{ID: "feature-1", ActiveRun: 2}
	runDir := filepath.Join(stateDir, "feature-1", "runs", "run-002")

	roadmapDir := filepath.Join(runDir, "roadmap")
	if err := WritePlanAttemptMeta(roadmapDir, PlanAttemptMeta{
		Attempt:     1,
		AgentStatus: agentStatusSuccess,
	}); err != nil {
		t.Fatalf("WritePlanAttemptMeta(roadmap) error = %v", err)
	}
	phasePlanDir := filepath.Join(runDir, "phase-03", "plan")
	if err := WritePlanAttemptMeta(phasePlanDir, PlanAttemptMeta{
		Attempt:     2,
		AgentStatus: agentStatusSuccess,
	}); err != nil {
		t.Fatalf("WritePlanAttemptMeta(phase plan) error = %v", err)
	}

	tests := []struct {
		name    string
		current feature.Feature
		want    string
		wantOK  bool
	}{
		{
			name:    "inquire uses its single shot artifact directory",
			current: feature.Feature{CurrentPhase: feature.PhaseInquire},
			want:    filepath.Join(runDir, "inquire"),
			wantOK:  true,
		},
		{
			name:    "research uses its single shot artifact directory",
			current: feature.Feature{CurrentPhase: feature.PhaseResearch},
			want:    filepath.Join(runDir, "research"),
			wantOK:  true,
		},
		{
			name:    "design uses its single shot artifact directory",
			current: feature.Feature{CurrentPhase: feature.PhaseDesign},
			want:    filepath.Join(runDir, "design"),
			wantOK:  true,
		},
		{
			name:    "roadmap planning uses the next completed attempt number",
			current: feature.Feature{CurrentPhase: feature.PhasePlan},
			want:    filepath.Join(roadmapDir, "attempt-02"),
			wantOK:  true,
		},
		{
			name: "roadmap phase planning uses the next completed attempt number",
			current: feature.Feature{
				CurrentPhase:        feature.PhasePlan,
				CurrentRoadmapPhase: 3,
			},
			want:   filepath.Join(phasePlanDir, "attempt-03"),
			wantOK: true,
		},
		{
			name: "implementation uses the current iteration",
			current: feature.Feature{
				CurrentPhase:        feature.PhaseImplement,
				CurrentRoadmapPhase: 3,
				CurrentIteration:    4,
			},
			want:   filepath.Join(runDir, "phase-03", "implement", "iteration-04"),
			wantOK: true,
		},
		{
			name:    "unsupported phase has no resume unit",
			current: feature.Feature{CurrentPhase: feature.PhasePublish},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := baseFeature
			current.CurrentPhase = test.current.CurrentPhase
			current.CurrentRoadmapPhase = test.current.CurrentRoadmapPhase
			current.CurrentIteration = test.current.CurrentIteration

			got, ok := ResumeUnitDir(stateDir, &current)
			if got != test.want || ok != test.wantOK {
				t.Errorf("ResumeUnitDir() = (%q, %v), want (%q, %v)", got, ok, test.want, test.wantOK)
			}
		})
	}
}

func TestEvaluateResumeEligibility(t *testing.T) {
	registry := resumeTestRegistry()
	baseFeature := feature.Feature{
		ID:                  "feature-1",
		ActiveRun:           3,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		ActiveTimingKey:     "phase-1-impl",
		CurrentRoadmapPhase: 1,
	}
	baseRecord := ResumeRecord{
		ProviderSessionID: "provider-session-123",
		Provider:          "codex",
		ResolvedModel:     "model-a",
		PhaseKey:          "phase-1-impl",
		Iteration:         2,
		RunNumber:         3,
	}

	tests := []struct {
		name          string
		mutateFeature func(*feature.Feature)
		mutateRecord  func(*ResumeRecord) *ResumeRecord
		model         string
		wantEligible  bool
		wantReason    ResumeEligibilityReason
	}{
		{
			name:         "matching prefixed model",
			model:        "codex:model-a",
			wantEligible: true,
		},
		{
			name:         "matching bare model",
			model:        "model-a",
			wantEligible: true,
		},
		{
			name: "missing record",
			mutateRecord: func(*ResumeRecord) *ResumeRecord {
				return nil
			},
			model:      "codex:model-a",
			wantReason: ResumeReasonNoRecord,
		},
		{
			name: "missing provider session identity",
			mutateRecord: func(record *ResumeRecord) *ResumeRecord {
				record.ProviderSessionID = ""
				return record
			},
			model:      "codex:model-a",
			wantReason: ResumeReasonNoRecord,
		},
		{
			name:       "model changed",
			model:      "codex:model-b",
			wantReason: ResumeReasonModelChanged,
		},
		{
			name:       "provider changed",
			model:      "claude:model-a",
			wantReason: ResumeReasonModelChanged,
		},
		{
			name: "run sealed",
			mutateFeature: func(current *feature.Feature) {
				current.ActiveRun = 4
			},
			model:      "codex:model-a",
			wantReason: ResumeReasonRunSealed,
		},
		{
			name: "phase changed",
			mutateFeature: func(current *feature.Feature) {
				current.ActiveTimingKey = "phase-2-impl"
			},
			model:      "codex:model-a",
			wantReason: ResumeReasonPositionChanged,
		},
		{
			name: "phase kind changed",
			mutateFeature: func(current *feature.Feature) {
				current.CurrentPhase = feature.PhasePlan
			},
			model:      "codex:model-a",
			wantReason: ResumeReasonPositionChanged,
		},
		{
			name: "iteration metadata does not determine eligibility",
			mutateFeature: func(current *feature.Feature) {
				current.CurrentIteration = 3
			},
			model:        "codex:model-a",
			wantEligible: true,
		},
		{
			name: "provider cannot resume",
			mutateRecord: func(record *ResumeRecord) *ResumeRecord {
				record.Provider = "legacy"
				return record
			},
			model:      "legacy:model-a",
			wantReason: ResumeReasonUnsupported,
		},
		{
			name: "completed wins over other mismatches",
			mutateFeature: func(current *feature.Feature) {
				current.ActiveRun = 4
			},
			mutateRecord: func(record *ResumeRecord) *ResumeRecord {
				record.Completed = true
				record.Rejected = true
				return record
			},
			model:      "claude:model-b",
			wantReason: ResumeReasonRecordCompleted,
		},
		{
			name: "rejected wins over model and run mismatch",
			mutateFeature: func(current *feature.Feature) {
				current.ActiveRun = 4
			},
			mutateRecord: func(record *ResumeRecord) *ResumeRecord {
				record.Rejected = true
				return record
			},
			model:      "claude:model-b",
			wantReason: ResumeReasonSessionRejected,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := baseFeature
			record := baseRecord
			recordPtr := &record
			if test.mutateFeature != nil {
				test.mutateFeature(&current)
			}
			if test.mutateRecord != nil {
				recordPtr = test.mutateRecord(recordPtr)
			}

			got := EvaluateResumeEligibility(&current, recordPtr, test.model, registry)
			if got.Eligible != test.wantEligible {
				t.Errorf("EvaluateResumeEligibility().Eligible = %v, want %v", got.Eligible, test.wantEligible)
			}
			if got.Reason != test.wantReason {
				t.Errorf("EvaluateResumeEligibility().Reason = %q, want %q", got.Reason, test.wantReason)
			}
			if !got.Eligible && got.Message == "" {
				t.Error("EvaluateResumeEligibility().Message = empty, want stable human message")
			}
		})
	}
}

func TestResumeEligibilityIgnoresNonIdentityLaunchChanges(t *testing.T) {
	registry := resumeTestRegistry()
	current := &feature.Feature{
		ID:                  "feature-1",
		ActiveRun:           1,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    1,
		ActiveTimingKey:     "phase-1-impl",
		CurrentRoadmapPhase: 1,
	}
	record := &ResumeRecord{
		ProviderSessionID: "provider-session-123",
		Provider:          "codex",
		ResolvedModel:     "model-a",
		PhaseKey:          "phase-1-impl",
		Iteration:         1,
		RunNumber:         1,
	}

	// Effort, sandbox, and prompt-template configuration are intentionally
	// absent from the evaluator's identity inputs and therefore cannot block.
	got := EvaluateResumeEligibility(current, record, "codex:model-a", registry)
	if !got.Eligible {
		t.Errorf("EvaluateResumeEligibility() = %#v, want eligible after non-identity launch changes", got)
	}
}

func TestResumeClaimPendingMarkerLifecyclePreservesIdentity(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	record := resumeTestRecord()
	if err := WriteResumeRecord(dir, record); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	coordinator := NewResumeCoordinator(dir)
	claimedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)

	claim, eligibility, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, claimedAt)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if !eligibility.Eligible || claim == nil {
		t.Fatalf("Claim() = (%#v, %#v), want claim and eligible verdict", claim, eligibility)
	}
	pending := coordinator.Snapshot()
	if pending == nil || !pending.PendingResume {
		t.Fatalf("Snapshot().PendingResume = %#v, want true", pending)
	}
	assertResumeIdentity(t, pending, record)

	releasedAt := claimedAt.Add(time.Minute)
	if err := claim.Release(releasedAt); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	cleared := coordinator.Snapshot()
	if cleared == nil || cleared.PendingResume {
		t.Fatalf("Snapshot().PendingResume after Release = %#v, want false", cleared)
	}
	assertResumeIdentity(t, cleared, record)
}

func TestResumeClaimAllowsExactlyOneConcurrentResumer(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	if err := WriteResumeRecord(dir, resumeTestRecord()); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	coordinator := NewResumeCoordinator(dir)

	const contenders = 16
	type result struct {
		claim *ResumeClaim
		err   error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, _, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, time.Now())
			results <- result{claim: claim, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var winner *ResumeClaim
	conflicts := 0
	for got := range results {
		switch {
		case got.err == nil && got.claim != nil:
			if winner != nil {
				t.Error("Claim() returned more than one winning claim")
			}
			winner = got.claim
		case errors.Is(got.err, ErrResumeAlreadyClaimed):
			conflicts++
		default:
			t.Errorf("Claim() = (%#v, %v), want winner or conflict", got.claim, got.err)
		}
	}
	if winner == nil {
		t.Fatal("Claim() produced no winner")
	}
	if conflicts != contenders-1 {
		t.Errorf("Claim() conflicts = %d, want %d", conflicts, contenders-1)
	}
	if err := winner.Release(time.Now()); err != nil {
		t.Fatalf("winner.Release() error = %v", err)
	}
}

func TestResumeClaimRechecksRunAfterRewindWins(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	if err := WriteResumeRecord(dir, resumeTestRecord()); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	coordinator := NewResumeCoordinator(dir)

	// The rewind has already sealed run 1 and moved the feature pointer before
	// the queued resume reaches the claim-time strict-match check.
	current.ActiveRun = 2
	claim, eligibility, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, time.Now())
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claim != nil {
		t.Fatal("Claim() returned a claim after rewind won, want nil")
	}
	if eligibility.Reason != ResumeReasonRunSealed {
		t.Errorf("Claim().Reason = %q, want %q", eligibility.Reason, ResumeReasonRunSealed)
	}
	if got := coordinator.Snapshot(); got == nil || got.PendingResume {
		t.Errorf("Snapshot().PendingResume = %#v, want false", got)
	}
}

func resumeTestRegistry() *llm.Registry {
	registry := llm.NewRegistry()
	registry.Register(&captureProvider{name: "codex", model: "model-a", sessionResume: true})
	registry.Register(&captureProvider{name: "claude", model: "model-a", sessionResume: true})
	registry.Register(&captureProvider{name: "legacy", model: "model-a", sessionResume: false})
	registry.Register(&captureProvider{name: "codex", model: "model-b", sessionResume: true})
	registry.Register(&captureProvider{name: "claude", model: "model-b", sessionResume: true})
	return registry
}

func resumeTestFeature() *feature.Feature {
	return &feature.Feature{
		ID:                  "feature-claim",
		ActiveRun:           1,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    1,
		ActiveTimingKey:     "phase-1-impl",
		CurrentRoadmapPhase: 1,
	}
}

func resumeTestRecord() ResumeRecord {
	createdAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	return ResumeRecord{
		ProviderSessionID:     "provider-session-123",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             1,
		RunNumber:             1,
		OrchestratorSessionID: "feature-claim-phase-01-impl-01",
		CreatedAt:             createdAt,
		UpdatedAt:             createdAt,
	}
}

func assertResumeIdentity(t *testing.T, got *ResumeRecord, want ResumeRecord) {
	t.Helper()
	if got.ProviderSessionID != want.ProviderSessionID ||
		got.Provider != want.Provider ||
		got.ResolvedModel != want.ResolvedModel ||
		got.PhaseKey != want.PhaseKey ||
		got.Iteration != want.Iteration ||
		got.RunNumber != want.RunNumber ||
		got.OrchestratorSessionID != want.OrchestratorSessionID ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("resume identity = %#v, want %#v", got, want)
	}
}
