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

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// fakeRecoveryNoop is a minimal ports.RecoveryOperator that returns zero
// items for scan and a no-op for execute. Used by the crash-recovery
// integration test to exercise Orchestrator.ScanRecovery's cleanup step
// without a live session layer.
type fakeRecoveryNoop struct {
	scanCalls int
}

func (f *fakeRecoveryNoop) ScanForRecovery(ctx context.Context) ([]ports.RecoveryItem, error) {
	f.scanCalls++
	return nil, nil
}

func (f *fakeRecoveryNoop) ExecuteRecovery(
	ctx context.Context,
	items []ports.RecoveryItem,
	actions map[string]ports.RecoveryAction,
) error {
	return nil
}

// TestRewindCrashAndRetry is the headline Phase 3 integration test. It
// drives a full feature lifecycle through an injected mid-populate crash,
// constructs a fresh orchestrator as the "restart", invokes ScanRecovery
// (which calls cleanup before the scan), then re-triggers RewindToPhase
// end-to-end. On-disk state is asserted at every checkpoint.
//
// Real Store, real Manager, fake recovery op (scan returns zero items).
// The test skips in -short mode because it exercises the full seal+fork
// transaction and the cleanup/rescan cycle.
func TestRewindCrashAndRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "repo")
	_ = os.MkdirAll(stateDir, 0o755)
	_ = os.MkdirAll(repoDir, 0o755)

	store := feature.NewStore(stateDir)
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Models: config.ModelConfig{
				Research:       "test-research",
				Planning:       "test-planning",
				Implementation: "test-impl",
				Review:         "test-review",
			},
			ExitCriteria:  "all tests pass",
			MaxIterations: 10,
			Pipeline:      "large",
		},
		Repos: map[string]config.RepoConfig{
			"test-repo": {Path: repoDir},
		},
	}
	mgr := feature.NewManager(store, cfg)
	// Integration test does not exercise worktrees or PRs.
	mgr.Worktrees = nil
	mgr.PRs = nil

	// --- Step 1: Create and advance feature ---------------------------------
	f, err := mgr.Create(
		"Crash Recovery Integration",
		"integration test: crash during rewind and recover",
		[]string{"test-repo"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, step := range []struct {
		name string
		fn   func(string) error
	}{
		{"StartInquire", mgr.StartInquire},
		{"CompleteInquire", mgr.CompleteInquire},
		{"StartResearch", mgr.StartResearch},
		{"CompleteResearch", mgr.CompleteResearch},
		{"StartDesign", mgr.StartDesign},
		{"CompleteDesign", mgr.CompleteDesign},
	} {
		if err := step.fn(f.ID); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	// Seed marker files under each phase dir so the carry-forward has
	// something real to copy.
	run1Dir := filepath.Join(stateDir, f.ID, "runs", "run-001")
	for _, phase := range []string{"inquire", "research", "design"} {
		dir := filepath.Join(run1Dir, phase)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir run-001/%s: %v", phase, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte(phase), 0o644); err != nil {
			t.Fatalf("write run-001/%s/marker.txt: %v", phase, err)
		}
	}

	// --- Step 2: Simulate a mid-populate crash -----------------------------
	// Drive SealAndForkRun directly with a populate that returns an error
	// AFTER writing a partial artifact, simulating a crash during the
	// carry-forward copy. The skeleton (committing:true) is already on disk
	// at this point; the bump does NOT run.
	tp := feature.PhaseResearch
	crashErr := errors.New("simulated mid-populate crash")
	_, err = store.SealAndForkRun(f.ID,
		func(oldRun *feature.Run) error {
			now := time.Now()
			oldRun.SealedAt = &now
			oldRun.SealReason = feature.SealReasonRewind
			oldRun.RewindTarget = &tp
			return nil
		},
		func(oldRun *feature.Run) (*feature.Run, error) {
			return &feature.Run{
				RunNumber:      oldRun.RunNumber + 1,
				CarriedFromRun: oldRun.RunNumber,
				Committing:     true,
			}, nil
		},
		func(oldRun, newRun *feature.Run) error {
			// Simulate a partial copy: write one file into run-002/inquire/
			// before "crashing".
			run2Dir := filepath.Join(stateDir, f.ID, "runs", "run-002", "inquire")
			if err := os.MkdirAll(run2Dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(run2Dir, "partial.txt"), []byte("partial"), 0o644); err != nil {
				return err
			}
			return crashErr
		},
	)
	if err == nil {
		t.Fatal("SealAndForkRun returned nil error, want wrapped crash")
	}
	if !errors.Is(err, crashErr) {
		t.Errorf("err = %v, want wrapped %v", err, crashErr)
	}

	// Post-crash assertions:
	//   - run-001 sealed (SealedAt stamped).
	//   - run-002/run.yaml exists with Committing:true (the skeleton).
	//   - feature.yaml still at ActiveRun:1.
	sealed, err := store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1) post-crash: %v", err)
	}
	if sealed.SealedAt == nil {
		t.Error("run-001 SealedAt is nil post-crash; seal step should have completed")
	}
	run2Yaml := filepath.Join(stateDir, f.ID, "runs", "run-002", "run.yaml")
	data, err := os.ReadFile(run2Yaml)
	if err != nil {
		t.Fatalf("run-002/run.yaml missing post-crash: %v", err)
	}
	var skel feature.Run
	if yerr := yaml.Unmarshal(data, &skel); yerr != nil {
		t.Fatalf("unmarshal skeleton: %v", yerr)
	}
	if !skel.Committing {
		t.Error("skeleton Committing=false, want true")
	}
	// Partial-copy evidence: partial.txt was written before the crash.
	if _, err := os.Stat(filepath.Join(stateDir, f.ID, "runs", "run-002", "inquire", "partial.txt")); err != nil {
		t.Errorf("partial copy evidence missing: %v", err)
	}
	// feature.yaml still at ActiveRun:1, RunCount:1.
	preRestart, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load pre-restart: %v", err)
	}
	if preRestart.ActiveRun != 1 || preRestart.RunCount != 1 {
		t.Errorf("post-crash ActiveRun/RunCount = %d/%d, want 1/1", preRestart.ActiveRun, preRestart.RunCount)
	}

	// --- Step 3: Restart simulation — fresh Orchestrator + ScanRecovery ---
	fakeRec := &fakeRecoveryNoop{}
	orch := orchestrator.New(
		orchestrator.Deps{Store: store, Recovery: fakeRec},
		orchestrator.Hooks{},
	)
	items, err := orch.ScanRecovery(context.Background())
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("ScanRecovery items = %d, want 0", len(items))
	}
	if fakeRec.scanCalls != 1 {
		t.Errorf("ScanForRecovery called %d times, want 1", fakeRec.scanCalls)
	}

	// Post-scan assertions:
	//   - run-002/ GONE (cleanup swept the committing:true skeleton).
	//   - run-001/ preserved.
	//   - feature.yaml still ActiveRun:1 (max-on-disk = 1 = ActiveRun; no rollback).
	if _, err := os.Stat(filepath.Join(stateDir, f.ID, "runs", "run-002")); !os.IsNotExist(err) {
		t.Errorf("run-002 still present after ScanRecovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, f.ID, "runs", "run-001", "run.yaml")); err != nil {
		t.Errorf("run-001 deleted or missing: %v", err)
	}
	postClean, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load post-clean: %v", err)
	}
	if postClean.ActiveRun != 1 || postClean.RunCount != 1 {
		t.Errorf("post-clean ActiveRun/RunCount = %d/%d, want 1/1", postClean.ActiveRun, postClean.RunCount)
	}
	if postClean.Run() == nil || !postClean.Run().IsSealed() {
		t.Fatal("post-clean Run() is not sealed; want sealed run-001")
	}

	// --- Step 4: Re-trigger the rewind via the manager ----------------------
	// Post-cleanup the feature's Status is whatever it was before the crash:
	// the crash simulation bypassed the manager's RewindToPhase lifecycle
	// marking, so Status is still StatusPlanReady. Drive back into
	// Implementing so the second RewindToPhase target is legal.
	_ = store.Modify(f.ID, func(fe *feature.Feature) error {
		fe.Status = feature.StatusImplementing
		fe.CurrentPhase = feature.PhaseImplement
		return nil
	})
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseResearch); err != nil {
		t.Fatalf("RewindToPhase after recovery: %v", err)
	}

	// Post-rewind assertions:
	//   - run-001 sealed (SealedAt may be updated — idempotent re-seal).
	//   - run-002/run.yaml exists with Committing:false, CarriedFromRun:1,
	//     CarriedPhases contains "inquire".
	//   - run-002/inquire/marker.txt exists (carried from run-001).
	//   - feature.yaml at ActiveRun:2, RunCount:2.
	finalSealed, err := store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1) post-rewind: %v", err)
	}
	if finalSealed.SealedAt == nil {
		t.Error("run-001 SealedAt is nil post-rewind")
	}
	finalRun2, err := store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2) post-rewind: %v", err)
	}
	if finalRun2.Committing {
		t.Error("run-002 Committing=true post-rewind, want false")
	}
	if finalRun2.CarriedFromRun != 1 {
		t.Errorf("run-002 CarriedFromRun = %d, want 1", finalRun2.CarriedFromRun)
	}
	hasInquire := false
	for _, p := range finalRun2.CarriedPhases {
		if p == "inquire" {
			hasInquire = true
			break
		}
	}
	if !hasInquire {
		t.Errorf("run-002 CarriedPhases = %v, want contains 'inquire'", finalRun2.CarriedPhases)
	}
	// Carry-forward copy landed the marker file.
	markerPath := filepath.Join(stateDir, f.ID, "runs", "run-002", "inquire", "marker.txt")
	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("run-002/inquire/marker.txt missing post-rewind: %v", err)
	}
	reloaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get post-rewind: %v", err)
	}
	if reloaded.ActiveRun != 2 || reloaded.RunCount != 2 {
		t.Errorf("post-rewind ActiveRun/RunCount = %d/%d, want 2/2", reloaded.ActiveRun, reloaded.RunCount)
	}
	// Reloaded run must NOT be sealed (fresh active run) and CarriedFromRun == 1.
	if reloaded.Run() == nil {
		t.Fatal("reloaded Run() is nil")
	}
	if reloaded.Run().IsSealed() {
		t.Error("reloaded Run() is sealed; want unsealed fresh run-002")
	}
	if reloaded.Run().CarriedFromRun != 1 {
		t.Errorf("reloaded Run().CarriedFromRun = %d, want 1", reloaded.Run().CarriedFromRun)
	}
}
