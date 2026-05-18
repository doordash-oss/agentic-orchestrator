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
	"testing"

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

func writeCarryForwardFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
