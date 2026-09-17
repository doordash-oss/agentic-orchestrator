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
	"testing"
	"time"
)

// TestRuntimeServerBindDefaultLoopback pins the default ephemeral bind: an
// empty ListenAddr binds 127.0.0.1 on an OS-assigned port, so BindHost
// reports the loopback host and Port a non-zero assigned port.
func TestRuntimeServerBindDefaultLoopback(t *testing.T) {
	t.Parallel()
	srv, err := Start(context.Background(), Options{
		AllowUnauthenticated: true,
		AuthToken:            "token",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	if got := srv.BindHost(); got != "127.0.0.1" {
		t.Fatalf("BindHost() = %q; want %q", got, "127.0.0.1")
	}
	if got := srv.Port(); got <= 0 {
		t.Fatalf("Port() = %d; want an assigned port > 0", got)
	}
}

// TestRuntimeServerBindFixedLoopbackPort pins that an explicitly pinned
// loopback port is reported exactly as bound.
func TestRuntimeServerBindFixedLoopbackPort(t *testing.T) {
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
	if got := srv.BindHost(); got != "127.0.0.1" {
		t.Fatalf("BindHost() = %q; want %q", got, "127.0.0.1")
	}
	if got := srv.Port(); got != port {
		t.Fatalf("Port() = %d; want the pinned port %d", got, port)
	}
}

// TestRuntimeServerBindWildcardPreservesWildcardHost pins that a wildcard
// bind reports the wildcard host itself (never the advertised primary
// interface address) so the same wildcard address can be rebound. Darwin
// maps an 0.0.0.0 request onto a dual-stack [::] listener, so both wildcard
// forms are accepted: what matters is that the reported host is the one the
// kernel actually bound and an equivalent rebinding can use. Explicit
// wildcard ports must be 1-65535, so an OS-assigned port is reserved first
// and then rebound, matching the wildcard pattern in listen_test.go.
func TestRuntimeServerBindWildcardPreservesWildcardHost(t *testing.T) {
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
	if got := srv.BindHost(); got != "0.0.0.0" && got != "::" {
		t.Fatalf("BindHost() = %q, want a wildcard host (0.0.0.0 or ::)", got)
	}
	if got := srv.Port(); got != port || got <= 0 {
		t.Fatalf("Port() = %d; want the assigned port %d", got, port)
	}
}

// TestRuntimeServerBindNilReceiverAccessors pins nil-receiver safety: the
// bind accessors return zero values without panicking.
func TestRuntimeServerBindNilReceiverAccessors(t *testing.T) {
	t.Parallel()
	var srv *RuntimeServer
	if got := srv.BindHost(); got != "" {
		t.Fatalf("nil BindHost() = %q; want %q", got, "")
	}
	if got := srv.Port(); got != 0 {
		t.Fatalf("nil Port() = %d; want 0", got)
	}
}
