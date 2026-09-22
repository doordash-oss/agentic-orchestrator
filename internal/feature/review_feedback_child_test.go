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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func TestCreateReviewFeedbackChildPersistsSelectedFeedback(t *testing.T) {
	t.Parallel()

	heads := map[string]string{
		"/wt/api": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"/wt/web": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	mgr := newChildTestManager(t, heads, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "parent-review-feedback",
		Name:     "Parent",
		Slug:     "parent",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "feature/parent-api", BaseBranch: "main"},
			{Name: "web", Path: "/src/web", WorktreePath: "/wt/web", Branch: "feature/parent-web", BaseBranch: "main"},
		},
		Models:       config.ModelConfig{Planning: "planning-model", Implementation: "implementation-model"},
		Effort:       config.EffortConfig{Planning: "high", Implementation: "medium"},
		RiskLevel:    feature.RiskHigh,
		ExitCriteria: "all selected feedback is addressed",
		Inquireness:  feature.InquirenessHigh,
		Checkpoints:  feature.Checkpoints{RoadmapReview: true, ManualPublish: true},
		RepoStates: map[string]*feature.RepoState{
			"api": {Touched: true},
			"web": {Touched: true},
		},
		// Each repository's pull request lives on its stack layer entry.
		Stack: []feature.StackLayer{{
			Position: 1,
			Repos: map[string]feature.StackRepoEntry{
				"api": {PRURL: "https://github.example/acme/api/pull/17", PRState: feature.StackPRStateOpen},
				"web": {PRURL: "https://github.example/acme/web/pull/23", PRState: feature.StackPRStateOpen},
			},
		}},
	}
	saveChildTestParent(t, mgr, parent)

	gate := false
	comments := []feature.ReviewFeedbackComment{
		{Repo: "api", ID: 101, Type: "review", Path: "handler.go", Line: 42, Author: "alice", Body: "Handle the error here.", DiffHunk: "@@ -40,2 +40,3 @@", InReplyTo: 99},
		{Repo: "web", ID: 202, Type: "issue", Author: "bob", Body: "Please add a regression test."},
	}
	child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{
		Comments:    comments,
		GateEnabled: &gate,
	})
	if err != nil {
		t.Fatalf("CreateReviewFeedbackChild() error = %v", err)
	}

	if child.Name != feature.ReviewFeedbackChildName {
		t.Errorf("child.Name = %q, want %q", child.Name, feature.ReviewFeedbackChildName)
	}
	if child.Parent == nil || child.Parent.Kind != feature.ChildKindReviewFeedback {
		t.Fatalf("child.Parent = %+v, want review-feedback relationship", child.Parent)
	}
	if child.Pipeline != feature.PipelineMedium {
		t.Errorf("child.Pipeline = %q, want %q", child.Pipeline, feature.PipelineMedium)
	}
	if child.Models != parent.Models || child.Effort != parent.Effort || child.RiskLevel != parent.RiskLevel ||
		child.Inquireness != parent.Inquireness {
		t.Errorf("child inherited config = models:%+v effort:%+v risk:%q exit:%q inquireness:%q", child.Models, child.Effort, child.RiskLevel, child.ExitCriteria, child.Inquireness)
	}
	if child.ExitCriteria == parent.ExitCriteria {
		t.Errorf("child ExitCriteria inherited parent's; want deterministic review-feedback outcomes contract")
	}
	if !strings.Contains(child.ExitCriteria, feature.ReviewFeedbackOutcomesFilename) {
		t.Errorf("child ExitCriteria = %q, want outcomes file path %q", child.ExitCriteria, feature.ReviewFeedbackOutcomesFilename)
	}
	if !strings.Contains(child.ExitCriteria, "Comment 101") || !strings.Contains(child.ExitCriteria, "Comment 202") {
		t.Errorf("child ExitCriteria = %q, want selected comment IDs 101 and 202", child.ExitCriteria)
	}
	if child.Checkpoints.RoadmapReview || child.Checkpoints.PhasePlanReview {
		t.Errorf("child review gates = %+v, want roadmap and phase-plan disabled", child.Checkpoints)
	}
	if child.BaseSHA("api") != heads["/wt/api"] || child.BaseSHA("web") != heads["/wt/web"] {
		t.Errorf("child base SHAs = api:%q web:%q", child.BaseSHA("api"), child.BaseSHA("web"))
	}
	if len(child.ReviewFeedback) != 2 || child.ReviewFeedback[0] != comments[0] || child.ReviewFeedback[1] != comments[1] {
		t.Errorf("child.ReviewFeedback = %+v, want %+v", child.ReviewFeedback, comments)
	}
	for _, text := range []string{
		"This pass addresses selected pull request review feedback.",
		"## Repository: api",
		"Pull request: https://github.example/acme/api/pull/17",
		"### Comment 101 (review)",
		"File: handler.go:42",
		"Author: alice",
		"Handle the error here.",
		"@@ -40,2 +40,3 @@",
		"## Repository: web",
		"Pull request: https://github.example/acme/web/pull/23",
		"### Comment 202 (issue)",
		"Please add a regression test.",
	} {
		if !strings.Contains(child.Description, text) {
			t.Errorf("child.Description missing %q:\n%s", text, child.Description)
		}
	}

	reloaded, err := mgr.Store.Load(child.ID)
	if err != nil {
		t.Fatalf("Store.Load(%q) error = %v", child.ID, err)
	}
	if len(reloaded.ReviewFeedback) != len(comments) || reloaded.ReviewFeedback[0] != comments[0] || reloaded.ReviewFeedback[1] != comments[1] {
		t.Errorf("reloaded.ReviewFeedback = %+v, want %+v", reloaded.ReviewFeedback, comments)
	}
	if reloaded.Description != child.Description {
		t.Errorf("reloaded.Description differs from created description")
	}

	reloadedParent, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatalf("Store.Load(%q) error = %v", parent.ID, err)
	}
	if reloadedParent.ExitCriteria != parent.ExitCriteria {
		t.Errorf("reloaded parent ExitCriteria = %q, want %q (parent's original exit criteria must survive review-feedback child creation)",
			reloadedParent.ExitCriteria, parent.ExitCriteria)
	}
}

func TestCreateReviewFeedbackChildInheritsParentDeliveryMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		parent      feature.DeliveryMode // stored value; "" models a legacy record
		wantPersist feature.DeliveryMode // child's persisted mode
	}{
		{"single parent", feature.DeliveryModeSingle, feature.DeliveryModeSingle},
		{"stack parent", feature.DeliveryModeStack, feature.DeliveryModeStack},
		{"legacy parent with no stored mode", "", feature.DeliveryModeStack},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newChildTestManager(t, map[string]string{"/wt/api": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, cleanEverywhere())
			parent := &feature.Feature{
				ID:       "parent-rf-delivery",
				Name:     "Parent",
				Slug:     "parent",
				Status:   feature.StatusPublished,
				Pipeline: feature.PipelineMoonshot,
				Repos: []feature.FeatureRepo{
					{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "feature/parent-api", BaseBranch: "main"},
				},
				ExitCriteria: "all selected feedback is addressed",
				Checkpoints:  feature.Checkpoints{RoadmapReview: true, ManualPublish: true},
				RepoStates: map[string]*feature.RepoState{
					"api": {Touched: true},
				},
				Stack: []feature.StackLayer{{
					Position: 1,
					Repos: map[string]feature.StackRepoEntry{
						"api": {PRURL: "https://github.example/acme/api/pull/17", PRState: feature.StackPRStateOpen},
					},
				}},
				DeliveryMode: tt.parent,
			}
			saveChildTestParent(t, mgr, parent)

			child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{
				Comments: []feature.ReviewFeedbackComment{
					{Repo: "api", ID: 101, Type: "issue", Author: "alice", Body: "Please add a regression test."},
				},
			})
			if err != nil {
				t.Fatalf("CreateReviewFeedbackChild() error = %v", err)
			}
			if child.DeliveryMode != tt.wantPersist {
				t.Fatalf("child DeliveryMode = %q, want %q", child.DeliveryMode, tt.wantPersist)
			}
			if child.EffectiveDeliveryMode() != parent.EffectiveDeliveryMode() {
				t.Fatalf("child effective DeliveryMode = %q, want parent effective %q", child.EffectiveDeliveryMode(), parent.EffectiveDeliveryMode())
			}

			loaded, err := mgr.Store.Load(child.ID)
			if err != nil {
				t.Fatalf("Store.Load(%q) error = %v", child.ID, err)
			}
			if loaded.DeliveryMode != tt.wantPersist {
				t.Fatalf("reloaded DeliveryMode = %q, want %q", loaded.DeliveryMode, tt.wantPersist)
			}

			// The paired-config back-sync must not copy the delivery mode.
			reloadedParent, err := mgr.Store.Load(parent.ID)
			if err != nil {
				t.Fatalf("Store.Load(%q) error = %v", parent.ID, err)
			}
			if reloadedParent.DeliveryMode != tt.parent {
				t.Fatalf("parent DeliveryMode = %q, want untouched %q", reloadedParent.DeliveryMode, tt.parent)
			}
		})
	}
}

func TestCreateReviewFeedbackChildResolvesGateOnParentAndChild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		parentRoadmap bool
		gate          *bool
		wantGate      bool
	}{
		{name: "omitted inherits enabled parent gate", parentRoadmap: true, wantGate: true},
		{name: "omitted inherits disabled parent gate", parentRoadmap: false, wantGate: false},
		{name: "explicit enable overrides parent", parentRoadmap: false, gate: boolPointer(true), wantGate: true},
		{name: "explicit disable overrides parent", parentRoadmap: true, gate: boolPointer(false), wantGate: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaaaaaa"}, cleanEverywhere())
			parent := &feature.Feature{
				ID:          "parent-gate",
				Slug:        "parent-gate",
				Status:      feature.StatusPublished,
				Pipeline:    feature.PipelineMoonshot,
				Repos:       []feature.FeatureRepo{{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main"}},
				Checkpoints: feature.Checkpoints{RoadmapReview: tt.parentRoadmap, ManualPublish: true},
				RepoStates:  map[string]*feature.RepoState{"repo": {Touched: true}},
				Stack: []feature.StackLayer{{
					Position: 1,
					Repos: map[string]feature.StackRepoEntry{
						"repo": {PRURL: "https://github.example/acme/repo/pull/1", PRState: feature.StackPRStateOpen},
					},
				}},
			}
			saveChildTestParent(t, mgr, parent)

			child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{
				Comments:    []feature.ReviewFeedbackComment{{Repo: "repo", ID: 1, Type: "issue"}},
				GateEnabled: tt.gate,
			})
			if err != nil {
				t.Fatalf("CreateReviewFeedbackChild() error = %v", err)
			}
			reloadedParent, err := mgr.Store.Load(parent.ID)
			if err != nil {
				t.Fatalf("Store.Load(parent) error = %v", err)
			}
			for record, checkpoints := range map[string]feature.Checkpoints{
				"parent": reloadedParent.Checkpoints,
				"child":  child.Checkpoints,
			} {
				if checkpoints.RoadmapReview != tt.wantGate || checkpoints.PhasePlanReview != tt.wantGate {
					t.Errorf("%s checkpoints = %+v, want coupled gate %t", record, checkpoints, tt.wantGate)
				}
				if !checkpoints.ManualPublish {
					t.Errorf("%s checkpoints = %+v, want unrelated ManualPublish preserved", record, checkpoints)
				}
			}
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestCreateReviewFeedbackChildRejectsInvalidSelectionBeforeWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    feature.ReviewFeedbackChildSpec
		wantErr error
	}{
		{
			name:    "empty selection",
			spec:    feature.ReviewFeedbackChildSpec{},
			wantErr: feature.ErrReviewFeedbackEmptySelection,
		},
		{
			name: "unsupported comment type",
			spec: feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{
				{Repo: "api", ID: 73, Type: "commit_comment"},
			}},
			wantErr: feature.ErrReviewFeedbackUnsupportedCommentType,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := newChildTestManager(t, nil, cleanEverywhere())
			_, err := mgr.CreateReviewFeedbackChild("missing-parent", tt.spec)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("CreateReviewFeedbackChild() error = %v, want %v", err, tt.wantErr)
			}
			features, err := mgr.Store.List()
			if err != nil {
				t.Fatalf("Store.List() error = %v", err)
			}
			if len(features) != 0 {
				t.Errorf("Store.List() = %+v, want no records after invalid selection", features)
			}
		})
	}
}

func TestCreateReviewFeedbackChildRejectsInvalidCommentRepositoryBeforeWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		commentRepo string
		wantText    string
	}{
		{name: "unknown repository", commentRepo: "unknown", wantText: "does not belong to parent"},
		{name: "repository without pull request", commentRepo: "web", wantText: "has no pull request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := newChildTestManager(t, nil, cleanEverywhere())
			parent := &feature.Feature{
				ID:     "parent-repo-validation",
				Slug:   "parent-repo-validation",
				Status: feature.StatusPublished,
				Repos: []feature.FeatureRepo{
					{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "main"},
					{Name: "web", Path: "/src/web", WorktreePath: "/wt/web", Branch: "main"},
				},
				RepoStates: map[string]*feature.RepoState{
					"api": {Touched: true},
					"web": {},
				},
				// Only api carries a pull request: web must be rejected as a
				// repository without one.
				Stack: []feature.StackLayer{{
					Position: 1,
					Repos: map[string]feature.StackRepoEntry{
						"api": {PRURL: "https://github.example/acme/api/pull/1", PRState: feature.StackPRStateOpen},
					},
				}},
			}
			saveChildTestParent(t, mgr, parent)

			_, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{
				{Repo: tt.commentRepo, ID: 7, Type: "issue"},
			}})
			if err == nil || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("CreateReviewFeedbackChild() error = %v, want text %q", err, tt.wantText)
			}
			reloaded, loadErr := mgr.Store.Load(parent.ID)
			if loadErr != nil {
				t.Fatalf("Store.Load(parent) error = %v", loadErr)
			}
			if reloaded.PendingChild != nil {
				t.Fatalf("parent pending child = %+v, want nil after validation failure", reloaded.PendingChild)
			}
			features, listErr := mgr.Store.List()
			if listErr != nil {
				t.Fatalf("Store.List() error = %v", listErr)
			}
			if len(features) != 1 {
				t.Fatalf("Store.List() = %+v, want only parent after validation failure", features)
			}
		})
	}
}

// Launch validation requires the comment's pull request to be an open layer
// pull request of its repository: a repository whose every layer pull
// request is merged counts as having no open layer pull request (the
// existing no-PR error), and a comment naming a pull request that is not
// one of the repository's open layer pull requests is rejected outright.
func TestCreateReviewFeedbackChildValidatesCommentPullRequest(t *testing.T) {
	t.Parallel()

	heads := map[string]string{"/wt/api": "aaaaaaaa", "/wt/web": "bbbbbbbb"}
	mgr := newChildTestManager(t, heads, cleanEverywhere())
	parent := &feature.Feature{
		ID:     "parent-pr-validation",
		Slug:   "parent-pr-validation",
		Status: feature.StatusPublished,
		Repos: []feature.FeatureRepo{
			{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "main"},
			{Name: "web", Path: "/src/web", WorktreePath: "/wt/web", Branch: "main"},
		},
		RepoStates: map[string]*feature.RepoState{
			"api": {Touched: true},
			"web": {Touched: true},
		},
		// api layer 1 is open and layer 2 is merged; web's only layer pull
		// request is merged, so web has no open layer pull request at all.
		Stack: []feature.StackLayer{
			{
				Position: 1,
				Title:    "Foundation",
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.example/acme/api/pull/1", PRState: feature.StackPRStateOpen},
					"web": {PRURL: "https://github.example/acme/web/pull/5", PRState: feature.StackPRStateMerged},
				},
			},
			{
				Position: 2,
				Title:    "Extension",
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.example/acme/api/pull/2", PRState: feature.StackPRStateMerged},
				},
			},
		},
	}
	saveChildTestParent(t, mgr, parent)

	if _, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{
		{Repo: "web", ID: 7, Type: "issue"},
	}}); !errors.Is(err, feature.ErrReviewFeedbackRepoHasNoPR) {
		t.Fatalf("CreateReviewFeedbackChild() error = %v, want the existing no-PR error for a repository without an open layer PR", err)
	}

	if _, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{
		{Repo: "api", ID: 8, Type: "issue", PRURL: "https://github.example/acme/api/pull/2", PRNumber: 2, LayerPosition: 2, LayerTitle: "Extension"},
	}}); !errors.Is(err, feature.ErrReviewFeedbackCommentPRNotOpen) {
		t.Fatalf("CreateReviewFeedbackChild() error = %v, want the not-open error for a comment naming a merged layer PR", err)
	}

	child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{
		{Repo: "api", ID: 9, Type: "issue", PRURL: "https://github.example/acme/api/pull/1", PRNumber: 1, LayerPosition: 1, LayerTitle: "Foundation"},
	}})
	if err != nil {
		t.Fatalf("CreateReviewFeedbackChild() error = %v, want acceptance for a comment on the open layer PR", err)
	}
	if len(child.ReviewFeedback) != 1 || child.ReviewFeedback[0].PRURL != "https://github.example/acme/api/pull/1" {
		t.Fatalf("child comments = %+v, want the open layer PR's comment with its identity", child.ReviewFeedback)
	}
}

func TestReconcilePendingReviewFeedbackChildCreation(t *testing.T) {
	t.Parallel()

	store := feature.NewStore(t.TempDir())
	comments := []feature.ReviewFeedbackComment{{Repo: "api", ID: 11, Type: "review", Path: "api.go", Line: 7, Author: "alice", Body: "Check this error.", DiffHunk: "@@ -7 +7 @@"}}
	parent := &feature.Feature{
		ID:            "parent-review-intent",
		Slug:          "parent-review-intent",
		Status:        feature.StatusPublished,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	parent.PendingChild = &feature.ChildCreationIntent{
		ChildID:   "child-review-intent",
		Kind:      feature.ChildKindReviewFeedback,
		CreatedAt: time.Now(),
		Child: feature.Feature{
			ID:             "child-review-intent",
			Name:           feature.ReviewFeedbackChildName,
			Slug:           "address-review-feedback",
			Description:    "deterministic review feedback description",
			Status:         feature.StatusSettingUpWorktrees,
			CurrentPhase:   feature.PhasePlan,
			Pipeline:       feature.PipelineMedium,
			Repos:          []feature.FeatureRepo{{Name: "api", Path: "/src/api", Branch: "feature/address-review-feedback-child"}},
			Checkpoints:    feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true},
			Parent:         &feature.ChildRelationship{ParentID: parent.ID, Kind: feature.ChildKindReviewFeedback, Bases: []feature.ChildRepoBase{{Repo: "api", SHA: "aaaaaaaa"}}},
			ReviewFeedback: comments,
			SchemaVersion:  feature.SchemaVersionCurrent,
		},
		Setup: &feature.SetupState{Status: feature.SetupStatusRunning, Attempt: 1},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("Store.Save(parent) error = %v", err)
	}

	reconciled, err := store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("ReconcilePendingChildCreations() error = %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != parent.ID {
		t.Fatalf("ReconcilePendingChildCreations() = %v, want [%s]", reconciled, parent.ID)
	}
	child, err := store.Load("child-review-intent")
	if err != nil {
		t.Fatalf("Store.Load(child) error = %v", err)
	}
	if child.Parent == nil || child.Parent.Kind != feature.ChildKindReviewFeedback || child.Pipeline != feature.PipelineMedium {
		t.Errorf("recovered child = parent:%+v pipeline:%q", child.Parent, child.Pipeline)
	}
	if len(child.ReviewFeedback) != 1 || child.ReviewFeedback[0] != comments[0] {
		t.Errorf("recovered ReviewFeedback = %+v, want %+v", child.ReviewFeedback, comments)
	}
	if child.Description != "deterministic review feedback description" {
		t.Errorf("recovered Description = %q", child.Description)
	}
	reloadedParent, err := store.Load(parent.ID)
	if err != nil {
		t.Fatalf("Store.Load(parent) error = %v", err)
	}
	if reloadedParent.PendingChild != nil {
		t.Errorf("reloaded parent PendingChild = %+v, want nil", reloadedParent.PendingChild)
	}
}

func TestCreateReviewFeedbackChildDescriptionIsDeterministic(t *testing.T) {
	t.Parallel()

	comments := []feature.ReviewFeedbackComment{
		{Repo: "api", ID: 41, Type: "review_body", Author: "reviewer", Body: "Explain why this is safe."},
		{Repo: "api", ID: 42, Type: "review", Path: "service.go", Line: 18, Author: "reviewer", Body: "Cover the nil case.", DiffHunk: "@@ -17,2 +17,4 @@"},
	}
	create := func(t *testing.T) *feature.Feature {
		t.Helper()
		mgr := newChildTestManager(t, map[string]string{"/wt/api": "aaaaaaaa"}, cleanEverywhere())
		parent := &feature.Feature{
			ID:       "parent-deterministic",
			Slug:     "parent-deterministic",
			Status:   feature.StatusPublished,
			Pipeline: feature.PipelineLarge,
			Repos:    []feature.FeatureRepo{{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "main"}},
			RepoStates: map[string]*feature.RepoState{
				"api": {Touched: true},
			},
			Stack: []feature.StackLayer{{
				Position: 1,
				Repos: map[string]feature.StackRepoEntry{
					"api": {PRURL: "https://github.example/acme/api/pull/3", PRState: feature.StackPRStateOpen},
				},
			}},
		}
		saveChildTestParent(t, mgr, parent)
		child, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: comments})
		if err != nil {
			t.Fatalf("CreateReviewFeedbackChild() error = %v", err)
		}
		return child
	}

	first := create(t)
	second := create(t)
	if first.ID == second.ID {
		t.Fatalf("independent children unexpectedly share ID %q", first.ID)
	}
	if first.Description != second.Description {
		t.Errorf("descriptions differ for identical selections:\nfirst:\n%s\nsecond:\n%s", first.Description, second.Description)
	}
}

func TestCreateReviewFeedbackChildInterruptedWriteRollsForward(t *testing.T) {
	t.Parallel()

	mgr := newChildTestManager(t, map[string]string{"/wt/api": "aaaaaaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "parent-interrupted-review",
		Slug:     "parent-interrupted-review",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos:    []feature.FeatureRepo{{Name: "api", Path: "/src/api", WorktreePath: "/wt/api", Branch: "main"}},
		RepoStates: map[string]*feature.RepoState{
			"api": {Touched: true},
		},
		Stack: []feature.StackLayer{{
			Position: 1,
			Repos: map[string]feature.StackRepoEntry{
				"api": {PRURL: "https://github.example/acme/api/pull/8", PRState: feature.StackPRStateOpen},
			},
		}},
	}
	saveChildTestParent(t, mgr, parent)
	comment := feature.ReviewFeedbackComment{Repo: "api", ID: 81, Type: "issue", Author: "reviewer", Body: "Add the missing test."}
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{FailOnCall: 2})
	_, err := mgr.CreateReviewFeedbackChild(parent.ID, feature.ReviewFeedbackChildSpec{Comments: []feature.ReviewFeedbackComment{comment}})
	if err == nil || !strings.Contains(err.Error(), "saving child") {
		t.Fatalf("CreateReviewFeedbackChild() error = %v, want injected child-save failure", err)
	}
	mgr.Store.ResetSaveHook()

	interruptedParent, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatalf("Store.Load(parent) error = %v", err)
	}
	if interruptedParent.PendingChild == nil || interruptedParent.PendingChild.Kind != feature.ChildKindReviewFeedback {
		t.Fatalf("interrupted parent PendingChild = %+v, want review-feedback intent", interruptedParent.PendingChild)
	}
	childID := interruptedParent.PendingChild.ChildID
	if len(interruptedParent.PendingChild.Child.ReviewFeedback) != 1 || interruptedParent.PendingChild.Child.ReviewFeedback[0] != comment {
		t.Errorf("pending intent ReviewFeedback = %+v, want %+v", interruptedParent.PendingChild.Child.ReviewFeedback, comment)
	}

	reconciled, err := mgr.Store.ReconcilePendingChildCreations()
	if err != nil {
		t.Fatalf("ReconcilePendingChildCreations() error = %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != parent.ID {
		t.Fatalf("ReconcilePendingChildCreations() = %v, want [%s]", reconciled, parent.ID)
	}
	child, err := mgr.Store.Load(childID)
	if err != nil {
		t.Fatalf("Store.Load(recovered child) error = %v", err)
	}
	if child.Parent == nil || child.Parent.Kind != feature.ChildKindReviewFeedback || len(child.ReviewFeedback) != 1 || child.ReviewFeedback[0] != comment {
		t.Errorf("recovered child = parent:%+v feedback:%+v", child.Parent, child.ReviewFeedback)
	}
	settledParent, err := mgr.Store.Load(parent.ID)
	if err != nil {
		t.Fatalf("Store.Load(settled parent) error = %v", err)
	}
	if settledParent.PendingChild != nil {
		t.Errorf("settled parent PendingChild = %+v, want nil", settledParent.PendingChild)
	}
}
