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

package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHandoffEncodeParseRoundTrip(t *testing.T) {
	t.Parallel()
	m := HandoffMetadata{
		SchemaVersion:  1,
		TransactionID:  "0123456789abcdef0123456789abcdef",
		RuntimeDir:     "/runtime",
		StateDir:       "/runtime/features",
		Config:         "/runtime/config.yaml",
		FromPID:        4242,
		LeaseFD:        7,
		LeasePath:      "/runtime/bin/.agentico-selfupdate/lease-x.lock",
		ExecutablePath: "/runtime/bin/agentico",
		NewDigest:      "bbbb",
		FromVersion:    "1.0.0",
		ToVersion:      "2.0.0",
		Bind: BindEndpoint{
			Host:         "127.0.0.1",
			Port:         54321,
			Wildcard:     true,
			AdvertiseURL: "http://localhost:54321",
			Policy:       "wildcard",
		},
		ReceiptPath:   "/runtime/bin/.agentico-selfupdate/receipt.json",
		AuthTokenPath: "/runtime/token-path",
	}
	got, err := ParseHandoff(EncodeHandoff(m))
	if err != nil {
		t.Fatalf("ParseHandoff: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip mismatch:\ngot  %+v\nwant %+v", got, m)
	}
	if _, err := ParseHandoff("{not json"); err == nil {
		t.Fatal("ParseHandoff accepted garbage")
	}
}

func TestParseHandoffEnv(t *testing.T) {
	t.Parallel()
	m := HandoffMetadata{
		SchemaVersion: 1,
		TransactionID: "tx",
		FromPID:       99,
		LeaseFD:       5,
	}
	env := []string{"PATH=/usr/bin", HandoffEnvVar + "=" + EncodeHandoff(m), "HOME=/home"}

	got, ok := ParseHandoffEnv(env)
	if !ok {
		t.Fatal("ParseHandoffEnv did not find the handoff entry")
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("parsed = %+v, want %+v", got, m)
	}
	if _, ok := ParseHandoffEnv([]string{"PATH=/usr/bin"}); ok {
		t.Fatal("ParseHandoffEnv found a handoff entry that is absent")
	}
	if _, ok := ParseHandoffEnv([]string{HandoffEnvVar + "={broken"}); ok {
		t.Fatal("ParseHandoffEnv must fail closed on an unparseable entry")
	}
}

func TestCLOEXECToggle(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "fd"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	fd := int(f.Fd())

	ok, err := IsCLOEXEC(fd)
	if err != nil || !ok {
		t.Fatalf("IsCLOEXEC(default) = (%v, %v), want (true, nil)", ok, err)
	}
	if err := ClearCLOEXEC(fd); err != nil {
		t.Fatalf("ClearCLOEXEC: %v", err)
	}
	if ok, _ := IsCLOEXEC(fd); ok {
		t.Fatal("IsCLOEXEC after ClearCLOEXEC = true, want false")
	}
	if err := SetCLOEXEC(fd); err != nil {
		t.Fatalf("SetCLOEXEC: %v", err)
	}
	if ok, _ := IsCLOEXEC(fd); !ok {
		t.Fatal("IsCLOEXEC after SetCLOEXEC = false, want true")
	}

	closed, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	closedFD := int(closed.Fd())
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := IsCLOEXEC(closedFD); err == nil {
		t.Fatal("IsCLOEXEC on closed fd: expected error")
	}
}

func TestExecReplaceErrorRestoresCLOEXEC(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "lease"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	fd := int(f.Fd())

	if err := ClearCLOEXEC(fd); err != nil {
		t.Fatalf("ClearCLOEXEC: %v", err)
	}
	env := []string{"PATH=/usr/bin", "HOME=/home"}
	entry := HandoffEnvVar + "={\"schema_version\":1}"
	execErr := errors.New("exec boom")
	var gotPath string
	var gotArgv, gotEnv []string
	err = ExecReplace("/installed", []string{"argv0", "flag"}, env, entry, fd, func(path string, argv, ee []string) error {
		gotPath, gotArgv, gotEnv = path, argv, ee
		return execErr
	})
	if !errors.Is(err, execErr) {
		t.Fatalf("ExecReplace error = %v, want %v", err, execErr)
	}
	ok, cerr := IsCLOEXEC(fd)
	if cerr != nil || !ok {
		t.Fatalf("IsCLOEXEC after failed exec = (%v, %v), want (true, nil)", ok, cerr)
	}
	if gotPath != "/installed" {
		t.Fatalf("exec path = %q", gotPath)
	}
	if !reflect.DeepEqual(gotArgv, []string{"argv0", "flag"}) {
		t.Fatalf("exec argv = %v", gotArgv)
	}
	wantEnv := []string{"PATH=/usr/bin", "HOME=/home", entry}
	if !reflect.DeepEqual(gotEnv, wantEnv) {
		t.Fatalf("exec env = %v, want %v", gotEnv, wantEnv)
	}
	if !reflect.DeepEqual(env, []string{"PATH=/usr/bin", "HOME=/home"}) {
		t.Fatalf("caller env mutated: %v", env)
	}

	// An empty extra entry must not add anything to the environment.
	err = ExecReplace("/installed", nil, env, "", fd, func(string, []string, []string) error { return execErr })
	if !errors.Is(err, execErr) {
		t.Fatalf("ExecReplace error = %v, want %v", err, execErr)
	}
}

// handoffFixture drives a real Begin+Commit against an isolated installed
// copy and returns everything AdoptHandoff needs.
type handoffFixture struct {
	f        *txFixture
	lease    *Lease
	tx       *Transaction
	newExec  Executable
	metadata HandoffMetadata
}

func newHandoffFixture(t *testing.T, commit bool) *handoffFixture {
	t.Helper()
	f := newTxFixture(t)
	lease, err := AcquireLease(f.exec, testRecordFor(f.exec, f.runtimeDir))
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}

	tx, err := Begin(f.exec, f.opts, FileOps{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if commit {
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	newExec, err := CaptureExecutableAt(f.installedPath)
	if err != nil {
		t.Fatalf("recapture installed: %v", err)
	}
	if err := ClearCLOEXEC(lease.FD()); err != nil {
		t.Fatalf("ClearCLOEXEC: %v", err)
	}
	// The handoff digest is the digest the receipt recorded for the target
	// image, independent of whether this fixture committed.
	metadata := HandoffMetadata{
		SchemaVersion:  handoffSchemaVersion,
		TransactionID:  tx.Receipt().TransactionID,
		RuntimeDir:     f.opts.RuntimeDir,
		StateDir:       f.opts.StateDir,
		Config:         f.opts.Config,
		FromPID:        os.Getpid(),
		LeaseFD:        lease.FD(),
		LeasePath:      lease.Path(),
		ExecutablePath: f.exec.Path,
		NewDigest:      f.opts.CandidateDigest,
		FromVersion:    f.opts.FromVersion,
		ToVersion:      f.opts.ToVersion,
		Bind:           f.opts.Bind,
		ReceiptPath:    ReceiptPath(f.exec.Path),
		AuthTokenPath:  filepath.Join(f.runtimeDir, "auth-token"),
	}
	return &handoffFixture{f: f, lease: lease, tx: tx, newExec: newExec, metadata: metadata}
}

func TestAdoptHandoffHappyPath(t *testing.T) {
	h := newHandoffFixture(t, true)

	adopted, receipt, err := AdoptHandoff(h.metadata, h.newExec)
	if err != nil {
		t.Fatalf("AdoptHandoff: %v", err)
	}
	// The descriptor now belongs to the adopted lease; the original Lease
	// object must not close it again.
	defer func() { _ = adopted.Close() }()

	if ok, err := IsCLOEXEC(adopted.FD()); err != nil || !ok {
		t.Fatalf("IsCLOEXEC after adoption = (%v, %v), want (true, nil)", ok, err)
	}
	if receipt.TransactionID != h.metadata.TransactionID ||
		receipt.Phase != PhaseReplacementComplete ||
		receipt.Outcome != OutcomePending {
		t.Fatalf("adopted receipt = %s/%s", receipt.Phase, receipt.Outcome)
	}

	rec := adopted.Record()
	if rec.PID != os.Getpid() || rec.Version != h.f.opts.ToVersion || rec.ExecutableDigest != h.newExec.Digest {
		t.Fatalf("updated record = %+v", rec)
	}
	if rec.ExecutableIno != h.newExec.ID.Ino {
		t.Fatalf("record inode = %d, want new installed inode %d", rec.ExecutableIno, h.newExec.ID.Ino)
	}
	diskRec, err := ReadOwnershipRecord(h.f.exec.Path)
	if err != nil || diskRec != rec {
		t.Fatalf("on-disk record = %+v (%v), want %+v", diskRec, err, rec)
	}

	// The adopted lease still holds the flock: a second acquirer contends.
	if _, err := AcquireLease(h.newExec, rec); !IsLeaseHeld(err) {
		t.Fatalf("AcquireLease during adoption = %v, want LeaseHeldError", err)
	}

	if err := adopted.Close(); err != nil {
		t.Fatalf("Close adopted: %v", err)
	}
	after, err := AcquireLease(h.newExec, testRecordFor(h.newExec, h.f.runtimeDir))
	if err != nil {
		t.Fatalf("AcquireLease after adopted close: %v", err)
	}
	if err := after.Close(); err != nil {
		t.Fatalf("Close after: %v", err)
	}
}

func TestAdoptHandoffFailureMatrix(t *testing.T) {
	tests := []struct {
		name   string
		commit bool
		mutate func(t *testing.T, h *handoffFixture)
	}{
		{"wrong from pid", true, func(t *testing.T, h *handoffFixture) {
			h.metadata.FromPID = os.Getpid() + 1
		}},
		{"tampered receipt transaction id", true, func(t *testing.T, h *handoffFixture) {
			h.metadata.TransactionID = "ffffffffffffffffffffffffffffffff"
		}},
		{"wrong phase", false, func(t *testing.T, h *handoffFixture) {
			// Pretend the target image is running with the candidate bytes:
			// the uncommitted receipt phase must still reject adoption.
			h.newExec.Digest = h.metadata.NewDigest
		}},
		{"wrong outcome", true, func(t *testing.T, h *handoffFixture) {
			r, err := ReadReceipt(ReceiptPath(h.f.exec.Path))
			if err != nil {
				t.Fatalf("ReadReceipt: %v", err)
			}
			r.Outcome = OutcomeConfirmed
			if err := writeReceiptAtomic(ReceiptPath(h.f.exec.Path), r, r.UpdatedAt); err != nil {
				t.Fatalf("tamper receipt: %v", err)
			}
		}},
		{"new digest mismatch", true, func(t *testing.T, h *handoffFixture) {
			h.metadata.NewDigest = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}},
		{"closed fd", true, func(t *testing.T, h *handoffFixture) {
			if err := h.lease.Close(); err != nil {
				t.Fatalf("Close lease: %v", err)
			}
		}},
		{"fd of a different file", true, func(t *testing.T, h *handoffFixture) {
			foreign, err := os.Create(filepath.Join(t.TempDir(), "foreign"))
			if err != nil {
				t.Fatalf("create foreign: %v", err)
			}
			t.Cleanup(func() { _ = foreign.Close() })
			h.metadata.LeaseFD = int(foreign.Fd())
		}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := newHandoffFixture(t, tc.commit)
			tc.mutate(t, h)
			lease, _, err := AdoptHandoff(h.metadata, h.newExec)
			// On rejection the original lease still owns the descriptor.
			defer func() { _ = h.lease.Close() }()
			if err == nil {
				_ = lease.Close()
				t.Fatal("AdoptHandoff unexpectedly succeeded")
			}
			if IsLeaseHeld(err) {
				t.Fatalf("AdoptHandoff returned lease contention: %v", err)
			}
		})
	}
}

func TestParseRecoveryGuardEnv(t *testing.T) {
	t.Parallel()
	g := RecoveryGuard{
		SchemaVersion: 1,
		TransactionID: "0123456789abcdef0123456789abcdef",
		AttemptID:     "abcdef1234567890abcdef1234567890",
		Kind:          RecoveryKindRestore,
		Bind:          BindEndpoint{Host: "127.0.0.1", Port: 54321},
		AuthTokenPath: "/runtime/token-path",
	}
	env := []string{"PATH=/usr/bin", RecoveryGuardEnvVar + "=" + EncodeRecoveryGuard(g), "HOME=/home"}

	got, present, err := ParseRecoveryGuardEnv(env)
	if err != nil || !present || !reflect.DeepEqual(got, g) {
		t.Fatalf("ParseRecoveryGuardEnv = (%+v, %v, %v), want the recorded guard", got, present, err)
	}

	got, present, err = ParseRecoveryGuardEnv([]string{"PATH=/usr/bin"})
	if err != nil || present || !reflect.DeepEqual(got, RecoveryGuard{}) {
		t.Fatalf("ParseRecoveryGuardEnv without entry = (%+v, %v, %v), want absent zero guard", got, present, err)
	}

	// A present-but-unparseable entry fails closed while still reporting
	// presence: the chain cannot prove which transaction it attempted.
	got, present, err = ParseRecoveryGuardEnv([]string{RecoveryGuardEnvVar + "={broken"})
	if err == nil {
		t.Fatal("ParseRecoveryGuardEnv on garbage = nil error, want fail-closed")
	}
	if !present {
		t.Fatal("ParseRecoveryGuardEnv on garbage = present false, want true")
	}
	if !reflect.DeepEqual(got, RecoveryGuard{}) {
		t.Fatalf("ParseRecoveryGuardEnv on garbage = %+v, want zero guard", got)
	}
}

func TestBuildRecoveryEnvDropsStaleEntriesAndAppendsOneGuard(t *testing.T) {
	t.Parallel()
	g := RecoveryGuard{
		SchemaVersion: 1,
		TransactionID: "0123456789abcdef0123456789abcdef",
		AttemptID:     "abcdef1234567890abcdef1234567890",
		Kind:          RecoveryKindRestore,
	}
	stale := RecoveryGuard{SchemaVersion: 1, TransactionID: "ffffffffffffffffffffffffffffffff", AttemptID: "old", Kind: RecoveryKindRestart}
	env := []string{
		"PATH=/usr/bin",
		HandoffEnvVar + "=" + EncodeHandoff(HandoffMetadata{SchemaVersion: 1}),
		RecoveryGuardEnvVar + "=" + EncodeRecoveryGuard(stale),
		"HOME=/home",
		RecoveryGuardEnvVar + "=garbage",
	}

	out := BuildRecoveryEnv(env, g)
	want := []string{"PATH=/usr/bin", "HOME=/home", RecoveryGuardEnvVar + "=" + EncodeRecoveryGuard(g)}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("BuildRecoveryEnv = %v, want %v", out, want)
	}
	guardEntries := 0
	for _, entry := range out {
		if strings.HasPrefix(entry, HandoffEnvVar+"=") {
			t.Fatalf("handoff entry leaked into recovery env: %s", entry)
		}
		if strings.HasPrefix(entry, RecoveryGuardEnvVar+"=") {
			guardEntries++
		}
	}
	if guardEntries != 1 {
		t.Fatalf("recovery env carries %d guard entries, want exactly one", guardEntries)
	}
	parsed, present, err := ParseRecoveryGuardEnv(out)
	if err != nil || !present || !reflect.DeepEqual(parsed, g) {
		t.Fatalf("ParseRecoveryGuardEnv(recovery env) = (%+v, %v, %v), want the appended guard", parsed, present, err)
	}
}

func TestExecRecoveryUsesSeam(t *testing.T) {
	t.Parallel()
	execErr := errors.New("exec boom")
	var gotPath string
	var gotArgv, gotEnv []string
	err := ExecRecovery("/installed", []string{"argv0", "flag"}, []string{"PATH=/usr/bin"}, func(path string, argv, env []string) error {
		gotPath, gotArgv, gotEnv = path, argv, env
		return execErr
	})
	if !errors.Is(err, execErr) {
		t.Fatalf("ExecRecovery error = %v, want %v", err, execErr)
	}
	if gotPath != "/installed" {
		t.Fatalf("exec path = %q, want /installed", gotPath)
	}
	if !reflect.DeepEqual(gotArgv, []string{"argv0", "flag"}) {
		t.Fatalf("exec argv = %v", gotArgv)
	}
	if !reflect.DeepEqual(gotEnv, []string{"PATH=/usr/bin"}) {
		t.Fatalf("exec env = %v", gotEnv)
	}
}
