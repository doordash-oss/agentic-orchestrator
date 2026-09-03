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
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// testPRURLAPI is the fixture PR URL tests use for the testRepoNameAPI repo
// wherever the specific URL doesn't matter.
const testPRURLAPI = "https://example.com/api/pr/1"

func newTestStoreWithFeature(t *testing.T, repos []feature.FeatureRepo, prior map[string]*feature.RepoState) (*feature.Store, string) {
	t.Helper()
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	store := feature.NewStore(stateDir)
	id := "atomicstamp-test"
	if prior == nil {
		prior = make(map[string]*feature.RepoState)
	}
	f := &feature.Feature{
		ID:            id,
		Name:          "atomic stamp test",
		Slug:          "atomic-stamp-test",
		Repos:         repos,
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		ActiveRun:     1,
		RunCount:      1,
		RepoStates:    prior,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}
	return store, id
}

func TestAtomicPhaseStamp_AllSuccess(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}, {Name: testRepoNameInfra}}
	store, id := newTestStoreWithFeature(t, repos, nil)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb, testRepoNameInfra},
		Outcome:   PhaseOutcomeReviewPassed,
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb, testRepoNameInfra} {
		ns := got.RepoStates[name]
		if ns == nil || !ns.Touched || ns.Error != nil {
			t.Errorf("repo %s = %+v, want Touched=true with no failure record", name, ns)
		}
	}
}

func TestAtomicPhaseStamp_AllFailed(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}
	store, id := newTestStoreWithFeature(t, repos, nil)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb},
		Outcome:   PhaseOutcomeFailed,
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		ns := got.RepoStates[name]
		if ns == nil || !ns.Touched {
			t.Errorf("repo %s = %+v, want Touched=true", name, ns)
		}
		if ns != nil && ns.Error != nil {
			t.Errorf("repo %s record = %+v, want none (the failure lives on the run record)", name, ns.Error)
		}
	}
}

func TestAtomicPhaseStamp_NeedUserInputDoesNotMutateRepoStatus(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}
	prior := map[string]*feature.RepoState{
		testRepoNameAPI: {},
		testRepoNameWeb: {},
	}
	store, id := newTestStoreWithFeature(t, repos, prior)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb},
		Outcome:   PhaseOutcomeNeedUserInput,
		GatePath:  "/state/runs/run-001/phase-01/implement/iteration-02/need-user-input.yaml",
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		if ns := got.RepoStates[name]; ns != nil && (ns.Touched || ns.Error != nil) {
			t.Errorf("repo %s = %+v, want untouched (NEED_USER_INPUT is feature-level only)", name, ns)
		}
	}
	if got.PendingNeedUserInputPath == "" {
		t.Errorf("expected feature-level gate path to be recorded")
	}
}

func TestAtomicPhaseStamp_DoesNotTouchOutsideRepos(t *testing.T) {
	// 3 feature repos, but only 2 in the phase.
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}, {Name: "outside"}}
	prior := map[string]*feature.RepoState{
		testRepoNameAPI: {},
		testRepoNameWeb: {},
		"outside":       {Touched: true, PRURL: "https://example.com/outside/pr/1"},
	}
	store, id := newTestStoreWithFeature(t, repos, prior)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb},
		Outcome:   PhaseOutcomeReviewPassed,
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.RepoStates["outside"].PRURL != "https://example.com/outside/pr/1" {
		t.Errorf("outside repo was mutated: %+v", got.RepoStates["outside"])
	}
	if !got.RepoStates[testRepoNameAPI].Touched {
		t.Errorf("api Touched = false, want true")
	}
}

func TestAtomicPhaseStamp_NilStoreErrors(t *testing.T) {
	err := AtomicPhaseStamp(nil, AtomicPhaseStampInput{FeatureID: "x"})
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestAtomicPhaseStamp_EmptyFeatureIDErrors(t *testing.T) {
	store, _ := newTestStoreWithFeature(t, nil, nil)
	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{FeatureID: ""})
	if err == nil {
		t.Fatal("expected error for empty feature id")
	}
}

func TestAtomicPhaseStamp_PRURLsApplied(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}
	store, id := newTestStoreWithFeature(t, repos, nil)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb},
		Outcome:   PhaseOutcomeReviewPassed,
		PRURLs:    map[string]string{testRepoNameAPI: testPRURLAPI},
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.RepoStates[testRepoNameAPI].PRURL != testPRURLAPI {
		t.Errorf("api PRURL = %q", got.RepoStates[testRepoNameAPI].PRURL)
	}
}

// TestAtomicPhaseStamp_FinalReviewPassedPreservesPublishRecords asserts the
// FR success outcome only mirrors PR URLs: the per-repo stored record is
// publish-scoped — written at the publish boundary and cleared by the
// published setter or phase retry — so a phase outcome stamp never touches
// it, while monotonic repo state is preserved.
func TestAtomicPhaseStamp_FinalReviewPassedPreservesPublishRecords(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}
	prior := map[string]*feature.RepoState{
		testRepoNameAPI: {Touched: true, Error: &errcat.FailureRecord{Code: errcat.PublishPushFailed, Diagnostics: "push failed"}},
		testRepoNameWeb: {Touched: true},
	}
	store, id := newTestStoreWithFeature(t, repos, prior)

	err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI, testRepoNameWeb},
		Outcome:   PhaseOutcomeFinalReviewPassed,
		PRURLs:    map[string]string{testRepoNameAPI: testPRURLAPI},
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, name := range []string{testRepoNameAPI, testRepoNameWeb} {
		// Touched flag must remain set (monotonic).
		if !got.RepoStates[name].Touched {
			t.Errorf("repo %s lost Touched=true after FR pass", name)
		}
	}
	if got.RepoStates[testRepoNameAPI].Error == nil {
		t.Errorf("api publish record cleared by the FR pass, want preserved (publish-scoped)")
	}
	if got.RepoStates[testRepoNameAPI].PRURL != testPRURLAPI {
		t.Errorf("api PRURL not mirrored: %q", got.RepoStates[testRepoNameAPI].PRURL)
	}
}

// TestAtomicPhaseStamp_TouchedIsMonotonic exercises the contract that
// Touched is monotonic: once true, repeated stamps never reset it.
func TestAtomicPhaseStamp_TouchedIsMonotonic(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}}
	store, id := newTestStoreWithFeature(t, repos, nil)

	if err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI},
		Outcome:   PhaseOutcomeReviewPassed,
	}); err != nil {
		t.Fatalf("stamp 1: %v", err)
	}
	if err := AtomicPhaseStamp(store, AtomicPhaseStampInput{
		FeatureID: id,
		Repos:     []string{testRepoNameAPI},
		Outcome:   PhaseOutcomeFailed,
	}); err != nil {
		t.Fatalf("stamp 2: %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ns := got.RepoStates[testRepoNameAPI]
	if ns == nil || !ns.Touched {
		t.Fatalf("Touched not monotonic: %+v", ns)
	}
	if ns.Error != nil {
		t.Errorf("record = %+v, want none (phase failures live on the run record)", ns.Error)
	}
}
