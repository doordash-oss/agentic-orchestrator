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

package orchestrator_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// Shared SHAs, branches, and refs for the restack reconciliation fixtures:
// a two-layer stack whose layer refs move from old to new SHAs when the
// journaled transaction lands.
const (
	restackOldLayerOne    = "1111111111111111111111111111111111111111"
	restackNewLayerOne    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	restackOldLayerTwo    = "2222222222222222222222222222222222222222"
	restackNewLayerTwo    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	restackOldAnchor      = "3333333333333333333333333333333333333333"
	restackNewAnchor      = "cccccccccccccccccccccccccccccccccccccccc"
	restackLayerOneBranch = "feature/restack/1-bootstrap"
	restackLayerTwoBranch = "feature/restack/2-build"
	restackLayerOneRef    = "refs/heads/" + restackLayerOneBranch
	restackLayerTwoRef    = "refs/heads/" + restackLayerTwoBranch
	restackWorktree       = "/wt/repo-a"
)

// restackResetCall records one hard-reset the mock worktree adapter served.
type restackResetCall struct {
	Path string
	SHA  string
}

// restackFixture wires a real store + manager (so the restack lifecycle
// operations run for real), a mock worktree adapter whose refs the test
// scripts and whose resets are recorded or failed on demand, and an
// orchestrator holding both.
type restackFixture struct {
	store      *feature.Store
	mgr        *feature.Manager
	f          *feature.Feature
	worktrees  *mocks.MockWorktreeOps
	o          *orchestrator.Orchestrator
	resetCalls []restackResetCall
	resetErr   error
}

// restackEntry builds the standard repo-a journal entry: both layer refs
// move old→new, the remap rewrites the phase-1 anchor and both layer tips,
// and the fix round asked to relocate into layer 1.
func restackEntry(state feature.RestackJournalState, pending bool) feature.RestackJournalEntry {
	return feature.RestackJournalEntry{
		Repository: "repo-a",
		Updates: []feature.RestackRefUpdate{
			{Ref: restackLayerOneRef, OldSHA: restackOldLayerOne, NewSHA: restackNewLayerOne},
			{Ref: restackLayerTwoRef, OldSHA: restackOldLayerTwo, NewSHA: restackNewLayerTwo},
		},
		Remap: feature.RestackRemap{
			Anchors: map[int]string{1: restackNewAnchor},
			Tips:    map[int]string{1: restackNewLayerOne, 2: restackNewLayerTwo},
		},
		State:          state,
		PendingSync:    pending,
		RequestedLayer: 1,
		NewTopSHA:      restackNewLayerTwo,
	}
}

func restackAllNewRefs() map[string]string {
	return map[string]string{
		restackLayerOneRef: restackNewLayerOne,
		restackLayerTwoRef: restackNewLayerTwo,
	}
}

func restackAllOldRefs() map[string]string {
	return map[string]string{
		restackLayerOneRef: restackOldLayerOne,
		restackLayerTwoRef: restackOldLayerTwo,
	}
}

func newRestackFixture(t *testing.T, entry feature.RestackJournalEntry, refs map[string]string) *restackFixture {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	mgr := feature.NewManager(store, &config.Config{})
	f := &feature.Feature{
		ID:            "feat-restack",
		Name:          "Restack Recovery",
		Slug:          "restack-recovery",
		Status:        feature.StatusFinalReviewing,
		CurrentPhase:  feature.PhaseFinalReview,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repo-a", Path: "/src/repo-a", WorktreePath: restackWorktree, Branch: restackLayerTwoBranch, BaseBranch: "main"},
		},
	}
	f.Stack = []feature.StackLayer{
		{
			Position: 1, Title: "Bootstrap", Slug: "bootstrap", Phases: []int{1}, Branch: restackLayerOneBranch,
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: restackOldLayerOne}},
		},
		{
			Position: 2, Title: "Build and polish", Slug: "build-and-polish", Phases: []int{2}, Branch: restackLayerTwoBranch,
			Repos: map[string]feature.StackRepoEntry{"repo-a": {TipSHA: restackOldLayerTwo}},
		},
	}
	f.CurrentRoadmapPhase = 2
	f.TotalRoadmapPhases = 2
	f.Run().RoadmapPhaseCommitAnchors = map[int]map[string]string{
		1: {"repo-a": restackOldAnchor},
	}
	f.Run().RestackJournal = []feature.RestackJournalEntry{entry}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	fx := &restackFixture{store: store, mgr: mgr, f: f}
	wt := mocks.NewMockWorktreeOps()
	wt.RefSHAFn = func(path, ref string) (string, error) { return refs[ref], nil }
	wt.ResetToCommitFn = func(path, sha string) error {
		fx.resetCalls = append(fx.resetCalls, restackResetCall{Path: path, SHA: sha})
		return fx.resetErr
	}
	fx.worktrees = wt
	fx.o = orchestrator.New(orchestrator.Deps{
		Store:     store,
		Lifecycle: mgr,
		Worktrees: wt,
	}, orchestrator.Hooks{})
	return fx
}

// loadRestackFeature reloads the fixture's feature from the store.
func (fx *restackFixture) loadRestackFeature(t *testing.T) *feature.Feature {
	t.Helper()
	loaded, err := fx.store.Load(fx.f.ID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	return loaded
}

// restackRelocationWarning returns the first fix-relocated-above-layer
// warning event, or nil when none was emitted.
func restackRelocationWarning(events []ports.Event) *ports.Event {
	for i, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.CanonicalError != nil &&
			ev.CanonicalError.Code == errcat.FixRelocatedAboveLayer {
			return &events[i]
		}
	}
	return nil
}

// restackLandedEvent returns the first repository-status event reporting a
// finished restack landing, or nil when none was emitted.
func restackLandedEvent(events []ports.Event) *ports.Event {
	for i, ev := range events {
		if ev.Type == ports.RepoStatusChanged && ev.CanonicalError == nil &&
			strings.Contains(ev.Message, "restack landing") {
			return &events[i]
		}
	}
	return nil
}

func restackRecoveryTip(stack []feature.StackLayer, position int, repo string) string {
	for _, layer := range stack {
		if layer.Position == position {
			return layer.Repos[repo].TipSHA
		}
	}
	return ""
}

// A prepared entry whose refs all sit at their new SHAs is a landed
// transaction: the remap is persisted (anchors and tips rewritten, entry
// removed) and the worktree is hard-reset to the rewritten chain's top.
func TestReconcileRestackJournal_LandedAppliesAndResets(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalPrepared, false), restackAllNewRefs())

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if loaded.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry applied and removed", loaded.Run().RestackJournal)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackNewAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want %q", got, restackNewAnchor)
	}
	if got := restackRecoveryTip(loaded.Stack, 1, "repo-a"); got != restackNewLayerOne {
		t.Errorf("layer 1 repo-a tip = %q, want %q", got, restackNewLayerOne)
	}
	if got := restackRecoveryTip(loaded.Stack, 2, "repo-a"); got != restackNewLayerTwo {
		t.Errorf("layer 2 repo-a tip = %q, want %q", got, restackNewLayerTwo)
	}
	if len(fx.resetCalls) != 1 || fx.resetCalls[0].SHA != restackNewLayerTwo || fx.resetCalls[0].Path != restackWorktree {
		t.Fatalf("reset calls = %+v, want one hard-reset of %q to %q", fx.resetCalls, restackWorktree, restackNewLayerTwo)
	}
	landed := restackLandedEvent(drainEvents(fx.o))
	if landed == nil {
		t.Fatal("no repository-status event reporting the finished restack landing")
	}
	if landed.FeatureID != fx.f.ID || landed.RepoName != "repo-a" || landed.Branch != restackLayerTwoBranch {
		t.Errorf("landed event = %+v, want the feature, repository, and top-layer branch", landed)
	}
}

// A prepared entry whose refs all sit at their old SHAs never landed: the
// entry is dropped, nothing is reset or remapped, and the
// fix-relocated-above-layer warning is emitted.
func TestReconcileRestackJournal_NeverLandedDropsWithWarning(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalPrepared, false), restackAllOldRefs())

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if loaded.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry dropped", loaded.Run().RestackJournal)
	}
	if len(fx.resetCalls) != 0 {
		t.Errorf("reset calls = %+v, want none", fx.resetCalls)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackOldAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want untouched %q", got, restackOldAnchor)
	}
	if got := restackRecoveryTip(loaded.Stack, 1, "repo-a"); got != restackOldLayerOne {
		t.Errorf("layer 1 repo-a tip = %q, want untouched %q", got, restackOldLayerOne)
	}
	warning := restackRelocationWarning(drainEvents(fx.o))
	if warning == nil {
		t.Fatal("no fix-relocated-above-layer warning event emitted")
	}
	if warning.FeatureID != fx.f.ID || warning.RepoName != "repo-a" || warning.Branch != restackLayerTwoBranch {
		t.Errorf("warning event = %+v, want the feature, repository, and top-layer branch", warning)
	}
	if warning.CanonicalError == nil || warning.CanonicalError.Summary == "" {
		t.Errorf("warning canonical error = %+v, want a rendered summary", warning.CanonicalError)
	}
	if warning.CanonicalError != nil && !strings.Contains(warning.CanonicalError.Diagnostics, "never landed") {
		t.Errorf("warning diagnostics = %q, want the never-landed diagnostic", warning.CanonicalError.Diagnostics)
	}
}

// An entry with mixed refs is externally moved state: the entry and refs
// are left untouched, nothing is reset, and the warning names the observed
// SHAs.
func TestReconcileRestackJournal_MixedRefsLeaveEntryWithWarning(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalPrepared, false), map[string]string{
		restackLayerOneRef: restackNewLayerOne,
		restackLayerTwoRef: restackOldLayerTwo,
	})
	entry := restackEntry(feature.RestackJournalPrepared, false)

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if !reflect.DeepEqual(loaded.Run().RestackJournal, []feature.RestackJournalEntry{entry}) {
		t.Fatalf("RestackJournal = %+v, want the entry left in place", loaded.Run().RestackJournal)
	}
	if len(fx.resetCalls) != 0 {
		t.Errorf("reset calls = %+v, want none", fx.resetCalls)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackOldAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want untouched %q", got, restackOldAnchor)
	}
	warning := restackRelocationWarning(drainEvents(fx.o))
	if warning == nil {
		t.Fatal("no fix-relocated-above-layer warning event emitted")
	}
	if warning.CanonicalError != nil && !strings.Contains(warning.CanonicalError.Diagnostics, restackLayerOneRef+"="+restackNewLayerOne) {
		t.Errorf("warning diagnostics = %q, want the observed ref=SHA pairs", warning.CanonicalError.Diagnostics)
	}
}

// An applied entry flagged pending sync is re-synced: the remap is not
// re-applied (anchors keep their already-persisted values), the worktree is
// reset to the new top, and the entry is cleared.
func TestReconcileRestackJournal_PendingSyncResetAndClear(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalApplied, true), restackAllNewRefs())

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if loaded.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry cleared after the sync", loaded.Run().RestackJournal)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackOldAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want the already-persisted %q (apply skipped)", got, restackOldAnchor)
	}
	if len(fx.resetCalls) != 1 || fx.resetCalls[0].SHA != restackNewLayerTwo {
		t.Fatalf("reset calls = %+v, want one hard-reset to %q", fx.resetCalls, restackNewLayerTwo)
	}
	if restackLandedEvent(drainEvents(fx.o)) == nil {
		t.Fatal("no repository-status event reporting the finished restack landing")
	}
}

// An applied entry flagged pending sync whose reset fails keeps the entry
// and its flag so the next scan retries.
func TestReconcileRestackJournal_PendingSyncFailedResetKeepsFlag(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalApplied, true), restackAllNewRefs())
	fx.resetErr = errors.New("reset boom")
	entry := restackEntry(feature.RestackJournalApplied, true)

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if !reflect.DeepEqual(loaded.Run().RestackJournal, []feature.RestackJournalEntry{entry}) {
		t.Fatalf("RestackJournal = %+v, want the applied entry kept with its pending-sync flag", loaded.Run().RestackJournal)
	}
	if len(fx.resetCalls) != 1 {
		t.Errorf("reset calls = %+v, want one attempted reset", fx.resetCalls)
	}
}

// A prepared entry whose refs landed but whose worktree reset fails is
// rewritten as applied with pending sync — the remap stayed persisted — so
// the next scan retries the reset.
func TestReconcileRestackJournal_PreparedFailedResetMarksPendingSync(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalPrepared, false), restackAllNewRefs())
	fx.resetErr = errors.New("reset boom")

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	want := restackEntry(feature.RestackJournalPrepared, false)
	want.State = feature.RestackJournalApplied
	want.PendingSync = true
	if !reflect.DeepEqual(loaded.Run().RestackJournal, []feature.RestackJournalEntry{want}) {
		t.Fatalf("RestackJournal = %+v, want the entry rewritten as applied with pending sync", loaded.Run().RestackJournal)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackNewAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want the persisted remap %q", got, restackNewAnchor)
	}
	if len(fx.resetCalls) != 1 {
		t.Errorf("reset calls = %+v, want one attempted reset", fx.resetCalls)
	}
}

// An entry naming a repository the feature no longer records is left
// untouched with the relocation warning naming it.
func TestReconcileRestackJournal_UnknownRepositoryLeavesEntry(t *testing.T) {
	entry := restackEntry(feature.RestackJournalPrepared, false)
	entry.Repository = "ghost-repo"
	fx := newRestackFixture(t, entry, restackAllNewRefs())

	if err := fx.o.ReconcileRestackJournal(context.Background()); err != nil {
		t.Fatalf("ReconcileRestackJournal: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if !reflect.DeepEqual(loaded.Run().RestackJournal, []feature.RestackJournalEntry{entry}) {
		t.Fatalf("RestackJournal = %+v, want the entry left untouched", loaded.Run().RestackJournal)
	}
	if len(fx.resetCalls) != 0 {
		t.Errorf("reset calls = %+v, want none", fx.resetCalls)
	}
	warning := restackRelocationWarning(drainEvents(fx.o))
	if warning == nil {
		t.Fatal("no fix-relocated-above-layer warning event emitted")
	}
	if warning.CanonicalError != nil && !strings.Contains(warning.CanonicalError.Diagnostics, "ghost-repo") {
		t.Errorf("warning diagnostics = %q, want the unknown repository named", warning.CanonicalError.Diagnostics)
	}
}

// The restack reconciliation pass runs inside ScanRecovery, after the
// integration pass and before the scan itself.
func TestScanRecovery_RunsRestackJournalReconciliation(t *testing.T) {
	fx := newRestackFixture(t, restackEntry(feature.RestackJournalPrepared, false), restackAllNewRefs())
	o := orchestrator.New(orchestrator.Deps{
		Store:     fx.store,
		Lifecycle: fx.mgr,
		Worktrees: fx.worktrees,
		Recovery:  &fakeRecoveryOp{},
	}, orchestrator.Hooks{})

	if _, err := o.ScanRecovery(context.Background()); err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}

	loaded := fx.loadRestackFeature(t)
	if loaded.Run().RestackJournal != nil {
		t.Fatalf("RestackJournal = %+v, want the entry reconciled by the scan", loaded.Run().RestackJournal)
	}
	if got := loaded.Run().RoadmapPhaseCommitAnchors[1]["repo-a"]; got != restackNewAnchor {
		t.Errorf("phase 1 repo-a anchor = %q, want %q", got, restackNewAnchor)
	}
	if len(fx.resetCalls) != 1 || fx.resetCalls[0].SHA != restackNewLayerTwo {
		t.Fatalf("reset calls = %+v, want one hard-reset to %q", fx.resetCalls, restackNewLayerTwo)
	}
}
