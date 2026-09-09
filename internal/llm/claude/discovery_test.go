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

package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func catalogResponse(models string) string {
	return `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":` + strings.ReplaceAll(models, "\n", "") + `}}}`
}

func TestClaudeInitializationCatalog(t *testing.T) {
	t.Parallel()
	models, err := readClaudeModelCatalog(strings.NewReader(`{"type":"system","subtype":"status"}` + "\n" + catalogResponse(`[
  {"value":"default","resolvedModel":"claude-opus-5"},
  {"value":"opus[1m]","resolvedModel":"claude-opus-5[1m]","displayName":"Opus (1M context)","supportsEffort":true,"supportedEffortLevels":["max","high","high","low","unknown"]},
  {"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1","displayName":"Fable","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"],"supportsAdaptiveThinking":true},
  {"value":"sonnet","resolvedModel":"claude-sonnet-5","displayName":"Sonnet","supportsEffort":true,"supportedEffortLevels":["low","high"]},
  {"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001","displayName":"Haiku"},
  {"value":"new-model","displayName":"New model","supportsEffort":false,"supportedEffortLevels":["max"]},
  {"value":"opusplan"}
 ]`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 5 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "opus[1M]" || models[0].ContextWindow != 1_000_000 || !slices.Equal(models[0].EffortCapabilities, []llm.EffortLevel{llm.EffortLow, llm.EffortHigh, llm.EffortMax}) {
		t.Fatalf("opus = %+v", models[0])
	}
	if models[1].ID != "claude-fable-5-1[1M]" || models[1].DisplayName != "Fable" || models[1].Category != "capable" || !slices.Contains(models[1].Aliases, "fable[1M]") {
		t.Fatalf("fable = %+v", models[1])
	}
	if models[2].ContextWindow != 0 {
		t.Fatal("invented context window for sonnet")
	}
	if len(models[3].EffortCapabilities) != 0 || len(models[4].EffortCapabilities) != 0 {
		t.Fatal("invented effort support")
	}
	p := &Provider{}
	p.SetModelCatalog(models)
	for _, model := range models {
		for _, alias := range append([]string{model.ID}, model.Aliases...) {
			if !p.MatchesModel(alias) {
				t.Fatalf("alias %q does not route", alias)
			}
			args, _, err := p.BuildCommand(llm.CommandBuildOpts{Model: alias})
			if err != nil {
				t.Fatal(err)
			}
			assertModelArg(t, args, model.Aliases[0])
		}
	}
	// Catalog persistence must retain exact selectors and model-specific effort.
	data, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var cached []llm.ModelInfo
	if err := json.Unmarshal(data, &cached); err != nil {
		t.Fatal(err)
	}
	p.SetModelCatalog(cached)
	args, _, err := p.BuildCommand(llm.CommandBuildOpts{Model: "fable[1M]"})
	if err != nil {
		t.Fatal(err)
	}
	assertModelArg(t, args, "claude-fable-5-1[1m]")
}

func TestClaudeInitializationRejectsIncompleteCatalog(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"EOF":                   "",
		"malformed JSON":        "{",
		"unrelated response":    `{"type":"control_response","response":{"request_id":"other","subtype":"success"}}`,
		"rejected":              `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"error","error":"secret-value"}}`,
		"missing models":        `{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{}}}`,
		"empty":                 catalogResponse(`[]`),
		"routing policies only": catalogResponse(`[{"value":"default"},{"value":"opusplan"}]`),
		"partial":               catalogResponse(`[{"value":"sonnet"},{"displayName":"Fable"}]`),
		"duplicate":             catalogResponse(`[{"value":"sonnet"},{"value":"SONNET"}]`),
		"oversized":             strings.Repeat("x", 4*1024*1024+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			models, err := readClaudeModelCatalog(strings.NewReader(input))
			if err == nil || models != nil {
				t.Fatalf("models=%+v err=%v", models, err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("provider text leaked")
			}
		})
	}
}

func TestClaudeInitializationPreservesDistinctVersions(t *testing.T) {
	t.Parallel()
	models, err := readClaudeModelCatalog(strings.NewReader(catalogResponse(`[
  {"value":"claude-fable-5[1m]","resolvedModel":"claude-fable-5"},
  {"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1"},
  {"value":"custom"}
 ]`)))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models=%+v", models)
	}
	for _, model := range models {
		if slices.Contains(model.Aliases, "fable") {
			t.Fatal("ambiguous family alias")
		}
	}
	if models[2].DisplayName != "custom" || models[2].ContextWindow != 0 {
		t.Fatalf("custom=%+v", models[2])
	}
}

func TestClaudeInitializationAliasesDoNotShadowPinnedSelectors(t *testing.T) {
	t.Parallel()
	models, err := readClaudeModelCatalog(strings.NewReader(catalogResponse(`[
  {"value":"sonnet","resolvedModel":"claude-sonnet-5"},
  {"value":"claude-sonnet-5","resolvedModel":"claude-sonnet-5"}
 ]`)))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{}
	p.SetModelCatalog(models)
	args, _, err := p.BuildCommand(llm.CommandBuildOpts{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	assertModelArg(t, args, "claude-sonnet-5")
}

func TestClaudeInitializationResolvesSavedContextAnnotations(t *testing.T) {
	t.Parallel()
	models, err := readClaudeModelCatalog(strings.NewReader(catalogResponse(`[
  {"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1"},
  {"value":"opus[1m]","resolvedModel":"claude-opus-5[1m]"},
  {"value":"sonnet","resolvedModel":"claude-sonnet-5"},
  {"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001"}
 ]`)))
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{}
	p.SetModelCatalog(models)
	registry := llm.NewRegistry()
	registry.Register(p)
	for saved, want := range map[string]string{
		"fable[1M]":        "claude-fable-5-1[1m]",
		"claude:fable[1M]": "claude-fable-5-1[1m]",
		"opus[1M]":         "opus[1m]",
		"sonnet[1M]":       "sonnet",
		"haiku[200K]":      "haiku",
	} {
		_, model, err := registry.ResolveModel(saved)
		if err != nil {
			t.Fatal(err)
		}
		args, _, err := p.BuildCommand(llm.CommandBuildOpts{Model: model})
		if err != nil {
			t.Fatal(err)
		}
		assertModelArg(t, args, want)
	}
}
