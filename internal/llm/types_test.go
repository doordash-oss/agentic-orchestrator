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

package llm_test

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// helper catalogs used across tests.
var (
	capableModel = llm.ModelInfo{
		ID: "opus", DisplayName: "Opus", ContextWindow: 200000, Category: "capable",
	}
	balancedModel = llm.ModelInfo{
		ID: "sonnet", DisplayName: "Sonnet", ContextWindow: 200000, Category: "balanced",
	}
	cheapModel = llm.ModelInfo{
		ID: "haiku", DisplayName: "Haiku", ContextWindow: 200000, Category: "cheap",
	}
	unrankedModel = llm.ModelInfo{
		ID: "experimental", DisplayName: "Experimental", ContextWindow: 100000, Category: "preview",
	}
)

func TestModelsByCategory(t *testing.T) {
	tests := []struct {
		name     string
		catalog  []llm.ModelInfo
		category string
		wantIDs  []string
	}{
		{
			name: "returns matching models from mixed catalog",
			catalog: []llm.ModelInfo{
				capableModel,
				balancedModel,
				cheapModel,
				{ID: "opus2", DisplayName: "Opus 2", ContextWindow: 400000, Category: "capable"},
			},
			category: "capable",
			wantIDs:  []string{"opus", "opus2"},
		},
		{
			name:     "returns empty slice for unknown category",
			catalog:  []llm.ModelInfo{capableModel, balancedModel, cheapModel},
			category: "unknown",
			wantIDs:  nil,
		},
		{
			name:     "handles empty catalog",
			catalog:  nil,
			category: "capable",
			wantIDs:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := llm.ModelsByCategory(tt.catalog, tt.category)
			gotIDs := modelIDs(got)
			if !stringSliceEqual(gotIDs, tt.wantIDs) {
				t.Errorf("ModelsByCategory() IDs = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestMostCapableFrom(t *testing.T) {
	tests := []struct {
		name    string
		catalog []llm.ModelInfo
		wantID  string
		wantOK  bool
	}{
		{
			name:    "selects capable category model",
			catalog: []llm.ModelInfo{cheapModel, balancedModel, capableModel},
			wantID:  "opus",
			wantOK:  true,
		},
		{
			name: "breaks ties by context window (larger wins)",
			catalog: []llm.ModelInfo{
				{ID: "opus-small", DisplayName: "Opus Small", ContextWindow: 100000, Category: "capable"},
				{ID: "opus-large", DisplayName: "Opus Large", ContextWindow: 400000, Category: "capable"},
			},
			wantID: "opus-large",
			wantOK: true,
		},
		{
			name:    "handles catalog with single entry",
			catalog: []llm.ModelInfo{cheapModel},
			wantID:  "haiku",
			wantOK:  true,
		},
		{
			name:    "returns false for empty catalog",
			catalog: nil,
			wantID:  "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := llm.MostCapableFrom(tt.catalog)
			if ok != tt.wantOK {
				t.Fatalf("MostCapableFrom() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.wantID {
				t.Errorf("MostCapableFrom() ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestCheapestFrom(t *testing.T) {
	tests := []struct {
		name    string
		catalog []llm.ModelInfo
		wantID  string
		wantOK  bool
	}{
		{
			name:    "selects cheap category model",
			catalog: []llm.ModelInfo{capableModel, balancedModel, cheapModel},
			wantID:  "haiku",
			wantOK:  true,
		},
		{
			name: "handles catalog with no cheap models (returns lowest ranked)",
			catalog: []llm.ModelInfo{
				capableModel,
				balancedModel,
			},
			wantID: "sonnet",
			wantOK: true,
		},
		{
			name:    "skips unranked models",
			catalog: []llm.ModelInfo{unrankedModel, cheapModel},
			wantID:  "haiku",
			wantOK:  true,
		},
		{
			name:    "returns false for empty catalog",
			catalog: nil,
			wantID:  "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := llm.CheapestFrom(tt.catalog)
			if ok != tt.wantOK {
				t.Fatalf("CheapestFrom() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.wantID {
				t.Errorf("CheapestFrom() ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestBalancedFrom(t *testing.T) {
	tests := []struct {
		name    string
		catalog []llm.ModelInfo
		wantID  string
		wantOK  bool
	}{
		{
			name:    "selects balanced category model",
			catalog: []llm.ModelInfo{capableModel, balancedModel, cheapModel},
			wantID:  "sonnet",
			wantOK:  true,
		},
		{
			name: "falls back to closest rank when no balanced model exists",
			catalog: []llm.ModelInfo{
				capableModel, // rank 3, distance 1
				cheapModel,   // rank 1, distance 1
			},
			// Both have distance 1 from rank 2; the first encountered wins.
			wantID: "opus",
			wantOK: true,
		},
		{
			name:    "returns false for empty catalog",
			catalog: nil,
			wantID:  "",
			wantOK:  false,
		},
		{
			name:    "returns false for all-unranked catalog",
			catalog: []llm.ModelInfo{unrankedModel},
			wantID:  "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := llm.BalancedFrom(tt.catalog)
			if ok != tt.wantOK {
				t.Fatalf("BalancedFrom() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.wantID {
				t.Errorf("BalancedFrom() ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestLargestContextFrom(t *testing.T) {
	tests := []struct {
		name    string
		catalog []llm.ModelInfo
		wantID  string
		wantOK  bool
	}{
		{
			name: "selects model with largest ContextWindow",
			catalog: []llm.ModelInfo{
				{ID: "small", DisplayName: "Small", ContextWindow: 100000, Category: "capable"},
				{ID: "large", DisplayName: "Large", ContextWindow: 400000, Category: "cheap"},
				{ID: "medium", DisplayName: "Medium", ContextWindow: 200000, Category: "balanced"},
			},
			wantID: "large",
			wantOK: true,
		},
		{
			name: "breaks ties by category rank (prefer more capable)",
			catalog: []llm.ModelInfo{
				{ID: "cheap-200k", DisplayName: "Cheap 200k", ContextWindow: 200000, Category: "cheap"},
				{ID: "capable-200k", DisplayName: "Capable 200k", ContextWindow: 200000, Category: "capable"},
			},
			wantID: "capable-200k",
			wantOK: true,
		},
		{
			name:    "returns false for empty catalog",
			catalog: nil,
			wantID:  "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := llm.LargestContextFrom(tt.catalog)
			if ok != tt.wantOK {
				t.Fatalf("LargestContextFrom() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.ID != tt.wantID {
				t.Errorf("LargestContextFrom() ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

// --- test helpers ---

func modelIDs(models []llm.ModelInfo) []string {
	if len(models) == 0 {
		return nil
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	return ids
}

func stringSliceEqual(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
