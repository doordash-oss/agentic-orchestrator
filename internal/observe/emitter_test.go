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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEmitter(t *testing.T) {
	t.Run("emit_creates_file_and_writes_valid_jsonl", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "emit_test_1"
		if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
			t.Fatal(err)
		}

		emitter := NewEmitter(stateDir)
		evt := Event{
			Timestamp: time.Now(),
			TraceID:   "trace1",
			SpanID:    "span1",
			EventType: "phase.started",
			Phase:     "research",
			Status:    "started",
			FeatureID: featureID,
		}

		if err := emitter.Emit(evt); err != nil {
			t.Fatalf("Emit failed: %v", err)
		}

		path := filepath.Join(stateDir, featureID, "events.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("could not read events.jsonl: %v", err)
		}

		var decoded Event
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if decoded.EventType != "phase.started" {
			t.Errorf("EventType = %q, want phase.started", decoded.EventType)
		}
	})

	t.Run("emit_appends_multiple_events", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "emit_test_2"
		if err := os.MkdirAll(filepath.Join(stateDir, featureID), 0755); err != nil {
			t.Fatal(err)
		}

		emitter := NewEmitter(stateDir)
		for i := 0; i < 3; i++ {
			evt := Event{
				Timestamp: time.Now(),
				TraceID:   "trace1",
				SpanID:    "span1",
				EventType: "test.event",
				FeatureID: featureID,
				Iteration: i,
			}
			if err := emitter.Emit(evt); err != nil {
				t.Fatalf("Emit %d failed: %v", i, err)
			}
		}

		path := filepath.Join(stateDir, featureID, "events.jsonl")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		var count int
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var evt Event
			if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
				t.Fatalf("line %d invalid JSON: %v", count, err)
			}
			count++
		}
		if count != 3 {
			t.Errorf("expected 3 lines, got %d", count)
		}
	})

	t.Run("emit_different_features_write_different_files", func(t *testing.T) {
		stateDir := t.TempDir()
		for _, id := range []string{"aaa", "bbb"} {
			if err := os.MkdirAll(filepath.Join(stateDir, id), 0755); err != nil {
				t.Fatal(err)
			}
		}

		emitter := NewEmitter(stateDir)

		emitter.Emit(Event{FeatureID: "aaa", EventType: "test", Timestamp: time.Now()})
		emitter.Emit(Event{FeatureID: "bbb", EventType: "test", Timestamp: time.Now()})

		if _, err := os.Stat(filepath.Join(stateDir, "aaa", "events.jsonl")); err != nil {
			t.Errorf("expected aaa/events.jsonl to exist: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, "bbb", "events.jsonl")); err != nil {
			t.Errorf("expected bbb/events.jsonl to exist: %v", err)
		}
	})

	// Regression test for the leak documented in emitter.go: when a feature
	// directory is deleted while a phase runner is mid-shutdown, the trailing
	// emissions must NOT resurrect the directory. The event is silently dropped.
	t.Run("emit_is_noop_when_feature_directory_is_missing", func(t *testing.T) {
		stateDir := t.TempDir()
		featureID := "deleted_feature"

		emitter := NewEmitter(stateDir)
		err := emitter.Emit(Event{
			Timestamp: time.Now(),
			EventType: "phase.failed",
			FeatureID: featureID,
			Error:     "interrupted",
		})
		if err != nil {
			t.Fatalf("Emit returned error for missing feature dir: %v", err)
		}
		if _, err := os.Stat(filepath.Join(stateDir, featureID)); !os.IsNotExist(err) {
			t.Errorf("feature dir should not be recreated; got stat err=%v", err)
		}
	})
}
