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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// newFRGitFeature seeds a Large-profile Final Review feature whose single
// repo is a real git repository, so the loop can tell whether a fix round
// changed anything.
func newFRGitFeature(t *testing.T, env frLoopTestEnv, id string) (*feature.Store, *feature.Feature, string, string) {
	t.Helper()
	store, f, _ := newFRTestFeature(t, env.stateDir, id, []string{testRepoNameAPI})
	f.Pipeline = feature.PipelineLarge
	repo := testutil.InitGitRepo(t)
	f.Repos[0].Path = repo
	f.Repos[0].WorktreePath = repo
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	artDir := frArtifactDir(env.stateDir, f)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact: %v", err)
	}
	return store, f, repo, artDir
}

// frAxisVerdictScript writes each scheduled axis's feedback: CHANGES_REQUESTED
// with the given finding for "iteration-NN/<axis>" keys, APPROVED otherwise.
func frAxisVerdictScript(artDir string, changes map[string]string) string {
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var arms strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&arms, "    %s) _verdict=CHANGES_REQUESTED; _findings=%q ;;\n", key, "- **High**: "+changes[key])
	}
	return fmt.Sprintf(`for _prompt in $(find "%s" -mindepth 3 -maxdepth 3 -name review-prompt.md -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _axis=$(basename "$_dir")
  _iter=$(basename "$(dirname "$_dir")")
  _fb="$_dir/review-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _verdict="APPROVED"
  _findings="- (none)"
  case "$_iter/$_axis" in
%s  esac
  _tmp="$_fb.tmp.$$"
  printf '## Findings\n%%s\n\n## Suggestions\n- (none)\n\n## Verdict\n%%s\n' "$_findings" "$_verdict" > "$_tmp"
  mv "$_tmp" "$_fb"
done`, artDir, arms.String())
}

func frReviewerScript(t *testing.T, env frLoopTestEnv, artDir string, changes map[string]string) string {
	t.Helper()
	return testutil.WriteScript(t, env.scriptsDir, "review.sh",
		testutil.JSONLInit+"\n"+frAxisVerdictScript(artDir, changes)+"\n"+testutil.JSONLSuccess+"\n")
}

// frFixerScript runs fixBody in regular fix sessions and handoffBody in
// handoff sessions, then ends with a closing note and a success outcome.
func frFixerScript(t *testing.T, env frLoopTestEnv, artDir, fixBody, handoffBody string) string {
	t.Helper()
	return testutil.WriteScript(t, env.scriptsDir, "fix.sh", fmt.Sprintf(`%s
_iter=$(ls -d "%s"/iteration-* | sort | tail -1)
if [ -f "$_iter/handoff/fix-prompt.md" ]; then
%s
else
%s
fi
%s
`, testutil.JSONLInit, artDir, handoffBody, fixBody, testutil.JSONLSuccess))
}

func frCommitChange(repo string) string {
	return fmt.Sprintf(`date +%%s%%N >> %q/fix.txt && git -C %q add -A && git -C %q commit -qm fix`, repo, repo, repo)
}

func frAskUser(question string, labels ...string) string {
	options := make([]string, 0, len(labels))
	for _, label := range labels {
		options = append(options, fmt.Sprintf(`{"label":%q,"description":"%s"}`, label, label))
	}
	input := fmt.Sprintf(`{"questions":[{"question":%q,"header":"Final Review","multiSelect":false,"options":[%s]}]}`, question, strings.Join(options, ","))
	return fmt.Sprintf(`echo '{"type":"control_request","request_id":"ask_%d","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":%s}}'
while read -r _line; do case "$_line" in *control_response*) break ;; esac; done`, time.Now().UnixNano(), input)
}

func frHandoffAsk() string {
	return frAskUser("How should Final Review proceed?", handoffContinueLabel, handoffAcceptLabel, handoffStopLabel)
}

// answerFRQuestions answers every AskUserQuestion the feature's sessions
// raise with answer(question), recording the questions it saw.
func answerFRQuestions(t *testing.T, sm ports.SessionManager, featureID string, answer func(question string) string) func() []string {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	var mu sync.Mutex
	var seen []string
	go func() {
		defer close(stopped)
		answered := map[string]bool{}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			}
			for _, view := range sm.FeatureSessions(featureID) {
				for _, req := range view.PendingControlRequests() {
					if req.Request.ToolName != "AskUserQuestion" || answered[req.RequestID] {
						continue
					}
					bundle, err := askuser.Parse(req.Request.Input)
					if err != nil || len(bundle.Questions) == 0 {
						continue
					}
					question := bundle.Questions[0].Question
					resolved, err := bundle.Resolve(map[string]string{"1": answer(question)})
					if err != nil {
						continue
					}
					if err := view.RespondToAskUser(req.RequestID, resolved); err == nil {
						answered[req.RequestID] = true
						mu.Lock()
						seen = append(seen, question)
						mu.Unlock()
					}
				}
			}
		}
	}()
	var once sync.Once
	finish := func() []string {
		once.Do(func() {
			close(done)
			<-stopped
		})
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	t.Cleanup(func() { finish() })
	return finish
}

func frLoopConfig(f *feature.Feature, store *feature.Store, env frLoopTestEnv, reviewScript, fixScript string, maxIterations int) OrchestratorConfig {
	return OrchestratorConfig{
		Feature:        f,
		FeatureStore:   store,
		StateDir:       env.stateDir,
		Model:          "agent",
		ReviewModel:    "reviewer",
		MaxIterations:  maxIterations,
		MaxConsecFails: 3,
		BuildSession: mockBuildSessionByModel(map[string]string{
			"reviewer": reviewScript,
			"agent":    fixScript,
		}),
	}
}

func readFRFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func frAxisRan(artDir string, iteration int, slug string) bool {
	_, err := os.Stat(filepath.Join(artDir, fmt.Sprintf("iteration-%02d", iteration), slug, "review-prompt.md"))
	return err == nil
}

func TestRunFeatureFinalReviewLoop_NoChangeFixHandsOff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	const fixerNote = "Changed nothing: the CSV export the finding asks for was dropped from scope."
	tests := []struct {
		name       string
		answer     string
		ask        bool
		wantStatus string
	}{
		{name: "stop", answer: handoffStopLabel, ask: true, wantStatus: finalStatusStopped},
		{name: "accept", answer: handoffAcceptLabel, ask: true, wantStatus: finalStatusReviewPassed},
		{name: "unanswered", wantStatus: finalStatusStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newFRLoopEnv(t)
			store, f, _, artDir := newFRGitFeature(t, env, "fr-no-change-"+tt.name)
			reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
			handoffBody := ":"
			if tt.ask {
				handoffBody = frHandoffAsk()
			}
			fixScript := frFixerScript(t, env, artDir, testutil.JSONLAssistant(fixerNote), handoffBody)

			eventCh := make(chan any, 100)
			sm := session.NewManager(eventCh)
			defer sm.Shutdown()
			finish := answerFRQuestions(t, sm, f.ID, func(string) string { return tt.answer })

			result, err := runFeatureFinalReviewLoop(frLoopConfig(f, store, env, reviewScript, fixScript, 5), sm)
			if err != nil {
				t.Fatalf("loop: %v", err)
			}
			if result.FinalStatus != tt.wantStatus {
				t.Fatalf("FinalStatus = %q, want %q (LastError=%q)", result.FinalStatus, tt.wantStatus, result.LastError)
			}
			if result.Iterations != 1 {
				t.Fatalf("Iterations = %d, want 1: no review round may follow a fix that changed nothing", result.Iterations)
			}
			if _, err := os.Stat(filepath.Join(artDir, "iteration-02")); !os.IsNotExist(err) {
				t.Fatalf("iteration-02 exists after a no-change fix: %v", err)
			}

			handoffNote := readFRFile(t, filepath.Join(artDir, "iteration-01", "handoff", "handoff.md"))
			for _, want := range []string{"Fix round 1 made no code changes", "CSV export is missing", fixerNote} {
				if !strings.Contains(handoffNote, want) {
					t.Fatalf("handoff note missing %q:\n%s", want, handoffNote)
				}
			}
			handoffPrompt := readFRFile(t, filepath.Join(artDir, "iteration-01", "handoff", "fix-prompt.md"))
			for _, want := range []string{"## Handoff to the User", handoffContinueLabel, handoffAcceptLabel, handoffStopLabel, "Craft: **High**: CSV export is missing"} {
				if !strings.Contains(handoffPrompt, want) {
					t.Fatalf("handoff prompt missing %q:\n%s", want, handoffPrompt)
				}
			}

			finish()
			decisions := filepath.Join(artDir, reviewDecisionsFile)
			if !tt.ask {
				if !strings.Contains(result.LastError, "has no answer yet") || !strings.Contains(result.LastError, "handoff.md") {
					t.Fatalf("LastError = %q, want the pending-handoff reason and note path", result.LastError)
				}
				if meta, err := (&artifactManager{}).readMeta(filepath.Join(artDir, "iteration-01")); err != nil || meta.Pending != pendingHandoff {
					t.Fatalf("iteration 1 meta = %+v, %v; want the handoff still pending", meta, err)
				}
				if _, err := os.Stat(decisions); !os.IsNotExist(err) {
					t.Fatalf("decisions file written without an answer: %v", err)
				}
				return
			}
			if body := readFRFile(t, decisions); !strings.Contains(body, "## Iteration 1 handoff") || !strings.Contains(body, "**A:** "+tt.answer) {
				t.Fatalf("decisions file missing the handoff answer:\n%s", body)
			}
		})
	}
}

func TestRunFeatureFinalReviewLoop_DisputedFindingRecordsDecisionForLaterReviewers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, _, artDir := newFRGitFeature(t, env, "fr-dispute")
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{"iteration-01/craft": "CSV export is missing"})
	const dispute = "Dispute: Craft: the CSV export is out of scope per the roadmap. Fix it anyway?"
	fixScript := frFixerScript(t, env, artDir,
		frAskUser(dispute, "Fix it", "Accept as is", "Change the scope")+"\n"+testutil.JSONLAssistant("Disputed the CSV finding; the user accepted it as is."),
		":")

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()
	finish := answerFRQuestions(t, sm, f.ID, func(string) string { return "Accept as is" })

	result, err := runFeatureFinalReviewLoop(frLoopConfig(f, store, env, reviewScript, fixScript, 5), sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	// The decision is progress, so round 2 runs instead of a handoff; the
	// fix changed no code, so the round-1 approvals need no confirmation.
	if result.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", result.Iterations)
	}
	if seen := finish(); len(seen) != 1 || seen[0] != dispute {
		t.Fatalf("questions asked = %q, want only the dispute", seen)
	}
	if _, err := os.Stat(filepath.Join(artDir, "iteration-01", "handoff")); !os.IsNotExist(err) {
		t.Fatalf("a recorded decision must not trigger the no-change handoff: %v", err)
	}

	decisionsPath := filepath.Join(artDir, reviewDecisionsFile)
	decisions := readFRFile(t, decisionsPath)
	for _, want := range []string{"## Iteration 1 fix", "**Q:** " + dispute, "**A:** Accept as is"} {
		if !strings.Contains(decisions, want) {
			t.Fatalf("decisions file missing %q:\n%s", want, decisions)
		}
	}

	for _, slug := range []string{"cleanliness", "qa"} {
		if frAxisRan(artDir, 2, slug) {
			t.Fatalf("approved axis %s reran in iteration 2", slug)
		}
	}
	reviewPrompt := readFRFile(t, filepath.Join(artDir, "iteration-02", "craft", "review-prompt.md"))
	for _, want := range []string{
		"Final Review decisions: " + decisionsPath,
		"Do not raise again a finding the decisions file records as accepted as is.",
		"## Round Scope: Follow-Up",
		filepath.Join(artDir, "iteration-01", "fix-notes.md"),
	} {
		if !strings.Contains(reviewPrompt, want) {
			t.Fatalf("iteration-02 craft prompt missing %q:\n%s", want, reviewPrompt)
		}
	}
}

func TestRunFeatureFinalReviewLoop_StalledAxisHandsOffAfterThreeRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	env := newFRLoopEnv(t)
	store, f, repo, artDir := newFRGitFeature(t, env, "fr-stall")
	reviewScript := frReviewerScript(t, env, artDir, map[string]string{
		"iteration-01/qa": "export button stays disabled",
		"iteration-02/qa": "export button stays disabled",
		"iteration-03/qa": "export button stays disabled",
	})
	const guidance = "Enable the button only after a dataset is selected."
	fixScript := frFixerScript(t, env, artDir,
		frCommitChange(repo),
		frHandoffAsk()+"\n"+frCommitChange(repo))

	eventCh := make(chan any, 100)
	sm := session.NewManager(eventCh)
	defer sm.Shutdown()
	finish := answerFRQuestions(t, sm, f.ID, func(string) string { return guidance })

	result, err := runFeatureFinalReviewLoop(frLoopConfig(f, store, env, reviewScript, fixScript, 6), sm)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if result.FinalStatus != finalStatusReviewPassed {
		t.Fatalf("FinalStatus = %q, want review_passed (LastError=%q)", result.FinalStatus, result.LastError)
	}
	// Rounds 1-3: QA requests changes; the third hands off instead of fixing.
	// Round 4: QA approves the guided fix. Round 5 confirms Craft and
	// Cleanliness, whose round-1 approvals predate the fixes.
	if result.Iterations != 5 {
		t.Fatalf("Iterations = %d, want 5", result.Iterations)
	}
	if seen := finish(); len(seen) != 1 {
		t.Fatalf("questions asked = %q, want one handoff", seen)
	}

	iter3 := filepath.Join(artDir, "iteration-03")
	if _, err := os.Stat(filepath.Join(iter3, "fix-prompt.md")); !os.IsNotExist(err) {
		t.Fatalf("a regular fix ran in the stalled round: %v", err)
	}
	if note := readFRFile(t, filepath.Join(iter3, "handoff", "handoff.md")); !strings.Contains(note, "The QA axis requested changes in 3 consecutive rounds.") {
		t.Fatalf("handoff note missing the stall reason:\n%s", note)
	}
	if decisions := readFRFile(t, filepath.Join(artDir, reviewDecisionsFile)); !strings.Contains(decisions, "**A:** "+guidance) {
		t.Fatalf("decisions file missing the guidance:\n%s", decisions)
	}
	for iteration, want := range map[int][]string{
		1: {"craft", "cleanliness", "qa"},
		2: {"qa"},
		3: {"qa"},
		4: {"qa"},
		5: {"craft", "cleanliness"},
	} {
		for _, slug := range []string{"craft", "cleanliness", "qa"} {
			ran := frAxisRan(artDir, iteration, slug)
			wanted := false
			for _, w := range want {
				wanted = wanted || w == slug
			}
			if ran != wanted {
				t.Fatalf("iteration %d %s ran = %v, want %v", iteration, slug, ran, wanted)
			}
		}
	}
	confirm := readFRFile(t, filepath.Join(artDir, "iteration-05", "craft", "review-prompt.md"))
	if !strings.Contains(confirm, "## Round Scope: Confirmation") || !strings.Contains(confirm, "`cd "+repo+" && git diff ") {
		t.Fatalf("confirmation prompt missing scope or fix delta:\n%s", confirm)
	}
}
