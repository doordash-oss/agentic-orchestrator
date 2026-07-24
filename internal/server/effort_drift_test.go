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

package server

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestEffortDriftWarnings(t *testing.T) {
	cases := []struct {
		name     string
		f        *feature.Feature
		reg      *llm.Registry
		wantLen  int
		wantCode string
	}{
		{
			name:    "nil feature yields no warnings",
			f:       nil,
			reg:     nil,
			wantLen: 0,
		},
		{
			name: "nil registry yields no warnings",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet"},
				Effort: config.EffortConfig{Implementation: "max"},
			},
			reg:     nil,
			wantLen: 0,
		},
		{
			name: "auto effort yields no warnings",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet"},
				Effort: config.EffortConfig{Implementation: "auto"},
			},
			reg:     testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantLen: 0,
		},
		{
			name: "empty effort yields no warnings",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet"},
				Effort: config.EffortConfig{},
			},
			reg:     testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantLen: 0,
		},
		{
			name: "supported explicit effort yields no warnings",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet"},
				Effort: config.EffortConfig{Implementation: "high"},
			},
			reg:     testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantLen: 0,
		},
		{
			name: "drifted effort yields warning",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet"},
				Effort: config.EffortConfig{Implementation: "max"},
			},
			reg:      testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantLen:  1,
			wantCode: effortDriftWarningCode,
		},
		{
			name: "multiple drifted roles yield multiple warnings",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "sonnet", Review: "opus"},
				Effort: config.EffortConfig{Implementation: "max", Review: "max"},
			},
			reg: func() *llm.Registry {
				reg := llm.NewRegistry()
				prov := &mockCatalogProvider{
					models: []llm.ModelInfo{
						{ID: "sonnet", EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}},
						{ID: "opus", EffortCapabilities: []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}},
					},
				}
				reg.Register(prov)
				return reg
			}(),
			wantLen:  2,
			wantCode: effortDriftWarningCode,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := effortDriftWarnings(tc.f, tc.reg)
			if len(warnings) != tc.wantLen {
				t.Fatalf("got %d warnings, want %d: %+v", len(warnings), tc.wantLen, warnings)
			}
			if tc.wantLen > 0 && tc.wantCode != "" {
				for _, w := range warnings {
					if w.Code != tc.wantCode {
						t.Errorf("warning code = %q, want %q", w.Code, tc.wantCode)
					}
					if w.FeatureID != tc.f.ID {
						t.Errorf("warning feature_id = %q, want %q", w.FeatureID, tc.f.ID)
					}
				}
			}
		})
	}
}

type mockCatalogProvider struct {
	models []llm.ModelInfo
}

func (m *mockCatalogProvider) Name() string                          { return "mock" }
func (m *mockCatalogProvider) MatchesModel(model string) bool        { return true }
func (m *mockCatalogProvider) DetectCLI() bool                       { return true }
func (m *mockCatalogProvider) AvailableModels() []string             { return nil }
func (m *mockCatalogProvider) BuildCommand(opts llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (m *mockCatalogProvider) NewProtocol(opts llm.ProtocolOpts) llm.Protocol { return nil }
func (m *mockCatalogProvider) InstallHint() string                            { return "" }
func (m *mockCatalogProvider) VersionInfo() (string, error)                   { return "1.0.0", nil }
func (m *mockCatalogProvider) MinVersion() [3]int                             { return [3]int{} }
func (m *mockCatalogProvider) EnvVarsToExclude() []string                     { return nil }
func (m *mockCatalogProvider) ModelCatalog() []llm.ModelInfo                  { return m.models }

func testRegistryWithCaps(model string, caps []llm.EffortLevel) *llm.Registry {
	reg := llm.NewRegistry()
	prov := &mockCatalogProvider{
		models: []llm.ModelInfo{
			{ID: model, EffortCapabilities: caps},
		},
	}
	reg.Register(prov)
	return reg
}
