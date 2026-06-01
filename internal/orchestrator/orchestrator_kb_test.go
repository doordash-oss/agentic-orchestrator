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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
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
			{Name: "repo-a", Path: "/tmp/repo-a"},
			{Name: "repo-b", Path: "/tmp/repo-b"},
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

// ---------------------------------------------------------------------------
// TestOrchestrator_StartKB_AfterLockReleased_StartsSession
// ---------------------------------------------------------------------------
//
// Verifies the wakeup half: once the lock is gone, re-running startKB on the
// same parked feature acquires the lock and dispatches a KB loop. This is the
// path that wakeKBWaiters drives after a holder releases kb.lock.
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
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return nil, nil
	})
	t.Cleanup(func() {
		for _, repoName := range loopRepos {
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), waiter.ID)
		}
	})

	// First attempt: lock held -> no loop, KBWaitMessage set.
	if err := o.StartFeature("feat-waiter"); err != nil {
		t.Fatalf("StartFeature (locked): %v", err)
	}
	if got := len(loopRepos); got != 0 {
		t.Fatalf("KB loop starts (locked) = %d, want 0", got)
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
	if len(loopRepos) != 1 {
		t.Fatalf("KB loop starts (after release) = %d, want 1", len(loopRepos))
	}
	if loopRepos[0] != "repo-locked" {
		t.Errorf("KB loop repo = %q, want repo-locked", loopRepos[0])
	}
	if waiter.KBWaitMessage != "" {
		t.Errorf("KBWaitMessage = %q, want cleared after successful start", waiter.KBWaitMessage)
	}
}

func TestOrchestrator_StartKB_AllFreshSkipsLoopAndReusesPersistentKB(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repo := feature.FeatureRepo{Name: "repo-fresh", Path: "/tmp/repo-fresh-main", WorktreePath: "/tmp/repo-fresh-worktree"}
	cpr.cmd.RunFn = func(ctx context.Context, name string, args []string, opts ports.CommandOpts) ([]byte, error) {
		if opts.Dir == repo.WorktreePath {
			return []byte("worktree-head\n"), nil
		}
		return []byte("main-head\n"), nil
	}
	kbDir := agent.KBStateDir(cpr.stateDir, repo.Name)
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# Fresh KB\n"), 0o644); err != nil {
		t.Fatalf("write KB index: %v", err)
	}
	if err := agent.SaveKBState(kbDir, &agent.KBState{HeadCommit: "worktree-head"}); err != nil {
		t.Fatalf("SaveKBState: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-kb-all-fresh",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		ActiveRun:    1,
		Repos:        []feature.FeatureRepo{repo},
	}
	lc := lifecycleForFeature(f)
	lc.StartInquireFn = func(id string) error {
		f.Status = feature.StatusInquiring
		return nil
	}
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		PhaseRunner: &agent.PhaseRunner{
			StateDir:      cpr.stateDir,
			CommandRunner: cpr.cmd,
		},
		CmdRunner: cpr.cmd,
	}, orchestrator.Hooks{})
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return nil, nil
	})
	o.SetRunInquireLoopFn(func(f *feature.Feature, kbInfos ...agent.KBInfo) (chan *agent.BlockingLoopResult, error) {
		return nil, nil
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	if len(loopRepos) != 0 {
		t.Fatalf("KB loop repos = %v, want none for fresh repo", loopRepos)
	}
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")
	assertLifecycleCall(t, lc, "StartInquire")
	if _, err := os.Stat(filepath.Join(cpr.stateDir, f.ID, "runs", "run-001", "knowledge-base")); !os.IsNotExist(err) {
		t.Fatalf("run-dir KB bookkeeping stat err = %v, want not exist for all-fresh skip", err)
	}
}

func TestOrchestrator_StartKB_ResumeSkipsFreshCompletedRepo(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repos := []feature.FeatureRepo{
		{Name: "repo-complete", Path: "/tmp/repo-complete"},
		{Name: "repo-inflight", Path: "/tmp/repo-inflight"},
	}
	kbDir := agent.KBStateDir(cpr.stateDir, "repo-complete")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# Completed KB\n"), 0o644); err != nil {
		t.Fatalf("write KB index: %v", err)
	}
	if err := agent.SaveKBState(kbDir, &agent.KBState{HeadCommit: "deadbeef"}); err != nil {
		t.Fatalf("SaveKBState: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-kb-resume",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		ActiveRun:    1,
		Repos:        repos,
		KBStatus: map[string]string{
			"repo-complete": "completed",
			"repo-inflight": "running",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return nil, nil
	})
	t.Cleanup(func() {
		for _, repoName := range loopRepos {
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), f.ID)
		}
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")
	refuteLifecycleCall(t, lc, "InitKBStatus")
	if !slices.Equal(loopRepos, []string{"repo-inflight"}) {
		t.Fatalf("KB loop repos = %v, want only repo-inflight", loopRepos)
	}
}

func TestOrchestrator_StartKB_ForcedResumeSkipsCompletedRepo(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repos := []feature.FeatureRepo{
		{Name: "repo-complete", Path: "/tmp/repo-complete"},
		{Name: "repo-inflight", Path: "/tmp/repo-inflight"},
	}
	kbDir := agent.KBStateDir(cpr.stateDir, "repo-complete")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# Completed KB\n"), 0o644); err != nil {
		t.Fatalf("write KB index: %v", err)
	}

	f := &feature.Feature{
		ID:             "feat-kb-forced-resume",
		Status:         feature.StatusBuildingKB,
		CurrentPhase:   feature.PhaseKnowledgeBase,
		Pipeline:       feature.PipelineLarge,
		ActiveRun:      1,
		ForceKBRebuild: true,
		Repos:          repos,
		KBStatus: map[string]string{
			"repo-complete": "completed",
			"repo-inflight": "running",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return nil, nil
	})
	t.Cleanup(func() {
		for _, repoName := range loopRepos {
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), f.ID)
		}
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	refuteLifecycleCall(t, lc, "StartKnowledgeBase")
	refuteLifecycleCall(t, lc, "InitKBStatus")
	refuteLifecycleCall(t, lc, "MarkRepoKBCompleted")
	if !slices.Equal(loopRepos, []string{"repo-inflight"}) {
		t.Fatalf("KB loop repos = %v, want only repo-inflight", loopRepos)
	}
}

func TestOrchestrator_StartKB_ForcedAllCompletedResumeClearsForceBeforeCreatedResume(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repo := feature.FeatureRepo{Name: "repo-complete", Path: "/tmp/repo-complete"}
	seedFreshKB(t, cpr.stateDir, repo.Name, "deadbeef")

	f := &feature.Feature{
		ID:             "feat-kb-forced-all-complete",
		Status:         feature.StatusBuildingKB,
		CurrentPhase:   feature.PhaseKnowledgeBase,
		Pipeline:       feature.PipelineLarge,
		ActiveRun:      1,
		ForceKBRebuild: true,
		Repos:          []feature.FeatureRepo{repo},
		KBStatus: map[string]string{
			repo.Name: "completed",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	stopAfterKBSkip := errors.New("stop after KB skip")
	startInquireCalls := 0
	lc.CompleteKnowledgeBaseFn = func(id string) error {
		return f.Transition(feature.StatusCreated)
	}
	lc.StartKnowledgeBaseFn = func(id string) error {
		return f.Transition(feature.StatusBuildingKB)
	}
	lc.StartInquireFn = func(id string) error {
		startInquireCalls++
		if startInquireCalls == 1 {
			return stopAfterKBSkip
		}
		return f.Transition(feature.StatusInquiring)
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle: lc,
		Store:     fs,
		PhaseRunner: &agent.PhaseRunner{
			StateDir:      cpr.stateDir,
			CommandRunner: cpr.cmd,
		},
		CmdRunner: cpr.cmd,
	}, orchestrator.Hooks{})
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return nil, nil
	})

	err := o.StartFeature(f.ID)
	if !errors.Is(err, stopAfterKBSkip) {
		t.Fatalf("StartFeature first error = %v, want %v", err, stopAfterKBSkip)
	}
	if f.Status != feature.StatusCreated {
		t.Fatalf("status after first resume = %v, want StatusCreated", f.Status)
	}

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature created-state resume: %v", err)
	}
	if f.ForceKBRebuild {
		t.Error("ForceKBRebuild should be cleared before the Created-state resume")
	}
	if countLifecycleCalls(lc, "StartKnowledgeBase") != 0 {
		t.Errorf("StartKnowledgeBase calls = %d, want 0", countLifecycleCalls(lc, "StartKnowledgeBase"))
	}
	if len(loopRepos) != 0 {
		t.Fatalf("KB loop repos after Created-state resume = %v, want none", loopRepos)
	}
	if f.Status != feature.StatusInquiring {
		t.Errorf("status after Created-state resume = %v, want StatusInquiring", f.Status)
	}
}

func TestOrchestrator_StartKB_ResumeSkipsInProcessLoopWithoutActiveSession(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repo := feature.FeatureRepo{Name: "repo-inflight", Path: "/tmp/repo-inflight"}
	f := &feature.Feature{
		ID:           "feat-kb-inprocess",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		ActiveRun:    1,
		Repos:        []feature.FeatureRepo{repo},
		KBStatus: map[string]string{
			repo.Name: "running",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	resultCh := make(chan *agent.BlockingLoopResult)
	var loopRepos []string
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopRepos = append(loopRepos, repo.Name)
		return resultCh, nil
	})
	t.Cleanup(func() {
		if agent.ReadKBLockOwner(agent.KBStateDir(cpr.stateDir, repo.Name)) == f.ID {
			select {
			case resultCh <- &agent.BlockingLoopResult{FinalStatus: agent.BlockingLoopStatusInterrupted}:
			case <-time.After(time.Second):
				t.Fatal("timed out stopping KB loop dispatcher")
			}
			waitForKBLockReleased(t, cpr.stateDir, repo.Name)
		}
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature (initial): %v", err)
	}
	if len(loopRepos) != 1 {
		t.Fatalf("KB loop starts after initial start = %d, want 1", len(loopRepos))
	}

	// Simulate a Smart Zone handoff gap: the per-repo blocking loop is still
	// alive, but there is no active provider session for activeKBSessionRepos
	// to see. startKB must still treat the repo as already owned in-process.
	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature (resume while loop alive): %v", err)
	}
	if len(loopRepos) != 1 {
		t.Fatalf("KB loop starts after resume = %d, want 1; repos=%v", len(loopRepos), loopRepos)
	}
}

func TestOrchestrator_KBLoopFailureCancelsSiblingLoopWithoutActiveSession(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "repo-b", Path: "/tmp/repo-b"},
	}
	f := &feature.Feature{
		ID:           "feat-kb-cancel-sibling",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		ActiveRun:    1,
		Repos:        repos,
		KBStatus: map[string]string{
			"repo-a": "running",
			"repo-b": "running",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	lc.GetFn = fs.Load
	repoAFinalized := make(chan struct{}, 1)
	lc.ListFn = func() ([]*feature.Feature, error) {
		features, err := fs.List()
		select {
		case repoAFinalized <- struct{}{}:
		default:
		}
		return features, err
	}
	lc.MarkFailedFn = func(id, ft, msg string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
	}
	lc.MarkRepoKBFailedFn = func(id, repoName, msg string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.KBStatus[repoName] = "failed: " + msg
			return nil
		})
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	loopContexts := make(map[string]context.Context)
	resultChans := make(map[string]chan *agent.BlockingLoopResult)
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopContexts[repo.Name] = ctx
		ch := make(chan *agent.BlockingLoopResult, 1)
		resultChans[repo.Name] = ch
		return ch, nil
	})
	t.Cleanup(func() {
		for repoName, ch := range resultChans {
			select {
			case ch <- &agent.BlockingLoopResult{FinalStatus: agent.BlockingLoopStatusInterrupted}:
			default:
			}
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), f.ID)
		}
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	for _, repo := range []string{"repo-a", "repo-b"} {
		if loopContexts[repo] == nil {
			t.Fatalf("missing loop context for %s; contexts=%v", repo, loopContexts)
		}
	}

	resultChans["repo-a"] <- &agent.BlockingLoopResult{
		FinalStatus: agent.BlockingLoopStatusProtocolViolation,
		LastError:   "repo-a protocol violation",
	}

	select {
	case <-loopContexts["repo-b"].Done():
	case <-time.After(time.Second):
		t.Fatal("repo-b KB loop context was not canceled after repo-a failed")
	}

	waitForKBLockReleased(t, cpr.stateDir, "repo-a")
	select {
	case <-repoAFinalized:
	case <-time.After(time.Second):
		t.Fatal("repo-a KB failure handler did not finish waiter wakeup")
	}
	resultChans["repo-b"] <- &agent.BlockingLoopResult{FinalStatus: agent.BlockingLoopStatusInterrupted}
	waitForKBLockReleased(t, cpr.stateDir, "repo-b")
}

func TestOrchestrator_KBLoopSuccessFinalizationErrorCancelsSiblingLoop(t *testing.T) {
	cpr := newCapturingPhaseRunner(t)
	repos := []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a"},
		{Name: "repo-b", Path: "/tmp/repo-b"},
	}
	kbDir := agent.KBStateDir(cpr.stateDir, "repo-a")
	if err := os.MkdirAll(kbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", kbDir, err)
	}
	if err := os.WriteFile(agent.KBPath(kbDir), []byte("# Repo A KB\n"), 0o644); err != nil {
		t.Fatalf("write KB index: %v", err)
	}

	f := &feature.Feature{
		ID:           "feat-kb-finalization-error",
		Status:       feature.StatusBuildingKB,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		ActiveRun:    1,
		Repos:        repos,
		KBStatus: map[string]string{
			"repo-a": "running",
			"repo-b": "running",
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	cpr.pr.FeatureStore = fs
	lc.GetFn = fs.Load
	wakeDone := make(chan struct{}, 2)
	lc.ListFn = func() ([]*feature.Feature, error) {
		features, err := fs.List()
		select {
		case wakeDone <- struct{}{}:
		default:
		}
		return features, err
	}
	lc.MarkRepoKBCompletedFn = func(id, repoName string) error {
		if repoName == "repo-a" {
			return errors.New("persist repo completion")
		}
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.KBStatus[repoName] = "completed"
			return nil
		})
	}
	lc.MarkRepoKBFailedFn = func(id, repoName, msg string) error {
		return fs.Modify(id, func(ff *feature.Feature) error {
			ff.KBStatus[repoName] = "failed: " + msg
			return nil
		})
	}
	markFailedDone := make(chan struct{}, 1)
	lc.MarkFailedFn = func(id, ft, msg string) error {
		err := fs.Modify(id, func(ff *feature.Feature) error {
			ff.Status = feature.StatusFailed
			ff.FailureType = ft
			ff.LastError = msg
			return nil
		})
		select {
		case markFailedDone <- struct{}{}:
		default:
		}
		return err
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    cpr.sm,
		PhaseRunner: cpr.pr,
		CmdRunner:   cpr.cmd,
	}, orchestrator.Hooks{})

	loopContexts := make(map[string]context.Context)
	resultChans := make(map[string]chan *agent.BlockingLoopResult)
	o.SetRunKnowledgeBaseLoopFn(func(ctx context.Context, f *feature.Feature, repo feature.FeatureRepo) (chan *agent.BlockingLoopResult, error) {
		loopContexts[repo.Name] = ctx
		ch := make(chan *agent.BlockingLoopResult, 1)
		resultChans[repo.Name] = ch
		return ch, nil
	})
	t.Cleanup(func() {
		for repoName, ch := range resultChans {
			select {
			case ch <- &agent.BlockingLoopResult{FinalStatus: agent.BlockingLoopStatusInterrupted}:
			default:
			}
			_ = agent.ReleaseKBLock(agent.KBStateDir(cpr.stateDir, repoName), f.ID)
		}
	})

	if err := o.StartFeature(f.ID); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	for _, repo := range []string{"repo-a", "repo-b"} {
		if loopContexts[repo] == nil {
			t.Fatalf("missing loop context for %s; contexts=%v", repo, loopContexts)
		}
	}

	resultChans["repo-a"] <- &agent.BlockingLoopResult{
		FinalStatus:   agent.BlockingLoopStatusSuccess,
		CanonicalPath: agent.KBPath(kbDir),
	}

	select {
	case <-loopContexts["repo-b"].Done():
	case <-time.After(time.Second):
		t.Fatal("repo-b KB loop context was not canceled after repo-a completion finalization failed")
	}
	select {
	case <-markFailedDone:
	case <-time.After(time.Second):
		t.Fatal("KB finalization error did not fail the feature")
	}
	waitForKBLockReleased(t, cpr.stateDir, "repo-a")
	select {
	case <-wakeDone:
	case <-time.After(time.Second):
		t.Fatal("repo-a KB finalization failure did not finish waiter wakeup")
	}

	reloaded, err := fs.Load(f.ID)
	if err != nil {
		t.Fatalf("Load(%q): %v", f.ID, err)
	}
	if reloaded.FailureType != feature.FailureInfrastructure {
		t.Fatalf("FailureType = %q, want %q", reloaded.FailureType, feature.FailureInfrastructure)
	}
	if got := reloaded.KBStatus["repo-a"]; !strings.Contains(got, "mark repo KB completed") {
		t.Fatalf("KBStatus[repo-a] = %q, want finalization failure", got)
	}

	resultChans["repo-b"] <- &agent.BlockingLoopResult{FinalStatus: agent.BlockingLoopStatusInterrupted}
	waitForKBLockReleased(t, cpr.stateDir, "repo-b")
	select {
	case <-wakeDone:
	case <-time.After(time.Second):
		t.Fatal("repo-b KB interruption did not finish waiter wakeup")
	}
}

func waitForKBLockReleased(t *testing.T, stateDir, repoName string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if owner := agent.ReadKBLockOwner(agent.KBStateDir(stateDir, repoName)); owner == "" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("kb.lock for repo %s was not released", repoName)
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
	kbDir := writeKBCompletionArtifacts(t, stateDir, "repo-shared", false, true)

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
		StateDir: stateDir,
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   mocks.NewMockCommandRunner(),
	}, orchestrator.Hooks{})

	// Holder's KB loop for repo-shared completed. wakeKBWaiters fires after
	// the loop completion releases kb.lock. The waiter must be advanced.
	err := o.HandlePhaseCompletion("feat-holder", orchestrator.PhaseCompletionInput{
		Phase:    feature.PhaseKnowledgeBase,
		RepoName: "repo-shared",
		KnowledgeBaseResult: &agent.BlockingLoopResult{
			FinalStatus:   agent.BlockingLoopStatusSuccess,
			CanonicalPath: agent.KBPath(kbDir),
		},
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
	stateDir := t.TempDir()
	kbDir := writeKBCompletionArtifacts(t, stateDir, "repo-shared", false, true)

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
		Lifecycle:   lc,
		Store:       fs,
		CmdRunner:   mocks.NewMockCommandRunner(),
		PhaseRunner: &agent.PhaseRunner{StateDir: stateDir},
	}, orchestrator.Hooks{
		OnPhaseStarted: func(id string, p feature.Phase) {
			if id == "feat-waiter" {
				startedPhases = append(startedPhases, p)
			}
		},
	})

	err := o.HandlePhaseCompletion("feat-holder", orchestrator.PhaseCompletionInput{
		Phase:    feature.PhaseKnowledgeBase,
		RepoName: "repo-shared",
		KnowledgeBaseResult: &agent.BlockingLoopResult{
			FinalStatus:   agent.BlockingLoopStatusSuccess,
			CanonicalPath: agent.KBPath(kbDir),
		},
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
// TestOrchestrator_KBSessionDoneOnInterruptedFeature_NoOps
// ---------------------------------------------------------------------------
//
// KB is loop-owned now: per-iteration SessionDone messages do not mutate the
// feature at all. This preserves the original interrupt invariant without
// reviving the retired single-shot KB completion path.
func TestOrchestrator_KBSessionDoneOnInterruptedFeature_NoOps(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-int-kb",
		Status:       feature.StatusInterrupted,
		CurrentPhase: feature.PhaseKnowledgeBase,
		Pipeline:     feature.PipelineLarge,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic"},
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
// run BEFORE StopSession. Racing completion handlers rely on seeing the
// terminal status. The mock StopSession asserts the transition already landed
// by the time it's invoked.
func TestOrchestrator_InterruptFeature_TransitionBeforeStopSession(t *testing.T) {
	f := &feature.Feature{
		ID:     "feat-order",
		Status: feature.StatusBuildingKB,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	sm := mocks.NewMockSessionManager()
	sm.FeatureSessionsFn = func(id string) []session.SessionView {
		return []session.SessionView{mocks.NewMockSessionView("s-1", "feat-order")}
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
		t.Errorf("Status at StopSession time = %v, want StatusInterrupted (transition must run first so racing completion handlers see terminal status)", statusAtStop)
	}
}
