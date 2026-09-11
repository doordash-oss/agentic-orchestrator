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

package feature_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
	"gopkg.in/yaml.v3"
)

// restackJournalFixtureEntries is the two-entry journal the round-trip and
// rewind tests persist: repo-a prepared mid-landing, repo-b applied and
// waiting on its worktree sync.
func restackJournalFixtureEntries() []feature.RestackJournalEntry {
	return []feature.RestackJournalEntry{
		{
			Repository: "repo-a",
			Updates: []feature.RestackRefUpdate{
				{Ref: "refs/heads/feature/restack/1-bootstrap", OldSHA: "1111111111111111111111111111111111111111", NewSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{Ref: "refs/heads/feature/restack/2-build", OldSHA: "2222222222222222222222222222222222222222", NewSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
			Remap: feature.RestackRemap{
				Anchors: map[int]string{1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				Tips:    map[int]string{1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			},
			State:          feature.RestackJournalPrepared,
			RequestedLayer: 1,
			NewTopSHA:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		{
			Repository: "repo-b",
			Updates: []feature.RestackRefUpdate{
				{Ref: "refs/heads/feature/restack/1-bootstrap", OldSHA: "3333333333333333333333333333333333333333", NewSHA: "cccccccccccccccccccccccccccccccccccccccc"},
			},
			State:       feature.RestackJournalApplied,
			PendingSync: true,
			NewTopSHA:   "cccccccccccccccccccccccccccccccccccccccc",
		},
	}
}

func TestRestackJournalYAMLRoundTrip(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "restack-journal-round-trip",
		Name:          "Restack Journal Round Trip",
		Status:        feature.StatusFinalReviewing,
		CurrentPhase:  feature.PhaseFinalReview,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	journal := restackJournalFixtureEntries()
	f.Run().RestackJournal = journal
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.RunDir(f.ID, 1), "run.yaml"))
	if err != nil {
		t.Fatalf("read run.yaml: %v", err)
	}
	if !strings.Contains(string(data), "restack_journal:") {
		t.Fatalf("run.yaml missing restack_journal: %s", string(data))
	}
	if !strings.Contains(string(data), "state: prepared") || !strings.Contains(string(data), "pending_sync: true") {
		t.Fatalf("run.yaml missing journal entry state: %s", string(data))
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got.Run().RestackJournal, journal) {
		t.Fatalf("RestackJournal = %+v, want %+v", got.Run().RestackJournal, journal)
	}
}

func TestRestackJournalOmittedWhenAbsent(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:            "restack-journal-absent",
		Name:          "Restack Journal Absent",
		Status:        feature.StatusImplementing,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.RunDir(f.ID, 1), "run.yaml"))
	if err != nil {
		t.Fatalf("read run.yaml: %v", err)
	}
	if strings.Contains(string(data), "restack_journal") {
		t.Fatalf("run.yaml without entries must omit restack_journal: %s", string(data))
	}

	got, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want nil", got.Run().RestackJournal)
	}
}

func TestRunYAMLWithoutRestackJournalLoads(t *testing.T) {
	var r feature.Run
	data := []byte("run_number: 1\ncurrent_roadmap_phase: 2\n")
	if err := yaml.Unmarshal(data, &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.RestackJournal != nil {
		t.Errorf("RestackJournal = %#v, want nil", r.RestackJournal)
	}
}

// newRestackJournalFeature seeds a two-repository feature with a two-layer
// stack (per-repository entries carrying push and pull-request fields the
// apply must preserve) and phase-1 commit anchors.
func newRestackJournalFeature(t *testing.T, mgr *feature.Manager) *feature.Feature {
	t.Helper()
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", Branch: "feature/restack/2-build", BaseBranch: "main"},
		{Name: "repo-b", Path: "/tmp/repo-b", WorktreePath: "/tmp/wt-b", Branch: "feature/restack/2-build", BaseBranch: "main"},
	})
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusFinalReviewing
		ff.CurrentPhase = feature.PhaseFinalReview
		ff.CurrentRoadmapPhase = 2
		ff.TotalRoadmapPhases = 2
		ff.Stack = []feature.StackLayer{
			{
				Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1}, Branch: "feature/restack/1-bootstrap",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: "1111111111111111111111111111111111111111", LastPushedSHA: "1111111111111111111111111111111111111111", PRURL: "https://github.com/org/repo-a/pull/1", PRState: feature.StackPRStateOpen},
					"repo-b": {TipSHA: "3333333333333333333333333333333333333333"},
				},
			},
			{
				Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{2}, Branch: "feature/restack/2-build",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: "2222222222222222222222222222222222222222", PRURL: "https://github.com/org/repo-a/pull/2"},
				},
			},
		}
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {
				"repo-a": "1111111111111111111111111111111111111111",
				"repo-b": "3333333333333333333333333333333333333333",
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	return f
}

func restackJournalLayer(stack []feature.StackLayer, position int) feature.StackLayer {
	for _, layer := range stack {
		if layer.Position == position {
			return layer
		}
	}
	return feature.StackLayer{}
}

func TestApplyRestackJournalEntryRewritesAnchorsAndTips(t *testing.T) {
	mgr := newTestManager(t)
	f := newRestackJournalFeature(t, mgr)
	entry := feature.RestackJournalEntry{
		Repository: "repo-a",
		Remap: feature.RestackRemap{
			Anchors: map[int]string{1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			Tips:    map[int]string{1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 2: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		State: feature.RestackJournalPrepared,
	}
	if err := mgr.SetRestackJournalEntry(f.ID, "repo-a", entry); err != nil {
		t.Fatalf("SetRestackJournalEntry: %v", err)
	}

	if err := mgr.ApplyRestackJournalEntry(f.ID, "repo-a"); err != nil {
		t.Fatalf("ApplyRestackJournalEntry: %v", err)
	}

	applied, err := mgr.Store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if applied.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry removed in the same write", applied.Run().RestackJournal)
	}
	anchors := applied.Run().RoadmapPhaseCommitAnchors
	if got := anchors[1]["repo-a"]; got != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("phase 1 repo-a anchor = %q, want the remapped SHA", got)
	}
	if got := anchors[1]["repo-b"]; got != "3333333333333333333333333333333333333333" {
		t.Errorf("phase 1 repo-b anchor = %q, want untouched", got)
	}
	layerOne := restackJournalLayer(applied.Stack, 1)
	if got := layerOne.Repos["repo-a"]; got.TipSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		got.LastPushedSHA != "1111111111111111111111111111111111111111" ||
		got.PRURL != "https://github.com/org/repo-a/pull/1" ||
		got.PRState != feature.StackPRStateOpen {
		t.Errorf("layer 1 repo-a = %+v, want the remapped tip with push and pull-request fields preserved", got)
	}
	if got := layerOne.Repos["repo-b"]; got.TipSHA != "3333333333333333333333333333333333333333" {
		t.Errorf("layer 1 repo-b = %+v, want untouched", got)
	}
	layerTwo := restackJournalLayer(applied.Stack, 2)
	if got := layerTwo.Repos["repo-a"]; got.TipSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || got.PRURL != "https://github.com/org/repo-a/pull/2" {
		t.Errorf("layer 2 repo-a = %+v, want the remapped tip with the pull request URL preserved", got)
	}
}

func TestRestackJournalEntryOps(t *testing.T) {
	mgr := newTestManager(t)
	f := newRestackJournalFeature(t, mgr)

	first := feature.RestackJournalEntry{Repository: "repo-a", State: feature.RestackJournalPrepared}
	if err := mgr.SetRestackJournalEntry(f.ID, "repo-a", first); err != nil {
		t.Fatalf("SetRestackJournalEntry: %v", err)
	}
	second := feature.RestackJournalEntry{Repository: "repo-a", State: feature.RestackJournalPrepared, NewTopSHA: "cccccccccccccccccccccccccccccccccccccccc"}
	if err := mgr.SetRestackJournalEntry(f.ID, "repo-a", second); err != nil {
		t.Fatalf("SetRestackJournalEntry (replace): %v", err)
	}
	loaded, err := mgr.Store.Load(f.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if journal := loaded.Run().RestackJournal; len(journal) != 1 || !reflect.DeepEqual(journal[0], second) {
		t.Fatalf("journal = %+v, want exactly the replaced entry %+v", journal, second)
	}

	if err := mgr.SetRestackJournalPendingSync(f.ID, "repo-a", true); err != nil {
		t.Fatalf("SetRestackJournalPendingSync(true): %v", err)
	}
	if loaded, err = mgr.Store.Load(f.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Run().RestackJournal[0].PendingSync {
		t.Fatalf("PendingSync = false, want true")
	}
	if err := mgr.SetRestackJournalPendingSync(f.ID, "repo-a", false); err != nil {
		t.Fatalf("SetRestackJournalPendingSync(false): %v", err)
	}
	if loaded, err = mgr.Store.Load(f.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Run().RestackJournal[0].PendingSync {
		t.Fatalf("PendingSync = true, want false")
	}

	if err := mgr.SetRestackJournalPendingSync(f.ID, "repo-b", true); err == nil {
		t.Errorf("SetRestackJournalPendingSync for a missing entry = nil error, want failure")
	}
	if err := mgr.ApplyRestackJournalEntry(f.ID, "repo-b"); err == nil {
		t.Errorf("ApplyRestackJournalEntry for a missing entry = nil error, want failure")
	}

	if err := mgr.DropRestackJournalEntry(f.ID, "repo-a"); err != nil {
		t.Fatalf("DropRestackJournalEntry: %v", err)
	}
	if loaded, err = mgr.Store.Load(f.ID); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry dropped", loaded.Run().RestackJournal)
	}
	if err := mgr.DropRestackJournalEntry(f.ID, "repo-a"); err != nil {
		t.Errorf("DropRestackJournalEntry for an absent entry = %v, want no-op success", err)
	}

	if err := mgr.SetRestackJournalEntry("no-such-feature", "repo-a", first); err == nil {
		t.Errorf("SetRestackJournalEntry for a missing feature = nil error, want failure")
	}
	if err := mgr.DropRestackJournalEntry("no-such-feature", "repo-a"); err == nil {
		t.Errorf("DropRestackJournalEntry for a missing feature = nil error, want failure")
	}
}

func TestRestackRemapForRepository(t *testing.T) {
	anchors := map[int]map[string]string{
		1: {"repo-a": "1111111111111111111111111111111111111111", "repo-b": "3333333333333333333333333333333333333333"},
		2: {"repo-a": "2222222222222222222222222222222222222222"},
		3: {"repo-a": "4444444444444444444444444444444444444444"},
	}
	stack := []feature.StackLayer{
		{Position: 1, Repos: map[string]feature.StackRepoEntry{
			"repo-a": {TipSHA: "1111111111111111111111111111111111111111"},
			"repo-b": {TipSHA: "5555555555555555555555555555555555555555"},
		}},
		{Position: 2, Repos: map[string]feature.StackRepoEntry{
			"repo-a": {TipSHA: "2222222222222222222222222222222222222222"},
		}},
	}
	commitMap := map[string]string{
		"1111111111111111111111111111111111111111": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"2222222222222222222222222222222222222222": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}

	remap := feature.RestackRemapForRepository(anchors, stack, "repo-a", commitMap)
	wantAnchors := map[int]string{
		1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		2: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if !reflect.DeepEqual(remap.Anchors, wantAnchors) {
		t.Errorf("Anchors = %v, want %v (phase 3 absent from the map stays omitted)", remap.Anchors, wantAnchors)
	}
	wantTips := map[int]string{
		1: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		2: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if !reflect.DeepEqual(remap.Tips, wantTips) {
		t.Errorf("Tips = %v, want %v", remap.Tips, wantTips)
	}

	remapB := feature.RestackRemapForRepository(anchors, stack, "repo-b", commitMap)
	if remapB.Anchors != nil || remapB.Tips != nil {
		t.Errorf("repo-b remap = %+v, want empty (no SHA of repo-b appears in the commit map)", remapB)
	}

	empty := feature.RestackRemapForRepository(anchors, stack, "repo-a", nil)
	if empty.Anchors != nil || empty.Tips != nil {
		t.Errorf("empty commit map remap = %+v, want empty", empty)
	}
}

// A partial rewind leaves the forked run without journal entries — the
// rewind resets the worktree itself — while the sealed run keeps its own
// copy.
func TestRewindWithRequest_PartialDropsRestackJournal(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	run1Dir := filepath.Join(mgr.Store.BaseDir, f.ID, "runs", "run-001")
	journal := restackJournalFixtureEntries()[:1]
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Stack = []feature.StackLayer{
			{
				Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1}, Branch: "feature/restack/1-bootstrap",
				Repos: map[string]feature.StackRepoEntry{
					"repo-a": {TipSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				},
			},
			{Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{2, 3}, Branch: "feature/restack/2-build"},
		}
		ff.Artifacts = map[string]string{
			"roadmap":      filepath.Join(run1Dir, "roadmap", "roadmap.md"),
			"phase-1-plan": filepath.Join(run1Dir, "phase-01", "plan", "phase-plan.md"),
			"phase-2-plan": filepath.Join(run1Dir, "phase-02", "plan", "phase-plan.md"),
			"phase-3-plan": filepath.Join(run1Dir, "phase-03", "plan", "phase-plan.md"),
		}
		ff.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
			1: {"repo-a": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			2: {"repo-a": "cccccccccccccccccccccccccccccccccccccccc"},
		}
		ff.Run().RestackJournal = journal
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	files := map[string]string{
		filepath.Join("roadmap", "roadmap.md"):             "roadmap",
		filepath.Join("phase-01", "plan", "phase-plan.md"): "phase 1 plan",
		filepath.Join("phase-02", "plan", "phase-plan.md"): "phase 2 plan",
		filepath.Join("phase-03", "plan", "phase-plan.md"): "phase 3 plan",
	}
	for rel, content := range files {
		full := filepath.Join(run1Dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mgr.Worktrees = mocks.NewMockWorktreeOps()
	mgr.PRs = nil

	if _, _, err := mgr.RewindWithRequest(f.ID, feature.RewindRequest{
		TargetPhase:  feature.PhaseImplement,
		RoadmapPhase: 2,
	}); err != nil {
		t.Fatalf("RewindWithRequest: %v", err)
	}

	newRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if newRun.RestackJournal != nil {
		t.Errorf("new run RestackJournal = %+v, want no entries after a partial rewind", newRun.RestackJournal)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if !reflect.DeepEqual(sealedRun.RestackJournal, journal) {
		t.Errorf("sealed run RestackJournal = %+v, want its own copy of %+v", sealedRun.RestackJournal, journal)
	}
}

// A full rewind to the roadmap phase or earlier leaves the forked run
// without journal entries too; the sealed run keeps its own copy.
func TestRewindToPhase_FullRewindDropsRestackJournal(t *testing.T) {
	mgr := newTestManager(t)
	f := newMultiRepoFeature(t, mgr, []feature.FeatureRepo{
		{Name: "repo-a", Path: "/tmp/repo-a", WorktreePath: "/tmp/wt-a", BaseBranch: "main"},
	})
	journal := restackJournalFixtureEntries()
	if err := mgr.Store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Status = feature.StatusImplementing
		ff.CurrentPhase = feature.PhaseImplement
		ff.CurrentRoadmapPhase = 3
		ff.TotalRoadmapPhases = 3
		ff.Stack = []feature.StackLayer{
			{Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1}, Branch: "feature/restack/1-bootstrap"},
			{Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{2, 3}, Branch: "feature/restack/2-build"},
		}
		ff.Run().RestackJournal = journal
		return nil
	}); err != nil {
		t.Fatalf("modify: %v", err)
	}
	mgr.Worktrees = mocks.NewMockWorktreeOps()
	mgr.PRs = nil

	if _, _, err := mgr.RewindToPhase(f.ID, feature.PhasePlan); err != nil {
		t.Fatalf("RewindToPhase: %v", err)
	}

	newRun, err := mgr.Store.LoadRun(f.ID, 2)
	if err != nil {
		t.Fatalf("LoadRun(2): %v", err)
	}
	if newRun.RestackJournal != nil {
		t.Errorf("new run RestackJournal = %+v, want no entries after a full rewind", newRun.RestackJournal)
	}
	sealedRun, err := mgr.Store.LoadRun(f.ID, 1)
	if err != nil {
		t.Fatalf("LoadRun(1): %v", err)
	}
	if !reflect.DeepEqual(sealedRun.RestackJournal, journal) {
		t.Errorf("sealed run RestackJournal = %+v, want its own copy of %+v", sealedRun.RestackJournal, journal)
	}
}
