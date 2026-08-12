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
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	collectorMetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectorTrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
)

type fleetCollector struct {
	collectorTrace.UnimplementedTraceServiceServer
	collectorMetric.UnimplementedMetricsServiceServer
	mu      sync.Mutex
	spans   []*tracepb.ResourceSpans
	metrics []*metricpb.ResourceMetrics
}

func (c *fleetCollector) Export(ctx context.Context, req *collectorTrace.ExportTraceServiceRequest) (*collectorTrace.ExportTraceServiceResponse, error) {
	c.mu.Lock()
	c.spans = append(c.spans, req.ResourceSpans...)
	c.mu.Unlock()
	return &collectorTrace.ExportTraceServiceResponse{}, nil
}
func (c *fleetCollector) ExportMetrics(ctx context.Context, req *collectorMetric.ExportMetricsServiceRequest) (*collectorMetric.ExportMetricsServiceResponse, error) {
	c.mu.Lock()
	c.metrics = append(c.metrics, req.ResourceMetrics...)
	c.mu.Unlock()
	return &collectorMetric.ExportMetricsServiceResponse{}, nil
}

// Export cannot be overloaded in Go, so the metric service is implemented by
// a narrow adapter while both signals still share one capture.
type metricCollectorAdapter struct {
	collectorMetric.UnimplementedMetricsServiceServer
	target *fleetCollector
}

func (a metricCollectorAdapter) Export(ctx context.Context, req *collectorMetric.ExportMetricsServiceRequest) (*collectorMetric.ExportMetricsServiceResponse, error) {
	return a.target.ExportMetrics(ctx, req)
}

func TestOTelOnlyExportsWideEventsAndMetricsWithoutLocalJSONL(t *testing.T) {
	collector := &fleetCollector{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collectorTrace.RegisterTraceServiceServer(server, collector)
	collectorMetric.RegisterMetricsServiceServer(server, metricCollectorAdapter{target: collector})
	go server.Serve(lis)
	defer server.Stop()
	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	if err := os.MkdirAll(filepath.Join(stateDir, "f1"), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := New(false, stateDir, true, lis.Addr().String(), true, "fleet-test")
	if err := obs.Emit(Event{Timestamp: time.Unix(1700000000, 0), TraceID: "00000000000000000000000000000001", EventType: "validation.completed", FeatureID: "f1", Status: "passed", DurationMs: 125}); err != nil {
		t.Fatal(err)
	}
	if err := obs.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "f1", "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("OTel-only observer wrote local events.jsonl: %v", err)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.spans) == 0 {
		t.Fatal("no trace exports")
	}
	if len(collector.metrics) == 0 {
		t.Fatal("no metric exports")
	}
	found := false
	for _, rs := range collector.spans {
		for _, ss := range rs.ScopeSpans {
			for _, span := range ss.Spans {
				if span.Name == "agentico.event.validation.completed" {
					found = true
					if span.StartTimeUnixNano != uint64(time.Unix(1700000000, 0).UnixNano()) {
						t.Fatalf("timestamp=%d", span.StartTimeUnixNano)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("wide-event span not exported")
	}
	info, err := os.Stat(filepath.Join(runtimeDir, "telemetry", "installation-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("installation id mode=%o", info.Mode().Perm())
	}
}

type captureWideExporter struct {
	mu      sync.Mutex
	fail    bool
	records []wideRecord
}

func (e *captureWideExporter) ExportEventBatch(_ context.Context, records []wideRecord) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		return context.DeadlineExceeded
	}
	e.records = append(e.records, records...)
	return nil
}

func TestOutboxSanitizesReplaysAndPreservesEventID(t *testing.T) {
	runtimeDir := t.TempDir()
	stateDir := filepath.Join(runtimeDir, "features")
	exporter := &captureWideExporter{fail: true}
	o := newEventOutbox(stateDir, exporter, nil, nil)
	o.Enqueue(Event{Timestamp: time.Now(), TraceID: "00000000000000000000000000000001", EventType: "question.answered", FeatureID: "f1", Error: "token=top-secret", Data: map[string]any{"answer": "see /Users/alice/private and api_key=abc123"}})
	deadline := time.Now().Add(2 * time.Second)
	for {
		segments, _ := filepath.Glob(filepath.Join(runtimeDir, "telemetry", "outbox", "segment-*.jsonl"))
		if len(segments) > 0 {
			data, err := os.ReadFile(segments[0])
			if err == nil {
				got := string(data)
				if strings.Contains(got, "top-secret") || strings.Contains(got, "/Users/alice") || strings.Contains(got, "abc123") {
					t.Fatalf("unsanitized outbox: %s", got)
				}
				if !strings.Contains(got, "[redacted]") || (!strings.Contains(got, "<user-path>") && !strings.Contains(got, `\u003cuser-path\u003e`)) {
					t.Fatalf("missing sanitization markers: %s", got)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox segment not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	exporter.mu.Lock()
	exporter.fail = false
	exporter.mu.Unlock()
	o.wake <- struct{}{}
	deadline = time.Now().Add(2 * time.Second)
	for {
		exporter.mu.Lock()
		count := len(exporter.records)
		var id string
		if count > 0 {
			id = exporter.records[0].EventID
		}
		exporter.mu.Unlock()
		if count > 0 {
			if id == "" {
				t.Fatal("empty event id")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox did not replay")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := o.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxRepairsPartialTrailingRecordBeforeAppend(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "telemetry", "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(dir, "segment-00000000000000000001-deadbeef.jsonl")
	first, err := json.Marshal(sanitizeWideEvent(Event{Timestamp: time.Now(), EventType: "feature.created"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(segment, append(append(first, '\n'), []byte(`{"event_id":"partial`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &eventOutbox{dir: dir, segment: segment, exporter: &captureWideExporter{}, wake: make(chan struct{}, 1)}
	o.Enqueue(Event{Timestamp: time.Now(), EventType: "feature.state_changed"})
	data, err := os.ReadFile(segment)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("record count=%d data=%q", len(lines), data)
	}
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is corrupt: %q", i+1, line)
		}
	}
}

func TestOutboxAdvancesPastCorruptSealedTailWithoutRepeatingLoss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "telemetry", "outbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(dir, "segment-00000000000000000001-deadbeef.jsonl")
	if err := os.WriteFile(segment, []byte(`{"event_id":"partial`), 0o600); err != nil {
		t.Fatal(err)
	}
	o := &eventOutbox{dir: dir, exporter: &captureWideExporter{}}
	if err := o.drainOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(segment); !os.IsNotExist(err) {
		t.Fatalf("corrupt sealed segment was not acknowledged and removed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "loss.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"dropped":1`) {
		t.Fatalf("loss metadata=%s", data)
	}
}

func TestTelemetryResourceFiltersPersonalAndMachineIdentity(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "deployment.environment.name=test,host.name=private-host,workspace.path=/Users/alice/project,team.name=private-team")
	res, _ := telemetryResource(filepath.Join(t.TempDir(), "features"), "public-service")
	got := resourceStrings(res)
	if got["service.name"] != "public-service" || got["deployment.environment.name"] != "test" {
		t.Fatalf("resource=%v", got)
	}
	for _, key := range []string{"host.name", "workspace.path", "team.name"} {
		if _, exists := got[key]; exists {
			t.Fatalf("forbidden resource attribute %q survived: %v", key, got)
		}
	}
}

func TestInstallationIDRecoversCorruptFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "telemetry")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "installation-id")
	if err := os.WriteFile(path, []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := loadInstallationID(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadInstallationID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second {
		t.Fatalf("installation ID was not persisted: %q %q", first, second)
	}
}

func TestMetricModelAttributeIsBounded(t *testing.T) {
	if got := metricEnum("model", "gpt-"+strings.Repeat("x", 200)); got != "other" {
		t.Fatalf("unbounded model normalized to %q", got)
	}
	if got := metricEnum("model", "gpt-5.4[272K]"); got != "gpt-5.4" {
		t.Fatalf("catalog alias normalized to %q", got)
	}
	if got := metricEnum("model", "anthropic/claude-sonnet-4-5"); got != "sonnet" {
		t.Fatalf("provider-qualified model normalized to %q", got)
	}
}

func TestWideEventTraceIDFallbackIsStableAndForbiddenContentIsRemoved(t *testing.T) {
	event := Event{Timestamp: time.Now(), TraceID: "legacy-trace", FeatureID: "feature-a", EventType: "session.ended", Data: map[string]any{
		"prompt": "do not export", "summary": "safe", "nested": map[string]any{"transcript": "private", "status": "ok"},
	}}
	first := sanitizeWideEvent(event)
	second := sanitizeWideEvent(event)
	if event.Data["prompt"] != "do not export" {
		t.Fatalf("sanitization mutated caller data: %v", event.Data)
	}
	if first.Event.TraceID != second.Event.TraceID || len(first.Event.TraceID) != 32 {
		t.Fatalf("trace IDs are not stable: %q %q", first.Event.TraceID, second.Event.TraceID)
	}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do not export") || strings.Contains(string(data), "private") {
		t.Fatalf("forbidden content survived: %s", data)
	}
	if !strings.Contains(string(data), "safe") {
		t.Fatalf("safe summary missing: %s", data)
	}
}
