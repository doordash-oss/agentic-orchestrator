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
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// newConfigOpFixture builds an orchestrator over a real store and manager
// holding features, so paired config updates run end to end.
func newConfigOpFixture(t *testing.T, hooks Hooks, features ...*feature.Feature) (*Orchestrator, *feature.Store) {
	t.Helper()
	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	for _, f := range features {
		f.SchemaVersion = feature.SchemaVersionCurrent
		if err := store.Save(f); err != nil {
			t.Fatalf("save %s: %v", f.ID, err)
		}
	}
	o := New(Deps{Lifecycle: feature.NewManager(store, config.NewDefault()), Store: store}, hooks)
	t.Cleanup(func() { _ = o.Shutdown() })
	return o, store
}

func configChangedIDs(o *Orchestrator) map[string]bool {
	ids := map[string]bool{}
	for _, ev := range drainOpEvents(o) {
		if ev.Type == ports.FeatureConfigChanged {
			ids[ev.FeatureID] = true
		}
	}
	return ids
}

func TestUpdateFeatureConfigOperation(t *testing.T) {
	newModels := config.ModelConfig{Planning: "new-planning"}

	t.Run("single record keeps the current mode when none is supplied and returns the snapshot", func(t *testing.T) {
		f := opFeature("cfg-1", feature.StatusInterrupted, feature.PhasePlan)
		f.AutomaticReviewMode = feature.AutomaticReviewEnabled
		o, _ := newConfigOpFixture(t, Hooks{}, f)
		var steps []string
		o.traceStep = func(step string) { steps = append(steps, step) }

		got, err := o.UpdateFeatureConfig(f.ID, UpdateFeatureConfigInput{Models: newModels, Inquireness: feature.InquirenessHigh})
		if err != nil {
			t.Fatalf("UpdateFeatureConfig() error = %v", err)
		}
		if got.Models.Planning != "new-planning" || got.Inquireness != feature.InquirenessHigh {
			t.Fatalf("snapshot = models %+v inquireness %q, want the update", got.Models, got.Inquireness)
		}
		if mode := feature.NormalizeAutomaticReviewMode(got.AutomaticReviewMode); mode != feature.AutomaticReviewEnabled {
			t.Fatalf("automatic review mode = %q, want the current mode kept", mode)
		}
		if want := []string{traceLockAcquired, traceLockReleased}; len(steps) != 2 || steps[0] != want[0] || steps[1] != want[1] {
			t.Fatalf("steps = %q, want %q", steps, want)
		}
		if ids := configChangedIDs(o); !ids[f.ID] || len(ids) != 1 {
			t.Fatalf("config-changed events = %v, want only %s", ids, f.ID)
		}
	})

	t.Run("supplied mode replaces the current mode", func(t *testing.T) {
		f := opFeature("cfg-1", feature.StatusInterrupted, feature.PhasePlan)
		f.AutomaticReviewMode = feature.AutomaticReviewEnabled
		o, _ := newConfigOpFixture(t, Hooks{}, f)
		disabled := feature.AutomaticReviewDisabled
		got, err := o.UpdateFeatureConfig(f.ID, UpdateFeatureConfigInput{AutomaticReviewMode: &disabled})
		if err != nil {
			t.Fatalf("UpdateFeatureConfig() error = %v", err)
		}
		if mode := feature.NormalizeAutomaticReviewMode(got.AutomaticReviewMode); mode != feature.AutomaticReviewDisabled {
			t.Fatalf("automatic review mode = %q, want disabled", mode)
		}
	})

	t.Run("active child routes through the paired update", func(t *testing.T) {
		parent, child := opParentWithChild()
		parent.Status = feature.StatusPublished
		o, store := newConfigOpFixture(t, Hooks{}, parent, child)
		got, err := o.UpdateFeatureConfig(child.ID, UpdateFeatureConfigInput{Models: newModels, Pipeline: child.Pipeline})
		if err != nil {
			t.Fatalf("UpdateFeatureConfig() error = %v", err)
		}
		if got.ID != child.ID || got.Models.Planning != "new-planning" {
			t.Fatalf("snapshot = %s models %+v, want the updated child", got.ID, got.Models)
		}
		if ids := configChangedIDs(o); !ids[parent.ID] || !ids[child.ID] {
			t.Fatalf("config-changed events = %v, want parent and child", ids)
		}
		if _, err := store.Load(parent.ID); err != nil {
			t.Fatalf("load parent: %v", err)
		}
	})

	t.Run("paired update rejects a pipeline that does not match the addressed record", func(t *testing.T) {
		parent, child := opParentWithChild()
		parent.Status = feature.StatusPublished
		o, _ := newConfigOpFixture(t, Hooks{}, parent, child)
		if _, err := o.UpdateFeatureConfig(child.ID, UpdateFeatureConfigInput{Pipeline: feature.PipelineMoonshot}); err == nil {
			t.Fatal("UpdateFeatureConfig() with a mismatched pipeline error = nil")
		}
	})
}

func TestConfigOperationsRejectClosedChild(t *testing.T) {
	operations := map[string]func(o *Orchestrator, id string) error{
		"config update": func(o *Orchestrator, id string) error {
			_, err := o.UpdateFeatureConfig(id, UpdateFeatureConfigInput{})
			return err
		},
		"automatic review enablement": func(o *Orchestrator, id string) error {
			return o.EnableFeatureAutomaticReview(id)
		},
	}
	for name, run := range operations {
		t.Run(name, func(t *testing.T) {
			parent, child := opParentWithChild()
			closedAt := time.Now()
			child.Parent.CloseOutcome = feature.ChildCloseOutcomeDiscarded
			child.Parent.ClosedAt = &closedAt
			o, store := newConfigOpFixture(t, Hooks{}, parent, child)
			if err := run(o, child.ID); !errors.Is(err, feature.ErrChildRelationshipClosed) {
				t.Fatalf("error = %v, want ErrChildRelationshipClosed", err)
			}
			loaded, err := store.Load(child.ID)
			if err != nil {
				t.Fatalf("load child: %v", err)
			}
			if loaded.AutomaticReviewMode != "" {
				t.Fatalf("closed child mutated: automatic review mode %q", loaded.AutomaticReviewMode)
			}
		})
	}
}

func TestEnableFeatureAutomaticReviewKeepsOtherAxes(t *testing.T) {
	f := opFeature("cfg-1", feature.StatusInterrupted, feature.PhasePlan)
	f.Models = config.ModelConfig{Implementation: "kept-model"}
	f.Inquireness = feature.InquirenessHigh
	var before, after feature.ConfigSnapshot
	o, store := newConfigOpFixture(t, Hooks{
		OnFeatureConfigChanged: func(_ string, b, a feature.ConfigSnapshot) { before, after = b, a },
	}, f)

	if err := o.EnableFeatureAutomaticReview(f.ID); err != nil {
		t.Fatalf("EnableFeatureAutomaticReview() error = %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if feature.NormalizeAutomaticReviewMode(loaded.AutomaticReviewMode) != feature.AutomaticReviewEnabled {
		t.Fatalf("automatic review mode = %q, want enabled", loaded.AutomaticReviewMode)
	}
	if loaded.Models.Implementation != "kept-model" || loaded.Inquireness != feature.InquirenessHigh {
		t.Fatalf("other axes changed: models %+v inquireness %q", loaded.Models, loaded.Inquireness)
	}
	if before.AutomaticReviewMode == after.AutomaticReviewMode || after.AutomaticReviewMode != feature.AutomaticReviewEnabled {
		t.Fatalf("audit before/after = %q/%q, want the enablement recorded", before.AutomaticReviewMode, after.AutomaticReviewMode)
	}
	if ids := configChangedIDs(o); !ids[f.ID] {
		t.Fatalf("config-changed events = %v, want %s", ids, f.ID)
	}
}

func TestRewindOperationGuards(t *testing.T) {
	guarded := func(f *feature.Feature) RewindInput {
		return RewindInput{SourceRunNumber: f.ActiveRun, SourceRevision: feature.RewindRevision(f)}
	}
	rewound := []string{traceLockAcquired, traceGuard, traceGuard, stepRewound, traceLockReleased}
	tests := []struct {
		name       string
		features   func() []*feature.Feature
		target     string
		input      func(f *feature.Feature) RewindInput
		wantErr    error
		wantStale  bool
		wantSource int
		wantSteps  []string
	}{
		{
			name:       "current revision is accepted",
			features:   func() []*feature.Feature { return []*feature.Feature{rewindFeature()} },
			target:     "rw-1",
			input:      guarded,
			wantSource: 3,
			wantSteps:  rewound,
		},
		{
			name:       "no revision runs unguarded from the active run",
			features:   func() []*feature.Feature { return []*feature.Feature{rewindFeature()} },
			target:     "rw-1",
			input:      func(*feature.Feature) RewindInput { return RewindInput{} },
			wantSource: 3,
			wantSteps:  rewound,
		},
		{
			name:     "stale revision is rejected before any side effect",
			features: func() []*feature.Feature { return []*feature.Feature{rewindFeature()} },
			target:   "rw-1",
			input: func(f *feature.Feature) RewindInput {
				return RewindInput{SourceRunNumber: f.ActiveRun, SourceRevision: "stale-revision-token"}
			},
			wantStale: true,
			wantSteps: []string{traceLockAcquired, traceLockReleased},
		},
		{
			name:     "changed active run is rejected before any side effect",
			features: func() []*feature.Feature { return []*feature.Feature{rewindFeature()} },
			target:   "rw-1",
			input: func(f *feature.Feature) RewindInput {
				in := guarded(f)
				in.SourceRunNumber = f.ActiveRun + 1
				return in
			},
			wantStale: true,
			wantSteps: []string{traceLockAcquired, traceLockReleased},
		},
		{
			name:      "unloadable feature is rejected before any side effect",
			features:  func() []*feature.Feature { return nil },
			target:    "rw-missing",
			input:     func(*feature.Feature) RewindInput { return RewindInput{SourceRevision: "some-revision"} },
			wantErr:   errors.New("loading feature for rewind guard"),
			wantSteps: []string{traceLockAcquired, traceLockReleased},
		},
		{
			name: "relationship guard runs after the preview guard",
			features: func() []*feature.Feature {
				parent, child := opParentWithChild()
				return []*feature.Feature{parent, child}
			},
			target:     "op-parent",
			input:      func(*feature.Feature) RewindInput { return RewindInput{} },
			wantErr:    feature.ErrParentMutationLocked,
			wantSource: 1,
			wantSteps:  []string{traceLockAcquired, traceGuard, traceLockReleased},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.features()...)
			var current *feature.Feature
			if loaded, err := fx.store.Load(tt.target); err == nil {
				current = loaded
			}
			result, err := fx.o.Rewind(tt.target, tt.input(current))
			switch {
			case tt.wantStale:
				var stale *StaleRewindPreviewError
				if !errors.Is(err, ErrStaleRewindPreview) || !errors.As(err, &stale) {
					t.Fatalf("Rewind() error = %v, want a stale-preview rejection", err)
				}
			case tt.wantErr != nil:
				if err == nil || (!errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error())) {
					t.Fatalf("Rewind() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Rewind() error = %v", err)
				}
				if result.EffectivePhase != feature.PhaseInquire || result.NewRunNumber != tt.wantSource {
					t.Fatalf("result = %+v, want the effective phase and new run", result)
				}
			}
			if result.SourceRunNumber != tt.wantSource {
				t.Fatalf("source run = %d, want %d", result.SourceRunNumber, tt.wantSource)
			}
			fx.assertSteps(tt.wantSteps...)
		})
	}
}

func rewindFeature() *feature.Feature {
	f := opFeature("rw-1", feature.StatusImplementing, feature.PhaseImplement)
	f.ActiveRun, f.RunCount = 3, 3
	return f
}

func TestAnswerQueuedHelp(t *testing.T) {
	withQueue := func(queue ...feature.HelpRequest) *feature.Feature {
		f := opFeature("help-1", feature.StatusImplementing, feature.PhaseImplement)
		f.HelpQueue = queue
		return f
	}
	tests := []struct {
		name      string
		feature   *feature.Feature
		wantFound bool
		wantQueue []feature.HelpRequest
	}{
		{
			name:      "answers the first pending entry",
			feature:   withQueue(feature.HelpRequest{Question: "old", Answer: "done"}, feature.HelpRequest{Question: "q1", Pending: true}, feature.HelpRequest{Question: "q2", Pending: true}),
			wantFound: true,
			wantQueue: []feature.HelpRequest{{Question: "old", Answer: "done"}, {Question: "q1", Answer: "reply"}, {Question: "q2", Pending: true}},
		},
		{
			name:      "reports nothing pending",
			feature:   withQueue(feature.HelpRequest{Question: "old", Answer: "done"}),
			wantQueue: []feature.HelpRequest{{Question: "old", Answer: "done"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newOpFixture(t, tt.feature)
			found, err := fx.o.AnswerQueuedHelp("help-1", "reply")
			if err != nil {
				t.Fatalf("AnswerQueuedHelp() error = %v", err)
			}
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			f, _ := fx.store.Load("help-1")
			if len(f.HelpQueue) != len(tt.wantQueue) {
				t.Fatalf("queue = %+v, want %+v", f.HelpQueue, tt.wantQueue)
			}
			for i, want := range tt.wantQueue {
				got := f.HelpQueue[i]
				if got.Question != want.Question || got.Answer != want.Answer || got.Pending != want.Pending {
					t.Fatalf("queue[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
	t.Run("missing feature fails", func(t *testing.T) {
		fx := newOpFixture(t)
		if _, err := fx.o.AnswerQueuedHelp("help-missing", "reply"); err == nil {
			t.Fatal("AnswerQueuedHelp() on a missing feature error = nil")
		}
	})
}

func TestPendingNeedUserInputGatePath(t *testing.T) {
	gated := opFeature("nui-1", feature.StatusNeedUserInput, feature.PhaseImplement)
	gated.PendingNeedUserInputPath = "/gates/need-user-input.md"
	open := opFeature("nui-2", feature.StatusImplementing, feature.PhaseImplement)
	fx := newOpFixture(t, gated, open)

	path, err := fx.o.PendingNeedUserInputGatePath("nui-1")
	if err != nil || path != "/gates/need-user-input.md" {
		t.Fatalf("PendingNeedUserInputGatePath() = %q, %v; want the gate path", path, err)
	}
	if _, err := fx.o.PendingNeedUserInputGatePath("nui-2"); err == nil {
		t.Fatal("PendingNeedUserInputGatePath() without an open gate error = nil")
	}
}
