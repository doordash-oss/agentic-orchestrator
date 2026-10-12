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
	"context"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestEffortCapabilitiesForProviderQualifiedModel(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&effortCatalogProvider{
		models: []llm.ModelInfo{{
			ID:                 "sonnet",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh},
		}},
	})

	got := (&PhaseRunner{Registry: reg}).effortCapabilitiesForModel("mock:sonnet")
	if len(got) != 3 || got[0] != llm.EffortLow || got[1] != llm.EffortMedium || got[2] != llm.EffortHigh {
		t.Fatalf("capabilities = %v, want [low medium high]", got)
	}
}

type effortCatalogProvider struct {
	models []llm.ModelInfo
}

func (p *effortCatalogProvider) Name() string                   { return "mock" }
func (p *effortCatalogProvider) MatchesModel(model string) bool { return true }
func (p *effortCatalogProvider) DetectCLI() bool                { return true }
func (p *effortCatalogProvider) AvailableModels() []string      { return nil }
func (p *effortCatalogProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (p *effortCatalogProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol { return nil }
func (p *effortCatalogProvider) InstallHint() string                            { return "" }
func (p *effortCatalogProvider) VersionInfo() (string, error)                   { return "1.0.0", nil }
func (p *effortCatalogProvider) MinVersion() [3]int                             { return [3]int{} }
func (p *effortCatalogProvider) EnvVarsToExclude() []string                     { return nil }
func (p *effortCatalogProvider) ModelCatalog() []llm.ModelInfo                  { return p.models }
func (p *effortCatalogProvider) SetModelCatalog(models []llm.ModelInfo)         { p.models = models }
func (p *effortCatalogProvider) DiscoverModelCatalog(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *effortCatalogProvider) DiscoverModelCatalogWithProgress(context.Context, llm.ModelDiscoveryReporter) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (p *effortCatalogProvider) ContextWindowForModel(string) int { return 0 }
func (p *effortCatalogProvider) AskingQuestionsClause() string    { return "" }
func (p *effortCatalogProvider) SupportsNativeToollessReview() bool {
	return false
}
func (p *effortCatalogProvider) CheckReadiness(context.Context) llm.ProviderReadiness {
	return llm.ProviderReadiness{Ready: true}
}
func (p *effortCatalogProvider) ReviewPreferenceBand(llm.ModelInfo) (int, bool) { return 0, false }

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
		cfg        planLoopConfig
		wantEffort llm.EffortLevel
	}{
		{
			name: "uses validator effort when set",
			cfg: planLoopConfig{
				EffortLevel:              llm.EffortHigh,
				EffectiveEffort:          llm.EffortHigh,
				ValidatorEffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to planner effort when validator not set",
			cfg: planLoopConfig{
				EffortLevel:     llm.EffortHigh,
				EffectiveEffort: llm.EffortMedium,
			},
			wantEffort: llm.EffortMedium,
		},
		{
			name: "falls back to pipeline effort when neither set",
			cfg: planLoopConfig{
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

func TestMultiRepoOrchestratorConfigEffortRouting(t *testing.T) {
	reg := llm.NewRegistry()
	reg.Register(&effortCatalogProvider{
		models: []llm.ModelInfo{{
			ID:                 "sonnet",
			EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh},
		}},
	})
	pr := &PhaseRunner{Registry: reg}
	f := &feature.Feature{
		ID:       "test-feature",
		Pipeline: feature.PipelineMedium,
		Models:   config.ModelConfig{Implementation: "mock:sonnet", Review: "mock:sonnet"},
		Effort:   config.EffortConfig{Implementation: "high"},
	}

	cases := []struct {
		name       string
		passRole   llm.PhaseRole
		wantEffort llm.EffortLevel
		wantSource llm.EffortSource
	}{
		{name: "implementation pass uses implementation effort", passRole: llm.PhaseImplementation, wantEffort: llm.EffortHigh, wantSource: llm.EffortSourceExplicit},
		{name: "final review pass uses review effort", passRole: llm.PhaseReview, wantEffort: llm.EffortMedium, wantSource: llm.EffortSourceAuto},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := pr.multiRepoOrchestratorConfig(f, tc.passRole, nil)
			if cfg.EffectiveEffort != tc.wantEffort || cfg.EffortSource != tc.wantSource {
				t.Errorf("effective effort = %q/%q, want %q/%q", cfg.EffectiveEffort, cfg.EffortSource, tc.wantEffort, tc.wantSource)
			}
			if cfg.ImplEffectiveEffort != llm.EffortHigh || cfg.ImplEffortSource != llm.EffortSourceExplicit {
				t.Errorf("impl effort = %q/%q, want high/explicit", cfg.ImplEffectiveEffort, cfg.ImplEffortSource)
			}
			if cfg.ReviewEffectiveEffort != llm.EffortMedium || cfg.ReviewEffortSource != llm.EffortSourceAuto {
				t.Errorf("review effort = %q/%q, want medium/auto", cfg.ReviewEffectiveEffort, cfg.ReviewEffortSource)
			}
			if cfg.EffortLevel != llm.EffortMedium {
				t.Errorf("pipeline effort = %q, want medium", cfg.EffortLevel)
			}
		})
	}
}
