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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestResolveEffortForRole(t *testing.T) {
	// When the PhaseRunner has no Registry (as in this unit test),
	// capabilities are nil and explicit values fall back to Auto — that is
	// the correct "Auto-only" behavior for models without effort control.
	// The capability-aware path is covered by llm.ResolveEffortFromString
	// tests.
	cases := []struct {
		name       string
		pipeline   feature.PipelineProfile
		effort     config.EffortConfig
		role       llm.PhaseRole
		model      string
		wantEffort llm.EffortLevel
		wantSource llm.EffortSource
	}{
		{
			name:       "auto uses feature pipeline medium",
			pipeline:   feature.PipelineMedium,
			effort:     config.EffortConfig{},
			role:       llm.PhasePlanning,
			model:      "claude-sonnet",
			wantEffort: llm.EffortMedium,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "auto uses feature pipeline large",
			pipeline: feature.PipelineLarge,
			effort: config.EffortConfig{
				Planning: "auto",
			},
			role:       llm.PhasePlanning,
			model:      "claude-sonnet",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "explicit drift falls back to pipeline when no caps",
			pipeline: feature.PipelineLarge,
			effort: config.EffortConfig{
				Planning: "max",
			},
			role:       llm.PhasePlanning,
			model:      "claude-sonnet",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:       "auto uses feature pipeline moonshot",
			pipeline:   feature.PipelineMoonshot,
			effort:     config.EffortConfig{},
			role:       llm.PhaseImplementation,
			model:      "claude-sonnet",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "utilities auto always low",
			pipeline: feature.PipelineMoonshot,
			effort: config.EffortConfig{
				Utilities: "auto",
			},
			role:       llm.PhaseChat,
			model:      "claude-haiku",
			wantEffort: llm.EffortLow,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "utilities explicit falls to pipeline when no caps",
			pipeline: feature.PipelineMoonshot,
			effort: config.EffortConfig{
				Utilities: "medium",
			},
			role:       llm.PhaseChat,
			model:      "claude-haiku",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "review explicit falls to pipeline when no caps",
			pipeline: feature.PipelineMoonshot,
			effort: config.EffortConfig{
				Review: "max",
			},
			role:       llm.PhaseReview,
			model:      "claude-opus",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:     "implementation drift falls to pipeline",
			pipeline: feature.PipelineMoonshot,
			effort: config.EffortConfig{
				Implementation: "max",
			},
			role:       llm.PhaseImplementation,
			model:      "claude-sonnet",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
		{
			name:       "unknown model no caps falls to pipeline",
			pipeline:   feature.PipelineMoonshot,
			effort:     config.EffortConfig{},
			role:       llm.PhaseImplementation,
			model:      "unknown-model",
			wantEffort: llm.EffortHigh,
			wantSource: llm.EffortSourceAuto,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := &PhaseRunner{
				BuildSessionFn: func(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
					return nil, nil, nil, nil
				},
			}
			f := &feature.Feature{
				ID:       "test-feature",
				Pipeline: tc.pipeline,
				Effort:   tc.effort,
			}
			gotEffort, gotSource := pr.resolveEffortForRole(f, tc.role, tc.model)
			if gotEffort != tc.wantEffort {
				t.Errorf("effort = %q, want %q", gotEffort, tc.wantEffort)
			}
			if gotSource != tc.wantSource {
				t.Errorf("source = %q, want %q", gotSource, tc.wantSource)
			}
		})
	}
}

func TestValidatorEffortLevel(t *testing.T) {
	cases := []struct {
		name       string
		cfg        PlanLoopConfig
		wantEffort llm.EffortLevel
	}{
		{
			name: "uses validator effort when set",
			cfg: PlanLoopConfig{
				EffortLevel:              llm.EffortHigh,
				EffectiveEffort:          llm.EffortHigh,
				ValidatorEffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to planner effort when validator not set",
			cfg: PlanLoopConfig{
				EffortLevel:     llm.EffortHigh,
				EffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to pipeline effort when neither set",
			cfg: PlanLoopConfig{
				EffortLevel: llm.EffortHigh,
			},
			wantEffort: llm.EffortHigh,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validatorEffortLevel(tc.cfg)
			if got != tc.wantEffort {
				t.Errorf("validatorEffortLevel = %q, want %q", got, tc.wantEffort)
			}
		})
	}
}

func TestReviewHelperEffortFromImpl(t *testing.T) {
	cases := []struct {
		name       string
		cfg        ImplementConfig
		wantEffort llm.EffortLevel
	}{
		{
			name: "uses review effort when set",
			cfg: ImplementConfig{
				EffortLevel:           llm.EffortHigh,
				EffectiveEffort:       llm.EffortHigh,
				ReviewEffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to impl effort when review not set",
			cfg: ImplementConfig{
				EffortLevel:     llm.EffortHigh,
				EffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to pipeline effort when neither set",
			cfg: ImplementConfig{
				EffortLevel: llm.EffortHigh,
			},
			wantEffort: llm.EffortHigh,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewHelperEffortFromImpl(tc.cfg)
			if got != tc.wantEffort {
				t.Errorf("reviewHelperEffortFromImpl = %q, want %q", got, tc.wantEffort)
			}
		})
	}
}

func TestFinalReviewEffortRouting(t *testing.T) {
	cases := []struct {
		name           string
		cfg            OrchestratorConfig
		wantAxisEffort llm.EffortLevel
		wantAxisSource llm.EffortSource
		wantFixEffort  llm.EffortLevel
		wantFixSource  llm.EffortSource
	}{
		{
			name: "review axes use review effort, fix uses impl effort",
			cfg: OrchestratorConfig{
				EffortLevel:           llm.EffortHigh,
				EffectiveEffort:       llm.EffortHigh,
				ImplEffectiveEffort:   llm.EffortMedium,
				ImplEffortSource:      llm.EffortSourceExplicit,
				ReviewEffectiveEffort: llm.EffortMax,
				ReviewEffortSource:    llm.EffortSourceExplicit,
			},
			wantAxisEffort: llm.EffortMax,
			wantAxisSource: llm.EffortSourceExplicit,
			wantFixEffort:  llm.EffortMedium,
			wantFixSource:  llm.EffortSourceExplicit,
		},
		{
			name: "falls back to effective effort when role-specific not set",
			cfg: OrchestratorConfig{
				EffortLevel:     llm.EffortHigh,
				EffectiveEffort: llm.EffortMedium,
				EffortSource:    llm.EffortSourceAuto,
			},
			wantAxisEffort: llm.EffortMedium,
			wantAxisSource: llm.EffortSourceAuto,
			wantFixEffort:  llm.EffortMedium,
			wantFixSource:  llm.EffortSourceAuto,
		},
		{
			name: "falls back to pipeline when no effort resolved",
			cfg: OrchestratorConfig{
				EffortLevel: llm.EffortHigh,
			},
			wantAxisEffort: llm.EffortHigh,
			wantAxisSource: "",
			wantFixEffort:  llm.EffortHigh,
			wantFixSource:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAxisEffort := finalReviewAxisEffortLevel(tc.cfg)
			if gotAxisEffort != tc.wantAxisEffort {
				t.Errorf("axis effort = %q, want %q", gotAxisEffort, tc.wantAxisEffort)
			}
			gotAxisSource := finalReviewAxisEffortSource(tc.cfg)
			if gotAxisSource != tc.wantAxisSource {
				t.Errorf("axis source = %q, want %q", gotAxisSource, tc.wantAxisSource)
			}
			gotFixEffort := finalReviewFixEffortLevel(tc.cfg)
			if gotFixEffort != tc.wantFixEffort {
				t.Errorf("fix effort = %q, want %q", gotFixEffort, tc.wantFixEffort)
			}
			gotFixSource := finalReviewFixEffortSource(tc.cfg)
			if gotFixSource != tc.wantFixSource {
				t.Errorf("fix source = %q, want %q", gotFixSource, tc.wantFixSource)
			}
		})
	}
}
