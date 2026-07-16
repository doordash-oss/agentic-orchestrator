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
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewDefault(t *testing.T) {
	cfg := NewDefault()
	wantModels := ModelConfig{
		Inquiry:        "sonnet[200K]",
		Research:       "sonnet[200K]",
		Planning:       "sonnet[200K]",
		Implementation: "sonnet[200K]",
		Review:         "gpt-5.4[272K]",
		Utilities:      "sonnet[200K]",
		KBBuild:        "sonnet[200K]",
	}
	if cfg.Defaults.Models != wantModels {
		t.Errorf("default models = %+v, want %+v", cfg.Defaults.Models, wantModels)
	}
	if cfg.Defaults.MaxIterations != 10 {
		t.Errorf("expected max iterations 10, got %d", cfg.Defaults.MaxIterations)
	}
	if cfg.Defaults.MaxConsecutiveFailures != 3 {
		t.Errorf("expected max consecutive failures 3, got %d", cfg.Defaults.MaxConsecutiveFailures)
	}
	if cfg.Defaults.Inquireness != "high" {
		t.Errorf("expected inquireness high, got %q", cfg.Defaults.Inquireness)
	}
	if cfg.Defaults.ExitCriteria == "" {
		t.Error("expected non-empty exit criteria")
	}
}

func TestNewDefaultManualPublish(t *testing.T) {
	cfg := NewDefault()
	if !cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to default to true (zero-config users should not auto-publish)")
	}
}

func TestCheckpoints_DraftPublish_DefaultsFalse(t *testing.T) {
	// A config YAML with no draft_publish key must unmarshal with DraftPublish==false.
	input := `
defaults:
  checkpoints:
    manual_publish: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Defaults.Checkpoints.DraftPublish {
		t.Error("DraftPublish should default to false when key is absent")
	}
}

func TestCheckpoints_DraftPublish_TrueWhenSet(t *testing.T) {
	input := `
defaults:
  checkpoints:
    draft_publish: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !cfg.Defaults.Checkpoints.DraftPublish {
		t.Error("DraftPublish should be true when set to true")
	}
}

func TestCheckpoints_DraftPublish_FalseWhenExplicitFalse(t *testing.T) {
	input := `
defaults:
  checkpoints:
    draft_publish: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Defaults.Checkpoints.DraftPublish {
		t.Error("DraftPublish should be false when explicitly set to false")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Defaults.PipelinePreferences = map[string]PipelinePreference{
		"medium": {
			Models: ModelConfig{
				Inquiry:        "claude:haiku",
				Research:       "claude:haiku",
				Planning:       "claude:haiku",
				Implementation: "claude:sonnet",
				Review:         "codex:gpt-5.4-mini",
				KBBuild:        "claude:opus",
			},
			Inquireness: "none",
		},
	}
	original.Repos["test-repo"] = RepoConfig{
		Path: "/tmp/test-repo",
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.Defaults.Models.Research != original.Defaults.Models.Research {
		t.Errorf("models mismatch: got %s, want %s", loaded.Defaults.Models.Research, original.Defaults.Models.Research)
	}
	if loaded.Defaults.Models.Inquiry != original.Defaults.Models.Inquiry {
		t.Errorf("inquiry model mismatch: got %s, want %s", loaded.Defaults.Models.Inquiry, original.Defaults.Models.Inquiry)
	}
	if loaded.Defaults.MaxIterations != original.Defaults.MaxIterations {
		t.Errorf("max iterations mismatch: got %d, want %d", loaded.Defaults.MaxIterations, original.Defaults.MaxIterations)
	}
	if got := loaded.Defaults.PipelinePreferences["medium"].Inquireness; got != "none" {
		t.Errorf("pipeline preference inquireness mismatch: got %q, want %q", got, "none")
	}
	if got := loaded.Defaults.PipelinePreferences["medium"].Models.Implementation; got != "claude:sonnet" {
		t.Errorf("pipeline preference implementation mismatch: got %q, want %q", got, "claude:sonnet")
	}
	rc, ok := loaded.Repos["test-repo"]
	if !ok {
		t.Fatal("test-repo not found in loaded config")
	}
	if rc.Path != "/tmp/test-repo" {
		t.Errorf("repo path mismatch: got %s", rc.Path)
	}
}

func TestLoadOrCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Should not exist yet
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected file to not exist")
	}

	cfg, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("load or create: %v", err)
	}

	if cfg.Defaults.Models.Research != "sonnet[200K]" {
		t.Errorf("expected default research model sonnet[200K], got %s", cfg.Defaults.Models.Research)
	}

	// File should exist now
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}

	// Load again - should return same config
	cfg2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second load or create: %v", err)
	}
	if cfg2.Defaults.Models.Research != cfg.Defaults.Models.Research {
		t.Errorf("mismatch on second load")
	}
}

func TestDefaultsConfigPreferenceForPipelineFallsBackToGlobalDefaults(t *testing.T) {
	defaults := NewDefault().Defaults

	pref := defaults.PreferenceForPipeline("medium")

	if pref.Models != defaults.Models {
		t.Errorf("models = %+v, want %+v", pref.Models, defaults.Models)
	}
	if pref.Inquireness != defaults.Inquireness {
		t.Errorf("inquireness = %q, want %q", pref.Inquireness, defaults.Inquireness)
	}
}

func TestDefaultsConfigPreferenceForPipelineOverlaysRememberedValues(t *testing.T) {
	defaults := NewDefault().Defaults
	defaults.PipelinePreferences = map[string]PipelinePreference{
		"moonshot": {
			Models: ModelConfig{
				Inquiry:        "claude:haiku",
				Implementation: "claude:sonnet",
				Review:         "codex:gpt-5.4-mini",
			},
			Inquireness: "high",
		},
	}

	pref := defaults.PreferenceForPipeline("moonshot")

	if pref.Models.Research != defaults.Models.Research {
		t.Errorf("research = %q, want fallback %q", pref.Models.Research, defaults.Models.Research)
	}
	if pref.Models.Inquiry != "claude:haiku" {
		t.Errorf("inquiry = %q, want %q", pref.Models.Inquiry, "claude:haiku")
	}
	if pref.Models.Implementation != "claude:sonnet" {
		t.Errorf("implementation = %q, want %q", pref.Models.Implementation, "claude:sonnet")
	}
	if pref.Models.Review != "codex:gpt-5.4-mini" {
		t.Errorf("review = %q, want %q", pref.Models.Review, "codex:gpt-5.4-mini")
	}
	if pref.Inquireness != "high" {
		t.Errorf("inquireness = %q, want %q", pref.Inquireness, "high")
	}
}

func TestLoadModelConfigMigratesMissingInquiryFromResearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("defaults:\n  models:\n    research: claude:custom-research\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Defaults.Models.Inquiry != "claude:custom-research" {
		t.Errorf("inquiry = %q, want migrated research model", cfg.Defaults.Models.Inquiry)
	}
	if cfg.Defaults.Models.Research != "claude:custom-research" {
		t.Errorf("research = %q, want configured research model", cfg.Defaults.Models.Research)
	}
}

func TestYAMLIgnoresUnknownAutoPublish(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	content := []byte("defaults:\n  auto_publish: true\n  checkpoints:\n    manual_publish: true\n")
	if err := os.WriteFile(configPath, content, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load with legacy auto_publish: %v", err)
	}
	if !cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected manual_publish to be parsed correctly")
	}
}

func TestLoadConfigCheckpointFieldsDefaultOnAndPlanReviewIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte(`defaults:
  checkpoints:
    plan_review: false
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := cfg.Defaults.Checkpoints
	if !got.InquiryReview ||
		!got.ResearchReview ||
		!got.DesignReview ||
		!got.RoadmapReview ||
		!got.PhasePlanReview ||
		!got.ManualPublish {
		t.Fatalf("omitted checkpoints should default on and plan_review should be ignored, got %+v", got)
	}
}

func TestLoadConfigMalformedLegacyPlanReviewIgnored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte(`defaults:
  checkpoints:
    plan_review:
      - not-a-bool
    manual_publish: false
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load should ignore malformed legacy plan_review: %v", err)
	}
	got := cfg.Defaults.Checkpoints
	if !got.InquiryReview || !got.ResearchReview || !got.DesignReview || !got.RoadmapReview || !got.PhasePlanReview {
		t.Fatalf("non-legacy omitted checkpoints should default on, got %+v", got)
	}
	if got.ManualPublish {
		t.Fatalf("explicit manual_publish=false should still win, got %+v", got)
	}
}

func TestLoadConfigCheckpointExplicitFalseWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte(`defaults:
  checkpoints:
    inquiry_review: false
    research_review: false
    design_review: false
    roadmap_review: false
    phase_plan_review: true
    manual_publish: false
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := cfg.Defaults.Checkpoints
	if got.InquiryReview ||
		got.ResearchReview ||
		got.DesignReview ||
		got.RoadmapReview ||
		!got.PhasePlanReview ||
		got.ManualPublish {
		t.Fatalf("loaded checkpoints did not preserve explicit values: %+v", got)
	}
}

func TestSaveWritesCheckpointFieldsExplicitly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := NewDefault()
	cfg.Defaults.Checkpoints = Checkpoints{
		InquiryReview:   false,
		ResearchReview:  true,
		DesignReview:    false,
		RoadmapReview:   true,
		PhasePlanReview: false,
		ManualPublish:   false,
	}

	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	yamlStr := string(data)
	for _, needle := range []string{
		"inquiry_review: false",
		"research_review: true",
		"design_review: false",
		"roadmap_review: true",
		"phase_plan_review: false",
		"manual_publish: false",
	} {
		if !strings.Contains(yamlStr, needle) {
			t.Fatalf("saved YAML missing %q:\n%s", needle, yamlStr)
		}
	}
	for _, line := range strings.Split(yamlStr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "plan_review:") {
			t.Fatalf("saved YAML should not contain legacy plan_review:\n%s", yamlStr)
		}
	}
}

func TestLoadConfigWithoutManualPublishDefaultsToTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Legacy config: has a checkpoints section but no manual_publish key.
	content := []byte("defaults:\n  checkpoints:\n    plan_review: true\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to default to true when omitted from loaded config")
	}
	if !cfg.Defaults.Checkpoints.RoadmapReview {
		t.Error("expected RoadmapReview to default to true")
	}
	if !cfg.Defaults.Checkpoints.PhasePlanReview {
		t.Error("expected PhasePlanReview to default to true")
	}
}

func TestLoadConfigWithExplicitManualPublishFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Config that explicitly sets manual_publish: false (auto-publish).
	content := []byte("defaults:\n  checkpoints:\n    manual_publish: false\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to honor explicit false")
	}
}

func TestLoadConfigWithoutCheckpointsSectionDefaultsToManualPublish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Config with no checkpoints section at all.
	content := []byte("defaults:\n  max_iterations: 5\n")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to default to true when checkpoints section is absent")
	}
	if cfg.Defaults.MaxIterations != 5 {
		t.Errorf("expected MaxIterations 5, got %d", cfg.Defaults.MaxIterations)
	}
}

func TestManualPublishFalseRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Defaults.Checkpoints.ManualPublish = false

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish=false to survive save/load round trip")
	}
}

func TestMaxPhasePlanIterationsDefault(t *testing.T) {
	cfg := NewDefault()
	applyDefaults(cfg)
	if cfg.Defaults.MaxPhasePlanIterations != 10 {
		t.Errorf("expected MaxPhasePlanIterations default 10, got %d", cfg.Defaults.MaxPhasePlanIterations)
	}
}

func TestMaxPhasePlanIterationsPreserved(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.MaxPhasePlanIterations = 5
	applyDefaults(cfg)
	if cfg.Defaults.MaxPhasePlanIterations != 5 {
		t.Errorf("expected MaxPhasePlanIterations to be preserved as 5, got %d", cfg.Defaults.MaxPhasePlanIterations)
	}
}

func TestMaxPhasePlanIterationsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := NewDefault()
	original.Defaults.MaxPhasePlanIterations = 4
	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Defaults.MaxPhasePlanIterations != 4 {
		t.Errorf("MaxPhasePlanIterations = %d, want 4 after round-trip", loaded.Defaults.MaxPhasePlanIterations)
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)

	if cfg.Defaults.Models.Research != "sonnet[200K]" {
		t.Errorf("expected default research model, got %s", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Utilities != "sonnet[200K]" {
		t.Errorf("expected default utilities model sonnet[200K], got %s", cfg.Defaults.Models.Utilities)
	}
	if cfg.Defaults.MaxIterations != 10 {
		t.Errorf("expected default max iterations, got %d", cfg.Defaults.MaxIterations)
	}
	if cfg.Repos == nil {
		t.Error("expected repos to be initialized")
	}
	if !cfg.Defaults.Checkpoints.ManualPublish {
		t.Error("expected ManualPublish to default to true via applyDefaults")
	}
}

func TestApplyDefaultsPreservesExplicitFalseCheckpointsOnSecondCall(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{
			Checkpoints: Checkpoints{
				InquiryReview:   false,
				ResearchReview:  false,
				DesignReview:    false,
				RoadmapReview:   false,
				PhasePlanReview: true,
				ManualPublish:   false,
				parsed:          true,
			},
		},
	}

	applyDefaults(cfg)
	applyDefaults(cfg)

	got := cfg.Defaults.Checkpoints
	if got.InquiryReview || got.ResearchReview || got.DesignReview || got.RoadmapReview || !got.PhasePlanReview || got.ManualPublish {
		t.Fatalf("explicit checkpoint values were not preserved after repeated applyDefaults: %+v", got)
	}
}

func TestUtilitiesModelDefault(t *testing.T) {
	cfg := NewDefault()
	if cfg.Defaults.Models.Utilities != "sonnet[200K]" {
		t.Errorf("expected utilities model sonnet[200K], got %s", cfg.Defaults.Models.Utilities)
	}
}

func TestApplyDefaultsPreservesExistingUtilities(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Models.Utilities = "haiku"
	applyDefaults(cfg)
	if cfg.Defaults.Models.Utilities != "haiku" {
		t.Errorf("expected utilities model to be preserved as haiku, got %s", cfg.Defaults.Models.Utilities)
	}
}

func TestUtilitiesModelRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Defaults.Models.Utilities = "haiku"
	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Defaults.Models.Utilities != "haiku" {
		t.Errorf("utilities model mismatch after round-trip: got %s, want haiku", loaded.Defaults.Models.Utilities)
	}
}

// --- KBBuild field tests ---

func TestNewDefaultKBBuild(t *testing.T) {
	cfg := NewDefault()
	if cfg.Defaults.Models.KBBuild != "sonnet[200K]" {
		t.Errorf("expected KBBuild default sonnet[200K], got %s", cfg.Defaults.Models.KBBuild)
	}
}

func TestApplyDefaultsFillsKBBuild(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Defaults.Models.KBBuild != "sonnet[200K]" {
		t.Errorf("expected KBBuild sonnet[200K] after applyDefaults, got %s", cfg.Defaults.Models.KBBuild)
	}
}

func TestApplyDefaultsPreservesExistingKBBuild(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Models.KBBuild = "sonnet"
	applyDefaults(cfg)
	if cfg.Defaults.Models.KBBuild != "sonnet" {
		t.Errorf("expected KBBuild to be preserved as sonnet, got %s", cfg.Defaults.Models.KBBuild)
	}
}

func TestKBBuildYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Defaults.Models.KBBuild = "sonnet"
	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Defaults.Models.KBBuild != "sonnet" {
		t.Errorf("KBBuild mismatch after round-trip: got %s, want sonnet", loaded.Defaults.Models.KBBuild)
	}
}

func TestLoadConfigWithoutKBBuildGetsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Legacy config without kb_build field.
	content := []byte("defaults:\n  models:\n    research: opus\n    planning: opus\n    implementation: opus\n    review: codex\n    chat: sonnet\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.Models.KBBuild != "sonnet[200K]" {
		t.Errorf("expected KBBuild sonnet[200K] for config without kb_build, got %s", cfg.Defaults.Models.KBBuild)
	}
}

func TestKBBuildYAMLPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte("defaults:\n  models:\n    research: opus\n    kb_build: haiku\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.Models.KBBuild != "haiku" {
		t.Errorf("expected KBBuild haiku, got %s", cfg.Defaults.Models.KBBuild)
	}
}

// --- ApplyProviderDefaults tests ---

func TestApplyProviderDefaults_FillsEmptyFields(t *testing.T) {
	cfg := &Config{}
	defaults := map[string]string{
		"research":       "claude:opus",
		"planning":       "claude:opus",
		"implementation": "claude:opus",
		"review":         "codex:codex",
		"chat":           "claude:sonnet",
		"kb_build":       "claude:opus",
	}
	ApplyProviderDefaults(cfg, defaults)

	m := cfg.Defaults.Models
	if m.Research != "claude:opus" {
		t.Errorf("Research = %q, want claude:opus", m.Research)
	}
	if m.Planning != "claude:opus" {
		t.Errorf("Planning = %q, want claude:opus", m.Planning)
	}
	if m.Implementation != "claude:opus" {
		t.Errorf("Implementation = %q, want claude:opus", m.Implementation)
	}
	if m.Review != "codex:codex" {
		t.Errorf("Review = %q, want codex:codex", m.Review)
	}
	if m.Utilities != "claude:sonnet" {
		t.Errorf("Utilities = %q, want claude:sonnet", m.Utilities)
	}
	if m.KBBuild != "claude:opus" {
		t.Errorf("KBBuild = %q, want claude:opus", m.KBBuild)
	}
}

func TestApplyProviderDefaults_PreservesUserSetValues(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Models.Research = "haiku"
	cfg.Defaults.Models.Utilities = "opus"
	cfg.Defaults.Models.KBBuild = "sonnet"

	defaults := map[string]string{
		"research":       "claude:opus",
		"planning":       "claude:opus",
		"implementation": "claude:opus",
		"review":         "codex:codex",
		"chat":           "claude:sonnet",
		"kb_build":       "claude:opus",
	}
	ApplyProviderDefaults(cfg, defaults)

	m := cfg.Defaults.Models
	if m.Research != "haiku" {
		t.Errorf("Research = %q, want haiku (user-set)", m.Research)
	}
	if m.Utilities != "opus" {
		t.Errorf("Utilities = %q, want opus (user-set)", m.Utilities)
	}
	if m.KBBuild != "sonnet" {
		t.Errorf("KBBuild = %q, want sonnet (user-set)", m.KBBuild)
	}
	// Non-user-set fields should be filled.
	if m.Planning != "claude:opus" {
		t.Errorf("Planning = %q, want claude:opus (provider default)", m.Planning)
	}
}

func TestApplyProviderDefaults_SingleProvider_BareNames(t *testing.T) {
	cfg := &Config{}
	// Single-provider scenario: bare names without provider prefix.
	defaults := map[string]string{
		"research":       "opus",
		"planning":       "opus",
		"implementation": "opus",
		"review":         "opus",
		"chat":           "sonnet",
		"kb_build":       "opus",
	}
	ApplyProviderDefaults(cfg, defaults)

	if cfg.Defaults.Models.Research != "opus" {
		t.Errorf("Research = %q, want opus (bare name)", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.KBBuild != "opus" {
		t.Errorf("KBBuild = %q, want opus (bare name)", cfg.Defaults.Models.KBBuild)
	}
}

func TestApplyProviderDefaults_MultiProvider_PrefixedNames(t *testing.T) {
	cfg := &Config{}
	// Multi-provider scenario: provider:model format.
	defaults := map[string]string{
		"research":       "claude:opus",
		"planning":       "claude:opus",
		"implementation": "claude:opus",
		"review":         "codex:codex",
		"chat":           "claude:sonnet",
		"kb_build":       "claude:opus",
	}
	ApplyProviderDefaults(cfg, defaults)

	if cfg.Defaults.Models.Research != "claude:opus" {
		t.Errorf("Research = %q, want claude:opus", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Review != "codex:codex" {
		t.Errorf("Review = %q, want codex:codex", cfg.Defaults.Models.Review)
	}
	if cfg.Defaults.Models.KBBuild != "claude:opus" {
		t.Errorf("KBBuild = %q, want claude:opus", cfg.Defaults.Models.KBBuild)
	}
}

func TestApplyProviderDefaults_NilDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Models.Research = "opus"
	// Should not panic with nil defaults.
	ApplyProviderDefaults(cfg, nil)
	if cfg.Defaults.Models.Research != "opus" {
		t.Errorf("Research = %q, want opus (unchanged)", cfg.Defaults.Models.Research)
	}
}

func TestApplyProviderDefaults_EmptyDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Models.Research = "opus"
	// Empty map: no changes.
	ApplyProviderDefaults(cfg, map[string]string{})
	if cfg.Defaults.Models.Research != "opus" {
		t.Errorf("Research = %q, want opus (unchanged)", cfg.Defaults.Models.Research)
	}
	if cfg.Defaults.Models.Planning != "" {
		t.Errorf("Planning = %q, want empty (no default available)", cfg.Defaults.Models.Planning)
	}
}

func TestLoadOrCreateWithStatus_NewConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg, isNew, err := LoadOrCreateWithStatus(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Error("expected isNew=true for non-existent config")
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// File should now exist
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("config file was not created on disk")
	}
}

func TestLoadOrCreateWithStatus_ExistingConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Create a config first
	initial := NewDefault()
	initial.Defaults.Models.Research = "custom-model"
	if err := Save(path, initial); err != nil {
		t.Fatalf("failed to save initial config: %v", err)
	}

	cfg, isNew, err := LoadOrCreateWithStatus(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Error("expected isNew=false for existing config")
	}
	if cfg.Defaults.Models.Research != "custom-model" {
		t.Errorf("expected Research=%q, got %q", "custom-model", cfg.Defaults.Models.Research)
	}
}

func TestMuteFeatureInputNotificationConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte(`defaults:
  models:
    research: opus
    planning: opus
    implementation: opus
    review: codex
notifications:
  mute_feature_input: true
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !loaded.Notifications.MuteFeatureInput {
		t.Error("expected mute_feature_input to be true after loading config")
	}

	loaded.Notifications.MuteFeatureInput = false
	path2 := filepath.Join(dir, "config2.yaml")
	if err := Save(path2, loaded); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded2, err := Load(path2)
	if err != nil {
		t.Fatalf("load second file: %v", err)
	}
	if loaded2.Notifications.MuteFeatureInput {
		t.Error("expected mute_feature_input to be false after round-trip")
	}
}

// --- helpers for workspace roots tests ---

func createFakeGitRepo(t *testing.T, parentDir, name string) string {
	t.Helper()
	repoDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repoDir
}

func createFakeGitWorktreeRepo(t *testing.T, parentDir, name string) string {
	t.Helper()
	repoDir := filepath.Join(parentDir, name)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, ".git"), []byte("gitdir: /tmp/main/.git/worktrees/"+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return repoDir
}

// --- workspace roots tests ---

func TestWorkspaceRootsYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.WorkspaceRoots = []string{"/a", "/b"}

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if len(loaded.WorkspaceRoots) != 2 {
		t.Fatalf("expected 2 workspace roots, got %d", len(loaded.WorkspaceRoots))
	}
	if loaded.WorkspaceRoots[0] != "/a" || loaded.WorkspaceRoots[1] != "/b" {
		t.Errorf("workspace roots mismatch: got %v", loaded.WorkspaceRoots)
	}
}

func TestWorkspaceRootsOmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte(`defaults:
  models:
    research: opus
    planning: opus
    implementation: opus
    review: codex
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.WorkspaceRoots) != 0 {
		t.Errorf("expected empty workspace_roots, got %v", loaded.WorkspaceRoots)
	}
}

func TestDiscoveredReposNotPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.WorkspaceRoots = []string{"/some/root"}
	original.DiscoveredRepos = map[string]RepoConfig{
		"discovered-repo": {Path: "/some/root/discovered-repo"},
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// WorkspaceRoots should persist
	if len(loaded.WorkspaceRoots) != 1 || loaded.WorkspaceRoots[0] != "/some/root" {
		t.Errorf("expected workspace_roots to persist, got %v", loaded.WorkspaceRoots)
	}

	// DiscoveredRepos should NOT persist (yaml:"-")
	if len(loaded.DiscoveredRepos) != 0 {
		t.Errorf("expected DiscoveredRepos to not persist, got %v", loaded.DiscoveredRepos)
	}
}

func TestDiscoverReposFromRoots(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	createFakeGitRepo(t, rootA, "alpha")
	createFakeGitRepo(t, rootA, "beta")
	createFakeGitRepo(t, rootB, "gamma")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{rootA, rootB}

	n := DiscoverReposFromRoots(cfg)
	if n != 3 {
		t.Fatalf("expected 3 discovered repos, got %d", n)
	}

	for _, name := range []string{"alpha", "beta", "gamma"} {
		if _, ok := cfg.DiscoveredRepos[name]; !ok {
			t.Errorf("expected %q in DiscoveredRepos", name)
		}
	}

	// cfg.Repos should remain unchanged
	if len(cfg.Repos) != 0 {
		t.Errorf("expected cfg.Repos to remain empty, got %d entries", len(cfg.Repos))
	}
}

func TestDiscoverReposFromRootsAcceptsWorktreeGitFile(t *testing.T) {
	root := t.TempDir()
	repoPath := createFakeGitWorktreeRepo(t, root, "worktree-repo")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{root}

	n := DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo, got %d", n)
	}
	if got := cfg.DiscoveredRepos["worktree-repo"].Path; got != repoPath {
		t.Fatalf("discovered worktree path = %q, want %q", got, repoPath)
	}
}

func TestDiscoverReposFromRootsRebuild(t *testing.T) {
	root := t.TempDir()
	createFakeGitRepo(t, root, "svc-a")
	createFakeGitRepo(t, root, "svc-b")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{root}

	n := DiscoverReposFromRoots(cfg)
	if n != 2 {
		t.Fatalf("expected 2 discovered repos, got %d", n)
	}

	// Remove svc-b
	if err := os.RemoveAll(filepath.Join(root, "svc-b")); err != nil {
		t.Fatal(err)
	}

	n = DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo after removal, got %d", n)
	}
	if _, ok := cfg.DiscoveredRepos["svc-a"]; !ok {
		t.Error("expected svc-a to remain in DiscoveredRepos")
	}
	if _, ok := cfg.DiscoveredRepos["svc-b"]; ok {
		t.Error("expected svc-b to be gone from DiscoveredRepos")
	}
}

func TestDiscoverReposFromRootsClearsOnEmptyRoots(t *testing.T) {
	root := t.TempDir()
	createFakeGitRepo(t, root, "repo-x")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{root}

	n := DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo, got %d", n)
	}

	// Set roots to nil and discover again
	cfg.WorkspaceRoots = nil
	n = DiscoverReposFromRoots(cfg)
	if n != 0 {
		t.Fatalf("expected 0 discovered repos after clearing roots, got %d", n)
	}
	if len(cfg.DiscoveredRepos) != 0 {
		t.Errorf("expected DiscoveredRepos to be empty, got %d entries", len(cfg.DiscoveredRepos))
	}
}

func TestDiscoverReposFromRootsDedup(t *testing.T) {
	root := t.TempDir()
	createFakeGitRepo(t, root, "my-service")

	cfg := NewDefault()
	// Same root listed twice
	cfg.WorkspaceRoots = []string{root, root}

	n := DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo (no duplicates), got %d", n)
	}
	if _, ok := cfg.DiscoveredRepos["my-service"]; !ok {
		t.Error("expected my-service in DiscoveredRepos")
	}
}

func TestDiscoverReposFromRootsExplicitPriority(t *testing.T) {
	root := t.TempDir()
	createFakeGitRepo(t, root, "my-repo")

	cfg := NewDefault()
	cfg.Repos["my-repo"] = RepoConfig{Path: "/explicit/path"}
	cfg.WorkspaceRoots = []string{root}

	n := DiscoverReposFromRoots(cfg)
	if n != 0 {
		t.Fatalf("expected 0 discovered repos (explicit takes priority), got %d", n)
	}
	if _, ok := cfg.DiscoveredRepos["my-repo"]; ok {
		t.Error("expected my-repo NOT in DiscoveredRepos since it is in explicit Repos")
	}
}

func TestAllReposExplicitOverridesDiscovered(t *testing.T) {
	cfg := NewDefault()
	cfg.DiscoveredRepos = map[string]RepoConfig{
		"svc": {Path: "/discovered/svc"},
	}
	cfg.Repos["svc"] = RepoConfig{Path: "/explicit/svc"}

	merged := AllRepos(cfg)
	if merged["svc"].Path != "/explicit/svc" {
		t.Errorf("expected explicit path /explicit/svc, got %s", merged["svc"].Path)
	}
}

func TestAllReposMergesBoth(t *testing.T) {
	cfg := NewDefault()
	cfg.DiscoveredRepos = map[string]RepoConfig{
		"discovered-svc": {Path: "/discovered/svc"},
	}
	cfg.Repos["explicit-svc"] = RepoConfig{Path: "/explicit/svc"}

	merged := AllRepos(cfg)
	if len(merged) != 2 {
		t.Fatalf("expected 2 merged repos, got %d", len(merged))
	}
	if _, ok := merged["discovered-svc"]; !ok {
		t.Error("expected discovered-svc in merged repos")
	}
	if _, ok := merged["explicit-svc"]; !ok {
		t.Error("expected explicit-svc in merged repos")
	}
}

func TestRepoNameCollision(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	createFakeGitRepo(t, rootA, "service")
	createFakeGitRepo(t, rootB, "service")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{rootA, rootB}

	n := DiscoverReposFromRoots(cfg)
	if n != 2 {
		t.Fatalf("expected 2 discovered repos for collision, got %d", n)
	}

	// Both should have qualified names: rootBasename/service
	rootABase := filepath.Base(rootA)
	rootBBase := filepath.Base(rootB)

	qualifiedA := rootABase + "/service"
	qualifiedB := rootBBase + "/service"

	if _, ok := cfg.DiscoveredRepos[qualifiedA]; !ok {
		t.Errorf("expected qualified name %q in DiscoveredRepos, keys: %v", qualifiedA, discoveredKeys(cfg))
	}
	if _, ok := cfg.DiscoveredRepos[qualifiedB]; !ok {
		t.Errorf("expected qualified name %q in DiscoveredRepos, keys: %v", qualifiedB, discoveredKeys(cfg))
	}

	// Plain "service" should NOT exist
	if _, ok := cfg.DiscoveredRepos["service"]; ok {
		t.Error("expected plain 'service' NOT in DiscoveredRepos when there is a collision")
	}
}

func TestRepoNameCollisionSameBasename(t *testing.T) {
	// Regression test: two roots share the same basename ("projects") but
	// live under different parent directories. Without the uniqueRootPrefix
	// fix, both would produce the key "projects/service" and one would
	// silently overwrite the other.
	tmpDir := t.TempDir()

	rootA := filepath.Join(tmpDir, "src", "projects")
	rootB := filepath.Join(tmpDir, "backup", "projects")

	if err := os.MkdirAll(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootB, 0o755); err != nil {
		t.Fatal(err)
	}

	createFakeGitRepo(t, rootA, "service")
	createFakeGitRepo(t, rootB, "service")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{rootA, rootB}

	n := DiscoverReposFromRoots(cfg)
	if n != 2 {
		t.Fatalf("expected 2 discovered repos, got %d (keys: %v)", n, discoveredKeys(cfg))
	}

	// Keys must use enough path components to be distinct.
	keyA := "src/projects/service"
	keyB := "backup/projects/service"

	repoA, okA := cfg.DiscoveredRepos[keyA]
	repoB, okB := cfg.DiscoveredRepos[keyB]

	if !okA {
		t.Errorf("expected key %q in DiscoveredRepos, keys: %v", keyA, discoveredKeys(cfg))
	}
	if !okB {
		t.Errorf("expected key %q in DiscoveredRepos, keys: %v", keyB, discoveredKeys(cfg))
	}

	// Verify each key maps to the correct path.
	expectedPathA := filepath.Join(rootA, "service")
	expectedPathB := filepath.Join(rootB, "service")

	if okA && repoA.Path != expectedPathA {
		t.Errorf("key %q: expected path %s, got %s", keyA, expectedPathA, repoA.Path)
	}
	if okB && repoB.Path != expectedPathB {
		t.Errorf("key %q: expected path %s, got %s", keyB, expectedPathB, repoB.Path)
	}

	// Plain "service" and single-component "projects/service" should NOT exist.
	if _, ok := cfg.DiscoveredRepos["service"]; ok {
		t.Error("expected plain 'service' NOT in DiscoveredRepos when there is a collision")
	}
	if _, ok := cfg.DiscoveredRepos["projects/service"]; ok {
		t.Error("expected 'projects/service' NOT in DiscoveredRepos (ambiguous basename)")
	}
}

func discoveredKeys(cfg *Config) []string {
	keys := make([]string, 0, len(cfg.DiscoveredRepos))
	for k := range cfg.DiscoveredRepos {
		keys = append(keys, k)
	}
	return keys
}

func TestDiscoverReposFromRootsUnreadableRoot(t *testing.T) {
	validRoot := t.TempDir()
	createFakeGitRepo(t, validRoot, "valid-repo")

	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{"/nonexistent/path/xyz", validRoot}

	n := DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo (from valid root), got %d", n)
	}
	if _, ok := cfg.DiscoveredRepos["valid-repo"]; !ok {
		t.Error("expected valid-repo in DiscoveredRepos")
	}
}

func TestDiscoverReposFromRootsTildeExpansion(t *testing.T) {
	// We can't test actual ~/ expansion without writing to the user's home dir.
	// Instead, create a symlink that simulates the expansion path.
	tmpDir := t.TempDir()
	actualRoot := filepath.Join(tmpDir, "actual-root")
	if err := os.MkdirAll(actualRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	createFakeGitRepo(t, actualRoot, "tilde-repo")

	// Create a symlink from "fakeHome/myprojects" -> actualRoot
	fakeHome := filepath.Join(tmpDir, "fakeHome")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(fakeHome, "myprojects")
	if err := os.Symlink(actualRoot, symlink); err != nil {
		t.Fatal(err)
	}

	// Use the symlinked path as a workspace root (no actual ~/, but proves
	// that ExpandHome is called and the path resolves correctly).
	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{symlink}

	n := DiscoverReposFromRoots(cfg)
	if n != 1 {
		t.Fatalf("expected 1 discovered repo via symlink, got %d", n)
	}
	if _, ok := cfg.DiscoveredRepos["tilde-repo"]; !ok {
		t.Error("expected tilde-repo in DiscoveredRepos")
	}
}

func TestDiscoverReposFromRootsEmpty(t *testing.T) {
	cfg := NewDefault()
	cfg.WorkspaceRoots = []string{}

	n := DiscoverReposFromRoots(cfg)
	if n != 0 {
		t.Fatalf("expected 0 discovered repos for empty roots, got %d", n)
	}
	if len(cfg.DiscoveredRepos) != 0 {
		t.Errorf("expected DiscoveredRepos to be empty, got %d entries", len(cfg.DiscoveredRepos))
	}
}

func TestExpandHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create test HOME: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, result string)
	}{
		{
			name:  "tilde path is expanded",
			input: "~/some/path",
			check: func(t *testing.T, result string) {
				t.Helper()
				if strings.HasPrefix(result, "~/") {
					t.Errorf("expected ~/ to be expanded, got %s", result)
				}
				expected := filepath.Join(home, "some/path")
				if result != expected {
					t.Errorf("expected %s, got %s", expected, result)
				}
			},
		},
		{
			name:  "non-tilde path unchanged",
			input: "/absolute/path",
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != "/absolute/path" {
					t.Errorf("expected /absolute/path, got %s", result)
				}
			},
		},
		{
			name:  "relative path unchanged",
			input: "relative/path",
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != "relative/path" {
					t.Errorf("expected relative/path, got %s", result)
				}
			},
		},
		{
			name:  "tilde only without slash unchanged",
			input: "~notapath",
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != "~notapath" {
					t.Errorf("expected ~notapath, got %s", result)
				}
			},
		},
		{
			name:  "empty string unchanged",
			input: "",
			check: func(t *testing.T, result string) {
				t.Helper()
				if result != "" {
					t.Errorf("expected empty string, got %s", result)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandHome(tt.input)
			tt.check(t, result)
		})
	}
}

// --- pipeline gates tests ---

func TestRepoConfigPipelineGatesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Repos["my-repo"] = RepoConfig{
		Path: "/tmp/my-repo",
		PipelineGates: map[string]Checkpoints{
			"medium": {ManualPublish: false},
		},
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	rc, ok := loaded.Repos["my-repo"]
	if !ok {
		t.Fatal("my-repo not found in loaded config")
	}
	if rc.PipelineGates == nil {
		t.Fatal("expected PipelineGates to be non-nil")
	}
	medium, ok := rc.PipelineGates["medium"]
	if !ok {
		t.Fatal("expected 'medium' key in PipelineGates")
	}
	if medium.ManualPublish {
		t.Error("expected ManualPublish=false for medium pipeline gate")
	}
}

func TestRepoPipelineGatesSparseCheckpointDefaultsOn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte(`repos:
  my-repo:
    path: /tmp/my-repo
    pipeline_gates:
      medium:
        manual_publish: false
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := cfg.Repos["my-repo"].PipelineGates["medium"]
	if !got.InquiryReview || !got.ResearchReview || !got.DesignReview || !got.RoadmapReview || !got.PhasePlanReview || got.ManualPublish {
		t.Fatalf("sparse repo pipeline gate should default omitted checkpoints on and preserve manual_publish=false, got %+v", got)
	}
}

func TestRepoConfigPipelineGatesEmpty(t *testing.T) {
	cfg := &Config{
		Repos: map[string]RepoConfig{
			"bare-repo": {Path: "/tmp/bare-repo"},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	yamlStr := string(data)
	if strings.Contains(yamlStr, "pipeline_gates") {
		t.Errorf("expected no pipeline_gates key in YAML when PipelineGates is nil, got:\n%s", yamlStr)
	}
}

func TestDefaultsPipelineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Defaults.Pipeline = "medium"

	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.Defaults.Pipeline != "medium" {
		t.Errorf("expected pipeline 'medium' after round-trip, got %q", loaded.Defaults.Pipeline)
	}
}

func TestDefaultsPipelineEmptyDefaultsToLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Config YAML with no pipeline field.
	content := []byte("defaults:\n  max_iterations: 5\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Defaults.Pipeline != "large" {
		t.Errorf("expected pipeline to default to 'large', got %q", cfg.Defaults.Pipeline)
	}
}

func TestDefaultsPipelinePreservesExisting(t *testing.T) {
	cfg := &Config{}
	cfg.Defaults.Pipeline = "medium"
	applyDefaults(cfg)

	if cfg.Defaults.Pipeline != "medium" {
		t.Errorf("expected pipeline to be preserved as 'medium', got %q", cfg.Defaults.Pipeline)
	}
}

func TestNewDefault_Utilities(t *testing.T) {
	cfg := NewDefault()
	if cfg.Defaults.Models.Utilities != "sonnet[200K]" {
		t.Errorf("expected Utilities default sonnet[200K], got %s", cfg.Defaults.Models.Utilities)
	}
}

func TestMigrateModelConfig_ChatToUtilities(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Legacy config with "chat" key
	content := []byte("defaults:\n  models:\n    research: opus\n    chat: haiku\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.Models.Utilities != "haiku" {
		t.Errorf("expected Utilities=haiku (migrated from chat), got %q", cfg.Defaults.Models.Utilities)
	}
}

func TestMigrateModelConfig_UtilitiesTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Config with both "chat" and "utilities" keys
	content := []byte("defaults:\n  models:\n    chat: haiku\n    utilities: opus\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.Models.Utilities != "opus" {
		t.Errorf("expected Utilities=opus (takes precedence over chat), got %q", cfg.Defaults.Models.Utilities)
	}
}

func TestMigrateModelConfig_NeitherSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Config with neither chat nor utilities
	content := []byte("defaults:\n  models:\n    research: opus\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// applyDefaults should fill it with "sonnet[200K]"
	if cfg.Defaults.Models.Utilities != "sonnet[200K]" {
		t.Errorf("expected Utilities=sonnet[200K] (from defaults), got %q", cfg.Defaults.Models.Utilities)
	}
}

func TestSaveWritesUtilitiesNotChat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := NewDefault()
	cfg.Defaults.Models.Utilities = "haiku"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	yamlStr := string(data)
	if !strings.Contains(yamlStr, "utilities:") {
		t.Errorf("expected YAML to contain 'utilities:' key, got:\n%s", yamlStr)
	}
	if strings.Contains(yamlStr, "chat:") {
		t.Errorf("expected YAML to NOT contain 'chat:' key, got:\n%s", yamlStr)
	}
}

func TestBackwardCompatRepoConfigWithoutPipelineGates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Old-format config: repos with only path, no pipeline_gates.
	content := []byte(`defaults:
  max_iterations: 5
repos:
  legacy-repo:
    path: /tmp/legacy-repo
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load old-format config: %v", err)
	}

	rc, ok := cfg.Repos["legacy-repo"]
	if !ok {
		t.Fatal("legacy-repo not found in loaded config")
	}
	if rc.Path != "/tmp/legacy-repo" {
		t.Errorf("expected path /tmp/legacy-repo, got %s", rc.Path)
	}
	if rc.PipelineGates != nil {
		t.Errorf("expected PipelineGates to be nil for old-format config, got %v", rc.PipelineGates)
	}
}

func TestObservabilityConfig(t *testing.T) {
	t.Run("absent_gets_defaults", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("repos: {}\n"), 0644)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		if !cfg.Observability.Events {
			t.Error("expected Observability.Events == true")
		}
		if cfg.Observability.OTelEnabled {
			t.Error("expected Observability.OTelEnabled == false")
		}
		if cfg.Observability.OTelServiceName != "agentico" {
			t.Errorf("OTelServiceName = %q, want agentico", cfg.Observability.OTelServiceName)
		}
	})

	t.Run("explicit_events_false", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("observability:\n  events: false\n"), 0644)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		if cfg.Observability.Events {
			t.Error("expected Observability.Events == false")
		}
	})

	t.Run("present_but_empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("observability: {}\n"), 0644)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		// UnmarshalYAML pre-sets Events to true
		if !cfg.Observability.Events {
			t.Error("expected Observability.Events == true for empty section")
		}
	})

	t.Run("custom_service_name", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")
		os.WriteFile(cfgPath, []byte("observability:\n  otel_service_name: my-app\n"), 0644)

		cfg, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load failed: %v", err)
		}
		if cfg.Observability.OTelServiceName != "my-app" {
			t.Errorf("OTelServiceName = %q, want my-app", cfg.Observability.OTelServiceName)
		}
	})

	t.Run("new_default_has_observability", func(t *testing.T) {
		cfg := NewDefault()
		if !cfg.Observability.Events {
			t.Error("expected NewDefault().Observability.Events == true")
		}
		if cfg.Observability.OTelServiceName != "agentico" {
			t.Errorf("OTelServiceName = %q, want agentico", cfg.Observability.OTelServiceName)
		}
		if cfg.Observability.OTelEnabled {
			t.Error("expected OTelEnabled == false")
		}
	})

	t.Run("load_or_create_first_run_round_trip", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgPath := filepath.Join(tmpDir, "subdir", "config.yaml")

		cfg, isNew, err := LoadOrCreateWithStatus(cfgPath)
		if err != nil {
			t.Fatalf("LoadOrCreateWithStatus failed: %v", err)
		}
		if !isNew {
			t.Error("expected isNew == true")
		}
		if !cfg.Observability.Events {
			t.Error("expected Observability.Events == true on first run")
		}
		if cfg.Observability.OTelServiceName != "agentico" {
			t.Errorf("OTelServiceName = %q, want agentico", cfg.Observability.OTelServiceName)
		}

		// Round-trip: re-load the saved file
		reloaded, err := Load(cfgPath)
		if err != nil {
			t.Fatalf("Load after create failed: %v", err)
		}
		if !reloaded.Observability.Events {
			t.Error("expected Observability.Events == true after round-trip")
		}
		if reloaded.Observability.OTelServiceName != "agentico" {
			t.Errorf("round-trip OTelServiceName = %q, want agentico", reloaded.Observability.OTelServiceName)
		}
	})
}

func TestProviderCLI_OverrideAndFallback(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]ProviderConfig
		key       string
		fallback  string
		want      string
	}{
		{
			name:      "override set returns override",
			providers: map[string]ProviderConfig{"claude": {CLI: "fcc-claude"}},
			key:       "claude",
			fallback:  "claude",
			want:      "fcc-claude",
		},
		{
			name:      "empty CLI returns fallback",
			providers: map[string]ProviderConfig{"claude": {CLI: ""}},
			key:       "claude",
			fallback:  "claude",
			want:      "claude",
		},
		{
			name:      "whitespace CLI returns fallback",
			providers: map[string]ProviderConfig{"claude": {CLI: "   "}},
			key:       "claude",
			fallback:  "claude",
			want:      "claude",
		},
		{
			name:      "key absent returns fallback",
			providers: map[string]ProviderConfig{"codex": {CLI: "my-codex"}},
			key:       "claude",
			fallback:  "claude",
			want:      "claude",
		},
		{
			name:      "nil providers map returns fallback",
			providers: nil,
			key:       "claude",
			fallback:  "claude",
			want:      "claude",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Providers: tc.providers}
			if got := cfg.ProviderCLI(tc.key, tc.fallback); got != tc.want {
				t.Errorf("ProviderCLI(%q, %q) = %q, want %q", tc.key, tc.fallback, got, tc.want)
			}
		})
	}
}

func TestProvidersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	original := NewDefault()
	original.Providers = map[string]ProviderConfig{"claude": {CLI: "fcc-claude"}}
	if err := Save(path, original); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := loaded.ProviderCLI("claude", "claude"); got != "fcc-claude" {
		t.Errorf("providers.claude.cli mismatch after round-trip: got %q, want fcc-claude", got)
	}
}

func TestProvidersEmptyOmittedFromYAML(t *testing.T) {
	data, err := yaml.Marshal(NewDefault())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "providers:") {
		t.Errorf("empty Providers map should be omitted from YAML, got:\n%s", data)
	}
}
