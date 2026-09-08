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

var allEffortRoles = []string{"Inquiry", "Research", "Planning", "Implementation", "Review", "Utilities", "KBBuild"}

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
	for _, role := range allEffortRoles {
		got := config.EffortConfigFieldByName(cfg.Defaults.Effort, role)
		if got != "" {
			t.Errorf("missing effort.%s should load as empty (Auto), got %q", role, got)
		}
	}
}

func TestEffortConfigExplicitAutoRoundTrips(t *testing.T) {
	for _, role := range allEffortRoles {
		t.Run(role, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			cfg := config.NewDefault()
			config.SetEffortConfigFieldByName(&cfg.Defaults.Effort, role, "auto")
			if err := config.Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := config.EffortConfigFieldByName(loaded.Defaults.Effort, role)
			if got != "auto" {
				t.Errorf("explicit auto round-trip %s: got %q, want %q", role, got, "auto")
			}
		})
	}
}

func TestEffortConfigEveryExplicitLevelRoundTrips(t *testing.T) {
	levels := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	for _, role := range allEffortRoles {
		for _, level := range levels {
			t.Run(role+"_"+level, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "config.yaml")
				cfg := config.NewDefault()
				config.SetEffortConfigFieldByName(&cfg.Defaults.Effort, role, level)
				if err := config.Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				loaded, err := config.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				got := config.EffortConfigFieldByName(loaded.Defaults.Effort, role)
				if got != level {
					t.Errorf("round-trip %s=%s: got %q, want %q", role, level, got, level)
				}
			})
		}
	}
}

func TestEffortConfigNoCrossRoleLeakage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.NewDefault()
	cfg.Defaults.Effort = config.EffortConfig{
		Inquiry:        "high",
		Implementation: "low",
		KBBuild:        "max",
	}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Defaults.Effort.Inquiry != "high" {
		t.Errorf("Inquiry: got %q, want %q", loaded.Defaults.Effort.Inquiry, "high")
	}
	if loaded.Defaults.Effort.Implementation != "low" {
		t.Errorf("Implementation: got %q, want %q", loaded.Defaults.Effort.Implementation, "low")
	}
	if loaded.Defaults.Effort.KBBuild != "max" {
		t.Errorf("KBBuild: got %q, want %q", loaded.Defaults.Effort.KBBuild, "max")
	}
	if loaded.Defaults.Effort.Research != "" {
		t.Errorf("Research should be empty, got %q", loaded.Defaults.Effort.Research)
	}
	if loaded.Defaults.Effort.Planning != "" {
		t.Errorf("Planning should be empty, got %q", loaded.Defaults.Effort.Planning)
	}
	if loaded.Defaults.Effort.Review != "" {
		t.Errorf("Review should be empty, got %q", loaded.Defaults.Effort.Review)
	}
	if loaded.Defaults.Effort.Utilities != "" {
		t.Errorf("Utilities should be empty, got %q", loaded.Defaults.Effort.Utilities)
	}
}

func TestEffortConfigYAMLShape(t *testing.T) {
	cfg := config.DefaultsConfig{
		Models: config.ModelConfig{Implementation: "sonnet[200K]"},
		Effort: config.EffortConfig{
			Inquiry:        "low",
			Research:       "medium",
			Planning:       "high",
			Implementation: "max",
			Review:         "high",
			Utilities:      "low",
			KBBuild:        "medium",
		},
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded config.DefaultsConfig
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"Inquiry": "low", "Research": "medium", "Planning": "high",
		"Implementation": "max", "Review": "high", "Utilities": "low", "KBBuild": "medium",
	}
	for _, role := range allEffortRoles {
		got := config.EffortConfigFieldByName(decoded.Effort, role)
		if got != want[role] {
			t.Errorf("YAML round-trip %s: got %q, want %q", role, got, want[role])
		}
	}
}

func TestPipelinePreferenceEffortOverlay(t *testing.T) {
	d := config.DefaultsConfig{
		Models: config.ModelConfig{Implementation: "sonnet[200K]"},
		Effort: config.EffortConfig{
			Implementation: "auto",
			Inquiry:        "high",
		},
		PipelinePreferences: map[string]config.PipelinePreference{
			"moonshot": {
				Models: config.ModelConfig{Implementation: "opus[200K]"},
				Effort: config.EffortConfig{
					Implementation: "high",
					Inquiry:        "low",
				},
			},
		},
	}
	pref := d.PreferenceForPipeline("moonshot")
	if pref.Effort.Implementation != "high" {
		t.Errorf("overlay Implementation: got %q, want %q", pref.Effort.Implementation, "high")
	}
	if pref.Effort.Inquiry != "low" {
		t.Errorf("overlay Inquiry: got %q, want %q", pref.Effort.Inquiry, "low")
	}
	pref = d.PreferenceForPipeline("medium")
	if pref.Effort.Implementation != "auto" {
		t.Errorf("non-overlay Implementation: got %q, want %q (global default)", pref.Effort.Implementation, "auto")
	}
	if pref.Effort.Inquiry != "high" {
		t.Errorf("non-overlay Inquiry: got %q, want %q (global default)", pref.Effort.Inquiry, "high")
	}
}

func TestPipelinePreferenceEffortPartialOverlayKeepsGlobal(t *testing.T) {
	d := config.DefaultsConfig{
		Effort: config.EffortConfig{
			Implementation: "high",
			Inquiry:        "medium",
		},
		PipelinePreferences: map[string]config.PipelinePreference{
			"large": {
				Models: config.ModelConfig{Implementation: "sonnet[200K]"},
				Effort: config.EffortConfig{Inquiry: "low"},
			},
		},
	}
	pref := d.PreferenceForPipeline("large")
	if pref.Effort.Implementation != "high" {
		t.Errorf("partial overlay should keep global Implementation: got %q, want %q", pref.Effort.Implementation, "high")
	}
	if pref.Effort.Inquiry != "low" {
		t.Errorf("partial overlay should use saved Inquiry: got %q, want %q", pref.Effort.Inquiry, "low")
	}
}
