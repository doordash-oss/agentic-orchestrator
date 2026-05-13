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
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestObserverWritesJSONLAndOTelConsistently(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up an observer with a real emitter and an OTel bridge wired to a
	// test span recorder.
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithIDGenerator(roadmapIDGen{}))
	bridge := &otelBridge{
		enabled: true,
		tracer:  tp.Tracer("agentic-test"),
		tp:      tp,
		spans:   make(map[string]activeSpan),
	}
	obs := &Observer{
		emitter: NewEmitter(tmpDir),
		otel:    bridge,
		enabled: true,
	}

	featureID := "test-feat-1"
	if err := os.MkdirAll(filepath.Join(tmpDir, featureID), 0755); err != nil {
		t.Fatalf("mkdir feature dir: %v", err)
	}
	rootSC := SpanContextForFeature(featureID, "", "", "")
	phaseSC := rootSC.Child()

	// Emit a realistic lifecycle sequence.
	obs.PhaseStarted(phaseSC, "implement")
	obs.PhaseCompleted(phaseSC, "implement", 2*time.Second, nil)
	obs.Shutdown()

	// --- Assert JSONL side ---
	eventsPath := filepath.Join(tmpDir, featureID, "events.jsonl")
	events := readJSONLEvents(t, eventsPath)

	if len(events) != 2 {
		t.Fatalf("expected 2 JSONL events, got %d", len(events))
	}
	if events[0].EventType != "phase.started" {
		t.Errorf("event[0] type = %q, want %q", events[0].EventType, "phase.started")
	}
	if events[1].EventType != "phase.completed" {
		t.Errorf("event[1] type = %q, want %q", events[1].EventType, "phase.completed")
	}
	// Both events should carry the same span context.
	if events[0].SpanID != events[1].SpanID {
		t.Errorf("JSONL events have different SpanIDs: %s vs %s", events[0].SpanID, events[1].SpanID)
	}
	if events[0].TraceID != events[1].TraceID {
		t.Errorf("JSONL events have different TraceIDs: %s vs %s", events[0].TraceID, events[1].TraceID)
	}

	// --- Assert OTel side ---
	_ = tp.Shutdown(context.Background())
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span (phase), got %d", len(spans))
	}

	otelSpan := spans[0]
	if otelSpan.Name() != "phase.implement" {
		t.Errorf("OTel span name = %q, want %q", otelSpan.Name(), "phase.implement")
	}

	// Verify the OTel span's feature_id matches the JSONL events.
	foundFeatureID := ""
	for _, a := range otelSpan.Attributes() {
		if string(a.Key) == "agentic.feature_id" {
			foundFeatureID = a.Value.Emit()
		}
	}
	if foundFeatureID != featureID {
		t.Errorf("OTel span feature_id = %q, want %q", foundFeatureID, featureID)
	}

	// Verify the JSONL SpanID matches the roadmap span ID used for OTel lookup.
	foundSpanID := ""
	for _, a := range otelSpan.Attributes() {
		if string(a.Key) == "agentic.span_id" {
			foundSpanID = a.Value.Emit()
		}
	}
	if foundSpanID != events[0].SpanID {
		t.Errorf("OTel agentic.span_id=%q != JSONL SpanID=%q", foundSpanID, events[0].SpanID)
	}

	// Verify the actual OTel SpanID matches the roadmap SpanID (not just the attribute).
	spanBytes, err := hex.DecodeString(events[0].SpanID)
	if err != nil || len(spanBytes) != 8 {
		t.Fatalf("invalid roadmap SpanID in JSONL: %q", events[0].SpanID)
	}
	var expectedSID trace.SpanID
	copy(expectedSID[:], spanBytes)
	if otelSpan.SpanContext().SpanID() != expectedSID {
		t.Errorf("OTel SpanID %s != JSONL roadmap SpanID %s",
			otelSpan.SpanContext().SpanID(), expectedSID)
	}
}

// readJSONLEvents reads a JSONL file and returns parsed events.
func readJSONLEvents(t *testing.T, path string) []Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt Event
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return events
}
