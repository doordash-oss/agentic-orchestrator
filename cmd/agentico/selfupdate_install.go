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
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// productionReleaseStager runs the existing signed-release pipeline for one
// pinned version: resolve-by-version, signature verification, authenticated
// staging, the isolated probe, and admission. It is the same pipeline the
// deliberately built test driver exercises; production reaches it only
// through the authenticated install endpoint.
type productionReleaseStager struct {
	feed           *selfupdate.FeedClient
	exec           selfupdate.Executable
	currentVersion string
	eligibility    selfupdate.Eligibility
}

// StageCandidate resolves, verifies, stages, probes, and admits the pinned
// release version. progress reports the active pipeline stage so the
// coordinator can classify a failure canonically; cleanup settles the owned
// release staging idempotently.
func (s *productionReleaseStager) StageCandidate(ctx context.Context, version string, progress func(stage string)) (selfupdate.VerifiedCandidate, *selfupdate.ServerContract, func() error, error) {
	cleanup := func() error { return nil }
	progress("resolve")
	resolved, err := s.feed.ResolveReleaseVersion(ctx, version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return selfupdate.VerifiedCandidate{}, nil, cleanup, err
	}
	progress("verify")
	verified, err := s.feed.VerifyResolvedRelease(ctx, resolved)
	if err != nil {
		return selfupdate.VerifiedCandidate{}, nil, cleanup, err
	}
	progress("download")
	staged, err := s.feed.StageVerifiedRelease(ctx, s.exec, verified, selfupdate.StageReleaseOptions{
		CurrentVersion: s.currentVersion,
		Eligibility:    s.eligibility,
	})
	if staged != nil {
		// The staging record is durable before any download byte, so this
		// cleanup is safe and idempotent in every outcome.
		execPath, txID := s.exec.Path, staged.TxID()
		cleanup = func() error {
			return selfupdate.CleanupSettledTransaction(execPath, txID, selfupdate.CleanupSeams{})
		}
	}
	if err != nil {
		return selfupdate.VerifiedCandidate{}, nil, cleanup, err
	}
	progress("probe")
	probe, err := selfupdate.ProbeStagedExecutable(ctx, staged.CandidatePath(), version)
	if err != nil {
		return selfupdate.VerifiedCandidate{}, nil, cleanup, err
	}
	progress("admit")
	candidate, err := selfupdate.AdmitStagedRelease(staged, probe)
	if err != nil {
		return selfupdate.VerifiedCandidate{}, nil, cleanup, err
	}
	return candidate, verified.ServerContract(), cleanup, nil
}

// productionInstallTransaction adapts one durable replacement transaction
// to the server coordinator's handle.
type productionInstallTransaction struct {
	run *serverRun
	tx  *selfupdate.Transaction

	// settleOnce keeps the pre-replacement settlement exactly-once across
	// worker cancellation and repeated DELETE cleanup retries.
	settleOnce sync.Once
	settleErr  error
}

// ID returns the durable transaction identifier.
func (t *productionInstallTransaction) ID() string {
	return t.tx.Receipt().TransactionID
}

// Cancel settles a pre-replacement transaction consistently: the pending
// receipt resolves as abandoned pre-replacement — never a failed boot, and
// never rollback suppression — and validated owned resources are cleaned.
// Idempotent.
func (t *productionInstallTransaction) Cancel() error {
	t.settleOnce.Do(func() {
		exec, lease := t.run.boot.selfUpdateExec, t.run.boot.updateLease
		plan := selfupdate.RecoveryPlan{
			Receipt:     t.tx.Receipt(),
			ReceiptPath: selfupdate.ReceiptPath(exec.Path),
		}
		if _, err := selfupdate.ResolveAbandoned(exec, lease, plan, "install cancelled before replacement", selfupdate.FileOps{}); err != nil {
			t.settleErr = fmt.Errorf("settling cancelled transaction: %w", err)
			return
		}
		t.settleErr = selfupdate.CleanupSettledTransaction(exec.Path, t.ID(), selfupdate.CleanupSeams{})
	})
	return t.settleErr
}

// productionInstallLifecycle owns the process-level replacement work for
// the ordinary (non-driver) server: the per-runtime update transaction
// lock, durable transaction preparation, and the final drain→commit→exec
// sequence — the shared replacement tail the driver journeys also
// exercise, run with the zero (production) replacementSeams.
type productionInstallLifecycle struct {
	// run is published once the server has started; install requests that
	// race startup on a preserved endpoint are refused until then.
	mu  sync.Mutex
	run *serverRun
	// stager carries the pipeline inputs for transaction preparation.
	stager *productionReleaseStager
	// exitCode carries a failed recovery boundary's termination request to
	// the serving goroutine, which owns the process exit: a second recovery
	// failure terminates without an exec loop and without os.Exit from this
	// goroutine.
	exitCode chan int
}

// setRun publishes the started server's handle.
func (l *productionInstallLifecycle) setRun(run *serverRun) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.run = run
}

// setRegistryDir records the central registry location on the run handle.
func (l *productionInstallLifecycle) setRegistryDir(dir string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.run.registryDir = dir
}

// current returns a copy of the run handle, or an error while the server
// is still starting.
func (l *productionInstallLifecycle) current() (serverRun, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil {
		return serverRun{}, errors.New("server is still starting")
	}
	return *l.run, nil
}

// AcquireUpdateLock takes the per-runtime update transaction lock.
func (l *productionInstallLifecycle) AcquireUpdateLock() (func(), bool) {
	r, err := l.current()
	if err != nil {
		return func() {}, false
	}
	lock, err := selfupdate.AcquireRuntimeUpdateLock(r.boot.runtime.RuntimeDir)
	if err != nil {
		return func() {}, false
	}
	return func() { _ = lock.Close() }, true
}

// Begin prepares the durable transaction — revalidated provenance, the
// owner-only backup, and the pending write-ahead receipt — while HTTP is
// still available. The bind endpoint comes from the live server, never the
// argv flags.
func (l *productionInstallLifecycle) Begin(candidate selfupdate.VerifiedCandidate) (serverruntime.InstallTransaction, error) {
	run, err := l.current()
	if err != nil {
		return nil, err
	}
	r := &run
	tx, err := selfupdate.BeginVerifiedRelease(r.boot.selfUpdateExec, candidate, selfupdate.BeginOptions{
		RuntimeDir:  r.boot.runtime.RuntimeDir,
		StateDir:    r.boot.runtime.StateDir,
		Config:      r.boot.runtime.Config,
		PID:         os.Getpid(),
		PGID:        r.boot.owner.PGID,
		FromVersion: buildinfo.Version(),
		ToVersion:   candidate.Release().Resolved().Version,
		Bind: selfupdate.BindEndpoint{
			Host:         r.server.BindHost(),
			Port:         r.server.Port(),
			Wildcard:     r.server.WildcardBind(),
			AdvertiseURL: r.server.BaseURL(),
			Policy:       r.server.RuntimePolicy(),
		},
	}, selfupdate.StageReleaseOptions{
		CurrentVersion: l.stager.currentVersion,
		Eligibility:    l.stager.eligibility,
	}, selfupdate.FileOps{})
	if err != nil {
		return nil, err
	}
	return &productionInstallTransaction{run: r, tx: tx}, nil
}

// Replace runs the final drain, commit, and exec through the shared
// replacement tail with the zero (production) seams. The caller has
// already published draining and waited for the accepted response to
// complete; the notify callback receives restarting after commit. On
// success and on every recovery path it never returns. It returns a
// sanitized reason only when an aborted drain leaves the old build
// serving.
func (l *productionInstallLifecycle) Replace(handle serverruntime.InstallTransaction, notify func(status string), _ <-chan struct{}) error {
	tx, ok := handle.(*productionInstallTransaction)
	if !ok {
		return errors.New("unknown install transaction handle")
	}
	r, err := l.current()
	if err != nil {
		return err
	}
	releaseUpdateLock := func() {}
	if lock, err := selfupdate.AcquireRuntimeUpdateLock(r.boot.runtime.RuntimeDir); err == nil {
		releaseUpdateLock = func() { _ = lock.Close() }
	}
	// requestExit hands a failed recovery boundary's termination code to
	// the serving goroutine; the runtime is already torn down, so the
	// process must exit without an exec loop.
	requestExit := func(code int) {
		select {
		case l.exitCode <- code:
		default:
		}
	}

	res := executeReplacementTail(r, tx.tx, releaseUpdateLock, notify, replacementSeams{})
	if res.reason != "" {
		if res.exitCode != 0 {
			requestExit(res.exitCode)
		}
		// The recovery boundary exec'd (which never returns) or left the
		// old build serving again; either way the surfaced failure is
		// truthful.
		return fmt.Errorf("%s", selfupdate.SanitizeError(res.reason))
	}
	// A successful exec never returns; this line only guards an
	// ExecReplace implementation that wrongly returns nil.
	return errors.New("the replacement returned without executing the target")
}

// replacementSeams carries the deliberately built selfupdate driver's
// failure-injection and barrier points through the one production
// replacement tail. The zero value is production: no injected failure, no
// barrier, the real settlement and exec, and no milestone printing.
type replacementSeams struct {
	// failDrain reports an armed pre-shutdown drain failure. The tail then
	// aborts before any serving resource closes, settles the installation
	// failure, and keeps serving the unchanged build.
	failDrain func() bool
	// rewriteServerClose rewrites the server-close outcome for the
	// server-close and shutdown-mixed injections: a tolerable real
	// deadline can be declared intolerable, a close failure synthesized,
	// or an unrelated error joined onto the deadline. err/tolerable are
	// the real outcome.
	rewriteServerClose func(err error, tolerable bool) (error, bool)
	// failStop reports an armed stop failure after the real stop succeeded
	// and serving resources closed.
	failStop func() bool
	// barrier blocks forever at a journey stage (pre-commit, post-rename)
	// so the harness can SIGKILL at a deterministic point; non-matching
	// stages no-op.
	barrier func(stage string)
	// failPostRename reports an armed post-commit failure: the target must
	// never be invoked and the boundary restores the previous build.
	failPostRename func() bool
	// execFn replaces the exec implementation for the exec-error injection;
	// nil performs the real exec(2). When set, a returned error is the
	// injected failure and the recovery reason names the injection.
	execFn selfupdate.ExecFunc
	// settleAbortedInstall overrides the pre-restart settlement of an
	// aborted installation on the close-failure path; nil runs the real
	// ResolveAbandoned. The driver installs a no-op for its injected close
	// failures only: their asserted journeys keep the receipt actionable
	// so the restart attempt stays recordable, and the restarted image's
	// boot performs the real settlement.
	settleAbortedInstall func(reason string) error
	// logf renders one driver milestone line; nil stays silent.
	logf func(format string, args ...any)
}

// milestone renders one driver milestone print; production seams stay
// silent.
func (s replacementSeams) milestone(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// blockAt runs the barrier for one journey stage; production seams never
// block.
func (s replacementSeams) blockAt(stage string) {
	if s.barrier != nil {
		s.barrier(stage)
	}
}

// replacementTailResult reports how the shared replacement tail ended. A
// successful exec never returns, so every field describes a failure
// surface.
type replacementTailResult struct {
	// keepServing is true when an aborted drain left the old runtime usable
	// and still serving: no recovery boundary ran.
	keepServing bool
	// exitCode carries a failed recovery boundary's termination request;
	// zero means the boundary exec'd (which never returns) or never ran.
	exitCode int
	// reason is the failure reason to surface: the production caller
	// sanitizes it for the HTTP surface, the driver prints milestones
	// instead.
	reason string
}

// executeReplacementTail carries one prepared transaction through the
// orderly shutdown, atomic commit, and exec handoff — the single
// implementation of the replacement tail, shared by the production install
// lifecycle (zero seams) and the driver's replace and release journeys
// (injection seams). The executable is never replaced unless the drain, the
// service stop, and the commit all succeed. releaseUpdateLock runs before
// every recovery boundary (the boundary re-acquires the same lock itself);
// notify receives restarting after a successful commit.
func executeReplacementTail(r serverRun, tx *selfupdate.Transaction, releaseUpdateLock func(), notify func(status string), seams replacementSeams) replacementTailResult {
	planFor := func() selfupdate.RecoveryPlan {
		return selfupdate.RecoveryPlan{
			Receipt:     tx.Receipt(),
			ReceiptPath: selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		}
	}
	// recoverUnchanged returns an old runtime whose serving resources
	// already closed to service through the guarded unchanged-build
	// restart — an installation failure, never a fictitious rollback.
	// recoverPrevious runs the recovery boundary from the old image once
	// the candidate bytes landed: restore the previous build and exec it
	// with the chain guard. Both release the update lock first.
	recoverUnchanged := func(reason string) int {
		releaseUpdateLock()
		seams.milestone("restarting unchanged build after aborted shutdown\n")
		return (failedTargetRecovery{
			boot:             r.boot,
			server:           r.server,
			registryDir:      r.registryDir,
			authToken:        r.authToken,
			exec:             r.boot.selfUpdateExec,
			lease:            r.boot.updateLease,
			receipt:          tx.Receipt(),
			runtimeDir:       r.boot.runtime.RuntimeDir,
			reason:           reason,
			restartUnchanged: true,
			bind:             tx.Receipt().Bind,
		}).recover()
	}
	recoverPrevious := func(reason string) int {
		releaseUpdateLock()
		return (failedTargetRecovery{
			boot:        r.boot,
			server:      r.server,
			registryDir: r.registryDir,
			authToken:   r.authToken,
			exec:        r.boot.selfUpdateExec,
			lease:       r.boot.updateLease,
			receipt:     tx.Receipt(),
			runtimeDir:  r.boot.runtime.RuntimeDir,
			reason:      reason,
			bind:        tx.Receipt().Bind,
		}).recover()
	}
	// An aborted drain settles the truthful installation failure before
	// the unchanged-build restart; a failed settle warns and never blocks
	// the restart.
	settle := seams.settleAbortedInstall
	if settle == nil {
		settle = func(reason string) error {
			_, err := selfupdate.ResolveAbandoned(r.boot.selfUpdateExec, r.boot.updateLease, planFor(), reason, selfupdate.FileOps{})
			return err
		}
	}

	// Draining fails while the old runtime is still usable (injected
	// failure only): abort the installation, settle the truthful
	// installation failure, and keep serving the unchanged build. No
	// serving resource is closed, the target is never executed, and the
	// target is never suppressed.
	if seams.failDrain != nil && seams.failDrain() {
		reason := "injected drain failure: installation aborted before any serving resource closed"
		settled, serr := selfupdate.ResolveAbandoned(r.boot.selfUpdateExec, r.boot.updateLease, planFor(), reason, selfupdate.FileOps{})
		if serr != nil {
			seams.milestone("settling aborted installation failed: %v\n", serr)
			return replacementTailResult{exitCode: 1}
		}
		seams.milestone("installation aborted pre-replacement (%s); still serving\n", settled.TransactionID)
		seams.milestone("drain failed (injected)\n")
		return replacementTailResult{keepServing: true}
	}

	// Drain and stop before any installed-path mutation. The executable is
	// never replaced unless all of these succeed.
	shutdownFeatures(r.boot.orchestrator, r.boot.sessionManager)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDrain()
	closeErr := r.server.Close(drainCtx)
	// A graceful SSE deadline permits progress only when every joined error
	// is the cooperative deadline itself (RuntimeServer.Close
	// force-terminates the remaining streams); unrelated joined errors are
	// never discarded merely because they include a timeout.
	tolerable := tolerableShutdownDeadline(closeErr)
	if seams.rewriteServerClose != nil {
		closeErr, tolerable = seams.rewriteServerClose(closeErr, tolerable)
	}
	if closeErr != nil && !tolerable {
		// An aborted drain keeps the old build usable only through the
		// unchanged-build restart; the sanitized reason surfaces if the
		// boundary instead leaves the process serving.
		reason := fmt.Sprintf("server shutdown failed: %v", closeErr)
		seams.milestone("server shutdown failed: %v\n", closeErr)
		if err := settle(reason); err != nil {
			fmt.Fprintf(os.Stderr, "agentico: settling aborted install failed: %v\n", err)
		}
		return replacementTailResult{exitCode: recoverUnchanged(reason), reason: reason}
	}
	// Idempotent release of the owned registry resource. The instance lock
	// is deliberately NOT released: it is close-on-exec and must stay held
	// until the exec boundary so no competing runtime can slip in.
	_ = serverruntime.RemoveRegistryEntry(r.registryDir, r.boot.runtime.RuntimeDir)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	if err := r.boot.StopServices(stopCtx); err != nil && !tolerableShutdownDeadline(err) {
		reason := fmt.Sprintf("stop services failed: %v", err)
		seams.milestone("stop services failed: %v\n", err)
		return replacementTailResult{exitCode: recoverUnchanged(reason), reason: reason}
	}
	if seams.failStop != nil && seams.failStop() {
		reason := "injected stop failure after serving resources closed"
		seams.milestone("stop failed (injected)\n")
		return replacementTailResult{exitCode: recoverUnchanged(reason), reason: reason}
	}

	seams.blockAt("pre-commit")

	if err := tx.Commit(); err != nil {
		// The rename may or may not have landed: decide by the installed
		// bytes. Old bytes still installed restart the unchanged build;
		// candidate bytes installed restore the previous build.
		seams.milestone("commit failed: %v\n", err)
		_ = tx.RecordError(fmt.Sprintf("commit: %v", err))
		if got, derr := selfupdate.DigestFile(r.boot.selfUpdateExec.Path); derr == nil && got == tx.Receipt().OldDigest {
			reason := fmt.Sprintf("commit failed before replacement: %v", err)
			return replacementTailResult{exitCode: recoverUnchanged(reason), reason: reason}
		}
		reason := fmt.Sprintf("commit failed after replacement: %v", err)
		return replacementTailResult{exitCode: recoverPrevious(reason), reason: reason}
	}
	seams.milestone("committed %s\n", tx.Receipt().TransactionID)

	if seams.failPostRename != nil && seams.failPostRename() {
		// The target is never invoked: recover through the boundary —
		// restore the previous build and exec it with the chain guard.
		reason := "injected post-rename failure: target never invoked"
		seams.milestone("post-rename failure (injected)\n")
		return replacementTailResult{exitCode: recoverPrevious(reason), reason: reason}
	}
	seams.blockAt("post-rename")

	if notify != nil {
		notify("restarting")
	}

	meta := selfupdate.HandoffMetadata{
		SchemaVersion:  1,
		TransactionID:  tx.Receipt().TransactionID,
		RuntimeDir:     r.boot.runtime.RuntimeDir,
		StateDir:       r.boot.runtime.StateDir,
		Config:         r.boot.runtime.Config,
		FromPID:        os.Getpid(),
		LeaseFD:        r.boot.updateLease.FD(),
		LeasePath:      r.boot.updateLease.Path(),
		ExecutablePath: r.boot.selfUpdateExec.Path,
		NewDigest:      tx.Receipt().NewDigest,
		FromVersion:    buildinfo.Version(),
		ToVersion:      tx.Receipt().ToVersion,
		Bind:           tx.Receipt().Bind,
		ReceiptPath:    selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		AuthTokenPath:  serverruntime.AuthTokenPath(r.boot.runtime.RuntimeDir),
	}
	seams.milestone("exec %s\n", r.boot.selfUpdateExec.Path)
	entry := selfupdate.HandoffEnvVar + "=" + selfupdate.EncodeHandoff(meta)
	execErr := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, seams.execFn)
	if execErr != nil {
		seams.milestone("exec failed: %v\n", execErr)
		reason := fmt.Sprintf("exec: %v", execErr)
		if seams.execFn != nil {
			reason = "injected exec failure: target never started"
		}
		return replacementTailResult{exitCode: recoverPrevious(reason), reason: reason}
	}
	// A successful exec never returns; this line only guards an ExecReplace
	// implementation that wrongly returns nil.
	return replacementTailResult{}
}
