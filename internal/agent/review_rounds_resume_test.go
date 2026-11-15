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

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// setFeatureStatus stands in for a pause or a resume between loop runs.
func setFeatureStatus(t *testing.T, store *feature.Store, id string, status feature.Status) {
	t.Helper()
	if err := store.Modify(id, func(f *feature.Feature) error {
		f.Status = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A dispute recorded before a pause still counts once the loop resumes.
func TestRunFeatureFinalReviewLoop_DecisionSurvivesResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, artDir := newFRGitFeature(t, env, "fr-decided-resume")
	status := f.Status
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
	dispute := "Dispute: Craft: CSV export is missing. The roadmap drops the export."
	first, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, frAskUser(dispute, "Fix it", "Accept as is"), ":"), 5, func(string) string {
		setFeatureStatus(t, store, f.ID, feature.StatusInterrupted)
		return "Accept as is"
	})
	if first.FinalStatus != "interrupted" {
		t.Fatalf("first run = %+v, want interrupted", first)
	}
	setFeatureStatus(t, store, f.ID, status)
	resumed, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, ":", ":"), 5, nil)
	if resumed.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("resumed = %+v, want review_passed: the recorded dispute is not a no-change round", resumed)
	}
}

func TestImplementLoopPhaseReview_DecisionSurvivesResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-decided-resume", feature.PipelineMoonshot)
	status := env.f.Status
	dispute := "Dispute: Craft: retry loop has no backoff. The plan keeps a fixed delay."
	first := runPhaseGate(t, env, 5, []string{"", frAskUser(dispute, "Fix it", "Accept as is")},
		map[string]string{"iteration-01/craft": "retry loop has no backoff"},
		func(string) string {
			setFeatureStatus(t, env.store, env.f.ID, feature.StatusInterrupted)
			return "Accept as is"
		})
	if first.result.FinalStatus != "interrupted" {
		t.Fatalf("first run = %+v, want interrupted", first.result)
	}
	setFeatureStatus(t, env.store, env.f.ID, status)
	resumed := runPhaseGate(t, env, 5, nil, nil, nil)
	if resumed.result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("resumed = %+v, want review_passed: the recorded dispute is not a no-change round", resumed.result)
	}
}

// Decisions from a phase's review reach Final Review.
func TestRunFeatureFinalReviewLoop_SeesPhaseReviewDecisions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, artDir := newFRGitFeature(t, env, "fr-phase-decisions")
	phaseDecisions := filepath.Join(filepath.Dir(artDir), "phase-01", "implement", reviewDecisionsFile)
	if err := os.MkdirAll(filepath.Dir(phaseDecisions), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phaseDecisions, []byte("# Review Decisions\n\n## Iteration 2 handoff\n\n**Q:** Phase 1 review needs your decision.\n\n**A:** Accept and finish the phase\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, _ := runFRLoop(t, f, store, env, frReviewerScript(t, env, artDir, nil), frFixerScript(t, env, artDir, ":", ":"), 3, nil)
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("result = %+v, want review_passed", result)
	}
	prompt := readFRFile(t, filepath.Join(artDir, "iteration-01", "craft", "review-prompt.md"))
	if !strings.Contains(prompt, phaseDecisions) {
		t.Fatalf("Final Review prompt does not list the phase review decisions %s:\n%s", phaseDecisions, prompt)
	}
}
