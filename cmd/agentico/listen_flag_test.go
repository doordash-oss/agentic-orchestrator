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
	"bytes"
	"strings"
	"testing"
)

func TestParseLaunchArgsListenAcceptedForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  string
	}{
		{"8080", "127.0.0.1:8080"},
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"127.0.0.2:8080", "127.0.0.2:8080"},
		{"localhost:8080", "localhost:8080"},
		{"[::1]:8080", "[::1]:8080"},
		{"192.168.0.5:8080", "192.168.0.5:8080"},
		{"0.0.0.0:8080", "0.0.0.0:8080"},
		{"[::]:8080", "[::]:8080"},
	}
	for _, tc := range tests {
		opts, err := parseLaunchArgs([]string{cliSubcommandServer, "--listen", tc.value})
		if err != nil {
			t.Errorf("parseLaunchArgs(server --listen %q) error = %v", tc.value, err)
			continue
		}
		if opts.listenAddr != tc.want {
			t.Errorf("parseLaunchArgs(server --listen %q) listenAddr = %q; want %q", tc.value, opts.listenAddr, tc.want)
		}
	}
}

func TestParseLaunchArgsListenRejectsBadValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value   string
		wantErr string
	}{
		{"abc", "--listen"},
		{"0", "65535"},
		{"65536", "65535"},
		{"127.0.0.1:", "--listen"},
		{"10.0.0.1:", "--listen"},
		{"10.0.0.1:abc", "65535"},
	}
	for _, tc := range tests {
		_, err := parseLaunchArgs([]string{cliSubcommandServer, "--listen", tc.value})
		if err == nil {
			t.Errorf("parseLaunchArgs(server --listen %q) succeeded; want rejection", tc.value)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("parseLaunchArgs(server --listen %q) error = %q; want token %q", tc.value, err, tc.wantErr)
		}
	}
}

func TestParseLaunchArgsFlaglessLeavesEphemeralListen(t *testing.T) {
	t.Parallel()
	opts, err := parseLaunchArgs([]string{cliSubcommandServer})
	if err != nil {
		t.Fatalf("parseLaunchArgs(server) error = %v", err)
	}
	if opts.listenAddr != "" {
		t.Fatalf("listenAddr = %q; want empty (ephemeral loopback default)", opts.listenAddr)
	}
	if opts.serverName != "" {
		t.Fatalf("serverName = %q; want empty (generated/persisted default)", opts.serverName)
	}
}

func TestParseLaunchArgsListenAndNameRequireServerSubcommand(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--listen", "8080"},
		{"--name", "my-server"},
	} {
		_, err := parseLaunchArgs(args)
		if err == nil || !strings.Contains(err.Error(), "available only with the headless server") {
			t.Errorf("parseLaunchArgs(%v) error = %v, want headless-server guidance", args, err)
		}
	}
}

func TestParseLaunchArgsNameValidation(t *testing.T) {
	t.Parallel()
	opts, err := parseLaunchArgs([]string{cliSubcommandServer, "--name", "  comfy-latte  "})
	if err != nil {
		t.Fatalf("parseLaunchArgs(server --name) error = %v", err)
	}
	if opts.serverName != "comfy-latte" {
		t.Fatalf("serverName = %q; want trimmed comfy-latte", opts.serverName)
	}
	for _, bad := range []string{"", "bad\nname", strings.Repeat("n", 65)} {
		if bad == "" {
			continue // bare "--name" with an empty string is treated as absent
		}
		if _, err := parseLaunchArgs([]string{cliSubcommandServer, "--name", bad}); err == nil {
			t.Errorf("parseLaunchArgs(server --name %q) succeeded; want rejection", bad)
		}
	}
	if _, err := parseLaunchArgs([]string{cliSubcommandServer, "--listen"}); err == nil {
		t.Error("parseLaunchArgs(server --listen) without value succeeded; want error")
	}
	if _, err := parseLaunchArgs([]string{cliSubcommandServer, "--name"}); err == nil {
		t.Error("parseLaunchArgs(server --name) without value succeeded; want error")
	}
}

func TestPrintUsageListsListenAndName(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printUsage(&buf)
	out := buf.String()
	for _, want := range []string{"--listen [host:]port", "--name <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage text missing %q:\n%s", want, out)
		}
	}
}
