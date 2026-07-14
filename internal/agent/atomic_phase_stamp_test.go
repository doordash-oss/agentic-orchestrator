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
		if ns == nil || !ns.Touched || ns.LastError != "" {
			t.Errorf("repo %s = %+v, want Touched=true LastError=\"\"", name, ns)
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
		LastError: "max iterations reached",
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
		if ns != nil && ns.LastError != "max iterations reached" {
			t.Errorf("repo %s LastError = %q", name, ns.LastError)
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
		if ns := got.RepoStates[name]; ns != nil && (ns.Touched || ns.LastError != "") {
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

// TestAtomicPhaseStamp_FinalReviewPassedClearsStaleRepoErrors asserts the FR
// success outcome clears stale per-repo error text left by earlier failed FR
// attempts while preserving monotonic repo state and PR URL mirroring.
func TestAtomicPhaseStamp_FinalReviewPassedClearsStaleRepoErrors(t *testing.T) {
	repos := []feature.FeatureRepo{{Name: testRepoNameAPI}, {Name: testRepoNameWeb}}
	prior := map[string]*feature.RepoState{
		testRepoNameAPI: {Touched: true, LastError: "protocol violation"},
		testRepoNameWeb: {Touched: true, LastError: "final review failed"},
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
		if got.RepoStates[name].LastError != "" {
			t.Errorf("repo %s LastError = %q, want cleared after FR pass", name, got.RepoStates[name].LastError)
		}
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
		LastError: "boom",
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
	if ns.LastError != "boom" {
		t.Errorf("LastError = %q, want %q", ns.LastError, "boom")
	}
}
