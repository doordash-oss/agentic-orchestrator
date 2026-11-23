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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func runFRLoop(t *testing.T, f *feature.Feature, store *feature.Store, env frLoopTestEnv, reviewScript, fixScript string, maxIterations int, answer func(string) string) (*FeatureFinalReviewResult, []string) {
	t.Helper()
	sm := session.NewManager(make(chan any, 100))
	defer sm.Shutdown()
	finish := answerFRQuestions(t, sm, f.ID, answer)
	result, err := runFeatureFinalReviewLoop(frLoopConfig(f, store, env, reviewScript, fixScript, maxIterations), sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	return result, finish()
}

// frCountingReviewerScript is frReviewerScript that also logs each reviewer
// session to log.
func frCountingReviewerScript(t *testing.T, env frLoopTestEnv, artDir, log string, changes map[string]string) string {
	t.Helper()
	return testutil.WriteScript(t, env.scriptsDir, fmt.Sprintf("review-%d.sh", time.Now().UnixNano()),
		testutil.JSONLInit+"\n"+fmt.Sprintf("echo run >> %q\n", log)+frAxisVerdictScript(artDir, changes)+"\n"+testutil.JSONLSuccess+"\n")
}

func frAxisRanAfter(artDir string, after int, slug string) bool {
	for i := after + 1; i <= after+10; i++ {
		if frAxisRan(artDir, i, slug) {
			return true
		}
	}
	return false
}

func gitCommitAll(t *testing.T, repo, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", message}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), testutil.GitTestEnv()...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// Approvals must follow the repos' actual state: code committed while the
// feature was stopped is confirmed by every axis that approved before it.
func TestRunFeatureFinalReviewLoop_ManualChangeWhileStoppedConfirmsApprovals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repo, artDir := newFRGitFeature(t, env, "fr-manual-change")
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
	fixScript := frFixerScript(t, env, artDir, ":", frHandoffAsk())
	result, _ := runFRLoop(t, f, store, env, reviewScript, fixScript, 5, func(string) string { return handoffStopLabel })
	if result.FinalStatus != finalStatusStopped {
		t.Fatalf("FinalStatus = %q, want stopped (LastError=%q)", result.FinalStatus, result.LastError)
	}

	if err := os.WriteFile(filepath.Join(repo, "manual.txt"), []byte("by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, repo, "manual fix")

	resumed, _ := runFRLoop(t, f, store, env, frReviewerScript(t, env, artDir, nil), fixScript, 6, nil)
	if resumed.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("resumed FinalStatus = %q, want review_passed (LastError=%q)", resumed.FinalStatus, resumed.LastError)
	}
	for _, slug := range []string{"cleanliness", "qa"} {
		if !frAxisRanAfter(artDir, 1, slug) {
			t.Fatalf("%s approved before the manual commit and never reviewed it", slug)
		}
	}
}

// A pause during a fix keeps the round durable: the resumed loop finishes
// that fix, and every approval older than the fix's commit confirms it.
func TestRunFeatureFinalReviewLoop_InterruptMidFixConfirmsOnResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repo, artDir := newFRGitFeature(t, env, "fr-interrupt-fix")
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{
		"iteration-01/craft": "CSV export is missing",
		"iteration-03/qa":    "export button stays disabled",
	})
	fixBody := frCommitChange(repo) + fmt.Sprintf(`
if [ "$(basename "$_iter")" = iteration-03 ] && [ ! -f %q ]; then
  touch "$_iter/fixing"
  while [ ! -f "$_iter/release" ]; do sleep 0.05; done
fi`, filepath.Join(artDir, "resumed"))
	fixScript := frFixerScript(t, env, artDir, fixBody, ":")

	go func() {
		iter3 := filepath.Join(artDir, "iteration-03")
		for range 600 {
			if _, err := os.Stat(filepath.Join(iter3, "fixing")); err == nil {
				_ = store.Modify(f.ID, func(ff *feature.Feature) error { ff.Status = feature.StatusInterrupted; return nil })
				_ = os.WriteFile(filepath.Join(iter3, "release"), nil, 0o644)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	result, _ := runFRLoop(t, f, store, env, reviewScript, fixScript, 8, nil)
	if result.FinalStatus != "interrupted" {
		t.Fatalf("FinalStatus = %q, want interrupted (LastError=%q)", result.FinalStatus, result.LastError)
	}

	if err := store.Modify(f.ID, func(ff *feature.Feature) error { ff.Status = feature.StatusFinalReviewing; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "resumed"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	resumed, _ := runFRLoop(t, f, store, env, frReviewerScript(t, env, artDir, nil), fixScript, 8, nil)
	if resumed.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("resumed FinalStatus = %q, want review_passed (LastError=%q)", resumed.FinalStatus, resumed.LastError)
	}
	if !frAxisRanAfter(artDir, 3, "craft") {
		t.Fatal("craft approved in iteration 2 and never reviewed the iteration 3 fix")
	}
}

// A handoff session that errors leaves the question pending and parks the
// feature; resuming asks again without rerunning the review round.
func TestRunFeatureFinalReviewLoop_HandoffSessionErrorStaysPending(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, artDir := newFRGitFeature(t, env, "fr-handoff-error")
	runsLog := filepath.Join(env.scriptsDir, "review-runs.log")
	reviewScript := frCountingReviewerScript(t, env, artDir, runsLog, map[string]string{"iteration-01/craft": "CSV export is missing"})
	failing := frFixerScript(t, env, artDir, ":", testutil.JSONLError("provider unavailable")+"\nexit 1")
	result, _ := runFRLoop(t, f, store, env, reviewScript, failing, 5, nil)
	if result.FinalStatus != finalStatusStopped {
		t.Fatalf("FinalStatus = %q, want stopped so the pending handoff resumes (LastError=%q)", result.FinalStatus, result.LastError)
	}
	before := readFRFile(t, runsLog)

	asking := frFixerScript(t, env, artDir, ":", frHandoffAsk())
	resumed, seen := runFRLoop(t, f, store, env, reviewScript, asking, 5, func(string) string { return handoffAcceptLabel })
	if resumed.FinalStatus != finalStatusReviewPassed || resumed.Iterations != 1 {
		t.Fatalf("resumed = %+v, want review_passed in iteration 1", resumed)
	}
	if len(seen) != 1 {
		t.Fatalf("questions = %q, want the handoff asked again", seen)
	}
	if after := readFRFile(t, runsLog); after != before {
		t.Fatalf("the review round reran on resume: %q -> %q", before, after)
	}
}

// A round the loop owes, a confirmation or the review of the user's
// guidance, runs even past MaxIterations.
func TestRunFeatureFinalReviewLoop_OwedRoundsRunPastMaxIterations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Run("confirmation", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, repo, artDir := newFRGitFeature(t, env, "fr-owed-confirm")
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
		result, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, frCommitChange(repo), ":"), 2, nil)
		if result.FinalStatus != finalStatusReviewPassed || result.Iterations != 3 {
			t.Fatalf("result = %+v, want review_passed after the owed confirmation in iteration 3", result)
		}
	})
	t.Run("guidance", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, repo, artDir := newFRGitFeature(t, env, "fr-owed-guidance")
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
		fixScript := frFixerScript(t, env, artDir, ":", frHandoffAsk()+"\n"+frCommitChange(repo))
		result, _ := runFRLoop(t, f, store, env, reviewScript, fixScript, 1, func(string) string { return "Add the CSV export." })
		if result.FinalStatus != finalStatusReviewPassed {
			t.Fatalf("result = %+v, want review_passed after reviewing the guided fix", result)
		}
		if !frAxisRan(artDir, 2, "craft") {
			t.Fatal("the guided fix was never reviewed")
		}
	})
}

// Only dispute and handoff answers are decisions: a clarifying question
// neither lands in the decisions file nor counts as progress.
func TestRunFeatureFinalReviewLoop_ClarifyingQuestionIsNotADecision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, artDir := newFRGitFeature(t, env, "fr-clarify")
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
	const clarify = "Which logger should the export use?"
	fixScript := frFixerScript(t, env, artDir, frAskUser(clarify, "slog", "zap"), frHandoffAsk())
	result, _ := runFRLoop(t, f, store, env, reviewScript, fixScript, 5, func(q string) string {
		if q == clarify {
			return "slog"
		}
		return handoffStopLabel
	})
	if result.FinalStatus != finalStatusStopped || result.Iterations != 1 {
		t.Fatalf("result = %+v, want the no-change handoff in iteration 1", result)
	}
	if decisions := readFRFile(t, filepath.Join(artDir, reviewDecisionsFile)); strings.Contains(decisions, clarify) || !strings.Contains(decisions, "## Iteration 1 handoff") {
		t.Fatalf("decisions file must hold only the handoff answer:\n%s", decisions)
	}
}

func TestRunFeatureFinalReviewLoop_NoChangeDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Run("multi_repo", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, _, artDir := newFRGitFeature(t, env, "fr-no-change-multi")
		second := testutil.InitGitRepo(t)
		f.Repos = append(f.Repos, feature.FeatureRepo{Name: testRepoNameWeb, Path: second, WorktreePath: second})
		f.RepoStates[testRepoNameWeb] = &feature.RepoState{Touched: true}
		if err := store.Save(f); err != nil {
			t.Fatal(err)
		}
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
		result, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, ":", frHandoffAsk()), 5, func(string) string { return handoffStopLabel })
		if result.FinalStatus != finalStatusStopped || result.Iterations != 1 {
			t.Fatalf("result = %+v, want the no-change handoff in iteration 1", result)
		}
	})
	t.Run("same_head_same_dirt", func(t *testing.T) {
		env := newFRLoopEnv(t)
		store, f, repo, artDir := newFRGitFeature(t, env, "fr-no-change-dirt")
		tracked := filepath.Join(repo, "tracked.txt")
		if err := os.WriteFile(tracked, []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitCommitAll(t, repo, "tracked")
		reviewScript := frReviewerScript(t, env, artDir, map[string]string{
			"iteration-01/craft": "CSV export is missing",
			"iteration-02/craft": "CSV export is still missing",
		})
		fixBody := fmt.Sprintf(`if [ "$(basename "$_iter")" = iteration-01 ]; then echo v2 > %q; fi`, tracked)
		result, _ := runFRLoop(t, f, store, env, reviewScript, frFixerScript(t, env, artDir, fixBody, frHandoffAsk()), 5, func(string) string { return handoffStopLabel })
		if result.FinalStatus != finalStatusStopped || result.Iterations != 2 {
			t.Fatalf("result = %+v, want the no-change handoff in iteration 2", result)
		}
		if note := readFRFile(t, filepath.Join(artDir, "iteration-02", handoffDirName, "handoff.md")); !strings.Contains(note, "Fix round 2 made no code changes") {
			t.Fatalf("handoff note = %q", note)
		}
	})
}
