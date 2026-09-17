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

package e2e

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

// The recovery journeys drive the mandatory pre-bootstrap recovery
// resolution, the guarded recovery exec chain, and the safety refusals with
// real processes: deterministic barriers let the harness SIGKILL the driver
// at exact transaction boundaries, then an ordinary operator launch must
// resolve the interrupted transaction — abandoning a provably un-installed
// attempt, finishing or retrying an interrupted restoration, or refusing
// nonzero on unsafe records — before any configuration load or bootstrap.

// recoveryKillFixture wraps one journey with a fixed loopback endpoint and a
// barrier file path.
type recoveryKillFixture struct {
	j           *selfupdateJourney
	port        int
	baseURL     string
	barrierFile string
}

func newRecoveryKillFixture(t *testing.T) *recoveryKillFixture {
	t.Helper()
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	return &recoveryKillFixture{
		j:           j,
		port:        port,
		baseURL:     "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		barrierFile: filepath.Join(filepath.Dir(j.installPath), "barrier"),
	}
}

// start begins the update journey that will be killed at the barrier.
func (f *recoveryKillFixture) start(barrier string) *driverProcess {
	f.j.t.Helper()
	return f.j.start(selfupdateStartOptions{
		listen:      "127.0.0.1:" + strconv.Itoa(f.port),
		barrier:     barrier,
		barrierFile: f.barrierFile,
	})
}

// killAtBarrier starts the journey, releases the trigger, waits for the
// barrier marker, and SIGKILLs the process at that deterministic point.
func (f *recoveryKillFixture) killAtBarrier(t *testing.T, barrier string) *driverProcess {
	t.Helper()
	p := f.start(barrier)
	f.j.trigger()
	deadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := os.Stat(f.barrierFile); err == nil {
			break
		}
		if p.exited() {
			t.Fatalf("driver died before reaching the %s barrier (stderr tail:\n%s)", barrier, p.stderrTail())
		}
		if time.Now().After(deadline) {
			t.Fatalf("barrier %s never fired within 45s (stderr tail:\n%s)", barrier, p.stderrTail())
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = p.cmd.Process.Kill()
	_ = p.waitBounded(10 * time.Second)
	return p
}

// relaunch performs an ordinary operator launch of the installed binary on
// the journey's runtime: production recovery resolution runs before any
// bootstrap step.
func (f *recoveryKillFixture) relaunch(extra ...string) *driverProcess {
	f.j.t.Helper()
	args := append([]string{
		"selfupdate-driver",
		"--config", f.j.configPath,
		"--state-dir", f.j.stateDir,
		"--listen", "127.0.0.1:" + strconv.Itoa(f.port),
	}, extra...)
	return f.j.launch(args...)
}

// requireAbandonedService asserts the relaunch resolved an interrupted
// pre-replacement attempt as an installation failure and kept serving the
// unchanged build.
func (f *recoveryKillFixture) requireAbandonedService(t *testing.T, p *driverProcess) {
	t.Helper()
	r := f.j.waitReceiptResolution(selfupdate.ResolutionAbandonedPreReplacement, 45*time.Second)
	if r.Outcome != selfupdate.OutcomePending || r.Phase != selfupdate.PhaseBackupReady {
		t.Fatalf("receipt = %q/%q; want pending/backup-ready", r.Outcome, r.Phase)
	}
	if !r.InstallFailed {
		t.Fatal("abandoned receipt must record the installation failure")
	}
	h := f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
	if h.Owner.PID != p.pid() {
		t.Fatalf("relaunched owner pid = %d; want the relaunch pid %d", h.Owner.PID, p.pid())
	}
	if got := f.j.installDigest(); got != f.j.bins.lowerDigest {
		t.Fatalf("installed digest = %s; want untouched lower digest %s", got, f.j.bins.lowerDigest)
	}
	requireNoSuppression(t, f.j, selfupdateVersionHigher)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

// requireRestoredService asserts the relaunch resolved an interrupted
// candidate-installed attempt by restoring the previous build and exec'ing
// onto it: the relaunch pid is preserved across the recovery exec.
func (f *recoveryKillFixture) requireRestoredService(t *testing.T, p *driverProcess) {
	t.Helper()
	r := f.j.waitReceiptRolledBack(45 * time.Second)
	if r.RecoveryKind != selfupdate.RecoveryKindRestore || r.RecoveryAttemptID == "" {
		t.Fatalf("receipt recovery = %q/%q; want restore kind with attempt id", r.RecoveryKind, r.RecoveryAttemptID)
	}
	h := f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
	if h.Owner.PID != p.pid() {
		t.Fatalf("recovered owner pid = %d; want the preserved relaunch pid %d", h.Owner.PID, p.pid())
	}
	if got := f.j.installDigest(); got != f.j.bins.lowerDigest {
		t.Fatalf("installed digest = %s; want restored lower digest %s", got, f.j.bins.lowerDigest)
	}
	requireSuppressedTarget(t, f.j, r)
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
	if !p.stderrContains("warning[update_rolled_back]") {
		t.Fatalf("no canonical update_rolled_back warning in stderr:\n%s", p.stderrTail())
	}
}

// TestSelfUpdateRecoveryInterruptions kills the driver at deterministic
// transaction boundaries and proves the next operator launch resolves the
// interrupted transaction before any bootstrap step.
func TestSelfUpdateRecoveryInterruptions(t *testing.T) {
	selfupdateJourneyGuard(t)

	// Interruptions before replacement: the original bytes are still
	// installed, so the attempt provably never replaced the executable —
	// settle the installation failure and keep serving the unchanged build.
	for _, barrier := range []string{"prepared", "pre-commit"} {
		t.Run(barrier, func(t *testing.T) {
			f := newRecoveryKillFixture(t)
			f.killAtBarrier(t, barrier)

			p := f.relaunch()
			f.requireAbandonedService(t, p)

			// The abandoned transaction's recognized objects are cleaned.
			r := f.j.waitReceiptResolution(selfupdate.ResolutionAbandonedPreReplacement, 5*time.Second)
			f.j.waitPathGone(filepath.Join(selfupdate.LeaseDir(f.j.installPath), "tx-"+r.TransactionID), "transaction dir", 10*time.Second)
		})
	}

	// Interruptions after replacement but before the receipt advanced: the
	// candidate bytes are installed, so the relaunch must restore the
	// previous build and exec onto it — never serving the unvalidated target.
	for _, barrier := range []string{"commit-receipt", "post-rename"} {
		t.Run(barrier, func(t *testing.T) {
			f := newRecoveryKillFixture(t)
			f.killAtBarrier(t, barrier)

			p := f.relaunch()
			f.requireRestoredService(t, p)
		})
	}

	// Interruptions through the restore boundaries: a relaunch is killed
	// mid-recovery (after the write-ahead attempt, after the restore rename,
	// after the durable rolled_back), and a second ordinary launch finishes
	// the reconciliation — reusing the recorded restore copy by identity.
	for _, barrier := range []string{"restore-attempt", "restore-rename", "restore-rolledback"} {
		t.Run(barrier, func(t *testing.T) {
			f := newRecoveryKillFixture(t)
			// Produce the candidate-installed pending state first.
			f.killAtBarrier(t, "post-rename")

			// The first relaunch performs production recovery and blocks at
			// the armed barrier inside the restoration. The previous
			// barrier's marker is removed first so the wait observes only
			// this relaunch's barrier.
			if err := os.Remove(f.barrierFile); err != nil {
				t.Fatalf("remove stale barrier marker: %v", err)
			}
			mid := f.relaunch("--barrier", barrier, "--barrier-file", f.barrierFile)
			deadline := time.Now().Add(45 * time.Second)
			for {
				if _, err := os.Stat(f.barrierFile); err == nil {
					break
				}
				if mid.exited() {
					t.Fatalf("recovery relaunch died before the %s barrier (stderr tail:\n%s)", barrier, mid.stderrTail())
				}
				if time.Now().After(deadline) {
					t.Fatalf("recovery barrier %s never fired (stderr tail:\n%s)", barrier, mid.stderrTail())
				}
				time.Sleep(25 * time.Millisecond)
			}
			if barrier == "restore-attempt" {
				// The write-ahead attempt identity is durable before the
				// rename: the interrupted receipt must carry it.
				r := f.j.waitReceipt(5 * time.Second)
				if r.RecoveryAttemptID == "" || r.RecoveryKind != selfupdate.RecoveryKindRestore {
					t.Fatalf("interrupted receipt recovery = %q/%q; want a durable restore attempt", r.RecoveryKind, r.RecoveryAttemptID)
				}
				if r.RestorePath == "" || r.RestoreID == nil {
					t.Fatalf("interrupted receipt lacks restore metadata: %q %v", r.RestorePath, r.RestoreID)
				}
			}
			var installedBefore selfupdate.FileIdentity
			if barrier == "restore-rename" {
				id, err := selfupdate.StatFile(f.j.installPath)
				if err != nil {
					t.Fatalf("stat installed before final relaunch: %v", err)
				}
				installedBefore = id
				if got := f.j.installDigest(); got != f.j.bins.lowerDigest {
					t.Fatalf("installed digest after restore rename = %s; want restored lower bytes", got)
				}
			}
			_ = mid.cmd.Process.Kill()
			_ = mid.waitBounded(10 * time.Second)

			// A second ordinary launch finishes the reconciliation.
			p := f.relaunch()
			f.requireRestoredService(t, p)
			if barrier == "restore-rename" {
				// Finish-rollback never touches the already-restalled bytes.
				id, err := selfupdate.StatFile(f.j.installPath)
				if err != nil {
					t.Fatalf("stat installed after finish: %v", err)
				}
				if id.Dev != installedBefore.Dev || id.Ino != installedBefore.Ino {
					t.Fatal("finish-rollback replaced the already-restored installed bytes")
				}
			}
		})
	}
}

// TestSelfUpdateRecoverySecondFailure proves the launch-chain guard bounds
// recovery: a failing recovery exec or a failing recovered build exits
// nonzero without another automatic exec, and a failing restore leaves an
// actionable receipt a fresh operator launch can retry.
func TestSelfUpdateRecoverySecondFailure(t *testing.T) {
	selfupdateJourneyGuard(t)

	t.Run("recovery exec failure", func(t *testing.T) {
		f := newRecoveryKillFixture(t)
		p := f.j.start(selfupdateStartOptions{
			listen: "127.0.0.1:" + strconv.Itoa(f.port),
			failAt: "discovery,recovery-exec",
		})
		f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
		f.j.trigger()

		if code := p.waitBounded(45 * time.Second); code == 0 {
			t.Fatalf("process exited 0 despite the failed recovery exec:\n%s", p.stderrTail())
		}
		// The restoration itself succeeded and stayed durable; only the
		// exec onto the restored build failed, so the receipt is rolled_back
		// and the installed bytes are the previous build.
		r := f.j.waitReceiptRolledBack(10 * time.Second)
		if got := f.j.installDigest(); got != f.j.bins.lowerDigest {
			t.Fatalf("installed digest = %s; want restored lower bytes", got)
		}
		requireSuppressedTarget(t, f.j, r)
		// Bounded: no second automatic exec, no loop.
		if n := p.stderrPrefixCount("selfupdate-driver: exec /"); n != 1 {
			t.Fatalf("exec crossings = %d; want exactly the single handoff crossing (stderr tail:\n%s)", n, p.stderrTail())
		}
	})

	t.Run("recovered build startup failure", func(t *testing.T) {
		f := newRecoveryKillFixture(t)
		p := f.j.start(selfupdateStartOptions{
			listen: "127.0.0.1:" + strconv.Itoa(f.port),
			failAt: "discovery,recovered-startup",
		})
		f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
		f.j.trigger()

		if code := p.waitBounded(45 * time.Second); code == 0 {
			t.Fatalf("recovered build exited 0 despite the injected startup failure:\n%s", p.stderrTail())
		}
		if !p.stderrContains("recovered-startup") {
			t.Fatalf("no recovered-startup failure diagnostic:\n%s", p.stderrTail())
		}
		f.j.waitReceiptRolledBack(10 * time.Second)
		if got := f.j.installDigest(); got != f.j.bins.lowerDigest {
			t.Fatalf("installed digest = %s; want restored lower bytes", got)
		}
		if n := p.stderrPrefixCount("selfupdate-driver: exec /"); n != 1 {
			t.Fatalf("exec crossings = %d; want exactly one — the chain guard forbids a loop (stderr tail:\n%s)", n, p.stderrTail())
		}
	})

	t.Run("restore failure leaves a retryable receipt", func(t *testing.T) {
		f := newRecoveryKillFixture(t)
		p := f.j.start(selfupdateStartOptions{
			listen: "127.0.0.1:" + strconv.Itoa(f.port),
			failAt: "discovery,restore-rename",
		})
		f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
		f.j.trigger()

		if code := p.waitBounded(45 * time.Second); code == 0 {
			t.Fatalf("process exited 0 despite the failed restore:\n%s", p.stderrTail())
		}
		r := f.j.waitReceipt(10 * time.Second)
		if !r.ActionablePending() {
			t.Fatalf("receipt = %q/%q; want actionable pending after the failed restore", r.Outcome, r.Phase)
		}
		if r.RecoveryAttemptID == "" || r.RestorePath == "" {
			t.Fatalf("failed restore lost the write-ahead attempt metadata: %+v", r)
		}

		// A fresh operator launch retries the validated recovery and
		// succeeds, reusing the recorded restore copy.
		relaunch := f.relaunch()
		f.requireRestoredService(t, relaunch)
	})
}

// TestSelfUpdateCleanupRetry proves a confirmed healthy transaction whose
// cleanup failed keeps serving, and a later launch retries the validated
// cleanup without disturbing the confirmed outcome.
func TestSelfUpdateCleanupRetry(t *testing.T) {
	selfupdateJourneyGuard(t)
	f := newRecoveryKillFixture(t)

	p := f.j.start(selfupdateStartOptions{
		listen: "127.0.0.1:" + strconv.Itoa(f.port),
		failAt: "cleanup-remove",
	})
	f.j.waitHealthy(p, f.baseURL, selfupdateVersionLower, 30*time.Second)
	f.j.trigger()

	// The update confirms and keeps serving; only the cleanup fails.
	f.j.waitHealthy(p, f.baseURL, selfupdateVersionHigher, 30*time.Second)
	r := f.j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	txDir := filepath.Join(selfupdate.LeaseDir(f.j.installPath), "tx-"+r.TransactionID)
	if _, err := os.Stat(txDir); err != nil {
		t.Fatalf("failed cleanup must retain the transaction dir: %v", err)
	}
	p.waitStderr(t, "selfupdate cleanup")
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit = %d; want 0 — a cleanup failure never breaks a confirmed build (stderr tail:\n%s)", code, p.stderrTail())
	}

	// A later launch retries the validated cleanup: the confirmed outcome
	// stands and the recognized objects are gone.
	relaunch := f.relaunch()
	f.j.waitHealthy(relaunch, f.baseURL, selfupdateVersionHigher, 30*time.Second)
	f.j.waitPathGone(txDir, "transaction dir", 15*time.Second)
	after, err := selfupdate.ReadReceipt(f.j.receiptPath())
	if err != nil {
		t.Fatalf("re-read receipt: %v", err)
	}
	if after.Outcome != selfupdate.OutcomeConfirmed || after.Phase != selfupdate.PhaseReplacementComplete {
		t.Fatalf("receipt after cleanup retry = %q/%q; want confirmed/replacement-complete", after.Outcome, after.Phase)
	}
	if code := relaunch.terminate(); code != 0 {
		t.Fatalf("relaunch SIGTERM exit = %d; want 0 (stderr tail:\n%s)", code, relaunch.stderrTail())
	}
}
