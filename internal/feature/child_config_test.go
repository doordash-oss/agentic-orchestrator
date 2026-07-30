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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

func boolPtr(b bool) *bool { return &b }

func TestUpdatePairedConfigAtomicallyUpdatesBothRecords(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-cfg",
		Slug:     "p-cfg",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main"},
		},
		Models:      config.ModelConfig{Implementation: "old-model"},
		Effort:      config.EffortConfig{Planning: "low"},
		Inquireness: feature.InquirenessMedium,
		Checkpoints: feature.Checkpoints{PhasePlanReview: false},
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-cfg", feature.RefactorChildSpec{
		Name:        "Child Cfg",
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{PhasePlanReview: true},
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Apply a paired config update addressed to the parent.
	input := feature.PairedConfigInput{
		Models:              config.ModelConfig{Implementation: "new-model"},
		Effort:              config.EffortConfig{Planning: "high"},
		Inquireness:         feature.InquirenessHigh,
		Checkpoints:         feature.Checkpoints{PhasePlanReview: true, ManualPublish: true},
		InputNotifications:  feature.InputNotificationsEnabled,
		AutomaticReviewMode: feature.AutomaticReviewDefault,
	}
	result, err := mgr.Store.UpdatePairedConfig("p-cfg", input, feature.PipelineMoonshot, "p-cfg")
	if err != nil {
		t.Fatalf("UpdatePairedConfig: %v", err)
	}
	if result.ParentID != "p-cfg" || result.ChildID != child.ID {
		t.Fatalf("result = %+v, want parent p-cfg child %s", result, child.ID)
	}

	// Verify both records have identical Review configuration axes.
	loadedParent, _ := mgr.Store.Load("p-cfg")
	loadedChild, _ := mgr.Store.Load(child.ID)

	if loadedParent.Models.Implementation != "new-model" || loadedChild.Models.Implementation != "new-model" {
		t.Fatalf("models not updated: parent=%v child=%v", loadedParent.Models.Implementation, loadedChild.Models.Implementation)
	}
	if loadedParent.Effort.Planning != "high" || loadedChild.Effort.Planning != "high" {
		t.Fatalf("effort not updated: parent=%v child=%v", loadedParent.Effort.Planning, loadedChild.Effort.Planning)
	}
	if loadedParent.Inquireness != feature.InquirenessHigh || loadedChild.Inquireness != feature.InquirenessHigh {
		t.Fatalf("inquireness not updated")
	}
	// Pipeline identity remains independent.
	if loadedParent.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("parent pipeline changed: %v", loadedParent.Pipeline)
	}
	if loadedChild.Pipeline != feature.PipelineMedium {
		t.Fatalf("child pipeline changed: %v", loadedChild.Pipeline)
	}
	// Intent is cleared.
	if loadedParent.PendingConfigUpdate != nil {
		t.Fatalf("parent still has pending config update")
	}
}

func TestUpdatePairedConfigRejectsPipelineMismatch(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-mismatch",
		Slug:     "p-mismatch",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main"},
		},
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-mismatch", feature.RefactorChildSpec{
		Name:     "Child Mismatch",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Submit pipeline that differs from the addressed parent record.
	_, err = mgr.Store.UpdatePairedConfig("p-mismatch", feature.PairedConfigInput{}, feature.PipelineMedium, "p-mismatch")
	if !errors.Is(err, feature.ErrPipelineMismatch) {
		t.Fatalf("err = %v, want ErrPipelineMismatch", err)
	}

	// Neither record changes.
	loadedParent, _ := mgr.Store.Load("p-mismatch")
	loadedChild, _ := mgr.Store.Load(child.ID)
	if loadedParent.Pipeline != feature.PipelineMoonshot {
		t.Fatalf("parent pipeline changed on mismatch: %v", loadedParent.Pipeline)
	}
	if loadedChild.Pipeline != feature.PipelineMedium {
		t.Fatalf("child pipeline changed on mismatch: %v", loadedChild.Pipeline)
	}
}

func TestUpdatePairedConfigChildAddressPreservesParentPublishMode(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-pub",
		Slug:     "p-pub",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main", Publishable: boolPtr(true)},
		},
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-pub", feature.RefactorChildSpec{
		Name:     "Child Pub",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Address the child with a config that sets ManualPublish=false.
	// Checkpoints persist as the canonical paired value without per-record
	// pipeline filtering; the parent's ManualPublish is set from the input.
	input := feature.PairedConfigInput{
		Checkpoints: feature.Checkpoints{ManualPublish: false},
	}
	_, err = mgr.Store.UpdatePairedConfig("p-pub", input, feature.PipelineMedium, child.ID)
	if err != nil {
		t.Fatalf("UpdatePairedConfig: %v", err)
	}

	loadedParent, _ := mgr.Store.Load("p-pub")
	loadedChild, _ := mgr.Store.Load(child.ID)

	// Parent's ManualPublish should be false (auto-publish).
	if loadedParent.Checkpoints.ManualPublish {
		t.Fatalf("parent ManualPublish should be false")
	}
	// Child's checkpoints are the same canonical value as the parent's.
	if loadedChild.Checkpoints.ManualPublish != loadedParent.Checkpoints.ManualPublish {
		t.Fatalf("child ManualPublish should match parent (canonical paired value)")
	}
}

func TestUpdatePairedConfigCheckpointsPersistIdentically(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-cp",
		Slug:     "p-cp",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main", Publishable: boolPtr(true)},
		},
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-cp", feature.RefactorChildSpec{
		Name:     "Child CP",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Set checkpoints with gates that may not be applicable to both
	// pipelines. They must persist identically without filtering.
	input := feature.PairedConfigInput{
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			ResearchReview:  true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	}
	if _, err := mgr.Store.UpdatePairedConfig("p-cp", input, feature.PipelineMoonshot, "p-cp"); err != nil {
		t.Fatalf("UpdatePairedConfig: %v", err)
	}

	loadedParent, _ := mgr.Store.Load("p-cp")
	loadedChild, _ := mgr.Store.Load(child.ID)

	if loadedParent.Checkpoints != loadedChild.Checkpoints {
		t.Fatalf("checkpoints diverged: parent=%+v child=%+v", loadedParent.Checkpoints, loadedChild.Checkpoints)
	}
	if loadedParent.Checkpoints != input.Checkpoints {
		t.Fatalf("parent checkpoints not canonical: got %+v want %+v", loadedParent.Checkpoints, input.Checkpoints)
	}
}

func TestReconcilePendingConfigUpdatesRollsForward(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-recon",
		Slug:     "p-recon",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main"},
		},
		Models: config.ModelConfig{Implementation: "old"},
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-recon", feature.RefactorChildSpec{
		Name:     "Child Recon",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Simulate a crash: write a pending config intent directly on the parent.
	mgr.Store.Modify("p-recon", func(f *feature.Feature) error {
		f.PendingConfigUpdate = &feature.PairedConfigIntent{
			ChildID: child.ID,
			Input: feature.PairedConfigInput{
				Models: config.ModelConfig{Implementation: "reconciled"},
			},
		}
		return nil
	})

	// Reconcile.
	reconciled, err := mgr.Store.ReconcilePendingConfigUpdates()
	if err != nil {
		t.Fatalf("ReconcilePendingConfigUpdates: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != "p-recon" {
		t.Fatalf("reconciled = %v, want [p-recon]", reconciled)
	}

	// Both records should have the new model.
	loadedParent, _ := mgr.Store.Load("p-recon")
	loadedChild, _ := mgr.Store.Load(child.ID)
	if loadedParent.Models.Implementation != "reconciled" {
		t.Fatalf("parent model not reconciled: %v", loadedParent.Models.Implementation)
	}
	if loadedChild.Models.Implementation != "reconciled" {
		t.Fatalf("child model not reconciled: %v", loadedChild.Models.Implementation)
	}
	if loadedParent.PendingConfigUpdate != nil {
		t.Fatalf("parent still has pending config intent")
	}

	// Idempotent: second run is a no-op.
	reconciled, err = mgr.Store.ReconcilePendingConfigUpdates()
	if err != nil {
		t.Fatalf("second ReconcilePendingConfigUpdates: %v", err)
	}
	if len(reconciled) != 0 {
		t.Fatalf("second reconcile should be no-op, got %v", reconciled)
	}
}

// TestUpdatePairedConfigParentSaveFailureRevertsChild verifies that when the
// child save succeeds but the parent save fails, the child is reverted to its
// pre-update config so both records converge to all-old rather than exposing
// a split pair. The pending intent is not left on the parent because the
// revert makes reconciliation unnecessary.
func TestUpdatePairedConfigParentSaveFailureRevertsChild(t *testing.T) {
	t.Parallel()
	mgr := newChildTestManager(t, map[string]string{"/wt/repo": "aaaa"}, cleanEverywhere())
	parent := &feature.Feature{
		ID:       "p-fail",
		Slug:     "p-fail",
		Status:   feature.StatusPublished,
		Pipeline: feature.PipelineMoonshot,
		Repos: []feature.FeatureRepo{
			{Name: "repo", Path: "/src/repo", WorktreePath: "/wt/repo", Branch: "main", BaseBranch: "main"},
		},
		Models:      config.ModelConfig{Implementation: "old-model"},
		Effort:      config.EffortConfig{Planning: "low"},
		Inquireness: feature.InquirenessMedium,
	}
	saveChildTestParent(t, mgr, parent)

	child, err := mgr.CreateRefactorChild("p-fail", feature.RefactorChildSpec{
		Name:     "Child Fail",
		Pipeline: feature.PipelineMedium,
	})
	if err != nil {
		t.Fatalf("CreateRefactorChild: %v", err)
	}

	// Install a save hook that fails on the 3rd saveUnlocked call.
	// Call sequence inside UpdatePairedConfig:
	//   1: saveUnlocked(parent) — writes intent
	//   2: saveUnlocked(child)  — writes new config
	//   3: saveUnlocked(parent)  — writes new config + clears intent → FAIL
	mgr.Store.SetSaveHook(&feature.StoreSaveHook{
		FailOnCall: 3,
	})
	defer mgr.Store.ResetSaveHook()

	input := feature.PairedConfigInput{
		Models:      config.ModelConfig{Implementation: "new-model"},
		Effort:      config.EffortConfig{Planning: "high"},
		Inquireness: feature.InquirenessHigh,
	}
	_, err = mgr.Store.UpdatePairedConfig("p-fail", input, feature.PipelineMoonshot, "p-fail")
	if err == nil {
		t.Fatal("expected error from UpdatePairedConfig when parent save fails")
	}

	// Both records should have the OLD config (all-old, not split).
	loadedParent, _ := mgr.Store.Load("p-fail")
	loadedChild, _ := mgr.Store.Load(child.ID)

	if loadedParent.Models.Implementation != "old-model" {
		t.Fatalf("parent model = %v, want old-model (reverted to all-old)", loadedParent.Models.Implementation)
	}
	if loadedChild.Models.Implementation != "old-model" {
		t.Fatalf("child model = %v, want old-model (reverted to all-old, not split)", loadedChild.Models.Implementation)
	}
	if loadedParent.Effort.Planning != "low" {
		t.Fatalf("parent effort = %v, want low (reverted)", loadedParent.Effort.Planning)
	}
	if loadedChild.Effort.Planning != "low" {
		t.Fatalf("child effort = %v, want low (reverted)", loadedChild.Effort.Planning)
	}

	// The pending intent should be cleared since both records are at old.
	if loadedParent.PendingConfigUpdate != nil {
		t.Fatalf("parent still has pending config intent after revert to all-old")
	}
}
