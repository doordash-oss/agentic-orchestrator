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
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

var metricCounterNames = []string{
	"agentico.runtime.startup.count", "agentico.feature.created.count", "agentico.feature.transition.count",
	"agentico.feature.milestone.count", "agentico.feature.run.terminal.count", "agentico.feature.rewind.count",
	"agentico.feature.repository.count", "agentico.phase.iteration.count", "agentico.session.outcome.count",
	"agentico.session.token.usage", "agentico.session.turn.count", "agentico.session.retry.count",
	"agentico.session.truncation.count", "agentico.session.context_handoff.count", "agentico.review.outcome.count",
	"agentico.validation.outcome.count", "agentico.verification.item.count", "agentico.publish.outcome.count",
	"agentico.interaction.request.count", "agentico.automatic_review.outcome.count", "agentico.automatic_review.unavailable.count",
	"agentico.recovery.action.count", "agentico.session.critical_message_dropped.count",
}

var metricHistogramNames = map[string]string{
	"agentico.feature.run.duration": "s", "agentico.feature.run.time_to_code_ready": "s", "agentico.feature.run.time_to_delivery": "s",
	"agentico.feature.output.files_changed": "{file}", "agentico.feature.output.lines_added": "{line}", "agentico.feature.output.lines_deleted": "{line}",
	"agentico.feature.output.commit.count": "{commit}", "agentico.phase.duration": "s", "agentico.session.duration": "s",
	"agentico.session.api.duration": "s", "agentico.session.cost": "USD", "agentico.review.duration": "s",
	"agentico.validation.duration": "s", "agentico.validator.duration": "s", "agentico.interaction.wait.duration": "s",
	"agentico.automatic_review.duration": "s", "agentico.publish.duration": "s",
}

type telemetryMetrics struct {
	mp               *sdkmetric.MeterProvider
	counters         map[string]metric.Int64Counter
	histograms       map[string]metric.Float64Histogram
	exportFailures   metric.Int64Counter
	dropped          metric.Int64Counter
	active           atomic.Int64
	httpRequests     metric.Int64Counter
	httpDuration     metric.Float64Histogram
	httpRequestSize  metric.Int64Histogram
	httpResponseSize metric.Int64Histogram
}

func newTelemetryMetrics(enabled bool, endpoint string, insecure bool, res *resource.Resource, stateDir string) *telemetryMetrics {
	if !enabled {
		return nil
	}
	opts := []otlpmetricgrpc.Option{}
	if endpoint != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(endpoint))
	}
	if insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	exporter, err := otlpmetricgrpc.New(context.Background(), opts...)
	if err != nil {
		return nil
	}
	interval := 60 * time.Second
	if raw := strings.TrimSpace(os.Getenv("OTEL_METRIC_EXPORT_INTERVAL")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
			interval = time.Duration(ms) * time.Millisecond
		}
	}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(interval))
	views := []sdkmetric.Option{sdkmetric.WithReader(reader), sdkmetric.WithResource(res)}
	for name := range metricHistogramNames {
		views = append(views, sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: name, Kind: sdkmetric.InstrumentKindHistogram},
			sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: histogramBounds(name)}},
		)))
	}
	for _, name := range []string{"http.server.request.duration", "http.server.request.body.size", "http.server.response.body.size"} {
		views = append(views, sdkmetric.WithView(sdkmetric.NewView(sdkmetric.Instrument{Name: name, Kind: sdkmetric.InstrumentKindHistogram}, sdkmetric.Stream{Aggregation: sdkmetric.AggregationExplicitBucketHistogram{Boundaries: histogramBounds(name)}})))
	}
	mp := sdkmetric.NewMeterProvider(views...)
	meter := mp.Meter("agentico")
	m := &telemetryMetrics{mp: mp, counters: map[string]metric.Int64Counter{}, histograms: map[string]metric.Float64Histogram{}}
	for _, name := range metricCounterNames {
		c, _ := meter.Int64Counter(name, metric.WithUnit(counterUnit(name)))
		m.counters[name] = c
	}
	for name, unit := range metricHistogramNames {
		h, _ := meter.Float64Histogram(name, metric.WithUnit(unit))
		m.histograms[name] = h
	}
	m.exportFailures, _ = meter.Int64Counter("agentico.telemetry.export.failure.count", metric.WithUnit("{failure}"))
	m.dropped, _ = meter.Int64Counter("agentico.telemetry.dropped.count", metric.WithUnit("{event}"))
	m.httpRequests, _ = meter.Int64Counter("http.server.request.count", metric.WithUnit("{request}"))
	m.httpDuration, _ = meter.Float64Histogram("http.server.request.duration", metric.WithUnit("s"))
	m.httpRequestSize, _ = meter.Int64Histogram("http.server.request.body.size", metric.WithUnit("By"))
	m.httpResponseSize, _ = meter.Int64Histogram("http.server.response.body.size", metric.WithUnit("By"))
	activeGauge, _ := meter.Int64ObservableGauge("agentico.feature.active", metric.WithUnit("{feature}"))
	pendingGauge, _ := meter.Int64ObservableGauge("agentico.telemetry.outbox.pending", metric.WithUnit("{event}"))
	bytesGauge, _ := meter.Int64ObservableGauge("agentico.telemetry.outbox.bytes", metric.WithUnit("By"))
	oldestGauge, _ := meter.Float64ObservableGauge("agentico.telemetry.outbox.oldest.age", metric.WithUnit("s"))
	outboxDir := filepath.Join(filepath.Dir(stateDir), "telemetry", "outbox")
	_, _ = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		observer.ObserveInt64(activeGauge, m.active.Load())
		var pending, bytes int64
		var oldest time.Time
		var cursor outboxCursor
		if data, err := os.ReadFile(filepath.Join(outboxDir, "cursor.json")); err == nil {
			_ = json.Unmarshal(data, &cursor)
		}
		entries, _ := os.ReadDir(outboxDir)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), "segment-") {
				continue
			}
			if info, err := entry.Info(); err == nil {
				f, err := os.Open(filepath.Join(outboxDir, entry.Name()))
				if err == nil {
					scanner := bufio.NewScanner(f)
					line := 0
					segmentPending := false
					for scanner.Scan() {
						line++
						if entry.Name() < cursor.Segment || (entry.Name() == cursor.Segment && line <= cursor.Line) {
							continue
						}
						pending++
						bytes += int64(len(scanner.Bytes()) + 1)
						segmentPending = true
					}
					_ = f.Close()
					if segmentPending && (oldest.IsZero() || info.ModTime().Before(oldest)) {
						oldest = info.ModTime()
					}
				}
			}
		}
		observer.ObserveInt64(pendingGauge, pending)
		observer.ObserveInt64(bytesGauge, bytes)
		if !oldest.IsZero() {
			observer.ObserveFloat64(oldestGauge, time.Since(oldest).Seconds())
		}
		return nil
	}, activeGauge, pendingGauge, bytesGauge, oldestGauge)
	m.counters["agentico.runtime.startup.count"].Add(context.Background(), 1)
	return m
}

func counterUnit(name string) string {
	switch name {
	case "agentico.session.token.usage":
		return "{token}"
	case "agentico.session.turn.count":
		return "{turn}"
	case "agentico.feature.repository.count":
		return "{repository}"
	case "agentico.phase.iteration.count":
		return "{iteration}"
	}
	return "{event}"
}

func (m *telemetryMetrics) setActive(n int64) {
	if m != nil {
		m.active.Store(n)
	}
}
func (m *telemetryMetrics) addActive(delta int64) {
	if m != nil {
		m.active.Add(delta)
	}
}

func (m *telemetryMetrics) recordHTTP(route, method string, status int, duration time.Duration, requestBytes, responseBytes int64) {
	if m == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
	m.httpRequests.Add(context.Background(), 1, attrs)
	m.httpDuration.Record(context.Background(), duration.Seconds(), attrs)
	if requestBytes >= 0 {
		m.httpRequestSize.Record(context.Background(), requestBytes, attrs)
	}
	m.httpResponseSize.Record(context.Background(), responseBytes, attrs)
}

func histogramBounds(name string) []float64 {
	switch {
	case strings.Contains(name, "duration") || strings.Contains(name, "time_to") || strings.Contains(name, "wait"):
		return []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600, 14400, 86400}
	case strings.Contains(name, "cost"):
		return []float64{0.001, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 50, 100}
	default:
		return []float64{0, 1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 5000, 10000, 50000}
	}
}

func (m *telemetryMetrics) record(evt Event) {
	if m == nil {
		return
	}
	ctx := context.Background()
	opts := metric.WithAttributes(metricAttrs(evt)...)
	add := func(name string, n int64) {
		if c := m.counters[name]; c != nil {
			c.Add(ctx, n, opts)
		}
	}
	recordSeconds := func(name string) {
		if h := m.histograms[name]; h != nil && evt.DurationMs >= 0 {
			h.Record(ctx, float64(evt.DurationMs)/1000, opts)
		}
	}
	switch evt.EventType {
	case "feature.created":
		add("agentico.feature.created.count", 1)
		if n, ok := number(evt.Data["repository_count"]); ok {
			m.counters["agentico.feature.repository.count"].Add(ctx, int64(n), opts)
		}
	case "feature.state_changed":
		add("agentico.feature.transition.count", 1)
	case "feature.output_ready", "feature.delivered":
		add("agentico.feature.milestone.count", 1)
		if evt.EventType == "feature.output_ready" {
			recordSeconds("agentico.feature.run.time_to_code_ready")
		} else {
			recordSeconds("agentico.feature.run.time_to_delivery")
		}
	case "feature.run_sealed":
		add("agentico.feature.run.terminal.count", 1)
		recordSeconds("agentico.feature.run.duration")
	case "feature.rewound":
		add("agentico.feature.rewind.count", 1)
	case "feature.output_stats_collected":
		for key, name := range map[string]string{"files_changed": "agentico.feature.output.files_changed", "lines_added": "agentico.feature.output.lines_added", "lines_deleted": "agentico.feature.output.lines_deleted", "commit_count": "agentico.feature.output.commit.count"} {
			if n, ok := number(evt.Data[key]); ok {
				m.histograms[name].Record(ctx, n, opts)
			}
		}
	case "phase.duration_recorded":
		recordSeconds("agentico.phase.duration")
	case "iteration.ended":
		add("agentico.phase.iteration.count", 1)
	case "session.ended":
		add("agentico.session.outcome.count", 1)
		recordSeconds("agentico.session.duration")
		if cost, ok := number(evt.Data["total_cost_usd"]); ok {
			m.histograms["agentico.session.cost"].Record(ctx, cost, opts)
		}
		for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
			if n, ok := number(evt.Data[key]); ok {
				m.counters["agentico.session.token.usage"].Add(ctx, int64(n), metric.WithAttributes(append(metricAttrs(evt), attribute.String("token.type", key))...))
			}
		}
		if n, ok := number(evt.Data["api_duration_ms"]); ok {
			m.histograms["agentico.session.api.duration"].Record(ctx, n/1000, opts)
		}
		if n, ok := number(evt.Data["turns"]); ok {
			m.counters["agentico.session.turn.count"].Add(ctx, int64(n), opts)
		}
		if n, ok := number(evt.Data["retry_count"]); ok && n > 0 {
			m.counters["agentico.session.retry.count"].Add(ctx, int64(n), opts)
		}
		if truncated, _ := evt.Data["truncated"].(bool); truncated {
			m.counters["agentico.session.truncation.count"].Add(ctx, 1, opts)
		}
	case "review.completed":
		add("agentico.review.outcome.count", 1)
		recordSeconds("agentico.review.duration")
	case "validation.completed":
		add("agentico.validation.outcome.count", 1)
		recordSeconds("agentico.validation.duration")
	case "validator.completed":
		recordSeconds("agentico.validator.duration")
	case "automatic_review.completed":
		add("agentico.automatic_review.outcome.count", 1)
		recordSeconds("agentico.automatic_review.duration")
	case "automatic_review.unavailable":
		add("agentico.automatic_review.unavailable.count", 1)
	case "session.critical_message_dropped":
		add("agentico.session.critical_message_dropped.count", 1)
	case "verification.item_completed":
		add("agentico.verification.item.count", 1)
	case "permission.requested", "question.asked", "review_gate.requested":
		add("agentico.interaction.request.count", 1)
	case "permission.resolved", "question.answered", "review_gate.resolved":
		recordSeconds("agentico.interaction.wait.duration")
	case "context.handoff_triggered":
		add("agentico.session.context_handoff.count", 1)
	case "recovery.action":
		add("agentico.recovery.action.count", 1)
	case "publish.completed", "publish.failed":
		add("agentico.publish.outcome.count", 1)
		recordSeconds("agentico.publish.duration")
	}
}

func metricAttrs(evt Event) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 5)
	if evt.Phase != "" {
		attrs = append(attrs, attribute.String("phase", metricEnum("phase", evt.Phase)))
	}
	if evt.Status != "" {
		attrs = append(attrs, attribute.String("outcome", metricEnum("outcome", evt.Status)))
	}
	for _, key := range []string{"pipeline", "feature_kind", "risk", "provider", "model", "effort", "failure_type", "publish_mode", "verification_type", "review_type", "validator_type", "interaction_kind", "decision", "automatic"} {
		if v, ok := evt.Data[key].(string); ok && v != "" {
			attrs = append(attrs, attribute.String(key, metricEnum(key, v)))
		}
	}
	return attrs
}

var safeModelPattern = regexp.MustCompile(`^gpt-[0-9](?:\.[0-9]{1,2})?(?:-mini|-codex)?$`)

func metricEnum(key, s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	allowed := map[string]map[string]bool{
		"pipeline":          {"medium": true, "large": true, "moonshot": true},
		"feature_kind":      {"parent": true, "child": true},
		"risk":              {"low": true, "medium": true, "high": true},
		"provider":          {"claude": true, "codex": true, "opencode": true},
		"effort":            {"low": true, "medium": true, "high": true, "xhigh": true, "max": true, "ultra": true, "none": true},
		"failure_type":      {"safety_rail": true, "max_iterations": true, "session_crash": true, "missing_artifact": true, "protocol_violation": true, "infrastructure": true, "worktree_setup": true},
		"publish_mode":      {"published": true, "done": true, "manual": true, "automatic": true, "draft": true},
		"verification_type": {"testing_contract": true, "validator": true, "plan": true},
		"review_type":       {"automatic": true, "final": true, "manual": true},
		"validator_type":    {"plan": true, "roadmap": true, "testing_contract": true},
		"interaction_kind":  {"permission": true, "question": true, "review_gate": true},
		"decision":          {"allow": true, "deny": true, "approved": true, "rejected": true, "answered": true, "waived": true},
		"automatic":         {"true": true, "false": true},
		"phase":             {"setup": true, "knowledgebase": true, "inquire": true, "research": true, "design": true, "plan": true, "implement": true, "review": true, "final_review": true, "publish": true},
		"outcome":           {"created": true, "started": true, "completed": true, "passed": true, "failed": true, "error": true, "ok": true, "success": true, "interrupted": true, "needuserinput": true, "codeready": true, "published": true, "done": true, "delivered": true, "ready": true, "resolved": true, "requested": true, "recorded": true, "unavailable": true, "blocked": true, "waived": true, "regression": true, "inherited_failure": true, "unclassified_failure": true, "missing_capability": true, "contract_error": true, "environment_limited": true, "flaky_failure": true},
	}
	if key == "model" {
		return normalizeMetricModel(v)
	}
	if values := allowed[key]; values != nil && values[v] {
		return v
	}
	return "other"
}

func normalizeMetricModel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if idx := strings.IndexByte(value, '['); idx >= 0 {
		value = value[:idx]
	}
	if idx := strings.LastIndexByte(value, '/'); idx >= 0 {
		value = value[idx+1:]
	}
	switch {
	case value == "opus" || strings.Contains(value, "claude-opus"):
		return "opus"
	case value == "sonnet" || strings.Contains(value, "claude-sonnet"):
		return "sonnet"
	case value == "haiku" || strings.Contains(value, "claude-haiku"):
		return "haiku"
	case value == "codex":
		return "codex"
	case len(value) <= 32 && safeModelPattern.MatchString(value):
		return value
	default:
		return "other"
	}
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case float32:
		return float64(n), true
	}
	return 0, false
}

func (m *telemetryMetrics) Shutdown(ctx context.Context) error {
	if m == nil || m.mp == nil {
		return nil
	}
	return m.mp.Shutdown(ctx)
}
