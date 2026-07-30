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

package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
)

func TestResumeFeatureClaimsEligibleRecordAndDispatchesImplementation(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	planPath := filepath.Join(stateDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1\n\nDo it.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:                  "manual-resume",
		Name:                "Manual resume",
		Slug:                "manual-resume",
		Status:              feature.StatusFailed,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Models:              config.ModelConfig{Implementation: "codex:model-a"},
		Artifacts:           map[string]string{"plan": planPath},
		Repos:               []feature.FeatureRepo{{Name: "repo", Path: stateDir}},
		RepoStates:          map[string]*feature.RepoState{"repo": {LastError: "failed"}},
		LastError:           "failed",
		FailureType:         feature.FailureInfrastructure,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	iterDir := filepath.Join(agent.ActiveImplementDir(stateDir, f), "iteration-02")
	if err := agent.WriteResumeRecord(iterDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-123",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             2,
		RunNumber:             1,
		OrchestratorSessionID: "manual-resume-phase-01-impl-02",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	phaseRunner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	o := New(Deps{Lifecycle: manager, Store: store, PhaseRunner: phaseRunner}, Hooks{})
	dispatches := 0
	o.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatches++
		return make(chan *agent.OrchestratorResult, 1), nil
	})

	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if err := o.ResumeFeature(f.ID); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("second ResumeFeature() error = %v, want ErrResumeConflict", err)
	}
	if dispatches != 1 {
		t.Fatalf("implementation dispatches = %d, want 1", dispatches)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Status != feature.StatusImplementing || got.LastError != "" || got.FailureType != "" {
		t.Errorf("feature after resume = status %s error %q failure %q, want Implementing with retry bookkeeping cleared", got.Status, got.LastError, got.FailureType)
	}
	if got.CurrentIteration != 2 || got.ActiveTimingKey != "phase-1-impl" {
		t.Errorf("feature resume position = iteration %d timing key %q, want preserved iteration 2 phase-1-impl", got.CurrentIteration, got.ActiveTimingKey)
	}
	record, err := agent.ReadResumeRecord(iterDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil || !record.PendingResume {
		t.Errorf("resume record = %#v, want durable intent handed to implementation loop", record)
	}
	agent.NewResumeCoordinator(iterDir).ClearPending(time.Now())
}

func TestResumeModelForFeatureUsesCurrentPhaseRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		phase feature.Phase
		model config.ModelConfig
		want  string
	}{
		{
			name:  "inquire",
			phase: feature.PhaseInquire,
			model: config.ModelConfig{Inquiry: "codex:inquiry"},
			want:  "codex:inquiry",
		},
		{
			name:  "inquire falls back to research",
			phase: feature.PhaseInquire,
			model: config.ModelConfig{Research: "codex:research"},
			want:  "codex:research",
		},
		{
			name:  "research",
			phase: feature.PhaseResearch,
			model: config.ModelConfig{Research: "codex:research"},
			want:  "codex:research",
		},
		{
			name:  "design",
			phase: feature.PhaseDesign,
			model: config.ModelConfig{Planning: "codex:planning"},
			want:  "codex:planning",
		},
		{
			name:  "plan",
			phase: feature.PhasePlan,
			model: config.ModelConfig{Planning: "codex:planning"},
			want:  "codex:planning",
		},
		{
			name:  "implement",
			phase: feature.PhaseImplement,
			model: config.ModelConfig{Implementation: "codex:implementation"},
			want:  "codex:implementation",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := &feature.Feature{CurrentPhase: test.phase, Models: test.model}
			if got := resumeModelForFeature(&agent.PhaseRunner{}, f); got != test.want {
				t.Errorf("resumeModelForFeature() = %q, want %q", got, test.want)
			}
		})
	}
}
