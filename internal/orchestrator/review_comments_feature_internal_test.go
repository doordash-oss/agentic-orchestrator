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
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestHandleFeatureReviewCommentsDone_NeedUserInputPausesCycle(t *testing.T) {
	store := feature.NewStore(t.TempDir())
	cfg := config.NewDefault()
	mgr := feature.NewManager(store, cfg)
	gatePath := filepath.Join(t.TempDir(), "need-user-input.yaml")

	const featureID = "feat-review-comments-nui"
	f := &feature.Feature{
		ID:            featureID,
		Name:          "Review Comments Need User Input",
		Slug:          "review-comments-need-user-input",
		Status:        feature.StatusPublished,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "agentic", Path: "/tmp/agentic", Branch: "feature/review-comments-need-user-input"},
		},
		RepoStates: map[string]*feature.RepoState{
			"agentic": {Touched: true, PRURL: "https://github.com/example/agentic/pull/1"},
		},
		RepoCycles: map[string]*feature.RepoCycleState{
			"agentic": {Type: feature.CycleReviewComments, Status: feature.RepoCycleRunning, Count: 1},
		},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}

	o := New(Deps{Lifecycle: mgr, Store: store}, Hooks{})
	o.handleFeatureReviewCommentsDone(featureID,
		[]agent.ReviewCommentsRepoTarget{{
			RepoName: "agentic",
			PRURL:    "https://github.com/example/agentic/pull/1",
			Comments: []ports.ReviewComment{{ID: 1}},
		}},
		&agent.ReviewCommentsLoopResult{
			FinalStatus:       "need_user_input",
			LastError:         "Reviewer request conflicts with product decision.",
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
	if rc.LastError != "Reviewer request conflicts with product decision." {
		t.Errorf("LastError = %q", rc.LastError)
	}
	if st := got.RepoStates["agentic"]; st == nil || st.LastError != "" {
		t.Errorf("RepoStates[agentic] = %+v, want no failure", st)
	}
}
