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

package llm

import (
	"slices"
	"testing"
)

func TestMostCapableFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		catalog []ModelInfo
		wantID  string
		wantOK  bool
	}{
		{name: "empty", wantOK: false},
		{
			name: "highest category wins",
			catalog: []ModelInfo{
				{ID: "cheap", Category: "cheap", ContextWindow: 400_000},
				{ID: "capable", Category: "capable", ContextWindow: 100_000},
			},
			wantID: "capable",
			wantOK: true,
		},
		{
			name: "context breaks category tie",
			catalog: []ModelInfo{
				{ID: "small", Category: "capable", ContextWindow: 100_000},
				{ID: "large", Category: "capable", ContextWindow: 400_000},
			},
			wantID: "large",
			wantOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := mostCapableFrom(tc.catalog)
			if ok != tc.wantOK {
				t.Fatalf("mostCapableFrom() ok = %v, want %v", ok, tc.wantOK)
			}
			if got.ID != tc.wantID {
				t.Errorf("mostCapableFrom() ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

func TestBalancedFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		catalog []ModelInfo
		wantID  string
		wantOK  bool
	}{
		{name: "empty", wantOK: false},
		{
			name: "exact balanced model",
			catalog: []ModelInfo{
				{ID: "capable", Category: "capable"},
				{ID: "balanced", Category: "balanced"},
			},
			wantID: "balanced",
			wantOK: true,
		},
		{
			name: "closest ranked fallback preserves order",
			catalog: []ModelInfo{
				{ID: "capable", Category: "capable"},
				{ID: "cheap", Category: "cheap"},
			},
			wantID: "capable",
			wantOK: true,
		},
		{
			name:    "unranked models are ignored",
			catalog: []ModelInfo{{ID: "preview", Category: "preview"}},
			wantOK:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := balancedFrom(tc.catalog)
			if ok != tc.wantOK {
				t.Fatalf("balancedFrom() ok = %v, want %v", ok, tc.wantOK)
			}
			if got.ID != tc.wantID {
				t.Errorf("balancedFrom() ID = %q, want %q", got.ID, tc.wantID)
			}
		})
	}
}

func TestRegistryAllModels(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.Register(&internalCatalogProvider{
		internalProvider: internalProvider{name: "first", models: []string{"capable"}, detected: true},
		catalog:          []ModelInfo{{ID: "capable", Category: "capable"}},
	})
	r.Register(&internalProvider{name: "second", models: []string{"synthetic"}, detected: true})
	r.Register(&internalProvider{name: "offline", models: []string{"hidden"}, detected: false})

	models := r.allModels()
	ids := make([]string, len(models))
	for i, model := range models {
		ids[i] = model.ID
	}
	if want := []string{"capable", "synthetic"}; !slices.Equal(ids, want) {
		t.Fatalf("allModels() IDs = %v, want %v", ids, want)
	}
	if models[1].Category != "" || models[1].ContextWindow != 0 {
		t.Errorf("synthetic model metadata = %+v, want zero values", models[1])
	}
}

type internalProvider struct {
	name     string
	models   []string
	detected bool
}

func (p *internalProvider) Name() string              { return p.name }
func (p *internalProvider) MatchesModel(string) bool  { return false }
func (p *internalProvider) DetectCLI() bool           { return p.detected }
func (p *internalProvider) AvailableModels() []string { return p.models }
func (p *internalProvider) BuildCommand(CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (p *internalProvider) NewProtocol(ProtocolOpts) Protocol { return nil }
func (p *internalProvider) InstallHint() string               { return "" }
func (p *internalProvider) VersionInfo() (string, error)      { return "", nil }
func (p *internalProvider) MinVersion() [3]int                { return [3]int{} }
func (p *internalProvider) EnvVarsToExclude() []string        { return nil }

type internalCatalogProvider struct {
	internalProvider
	catalog []ModelInfo
}

func (p *internalCatalogProvider) ModelCatalog() []ModelInfo { return p.catalog }
