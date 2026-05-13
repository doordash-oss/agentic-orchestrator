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

package observe_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestModuleFxWiring(t *testing.T) {
	t.Run("config_from_yaml_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		// Write minimal YAML with no observability section
		os.WriteFile(cfgPath, []byte("repos: {}\n"), 0644)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("config.Load failed: %v", err)
		}

		// applyDefaults should set observability defaults
		if !cfg.Observability.Events {
			t.Error("expected Observability.Events == true after applyDefaults")
		}
		if cfg.Observability.OTelServiceName != "agentico" {
			t.Errorf("OTelServiceName = %q, want agentico", cfg.Observability.OTelServiceName)
		}

		// Wire through fx
		stateDir := t.TempDir()
		var obs *observe.Observer

		app := fxtest.New(t,
			fx.Supply(
				cfg,
				fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			),
			observe.Module,
			fx.Populate(&obs),
		)
		app.RequireStart()
		defer app.RequireStop()

		if obs == nil {
			t.Fatal("expected non-nil Observer")
		}

		// Emit events and verify JSONL
		featureID := "fxtest1"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)
		sc := observe.SpanContextForFeature(featureID, "", "", "")
		child := sc.Child()
		obs.PhaseStarted(child, "plan")
		obs.PhaseCompleted(child, "plan", 0, nil)

		eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
		f, err := os.Open(eventsPath)
		if err != nil {
			t.Fatalf("events.jsonl not created: %v", err)
		}
		defer f.Close()

		var count int
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var raw map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			count++
		}
		if count != 2 {
			t.Errorf("expected 2 events, got %d", count)
		}
	})

	t.Run("config_with_explicit_observability_yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("observability:\n  events: true\n  otel_service_name: custom\n"), 0644)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("config.Load failed: %v", err)
		}
		if cfg.Observability.OTelServiceName != "custom" {
			t.Errorf("OTelServiceName = %q, want custom", cfg.Observability.OTelServiceName)
		}

		stateDir := t.TempDir()
		var obs *observe.Observer
		app := fxtest.New(t,
			fx.Supply(
				cfg,
				fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			),
			observe.Module,
			fx.Populate(&obs),
		)
		app.RequireStart()
		defer app.RequireStop()

		if obs == nil {
			t.Fatal("expected non-nil Observer")
		}
	})

	t.Run("disabled_via_yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("observability:\n  events: false\n"), 0644)

		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatalf("config.Load failed: %v", err)
		}
		if cfg.Observability.Events {
			t.Error("expected Observability.Events == false")
		}

		stateDir := t.TempDir()
		featureID := "disabled_fx"
		os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

		var obs *observe.Observer
		app := fxtest.New(t,
			fx.Supply(
				cfg,
				fx.Annotate(stateDir, fx.ResultTags(`name:"stateDir"`)),
			),
			observe.Module,
			fx.Populate(&obs),
		)
		app.RequireStart()
		defer app.RequireStop()

		if obs == nil {
			t.Fatal("expected non-nil Observer even when disabled")
		}

		sc := observe.SpanContextForFeature(featureID, "", "", "")
		obs.PhaseStarted(sc.Child(), "plan")

		eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
		if _, err := os.Stat(eventsPath); !os.IsNotExist(err) {
			t.Error("expected no events.jsonl for disabled observer")
		}
	})
}
