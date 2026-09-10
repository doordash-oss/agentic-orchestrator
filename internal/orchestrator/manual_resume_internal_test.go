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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

type kbResumeSessionHandle struct {
	*mocks.MockSessionView
}

func (s *kbResumeSessionHandle) SetStatus(session.SessionStatus) {}
func (s *kbResumeSessionHandle) SetLogFile(*os.File)             {}
func (s *kbResumeSessionHandle) AddCleanupFunc(func())           {}
func (s *kbResumeSessionHandle) SetHasUnansweredQuestion(bool)   {}
func (s *kbResumeSessionHandle) CloseStdin()                     {}
func (s *kbResumeSessionHandle) SetOnToolAllowed(func(string, json.RawMessage)) {
}
func (s *kbResumeSessionHandle) SetOnFileRead(func(llm.FileReadEvent))   {}
func (s *kbResumeSessionHandle) SetOnSubagentEvent(func(llm.SDKMessage)) {}

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
		RepoStates: map[string]*feature.RepoState{
			"repo": {Error: &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "failed"}},
		},
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "failed"}
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
	// The dispatched feature now sits in StatusImplementing, which does not
	// admit another resume.
	if err := o.ResumeFeature(f.ID); !errors.Is(err, ErrResumeNotAvailable) {
		t.Fatalf("second ResumeFeature() error = %v, want ErrResumeNotAvailable", err)
	}
	if dispatches != 1 {
		t.Fatalf("implementation dispatches = %d, want 1", dispatches)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Status != feature.StatusImplementing || got.Run().Failure != nil || got.RepoStates["repo"].Error != nil {
		t.Errorf("feature after resume = status %s failure %+v repo error %+v, want Implementing with retry bookkeeping cleared",
			got.Status, got.Run().Failure, got.RepoStates["repo"].Error)
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
	if err := agent.NewResumeCoordinator(iterDir).ClearPending(time.Now()); err != nil {
		t.Fatalf("ClearPending() error = %v", err)
	}
}

func TestResumeFeatureClaimsInterruptedSequentialProviderSession(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	sm := mocks.NewMockSessionManager()
	var activeSessions []ports.SessionView
	sm.FeatureSessionsFn = func(string) []ports.SessionView {
		return activeSessions
	}
	sm.StartSessionFn = func(
		id string,
		featureID string,
		phase feature.Phase,
		_ []string,
		_ string,
		_ []string,
		_ ...*session.SessionOpts,
	) (ports.SessionHandle, error) {
		view := mocks.NewMockSessionView(id, featureID)
		view.PhaseVal = phase
		handle := &kbResumeSessionHandle{MockSessionView: view}
		activeSessions = append(activeSessions, handle)
		return handle, nil
	}
	var builds []agent.BuildSessionOpts
	phaseRunner := &agent.PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		Registry:       registry,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			builds = append(builds, opts)
			return []string{"true"}, nil, &session.SessionOpts{
				PIDDir:                opts.PIDDir,
				ProviderName:          "codex",
				Model:                 "model-a",
				SupportsSessionResume: true,
			}, nil
		},
	}
	f := &feature.Feature{
		ID:            "manual-interrupted-inquire",
		Name:          "Manual interrupted inquire",
		Slug:          "manual-interrupted-inquire",
		Status:        feature.StatusInterrupted,
		CurrentPhase:  feature.PhaseInquire,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineLarge,
		Models:        config.ModelConfig{Inquiry: "codex:model-a"},
		Repos:         []feature.FeatureRepo{{Name: "repo", Path: t.TempDir()}},
		RepoStates:    map[string]*feature.RepoState{"repo": {}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	resumeDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve inquire")
	}
	if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-interrupted-inquire",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "inquire",
		RunNumber:             1,
		OrchestratorSessionID: f.ID + "-inquire",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}

	o := New(Deps{
		Lifecycle:   manager,
		Store:       store,
		Sessions:    sm,
		PhaseRunner: phaseRunner,
	}, Hooks{})
	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if len(builds) != 1 {
		t.Fatalf("BuildSession calls = %d, want 1", len(builds))
	}
	if got := builds[0].ResumeSessionID; got != "thread-interrupted-inquire" {
		t.Fatalf("ResumeSessionID = %q, want thread-interrupted-inquire", got)
	}
	// The first resume's claim is still held, so a second attempt is a
	// genuine conflict, not a status-ineligible rejection.
	if err := o.ResumeFeature(f.ID); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("second ResumeFeature() error = %v, want ErrResumeConflict", err)
	}
}

func TestPhaseUsesParentResumeClaim(t *testing.T) {
	tests := []struct {
		phase feature.Phase
		want  bool
	}{
		{phase: feature.PhaseKnowledgeBase},
		{phase: feature.PhaseInquire, want: true},
		{phase: feature.PhaseResearch, want: true},
		{phase: feature.PhaseDesign, want: true},
		{phase: feature.PhasePlan, want: true},
		{phase: feature.PhaseImplement, want: true},
		{phase: feature.PhaseReview},
		{phase: feature.PhaseFinalReview},
		{phase: feature.PhasePublish},
	}
	for _, test := range tests {
		if got := phaseUsesParentResumeClaim(test.phase); got != test.want {
			t.Errorf("phaseUsesParentResumeClaim(%s) = %v, want %v", test.phase, got, test.want)
		}
	}
}

func TestResumeFeatureInterruptedSequentialLaunchFailureReleasesClaim(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	f := &feature.Feature{
		ID:            "manual-interrupted-release",
		Name:          "Manual interrupted release",
		Slug:          "manual-interrupted-release",
		Status:        feature.StatusInterrupted,
		CurrentPhase:  feature.PhaseInquire,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineLarge,
		Models:        config.ModelConfig{Inquiry: "codex:model-a"},
		Repos:         []feature.FeatureRepo{{Name: "repo", Path: t.TempDir()}},
		RepoStates:    map[string]*feature.RepoState{"repo": {}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	resumeDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve inquire")
	}
	now := time.Now()
	if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
		ProviderSessionID: "thread-release",
		Provider:          "codex",
		ResolvedModel:     "model-a",
		PhaseKey:          "inquire",
		RunNumber:         1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	buildErr := errors.New("injected build failure")
	runner := &agent.PhaseRunner{
		StateDir: stateDir,
		Registry: registry,
		BuildSessionFn: func(agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			return nil, nil, nil, buildErr
		},
	}
	o := New(Deps{Lifecycle: manager, Store: store, PhaseRunner: runner}, Hooks{})

	if err := o.ResumeFeature(f.ID); !errors.Is(err, buildErr) {
		t.Fatalf("ResumeFeature() error = %v, want injected build failure", err)
	}
	record, err := agent.ReadResumeRecord(resumeDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil || record.PendingResume {
		t.Fatalf("resume record after failed launch = %#v, want cleared pending intent", record)
	}
	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	claim, eligibility, err := agent.NewResumeCoordinator(resumeDir).Claim(
		f.ID,
		reloaded,
		"codex:model-a",
		registry,
		time.Now(),
	)
	if err != nil || !eligibility.Eligible || claim == nil {
		t.Fatalf("second Claim() = (%#v, %#v, %v), want reusable claim", claim, eligibility, err)
	}
	t.Cleanup(func() {
		_ = claim.Release(time.Now())
	})
}

func TestResumeFeatureInterruptedImplementationPreservesIteration(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	planPath := filepath.Join(stateDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1\n\n**Repo:** repo\n\nDo it.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:                  "manual-interrupted-implement",
		Name:                "Manual interrupted implement",
		Slug:                "manual-interrupted-implement",
		Status:              feature.StatusInterrupted,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Pipeline:            feature.PipelineLarge,
		Models:              config.ModelConfig{Implementation: "codex:model-a"},
		Artifacts:           map[string]string{"plan": planPath},
		Repos:               []feature.FeatureRepo{{Name: "repo", Path: stateDir}},
		RepoStates:          map[string]*feature.RepoState{"repo": {}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	resumeDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve implementation iteration")
	}
	now := time.Now()
	if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-interrupted-implement",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             2,
		RunNumber:             1,
		OrchestratorSessionID: f.ID + "-phase-01-impl-02",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	runner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	o := New(Deps{Lifecycle: manager, Store: store, PhaseRunner: runner}, Hooks{})
	dispatchedIteration := 0
	o.SetRunMultiRepoImplFn(func(current *feature.Feature, _ string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatchedIteration = current.CurrentIteration
		return make(chan *agent.OrchestratorResult, 1), nil
	})

	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if dispatchedIteration != 2 {
		t.Fatalf("dispatched implementation iteration = %d, want 2", dispatchedIteration)
	}
	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.Status != feature.StatusImplementing ||
		reloaded.CurrentIteration != 2 ||
		reloaded.ActiveTimingKey != "phase-1-impl" {
		t.Fatalf("feature after resume = status %s iteration %d timing %q",
			reloaded.Status, reloaded.CurrentIteration, reloaded.ActiveTimingKey)
	}
	record, err := agent.ReadResumeRecord(resumeDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil || !record.PendingResume {
		t.Fatalf("resume record = %#v, want pending implementation continuation", record)
	}
	t.Cleanup(func() {
		_ = agent.NewResumeCoordinator(resumeDir).ClearPending(time.Now())
	})
}

func TestResumeFeatureInterruptedImplementationDispatchFailureRollsBackTransition(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	planPath := filepath.Join(stateDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1\n\n**Repo:** repo\n\nDo it.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:                  "manual-interrupted-rollback",
		Name:                "Manual interrupted rollback",
		Slug:                "manual-interrupted-rollback",
		Status:              feature.StatusInterrupted,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Pipeline:            feature.PipelineLarge,
		Models:              config.ModelConfig{Implementation: "codex:model-a"},
		Artifacts:           map[string]string{"plan": planPath},
		Repos:               []feature.FeatureRepo{{Name: "repo", Path: stateDir}},
		RepoStates:          map[string]*feature.RepoState{"repo": {}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	resumeDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve implementation iteration")
	}
	now := time.Now()
	if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-rollback",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             2,
		RunNumber:             1,
		OrchestratorSessionID: f.ID + "-phase-01-impl-02",
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	runner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	o := New(Deps{Lifecycle: manager, Store: store, PhaseRunner: runner}, Hooks{})
	dispatchErr := errors.New("injected dispatch failure")
	o.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		return nil, dispatchErr
	})

	if err := o.ResumeFeature(f.ID); !errors.Is(err, dispatchErr) {
		t.Fatalf("ResumeFeature() error = %v, want injected dispatch failure", err)
	}
	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.Status == feature.StatusImplementing {
		t.Fatalf("feature status after failed dispatch = %s, want rolled back (a phantom Implementing feature 409s every later resume)", reloaded.Status)
	}
	if reloaded.Status != feature.StatusInterrupted {
		t.Fatalf("feature status after failed dispatch = %s, want Interrupted restored", reloaded.Status)
	}
	// The retry affordance must survive: a second resume re-attempts dispatch
	// instead of returning ErrResumeNotAvailable forever.
	if err := o.ResumeFeature(f.ID); !errors.Is(err, dispatchErr) {
		t.Fatalf("second ResumeFeature() error = %v, want the injected dispatch failure again (not a permanent conflict)", err)
	}
}

func TestResumeFeatureInterruptedImplementationReviewPreservesIteration(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	planPath := filepath.Join(stateDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1\n\nReview it.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	f := &feature.Feature{
		ID:                  "manual-interrupted-implementation-review",
		Name:                "Manual interrupted implementation review",
		Slug:                "manual-interrupted-implementation-review",
		Status:              feature.StatusInterrupted,
		CurrentPhase:        feature.PhaseImplement,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		RunCount:            1,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Pipeline:            feature.PipelineMoonshot,
		Models: config.ModelConfig{
			Implementation: "codex:model-a",
			Review:         "codex:model-a",
		},
		Artifacts:  map[string]string{"plan": planPath},
		Repos:      []feature.FeatureRepo{{Name: "repo", Path: stateDir}},
		RepoStates: map[string]*feature.RepoState{"repo": {}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	unitDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve implementation iteration")
	}
	now := time.Now()
	completedAt := now
	if err := agent.WriteResumeRecord(unitDir, agent.ResumeRecord{
		ProviderSessionID: "implementation-thread", Provider: "codex", ResolvedModel: "model-a",
		PhaseKey: "phase-1-impl", Iteration: 2, RunNumber: 1,
		OrchestratorSessionID: "implementation-parent", CreatedAt: now, UpdatedAt: now,
		Completed: true, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatalf("WriteResumeRecord(parent) error = %v", err)
	}
	if err := agent.WriteResumeRecord(filepath.Join(unitDir, "review", "craft"), agent.ResumeRecord{
		ProviderSessionID: "craft-thread", Provider: "codex", ResolvedModel: "model-a",
		PhaseKey: "phase-1-impl", ChildKey: "craft", Iteration: 2, RunNumber: 1,
		OrchestratorSessionID: "implementation-review-craft", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("WriteResumeRecord(child) error = %v", err)
	}
	runner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	o := New(Deps{Lifecycle: manager, Store: store, PhaseRunner: runner}, Hooks{})
	dispatchedIteration := 0
	o.SetRunMultiRepoImplFn(func(current *feature.Feature, _ string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatchedIteration = current.CurrentIteration
		return make(chan *agent.OrchestratorResult, 1), nil
	})

	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if dispatchedIteration != 2 {
		t.Fatalf("dispatched implementation iteration = %d, want interrupted review iteration 2", dispatchedIteration)
	}
	reloaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.Status != feature.StatusImplementing ||
		reloaded.CurrentIteration != 2 ||
		reloaded.ActiveTimingKey != "phase-1-impl" {
		t.Fatalf("feature after review resume = status %s iteration %d timing %q",
			reloaded.Status, reloaded.CurrentIteration, reloaded.ActiveTimingKey)
	}
	eligibility := o.implementationReviewResumeEligibility(reloaded)
	if !eligibility.Eligible {
		t.Fatalf("implementation review eligibility = %#v, want eligible child continuation", eligibility)
	}
}

func TestResumeFeatureClaimsInterruptedKnowledgeBaseRepositories(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	sm := mocks.NewMockSessionManager()
	var activeSessions []ports.SessionView
	sm.FeatureSessionsFn = func(string) []ports.SessionView {
		return activeSessions
	}
	sm.StartSessionFn = func(
		id string,
		featureID string,
		phase feature.Phase,
		_ []string,
		_ string,
		_ []string,
		opts ...*session.SessionOpts,
	) (ports.SessionHandle, error) {
		repoName := ""
		if len(opts) > 0 && opts[0] != nil {
			repoName = opts[0].RepoName
		}
		view := mocks.NewMockSessionView(id, featureID)
		view.PhaseVal = phase
		view.RepoNameVal = repoName
		handle := &kbResumeSessionHandle{MockSessionView: view}
		activeSessions = append(activeSessions, handle)
		return handle, nil
	}
	var builds []agent.BuildSessionOpts
	phaseRunner := &agent.PhaseRunner{
		SessionManager: sm,
		FeatureStore:   store,
		StateDir:       stateDir,
		Registry:       registry,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
			builds = append(builds, opts)
			return []string{"true"}, nil, &session.SessionOpts{
				PIDDir:                opts.PIDDir,
				ProviderName:          "codex",
				Model:                 "model-a",
				RepoName:              opts.RepoName,
				SupportsSessionResume: true,
			}, nil
		},
	}

	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: t.TempDir()},
		{Name: "repo-b", Path: t.TempDir()},
	}
	f := &feature.Feature{
		ID:            "manual-kb-resume",
		Name:          "Manual KB resume",
		Slug:          "manual-kb-resume",
		Status:        feature.StatusInterrupted,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineLarge,
		Models:        config.ModelConfig{KBBuild: "codex:model-a"},
		Repos:         repos,
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {},
			"repo-b": {},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	now := time.Now()
	for _, repo := range repos {
		resumeDir := agent.KBResumeDir(stateDir, f, repo.Name)
		if err := agent.WriteResumeRecord(resumeDir, agent.ResumeRecord{
			ProviderSessionID:     "thread-" + repo.Name,
			Provider:              "codex",
			ResolvedModel:         "model-a",
			PhaseKey:              feature.PhaseKnowledgeBase.DirName(),
			ChildKey:              repo.Name,
			RunNumber:             1,
			OrchestratorSessionID: "manual-kb-kb-" + repo.Name,
			CreatedAt:             now,
			UpdatedAt:             now,
		}); err != nil {
			t.Fatalf("WriteResumeRecord(%s) error = %v", repo.Name, err)
		}
	}

	o := New(Deps{
		Lifecycle:   manager,
		Store:       store,
		Sessions:    sm,
		PhaseRunner: phaseRunner,
	}, Hooks{})
	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}

	if len(builds) != len(repos) {
		t.Fatalf("KB build count = %d, want %d", len(builds), len(repos))
	}
	resumeIDs := make(map[string]string, len(builds))
	for _, build := range builds {
		resumeIDs[build.RepoName] = build.ResumeSessionID
	}
	for _, repo := range repos {
		if got := resumeIDs[repo.Name]; got != "thread-"+repo.Name {
			t.Errorf("repo %s ResumeSessionID = %q, want %q", repo.Name, got, "thread-"+repo.Name)
		}
		record, err := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, repo.Name))
		if err != nil {
			t.Fatalf("ReadResumeRecord(%s) error = %v", repo.Name, err)
		}
		if record == nil || !record.PendingResume {
			t.Errorf("repo %s resume record = %#v, want pending resume", repo.Name, record)
		}
		t.Cleanup(func() {
			_ = agent.ReleaseKBLock(agent.KBStateDir(stateDir, repo.Name), f.ID)
		})
	}
	if err := o.ResumeFeature(f.ID); !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("second ResumeFeature() error = %v, want ErrResumeConflict", err)
	}
}

func TestClaimKBResumesRecordsFreshFallbackForIneligibleRepository(t *testing.T) {
	stateDir := t.TempDir()
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	runner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	f := &feature.Feature{
		ID:           "kb-mixed-resume",
		CurrentPhase: feature.PhaseKnowledgeBase,
		ActiveRun:    1,
		Models:       config.ModelConfig{KBBuild: "codex:model-a"},
		Repos: []feature.FeatureRepo{
			{Name: "eligible", Path: t.TempDir()},
			{Name: "model-changed", Path: t.TempDir()},
		},
	}
	now := time.Now()
	for _, repo := range f.Repos {
		model := "model-a"
		if repo.Name == "model-changed" {
			model = "model-b"
		}
		if err := agent.WriteResumeRecord(agent.KBResumeDir(stateDir, f, repo.Name), agent.ResumeRecord{
			ProviderSessionID: "thread-" + repo.Name,
			Provider:          "codex",
			ResolvedModel:     model,
			PhaseKey:          feature.PhaseKnowledgeBase.DirName(),
			ChildKey:          repo.Name,
			RunNumber:         1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}); err != nil {
			t.Fatalf("WriteResumeRecord(%s) error = %v", repo.Name, err)
		}
	}
	o := New(Deps{PhaseRunner: runner}, Hooks{})

	claims, err := o.claimKBResumes(f, nil)
	if err != nil {
		t.Fatalf("claimKBResumes() error = %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("claim count = %d, want 1", len(claims))
	}
	t.Cleanup(func() {
		_ = releaseResumeClaims(claims, time.Now())
	})
	eligible, err := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, "eligible"))
	if err != nil {
		t.Fatalf("ReadResumeRecord(eligible) error = %v", err)
	}
	if eligible == nil || !eligible.PendingResume {
		t.Fatalf("eligible record = %#v, want pending resume", eligible)
	}
	ineligible, err := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, "model-changed"))
	if err != nil {
		t.Fatalf("ReadResumeRecord(model-changed) error = %v", err)
	}
	if ineligible == nil ||
		ineligible.PendingResume ||
		ineligible.FreshFallbackCount != 1 ||
		ineligible.FreshFallbackReason != string(agent.ResumeReasonModelChanged) {
		t.Fatalf("model-changed record = %#v, want one model_changed fresh fallback", ineligible)
	}
}

func TestClaimKBResumesReleasesEarlierClaimsOnConflict(t *testing.T) {
	stateDir := t.TempDir()
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	runner := &agent.PhaseRunner{StateDir: stateDir, Registry: registry}
	f := &feature.Feature{
		ID:           "kb-claim-conflict",
		CurrentPhase: feature.PhaseKnowledgeBase,
		ActiveRun:    1,
		Models:       config.ModelConfig{KBBuild: "codex:model-a"},
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: t.TempDir()},
			{Name: "repo-b", Path: t.TempDir()},
		},
	}
	now := time.Now()
	parent := agent.ResumeParentContext{PhaseKey: feature.PhaseKnowledgeBase.DirName()}
	for _, repo := range f.Repos {
		if err := agent.WriteResumeRecord(agent.KBResumeDir(stateDir, f, repo.Name), agent.ResumeRecord{
			ProviderSessionID: "thread-" + repo.Name,
			Provider:          "codex",
			ResolvedModel:     "model-a",
			PhaseKey:          feature.PhaseKnowledgeBase.DirName(),
			ChildKey:          repo.Name,
			RunNumber:         1,
			CreatedAt:         now,
			UpdatedAt:         now,
		}); err != nil {
			t.Fatalf("WriteResumeRecord(%s) error = %v", repo.Name, err)
		}
	}
	repoBCoordinator := agent.NewChildResumeCoordinator(
		agent.KBResumeDir(stateDir, f, "repo-b"),
		"repo-b",
		parent,
	)
	heldClaim, eligibility, err := repoBCoordinator.Claim(
		f.ID,
		f,
		"codex:model-a",
		registry,
		time.Now(),
	)
	if err != nil || !eligibility.Eligible || heldClaim == nil {
		t.Fatalf("held repo-b Claim() = (%#v, %#v, %v)", heldClaim, eligibility, err)
	}
	t.Cleanup(func() {
		_ = heldClaim.Release(time.Now())
	})
	o := New(Deps{PhaseRunner: runner}, Hooks{})

	if _, err := o.claimKBResumes(f, nil); !errors.Is(err, agent.ErrResumeAlreadyClaimed) {
		t.Fatalf("claimKBResumes() error = %v, want ErrResumeAlreadyClaimed", err)
	}
	repoARecord, err := agent.ReadResumeRecord(agent.KBResumeDir(stateDir, f, "repo-a"))
	if err != nil {
		t.Fatalf("ReadResumeRecord(repo-a) error = %v", err)
	}
	if repoARecord == nil || repoARecord.PendingResume {
		t.Fatalf("repo-a record = %#v, want earlier claim released", repoARecord)
	}
	repoACoordinator := agent.NewChildResumeCoordinator(
		agent.KBResumeDir(stateDir, f, "repo-a"),
		"repo-a",
		parent,
	)
	retryClaim, retryEligibility, err := repoACoordinator.Claim(
		f.ID,
		f,
		"codex:model-a",
		registry,
		time.Now(),
	)
	if err != nil || !retryEligibility.Eligible || retryClaim == nil {
		t.Fatalf("repo-a retry Claim() = (%#v, %#v, %v), want reusable claim", retryClaim, retryEligibility, err)
	}
	t.Cleanup(func() {
		_ = retryClaim.Release(time.Now())
	})
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

func TestResumeFeatureDispatchesFailedFinalReviewFromEligibleChild(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	publishable := false
	f := &feature.Feature{
		ID:              "manual-final-review-resume",
		Name:            "Manual final review resume",
		Slug:            "manual-final-review-resume",
		Status:          feature.StatusFailed,
		CurrentPhase:    feature.PhaseFinalReview,
		ReviewIteration: 1,
		ActiveRun:       1,
		RunCount:        1,
		SchemaVersion:   feature.SchemaVersionCurrent,
		Pipeline:        feature.PipelineLarge,
		Models: config.ModelConfig{
			Implementation: "codex:model-a",
			Review:         "codex:model-a",
		},
		Repos: []feature.FeatureRepo{{Name: "repo", Path: stateDir, Publishable: &publishable}},
		RepoStates: map[string]*feature.RepoState{
			"repo": {Touched: true, Error: &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}},
		},
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	unitDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve final review")
	}
	if err := agent.WriteResumeRecord(filepath.Join(unitDir, "qa"), agent.ResumeRecord{
		ProviderSessionID:     "qa-thread",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              feature.PhaseFinalReview.DirName(),
		ChildKey:              "qa",
		Iteration:             f.ReviewIteration,
		RunNumber:             f.ActiveRun,
		OrchestratorSessionID: "manual-final-review-qa",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	o := New(Deps{
		Lifecycle:   manager,
		Store:       store,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir, Registry: registry},
	}, Hooks{})
	t.Cleanup(o.WaitForCycles)
	dispatches := 0
	o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatches++
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if dispatches != 1 {
		t.Fatalf("final-review dispatches = %d, want 1", dispatches)
	}
}

func TestResumeFeatureDispatchesFailedImplementationReviewFromEligibleChild(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	planPath := filepath.Join(stateDir, "plan.md")
	if err := os.WriteFile(planPath, []byte("# Plan\n\n## Tasks\n\n### Task 1\n\nResume review.\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(plan) error = %v", err)
	}
	f := &feature.Feature{
		ID: "manual-implementation-review-resume", Name: "Manual implementation review resume",
		Slug: "manual-implementation-review-resume", Status: feature.StatusFailed,
		CurrentPhase: feature.PhaseImplement, CurrentIteration: 2, CurrentRoadmapPhase: 1,
		ActiveTimingKey: "phase-1-impl", ActiveRun: 1, RunCount: 1,
		SchemaVersion: feature.SchemaVersionCurrent, Pipeline: feature.PipelineMoonshot,
		Models:    config.ModelConfig{Implementation: "codex:model-a", Review: "codex:model-a"},
		Artifacts: map[string]string{"plan": planPath},
		Repos:     []feature.FeatureRepo{{Name: "repo", Path: stateDir}},
		RepoStates: map[string]*feature.RepoState{
			"repo": {Touched: true, Error: &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}},
		},
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	unitDir, ok := agent.ResumeUnitDir(stateDir, f)
	if !ok {
		t.Fatal("ResumeUnitDir() did not resolve implementation iteration")
	}
	now := time.Now()
	if err := agent.WriteResumeRecord(unitDir, agent.ResumeRecord{
		ProviderSessionID: "implementation-thread", Provider: "codex", ResolvedModel: "model-a",
		PhaseKey: "phase-1-impl", Iteration: 2, RunNumber: 1,
		OrchestratorSessionID: "implementation-parent", CreatedAt: now, UpdatedAt: now, Completed: true,
	}); err != nil {
		t.Fatalf("WriteResumeRecord(parent) error = %v", err)
	}
	if err := agent.WriteResumeRecord(filepath.Join(unitDir, "review", "qa"), agent.ResumeRecord{
		ProviderSessionID: "qa-thread", Provider: "codex", ResolvedModel: "model-a",
		PhaseKey: "phase-1-impl", ChildKey: "qa", Iteration: 2, RunNumber: 1,
		OrchestratorSessionID: "implementation-review-qa", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("WriteResumeRecord(child) error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	o := New(Deps{
		Lifecycle: manager, Store: store,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir, Registry: registry},
	}, Hooks{})
	t.Cleanup(o.WaitForCycles)
	dispatches := 0
	o.SetRunMultiRepoImplFn(func(*feature.Feature, string, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatches++
		ch := make(chan *agent.OrchestratorResult, 1)
		close(ch)
		return ch, nil
	})

	if err := o.ResumeFeature(f.ID); err != nil {
		t.Fatalf("ResumeFeature() error = %v", err)
	}
	if dispatches != 1 {
		t.Fatalf("implementation dispatches = %d, want 1", dispatches)
	}
	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.CurrentIteration != 2 || got.ActiveTimingKey != "phase-1-impl" {
		t.Fatalf("resume position = iteration %d timing %q, want preserved implementation artifacts", got.CurrentIteration, got.ActiveTimingKey)
	}
}

func TestResumeFeatureFailedSequentialPhaseRefusesOpenNeedUserInputGate(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	publishable := false
	f := &feature.Feature{
		ID:              "manual-resume-input-gate",
		Name:            "Manual resume input gate",
		Slug:            "manual-resume-input-gate",
		Status:          feature.StatusFailed,
		CurrentPhase:    feature.PhaseFinalReview,
		ReviewIteration: 1,
		ActiveRun:       1,
		RunCount:        1,
		SchemaVersion:   feature.SchemaVersionCurrent,
		Pipeline:        feature.PipelineLarge,
		Models: config.ModelConfig{
			Implementation: "codex:model-a",
			Review:         "codex:model-a",
		},
		Repos: []feature.FeatureRepo{{Name: "repo", Path: stateDir, Publishable: &publishable}},
		RepoStates: map[string]*feature.RepoState{
			"repo": {Touched: true, Error: &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}},
		},
		PendingNeedUserInputPath: filepath.Join(stateDir, "need-input.md"),
	}
	f.Run().Failure = &errcat.FailureRecord{Code: errcat.InfrastructureFailure, Diagnostics: "review failed"}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	o := New(Deps{
		Lifecycle:   manager,
		Store:       store,
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir, Registry: registry},
	}, Hooks{})
	t.Cleanup(o.WaitForCycles)
	dispatches := 0
	o.SetRunMultiRepoFinalReviewFn(func(*feature.Feature, ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		dispatches++
		ch := make(chan *agent.OrchestratorResult, 1)
		ch <- &agent.OrchestratorResult{FinalStatus: "all_passed"}
		return ch, nil
	})

	err := o.ResumeFeature(f.ID)
	if !errors.Is(err, feature.ErrNeedUserInputGateOpen) {
		t.Fatalf("ResumeFeature() error = %v, want ErrNeedUserInputGateOpen", err)
	}
	if dispatches != 0 {
		t.Fatalf("final-review dispatches = %d, want 0 (input gate must block dispatch)", dispatches)
	}
}
