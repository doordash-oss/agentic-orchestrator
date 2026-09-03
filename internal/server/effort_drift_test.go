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
	"encoding/json"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
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
			name: "provider-qualified drifted effort yields warning",
			f: &feature.Feature{
				ID:     "test",
				Models: config.ModelConfig{Implementation: "mock:sonnet"},
				Effort: config.EffortConfig{Implementation: "max"},
			},
			reg:      testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh}),
			wantLen:  1,
			wantCode: string(errcat.EffortCapabilityDrift),
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
			wantCode: string(errcat.EffortCapabilityDrift),
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
			wantCode: string(errcat.EffortCapabilityDrift),
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
					if w.Class != ErrorClass(errcat.ClassWarning) {
						t.Errorf("warning class = %q, want warning", w.Class)
					}
					if w.Remediation != nil && len(w.Remediation.Actions) != 0 {
						t.Errorf("warning remediation carries actions: %+v", w.Remediation)
					}
					if !strings.Contains(w.Summary, tc.f.Effort.Implementation) && !strings.Contains(w.Summary, tc.f.Effort.Review) {
						t.Errorf("warning summary does not name the effort: %q", w.Summary)
					}
				}
			}
		})
	}
}

type mockCatalogProvider struct {
	models []llm.ModelInfo
}

func (m *mockCatalogProvider) Name() string                   { return "mock" }
func (m *mockCatalogProvider) MatchesModel(model string) bool { return true }
func (m *mockCatalogProvider) DetectCLI() bool                { return true }
func (m *mockCatalogProvider) AvailableModels() []string      { return nil }
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

// TestEffortDriftWarningsRenderCanonicallyOnListAndDetailRoutes pins the
// wire contract: a drifted effort renders as one canonical warning-class
// effort_capability_drift error whose summary names the role, effort, and
// model, on both the feature list summary and the feature detail route.
func TestEffortDriftWarningsRenderCanonicallyOnListAndDetailRoutes(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	if err := store.Modify(f.ID, func(ff *feature.Feature) error {
		ff.Models.Implementation = "sonnet"
		ff.Effort.Implementation = "max"
		return nil
	}); err != nil {
		t.Fatalf("drift the implementation effort: %v", err)
	}
	opts := baseReadHandlerOptions(store)
	opts.Registry = testRegistryWithCaps("sonnet", []llm.EffortLevel{llm.EffortLow, llm.EffortMedium, llm.EffortHigh})
	handler := NewHandler(opts)

	assertDriftWarning := func(t *testing.T, warning Error) {
		t.Helper()
		if warning.Code != string(errcat.EffortCapabilityDrift) {
			t.Fatalf("warning code = %q; want %q", warning.Code, errcat.EffortCapabilityDrift)
		}
		if warning.Class != ErrorClass(errcat.ClassWarning) {
			t.Fatalf("warning class = %q; want warning", warning.Class)
		}
		if warning.Remediation != nil && len(warning.Remediation.Actions) != 0 {
			t.Fatalf("warning remediation carries actions: %+v", warning.Remediation)
		}
		for _, want := range []string{"implementation", "max", "sonnet"} {
			if !strings.Contains(warning.Summary, want) {
				t.Fatalf("warning summary = %q; want it to name %q", warning.Summary, want)
			}
		}
	}

	list := getJSONMap(t, handler, apiPathFeatures)
	features := list["features"].([]any)
	var summary map[string]any
	for _, entry := range features {
		if candidate := entry.(map[string]any); candidate["id"] == f.ID {
			summary = candidate
			break
		}
	}
	if summary == nil {
		t.Fatalf("list does not carry feature %q: %#v", f.ID, list)
	}
	warnings := summary["warnings"].([]any)
	if len(warnings) != 1 {
		t.Fatalf("summary warnings = %#v; want exactly one drift warning", warnings)
	}
	var listWarning Error
	if err := json.Unmarshal([]byte(mustMarshalJSON(t, warnings[0])), &listWarning); err != nil {
		t.Fatalf("decode list warning: %v", err)
	}
	assertDriftWarning(t, listWarning)

	detail := getJSONMap(t, handler, "/api/v1/features/"+f.ID)[entityFeature].(map[string]any)
	detailWarnings := detail["warnings"].([]any)
	if len(detailWarnings) != 1 {
		t.Fatalf("detail warnings = %#v; want exactly one drift warning", detailWarnings)
	}
	var detailWarning Error
	if err := json.Unmarshal([]byte(mustMarshalJSON(t, detailWarnings[0])), &detailWarning); err != nil {
		t.Fatalf("decode detail warning: %v", err)
	}
	assertDriftWarning(t, detailWarning)
}
