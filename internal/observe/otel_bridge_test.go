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
	"encoding/hex"
	"net"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// mockTraceCollector implements the OTLP collector trace service for testing.
type mockTraceCollector struct {
	collectortracepb.UnimplementedTraceServiceServer
	requestCount int32
}

func (m *mockTraceCollector) Export(ctx context.Context, req *collectortracepb.ExportTraceServiceRequest) (*collectortracepb.ExportTraceServiceResponse, error) {
	atomic.AddInt32(&m.requestCount, 1)
	return &collectortracepb.ExportTraceServiceResponse{}, nil
}

// newTestBridge creates an otelBridge wired to an in-memory span recorder.
func newTestBridge(t *testing.T) (*otelBridge, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithIDGenerator(roadmapIDGen{}))
	b := &otelBridge{
		enabled: true,
		tracer:  tp.Tracer("agentic-test"),
		tp:      tp,
		spans:   make(map[string]activeSpan),
	}
	return b, rec
}

func TestOTelBridgeStartEndCreatesHierarchy(t *testing.T) {
	b, rec := newTestBridge(t)

	root := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-1",
	}
	child := SpanContext{
		TraceID:      "00000000000000000000000000000001",
		SpanID:       "bbbbbbbbbbbbbbbb",
		ParentSpanID: "aaaaaaaaaaaaaaaa",
		FeatureID:    "feat-1",
	}

	b.StartSpan(root, "phase.research", map[string]string{"phase": "research"})
	b.StartSpan(child, "validator.arch", map[string]string{"validator": "arch"})
	b.EndSpan(child.SpanID, "completed", nil)
	b.EndSpan(root.SpanID, "completed", nil)

	_ = b.tp.Shutdown(context.Background())
	spans := rec.Ended()

	if len(spans) != 2 {
		t.Fatalf("expected 2 ended spans, got %d", len(spans))
	}

	// First ended span is the child (ended first).
	childSpan := spans[0]
	rootSpan := spans[1]

	if childSpan.Parent().SpanID() != rootSpan.SpanContext().SpanID() {
		t.Errorf("child parent span ID %s != root span ID %s",
			childSpan.Parent().SpanID(), rootSpan.SpanContext().SpanID())
	}

	// Verify they share the same trace ID.
	if childSpan.SpanContext().TraceID() != rootSpan.SpanContext().TraceID() {
		t.Errorf("trace IDs differ: child=%s root=%s",
			childSpan.SpanContext().TraceID(), rootSpan.SpanContext().TraceID())
	}

	// Verify OTel SpanIDs match roadmap SpanIDs (deterministic mapping).
	assertSpanID(t, rootSpan, root.SpanID)
	assertSpanID(t, childSpan, child.SpanID)

	// Verify standard attributes are present.
	assertAttr(t, rootSpan, "agentic.feature_id", "feat-1")
	assertAttr(t, rootSpan, "phase", "research")
	assertAttr(t, childSpan, "agentic.feature_id", "feat-1")
	assertAttr(t, childSpan, "validator", "arch")
}

func TestOTelBridgeDisabledIsNoop(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	b := &otelBridge{
		enabled: false,
		tracer:  tp.Tracer("agentic-test"),
		tp:      tp,
		spans:   make(map[string]activeSpan),
	}

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-1",
	}

	b.StartSpan(sc, "phase.research", nil)
	b.EndSpan(sc.SpanID, "completed", nil)

	_ = tp.Shutdown(context.Background())

	if len(rec.Ended()) != 0 {
		t.Errorf("expected 0 spans when disabled, got %d", len(rec.Ended()))
	}
	if len(b.spans) != 0 {
		t.Errorf("expected empty store when disabled, got %d entries", len(b.spans))
	}
}

func TestOTelBridgeEndSpanCleansStore(t *testing.T) {
	b, _ := newTestBridge(t)

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-1",
	}

	b.StartSpan(sc, "phase.plan", nil)
	if len(b.spans) != 1 {
		t.Fatalf("expected 1 span in store after StartSpan, got %d", len(b.spans))
	}

	b.EndSpan(sc.SpanID, "completed", nil)
	if len(b.spans) != 0 {
		t.Fatalf("expected 0 spans in store after EndSpan, got %d", len(b.spans))
	}
}

func TestOTelBridgeShutdownFlushes(t *testing.T) {
	b, rec := newTestBridge(t)

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-1",
	}

	b.StartSpan(sc, "phase.implement", nil)
	b.EndSpan(sc.SpanID, "completed", nil)

	err := b.Shutdown()
	if err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 flushed span, got %d", len(spans))
	}

	// Second shutdown should not error.
	err = b.Shutdown()
	if err != nil {
		t.Fatalf("second Shutdown returned error: %v", err)
	}
}

func TestOTelBridgeMissingParentFallsBackToRoot(t *testing.T) {
	b, rec := newTestBridge(t)

	// Start a child span whose parent is not in the store.
	sc := SpanContext{
		TraceID:      "00000000000000000000000000000001",
		SpanID:       "cccccccccccccccc",
		ParentSpanID: "deaddeaddeaddead", // not in store
		FeatureID:    "feat-1",
	}

	b.StartSpan(sc, "validator.test", map[string]string{"validator": "test"})
	b.EndSpan(sc.SpanID, "completed", nil)

	_ = b.tp.Shutdown(context.Background())
	spans := rec.Ended()

	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	s := spans[0]
	// Should be a root span (no valid parent).
	if s.Parent().IsValid() {
		t.Errorf("expected root span (no valid parent), got parent=%s", s.Parent().SpanID())
	}

	// Verify OTel SpanID matches roadmap SpanID even for orphaned spans.
	assertSpanID(t, s, sc.SpanID)

	// Should record that parent was missing.
	assertAttr(t, s, "agentic.parent_missing", "true")
}

func TestOTelBridgeAddSpanEvent(t *testing.T) {
	b, rec := newTestBridge(t)

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-1",
	}
	b.StartSpan(sc, "session.implement", nil)

	b.AddSpanEvent(sc.SpanID, "permission.requested", map[string]string{
		"tool_name": "Bash",
	})
	b.AddSpanEvent(sc.SpanID, "permission.resolved", map[string]string{
		"tool_name": "Bash",
		"decision":  "allow",
	})

	b.EndSpan(sc.SpanID, "completed", nil)
	_ = b.tp.Shutdown(context.Background())

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	events := spans[0].Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 span events, got %d", len(events))
	}
	if events[0].Name != "permission.requested" {
		t.Errorf("event[0].Name = %q, want %q", events[0].Name, "permission.requested")
	}
	if events[1].Name != "permission.resolved" {
		t.Errorf("event[1].Name = %q, want %q", events[1].Name, "permission.resolved")
	}
}

func TestOTelBridgeAddSpanEventMissingSpanIsNoop(t *testing.T) {
	b, _ := newTestBridge(t)
	// Should not panic when span doesn't exist
	b.AddSpanEvent("nonexistent", "test.event", map[string]string{"key": "val"})
}

func TestNewOtelBridgeProductionConstructor(t *testing.T) {
	t.Run("enabled with OTLP endpoint", func(t *testing.T) {
		collector := &mockTraceCollector{}

		lis, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		grpcServer := grpc.NewServer()
		collectortracepb.RegisterTraceServiceServer(grpcServer, collector)
		go func() { _ = grpcServer.Serve(lis) }()
		defer grpcServer.Stop()

		// Pass explicit gRPC options so the test controls endpoint and TLS.
		bridge := newOtelBridge(true, "", false, "test-agentic-service",
			otlptracegrpc.WithEndpoint(lis.Addr().String()),
			otlptracegrpc.WithInsecure(),
		)

		if !bridge.enabled {
			t.Fatal("expected bridge to be enabled")
		}
		if bridge.tp == nil {
			t.Fatal("expected non-nil TracerProvider")
		}

		// Exercise full span lifecycle to verify no panics.
		sc := SpanContext{
			TraceID:   "00000000000000000000000000000001",
			SpanID:    "aaaaaaaaaaaaaaaa",
			FeatureID: "feat-prod-test",
		}
		bridge.StartSpan(sc, "prod.test", map[string]string{"env": "test"})
		bridge.EndSpan(sc.SpanID, "completed", nil)

		err = bridge.Shutdown()
		if err != nil {
			t.Fatalf("Shutdown returned error: %v", err)
		}
	})

	t.Run("disabled returns noop bridge", func(t *testing.T) {
		bridge := newOtelBridge(false, "", false, "")

		if bridge.enabled {
			t.Fatal("expected bridge to be disabled")
		}
		if bridge.tp != nil {
			t.Fatal("expected nil TracerProvider for disabled bridge")
		}
	})
}

// TestOTelExportPathSendsToEndpoint proves that the OTLP gRPC exporter code
// path actually delivers spans to a remote endpoint. Uses SimpleSpanProcessor
// (synchronous) for deterministic test behavior and explicit endpoint options
// for test-server compatibility.
func TestOTelExportPathSendsToEndpoint(t *testing.T) {
	collector := &mockTraceCollector{}

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	collectortracepb.RegisterTraceServiceServer(grpcServer, collector)
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	ctx := context.Background()
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(lis.Addr().String()),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("create OTLP exporter: %v", err)
	}

	// Use SimpleSpanProcessor (WithSyncer) for synchronous, deterministic export.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithIDGenerator(roadmapIDGen{}))
	bridge := &otelBridge{
		enabled: true,
		tracer:  tp.Tracer("agentic-export-test"),
		tp:      tp,
		spans:   make(map[string]activeSpan),
	}

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-export-test",
	}
	bridge.StartSpan(sc, "export.test", map[string]string{"env": "test"})
	bridge.EndSpan(sc.SpanID, "completed", nil)

	err = bridge.Shutdown()
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if got := atomic.LoadInt32(&collector.requestCount); got == 0 {
		t.Fatal("expected OTLP endpoint to receive at least one export request, got 0")
	}
}

// TestDisabledBridgeProducesNoExporterActivity proves that a disabled bridge
// does not send any requests to an OTLP endpoint, even when one is configured.
func TestDisabledBridgeProducesNoExporterActivity(t *testing.T) {
	collector := &mockTraceCollector{}

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	collectortracepb.RegisterTraceServiceServer(grpcServer, collector)
	go func() { _ = grpcServer.Serve(lis) }()
	defer grpcServer.Stop()

	// Pass endpoint options so any accidental export attempt would be caught.
	bridge := newOtelBridge(false, "", false, "",
		otlptracegrpc.WithEndpoint(lis.Addr().String()),
		otlptracegrpc.WithInsecure(),
	)

	sc := SpanContext{
		TraceID:   "00000000000000000000000000000001",
		SpanID:    "aaaaaaaaaaaaaaaa",
		FeatureID: "feat-disabled-test",
	}
	bridge.StartSpan(sc, "disabled.test", nil)
	bridge.EndSpan(sc.SpanID, "completed", nil)
	_ = bridge.Shutdown()

	if got := atomic.LoadInt32(&collector.requestCount); got != 0 {
		t.Fatalf("expected 0 export requests for disabled bridge, got %d", got)
	}
}

// assertSpanID verifies that the OTel span's SpanID matches the hex-encoded
// roadmap SpanID (deterministic mapping via roadmapIDGen).
func assertSpanID(t *testing.T, s sdktrace.ReadOnlySpan, roadmapID string) {
	t.Helper()
	b, err := hex.DecodeString(roadmapID)
	if err != nil || len(b) != 8 {
		t.Fatalf("invalid roadmap SpanID %q", roadmapID)
	}
	var want trace.SpanID
	copy(want[:], b)
	got := s.SpanContext().SpanID()
	if got != want {
		t.Errorf("OTel SpanID %s != roadmap SpanID %s", got, want)
	}
}

// assertAttr checks that the span has the given key-value attribute.
func assertAttr(t *testing.T, s sdktrace.ReadOnlySpan, key, wantVal string) {
	t.Helper()
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			got := a.Value.Emit()
			if got != wantVal {
				t.Errorf("attribute %s = %q, want %q", key, got, wantVal)
			}
			return
		}
	}
	t.Errorf("attribute %s not found on span %s", key, s.Name())
}

// Ensure trace and attribute packages are used (prevent unused import).
var _ trace.Tracer
var _ = attribute.Key("test")
