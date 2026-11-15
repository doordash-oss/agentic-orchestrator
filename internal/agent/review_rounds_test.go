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

func requirePlan(t *testing.T, h *reviewRoundHistory, want ...plannedAxis) {
	t.Helper()
	got := plannedAxes(h.planRound(finalGateAxesForTest()))
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

	requirePlan(t, h,
		plannedAxis{"craft", reviewRoundFull, 0},
		plannedAxis{"cleanliness", reviewRoundFull, 0},
		plannedAxis{"qa", reviewRoundFull, 0})

	h.recordReview(1, map[string]string{"craft": verdictChanges, "cleanliness": verdictApproved, "qa": verdictApproved}, nil)
	h.recordFix(1, true)
	requirePlan(t, h, plannedAxis{"craft", reviewRoundFollowUp, 1})

	h.recordReview(2, map[string]string{"craft": verdictApproved}, nil)
	requirePlan(t, h,
		plannedAxis{"cleanliness", reviewRoundConfirm, 1},
		plannedAxis{"qa", reviewRoundConfirm, 1})

	h.recordReview(3, map[string]string{"cleanliness": verdictApproved, "qa": verdictApproved}, nil)
	requirePlan(t, h)
}

func TestReviewRoundHistoryApprovalWithoutLaterFixNeedsNoConfirmation(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}

	h.recordReview(1, map[string]string{"craft": verdictChanges, "cleanliness": verdictApproved, "qa": verdictApproved}, nil)
	// The fix only recorded decisions; no code moved.
	h.recordFix(1, false)
	h.recordReview(2, map[string]string{"craft": verdictApproved}, nil)
	requirePlan(t, h)
}

func TestReviewRoundHistoryRoundOneApprovalIsFinal(t *testing.T) {
	t.Parallel()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}
	h.recordReview(1, map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictApproved}, nil)
	requirePlan(t, h)
}

func TestReviewRoundHistoryStallsAtThreeConsecutiveRounds(t *testing.T) {
	t.Parallel()
	axes := finalGateAxesForTest()
	h := &reviewRoundHistory{axes: map[string]*reviewAxisRecord{}}

	h.recordReview(1, map[string]string{"craft": verdictApproved, "cleanliness": verdictApproved, "qa": verdictChanges}, nil)
	h.recordReview(2, map[string]string{"qa": verdictChanges}, nil)
	if _, stalled := h.stalledAxis(axes); stalled {
		t.Fatal("stalled after two rounds, want three")
	}
	h.recordReview(3, map[string]string{"qa": verdictChanges}, nil)
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
	h.recordReview(1, map[string]string{"craft": verdictChanges}, nil)
	h.recordReview(2, map[string]string{"craft": verdictChanges}, nil)
	h.recordReview(3, map[string]string{"craft": verdictApproved}, nil)
	h.recordReview(4, map[string]string{"craft": verdictChanges}, nil)
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
		{Iteration: 3, ReviewStatus: frMetaChangesRequested, AxisVerdicts: map[string]string{"qa": verdictChanges}, Handoff: reviewHandoffContinued},
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
	plan := h.planRound(finalGateAxesForTest())
	if len(plan) != 1 || plan[0].Axis.Slug != "qa" || plan[0].Mode != reviewRoundFollowUp || plan[0].Since != 3 {
		t.Fatalf("plan = %+v, want qa follow-up since iteration 3", plannedAxes(plan))
	}

	partial := loadReviewRoundHistory(am, dir, 1)
	plan = partial.planRound(finalGateAxesForTest())
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
	if plan := loadReviewRoundHistory(am, dir, 1).planRound(finalGateAxesForTest()); len(plan) != 0 {
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
	plan := loadReviewRoundHistory(am, dir, 1).planRound(finalGateAxesForTest())
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
		name string
		qa   []ports.QAPair
		want reviewHandoffChoice
	}{
		{name: "unanswered", want: handoffUnanswered},
		{name: "accept", qa: []ports.QAPair{{Answer: "Accept and proceed to Publish"}}, want: handoffAccept},
		{name: "accept recommended", qa: []ports.QAPair{{Answer: "Accept and proceed to Publish (Recommended)"}}, want: handoffAccept},
		{name: "stop", qa: []ports.QAPair{{Answer: " stop "}}, want: handoffStop},
		{name: "continue label", qa: []ports.QAPair{{Answer: "Continue with guidance"}}, want: handoffContinue},
		{name: "free text guidance", qa: []ports.QAPair{{Answer: "Drop the CSV export; it is out of scope."}}, want: handoffContinue},
		{name: "dispute accept is not a handoff accept", qa: []ports.QAPair{{Answer: "Accept as is"}}, want: handoffContinue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyHandoffAnswer(tt.qa); got != tt.want {
				t.Fatalf("classifyHandoffAnswer() = %v, want %v", got, tt.want)
			}
		})
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
	before := repoHeads(repos)
	if before == nil {
		t.Fatal("repoHeads() = nil for a git repo")
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

	cmd := exec.Command("git", "-C", repo, "commit", "-qam", "edit")
	cmd.Env = append(os.Environ(), testutil.GitTestEnv()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	if !reposChangedSince(repos, before) {
		t.Fatal("new commit reported as unchanged")
	}

	if !reposChangedSince(map[string]string{"plain": t.TempDir()}, nil) {
		t.Fatal("unknown repo state must count as changed")
	}
}

func TestHandoffQuestionListsBlockingFindings(t *testing.T) {
	t.Parallel()
	feedback := formatStructuredReviewFeedback("Multi-Axis Final Review",
		"### Craft\n- (none)\n\n### QA\n- **High**: export button stays disabled\n- **Low**: label casing",
		"", reviewChangesRequested)
	got := handoffQuestion("Final Review", reviewHandoff{
		Reason:    "The QA axis requested changes in 3 consecutive rounds.",
		Findings:  feedback,
		FixerNote: "Made no changes: the export was dropped from scope in the roadmap.",
	}, "/review/iteration-03/handoff/handoff.md")
	for _, want := range []string{
		"Final Review needs your decision. The QA axis requested changes in 3 consecutive rounds.",
		"- QA: **High**: export button stays disabled",
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
}
