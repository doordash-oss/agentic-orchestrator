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

package orchestrator

import (
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestHandleFeatureRebaseDone_PrePRCodeReadyResumesPublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-pre-pr-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Pre PR Rebase",
		Slug:          "pre-pr-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/pre-pr-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	var publishCalls int
	o.SetPublishFn(func(id string) error {
		publishCalls++
		if id != featureID {
			t.Fatalf("publish id = %q, want %q", id, featureID)
		}
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{FinalStatus: "review_passed", Repos: []string{"agentic"}},
		nil,
	)

	if publishCalls != 1 {
		t.Fatalf("publish calls = %d, want 1", publishCalls)
	}
	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	if got.RepoCycles != nil {
		t.Fatalf("RepoCycles = %+v, want cleared before publish resume", got.RepoCycles)
	}
}

func TestHandleFeatureRebaseDone_ManualPublishDoesNotResumePublish(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)

	const featureID = "feat-manual-publish-rebase"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Manual Publish Rebase",
		Slug:          "manual-publish-rebase",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
		Checkpoints:   feature.Checkpoints{ManualPublish: true},
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/manual-publish-rebase"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.SetPublishFn(func(id string) error {
		t.Fatalf("publish should not resume for manual publish feature %q", id)
		return nil
	})

	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{FinalStatus: "review_passed", Repos: []string{"agentic"}},
		nil,
	)
}

func TestHandleFeatureRebaseDone_NeedUserInputPausesCycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	const featureID = "feat-rebase-nui"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Rebase Need User Input",
		Slug:          "rebase-need-user-input",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/rebase-need-user-input"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true, PRURL: "https://github.com/example/agentic/pull/1"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleRebase, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.handleFeatureRebaseDone(featureID,
		[]agent.RebaseRepoTarget{{RepoName: "agentic", RebaseTarget: "master"}},
		&agent.RebaseLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Build gate needs a human decision.",
			NeedUserInputPath: gatePath,
			Repos:             []string{"agentic"},
		},
		nil,
	)

	got, err := mgr.Get(featureID)
	if err != nil {
		t.Fatalf("load feature: %v", err)
	}
	rc := got.RepoCycles["agentic"]
	if rc == nil {
		t.Fatal("RepoCycles[agentic] missing")
	}
	if rc.Status != feature.RepoCycleNeedUserInput {
		t.Fatalf("RepoCycles[agentic].Status = %q, want %q", rc.Status, feature.RepoCycleNeedUserInput)
	}
	if rc.PendingNeedUserInputPath != gatePath {
		t.Errorf("PendingNeedUserInputPath = %q, want %q", rc.PendingNeedUserInputPath, gatePath)
	}
	if rc.LastError != "Build gate needs a human decision." {
		t.Errorf("LastError = %q", rc.LastError)
	}
	if st := got.RepoStates["agentic"]; st == nil || st.LastError != "" {
		t.Errorf("RepoStates[agentic] = %+v, want no failure", st)
	}
}
