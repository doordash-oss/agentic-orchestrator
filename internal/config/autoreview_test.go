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
)

func TestAutomaticReviewDefaultsDisabledAndEmpty(t *testing.T) {
	cfg := NewDefault()
	if cfg.Defaults.AutomaticReviewEnabled {
		t.Errorf("NewDefault AutomaticReviewEnabled = true, want false")
	}
	if cfg.Defaults.Models.AutomaticReview != "" {
		t.Errorf("NewDefault AutomaticReview = %q, want empty (Automatic)", cfg.Defaults.Models.AutomaticReview)
	}
}

func TestAutomaticReviewAbsentLegacyLoadsDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	legacy := `
defaults:
  models:
    inquiry: sonnet
    research: sonnet
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Defaults.AutomaticReviewEnabled {
		t.Errorf("absent legacy AutomaticReviewEnabled = true, want false")
	}
	if cfg.Defaults.Models.AutomaticReview != "" {
		t.Errorf("absent legacy AutomaticReview = %q, want empty", cfg.Defaults.Models.AutomaticReview)
	}
	// Established defaults must not be altered.
	if cfg.Defaults.Models.Inquiry != "sonnet" {
		t.Errorf("Inquiry = %q, want sonnet", cfg.Defaults.Models.Inquiry)
	}
}

func TestAutomaticReviewRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := NewDefault()
	cfg.Defaults.AutomaticReviewEnabled = true
	cfg.Defaults.Models.AutomaticReview = "claude:haiku[200K]"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Defaults.AutomaticReviewEnabled {
		t.Errorf("round-trip AutomaticReviewEnabled = false, want true")
	}
	if loaded.Defaults.Models.AutomaticReview != "claude:haiku[200K]" {
		t.Errorf("round-trip AutomaticReview = %q, want claude:haiku[200K]", loaded.Defaults.Models.AutomaticReview)
	}
}

func TestAutomaticReviewFalseAndEmptyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := NewDefault()
	cfg.Defaults.AutomaticReviewEnabled = false
	cfg.Defaults.Models.AutomaticReview = ""
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Defaults.AutomaticReviewEnabled {
		t.Errorf("round-trip false AutomaticReviewEnabled = true, want false")
	}
	if loaded.Defaults.Models.AutomaticReview != "" {
		t.Errorf("round-trip empty AutomaticReview = %q, want empty", loaded.Defaults.Models.AutomaticReview)
	}
}

func TestAutomaticReviewNotSeededByApplyDefaults(t *testing.T) {
	// An empty AutomaticReview must survive startup defaulting unchanged — no
	// inferred Claude model is written back.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := NewDefault()
	cfg.Defaults.Models.AutomaticReview = ""
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Defaults.Models.AutomaticReview != "" {
		t.Errorf("applyDefaults seeded AutomaticReview = %q, want empty", loaded.Defaults.Models.AutomaticReview)
	}
}

func TestAutomaticReviewYAMLKeyDecoded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
defaults:
  models:
    research: sonnet
    automatic_review: haiku[200K]
  automatic_review_enabled: true
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Defaults.AutomaticReviewEnabled {
		t.Errorf("AutomaticReviewEnabled = false, want true")
	}
	if cfg.Defaults.Models.AutomaticReview != "haiku[200K]" {
		t.Errorf("AutomaticReview = %q, want haiku[200K]", cfg.Defaults.Models.AutomaticReview)
	}
	// UnmarshalYAML migration still defaults Inquiry from Research.
	if cfg.Defaults.Models.Inquiry != "sonnet" {
		t.Errorf("Inquiry = %q, want sonnet (migrated)", cfg.Defaults.Models.Inquiry)
	}
}

func TestAutomaticReviewPersistsOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := NewDefault()
	cfg.Defaults.AutomaticReviewEnabled = true
	cfg.Defaults.Models.AutomaticReview = "haiku[200K]"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "automatic_review_enabled: true") {
		t.Errorf("disk config missing automatic_review_enabled: true:\n%s", string(data))
	}
	if !strings.Contains(string(data), "automatic_review: haiku[200K]") {
		t.Errorf("disk config missing automatic_review: haiku[200K]:\n%s", string(data))
	}
}
