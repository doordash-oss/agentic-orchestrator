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
	"net"
	"strings"
	"testing"
	"time"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

// captureStartup swaps the startup log sink and probe for the test's
// lifetime. The probe is disabled so the constructor stays synchronous and
// the captured lines are deterministic.
func captureStartup(t *testing.T) *[]string {
	t.Helper()
	var lines []string
	prevLog, prevProbe := otelStartupLogf, otelStartupProbe
	otelStartupLogf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	otelStartupProbe = func(string) {}
	t.Cleanup(func() {
		otelStartupLogf, otelStartupProbe = prevLog, prevProbe
	})
	return &lines
}

func requireLine(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, want) {
			return
		}
	}
	t.Fatalf("no startup line contains %q; got %q", want, lines)
}

func TestEffectiveOTLPEndpoint(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		traces     string
		generic    string
		want       string
	}{
		{name: "configured wins", configured: "collector.internal:4317", traces: "x:1", generic: "y:2", want: "collector.internal:4317"},
		{name: "traces env before generic", traces: "http://traces.internal:4318/v1/traces", generic: "y:2", want: "traces.internal:4318"},
		{name: "generic env strips scheme", generic: "https://generic.internal:4317", want: "generic.internal:4317"},
		{name: "sdk default", want: "localhost:4317"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", tc.traces)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", tc.generic)
			if got := effectiveOTLPEndpoint(tc.configured); got != tc.want {
				t.Fatalf("effectiveOTLPEndpoint(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestCollectorProber(t *testing.T) {
	t.Parallel()
	resolveOK := func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	realDial := (&net.Dialer{}).DialContext

	t.Run("reachable endpoint returns no reason", func(t *testing.T) {
		t.Parallel()
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer lis.Close()
		go func() {
			for {
				c, err := lis.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}()
		p := collectorProber{lookupHost: resolveOK, dial: realDial}
		if reason := p.probe(context.Background(), lis.Addr().String()); reason != "" {
			t.Fatalf("expected reachable, got %q", reason)
		}
	})

	t.Run("closed port names the endpoint", func(t *testing.T) {
		t.Parallel()
		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := lis.Addr().String()
		_ = lis.Close()
		p := collectorProber{lookupHost: resolveOK, dial: realDial}
		reason := p.probe(context.Background(), addr)
		if !strings.Contains(reason, addr) || !strings.Contains(reason, "not accepting connections") {
			t.Fatalf("unexpected reason %q", reason)
		}
	})

	t.Run("unresolvable host is reported as DNS, not as a closed port", func(t *testing.T) {
		t.Parallel()
		p := collectorProber{
			lookupHost: func(context.Context, string) ([]string, error) {
				return nil, errors.New("no such host")
			},
			dial: func(context.Context, string, string) (net.Conn, error) {
				t.Fatal("dial must not run when the name does not resolve")
				return nil, nil
			},
		}
		reason := p.probe(context.Background(), "otel-collector.invalid:4317")
		if !strings.Contains(reason, "otel-collector.invalid does not resolve") {
			t.Fatalf("unexpected reason %q", reason)
		}
	})

	t.Run("literal IP skips resolution", func(t *testing.T) {
		t.Parallel()
		p := collectorProber{
			lookupHost: func(context.Context, string) ([]string, error) {
				t.Fatal("lookup must not run for an IP literal")
				return nil, nil
			},
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("connection refused")
			},
		}
		if reason := p.probe(context.Background(), "127.0.0.1:1"); !strings.Contains(reason, "connection refused") {
			t.Fatalf("unexpected reason %q", reason)
		}
	})

	t.Run("malformed endpoint", func(t *testing.T) {
		t.Parallel()
		p := collectorProber{lookupHost: resolveOK, dial: realDial}
		if reason := p.probe(context.Background(), "no-port"); !strings.Contains(reason, "invalid endpoint") {
			t.Fatalf("unexpected reason %q", reason)
		}
	})
}

func TestNewOtelBridgeStartupReport(t *testing.T) {
	t.Run("disabled says so", func(t *testing.T) {
		lines := captureStartup(t)
		_ = newOtelBridge(false, "", false, "agentico")
		requireLine(t, *lines, "trace export disabled")
	})

	t.Run("enabled reports the effective endpoint and service", func(t *testing.T) {
		lines := captureStartup(t)
		collector := &mockTraceCollector{}
		lis, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		srv := grpc.NewServer()
		collectortracepb.RegisterTraceServiceServer(srv, collector)
		go func() { _ = srv.Serve(lis) }()
		defer srv.Stop()

		b := newOtelBridge(true, lis.Addr().String(), true, "svc-under-test")
		defer b.Shutdown()
		requireLine(t, *lines, "trace export enabled: endpoint="+lis.Addr().String()+" insecure=true service=svc-under-test")
		for _, l := range *lines {
			if strings.Contains(l, "create OTLP exporter") {
				t.Fatalf("unexpected exporter failure line: %q", l)
			}
		}
	})

	t.Run("probe outcome is logged asynchronously", func(t *testing.T) {
		got := make(chan string, 4)
		prevLog := otelStartupLogf
		otelStartupLogf = func(format string, args ...any) { got <- fmt.Sprintf(format, args...) }
		t.Cleanup(func() { otelStartupLogf = prevLog })

		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := lis.Addr().String()
		_ = lis.Close()

		otelStartupProbe(addr)
		select {
		case line := <-got:
			if !strings.Contains(line, "collector unreachable at startup") || !strings.Contains(line, addr) {
				t.Fatalf("unexpected probe line %q", line)
			}
		case <-time.After(collectorProbeTimeout + time.Second):
			t.Fatal("probe never reported")
		}
	})
}
