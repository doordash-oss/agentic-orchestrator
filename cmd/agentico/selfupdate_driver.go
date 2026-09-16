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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// This file is the compile-time-isolated selfupdate test driver. It exists
// only in agentico_selfupdate_driver builds: an ordinarily built binary has
// no driver code at all, so no flag, environment value, or HTTP request can
// activate any journey below.

// cliSubcommandSelfUpdateDriver is the driver-only first argument.
const cliSubcommandSelfUpdateDriver = "selfupdate-driver"

// driverConfig carries the driver-only controls parsed from argv plus the
// handoff state stashed by the adoption hook. serverFlags is the parsed
// server-shaped launch options; adopted/adoptedMeta/adoptedReceipt mirror
// what the adoption hook returned to runServerWithJourney.
type driverConfig struct {
	candidatePath  string
	triggerFile    string
	failAt         string
	prepareOnly    bool
	serverFlags    launchOptions
	adopted        *selfupdate.Lease
	adoptedMeta    selfupdate.HandoffMetadata
	adoptedReceipt selfupdate.Receipt
}

// driver is the driver's process-lifetime state.
var driver driverConfig

// driverFailPoints is the closed set of --fail-at injection points.
var driverFailPoints = map[string]bool{
	"backup-sync":    true,
	"receipt-write":  true,
	"drain":          true,
	"stop":           true,
	"post-rename":    true,
	"exec-error":     true,
	"target-startup": true,
	"discovery":      true,
	"confirm":        true,
}

func init() {
	driverArgParseHook = parseDriverArgs
	handoffAdoptHook = adoptDriverHandoff
	handoffListenOverrideHook = driverListenOverride
	serverJourneyHook = driverJourney
	// The discovery publish stub is armed by adoptDriverHandoff, not here:
	// --fail-at discovery must fail the ADOPTED image's publication, never
	// the old image's own boot.
}

// parseDriverArgs recognizes the selfupdate-driver subcommand and parses the
// same flags `agentico server` accepts plus the driver-only controls.
// handled is false (with a nil error) for any other argument vector so
// parseLaunchArgs continues its ordinary parsing; a non-nil error is a
// recognized-but-invalid driver invocation that renders through the normal
// invalid-usage path. A candidate is deliberately not required at parse
// time: a handoff-env launch (the confirm journey) carries no candidate, so
// the replace journey validates it instead.
func parseDriverArgs(args []string) (launchOptions, bool, error) {
	if len(args) == 0 || args[0] != cliSubcommandSelfUpdateDriver {
		return launchOptions{}, false, nil
	}
	opts := defaultLaunchOptions()
	opts.mode = launchModeServer
	driver.candidatePath = ""
	driver.triggerFile = ""
	driver.failAt = ""
	driver.prepareOnly = false
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch arg {
		case "--config":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--config requires a value")
			}
			i++
			opts.configPath = rest[i]
		case "--state-dir":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--state-dir requires a value")
			}
			i++
			opts.stateDir = rest[i]
		case "--dangerously-skip-permissions":
			opts.dangerouslySkipPerms = true
		case "--providers":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--providers requires a value")
			}
			i++
			opts.enabledProviders = strings.Split(rest[i], ",")
		case "--refresh-models":
			opts.refreshModels = true
		case "--listen":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--listen requires a value")
			}
			i++
			opts.listenAddr = rest[i]
		case "--name":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--name requires a value")
			}
			i++
			opts.serverName = rest[i]
		case "--help", "-h":
			opts.mode = launchModeHelp
			driver.serverFlags = opts
			return opts, true, nil
		case "--version", "-v":
			opts.mode = launchModeVersion
			driver.serverFlags = opts
			return opts, true, nil
		case "--candidate":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--candidate requires a value")
			}
			i++
			driver.candidatePath = rest[i]
		case "--trigger-file":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--trigger-file requires a value")
			}
			i++
			driver.triggerFile = rest[i]
		case "--fail-at":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--fail-at requires a value")
			}
			i++
			point := rest[i]
			if !driverFailPoints[point] {
				return opts, true, fmt.Errorf("unknown --fail-at point: %s", point)
			}
			driver.failAt = point
		case "--prepare-only":
			driver.prepareOnly = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, true, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, true, fmt.Errorf("unknown selfupdate-driver argument: %s", arg)
		}
	}
	// The same parse-time normalization the server path applies, so bad
	// values fail before any socket is opened.
	if opts.listenAddr != "" {
		resolved, err := serverruntime.ResolveListenAddr(opts.listenAddr)
		if err != nil {
			return opts, true, err
		}
		opts.listenAddr = resolved
	}
	if name := strings.TrimSpace(opts.serverName); name != "" {
		if err := serverruntime.ValidateServerName(name); err != nil {
			return opts, true, fmt.Errorf("invalid --name value: %w", err)
		}
		opts.serverName = name
	}
	driver.serverFlags = opts
	return opts, true, nil
}

// armDiscoveryFailure installs the injected discovery-publish failure. It is
// armed only on the ADOPTED (new) image: the old image must still publish its
// own discovery record normally, and the failure must land on the target's
// post-exec publication.
func armDiscoveryFailure() {
	publishDiscoveryFn = func(string, serverruntime.DiscoveryRecord) error {
		return errors.New("injected discovery publish failure")
	}
}

// handoffEnvPresent reports whether the handoff entry exists in env,
// regardless of parseability.
func handoffEnvPresent(env []string) bool {
	prefix := selfupdate.HandoffEnvVar + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

// adoptDriverHandoff validates and adopts an inherited handoff before
// general bootstrap. No handoff entry means an ordinary driver launch:
// nils with a nil error. A present-but-unparseable entry fails closed — an
// arbitrary record must never authorize adoption.
func adoptDriverHandoff() (*selfupdate.Lease, selfupdate.HandoffMetadata, selfupdate.Receipt, error) {
	env := os.Environ()
	if !handoffEnvPresent(env) {
		return nil, selfupdate.HandoffMetadata{}, selfupdate.Receipt{}, nil
	}
	m, ok := selfupdate.ParseHandoffEnv(env)
	if !ok {
		return nil, m, selfupdate.Receipt{}, errors.New("selfupdate handoff entry is present but unparseable")
	}
	exec, err := selfupdate.CaptureExecutable()
	if err != nil {
		return nil, m, selfupdate.Receipt{}, err
	}
	lease, receipt, err := selfupdate.AdoptHandoff(m, exec)
	if err != nil {
		return nil, m, selfupdate.Receipt{}, err
	}
	if driver.failAt == "target-startup" {
		// The new image booted but must not confirm or serve: release the
		// adopted lease so the pending receipt and backup stay actionable.
		_ = lease.Close()
		return nil, m, selfupdate.Receipt{}, errors.New("injected target-startup failure")
	}
	driver.adopted = lease
	driver.adoptedMeta = m
	driver.adoptedReceipt = receipt
	if driver.failAt == "discovery" {
		armDiscoveryFailure()
	}
	return lease, m, receipt, nil
}

// driverListenOverride returns the rebind address recorded in the adopted
// handoff — the concrete bound host (wildcard form preserved) and assigned
// port — or "" when nothing was adopted, keeping argv's --listen.
func driverListenOverride() string {
	if driver.adopted == nil {
		return ""
	}
	return handoffListenAddr(driver.adoptedMeta)
}

// handoffListenAddr renders the rebind address from handoff metadata.
func handoffListenAddr(m selfupdate.HandoffMetadata) string {
	return net.JoinHostPort(m.Bind.Host, strconv.Itoa(m.Bind.Port))
}

// driverJourney selects the journey: the confirm journey when this process
// adopted a handoff, else the replace journey when a candidate was
// configured, else none — a tagged binary run as a plain server behaves
// like an ordinary server.
func driverJourney() serverJourney {
	if driver.adopted != nil {
		return confirmJourney{}
	}
	if driver.candidatePath != "" {
		return replaceJourney{}
	}
	return nil
}

// seamsForFailAt returns transaction seams that inject the requested
// preparation failure. "backup-sync" fails Begin's first SyncFile call —
// the rollback backup's sync — while later syncs (staging) still run so the
// failure is attributable to the backup alone. "receipt-write" fails the
// pending receipt write. Every other point needs no seam.
func seamsForFailAt(failAt string) selfupdate.TxSeams {
	switch failAt {
	case "backup-sync":
		calls := 0
		return selfupdate.TxSeams{SyncFile: func(path string) error {
			calls++
			if calls == 1 {
				return errors.New("injected backup sync failure")
			}
			return selfupdate.SyncFile(path)
		}}
	case "receipt-write":
		return selfupdate.TxSeams{WriteReceipt: func(string, selfupdate.Receipt) error {
			return errors.New("injected receipt write failure")
		}}
	default:
		return selfupdate.TxSeams{}
	}
}

// waitForTriggerFile polls for path's existence every 25ms until deadline.
// It never blocks unbounded: a missing file at the deadline is an error.
func waitForTriggerFile(path string, deadline time.Duration) error {
	if path == "" {
		return errors.New("no trigger file configured")
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(deadline)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ticker.C:
		case <-timeout:
			return fmt.Errorf("trigger file %s not present within %s", path, deadline)
		}
	}
}

// candidateVersion derives the candidate's version by running
// `<candidate> --version` and parsing the "agentico v<version>" banner
// (buildinfo.VersionLine format). Any failure falls back to "unknown"; a
// version probe must never block the transaction.
func candidateVersion(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return parseAgenticoVersion(string(out))
}

// parseAgenticoVersion extracts the version token from an "agentico
// v<version>" banner line, ignoring a trailing "(revision ...)" suffix.
func parseAgenticoVersion(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "agentico v"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				return fields[0]
			}
		}
	}
	return "unknown"
}

// printDriverFingerprints prints the args/env digests the e2e harness
// compares across the exec boundary. The env digest excludes the handoff
// entry, so the pre-exec and post-exec application environments must match.
func printDriverFingerprints(w io.Writer) {
	fmt.Fprintf(w, "selfupdate-driver: args-digest %s\n", argsDigest(os.Args))
	fmt.Fprintf(w, "selfupdate-driver: env-digest %s\n", envDigest(os.Environ()))
}

// argsDigest digests the full argument vector, NUL-separated.
func argsDigest(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])
}

// envDigest digests the environment entries, NUL-separated, excluding the
// handoff entry: it is the one entry exec is allowed to add.
func envDigest(env []string) string {
	h := sha256.New()
	prefix := selfupdate.HandoffEnvVar + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		h.Write([]byte(entry))
		h.Write([]byte("\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// replaceJourney is the old-image journey: prepare a durable replacement,
// shut down orderly, commit, and exec onto the newly installed binary.
type replaceJourney struct{}

func (replaceJourney) run(r serverRun) int {
	printDriverFingerprints(os.Stderr)

	// Ownership precondition: a secondary runtime (no lease) can never begin
	// a transaction, and a record that does not bind this executable and
	// runtime is stale, not authoritative.
	if r.boot.updateLease == nil {
		fmt.Fprintln(os.Stderr, "selfupdate-driver: no update ownership")
		return 1
	}
	if err := selfupdate.ValidateOwnershipRecord(r.boot.updateLease.Record(), r.boot.selfUpdateExec, r.boot.runtime.RuntimeDir); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: ownership validation failed: %v\n", err)
		return 1
	}

	// Per-runtime serialization of update work, separate from the instance
	// lock and the binary lease.
	rlock, err := selfupdate.AcquireRuntimeUpdateLock(r.boot.runtime.RuntimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: runtime update lock unavailable: %v\n", err)
		return 1
	}
	defer func() { _ = rlock.Close() }()

	if err := waitForTriggerFile(driver.triggerFile, 120*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: trigger wait failed: %v\n", err)
		return 1
	}

	digest, err := selfupdate.DigestFile(driver.candidatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: candidate digest failed: %v\n", err)
		return 1
	}

	// Bind endpoint from the LIVE server: the concrete bound address (wildcard
	// form preserved) and assigned port, not the argv flag.
	bind := selfupdate.BindEndpoint{
		Host:         r.server.BindHost(),
		Port:         r.server.Port(),
		Wildcard:     r.server.WildcardBind(),
		AdvertiseURL: r.server.BaseURL(),
		Policy:       r.server.RuntimePolicy(),
	}
	toVersion := candidateVersion(driver.candidatePath)

	tx, err := selfupdate.Begin(r.boot.selfUpdateExec, selfupdate.BeginOptions{
		RuntimeDir:      r.boot.runtime.RuntimeDir,
		StateDir:        r.boot.runtime.StateDir,
		Config:          r.boot.runtime.Config,
		PID:             os.Getpid(),
		PGID:            r.boot.owner.PGID,
		FromVersion:     buildinfo.Version(),
		ToVersion:       toVersion,
		CandidatePath:   driver.candidatePath,
		CandidateDigest: digest,
		Bind:            bind,
		ReceiptDest:     selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
	}, seamsForFailAt(driver.failAt))
	if err != nil {
		// Injected preparation failures (backup-sync, receipt-write) land
		// here: the installed bytes were never touched.
		fmt.Fprintf(os.Stderr, "selfupdate-driver: begin failed: %v\n", err)
		return 1
	}
	txid := tx.Receipt().TransactionID
	_ = r.boot.updateLease.SetTransactionID(txid)

	if driver.prepareOnly {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: prepared %s\n", txid)
		shutdownSequence(r)
		return 0
	}

	// Drain and stop before any installed-path mutation. The executable is
	// never replaced unless all of these succeed.
	shutdownFeatures(r.boot.orchestrator, r.boot.sessionManager)
	if driver.failAt == "drain" {
		_ = tx.RecordError("injected drain failure")
		fmt.Fprintln(os.Stderr, "selfupdate-driver: drain failed (injected)")
		shutdownSequence(r)
		return 1
	}
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDrain()
	if err := r.server.Close(drainCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		_ = tx.RecordError(fmt.Sprintf("server shutdown: %v", err), r.authToken)
		fmt.Fprintf(os.Stderr, "selfupdate-driver: server shutdown failed: %v\n", err)
		shutdownSequence(r)
		return 1
	}
	// The graceful phase may end at its deadline when never-idle SSE streams
	// are open; RuntimeServer.Close then force-terminates them, so draining
	// still completed within the budget. Only real failures abort above.
	if driver.failAt == "stop" {
		_ = tx.RecordError("injected stop failure")
		fmt.Fprintln(os.Stderr, "selfupdate-driver: stop failed (injected)")
		shutdownSequence(r)
		return 1
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	if err := r.boot.StopServices(stopCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		_ = tx.RecordError(fmt.Sprintf("stop services: %v", err))
		fmt.Fprintf(os.Stderr, "selfupdate-driver: stop services failed: %v\n", err)
		return 1
	}
	// Idempotent release of the owned registry resource. The instance lock
	// is deliberately NOT released: it is close-on-exec and must stay held
	// until the exec boundary so no competing runtime can slip in.
	_ = serverruntime.RemoveRegistryEntry(r.registryDir, r.boot.runtime.RuntimeDir)

	if err := tx.Commit(); err != nil {
		// A rename-succeeds-but-sync-fails case keeps a truthful backup-ready
		// receipt; selfupdate documents that honesty contract.
		_ = tx.RecordError(fmt.Sprintf("commit: %v", err))
		fmt.Fprintf(os.Stderr, "selfupdate-driver: commit failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: committed %s\n", txid)

	if driver.failAt == "post-rename" {
		// Pending receipt and backup are retained; the target is never invoked.
		fmt.Fprintln(os.Stderr, "selfupdate-driver: post-rename failure (injected)")
		return 1
	}

	meta := selfupdate.HandoffMetadata{
		SchemaVersion:  1,
		TransactionID:  txid,
		RuntimeDir:     r.boot.runtime.RuntimeDir,
		StateDir:       r.boot.runtime.StateDir,
		Config:         r.boot.runtime.Config,
		FromPID:        os.Getpid(),
		LeaseFD:        r.boot.updateLease.FD(),
		LeasePath:      r.boot.updateLease.Path(),
		ExecutablePath: r.boot.selfUpdateExec.Path,
		NewDigest:      tx.Receipt().NewDigest,
		FromVersion:    buildinfo.Version(),
		ToVersion:      toVersion,
		Bind:           bind,
		ReceiptPath:    selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		AuthTokenPath:  serverruntime.AuthTokenPath(r.boot.runtime.RuntimeDir),
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: exec %s\n", r.boot.selfUpdateExec.Path)

	entry := selfupdate.HandoffEnvVar + "=" + selfupdate.EncodeHandoff(meta)
	if driver.failAt == "exec-error" {
		// Simulate exec returning an error without calling exec: ExecReplace
		// clears CLOEXEC on the lease fd, the seam fails, and ExecReplace
		// restores CLOEXEC before returning.
		err := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, func(string, []string, []string) error {
			return errors.New("injected exec failure")
		})
		_ = tx.RecordError("injected exec failure")
		fmt.Fprintf(os.Stderr, "selfupdate-driver: exec failed: %v\n", err)
		return 1
	}
	if err := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, nil); err != nil {
		_ = tx.RecordError(fmt.Sprintf("exec: %v", err), r.authToken)
		fmt.Fprintf(os.Stderr, "selfupdate-driver: exec failed: %v\n", err)
		return 1
	}
	// A successful exec never returns; this line only guards a seam
	// implementation that wrongly returns nil.
	return 0
}

// confirmJourney is the new-image journey: confirm the adopted transaction
// durably, clean up the recognized transaction objects, and keep serving.
type confirmJourney struct{}

func (confirmJourney) run(r serverRun) int {
	printDriverFingerprints(os.Stderr)

	txid := r.adoptedHandoff.TransactionID
	if driver.failAt == "confirm" {
		// Never report confirmed from listening alone: the pending receipt
		// and backup stay actionable for a later retry.
		fmt.Fprintln(os.Stderr, "selfupdate-driver: confirm failed (injected)")
		shutdownSequence(r)
		return 1
	}

	// Re-read the receipt: confirmation is write-once against the exact
	// adopted transaction. A tampered or advanced receipt aborts without
	// touching anything.
	receipt, err := selfupdate.ReadReceipt(r.adoptedHandoff.ReceiptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: re-reading receipt failed: %v\n", err)
		shutdownSequence(r)
		return 1
	}
	if receipt.TransactionID != txid || receipt.Outcome != selfupdate.OutcomePending {
		fmt.Fprintln(os.Stderr, "selfupdate-driver: receipt no longer matches the adopted transaction")
		shutdownSequence(r)
		return 1
	}

	if _, err := selfupdate.ConfirmTransaction(r.adoptedHandoff.ExecutablePath, txid); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: confirm failed: %v\n", err)
		shutdownSequence(r)
		return 1
	}
	if err := selfupdate.CleanupTransaction(r.adoptedHandoff.ExecutablePath, txid); err != nil {
		// A cleanup failure after confirmation retains the healthy target
		// and truthful metadata: warn and keep serving.
		fmt.Fprintf(os.Stderr, "selfupdate-driver: cleanup failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "selfupdate-driver: confirmed %s\n", txid)
		return -1
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: confirmed %s\n", txid)
	fmt.Fprintln(os.Stderr, "selfupdate-driver: cleanup-done")
	return -1
}

// shutdownSequence performs the orderly pre-exec/abort shutdown the driver
// controls explicitly: drain features, close the HTTP server, stop the fx
// graph, and drop the registry entry. It never releases the instance lock —
// that stays held until the deferred runtimeBootstrap.Close on process
// exit — and never closes the update lease. server.Close, StopServices, and
// RemoveRegistryEntry are idempotent, so overlapping with the deferred
// cleanup in runServerWithJourney is safe.
func shutdownSequence(r serverRun) {
	shutdownFeatures(r.boot.orchestrator, r.boot.sessionManager)
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.server.Close(closeCtx); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: server close during shutdown: %v\n", err)
	}
	if err := r.boot.StopServices(closeCtx); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: stop services during shutdown: %v\n", err)
	}
	_ = serverruntime.RemoveRegistryEntry(r.registryDir, r.boot.runtime.RuntimeDir)
}
