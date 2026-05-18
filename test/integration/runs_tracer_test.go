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
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRunsLayout_CreateInquireDesignRewindInquireAgain is the end-to-end
// tracer smoke test that proves the runs-first state layout is coherent
// through the tracer path.
//
//  1. Create feature → run-001 exists with run.yaml.
//  2. Simulate inquire/research/design: lifecycle transitions plus real
//     artifact writes from the agent package (agent.LogPhaseError, the
//     run-aware path helpers PhaseDir / RunDir). These are the same writers
//     production PhaseRunner code goes through — NOT hand-seeded markers —
//     so the test genuinely exercises the routing layer.
//  3. Rewind to PhaseInquire → run-001 sealed in place, run-002 forked;
//     sealed artifacts preserved, run-002 empty.
//  4. Run the writers again against the reloaded feature (ActiveRun=2) and
//     verify they now land under run-002/, with run-001/ untouched.
//
// If any artifact writer still targeted the feature root, steps (2) and (4)
// would leak into the same directory and step (4) would overwrite step (2)'s
// fixtures. The run-aware helpers keep them isolated.
func TestRunsLayout_CreateInquireDesignRewindInquireAgain(t *testing.T) {
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

	// --- Step 1: Create ----------------------------------------------------
	f, err := mgr.Create(
		"Runs Tracer",
		"tracer bullet for runs-first layout",
		[]string{"test-repo"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if f.ActiveRun != 1 || f.RunCount != 1 {
		t.Fatalf("fresh feature ActiveRun/RunCount = %d/%d, want 1/1", f.ActiveRun, f.RunCount)
	}

	run1Path := filepath.Join(stateDir, f.ID, "runs", "run-001", "run.yaml")
	if _, err := os.Stat(run1Path); err != nil {
		t.Fatalf("run-001/run.yaml missing after Create: %v", err)
	}
	// No flat per-phase dirs at feature root.
	for _, name := range []string{"inquire", "research", "design", "plan", "implement", "roadmap"} {
		if _, err := os.Stat(filepath.Join(stateDir, f.ID, name)); !os.IsNotExist(err) {
			t.Errorf("feature root should not contain legacy %s/ dir", name)
		}
	}

	// --- Step 2: Simulate inquire → research → design ------------------
	// Drive lifecycle transitions and exercise REAL agent-package path
	// writers against the feature. agent.LogPhaseError is the canonical
	// per-phase writer; if it still targeted the feature root, markers
	// would land outside runs/run-001/ and step 4's writes would overwrite
	// step 2's fixtures after rewind.
	run1Dir := filepath.Join(stateDir, f.ID, "runs", "run-001")

	if err := mgr.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire: %v", err)
	}
	if err := mgr.CompleteInquire(f.ID); err != nil {
		t.Fatalf("CompleteInquire: %v", err)
	}
	if f, err = mgr.Get(f.ID); err != nil {
		t.Fatalf("Get after inquire: %v", err)
	}
	agent.LogPhaseError(stateDir, f, "inquire", "r1-inquire-error")
	assertUnderActiveRun(t, stateDir, f, "inquire", "error.log")

	if err := mgr.StartResearch(f.ID); err != nil {
		t.Fatalf("StartResearch: %v", err)
	}
	if err := mgr.CompleteResearch(f.ID); err != nil {
		t.Fatalf("CompleteResearch: %v", err)
	}
	if f, err = mgr.Get(f.ID); err != nil {
		t.Fatalf("Get after research: %v", err)
	}
	agent.LogPhaseError(stateDir, f, "research", "r1-research-error")
	assertUnderActiveRun(t, stateDir, f, "research", "error.log")

	if err := mgr.StartDesign(f.ID); err != nil {
		t.Fatalf("StartDesign: %v", err)
	}
	if err := mgr.CompleteDesign(f.ID); err != nil {
		t.Fatalf("CompleteDesign: %v", err)
	}
	if f, err = mgr.Get(f.ID); err != nil {
		t.Fatalf("Get after design: %v", err)
	}
	agent.LogPhaseError(stateDir, f, "design", "r1-design-error")
	assertUnderActiveRun(t, stateDir, f, "design", "error.log")

	// Also verify the exported path helpers all root inside run-001.
	phaseDir := agent.PhaseDir(stateDir, f, 1)
	if !underRun1(phaseDir, run1Dir) {
		t.Errorf("PhaseDir(..., 1) = %q, want under %q", phaseDir, run1Dir)
	}
	if got := agent.PhasePlanDir(stateDir, f, 1); !underRun1(got, run1Dir) {
		t.Errorf("PhasePlanDir(..., 1) = %q, want under %q", got, run1Dir)
	}
	if got := agent.PhaseImplementDir(stateDir, f, 1); !underRun1(got, run1Dir) {
		t.Errorf("PhaseImplementDir(..., 1) = %q, want under %q", got, run1Dir)
	}
	if got := agent.RoadmapDir(stateDir, f); !underRun1(got, run1Dir) {
		t.Errorf("RoadmapDir = %q, want under %q", got, run1Dir)
	}
	if got := agent.RefactorBaseDir(stateDir, f, 1); !underRun1(got, run1Dir) {
		t.Errorf("RefactorBaseDir(..., 1) = %q, want under %q", got, run1Dir)
	}

	// No error.log should have leaked to the feature root.
	for _, phase := range []string{"inquire", "research", "design"} {
		leaked := filepath.Join(stateDir, f.ID, phase, "error.log")
		if _, err := os.Stat(leaked); !os.IsNotExist(err) {
			t.Errorf("leak: %s exists at feature root (should be under runs/run-001/)", leaked)
		}
	}

	// --- Step 3: Rewind to PhaseInquire -----------------------------------
	// Transition to a state where rewinding to Inquire is valid (Implementing).
	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	})

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseInquire); err != nil {
		t.Fatalf("RewindToPhase(Inquire): %v", err)
	}

	// Post-seal: run-001 is sealed and its artifacts are preserved.
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.SealedAt == nil {
		t.Error("run-001 should be sealed (SealedAt != nil)")
	}
	if sealedRun.SealReason != feature.SealReasonRewind {
		t.Errorf("seal reason = %q, want %q", sealedRun.SealReason, feature.SealReasonRewind)
	}
	if sealedRun.RewindTarget == nil || *sealedRun.RewindTarget != feature.PhaseInquire {
		t.Errorf("rewind target = %v, want %v", sealedRun.RewindTarget, feature.PhaseInquire)
	}
	// The seal closure sets an empty map; YAML omitempty drops it so the
	// loaded sealed run observes nil OR an empty map. Either way is fine; we
	// only care that nothing leaked through.
	if len(sealedRun.BackupBranches) != 0 {
		t.Errorf("BackupBranches should be empty, got %v", sealedRun.BackupBranches)
	}
	// The real error.log files written by LogPhaseError must still exist in
	// the sealed run tree — seal+fork must not delete anything.
	for _, phase := range []string{"inquire", "research", "design"} {
		path := filepath.Join(run1Dir, phase, "error.log")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("sealed run-001/%s/error.log missing: %v (seal+fork must preserve artifacts)", phase, err)
			continue
		}
		// Content should still include the original marker substring.
		if want := "r1-" + phase + "-error"; !containsSubstr(string(data), want) {
			t.Errorf("sealed run-001/%s/error.log = %q, should contain %q", phase, string(data), want)
		}
	}

	// Fresh run-002 exists with empty state.
	run2Dir := filepath.Join(stateDir, f.ID, "runs", "run-002")
	freshRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if freshRun.CarriedFromRun != 1 {
		t.Errorf("CarriedFromRun = %d, want 1", freshRun.CarriedFromRun)
	}
	if len(freshRun.CarriedPhases) != 0 {
		t.Errorf("CarriedPhases should be empty, got %v", freshRun.CarriedPhases)
	}
	if freshRun.SealedAt != nil {
		t.Error("run-002 must not be sealed")
	}

	// Feature state reflects the new active run.
	reloaded, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.ActiveRun != 2 || reloaded.RunCount != 2 {
		t.Errorf("ActiveRun/RunCount = %d/%d, want 2/2", reloaded.ActiveRun, reloaded.RunCount)
	}
	if reloaded.PendingReviewPhase == nil || *reloaded.PendingReviewPhase != feature.PhaseInquire {
		t.Errorf("PendingReviewPhase = %v, want %v", reloaded.PendingReviewPhase, feature.PhaseInquire)
	}
	if !reloaded.IsRewind {
		t.Error("IsRewind should be true on the freshly-forked run")
	}

	// description-review.md lives at the feature root (not inside any run).
	descPath := filepath.Join(stateDir, f.ID, "description-review.md")
	if _, err := os.Stat(descPath); err != nil {
		t.Errorf("description-review.md missing at feature root: %v", err)
	}

	// Exported path helpers should now return run-002 paths.
	if got := agent.ActiveRunDir(stateDir, reloaded); !underRun2(got, run2Dir) {
		t.Errorf("ActiveRunDir after rewind = %q, want under %q", got, run2Dir)
	}
	if got := agent.PhaseDir(stateDir, reloaded, 1); !underRun2(got, run2Dir) {
		t.Errorf("PhaseDir after rewind = %q, want under %q", got, run2Dir)
	}

	// --- Step 4: Simulate inquire under run-002 ---------------------------
	// Transition back out of the rewind-review state so StartInquire is valid.
	_ = mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusCreated
		ff.CurrentPhase = feature.PhaseInquire
		ff.PendingReviewPhase = nil
		ff.IsRewind = false
		return nil
	})

	if err := mgr.StartInquire(f.ID); err != nil {
		t.Fatalf("StartInquire (run-002): %v", err)
	}
	if err := mgr.CompleteInquire(f.ID); err != nil {
		t.Fatalf("CompleteInquire (run-002): %v", err)
	}
	reloaded, err = mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get after run-002 inquire: %v", err)
	}
	// Real writer again — this one must land inside run-002/, not run-001/,
	// because ActiveRun is now 2.
	agent.LogPhaseError(stateDir, reloaded, "inquire", "r2-inquire-error")
	assertUnderActiveRun(t, stateDir, reloaded, "inquire", "error.log")

	// run-001 untouched — its error.log still contains the r1 marker.
	if data, err := os.ReadFile(filepath.Join(run1Dir, "inquire", "error.log")); err != nil {
		t.Errorf("run-001/inquire/error.log vanished: %v", err)
	} else if got := string(data); !containsSubstr(got, "r1-inquire-error") {
		t.Errorf("run-001 inquire error.log = %q, should contain r1-inquire-error (NOT overwritten)", got)
	}

	// run-002 now has its own inquire error log.
	r2LogPath := filepath.Join(run2Dir, "inquire", "error.log")
	if data, err := os.ReadFile(r2LogPath); err != nil {
		t.Errorf("run-002/inquire/error.log missing: %v", err)
	} else if got := string(data); !containsSubstr(got, "r2-inquire-error") {
		t.Errorf("run-002 inquire error.log = %q, should contain r2-inquire-error", got)
	}

	// Final defense: no error.log anywhere at feature root.
	for _, phase := range []string{"inquire", "research", "design"} {
		leaked := filepath.Join(stateDir, f.ID, phase, "error.log")
		if _, err := os.Stat(leaked); !os.IsNotExist(err) {
			t.Errorf("final leak: %s exists at feature root", leaked)
		}
	}
}

// assertUnderActiveRun verifies that the given `<phase>/<file>` path exists
// inside the feature's active run directory. The feature must be reloaded
// from disk before this call so ActiveRun is current.
func assertUnderActiveRun(t *testing.T, stateDir string, f *feature.Feature, phase, file string) {
	t.Helper()
	runDir := agent.ActiveRunDir(stateDir, f)
	want := filepath.Join(runDir, phase, file)
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected %s (under active run %s), got: %v", want, runDir, err)
	}
}

func underRun1(path, run1Dir string) bool { return hasPrefix(path, run1Dir) }
func underRun2(path, run2Dir string) bool { return hasPrefix(path, run2Dir) }

func hasPrefix(path, prefix string) bool {
	if len(path) < len(prefix) {
		return false
	}
	return path[:len(prefix)] == prefix
}

func containsSubstr(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
