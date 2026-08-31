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

package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// Helper: build an orchestrator + hook recorders primed for recovery tests.
// ---------------------------------------------------------------------------

type recoveryHookLog struct {
	mu      sync.Mutex
	Scanned []int
	Actions []recoveryActionLog
}

type recoveryActionLog struct {
	FeatureID string
	RepoName  string
	Action    string
}

func (l *recoveryHookLog) recordScan(items []ports.RecoveryItem) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Scanned = append(l.Scanned, len(items))
}

func (l *recoveryHookLog) recordAction(featureID, repoName, action string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Actions = append(l.Actions, recoveryActionLog{FeatureID: featureID, RepoName: repoName, Action: action})
}

func (l *recoveryHookLog) scanCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Scanned)
}

func (l *recoveryHookLog) actionCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.Actions)
}

// ---------------------------------------------------------------------------
// T1. ScanRecovery_EmitsRecoveryScannedEvent_WithCountInMessage
// ---------------------------------------------------------------------------

func TestScanRecovery_EmitsRecoveryScannedEvent_WithCountInMessage(t *testing.T) {
	items := fakeRecoveryItems(
		itemSpec{FeatureID: "feat-a", CurrentPhase: feature.PhaseImplement},
		itemSpec{FeatureID: "feat-b", RepoName: "repo-x", CurrentPhase: feature.PhaseImplement},
	)
	fake := &fakeRecoveryOp{Items: items}

	o := orchestrator.New(orchestrator.Deps{Recovery: fake}, orchestrator.Hooks{})

	got, err := o.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len(items) = %d, want 2", len(got))
	}

	events := drainEvents(o)
	var scanned []ports.Event
	for _, ev := range events {
		if ev.Type == ports.RecoveryScanned {
			scanned = append(scanned, ev)
		}
	}
	if len(scanned) != 1 {
		t.Fatalf("RecoveryScanned events = %d, want 1", len(scanned))
	}
	if scanned[0].Message != "2 items" {
		t.Errorf("Message = %q, want %q", scanned[0].Message, "2 items")
	}
}

// ---------------------------------------------------------------------------
// T2. ScanRecovery_FiresOnRecoveryScannedHookWithCount
// ---------------------------------------------------------------------------

func TestScanRecovery_FiresOnRecoveryScannedHookWithCount(t *testing.T) {
	items := fakeRecoveryItems(
		itemSpec{FeatureID: "feat-a"},
		itemSpec{FeatureID: "feat-b"},
	)
	fake := &fakeRecoveryOp{Items: items}
	log := &recoveryHookLog{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake},
		orchestrator.Hooks{OnRecoveryScanned: log.recordScan},
	)

	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if log.scanCount() != 1 {
		t.Fatalf("hook invocations = %d, want 1", log.scanCount())
	}
	if log.Scanned[0] != 2 {
		t.Errorf("hook items = %d, want 2", log.Scanned[0])
	}
}

// ---------------------------------------------------------------------------
// T3. ScanRecovery_ZeroItems_StillEmitsEventAndHook
// ---------------------------------------------------------------------------

func TestScanRecovery_ZeroItems_StillEmitsEventAndHook(t *testing.T) {
	fake := &fakeRecoveryOp{Items: nil}
	log := &recoveryHookLog{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake},
		orchestrator.Hooks{OnRecoveryScanned: log.recordScan},
	)

	got, err := o.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len(items) = %d, want 0", len(got))
	}
	if log.scanCount() != 1 {
		t.Errorf("hook invocations = %d, want 1", log.scanCount())
	}
	if log.Scanned[0] != 0 {
		t.Errorf("hook items = %d, want 0", log.Scanned[0])
	}

	events := drainEvents(o)
	var scanned *ports.Event
	for i, ev := range events {
		if ev.Type == ports.RecoveryScanned {
			scanned = &events[i]
			break
		}
	}
	if scanned == nil {
		t.Fatal("no RecoveryScanned event emitted on zero items")
	}
	if scanned.Message != "0 items" {
		t.Errorf("Message = %q, want %q", scanned.Message, "0 items")
	}
}

// ---------------------------------------------------------------------------
// T4. ScanRecovery_PortError_PropagatesAndDoesNotEmit
// ---------------------------------------------------------------------------

func TestScanRecovery_PortError_PropagatesAndDoesNotEmit(t *testing.T) {
	wantErr := errors.New("scan kaboom")
	fake := &fakeRecoveryOp{ScanErr: wantErr}
	log := &recoveryHookLog{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake},
		orchestrator.Hooks{OnRecoveryScanned: log.recordScan},
	)

	items, err := o.ScanRecovery(context.Background())
	if err == nil {
		t.Fatal("ScanRecovery: expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}
	if items != nil {
		t.Errorf("items = %v, want nil on error", items)
	}
	if log.scanCount() != 0 {
		t.Errorf("hook fired %d times on error, want 0", log.scanCount())
	}

	events := drainEvents(o)
	for _, ev := range events {
		if ev.Type == ports.RecoveryScanned {
			t.Errorf("unexpected RecoveryScanned event emitted on error")
		}
	}
}

// ---------------------------------------------------------------------------
// T4b. ScanRecovery_DropsSessionsThisProcessStillSupervises
// ---------------------------------------------------------------------------

// A PID file whose manager id matches a live managed session belongs to a
// healthy run this process is supervising, not an orphan. Offering resume or
// kill for it would terminate the run, so the scan must drop it.
func TestScanRecovery_DropsSessionsThisProcessStillSupervises(t *testing.T) {
	items := fakeRecoveryItems(
		itemSpec{FeatureID: "feat-a", CurrentPhase: feature.PhaseImplement, ProcessAlive: true},
		itemSpec{FeatureID: "feat-b", CurrentPhase: feature.PhaseImplement},
	)
	items[0].PIDFile.ManagerID = "feat-a-implement"
	fake := &fakeRecoveryOp{Items: items}
	sessions := mocks.NewMockSessionManager()
	sessions.ActiveSessionsFn = func() []ports.SessionView {
		return []ports.SessionView{mocks.NewMockSessionView("feat-a-implement", "feat-a")}
	}

	o := orchestrator.New(orchestrator.Deps{Recovery: fake, Sessions: sessions}, orchestrator.Hooks{})

	got, err := o.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(got) != 1 || got[0].PIDFile.FeatureID != "feat-b" {
		t.Fatalf("items = %+v, want only feat-b", got)
	}

	events := drainEvents(o)
	for _, ev := range events {
		if ev.Type == ports.RecoveryScanned && ev.Message != "1 items" {
			t.Errorf("Message = %q, want %q", ev.Message, "1 items")
		}
	}
}

// ---------------------------------------------------------------------------
// T5. ExecuteRecovery_DispatchesThroughPort
// ---------------------------------------------------------------------------

func TestExecuteRecovery_DispatchesThroughPort(t *testing.T) {
	items := fakeRecoveryItems(itemSpec{FeatureID: "feat-a", RepoName: "repo-x"})
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-a", "repo-x"): ports.RecoveryKill,
	}

	fake := &fakeRecoveryOp{}
	o := orchestrator.New(orchestrator.Deps{Recovery: fake}, orchestrator.Hooks{})

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}
	if len(fake.ExecCalls) != 1 {
		t.Fatalf("ExecCalls = %d, want 1", len(fake.ExecCalls))
	}
	call := fake.ExecCalls[0]
	if len(call.Items) != 1 || call.Items[0].RepoName != "repo-x" {
		t.Errorf("items forwarded incorrectly: %+v", call.Items)
	}
	if got := call.Actions[ports.RecoveryActionKey("feat-a", "repo-x")]; got != ports.RecoveryKill {
		t.Errorf("actions forwarded incorrectly: got %v, want %v", got, ports.RecoveryKill)
	}
}

// ---------------------------------------------------------------------------
// T6. ExecuteRecovery_PortError_NoEventsNoRelaunch
// ---------------------------------------------------------------------------

func TestExecuteRecovery_PortError_NoEventsNoRelaunch(t *testing.T) {
	wantErr := errors.New("exec kaboom")
	items := fakeRecoveryItems(itemSpec{
		FeatureID:    "feat-a",
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
	})
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-a", ""): ports.RecoveryResume,
	}

	fake := &fakeRecoveryOp{ExecErr: wantErr}

	lifecycle := mocks.NewMockFeatureLifecycle()
	spy := &fakeRunMultiRepoImpl{}
	hookLog := &recoveryHookLog{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Lifecycle: lifecycle},
		orchestrator.Hooks{OnRecoveryAction: hookLog.recordAction},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	err := o.ExecuteRecovery(context.Background(), items, actions)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}

	events := drainEvents(o)
	for _, ev := range events {
		if ev.Type == ports.RecoveryExecuted {
			t.Errorf("unexpected RecoveryExecuted event emitted on port error")
		}
	}
	if hookLog.actionCount() != 0 {
		t.Errorf("OnRecoveryAction fired %d times; want 0", hookLog.actionCount())
	}
	if spy.numCalls() != 0 {
		t.Errorf("relaunch spy called %d times; want 0", spy.numCalls())
	}
}

// ---------------------------------------------------------------------------
// T7. ExecuteRecovery_EmitsOneEventPerItemWithAction
// ---------------------------------------------------------------------------

func TestExecuteRecovery_EmitsOneEventPerItemWithAction(t *testing.T) {
	// feat-a's relaunch fails via lifecycle.Get returning DefaultError, so
	// the resume action emits an event but does not fire the relaunch spy —
	// keeps the test focused on per-item observations. Relaunch behavior is
	// exercised by T9.
	items := fakeRecoveryItems(
		itemSpec{FeatureID: "feat-a", CurrentPhase: feature.PhaseImplement, Status: feature.StatusImplementing},
		itemSpec{FeatureID: "feat-b", RepoName: "repo-x"},
		itemSpec{FeatureID: "feat-c"},
	)
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-a", ""):       ports.RecoveryResume,
		ports.RecoveryActionKey("feat-b", "repo-x"): ports.RecoveryKill,
		ports.RecoveryActionKey("feat-c", ""):       ports.RecoverySkip,
	}

	fake := &fakeRecoveryOp{}
	lifecycle := mocks.NewMockFeatureLifecycle()
	lifecycle.DefaultError = errors.New("feature not found")
	hookLog := &recoveryHookLog{}
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Lifecycle: lifecycle},
		orchestrator.Hooks{OnRecoveryAction: hookLog.recordAction},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err == nil {
		// relaunch for feat-a will fail because lifecycle.Get returns
		// DefaultError — that's fine, we only care about events here.
	}

	events := drainEvents(o)
	type seen struct {
		FeatureID string
		Message   string
	}
	var executed []seen
	for _, ev := range events {
		if ev.Type == ports.RecoveryExecuted {
			executed = append(executed, seen{FeatureID: ev.FeatureID, Message: ev.Message})
		}
	}
	if len(executed) != 3 {
		t.Fatalf("RecoveryExecuted events = %d, want 3; events = %+v", len(executed), executed)
	}

	wantSet := map[string]string{
		"feat-a": ":resume",
		"feat-b": "repo-x:kill",
		"feat-c": ":skip",
	}
	gotSet := map[string]string{}
	for _, ev := range executed {
		gotSet[ev.FeatureID] = ev.Message
	}
	for fid, wantMsg := range wantSet {
		if gotSet[fid] != wantMsg {
			t.Errorf("Event[%s].Message = %q, want %q", fid, gotSet[fid], wantMsg)
		}
	}

	if hookLog.actionCount() != 3 {
		t.Errorf("hook invocations = %d, want 3", hookLog.actionCount())
	}
}

// ---------------------------------------------------------------------------
// T8. ExecuteRecovery_ItemsWithoutActionAreNotObserved
// ---------------------------------------------------------------------------

func TestExecuteRecovery_ItemsWithoutActionAreNotObserved(t *testing.T) {
	items := fakeRecoveryItems(
		itemSpec{FeatureID: "feat-a"},
		itemSpec{FeatureID: "feat-b"},
	)
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-a", ""): ports.RecoverySkip,
	}

	fake := &fakeRecoveryOp{}
	hookLog := &recoveryHookLog{}
	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake},
		orchestrator.Hooks{OnRecoveryAction: hookLog.recordAction},
	)

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}

	events := drainEvents(o)
	var executed int
	for _, ev := range events {
		if ev.Type == ports.RecoveryExecuted {
			executed++
		}
	}
	if executed != 1 {
		t.Errorf("RecoveryExecuted events = %d, want 1", executed)
	}
	if hookLog.actionCount() != 1 {
		t.Errorf("hook invocations = %d, want 1", hookLog.actionCount())
	}
	if hookLog.actionCount() == 1 && hookLog.Actions[0].FeatureID != "feat-a" {
		t.Errorf("hook FeatureID = %q, want feat-a", hookLog.Actions[0].FeatureID)
	}
}

// ---------------------------------------------------------------------------
// T9. ExecuteRecovery_ResumeActionRelaunchesPhase
//
// Uses PhaseImplement as the relaunch target and runMultiRepoImplFn as the
// observable: the spy records that startPhase(feat-a, PhaseImplement) flowed
// through the full dispatch chain into StartMultiRepoImplementation.
// ---------------------------------------------------------------------------

func TestExecuteRecovery_ResumeActionRelaunchesPhase(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:           "feat-a",
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
		Artifacts:    map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	items := fakeRecoveryItems(itemSpec{
		FeatureID:    "feat-a",
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
	})
	// Overwrite so the item carries the same *Feature the store/lifecycle do.
	items[0].Feature = f

	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-a", ""): ports.RecoveryResume,
	}

	fake := &fakeRecoveryOp{}
	store := newFeatureStore(f)
	lc := lifecycleForFeature(f)
	// Track any StartImplementation calls; the idempotent relaunch path in
	// startImplement MUST skip StartImplementation for a feature already in
	// StatusImplementing. Seeing this function called would be a regression.
	var startImplCalls int
	lc.StartImplementationFn = func(id string) error {
		startImplCalls++
		return nil
	}

	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(orchestrator.Deps{
		Recovery:  fake,
		Lifecycle: lc,
		Store:     store,
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}
	if startImplCalls != 0 {
		t.Errorf("StartImplementation called %d times; want 0 (feature already StatusImplementing)", startImplCalls)
	}
	if spy.numCalls() != 1 {
		t.Fatalf("runMultiRepoImplFn calls = %d, want 1", spy.numCalls())
	}
	if spy.Calls[0].Feature == nil || spy.Calls[0].Feature.ID != "feat-a" {
		t.Errorf("spy Feature.ID = %+v, want feat-a", spy.Calls[0].Feature)
	}
}

func TestExecuteRecovery_ResumeEligibleImplementStampsPendingIntent(t *testing.T) {
	stateDir := t.TempDir()
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:                  "recovery-resume-eligible",
		CurrentPhase:        feature.PhaseImplement,
		Status:              feature.StatusImplementing,
		CurrentIteration:    2,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		Models:              config.ModelConfig{Implementation: "codex:model-a"},
		Repos:               []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
		Artifacts:           map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	iterDir := filepath.Join(agent.ActiveImplementDir(stateDir, f), "iteration-02")
	if err := agent.WriteResumeRecord(iterDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-recovery-123",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             2,
		RunNumber:             1,
		OrchestratorSessionID: "recovery-resume-eligible-phase-01-impl-02",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}

	items := fakeRecoveryItems(itemSpec{
		FeatureID:    f.ID,
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
	})
	items[0].Feature = f
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	store := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{
		Recovery:  &fakeRecoveryOp{},
		Lifecycle: lifecycleForFeature(f),
		Store:     store,
		PhaseRunner: &agent.PhaseRunner{
			StateDir: stateDir,
			Registry: registry,
		},
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn((&fakeRunMultiRepoImpl{}).Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}
	record, err := agent.ReadResumeRecord(iterDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if record == nil || !record.PendingResume {
		t.Errorf("resume record = %#v, want recovery-stamped pending intent", record)
	}
	if err := agent.NewResumeCoordinator(iterDir).ClearPending(time.Now()); err != nil {
		t.Fatalf("ClearPending() error = %v", err)
	}
}

func TestExecuteRecovery_ResumeIneligibleImplementMarksFreshFallback(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		record     string
		runNumber  int
		wantReason string
	}{
		{
			name:       "model changed",
			configured: "codex:model-a",
			record:     "model-b",
			runNumber:  1,
			wantReason: string(agent.ResumeReasonModelChanged),
		},
		{
			name:       "run sealed",
			configured: "codex:model-a",
			record:     "model-a",
			runNumber:  2,
			wantReason: string(agent.ResumeReasonRunSealed),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir := t.TempDir()
			planPath := writeTempFile(t, "plan.md", "# plan")
			f := &feature.Feature{
				ID:                  "recovery-fallback-" + strings.ReplaceAll(test.name, " ", "-"),
				CurrentPhase:        feature.PhaseImplement,
				Status:              feature.StatusImplementing,
				CurrentIteration:    2,
				CurrentRoadmapPhase: 1,
				ActiveTimingKey:     "phase-1-impl",
				ActiveRun:           1,
				Models:              config.ModelConfig{Implementation: test.configured},
				Repos:               []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
				Artifacts:           map[string]string{"plan": planPath},
			}
			writeExecOrderNextToPlan(t, planPath, f.Repos)
			iterDir := filepath.Join(agent.ActiveImplementDir(stateDir, f), "iteration-02")
			originalSessionID := "thread-before-fallback"
			if err := agent.WriteResumeRecord(iterDir, agent.ResumeRecord{
				ProviderSessionID:     originalSessionID,
				Provider:              "codex",
				ResolvedModel:         test.record,
				PhaseKey:              "phase-1-impl",
				Iteration:             2,
				RunNumber:             test.runNumber,
				OrchestratorSessionID: f.ID + "-phase-01-impl-02",
				CreatedAt:             time.Now(),
				UpdatedAt:             time.Now(),
			}); err != nil {
				t.Fatalf("WriteResumeRecord() error = %v", err)
			}
			items := fakeRecoveryItems(itemSpec{
				FeatureID:    f.ID,
				CurrentPhase: feature.PhaseImplement,
				Status:       feature.StatusImplementing,
			})
			items[0].Feature = f
			registry := llm.NewRegistry()
			registry.Register(&codex.Provider{})
			spy := &fakeRunMultiRepoImpl{}
			o := orchestrator.New(orchestrator.Deps{
				Recovery:  &fakeRecoveryOp{},
				Lifecycle: lifecycleForFeature(f),
				Store:     newFeatureStore(f),
				PhaseRunner: &agent.PhaseRunner{
					StateDir: stateDir,
					Registry: registry,
				},
			}, orchestrator.Hooks{})
			o.SetRunMultiRepoImplFn(spy.Fn())

			err := o.ExecuteRecovery(context.Background(), items, map[string]ports.RecoveryAction{
				ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
			})
			if err != nil {
				t.Fatalf("ExecuteRecovery() error = %v", err)
			}
			if spy.numCalls() != 1 {
				t.Fatalf("fresh relaunches = %d, want 1", spy.numCalls())
			}
			record, err := agent.ReadResumeRecord(iterDir)
			if err != nil {
				t.Fatalf("ReadResumeRecord() error = %v", err)
			}
			if record == nil ||
				record.ProviderSessionID != originalSessionID ||
				record.PendingResume ||
				record.FreshFallbackCount != 1 ||
				record.FreshFallbackReason != test.wantReason {
				t.Errorf("resume record = %#v, want retained identity and fallback reason %q", record, test.wantReason)
			}
		})
	}
}

func TestExecuteRecovery_ClaimsKnowledgeBaseRepositorySessions(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	store := feature.NewStore(cpr.stateDir)
	manager := feature.NewManager(store, config.NewDefault())
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	cpr.pr.FeatureStore = store
	cpr.pr.Registry = registry
	cpr.pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		cpr.mu.Lock()
		cpr.capturedOpts = append(cpr.capturedOpts, opts)
		cpr.mu.Unlock()
		return []string{"echo", "test"}, nil, &session.SessionOpts{
			PIDDir:                opts.PIDDir,
			InitialPrompt:         opts.Prompt,
			ProviderName:          "codex",
			ResolvedModel:         "model-a",
			RepoName:              opts.RepoName,
			SupportsSessionResume: true,
		}, nil
	}
	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: t.TempDir()},
		{Name: "repo-b", Path: t.TempDir()},
	}
	f := &feature.Feature{
		ID:            "recovery-kb-provider-resume",
		Name:          "Recovery KB provider resume",
		Slug:          "recovery-kb-provider-resume",
		Status:        feature.StatusBuildingKB,
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
		KBStatus: map[string]string{
			"repo-a": "pending",
			"repo-b": "pending",
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	now := time.Now()
	items := make([]ports.RecoveryItem, 0, len(repos))
	actions := make(map[string]ports.RecoveryAction, len(repos))
	for i, repo := range repos {
		if err := agent.WriteResumeRecord(agent.KBResumeDir(cpr.stateDir, f, repo.Name), agent.ResumeRecord{
			ProviderSessionID:     "thread-" + repo.Name,
			Provider:              "codex",
			ResolvedModel:         "model-a",
			PhaseKey:              feature.PhaseKnowledgeBase.DirName(),
			ChildKey:              repo.Name,
			RunNumber:             1,
			OrchestratorSessionID: f.ID + "-kb-" + repo.Name,
			CreatedAt:             now,
			UpdatedAt:             now,
		}); err != nil {
			t.Fatalf("WriteResumeRecord(%s) error = %v", repo.Name, err)
		}
		items = append(items, ports.RecoveryItem{
			PIDFile: session.PIDFile{
				PID:       999999900 + i,
				FeatureID: f.ID,
				Phase:     feature.PhaseKnowledgeBase.String(),
				RepoName:  repo.Name,
			},
			Feature:  f,
			RepoName: repo.Name,
		})
		actions[ports.RecoveryActionKey(f.ID, repo.Name)] = ports.RecoveryResume
		repoName := repo.Name
		t.Cleanup(func() {
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), f.ID)
		})
	}

	o := orchestrator.New(orchestrator.Deps{
		Recovery:    &fakeRecoveryOp{},
		Lifecycle:   manager,
		Store:       store,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}

	builds := cpr.capturedByPhase(feature.PhaseKnowledgeBase)
	if len(builds) != len(repos) {
		t.Fatalf("KB BuildSession calls = %d, want %d", len(builds), len(repos))
	}
	resumeIDs := make(map[string]string, len(builds))
	for _, build := range builds {
		resumeIDs[build.RepoName] = build.ResumeSessionID
	}
	for _, repo := range repos {
		if got := resumeIDs[repo.Name]; got != "thread-"+repo.Name {
			t.Errorf("repo %s ResumeSessionID = %q, want %q", repo.Name, got, "thread-"+repo.Name)
		}
	}
}

func TestExecuteRecovery_ResumeClaimConflictDegradesToFresh(t *testing.T) {
	stateDir := t.TempDir()
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:                  "recovery-claim-conflict",
		CurrentPhase:        feature.PhaseImplement,
		Status:              feature.StatusImplementing,
		CurrentIteration:    1,
		CurrentRoadmapPhase: 1,
		ActiveTimingKey:     "phase-1-impl",
		ActiveRun:           1,
		Models:              config.ModelConfig{Implementation: "codex:model-a"},
		Repos:               []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
		Artifacts:           map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	iterDir := filepath.Join(agent.ActiveImplementDir(stateDir, f), "iteration-01")
	if err := agent.WriteResumeRecord(iterDir, agent.ResumeRecord{
		ProviderSessionID:     "thread-conflict",
		Provider:              "codex",
		ResolvedModel:         "model-a",
		PhaseKey:              "phase-1-impl",
		Iteration:             1,
		RunNumber:             1,
		OrchestratorSessionID: f.ID + "-phase-01-impl-01",
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	registry := llm.NewRegistry()
	registry.Register(&codex.Provider{})
	coordinator := agent.NewResumeCoordinator(iterDir)
	heldClaim, eligibility, err := coordinator.Claim(f.ID, f, "codex:model-a", registry, time.Now())
	if err != nil || !eligibility.Eligible {
		t.Fatalf("holding Claim() = (%#v, %v), want eligible claim", eligibility, err)
	}
	defer heldClaim.Release(time.Now())

	items := fakeRecoveryItems(itemSpec{
		FeatureID:    f.ID,
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
	})
	items[0].Feature = f
	spy := &fakeRunMultiRepoImpl{}
	o := orchestrator.New(orchestrator.Deps{
		Recovery:  &fakeRecoveryOp{},
		Lifecycle: lifecycleForFeature(f),
		Store:     newFeatureStore(f),
		PhaseRunner: &agent.PhaseRunner{
			StateDir: stateDir,
			Registry: registry,
		},
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, map[string]ports.RecoveryAction{
		ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
	}); err != nil {
		t.Fatalf("ExecuteRecovery() error = %v", err)
	}
	record, err := agent.ReadResumeRecord(iterDir)
	if err != nil {
		t.Fatalf("ReadResumeRecord() error = %v", err)
	}
	if spy.numCalls() != 0 ||
		record == nil ||
		!record.PendingResume ||
		record.FreshFallbackCount != 0 {
		t.Errorf("duplicate recovery claim = calls %d, record %#v; want no second dispatch and original pending claim preserved", spy.numCalls(), record)
	}
}

func TestExecuteRecovery_Resume_InquiringFeature_RealManager_NoInvalidTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Store/Manager recovery transition regression in short mode; extended orchestrator run owns the transition-rule check")
	}

	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, &config.Config{})

	// Seed a crashed inquire feature directly via Store.Save so we bypass the
	// normal transition machinery. Critical setup:
	//   Status == StatusInquiring (matches crashed-inquire state)
	//   CurrentPhase == PhaseInquire (drives startPhase dispatch)
	f := &feature.Feature{
		ID:            "feat-inquire-resume",
		Name:          "resume inquire real manager",
		Slug:          "resume-inquire-real",
		Status:        feature.StatusInquiring,
		CurrentPhase:  feature.PhaseInquire,
		Pipeline:      feature.PipelineMoonshot,
		Repos:         []feature.FeatureRepo{{Name: repoName, Path: filepath.Join(stateDir, repoName)}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	items := []ports.RecoveryItem{{
		PIDFile:  session.PIDFile{PID: 999999998, FeatureID: f.ID},
		Feature:  f,
		RepoName: "",
	}}
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey(f.ID, ""): ports.RecoveryResume,
	}

	fakeRec := &fakeRecoveryOp{}

	// PhaseRunner is left nil — startInquire's `if o.deps.PhaseRunner != nil`
	// guard short-circuits the dispatch, so no external services are needed.
	// The test only exercises the idempotent-StartInquire path: success here
	// proves that ExecuteRecovery → startPhase → startInquire did NOT call
	// Lifecycle.StartInquire (which would have re-fired the invalid
	// StatusInquiring → StatusInquiring transition).
	o := orchestrator.New(orchestrator.Deps{
		Recovery:  fakeRec,
		Lifecycle: mgr,   // real *feature.Manager — enforces real transition rules
		Store:     store, // real *feature.Store
	}, orchestrator.Hooks{})

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		// Before the fix, err would read:
		//   "relaunch feat-inquire-resume phase inquire: start inquire:
		//    invalid transition from inquiring to inquiring"
		t.Fatalf("ExecuteRecovery returned error (regression — Inquiring→Inquiring re-attempted): %v", err)
	}

	// Post-condition: feature status preserved (no clobber).
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature after recovery: %v", err)
	}
	if loaded.Status != feature.StatusInquiring {
		t.Errorf("feature status = %s, want Inquiring (recovery must preserve crashed-inquire state)", loaded.Status)
	}
	if loaded.CurrentPhase != feature.PhaseInquire {
		t.Errorf("feature phase = %s, want PhaseInquire", loaded.CurrentPhase)
	}
}

// ---------------------------------------------------------------------------
// T15. Regression: resuming a crashed KB feature whose repos are now all
// fresh must NOT attempt the forbidden StatusBuildingKB → StatusInquiring
// transition.
//
// Background: `startKB`'s `allFresh` branch previously
// returned PhaseSkipped → PhaseInquire while the feature was still in
// StatusBuildingKB. The recursive `startPhase(PhaseInquire)` → `startInquire`
// then called `Lifecycle.StartInquire`, which resolves to
// `f.Transition(StatusInquiring)`. validTransitions[StatusBuildingKB]
// (feature/feature.go:488) lists only {Created, Failed, Interrupted}, so
// Inquiring is not a legal successor — the relaunch failed with:
//   "relaunch <fid> phase knowledge_base:
//    start inquire: invalid transition from building_kb to inquiring"
//
// The fix (orchestrator.go:488-496) introduces `finalizeKBForSkip`, which
// invokes `CompleteKnowledgeBase` (BuildingKB → Created) whenever startKB
// skips to Inquire from a recovered StatusBuildingKB feature. That bridge
// keeps startInquire's subsequent Created → Inquiring transition legal.
//
// This regression uses the real `*feature.Manager` + `*feature.Store` so
// the real transition rules are enforced — a future regression that removes
// `finalizeKBForSkip` or breaks the call sites in startKB (no-repos /
// allFresh) will surface as an "invalid transition from building_kb to
// inquiring" error here. Mock-based tests do NOT catch this class of bug.
//
// Test shape:
//   - Real git repo + matching head commit → fresh KB + fresh codebase index.
//   - BuildSessionFn short-circuits RunInquire *after* StartInquire's
//     transition has been applied, so we can observe that Inquiring was
//     reached without needing a live claude CLI.
//   - Post-condition: feature status == StatusInquiring (not BuildingKB or
//     Created), proving both CompleteKnowledgeBase AND StartInquire fired.
// ---------------------------------------------------------------------------

func TestExecuteRecovery_Resume_BuildingKBFeature_AllFresh_RealManager_NoInvalidTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real Store/Manager plus real-git KB freshness recovery regression in short mode; extended orchestrator run owns the transition-rule check")
	}

	// Real state dir with real Store + Manager so we exercise the real
	// transition rules (BuildingKB → Inquiring is invalid; BuildingKB →
	// Created → Inquiring is the legal path finalizeKBForSkip unlocks).
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, &config.Config{})

	// Real git repo so KB freshness check has a real HEAD to match against.
	// InitGitRepo creates an initial commit; CommitFile returns the hash.
	repoPath := testutil.InitGitRepo(t)
	headCommit := testutil.CommitFile(t, repoPath, "kb-sync.txt", "sync", "KB sync commit")

	// Seed fresh KB state.json + index.md for repo-a matched to headCommit so
	// IsKBFresh returns true, forcing startKB down the allFresh branch.
	kbDir := agent.KBStateDir(stateDir, repoName)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("mkdir KB: %v", err)
	}
	kbState, _ := json.Marshal(map[string]any{
		"head_commit":  headCommit,
		"last_updated": time.Now().UTC().Format(time.RFC3339),
		"version":      1,
	})
	if err := os.WriteFile(filepath.Join(kbDir, "state.json"), kbState, 0o644); err != nil {
		t.Fatalf("write KB state: %v", err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# KB\n"), 0o644); err != nil {
		t.Fatalf("write KB index: %v", err)
	}

	// Seed fresh codebase index state too — startKB's allFresh branch calls
	// RunCodebaseIndexForRepo, which short-circuits when the index is fresh.
	// If this were missing, RunCodebaseIndexForRepo would call BuildCodebaseIndex
	// against the real repo and add nondeterministic I/O to the test; the
	// transition rules we're exercising would still be covered, but leaving
	// the codebase index fresh keeps the test purely focused on the
	// orchestrator's transition bridging.
	idxDir := agent.IndexDir(kbDir)
	if err := os.MkdirAll(idxDir, 0o755); err != nil {
		t.Fatalf("mkdir index: %v", err)
	}
	idxState, _ := json.Marshal(map[string]any{
		"head_commit":  headCommit,
		"last_updated": time.Now().UTC().Format(time.RFC3339),
		"version":      1,
		"symbol_count": 0,
		"file_count":   0,
	})
	if err := os.WriteFile(filepath.Join(idxDir, "index-state.json"), idxState, 0o644); err != nil {
		t.Fatalf("write index state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(idxDir, "index.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write index.json: %v", err)
	}

	// MockCommandRunner returning the real HEAD commit for any git invocation.
	// IsKBFresh and IsCodebaseIndexFresh both call GetCurrentCommit via this
	// runner — matching the seeded head_commit makes both checks succeed.
	cmd := mocks.NewMockCommandRunner()
	cmd.RunFn = func(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
		return []byte(headCommit + "\n"), nil
	}

	// Seed a crashed BuildingKB feature directly via Store.Save.
	f := &feature.Feature{
		ID:            "feat-kb-resume",
		Name:          "resume kb real manager",
		Slug:          "resume-kb-real",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineMoonshot,
		Repos:         []feature.FeatureRepo{{Name: repoName, Path: repoPath}},
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	items := []ports.RecoveryItem{{
		PIDFile:  session.PIDFile{PID: 999999997, FeatureID: f.ID, RepoName: repoName},
		Feature:  f,
		RepoName: repoName,
	}}
	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey(f.ID, repoName): ports.RecoveryResume,
	}

	// PhaseRunner: StateDir + CommandRunner required by startKB's
	// IsKBFresh path; SessionManager is a mock that never reaches
	// StartSession (BuildSessionFn returns an error first).
	//
	// BuildSessionFn returns a sentinel error so RunInquire short-circuits
	// *after* Lifecycle.StartInquire has already fired its transition.
	// That lets us observe "the transitions succeeded" without needing a
	// live claude CLI to drive RunInquire end-to-end.
	pr := &agent.PhaseRunner{
		StateDir:       stateDir,
		CommandRunner:  cmd,
		FeatureStore:   store,
		SessionManager: mocks.NewMockSessionManager(),
	}
	sentinelBuildErr := errors.New("test-only: build session short-circuited")
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return nil, nil, nil, sentinelBuildErr
	}

	fakeRec := &fakeRecoveryOp{}
	o := orchestrator.New(orchestrator.Deps{
		Recovery:    fakeRec,
		Lifecycle:   mgr, // real *feature.Manager — enforces real transition rules
		Store:       store,
		PhaseRunner: pr,
		CmdRunner:   cmd,
	}, orchestrator.Hooks{})

	err := o.ExecuteRecovery(context.Background(), items, actions)

	// The error we expect is the test-only BuildSession shutdown propagated
	// through RunInquire. The error we MUST NOT see is the invalid-transition
	// message — that would prove finalizeKBForSkip didn't fire.
	if err != nil {
		if strings.Contains(err.Error(), "invalid transition from building_kb to inquiring") {
			t.Fatalf("regression: BuildingKB → Inquiring transition attempted without finalizeKBForSkip bridge: %v", err)
		}
		if !strings.Contains(err.Error(), sentinelBuildErr.Error()) {
			t.Errorf("unexpected error path (not the BuildSession short-circuit): %v", err)
		}
	}

	// Critical post-condition: both CompleteKnowledgeBase (BuildingKB →
	// Created) AND StartInquire (Created → Inquiring) fired. A feature stuck
	// in StatusBuildingKB would indicate finalizeKBForSkip never ran; a
	// feature in StatusCreated would indicate finalizeKBForSkip ran but
	// StartInquire then failed — both are regressions for this path.
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load feature after recovery: %v", err)
	}
	if loaded.Status != feature.StatusInquiring {
		t.Fatalf("feature status = %s, want StatusInquiring (regression — expected CompleteKnowledgeBase + StartInquire to both fire, bridging BuildingKB → Created → Inquiring)", loaded.Status)
	}
	if loaded.CurrentPhase != feature.PhaseInquire {
		t.Errorf("feature phase = %s, want PhaseInquire", loaded.CurrentPhase)
	}
}

// ---------------------------------------------------------------------------
// ScanRecovery cleanup-orphan-runs wiring tests
// ---------------------------------------------------------------------------

// TestScanRecovery_CallsCleanupOrphanRunsBeforeScan asserts that ScanRecovery
// invokes CleanupOrphanRuns on every feature returned by Store.List() BEFORE
// calling ScanForRecovery. Uses a call-order slice shared between the mock
// store's cleanup callback and the fake recovery op's scan callback.
func TestScanRecovery_CallsCleanupOrphanRunsBeforeScan(t *testing.T) {
	var mu sync.Mutex
	var callOrder []string

	mockStore := mocks.NewMockFeatureStore()
	mockStore.ListFn = func() ([]*feature.Feature, error) {
		return []*feature.Feature{
			{ID: "feat-a", ActiveRun: 1, RunCount: 1},
			{ID: "feat-b", ActiveRun: 1, RunCount: 1},
		}, nil
	}
	mockStore.CleanupOrphanRunsFn = func(id string) ([]int, error) {
		mu.Lock()
		defer mu.Unlock()
		callOrder = append(callOrder, "cleanup:"+id)
		return nil, nil
	}

	fake := &fakeRecoveryOp{
		ScanFn: func(ctx context.Context) ([]ports.RecoveryItem, error) {
			mu.Lock()
			defer mu.Unlock()
			callOrder = append(callOrder, "scan")
			return nil, nil
		},
	}

	o := orchestrator.New(
		orchestrator.Deps{Store: mockStore, Recovery: fake},
		orchestrator.Hooks{},
	)

	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"cleanup:feat-a", "cleanup:feat-b", "scan"}
	if len(callOrder) != len(want) {
		t.Fatalf("callOrder = %v, want %v", callOrder, want)
	}
	for i, w := range want {
		if callOrder[i] != w {
			t.Errorf("callOrder[%d] = %q, want %q (full: %v)", i, callOrder[i], w, callOrder)
		}
	}
	if got, want := mockStore.CleanupOrphanRunsCalls, []string{"feat-a", "feat-b"}; !equalStrings(got, want) {
		t.Errorf("CleanupOrphanRunsCalls = %v, want %v", got, want)
	}
}

func TestScanRecovery_ReconcilesAbandonedSetupBeforeScan(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	repoPath := testutil.InitGitRepo(t)
	cfg.Repos["test-repo"] = config.RepoConfig{Path: repoPath}
	mgr := feature.NewManager(store, cfg)

	f, err := mgr.Create("Abandoned Setup", "stale setup", []string{"test-repo"}, cfg.Defaults.Models, "", "", nil, feature.CreateOptions{QueueSetup: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	logPath := filepath.Join(store.RunDir(f.ID, 1), "setup", "attempt-01-output.txt")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("partial setup"), 0o644); err != nil {
		t.Fatalf("write setup log: %v", err)
	}
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		setup := ff.Run().Setup
		setup.LatestLogPath = logPath
		task := setup.Tasks["worktree:test-repo"]
		task.Status = feature.SetupStatusRunning
		task.Path = "/tmp/worktrees/abandoned-setup/test-repo"
		setup.Tasks[task.Key] = task
		return nil
	}); err != nil {
		t.Fatalf("seed setup state: %v", err)
	}

	var scanSawFailed bool
	fake := &fakeRecoveryOp{
		ScanFn: func(ctx context.Context) ([]ports.RecoveryItem, error) {
			got, err := store.Load(f.ID)
			if err != nil {
				t.Fatalf("load during scan: %v", err)
			}
			scanSawFailed = got.Status == feature.StatusFailed &&
				got.FailureType == feature.FailureWorktreeSetup &&
				got.Run().Setup != nil &&
				got.Run().Setup.Status == feature.SetupStatusFailed
			return nil, nil
		},
	}

	o := orchestrator.New(orchestrator.Deps{Store: store, Lifecycle: mgr, Recovery: fake}, orchestrator.Hooks{})
	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if !scanSawFailed {
		t.Fatal("ScanForRecovery ran before abandoned setup was marked failed")
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load reconciled feature: %v", err)
	}
	if loaded.Run().Setup.LatestLogPath != logPath {
		t.Fatalf("LatestLogPath = %q, want preserved %q", loaded.Run().Setup.LatestLogPath, logPath)
	}
	events := drainEvents(o)
	var setupFailed, recoveryScanned bool
	for _, ev := range events {
		switch ev.Type {
		case ports.SetupFailed:
			setupFailed = true
			if ev.FeatureID != f.ID || ev.RunNumber != 1 || ev.Attempt != 1 || ev.SetupLog != logPath {
				t.Fatalf("SetupFailed event = %+v, want reconciled setup metadata", ev)
			}
		case ports.RecoveryScanned:
			recoveryScanned = true
		case ports.PhaseStarted, ports.PhaseCompleted:
			t.Fatalf("unexpected phase telemetry for setup reconciliation: %+v", ev)
		}
	}
	if !setupFailed || !recoveryScanned {
		t.Fatalf("events = %+v, want SetupFailed and RecoveryScanned", events)
	}
}

// TestScanRecovery_CleanupError_SuppressesScan asserts that when cleanup
// returns an error for any feature, ScanRecovery propagates the error and
// does NOT call ScanForRecovery. This encodes the "recovery decisions always
// observe a reconciled run set" invariant.
func TestScanRecovery_CleanupError_SuppressesScan(t *testing.T) {
	wantErr := errors.New("cleanup kaboom")

	mockStore := mocks.NewMockFeatureStore()
	mockStore.ListFn = func() ([]*feature.Feature, error) {
		return []*feature.Feature{{ID: "feat-a", ActiveRun: 1, RunCount: 1}}, nil
	}
	mockStore.CleanupOrphanRunsFn = func(id string) ([]int, error) {
		return nil, wantErr
	}

	var scanCount int
	fake := &fakeRecoveryOp{
		ScanFn: func(ctx context.Context) ([]ports.RecoveryItem, error) {
			scanCount++
			return nil, nil
		},
	}

	hookLog := &recoveryHookLog{}
	o := orchestrator.New(
		orchestrator.Deps{Store: mockStore, Recovery: fake},
		orchestrator.Hooks{OnRecoveryScanned: hookLog.recordScan},
	)

	items, err := o.ScanRecovery(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "cleanup orphan runs") {
		t.Errorf("err = %q, want contains %q", err.Error(), "cleanup orphan runs")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Errorf("err = %q, want contains %q", err.Error(), "kaboom")
	}
	if items != nil {
		t.Errorf("items = %v, want nil on cleanup error", items)
	}
	if scanCount != 0 {
		t.Errorf("ScanForRecovery called %d times, want 0 (cleanup error should suppress scan)", scanCount)
	}
	if hookLog.scanCount() != 0 {
		t.Errorf("OnRecoveryScanned fired %d times; want 0", hookLog.scanCount())
	}
	events := drainEvents(o)
	for _, ev := range events {
		if ev.Type == ports.RecoveryScanned {
			t.Errorf("unexpected RecoveryScanned event emitted on cleanup error")
		}
	}
}

// TestScanRecovery_CleanupTolerates_PartialLoadError asserts that cleanup
// proceeds even when Store.List returns a PartialLoadError, invoking
// CleanupOrphanRuns on BOTH successfully-loaded features AND the IDs
// surfaced via PartialLoadError.Warnings. This is the critical invariant:
// partial-load features are often the ones that MOST need cleanup.
func TestScanRecovery_CleanupTolerates_PartialLoadError(t *testing.T) {
	mockStore := mocks.NewMockFeatureStore()
	mockStore.ListFn = func() ([]*feature.Feature, error) {
		return []*feature.Feature{
				{ID: "feat-good", ActiveRun: 1, RunCount: 1},
			},
			&feature.PartialLoadError{
				Warnings: []feature.LoadWarning{
					{ID: "feat-bad", Err: errors.New("corrupt")},
				},
			}
	}
	var cleanupIDs []string
	mockStore.CleanupOrphanRunsFn = func(id string) ([]int, error) {
		cleanupIDs = append(cleanupIDs, id)
		return nil, nil
	}

	var scanCount int
	fake := &fakeRecoveryOp{
		ScanFn: func(ctx context.Context) ([]ports.RecoveryItem, error) {
			scanCount++
			return nil, nil
		},
	}

	o := orchestrator.New(
		orchestrator.Deps{Store: mockStore, Recovery: fake},
		orchestrator.Hooks{},
	)

	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	// Partial load does NOT propagate as an error from cleanupOrphanRuns.
	if !containsString(cleanupIDs, "feat-good") {
		t.Errorf("cleanupIDs = %v, missing feat-good", cleanupIDs)
	}
	if !containsString(cleanupIDs, "feat-bad") {
		t.Errorf("cleanupIDs = %v, missing feat-bad (partial-load features must also get cleanup)", cleanupIDs)
	}
	if scanCount != 1 {
		t.Errorf("ScanForRecovery called %d times, want 1 (cleanup succeeded; scan should proceed)", scanCount)
	}
}

// equalStrings is a tiny helper for asserting slice equality in the ordering
// test, scoped locally to avoid collision with any package-level helper.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containsString reports whether sl contains v.
func containsString(sl []string, v string) bool {
	for _, s := range sl {
		if s == v {
			return true
		}
	}
	return false
}

// T10. TestExecuteRecovery_Resume_NeedUserInputCycle_DoesNotRelaunch
//
// When a recovery item carries a RepoName and the feature has a
// pending need-user-input gate for that repo, Resume must NOT relaunch the
// phase (that would bypass the gate's answer-validating, shared-gate-clearing
// single dispatch) and must NOT fall through to a generic feature-phase
// restart via startPhase. The process-level recovery action runs; the user
// answers the gate separately to resume.
func TestExecuteRecovery_Resume_NoCycle_FallsThroughToPhase(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "# plan")
	f := &feature.Feature{
		ID:           "feat-phase-recovery",
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
		Repos:        []feature.FeatureRepo{{Name: repoName, Path: repoAPath}},
		Artifacts:    map[string]string{"plan": planPath},
	}
	writeExecOrderNextToPlan(t, planPath, f.Repos)
	items := fakeRecoveryItems(itemSpec{
		FeatureID:    "feat-phase-recovery",
		CurrentPhase: feature.PhaseImplement,
		Status:       feature.StatusImplementing,
	})
	items[0].Feature = f

	actions := map[string]ports.RecoveryAction{
		ports.RecoveryActionKey("feat-phase-recovery", ""): ports.RecoveryResume,
	}

	fake := &fakeRecoveryOp{}
	store := newFeatureStore(f)
	lc := lifecycleForFeature(f)
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(orchestrator.Deps{
		Recovery:  fake,
		Lifecycle: lc,
		Store:     store,
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}
	if spy.numCalls() != 1 {
		t.Errorf("runMultiRepoImplFn calls = %d, want 1 (phase resume should dispatch)", spy.numCalls())
	}
}
