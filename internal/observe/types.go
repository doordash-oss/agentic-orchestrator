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

import "time"

// SummaryArtifact is the top-level YAML document written to observe-summary.yaml.
type SummaryArtifact struct {
	Feature    FeatureSummaryBlock     `yaml:"feature"`
	ActiveRun  int                     `yaml:"active_run,omitempty"`
	Totals     SummaryTotals           `yaml:"totals"`
	Phases     map[string]PhaseSummary `yaml:"phases,omitempty"`
	Repos      map[string]RepoSummary  `yaml:"repos,omitempty"`
	SealedRuns []SealedRunSummary      `yaml:"sealed_runs,omitempty"`
}

// SealedRunSummary describes one sealed run of a feature. Emitted in
// observe-summary.yaml under the sealed_runs block, in run_number order.
// Populated by readSealedRuns by scanning <featureDir>/runs/run-*/run.yaml
// and filtering entries where sealed_at is set.
type SealedRunSummary struct {
	RunNumber          int       `yaml:"run_number"`
	SealedAt           time.Time `yaml:"sealed_at"`
	SealReason         string    `yaml:"seal_reason,omitempty"`
	RewindTarget       string    `yaml:"rewind_target,omitempty"`
	RewindRoadmapPhase int       `yaml:"rewind_roadmap_phase,omitempty"`
	DurationMS         int64     `yaml:"duration_ms,omitempty"`
	CostUSD            float64   `yaml:"cost_usd,omitempty"`
}

type FeatureSummaryBlock struct {
	ID         string `yaml:"id"`
	Name       string `yaml:"name"`
	Status     string `yaml:"status"`
	ErrorCode  string `yaml:"error_code,omitempty"`
	ErrorClass string `yaml:"error_class,omitempty"`
}

type SummaryTotals struct {
	DurationMS       int64   `yaml:"duration_ms"`
	CostUSD          float64 `yaml:"cost_usd"`
	InputTokens      int64   `yaml:"input_tokens,omitempty"`
	OutputTokens     int64   `yaml:"output_tokens,omitempty"`
	CacheReadTokens  int64   `yaml:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `yaml:"cache_write_tokens,omitempty"`
	Iterations       int     `yaml:"iterations,omitempty"`
	Reviews          int     `yaml:"reviews,omitempty"`
}

type PhaseSummary struct {
	DurationMS   int64   `yaml:"duration_ms,omitempty"`
	CostUSD      float64 `yaml:"cost_usd,omitempty"`
	InputTokens  int64   `yaml:"input_tokens,omitempty"`
	OutputTokens int64   `yaml:"output_tokens,omitempty"`
	Iterations   int     `yaml:"iterations,omitempty"`
	Reviews      int     `yaml:"reviews,omitempty"`
}

type RepoSummary struct {
	Status       string  `yaml:"status,omitempty"`
	PRURL        string  `yaml:"pr_url,omitempty"`
	CostUSD      float64 `yaml:"cost_usd,omitempty"`
	InputTokens  int64   `yaml:"input_tokens,omitempty"`
	OutputTokens int64   `yaml:"output_tokens,omitempty"`
}
