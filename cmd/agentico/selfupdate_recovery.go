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
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"golang.org/x/sys/unix"
)

// defaultStartupDeadline bounds how long an adopted target image may take to
// reach full bootstrap cooperatively. Expiry routes through the recovery
// boundary: a hung target never keeps the previous build out of service.
const defaultStartupDeadline = 120 * time.Second

// selfUpdateHealthClient bounds every self-health probe.
var selfUpdateHealthClient = &http.Client{Timeout: time.Second}

// startupDeadline resolves the adopted-image startup deadline (driver hook
// may shrink it for injection).
func startupDeadline() time.Duration {
	if startupDeadlineHook != nil {
		if d := startupDeadlineHook(); d > 0 {
			return d
		}
	}
	return defaultStartupDeadline
}

// selfHealthProbeURLs returns the URLs to probe for the server's own health:
// the advertised base URL first, plus the loopback family for a wildcard
// bind — the listener is always reachable on loopback even when a host
// firewall blocks the advertised interface address.
func selfHealthProbeURLs(s *serverruntime.RuntimeServer) []string {
	if s == nil {
		return nil
	}
	urls := []string{s.BaseURL()}
	if s.WildcardBind() && s.Port() > 0 {
		if s.BindHost() == "::" {
			urls = append(urls, fmt.Sprintf("http://[::1]:%d", s.Port()))
		} else {
			urls = append(urls, fmt.Sprintf("http://127.0.0.1:%d", s.Port()))
		}
	}
	return urls
}

// waitSelfHealthy polls the server's own unauthenticated health endpoint
// (each probe URL in turn) until one reports ok. The adopted target is never
// confirmed, and the recovered build never admits ordinary work, before this
// wait succeeds.
func waitSelfHealthy(urls []string, timeout time.Duration) error {
	if selfUpdateHealthWaitFn != nil {
		return selfUpdateHealthWaitFn(urls)
	}
	if len(urls) == 0 {
		return errors.New("no self-health probe URL")
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		for _, u := range urls {
			resp, err := selfUpdateHealthClient.Get(u + "/api/v1/health")
			if err == nil {
				if resp.StatusCode == http.StatusOK {
					_ = resp.Body.Close()
					return nil
				}
				lastErr = fmt.Errorf("health status = %d", resp.StatusCode)
				_ = resp.Body.Close()
			} else {
				lastErr = err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("self-health wait at %v: %w", urls, lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// confirmAdoptedTransaction durably confirms the adopted transaction. The
// seam lets a tagged driver inject the confirmation failure; production
// always runs the real write-once confirm.
func confirmAdoptedTransaction(execPath, txID string) (selfupdate.Receipt, error) {
	if selfUpdateConfirmFn != nil {
		return selfUpdateConfirmFn(execPath, txID)
	}
	return selfupdate.ConfirmTransaction(execPath, txID)
}

// recoveryBarrier reports a deterministic stage of production recovery to
// the (driver-only) barrier hook; nil in production builds.
func recoveryBarrier(stage string) {
	if selfUpdateRecoveryBarrier != nil {
		selfUpdateRecoveryBarrier(stage)
	}
}

// recoverySeams resolves the (driver-only) recovery failure seams.
func recoverySeams() selfupdate.FileOps {
	return selfUpdateFileOps
}

// bootRecoveryResult is the outcome of pre-bootstrap recovery resolution.
type bootRecoveryResult struct {
	// exit is true when the launch must stop with code (rendered refusal or
	// an exec that never returned is impossible here — exec only happens for
	// restore, which never returns).
	exit bool
	code int
	// lease is the binary lease acquired during resolution, to be carried
	// into the ordinary boot so ownership stays continuous for the process
	// lifetime.
	lease *selfupdate.Lease
	// listenOverride rebinds the recovered build's recorded concrete
	// endpoint (wildcard form preserved) when this launch is a recovered or
	// restarted image.
	listenOverride string
	// recoveredChain marks that this launch carries the recovery guard: it
	// is the recovered or restarted image of a prior chain, so its own
	// startup is held to the same health-wait bar and a further automatic
	// recovery is forbidden.
	recoveredChain bool
	// restoredRollback marks that this launch resolved (or finished) an
	// actual rollback and should report the canonical update_rolled_back
	// failure for the consumed update result.
	restoredRollback *selfupdate.Receipt
}

// acquireRecoveryLease takes the binary lease for recovery resolution. A
// live holder blocks: recovery authority is never stolen, and a competing
// launch must fail rather than wait unbounded.
func acquireRecoveryLease(exec selfupdate.Executable, runtimeDir, stateDir, configPath string) (*selfupdate.Lease, error) {
	pgid := 0
	if g, err := unix.Getpgid(0); err == nil {
		pgid = g
	}
	return selfupdate.AcquireLease(exec, selfupdate.OwnershipRecord{
		RuntimeDir:       runtimeDir,
		StateDir:         stateDir,
		Config:           configPath,
		PID:              os.Getpid(),
		PGID:             pgid,
		Version:          buildinfo.Version(),
		StartedAt:        time.Now().UTC(),
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		ExecutableIno:    exec.ID.Ino,
	})
}

// renderRecoveryRefusal renders an unsafe-record or persistence refusal as a
// sanitized startup fatal and returns exit code 1.
func renderRecoveryRefusal(err error) int {
	renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("resolving interrupted selfupdate transaction: %v", err)})
	return 1
}

// resolveRecoveryOnBoot runs mandatory recovery resolution before
// configuration loading, fx construction, bootstrap children, update-policy
// selection, or any ownership-record overwrite. Absence of a transaction
// preserves ordinary startup. The executable's actual identity and bytes,
// the durable phase, and the lease flock are the only authorities; stale PID
// metadata is never consulted.
func resolveRecoveryOnBoot(stateDir, configPath, listenAddr string) bootRecoveryResult {
	exec, err := selfupdate.CaptureExecutable()
	if err != nil {
		// A failed capture disables resolution, never the launch (mirrors the
		// ordinary boot's tolerance).
		return bootRecoveryResult{}
	}
	runtimeDir := filepath.Dir(canonicalizeStateDir(stateDir))

	// A live original handoff blocks a competing startup before any decision.
	if err := checkLiveHandoff(exec.Path); err != nil {
		renderStartupFailure(os.Stderr, err)
		return bootRecoveryResult{exit: true, code: 1}
	}

	guard, guardPresent, guardErr := selfupdate.ParseRecoveryGuardEnv(os.Environ())

	plan, err := selfupdate.InspectRecovery(exec, runtimeDir)
	if err != nil {
		return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
	}
	guardListen := ""
	if guardPresent && guardErr == nil && guard.Bind.Host != "" && guard.Bind.Port > 0 {
		guardListen = net.JoinHostPort(guard.Bind.Host, strconv.Itoa(guard.Bind.Port))
	}

	switch plan.Action {
	case selfupdate.RecoveryActionNone:
		reconcileSettledState(exec, plan, runtimeDir, stateDir, configPath)
		result := bootRecoveryResult{listenOverride: guardListen, recoveredChain: guardPresent}
		if plan.Receipt.Outcome == selfupdate.OutcomeRolledBack && exec.Digest == plan.Receipt.OldDigest {
			// This runtime is the restored previous build: report the
			// canonical update_rolled_back failure for the consumed result.
			result.restoredRollback = &plan.Receipt
		}
		return result

	case selfupdate.RecoveryActionAbandon:
		// Abandonment is always safe in-chain: it mutates no installed bytes.
		lease, err := acquireRecoveryLease(exec, runtimeDir, stateDir, configPath)
		if err != nil {
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		rlock, err := selfupdate.AcquireRuntimeUpdateLock(runtimeDir)
		if err != nil {
			_ = lease.Close()
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		reason := fmt.Sprintf("update to %s never replaced the installed build; installation failed and the previous build stays in service", plan.Receipt.ToVersion)
		settled, err := selfupdate.ResolveAbandoned(exec, lease, plan, reason, recoverySeams())
		_ = rlock.Close()
		if err != nil {
			_ = lease.Close()
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("selfupdate transaction %s settled as an installation failure (target %s was never installed); the previous build stays in service", settled.TransactionID, settled.ToVersion)))
		cleanupSettledBestEffort(exec, settled, plan)
		return bootRecoveryResult{lease: lease, listenOverride: guardListen}

	case selfupdate.RecoveryActionRestore, selfupdate.RecoveryActionFinishRollback:
		// The launch-chain guard: one automatic recovery per transaction per
		// chain; a second failure exits nonzero instead of looping. A
		// corrupted guard fails closed for automatic restore: this chain
		// cannot prove which transaction it already attempted.
		if guardPresent && (guardErr != nil || guard.TransactionID == plan.Receipt.TransactionID) {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("refusing a second automatic recovery of transaction %s in the same launch chain; start a new launch to retry", plan.Receipt.TransactionID)})
			return bootRecoveryResult{exit: true, code: 1}
		}
		lease, err := acquireRecoveryLease(exec, runtimeDir, stateDir, configPath)
		if err != nil {
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		rlock, err := selfupdate.AcquireRuntimeUpdateLock(runtimeDir)
		if err != nil {
			_ = lease.Close()
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		recoveryBarrier("before-restore")
		reason := fmt.Sprintf("target %s failed to return to service; previous build %s restored", plan.Receipt.ToVersion, plan.Receipt.FromVersion)
		receipt, err := selfupdate.RestorePrevious(exec, lease, plan, reason, recoverySeams())
		_ = rlock.Close()
		if err != nil {
			_ = lease.Close()
			return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(err)}
		}
		recoveryBarrier("after-restore")
		// The lease does not cross the recovery exec: the restored build
		// takes its own ownership on boot, and no inherited descriptor can
		// outlive a build that may predate this feature.
		if err := lease.Close(); err != nil {
			reportDeferredClose(os.Stderr, "close recovery lease", err)
		}
		guard := selfupdate.RecoveryGuard{
			SchemaVersion: 1,
			TransactionID: receipt.TransactionID,
			AttemptID:     receipt.RecoveryAttemptID,
			Kind:          selfupdate.RecoveryKindRestore,
			Bind:          receipt.Bind,
			AuthTokenPath: serverruntime.AuthTokenPath(runtimeDir),
		}
		env := selfupdate.BuildRecoveryEnv(os.Environ(), guard)
		recoveryBarrier("before-recovery-exec")
		if err := selfupdate.ExecRecovery(exec.Path, os.Args, env, selfUpdateRecoveryExecFn); err != nil {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("recovery exec onto restored build: %v", err)})
			return bootRecoveryResult{exit: true, code: 1}
		}
		// A successful exec never returns.
		return bootRecoveryResult{exit: true, code: 0}
	default:
		return bootRecoveryResult{exit: true, code: renderRecoveryRefusal(fmt.Errorf("unsupported recovery action %q", plan.Action))}
	}
}

// reconcileSettledState performs the idempotent boot-time reconciliation for
// a settled receipt: exact-target suppression is re-asserted (a failed
// best-effort write at rollback time must never erase the outcome), and
// recognized cleanup of the settled transaction and of abandoned pre-install
// staging is retried under the binary lease, skipping anything live-owned.
// A plan with no receipt path is the ordinary no-transaction launch: there is
// no settled outcome to re-assert or retry, and attempting cleanup with an
// empty transaction id would report false maintenance failures and write a
// receipt outside the updater-owned location. Staging reconciliation still
// runs so abandoned staging from a lost pre-install attempt is collected.
func reconcileSettledState(exec selfupdate.Executable, plan selfupdate.RecoveryPlan, runtimeDir, stateDir, configPath string) {
	hasReceipt := plan.ReceiptPath != ""
	if hasReceipt && plan.Receipt.Outcome == selfupdate.OutcomeRolledBack {
		if err := selfupdate.ReconcileSuppression(exec.Path, plan.Receipt); err != nil {
			renderError(os.Stderr, errcat.StartupMaintenanceFailed,
				errcat.WithDiagnostics(fmt.Sprintf("re-asserting update rollback suppression: %v", err)))
		}
	}
	// Cleanup and staging reconciliation run only while the binary lease is
	// free: a live owner's in-progress staging is never touched.
	lease, err := acquireRecoveryLease(exec, runtimeDir, stateDir, configPath)
	if err != nil {
		return
	}
	defer func() { _ = lease.Close() }()
	if hasReceipt {
		cleanupSettledBestEffort(exec, plan.Receipt, plan)
	}
	if err := selfupdate.ReconcileStaging(exec.Path, "", selfUpdateCleanupSeams); err != nil {
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("selfupdate staging reconciliation: %v", err)))
	}
}

// cleanupSettledBestEffort retries validated cleanup for one settled
// transaction: a cleanup failure never turns a confirmed healthy
// transaction into rollback or suppression; it records a sanitized failure
// in the settled receipt and is retried on a later launch.
func cleanupSettledBestEffort(exec selfupdate.Executable, r selfupdate.Receipt, plan selfupdate.RecoveryPlan) {
	if err := selfupdate.CleanupSettledTransaction(exec.Path, r.TransactionID, selfUpdateCleanupSeams); err != nil {
		renderError(os.Stderr, errcat.StartupMaintenanceFailed,
			errcat.WithDiagnostics(fmt.Sprintf("selfupdate cleanup for transaction %s: %v", r.TransactionID, err)))
		if rerr := selfupdate.RecordSettledError(exec.Path, plan.ReceiptPath, r, fmt.Sprintf("cleanup retry pending: %v", err)); rerr != nil {
			renderError(os.Stderr, errcat.StartupMaintenanceFailed,
				errcat.WithDiagnostics(fmt.Sprintf("recording selfupdate cleanup failure: %v", rerr)))
		}
	}
}

// failedTargetRecovery carries everything the one-shot recovery boundary
// needs: the partially started state to tear down explicitly, the adopted
// lease and receipt, and the failure reason. It runs at most once per
// launch chain; any failure of restore, recovery exec, or the boundary
// itself exits nonzero with actionable metadata retained.
type failedTargetRecovery struct {
	boot        *runtimeBootstrap
	server      *serverruntime.RuntimeServer
	registryDir string
	authToken   string
	exec        selfupdate.Executable
	lease       *selfupdate.Lease
	receipt     selfupdate.Receipt
	runtimeDir  string
	reason      string
	// restartUnchanged switches the boundary from restore-and-reexec to the
	// unchanged-binary restart of an aborted shutdown (the transaction never
	// committed; nothing is restored).
	restartUnchanged bool
	// bind records the concrete endpoint the re-executed image must rebind.
	bind selfupdate.BindEndpoint
}

// teardownPartialStartup explicitly stops partially started HTTP/SSE,
// workers, services, and observations with fresh bounded contexts — never
// depending on successful-exec defers or closed runtime objects, and never
// reusing an expired startup context. Only owned discovery and registry
// resources are removed.
func (t failedTargetRecovery) teardownPartialStartup() {
	if t.boot != nil {
		shutdownFeatures(t.boot.orchestrator, t.boot.sessionManager)
	}
	if t.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := t.server.Close(ctx); err != nil {
			reportDeferredClose(os.Stderr, "close server during recovery", err)
		}
		cancel()
	}
	if t.boot != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := t.boot.StopServices(ctx); err != nil {
			reportDeferredClose(os.Stderr, "stop services during recovery", err)
		}
		cancel()
	}
	if t.registryDir != "" && t.boot != nil {
		_ = serverruntime.RemoveRegistryEntry(t.registryDir, t.boot.runtime.RuntimeDir)
	}
}

// recover runs the single recovery boundary for this launch chain: tear
// down, restore (or restart the unchanged build), and exec the installed
// path — never a backup path — with the chain guard. Returns the exit code
// when recovery itself fails; never returns after a successful exec.
func (t failedTargetRecovery) recover() int {
	t.teardownPartialStartup()

	rlock, err := selfupdate.AcquireRuntimeUpdateLock(t.runtimeDir)
	if err != nil {
		renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("recovery boundary: %v", err)})
		return 1
	}
	defer func() { _ = rlock.Close() }()

	plan := selfupdate.RecoveryPlan{Receipt: t.receipt, ReceiptPath: selfupdate.ReceiptPath(t.exec.Path)}
	if t.restartUnchanged {
		recoveryBarrier("before-restart")
		receipt, err := selfupdate.MarkRecoveryRestart(t.exec, t.lease, plan, t.reason, recoverySeams())
		if err != nil {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("marking unchanged-build restart: %v", err)})
			return 1
		}
		recoveryBarrier("after-restart-marked")
		if err := t.lease.Close(); err != nil {
			reportDeferredClose(os.Stderr, "close lease before restart exec", err)
		}
		guard := selfupdate.RecoveryGuard{
			SchemaVersion: 1,
			TransactionID: receipt.TransactionID,
			AttemptID:     receipt.RecoveryAttemptID,
			Kind:          selfupdate.RecoveryKindRestart,
			Bind:          t.bind,
			AuthTokenPath: serverruntime.AuthTokenPath(t.runtimeDir),
		}
		env := selfupdate.BuildRecoveryEnv(os.Environ(), guard)
		recoveryBarrier("before-recovery-exec")
		if err := selfupdate.ExecRecovery(t.exec.Path, os.Args, env, selfUpdateRecoveryExecFn); err != nil {
			renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("restart exec onto unchanged build: %v", err)})
			return 1
		}
		return 0
	}

	recoveryBarrier("before-restore")
	receipt, err := selfupdate.RestorePrevious(t.exec, t.lease, plan, t.reason, recoverySeams())
	if err != nil {
		renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("recovery restore: %v", err)})
		return 1
	}
	recoveryBarrier("after-restore")
	if err := t.lease.Close(); err != nil {
		reportDeferredClose(os.Stderr, "close lease before recovery exec", err)
	}
	guard := selfupdate.RecoveryGuard{
		SchemaVersion: 1,
		TransactionID: receipt.TransactionID,
		AttemptID:     receipt.RecoveryAttemptID,
		Kind:          selfupdate.RecoveryKindRestore,
		Bind:          receipt.Bind,
		AuthTokenPath: serverruntime.AuthTokenPath(t.runtimeDir),
	}
	env := selfupdate.BuildRecoveryEnv(os.Environ(), guard)
	recoveryBarrier("before-recovery-exec")
	if err := selfupdate.ExecRecovery(t.exec.Path, os.Args, env, selfUpdateRecoveryExecFn); err != nil {
		renderStartupFailure(os.Stderr, &runtimeInitError{fmt.Errorf("recovery exec onto restored build: %v", err)})
		return 1
	}
	return 0
}

// tolerableShutdownDeadline reports whether a server close error is solely
// the cooperative graceful-drain deadline: never-idle SSE streams can only
// end the graceful phase at its deadline, and RuntimeServer.Close then
// force-closes them, so a deadline-only error means draining completed
// within budget. An error that joins anything else is never discarded merely
// because it includes a timeout.
func tolerableShutdownDeadline(err error) bool {
	if err == nil {
		return true
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Every leaf must be a deadline: a joined non-deadline error means some
	// required closure did not positively complete.
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, leaf := range joined.Unwrap() {
			if leaf == nil {
				continue
			}
			if !tolerableShutdownDeadline(leaf) {
				return false
			}
		}
	}
	return true
}
