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

package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResolveListenAddrAcceptedForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  string
	}{
		{"", DefaultListenAddr},
		{"  ", DefaultListenAddr},
		{"8080", "127.0.0.1:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"localhost:9000", "localhost:9000"},
		{"[::1]:8080", "[::1]:8080"},
		{":8080", "127.0.0.1:8080"},
	}
	for _, tc := range tests {
		got, err := ResolveListenAddr(tc.value)
		if err != nil {
			t.Errorf("ResolveListenAddr(%q) error = %v", tc.value, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveListenAddr(%q) = %q; want %q", tc.value, got, tc.want)
		}
	}
}

// TestResolveListenPolicyMatrix pins the bind-mode policy selection: loopback
// forms keep the loopback policy; concrete non-loopback IPs, non-loopback
// hostnames, and wildcards select the network policy.
func TestResolveListenPolicyMatrix(t *testing.T) {
	tests := []struct {
		value       string
		wantBind    string
		wantAdvert  string
		wantPolicy  string
		wantWilcard bool
	}{
		{"", DefaultListenAddr, "127.0.0.1", CompatibilityRuntimePolicy, false},
		{"8080", "127.0.0.1:8080", "127.0.0.1", CompatibilityRuntimePolicy, false},
		{":8080", "127.0.0.1:8080", "127.0.0.1", CompatibilityRuntimePolicy, false},
		{"127.0.0.1:8080", "127.0.0.1:8080", "127.0.0.1", CompatibilityRuntimePolicy, false},
		{"127.0.0.2:8080", "127.0.0.2:8080", "127.0.0.2", CompatibilityRuntimePolicy, false},
		{"localhost:9000", "localhost:9000", "localhost", CompatibilityRuntimePolicy, false},
		{"[::1]:8080", "[::1]:8080", "::1", CompatibilityRuntimePolicy, false},
		{"192.168.1.10:8080", "192.168.1.10:8080", "192.168.1.10", CompatibilityNetworkRuntimePolicy, false},
		{"[fe80::1]:8080", "[fe80::1]:8080", "fe80::1", CompatibilityNetworkRuntimePolicy, false},
		{"0.0.0.0:8080", "0.0.0.0:8080", "10.9.8.7", CompatibilityNetworkRuntimePolicy, true},
		{"[::]:8080", "[::]:8080", "10.9.8.7", CompatibilityNetworkRuntimePolicy, true},
	}
	restoreProbe := probePrimaryIPv4
	probePrimaryIPv4 = func() (string, error) { return "10.9.8.7", nil }
	t.Cleanup(func() { probePrimaryIPv4 = restoreProbe })
	for _, tc := range tests {
		res, err := ResolveListen(tc.value)
		if err != nil {
			t.Errorf("ResolveListen(%q) error = %v", tc.value, err)
			continue
		}
		if res.BindAddr != tc.wantBind {
			t.Errorf("ResolveListen(%q).BindAddr = %q; want %q", tc.value, res.BindAddr, tc.wantBind)
		}
		if res.Policy != tc.wantPolicy {
			t.Errorf("ResolveListen(%q).Policy = %q; want %q", tc.value, res.Policy, tc.wantPolicy)
		}
		if res.Wildcard != tc.wantWilcard {
			t.Errorf("ResolveListen(%q).Wildcard = %v; want %v", tc.value, res.Wildcard, tc.wantWilcard)
		}
		if res.AdvertiseHost != tc.wantAdvert {
			t.Errorf("ResolveListen(%q).AdvertiseHost = %q; want %q", tc.value, res.AdvertiseHost, tc.wantAdvert)
		}
	}
}

// TestResolveListenHostnameClassification pins hostname classification: a
// hostname is loopback only when it resolves exclusively to loopback
// addresses.
func TestResolveListenHostnameClassification(t *testing.T) {
	restoreLookup := lookupListenHostIPs
	lookupListenHostIPs = func(host string) ([]net.IP, error) {
		switch host {
		case "loop-only":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, nil
		case "net-host":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "mixed-host":
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.1.2.3")}, nil
		}
		return nil, fmt.Errorf("no such host")
	}
	t.Cleanup(func() { lookupListenHostIPs = restoreLookup })

	cases := []struct {
		value      string
		wantPolicy string
	}{
		{"loop-only:8080", CompatibilityRuntimePolicy},
		{"net-host:8080", CompatibilityNetworkRuntimePolicy},
		{"mixed-host:8080", CompatibilityNetworkRuntimePolicy},
	}
	for _, tc := range cases {
		res, err := ResolveListen(tc.value)
		if err != nil {
			t.Errorf("ResolveListen(%q) error = %v", tc.value, err)
			continue
		}
		if res.Policy != tc.wantPolicy {
			t.Errorf("ResolveListen(%q).Policy = %q; want %q", tc.value, res.Policy, tc.wantPolicy)
		}
	}
	if _, err := ResolveListen("unknown-host:8080"); err == nil || !strings.Contains(err.Error(), "resolve hostname") {
		t.Fatalf("ResolveListen(unknown-host:8080) error = %v; want a resolve error", err)
	}
}

// TestResolveListenWildcardRequiresInterface pins fail-fast wildcard
// resolution when no non-loopback interface is available.
func TestResolveListenWildcardRequiresInterface(t *testing.T) {
	restoreProbe := probePrimaryIPv4
	probePrimaryIPv4 = func() (string, error) { return "", fmt.Errorf("no non-loopback interface found") }
	t.Cleanup(func() { probePrimaryIPv4 = restoreProbe })
	if _, err := ResolveListen("0.0.0.0:8080"); err == nil ||
		!strings.Contains(err.Error(), "non-loopback network interface") ||
		!strings.Contains(err.Error(), "loopback bind") {
		t.Fatalf("ResolveListen(0.0.0.0:8080) error = %v; want actionable no-interface error", err)
	}
}

func TestResolveListenAddrRejectsBadPorts(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"0",
		"65536",
		"-1",
		"99999",
		"127.0.0.1:abc",
		"127.0.0.1:0",
		"localhost:65536",
	} {
		_, err := ResolveListenAddr(value)
		if err == nil {
			t.Errorf("ResolveListenAddr(%q) succeeded; want port rejection", value)
			continue
		}
		if !strings.Contains(err.Error(), "65535") {
			t.Errorf("ResolveListenAddr(%q) error = %q; want a clear port boundary error", value, err)
		}
	}
}

func TestResolveListenAddrRejectsMalformedValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"abc",
		"127.0.0.1",
		"127.0.0.1:",
		"http://127.0.0.1:8080",
		"127.0.0.1:8080:extra",
	} {
		if _, err := ResolveListenAddr(value); err == nil {
			t.Errorf("ResolveListenAddr(%q) succeeded; want a parse error", value)
		}
	}
}

// TestStartRejectsBusyListenAddr pins fail-fast bind behavior: an occupied
// port surfaces an error naming the address, with no retry and no started
// server.
func TestStartRejectsBusyListenAddr(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	srv, err := Start(context.Background(), Options{
		AllowUnauthenticated: true,
		AuthToken:            "token",
		ListenAddr:           ln.Addr().String(),
	})
	if err == nil {
		_ = srv.Close(context.Background())
		t.Fatal("Start() succeeded on an occupied address; want a bind error")
	}
	if !strings.Contains(err.Error(), ln.Addr().String()) {
		t.Fatalf("Start() error = %q; want the busy address named", err)
	}
}

// TestStartHonorsExplicitLoopbackPin binds a named loopback address and
// confirms the advertised base URL matches it.
func TestStartHonorsExplicitLoopbackPin(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	srv, err := Start(context.Background(), Options{
		AllowUnauthenticated: true,
		AuthToken:            "token",
		ListenAddr:           "127.0.0.1:" + strconv.Itoa(port),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	if !strings.HasSuffix(srv.BaseURL(), ":"+strconv.Itoa(port)) {
		t.Fatalf("BaseURL() = %q; want port %d", srv.BaseURL(), port)
	}
	if srv.RuntimePolicy() != CompatibilityRuntimePolicy {
		t.Fatalf("RuntimePolicy() = %q; want %q", srv.RuntimePolicy(), CompatibilityRuntimePolicy)
	}
	if srv.WildcardBind() {
		t.Fatal("WildcardBind() = true for a loopback bind; want false")
	}
}

// TestStartWildcardAdvertisesPrimaryAddress pins wildcard-bind advertising:
// the base URL carries the resolved primary interface address (never
// 0.0.0.0), and the runtime policy is the network policy.
func TestStartWildcardAdvertisesPrimaryAddress(t *testing.T) {
	restoreProbe := probePrimaryIPv4
	probePrimaryIPv4 = func() (string, error) { return "10.9.8.7", nil }
	t.Cleanup(func() { probePrimaryIPv4 = restoreProbe })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	srv, err := Start(context.Background(), Options{
		AllowUnauthenticated: true,
		AuthToken:            "token",
		ListenAddr:           "0.0.0.0:" + strconv.Itoa(port),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	want := "http://10.9.8.7:" + strconv.Itoa(port)
	if srv.BaseURL() != want {
		t.Fatalf("BaseURL() = %q; want advertised primary address %q", srv.BaseURL(), want)
	}
	if srv.RuntimePolicy() != CompatibilityNetworkRuntimePolicy {
		t.Fatalf("RuntimePolicy() = %q; want %q", srv.RuntimePolicy(), CompatibilityNetworkRuntimePolicy)
	}
	if !srv.WildcardBind() {
		t.Fatal("WildcardBind() = false for a wildcard bind; want true")
	}
}
