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

func TestResolveListenAddrRejectsNonLoopbackHosts(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"127.0.0.2:8080",
		"0.0.0.0:8080",
		"192.168.1.10:8080",
		"example.com:8080",
		"[fe80::1]:8080",
	} {
		_, err := ResolveListenAddr(value)
		if err == nil {
			t.Errorf("ResolveListenAddr(%q) succeeded; want loopback-only rejection", value)
			continue
		}
		if !strings.Contains(err.Error(), "loopback") || !strings.Contains(err.Error(), "network policy") {
			t.Errorf("ResolveListenAddr(%q) error = %q; want actionable loopback-only steering", value, err)
		}
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
}
