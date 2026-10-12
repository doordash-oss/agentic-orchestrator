// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

// endpointProvider is a ready provider that reports custom endpoints.
type endpointProvider struct {
	*readinessProbeProvider
	endpoints []string
}

func (p *endpointProvider) ConfiguredEndpoints() []string { return p.endpoints }

func newEndpointProvider(name string, endpoints ...string) *endpointProvider {
	return &endpointProvider{
		readinessProbeProvider: &readinessProbeProvider{
			MockProvider: &mocks.MockProvider{ProviderName: name, CLIDetected: true, Models: []string{name + "-model"}, VersionInfoResult: "1.0.0"},
			status:       staticReadiness(llm.ProviderReadiness{Ready: true}),
		},
		endpoints: endpoints,
	}
}

func closedPortURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr + "/v1"
}

func TestReadinessRefreshWarnsOnUnreachableProviderEndpoint(t *testing.T) {
	t.Parallel()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ok.Close()
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthorized.Close()
	refused := closedPortURL(t)

	registry := newReadinessRegistry(
		newEndpointProvider("reachable", ok.URL+"/v1"),
		newEndpointProvider("unauthorized", unauthorized.URL+"/v1"),
		newEndpointProvider("offline", ok.URL+"/v1", refused),
		&readinessProbeProvider{
			MockProvider: &mocks.MockProvider{ProviderName: "plain", CLIDetected: true, Models: []string{"m"}},
			status:       staticReadiness(llm.ProviderReadiness{Ready: true}),
		},
	)
	handler := NewHandler(HandlerOptions{Registry: registry, Mutations: &createFeatureRecorder{}, DisableHostValidation: true})

	w := postTrustedJSON(handler, "/api/v1/readiness/runtime/refresh", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body=%s", w.Code, w.Body.String())
	}
	var snapshot ReadinessResponse
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Ready {
		t.Fatalf("snapshot.Ready = false; an unreachable endpoint must not block readiness: %+v", snapshot.Issues)
	}
	for _, name := range []string{"reachable", "unauthorized"} {
		entry := providerEntry(t, snapshot, name)
		if entry.EndpointReachable == nil || !*entry.EndpointReachable || entry.Warning != nil {
			t.Errorf("%s entry = %+v; want endpoint_reachable=true without warning", name, entry)
		}
	}
	if plain := providerEntry(t, snapshot, "plain"); plain.EndpointReachable != nil || plain.Warning != nil {
		t.Errorf("plain entry = %+v; want no endpoint fields without a configured endpoint", plain)
	}
	offline := providerEntry(t, snapshot, "offline")
	if !offline.Ready || offline.Issue != nil {
		t.Errorf("offline entry = %+v; want ready with no blocking issue", offline)
	}
	if offline.EndpointReachable == nil || *offline.EndpointReachable {
		t.Fatalf("offline endpoint_reachable = %v; want false", offline.EndpointReachable)
	}
	warning := offline.Warning
	if warning == nil || warning.Code != string(errcat.ProviderEndpointUnreachable) || warning.Class != "warning" {
		t.Fatalf("offline warning = %+v; want warning-class provider_endpoint_unreachable", warning)
	}
	host := llm.EndpointHost(refused)
	if want := "Provider endpoint " + host + " (offline) is unreachable; offline sessions will fail until it can be reached."; warning.Summary != want {
		t.Errorf("warning summary = %q; want %q", warning.Summary, want)
	}
	if warning.Remediation == nil || warning.Remediation.Hint == "" || warning.Diagnostics == "" {
		t.Errorf("warning = %+v; want a remediation hint and transport diagnostics", warning)
	}
	found := false
	for _, issue := range snapshot.Issues {
		found = found || issue.Code == string(errcat.ProviderEndpointUnreachable)
	}
	if !found {
		t.Errorf("issues = %+v; want the endpoint warning flattened in", snapshot.Issues)
	}
}

func TestReadinessReadDoesNotWaitForEndpointChecks(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hanging.Close()
	defer close(release)

	handler := NewHandler(HandlerOptions{
		Registry:              newReadinessRegistry(newEndpointProvider("slow", hanging.URL)),
		DisableHostValidation: true,
	})
	start := time.Now()
	snapshot := getReadinessSnapshot(t, handler)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("GET readiness took %v; endpoint checks must run in the background", elapsed)
	}
	if entry := providerEntry(t, snapshot, "slow"); !entry.Ready || entry.EndpointReachable != nil {
		t.Fatalf("slow entry = %+v; want ready with the endpoint result still pending", entry)
	}
}

func TestProbeEndpointsTimesOutHangingEndpoint(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hanging.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	results := probeEndpoints(ctx, hanging.Client(), map[string][]string{"slow": {hanging.URL}})
	if got := results["slow"]; got.reachable || got.host != llm.EndpointHost(hanging.URL) || got.detail == "" {
		t.Fatalf("probeEndpoints() = %+v; want unreachable on timeout", got)
	}
}
