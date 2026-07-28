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

package feature_test

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestRewindToPhase_CarryForwardArtifactPathEdges(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs and mocks isolate filesystem and collaborator state.
	if testing.Short() {
		t.Skip("extended carry-forward artifact path edge regression")
	}

	mgr := newTestManager(t)
	f, run1Dir := seedRewindableFeature(t, mgr)

	relInquire := filepath.Join("inquire", "inquire.md")
	outsideResearch := filepath.Join(t.TempDir(), "research.md")
	absDesign := filepath.Join(run1Dir, "design", "design.md")
	writeCarryForwardFile(t, filepath.Join(run1Dir, relInquire), "inquire")
	writeCarryForwardFile(t, outsideResearch, "research")
	writeCarryForwardFile(t, absDesign, "design")

	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Artifacts = map[string]string{
			"inquire":  relInquire,
			"research": outsideResearch,
			"design":   absDesign,
			"pr_url":   "https://github.com/o/r/pull/42",
		}
		return nil
	}); err != nil {
		t.Fatalf("modify artifacts: %v", err)
	}

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := map[string]string{
		"inquire":  relInquire,
		"research": outsideResearch,
		"design":   filepath.Join("design", "design.md"),
	}
	if len(got.Artifacts) != len(want) {
		t.Fatalf("Artifacts len = %d, want %d: %v", len(got.Artifacts), len(want), got.Artifacts)
	}
	for k, wantVal := range want {
		if got.Artifacts[k] != wantVal {
			t.Errorf("Artifacts[%q] = %q, want %q", k, got.Artifacts[k], wantVal)
		}
	}
	if _, ok := got.Artifacts["pr_url"]; ok {
		t.Errorf("Artifacts carried pr_url, want it dropped")
	}

	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if sealedRun.Artifacts["design"] != absDesign {
		t.Errorf("sealed Artifacts[design] = %q, want %q", sealedRun.Artifacts["design"], absDesign)
	}
	if sealedRun.Artifacts["research"] != outsideResearch {
		t.Errorf("sealed Artifacts[research] = %q, want %q", sealedRun.Artifacts["research"], outsideResearch)
	}
}

func TestRewindToPhase_CarriesCostsOfKeptPhases(t *testing.T) {
	t.Parallel()

	mgr := newTestManager(t)
	f, _ := seedRewindableFeature(t, mgr)

	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.RecordSessionCost(feature.SessionCostRecord{SessionID: "s-kb", PhaseKey: "knowledgebase", CostUSD: 1.25})
		ff.RecordSessionCost(feature.SessionCostRecord{SessionID: "s-inq", PhaseKey: "inquire", CostUSD: 2.5})
		ff.RecordSessionCost(feature.SessionCostRecord{SessionID: "s-des", PhaseKey: "design", CostUSD: 4})
		ff.RecordSessionCost(feature.SessionCostRecord{SessionID: "s-plan", PhaseKey: "plan", CostUSD: 8})
		ff.RecordSessionCost(feature.SessionCostRecord{SessionID: "s-impl", PhaseKey: "phase-1-impl", CostUSD: 16})
		ff.PhaseTimings = map[string]time.Duration{
			"inquire": time.Minute,
			"plan":    2 * time.Minute,
		}
		return nil
	}); err != nil {
		t.Fatalf("modify costs: %v", err)
	}

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	got, err := mgr.Get(f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantCosts := map[string]float64{
		"knowledgebase": 1.25,
		"inquire":       2.5,
		"design":        4,
	}
	if len(got.PhaseCosts) != len(wantCosts) {
		t.Fatalf("PhaseCosts = %v, want %v", got.PhaseCosts, wantCosts)
	}
	for k, want := range wantCosts {
		if got.PhaseCosts[k] != want {
			t.Errorf("PhaseCosts[%q] = %v, want %v", k, got.PhaseCosts[k], want)
		}
	}
	if got.PhaseRuntime("inquire") != time.Minute {
		t.Errorf("PhaseRuntime(inquire) = %v, want 1m", got.PhaseRuntime("inquire"))
	}
	if got.PhaseRuntime("plan") != 0 {
		t.Errorf("PhaseRuntime(plan) = %v, want dropped", got.PhaseRuntime("plan"))
	}
	var sessionKeys []string
	for _, rec := range got.SessionCosts {
		sessionKeys = append(sessionKeys, rec.PhaseKey)
	}
	sort.Strings(sessionKeys)
	if want := []string{"design", "inquire", "knowledgebase"}; !slices.Equal(sessionKeys, want) {
		t.Errorf("SessionCosts keys = %v, want %v", sessionKeys, want)
	}

	// The sealed run keeps the full ledger untouched.
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if len(sealedRun.PhaseCosts) != 5 || sealedRun.PhaseCosts["phase-1-impl"] != 16 {
		t.Errorf("sealed PhaseCosts = %v, want full 5-key ledger", sealedRun.PhaseCosts)
	}
}

func writeCarryForwardFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
