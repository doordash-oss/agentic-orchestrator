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
// cleanup restoring it (and every production seam the driver can arm) after
// the test. These tests touch package-level mutable globals, so none of
// them may run in parallel.
func resetDriverState(t *testing.T) {
	t.Helper()
	origPublish := publishDiscoveryFn
	origDeadline := startupDeadlineHook
	origHealth := selfUpdateHealthWaitFn
	origConfirm := selfUpdateConfirmFn
	origRecoverySeams := selfUpdateRecoverySeams
	origBarrier := selfUpdateRecoveryBarrier
	origRecoveryExec := selfUpdateRecoveryExecFn
	origStartupAbort := selfUpdateStartupAbortHook
	origCleanupSeams := selfUpdateCleanupSeams
	origArmOnAdoption := driverArmOnAdoption
	reset := func() {
		publishDiscoveryFn = origPublish
		startupDeadlineHook = origDeadline
		selfUpdateHealthWaitFn = origHealth
		selfUpdateConfirmFn = origConfirm
		selfUpdateRecoverySeams = origRecoverySeams
		selfUpdateRecoveryBarrier = origBarrier
		selfUpdateRecoveryExecFn = origRecoveryExec
		selfUpdateStartupAbortHook = origStartupAbort
		selfUpdateCleanupSeams = origCleanupSeams
		driverArmOnAdoption = origArmOnAdoption
		driver.candidatePath = ""
		driver.triggerFile = ""
		driver.failAt = nil
		driver.barrier = ""
		driver.barrierFile = ""
		driver.prepareOnly = false
		driver.updateFeedURL = ""
		driver.fixtureFeed = nil
		driver.installRelease = false
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
	if driver.anyFailArmed() || driver.prepareOnly {
		t.Fatalf("driver failAt/prepareOnly = %v/%v; want defaults", driver.failAt, driver.prepareOnly)
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

	// Comma-separated fail points all arm.
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--fail-at", "discovery,recovered-startup"}); err != nil {
		t.Fatalf("comma fail-at parse: %v", err)
	}
	if !driver.failAtSet("discovery") || !driver.failAtSet("recovered-startup") {
		t.Fatalf("comma fail-at armed = %v; want discovery and recovered-startup", driver.failAt)
	}

	// Invalid --barrier points are rejected, and barrier/fail-at are
	// mutually exclusive.
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--barrier", "nonsense", "--barrier-file", "f"})
	if !handled || err == nil || !strings.Contains(err.Error(), "unknown --barrier point") {
		t.Fatalf("invalid barrier: handled %v, err %v; want rejection", handled, err)
	}
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--fail-at", "drain", "--barrier", "prepared", "--barrier-file", "f"})
	if !handled || err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("barrier+fail-at: handled %v, err %v; want rejection", handled, err)
	}
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--barrier", "prepared"})
	if !handled || err == nil || !strings.Contains(err.Error(), "requires --barrier-file") {
		t.Fatalf("barrier without file: handled %v, err %v; want rejection", handled, err)
	}

	// --prepare-only records the flag.
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--candidate", "x", "--prepare-only"}); err != nil {
		t.Fatalf("prepare-only parse: %v", err)
	}
	if !driver.prepareOnly {
		t.Fatal("prepareOnly = false; want true")
	}

	// --update-feed only accepts a loopback http fixture URL, and the
	// constructor pins the one origin at parse time.
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--update-feed", "https://api.github.com"})
	if !handled || err == nil || !strings.Contains(err.Error(), "loopback http fixture URL") {
		t.Fatalf("non-loopback update-feed: handled %v, err %v; want rejection", handled, err)
	}
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--update-feed", "http://127.0.0.1:9/base"})
	if !handled || err == nil || !strings.Contains(err.Error(), "invalid --update-feed value") {
		t.Fatalf("path-bearing update-feed: handled %v, err %v; want rejection", handled, err)
	}
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--update-feed", "http://127.0.0.1:9", "--install-release"}); err != nil {
		t.Fatalf("install-release parse: %v", err)
	}
	if !driver.installRelease || driver.fixtureFeed == nil {
		t.Fatal("install-release not armed with a fixture feed")
	}

	// The release-backed journey never accepts a caller-provided candidate.
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--update-feed", "http://127.0.0.1:9", "--install-release", "--candidate", "x"})
	if !handled || err == nil || !strings.Contains(err.Error(), "cannot be combined with --candidate") {
		t.Fatalf("install-release+candidate: handled %v, err %v; want rejection", handled, err)
	}
	_, handled, err = driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--install-release"})
	if !handled || err == nil || !strings.Contains(err.Error(), "requires --update-feed") {
		t.Fatalf("install-release without feed: handled %v, err %v; want rejection", handled, err)
	}
}

func TestSelfUpdateDriverFailAtDiscoveryStubsPublishOnlyOnAdoption(t *testing.T) {
	resetDriverState(t)
	// The real publish writes into its runtime dir: keep it inside the
	// test's temp tree, never the package's working directory.
	runtimeDir := t.TempDir()

	// Parsing alone must NOT arm the stub: the old image still publishes its
	// own discovery record normally.
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--fail-at", "discovery"}); err != nil {
		t.Fatalf("parse --fail-at discovery: %v", err)
	}
	if err := publishDiscoveryFn(runtimeDir, serverruntime.DiscoveryRecord{}); err != nil {
		t.Fatalf("publishDiscoveryFn() error = %v; want the real function after parse alone", err)
	}
	if selfUpdateRecoveryBarrier != nil {
		t.Fatal("fail-at arming must not install a barrier hook")
	}

	// Arming happens only on the adopted image.
	armDiscoveryFailure()
	err := publishDiscoveryFn(runtimeDir, serverruntime.DiscoveryRecord{})
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
		{"no endpoint", selfupdate.BindEndpoint{}, ""},
		{"portless", selfupdate.BindEndpoint{Host: "myhost"}, ""},
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
	if handoffEnvPresent(os.Environ()) {
		t.Skip("handoff environment entry present; cannot test journey selection")
	}

	if journey := serverJourneyHook(); journey != nil {
		t.Fatalf("serverJourneyHook() = %T; want nil without a candidate", journey)
	}
	driver.candidatePath = "candidate"
	if _, ok := serverJourneyHook().(replaceJourney); !ok {
		t.Fatalf("serverJourneyHook() = %T; want replaceJourney", serverJourneyHook())
	}

	// The release-backed journey is selected only by the deliberate
	// --install-release flag with its fixture feed, and it wins over a
	// stray candidate path (parse rejects the combination anyway).
	resetDriverState(t)
	if _, _, err := driverArgParseHook([]string{cliSubcommandSelfUpdateDriver, "--update-feed", "http://127.0.0.1:9", "--install-release"}); err != nil {
		t.Fatalf("install-release parse: %v", err)
	}
	if _, ok := serverJourneyHook().(releaseJourney); !ok {
		t.Fatalf("serverJourneyHook() = %T; want releaseJourney", serverJourneyHook())
	}

	// A recovery chain never re-attempts installation without fresh consent:
	// the guard entry suppresses the journey even with a candidate armed.
	t.Setenv(selfupdate.RecoveryGuardEnvVar, `{"schema_version":1,"transaction_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","attempt_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","kind":"restore"}`)
	if journey := serverJourneyHook(); journey != nil {
		t.Fatalf("serverJourneyHook() = %T; want nil in a recovery chain", journey)
	}
}

func TestSelfUpdateDriverSeamsForFailAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync-target")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write sync target: %v", err)
	}

	seams := seamsForFailAt(map[string]bool{"backup-sync": true})
	if seams.SyncFile == nil {
		t.Fatal("backup-sync seam must override SyncFile")
	}
	if err := seams.SyncFile(path); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("first SyncFile = %v; want injected failure", err)
	}
	if err := seams.SyncFile(path); err != nil {
		t.Fatalf("second SyncFile = %v; want the real sync", err)
	}

	seams = seamsForFailAt(map[string]bool{"receipt-write": true})
	if seams.WriteReceipt == nil {
		t.Fatal("receipt-write seam must override WriteReceipt")
	}
	if err := seams.WriteReceipt(path, selfupdate.Receipt{}); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("WriteReceipt = %v; want injected failure", err)
	}

	if seams := seamsForFailAt(nil); seams.SyncFile != nil || seams.WriteReceipt != nil {
		t.Fatal("no fail-at must yield zero seams")
	}
	if seams := seamsForFailAt(map[string]bool{"post-rename": true}); seams.SyncFile != nil || seams.WriteReceipt != nil {
		t.Fatal("post-rename needs no transaction seam")
	}
}

func TestSelfUpdateDriverRecoverySeamsForFailAt(t *testing.T) {
	attemptPending := selfupdate.Receipt{
		Outcome:           selfupdate.OutcomePending,
		RecoveryAttemptID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecoveryKind:      selfupdate.RecoveryKindRestore,
	}
	rolledBack := selfupdate.Receipt{Outcome: selfupdate.OutcomeRolledBack}

	seams := recoverySeamsForFailAt(map[string]bool{"restore-attempt-write": true})
	if err := seams.WriteReceipt("unused", attemptPending); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("attempt write = %v; want injected failure", err)
	}
	other := selfupdate.Receipt{Outcome: selfupdate.OutcomePending}
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.json")
	if err := seams.WriteReceipt(receiptPath, other); err != nil {
		t.Fatalf("non-attempt write must run the real implementation: %v", err)
	}

	seams = recoverySeamsForFailAt(map[string]bool{"restore-rolledback": true})
	if err := seams.WriteReceipt(receiptPath, rolledBack); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("rolled-back write = %v; want injected failure", err)
	}
	if err := seams.WriteReceipt(receiptPath, other); err != nil {
		t.Fatalf("non-rolledback write must run the real implementation: %v", err)
	}

	seams = recoverySeamsForFailAt(map[string]bool{"restore-rename": true})
	if err := seams.Rename("a", "b"); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("restore rename = %v; want injected failure", err)
	}

	seams = recoverySeamsForFailAt(map[string]bool{"restore-sync": true})
	txLike := filepath.Join(t.TempDir(), "tx-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Mkdir(txLike, 0o700); err != nil {
		t.Fatalf("mkdir tx-like dir: %v", err)
	}
	if err := seams.SyncDir(txLike); err != nil {
		t.Fatalf("tx-dir sync must run the real implementation: %v", err)
	}
	if err := seams.SyncDir(t.TempDir()); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("installed-dir sync = %v; want injected failure", err)
	}

	if seams := recoverySeamsForFailAt(map[string]bool{"drain": true}); seams.WriteReceipt != nil || seams.Rename != nil || seams.SyncDir != nil {
		t.Fatal("unrelated fail points must not install recovery seams")
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
		t.Fatal("env digest must ignore the handoff entry: it is the one entry exec may replace")
	}
	withGuard := append(append([]string(nil), base...), selfupdate.RecoveryGuardEnvVar+"="+"{\"schema_version\":1}")
	if envDigest(base) != envDigest(withGuard) {
		t.Fatal("env digest must ignore the recovery guard entry: it is the other entry exec may replace")
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
