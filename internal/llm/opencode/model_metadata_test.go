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

package opencode

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestDiscoveredVariantsSurviveCacheAndDriveManagedConfig(t *testing.T) {
	for _, provider := range []string{"portkey", "doordash", "openai", "future-gateway"} {
		t.Run(provider, func(t *testing.T) {
			fixture := provider + `/glm-flash-new
{"name":"anything","capabilities":{"toolcall":true,"output":{"text":true}},"cost":{"input":1.4,"output":4.4},"limit":{"context":1040000},"variants":{"high":{"reasoningEffort":"high"},"max":{"reasoningEffort":"max","thinking":{"budgetTokens":32000}},"low":{"disabled":true}}}`
			models, _, err := parseOpenCodeModels([]byte(fixture))
			if err != nil {
				t.Fatal(err)
			}
			if models[0].Category != "" {
				t.Fatal("name must not determine category")
			}
			if !slices.Equal(models[0].EffortCapabilities, []llm.EffortLevel{llm.EffortHigh, llm.EffortMax}) {
				t.Fatal(models[0].EffortCapabilities)
			}
			data, _ := json.Marshal(models)
			var cached []llm.ModelInfo
			if err := json.Unmarshal(data, &cached); err != nil {
				t.Fatal(err)
			}
			p := New()
			p.SetModelCatalog(cached)
			// Caller mutation must not change cached variants or a concurrent session.
			cached[0].EffortVariants[llm.EffortMax]["reasoningEffort"] = "low"
			snapshot := p.ModelCatalog()
			snapshot[0].EffortVariants[llm.EffortHigh]["reasoningEffort"] = "low"
			for _, level := range []llm.EffortLevel{llm.EffortHigh, llm.EffortMax} {
				_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: provider + "/glm-flash-new", StateDir: t.TempDir(), EffortLevel: level})
				if err != nil {
					t.Fatal(err)
				}
				cfg := readManagedConfigFile(t, env)
				var emitted struct {
					Models map[string]struct {
						Options map[string]any `json:"options"`
					} `json:"models"`
				}
				if err := json.Unmarshal(cfg.Provider[provider], &emitted); err != nil {
					t.Fatal(err)
				}
				if got := emitted.Models["glm-flash-new"].Options; !reflect.DeepEqual(got, models[0].EffortVariants[level]) {
					t.Fatalf("%s options = %#v", level, got)
				}
			}
			if cost := p.ComputeCost(provider+"/glm-flash-new", 1000000, 1000000); cost < 5.79 || cost > 5.81 {
				t.Fatalf("cached pricing lost: %v", cost)
			}
		})
	}
}

func TestVariantDiscoveryDoesNotInventControls(t *testing.T) {
	variants, levels := discoverEffortVariants(map[string]map[string]any{
		"high": {"reasoningEffort": "high"}, "max": {"reasoningEffort": "high"},
		"xhigh": {"disabled": true, "reasoningEffort": "xhigh"}, "ultra": {},
		"unranked":   {"thinking": map[string]any{"budgetTokens": 123}},
		"economical": {"reasoningEffort": "low"},
	})
	if !slices.Equal(levels, []llm.EffortLevel{llm.EffortLow, llm.EffortHigh}) || len(variants) != 2 {
		t.Fatalf("%v: %#v", levels, variants)
	}
}

func TestMissingMetadataDoesNotImplyEffortOrCategory(t *testing.T) {
	models, _, err := parseOpenCodeModels([]byte("openai/gpt-99\nportkey/kimi-next\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range models {
		if model.Category != "" || len(model.EffortCapabilities) != 0 || model.Capabilities != nil {
			t.Fatalf("invented metadata: %+v", model)
		}
	}
	p := New()
	p.SetModelCatalog(models)
	_, env, err := p.BuildCommand(llm.CommandBuildOpts{Model: "openai/gpt-99", EffortLevel: llm.EffortMax})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configContentValue(t, env), "reasoningEffort") {
		t.Fatal("invented effort option")
	}
}
