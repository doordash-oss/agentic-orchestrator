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
		if _, err := selfupdate.ResolveAbandoned(exec, lease, plan, "install cancelled before replacement", selfupdate.RecoverySeams{}); err != nil {
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
// sequence — the same sequence the driver journeys prove, without the
// failure-injection seams.
type productionInstallLifecycle struct {
	// run is set once the server has started; install requests can only
	// arrive after that point.
	run *serverRun
	// stager carries the pipeline inputs for transaction preparation.
	stager *productionReleaseStager
	// exitCode carries a failed recovery boundary's termination request to
	// the serving goroutine, which owns the process exit: a second recovery
	// failure terminates without an exec loop and without os.Exit from this
	// goroutine.
	exitCode chan int
}

// AcquireUpdateLock takes the per-runtime update transaction lock.
func (l *productionInstallLifecycle) AcquireUpdateLock() (func(), bool) {
	lock, err := selfupdate.AcquireRuntimeUpdateLock(l.run.boot.runtime.RuntimeDir)
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
	r := l.run
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
		ReceiptDest: selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
	}, selfupdate.StageReleaseOptions{
		CurrentVersion: l.stager.currentVersion,
		Eligibility:    l.stager.eligibility,
	}, selfupdate.TxSeams{})
	if err != nil {
		return nil, err
	}
	return &productionInstallTransaction{run: r, tx: tx}, nil
}

// Replace runs the final drain, commit, and exec. The caller has already
// published draining and waited for the accepted response to complete; the
// notify callback receives restarting after commit. On success and on
// every recovery path it never returns. It returns a sanitized reason only
// when an aborted drain leaves the old build serving.
func (l *productionInstallLifecycle) Replace(handle serverruntime.InstallTransaction, notify func(status string), _ <-chan struct{}) error {
	tx, ok := handle.(*productionInstallTransaction)
	if !ok {
		return errors.New("unknown install transaction handle")
	}
	r := *l.run
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
	recoverUnchanged := func(reason string) {
		releaseUpdateLock()
		rt := failedTargetRecovery{
			boot:             r.boot,
			server:           r.server,
			registryDir:      r.registryDir,
			authToken:        r.authToken,
			exec:             r.boot.selfUpdateExec,
			lease:            r.boot.updateLease,
			receipt:          tx.tx.Receipt(),
			runtimeDir:       r.boot.runtime.RuntimeDir,
			reason:           reason,
			restartUnchanged: true,
			bind:             tx.tx.Receipt().Bind,
		}
		if code := rt.recover(); code != 0 {
			requestExit(code)
		}
	}
	recoverPrevious := func(reason string) {
		releaseUpdateLock()
		rt := failedTargetRecovery{
			boot:        r.boot,
			server:      r.server,
			registryDir: r.registryDir,
			authToken:   r.authToken,
			exec:        r.boot.selfUpdateExec,
			lease:       r.boot.updateLease,
			receipt:     tx.tx.Receipt(),
			runtimeDir:  r.boot.runtime.RuntimeDir,
			reason:      reason,
			bind:        tx.tx.Receipt().Bind,
		}
		if code := rt.recover(); code != 0 {
			requestExit(code)
		}
	}

	// Drain and stop before any installed-path mutation. The executable is
	// never replaced unless all of these succeed.
	shutdownFeatures(r.boot.orchestrator, r.boot.sessionManager)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDrain()
	closeErr := r.server.Close(drainCtx)
	if closeErr != nil && !tolerableShutdownDeadline(closeErr) {
		// An aborted drain keeps the old build usable only through the
		// unchanged-build restart; the sanitized reason surfaces if the
		// boundary instead leaves the process serving.
		reason := fmt.Sprintf("server shutdown failed: %v", closeErr)
		if settled, serr := selfupdate.ResolveAbandoned(r.boot.selfUpdateExec, r.boot.updateLease, selfupdate.RecoveryPlan{
			Receipt:     tx.tx.Receipt(),
			ReceiptPath: selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		}, reason, selfupdate.RecoverySeams{}); serr != nil {
			fmt.Fprintf(os.Stderr, "agentico: settling aborted install failed: %v\n", serr)
		} else {
			_ = settled
		}
		recoverUnchanged(reason)
		// recoverUnchanged never returns on success; if it did, the old
		// build is serving again and the failure is truthful.
		return fmt.Errorf("%s", selfupdate.SanitizeError(reason))
	}
	// Idempotent release of the owned registry resource. The instance lock
	// is deliberately NOT released: it is close-on-exec and must stay held
	// until the exec boundary so no competing runtime can slip in.
	_ = serverruntime.RemoveRegistryEntry(r.registryDir, r.boot.runtime.RuntimeDir)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	if err := r.boot.StopServices(stopCtx); err != nil && !tolerableShutdownDeadline(err) {
		reason := fmt.Sprintf("stop services failed: %v", err)
		recoverUnchanged(reason)
		return fmt.Errorf("%s", selfupdate.SanitizeError(reason))
	}

	if err := tx.tx.Commit(); err != nil {
		// The rename may or may not have landed: decide by the installed
		// bytes. Old bytes still installed restart the unchanged build;
		// candidate bytes installed restore the previous build.
		_ = tx.tx.RecordError(fmt.Sprintf("commit: %v", err))
		if got, derr := selfupdate.DigestFile(r.boot.selfUpdateExec.Path); derr == nil && got == tx.tx.Receipt().OldDigest {
			reason := fmt.Sprintf("commit failed before replacement: %v", err)
			recoverUnchanged(reason)
			return fmt.Errorf("%s", selfupdate.SanitizeError(reason))
		}
		reason := fmt.Sprintf("commit failed after replacement: %v", err)
		recoverPrevious(reason)
		return fmt.Errorf("%s", selfupdate.SanitizeError(reason))
	}

	if notify != nil {
		notify("restarting")
	}

	meta := selfupdate.HandoffMetadata{
		SchemaVersion:  1,
		TransactionID:  tx.ID(),
		RuntimeDir:     r.boot.runtime.RuntimeDir,
		StateDir:       r.boot.runtime.StateDir,
		Config:         r.boot.runtime.Config,
		FromPID:        os.Getpid(),
		LeaseFD:        r.boot.updateLease.FD(),
		LeasePath:      r.boot.updateLease.Path(),
		ExecutablePath: r.boot.selfUpdateExec.Path,
		NewDigest:      tx.tx.Receipt().NewDigest,
		FromVersion:    buildinfo.Version(),
		ToVersion:      tx.tx.Receipt().ToVersion,
		Bind:           tx.tx.Receipt().Bind,
		ReceiptPath:    selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		AuthTokenPath:  serverruntime.AuthTokenPath(r.boot.runtime.RuntimeDir),
	}
	entry := selfupdate.HandoffEnvVar + "=" + selfupdate.EncodeHandoff(meta)
	if err := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, nil); err != nil {
		reason := fmt.Sprintf("exec: %v", err)
		recoverPrevious(reason)
		return fmt.Errorf("%s", selfupdate.SanitizeError(reason))
	}
	// A successful exec never returns; this line only guards an ExecReplace
	// implementation that wrongly returns nil.
	return errors.New("the replacement returned without executing the target")
}
