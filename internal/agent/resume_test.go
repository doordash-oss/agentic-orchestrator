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
	"testing"
	"time"
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
