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
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// flakyExporter is a fake sdktrace.SpanExporter whose ExportSpans result
// is controlled by the test. Used to drive exporterWithSuccess through
// the fail-then-succeed sequence we want to cover.
type flakyExporter struct {
	mu       sync.Mutex
	err      error // returned from ExportSpans until cleared
	exported atomic.Int64
}

func (f *flakyExporter) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *flakyExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	f.exported.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *flakyExporter) Shutdown(ctx context.Context) error { return nil }

// captureLogger collects formatted log lines for assertions.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) Logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLogger) Snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

func TestOtelErrorHandler_SuppressCollectorUnreachableUntilFirstSuccess(t *testing.T) {
	inner := &flakyExporter{err: errors.New(`traces export: exporter export timeout: rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial tcp [::1]:4317: connect: connection refused"`)}
	wrapped := &exporterWithSuccess{inner: inner}

	cap := &captureLogger{}
	h := &otelErrorHandler{exporter: wrapped, logf: cap.Logf}

	// Feed a matching error — first call emits a single summary line.
	h.Handle(inner.err)
	lines := cap.Snapshot()
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line after first matching error, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "OTEL collector unreachable") {
		t.Errorf("expected summary line, got %q", lines[0])
	}
	if !strings.Contains(lines[0], "suppressing further export errors") {
		t.Errorf("expected suppression notice, got %q", lines[0])
	}

	// Subsequent matching errors while we haven't had a successful
	// export must stay silent.
	for i := 0; i < 5; i++ {
		h.Handle(inner.err)
	}
	if got := len(cap.Snapshot()); got != 1 {
		t.Errorf("expected no additional lines while still failing; got %d", got)
	}

	// Non-matching errors are always surfaced.
	h.Handle(errors.New("bad payload"))
	lines = cap.Snapshot()
	if len(lines) != 2 {
		t.Fatalf("expected non-matching error to be logged, got %d lines: %v", len(lines), lines)
	}
	if !strings.Contains(lines[1], "bad payload") {
		t.Errorf("expected non-matching error text, got %q", lines[1])
	}

	// Flip the exporter to success and run ExportSpans once so
	// first.Store(true) fires.
	inner.setError(nil)
	if err := wrapped.ExportSpans(context.Background(), nil); err != nil {
		t.Fatalf("unexpected export error after flipping to success: %v", err)
	}
	if !wrapped.first.Load() {
		t.Fatalf("exporterWithSuccess.first not set after successful export")
	}

	// Now a matching error must be surfaced via the default path
	// (we're past the quiet window).
	h.Handle(errors.New("rpc error: code = Unavailable desc = something temporary"))
	lines = cap.Snapshot()
	if len(lines) != 3 {
		t.Fatalf("expected post-success matching error to surface, got %d lines: %v", len(lines), lines)
	}
	if !strings.Contains(lines[2], "Unavailable") {
		t.Errorf("expected post-success error, got %q", lines[2])
	}
	if strings.Contains(lines[2], "suppressing") {
		t.Errorf("post-success error should use default format, got %q", lines[2])
	}
}

func TestOtelErrorHandler_NilErrorIsNoop(t *testing.T) {
	cap := &captureLogger{}
	h := &otelErrorHandler{logf: cap.Logf}
	h.Handle(nil)
	if n := len(cap.Snapshot()); n != 0 {
		t.Errorf("expected no log lines for nil error, got %d", n)
	}
}

func TestOtelErrorHandler_MatchesAllKnownPatterns(t *testing.T) {
	patterns := []string{
		`connection refused: dial tcp [::1]:4317`,
		`context deadline exceeded`,
		`rpc error: code = Unavailable desc = foo`,
		`traces export: exporter export timeout: something`,
	}
	for _, p := range patterns {
		err := errors.New(p)
		if !isCollectorUnreachable(err) {
			t.Errorf("expected %q to be classified as collector-unreachable", p)
		}
	}

	// Non-matching error must not be classified as collector-unreachable.
	if isCollectorUnreachable(errors.New("permission denied")) {
		t.Error("permission denied should NOT be collector-unreachable")
	}
}

func TestExporterWithSuccess_TracksFirstSuccess(t *testing.T) {
	inner := &flakyExporter{err: errors.New("boom")}
	wrapped := &exporterWithSuccess{inner: inner}

	// First call fails — first stays false.
	if err := wrapped.ExportSpans(context.Background(), nil); err == nil {
		t.Fatal("expected error from failing exporter")
	}
	if wrapped.first.Load() {
		t.Error("first should not be set after failing export")
	}

	// Now succeed.
	inner.setError(nil)
	if err := wrapped.ExportSpans(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !wrapped.first.Load() {
		t.Error("first should be set after a successful export")
	}

	// Sanity: ExportSpans was actually called on the inner exporter.
	if n := inner.exported.Load(); n != 2 {
		t.Errorf("expected inner.exported=2, got %d", n)
	}
}
