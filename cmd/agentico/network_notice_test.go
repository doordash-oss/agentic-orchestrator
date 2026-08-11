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

package main

import (
	"strings"
	"testing"

	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// TestWriteNetworkAccessNoticeLoopbackSilent pins the loopback posture: no
// notice, no connection string, no token printed — the startup output is
// byte-for-byte the loopback "listening at" line and nothing else.
func TestWriteNetworkAccessNoticeLoopbackSilent(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := writeNetworkAccessNotice(&buf, serverruntime.CompatibilityRuntimePolicy, "http://127.0.0.1:8080", false, "secret-token", "server"); err != nil {
		t.Fatalf("writeNetworkAccessNotice() error = %v", err)
	}
	if buf.String() != "" {
		t.Fatalf("loopback output = %q; want silent", buf.String())
	}
}

// TestWriteNetworkAccessNoticeNetworkOutput pins the network-bind output: one
// security notice, exactly one agentico:// connection string carrying the
// advertised address, token, and urlencoded name, and the wildcard variance
// line naming the resolved primary address.
func TestWriteNetworkAccessNoticeNetworkOutput(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	if err := writeNetworkAccessNotice(&buf, serverruntime.CompatibilityNetworkRuntimePolicy, "http://10.9.8.7:8080", true, "tok-xyz", "my server"); err != nil {
		t.Fatalf("writeNetworkAccessNotice() error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "SECURITY NOTICE") || !strings.Contains(out, "trusted network") ||
		!strings.Contains(out, "SSH tunneling") || !strings.Contains(out, "Tailscale") {
		t.Fatalf("notice missing required warnings:\n%s", out)
	}
	if !strings.Contains(out, "Listening on all interfaces") || !strings.Contains(out, "10.9.8.7") {
		t.Fatalf("wildcard line missing the advertised primary address:\n%s", out)
	}
	wantStr := "Connection string: agentico://tok-xyz@10.9.8.7:8080?name=my+server"
	if !strings.Contains(out, wantStr) {
		t.Fatalf("output missing %q:\n%s", wantStr, out)
	}
	if strings.Count(out, "agentico://") != 1 {
		t.Fatalf("output prints %d agentico:// strings; want exactly one:\n%s", strings.Count(out, "agentico://"), out)
	}

	// A concrete (non-wildcard) bind prints no all-interfaces line.
	buf.Reset()
	if err := writeNetworkAccessNotice(&buf, serverruntime.CompatibilityNetworkRuntimePolicy, "http://192.168.1.10:8080", false, "tok", "srv"); err != nil {
		t.Fatalf("writeNetworkAccessNotice() error = %v", err)
	}
	if strings.Contains(buf.String(), "all interfaces") {
		t.Fatalf("concrete bind printed the all-interfaces line:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "agentico://tok@192.168.1.10:8080?name=srv") {
		t.Fatalf("concrete bind connection string wrong:\n%s", buf.String())
	}
}
