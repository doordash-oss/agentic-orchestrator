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
package llm

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeEndpointTreatsAnyHTTPResponseAsReachable(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusNotFound, http.StatusBadGateway} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" {
				t.Errorf("probe sent %s with Authorization %q; want bare HEAD", r.Method, r.Header.Get("Authorization"))
			}
			w.WriteHeader(status)
		}))
		err := ProbeEndpoint(context.Background(), srv.Client(), srv.URL+"/v1")
		srv.Close()
		if err != nil {
			t.Errorf("status %d: ProbeEndpoint() = %v; want reachable", status, err)
		}
	}
}

func TestProbeEndpointReportsConnectionRefused(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if err := ProbeEndpoint(context.Background(), http.DefaultClient, "http://"+addr+"/v1"); err == nil {
		t.Fatal("ProbeEndpoint() = nil for a closed port; want connection error")
	}
}

func TestProbeEndpointHonorsDeadline(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := ProbeEndpoint(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("ProbeEndpoint() = nil for a hanging server; want timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("ProbeEndpoint() took %v; want bounded by the context deadline", elapsed)
	}
}

func TestEndpointURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://gw.example.team/v1":        "https://gw.example.team/v1",
		" https://u:p@gw.example/v1?key=x ": "https://gw.example/v1",
		"http://localhost:4000":             "http://localhost:4000",
		"":                                  "",
		"gw.example/v1":                     "",
		"ftp://gw.example":                  "",
		"{env:GATEWAY_URL}":                 "",
	}
	for raw, want := range cases {
		got, ok := EndpointURL(raw)
		if got != want || ok != (want != "") {
			t.Errorf("EndpointURL(%q) = %q, %v; want %q", raw, got, ok, want)
		}
	}
	if got := EndpointHost("https://gw.example.team:8443/v1"); got != "gw.example.team:8443" {
		t.Errorf("EndpointHost() = %q", got)
	}
}
