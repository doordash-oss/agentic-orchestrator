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
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

const previewRepoName = "agentic-orchestrator"

const (
	stackedLayer1Branch = "feature/stacked-ws/1-core"
	stackedLayer2Branch = "feature/stacked-ws/2-ext"
)

func TestRewindPreviewForFeatureEligibleImplementConsequences(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)
	// A stack layer carrying the repository's pull request: the preview's
	// PR consequence reads the top layer's PR per repository.
	f.Stack = []StackLayer{{
		Position: 1, Branch: stackedLayer1Branch,
		Repos: map[string]StackRepoEntry{
			previewRepoName: {PRURL: "https://github.example/pr/1", PRState: StackPRStateOpen},
		},
	}}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
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
	if len(result.PRConsequences) != 1 {
		t.Fatalf("pr_consequences = %v, want the top layer's PR for the repo", result.PRConsequences)
	}
	if got := result.PRConsequences[0]; got.Repo != previewRepoName || got.PRURL != "https://github.example/pr/1" {
		t.Fatalf("pr_consequences[0] = %+v, want %s at https://github.example/pr/1", got, previewRepoName)
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

// TestRewindRevisionChangesOnStackPRChange pins the per-layer hash: any
// layer's pull request state or URL moving invalidates a stale preview,
// exactly like the legacy per-repo URL once did.
func TestRewindRevisionChangesOnStackPRChange(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)

	rev1 := RewindRevision(f)
	// A layer's pull request state moving (open -> merged) changes the hash.
	entry := f.Stack[1].Repos["alpha"]
	entry.PRState = StackPRStateMerged
	f.Stack[1].Repos["alpha"] = entry
	rev2 := RewindRevision(f)
	if rev2 == rev1 {
		t.Fatal("revision unchanged after a layer's PR state moved; want change")
	}
	// A layer's pull request URL moving changes it again.
	entry.PRURL = "https://github.example/alpha/pull/99"
	f.Stack[1].Repos["alpha"] = entry
	if rev3 := RewindRevision(f); rev3 == rev2 {
		t.Fatal("revision unchanged after a layer's PR URL moved; want change")
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
// Implement rewind: StatusImplementing with a publishable repo and a touched
// repository state. It carries no stack: the unstacked rewind decisions read
// it as the no-stack fixture.
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
		RepoStates:    map[string]*RepoState{repo: {Touched: true}},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return f
}

// newStackedRewindFeature builds and persists a feature carrying a two-layer,
// three-phase pull-request stack: layer 1 owns roadmap phases [1,2] and layer 2
// owns [3]. Layer 1 records per-repo tip SHAs so a rewind to phase 3 (the first
// phase of layer 2) can reset to the layer-1 tips, and every layer's entries
// carry the repository's pull request, as a published stack would.
func newStackedRewindFeature(t *testing.T, store *Store, publishable bool) *Feature {
	t.Helper()
	publishablePtr := publishable
	f := &Feature{
		ID:           "feat-stacked",
		Name:         "Stacked",
		Slug:         "stacked",
		Status:       StatusImplementing,
		CurrentPhase: PhaseImplement,
		ActiveRun:    1,
		RunCount:     1,
		Repos: []FeatureRepo{
			{Name: "alpha", Path: "/repo/alpha", WorktreePath: filepath.Join(store.BaseDir, "wt", "alpha"),
				BaseBranch: "main", Branch: "feature/old-alpha", Publishable: &publishablePtr},
			{Name: "beta", Path: "/repo/beta", WorktreePath: filepath.Join(store.BaseDir, "wt", "beta"),
				BaseBranch: "main", Branch: "feature/old-beta", Publishable: &publishablePtr},
		},
		RepoStates: map[string]*RepoState{
			"alpha": {Touched: true},
			"beta":  {Touched: true},
		},
		SchemaVersion: SchemaVersionCurrent,
	}
	f.CurrentRoadmapPhase = 3
	f.TotalRoadmapPhases = 3
	f.Stack = []StackLayer{
		{
			Position: 1, Slug: "core", Phases: []int{1, 2}, Branch: stackedLayer1Branch,
			Repos: map[string]StackRepoEntry{
				"alpha": {TipSHA: "tip-alpha-1", PRURL: "https://github.example/alpha/pull/1", PRState: StackPRStateOpen},
				"beta":  {TipSHA: "tip-beta-1", PRURL: "https://github.example/beta/pull/1", PRState: StackPRStateOpen},
			},
		},
		{
			Position: 2, Slug: "ext", Phases: []int{3}, Branch: stackedLayer2Branch,
			Repos: map[string]StackRepoEntry{
				"alpha": {TipSHA: "tip-alpha-2", PRURL: "https://github.example/alpha/pull/2", PRState: StackPRStateOpen},
				"beta":  {TipSHA: "tip-beta-2", PRURL: "https://github.example/beta/pull/2", PRState: StackPRStateOpen},
			},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	return f
}

func TestStackedPartialRewindDecisionLayerTipForFirstPhaseOfUpperLayer(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)

	if got := WorktreeResetKind(f, f.Repos[0], true, 3); got != ResetKindLayerTip {
		t.Fatalf("WorktreeResetKind(phase 3) = %q; want %q", got, ResetKindLayerTip)
	}
	if got := rewindTargetBranch(f, true, 3); got != stackedLayer2Branch {
		t.Fatalf("rewindTargetBranch(phase 3) = %q; want %q", got, stackedLayer2Branch)
	}
	plan, err := validatePartialRewindRequestForFeature(f, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 3})
	if err != nil {
		t.Fatalf("validatePartialRewindRequestForFeature(phase 3) error = %v", err)
	}
	if plan.layerTips["alpha"] != "tip-alpha-1" || plan.layerTips["beta"] != "tip-beta-1" {
		t.Fatalf("plan.layerTips = %v; want layer-1 per-repo tips", plan.layerTips)
	}
	if len(plan.resetAnchors) != 0 {
		t.Fatalf("plan.resetAnchors = %v; want empty for the layer-tip case", plan.resetAnchors)
	}
}

func TestStackedPartialRewindDecisionAnchorForLaterPhaseOfLayer(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"alpha": "sha-alpha-1", "beta": "sha-beta-1"},
	}

	if got := WorktreeResetKind(f, f.Repos[0], true, 2); got != ResetKindAnchor {
		t.Fatalf("WorktreeResetKind(phase 2) = %q; want %q", got, ResetKindAnchor)
	}
	if got := rewindTargetBranch(f, true, 2); got != stackedLayer1Branch {
		t.Fatalf("rewindTargetBranch(phase 2) = %q; want %q", got, stackedLayer1Branch)
	}
	plan, err := validatePartialRewindRequestForFeature(f, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2})
	if err != nil {
		t.Fatalf("validatePartialRewindRequestForFeature(phase 2) error = %v", err)
	}
	if plan.resetAnchors["alpha"] != "sha-alpha-1" || plan.resetAnchors["beta"] != "sha-beta-1" {
		t.Fatalf("plan.resetAnchors = %v; want phase-1 anchors", plan.resetAnchors)
	}
	if len(plan.layerTips) != 0 {
		t.Fatalf("plan.layerTips = %v; want empty for the anchor case", plan.layerTips)
	}
}

func TestStackedPartialRewindDecisionPhase1Base(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)

	if got := WorktreeResetKind(f, f.Repos[0], true, 1); got != ResetKindBase {
		t.Fatalf("WorktreeResetKind(phase 1, publishable) = %q; want %q", got, ResetKindBase)
	}
	if got := rewindTargetBranch(f, true, 1); got != stackedLayer1Branch {
		t.Fatalf("rewindTargetBranch(phase 1) = %q; want %q", got, stackedLayer1Branch)
	}
}

func TestStackedPartialRewindDecisionPhase1BaseLocalForNonPublishable(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, false)

	if got := WorktreeResetKind(f, f.Repos[0], true, 1); got != ResetKindBaseLocal {
		t.Fatalf("WorktreeResetKind(phase 1, non-publishable) = %q; want %q", got, ResetKindBaseLocal)
	}
	if got := rewindTargetBranch(f, true, 1); got != stackedLayer1Branch {
		t.Fatalf("rewindTargetBranch(phase 1) = %q; want %q", got, stackedLayer1Branch)
	}
}

func TestStackedFullRewindUsesBaseAndProvisionalBranch(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)

	if got := WorktreeResetKind(f, f.Repos[0], false, 0); got != ResetKindBase {
		t.Fatalf("WorktreeResetKind(full) = %q; want %q", got, ResetKindBase)
	}
	want := git.LayerBranchName(f.WorkspaceSlug(), 1, f.Slug)
	if got := rewindTargetBranch(f, false, 0); got != want {
		t.Fatalf("rewindTargetBranch(full) = %q; want provisional layer-1 name %q", got, want)
	}
}

func TestUnstackedRewindDecisionUnchanged(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newRewindableFeature(t, store, previewRepoName, true)

	if got := WorktreeResetKind(f, f.Repos[0], true, 2); got != ResetKindAnchor {
		t.Fatalf("WorktreeResetKind(phase 2, no stack) = %q; want %q", got, ResetKindAnchor)
	}
	if got := WorktreeResetKind(f, f.Repos[0], true, 1); got != ResetKindBase {
		t.Fatalf("WorktreeResetKind(phase 1, no stack) = %q; want %q", got, ResetKindBase)
	}
	if got := WorktreeResetKind(f, f.Repos[0], false, 0); got != ResetKindBase {
		t.Fatalf("WorktreeResetKind(full, no stack) = %q; want %q", got, ResetKindBase)
	}
	for _, tc := range []struct {
		partial bool
		phase   int
	}{
		{true, 1}, {true, 2}, {false, 0},
	} {
		if got := rewindTargetBranch(f, tc.partial, tc.phase); got != "" {
			t.Fatalf("rewindTargetBranch(no stack, partial=%v, phase=%d) = %q; want empty", tc.partial, tc.phase, got)
		}
	}
}

func TestStackedPartialValidationRejectsMissingLayerTip(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"alpha": "sha-alpha-1", "beta": "sha-beta-1"},
	}
	// Layer 1 loses beta's tip: a rewind to phase 3 (the first phase of
	// layer 2) can no longer resolve a reset point for beta.
	betaEntry := f.Stack[0].Repos["beta"]
	betaEntry.TipSHA = ""
	f.Stack[0].Repos["beta"] = betaEntry
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	_, err := validatePartialRewindRequestForFeature(f, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 3})
	if err == nil {
		t.Fatal("validatePartialRewindRequestForFeature(phase 3) error = nil; want missing-tip error")
	}
	if !strings.Contains(err.Error(), "3") || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("error = %q; want it to name roadmap phase 3 and repo beta", err.Error())
	}

	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)
	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 3}, "")
	if result.Eligible {
		t.Fatal("eligible = true for phase 3 without a complete layer-1 tip set; want false")
	}

	// Phase 3 disappears from the offered roadmap phases; phase 2 still only
	// depends on the phase-1 anchors.
	result = RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false for phase 2; findings %v", result.ValidationFindings)
	}
	if got, want := result.ValidRoadmapPhases, []int{1, 2}; !slices.Equal(got, want) {
		t.Fatalf("valid_roadmap_phases = %v; want %v (phase 3 omitted)", got, want)
	}
}

func TestStackedPartialValidationPhase2DependsOnlyOnAnchors(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	// No layer tips at all, but complete phase-1 anchors: a phase-2 rewind
	// (a later phase of layer 1) must remain eligible.
	f.Stack[0].Repos = nil
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"alpha": "sha-alpha-1", "beta": "sha-beta-1"},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if _, err := validatePartialRewindRequestForFeature(f, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2}); err != nil {
		t.Fatalf("validatePartialRewindRequestForFeature(phase 2) error = %v; want nil (anchors only)", err)
	}
}

func TestStackedPhaseBelongingToNoLayerRejected(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	// The stack covers only phases 1..2; roadmap phase 3 belongs to no layer.
	f.Stack = f.Stack[:1]
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"alpha": "sha-alpha-1", "beta": "sha-beta-1"},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)
	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 3}, "")
	if result.Eligible {
		t.Fatal("eligible = true for a phase belonging to no stack layer; want false")
	}
	if len(result.ValidationFindings) == 0 || !strings.Contains(result.ValidationFindings[0], "roadmap phase 3") {
		t.Fatalf("validation_findings = %v; want a finding naming roadmap phase 3", result.ValidationFindings)
	}

	// The layerless phase is also omitted from the offered roadmap phases.
	result = RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 2}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false for phase 2; findings %v", result.ValidationFindings)
	}
	if got, want := result.ValidRoadmapPhases, []int{1, 2}; !slices.Equal(got, want) {
		t.Fatalf("valid_roadmap_phases = %v; want %v (phase 3 omitted)", got, want)
	}
}

func TestRewindPreviewStackedWorktreeConsequencesCarryKindAndBranch(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement, RoadmapPhase: 3}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false; findings %v", result.ValidationFindings)
	}
	if len(result.WorktreeConsequences) != 2 {
		t.Fatalf("worktree_consequences = %v; want one per worktree repo", result.WorktreeConsequences)
	}
	for _, wc := range result.WorktreeConsequences {
		if wc.ResetKind != ResetKindLayerTip {
			t.Fatalf("worktree consequence for %s reset_kind = %q; want %q", wc.Repo, wc.ResetKind, ResetKindLayerTip)
		}
		if wc.Branch != stackedLayer2Branch {
			t.Fatalf("worktree consequence for %s branch = %q; want %q", wc.Repo, wc.Branch, stackedLayer2Branch)
		}
	}

	full := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement}, "")
	if !full.Eligible {
		t.Fatalf("eligible = false for full rewind; findings %v", full.ValidationFindings)
	}
	wantBranch := git.LayerBranchName(f.WorkspaceSlug(), 1, f.Slug)
	for _, wc := range full.WorktreeConsequences {
		if wc.ResetKind != ResetKindBase {
			t.Fatalf("full-rewind consequence for %s reset_kind = %q; want %q", wc.Repo, wc.ResetKind, ResetKindBase)
		}
		if wc.Branch != wantBranch {
			t.Fatalf("full-rewind consequence for %s branch = %q; want provisional %q", wc.Repo, wc.Branch, wantBranch)
		}
	}
}

// TestRewindPreviewStackedPRConsequencesListTopLayerPerRepo pins the PR
// consequence list on a stacked feature: one entry per repository, in
// f.Repos order, each carrying its top layer's pull request — the close
// loop's per-repo target.
func TestRewindPreviewStackedPRConsequencesListTopLayerPerRepo(t *testing.T) {
	store := NewStore(t.TempDir())
	f := newStackedRewindFeature(t, store, true)
	sealedRunDir := store.RunDir(f.ID, f.ActiveRun)

	result := RewindPreviewForFeature(f, sealedRunDir, RewindRequest{TargetPhase: PhaseImplement}, "")
	if !result.Eligible {
		t.Fatalf("eligible = false; findings %v", result.ValidationFindings)
	}
	want := []RewindPRConsequence{
		{Repo: "alpha", PRURL: "https://github.example/alpha/pull/2"},
		{Repo: "beta", PRURL: "https://github.example/beta/pull/2"},
	}
	if !slices.Equal(result.PRConsequences, want) {
		t.Fatalf("pr_consequences = %v, want %v", result.PRConsequences, want)
	}
}
