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

package observe

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

func TestWriteFeatureSummaryFromEvents(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "summary_from_events"

	// Write synthetic events.jsonl
	eventsPath := filepath.Join(featureDir, "events.jsonl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatalf("creating events.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)

	now := time.Now()

	// session.ended for phase "implement" with token/cost data
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span1",
		EventType: "session.ended",
		Phase:     "implement",
		Status:    "ok",
		FeatureID: featureID,
		SessionID: "sess-1",
		Data: map[string]any{
			"input_tokens":                float64(1000),
			"output_tokens":               float64(500),
			"cache_read_input_tokens":     float64(200),
			"cache_creation_input_tokens": float64(100),
			"total_cost_usd":              float64(0.50),
		},
	}); err != nil {
		t.Fatalf("writing session.ended event: %v", err)
	}

	// iteration.started for phase "implement"
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span2",
		EventType: "iteration.started",
		Phase:     "implement",
		FeatureID: featureID,
		Iteration: 1,
	}); err != nil {
		t.Fatalf("writing iteration.started event: %v", err)
	}

	// review.completed for phase "implement"
	if err := enc.Encode(Event{
		Timestamp:  now,
		TraceID:    "trace1",
		SpanID:     "span3",
		EventType:  "review.completed",
		Phase:      "implement",
		Status:     "APPROVED",
		FeatureID:  featureID,
		Iteration:  1,
		DurationMs: 10000,
	}); err != nil {
		t.Fatalf("writing review.completed event: %v", err)
	}
	f.Close()

	// Build input with metadata-driven phase timings and costs
	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "test-feature",
		Status:     "done",
		PhaseTimings: map[string]time.Duration{
			"implement": 45 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 1.25,
		},
	}

	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	// Read and unmarshal the output YAML
	yamlPath := filepath.Join(featureDir, "observe-summary.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading observe-summary.yaml: %v", err)
	}

	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary YAML: %v", err)
	}

	// Feature block
	if summary.Feature.ID != featureID {
		t.Errorf("Feature.ID = %q, want %q", summary.Feature.ID, featureID)
	}
	if summary.Feature.Name != "test-feature" {
		t.Errorf("Feature.Name = %q, want %q", summary.Feature.Name, "test-feature")
	}
	if summary.Feature.Status != "done" {
		t.Errorf("Feature.Status = %q, want %q", summary.Feature.Status, "done")
	}

	// Totals: duration and cost from metadata
	if summary.Totals.DurationMS != 45000 {
		t.Errorf("Totals.DurationMS = %d, want 45000", summary.Totals.DurationMS)
	}
	if summary.Totals.CostUSD != 1.25 {
		t.Errorf("Totals.CostUSD = %v, want 1.25", summary.Totals.CostUSD)
	}

	// Totals: tokens from events
	if summary.Totals.InputTokens != 1000 {
		t.Errorf("Totals.InputTokens = %d, want 1000", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 500 {
		t.Errorf("Totals.OutputTokens = %d, want 500", summary.Totals.OutputTokens)
	}
	if summary.Totals.CacheReadTokens != 200 {
		t.Errorf("Totals.CacheReadTokens = %d, want 200", summary.Totals.CacheReadTokens)
	}
	if summary.Totals.CacheWriteTokens != 100 {
		t.Errorf("Totals.CacheWriteTokens = %d, want 100", summary.Totals.CacheWriteTokens)
	}

	// Totals: iteration and review counts from events
	if summary.Totals.Iterations != 1 {
		t.Errorf("Totals.Iterations = %d, want 1", summary.Totals.Iterations)
	}
	if summary.Totals.Reviews != 1 {
		t.Errorf("Totals.Reviews = %d, want 1", summary.Totals.Reviews)
	}

	// Phase "implement"
	ps, ok := summary.Phases["implement"]
	if !ok {
		t.Fatal("expected phases to contain 'implement'")
	}
	if ps.DurationMS != 45000 {
		t.Errorf("Phase[implement].DurationMS = %d, want 45000", ps.DurationMS)
	}
	if ps.CostUSD != 1.25 {
		t.Errorf("Phase[implement].CostUSD = %v, want 1.25", ps.CostUSD)
	}
	if ps.InputTokens != 1000 {
		t.Errorf("Phase[implement].InputTokens = %d, want 1000", ps.InputTokens)
	}
	if ps.OutputTokens != 500 {
		t.Errorf("Phase[implement].OutputTokens = %d, want 500", ps.OutputTokens)
	}
	if ps.Iterations != 1 {
		t.Errorf("Phase[implement].Iterations = %d, want 1", ps.Iterations)
	}
	if ps.Reviews != 1 {
		t.Errorf("Phase[implement].Reviews = %d, want 1", ps.Reviews)
	}
}

func TestWriteFeatureSummaryMultiRepo(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "summary_multi_repo"

	// Write events with repo-scoped data
	eventsPath := filepath.Join(featureDir, "events.jsonl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatalf("creating events.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)
	now := time.Now()

	// session.ended for repo-a
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span1",
		EventType: "session.ended",
		Phase:     "implement",
		Status:    "ok",
		FeatureID: featureID,
		SessionID: "sess-a",
		RepoName:  "repo-a",
		Data: map[string]any{
			"input_tokens":   float64(800),
			"output_tokens":  float64(300),
			"total_cost_usd": float64(0.40),
		},
	}); err != nil {
		t.Fatalf("writing repo-a session.ended: %v", err)
	}

	// session.ended for repo-b
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span2",
		EventType: "session.ended",
		Phase:     "implement",
		Status:    "ok",
		FeatureID: featureID,
		SessionID: "sess-b",
		RepoName:  "repo-b",
		Data: map[string]any{
			"input_tokens":   float64(1200),
			"output_tokens":  float64(600),
			"total_cost_usd": float64(0.80),
		},
	}); err != nil {
		t.Fatalf("writing repo-b session.ended: %v", err)
	}

	// repo.status_changed for repo-a
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span3",
		EventType: "repo.status_changed",
		FeatureID: featureID,
		RepoName:  "repo-a",
		Data: map[string]any{
			"from_status": "implementing",
			"to_status":   "published",
		},
	}); err != nil {
		t.Fatalf("writing repo-a status_changed: %v", err)
	}

	// repo.status_changed for repo-b
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span4",
		EventType: "repo.status_changed",
		FeatureID: featureID,
		RepoName:  "repo-b",
		Data: map[string]any{
			"from_status": "implementing",
			"to_status":   "published",
		},
	}); err != nil {
		t.Fatalf("writing repo-b status_changed: %v", err)
	}
	f.Close()

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "multi-repo-feature",
		Status:     "done",
		PhaseTimings: map[string]time.Duration{
			"implement": 60 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 1.20,
		},
		RepoStates: map[string]RepoSummaryInput{
			"repo-a": {Status: "published", Iteration: 2},
			"repo-b": {Status: "published", Iteration: 1},
		},
	}

	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	yamlPath := filepath.Join(featureDir, "observe-summary.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading observe-summary.yaml: %v", err)
	}

	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary YAML: %v", err)
	}

	// Assert repos section has both repos
	if len(summary.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(summary.Repos))
	}

	repoA, ok := summary.Repos["repo-a"]
	if !ok {
		t.Fatal("expected repos to contain 'repo-a'")
	}
	if repoA.Status != "published" {
		t.Errorf("Repos[repo-a].Status = %q, want %q", repoA.Status, "published")
	}
	if repoA.InputTokens != 800 {
		t.Errorf("Repos[repo-a].InputTokens = %d, want 800", repoA.InputTokens)
	}
	if repoA.OutputTokens != 300 {
		t.Errorf("Repos[repo-a].OutputTokens = %d, want 300", repoA.OutputTokens)
	}
	if repoA.CostUSD != 0.40 {
		t.Errorf("Repos[repo-a].CostUSD = %v, want 0.40", repoA.CostUSD)
	}

	repoB, ok := summary.Repos["repo-b"]
	if !ok {
		t.Fatal("expected repos to contain 'repo-b'")
	}
	if repoB.Status != "published" {
		t.Errorf("Repos[repo-b].Status = %q, want %q", repoB.Status, "published")
	}
	if repoB.InputTokens != 1200 {
		t.Errorf("Repos[repo-b].InputTokens = %d, want 1200", repoB.InputTokens)
	}
	if repoB.OutputTokens != 600 {
		t.Errorf("Repos[repo-b].OutputTokens = %d, want 600", repoB.OutputTokens)
	}
	if repoB.CostUSD != 0.80 {
		t.Errorf("Repos[repo-b].CostUSD = %v, want 0.80", repoB.CostUSD)
	}

	// Totals should aggregate tokens from both repos
	if summary.Totals.InputTokens != 2000 {
		t.Errorf("Totals.InputTokens = %d, want 2000", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 900 {
		t.Errorf("Totals.OutputTokens = %d, want 900", summary.Totals.OutputTokens)
	}
}

func TestWriteFeatureSummaryFallsBackWithoutEvents(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "summary_no_events"

	// Do NOT create events.jsonl

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "no-events-feature",
		Status:     "done",
		PhaseTimings: map[string]time.Duration{
			"research":  5 * time.Second,
			"implement": 30 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"research":  0.25,
			"implement": 1.50,
		},
		RepoStates: map[string]RepoSummaryInput{
			"my-repo": {Status: "published"},
		},
	}

	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	yamlPath := filepath.Join(featureDir, "observe-summary.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading observe-summary.yaml: %v", err)
	}

	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary YAML: %v", err)
	}

	// Totals: duration = 5s + 30s = 35s = 35000ms, cost = 0.25 + 1.50 = 1.75
	if summary.Totals.DurationMS != 35000 {
		t.Errorf("Totals.DurationMS = %d, want 35000", summary.Totals.DurationMS)
	}
	if summary.Totals.CostUSD != 1.75 {
		t.Errorf("Totals.CostUSD = %v, want 1.75", summary.Totals.CostUSD)
	}

	// Tokens should all be zero (omitted in YAML due to omitempty)
	if summary.Totals.InputTokens != 0 {
		t.Errorf("Totals.InputTokens = %d, want 0", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 0 {
		t.Errorf("Totals.OutputTokens = %d, want 0", summary.Totals.OutputTokens)
	}
	if summary.Totals.CacheReadTokens != 0 {
		t.Errorf("Totals.CacheReadTokens = %d, want 0", summary.Totals.CacheReadTokens)
	}
	if summary.Totals.CacheWriteTokens != 0 {
		t.Errorf("Totals.CacheWriteTokens = %d, want 0", summary.Totals.CacheWriteTokens)
	}

	// Phase entries have duration/cost but zero tokens
	researchPhase, ok := summary.Phases["research"]
	if !ok {
		t.Fatal("expected phases to contain 'research'")
	}
	if researchPhase.DurationMS != 5000 {
		t.Errorf("Phase[research].DurationMS = %d, want 5000", researchPhase.DurationMS)
	}
	if researchPhase.CostUSD != 0.25 {
		t.Errorf("Phase[research].CostUSD = %v, want 0.25", researchPhase.CostUSD)
	}
	if researchPhase.InputTokens != 0 {
		t.Errorf("Phase[research].InputTokens = %d, want 0", researchPhase.InputTokens)
	}
	if researchPhase.OutputTokens != 0 {
		t.Errorf("Phase[research].OutputTokens = %d, want 0", researchPhase.OutputTokens)
	}

	implPhase, ok := summary.Phases["implement"]
	if !ok {
		t.Fatal("expected phases to contain 'implement'")
	}
	if implPhase.DurationMS != 30000 {
		t.Errorf("Phase[implement].DurationMS = %d, want 30000", implPhase.DurationMS)
	}
	if implPhase.CostUSD != 1.50 {
		t.Errorf("Phase[implement].CostUSD = %v, want 1.50", implPhase.CostUSD)
	}

	// Repo entry with status but zero tokens
	repo, ok := summary.Repos["my-repo"]
	if !ok {
		t.Fatal("expected repos to contain 'my-repo'")
	}
	if repo.Status != "published" {
		t.Errorf("Repos[my-repo].Status = %q, want %q", repo.Status, "published")
	}
	if repo.InputTokens != 0 {
		t.Errorf("Repos[my-repo].InputTokens = %d, want 0", repo.InputTokens)
	}
	if repo.OutputTokens != 0 {
		t.Errorf("Repos[my-repo].OutputTokens = %d, want 0", repo.OutputTokens)
	}

	// Verify CacheReadTokens and CacheWriteTokens are omitted (zero) in raw YAML
	rawYAML := string(data)
	if containsField(rawYAML, "cache_read_tokens") {
		t.Error("expected cache_read_tokens to be omitted from YAML (zero value with omitempty)")
	}
	if containsField(rawYAML, "cache_write_tokens") {
		t.Error("expected cache_write_tokens to be omitted from YAML (zero value with omitempty)")
	}
}

func TestWriteFeatureSummaryAfterRewindPrefersFeatureMetadata(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "summary_rewind"

	// Write events with stale (pre-rewind) cost and token data
	eventsPath := filepath.Join(featureDir, "events.jsonl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatalf("creating events.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)

	if err := enc.Encode(Event{
		Timestamp:  time.Now(),
		TraceID:    "trace1",
		SpanID:     "span1",
		EventType:  "session.ended",
		Phase:      "implement",
		Status:     "ok",
		FeatureID:  featureID,
		SessionID:  "sess-1",
		DurationMs: 999000, // stale duration in the event itself
		Data: map[string]any{
			"input_tokens":                float64(5000),
			"output_tokens":               float64(2000),
			"cache_read_input_tokens":     float64(400),
			"cache_creation_input_tokens": float64(300),
			"total_cost_usd":              float64(99.99), // stale cost
		},
	}); err != nil {
		t.Fatalf("writing session.ended event: %v", err)
	}
	f.Close()

	// Metadata has the correct post-rewind values
	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "rewind-feature",
		Status:     "done",
		PhaseTimings: map[string]time.Duration{
			"implement": 20 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 2.00,
		},
	}

	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	yamlPath := filepath.Join(featureDir, "observe-summary.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading observe-summary.yaml: %v", err)
	}

	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary YAML: %v", err)
	}

	// Duration and cost totals come from metadata, NOT events
	if summary.Totals.DurationMS != 20000 {
		t.Errorf("Totals.DurationMS = %d, want 20000 (from metadata)", summary.Totals.DurationMS)
	}
	if summary.Totals.CostUSD != 2.00 {
		t.Errorf("Totals.CostUSD = %v, want 2.00 (from metadata)", summary.Totals.CostUSD)
	}

	// Phase duration and cost also come from metadata
	ps, ok := summary.Phases["implement"]
	if !ok {
		t.Fatal("expected phases to contain 'implement'")
	}
	if ps.DurationMS != 20000 {
		t.Errorf("Phase[implement].DurationMS = %d, want 20000 (from metadata)", ps.DurationMS)
	}
	if ps.CostUSD != 2.00 {
		t.Errorf("Phase[implement].CostUSD = %v, want 2.00 (from metadata)", ps.CostUSD)
	}

	// Token data is still enriched from events
	if summary.Totals.InputTokens != 5000 {
		t.Errorf("Totals.InputTokens = %d, want 5000 (from events)", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 2000 {
		t.Errorf("Totals.OutputTokens = %d, want 2000 (from events)", summary.Totals.OutputTokens)
	}
	if summary.Totals.CacheReadTokens != 400 {
		t.Errorf("Totals.CacheReadTokens = %d, want 400 (from events)", summary.Totals.CacheReadTokens)
	}
	if summary.Totals.CacheWriteTokens != 300 {
		t.Errorf("Totals.CacheWriteTokens = %d, want 300 (from events)", summary.Totals.CacheWriteTokens)
	}
}

func TestWriteFeatureSummarySkipsMalformedEventLines(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "summary_malformed"

	eventsPath := filepath.Join(featureDir, "events.jsonl")
	f, err := os.Create(eventsPath)
	if err != nil {
		t.Fatalf("creating events.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)

	now := time.Now()

	// First valid event: iteration.started
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span1",
		EventType: "iteration.started",
		Phase:     "implement",
		FeatureID: featureID,
		Iteration: 1,
	}); err != nil {
		t.Fatalf("writing first valid event: %v", err)
	}

	// Malformed line (not JSON)
	if _, err := f.WriteString("this is not json\n"); err != nil {
		t.Fatalf("writing malformed line: %v", err)
	}

	// Second valid event: session.ended with tokens
	if err := enc.Encode(Event{
		Timestamp: now,
		TraceID:   "trace1",
		SpanID:    "span2",
		EventType: "session.ended",
		Phase:     "implement",
		Status:    "ok",
		FeatureID: featureID,
		SessionID: "sess-1",
		Data: map[string]any{
			"input_tokens":   float64(700),
			"output_tokens":  float64(350),
			"total_cost_usd": float64(0.30),
		},
	}); err != nil {
		t.Fatalf("writing second valid event: %v", err)
	}
	f.Close()

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "malformed-events-feature",
		Status:     "done",
		PhaseTimings: map[string]time.Duration{
			"implement": 15 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 0.60,
		},
	}

	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	yamlPath := filepath.Join(featureDir, "observe-summary.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("reading observe-summary.yaml: %v", err)
	}

	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary YAML: %v", err)
	}

	// Data from both valid events should be present
	// iteration.started contributes 1 iteration count
	if summary.Totals.Iterations != 1 {
		t.Errorf("Totals.Iterations = %d, want 1", summary.Totals.Iterations)
	}

	// session.ended contributes token counts
	if summary.Totals.InputTokens != 700 {
		t.Errorf("Totals.InputTokens = %d, want 700", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 350 {
		t.Errorf("Totals.OutputTokens = %d, want 350", summary.Totals.OutputTokens)
	}

	// Phase should have both iteration count and token data
	ps, ok := summary.Phases["implement"]
	if !ok {
		t.Fatal("expected phases to contain 'implement'")
	}
	if ps.Iterations != 1 {
		t.Errorf("Phase[implement].Iterations = %d, want 1", ps.Iterations)
	}
	if ps.InputTokens != 700 {
		t.Errorf("Phase[implement].InputTokens = %d, want 700", ps.InputTokens)
	}
	if ps.OutputTokens != 350 {
		t.Errorf("Phase[implement].OutputTokens = %d, want 350", ps.OutputTokens)
	}
}

// containsField checks whether a YAML key appears in the raw YAML string.
// Used to verify omitempty behavior for zero-valued fields.
func containsField(yamlStr, field string) bool {
	// Check for "field:" pattern to match YAML keys
	for _, line := range splitLines(yamlStr) {
		trimmed := trimLeftSpace(line)
		if len(trimmed) >= len(field)+1 && trimmed[:len(field)] == field && trimmed[len(field)] == ':' {
			return true
		}
	}
	return false
}

// splitLines splits a string into lines without importing strings.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// trimLeftSpace trims leading spaces and tabs.
func trimLeftSpace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[i:]
}

// seedSealedRunYAML writes a run.yaml skeleton under
// <featureDir>/runs/run-NNN/run.yaml by marshaling a real feature.Run so the
// serialized bytes match production shape (duration strings, phase-as-int,
// etc). If sealedAt is nil, the run is left as "active" (writeFeatureSummary
// should then filter it out of sealed_runs).
func seedSealedRunYAML(t *testing.T, featureDir string, runNum int, sealedAt *time.Time,
	sealReason feature.SealReason, rewindTarget *feature.Phase,
	phaseTimings map[string]time.Duration, phaseCosts map[string]float64, rewindRoadmapPhase ...int) {
	t.Helper()
	dir := filepath.Join(featureDir, "runs", fmt.Sprintf("run-%03d", runNum))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("creating run dir: %v", err)
	}
	var selectedRoadmapPhase *int
	if len(rewindRoadmapPhase) > 0 {
		selectedRoadmapPhase = &rewindRoadmapPhase[0]
	}
	run := &feature.Run{
		RunNumber:          runNum,
		SealedAt:           sealedAt,
		SealReason:         sealReason,
		RewindTarget:       rewindTarget,
		RewindRoadmapPhase: selectedRoadmapPhase,
		PhaseTimings:       phaseTimings,
		PhaseCosts:         phaseCosts,
	}
	data, err := yaml.Marshal(run)
	if err != nil {
		t.Fatalf("marshaling run.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.yaml"), data, 0644); err != nil {
		t.Fatalf("writing run.yaml: %v", err)
	}
}

// TestEmit_PreMigrationEventsTreatedAsUnknownRun seeds events.jsonl with raw
// JSONL lines lacking run_number (pre-Phase-4 byte shape) and asserts
// writeFeatureSummaryImpl still produces a well-formed summary. Pre-migration
// tolerance kicks in only when ActiveRun == 1, so this test uses ActiveRun:1.
func TestEmit_PreMigrationEventsTreatedAsUnknownRun(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "pre_migration_feat"

	// Write raw JSONL lines that lack a run_number key — pre-Phase-4 shape.
	eventsPath := filepath.Join(featureDir, "events.jsonl")
	raw := `{"timestamp":"2026-04-20T10:00:00Z","trace_id":"t","span_id":"s1","event_type":"session.ended","phase":"implement","feature_id":"pre_migration_feat","session_id":"s1","data":{"input_tokens":300,"output_tokens":100,"total_cost_usd":0.1}}
{"timestamp":"2026-04-20T10:01:00Z","trace_id":"t","span_id":"s2","event_type":"iteration.started","phase":"implement","feature_id":"pre_migration_feat","iteration":1}
{"timestamp":"2026-04-20T10:02:00Z","trace_id":"t","span_id":"s3","event_type":"review.completed","phase":"implement","feature_id":"pre_migration_feat","iteration":1,"status":"APPROVED"}
`
	if err := os.WriteFile(eventsPath, []byte(raw), 0644); err != nil {
		t.Fatalf("writing events.jsonl: %v", err)
	}

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "pre-migration-feature",
		Status:     "done",
		ActiveRun:  1,
		PhaseTimings: map[string]time.Duration{
			"implement": 30 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 0.5,
		},
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}
	if summary.ActiveRun != 1 {
		t.Errorf("ActiveRun = %d, want 1", summary.ActiveRun)
	}
	if summary.Totals.InputTokens != 300 {
		t.Errorf("Totals.InputTokens = %d, want 300 (pre-migration tolerance under ActiveRun:1)", summary.Totals.InputTokens)
	}
	if summary.Totals.OutputTokens != 100 {
		t.Errorf("Totals.OutputTokens = %d, want 100", summary.Totals.OutputTokens)
	}
	if summary.Totals.Iterations != 1 {
		t.Errorf("Totals.Iterations = %d, want 1", summary.Totals.Iterations)
	}
	if summary.Totals.Reviews != 1 {
		t.Errorf("Totals.Reviews = %d, want 1", summary.Totals.Reviews)
	}
}

// TestWriteFeatureSummary_EnumeratesSealedRuns seeds a single sealed run-001
// plus an active run-002 and asserts the sealed-run block enumerates only
// run-001, with duration/cost summed from its phase_timings/phase_costs.
func TestWriteFeatureSummary_EnumeratesSealedRuns(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "enum_sealed_feat"

	sealedAt := time.Date(2026, 4, 20, 12, 34, 56, 0, time.UTC)
	target := feature.PhasePlan
	seedSealedRunYAML(t, featureDir, 1, &sealedAt, feature.SealReasonRewind, &target,
		map[string]time.Duration{"inquire": 5 * time.Second, "research": 10 * time.Second},
		map[string]float64{"inquire": 0.1, "research": 0.3})

	// Active run (no sealed_at) — must NOT appear in SealedRuns.
	seedSealedRunYAML(t, featureDir, 2, nil, feature.SealReason(""), nil, nil, nil)

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "enum-sealed",
		Status:     "planning",
		ActiveRun:  2,
		PhaseTimings: map[string]time.Duration{
			"plan": 2 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"plan": 0.05,
		},
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}

	if summary.ActiveRun != 2 {
		t.Errorf("ActiveRun = %d, want 2", summary.ActiveRun)
	}
	if len(summary.SealedRuns) != 1 {
		t.Fatalf("expected 1 sealed run, got %d", len(summary.SealedRuns))
	}
	sealed := summary.SealedRuns[0]
	if sealed.RunNumber != 1 {
		t.Errorf("sealed[0].RunNumber = %d, want 1", sealed.RunNumber)
	}
	if sealed.SealReason != "rewind" {
		t.Errorf("sealed[0].SealReason = %q, want rewind", sealed.SealReason)
	}
	if sealed.RewindTarget != "plan" {
		t.Errorf("sealed[0].RewindTarget = %q, want plan", sealed.RewindTarget)
	}
	if sealed.RewindRoadmapPhase != 0 {
		t.Errorf("sealed[0].RewindRoadmapPhase = %d, want omitted zero", sealed.RewindRoadmapPhase)
	}
	if containsField(string(data), "rewind_roadmap_phase") {
		t.Errorf("full rewind summary should omit rewind_roadmap_phase, got: %s", string(data))
	}
	if sealed.DurationMS != 15000 {
		t.Errorf("sealed[0].DurationMS = %d, want 15000 (5s+10s)", sealed.DurationMS)
	}
	if sealed.CostUSD < 0.399 || sealed.CostUSD > 0.401 {
		t.Errorf("sealed[0].CostUSD = %v, want ~0.4 (0.1+0.3)", sealed.CostUSD)
	}

	// Top-level totals reflect the active run (metadata-driven).
	if summary.Totals.DurationMS != 2000 {
		t.Errorf("Totals.DurationMS = %d, want 2000 (active run 2)", summary.Totals.DurationMS)
	}
	if summary.Totals.CostUSD < 0.049 || summary.Totals.CostUSD > 0.051 {
		t.Errorf("Totals.CostUSD = %v, want ~0.05", summary.Totals.CostUSD)
	}
}

func TestWriteFeatureSummary_SealedPartialRewindIncludesRoadmapPhase(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "partial_rewind_summary_feat"

	sealedAt := time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC)
	target := feature.PhaseImplement
	seedSealedRunYAML(t, featureDir, 1, &sealedAt, feature.SealReasonRewind, &target,
		map[string]time.Duration{"implement": 9 * time.Second},
		map[string]float64{"implement": 0.2},
		2)
	seedSealedRunYAML(t, featureDir, 2, nil, feature.SealReason(""), nil, nil, nil)

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "partial-rewind-summary",
		Status:     "implement_ready",
		ActiveRun:  2,
		PhaseTimings: map[string]time.Duration{
			"implement": 1 * time.Second,
		},
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}
	if len(summary.SealedRuns) != 1 {
		t.Fatalf("SealedRuns len = %d, want 1", len(summary.SealedRuns))
	}
	sealed := summary.SealedRuns[0]
	if sealed.RewindTarget != "implement" {
		t.Errorf("sealed[0].RewindTarget = %q, want implement", sealed.RewindTarget)
	}
	if sealed.RewindRoadmapPhase != 2 {
		t.Errorf("sealed[0].RewindRoadmapPhase = %d, want 2", sealed.RewindRoadmapPhase)
	}
	if !containsField(string(data), "rewind_roadmap_phase") {
		t.Errorf("partial rewind summary should include rewind_roadmap_phase, got: %s", string(data))
	}
}

func TestWriteFeatureSummary_NonImplementRewindOmitsRoadmapPhase(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "plan_rewind_summary_feat"

	sealedAt := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	target := feature.PhasePlan
	seedSealedRunYAML(t, featureDir, 1, &sealedAt, feature.SealReasonRewind, &target,
		map[string]time.Duration{"plan": 2 * time.Second},
		map[string]float64{"plan": 0.1},
		2)

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "plan-rewind-summary",
		Status:     "plan_ready",
		ActiveRun:  2,
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}
	if len(summary.SealedRuns) != 1 {
		t.Fatalf("SealedRuns len = %d, want 1", len(summary.SealedRuns))
	}
	if summary.SealedRuns[0].RewindRoadmapPhase != 0 {
		t.Errorf("RewindRoadmapPhase = %d, want omitted zero", summary.SealedRuns[0].RewindRoadmapPhase)
	}
	if containsField(string(data), "rewind_roadmap_phase") {
		t.Errorf("non-Implement rewind summary should omit rewind_roadmap_phase, got: %s", string(data))
	}
}

// TestWriteFeatureSummary_MultipleRewindsAccumulate seeds three runs — 001 +
// 002 sealed, 003 active — and asserts SealedRuns lists [run-001, run-002]
// in ascending order.
func TestWriteFeatureSummary_MultipleRewindsAccumulate(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "multi_rewind_feat"

	sealed1 := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	sealed2 := time.Date(2026, 4, 20, 13, 0, 0, 0, time.UTC)
	target := feature.PhasePlan

	// Seed in reverse order so the reader must sort them back.
	seedSealedRunYAML(t, featureDir, 2, &sealed2, feature.SealReasonRewind, &target,
		map[string]time.Duration{"implement": 6 * time.Second},
		map[string]float64{"implement": 0.2})
	seedSealedRunYAML(t, featureDir, 1, &sealed1, feature.SealReasonRewind, &target,
		map[string]time.Duration{"inquire": 3 * time.Second},
		map[string]float64{"inquire": 0.15})
	seedSealedRunYAML(t, featureDir, 3, nil, feature.SealReason(""), nil, nil, nil)

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "multi-rewind",
		Status:     "planning",
		ActiveRun:  3,
		PhaseTimings: map[string]time.Duration{
			"plan": 1 * time.Second,
		},
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}
	if len(summary.SealedRuns) != 2 {
		t.Fatalf("expected 2 sealed runs, got %d", len(summary.SealedRuns))
	}
	if summary.SealedRuns[0].RunNumber != 1 || summary.SealedRuns[1].RunNumber != 2 {
		t.Errorf("sealed runs not in ascending order: %+v", summary.SealedRuns)
	}
	// Confirm run-3 (active) never appears.
	for _, s := range summary.SealedRuns {
		if s.RunNumber == 3 {
			t.Errorf("active run 3 must not appear in SealedRuns")
		}
	}
}

// TestWriteFeatureSummary_NoRunsDir_EmptySealedRuns asserts that on a
// featureDir with no runs/ subdirectory, SealedRuns is empty/omitted and the
// rest of the summary is produced normally. Covers the never-rewound feature
// case and the unit-test-without-seeded-runs case.
func TestWriteFeatureSummary_NoRunsDir_EmptySealedRuns(t *testing.T) {
	featureDir := t.TempDir()
	featureID := "no_runs_dir_feat"

	input := FeatureSummaryInput{
		FeatureID:  featureID,
		FeatureDir: featureDir,
		Name:       "no-runs",
		Status:     "done",
		ActiveRun:  1,
		PhaseTimings: map[string]time.Duration{
			"implement": 30 * time.Second,
		},
		PhaseCosts: map[string]float64{
			"implement": 1.0,
		},
	}
	if err := writeFeatureSummaryImpl(input); err != nil {
		t.Fatalf("writeFeatureSummaryImpl: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
	if err != nil {
		t.Fatalf("reading summary: %v", err)
	}
	var summary SummaryArtifact
	if err := yaml.Unmarshal(data, &summary); err != nil {
		t.Fatalf("unmarshaling summary: %v", err)
	}
	if len(summary.SealedRuns) != 0 {
		t.Errorf("SealedRuns = %+v, want empty", summary.SealedRuns)
	}
	if summary.Totals.DurationMS != 30000 {
		t.Errorf("Totals.DurationMS = %d, want 30000", summary.Totals.DurationMS)
	}
	// sealed_runs key must be absent from the YAML (omitempty on nil/empty slice).
	if containsField(string(data), "sealed_runs") {
		t.Errorf("sealed_runs should be omitted from YAML when empty, got: %s", string(data))
	}
}

// TestReadSealedRuns_PhaseNameTableMatchesFeaturePackage is a coupling guard:
// the local phaseNameByIntValue table inside summary.go must agree with
// feature.Phase.DirName() for all 8 enum values. Drift in the feature
// package's enum order or DirName spellings fails this test immediately.
func TestReadSealedRuns_PhaseNameTableMatchesFeaturePackage(t *testing.T) {
	for i := range 8 {
		wantName := feature.Phase(i).DirName()
		gotName := phaseNameByIntValue[i]
		if gotName != wantName {
			t.Errorf("phaseNameByIntValue[%d] = %q, want %q", i, gotName, wantName)
		}
	}
}

// TestWriteFeatureSummary_FiltersEventsByActiveRun pins the §12 behaviour:
// after a rewind (ActiveRun >= 2), the top-level totals / phases / repos
// blocks aggregate ONLY events whose run_number matches the active run.
// Pre-Phase-4 lines (missing run_number) are excluded because they cannot
// logically belong to a post-rewind run.
func TestWriteFeatureSummary_FiltersEventsByActiveRun(t *testing.T) {
	t.Run("post_rewind_run_2_only", func(t *testing.T) {
		featureDir := t.TempDir()
		featureID := "filter_run2_feat"

		// Seed 3 run-1 session.ended events + 2 run-2 session.ended events
		// + 1 pre-Phase-4 session.ended event (no run_number).
		eventsPath := filepath.Join(featureDir, "events.jsonl")
		raw := `{"timestamp":"2026-04-20T10:00:00Z","trace_id":"t","span_id":"s1","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s1","repo_name":"repo-a","run_number":1,"data":{"input_tokens":100,"output_tokens":50,"total_cost_usd":0.01}}
{"timestamp":"2026-04-20T10:01:00Z","trace_id":"t","span_id":"s2","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s2","repo_name":"repo-a","run_number":1,"data":{"input_tokens":100,"output_tokens":50,"total_cost_usd":0.01}}
{"timestamp":"2026-04-20T10:02:00Z","trace_id":"t","span_id":"s3","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s3","repo_name":"repo-a","run_number":1,"data":{"input_tokens":100,"output_tokens":50,"total_cost_usd":0.01}}
{"timestamp":"2026-04-20T11:00:00Z","trace_id":"t","span_id":"s4","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s4","repo_name":"repo-a","run_number":2,"data":{"input_tokens":200,"output_tokens":80,"total_cost_usd":0.02}}
{"timestamp":"2026-04-20T11:01:00Z","trace_id":"t","span_id":"s5","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s5","repo_name":"repo-a","run_number":2,"data":{"input_tokens":200,"output_tokens":80,"total_cost_usd":0.02}}
{"timestamp":"2026-04-20T09:00:00Z","trace_id":"t","span_id":"s0","event_type":"session.ended","phase":"implement","feature_id":"filter_run2_feat","session_id":"s0","repo_name":"repo-a","data":{"input_tokens":999,"output_tokens":999,"total_cost_usd":0.99}}
`
		if err := os.WriteFile(eventsPath, []byte(raw), 0644); err != nil {
			t.Fatalf("writing events.jsonl: %v", err)
		}

		// Seal run-1 so sealed_runs block co-exists with the filter.
		sealed := time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC)
		target := feature.PhasePlan
		seedSealedRunYAML(t, featureDir, 1, &sealed, feature.SealReasonRewind, &target,
			map[string]time.Duration{"implement": 60 * time.Second},
			map[string]float64{"implement": 1.0})

		input := FeatureSummaryInput{
			FeatureID:  featureID,
			FeatureDir: featureDir,
			Name:       "filter-run2",
			Status:     "implementing",
			ActiveRun:  2,
			PhaseTimings: map[string]time.Duration{
				"implement": 40 * time.Second,
			},
			PhaseCosts: map[string]float64{
				"implement": 0.04,
			},
		}
		if err := writeFeatureSummaryImpl(input); err != nil {
			t.Fatalf("writeFeatureSummaryImpl: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
		if err != nil {
			t.Fatalf("reading summary: %v", err)
		}
		var summary SummaryArtifact
		if err := yaml.Unmarshal(data, &summary); err != nil {
			t.Fatalf("unmarshaling summary: %v", err)
		}

		// Only run-2 lines contribute to top-level totals.
		if summary.Totals.InputTokens != 400 {
			t.Errorf("Totals.InputTokens = %d, want 400 (2 × 200)", summary.Totals.InputTokens)
		}
		if summary.Totals.OutputTokens != 160 {
			t.Errorf("Totals.OutputTokens = %d, want 160 (2 × 80)", summary.Totals.OutputTokens)
		}
		if summary.Totals.DurationMS != 40000 {
			t.Errorf("Totals.DurationMS = %d, want 40000 (run-2 metadata)", summary.Totals.DurationMS)
		}
		if summary.Totals.CostUSD < 0.039 || summary.Totals.CostUSD > 0.041 {
			t.Errorf("Totals.CostUSD = %v, want ~0.04", summary.Totals.CostUSD)
		}
		if summary.ActiveRun != 2 {
			t.Errorf("ActiveRun = %d, want 2", summary.ActiveRun)
		}
		// Phase bucket reflects only run-2 tokens.
		ps := summary.Phases["implement"]
		if ps.InputTokens != 400 {
			t.Errorf("Phases[implement].InputTokens = %d, want 400", ps.InputTokens)
		}
		// Repo bucket reflects only run-2 tokens.
		rs := summary.Repos["repo-a"]
		if rs.InputTokens != 400 {
			t.Errorf("Repos[repo-a].InputTokens = %d, want 400", rs.InputTokens)
		}
		// Sealed run-1 still enumerated separately.
		if len(summary.SealedRuns) != 1 || summary.SealedRuns[0].RunNumber != 1 {
			t.Errorf("SealedRuns = %+v, want [run-001]", summary.SealedRuns)
		}
	})

	t.Run("never_rewound_run_1_with_pre_migration_events", func(t *testing.T) {
		featureDir := t.TempDir()
		featureID := "never_rewound_feat"
		eventsPath := filepath.Join(featureDir, "events.jsonl")
		// 2 run-1 events (input=100 each) + 2 pre-Phase-4 events (input=50 each).
		raw := `{"timestamp":"2026-04-20T10:00:00Z","trace_id":"t","span_id":"s1","event_type":"session.ended","phase":"implement","feature_id":"never_rewound_feat","session_id":"s1","run_number":1,"data":{"input_tokens":100,"output_tokens":0,"total_cost_usd":0}}
{"timestamp":"2026-04-20T10:01:00Z","trace_id":"t","span_id":"s2","event_type":"session.ended","phase":"implement","feature_id":"never_rewound_feat","session_id":"s2","run_number":1,"data":{"input_tokens":100,"output_tokens":0,"total_cost_usd":0}}
{"timestamp":"2026-04-20T09:00:00Z","trace_id":"t","span_id":"s3","event_type":"session.ended","phase":"implement","feature_id":"never_rewound_feat","session_id":"s3","data":{"input_tokens":50,"output_tokens":0,"total_cost_usd":0}}
{"timestamp":"2026-04-20T09:01:00Z","trace_id":"t","span_id":"s4","event_type":"session.ended","phase":"implement","feature_id":"never_rewound_feat","session_id":"s4","data":{"input_tokens":50,"output_tokens":0,"total_cost_usd":0}}
`
		if err := os.WriteFile(eventsPath, []byte(raw), 0644); err != nil {
			t.Fatalf("writing events.jsonl: %v", err)
		}
		input := FeatureSummaryInput{
			FeatureID:  featureID,
			FeatureDir: featureDir,
			Name:       "never-rewound",
			Status:     "done",
			ActiveRun:  1,
		}
		if err := writeFeatureSummaryImpl(input); err != nil {
			t.Fatalf("writeFeatureSummaryImpl: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
		if err != nil {
			t.Fatalf("reading summary: %v", err)
		}
		var summary SummaryArtifact
		if err := yaml.Unmarshal(data, &summary); err != nil {
			t.Fatalf("unmarshaling summary: %v", err)
		}
		// Pre-Phase-4 tolerance under ActiveRun:1 — both runs contribute.
		if summary.Totals.InputTokens != 300 {
			t.Errorf("Totals.InputTokens = %d, want 300 (2×100 run-1 + 2×50 pre-Phase-4)", summary.Totals.InputTokens)
		}
	})

	t.Run("active_run_0_no_filter", func(t *testing.T) {
		featureDir := t.TempDir()
		featureID := "zero_run_feat"
		eventsPath := filepath.Join(featureDir, "events.jsonl")
		// Mixed: run_number:1, run_number:5, no run_number. All must contribute.
		raw := `{"timestamp":"2026-04-20T10:00:00Z","trace_id":"t","span_id":"s1","event_type":"session.ended","phase":"implement","feature_id":"zero_run_feat","session_id":"s1","run_number":1,"data":{"input_tokens":10,"output_tokens":0,"total_cost_usd":0}}
{"timestamp":"2026-04-20T10:01:00Z","trace_id":"t","span_id":"s2","event_type":"session.ended","phase":"implement","feature_id":"zero_run_feat","session_id":"s2","run_number":5,"data":{"input_tokens":20,"output_tokens":0,"total_cost_usd":0}}
{"timestamp":"2026-04-20T10:02:00Z","trace_id":"t","span_id":"s3","event_type":"session.ended","phase":"implement","feature_id":"zero_run_feat","session_id":"s3","data":{"input_tokens":30,"output_tokens":0,"total_cost_usd":0}}
`
		if err := os.WriteFile(eventsPath, []byte(raw), 0644); err != nil {
			t.Fatalf("writing events.jsonl: %v", err)
		}
		input := FeatureSummaryInput{
			FeatureID:  featureID,
			FeatureDir: featureDir,
			Name:       "zero-run",
			Status:     "done",
			ActiveRun:  0, // legacy test fixture — filter disabled
		}
		if err := writeFeatureSummaryImpl(input); err != nil {
			t.Fatalf("writeFeatureSummaryImpl: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(featureDir, "observe-summary.yaml"))
		if err != nil {
			t.Fatalf("reading summary: %v", err)
		}
		var summary SummaryArtifact
		if err := yaml.Unmarshal(data, &summary); err != nil {
			t.Fatalf("unmarshaling summary: %v", err)
		}
		if summary.Totals.InputTokens != 60 {
			t.Errorf("Totals.InputTokens = %d, want 60 (10+20+30, filter disabled)", summary.Totals.InputTokens)
		}
	})
}
