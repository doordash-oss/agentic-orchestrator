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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

const (
	verdictApproved = "APPROVED"
	verdictChanges  = "CHANGES_REQUESTED"
)

func finalGateAxesForTest() []implementationReviewAxis {
	return implementationReviewAxesForGate(implementationReviewGateFinal, implementationReviewAxisSelection{Profile: feature.PipelineLarge})
}

type plannedAxis struct {
	slug  string
	mode  reviewRoundMode
	since int
}

func plannedAxes(plan []reviewAxisRun) []plannedAxis {
	out := make([]plannedAxis, 0, len(plan))
	for _, run := range plan {
		out = append(out, plannedAxis{slug: run.Axis.Slug, mode: run.Mode, since: run.Since})
	}
	return out
}

func stateAt(head string) repoState {
	return repoState{Heads: map[string]string{"api": head}}
}

func requirePlan(t *testing.T, h *reviewRoundHistory, now repoState, want ...plannedAxis) {
	t.Helper()
	got := plannedAxes(h.planRound(finalGateAxesForTest(), now))
	if len(got) != len(want) {
		t.Fatalf("plan = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plan = %+v, want %+v", got, want)
		}
	}
}

func TestReviewRoundHistoryStickyApprovalsConfirmOnce(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}

	requirePlan(t, h, stateAt("aaa"),
		plannedAxis{"craft", reviewRoundFull, 0},
		plannedAxis{"cleanliness", reviewRoundFull, 0},
		plannedAxis{"qa", reviewRoundFull, 0})

	h.recordReview(1, map[string]string{"craft": verdictChanges, "cleanliness": verdictApproved, "qa": verdictApproved}, stateAt("aaa"))
	h.recordFix(1, true)
	requirePlan(t, h, stateAt("bbb"), plannedAxis{"craft", reviewRoundFollowUp, 1})

	h.recordReview(2, map[string]string{"craft": verdictApproved}, stateAt("bbb"))
	requirePlan(t, h, stateAt("bbb"),
		plannedAxis{"cleanliness", reviewRoundConfirm, 1},
		plannedAxis{"qa", reviewRoundConfirm, 1})

	h.recordReview(3, map[string]string{"cleanliness": verdictApproved, "qa": verdictApproved}, stateAt("bbb"))
	requirePlan(t, h, stateAt("bbb"))
}

// An approval is stale whenever the repos differ from what the axis
// approved, even with no recorded fix round.
func TestReviewRoundHistoryRepoStateStalesApprovals(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	h.recordReview(1, map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictApproved}, stateAt("aaa"))
	requirePlan(t, h, stateAt("aaa"))
	requirePlan(t, h, stateAt("bbb"),
		plannedAxis{"craft", reviewRoundConfirm, 1},
		plannedAxis{"cleanliness", reviewRoundConfirm, 1},
		plannedAxis{"qa", reviewRoundConfirm, 1})
	dirty := stateAt("aaa")
	dirty.Dirt = "edited"
	requirePlan(t, h, dirty,
		plannedAxis{"craft", reviewRoundConfirm, 1},
		plannedAxis{"cleanliness", reviewRoundConfirm, 1},
		plannedAxis{"qa", reviewRoundConfirm, 1})
}

// Without a known delta, an axis with open findings or a stale approval
// reruns in full rather than against a delta it cannot name.
func TestReviewRoundHistoryUnknownDeltaRunsFull(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	h.recordReview(1, map[string]string{"craft": verdictChanges, "cleanliness": verdictApproved, "qa": verdictApproved}, repoState{})
	h.recordFix(1, true)
	requirePlan(t, h, stateAt("bbb"), plannedAxis{"craft", reviewRoundFull, 0})
	h.recordReview(2, map[string]string{"craft": verdictApproved}, stateAt("bbb"))
	requirePlan(t, h, repoState{},
		plannedAxis{"craft", reviewRoundFull, 0},
		plannedAxis{"cleanliness", reviewRoundFull, 0},
		plannedAxis{"qa", reviewRoundFull, 0})
}

func TestReviewRoundHistoryApprovalWithoutLaterFixNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}

	h.recordReview(1, map[string]string{"craft": verdictChanges, "cleanliness": verdictApproved, "qa": verdictApproved}, stateAt("aaa"))
	// The fix only recorded decisions; no code moved.
	h.recordFix(1, false)
	h.recordReview(2, map[string]string{"craft": verdictApproved}, stateAt("aaa"))
	requirePlan(t, h, stateAt("aaa"))
}

func TestReviewRoundHistoryRoundOneApprovalIsFinal(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	h.recordReview(1, map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictApproved}, stateAt("aaa"))
	requirePlan(t, h, stateAt("aaa"))
}

func TestReviewRoundHistoryStallsAtThreeConsecutiveRounds(t *testing.T) {
	t.Parallel()
	axes := finalGateAxesForTest()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}

	h.recordReview(1, map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictChanges}, repoState{})
	h.recordReview(2, map[string]string{"qa": verdictChanges}, repoState{})
	if _, stalled := h.stalledAxis(axes); stalled {
		t.Fatal("stalled after two rounds, want three")
	}
	h.recordReview(3, map[string]string{"qa": verdictChanges}, repoState{})
	axis, stalled := h.stalledAxis(axes)
	if !stalled || axis.Slug != "qa" {
		t.Fatalf("stalledAxis = %q, %v; want qa stalled", axis.Slug, stalled)
	}

	h.resetStreaks()
	if _, stalled := h.stalledAxis(axes); stalled {
		t.Fatal("still stalled after a handoff reset")
	}
}

func TestReviewRoundHistoryApprovalBreaksStreak(t *testing.T) {
	t.Parallel()
	axes := finalGateAxesForTest()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	h.recordReview(1, map[string]string{"craft": verdictChanges}, repoState{})
	h.recordReview(2, map[string]string{"craft": verdictChanges}, repoState{})
	h.recordReview(3, map[string]string{"craft": verdictApproved}, repoState{})
	h.recordReview(4, map[string]string{"craft": verdictChanges}, repoState{})
	if _, stalled := h.stalledAxis(axes); stalled {
		t.Fatal("stalled although an approval broke the streak")
	}
}

func TestLoadReviewRoundHistoryReplaysIterationMetas(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := newArtifactManager(dir)
	metas := []iterationMeta{
		{Iteration: 1, ReviewStatus: frMetaChangesRequested, MadeProgress: true, AxisVerdicts: map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictChanges}, ReviewHeads: map[string]string{"api": "aaa"}},
		{Iteration: 2, ReviewStatus: frMetaChangesRequested, MadeProgress: true, AxisVerdicts: map[string]string{"qa": verdictChanges}, ReviewHeads: map[string]string{"api": "bbb"}},
		{Iteration: 3, ReviewStatus: frMetaChangesRequested, AxisVerdicts: map[string]string{"qa": verdictChanges}, ReviewHeads: map[string]string{"api": "ccc"}, Handoff: reviewHandoffContinued},
	}
	for _, meta := range metas {
		iterDir, err := am.createIterationDir(meta.Iteration)
		if err != nil {
			t.Fatal(err)
		}
		if err := am.writeMeta(iterDir, meta); err != nil {
			t.Fatal(err)
		}
	}

	h := loadReviewRoundHistory(am, dir, 3)
	if _, stalled := h.stalledAxis(finalGateAxesForTest()); stalled {
		t.Fatal("replayed history is stalled although the handoff reset it")
	}
	plan := h.planRound(finalGateAxesForTest(), stateAt("ccc"))
	if len(plan) != 1 || plan[0].Axis.Slug != "qa" || plan[0].Mode != reviewRoundFollowUp || plan[0].Since != 3 {
		t.Fatalf("plan = %+v, want qa follow-up since iteration 3", plannedAxes(plan))
	}

	partial := loadReviewRoundHistory(am, dir, 1)
	plan = partial.planRound(finalGateAxesForTest(), stateAt("bbb"))
	if len(plan) != 1 || plan[0].SinceHeads["api"] != "aaa" {
		t.Fatalf("plan after iteration 1 = %+v, want qa follow-up from api@aaa", plan)
	}
}

func TestLoadReviewRoundHistoryAcceptedHandoffApproves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := newArtifactManager(dir)
	iterDir, err := am.createIterationDir(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := am.writeMeta(iterDir, iterationMeta{Iteration: 1, ReviewStatus: "approved", Handoff: reviewHandoffAccepted, AxisVerdicts: map[string]string{"qa": verdictChanges}}); err != nil {
		t.Fatal(err)
	}
	if plan := loadReviewRoundHistory(am, dir, 1).planRound(finalGateAxesForTest(), stateAt("aaa")); len(plan) != 0 {
		t.Fatalf("plan after an accepted handoff = %+v, want none", plannedAxes(plan))
	}
}

func TestLoadReviewRoundHistoryLegacyMetasRunFullRound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	am := newArtifactManager(dir)
	iterDir, err := am.createIterationDir(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, "meta.yaml"), []byte("iteration: 1\nreview_status: changes_requested\nmade_progress: false\nlegacy_field: ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := loadReviewRoundHistory(am, dir, 1).planRound(finalGateAxesForTest(), stateAt("aaa"))
	if len(plan) != 3 {
		t.Fatalf("plan after legacy meta = %+v, want every axis", plannedAxes(plan))
	}
	for _, run := range plan {
		if run.Mode != reviewRoundFull {
			t.Fatalf("plan after legacy meta = %+v, want full rounds", plannedAxes(plan))
		}
	}
}

func TestClassifyHandoffAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		answer string
		want   reviewHandoffChoice
	}{
		{name: "unanswered", want: handoffUnanswered},
		{name: "accept", answer: "Accept and proceed to Publish", want: handoffAccept},
		{name: "phase accept", answer: "Accept and finish the phase", want: handoffAccept},
		{name: "accept recommended", answer: "Accept and proceed to Publish (Recommended)", want: handoffAccept},
		{name: "shortened accept", answer: "Accept", want: handoffAccept},
		{name: "stop", answer: " Stop. ", want: handoffStop},
		{name: "continue label", answer: "Continue with guidance", want: handoffContinue},
		{name: "shortened continue", answer: "continue", want: handoffContinue},
		{name: "free text guidance", answer: "Drop the CSV export; it is out of scope.", want: handoffContinue},
		{name: "dispute accept is not a handoff accept", answer: "Accept as is", want: handoffContinue},
		{name: "two options is no answer", answer: "Accept and proceed to Publish, Stop", want: handoffUnanswered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyHandoffAnswer(tt.answer); got != tt.want {
				t.Fatalf("classifyHandoffAnswer(%q) = %v, want %v", tt.answer, got, tt.want)
			}
		})
	}
}

func TestReviewDecisionPairsKeepsDisputesAndTheHandoff(t *testing.T) {
	t.Parallel()
	question := handoffQuestion("Final Review", reviewHandoff{Reason: "QA stalled."}, "/handoff.md")
	qa := []ports.QAPair{
		{Question: "Which logger should the export use?", Answer: "slog"},
		{Question: "Dispute: Craft: **High**: the CSV export is out of scope.", Answer: "Accept as is"},
		{Question: "Dispute: auto-picked", Answer: "Fix it", AutoPicked: true},
		{Question: "Final Review needs your decision. QA stalled.\n\nHow should Final Review proceed?", Answer: "Stop"},
	}
	got := reviewDecisionPairs(qa, question)
	if len(got) != 2 || got[0].Answer != "Accept as is" || got[1].Answer != "Stop" {
		t.Fatalf("reviewDecisionPairs() = %+v, want the dispute and the handoff answer", got)
	}
	if answer, ok := handoffAnswer(qa, question); !ok || answer != "Stop" {
		t.Fatalf("handoffAnswer() = %q, %v; want Stop", answer, ok)
	}
	if _, ok := handoffAnswer(qa[:2], question); ok {
		t.Fatal("handoffAnswer() matched a dispute answer")
	}
}

func TestAppendReviewDecisionsKeepsARerunSessionsEarlierAnswers(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), reviewDecisionsFile)
	guidance := ports.QAPair{Question: "How should Final Review proceed?", Answer: "Drop the banner and keep the export."}
	dispute := ports.QAPair{Question: "Dispute: drop the banner?", Answer: "Accept as is"}
	for _, qa := range [][]ports.QAPair{{guidance}, {dispute}, {dispute}} {
		if _, err := appendReviewDecisions(path, "Iteration 2 handoff", qa); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, "## Iteration 2 handoff") != 1 || !strings.Contains(body, guidance.Answer) || strings.Count(body, "**A:** Accept as is") != 1 {
		t.Fatalf("decisions after a rerun session:\n%s", body)
	}
}

func TestAppendReviewDecisionsRecordsEachSession(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), reviewDecisionsFile)

	recorded, err := appendReviewDecisions(path, "Iteration 1 fix", nil)
	if err != nil || recorded {
		t.Fatalf("appendReviewDecisions(no answers) = %v, %v; want false, nil", recorded, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("decisions file created without answers: %v", err)
	}

	if recorded, err := appendReviewDecisions(path, "Iteration 1 fix", []ports.QAPair{{Question: "Craft: drop the retry banner?", Answer: "Accept as is"}}); err != nil || !recorded {
		t.Fatalf("appendReviewDecisions() = %v, %v; want true, nil", recorded, err)
	}
	if _, err := appendReviewDecisions(path, "Iteration 2 handoff", []ports.QAPair{{Question: "How should Final Review proceed?", Answer: "Stop"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, "# Review Decisions") != 1 {
		t.Fatalf("decisions file header repeated:\n%s", body)
	}
	for _, want := range []string{
		"## Iteration 1 fix",
		"**Q:** Craft: drop the retry banner?",
		"**A:** Accept as is",
		"## Iteration 2 handoff",
		"**A:** Stop",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("decisions file missing %q:\n%s", want, body)
		}
	}
}

func TestReposChangedSince(t *testing.T) {
	t.Parallel()
	repo := testutil.InitGitRepo(t)
	repos := map[string]string{"api": repo}
	before := currentRepoState(repos)
	if !before.known() {
		t.Fatal("currentRepoState() unknown for a git repo")
	}
	if reposChangedSince(repos, before) {
		t.Fatal("unchanged clean repo reported as changed")
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !reposChangedSince(repos, before) {
		t.Fatal("dirty worktree reported as unchanged")
	}
	if dirty := currentRepoState(repos); reposChangedSince(repos, dirty) {
		t.Fatal("the same uncommitted work reported as changed")
	}

	cmd := exec.Command("git", "-C", repo, "commit", "-qam", "edit")
	cmd.Env = append(os.Environ(), testutil.GitTestEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if !reposChangedSince(repos, before) {
		t.Fatal("new commit reported as unchanged")
	}

	if !reposChangedSince(map[string]string{"plain": t.TempDir()}, repoState{}) {
		t.Fatal("unknown repo state must count as changed")
	}
}

func TestHandoffQuestionListsBlockingFindings(t *testing.T) {
	t.Parallel()
	feedback := formatStructuredReviewFeedback("Multi-Axis Final Review",
		"### Craft\n- (none)\n\n### QA\n- **High**: `Export` button stays [disabled](https://example.com/x)\n- **Low**: label casing",
		"", reviewChangesRequested)
	got := handoffQuestion("Final Review", reviewHandoff{
		Reason:    "The QA axis requested changes in 3 consecutive rounds.",
		Findings:  feedback,
		FixerNote: "Made no changes: the export was dropped from scope in the **roadmap**.",
	}, "/review/iteration-03/handoff/handoff.md")
	for _, want := range []string{
		"Final Review needs your decision. The QA axis requested changes in 3 consecutive rounds.",
		"- QA: High: Export button stays disabled",
		"Fixer's note: Made no changes: the export was dropped from scope in the roadmap.",
		"Details: /review/iteration-03/handoff/handoff.md",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("handoffQuestion() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "label casing") {
		t.Fatalf("handoffQuestion() listed a non-blocking finding:\n%s", got)
	}
	if strings.ContainsAny(got, "*`[") {
		t.Fatalf("handoffQuestion() kept markdown; the question is shown as plain text:\n%s", got)
	}
}

func TestHandoffAnswerOnlyMatchesTheHandoffQuestion(t *testing.T) {
	t.Parallel()
	question := handoffQuestion("Final Review", reviewHandoff{Reason: "QA stalled."}, "/handoff.md")
	qa := []ports.QAPair{
		{Question: "Should the retry stop after five attempts?", Answer: "Stop"},
		{Question: "Final Review needs your decision. QA stalled.\n\nHow should Final Review proceed?", Answer: "Accept and proceed to Publish"},
		{Question: "Final Review needs your decision. QA stalled.\n\nHow should Final Review proceed?", Answer: "Stop"},
	}
	if answer, ok := handoffAnswer(qa, question); !ok || answer != "Accept and proceed to Publish" {
		t.Fatalf("handoffAnswer() = %q, %v; want the first answer to the handoff question", answer, ok)
	}
	if got := reviewDecisionPairs(qa[:1], question); len(got) != 0 {
		t.Fatalf("reviewDecisionPairs() = %+v, want an unrelated question left out", got)
	}
}

func TestClassifyHandoffAnswerWithTrailingText(t *testing.T) {
	t.Parallel()
	for answer, want := range map[string]reviewHandoffChoice{
		"Stop, I'll fix it myself":                    handoffStop,
		"Accept and finish the phase. Ship it.":       handoffAccept,
		"Fix the tests first, then stop":              handoffContinue,
		"Accept and proceed to Publish, Stop":         handoffUnanswered,
		"Continue with guidance: add the CSV export.": handoffContinue,
	} {
		if got := classifyHandoffAnswer(answer); got != want {
			t.Errorf("classifyHandoffAnswer(%q) = %v, want %v", answer, got, want)
		}
	}
}

// Unknown repo state keeps an approval only while nothing could have changed
// the code since: no fix, no change in what is known, and no gap between loop
// runs. Each stale approval reruns once, in full.
func TestPlanRoundUnknownStateStalesApprovals(t *testing.T) {
	t.Parallel()
	axes := finalGateAxesForTest()
	all := map[string]string{}
	for _, axis := range axes {
		all[axis.Slug] = ReviewApproved.String()
	}
	newHistory := func(loadedAt int) *reviewRoundHistory {
		return &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}, loadedAt: loadedAt}
	}

	lostState := newHistory(0)
	lostState.recordReview(1, all, stateAt("a"))
	if plan := lostState.planRound(axes, repoState{}); len(plan) != len(axes) || plan[0].Mode != reviewRoundFull {
		t.Fatalf("plan once state is unknown = %+v, want every axis in full", plannedAxes(plan))
	}
	lostState.recordReview(2, all, repoState{})
	if plan := lostState.planRound(axes, repoState{}); len(plan) != 0 {
		t.Fatalf("plan after the full round = %+v, want approval", plannedAxes(plan))
	}

	gainedState := newHistory(0)
	gainedState.recordReview(1, all, repoState{})
	if plan := gainedState.planRound(axes, stateAt("a")); len(plan) != len(axes) || plan[0].Mode != reviewRoundFull {
		t.Fatalf("plan once state is known = %+v, want every axis in full", plannedAxes(plan))
	}

	resumed := newHistory(1)
	resumed.recordReview(1, all, repoState{})
	if plan := resumed.planRound(axes, repoState{}); len(plan) != len(axes) {
		t.Fatalf("plan after a resume = %+v, want every axis in full", plannedAxes(plan))
	}
	resumed.recordReview(2, all, repoState{})
	if plan := resumed.planRound(axes, repoState{}); len(plan) != 0 {
		t.Fatalf("plan after the full round = %+v, want approval", plannedAxes(plan))
	}

	fixed := newHistory(0)
	fixed.recordReview(1, all, repoState{})
	fixed.recordReview(2, map[string]string{axes[0].Slug: reviewChangesRequested.String()}, repoState{})
	fixed.recordFix(2, true)
	fixed.recordReview(3, map[string]string{axes[0].Slug: ReviewApproved.String()}, repoState{})
	if plan := fixed.planRound(axes, repoState{}); len(plan) != len(axes)-1 {
		t.Fatalf("plan after a fix = %+v, want every axis but the one just approved", plannedAxes(plan))
	}
}
