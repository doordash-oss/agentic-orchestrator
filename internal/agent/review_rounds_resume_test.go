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
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
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

// The cap limits fixes, never a round the loop owes.
func TestRunFeatureFinalReviewLoop_ResumeAtCapRunsTheOwedRound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Run("after_stop", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, _, artDir := newFRGitFeature(t, env, "fr-cap-stop")
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
		fixScript := frFixerScript(t, env, artDir, ":", frHandoffAsk())
		result, _ := runFRLoop(t, f, store, env, reviewScript, fixScript, 1, func(string) string { return handoffStopLabel })
		if result.FinalStatus != finalStatusStopped {
			t.Fatalf("first run = %+v, want stopped", result)
		}
		resumed, _ := runFRLoop(t, f, store, env, frReviewerScript(t, env, artDir, nil), fixScript, 1, nil)
		if resumed.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("resumed = %+v, want review_passed", resumed)
		}
	})
	t.Run("confirmation_lost_in_a_crash", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, repo, artDir := newFRGitFeature(t, env, "fr-cap-confirm")
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
		fix := frFixerScript(t, env, artDir, frCommitChange(repo), ":")
		result, _ := runFRLoop(t, f, store, env, reviewScript, fix, 1, nil)
		if result.FinalStatus != finalStatusReviewPassed || result.Iterations != 3 {
			t.Fatalf("first run = %+v, want review_passed in iteration 3", result)
		}
		if err := os.Remove(filepath.Join(artDir, "iteration-03", "meta.yaml")); err != nil {
			t.Fatal(err)
		}
		resumed, _ := runFRLoop(t, f, store, env, reviewScript, fix, 1, nil)
		if resumed.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("resumed = %+v, want review_passed", resumed)
		}
	})
}

// A confirmation owed past the cap survives a verdict that comes back
// unclear: the round reruns instead of ending at the cap.
func TestRunFeatureFinalReviewLoop_UnclearOwedRoundPastCapReruns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repo, artDir := newFRGitFeature(t, env, "fr-cap-unclear")
	script := frAxisVerdictScript(artDir, map[string]string{"iteration-01/craft": "CSV export is missing", "iteration-03/cleanliness": "unclear"})
	script = strings.Replace(script, `_verdict=CHANGES_REQUESTED; _findings="- **High**: unclear"`, `_verdict=MAYBE`, 1)
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh", testutil.JSONLInit+"\n"+script+"\n"+testutil.JSONLSuccess+"\n")
	result, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, frCommitChange(repo), ":"), 1, nil)
	if result.FinalStatus != finalStatusReviewPassed || result.Iterations != 4 {
		t.Fatalf("result = %+v, want review_passed in iteration 4", result)
	}
}

func TestImplementLoopPhaseReview_ResumeAtCapRunsTheOwedRound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-cap-stop", feature.PipelineMoonshot)
	run := runPhaseGate(t, env, 2, []string{"", "", phaseGateHandoffAsk()},
		map[string]string{"iteration-01/craft": "retry loop has no backoff"},
		func(string) string { return handoffStopLabel })
	if run.result.FinalStatus != finalStatusStopped {
		t.Fatalf("first run = %+v, want stopped", run.result)
	}
	resumed := runPhaseGate(t, env, 2, []string{phaseGateCommit(env.repo)}, nil, nil)
	if resumed.result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("resumed = %+v, want review_passed", resumed.result)
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
