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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

type phaseGateEnv struct {
	repo        string
	artifactDir string
	stateDir    string
	scriptsDir  string
	planPath    string
	store       *feature.Store
	f           *feature.Feature
}

// newPhaseGateEnv seeds a phase-1 feature whose repo is a real git
// repository, so the loop can tell whether an implement iteration changed
// anything.
func newPhaseGateEnv(t *testing.T, id string, pipeline feature.PipelineProfile) *phaseGateEnv {
	t.Helper()
	tmp := t.TempDir()
	env := &phaseGateEnv{
		repo:        testutil.InitGitRepo(t),
		artifactDir: filepath.Join(tmp, "artifacts"),
		stateDir:    filepath.Join(tmp, "state", id),
		scriptsDir:  filepath.Join(tmp, "scripts"),
	}
	for _, dir := range []string{env.artifactDir, env.stateDir, env.scriptsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.planPath = writePlanFile(t, env.artifactDir, "# Phase 1: Retry\n\n## Tasks\n\n### Task 1: Retry with backoff\n")
	env.f = newTestFeature(t, env.repo)
	env.f.ID = id
	env.f.Pipeline = pipeline
	env.f.CurrentRoadmapPhase = 1
	env.store = feature.NewStore(filepath.Join(tmp, "state"))
	if err := env.store.Save(env.f); err != nil {
		t.Fatal(err)
	}
	return env
}

type phaseGateRun struct {
	result           *LoopResult
	reviewPrompts    map[string]string
	implementPrompts []string
	questions        []string
}

func (r phaseGateRun) reviewed() []string {
	keys := make([]string, 0, len(r.reviewPrompts))
	for key := range r.reviewPrompts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// phaseGateCommit commits a change to the repo, as an implementer fixing a
// finding would.
func phaseGateCommit(repo string) string {
	return frCommitChange(repo)
}

func phaseGateHandoffAsk() string {
	return frAskUser("How should Phase 1 review proceed?", handoffContinueLabel, phaseHandoffAcceptLabel, handoffStopLabel)
}

// runPhaseGate drives the implementation loop. The k-th implement session
// runs implementBodies[k] then reports success with a closing note; every
// review axis approves unless changes names its "iteration-NN/<axis>" key
// ("iteration-NN/confirm/<axis>" in a confirmation round).
func runPhaseGate(t *testing.T, env *phaseGateEnv, maxIterations int, implementBodies []string, changes map[string]string, answer func(question string) string) phaseGateRun {
	t.Helper()
	var arms strings.Builder
	for key, finding := range changes {
		fmt.Fprintf(&arms, "    %s) _verdict=CHANGES_REQUESTED; _findings=%q ;;\n", key, "- **High**: "+finding)
	}
	reviewScript := testutil.WriteScript(t, env.scriptsDir, "review.sh", testutil.JSONLInit+"\n"+fmt.Sprintf(`for _prompt in $(find "%s" -name review-prompt.md -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _fb="$_dir/review-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _key=$(echo "${_dir#%s/}" | sed 's#/review/#/#')
  _verdict="APPROVED"
  _findings="- (none)"
  case "$_key" in
%s  esac
  _tmp="$_fb.tmp.$$"
  printf '## Findings\n%%s\n\n## Suggestions\n- (none)\n\n## Verdict\n%%s\n' "$_findings" "$_verdict" > "$_tmp"
  mv "$_tmp" "$_fb"
done`, env.artifactDir, env.artifactDir, arms.String())+"\n"+testutil.JSONLSuccess+"\n")

	var mu sync.Mutex
	run := phaseGateRun{reviewPrompts: map[string]string{}}
	buildSession := func(opts BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		mu.Lock()
		defer mu.Unlock()
		if isReviewHelper(opts.PermHandler) {
			_, after, _ := strings.Cut(opts.Prompt, "Review feedback output: ")
			feedbackPath, _, _ := strings.Cut(after, "\n")
			rel, _ := filepath.Rel(env.artifactDir, filepath.Dir(feedbackPath))
			run.reviewPrompts[strings.Replace(rel, "/review/", "/", 1)] = opts.Prompt
			return mockBuildSession("", reviewScript)(opts)
		}
		k := len(run.implementPrompts)
		run.implementPrompts = append(run.implementPrompts, opts.Prompt)
		body := ""
		if k < len(implementBodies) {
			body = implementBodies[k]
		}
		script := testutil.WriteScript(t, env.scriptsDir, fmt.Sprintf("implement-%02d.sh", k+1), testutil.JSONLInit+"\n"+body+"\n"+
			testutil.WriteImplementNotes(env.artifactDir)+"\n"+testutil.JSONLAssistant(fmt.Sprintf("Implementer note %d.", k+1))+"\n"+testutil.JSONLSuccess+"\n")
		return mockBuildSession(script, "")(opts)
	}

	sm := session.NewManager(make(chan interface{}, 100))
	defer sm.Shutdown()
	finish := answerFRQuestions(t, sm, env.f.ID, answer)

	result, err := runImplementationLoop(ImplementConfig{
		Feature: env.f, FeatureStore: env.store, WorkDir: env.repo, PlanPath: env.planPath,
		MaxIterations: maxIterations, MaxConsecFails: 3, MaxConsecNoProgress: 5,
		ExitCriteria: "Retries back off.", Model: "agent", ReviewModel: "reviewer",
		ArtifactDir: env.artifactDir, StateDir: env.stateDir, BuildSession: buildSession,
		SkipIterationReview: env.f.EffectivePipeline().ShouldSkipIterationReview(),
	}, sm)
	if err != nil {
		t.Fatalf("runImplementationLoop() error = %v", err)
	}
	run.result = result
	run.questions = finish()
	return run
}

func phaseGateMeta(t *testing.T, env *phaseGateEnv, iteration int) iterationMeta {
	t.Helper()
	meta, err := (&artifactManager{}).readMeta(filepath.Join(env.artifactDir, fmt.Sprintf("iteration-%02d", iteration)))
	if err != nil {
		t.Fatalf("read iteration %d meta: %v", iteration, err)
	}
	return meta
}

func requireReviewed(t *testing.T, run phaseGateRun, want ...string) {
	t.Helper()
	sort.Strings(want)
	if got := run.reviewed(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reviewed axes = %v, want %v", got, want)
	}
}

func requirePromptContains(t *testing.T, label, prompt string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(prompt, want) {
			t.Fatalf("%s missing %q:\n%s", label, want, prompt)
		}
	}
}

var phaseGateFirstRound = []string{"iteration-01/cleanliness", "iteration-01/craft", "iteration-01/functionality-evidence"}

func TestPhaseReviewGate_FirstReviewApprovalPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-approve", feature.PipelineMoonshot)
	run := runPhaseGate(t, env, 3, nil, nil, nil)

	if run.result.FinalStatus != finalStatusReviewPassed || run.result.Iterations != 1 {
		t.Fatalf("result = %+v, want review_passed after 1 iteration", run.result)
	}
	requireReviewed(t, run, phaseGateFirstRound...)
	for key, prompt := range run.reviewPrompts {
		requirePromptContains(t, key, prompt, "## Acceptance Authority")
		if strings.Contains(prompt, "## Round Scope") {
			t.Fatalf("%s first round was narrowed:\n%s", key, prompt)
		}
	}
	if verdicts := phaseGateMeta(t, env, 1).AxisVerdicts; len(verdicts) != 3 {
		t.Fatalf("iteration 1 axis verdicts = %v, want all three axes", verdicts)
	}
}

func TestPhaseReviewGate_MediumAndLargeSkipReview(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	for _, pipeline := range []feature.PipelineProfile{feature.PipelineMedium, feature.PipelineLarge} {
		t.Run(string(pipeline), func(t *testing.T) {
			env := newPhaseGateEnv(t, "feat-phase-skip-"+string(pipeline), pipeline)
			run := runPhaseGate(t, env, 3, nil, map[string]string{"iteration-01/craft": "never reviewed"}, nil)
			if run.result.FinalStatus != finalStatusReviewPassed || run.result.Iterations != 1 {
				t.Fatalf("result = %+v, want review_passed after 1 iteration", run.result)
			}
			requireReviewed(t, run)
		})
	}
}

func TestPhaseReviewGate_StickyApprovalsConfirmOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-sticky", feature.PipelineMoonshot)
	run := runPhaseGate(t, env, 4, []string{"", phaseGateCommit(env.repo)}, map[string]string{
		"iteration-01/craft": "retry loop has no backoff",
	}, nil)

	if run.result.FinalStatus != finalStatusReviewPassed || run.result.Iterations != 2 {
		t.Fatalf("result = %+v, want review_passed after 2 iterations", run.result)
	}
	requireReviewed(t, run, append([]string{
		"iteration-02/craft",
		"iteration-02/confirm/cleanliness",
		"iteration-02/confirm/functionality-evidence",
	}, phaseGateFirstRound...)...)
	requirePromptContains(t, "iteration 2 implement prompt", run.implementPrompts[1], "retry loop has no backoff")
	requirePromptContains(t, "iteration 2 craft prompt", run.reviewPrompts["iteration-02/craft"],
		"## Round Scope: Follow-Up",
		filepath.Join(env.artifactDir, "iteration-01", "review", "craft", "review-feedback.md"),
		"`cd "+env.repo+" && git diff ",
		filepath.Join(env.artifactDir, "iteration-02", fixNotesFile))
	requirePromptContains(t, "iteration 2 cleanliness confirmation", run.reviewPrompts["iteration-02/confirm/cleanliness"], "## Round Scope: Confirmation")
	requirePromptContains(t, "iteration 2 functionality confirmation", run.reviewPrompts["iteration-02/confirm/functionality-evidence"],
		"## Round Scope: Confirmation", "Rerun the checks you ran for that approval")
	if note := readFRFile(t, filepath.Join(env.artifactDir, "iteration-02", fixNotesFile)); !strings.Contains(note, "Implementer note 2.") {
		t.Fatalf("iteration 2 fix note = %q", note)
	}
	if verdicts := phaseGateMeta(t, env, 2).AxisVerdicts; len(verdicts) != 3 {
		t.Fatalf("iteration 2 axis verdicts = %v, want the follow-up and both confirmations", verdicts)
	}
}

func TestPhaseReviewGate_DisputeRecordsDecisionForLaterRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-dispute", feature.PipelineMoonshot)
	dispute := "Dispute: Craft: **High**: rename the retry helper. The roadmap keeps the existing name."
	run := runPhaseGate(t, env, 4, []string{
		"",
		frAskUser(dispute, "Fix it", "Accept as is", "Change the scope") + "\n" + phaseGateCommit(env.repo),
		phaseGateCommit(env.repo),
	}, map[string]string{
		"iteration-01/craft":       "rename the retry helper",
		"iteration-01/cleanliness": "drop the debug log",
		"iteration-02/cleanliness": "debug log still present",
	}, func(string) string { return "Accept as is" })

	if run.result.FinalStatus != finalStatusReviewPassed || run.result.Iterations != 3 {
		t.Fatalf("result = %+v, want review_passed after 3 iterations", run.result)
	}
	decisionsPath := filepath.Join(env.artifactDir, reviewDecisionsFile)
	decisions := readFRFile(t, decisionsPath)
	for _, want := range []string{"## Iteration 2 implementation", "rename the retry helper", "**A:** Accept as is"} {
		if !strings.Contains(decisions, want) {
			t.Fatalf("decisions missing %q:\n%s", want, decisions)
		}
	}
	requireReviewed(t, run, append([]string{
		"iteration-02/cleanliness",
		"iteration-02/craft",
		"iteration-03/cleanliness",
		"iteration-03/confirm/craft",
		"iteration-03/confirm/functionality-evidence",
	}, phaseGateFirstRound...)...)
	for _, key := range []string{"iteration-02/craft", "iteration-02/cleanliness", "iteration-03/cleanliness"} {
		requirePromptContains(t, key, run.reviewPrompts[key], "decisions: "+decisionsPath, "Do not raise again a finding")
	}
	requirePromptContains(t, "iteration 3 implement prompt", run.implementPrompts[2], "## Approved Review Decisions", decisionsPath)
}

func TestPhaseReviewGate_NoChangeFixHandsOff(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	tests := []struct {
		name       string
		ask        bool
		answer     string
		wantStatus string
	}{
		{name: "stop", ask: true, answer: handoffStopLabel, wantStatus: finalStatusStopped},
		{name: "accept", ask: true, answer: phaseHandoffAcceptLabel, wantStatus: finalStatusReviewPassed},
		{name: "unanswered", wantStatus: finalStatusStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPhaseGateEnv(t, "feat-phase-nochange-"+tt.name, feature.PipelineMoonshot)
			handoffBody := ""
			if tt.ask {
				handoffBody = phaseGateHandoffAsk()
			}
			changes := map[string]string{"iteration-01/craft": "retry loop has no backoff"}
			run := runPhaseGate(t, env, 5, []string{"", "", handoffBody}, changes, func(string) string { return tt.answer })

			if run.result.FinalStatus != tt.wantStatus || run.result.Iterations != 3 {
				t.Fatalf("result = %+v, want %s after 3 iterations", run.result, tt.wantStatus)
			}
			requireReviewed(t, run, phaseGateFirstRound...)
			if got := phaseGateMeta(t, env, 2).Handoff; got != reviewHandoffAsked {
				t.Fatalf("iteration 2 handoff = %q, want %q", got, reviewHandoffAsked)
			}
			note := readFRFile(t, filepath.Join(env.artifactDir, "iteration-02", handoffDirName, "handoff.md"))
			for _, want := range []string{"Fix iteration 2 made no code changes", "retry loop has no backoff", "Implementer note 2."} {
				if !strings.Contains(note, want) {
					t.Fatalf("handoff note missing %q:\n%s", want, note)
				}
			}
			requirePromptContains(t, "iteration 3 implement prompt", run.implementPrompts[2],
				"## Handoff to the User", "Phase 1 review", phaseHandoffAcceptLabel, "retry loop has no backoff")

			switch tt.name {
			case "stop":
				if !strings.Contains(run.result.LastError, "made no code changes") {
					t.Fatalf("LastError = %q", run.result.LastError)
				}
				if got := phaseGateMeta(t, env, 3).Handoff; got != reviewHandoffStopped {
					t.Fatalf("iteration 3 handoff = %q, want %q", got, reviewHandoffStopped)
				}
				if decisions := readFRFile(t, filepath.Join(env.artifactDir, reviewDecisionsFile)); !strings.Contains(decisions, "## Iteration 3 handoff") {
					t.Fatalf("decisions missing the handoff answer:\n%s", decisions)
				}
				resumed := runPhaseGate(t, env, 6, []string{phaseGateCommit(env.repo)}, changes, nil)
				if resumed.result.FinalStatus != finalStatusReviewPassed || resumed.result.Iterations != 4 {
					t.Fatalf("resumed result = %+v, want review_passed after 4 iterations", resumed.result)
				}
				requirePromptContains(t, "resumed implement prompt", resumed.implementPrompts[0], "retry loop has no backoff")
				if strings.Contains(resumed.implementPrompts[0], "## Handoff to the User") {
					t.Fatalf("resumed loop asked the answered handoff again")
				}
				requireReviewed(t, resumed, "iteration-04/craft", "iteration-04/confirm/cleanliness", "iteration-04/confirm/functionality-evidence")
			case "unanswered":
				if !strings.Contains(run.result.LastError, "has no answer yet") {
					t.Fatalf("LastError = %q", run.result.LastError)
				}
			}
		})
	}
}

func TestPhaseReviewGate_StalledAxisHandsOffAfterThreeRounds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	tests := []struct {
		name       string
		answer     string
		wantStatus string
	}{
		{name: "continue", answer: "Use exponential backoff with jitter.", wantStatus: finalStatusReviewPassed},
		{name: "accept", answer: phaseHandoffAcceptLabel, wantStatus: finalStatusReviewPassed},
		{name: "stop", answer: handoffStopLabel, wantStatus: finalStatusStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newPhaseGateEnv(t, "feat-phase-stall-"+tt.name, feature.PipelineMoonshot)
			commit := phaseGateCommit(env.repo)
			handoffBody := phaseGateHandoffAsk()
			if tt.name == "continue" {
				handoffBody += "\n" + commit
			}
			run := runPhaseGate(t, env, 6, []string{"", commit, commit, handoffBody}, map[string]string{
				"iteration-01/craft": "retry loop has no backoff",
				"iteration-02/craft": "backoff is linear",
				"iteration-03/craft": "backoff is still linear",
			}, func(string) string { return tt.answer })

			if run.result.FinalStatus != tt.wantStatus || run.result.Iterations != 4 {
				t.Fatalf("result = %+v, want %s after 4 iterations", run.result, tt.wantStatus)
			}
			if got := phaseGateMeta(t, env, 3).Handoff; got != reviewHandoffAsked {
				t.Fatalf("iteration 3 handoff = %q, want %q", got, reviewHandoffAsked)
			}
			note := readFRFile(t, filepath.Join(env.artifactDir, "iteration-03", handoffDirName, "handoff.md"))
			for _, want := range []string{"The Craft axis requested changes in 3 consecutive rounds.", "backoff is still linear", "Implementer note 3."} {
				if !strings.Contains(note, want) {
					t.Fatalf("handoff note missing %q:\n%s", want, note)
				}
			}
			requirePromptContains(t, "iteration 4 implement prompt", run.implementPrompts[3], "## Handoff to the User", "The Craft axis requested changes in 3 consecutive rounds.")
			if len(run.questions) != 1 || !strings.Contains(run.questions[0], "How should Phase 1 review proceed?") {
				t.Fatalf("questions = %q, want the one handoff question", run.questions)
			}
			rounds := append([]string{"iteration-02/craft", "iteration-03/craft"}, phaseGateFirstRound...)

			switch tt.name {
			case "continue":
				requireReviewed(t, run, append(rounds, "iteration-04/craft", "iteration-04/confirm/cleanliness", "iteration-04/confirm/functionality-evidence")...)
				decisionsPath := filepath.Join(env.artifactDir, reviewDecisionsFile)
				if decisions := readFRFile(t, decisionsPath); !strings.Contains(decisions, tt.answer) {
					t.Fatalf("decisions missing the guidance:\n%s", decisions)
				}
				requirePromptContains(t, "iteration 4 craft prompt", run.reviewPrompts["iteration-04/craft"], "decisions: "+decisionsPath)
				if got := phaseGateMeta(t, env, 4).Handoff; got != reviewHandoffContinued {
					t.Fatalf("iteration 4 handoff = %q, want %q", got, reviewHandoffContinued)
				}
			case "accept":
				requireReviewed(t, run, rounds...)
				if got := phaseGateMeta(t, env, 4).Handoff; got != reviewHandoffAccepted {
					t.Fatalf("iteration 4 handoff = %q, want %q", got, reviewHandoffAccepted)
				}
			case "stop":
				requireReviewed(t, run, rounds...)
				if !strings.Contains(run.result.LastError, "3 consecutive rounds") {
					t.Fatalf("LastError = %q", run.result.LastError)
				}
			}
		})
	}
}

// A clarifying question is not a review decision: it neither lands in the
// decisions file nor saves a fix iteration that changed nothing.
func TestPhaseReviewGate_ClarifyingQuestionIsNotADecision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-clarify", feature.PipelineMoonshot)
	const clarify = "Which logger should the retry loop use?"
	run := runPhaseGate(t, env, 5, []string{"", frAskUser(clarify, "slog", "zap"), phaseGateHandoffAsk()},
		map[string]string{"iteration-01/craft": "retry loop has no backoff"},
		func(q string) string {
			if q == clarify {
				return "slog"
			}
			return handoffStopLabel
		})
	if run.result.FinalStatus != finalStatusStopped || run.result.Iterations != 3 {
		t.Fatalf("result = %+v, want the no-change handoff stopped in iteration 3", run.result)
	}
	if got := phaseGateMeta(t, env, 2).Handoff; got != reviewHandoffAsked {
		t.Fatalf("iteration 2 handoff = %q, want %q", got, reviewHandoffAsked)
	}
	if decisions := readFRFile(t, filepath.Join(env.artifactDir, reviewDecisionsFile)); strings.Contains(decisions, clarify) {
		t.Fatalf("clarifying question recorded as a decision:\n%s", decisions)
	}
}

// A handoff raised on the last allowed iteration is still asked.
func TestPhaseReviewGate_HandoffAtMaxIterationsIsAsked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping loop test in short mode")
	}
	env := newPhaseGateEnv(t, "feat-phase-cap", feature.PipelineMoonshot)
	run := runPhaseGate(t, env, 2, []string{"", "", phaseGateHandoffAsk()},
		map[string]string{"iteration-01/craft": "retry loop has no backoff"},
		func(string) string { return phaseHandoffAcceptLabel })
	if run.result.FinalStatus != finalStatusReviewPassed || run.result.Iterations != 3 {
		t.Fatalf("result = %+v, want the handoff asked and accepted in iteration 3", run.result)
	}
}
