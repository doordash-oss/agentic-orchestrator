//go:build tui_observe

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

package tui

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// readObserveEvents reads all JSONL events for a given feature from the observer output directory.
func readObserveEvents(t *testing.T, stateDir, featureID string) []observe.Event {
	t.Helper()
	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	f, err := os.Open(eventsPath)
	if err != nil {
		t.Fatalf("failed to open events file: %v", err)
	}
	defer f.Close()
	var events []observe.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt observe.Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("failed to unmarshal event: %v", err)
		}
		events = append(events, evt)
	}
	return events
}

// testObservedAppModel creates an AppModel with a real feature manager and observer
// backed by temp directories. Returns the model, manager, and observer state dir.
func testObservedAppModel(t *testing.T) (AppModel, *feature.Manager, string) {
	t.Helper()
	featureDir := t.TempDir()
	observeDir := t.TempDir()

	store := feature.NewStore(featureDir)
	cfg := config.NewDefault()
	fm := feature.NewManager(store, cfg)
	obs := observe.New(true, observeDir, false, "", false, "agentic")
	registry := llm.NewRegistry()
	phaseRunner := &agent.PhaseRunner{
		CommandRunner: agent.NewExecCommandRunner(),
		Registry:      registry,
		StateDir:      featureDir,
	}
	sm := session.NewManager(nil)
	orch := orchestrator.New(orchestrator.Deps{
		Lifecycle:   fm,
		Store:       store,
		Sessions:    sm,
		PhaseRunner: phaseRunner,
		CmdRunner:   phaseRunner.CommandRunner,
	}, orchestrator.BuildHooks(obs, nil, store, store.BaseDir))

	m := AppModel{
		featureManager: fm,
		observer:       observeTestAdapter{observer: obs},
		sessionManager: sm,
		orchestrator:   orch,
		registry:       registry,
		phaseRunner:    phaseRunner,
	}
	return m, fm, observeDir
}

type observeTestAdapter struct {
	observer *observe.Observer
}

func (a observeTestAdapter) PermissionRequested(sc ObservabilityContext, sessionID, repoName string, iteration int, toolName, toolInput string) {
	a.observer.PermissionRequested(toObserveTestContext(sc), sessionID, repoName, iteration, toolName, toolInput)
}

func (a observeTestAdapter) PermissionResolved(sc ObservabilityContext, sessionID, repoName string, iteration int, toolName, decision string) {
	a.observer.PermissionResolved(toObserveTestContext(sc), sessionID, repoName, iteration, toolName, decision)
}

func (a observeTestAdapter) QuestionAsked(sc ObservabilityContext, sessionID, repoName string, iteration int, question string) {
	a.observer.QuestionAsked(toObserveTestContext(sc), sessionID, repoName, iteration, question)
}

func (a observeTestAdapter) QuestionAnswered(sc ObservabilityContext, sessionID, repoName string, iteration int, question, answer string) {
	a.observer.QuestionAnswered(toObserveTestContext(sc), sessionID, repoName, iteration, question, answer)
}

func toObserveTestContext(sc ObservabilityContext) observe.SpanContext {
	return observe.SpanContext{
		TraceID:      sc.TraceID,
		SpanID:       sc.SpanID,
		ParentSpanID: sc.ParentSpanID,
		FeatureID:    sc.FeatureID,
		FeatureName:  sc.FeatureName,
		RunNumber:    sc.RunNumber,
	}
}

// createTestFeature saves a feature to the store and pre-creates the observer output directory.
func createTestFeature(t *testing.T, fm *feature.Manager, observeDir string, f *feature.Feature) {
	t.Helper()
	// Mirror Manager.Create's SchemaVersion=2 stamp so test fixtures are
	// treated as fresh per-repo-shape features (not legacy single-repo).
	// Otherwise the legacy reverse-translation in store.saveUnlocked would
	// strip RepoImpl entries for single-repo features and break tests that
	// rely on the per-repo state surface.
	if f.SchemaVersion == 0 {
		f.SchemaVersion = feature.SchemaVersionCurrent
	}
	if err := fm.Store.Save(f); err != nil {
		t.Fatalf("failed to save feature: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(observeDir, f.ID), 0755); err != nil {
		t.Fatalf("failed to create observer dir: %v", err)
	}
}

func TestTryCompletePublishObservedEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-pub-1"
	now := time.Now()
	f := &feature.Feature{
		ID:      fID,
		Name:    "test-publish",
		Status:  feature.StatusCodeReady,
		TraceID: "trace-pub-1",
		Repos:   []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/test/pr/1"},
		},
		PhaseCosts:   map[string]float64{"implement": 0.50},
		PhaseTimings: map[string]time.Duration{"implement": 5 * time.Second},
		StartedAt:    &now,
	}
	createTestFeature(t, fm, observeDir, f)

	published, err := m.orchestrator.TryCompletePublish(fID)
	if err != nil {
		t.Fatalf("tryCompletePublishObserved error: %v", err)
	}
	if !published {
		t.Fatal("expected published=true")
	}

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 0.50 {
				t.Errorf("expected total_cost_usd=0.50, got %v", evt.Data["total_cost_usd"])
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found")
	}
}

func TestTryCompletePublishObservedNoEventWhenNotPublished(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-nopub-1"
	now := time.Now()
	// Feature is Implementing, not PRReady — TryCompletePublish should return false
	f := &feature.Feature{
		ID:        fID,
		Name:      "not-ready",
		Status:    feature.StatusImplementing,
		TraceID:   "trace-nopub-1",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)

	published, err := m.orchestrator.TryCompletePublish(fID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if published {
		t.Fatal("expected published=false")
	}

	eventsPath := filepath.Join(observeDir, fID, "events.jsonl")
	if _, err := os.Stat(eventsPath); err == nil {
		events := readObserveEvents(t, observeDir, fID)
		for _, evt := range events {
			if evt.EventType == "feature.completed" {
				t.Error("unexpected feature.completed event when not published")
			}
		}
	}
}

func TestMarkPublishedObservedEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-markpub-1"
	now := time.Now()
	f := &feature.Feature{
		ID:           fID,
		Name:         "mark-published",
		Status:       feature.StatusCodeReady,
		TraceID:      "trace-markpub-1",
		Repos:        []feature.FeatureRepo{{Name: "repo-b", Path: "/tmp/repo-b"}},
		PhaseCosts:   map[string]float64{"plan": 0.25, "implement": 0.75},
		PhaseTimings: map[string]time.Duration{"plan": 2 * time.Second, "implement": 8 * time.Second},
		StartedAt:    &now,
	}
	createTestFeature(t, fm, observeDir, f)

	err := m.orchestrator.MarkPublished(fID, "https://github.com/test/pr/2")
	if err != nil {
		t.Fatalf("markPublishedObserved error: %v", err)
	}

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 1.0 {
				t.Errorf("expected total_cost_usd=1.0, got %v", evt.Data["total_cost_usd"])
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found")
	}
}

func TestMarkFailedObservedEmitsFeatureFailed(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-fail-1"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "test-fail",
		Status:    feature.StatusImplementing,
		TraceID:   "trace-fail-1",
		Repos:     []feature.FeatureRepo{{Name: "repo-c", Path: "/tmp/repo-c"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)

	err := m.orchestrator.MarkFailed(fID, "phase_start", "worktree not found")
	if err != nil {
		t.Fatalf("markFailedObserved error: %v", err)
	}

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.failed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if evt.Error != "worktree not found" {
				t.Errorf("expected error='worktree not found', got %q", evt.Error)
			}
			if ft, ok := evt.Data["failure_type"].(string); !ok || ft != "phase_start" {
				t.Errorf("expected failure_type='phase_start', got %v", evt.Data["failure_type"])
			}
		}
	}
	if !found {
		t.Error("no feature.failed event found")
	}
}

// Phase 8: tests for emitFeatureStarted/emitFeatureInterrupted/emitRecoveryScanned/
// emitRecoveryAction were deleted alongside the helpers. The equivalent
// hook-driven emissions are covered by orchestrator.BuildHooks tests in
// internal/orchestrator/hooks_test.go.

func TestHandlePhaseCompletedInquireEmitsPhaseCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	m.sessionManager = session.NewManager(nil)
	fID := "feat-hpc-inquire"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "inquire-phase-test",
		Status:    feature.StatusInquiring,
		TraceID:   "trace-hpc-inquire",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)
	writeTUIPhaseComplete(t, fm.Store.BaseDir, f, feature.PhaseInquire)
	writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, feature.PhaseInquire, "inquire.md")

	msg := PhaseCompletedMsg{
		FeatureID: fID,
		Phase:     feature.PhaseInquire,
		SessionID: "sess-inquire-1",
		Success:   true,
	}
	m.handlePhaseCompleted(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "phase.completed" {
			found = true
			if evt.Phase != feature.PhaseInquire.String() {
				t.Errorf("expected phase=%q, got %q", feature.PhaseInquire.String(), evt.Phase)
			}
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no phase.completed event found for inquire phase")
	}
}

func TestHandlePhaseCompletedResearchEmitsPhaseCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	m.sessionManager = session.NewManager(nil)
	fID := "feat-hpc-research"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "research-phase-test",
		Status:    feature.StatusResearching,
		TraceID:   "trace-hpc-research",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)
	writeTUIPhaseComplete(t, fm.Store.BaseDir, f, feature.PhaseResearch)
	writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, feature.PhaseResearch, "research.md")

	msg := PhaseCompletedMsg{
		FeatureID: fID,
		Phase:     feature.PhaseResearch,
		SessionID: "sess-research-1",
		Success:   true,
	}
	m.handlePhaseCompleted(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "phase.completed" {
			found = true
			if evt.Phase != feature.PhaseResearch.String() {
				t.Errorf("expected phase=%q, got %q", feature.PhaseResearch.String(), evt.Phase)
			}
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no phase.completed event found for research phase")
	}
}

func TestHandlePhaseCompletedDesignEmitsPhaseCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	m.sessionManager = session.NewManager(nil)
	fID := "feat-hpc-design"
	now := time.Now()
	f := &feature.Feature{
		ID:          fID,
		Name:        "design-phase-test",
		Status:      feature.StatusDesigning,
		TraceID:     "trace-hpc-design",
		Repos:       []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		Checkpoints: feature.Checkpoints{DesignReview: true},
		StartedAt:   &now,
	}
	createTestFeature(t, fm, observeDir, f)
	writeTUIPhaseComplete(t, fm.Store.BaseDir, f, feature.PhaseDesign)
	writeTUIPhaseMarkdown(t, fm.Store.BaseDir, f, feature.PhaseDesign, "design.md")

	msg := PhaseCompletedMsg{
		FeatureID: fID,
		Phase:     feature.PhaseDesign,
		SessionID: "sess-design-1",
		Success:   true,
	}
	m.handlePhaseCompleted(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "phase.completed" {
			found = true
			if evt.Phase != feature.PhaseDesign.String() {
				t.Errorf("expected phase=%q, got %q", feature.PhaseDesign.String(), evt.Phase)
			}
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no phase.completed event found for design phase")
	}
}

func TestHandlePhaseCompletedKBEmitsPhaseCompletedWhenAllDone(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	m.sessionManager = session.NewManager(nil)
	m.kbStaleWarnings = make(map[string]string)
	fID := "feat-hpc-knowbase"
	now := time.Now()
	repoPath := testutil.InitGitRepo(t)
	f := &feature.Feature{
		ID:           fID,
		Name:         "kb-phase-test",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		TraceID:      "trace-hpc-knowbase",
		Repos:        []feature.FeatureRepo{{Name: "my-repo", Path: repoPath}},
		KBStatus:     map[string]string{"my-repo": "pending"},
		StartedAt:    &now,
	}
	createTestFeature(t, fm, observeDir, f)

	// Pre-create the persistent KB artifact that the per-repo loop validates
	// before advancing.
	kbDir := agent.KBStateDir(fm.Store.BaseDir, "my-repo")
	if err := os.MkdirAll(kbDir, 0755); err != nil {
		t.Fatalf("failed to create KB state dir: %v", err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# kb\n"), 0o644); err != nil {
		t.Fatalf("failed to write KB index: %v", err)
	}

	if err := m.orchestrator.HandlePhaseCompletion(fID, orchestrator.PhaseCompletionInput{
		Phase:    feature.PhaseKnowledgeBase,
		RepoName: "my-repo",
		KnowledgeBaseResult: &agent.BlockingLoopResult{
			FinalStatus:   agent.BlockingLoopStatusSuccess,
			CanonicalPath: agent.KBPath(kbDir),
		},
	}); err != nil {
		t.Fatalf("HandlePhaseCompletion(KB loop result): %v", err)
	}

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "phase.completed" {
			found = true
			if evt.Phase != feature.PhaseKnowledgeBase.String() {
				t.Errorf("expected phase=%q, got %q", feature.PhaseKnowledgeBase.String(), evt.Phase)
			}
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no phase.completed event found for KB phase")
	}
}

func TestHandlePhaseCompletedFailureEmitsPhaseFailed(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	m.sessionManager = session.NewManager(nil)
	fID := "feat-hpc-fail"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "failure-test",
		Status:    feature.StatusResearching,
		TraceID:   "trace-hpc-fail",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)

	msg := PhaseCompletedMsg{
		FeatureID:   fID,
		Phase:       feature.PhaseResearch,
		SessionID:   "sess-research-1",
		Success:     false,
		ErrorDetail: "agent crashed",
	}
	m.handlePhaseCompleted(msg)

	events := readObserveEvents(t, observeDir, fID)
	foundPhaseFailed := false
	foundFeatureFailed := false
	for _, evt := range events {
		if evt.EventType == "phase.failed" {
			foundPhaseFailed = true
			if evt.Phase != feature.PhaseResearch.String() {
				t.Errorf("expected phase=%q, got %q", feature.PhaseResearch.String(), evt.Phase)
			}
			if evt.Error == "" {
				t.Error("expected non-empty error in phase.failed event")
			}
		}
		if evt.EventType == "feature.failed" {
			foundFeatureFailed = true
		}
	}
	if !foundPhaseFailed {
		t.Error("no phase.failed event found")
	}
	if !foundFeatureFailed {
		t.Error("no feature.failed event found (markFailedObserved should also emit)")
	}
}

// emitPhaseResult tests were removed alongside emitPhaseResult. The
// orchestrator fires OnPhaseCompleted via BuildHooks; coverage lives in
// internal/orchestrator/hooks_test.go.

func TestHandlePlanLoopDoneFailedEmitsFeatureFailed(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-planfail"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "plan-failure-test",
		Status:    feature.StatusPlanning,
		TraceID:   "trace-planfail",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)

	msg := PlanLoopDoneMsg{
		FeatureID: fID,
		Result: &agent.PlanLoopResult{
			FinalStatus: "failed",
			LastError:   "planning session crashed",
		},
	}
	m.handlePlanLoopDone(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.failed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if evt.Error == "" {
				t.Error("expected non-empty error in feature.failed event")
			}
		}
	}
	if !found {
		t.Error("no feature.failed event found from handlePlanLoopDone")
	}
}

// --- Boundary-level tests: exercise real Update() / handler methods ---

func TestPublishExecuteResultMsgMultiRepoEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-pubexec-multi"
	now := time.Now()
	f := &feature.Feature{
		ID:      fID,
		Name:    "publish-exec-multi-test",
		Status:  feature.StatusCodeReady,
		TraceID: "trace-pubexec-multi",
		Repos:   []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true},
		},
		PhaseCosts:   map[string]float64{"implement": 3.0},
		PhaseTimings: map[string]time.Duration{"implement": 30 * time.Second},
		StartedAt:    &now,
	}
	createTestFeature(t, fm, observeDir, f)

	// Set m.publish.featureID so the handler can reference it
	m.publish = PublishModel{featureID: fID}

	msg := publishExecuteResultMsg{
		prURL:    "https://github.com/test/pr/20",
		repoName: "repo-a",
	}
	m.Update(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found from publishExecuteResultMsg multi-repo via Update()")
	}
}

func TestPublishExecuteResultMsgSingleRepoEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-pubexec-single"
	now := time.Now()
	f := &feature.Feature{
		ID:           fID,
		Name:         "publish-exec-single-test",
		Status:       feature.StatusCodeReady,
		TraceID:      "trace-pubexec-single",
		Repos:        []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		PhaseCosts:   map[string]float64{"implement": 1.5},
		PhaseTimings: map[string]time.Duration{"implement": 15 * time.Second},
		StartedAt:    &now,
	}
	createTestFeature(t, fm, observeDir, f)

	// Set m.publish.featureID for the single-repo path (no repoName)
	m.publish = PublishModel{featureID: fID}

	msg := publishExecuteResultMsg{
		prURL: "https://github.com/test/pr/21",
	}
	m.Update(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if cost, ok := evt.Data["total_cost_usd"].(float64); !ok || cost != 1.5 {
				t.Errorf("expected total_cost_usd=1.5, got %v", evt.Data["total_cost_usd"])
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found from publishExecuteResultMsg single-repo via Update()")
	}
}

func TestPublishExecuteResultMsgSingleRepoErrorEmitsFeatureFailed(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-pubexec-err"
	now := time.Now()
	f := &feature.Feature{
		ID:        fID,
		Name:      "publish-exec-error-test",
		Status:    feature.StatusImplementing,
		TraceID:   "trace-pubexec-err",
		Repos:     []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt: &now,
	}
	createTestFeature(t, fm, observeDir, f)

	// Set m.publish.featureID for the single-repo error path (no repoName)
	m.publish = PublishModel{featureID: fID}

	msg := publishExecuteResultMsg{
		err: errors.New("push rejected by remote"),
	}
	m.Update(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.failed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
			if evt.Error != "push rejected by remote" {
				t.Errorf("expected error='push rejected by remote', got %q", evt.Error)
			}
			if ft, ok := evt.Data["failure_type"].(string); !ok || ft != feature.FailureInfrastructure {
				t.Errorf("expected failure_type=%q, got %v", feature.FailureInfrastructure, evt.Data["failure_type"])
			}
		}
	}
	if !found {
		t.Error("no feature.failed event found from publishExecuteResultMsg single-repo error via Update()")
	}
}

func TestMultiRepoImplDoneAutoPublishEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-mrimpl-autopub"
	now := time.Now()
	f := &feature.Feature{
		ID:      fID,
		Name:    "multi-repo-impl-autopub-test",
		Status:  feature.StatusImplementing,
		TraceID: "trace-mrimpl-autopub",
		Repos:   []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}, {Name: "repo-b", Path: "/tmp/repo-b"}},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/test/pr/30"},
			"repo-b": {Touched: true, PRURL: "https://github.com/test/pr/31"},
		},
		PhaseCosts:   map[string]float64{"implement": 5.0},
		PhaseTimings: map[string]time.Duration{"implement": 50 * time.Second},
		StartedAt:    &now,
		// AutoPublish: ManualPublish=false (default) => AutoPublish() = true
		// CurrentRoadmapPhase: 0 (default) => non-roadmap path
		// len(Repos) > 1 => bypasses Final Review, reaches tryCompletePublishObserved
	}
	createTestFeature(t, fm, observeDir, f)

	msg := MultiRepoImplDoneMsg{
		FeatureID: fID,
		Result: &agent.OrchestratorResult{
			FinalStatus:  "all_passed",
			RepoStatuses: map[string]string{"repo-a": "success", "repo-b": "success"},
		},
	}
	m.handleMultiRepoImplDone(msg)

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("expected feature_id=%s, got %s", fID, evt.FeatureID)
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found from handleMultiRepoImplDone with auto-publish")
	}
}

// --- Boundary-level tests: Init() resume, StartPhaseMsg publish, start* failure, recovery observer injection ---

func TestInitResumeAutoPublishEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-init-resume"
	now := time.Now()
	f := &feature.Feature{
		ID:         fID,
		Name:       "init-resume-test",
		Status:     feature.StatusCodeReady,
		TraceID:    "trace-init-resume",
		Repos:      []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt:  &now,
		PhaseCosts: map[string]float64{"research": 0.10, "implement": 0.25},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/repo-a/pull/1"},
		},
	}
	createTestFeature(t, fm, observeDir, f)

	// Simulate the Init() startup resume path (app.go lines 863-886):
	// Init lists features and for CodeReady + AutoPublish + AllReposPublished,
	// calls tryCompletePublishObserved.
	features, err := fm.List()
	if err != nil {
		t.Fatal(err)
	}
	resumeFound := false
	for _, feat := range features {
		if feat.Status == feature.StatusCodeReady && feat.Checkpoints.AutoPublish() {
			if feat.AllReposPublished() {
				published, pubErr := m.orchestrator.TryCompletePublish(feat.ID)
				if pubErr != nil {
					t.Fatalf("TryCompletePublish: %v", pubErr)
				}
				if !published {
					t.Fatal("expected published=true")
				}
				resumeFound = true
			}
		}
	}
	if !resumeFound {
		t.Fatal("Init resume path did not find the PRReady feature")
	}

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("feature_id = %q, want %q", evt.FeatureID, fID)
			}
			costRaw, ok := evt.Data["total_cost_usd"].(float64)
			if !ok {
				t.Error("missing total_cost_usd")
			} else if costRaw < 0.30 {
				t.Errorf("total_cost_usd = %f, want >= 0.30", costRaw)
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found for Init resume path")
	}
}

func TestStartPhaseMsgPublishResumeEmitsFeatureCompleted(t *testing.T) {
	m, fm, observeDir := testObservedAppModel(t)
	fID := "feat-pr-resume"
	now := time.Now()
	f := &feature.Feature{
		ID:         fID,
		Name:       "pr-ready-resume-test",
		Status:     feature.StatusCodeReady,
		TraceID:    "trace-pr-resume",
		Repos:      []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
		StartedAt:  &now,
		PhaseCosts: map[string]float64{"implement": 0.20},
		RepoStates: map[string]*feature.RepoState{
			"repo-a": {Touched: true, PRURL: "https://github.com/org/repo-a/pull/1"},
		},
	}
	createTestFeature(t, fm, observeDir, f)

	msg := StartPhaseMsg{
		FeatureID: fID,
		Phase:     feature.PhasePublish,
	}
	var cmd tea.Cmd
	_, cmd = m.Update(msg)
	if cmd == nil {
		t.Fatal("Update(StartPhaseMsg{PhasePublish}) returned nil cmd")
	}

	// Execute the cmd — it calls tryCompletePublishObserved
	cmd()

	events := readObserveEvents(t, observeDir, fID)
	found := false
	for _, evt := range events {
		if evt.EventType == "feature.completed" {
			found = true
			if evt.FeatureID != fID {
				t.Errorf("feature_id = %q, want %q", evt.FeatureID, fID)
			}
		}
	}
	if !found {
		t.Error("no feature.completed event found for StartPhaseMsg publish resume path")
	}
}

// TestRecoveryScanEmitsOnlyAfterObserverInjection was removed with
// emitRecoveryScanned; OnRecoveryScanned hook coverage now lives in
// internal/orchestrator/hooks_test.go.
