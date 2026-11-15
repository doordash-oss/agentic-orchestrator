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

// phaseGateAskedHandoff runs a phase to a no-change handoff asked in
// iteration 3 and answered with answer.
func phaseGateAskedHandoff(t *testing.T, env *phaseGateEnv, askBody string, answer string) phaseGateRun {
	t.Helper()
	return runPhaseGate(t, env, 5, []string{"", "", askBody},
		map[string]string{"iteration-01/craft": "retry loop has no backoff"},
		func(string) string { return answer })
}

func TestImplementLoopPhaseReview_HandoffSessionFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	t.Run("provider_error_parks", func(t *testing.T) {
		env := newPhaseGateEnv(t, "feat-phase-handoff-err", feature.PipelineMoonshot)
		run := phaseGateAskedHandoff(t, env, testutil.JSONLError("provider overloaded")+"\nexit 1", "")
		if run.result == nil || run.result.FinalStatus != finalStatusStopped {
			t.Fatalf("result = %+v, want stopped with the handoff still pending", run.result)
		}
		if gate := newPhaseReviewGate(ImplementConfig{Feature: env.f, ArtifactDir: env.artifactDir}, newArtifactManager(env.artifactDir), 5); gate.pending == nil {
			t.Fatal("the handoff is no longer pending")
		}
	})
	t.Run("provider_dies_while_asking_parks", func(t *testing.T) {
		env := newPhaseGateEnv(t, "feat-phase-handoff-died", feature.PipelineMoonshot)
		ask, _, _ := strings.Cut(phaseGateHandoffAsk(), "\nwhile ")
		// The crash-resume session dies too.
		run := runPhaseGate(t, env, 5, []string{"", "", ask + "\nexit 1", "exit 1"},
			map[string]string{"iteration-01/craft": "retry loop has no backoff"}, func(string) string { return "" })
		if run.result == nil || run.result.FinalStatus != finalStatusStopped {
			t.Fatalf("result = %+v, want stopped with the handoff still pending", run.result)
		}
		if n := len(run.implementPrompts); n != 4 || !strings.Contains(run.implementPrompts[3], crashResumeMessageFragment) {
			t.Fatalf("implement sessions = %d, want the asking session resumed once, then the phase parked", n)
		}
		if gate := newPhaseReviewGate(ImplementConfig{Feature: env.f, ArtifactDir: env.artifactDir}, newArtifactManager(env.artifactDir), 5); gate.pending == nil {
			t.Fatal("the handoff is no longer pending")
		}
	})
	t.Run("accept_then_error_passes", func(t *testing.T) {
		env := newPhaseGateEnv(t, "feat-phase-handoff-accept-crash", feature.PipelineMoonshot)
		run := phaseGateAskedHandoff(t, env, phaseGateHandoffAsk()+"\n"+testutil.JSONLError("provider overloaded")+"\nexit 1", phaseHandoffAcceptLabel)
		if run.result == nil || run.result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("result = %+v, want review_passed after accept", run.result)
		}
		if len(run.implementPrompts) != 3 {
			t.Fatalf("implement sessions = %d, want none after the accepted handoff", len(run.implementPrompts))
		}
	})
}

// A saved Accept or Stop answer is acted on at resume without another
// implement session.
func TestImplementLoopPhaseReview_SavedAnswerResolvesWithoutASession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	for _, tc := range []struct {
		answer string
		want   string
	}{
		{phaseHandoffAcceptLabel, finalStatusReviewPassed},
		{handoffStopLabel, finalStatusStopped},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			env := newPhaseGateEnv(t, "feat-phase-saved-"+strings.ToLower(tc.answer[:4]), feature.PipelineMoonshot)
			phaseGateAskedHandoff(t, env, phaseGateHandoffAsk(), tc.answer)
			if err := os.Remove(filepath.Join(env.artifactDir, "iteration-03", "meta.yaml")); err != nil {
				t.Fatal(err)
			}
			resumed := runPhaseGate(t, env, 5, []string{phaseGateCommit(env.repo)}, nil, nil)
			if resumed.result.FinalStatus != tc.want {
				t.Fatalf("resumed = %+v, want %s", resumed.result, tc.want)
			}
			if len(resumed.implementPrompts) != 0 {
				t.Fatalf("resume ran %d implement sessions for a saved %q answer", len(resumed.implementPrompts), tc.answer)
			}
		})
	}
}

// A review verdict cached in an iteration is never reused for code a rerun
// implement session changed.
func TestImplementLoopPhaseReview_ResumeAfterGuidanceKeepsReviewTruthful(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	// The asking iteration's session finished; its review is resumed over
	// the same code, without another session.
	t.Run("finished_session_is_not_rerun", func(t *testing.T) {
		env := newPhaseGateEnv(t, "feat-phase-guidance-resume", feature.PipelineMoonshot)
		first := phaseGateAskedHandoff(t, env, phaseGateHandoffAsk()+"\n"+phaseGateCommit(env.repo), "Add exponential backoff.")
		if first.result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("first run = %+v, want review_passed", first.result)
		}
		if err := os.Remove(filepath.Join(env.artifactDir, "iteration-03", "meta.yaml")); err != nil {
			t.Fatal(err)
		}
		resumed := runPhaseGate(t, env, 5, nil, nil, nil)
		if resumed.result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("resumed = %+v, want review_passed", resumed.result)
		}
		if len(resumed.implementPrompts) != 0 {
			t.Fatalf("resume ran %d implement sessions for a finished asking session", len(resumed.implementPrompts))
		}
	})
	// A session that reruns in an iteration whose review already ran makes
	// that review's verdicts stale.
	t.Run("rerun_session_is_reviewed_again", func(t *testing.T) {
		env := newPhaseGateEnv(t, "feat-phase-guidance-rerun", feature.PipelineMoonshot)
		first := phaseGateAskedHandoff(t, env, phaseGateHandoffAsk()+"\n"+phaseGateCommit(env.repo), "Add exponential backoff.")
		if first.result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("first run = %+v, want review_passed", first.result)
		}
		iterDir := filepath.Join(env.artifactDir, "iteration-03")
		for _, name := range []string{"meta.yaml", PhaseCompleteFile} {
			if err := os.Remove(filepath.Join(iterDir, name)); err != nil {
				t.Fatal(err)
			}
		}
		resumed := runPhaseGate(t, env, 5, []string{phaseGateCommit(env.repo)}, nil, nil)
		if resumed.result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("resumed = %+v, want review_passed", resumed.result)
		}
		if len(resumed.implementPrompts) != 1 {
			t.Fatalf("implement sessions = %d, want the rerun session", len(resumed.implementPrompts))
		}
		if _, ok := resumed.reviewPrompts["iteration-03/craft"]; !ok {
			t.Fatalf("the rerun session's changes passed on a cached Craft verdict; reviewed %v", resumed.reviewed())
		}
	})
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
