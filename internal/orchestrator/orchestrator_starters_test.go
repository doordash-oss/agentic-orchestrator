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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ---------------------------------------------------------------------------
// TestOrchestrator_StartPhase_AllPhaseTypes (table-driven)
// ---------------------------------------------------------------------------
//
// Tests the startPhase dispatcher for each of the 7 phase-runner phases. We
// drive dispatch via StartFeature by setting Status=StatusInterrupted and
// CurrentPhase=X (StartFeature uses CurrentPhase for non-StatusCreated features).
// Publish dispatch is covered in TestOrchestrator_StartPublish_* tests.
func TestOrchestrator_StartPhase_AllPhaseTypes(t *testing.T) {
	planPath := writeTempFile(t, "plan.md", "plan body")
	inquirePath := writeTempFile(t, "inquire.md", "inquire body")
	researchPath := writeTempFile(t, "research.md", "research body")

	tests := []struct {
		name              string
		phase             feature.Phase
		setupFeature      func(f *feature.Feature)
		wantTransition    string
		wantPhaseStarted  bool
		wantDispatchPhase feature.Phase
		wantError         bool
	}{
		{
			name:              "KB_NoReposSkipsToInquire",
			phase:             feature.PhaseKnowledgeBase,
			setupFeature:      func(f *feature.Feature) { f.Repos = nil },
			wantTransition:    "StartInquire",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhaseInquire,
		},
		{
			name:              "Inquire",
			phase:             feature.PhaseInquire,
			setupFeature:      func(f *feature.Feature) {},
			wantTransition:    "StartInquire",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhaseInquire,
		},
		{
			name:  "Research",
			phase: feature.PhaseResearch,
			// CurrentPhase==PhaseResearch is the iota-0 zero value. StartFeature
			// treats `StatusInterrupted + CurrentPhase==0 + StartedAt nil` as
			// a corrupted/missing-current_phase case and falls back to
			// Pipeline.FirstPhase(). Set StartedAt so the Research case models
			// a legitimate interrupted-mid-Research resume, not a corrupted YAML.
			setupFeature: func(f *feature.Feature) {
				started := time.Now().Add(-time.Hour)
				f.StartedAt = &started
				f.Artifacts = map[string]string{"inquire": inquirePath}
			},
			wantTransition:    "StartResearch",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhaseResearch,
		},
		{
			name:  "Design",
			phase: feature.PhaseDesign,
			setupFeature: func(f *feature.Feature) {
				f.Artifacts = map[string]string{"research": researchPath}
			},
			wantTransition:    "StartDesign",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhaseDesign,
		},
		{
			name:  "Plan_Large_NoArtifacts_Fails",
			phase: feature.PhasePlan,
			setupFeature: func(f *feature.Feature) {
				// No design/research → error on large pipeline.
			},
			wantTransition:   "StartPlanning",
			wantPhaseStarted: false,
			wantError:        true,
		},
		{
			name:  "Plan_Medium_EmptyArtifactOK",
			phase: feature.PhasePlan,
			setupFeature: func(f *feature.Feature) {
				f.Pipeline = feature.PipelineMedium
			},
			wantTransition:    "StartPlanning",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhasePlan,
		},
		{
			name:  "Implement",
			phase: feature.PhaseImplement,
			setupFeature: func(f *feature.Feature) {
				f.Artifacts = map[string]string{"plan": planPath}
				if len(f.Repos) == 0 {
					f.Repos = []feature.FeatureRepo{{Name: "test-repo", Path: "/tmp/test-repo"}}
				}
				writeExecOrderNextToPlan(t, planPath, f.Repos)
			},
			wantTransition:    "StartImplementation",
			wantPhaseStarted:  true,
			wantDispatchPhase: feature.PhaseImplement,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &feature.Feature{
				ID:           "feat-dispatch",
				Status:       feature.StatusInterrupted, // use CurrentPhase path
				CurrentPhase: tt.phase,
				Pipeline:     feature.PipelineLarge,
			}
			if tt.setupFeature != nil {
				tt.setupFeature(f)
			}

			lc := lifecycleForFeature(f)
			// Implement path delegates to StartMultiRepoImplementation which
			// requires StatusImplementing; wire the transition explicitly.
			if tt.phase == feature.PhaseImplement {
				lc.StartImplementationFn = func(id string) error {
					f.Status = feature.StatusImplementing
					return nil
				}
				lc.InitRepoImplFn = func(id string) error { return nil }
			}
			fs := newFeatureStore(f)

			var startedPhase feature.Phase
			var startedCalled bool
			o := orchestrator.New(orchestrator.Deps{
				Lifecycle: lc,
				Store:     fs,
			}, orchestrator.Hooks{
				OnPhaseStarted: func(id string, p feature.Phase) {
					startedCalled = true
					startedPhase = p
				},
			})
			// No-op engine seam for Implement so the dispatch goroutine doesn't
			// error on missing PhaseRunner.
			if tt.phase == feature.PhaseImplement {
				o.SetRunMultiRepoImplFn(noopRunMultiRepoImplFn())
			}
			if tt.phase == feature.PhaseResearch {
				o.SetRunResearchLoopFn(func(f *feature.Feature, questionsPath string, kbInfos ...agent.KBInfo) (chan *agent.BlockingLoopResult, error) {
					return nil, nil
				})
			}
			if tt.phase == feature.PhaseInquire {
				o.SetRunInquireLoopFn(func(f *feature.Feature, kbInfos ...agent.KBInfo) (chan *agent.BlockingLoopResult, error) {
					return nil, nil
				})
			}
			if tt.phase == feature.PhaseDesign {
				o.SetRunDesignLoopFn(func(f *feature.Feature, researchPath string, qaFilePaths []string, kbInfos ...agent.KBInfo) (chan *agent.BlockingLoopResult, error) {
					return nil, nil
				})
			}

			err := o.StartFeature("feat-dispatch")

			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else if err != nil {
				t.Fatalf("StartFeature: unexpected error: %v", err)
			}

			if tt.wantTransition != "" {
				assertLifecycleCall(t, lc, tt.wantTransition)
			}

			events := drainEvents(o)
			if tt.wantPhaseStarted {
				if !startedCalled {
					t.Error("expected OnPhaseStarted hook to fire")
				}
				if startedPhase != tt.wantDispatchPhase {
					t.Errorf("OnPhaseStarted phase = %v, want %v", startedPhase, tt.wantDispatchPhase)
				}
				if hasPhaseStarted(events, tt.wantDispatchPhase) == nil {
					t.Errorf("expected PhaseStarted event for %v; got events with phases: %v", tt.wantDispatchPhase, eventPhases(events))
				}
			} else {
				if startedCalled {
					t.Error("expected no OnPhaseStarted hook to fire on error")
				}
				if hasPhaseStarted(events, tt.phase) != nil {
					t.Error("no PhaseStarted event should be emitted on error")
				}
			}
		})
	}
}

// eventPhases extracts phases from PhaseStarted events for diagnostic output.
func eventPhases(events []ports.Event) []feature.Phase {
	var out []feature.Phase
	for _, e := range events {
		if e.Type == ports.PhaseStarted {
			out = append(out, e.Phase)
		}
	}
	return out
}
