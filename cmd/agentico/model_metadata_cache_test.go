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

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

func TestOpenCodeRefreshesVariantsWithoutCLIVersionChange(t *testing.T) {
	root := t.TempDir()
	calls := 0
	fail := false
	runner := func(_ context.Context, _ string, args []string, _ []string) ([]byte, error) {
		if strings.Join(args, " ") == "--version" {
			return []byte("1.18.25"), nil
		}
		if fail {
			return nil, errors.New("offline")
		}
		calls++
		variants := `"high":{"reasoningEffort":"high"}`
		if calls > 1 {
			variants += `,"max":{"reasoningEffort":"max"}`
		}
		return []byte(`portkey/custom
{"variants":{` + variants + `}}`), nil
	}
	for i := 1; i <= 2; i++ {
		p := opencode.NewWithRunner(runner)
		warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, root, nil, false)
		if len(warnings) != 0 {
			t.Fatal(warnings)
		}
		if calls != i || len(p.ModelCatalog()[0].EffortCapabilities) != i {
			t.Fatalf("stale catalog reused: calls=%d catalog=%+v", calls, p.ModelCatalog())
		}
	}
	fail = true
	p := opencode.NewWithRunner(runner)
	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, root, nil, false)
	if len(warnings) != 1 || !strings.Contains(warningText(warnings[0]), "stale cache") {
		t.Fatal(warnings)
	}
	if len(p.ModelCatalog()[0].EffortVariants) != 2 {
		t.Fatal("cached options lost")
	}
}

func TestRemapUsesRoleRecommendations(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubCatalogProvider{
		stubProvider: stubProvider{name: "gateway", hasCLI: true, models: []string{"planner", "worker"}},
		catalog:      []llm.ModelInfo{{ID: "planner"}, {ID: "worker"}},
	})
	r.SetModelRecommendations(map[string][]string{
		"planning":       {"gateway:planner"},
		"implementation": {"gateway:worker"},
	})
	cfg := config.NewDefault()
	cfg.Defaults.Models.Planning = "missing"
	cfg.Defaults.Models.Implementation = "missing"
	cfg.Defaults.Models.Review = "worker"
	remapUnresolvableModels(cfg, r)
	if cfg.Defaults.Models.Planning != "planner" || cfg.Defaults.Models.Implementation != "worker" {
		t.Fatalf("role recommendations lost during recovery: %+v", cfg.Defaults.Models)
	}
	if cfg.Defaults.Models.Review != "worker" {
		t.Fatal("recovery replaced a resolvable explicit selection")
	}
}
