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
	"crypto/rand"
	"encoding/hex"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// collectorUnreachablePatterns lists substrings that identify
// "the OTel collector is simply not running" errors. These are expected
// when users run agentic without an OTel collector (the default) and
// should not pollute agentic.log with verbose gRPC messages.
//
// A non-matching error is always logged — those indicate something else
// (bad payload, auth failure, exporter bug) and a user should see them.
var collectorUnreachablePatterns = []string{
	"connection refused",
	"context deadline exceeded",
	"rpc error: code = Unavailable",
	"traces export: exporter export timeout",
}

// isCollectorUnreachable returns true if err's text matches one of the
// noise-pattern substrings we want to suppress until a first successful
// export has happened.
func isCollectorUnreachable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, p := range collectorUnreachablePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// exporterWithSuccess wraps a sdktrace.SpanExporter and records whether
// a single successful ExportSpans has ever completed. The OTel error
// handler installed alongside it uses this signal to decide whether
// collector-unreachable errors are noise (suppress) or legitimate
// (surface).
type exporterWithSuccess struct {
	inner sdktrace.SpanExporter
	first atomic.Bool
}

func (e *exporterWithSuccess) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.inner.ExportSpans(ctx, spans)
	if err == nil {
		e.first.Store(true)
	}
	return err
}

func (e *exporterWithSuccess) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

// otelErrorHandler is a go.opentelemetry.io/otel.ErrorHandler that
// suppresses collector-unreachable errors before the first successful
// export, logging exactly one summary line the first time such an error
// is seen. After a first successful export (or for any non-matching
// error) it falls through to the default log.Printf format.
type otelErrorHandler struct {
	exporter    *exporterWithSuccess
	loggedFirst atomic.Bool
	logf        func(format string, args ...any) // injectable for tests
}

func (h *otelErrorHandler) Handle(err error) {
	if err == nil {
		return
	}
	logf := h.logf
	if logf == nil {
		logf = log.Printf
	}

	// Non-matching errors always surface — they indicate something other
	// than the collector-unreachable case we're trying to quiet.
	if !isCollectorUnreachable(err) {
		logf("OTel error: %v", err)
		return
	}

	// Matching error: suppress unless we've already had a successful export.
	if h.exporter != nil && h.exporter.first.Load() {
		logf("OTel error: %v", err)
		return
	}

	// First matching error before any successful export: emit one summary
	// line, then suppress repeats until export succeeds.
	if !h.loggedFirst.Swap(true) {
		logf("OTEL collector unreachable (example: %v) — suppressing further export errors until first successful export", err)
	}
}

// otelBridge manages OTel span lifecycle. It maps roadmap SpanID values to
// real OTel spans using an in-memory store, without requiring context.Context
// propagation through the application.
type otelBridge struct {
	enabled bool
	tracer  trace.Tracer
	tp      *sdktrace.TracerProvider

	mu    sync.Mutex
	spans map[string]activeSpan // keyed by roadmap SpanID
}

// activeSpan holds a live OTel span and its context for parent resolution.
type activeSpan struct {
	span trace.Span
	ctx  context.Context
}

// wantSpanIDKey is a context key for passing a desired OTel SpanID to the
// custom IDGenerator. Each call to StartSpan injects the roadmap SpanID into
// the context so that the generator returns it instead of a random value.
type otelCtxKey int

const wantSpanIDKey otelCtxKey = iota

// roadmapIDGen implements sdktrace.IDGenerator. When the start context carries
// a desired SpanID (set by StartSpan), it returns that value; otherwise it
// falls back to cryptographically random generation.
type roadmapIDGen struct{}

func (roadmapIDGen) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	var tid trace.TraceID
	_, _ = rand.Read(tid[:])
	if sid, ok := ctx.Value(wantSpanIDKey).(trace.SpanID); ok && sid.IsValid() {
		return tid, sid
	}
	var sid trace.SpanID
	_, _ = rand.Read(sid[:])
	return tid, sid
}

func (roadmapIDGen) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	if sid, ok := ctx.Value(wantSpanIDKey).(trace.SpanID); ok && sid.IsValid() {
		return sid
	}
	var sid trace.SpanID
	_, _ = rand.Read(sid[:])
	return sid
}

// newOtelBridge creates an otelBridge. When enabled is false, all methods no-op.
// When enabled, it creates an OTLP gRPC exporter (reading OTEL_EXPORTER_OTLP_ENDPOINT
// from the environment) and configures a TracerProvider with the service.name resource
// attribute. Optional grpcOpts are forwarded to the exporter constructor, allowing
// callers to override endpoint/TLS settings. If exporter creation fails, it degrades
// gracefully to a bare provider.
func newOtelBridge(enabled bool, endpoint string, insecure bool, serviceName string, grpcOpts ...otlptracegrpc.Option) *otelBridge {
	if !enabled {
		return &otelBridge{
			enabled: false,
			spans:   make(map[string]activeSpan),
		}
	}

	if serviceName == "" {
		serviceName = "agentico"
	}

	if endpoint != "" {
		grpcOpts = append([]otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}, grpcOpts...)
	}
	if insecure {
		grpcOpts = append([]otlptracegrpc.Option{otlptracegrpc.WithInsecure()}, grpcOpts...)
	}

	ctx := context.Background()
	exporter, err := otlptracegrpc.New(ctx, grpcOpts...)
	if err != nil {
		// Graceful degradation: create bridge with bare provider.
		tp := sdktrace.NewTracerProvider(sdktrace.WithIDGenerator(roadmapIDGen{}))
		return &otelBridge{
			enabled: true,
			tracer:  tp.Tracer(serviceName),
			tp:      tp,
			spans:   make(map[string]activeSpan),
		}
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		res = resource.NewSchemaless(semconv.ServiceNameKey.String(serviceName))
	}

	// Wrap the exporter so we can track whether a first successful export
	// has happened. Before that, collector-unreachable errors are noise —
	// the custom ErrorHandler (installed below) suppresses them after a
	// single summary line so agentic.log stays readable when no OTel
	// collector is running on localhost.
	wrappedExporter := &exporterWithSuccess{inner: exporter}
	otel.SetErrorHandler(&otelErrorHandler{exporter: wrappedExporter})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(wrappedExporter),
		sdktrace.WithResource(res),
		sdktrace.WithIDGenerator(roadmapIDGen{}),
	)
	return &otelBridge{
		enabled: true,
		tracer:  tp.Tracer(serviceName),
		tp:      tp,
		spans:   make(map[string]activeSpan),
	}
}

// ensureSpan creates the span in the bridge only if it does not already exist.
// Used to re-materialize long-lived parent spans (e.g. the feature span) that
// may have been lost on process restart while the feature is still running.
func (b *otelBridge) ensureSpan(sc SpanContext, operationName string, attrs map[string]string) {
	if !b.enabled {
		return
	}
	b.mu.Lock()
	_, exists := b.spans[sc.SpanID]
	b.mu.Unlock()
	if exists {
		return
	}
	b.StartSpan(sc, operationName, attrs)
}

// StartSpan creates a real OTel span. Parent-child relationships are resolved
// from the in-memory store using ParentSpanID, not context propagation.
func (b *otelBridge) StartSpan(sc SpanContext, operationName string, attrs map[string]string) {
	if !b.enabled {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Resolve parent context from store.
	parentCtx := context.Background()
	parentMissing := false
	if sc.ParentSpanID != "" {
		if parent, ok := b.spans[sc.ParentSpanID]; ok {
			parentCtx = parent.ctx
		} else {
			parentMissing = true
		}
	}

	// Build span options with standard attributes.
	spanAttrs := []attribute.KeyValue{
		attribute.String("agentic.feature_id", sc.FeatureID),
		attribute.String("agentic.span_id", sc.SpanID),
		attribute.String("agentic.trace_id", sc.TraceID),
	}
	if sc.FeatureName != "" {
		spanAttrs = append(spanAttrs, attribute.String("agentic.feature_name", sc.FeatureName))
	}
	if sc.ParentSpanID != "" {
		spanAttrs = append(spanAttrs, attribute.String("agentic.parent_span_id", sc.ParentSpanID))
	}
	if parentMissing {
		spanAttrs = append(spanAttrs, attribute.String("agentic.parent_missing", "true"))
	}
	for k, v := range attrs {
		spanAttrs = append(spanAttrs, attribute.String(k, v))
	}

	// Seed OTel trace ID from roadmap TraceID when possible.
	opts := []trace.SpanStartOption{
		trace.WithAttributes(spanAttrs...),
	}
	remoteCtx := seedTraceContext(sc, parentCtx)

	// Inject roadmap SpanID so the custom IDGenerator returns it
	// instead of a random value.
	if spanBytes, err := hex.DecodeString(sc.SpanID); err == nil && len(spanBytes) == 8 {
		var otelSID trace.SpanID
		copy(otelSID[:], spanBytes)
		remoteCtx = context.WithValue(remoteCtx, wantSpanIDKey, otelSID)
	}

	ctx, span := b.tracer.Start(remoteCtx, operationName, opts...)
	b.spans[sc.SpanID] = activeSpan{span: span, ctx: ctx}
}

// EndSpan ends the span matching the given roadmap SpanID, applying final
// status and attributes. Tolerates unknown SpanIDs without panicking.
func (b *otelBridge) EndSpan(spanID string, status string, attrs map[string]string) {
	if !b.enabled {
		return
	}

	b.mu.Lock()
	active, ok := b.spans[spanID]
	if !ok {
		b.mu.Unlock()
		return
	}
	delete(b.spans, spanID)
	b.mu.Unlock()

	// Apply final attributes.
	for k, v := range attrs {
		active.span.SetAttributes(attribute.String(k, v))
	}
	active.span.SetAttributes(attribute.String("agentic.status", status))

	// Map status to OTel status code.
	switch status {
	case "failed", "error":
		active.span.SetStatus(codes.Error, status)
	default:
		active.span.SetStatus(codes.Ok, status)
	}

	active.span.End()
}

// AddSpanEvent adds a timestamped event (log) to an existing span identified
// by spanID. If the span is not found (e.g. OTel was disabled when it started),
// the call is silently ignored. This is used for point-in-time observations
// like permission prompts and questions that annotate a parent session span.
func (b *otelBridge) AddSpanEvent(spanID string, eventName string, attrs map[string]string) {
	if !b.enabled {
		return
	}

	b.mu.Lock()
	active, ok := b.spans[spanID]
	b.mu.Unlock()
	if !ok {
		return
	}

	otelAttrs := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		otelAttrs = append(otelAttrs, attribute.String(k, v))
	}
	active.span.AddEvent(eventName, trace.WithAttributes(otelAttrs...))
}

// Shutdown flushes the TracerProvider with a bounded 5-second timeout.
// Safe to call when disabled or already shut down.
func (b *otelBridge) Shutdown() error {
	if !b.enabled || b.tp == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return b.tp.Shutdown(ctx)
}

// seedTraceContext creates a span context seeded with the roadmap TraceID
// so all spans within a feature share the same OTel trace. When the parent
// context already has a valid trace, that takes precedence.
func seedTraceContext(sc SpanContext, parentCtx context.Context) context.Context {
	// If parent already carries a valid span, use it directly.
	if trace.SpanFromContext(parentCtx).SpanContext().IsValid() {
		return parentCtx
	}

	// Attempt to parse roadmap TraceID into OTel TraceID.
	traceIDBytes, err := hex.DecodeString(sc.TraceID)
	if err != nil || len(traceIDBytes) != 16 {
		return parentCtx
	}
	var otelTraceID trace.TraceID
	copy(otelTraceID[:], traceIDBytes)

	// Create a remote span context so the tracer adopts our trace ID.
	remoteSC := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    otelTraceID,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithRemoteSpanContext(parentCtx, remoteSC)
}
