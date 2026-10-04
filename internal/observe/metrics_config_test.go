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
	"bytes"
	"fmt"
	"log"
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
	"google.golang.org/grpc"
)

// startCollector serves OTLP traces, plus metrics when withMetrics is set,
// mirroring trace-only gateways when it is not.
func startCollector(t *testing.T, withMetrics bool) (*fleetCollector, string) {
	t.Helper()
	collector := &fleetCollector{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	collectorTrace.RegisterTraceServiceServer(server, collector)
	if withMetrics {
		collectorMetric.RegisterMetricsServiceServer(server, metricCollectorAdapter{target: collector})
	}
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	return collector, lis.Addr().String()
}

// captureOTelLogs records startup lines and anything written through the
// standard logger, which is where the global OTel error handler prints.
func captureOTelLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prevLog, prevProbe := otelStartupLogf, otelStartupProbe
	otelStartupLogf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&buf, format+"\n", args...)
	}
	otelStartupProbe = func(string) {}
	prevOut := log.Writer()
	log.SetOutput(lockedWriter{mu: &mu, w: &buf})
	t.Cleanup(func() {
		otelStartupLogf, otelStartupProbe = prevLog, prevProbe
		log.SetOutput(prevOut)
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func runObserver(t *testing.T, endpoint string, cfg MetricsConfig, hold time.Duration) {
	t.Helper()
	stateDir := filepath.Join(t.TempDir(), "features")
	if err := os.MkdirAll(filepath.Join(stateDir, "f1"), 0o755); err != nil {
		t.Fatal(err)
	}
	obs := New(false, stateDir, true, endpoint, true, "metrics-test", WithMetrics(cfg))
	for i := 0; i < 2; i++ {
		if err := obs.Emit(Event{Timestamp: time.Now(), EventType: "validation.completed", FeatureID: "f1", Status: "passed", DurationMs: 125}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(hold)
	if err := obs.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func (c *fleetCollector) counts() (spans, metrics int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.spans), len(c.metrics)
}

func TestMetricsExportConfiguration(t *testing.T) {
	t.Run("trace-only collector disables metrics after one log line", func(t *testing.T) {
		logs := captureOTelLogs(t)
		t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "10")
		traces, endpoint := startCollector(t, false)
		runObserver(t, endpoint, MetricsConfig{}, 150*time.Millisecond)
		if spans, _ := traces.counts(); spans == 0 {
			t.Fatal("trace export stopped working")
		}
		out := logs()
		if got := strings.Count(out, "metric export disabled"); got != 1 {
			t.Fatalf("disabled notices = %d, want 1; logs:\n%s", got, out)
		}
		if strings.Contains(out, "Unimplemented") {
			t.Fatalf("Unimplemented export errors still logged:\n%s", out)
		}
	})

	t.Run("opt-out exports traces only", func(t *testing.T) {
		logs := captureOTelLogs(t)
		collector, endpoint := startCollector(t, true)
		runObserver(t, endpoint, MetricsConfig{Disabled: true}, 0)
		spans, metrics := collector.counts()
		if spans == 0 || metrics != 0 {
			t.Fatalf("spans=%d metrics=%d, want spans and no metrics", spans, metrics)
		}
		if strings.Contains(logs(), "metric export enabled") {
			t.Fatal("metric exporter started despite opt-out")
		}
	})

	t.Run("metrics endpoint overrides the trace endpoint", func(t *testing.T) {
		captureOTelLogs(t)
		traces, traceEndpoint := startCollector(t, false)
		metricsCollector, metricsEndpoint := startCollector(t, true)
		runObserver(t, traceEndpoint, MetricsConfig{Endpoint: metricsEndpoint}, 0)
		if spans, _ := traces.counts(); spans == 0 {
			t.Fatal("no spans at the trace endpoint")
		}
		if _, metrics := metricsCollector.counts(); metrics == 0 {
			t.Fatal("no metrics at the metrics endpoint")
		}
	})

	for _, tt := range []struct {
		temporality string
		want        metricpb.AggregationTemporality
	}{
		{"", metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE},
		{"delta", metricpb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA},
	} {
		t.Run("counter temporality "+tt.temporality, func(t *testing.T) {
			captureOTelLogs(t)
			collector, endpoint := startCollector(t, true)
			runObserver(t, endpoint, MetricsConfig{Temporality: tt.temporality}, 0)
			collector.mu.Lock()
			defer collector.mu.Unlock()
			found := false
			for _, rm := range collector.metrics {
				for _, sm := range rm.ScopeMetrics {
					for _, m := range sm.Metrics {
						if m.Name != "agentico.validation.outcome.count" {
							continue
						}
						found = true
						if got := m.GetSum().GetAggregationTemporality(); got != tt.want {
							t.Fatalf("temporality = %v, want %v", got, tt.want)
						}
					}
				}
			}
			if !found {
				t.Fatal("validation outcome counter not exported")
			}
		})
	}
}
