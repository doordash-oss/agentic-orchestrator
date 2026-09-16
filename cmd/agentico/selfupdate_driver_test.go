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

//go:build agentico_selfupdate_driver

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// resetDriverState clears the package-level driver state and registers a
// cleanup restoring it (and the discovery publish seam) after the test.
// These tests touch package-level mutable globals, so none of them may run
// in parallel.
func resetDriverState(t *testing.T) {
	t.Helper()
	origPublish := publishDiscoveryFn
	reset := func() {
		publishDiscoveryFn = origPublish
		driver.candidatePath = ""
		driver.triggerFile = ""
		driver.failAt = ""
		driver.prepareOnly = false
		driver.adopted = nil
		driver.adoptedMeta = selfupdate.HandoffMetadata{}
		driver.adoptedReceipt = selfupdate.Receipt{}
	}
	reset()
	t.Cleanup(reset)
}

func TestSelfUpdateDriverArgParse(t *testing.T) {
	resetDriverState(t)

	opts, handled, err := driverArgParseHook([]string{
		cliSubcommandSelfUpdateDriver, "--candidate", "x", "--trigger-file", "y",
		"--config", "c", "--state-dir", "s",
	})
	if err != nil || !handled {
		t.Fatalf("driverArgParseHook() = handled %v, err %v; want handled, nil", handled, err)
	}
	if opts.mode != launchModeServer {
		t.Fatalf("mode = %v; want launchModeServer", opts.mode)
	}
	if opts.configPath != "c" || opts.stateDir != "s" {
		t.Fatalf("config/state = %q/%q; want c/s", opts.configPath, opts.stateDir)
	}
	if driver.candidatePath != "x" || driver.triggerFile != "y" {
		t.Fatalf("driver candidate/trigger = %q/%q; want x/y", driver.candidatePath, driver.triggerFile)
	}
	if driver.failAt != "" || driver.prepareOnly {
		t.Fatalf("driver failAt/prepareOnly = %q/%v; want defaults", driver.failAt, driver.prepareOnly)
	}
	if driver.serverFlags.mode != launchModeServer {
		t.Fatalf("driver.serverFlags.mode = %v; want launchModeServer", driver.serverFlags.mode)
	}

	// Server-style flags left out keep the launch defaults.
	defaults := defaultLaunchOptions()
	bare, handled, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--candidate", "x"})
	if err != nil || !handled {
		t.Fatalf("driverArgParseHook(bare) = handled %v, err %v; want handled, nil", handled, err)
	}
	if bare.configPath != defaults.configPath || bare.stateDir != defaults.stateDir {
		t.Fatalf("bare defaults = %q/%q; want %q/%q", bare.configPath, bare.stateDir, defaults.configPath, defaults.stateDir)
	}
	if bare.listenAddr != "" || bare.serverName != "" || bare.refreshModels || bare.dangerouslySkipPerms {
		t.Fatalf("bare opts = %+v; want untouched defaults", bare)
	}

	// Unknown flags keep the server parsing error shape.
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--bogus"})
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown flag: --bogus") {
		t.Fatalf("unknown flag: handled %v, err %v; want server-shaped unknown-flag error", handled, err)
	}

	// Non-driver argument vectors fall through untouched.
	_, handled, err = driverArgParseHook([]string{cliSubcommandServer, "--config", "c"})
	if handled || err != nil {
		t.Fatalf("server args: handled %v, err %v; want fall-through", handled, err)
	}
	_, handled, err = driverArgParseHook(nil)
	if handled || err != nil {
		t.Fatalf("empty args: handled %v, err %v; want fall-through", handled, err)
	}

	// Invalid --fail-at points are rejected at parse time.
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--fail-at", "nonsense"})
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown --fail-at point") {
		t.Fatalf("invalid fail-at: handled %v, err %v; want rejection", handled, err)
	}

	// --prepare-only records the flag.
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--candidate", "x", "--prepare-only"}); err != nil {
		t.Fatalf("prepare-only parse: %v", err)
	}
	if !driver.prepareOnly {
		t.Fatal("prepareOnly = false; want true")
	}
}

func TestSelfUpdateDriverFailAtDiscoveryStubsPublish(t *testing.T) {
	resetDriverState(t)

	// Parsing alone must NOT arm the stub: the old image still publishes its
	// own discovery record normally.
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--fail-at", "discovery"}); err != nil {
		t.Fatalf("parse --fail-at discovery: %v", err)
	}
	if err := publishDiscoveryFn("irrelevant", serverruntime.DiscoveryRecord{}); err != nil {
		t.Fatalf("publishDiscoveryFn() error = %v; want the real function after parse alone", err)
	}

	// Arming happens only on the adopted image.
	armDiscoveryFailure()
	err := publishDiscoveryFn("irrelevant", serverruntime.DiscoveryRecord{})
	if err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("publishDiscoveryFn() error = %v; want injected discovery publish failure", err)
	}
}

func TestSelfUpdateDriverAdoptionHookWithoutEnvReturnsNils(t *testing.T) {
	resetDriverState(t)
	if handoffEnvPresent(os.Environ()) {
		t.Skip("handoff environment entry present; cannot test the no-handoff path")
	}
	lease, meta, receipt, err := handoffAdoptHook()
	if err != nil {
		t.Fatalf("handoffAdoptHook() err = %v; want nil", err)
	}
	if lease != nil {
		t.Fatalf("handoffAdoptHook() lease = %v; want nil", lease)
	}
	if meta != (selfupdate.HandoffMetadata{}) || receipt != (selfupdate.Receipt{}) {
		t.Fatalf("handoffAdoptHook() meta/receipt = %+v/%+v; want zero values", meta, receipt)
	}
	if driver.adopted != nil {
		t.Fatal("driver.adopted != nil after no-handoff adoption probe")
	}
}

func TestSelfUpdateDriverAdoptionHookFailsClosedOnMalformedEnv(t *testing.T) {
	resetDriverState(t)
	t.Setenv(selfupdate.HandoffEnvVar, "{not-json")

	lease, _, _, err := handoffAdoptHook()
	if err == nil {
		t.Fatal("handoffAdoptHook() err = nil; want fail-closed error for malformed handoff entry")
	}
	if lease != nil {
		t.Fatal("handoffAdoptHook() lease != nil on malformed entry")
	}
	if driver.adopted != nil {
		t.Fatal("malformed handoff must not be stashed in driver state")
	}
}

func TestSelfUpdateDriverListenOverride(t *testing.T) {
	resetDriverState(t)

	if got := handoffListenOverrideHook(); got != "" {
		t.Fatalf("handoffListenOverrideHook() = %q; want empty without adoption", got)
	}
	driver.adopted = &selfupdate.Lease{}
	driver.adoptedMeta = selfupdate.HandoffMetadata{Bind: selfupdate.BindEndpoint{Host: "127.0.0.1", Port: 54321}}
	if got := handoffListenOverrideHook(); got != "127.0.0.1:54321" {
		t.Fatalf("handoffListenOverrideHook() = %q; want 127.0.0.1:54321", got)
	}
}

func TestSelfUpdateDriverHandoffListenAddrFormatting(t *testing.T) {
	tests := []struct {
		name string
		bind selfupdate.BindEndpoint
		want string
	}{
		{"loopback", selfupdate.BindEndpoint{Host: "127.0.0.1", Port: 8080}, "127.0.0.1:8080"},
		{"wildcard v4", selfupdate.BindEndpoint{Host: "0.0.0.0", Port: 8080}, "0.0.0.0:8080"},
		{"ipv6", selfupdate.BindEndpoint{Host: "::1", Port: 8080}, "[::1]:8080"},
		{"hostname", selfupdate.BindEndpoint{Host: "myhost", Port: 9090}, "myhost:9090"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			meta := selfupdate.HandoffMetadata{Bind: tc.bind}
			if got := handoffListenAddr(meta); got != tc.want {
				t.Fatalf("handoffListenAddr() = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestSelfUpdateDriverJourneySelection(t *testing.T) {
	resetDriverState(t)

	if journey := serverJourneyHook(); journey != nil {
		t.Fatalf("serverJourneyHook() = %T; want nil without adoption or candidate", journey)
	}
	driver.candidatePath = "candidate"
	if _, ok := serverJourneyHook().(replaceJourney); !ok {
		t.Fatalf("serverJourneyHook() = %T; want replaceJourney", serverJourneyHook())
	}
	// Adoption takes priority over a candidate: the new image confirms, it
	// never starts a second replacement.
	driver.adopted = &selfupdate.Lease{}
	if _, ok := serverJourneyHook().(confirmJourney); !ok {
		t.Fatalf("serverJourneyHook() = %T; want confirmJourney", serverJourneyHook())
	}
}

func TestSelfUpdateDriverSeamsForFailAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-target")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write sync target: %v", err)
	}

	seams := seamsForFailAt("backup-sync")
	if seams.SyncFile == nil {
		t.Fatal("backup-sync seam must override SyncFile")
	}
	if err := seams.SyncFile(path); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("first SyncFile = %v; want injected failure", err)
	}
	if err := seams.SyncFile(path); err != nil {
		t.Fatalf("second SyncFile = %v; want the real sync", err)
	}

	seams = seamsForFailAt("receipt-write")
	if seams.WriteReceipt == nil {
		t.Fatal("receipt-write seam must override WriteReceipt")
	}
	if err := seams.WriteReceipt(path, selfupdate.Receipt{}); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("WriteReceipt = %v; want injected failure", err)
	}

	if seams := seamsForFailAt(""); seams.SyncFile != nil || seams.WriteReceipt != nil {
		t.Fatal("no fail-at must yield zero seams")
	}
	if seams := seamsForFailAt("post-rename"); seams.SyncFile != nil || seams.WriteReceipt != nil {
		t.Fatal("post-rename needs no transaction seam")
	}
}

func TestSelfUpdateDriverWaitForTriggerFile(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "trigger")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatalf("write trigger: %v", err)
	}
	if err := waitForTriggerFile(present, time.Second); err != nil {
		t.Fatalf("waitForTriggerFile(present) = %v; want nil", err)
	}
	if err := waitForTriggerFile(filepath.Join(dir, "missing"), 50*time.Millisecond); err == nil {
		t.Fatal("waitForTriggerFile(missing) = nil; want bounded deadline error")
	}
	if err := waitForTriggerFile("", time.Second); err == nil {
		t.Fatal("waitForTriggerFile(empty) = nil; want configuration error")
	}
}

func TestSelfUpdateDriverParseAgenticoVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"revision suffix", "agentico v9.9.9 (revision abc123)\n", "9.9.9"},
		{"bare version", "agentico v2.0.0\n", "2.0.0"},
		{"dev fallback", "agentico vdev\n", "dev"},
		{"no banner", "something else\n", "unknown"},
		{"empty output", "", "unknown"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := parseAgenticoVersion(tc.in); got != tc.want {
				t.Fatalf("parseAgenticoVersion(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSelfUpdateDriverFingerprintDigests(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/tmp"}
	withHandoff := append(append([]string(nil), base...), selfupdate.HandoffEnvVar+"="+"{\"schema_version\":1}")
	if envDigest(base) != envDigest(withHandoff) {
		t.Fatal("env digest must ignore the handoff entry: it is the one entry exec may add")
	}
	if envDigest([]string{"A=1", "B=2"}) == envDigest([]string{"B=2", "A=1"}) {
		t.Fatal("env digest must be order-sensitive")
	}
	if argsDigest([]string{"a", "b"}) == argsDigest([]string{"a"}) {
		t.Fatal("args digest must distinguish argument vectors")
	}
	if len(argsDigest([]string{"a"})) != 64 {
		t.Fatalf("args digest length = %d; want 64 hex chars", len(argsDigest([]string{"a"})))
	}
}
