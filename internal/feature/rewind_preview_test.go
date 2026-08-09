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

package feature

import (
	"path/filepath"
	"slices"
	"testing"
)

const previewRepoName = "agentic-orchestrator"

func TestRewindPreviewForFeatureEligibleImplementConsequences(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false; findings %v", result.ValidationFindings)
	}
	if result.SourceRunNumber != f.ActiveRun {
		t.Fatalf("source_run_number = %d; want %d", result.SourceRunNumber, f.ActiveRun)
	}
	if result.SourceRevision == "" {
		t.Fatal("source_revision is empty")
	}
	if result.EffectivePhase != PhaseImplement {
		t.Fatalf("effective_phase = %v; want implement", result.EffectivePhase)
	}
	if len(result.ValidPhases) == 0 {
		t.Fatal("valid_phases empty for a rewindable feature")
	}
	// Carry-forward set for Implement includes the static matrix entries.
	if !slices.Contains(result.CarriedPhases, "plan") {
		t.Fatalf("carried_phases = %v; want to include plan", result.CarriedPhases)
	}
	// Publishable feature: a PR consequence and a worktree reset are present.
	if len(result.PRConsequences) == 0 {
		t.Fatalf("pr_consequences empty for publishable feature")
	}
	if len(result.WorktreeConsequences) == 0 {
		t.Fatalf("worktree_consequences empty")
	}
	if !slices.Contains(result.BackupBranchRepos, previewRepoName) {
		t.Fatalf("backup_branch_repos = %v; want %s", result.BackupBranchRepos, previewRepoName)
	}
	if len(result.ValidationFindings) != 0 {
		t.Fatalf("validation_findings = %v; want empty", result.ValidationFindings)
	}
}

func TestRewindPreviewRejectsInvalidTarget(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseKnowledgeBase}, "")
	if result.Eligible {
		t.Fatalf("eligible = true for KB target; want false")
	}
	if len(result.ValidationFindings) == 0 {
		t.Fatal("validation_findings empty for invalid target")
	}
}

func TestRewindPreviewPartialRoadmapValidPhases(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	f.TotalRoadmapPhases = 3
	f.CurrentRoadmapPhase = 3
	// Commit anchors for phases 1 and 2 (so roadmap phases 2 and 3 are valid).
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {previewRepoName: "sha-1"},
		2: {previewRepoName: "sha-2"},
	}
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false; findings %v", result.ValidationFindings)
	}
	want := []int{1, 2, 3}
	if len(result.ValidRoadmapPhases) != len(want) {
		t.Fatalf("valid_roadmap_phases = %v; want %v", result.ValidRoadmapPhases, want)
	}
	for i, p := range want {
		if result.ValidRoadmapPhases[i] != p {
			t.Fatalf("valid_roadmap_phases = %v; want %v", result.ValidRoadmapPhases, want)
		}
	}
}

func TestRewindPreviewPartialRoadmapRejectsMissingAnchor(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	f.TotalRoadmapPhases = 3
	f.CurrentRoadmapPhase = 3
	// No anchors for phase 1 -> roadmap phase 2 invalid.
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2}, "")
	if result.Eligible {
		t.Fatalf("eligible = true for partial rewind without anchor; want false")
	}
}

func TestRewindRevisionStableThenChangesOnStateAdvance(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)

	rev1 := RewindRevision(f)
	if rev1 == "" {
		t.Fatal("revision empty")
	}
	// Same state -> same revision.
	if rev2 := RewindRevision(f); rev2 != rev1 {
		t.Fatalf("revision not stable for unchanged state: %s != %s", rev2, rev1)
	}
	// Active run advances -> revision changes.
	f.ActiveRun = f.ActiveRun + 1
	if rev3 := RewindRevision(f); rev3 == rev1 {
		t.Fatal("revision unchanged after active run advanced; want change")
	}
}

func TestRewindPreviewSourceRevisionMatchesRewindRevision(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement}, "")
	if result.SourceRevision != RewindRevision(f) {
		t.Fatalf("preview source_revision %s != RewindRevision %s", result.SourceRevision, RewindRevision(f))
	}
}

func TestRewindPreviewUpgradePipelineComputesChoicesForUpgradedProfile(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	f.Pipeline = PipelineMedium
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseInquire}, PipelineLarge)
	if !result.Eligible {
		t.Fatalf("eligible = false; findings %v", result.ValidationFindings)
	}
	if result.UpgradePipeline != PipelineLarge {
		t.Fatalf("upgrade_pipeline = %v; want large", result.UpgradePipeline)
	}
	// Large pipeline offers Moonshot as a further upgrade option.
	found := false
	for _, opt := range result.UpgradePipelineOptions {
		if opt == PipelineMoonshot {
			found = true
		}
	}
	if !found {
		t.Fatalf("upgrade_pipeline_options = %v; want to include moonshot", result.UpgradePipelineOptions)
	}
}

// newRewindableFeature builds and persists a feature that is eligible for an
// Implement rewind: StatusImplementing with a publishable repo and a PR URL.
func newRewindableFeature(t *testing.T, store *Store, repo string, publishable bool) *Feature {
	t.Helper()
	publishablePtr := publishable
	f := &Feature{
		ID:           "feat-preview",
		Name:         "Preview",
		Slug:         "preview",
		Status:       StatusImplementing,
		CurrentPhase: PhaseImplement,
		ActiveRun:    1,
		RunCount:     1,
		Repos: []FeatureRepo{{
			Name: repo, Path: "/repo/" + repo, WorktreePath: filepath.Join(store.BaseDir, "wt", repo),
			BaseBranch: "main", Branch: "feature/x", Publishable: &publishablePtr,
		}},
		RepoStates:    map[string]*RepoState{repo: {Touched: true, PRURL: "https://github.example/pr/1"}},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return f
}
