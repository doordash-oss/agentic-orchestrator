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
	"strings"
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

func TestResumeSidecarParentBytesRemainCompatibleAndChildRoundTrips(t *testing.T) {
	parentDir := t.TempDir()
	at := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	parent := ResumeRecord{
		PhaseKey:              "phase-1-impl",
		RunNumber:             1,
		OrchestratorSessionID: "parent-session",
		CreatedAt:             at,
		UpdatedAt:             at,
	}
	if err := WriteResumeRecord(parentDir, parent); err != nil {
		t.Fatalf("WriteResumeRecord(parent) error = %v", err)
	}
	parentBytes, err := os.ReadFile(filepath.Join(parentDir, ResumeSidecarFile))
	if err != nil {
		t.Fatalf("ReadFile(parent) error = %v", err)
	}
	wantParent := "" +
		"phase_key: phase-1-impl\n" +
		"run_number: 1\n" +
		"orchestrator_session_id: parent-session\n" +
		"created_at: 2026-07-29T10:00:00Z\n" +
		"updated_at: 2026-07-29T10:00:00Z\n" +
		"resumed: false\n" +
		"resume_count: 0\n" +
		"fresh_fallback_count: 0\n" +
		"completed: false\n" +
		"rejected: false\n"
	if string(parentBytes) != wantParent {
		t.Errorf("parent resume.yaml changed:\n got:\n%s\nwant:\n%s", parentBytes, wantParent)
	}

	childDir := t.TempDir()
	child := parent
	child.OrchestratorSessionID = "child-session"
	coordinator := NewChildResumeCoordinator(childDir, "craft")
	requireResumeMutation(t, coordinator.Initialize(child))
	got := coordinator.Snapshot()
	if got == nil || got.ChildKey != "craft" {
		t.Fatalf("child Snapshot() = %#v, want child_key craft", got)
	}
	childBytes, err := os.ReadFile(filepath.Join(childDir, ResumeSidecarFile))
	if err != nil {
		t.Fatalf("ReadFile(child) error = %v", err)
	}
	if !strings.Contains(string(childBytes), "child_key: craft\n") {
		t.Errorf("child resume.yaml =\n%s\nwant child_key", childBytes)
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
			requireResumeMutation(t, coordinator.Initialize(resumeTestRecord()))
			if test.successResume {
				requireResumeMutation(t, coordinator.MarkResumed(time.Now()))
			}
			for i := 0; i < test.fallbacks; i++ {
				requireResumeMutation(t, coordinator.MarkFreshFallback("model_changed", time.Now()))
				next := resumeTestRecord()
				next.ProviderSessionID = fmt.Sprintf("provider-session-%d", i+2)
				next.OrchestratorSessionID = fmt.Sprintf("orchestrator-session-%d", i+2)
				requireResumeMutation(t, coordinator.Initialize(next))
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

func TestResumeCoordinatorMutationsReturnPersistenceErrors(t *testing.T) {
	blockedPath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedPath, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	coordinator := NewResumeCoordinator(blockedPath)
	now := time.Now()
	tests := []struct {
		name   string
		mutate func() error
	}{
		{name: "initialize", mutate: func() error { return coordinator.Initialize(resumeTestRecord()) }},
		{name: "capture provider", mutate: func() error {
			return coordinator.CaptureProviderSnapshot("thread", "codex", "model-a")
		}},
		{name: "mark resumed", mutate: func() error { return coordinator.MarkResumed(now) }},
		{name: "mark completed", mutate: func() error { return coordinator.MarkCompleted(now) }},
		{name: "mark rejected", mutate: func() error { return coordinator.MarkRejected("expired", now) }},
		{name: "mark fresh fallback", mutate: func() error {
			return coordinator.MarkFreshFallback("expired", now)
		}},
		{name: "clear pending", mutate: func() error { return coordinator.ClearPending(now) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.mutate(); err == nil {
				t.Fatalf("%s mutation error = nil, want persistence failure", test.name)
			}
		})
	}
}

func TestAutoResumeEngineOwnsAccountingRejectionAndFreshFallback(t *testing.T) {
	initial := newUtilityTestSession()
	fresh := newUtilityTestSession()
	accounted := 0
	resumes := 0
	fallbacks := 0
	result, err := (AutoResumeEngine{}).Run(
		AutoResumeProcess{Session: initial, Status: agentStatusFailed, ID: "initial"},
		AutoResumeCallbacks{
			Failed:         func(process AutoResumeProcess) bool { return process.Status == agentStatusFailed },
			SupportsResume: func(AutoResumeProcess) bool { return true },
			HasCompleted:   func(AutoResumeProcess) bool { return false },
			ResumeID:       func(AutoResumeProcess) string { return "provider-thread" },
			WaitBackoff:    func(AutoResumeProcess, time.Duration) bool { return true },
			Account: func(AutoResumeProcess) error {
				accounted++
				return nil
			},
			Resume: func(AutoResumeProcess, string, int) (AutoResumeAttempt, error) {
				resumes++
				return AutoResumeAttempt{Rejected: true, Reason: "expired"}, nil
			},
			FreshFallback: func(AutoResumeProcess, string, int) (AutoResumeAttempt, error) {
				fallbacks++
				return AutoResumeAttempt{
					Process: AutoResumeProcess{Session: fresh, Status: agentStatusSuccess, ID: "fresh"},
				}, nil
			},
			Interrupted: func(AutoResumeProcess) bool { return false },
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Process.ID != "fresh" || result.Process.Status != agentStatusSuccess {
		t.Fatalf("Run() result = %#v, want fresh successful process", result)
	}
	if accounted != 1 || resumes != 1 || fallbacks != 1 {
		t.Fatalf("lifecycle counts = account:%d resume:%d fresh:%d, want 1/1/1", accounted, resumes, fallbacks)
	}
}

// TestAutoResumeEngineRejectThenFreshCycleTripsAbsoluteCap proves a persistent
// reject-then-fresh cycle is bounded: the rejected resume and the fresh
// fallback are both charged against the absolute attempt cap, so the engine
// stops after 10 dispatches instead of retrying forever.
func TestAutoResumeEngineRejectThenFreshCycleTripsAbsoluteCap(t *testing.T) {
	resumes := 0
	fallbacks := 0
	result, err := (AutoResumeEngine{}).Run(
		AutoResumeProcess{Session: newUtilityTestSession(), Status: agentStatusFailed, ID: "initial"},
		AutoResumeCallbacks{
			Failed:         func(process AutoResumeProcess) bool { return process.Status == agentStatusFailed },
			SupportsResume: func(AutoResumeProcess) bool { return true },
			HasCompleted:   func(AutoResumeProcess) bool { return false },
			ResumeID:       func(AutoResumeProcess) string { return "provider-thread" },
			WaitBackoff:    func(AutoResumeProcess, time.Duration) bool { return true },
			Resume: func(AutoResumeProcess, string, int) (AutoResumeAttempt, error) {
				resumes++
				return AutoResumeAttempt{Rejected: true, Reason: "expired"}, nil
			},
			FreshFallback: func(_ AutoResumeProcess, _ string, ordinal int) (AutoResumeAttempt, error) {
				fallbacks++
				if fallbacks > autoResumeAbsoluteCap {
					// Safety valve for a regressed engine that never stops:
					// return a successful process so the loop terminates and
					// the assertions below fail instead of the test hanging.
					return AutoResumeAttempt{Process: AutoResumeProcess{Status: agentStatusSuccess, ID: "runaway"}}, nil
				}
				sess := newUtilityTestSession()
				// Observable progress keeps the consecutive-idle cap reset so
				// the absolute cap is the one that must trip.
				sess.msgLog.Append(llm.SDKMessage{
					Type: "assistant",
					Assistant: &llm.AssistantMessage{
						Message: llm.ConversationMsg{
							Role:    "assistant",
							Content: []llm.ContentBlock{{Type: "text", Text: "worked then died"}},
						},
					},
				})
				return AutoResumeAttempt{
					Process: AutoResumeProcess{Session: sess, Status: agentStatusFailed, ID: fmt.Sprintf("fresh-%02d", ordinal)},
				}, nil
			},
			Interrupted: func(AutoResumeProcess) bool { return false },
		},
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resumes != autoResumeAbsoluteCap/2 || fallbacks != autoResumeAbsoluteCap/2 {
		t.Fatalf("reject-then-fresh cycle counts = resume:%d fresh:%d, want %d/%d (both dispatches charged)",
			resumes, fallbacks, autoResumeAbsoluteCap/2, autoResumeAbsoluteCap/2)
	}
	if result.Failure == "" || !strings.Contains(result.Failure, "absolute ceiling") {
		t.Fatalf("Run() failure = %q, want the absolute-cap stop reason", result.Failure)
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
		"required artifacts, run the artifact preflight, and emit the structured root outcome; otherwise update " +
		"progress and continue from where you left off."
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
			name:    "knowledge base",
			current: feature.Feature{CurrentPhase: feature.PhaseKnowledgeBase},
			want:    "knowledgebase",
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

func TestEvaluateResumeEligibilityForChild(t *testing.T) {
	registry := resumeTestRegistry()
	baseFeature := *resumeTestFeature()
	baseFeature.CurrentIteration = 4
	baseRecord := resumeTestRecord()
	baseRecord.ChildKey = "craft"
	baseRecord.Iteration = 4

	tests := []struct {
		name         string
		childKey     string
		mutateRecord func(*ResumeRecord)
		mutate       func(*feature.Feature)
		model        string
		wantEligible bool
		wantReason   ResumeEligibilityReason
	}{
		{name: "matching child and parent context", childKey: "craft", model: "codex:model-a", wantEligible: true},
		{name: "wrong child", childKey: "correctness", model: "codex:model-a", wantReason: ResumeReasonPositionChanged},
		{name: "absent dispatched child", model: "codex:model-a", wantReason: ResumeReasonPositionChanged},
		{
			name:     "absent record child",
			childKey: "craft",
			model:    "codex:model-a",
			mutateRecord: func(record *ResumeRecord) {
				record.ChildKey = ""
			},
			wantReason: ResumeReasonPositionChanged,
		},
		{
			name:     "stale parent iteration",
			childKey: "craft",
			model:    "codex:model-a",
			mutate: func(current *feature.Feature) {
				current.CurrentIteration++
			},
			wantReason: ResumeReasonPositionChanged,
		},
		{
			name:     "sealed run",
			childKey: "craft",
			model:    "codex:model-a",
			mutate: func(current *feature.Feature) {
				current.ActiveRun++
			},
			wantReason: ResumeReasonRunSealed,
		},
		{name: "model changed", childKey: "craft", model: "codex:model-b", wantReason: ResumeReasonModelChanged},
		{
			name:     "completed",
			childKey: "craft",
			model:    "codex:model-a",
			mutateRecord: func(record *ResumeRecord) {
				record.Completed = true
			},
			wantReason: ResumeReasonRecordCompleted,
		},
		{
			name:     "rejected",
			childKey: "craft",
			model:    "codex:model-a",
			mutateRecord: func(record *ResumeRecord) {
				record.Rejected = true
			},
			wantReason: ResumeReasonSessionRejected,
		},
		{
			name:     "unsupported provider",
			childKey: "craft",
			model:    "legacy:model-a",
			mutateRecord: func(record *ResumeRecord) {
				record.Provider = "legacy"
			},
			wantReason: ResumeReasonUnsupported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := baseFeature
			record := baseRecord
			if test.mutate != nil {
				test.mutate(&current)
			}
			if test.mutateRecord != nil {
				test.mutateRecord(&record)
			}
			got := EvaluateResumeEligibility(&current, &record, test.model, registry, test.childKey)
			if got.Eligible != test.wantEligible || got.Reason != test.wantReason {
				t.Errorf("EvaluateResumeEligibility() = %#v, want eligible=%v reason=%q",
					got, test.wantEligible, test.wantReason)
			}
		})
	}
}

func TestEvaluateChildResumeEligibilityUsesExplicitCompositeParentContext(t *testing.T) {
	current := resumeTestFeature()
	current.CurrentPhase = feature.PhaseFinalReview
	current.CurrentIteration = 0
	record := resumeTestRecord()
	record.PhaseKey = "final-review"
	record.Iteration = 3
	record.ChildKey = "correctness"
	parent := ResumeParentContext{PhaseKey: "final-review", Iteration: 3}

	got := EvaluateChildResumeEligibility(
		current, &record, "codex:model-a", resumeTestRegistry(), parent, "correctness",
	)
	if !got.Eligible {
		t.Fatalf("EvaluateChildResumeEligibility() = %#v, want eligible", got)
	}

	dir := t.TempDir()
	if err := WriteResumeRecord(dir, record); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	coordinator := NewChildResumeCoordinator(dir, "correctness", parent)
	if got := coordinator.Eligibility(current, "codex:model-a", resumeTestRegistry()); !got.Eligible {
		t.Errorf("coordinator.Eligibility() = %#v, want explicit-context eligibility", got)
	}
}

func TestEvaluateCompositeResumeEligibilityUsesIncompleteStrictMatch(t *testing.T) {
	t.Parallel()

	unitDir := t.TempDir()
	current := resumeTestFeature()
	current.CurrentPhase = feature.PhaseFinalReview
	current.ReviewIteration = 3
	parent := ResumeParentContext{PhaseKey: feature.PhaseFinalReview.DirName(), Iteration: 3}

	completed := resumeTestRecord()
	completed.PhaseKey = parent.PhaseKey
	completed.Iteration = parent.Iteration
	completed.ChildKey = "craft"
	completed.Completed = true
	if err := WriteResumeRecord(filepath.Join(unitDir, "craft"), completed); err != nil {
		t.Fatalf("WriteResumeRecord(completed) error = %v", err)
	}
	resumable := completed
	resumable.ChildKey = "qa"
	resumable.Completed = false
	if err := WriteResumeRecord(filepath.Join(unitDir, "qa"), resumable); err != nil {
		t.Fatalf("WriteResumeRecord(resumable) error = %v", err)
	}
	stale := resumable
	stale.ChildKey = "design"
	stale.Iteration--
	if err := WriteResumeRecord(filepath.Join(unitDir, "design"), stale); err != nil {
		t.Fatalf("WriteResumeRecord(stale) error = %v", err)
	}

	got := EvaluateCompositeResumeEligibility(
		unitDir,
		current,
		resumeTestRegistry(),
		parent,
		func(string) string { return "codex:model-a" },
	)
	if !got.Eligible {
		t.Fatalf("EvaluateCompositeResumeEligibility() = %#v, want eligible qa child", got)
	}

	resumable.ResolvedModel = "model-b"
	if err := WriteResumeRecord(filepath.Join(unitDir, "qa"), resumable); err != nil {
		t.Fatalf("WriteResumeRecord(model mismatch) error = %v", err)
	}
	if err := os.Remove(filepath.Join(unitDir, "design", ResumeSidecarFile)); err != nil {
		t.Fatalf("Remove(stale sidecar) error = %v", err)
	}
	got = EvaluateCompositeResumeEligibility(
		unitDir,
		current,
		resumeTestRegistry(),
		parent,
		func(string) string { return "codex:model-a" },
	)
	if got.Eligible || got.Reason != ResumeReasonModelChanged {
		t.Fatalf("EvaluateCompositeResumeEligibility() = %#v, want model_changed", got)
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

func TestResumeClaimReleaseFailsClosedWhenIntentCannotBeCleared(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	// The fail-closed release intentionally retains the process-global claim, so
	// use a unique feature ID to avoid colliding with other claim tests.
	current.ID = "feature-claim-fail-closed"
	if err := WriteResumeRecord(dir, resumeTestRecord()); err != nil {
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

	// Make the sidecar directory unwritable so clearing the durable intent fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("os.Chmod(read-only) error = %v", err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Logf("os.Chmod(restore) error = %v", err)
		}
	}()

	if err := claim.Release(claimedAt.Add(time.Minute)); err == nil {
		t.Fatal("Release() error = nil, want error when durable intent cannot be cleared")
	}

	// The in-memory claim must stay held: no second claimant is admitted.
	if _, _, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, claimedAt.Add(2*time.Minute)); !errors.Is(err, ErrResumeAlreadyClaimed) {
		t.Fatalf("Claim() after failed Release error = %v, want ErrResumeAlreadyClaimed", err)
	}

	// Repeat Release returns the stored error and still does not free the claim.
	if err := claim.Release(claimedAt.Add(3 * time.Minute)); err == nil {
		t.Fatal("second Release() error = nil, want stored clearing error")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("os.Chmod(restore) error = %v", err)
	}
	snap := coordinator.Snapshot()
	if snap == nil || !snap.PendingResume {
		t.Fatalf("Snapshot().PendingResume after failed Release = %#v, want still true", snap)
	}
}

func TestResumeClaimRejectPersistsRejectionBeforeReleasing(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	current.ID = "feature-claim-reject"
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

	rejectedAt := claimedAt.Add(time.Minute)
	if err := claim.Reject("provider session expired", rejectedAt); err != nil {
		t.Fatalf("Reject() error = %v", err)
	}

	snap := coordinator.Snapshot()
	if snap == nil || !snap.Rejected || snap.PendingResume {
		t.Fatalf("Snapshot() after Reject = %#v, want rejected with pending intent cleared", snap)
	}
	if snap.RejectionReason != "provider session expired" {
		t.Errorf("Snapshot().RejectionReason = %q, want %q", snap.RejectionReason, "provider session expired")
	}
	assertResumeIdentity(t, snap, record)

	// The in-memory claim was freed only after the rejection was durable, so a
	// new claimant sees the rejected record rather than an eligible pending one.
	second, secondEligibility, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, rejectedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second != nil || secondEligibility.Eligible {
		t.Errorf("second Claim() = (%#v, %#v), want no claim and ineligible verdict after rejection", second, secondEligibility)
	}
}

func TestResumeClaimRejectFailsClosedWhenRejectionCannotBePersisted(t *testing.T) {
	dir := t.TempDir()
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	// The fail-closed reject intentionally retains the process-global claim, so
	// use a unique feature ID to avoid colliding with other claim tests.
	current.ID = "feature-claim-reject-fail-closed"
	if err := WriteResumeRecord(dir, resumeTestRecord()); err != nil {
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

	// Make the sidecar directory unwritable so persisting the rejection fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("os.Chmod(read-only) error = %v", err)
	}
	defer func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Logf("os.Chmod(restore) error = %v", err)
		}
	}()

	if err := claim.Reject("provider session expired", claimedAt.Add(time.Minute)); err == nil {
		t.Fatal("Reject() error = nil, want error when rejection cannot be persisted")
	}

	// The in-memory claim must stay held: no second claimant is admitted while
	// the rejection is unpersisted.
	if _, _, err := coordinator.Claim(current.ID, current, "codex:model-a", registry, claimedAt.Add(2*time.Minute)); !errors.Is(err, ErrResumeAlreadyClaimed) {
		t.Fatalf("Claim() after failed Reject error = %v, want ErrResumeAlreadyClaimed", err)
	}

	// Repeat Reject returns the stored error and still does not free the claim.
	if err := claim.Reject("provider session expired", claimedAt.Add(3*time.Minute)); err == nil {
		t.Fatal("second Reject() error = nil, want stored persistence error")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("os.Chmod(restore) error = %v", err)
	}
	snap := coordinator.Snapshot()
	if snap == nil || snap.Rejected || !snap.PendingResume {
		t.Fatalf("Snapshot() after failed Reject = %#v, want unrejected with pending intent still set", snap)
	}
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

func TestResumeClaimScopesParallelResumersByChild(t *testing.T) {
	registry := resumeTestRegistry()
	current := resumeTestFeature()
	const childCount = 5
	coordinators := make([]*ResumeCoordinator, childCount)
	for i := range childCount {
		childKey := fmt.Sprintf("axis-%d", i)
		dir := t.TempDir()
		record := resumeTestRecord()
		record.ChildKey = childKey
		if err := WriteResumeRecord(dir, record); err != nil {
			t.Fatalf("WriteResumeRecord(%s) error = %v", childKey, err)
		}
		coordinators[i] = NewChildResumeCoordinator(dir, childKey)
	}

	type result struct {
		child int
		claim *ResumeClaim
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, childCount+1)
	var wg sync.WaitGroup
	for i := range childCount {
		wg.Add(1)
		go func(child int) {
			defer wg.Done()
			<-start
			claim, _, err := coordinators[child].Claim(
				current.ID, current, "codex:model-a", registry, time.Now(),
			)
			results <- result{child: child, claim: claim, err: err}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		claim, _, err := coordinators[2].Claim(
			current.ID, current, "codex:model-a", registry, time.Now(),
		)
		results <- result{child: 2, claim: claim, err: err}
	}()
	close(start)
	wg.Wait()
	close(results)

	winners := make(map[int]*ResumeClaim, childCount)
	duplicateConflicts := 0
	for got := range results {
		switch {
		case got.err == nil && got.claim != nil:
			if previous := winners[got.child]; previous != nil {
				t.Errorf("child %d produced duplicate winning claims", got.child)
			}
			winners[got.child] = got.claim
		case got.child == 2 && errors.Is(got.err, ErrResumeAlreadyClaimed):
			duplicateConflicts++
		default:
			t.Errorf("child %d Claim() = (%#v, %v), want winner or duplicate conflict",
				got.child, got.claim, got.err)
		}
	}
	if len(winners) != childCount {
		t.Errorf("distinct child winners = %d, want %d", len(winners), childCount)
	}
	if duplicateConflicts != 1 {
		t.Errorf("duplicate child conflicts = %d, want 1", duplicateConflicts)
	}
	for child, claim := range winners {
		if err := claim.Release(time.Now()); err != nil {
			t.Errorf("child %d Release() error = %v", child, err)
		}
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
		got.ChildKey != want.ChildKey ||
		got.Iteration != want.Iteration ||
		got.RunNumber != want.RunNumber ||
		got.OrchestratorSessionID != want.OrchestratorSessionID ||
		!got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("resume identity = %#v, want %#v", got, want)
	}
}

func requireResumeMutation(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("resume mutation error = %v", err)
	}
}
