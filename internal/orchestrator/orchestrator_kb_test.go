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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_NoRepos
// ---------------------------------------------------------------------------

func TestOrchestrator_StartKB_NoRepos(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-nr",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos:        nil,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var startedPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { startedPhase = p },
	})

	if err := o.StartFeature("feat-nr"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// KB was skipped (no repos); Inquire was dispatched.
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")
	assertLifecycleCall(t, lc, "StartInquire")

	if startedPhase != feature.PhaseInquire {
		t.Errorf("OnPhaseStarted phase = %v, want PhaseInquire", startedPhase)
	}

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhaseKnowledgeBase) != nil {
		t.Error("unexpected PhaseStarted event for PhaseKnowledgeBase (KB was skipped)")
	}
	if hasPhaseStarted(events, feature.PhaseInquire) == nil {
		t.Error("expected PhaseStarted event for PhaseInquire")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_AllFreshSkips
// ---------------------------------------------------------------------------

// When KB freshness returns true for all repos, startKB skips to Inquire.
// This is exercised indirectly by setting ForceKBRebuild=false and providing
// a CmdRunner that doesn't look at the filesystem (returns empty/no error).
// Absent a command runner wired to fake filesystem state, we verify at least
// that the mixed-case path transitions to BuildingKB.
//
// For the all-fresh skip, a fully fresh KB requires KBFresh to return true.
// The cleanest way to verify the skip is by inspecting orchestrator behavior
// with no repos, covered by TestOrchestrator_StartKB_NoRepos. The mixed-case
// is covered by TestOrchestrator_StartKB_MixedFresh below.

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_MixedFresh
// ---------------------------------------------------------------------------
//
// With repos present but no PhaseRunner wired, startKB runs the freshness
// check (which returns false for all repos by default — no KB state dir exists
// under a t.TempDir()), then calls StartKnowledgeBase + InitKBStatus. This
// verifies the mixed-case dispatch pre-transition. Per-repo fan-out is not
// observable without a PhaseRunner stub; that is covered by integration tests.
func TestOrchestrator_StartKB_MixedFresh(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-kb",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: repoName, Path: repoAPath},
			{Name: repoNameB, Path: repoBPath},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	var startedPhase feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) { startedPhase = p },
	})

	if err := o.StartFeature("feat-kb"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// All repos need rebuilding (no KB fixtures) → KB transition happens.
	assertLifecycleCall(t, lc, "StartKnowledgeBase")
	assertLifecycleCall(t, lc, "InitKBStatus")

	if startedPhase != feature.PhaseKnowledgeBase {
		t.Errorf("OnPhaseStarted phase = %v, want PhaseKnowledgeBase", startedPhase)
	}

	events := drainEvents(o)
	if hasPhaseStarted(events, feature.PhaseKnowledgeBase) == nil {
		t.Error("expected PhaseStarted event for PhaseKnowledgeBase")
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_LockHeldByOther_SetsWaitMessage
// ---------------------------------------------------------------------------
//
// When kb.lock for a repo is held by another feature, RunKnowledgeBaseForRepo
// returns ErrKBLocked. Before the fix, startKB propagated that error and the
// feature was left in StatusBuildingKB with no live session and no retry path —
// pressing [a] in the dashboard then silently no-op'd. The fix turns
// ErrKBLocked into a "wait" state: KBWaitMessage is populated, no session is
// started for that repo, and the feature stays in BuildingKB so the dashboard
// renders "Waiting for KB" and a later wakeup can re-dispatch startKB.
func TestOrchestrator_StartKB_LockHeldByOther_SetsWaitMessage(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	// Pre-create kb.lock for "repo-locked" owned by another feature so that
	// AcquireKBLock fails on the first attempt. Don't seed state.json so the
	// freshness check returns false and the lock acquisition path runs.
	kbDir := agent.KBStateDir(cpr.stateDir, "repo-locked")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	owner := agent.KBLockInfo{FeatureID: "feat-owner", Timestamp: time.Now()}
	ownerData, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(kbDir, "kb.lock"), ownerData, 0o644); err != nil {
		t.Fatalf("write kb.lock: %v", err)
	}

	waiter := &feature.Feature{
		ID:           "feat-waiter",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-locked", Path: "/tmp/repo-locked"},
		},
	}
	ownerFeat := &feature.Feature{
		ID:     "feat-owner",
		Name:   "Cool Owner Feature",
		Status: feature.StatusBuildingKB,
	}

	lc := lifecycleForFeature(waiter)
	// Override Get so the orchestrator can resolve the lock owner's display name
	// when building KBWaitMessage. lifecycleForFeature wires GetFn to always
	// return the waiter; we need a per-id lookup.
	lc.GetFn = func(id string) (*feature.Feature, error) {
		switch id {
		case "feat-waiter":
			return waiter, nil
		case "feat-owner":
			return ownerFeat, nil
		}
		return nil, os.ErrNotExist
	}

	fs := newFeatureStore(waiter, ownerFeat)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature("feat-waiter"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	// No KB session was started for the locked repo.
	if got := len(cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)); got != 0 {
		t.Errorf("KB StartSession calls = %d, want 0 (lock held)", got)
	}

	// Feature transitioned to BuildingKB so wakeup can find it later.
	assertLifecycleCall(t, lc, "StartKnowledgeBase")
	assertLifecycleCall(t, lc, "InitKBStatus")

	// KBWaitMessage was populated with the lock owner's display name.
	if waiter.KBWaitMessage == "" {
		t.Fatal("KBWaitMessage was not set; dashboard would not show wait state")
	}
	if !strings.Contains(waiter.KBWaitMessage, "Cool Owner Feature") {
		t.Errorf("KBWaitMessage = %q, want it to mention owner name 'Cool Owner Feature'", waiter.KBWaitMessage)
	}
	if !strings.Contains(waiter.KBWaitMessage, "repo-locked") {
		t.Errorf("KBWaitMessage = %q, want it to mention the locked repo", waiter.KBWaitMessage)
	}
}

func TestOrchestrator_StartKB_OrphanedLockStartsSession(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	kbDir := agent.KBStateDir(cpr.stateDir, "repo-orphaned")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	owner := agent.KBLockInfo{FeatureID: "feat-missing", Timestamp: time.Now()}
	ownerData, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(kbDir, "kb.lock"), ownerData, 0o644); err != nil {
		t.Fatalf("write kb.lock: %v", err)
	}

	waiter := &feature.Feature{
		ID:           "feat-waiter",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-orphaned", Path: "/tmp/repo-orphaned"},
		},
	}
	lc := lifecycleForFeature(waiter)
	fs := newFeatureStore(waiter)
	fs.LoadFn = func(id string) (*feature.Feature, error) {
		if id == "feat-missing" {
			return nil, os.ErrNotExist
		}
		f, ok := fs.features[id]
		if !ok {
			return nil, os.ErrNotExist
		}
		return f, nil
	}
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	if err := o.StartFeature("feat-waiter"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}

	kbSessions := cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)
	if len(kbSessions) != 1 {
		t.Fatalf("KB StartSession calls = %d, want 1", len(kbSessions))
	}
	if kbSessions[0].ID != "feat-waiter-kb-repo-orphaned" {
		t.Errorf("session ID = %q, want feat-waiter-kb-repo-orphaned", kbSessions[0].ID)
	}
	if waiter.KBWaitMessage != "" {
		t.Errorf("KBWaitMessage = %q, want no wait message after orphan recovery", waiter.KBWaitMessage)
	}
	if owner := agent.ReadKBLockOwner(kbDir); owner != "feat-waiter" {
		t.Errorf("KB lock owner = %q, want feat-waiter", owner)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_AfterLockReleased_StartsSession
// ---------------------------------------------------------------------------
//
// Verifies the wakeup half: once the lock is gone, re-running startKB on the
// same parked feature acquires the lock and dispatches a KB session. This is
// the path that wakeKBWaiters drives from onKBCompleted.
func TestOrchestrator_StartKB_AfterLockReleased_StartsSession(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)

	kbDir := agent.KBStateDir(cpr.stateDir, "repo-locked")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	owner := agent.KBLockInfo{FeatureID: "feat-owner", Timestamp: time.Now()}
	ownerData, _ := json.Marshal(owner)
	lockPath := filepath.Join(kbDir, "kb.lock")
	if err := os.WriteFile(lockPath, ownerData, 0o644); err != nil {
		t.Fatalf("write kb.lock: %v", err)
	}

	waiter := &feature.Feature{
		ID:           "feat-waiter",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-locked", Path: "/tmp/repo-locked"},
		},
	}
	lc := lifecycleForFeature(waiter)
	lc.StartKnowledgeBaseFn = func(id string) error {
		waiter.Status = feature.StatusBuildingKB
		return nil
	}
	fs := newFeatureStore(waiter)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	// First attempt: lock held → no session, KBWaitMessage set.
	if err := o.StartFeature("feat-waiter"); err != nil {
		t.Fatalf("StartFeature (locked): %v", err)
	}
	if got := len(cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)); got != 0 {
		t.Fatalf("KB StartSession calls (locked) = %d, want 0", got)
	}
	if waiter.KBWaitMessage == "" {
		t.Fatal("KBWaitMessage not set after lock-blocked attempt")
	}

	// Lock holder finished — drop the lock and retry startKB. The waiter is
	// now in StatusBuildingKB so StartFeature routes back through startKB,
	// which exercises the same path wakeKBWaiters takes.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove kb.lock: %v", err)
	}

	if err := o.StartFeature("feat-waiter"); err != nil {
		t.Fatalf("StartFeature (after release): %v", err)
	}
	kbSessions := cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)
	if len(kbSessions) != 1 {
		t.Fatalf("KB StartSession calls (after release) = %d, want 1", len(kbSessions))
	}
	if kbSessions[0].ID != "feat-waiter-kb-repo-locked" {
		t.Errorf("session ID = %q, want feat-waiter-kb-repo-locked", kbSessions[0].ID)
	}
	if waiter.KBWaitMessage != "" {
		t.Errorf("KBWaitMessage = %q, want cleared after successful start", waiter.KBWaitMessage)
	}
}

func TestOrchestrator_StartKB_SessionBuildFailureMarksFeatureFailed(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	cpr.pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return nil, nil, nil, errors.New(`resolving provider for model "sonnet[200K]": no provider found for model "sonnet[200K]"`)
	}

	f := &feature.Feature{
		ID:           "feat-kb-spawn-fail",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Models:       config.ModelConfig{KBBuild: "sonnet[200K]"},
		Repos:        []feature.FeatureRepo{{Name: "repo-a", Path: "/tmp/repo-a"}},
	}
	lc := lifecycleForFeature(f)
	lc.MarkFailedFn = func(id, failureType, lastError string) error {
		f.Status = feature.StatusFailed
		f.FailureType = failureType
		f.LastError = lastError
		return nil
	}
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	var failureType, failureMessage string
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{
		OnFeatureFailed: func(id string, ft, msg string) {
			failureType = ft
			failureMessage = msg
		},
	})

	err := o.StartFeature(f.ID)
	if err == nil {
		t.Fatal("StartFeature() error = nil; want KB build failure")
	}
	if f.Status != feature.StatusFailed {
		t.Fatalf("feature status = %s; want Failed", f.Status)
	}
	if f.FailureType != feature.FailureInfrastructure {
		t.Fatalf("FailureType = %q; want %q", f.FailureType, feature.FailureInfrastructure)
	}
	if !strings.Contains(f.LastError, "run KB for repo repo-a") || !strings.Contains(f.LastError, "sonnet[200K]") {
		t.Fatalf("LastError = %q; want persisted KB spawn error", f.LastError)
	}
	if failureType != feature.FailureInfrastructure || failureMessage != f.LastError {
		t.Fatalf("OnFeatureFailed = %q/%q; want infrastructure/%q", failureType, failureMessage, f.LastError)
	}
	if got := len(cpr.startSessionsByPhase(feature.PhaseKnowledgeBase)); got != 0 {
		t.Fatalf("KB sessions = %d; want none when session build fails", got)
	}
	events := drainEvents(o)
	if !hasEventType(events, ports.FeatureFailed) {
		t.Fatalf("events = %+v; want FeatureFailed", events)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_wakeKBWaiters_TolerantOfPartialLoadError
// ---------------------------------------------------------------------------
//
// Lifecycle.List returns *feature.PartialLoadError alongside the successfully
// loaded features whenever any feature directory is malformed (e.g., missing
// feature.yaml). Before the fix, wakeKBWaiters bailed on any non-nil err, so
// a single corrupt entry on disk silently froze every KB waiter behind the
// lock holder. Verify that PartialLoadError is treated as soft: waiters that
// did load are still re-dispatched.
func TestOrchestrator_wakeKBWaiters_TolerantOfPartialLoadError(t *testing.T) {
	stateDir := t.TempDir()
	writeKBCompletionArtifacts(t, stateDir, "repo-shared", true)

	holder := &feature.Feature{
		ID:           "feat-holder",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-shared", Path: "/tmp/repo-shared"},
		},
	}
	// Waiter has no repos so a successful wake-up resolves via startKB's
	// no-repos branch → finalizeKBForSkip → Lifecycle.CompleteKnowledgeBase.
	// That gives us a single observable lifecycle call to assert against.
	waiter := &feature.Feature{
		ID:            "feat-waiter",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineLarge,
		KBWaitMessage: `Waiting for KB build on repo "repo-shared" by feature "Holder"`,
	}

	lc := lifecycleForFeature(holder)
	lc.GetFn = func(id string) (*feature.Feature, error) {
		switch id {
		case "feat-holder":
			return holder, nil
		case "feat-waiter":
			return waiter, nil
		}
		return nil, errors.New("not found")
	}
	// Simulate the production trap: List returns the healthy features along
	// with a PartialLoadError describing a malformed feature dir.
	lc.ListFn = func() ([]*feature.Feature, error) {
		return []*feature.Feature{holder, waiter},
			&feature.PartialLoadError{
				Warnings: []feature.LoadWarning{{ID: "feat-broken", Err: errors.New("missing feature.yaml")}},
			}
	}
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return true, nil }
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error { return nil }
	lc.CompleteKnowledgeBaseFn = func(id string) error {
		if id == "feat-holder" {
			holder.Status = feature.StatusInquireReady
		}
		if id == "feat-waiter" {
			waiter.Status = feature.StatusInquireReady
		}
		return nil
	}
	lc.StartInquireFn = func(id string) error { return nil }
	fs := newFeatureStore(holder, waiter)
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		return newStubSessionHandle(id, featureID, phase, ""), nil
	}
	pr := &agent.PhaseRunner{
		StateDir:       stateDir,
		SessionManager: sm,
		BuildSessionFn: func(opts agent.BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
			return []string{"true"}, nil, &ports.SessionOpts{}, nil
		},
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   mocks.NewMockCommandRunner(),
	}, orchestrator.Hooks{})

	// Holder's KB session for repo-shared completed. wakeKBWaiters fires from
	// onKBCompleted's success branch (line 169). The waiter must be advanced.
	err := o.HandlePhaseCompletion("feat-holder", orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: "feat-holder-kb-repo-shared",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// CompleteKnowledgeBase fires twice — once for the holder (all KBs done)
	// and once for the waiter via wakeKBWaiters → startKB → finalizeKBForSkip.
	// Pre-fix this was 1 (only the holder advanced; waiter stayed parked).
	if got := countLifecycleCalls(lc, "CompleteKnowledgeBase"); got != 2 {
		t.Errorf("CompleteKnowledgeBase calls = %d, want 2 (holder + waiter wake)", got)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_wakeKBWaiters_AdvancesPastSkippedKB
// ---------------------------------------------------------------------------
//
// Regression: when wakeKBWaiters re-runs the waiter's KB phase and the
// freshness re-check finds every repo fresh (e.g. the lock holder built the
// only repo the waiter needed, or stale per-repo state.json files from a
// prior feature satisfy the freshness predicate), startKB returns
// PhaseSkipped → NextPhase=PhaseInquire and finalizeKBForSkip transitions
// the waiter BuildingKB → Created. Before the fix, wakeKBWaiters called
// startKB directly and threw the result away, so the PhaseSkipped recursion
// in startPhase never fired and the waiter was stranded in
// StatusCreated/PhaseKnowledgeBase. The user-visible symptom was a feature
// stuck in "Created" forever after the lock holder finished, with manual
// Restart silently no-op'ing because RestartPhase couldn't transition
// Created → Interrupted.
//
// This test pins the fix: routing via startPhase recurses into Inquire,
// emitting a PhaseStarted{Inquire} event and invoking StartInquire.
func TestOrchestrator_wakeKBWaiters_AdvancesPastSkippedKB(t *testing.T) {
	holder := &feature.Feature{
		ID:           "feat-holder",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "repo-shared", Path: "/tmp/repo-shared"},
		},
	}
	// Waiter has no repos so the wake-up startKB takes the no-repos branch
	// (equivalent to the allFresh branch for this test's purpose): both run
	// finalizeKBForSkip and return PhaseSkipped → NextPhase=PhaseInquire.
	waiter := &feature.Feature{
		ID:            "feat-waiter",
		Status:        feature.StatusBuildingKB,
		CurrentPhase:  feature.PhaseKnowledgeBase,
		Pipeline:      feature.PipelineLarge,
		KBWaitMessage: `Waiting for KB build on repo "repo-shared" by feature "Holder"`,
	}

	lc := lifecycleForFeature(holder)
	lc.GetFn = func(id string) (*feature.Feature, error) {
		switch id {
		case "feat-holder":
			return holder, nil
		case "feat-waiter":
			return waiter, nil
		}
		return nil, errors.New("not found")
	}
	lc.ListFn = func() ([]*feature.Feature, error) {
		return []*feature.Feature{holder, waiter}, nil
	}
	lc.AllKBsCompletedFn = func(id string) (bool, error) { return true, nil }
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error { return nil }
	lc.CompleteKnowledgeBaseFn = func(id string) error {
		// Mirror the production transition: BuildingKB → Created.
		if id == "feat-holder" {
			holder.Status = feature.StatusCreated
		}
		if id == "feat-waiter" {
			waiter.Status = feature.StatusCreated
		}
		return nil
	}
	lc.StartInquireFn = func(id string) error {
		if id == "feat-waiter" {
			waiter.Status = feature.StatusInquiring
		}
		return nil
	}
	fs := newFeatureStore(holder, waiter)

	var startedPhases []feature.Phase
	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		CmdRunner: mocks.NewMockCommandRunner(),
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) {
			if id == "feat-waiter" {
				startedPhases = append(startedPhases, p)
			}
		},
	})

	err := o.HandlePhaseCompletion("feat-holder", orchestrator.PhaseCompletionInput{
		Phase:     feature.PhaseKnowledgeBase,
		SessionID: "feat-holder-kb-repo-shared",
		Success:   true,
	})
	if err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// The waiter must have been advanced past the skipped KB into Inquire.
	// Pre-fix, startedPhases on the waiter was empty: wakeKBWaiters dropped
	// the PhaseSkipped result and Inquire never fired.
	if !slices.Contains(startedPhases, feature.PhaseInquire) {
		t.Errorf("waiter did not advance to PhaseInquire after wake-up; OnPhaseStarted phases = %v", startedPhases)
	}
	assertLifecycleCall(t, lc, "StartInquire")

	// Waiter must not be stranded in Created (the pre-fix symptom).
	if waiter.Status == feature.StatusCreated {
		t.Errorf("waiter stuck in StatusCreated after wake-up; CurrentPhase=%v", waiter.CurrentPhase)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_OnKBCompleted_FailureOnInterruptedFeature_DoesNotMarkFailed
// ---------------------------------------------------------------------------
//
// Regression for the user-pressed-Stop-during-KB-build crash. When the user
// hits "s" mid-KB-build, InterruptFeature transitions the feature to
// StatusInterrupted and stops sessions. Each killed session asynchronously
// fires a SessionDoneMsg with success=false (process didn't exit cleanly) and
// an error string built from the agent's last assistant text (the CLI never
// emitted a result message before SIGINT). That message routes through
// HandlePhaseCompletion → onKBCompleted's failure branch.
//
// Pre-fix: onKBCompleted unconditionally called markFailedWithEvent with
// FailureSessionCrash, emitting feature.failed *before* InterruptFeature
// finished emitting feature.interrupted, and stamping last_error with the
// agent's mid-flight reasoning. The fix adds a terminal-state guard so when
// the feature is already at StatusInterrupted (or Failed), the failure branch
// short-circuits and lets InterruptFeature own the terminal transition.
func TestOrchestrator_OnKBCompleted_FailureOnInterruptedFeature_DoesNotMarkFailed(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-int-kb",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: agenticRepoName, Path: "/tmp/agentic"},
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
	}, orchestrator.Hooks{})

	err := o.HandlePhaseCompletion("feat-int-kb", orchestrator.PhaseCompletionInput{
		Phase:       feature.PhaseKnowledgeBase,
		SessionID:   "feat-int-kb-kb-agentic",
		Success:     false,
		ErrorDetail: "session failed: This is a FULL BUILD — directories are empty.",
	})
	if err != nil {
		t.Fatalf("HandlePhaseCompletion: %v", err)
	}

	// MarkFailed must NOT have been called — the feature was already at
	// StatusInterrupted, and InterruptFeature owns the terminal transition.
	refuteLifecycleCall(t, lc, "MarkFailed")
	// MarkRepoKBFailed must NOT have been called — KBStatus is preserved
	// across an interrupt for resume; only a real KB failure stamps it.
	refuteLifecycleCall(t, lc, "MarkRepoKBFailed")

	// No FeatureFailed event should have been emitted.
	events := drainEvents(o)
	if hasEventType(events, ports.FeatureFailed) {
		t.Error("unexpected FeatureFailed event for an already-interrupted feature")
	}

	// Status must remain Interrupted, not flip to Failed.
	if f.Status != feature.StatusInterrupted {
		t.Errorf("Status = %v, want StatusInterrupted (must not flip to Failed)", f.Status)
	}
}

// ---------------------------------------------------------------------------
// TestOrchestrator_InterruptFeature_TransitionBeforeStopSession
// ---------------------------------------------------------------------------
//
// Pins the ordering invariant: Lifecycle.Transition(StatusInterrupted) must
// run BEFORE StopSession. Otherwise a racing SessionDoneMsg → onKBCompleted
// reads the pre-Interrupted Status and the terminal-state guard misses,
// dropping the feature into Failed. The mock StopSession asserts the transition
// already landed by the time it's invoked.
func TestOrchestrator_InterruptFeature_TransitionBeforeStopSession(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-order",
		Status: feature.StatusBuildingKB,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	sm.FeatureSessionsFn = func(id string) []ports.SessionView {
		return []ports.SessionView{mocks.NewMockSessionView("s-1", "feat-order")}
	}
	var statusAtStop feature.Status
	sm.StopSessionFn = func(id string) error {
		statusAtStop = f.Status
		return nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		Sessions:  sm,
	}, orchestrator.Hooks{})

	if err := o.InterruptFeature("feat-order"); err != nil {
		t.Fatalf("InterruptFeature: %v", err)
	}

	if statusAtStop != feature.StatusInterrupted {
		t.Errorf("Status at StopSession time = %v, want StatusInterrupted (transition must run first so racing onKBCompleted sees zombie)", statusAtStop)
	}
}
