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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
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

	fs, manager := newStoredFeatureFixture(t)
	o := orchestrator.New(orchestrator.Deps{Recovery: fake, Store: fs, Manager: manager}, orchestrator.Hooks{})

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

	fs, manager := newStoredFeatureFixture(t)
	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Store: fs, Manager: manager},
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

	fs, manager := newStoredFeatureFixture(t)
	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Store: fs, Manager: manager},
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

	fs, manager := newStoredFeatureFixture(t)
	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Store: fs, Manager: manager},
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

	fs, manager := newStoredFeatureFixture(t)
	o := orchestrator.New(orchestrator.Deps{Recovery: fake, Store: fs, Manager: manager, Sessions: sessions}, orchestrator.Hooks{})

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

	spy := &fakeRunMultiRepoImpl{}
	hookLog := &recoveryHookLog{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake},
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
	// feat-a's relaunch fails because its id is absent from the real store, so
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
	store, manager := newStoredFeatureFixture(t)
	hookLog := &recoveryHookLog{}
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(
		orchestrator.Deps{Recovery: fake, Manager: manager, Store: store},
		orchestrator.Hooks{OnRecoveryAction: hookLog.recordAction},
	)
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err == nil {
		// The missing feature cannot relaunch; recovery events still emit.
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
	store, manager := newStoredFeatureFixture(t, f)

	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(orchestrator.Deps{
		Recovery: fake,
		Manager:  manager,
		Store:    store,
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != feature.StatusImplementing || loaded.CurrentPhase != feature.PhaseImplement {
		t.Fatalf("reloaded feature status/phase = %s/%s, want Implementing/Implement", loaded.Status, loaded.CurrentPhase)
	}
	if spy.numCalls() != 1 {
		t.Fatalf("runMultiRepoImplFn calls = %d, want 1", spy.numCalls())
	}
	if spy.Calls[0].Feature == nil || spy.Calls[0].Feature.ID != "feat-a" {
		t.Errorf("spy Feature.ID = %+v, want feat-a", spy.Calls[0].Feature)
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
		Recovery: fakeRec,
		Manager:  mgr,   // real *feature.Manager — enforces real transition rules
		Store:    store, // real *feature.Store
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
//   - Real git repo + matching head commit → fresh KB.
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

	// MockCommandRunner returning the real HEAD commit for any git invocation.
	// IsKBFresh calls GetCurrentCommit via this runner — matching the seeded
	// head_commit makes the check succeed.
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
		Manager:     mgr, // real *feature.Manager — enforces real transition rules
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

// TestScanRecovery_CleansOrphansBeforeScan checks that the real store removes
// stale run directories before the recovery adapter observes feature state.
func TestScanRecovery_CleansOrphansBeforeScan(t *testing.T) {
	features := []*feature.Feature{
		{ID: "feat-a", Name: "A", Status: feature.StatusCreated, ActiveRun: 1, RunCount: 1},
		{ID: "feat-b", Name: "B", Status: feature.StatusCreated, ActiveRun: 1, RunCount: 1},
	}
	store, manager := newStoredFeatureFixture(t, features...)
	var orphans []string
	for _, f := range features {
		orphan := store.RunDir(f.ID, 2)
		if err := os.MkdirAll(orphan, 0o755); err != nil {
			t.Fatal(err)
		}
		orphans = append(orphans, orphan)
	}
	fake := &fakeRecoveryOp{ScanFn: func(context.Context) ([]ports.RecoveryItem, error) {
		for _, orphan := range orphans {
			if _, err := os.Stat(orphan); !os.IsNotExist(err) {
				t.Fatalf("orphan %s still exists at scan: %v", orphan, err)
			}
		}
		return nil, nil
	}}
	o := orchestrator.New(orchestrator.Deps{Store: store, Manager: manager, Recovery: fake}, orchestrator.Hooks{})
	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatal(err)
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
		setup.Status = feature.SetupStatusRunning
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
				errcat.IsSetupFailure(got.FailureCode()) &&
				got.Run().Setup != nil &&
				got.Run().Setup.Status == feature.SetupStatusFailed
			return nil, nil
		},
	}

	o := orchestrator.New(orchestrator.Deps{Store: store, Manager: mgr, Recovery: fake}, orchestrator.Hooks{})
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

// TestScanRecovery_CleanupError_SuppressesScan uses a malformed on-disk
// feature record to make the real cleanup fail before recovery scanning.
func TestScanRecovery_CleanupError_SuppressesScan(t *testing.T) {
	store, manager := newStoredFeatureFixture(t, &feature.Feature{ID: "feat-bad", Name: "Bad", Status: feature.StatusCreated})
	featurePath := filepath.Join(store.BaseDir, "feat-bad", "feature.yaml")
	if err := os.WriteFile(featurePath, []byte("id: [not valid yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	var scanCount int
	fake := &fakeRecoveryOp{ScanFn: func(context.Context) ([]ports.RecoveryItem, error) { scanCount++; return nil, nil }}
	hookLog := &recoveryHookLog{}
	o := orchestrator.New(orchestrator.Deps{Store: store, Manager: manager, Recovery: fake}, orchestrator.Hooks{OnRecoveryScanned: hookLog.recordScan})
	items, err := o.ScanRecovery(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cleanup orphan runs") || !strings.Contains(err.Error(), "parsing feature file") {
		t.Fatalf("ScanRecovery error = %v, want real cleanup parse failure", err)
	}
	if items != nil || scanCount != 0 || hookLog.scanCount() != 0 {
		t.Fatalf("items=%v scanCount=%d hookCount=%d, want no scan after cleanup failure", items, scanCount, hookLog.scanCount())
	}
}

// A legacy-schema feature survives orphan cleanup, then the later cascade
// reconciliation reports the partial load before process scanning starts.
func TestScanRecovery_LegacyFeatureReportsReconcileError(t *testing.T) {
	store, manager := newStoredFeatureFixture(t,
		&feature.Feature{ID: "feat-good", Name: "Good", Status: feature.StatusCreated},
		&feature.Feature{ID: "feat-legacy", Name: "Legacy", Status: feature.StatusCreated},
	)
	legacyPath := filepath.Join(store.BaseDir, "feat-legacy", "feature.yaml")
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	currentVersion := "schema_version: " + strconv.Itoa(feature.SchemaVersionCurrent)
	if !strings.Contains(string(data), currentVersion) {
		t.Fatalf("feature record missing %q", currentVersion)
	}
	data = []byte(strings.Replace(string(data), currentVersion, "schema_version: 1", 1))
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var scanCount int
	fake := &fakeRecoveryOp{ScanFn: func(context.Context) ([]ports.RecoveryItem, error) { scanCount++; return nil, nil }}
	o := orchestrator.New(orchestrator.Deps{Store: store, Manager: manager, Recovery: fake}, orchestrator.Hooks{})
	if _, err := o.ScanRecovery(context.Background()); err == nil || !strings.Contains(err.Error(), "reconcile cascade deletes") {
		t.Fatalf("ScanRecovery error = %v, want cascade reconciliation error", err)
	}
	if scanCount != 0 {
		t.Fatalf("ScanForRecovery calls = %d, want 0", scanCount)
	}
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
	store, manager := newStoredFeatureFixture(t, f)
	spy := &fakeRunMultiRepoImpl{}

	o := orchestrator.New(orchestrator.Deps{
		Recovery: fake,
		Manager:  manager,
		Store:    store,
	}, orchestrator.Hooks{})
	o.SetRunMultiRepoImplFn(spy.Fn())

	if err := o.ExecuteRecovery(context.Background(), items, actions); err != nil {
		t.Fatalf("ExecuteRecovery: %v", err)
	}
	if spy.numCalls() != 1 {
		t.Errorf("runMultiRepoImplFn calls = %d, want 1 (phase resume should dispatch)", spy.numCalls())
	}
}
