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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func phaseGateConfigForTest(t *testing.T) (ImplementConfig, *artifactManager) {
	t.Helper()
	dir := t.TempDir()
	return ImplementConfig{
		Feature:     &feature.Feature{ID: "feat-phase", Pipeline: feature.PipelineMoonshot, CurrentRoadmapPhase: 1},
		ArtifactDir: dir,
	}, newArtifactManager(dir)
}

func writePhaseGateMetas(t *testing.T, am *artifactManager, metas ...iterationMeta) {
	t.Helper()
	for _, meta := range metas {
		iterDir, err := am.createIterationDir(meta.Iteration)
		if err != nil {
			t.Fatal(err)
		}
		if err := am.writeMeta(iterDir, meta); err != nil {
			t.Fatal(err)
		}
	}
}

// A phase that was mid-review before round state was recorded resumes with
// a full round of every axis.
func TestPhaseReviewGateLegacyMetasRunFullRound(t *testing.T) {
	t.Parallel()
	cfg, am := phaseGateConfigForTest(t)
	iterDir, err := am.createIterationDir(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, "meta.yaml"), []byte("iteration: 1\nagent_status: SUCCESS\nreview_status: CHANGES_REQUESTED\nmade_progress: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newPhaseReviewGate(cfg, am, 1)
	plan := g.plan(repoState{})
	if len(plan) != 3 {
		t.Fatalf("plan = %+v, want every per-phase axis", plannedAxes(plan))
	}
	for _, run := range plan {
		if run.Mode != reviewRoundFull {
			t.Fatalf("plan = %+v, want full rounds", plannedAxes(plan))
		}
	}
	if g.awaitingFix() || g.pending != nil {
		t.Fatalf("legacy history awaitingFix=%v pending=%v, want neither", g.awaitingFix(), g.pending)
	}
}

func TestPhaseReviewGateReplaysRoundsAndPendingHandoff(t *testing.T) {
	t.Parallel()
	cfg, am := phaseGateConfigForTest(t)
	changes := verdictChanges
	writePhaseGateMetas(t, am,
		iterationMeta{Iteration: 1, AgentStatus: agentStatusSuccess, ReviewStatus: changes, CodeChanged: true,
			AxisVerdicts: map[string]string{"craft": changes, "functionality-evidence": verdictApproved, "cleanliness": verdictApproved}, ReviewHeads: map[string]string{"api": "aaa"}},
		iterationMeta{Iteration: 2, AgentStatus: agentStatusSuccess, ReviewStatus: changes, CodeChanged: true,
			AxisVerdicts: map[string]string{"craft": changes}, ReviewHeads: map[string]string{"api": "bbb"}},
		iterationMeta{Iteration: 3, AgentStatus: agentStatusSuccess, ReviewStatus: changes, CodeChanged: true, Handoff: reviewHandoffAsked,
			AxisVerdicts: map[string]string{"craft": changes}, ReviewHeads: map[string]string{"api": "ccc"}},
	)
	g := newPhaseReviewGate(cfg, am, 2)
	if err := g.raiseHandoff(3, filepath.Join(cfg.ArtifactDir, "iteration-03"), reviewHandoff{Reason: "The Craft axis requested changes in 3 consecutive rounds."}); err != nil {
		t.Fatal(err)
	}

	resumed := newPhaseReviewGate(cfg, am, 3)
	if resumed.pending == nil || resumed.pending.Reason != "The Craft axis requested changes in 3 consecutive rounds." || resumed.pending.AcceptLabel != phaseHandoffAcceptLabel {
		t.Fatalf("pending handoff = %+v, want the iteration 3 handoff", resumed.pending)
	}
	if axis, stalled := resumed.history.stalledAxis(resumed.axes); !stalled || axis.Slug != "craft" {
		t.Fatalf("stalled = %v %q, want craft", stalled, axis.Slug)
	}
	plan := resumed.plan(stateAt("ccc"))
	if len(plan) != 1 || plan[0].Axis.Slug != "craft" || plan[0].Mode != reviewRoundFollowUp || plan[0].Since != 3 || plan[0].SinceHeads["api"] != "ccc" {
		t.Fatalf("plan = %+v, want craft follow-up since iteration 3", plannedAxes(plan))
	}

	writePhaseGateMetas(t, am, iterationMeta{Iteration: 4, AgentStatus: agentStatusSuccess, ReviewStatus: verdictApproved, CodeChanged: true, Handoff: reviewHandoffContinued,
		AxisVerdicts: map[string]string{"craft": verdictApproved}, ReviewHeads: map[string]string{"api": "ddd"}})
	answered := newPhaseReviewGate(cfg, am, 4)
	if answered.pending != nil {
		t.Fatalf("pending handoff after an answer = %+v, want none", answered.pending)
	}
	if got := plannedAxes(answered.plan(stateAt("ddd"))); len(got) != 2 || got[0].mode != reviewRoundConfirm || got[1].mode != reviewRoundConfirm {
		t.Fatalf("plan = %+v, want the two earlier approvals to confirm", got)
	}
}

// Reviewers are pointed at the implementer's progress notes only when the
// file exists.
func TestPhaseReviewGateProgressNotesOnlyWhenPresent(t *testing.T) {
	t.Parallel()
	cfg, am := phaseGateConfigForTest(t)
	g := newPhaseReviewGate(cfg, am, 0)
	if got := g.reviewInput(1, nil).ProgressPath; got != "" {
		t.Fatalf("ProgressPath = %q, want empty without progress notes", got)
	}
	progress := filepath.Join(cfg.ArtifactDir, "progress.md")
	if err := os.WriteFile(progress, []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := g.reviewInput(1, nil).ProgressPath; got != progress {
		t.Fatalf("ProgressPath = %q, want %q", got, progress)
	}
}
