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
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FeatureSummaryInput carries all data needed to produce observe-summary.yaml.
// Kept in the observe package so it does not depend on the feature package.
type FeatureSummaryInput struct {
	FeatureID           string
	FeatureDir          string
	Name                string
	Status              string
	FailureType         string
	LastError           string
	CurrentRoadmapPhase int
	TotalRoadmapPhases  int
	ActiveRun           int
	PhaseTimings        map[string]time.Duration
	PhaseCosts          map[string]float64
	RepoStates          map[string]RepoSummaryInput
}

// RepoSummaryInput carries per-repo state for summary generation.
type RepoSummaryInput struct {
	Status    string
	Iteration int
	PRURL     string
	LastError string
}

// BuildFeatureSummaryInput is a convenience adapter that populates a
// FeatureSummaryInput from individual parameters.
func BuildFeatureSummaryInput(
	featureID, featureDir, name, status, failureType, lastError string,
	currentRoadmapPhase, totalRoadmapPhases int,
	phaseTimings map[string]time.Duration,
	phaseCosts map[string]float64,
	repoStates map[string]RepoSummaryInput,
	activeRun int,
) FeatureSummaryInput {
	return FeatureSummaryInput{
		FeatureID:           featureID,
		FeatureDir:          featureDir,
		Name:                name,
		Status:              status,
		FailureType:         failureType,
		LastError:           lastError,
		CurrentRoadmapPhase: currentRoadmapPhase,
		TotalRoadmapPhases:  totalRoadmapPhases,
		ActiveRun:           activeRun,
		PhaseTimings:        phaseTimings,
		PhaseCosts:          phaseCosts,
		RepoStates:          repoStates,
	}
}

// writeFeatureSummaryImpl is the real implementation behind Observer.WriteFeatureSummary.
// It builds a fallback skeleton from the input metadata, enriches it with event
// data when available, then atomically writes observe-summary.yaml.
//
// Re-entrancy: safe on repeated calls — the write is atomic (temp+rename) and
// consumes only the input + on-disk state. A crash between the event scan and
// the rename leaves observe-summary.yaml unchanged; the next call rebuilds it
// from the same inputs. No in-memory mutation persists between calls.
func writeFeatureSummaryImpl(input FeatureSummaryInput) error {
	// 1. Build fallback skeleton from FeatureSummaryInput.
	summary := SummaryArtifact{
		Feature: FeatureSummaryBlock{
			ID:          input.FeatureID,
			Name:        input.Name,
			Status:      input.Status,
			FailureType: input.FailureType,
			LastError:   input.LastError,
		},
		ActiveRun: input.ActiveRun,
	}

	// Populate per-phase durations and costs from metadata maps.
	phases := make(map[string]PhaseSummary)
	for key, dur := range input.PhaseTimings {
		k := normalizePhaseKey(key)
		ps := phases[k]
		ps.DurationMS = dur.Milliseconds()
		phases[k] = ps
	}
	for key, cost := range input.PhaseCosts {
		k := normalizePhaseKey(key)
		ps := phases[k]
		ps.CostUSD = cost
		phases[k] = ps
	}

	// Compute totals from phase metadata.
	var totalDurationMS int64
	var totalCostUSD float64
	for _, dur := range input.PhaseTimings {
		totalDurationMS += dur.Milliseconds()
	}
	for _, cost := range input.PhaseCosts {
		totalCostUSD += cost
	}
	summary.Totals.DurationMS = totalDurationMS
	summary.Totals.CostUSD = totalCostUSD

	// Populate repos from RepoStates.
	repos := make(map[string]RepoSummary)
	for name, rs := range input.RepoStates {
		repos[name] = RepoSummary{
			Status: rs.Status,
			PRURL:  rs.PRURL,
		}
	}

	// 2. Try to read events.jsonl, filter to the active run, and enrich the skeleton.
	// Active-run filtering (§12) keeps top-level totals/phases/repos scoped to the
	// current run — post-rewind features do not leak predecessor-run tokens into
	// the headline numbers. Sealed runs are surfaced separately under sealed_runs.
	eventsPath := filepath.Join(input.FeatureDir, "events.jsonl")
	events, _ := readSummaryEvents(eventsPath)
	activeEvents := filterEventsByActiveRun(events, input.ActiveRun)
	if len(activeEvents) > 0 {
		aggregateSummaryFromEvents(&summary, phases, repos, activeEvents)
	}

	// 3. After event enrichment, overwrite duration/cost from feature metadata
	// (metadata is authoritative for duration/cost, events only enrich tokens/counters).
	summary.Totals.DurationMS = totalDurationMS
	summary.Totals.CostUSD = totalCostUSD
	for key, dur := range input.PhaseTimings {
		k := normalizePhaseKey(key)
		ps := phases[k]
		ps.DurationMS = dur.Milliseconds()
		phases[k] = ps
	}
	for key, cost := range input.PhaseCosts {
		k := normalizePhaseKey(key)
		ps := phases[k]
		ps.CostUSD = cost
		phases[k] = ps
	}

	if len(phases) > 0 {
		summary.Phases = phases
	}
	if len(repos) > 0 {
		summary.Repos = repos
	}

	// 4. Enumerate sealed runs (rewind history). Failures here never abort
	// summary writes — a missing runs/ dir, unparseable run.yaml, or permission
	// error leaves SealedRuns empty but preserves the rest of the summary.
	if sealed, err := readSealedRuns(input.FeatureDir); err == nil {
		summary.SealedRuns = sealed
	}

	// 5. Write YAML atomically.
	outputPath := filepath.Join(input.FeatureDir, "observe-summary.yaml")
	return atomicWriteYAML(outputPath, &summary)
}

// filterEventsByActiveRun returns the subset of events that belong to the
// active run. Earlier events may lack the run_number field and therefore
// unmarshal with RunNumber == 0; those are treated as "run-1 lineage" only
// when activeRun == 1 (never-rewound feature — the only run that ever
// existed). Once the feature reaches activeRun >= 2, any RunNumber == 0
// entry is historically ambiguous and is excluded from the active-run
// aggregation. activeRun <= 0 is treated as an unversioned fixture (no
// production code path hits this today because Store.Load fails fast on
// ActiveRun == 0) and disables filtering entirely so legacy test fixtures
// that construct inputs without setting ActiveRun keep passing.
func filterEventsByActiveRun(events []Event, activeRun int) []Event {
	if activeRun <= 0 {
		return events
	}
	out := make([]Event, 0, len(events))
	for _, e := range events {
		switch {
		case e.RunNumber == activeRun:
			out = append(out, e)
		case e.RunNumber == 0 && activeRun == 1:
			out = append(out, e) // Missing run_number belongs to run-1 lineage.
		}
	}
	return out
}

// readSealedRuns walks <featureDir>/runs/run-*/run.yaml, filters entries
// where sealed_at is set, and returns them sorted by run_number ascending.
// Unreadable / unparseable run.yaml files are skipped — matches the
// tolerant posture of readSummaryEvents. Returns an empty slice (never nil)
// when no sealed runs are present.
//
// This helper reads YAML directly rather than importing feature.Store —
// observe must not depend on internal/feature. The local sealedRunYAML type
// mirrors only the subset of feature.Run fields we need (run_number,
// sealed_at, seal_reason, rewind_target, rewind_roadmap_phase,
// phase_timings, phase_costs) so adding or renaming other Run fields never
// forces a summary-side update.
func readSealedRuns(featureDir string) ([]SealedRunSummary, error) {
	runsDir := filepath.Join(featureDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SealedRunSummary{}, nil
		}
		return nil, fmt.Errorf("reading runs dir: %w", err)
	}
	out := make([]SealedRunSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, ok := parseRunDirNameLocal(e.Name())
		if !ok {
			continue
		}
		runYAML := filepath.Join(runsDir, e.Name(), "run.yaml")
		data, err := os.ReadFile(runYAML)
		if err != nil {
			continue
		}
		var r sealedRunYAML
		if err := yaml.Unmarshal(data, &r); err != nil {
			continue
		}
		if r.SealedAt == nil {
			continue // active or committing run — not a sealed record
		}
		entry := SealedRunSummary{
			RunNumber:  n,
			SealedAt:   *r.SealedAt,
			SealReason: r.SealReason,
			DurationMS: sumDurations(r.PhaseTimings),
			CostUSD:    sumFloats(r.PhaseCosts),
		}
		rewindTarget := ""
		if r.RewindTarget != nil {
			rewindTarget = phaseNameByIntValue[*r.RewindTarget]
			entry.RewindTarget = rewindTarget
		}
		if rewindTarget == "implement" && r.RewindRoadmapPhase != nil {
			entry.RewindRoadmapPhase = *r.RewindRoadmapPhase
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].RunNumber < out[j].RunNumber
	})
	return out, nil
}

// sealedRunYAML mirrors the subset of feature.Run needed for the sealed-run
// enumeration. Keeping a local subset preserves the observe package's
// zero-feature-import invariant (observe depends on feature only via test
// code, never production sources).
type sealedRunYAML struct {
	RunNumber          int                      `yaml:"run_number"`
	SealedAt           *time.Time               `yaml:"sealed_at,omitempty"`
	SealReason         string                   `yaml:"seal_reason,omitempty"`
	RewindTarget       *int                     `yaml:"rewind_target,omitempty"`
	RewindRoadmapPhase *int                     `yaml:"rewind_roadmap_phase,omitempty"`
	PhaseTimings       map[string]time.Duration `yaml:"phase_timings,omitempty"`
	PhaseCosts         map[string]float64       `yaml:"phase_costs,omitempty"`
}

// parseRunDirNameLocal mirrors feature.parseRunDirName. Local copy preserves
// the observe package's no-feature-import invariant.
func parseRunDirNameLocal(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, "run-")
	if !ok || rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func sumDurations(m map[string]time.Duration) int64 {
	var total time.Duration
	for _, d := range m {
		total += d
	}
	return total.Milliseconds()
}

func sumFloats(m map[string]float64) float64 {
	var total float64
	for _, v := range m {
		total += v
	}
	return total
}

// phaseNameByIntValue mirrors feature.Phase.DirName() verbatim for the eight
// enum values at feature/feature.go:13-21. Kept hand-synced to preserve
// observe's zero-import posture toward feature. Drift is caught by the
// TestReadSealedRuns_PhaseNameTableMatchesFeaturePackage consistency test.
var phaseNameByIntValue = map[int]string{
	0: "research",
	1: "plan",
	2: "implement",
	3: "publish",
	4: "review",
	5: "knowledgebase",
	6: "inquire",
	7: "design",
}

// readSummaryEvents reads events.jsonl tolerantly, skipping malformed lines.
func readSummaryEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening events file: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max token size
	for scanner.Scan() {
		var evt Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			// Skip malformed lines — don't fail the whole operation.
			continue
		}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("scanning events file: %w", err)
	}
	return events, nil
}

// aggregateSummaryFromEvents enriches the summary skeleton with token counts,
// iteration/review counts, and repo status updates from the event stream.
func aggregateSummaryFromEvents(summary *SummaryArtifact, phases map[string]PhaseSummary, repos map[string]RepoSummary, events []Event) {
	for _, evt := range events {
		switch evt.EventType {
		case "session.ended":
			phase := normalizePhaseKey(evt.Phase)
			inputTokens := intFromData(evt.Data, "input_tokens")
			outputTokens := intFromData(evt.Data, "output_tokens")
			cacheRead := intFromData(evt.Data, "cache_read_input_tokens")
			cacheWrite := intFromData(evt.Data, "cache_creation_input_tokens")
			cost := floatFromData(evt.Data, "total_cost_usd")

			// Enrich totals with token data.
			summary.Totals.InputTokens += inputTokens
			summary.Totals.OutputTokens += outputTokens
			summary.Totals.CacheReadTokens += cacheRead
			summary.Totals.CacheWriteTokens += cacheWrite

			// Enrich phase bucket.
			if phase != "" {
				ps := phases[phase]
				ps.InputTokens += inputTokens
				ps.OutputTokens += outputTokens
				phases[phase] = ps
			}

			// Enrich repo bucket.
			if evt.RepoName != "" {
				rs := repos[evt.RepoName]
				rs.InputTokens += inputTokens
				rs.OutputTokens += outputTokens
				rs.CostUSD += cost
				repos[evt.RepoName] = rs
			}

		case "iteration.started":
			phase := normalizePhaseKey(evt.Phase)
			summary.Totals.Iterations++
			if phase != "" {
				ps := phases[phase]
				ps.Iterations++
				phases[phase] = ps
			}

		case "review.completed":
			phase := normalizePhaseKey(evt.Phase)
			summary.Totals.Reviews++
			if phase != "" {
				ps := phases[phase]
				ps.Reviews++
				phases[phase] = ps
			}

		case "repo.status_changed":
			if evt.RepoName != "" {
				toStatus := stringFromData(evt.Data, "to_status")
				if toStatus != "" {
					rs := repos[evt.RepoName]
					rs.Status = toStatus
					repos[evt.RepoName] = rs
				}
			}
		}
	}
}

// normalizePhaseKey returns the key with basic cleanup (trim whitespace, lowercase).
// All keys are preserved: canonical phases, roadmap keys, cycle keys.
func normalizePhaseKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// floatFromData safely extracts a float64 from a map[string]any.
func floatFromData(data map[string]any, key string) float64 {
	if data == nil {
		return 0
	}
	v, ok := data[key]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return f
}

// intFromData safely extracts an int64 from a map[string]any.
// JSON numbers unmarshal as float64, so we type-assert to float64 first.
func intFromData(data map[string]any, key string) int64 {
	if data == nil {
		return 0
	}
	v, ok := data[key]
	if !ok {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return int64(f)
}

// stringFromData safely extracts a string from a map[string]any.
func stringFromData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	v, ok := data[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// atomicWriteYAML writes v as YAML to path using a temp file + os.Rename for atomicity.
func atomicWriteYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling summary: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating summary dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "observe-summary-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing summary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming summary: %w", err)
	}
	return nil
}
