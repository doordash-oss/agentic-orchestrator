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
	"errors"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// TestUpdateFeatureConfig_ClosedChildRejectsWithoutMutation verifies that the
// serialized configuration mutation boundary keeps settled relationship
// history immutable even when a caller bypasses action discovery.
func TestUpdateFeatureConfig_ClosedChildRejectsWithoutMutation(t *testing.T) {
	closedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	f := &feature.Feature{
		ID:          "closed-child",
		Status:      feature.StatusDone,
		Pipeline:    feature.PipelineMedium,
		Models:      config.ModelConfig{Research: "old-research"},
		Inquireness: feature.InquirenessMedium,
		Checkpoints: feature.Checkpoints{RoadmapReview: true},
		Parent: &feature.ChildRelationship{
			ParentID:     "parent",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.UpdateFeatureConfig(f.ID, orchestrator.UpdateFeatureConfigInput{
		Models:      config.ModelConfig{Research: "new-research"},
		Inquireness: feature.InquirenessHigh,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	})
	if !errors.Is(err, feature.ErrChildRelationshipClosed) {
		t.Fatalf("UpdateFeatureConfig() error = %v, want ErrChildRelationshipClosed", err)
	}
	if f.Models.Research != "old-research" || f.Inquireness != feature.InquirenessMedium || f.Checkpoints != (feature.Checkpoints{RoadmapReview: true}) {
		t.Fatalf("closed child config mutated: models=%+v inquireness=%q checkpoints=%+v", f.Models, f.Inquireness, f.Checkpoints)
	}
	if events := drainEvents(o); len(events) != 0 {
		t.Fatalf("rejected config mutation emitted events: %+v", events)
	}
}

// TestUpdateFeatureConfig_QuiescentWritesAllAxes verifies that calling
// UpdateFeatureConfig on a quiescent feature overwrites every editable feature
// configuration axis; fires the OnFeatureConfigChanged hook with the
// before/after snapshots; and emits a ports.FeatureConfigChanged event.
func TestUpdateFeatureConfig_QuiescentWritesAllAxes(t *testing.T) {
	cases := []struct {
		name   string
		status feature.Status
	}{
		{"failed", feature.StatusFailed},
		{"created", feature.StatusCreated},
		{"code-ready", feature.StatusCodeReady},
		{"interrupted", feature.StatusInterrupted},
		{"done", feature.StatusDone},
		{"all three axes differ", feature.StatusCodeReady},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:                  "feat-1",
				Status:              tc.status,
				AutomaticReviewMode: feature.AutomaticReviewDisabled,
				Models: config.ModelConfig{
					Research: "old-research",
				},
				Inquireness: feature.InquirenessMedium,
				Checkpoints: feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true},
			}
			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)

			var gotBefore, gotAfter feature.ConfigSnapshot
			var hookCount int
			hooks := orchestrator.Hooks{
				OnFeatureConfigChanged: func(featureID string, before, after feature.ConfigSnapshot) {
					hookCount++
					gotBefore = before
					gotAfter = after
				},
			}
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, hooks)

			newInput := orchestrator.UpdateFeatureConfigInput{
				Models:              config.ModelConfig{Research: "new-research", Planning: "new-planning"},
				Inquireness:         feature.InquirenessHigh,
				Checkpoints:         feature.Checkpoints{InquiryReview: true, ManualPublish: true},
				AutomaticReviewMode: feature.AutomaticReviewEnabled,
			}
			if err := o.UpdateFeatureConfig("feat-1", newInput); err != nil {
				t.Fatalf("UpdateFeatureConfig: %v", err)
			}

			if f.Models.Research != "new-research" || f.Models.Planning != "new-planning" {
				t.Errorf("Models not updated: %+v", f.Models)
			}
			if f.Inquireness != feature.InquirenessHigh {
				t.Errorf("Inquireness = %q, want high", f.Inquireness)
			}
			if !f.Checkpoints.InquiryReview || !f.Checkpoints.ManualPublish {
				t.Errorf("Checkpoints not updated: %+v", f.Checkpoints)
			}
			if f.Checkpoints.RoadmapReview || f.Checkpoints.PhasePlanReview {
				t.Errorf("Checkpoints overwritten to zero-valued planning gates — RoadmapReview/PhasePlanReview should now be false, got %+v", f.Checkpoints)
			}
			if got := feature.NormalizeAutomaticReviewMode(f.AutomaticReviewMode); got != feature.AutomaticReviewEnabled {
				t.Errorf("AutomaticReviewMode = %q, want enabled", got)
			}

			if hookCount != 1 {
				t.Errorf("hook fired %d times, want 1", hookCount)
			}
			if gotBefore.Inquireness != feature.InquirenessMedium {
				t.Errorf("before.Inquireness = %q, want medium", gotBefore.Inquireness)
			}
			if gotAfter.Inquireness != feature.InquirenessHigh {
				t.Errorf("after.Inquireness = %q, want high", gotAfter.Inquireness)
			}
			if gotBefore.Models.Research != "old-research" {
				t.Errorf("before.Models.Research = %q, want old-research", gotBefore.Models.Research)
			}
			if gotAfter.Models.Research != "new-research" {
				t.Errorf("after.Models.Research = %q, want new-research", gotAfter.Models.Research)
			}
			if gotBefore.AutomaticReviewMode != feature.AutomaticReviewDisabled {
				t.Errorf("before.AutomaticReviewMode = %q, want disabled", gotBefore.AutomaticReviewMode)
			}
			if gotAfter.AutomaticReviewMode != feature.AutomaticReviewEnabled {
				t.Errorf("after.AutomaticReviewMode = %q, want enabled", gotAfter.AutomaticReviewMode)
			}

			events := drainEvents(o)
			var found bool
			for _, ev := range events {
				if ev.Type == ports.FeatureConfigChanged && ev.FeatureID == "feat-1" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected ports.FeatureConfigChanged event for feat-1; got %+v", events)
			}

			if tc.name == "all three axes differ" {
				// Deeper assertions: before/after captured by the hook must
				// mismatch on all three axes (the event itself carries only
				// the event type + feature ID).
				if gotBefore.Models == gotAfter.Models {
					t.Errorf("before.Models == after.Models: %+v", gotBefore.Models)
				}
				if gotBefore.Inquireness == gotAfter.Inquireness {
					t.Errorf("before.Inquireness == after.Inquireness: %q", gotBefore.Inquireness)
				}
				if gotBefore.Checkpoints == gotAfter.Checkpoints {
					t.Errorf("before.Checkpoints == after.Checkpoints: %+v", gotBefore.Checkpoints)
				}
				if gotBefore.AutomaticReviewMode == gotAfter.AutomaticReviewMode {
					t.Errorf("before.AutomaticReviewMode == after.AutomaticReviewMode: %q", gotBefore.AutomaticReviewMode)
				}
			}
		})
	}
}

// TestUpdateFeatureConfig_NonQuiescentWritesAllThreeAxes verifies that
// running, needs-review, and active repo-cycle features can still update the
// persisted config. The active session keeps its current snapshot; the new
// values are picked up by the next phase or restart.
func TestUpdateFeatureConfig_NonQuiescentWritesAllThreeAxes(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *feature.Feature)
	}{
		{"implementing", func(f *feature.Feature) { f.Status = feature.StatusImplementing }},
		{"planning", func(f *feature.Feature) { f.Status = feature.StatusPlanning }},
		{"researching", func(f *feature.Feature) { f.Status = feature.StatusResearching }},
		{"plan-needs-review", func(f *feature.Feature) { f.Status = feature.StatusPlanNeedsReview }},
		{"research-needs-review", func(f *feature.Feature) {
			f.Status = feature.StatusResearchNeedsReview
		}},
		{"active-repo-cycle-running", func(f *feature.Feature) {
			f.Status = feature.StatusPublished
			f.RepoCycles = map[string]*feature.RepoCycleState{
				repoName: {Status: feature.RepoCycleRunning},
			}
		}},
		{"active-repo-cycle-reviewing", func(f *feature.Feature) {
			f.Status = feature.StatusPublished
			f.RepoCycles = map[string]*feature.RepoCycleState{
				repoName: {Status: feature.RepoCycleReviewing},
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:          "feat-1",
				Models:      config.ModelConfig{Research: "old-research"},
				Inquireness: feature.InquirenessMedium,
				Checkpoints: feature.Checkpoints{RoadmapReview: true},
			}
			tc.setup(f)

			lc := lifecycleForFeature(f)
			fs := newFeatureStore(f)

			var hookCount int
			hooks := orchestrator.Hooks{
				OnFeatureConfigChanged: func(string, feature.ConfigSnapshot, feature.ConfigSnapshot) {
					hookCount++
				},
			}
			o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, hooks)

			err := o.UpdateFeatureConfig("feat-1", orchestrator.UpdateFeatureConfigInput{
				Models:      config.ModelConfig{Research: "new-research"},
				Inquireness: feature.InquirenessHigh,
				Checkpoints: feature.Checkpoints{ManualPublish: true},
			})
			if err != nil {
				t.Fatalf("UpdateFeatureConfig: %v", err)
			}

			if f.Models.Research != "new-research" {
				t.Errorf("Models.Research = %q, want new-research", f.Models.Research)
			}
			if f.Inquireness != feature.InquirenessHigh {
				t.Errorf("Inquireness = %q, want high", f.Inquireness)
			}
			if !f.Checkpoints.ManualPublish || f.Checkpoints.RoadmapReview {
				t.Errorf("Checkpoints not overwritten to edited values: %+v", f.Checkpoints)
			}
			if hookCount != 1 {
				t.Errorf("hook fired %d times, want 1", hookCount)
			}
			events := drainEvents(o)
			var found bool
			for _, ev := range events {
				if ev.Type == ports.FeatureConfigChanged && ev.FeatureID == "feat-1" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected FeatureConfigChanged event for non-quiescent update; got %+v", events)
			}
		})
	}
}

// TestUpdateFeatureConfig_NilHookIsSafe verifies that a nil
// OnFeatureConfigChanged hook does not cause a panic — the orchestrator
// checks for nil before calling. This mirrors the nil-safety pattern
// used by every other hook invocation.
func TestUpdateFeatureConfig_NilHookIsSafe(t *testing.T) {
	f := &feature.Feature{
		ID:          "feat-1",
		Status:      feature.StatusFailed,
		Inquireness: feature.InquirenessNone,
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)

	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	err := o.UpdateFeatureConfig("feat-1", orchestrator.UpdateFeatureConfigInput{
		Inquireness: feature.InquirenessHigh,
	})
	if err != nil {
		t.Fatalf("UpdateFeatureConfig with nil hook: %v", err)
	}
	if f.Inquireness != feature.InquirenessHigh {
		t.Errorf("Inquireness not updated: %q", f.Inquireness)
	}
}

func TestUpdateFeatureConfig_NormalizesCheckpointsForPipeline(t *testing.T) {
	f := &feature.Feature{
		ID:          "feat-1",
		Status:      feature.StatusCodeReady,
		Pipeline:    feature.PipelineMedium,
		Checkpoints: feature.Checkpoints{ManualPublish: true},
	}
	lc := lifecycleForFeature(f)
	fs := newFeatureStore(f)
	o := orchestrator.New(orchestrator.Deps{Lifecycle: lc, Store: fs}, orchestrator.Hooks{})

	input := orchestrator.UpdateFeatureConfigInput{
		Checkpoints: feature.Checkpoints{
			InquiryReview:   true,
			DesignReview:    true,
			RoadmapReview:   true,
			PhasePlanReview: true,
			ManualPublish:   true,
		},
	}
	if err := o.UpdateFeatureConfig("feat-1", input); err != nil {
		t.Fatalf("UpdateFeatureConfig: %v", err)
	}
	if got := f.Checkpoints; got != (feature.Checkpoints{RoadmapReview: true, PhasePlanReview: true, ManualPublish: true}) {
		t.Fatalf("normalized checkpoints = %+v, want RoadmapReview+PhasePlanReview+ManualPublish", got)
	}
}

func TestUpdateFeatureConfig_NextAskUserAutoPickUsesEditedInquireness(t *testing.T) {
	f := &feature.Feature{
		ID:           "feat-1",
		Status:       feature.StatusFailed,
		CurrentPhase: feature.PhaseInquire,
		Pipeline:     feature.PipelineLarge,
		Inquireness:  feature.InquirenessHigh,
	}
	fs := newFeatureStore(f)
	lc := lifecycleForFeature(f)

	var captured *session.SessionOpts
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*session.SessionOpts) (ports.SessionHandle, error) {
		if len(opts) > 0 {
			captured = opts[0]
		}
		return newStubSessionHandle(id, featureID, phase, ""), nil
	}

	pr := agent.NewPhaseRunner(sm, fs, t.TempDir())
	pr.BuildSessionFn = func(opts agent.BuildSessionOpts) ([]string, []string, *session.SessionOpts, error) {
		return []string{"echo", "ok"}, nil, &session.SessionOpts{
			PIDDir:        opts.PIDDir,
			PermHandler:   opts.PermHandler,
			InitialPrompt: opts.Prompt,
		}, nil
	}

	o := orchestrator.New(orchestrator.Deps{
		Lifecycle:   lc,
		Store:       fs,
		Sessions:    sm,
		PhaseRunner: pr,
		CmdRunner:   pr.CommandRunner,
	}, orchestrator.Hooks{})

	if err := o.UpdateFeatureConfig("feat-1", orchestrator.UpdateFeatureConfigInput{
		Inquireness: feature.InquirenessNone,
	}); err != nil {
		t.Fatalf("UpdateFeatureConfig: %v", err)
	}
	if err := o.StartFeature("feat-1"); err != nil {
		t.Fatalf("StartFeature: %v", err)
	}
	if captured == nil || captured.AskUserAutoPick == nil {
		t.Fatalf("StartFeature did not install AskUserAutoPick config: %+v", captured)
	}
	got, err := captured.AskUserAutoPick.LoadInquireness()
	if err != nil {
		t.Fatalf("LoadInquireness: %v", err)
	}
	if got != feature.InquirenessNone {
		t.Fatalf("LoadInquireness() = %q, want edited value %q", got, feature.InquirenessNone)
	}
	if len(fs.LoadCalls) == 0 {
		t.Fatal("LoadInquireness should read through the feature store at decision time")
	}
}
