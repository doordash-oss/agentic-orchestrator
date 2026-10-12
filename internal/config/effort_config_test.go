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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEffortConfigMissingLegacyLoadsAsAuto(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaml")
	oldConfig := `defaults:
  models:
    implementation: sonnet[200K]
  inquireness: high
  pipeline: large
`
	if err := os.WriteFile(path, []byte(oldConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range Roles {
		got := *role.Effort(&cfg.Defaults.Effort)
		if got != "" {
			t.Errorf("missing effort.%s should load as empty (Auto), got %q", role.Name, got)
		}
	}
}

func TestEffortConfigExplicitAutoRoundTrips(t *testing.T) {
	for _, role := range Roles {
		t.Run(role.Name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "yaml")
			cfg := NewDefault()
			*role.Effort(&cfg.Defaults.Effort) = "auto"
			if err := Save(path, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := *role.Effort(&loaded.Defaults.Effort)
			if got != "auto" {
				t.Errorf("explicit auto round-trip %s: got %q, want %q", role.Name, got, "auto")
			}
		})
	}
}

func TestEffortConfigEveryExplicitLevelRoundTrips(t *testing.T) {
	levels := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	for _, role := range Roles {
		for _, level := range levels {
			t.Run(role.Name+"_"+level, func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "yaml")
				cfg := NewDefault()
				*role.Effort(&cfg.Defaults.Effort) = level
				if err := Save(path, cfg); err != nil {
					t.Fatal(err)
				}
				loaded, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				got := *role.Effort(&loaded.Defaults.Effort)
				if got != level {
					t.Errorf("round-trip %s=%s: got %q, want %q", role.Name, level, got, level)
				}
			})
		}
	}
}

func TestEffortConfigNoCrossRoleLeakage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yaml")
	cfg := NewDefault()
	cfg.Defaults.Effort = EffortConfig{
		Inquiry:        "high",
		Implementation: "low",
		KBBuild:        "max",
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
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
	cfg := DefaultsConfig{
		Models: ModelConfig{Implementation: "sonnet[200K]"},
		Effort: EffortConfig{
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
	var decoded DefaultsConfig
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"inquiry": "low", "research": "medium", "planning": "high",
		"implementation": "max", "review": "high", "chat": "low", "kb_build": "medium",
	}
	for _, role := range Roles {
		got := *role.Effort(&decoded.Effort)
		if got != want[role.Name] {
			t.Errorf("YAML round-trip %s: got %q, want %q", role.Name, got, want[role.Name])
		}
	}
}

func TestPipelinePreferenceEffortOverlay(t *testing.T) {
	d := DefaultsConfig{
		Models: ModelConfig{Implementation: "sonnet[200K]"},
		Effort: EffortConfig{
			Implementation: "auto",
			Inquiry:        "high",
		},
		PipelinePreferences: map[string]PipelinePreference{
			"moonshot": {
				Models: ModelConfig{Implementation: "opus[200K]"},
				Effort: EffortConfig{
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
	d := DefaultsConfig{
		Effort: EffortConfig{
			Implementation: "high",
			Inquiry:        "medium",
		},
		PipelinePreferences: map[string]PipelinePreference{
			"large": {
				Models: ModelConfig{Implementation: "sonnet[200K]"},
				Effort: EffortConfig{Inquiry: "low"},
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
