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

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"gopkg.in/yaml.v3"
)

func TestEffortConfigMissingLegacyLoadsAsAuto(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	oldConfig := `defaults:
  models:
    implementation: sonnet[200K]
  inquireness: high
  pipeline: large
`
	if err := os.WriteFile(path, []byte(oldConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Effort.Implementation != "" {
		t.Errorf("missing effort should load as empty (Auto), got %q", cfg.Defaults.Effort.Implementation)
	}
}

func TestEffortConfigExplicitAutoRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.NewDefault()
	cfg.Defaults.Effort.Implementation = "auto"
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Effort.Implementation != "auto" {
		t.Errorf("explicit auto round-trip: got %q, want %q", loaded.Defaults.Effort.Implementation, "auto")
	}
}

func TestEffortConfigEveryExplicitLevelRoundTrips(t *testing.T) {
	levels := []string{"low", "medium", "high", "max"}
	for _, level := range levels {
		t.Run(level, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			cfg := config.NewDefault()
			cfg.Defaults.Effort.Implementation = level
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Defaults.Effort.Implementation != level {
				t.Errorf("round-trip %s: got %q, want %q", level, loaded.Defaults.Effort.Implementation, level)
			}
		})
	}
}

func TestEffortConfigYAMLShape(t *testing.T) {
	cfg := config.DefaultsConfig{
		Models: config.ModelConfig{Implementation: "sonnet[200K]"},
		Effort: config.EffortConfig{Implementation: "high"},
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded config.DefaultsConfig
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Effort.Implementation != "high" {
		t.Errorf("YAML round-trip: got %q, want %q", decoded.Effort.Implementation, "high")
	}
}

func TestPipelinePreferenceEffortOverlay(t *testing.T) {
	d := config.DefaultsConfig{
		Models: config.ModelConfig{Implementation: "sonnet[200K]"},
		Effort: config.EffortConfig{Implementation: "auto"},
		PipelinePreferences: map[string]config.PipelinePreference{
			"moonshot": {
				Models: config.ModelConfig{Implementation: "opus[200K]"},
				Effort: config.EffortConfig{Implementation: "high"},
			},
		},
	}
	pref := d.PreferenceForPipeline("moonshot")
	if pref.Effort.Implementation != "high" {
		t.Errorf("overlay: got %q, want %q", pref.Effort.Implementation, "high")
	}
	pref = d.PreferenceForPipeline("medium")
	if pref.Effort.Implementation != "auto" {
		t.Errorf("non-overlay: got %q, want %q (global default)", pref.Effort.Implementation, "auto")
	}
}

func TestPipelinePreferenceEffortEmptyOverlayKeepsGlobal(t *testing.T) {
	d := config.DefaultsConfig{
		Effort: config.EffortConfig{Implementation: "high"},
		PipelinePreferences: map[string]config.PipelinePreference{
			"large": {
				Models: config.ModelConfig{Implementation: "sonnet[200K]"},
			},
		},
	}
	pref := d.PreferenceForPipeline("large")
	if pref.Effort.Implementation != "high" {
		t.Errorf("empty overlay should keep global: got %q, want %q", pref.Effort.Implementation, "high")
	}
}
