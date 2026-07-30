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

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestFeatureReadModelsExposeActiveImplementResumeIndicator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		record      *agent.ResumeRecord
		meta        string
		wantResumed bool
		wantCount   int
	}{
		{
			name: "no persisted marker",
		},
		{
			name: "one resume from sidecar",
			record: &agent.ResumeRecord{
				Resumed:     true,
				ResumeCount: 1,
			},
			wantResumed: true,
			wantCount:   1,
		},
		{
			name: "completed record retains many resumes",
			record: &agent.ResumeRecord{
				Resumed:     true,
				ResumeCount: 3,
				Completed:   true,
			},
			wantResumed: true,
			wantCount:   3,
		},
		{
			name:        "legacy meta fallback",
			meta:        "iteration: 2\nresumed: true\nresume_count: 2\n",
			wantResumed: true,
			wantCount:   2,
		},
		{
			name: "sidecar is authoritative over legacy meta",
			record: &agent.ResumeRecord{
				Resumed:     true,
				ResumeCount: 1,
			},
			meta:        "iteration: 2\nresumed: true\nresume_count: 9\n",
			wantResumed: true,
			wantCount:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, f := seedReadFeature(t)
			f.Status = feature.StatusCodeReady
			if err := store.Save(f); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			iterDir := activeImplementIterationDir(store, f)
			if tt.record != nil {
				record := *tt.record
				record.PhaseKey = "phase-1-impl"
				record.Iteration = f.CurrentIteration
				record.RunNumber = f.ActiveRun
				record.CreatedAt = time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
				record.UpdatedAt = record.CreatedAt
				if err := agent.WriteResumeRecord(iterDir, record); err != nil {
					t.Fatalf("WriteResumeRecord() error = %v", err)
				}
			}
			if tt.meta != "" {
				writeFile(t, filepath.Join(iterDir, "meta.yaml"), tt.meta)
			}

			handler := NewHandler(baseReadHandlerOptions(store))
			list := getJSONMap(t, handler, apiPathFeatures)
			summary := list["features"].([]any)[0].(map[string]any)
			assertResumeIndicator(t, "summary", summary, tt.wantResumed, tt.wantCount)

			detail := getJSONMap(t, handler, apiPathFeatures+"/"+f.ID)
			assertResumeIndicator(t, "detail", detail[entityFeature].(map[string]any), tt.wantResumed, tt.wantCount)
		})
	}
}

func TestFeatureReadModelResumeIndicatorIgnoresInactiveIteration(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	inactiveDir := filepath.Join(
		store.RunDir(f.ID, f.ActiveRun),
		"phase-01",
		"implement",
		"iteration-01",
	)
	if err := agent.WriteResumeRecord(inactiveDir, agent.ResumeRecord{
		PhaseKey:    "phase-1-impl",
		Iteration:   1,
		RunNumber:   f.ActiveRun,
		Resumed:     true,
		ResumeCount: 4,
	}); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	list := getJSONMap(t, handler, apiPathFeatures)
	summary := list["features"].([]any)[0].(map[string]any)
	assertResumeIndicator(t, "summary", summary, false, 0)
}

func activeImplementIterationDir(store *feature.Store, f *feature.Feature) string {
	return filepath.Join(
		store.RunDir(f.ID, f.ActiveRun),
		"phase-01",
		"implement",
		"iteration-02",
	)
}

func assertResumeIndicator(t testing.TB, label string, raw map[string]any, wantResumed bool, wantCount int) {
	t.Helper()
	if got, ok := raw["resumed"].(bool); !ok || got != wantResumed {
		t.Errorf("%s resumed = %v (present %v); want %v", label, raw["resumed"], ok, wantResumed)
	}
	gotCount, ok := raw["resume_count"].(float64)
	if !ok || int(gotCount) != wantCount {
		t.Errorf("%s resume_count = %v (present %v); want %d", label, raw["resume_count"], ok, wantCount)
	}
}

func TestFeatureResumeReadPathDoesNotMutateSidecar(t *testing.T) {
	t.Parallel()
	store, f := seedReadFeature(t)
	iterDir := activeImplementIterationDir(store, f)
	record := agent.ResumeRecord{
		PhaseKey:    "phase-1-impl",
		Iteration:   f.CurrentIteration,
		RunNumber:   f.ActiveRun,
		Resumed:     true,
		ResumeCount: 2,
	}
	if err := agent.WriteResumeRecord(iterDir, record); err != nil {
		t.Fatalf("WriteResumeRecord() error = %v", err)
	}
	path := filepath.Join(iterDir, agent.ResumeSidecarFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	handler := NewHandler(baseReadHandlerOptions(store))
	_ = getJSONMap(t, handler, apiPathFeatures)
	_ = getJSONMap(t, handler, apiPathFeatures+"/"+f.ID)

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("resume sidecar changed during read:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestFailedFeatureResumeActionUsesStrictEligibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		record       *agent.ResumeRecord
		currentModel string
		wantEnabled  bool
		wantCode     agent.ResumeEligibilityReason
		wantMessage  string
	}{
		{
			name:         "matching record",
			record:       eligibleServerResumeRecord(),
			currentModel: "codex:model-a",
			wantEnabled:  true,
		},
		{
			name:         "no record",
			currentModel: "codex:model-a",
			wantCode:     agent.ResumeReasonNoRecord,
			wantMessage:  "no resume record",
		},
		{
			name: "model changed",
			record: func() *agent.ResumeRecord {
				record := eligibleServerResumeRecord()
				record.ResolvedModel = "model-b"
				return record
			}(),
			currentModel: "codex:model-a",
			wantCode:     agent.ResumeReasonModelChanged,
			wantMessage:  "model changed",
		},
		{
			name: "run sealed",
			record: func() *agent.ResumeRecord {
				record := eligibleServerResumeRecord()
				record.RunNumber = 2
				return record
			}(),
			currentModel: "codex:model-a",
			wantCode:     agent.ResumeReasonRunSealed,
			wantMessage:  "run sealed",
		},
		{
			name: "session rejected",
			record: func() *agent.ResumeRecord {
				record := eligibleServerResumeRecord()
				record.Rejected = true
				return record
			}(),
			currentModel: "codex:model-a",
			wantCode:     agent.ResumeReasonSessionRejected,
			wantMessage:  "session previously rejected",
		},
		{
			name: "record completed",
			record: func() *agent.ResumeRecord {
				record := eligibleServerResumeRecord()
				record.Completed = true
				return record
			}(),
			currentModel: "codex:model-a",
			wantCode:     agent.ResumeReasonRecordCompleted,
			wantMessage:  "record completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, f := seedReadFeature(t)
			f.Status = feature.StatusFailed
			f.Models = config.ModelConfig{Implementation: tt.currentModel}
			if err := store.Save(f); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			if tt.record != nil {
				if err := agent.WriteResumeRecord(activeImplementIterationDir(store, f), *tt.record); err != nil {
					t.Fatalf("WriteResumeRecord() error = %v", err)
				}
			}
			registry := llm.NewRegistry()
			registry.Register(resumableServerProvider{
				fakeProvider: fakeProvider{name: "codex", models: []string{"model-a", "model-b"}},
			})
			opts := baseReadHandlerOptions(store)
			opts.Registry = registry
			handler := NewHandler(opts)

			detail := getJSONMap(t, handler, apiPathFeatures+"/"+f.ID)
			actions := detail[entityFeature].(map[string]any)["actions"].([]any)
			resume := actionFromJSON(t, actions, actionResume)
			if got := resume["enabled"].(bool); got != tt.wantEnabled {
				t.Fatalf("resume enabled = %v; want %v", got, tt.wantEnabled)
			}
			retry := actionFromJSON(t, actions, actionRetry)
			if !retry["enabled"].(bool) {
				t.Fatal("retry enabled = false; want true for every failed feature")
			}
			if tt.wantEnabled {
				if _, ok := resume["disabled_reasons"]; ok {
					t.Fatalf("enabled resume disabled_reasons = %+v; want absent", resume["disabled_reasons"])
				}
				return
			}
			reasons := resume["disabled_reasons"].([]any)
			if len(reasons) != 1 {
				t.Fatalf("resume disabled_reasons = %+v; want exactly one", reasons)
			}
			reason := reasons[0].(map[string]any)
			if got := reason["code"]; got != string(tt.wantCode) {
				t.Errorf("resume disabled code = %q; want %q", got, tt.wantCode)
			}
			if got := reason["message"]; got != tt.wantMessage {
				t.Errorf("resume disabled message = %q; want %q", got, tt.wantMessage)
			}
		})
	}
}

func TestInterruptedAndNeedUserInputResumeRemainEnabled(t *testing.T) {
	t.Parallel()
	for _, status := range []feature.Status{feature.StatusInterrupted, feature.StatusNeedUserInput} {
		t.Run(status.String(), func(t *testing.T) {
			t.Parallel()
			store, f := seedReadFeature(t)
			f.Status = status
			if err := store.Save(f); err != nil {
				t.Fatalf("Save() error = %v", err)
			}
			handler := NewHandler(baseReadHandlerOptions(store))
			detail := getJSONMap(t, handler, apiPathFeatures+"/"+f.ID)
			actions := detail[entityFeature].(map[string]any)["actions"].([]any)
			if resume := actionFromJSON(t, actions, actionResume); !resume["enabled"].(bool) {
				t.Fatalf("resume enabled = false for %s; want true", status)
			}
		})
	}
}

func eligibleServerResumeRecord() *agent.ResumeRecord {
	return &agent.ResumeRecord{
		ProviderSessionID: "provider-session-1",
		Provider:          "codex",
		ResolvedModel:     "model-a",
		PhaseKey:          "phase-1-impl",
		Iteration:         2,
		RunNumber:         1,
	}
}

func actionFromJSON(t testing.TB, actions []any, id string) map[string]any {
	t.Helper()
	for _, raw := range actions {
		action := raw.(map[string]any)
		if action["id"] == id {
			return action
		}
	}
	t.Fatalf("action catalog missing %q", id)
	return nil
}

type resumableServerProvider struct {
	fakeProvider
}

func (resumableServerProvider) SupportsSessionResume() bool { return true }
