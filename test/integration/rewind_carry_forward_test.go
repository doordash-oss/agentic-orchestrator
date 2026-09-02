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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestRewindToPlan_EndToEnd_CarriesForwardContent drives a feature through
// inquire → research → design, writes real marker files under the
// run-001 phase directories, rewinds to PhasePlan, and asserts:
//
//  1. run-002/inquire|research|design/marker.txt exist with identical
//     content to the run-001 originals (deep-copy).
//  2. run-002.Artifacts values are run-relative (not absolute, not
//     containing "run-001").
//  3. run-001's Artifacts map is UNCHANGED — sealed-run immutability.
//  4. Writing a new file under run-002/inquire/ does not leak into run-001.
//
// This test is the end-to-end counterpart to the unit tests in
// internal/feature/ and the runs-tracer smoke in runs_tracer_test.go.
func TestRewindToPlan_EndToEnd_CarriesForwardContent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir stateDir: %v", err)
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repoDir: %v", err)
	}

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

	f, err := mgr.Create(
		"Carry Forward End-to-End",
		"integration test for Phase 2 carry-forward",
		[]string{"test-repo"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run1Dir := filepath.Join(stateDir, f.ID, "runs", "run-001")

	// Walk inquire → research → design with real writers.
	phaseSteps := []struct {
		name  string
		start func(string) error
		done  func(string) error
	}{
		{"inquire", mgr.StartInquire, mgr.CompleteInquire},
		{"research", mgr.StartResearch, mgr.CompleteResearch},
		{"design", mgr.StartDesign, mgr.CompleteDesign},
	}
	for _, step := range phaseSteps {
		if err := step.start(f.ID); err != nil {
			t.Fatalf("Start%s: %v", step.name, err)
		}
		if err := step.done(f.ID); err != nil {
			t.Fatalf("Complete%s: %v", step.name, err)
		}
		if f, err = mgr.Get(f.ID); err != nil {
			t.Fatalf("Get after %s: %v", step.name, err)
		}
		// Write a marker file with the phase name under <run-001>/<phase>/.
		markerDir := filepath.Join(run1Dir, step.name)
		if err := os.MkdirAll(markerDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", markerDir, err)
		}
		markerPath := filepath.Join(markerDir, step.name+".md")
		content := "content-of-" + step.name
		if err := os.WriteFile(markerPath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", markerPath, err)
		}
		// Also write the legacy per-phase error.log fixture so the real path
		// layout runs and lands inside runs/run-001/<phase>/.
		writePhaseErrorLog(t, stateDir, f, step.name, "r1-"+step.name+"-error")
	}

	// Populate Artifacts with absolute paths pointing under run-001/.
	absInquire := filepath.Join(run1Dir, "inquire", "inquire.md")
	absResearch := filepath.Join(run1Dir, "research", "research.md")
	absDesign := filepath.Join(run1Dir, "design", "design.md")
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Artifacts = map[string]string{
			"inquire":  absInquire,
			"research": absResearch,
			"design":   absDesign,
			"pr_url":   "https://github.com/o/r/pull/99",
		}
		// Advance to implementing state so rewind to Plan is valid.
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	// Rewind to plan.
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	run2Dir := filepath.Join(stateDir, f.ID, "runs", "run-002")
	// 1. Carried marker files exist at run-002/<phase>/<phase>.md with
	//    identical content.
	for _, phase := range []string{"inquire", "research", "design"} {
		r1 := filepath.Join(run1Dir, phase, phase+".md")
		r2 := filepath.Join(run2Dir, phase, phase+".md")
		r1Bytes, err := os.ReadFile(r1)
		if err != nil {
			t.Fatalf("read sealed run-001 %s: %v", r1, err)
		}
		r2Bytes, err := os.ReadFile(r2)
		if err != nil {
			t.Errorf("run-002/%s/%s.md missing (should be carried): %v", phase, phase, err)
			continue
		}
		if string(r1Bytes) != string(r2Bytes) {
			t.Errorf("run-002/%s content = %q, want %q", phase, r2Bytes, r1Bytes)
		}
	}

	// 2. New run Artifacts values are run-relative.
	freshRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if freshRun.CarriedFromRun != 1 {
		t.Errorf("CarriedFromRun = %d, want 1", freshRun.CarriedFromRun)
	}
	for _, phase := range []string{"inquire", "research", "design"} {
		v, ok := freshRun.Artifacts[phase]
		if !ok {
			t.Errorf("run-002 Artifacts missing carried key %q", phase)
			continue
		}
		if filepath.IsAbs(v) {
			t.Errorf("run-002 Artifacts[%q] = %q is absolute, want run-relative", phase, v)
		}
		if strings.Contains(v, "run-001") {
			t.Errorf("run-002 Artifacts[%q] = %q still contains run-001 prefix", phase, v)
		}
	}
	for _, disallowed := range []string{"pr_url"} {
		if _, ok := freshRun.Artifacts[disallowed]; ok {
			t.Errorf("run-002 Artifacts must NOT carry %q", disallowed)
		}
	}

	// 3. Sealed run-001 Artifacts unchanged (absolute values preserved).
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if got := sealedRun.Artifacts["inquire"]; got != absInquire {
		t.Errorf("sealed run-001 Artifacts[inquire] = %q, want %q (sealed-run immutability)", got, absInquire)
	}
	if got := sealedRun.Artifacts["pr_url"]; got != "https://github.com/o/r/pull/99" {
		t.Errorf("sealed run-001 Artifacts[pr_url] = %q, want URL verbatim", got)
	}

	// 4. Mutate run-002 under inquire/ and confirm run-001 is not affected.
	mutatePath := filepath.Join(run2Dir, "inquire", "r2-new.md")
	if err := os.WriteFile(mutatePath, []byte("run-2-only"), 0o644); err != nil {
		t.Fatalf("write run-2 marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run1Dir, "inquire", "r2-new.md")); !os.IsNotExist(err) {
		t.Errorf("run-002 mutation leaked into run-001 (err=%v)", err)
	}

	// Baseline run-001 file content still intact after run-002 mutation.
	origBytes, err := os.ReadFile(filepath.Join(run1Dir, "inquire", "inquire.md"))
	if err != nil {
		t.Fatalf("read run-001 inquire.md: %v", err)
	}
	if got := string(origBytes); got != "content-of-inquire" {
		t.Errorf("run-001 inquire.md = %q, want %q", got, "content-of-inquire")
	}
}

// TestRewindToImplement_RoadmapPipeline_CarriedPlanPathResolves is the
// end-to-end counterpart to iteration-02's reviewer fix: on Large/Moonshot
// pipelines the orchestrator writes f.Artifacts["plan"] = abs(<run-NNN>/phase-NN/plan/plan.md)
// when the implementer starts, so after Phase 2 carry-forward the new run
// carries a RUN-RELATIVE "phase-NN/plan/plan.md" value. The desktop app and orchestrator
// path resolvers must combine that relative value with ActiveRunDir to locate
// the deep-copied plan in run-002; otherwise the rewind review fails with
// "no artifact found for the previous phase" and proceed persists an empty
// plan path.
//
// This test seeds the lifecycle through inquire → research → design,
// writes phase-NN/plan/plan.md under run-001 (simulating a roadmap planner's
// output), populates f.Artifacts["plan"] with the absolute path (matching
// orchestrator.go:854's write), then drives RewindToPhase(PhaseImplement)
// end-to-end. Assertions:
//
//  1. run-002/phase-NN/plan/plan.md exists with identical content to run-001.
//  2. f.Artifacts["plan"] on run-002 is run-relative (no absolute, no "run-001").
//  3. Joining the carried value to ActiveRunDir(run-002) produces a valid path
//     that stats cleanly through orchestrator/context.go.
func TestRewindToImplement_RoadmapPipeline_CarriedPlanPathResolves(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	stateDir := filepath.Join(tmpDir, "state")
	repoDir := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir stateDir: %v", err)
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repoDir: %v", err)
	}

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

	f, err := mgr.Create(
		"Roadmap Rewind",
		"integration test for Phase 2 rewind-to-Implement on roadmap pipeline",
		[]string{"test-repo"},
		cfg.Defaults.Models,
		"",
		"",
		nil,
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	run1Dir := filepath.Join(stateDir, f.ID, "runs", "run-001")

	// Walk inquire → research → design with real writers so carry-forward
	// has genuine content to copy, not just seeded fixtures.
	phaseSteps := []struct {
		name  string
		start func(string) error
		done  func(string) error
	}{
		{"inquire", mgr.StartInquire, mgr.CompleteInquire},
		{"research", mgr.StartResearch, mgr.CompleteResearch},
		{"design", mgr.StartDesign, mgr.CompleteDesign},
	}
	for _, step := range phaseSteps {
		if err := step.start(f.ID); err != nil {
			t.Fatalf("Start%s: %v", step.name, err)
		}
		if err := step.done(f.ID); err != nil {
			t.Fatalf("Complete%s: %v", step.name, err)
		}
		if f, err = mgr.Get(f.ID); err != nil {
			t.Fatalf("Get after %s: %v", step.name, err)
		}
	}

	// Seed the Large-pipeline artifacts on disk: roadmap + two phase plans +
	// an implement marker (simulating a partially-completed implementation).
	phaseSeeds := map[string]string{
		filepath.Join("roadmap", "roadmap.md"):           "# roadmap",
		filepath.Join("phase-01", "plan", "plan.md"):     "# phase-01 plan",
		filepath.Join("phase-01", "implement", "out.md"): "phase-01 output",
		filepath.Join("phase-02", "plan", "plan.md"):     "# phase-02 plan (active)",
	}
	for rel, content := range phaseSeeds {
		full := filepath.Join(run1Dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// Mirror orchestrator.go:854 exactly: f.Artifacts["plan"] is stamped with
	// the absolute path of the current phase's plan.md. For Phase 2 this is
	// the critical input: carryForwardArtifactsMap MUST strip the sealedRunDir
	// prefix and produce "phase-02/plan/plan.md" on run-002.
	absPhase02Plan := filepath.Join(run1Dir, "phase-02", "plan", "plan.md")
	absRoadmap := filepath.Join(run1Dir, "roadmap", "roadmap.md")
	absPhase01Plan := filepath.Join(run1Dir, "phase-01", "plan", "plan.md")
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Artifacts = map[string]string{
			"plan":         absPhase02Plan,
			"roadmap":      absRoadmap,
			"phase-1-plan": absPhase01Plan,
			"phase-2-plan": absPhase02Plan,
		}
		ff.Pipeline = feature.PipelineLarge
		ff.TotalRoadmapPhases = 2
		ff.CurrentRoadmapPhase = 2
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		return nil
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	// Drive the rewind to Implement.
	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhaseImplement); err != nil {
		t.Fatalf("RewindToPhase(PhaseImplement): %v", err)
	}

	run2Dir := filepath.Join(stateDir, f.ID, "runs", "run-002")

	// 1. Carried phase-NN/plan/plan.md files exist with identical content.
	for _, rel := range []string{filepath.Join("phase-01", "plan", "plan.md"), filepath.Join("phase-02", "plan", "plan.md"), filepath.Join("roadmap", "roadmap.md")} {
		r1 := filepath.Join(run1Dir, rel)
		r2 := filepath.Join(run2Dir, rel)
		r1Bytes, err := os.ReadFile(r1)
		if err != nil {
			t.Fatalf("read run-001/%s: %v", rel, err)
		}
		r2Bytes, err := os.ReadFile(r2)
		if err != nil {
			t.Errorf("run-002/%s missing (should be carried): %v", rel, err)
			continue
		}
		if string(r1Bytes) != string(r2Bytes) {
			t.Errorf("run-002/%s content = %q, want %q", rel, r2Bytes, r1Bytes)
		}
	}
	// phase-NN/implement/ must NOT carry — implementation is regenerated.
	if _, err := os.Stat(filepath.Join(run2Dir, "phase-01", "implement")); !os.IsNotExist(err) {
		t.Errorf("run-002/phase-01/implement should NOT exist (err=%v)", err)
	}

	// 2. Carried Artifacts["plan"] is run-relative "phase-02/plan/plan.md".
	freshRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	carriedPlan, ok := freshRun.Artifacts["plan"]
	if !ok || carriedPlan == "" {
		t.Fatalf("run-002 Artifacts missing 'plan' key — carry-forward dropped it")
	}
	if filepath.IsAbs(carriedPlan) {
		t.Errorf("run-002 Artifacts[plan] = %q is absolute, want run-relative", carriedPlan)
	}
	if strings.Contains(carriedPlan, "run-001") {
		t.Errorf("run-002 Artifacts[plan] = %q still contains run-001 prefix", carriedPlan)
	}
	wantRel := filepath.Join("phase-02", "plan", "plan.md")
	if carriedPlan != wantRel {
		t.Errorf("run-002 Artifacts[plan] = %q, want %q", carriedPlan, wantRel)
	}

	// 3. Joining the run-relative value to ActiveRunDir(run-002) must produce
	// a valid absolute path that stats cleanly. This is the exact operation
	// the desktop app's resolvePhaseArtifactPath and the orchestrator's
	// resolvePlanPath perform after iteration-02's fix — the rewind review
	// path ("startRewindReviewSessionCmd") and the proceed path
	// ("reviewProceed") both rely on this resolution.
	if fresh, err := mgr.Get(f.ID); err != nil {
		t.Fatalf("Get after rewind: %v", err)
	} else {
		activeRun := agent.ActiveRunDir(stateDir, fresh)
		resolved := filepath.Join(activeRun, carriedPlan)
		info, err := os.Stat(resolved)
		if err != nil {
			t.Fatalf("resolver simulation: stat %q failed (%v); run-relative plan did not resolve via ActiveRunDir", resolved, err)
		}
		if info.IsDir() {
			t.Errorf("resolved plan path is a directory, want file: %s", resolved)
		}
		body, err := os.ReadFile(resolved)
		if err != nil {
			t.Fatalf("read resolved plan %q: %v", resolved, err)
		}
		if got, want := string(body), "# phase-02 plan (active)"; got != want {
			t.Errorf("resolved plan content = %q, want %q (wrong file resolved)", got, want)
		}
	}

	// 4. phase-*-plan keys also survive carry-forward for Implement target.
	for _, k := range []string{"phase-1-plan", "phase-2-plan", "roadmap"} {
		if _, ok := freshRun.Artifacts[k]; !ok {
			t.Errorf("run-002 Artifacts missing %q (should carry for PhaseImplement)", k)
		}
	}
}
