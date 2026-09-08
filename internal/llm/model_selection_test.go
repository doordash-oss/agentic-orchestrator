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
	"slices"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func testBool(value bool) *bool { return &value }

func TestRecommendationsSeparateEligibilityQualityAndPrice(t *testing.T) {
	r := llm.NewRegistry()
	p := &stubCatalogProvider{stubProvider: stubProvider{name: "opencode", hasCLI: true}, catalog: []llm.ModelInfo{
		{ID: "gateway/flash", Category: "cheap", Cost: &llm.ModelCost{Input: .1, Output: .2}, Capabilities: &llm.ModelCapabilities{ToolCall: testBool(true), TextOutput: testBool(true)}},
		{ID: "gateway/judgment[200K]", Aliases: []string{"gateway/judgment"}, Cost: &llm.ModelCost{Input: 10, Output: 50}, Capabilities: &llm.ModelCapabilities{ToolCall: testBool(true), TextOutput: testBool(true)}},
		{ID: "gateway/unknown"}, {ID: "gateway/fourth"},
		{ID: "gateway/no-tools", Capabilities: &llm.ModelCapabilities{ToolCall: testBool(false), TextOutput: testBool(true)}},
		{ID: "gateway/image-output", Capabilities: &llm.ModelCapabilities{TextOutput: testBool(false)}},
	}}
	r.Register(p)
	preferences := map[string][]string{"planning": {"opencode:absent", "opencode:gateway/no-tools", "opencode:gateway/judgment"}}
	r.SetModelRecommendations(preferences)
	preferences["planning"][2] = "opencode:gateway/flash"
	defaults := r.CatalogDefaultModels()
	if defaults.Planning != "gateway/judgment[200K]" || defaults.Implementation != "gateway/flash" {
		t.Fatalf("defaults = %+v", defaults)
	}
	implementation := r.EligibleModelsForPhase(llm.PhaseImplementation)["opencode"]
	if len(implementation) != 4 || !slices.Contains(implementation, "gateway/unknown") {
		t.Fatal(implementation)
	}
	chat := r.EligibleModelsForPhase(llm.PhaseChat)["opencode"]
	if !slices.Contains(chat, "gateway/no-tools") || slices.Contains(chat, "gateway/image-output") {
		t.Fatal(chat)
	}
	// Catalog order, category and display-name changes cannot change preferences.
	slices.Reverse(p.catalog)
	p.catalog[4].DisplayName = "ultra max cheap flash"
	if r.CatalogDefaultModels() != defaults {
		t.Fatal("recommendations depend on catalog order/name")
	}
}

func TestRecommendationFallbackIsDeterministicAndPricesAreOptional(t *testing.T) {
	r := llm.NewRegistry()
	if r.CatalogDefaultModels().Implementation != "" {
		t.Fatal("empty registry invented a default")
	}
	p := &stubCatalogProvider{stubProvider: stubProvider{name: "gateway", hasCLI: true}, catalog: []llm.ModelInfo{
		{ID: "zero", Cost: &llm.ModelCost{}}, {ID: "unknown"},
		{ID: "known", Cost: &llm.ModelCost{Input: .2, Output: .4}},
	}}
	r.Register(p)
	if r.CatalogDefaultModels().Implementation != "known" {
		t.Fatal("unknown/zero pricing treated as free")
	}
	r.SetModelRecommendations(map[string][]string{"implementation": {"gateway:unknown"}})
	if r.CatalogDefaultModels().Implementation != "unknown" {
		t.Fatal("explicit recommendation lost to price")
	}
	r.RestrictToProviders(nil)
	if r.CatalogDefaultModels().Implementation != "" {
		t.Fatal("unavailable provider recommended")
	}
}

func TestPipelineEffortUsesDeclaredLevels(t *testing.T) {
	caps := []llm.EffortLevel{llm.EffortHigh, llm.EffortMax}
	for _, tc := range []struct{ configured, want llm.EffortLevel }{{llm.EffortAuto, llm.EffortHigh}, {llm.EffortMax, llm.EffortMax}} {
		got, _ := llm.ResolveEffort(tc.configured, caps, llm.EffortMedium)
		if got != tc.want {
			t.Fatalf("%s -> %s, want %s", tc.configured, got, tc.want)
		}
	}
}
