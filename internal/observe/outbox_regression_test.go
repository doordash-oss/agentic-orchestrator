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
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboxReplayPreservesRecordsAcrossSegmentByteLimit(t *testing.T) {
	dir := t.TempDir()
	exporter := &captureWideExporter{}
	o := &eventOutbox{dir: dir, exporter: exporter}
	var first []byte
	for i := 0; i < 80; i++ {
		record := sanitizeWideEvent(Event{EventType: "question.answered", Data: map[string]any{
			"a": strings.Repeat("x", 4000), "b": strings.Repeat("x", 4000),
			"c": strings.Repeat("x", 4000), "d": strings.Repeat("x", 4000),
		}})
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		first = append(first, append(line, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, "segment-0001.jsonl"), first, 0600); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(sanitizeWideEvent(Event{EventType: "feature.delivered"}))
	if err := os.WriteFile(filepath.Join(dir, "segment-0002.jsonl"), append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := o.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(exporter.records) != 81 {
		t.Fatalf("exported %d/81 records; %d silently lost", len(exporter.records), 81-len(exporter.records))
	}
}

// A restart after collector acceptance but before cursor persistence must replay
// exactly the same identities so a downstream consumer can deduplicate them.
func TestOutboxRestartReplaysUnacknowledgedEventIdentity(t *testing.T) {
	dir := t.TempDir()
	exporter := &captureWideExporter{}
	o := &eventOutbox{dir: dir, exporter: exporter, wake: make(chan struct{}, 1)}
	if !o.Enqueue(Event{EventType: "feature.delivered"}) {
		t.Fatal("enqueue failed")
	}
	batch, _, _, err := o.readBatch()
	if err != nil || len(batch) != 1 {
		t.Fatalf("batch=%v error=%v", batch, err)
	}
	if err := exporter.ExportEventBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	restarted := &eventOutbox{dir: dir, exporter: exporter}
	if err := restarted.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(exporter.records) != 2 || exporter.records[0].EventID != exporter.records[1].EventID || exporter.records[0].SpanID != exporter.records[1].SpanID {
		t.Fatalf("replay changed event identity: %+v", exporter.records)
	}
	if err := restarted.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(exporter.records) != 2 {
		t.Fatal("acknowledged records replayed")
	}
}

func TestOutboxFailedExportDoesNotAdvanceCursor(t *testing.T) {
	dir := t.TempDir()
	exporter := &captureWideExporter{fail: true}
	o := &eventOutbox{dir: dir, exporter: exporter, wake: make(chan struct{}, 1)}
	if !o.Enqueue(Event{EventType: "feature.delivered"}) {
		t.Fatal("enqueue failed")
	}
	if err := o.drainOnce(context.Background()); err == nil {
		t.Fatal("expected export failure")
	}
	if got := o.loadCursor(); got != (outboxCursor{}) {
		t.Fatalf("cursor advanced: %+v", got)
	}
	exporter.fail = false
	if err := o.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(exporter.records) != 1 {
		t.Fatalf("exported %d records", len(exporter.records))
	}
}

// Shutdown cancels an in-flight retry before performing its final bounded drain.
type blockingOnceWideExporter struct {
	calls   atomic.Int32
	started chan struct{}
}

func (e *blockingOnceWideExporter) ExportEventBatch(ctx context.Context, records []wideRecord) error {
	if e.calls.Add(1) == 1 {
		close(e.started)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func TestOutboxShutdownCancelsInFlightExport(t *testing.T) {
	e := &blockingOnceWideExporter{started: make(chan struct{})}
	o := newEventOutbox(filepath.Join(t.TempDir(), "features"), e, nil, nil)
	if !o.Enqueue(Event{EventType: "feature.delivered"}) {
		t.Fatal("enqueue failed")
	}
	select {
	case <-e.started:
	case <-time.After(time.Second):
		t.Fatal("export did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-o.done:
	default:
		t.Fatal("export goroutine survived shutdown")
	}
	if e.calls.Load() != 2 {
		t.Fatalf("final drain did not replay record: calls=%d", e.calls.Load())
	}
}

func TestWideEventSanitizesTypedMapsAndEnvelopeWithoutMutatingCaller(t *testing.T) {
	data := map[string]string{"transcript": "private transcript", "safe": "token=secret"}
	evt := Event{EventType: "session.ended", RepoName: "/Users/alice/private", Data: map[string]any{"nested": data}}
	record := sanitizeWideEvent(evt)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private transcript", "secret", "/Users/alice"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("sensitive content survived: %s", raw)
		}
	}
	if data["transcript"] != "private transcript" || evt.RepoName != "/Users/alice/private" {
		t.Fatal("caller data mutated")
	}
}

func TestOutboxOversizedEnvelopeReportsLossWithoutBlockingReplay(t *testing.T) {
	exporter := &captureWideExporter{}
	o := &eventOutbox{dir: t.TempDir(), exporter: exporter, wake: make(chan struct{}, 1)}
	large := strings.Repeat("x", 8000)
	evt := Event{EventType: large, Phase: large, Status: large, FeatureID: large, SessionID: large, RepoName: large, SpanID: large, ParentSpanID: large, Error: large}
	if o.Enqueue(evt) {
		t.Fatal("oversized envelope accepted")
	}
	if !o.Enqueue(Event{EventType: "feature.delivered"}) {
		t.Fatal("subsequent event rejected")
	}
	if err := o.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	foundLoss := false
	for _, record := range exporter.records {
		if record.Event.EventType == "telemetry.data_loss" {
			foundLoss = true
		}
	}
	if !foundLoss {
		t.Fatal("oversized event loss not reported")
	}
}
