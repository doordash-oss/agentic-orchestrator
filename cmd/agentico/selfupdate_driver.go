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
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// This file is the compile-time-isolated selfupdate test driver. It exists
// only in agentico_selfupdate_driver builds: an ordinarily built binary has
// no driver code at all, so no flag, environment value, or HTTP request can
// activate any journey below. The driver injects failures and barriers into
// the production recovery machinery through the seams declared in
// selfupdate_hooks.go; it never reimplements recovery.

// cliSubcommandSelfUpdateDriver is the driver-only first argument.
const cliSubcommandSelfUpdateDriver = "selfupdate-driver"

// driverConfig carries the driver-only controls parsed from argv plus the
// handoff state stashed by the adoption hook. serverFlags is the parsed
// server-shaped launch options.
type driverConfig struct {
	candidatePath string
	triggerFile   string
	failAt        map[string]bool
	barrier       string
	barrierFile   string
	prepareOnly   bool
	// updateFeedURL routes the release-availability feed to a local test
	// fixture. Empty keeps the fixed production feed. The fixture receives
	// no credentials. fixtureFeed is the validated client built at parse
	// time so an invalid fixture origin fails before any socket opens.
	updateFeedURL string
	fixtureFeed   *selfupdate.FeedClient
	// installRelease selects the release-backed journey: the driver resolves
	// a signed release from the fixture feed, verifies it end to end, stages
	// it through the updater-owned transaction machinery, probes it once,
	// and commits it. It can never be combined with --candidate: the
	// release-backed path only accepts verifier-produced provenance.
	installRelease bool
	serverFlags    launchOptions
}

// driver is the driver's process-lifetime state.
var driver driverConfig

// driverFailPoints is the closed set of --fail-at injection points. Points
// may be combined comma-separated so one journey can fail the target and
// then fail the recovered build.
var driverFailPoints = map[string]bool{
	// Preparation (Begin) failures: no receipt, untouched bytes.
	"backup-sync":   true,
	"receipt-write": true,
	// Aborted-shutdown failures on the old image. A failure while the old
	// runtime is still usable aborts the installation and keeps serving; a
	// failure after serving resources closed restarts the unchanged build
	// through the guarded recovery path.
	"drain":          true,
	"server-close":   true,
	"shutdown-mixed": true,
	"stop":           true,
	// Handoff failures: the old image recovers through the boundary —
	// restore the previous build and exec it with the chain guard.
	"post-rename": true,
	"exec-error":  true,
	// Target startup failures: the adopted image fails and the production
	// recovery boundary restores the previous build.
	"target-startup":   true,
	"startup-deadline": true,
	"discovery":        true,
	"health":           true,
	"confirm":          true,
	// Recovery failures: the boundary itself fails; the process exits
	// nonzero with actionable metadata retained and no second exec.
	"restore-attempt-write": true,
	"restore-rename":        true,
	"restore-sync":          true,
	"restore-rolledback":    true,
	"recovery-exec":         true,
	// The recovered build itself fails startup: nonzero exit, no loop.
	"recovered-startup": true,
	// Post-verification substitution on the release-backed path: the staged
	// candidate's bytes are replaced after the probe, and the commit-boundary
	// revalidation must refuse before replacement.
	"substitute-candidate": true,
	// Post-confirmation cleanup failure: warn and keep serving.
	"cleanup-remove": true,
	"cleanup-sync":   true,
}

// driverBarrierPoints is the closed set of --barrier stages. A barrier
// writes the barrier file and blocks forever so the harness can SIGKILL at
// a deterministic point.
var driverBarrierPoints = map[string]bool{
	"prepared":           true, // after Begin, before any shutdown
	"pre-commit":         true, // after teardown, before Commit
	"commit-receipt":     true, // inside Commit, after rename, before receipt advance
	"post-rename":        true, // after Commit, before handoff exec
	"release-staged":     true, // after release download+extraction, before probe or install transaction
	"restore-attempt":    true, // after the recovery attempt receipt is durable
	"restore-rename":     true, // after the restore rename, before dir sync
	"restore-sync":       true, // after the installed-dir sync, before rolled_back
	"restore-rolledback": true, // after rolled_back is durable, before exec
	"restart-marked":     true, // after the restart attempt is durable, before exec
}

// driverFailAt reports whether the named injection point is armed.
func (d *driverConfig) failAtSet(point string) bool {
	return d.failAt[point]
}

// driverArmed reports whether any fail point is armed.
func (d *driverConfig) anyFailArmed() bool {
	return len(d.failAt) > 0
}

func init() {
	driverArgParseHook = parseDriverArgs
	handoffAdoptHook = adoptDriverHandoff
	serverJourneyHook = driverJourney
	// The adopted image prints its fingerprints once adoption succeeds so
	// the e2e harness can compare args/env digests across the exec boundary.
	selfUpdateAdoptedStartHook = func() { printDriverFingerprints(os.Stderr) }
	// Fixture feed routing is armed only by the --update-feed driver flag
	// parsed above; ordinary builds keep the nil hook and the fixed
	// production feed.
	updateFeedHook = func() serverruntime.FeedChecker {
		if driver.fixtureFeed == nil {
			return nil
		}
		return driver.fixtureFeed
	}
}

// parseDriverArgs recognizes the selfupdate-driver subcommand and parses the
// same flags `agentico server` accepts plus the driver-only controls.
// handled is false (with a nil error) for any other argument vector so
// parseLaunchArgs continues its ordinary parsing; a non-nil error is a
// recognized-but-invalid driver invocation that renders through the normal
// invalid-usage path. A candidate is deliberately not required at parse
// time: a handoff-env or recovery launch carries no candidate.
func parseDriverArgs(args []string) (launchOptions, bool, error) {
	if len(args) == 0 || args[0] != cliSubcommandSelfUpdateDriver {
		return launchOptions{}, false, nil
	}
	opts := defaultLaunchOptions()
	opts.mode = launchModeServer
	driver.candidatePath = ""
	driver.triggerFile = ""
	driver.failAt = nil
	driver.barrier = ""
	driver.barrierFile = ""
	driver.prepareOnly = false
	driver.updateFeedURL = ""
	driver.fixtureFeed = nil
	driver.installRelease = false
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
		case "--updates":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--updates requires a value")
			}
			i++
			opts.updatesPolicy = rest[i]
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
			for _, point := range strings.Split(rest[i], ",") {
				point = strings.TrimSpace(point)
				if point == "" {
					continue
				}
				if !driverFailPoints[point] {
					return opts, true, fmt.Errorf("unknown --fail-at point: %s", point)
				}
				if driver.failAt == nil {
					driver.failAt = make(map[string]bool)
				}
				driver.failAt[point] = true
			}
		case "--barrier":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--barrier requires a value")
			}
			i++
			point := rest[i]
			if !driverBarrierPoints[point] {
				return opts, true, fmt.Errorf("unknown --barrier point: %s", point)
			}
			driver.barrier = point
		case "--barrier-file":
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--barrier-file requires a value")
			}
			i++
			driver.barrierFile = rest[i]
		case "--prepare-only":
			driver.prepareOnly = true
		case "--update-feed":
			// Driver-only fixture routing for availability and signed-release
			// journeys: a deliberately built test binary points the release
			// feed at a local fixture server. No credential is ever sent
			// there, and the constructor pins the one loopback origin.
			if i+1 >= len(rest) {
				return opts, true, fmt.Errorf("--update-feed requires a value")
			}
			i++
			driver.updateFeedURL = rest[i]
			if !strings.HasPrefix(driver.updateFeedURL, "http://127.0.0.1:") {
				return opts, true, fmt.Errorf("--update-feed only accepts a loopback http fixture URL")
			}
			client, err := selfupdate.NewFixtureFeedClient(driver.updateFeedURL, selfupdate.ProductionFeedSlug)
			if err != nil {
				return opts, true, fmt.Errorf("invalid --update-feed value: %w", err)
			}
			driver.fixtureFeed = client
		case "--install-release":
			// Driver-only release-backed journey selector. The signed release
			// path never accepts a caller-provided candidate: provenance is
			// produced only by the release verifier.
			driver.installRelease = true
		default:
			if strings.HasPrefix(arg, "--updates=") {
				opts.updatesPolicy = strings.TrimPrefix(arg, "--updates=")
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return opts, true, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, true, fmt.Errorf("unknown selfupdate-driver argument: %s", arg)
		}
	}
	if opts.updatesPolicy != "" {
		if _, ok := selfupdate.ParsePolicyValue(opts.updatesPolicy); !ok {
			return opts, true, fmt.Errorf("invalid --updates value %q: expected off, notify, or auto", opts.updatesPolicy)
		}
	}
	if driver.barrier != "" && driver.anyFailArmed() {
		return opts, true, fmt.Errorf("--barrier and --fail-at are mutually exclusive")
	}
	if driver.barrier != "" && driver.barrierFile == "" {
		return opts, true, fmt.Errorf("--barrier requires --barrier-file")
	}
	if driver.installRelease {
		if driver.fixtureFeed == nil {
			return opts, true, fmt.Errorf("--install-release requires --update-feed")
		}
		if driver.candidatePath != "" {
			return opts, true, fmt.Errorf("--install-release cannot be combined with --candidate: the release path only accepts verifier-produced provenance")
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
	armDriverSeams()
	driver.serverFlags = opts
	return opts, true, nil
}

// armDriverSeams wires the driver's failure injections and barriers into the
// production seams. Only the armed points are installed; everything else
// keeps the real implementation.
func armDriverSeams() {
	if driver.anyFailArmed() {
		if driver.failAtSet("target-startup") || driver.failAtSet("startup-deadline") {
			// Cooperative startup deadline expiry: the adopted target gets a
			// deadline that expires before bootstrap can complete, routing
			// through the recovery boundary.
			startupDeadlineHook = func() time.Duration { return time.Millisecond }
		}
		if driver.failAtSet("discovery") {
			// Armed only for the ADOPTED image: the old image must still
			// publish its own discovery record normally, and the failure
			// must land on the target's post-exec publication.
			armDiscoveryFailureOnAdoption()
		}
		if driver.failAtSet("health") {
			// Armed only for the ADOPTED image, like the discovery injection:
			// the recovered build's own mandatory health wait must still pass.
			armOnAdoption(func() {
				selfUpdateHealthWaitFn = func([]string) error {
					return errors.New("injected health wait failure")
				}
			})
		}
		if driver.failAtSet("confirm") {
			selfUpdateConfirmFn = func(string, string) (selfupdate.Receipt, error) {
				return selfupdate.Receipt{}, errors.New("injected confirm failure")
			}
		}
		if driver.failAtSet("recovery-exec") {
			selfUpdateRecoveryExecFn = func(string, []string, []string) error {
				return errors.New("injected recovery exec failure")
			}
		}
		if driver.failAtSet("recovered-startup") {
			// Abort the startup of the recovered or restarted image itself:
			// the chain guard forbids another automatic recovery, so the
			// process must terminate nonzero. The first image of the chain
			// carries no guard entry and is not affected.
			selfUpdateStartupAbortHook = func() error {
				if _, present, perr := selfupdate.ParseRecoveryGuardEnv(os.Environ()); present && perr == nil {
					return errors.New("injected recovered-startup failure")
				}
				return nil
			}
		}
		if driver.failAtSet("cleanup-remove") {
			selfUpdateCleanupSeams = selfupdate.CleanupSeams{
				Remove: func(string) error { return errors.New("injected cleanup remove failure") },
			}
		}
		if driver.failAtSet("cleanup-sync") {
			selfUpdateCleanupSeams = selfupdate.CleanupSeams{
				SyncDir: func(string) error { return errors.New("injected cleanup sync failure") },
			}
		}
		selfUpdateRecoverySeams = recoverySeamsForFailAt(driver.failAt)
	}
	if driver.barrier != "" {
		selfUpdateRecoveryBarrier = barrierHook
		selfUpdateRecoverySeams = recoverySeamsForBarrier(driver.barrier)
	}
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

// armDiscoveryFailureOnAdoption defers the discovery failure arming to the
// adoption hook so it never affects the old image's own boot.
func armDiscoveryFailureOnAdoption() {
	armOnAdoption(armDiscoveryFailure)
}

// armOnAdoption defers one failure arming to the adoption hook so an
// adopted-image injection never affects the old image's boot or the
// recovered build's own boot either.
func armOnAdoption(fn func()) {
	if driverArmOnAdoption == nil {
		driverArmOnAdoption = fn
		return
	}
	prev := driverArmOnAdoption
	driverArmOnAdoption = func() { prev(); fn() }
}

// driverArmOnAdoption is armed by armDriverSeams and invoked by
// adoptDriverHandoff once a handoff is adopted.
var driverArmOnAdoption func()

// barrierHook writes the barrier marker and blocks forever: the harness
// SIGKILLs the process at this deterministic stage.
func barrierHook(stage string) {
	if stage != driver.barrier {
		return
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: barrier %s\n", stage)
	_ = os.WriteFile(driver.barrierFile, []byte(stage), 0o644)
	select {}
}

// barrierSeam runs the real implementation, then writes the marker and
// blocks when the wrapped point is the armed barrier stage.
func barrierSeam(stage string, run func() error) error {
	err := run()
	barrierHook(stage)
	return err
}

// recoverySeamsForBarrier wraps the recovery seams so the armed barrier
// blocks at the matching deterministic stage inside the production
// restoration.
func recoverySeamsForBarrier(stage string) selfupdate.RecoverySeams {
	return selfupdate.RecoverySeams{
		WriteReceipt: func(path string, r selfupdate.Receipt) error {
			return barrierSeam(barrierStageForReceiptWrite(r), func() error {
				return selfupdate.WriteReceiptDurable(path, r)
			})
		},
		Rename: func(oldpath, newpath string) error {
			return barrierSeam("restore-rename", func() error {
				return os.Rename(oldpath, newpath)
			})
		},
		SyncDir: func(path string) error {
			stageForSync := "restore-sync"
			if selfupdate.IsTxDirPath(path) {
				stageForSync = "restore-prepared-sync"
			}
			return barrierSeam(stageForSync, func() error {
				return selfupdate.SyncDir(path)
			})
		},
		Copy: func(dst, src string, mode uint32) error {
			return selfupdate.CopyFile(dst, src, mode)
		},
		SyncFile: func(path string) error {
			return selfupdate.SyncFile(path)
		},
	}
}

// barrierStageForReceiptWrite maps one recovery receipt write to its
// barrier stage name.
func barrierStageForReceiptWrite(r selfupdate.Receipt) string {
	switch {
	case r.Outcome == selfupdate.OutcomeRolledBack:
		return "restore-rolledback"
	case r.RecoveryAttemptID != "" && r.RecoveryKind == selfupdate.RecoveryKindRestore:
		return "restore-attempt"
	default:
		return "restore-receipt-other"
	}
}

// recoverySeamsForFailAt builds the recovery failure seams for the armed
// injection points: each armed point fails exactly once at its boundary,
// leaving the durable receipt truthful for the furthest completed step.
func recoverySeamsForFailAt(points map[string]bool) selfupdate.RecoverySeams {
	seams := selfupdate.RecoverySeams{}
	if points["restore-attempt-write"] {
		seams.WriteReceipt = func(path string, r selfupdate.Receipt) error {
			if r.Outcome == selfupdate.OutcomePending && r.RecoveryAttemptID != "" && r.RecoveryKind == selfupdate.RecoveryKindRestore {
				return errors.New("injected recovery attempt write failure")
			}
			return selfupdate.WriteReceiptDurable(path, r)
		}
	}
	if points["restore-rename"] {
		seams.Rename = func(oldpath, newpath string) error {
			return errors.New("injected restore rename failure")
		}
	}
	if points["restore-sync"] {
		seams.SyncDir = func(path string) error {
			if selfupdate.IsTxDirPath(path) {
				return selfupdate.SyncDir(path)
			}
			return errors.New("injected restore dir sync failure")
		}
	}
	if points["restore-rolledback"] {
		seams.WriteReceipt = func(path string, r selfupdate.Receipt) error {
			if r.Outcome == selfupdate.OutcomeRolledBack {
				return errors.New("injected rolled-back receipt write failure")
			}
			return selfupdate.WriteReceiptDurable(path, r)
		}
	}
	return seams
}

// adoptDriverHandoff validates and adopts an inherited handoff before
// general bootstrap, with the driver's adoption-time injection. No handoff
// entry means an ordinary driver launch.
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
	if driverArmOnAdoption != nil {
		driverArmOnAdoption()
	}
	return lease, m, receipt, nil
}

// driverJourney selects the journey: the release-backed journey when
// --install-release was configured, the replace journey when a local
// candidate was configured, and otherwise the production lifecycle. An
// adopted image, a recovered or restarted image, and a plain tagged server
// all run the production lifecycle (adoption, recovery resolution,
// confirmation, serving); a recovered chain never re-attempts installation
// without fresh consent.
func driverJourney() serverJourney {
	env := os.Environ()
	if handoffEnvPresent(env) {
		return nil
	}
	if _, guardPresent, _ := selfupdate.ParseRecoveryGuardEnv(env); guardPresent {
		fmt.Fprintln(os.Stderr, "selfupdate-driver: refusing update journey in a recovery chain; fresh consent required")
		return nil
	}
	if driver.installRelease {
		return releaseJourney{}
	}
	if driver.candidatePath == "" {
		return nil
	}
	return replaceJourney{}
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
// compares across the exec boundary. The env digest excludes the handoff and
// recovery-guard entries, so the pre-exec and post-exec application
// environments must match.
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
// private handoff and recovery-guard entries: they are the only entries
// exec is allowed to replace.
func envDigest(env []string) string {
	h := sha256.New()
	handoffPrefix := selfupdate.HandoffEnvVar + "="
	guardPrefix := selfupdate.RecoveryGuardEnvVar + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, handoffPrefix) || strings.HasPrefix(entry, guardPrefix) {
			continue
		}
		h.Write([]byte(entry))
		h.Write([]byte("\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// replaceJourney is the old-image journey: prepare a durable replacement,
// shut down orderly, commit, and exec onto the newly installed binary — or,
// when a shutdown step fails, abort the installation (keeping the still
// usable runtime in service) or restart the unchanged build through the
// guarded recovery path.
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
	// lock and the binary lease. Released before any recovery boundary: the
	// boundary's restoration acquires the same lock itself.
	rlock, err := selfupdate.AcquireRuntimeUpdateLock(r.boot.runtime.RuntimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: runtime update lock unavailable: %v\n", err)
		return 1
	}
	defer func() { _ = rlock.Close() }()
	releaseUpdateLock := func() {
		if rlock != nil {
			_ = rlock.Close()
			rlock = nil
		}
	}

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

	return runReplacementTail(r, tx, toVersion, releaseUpdateLock)
}

// runReplacementTail carries one prepared transaction through the orderly
// shutdown, atomic commit, and exec handoff — the journey steps shared by the
// local-candidate replace journey and the release-backed journey. A return
// of -1 means the old runtime is still usable and keeps serving.
func runReplacementTail(r serverRun, tx *selfupdate.Transaction, toVersion string, releaseUpdateLock func()) int {
	txid := tx.Receipt().TransactionID
	bind := tx.Receipt().Bind
	planFor := func() selfupdate.RecoveryPlan {
		return selfupdate.RecoveryPlan{
			Receipt:     tx.Receipt(),
			ReceiptPath: selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
		}
	}
	if driver.prepareOnly {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: prepared %s\n", txid)
		driverBarrierJourney("prepared")
		shutdownSequence(r)
		return 0
	}
	driverBarrierJourney("prepared")

	// Draining fails while the old runtime is still usable: abort the
	// installation, settle the truthful installation failure, and keep
	// serving the unchanged build. No serving resource is closed, the
	// target is never executed, and the target is never suppressed.
	if driver.failAtSet("drain") {
		reason := "injected drain failure: installation aborted before any serving resource closed"
		settled, serr := selfupdate.ResolveAbandoned(r.boot.selfUpdateExec, r.boot.updateLease, planFor(), reason, selfupdate.RecoverySeams{})
		if serr != nil {
			fmt.Fprintf(os.Stderr, "selfupdate-driver: settling aborted installation failed: %v\n", serr)
			return 1
		}
		fmt.Fprintf(os.Stderr, "selfupdate-driver: installation aborted pre-replacement (%s); still serving\n", settled.TransactionID)
		fmt.Fprintln(os.Stderr, "selfupdate-driver: drain failed (injected)")
		return -1
	}

	// Drain and stop before any installed-path mutation. The executable is
	// never replaced unless all of these succeed.
	shutdownFeatures(r.boot.orchestrator, r.boot.sessionManager)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelDrain()
	closeErr := r.server.Close(drainCtx)
	// A graceful SSE deadline permits progress only when every joined error
	// is the cooperative deadline itself (RuntimeServer.Close force-terminates
	// the remaining streams); unrelated joined errors are never discarded
	// merely because they include a timeout.
	tolerable := tolerableShutdownDeadline(closeErr)
	if driver.failAtSet("server-close") {
		tolerable = false
		if closeErr == nil {
			closeErr = errors.New("injected server close failure")
		}
	}
	if driver.failAtSet("shutdown-mixed") {
		tolerable = false
		closeErr = errors.Join(closeErr, context.DeadlineExceeded, errors.New("injected unfinished stream closure"))
	}
	if closeErr != nil && !tolerable {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: server shutdown failed: %v\n", closeErr)
		releaseUpdateLock()
		return restartUnchangedBuild(r, planFor(), fmt.Sprintf("server shutdown failed: %v", closeErr), bind)
	}
	// Idempotent release of the owned registry resource. The instance lock
	// is deliberately NOT released: it is close-on-exec and must stay held
	// until the exec boundary so no competing runtime can slip in.
	_ = serverruntime.RemoveRegistryEntry(r.registryDir, r.boot.runtime.RuntimeDir)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStop()
	if err := r.boot.StopServices(stopCtx); err != nil && !tolerableShutdownDeadline(err) {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: stop services failed: %v\n", err)
		releaseUpdateLock()
		return restartUnchangedBuild(r, planFor(), fmt.Sprintf("stop services failed: %v", err), bind)
	}
	if driver.failAtSet("stop") {
		fmt.Fprintln(os.Stderr, "selfupdate-driver: stop failed (injected)")
		releaseUpdateLock()
		return restartUnchangedBuild(r, planFor(), "injected stop failure after serving resources closed", bind)
	}

	driverBarrierJourney("pre-commit")

	if err := tx.Commit(); err != nil {
		// The rename may or may not have landed: decide by the installed
		// bytes. Old bytes still installed restart the unchanged build;
		// candidate bytes installed restore the previous build.
		fmt.Fprintf(os.Stderr, "selfupdate-driver: commit failed: %v\n", err)
		_ = tx.RecordError(fmt.Sprintf("commit: %v", err))
		if got, derr := selfupdate.DigestFile(r.boot.selfUpdateExec.Path); derr == nil && got == tx.Receipt().OldDigest {
			releaseUpdateLock()
			return restartUnchangedBuild(r, planFor(), fmt.Sprintf("commit failed before replacement: %v", err), bind)
		}
		releaseUpdateLock()
		return recoverPreviousBuild(r, planFor(), fmt.Sprintf("commit failed after replacement: %v", err))
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: committed %s\n", txid)

	if driver.failAtSet("post-rename") {
		// The target is never invoked: recover through the boundary —
		// restore the previous build and exec it with the chain guard.
		fmt.Fprintln(os.Stderr, "selfupdate-driver: post-rename failure (injected)")
		releaseUpdateLock()
		return recoverPreviousBuild(r, planFor(), "injected post-rename failure: target never invoked")
	}
	driverBarrierJourney("post-rename")

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
	if driver.failAtSet("exec-error") {
		// Simulate exec returning an error without calling exec: ExecReplace
		// clears CLOEXEC on the lease fd, the seam fails, and ExecReplace
		// restores CLOEXEC before returning. The boundary then restores the
		// previous build and execs it with the guard.
		err := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, func(string, []string, []string) error {
			return errors.New("injected exec failure")
		})
		fmt.Fprintf(os.Stderr, "selfupdate-driver: exec failed: %v\n", err)
		releaseUpdateLock()
		return recoverPreviousBuild(r, planFor(), "injected exec failure: target never started")
	}
	if err := selfupdate.ExecReplace(r.boot.selfUpdateExec.Path, os.Args, os.Environ(), entry, meta.LeaseFD, nil); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: exec failed: %v\n", err)
		releaseUpdateLock()
		return recoverPreviousBuild(r, planFor(), fmt.Sprintf("exec: %v", err))
	}
	// A successful exec never returns; this line only guards a seam
	// implementation that wrongly returns nil.
	return 0
}

// releaseJourney is the old-image journey for a signed fixture release: the
// driver resolves the release from its fixture feed, verifies the signed
// checksum manifest (and any advertised envelope), stages the authenticated
// archive through updater-owned staging, probes the extracted executable
// once in isolation, and only then prepares and commits the transaction —
// the same verification, staging, transaction, exec, confirmation, and
// recovery implementations production will use. A failure at any step before
// shutdown keeps the old build installed and serving.
type releaseJourney struct{}

func (releaseJourney) run(r serverRun) int {
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

	rlock, err := selfupdate.AcquireRuntimeUpdateLock(r.boot.runtime.RuntimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: runtime update lock unavailable: %v\n", err)
		return 1
	}
	defer func() { _ = rlock.Close() }()
	releaseUpdateLock := func() {
		if rlock != nil {
			_ = rlock.Close()
			rlock = nil
		}
	}

	if driver.triggerFile != "" {
		if err := waitForTriggerFile(driver.triggerFile, 120*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "selfupdate-driver: trigger wait failed: %v\n", err)
			return 1
		}
	}

	ctx := context.Background()
	stageOpts := selfupdate.StageReleaseOptions{
		CurrentVersion: buildinfo.Version(),
		Eligibility:    classifyRuntimeEligibility(r.boot),
	}

	resolved, err := driver.fixtureFeed.ResolveLatestRelease(ctx, runtimeGOOS(), runtimeGOARCH())
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release resolve failed: %v\n", err)
		return -1
	}
	verified, err := driver.fixtureFeed.VerifyResolvedRelease(ctx, resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release verification failed: %v\n", err)
		return -1
	}
	staged, err := driver.fixtureFeed.StageVerifiedRelease(ctx, r.boot.selfUpdateExec, verified, stageOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release staging failed: %v\n", err)
		return -1
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: release staged %s (%s)\n", staged.TxID(), verified.Resolved().Version)
	driverBarrierJourney("release-staged")

	probe, err := selfupdate.ProbeStagedExecutable(ctx, staged.CandidatePath(), verified.Resolved().Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release probe failed: %v\n", err)
		return -1
	}

	// Post-verification substitution injection: the staged candidate's bytes
	// change after the probe. The verifier-produced provenance must refuse
	// the transaction before replacement.
	if driver.failAtSet("substitute-candidate") {
		if err := os.WriteFile(staged.CandidatePath(), append([]byte("#!/bin/sh\necho stolen\n"), 0), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "selfupdate-driver: substitution injection failed: %v\n", err)
			return 1
		}
	}

	cand, err := selfupdate.AdmitStagedRelease(staged, probe)
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release admission failed: %v\n", err)
		return -1
	}
	tx, err := selfupdate.BeginVerifiedRelease(r.boot.selfUpdateExec, cand, selfupdate.BeginOptions{
		RuntimeDir:  r.boot.runtime.RuntimeDir,
		StateDir:    r.boot.runtime.StateDir,
		Config:      r.boot.runtime.Config,
		PID:         os.Getpid(),
		PGID:        r.boot.owner.PGID,
		FromVersion: buildinfo.Version(),
		ToVersion:   verified.Resolved().Version,
		Bind: selfupdate.BindEndpoint{
			Host:         r.server.BindHost(),
			Port:         r.server.Port(),
			Wildcard:     r.server.WildcardBind(),
			AdvertiseURL: r.server.BaseURL(),
			Policy:       r.server.RuntimePolicy(),
		},
		ReceiptDest: selfupdate.ReceiptPath(r.boot.selfUpdateExec.Path),
	}, stageOpts, seamsForFailAt(driver.failAt))
	if err != nil {
		// Injected preparation failures and the substitution injection land
		// here: the installed bytes were never touched and the runtime keeps
		// serving the old build.
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release begin failed: %v\n", err)
		return -1
	}
	txid := tx.Receipt().TransactionID
	_ = r.boot.updateLease.SetTransactionID(txid)
	// The verified executable now lives in the transaction's own staging; the
	// release download staging is recognized owned abandoned staging and is
	// cleaned with full validation (a failure retries on a later launch).
	if err := selfupdate.CleanupSettledTransaction(r.boot.selfUpdateExec.Path, staged.TxID(), selfUpdateCleanupSeams); err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate-driver: release staging cleanup deferred: %v\n", err)
	}
	return runReplacementTail(r, tx, verified.Resolved().Version, releaseUpdateLock)
}

// driverBarrierJourney blocks at a journey-level barrier stage.
func driverBarrierJourney(stage string) {
	if driver.barrier == stage {
		barrierHook(stage)
	}
}

// restartUnchangedBuild returns an old runtime whose serving resources
// already closed to service: the unchanged executable is re-executed under
// the durable recovery-attempt tracking of the guarded recovery path — an
// installation failure, never a fictitious rollback.
func restartUnchangedBuild(r serverRun, plan selfupdate.RecoveryPlan, reason string, bind selfupdate.BindEndpoint) int {
	rt := failedTargetRecovery{
		boot:             r.boot,
		server:           r.server,
		registryDir:      r.registryDir,
		authToken:        r.authToken,
		exec:             r.boot.selfUpdateExec,
		lease:            r.boot.updateLease,
		receipt:          plan.Receipt,
		runtimeDir:       r.boot.runtime.RuntimeDir,
		reason:           reason,
		restartUnchanged: true,
		bind:             bind,
	}
	fmt.Fprintf(os.Stderr, "selfupdate-driver: restarting unchanged build after aborted shutdown\n")
	return rt.recover()
}

// recoverPreviousBuild runs the recovery boundary from the old image: the
// candidate is installed but the target failed before or at exec — restore
// the previous build and exec it with the chain guard.
func recoverPreviousBuild(r serverRun, plan selfupdate.RecoveryPlan, reason string) int {
	rt := failedTargetRecovery{
		boot:        r.boot,
		server:      r.server,
		registryDir: r.registryDir,
		authToken:   r.authToken,
		exec:        r.boot.selfUpdateExec,
		lease:       r.boot.updateLease,
		receipt:     plan.Receipt,
		runtimeDir:  r.boot.runtime.RuntimeDir,
		reason:      reason,
		bind:        plan.Receipt.Bind,
	}
	return rt.recover()
}

// seamsForFailAt returns transaction seams that inject the requested
// preparation failure. "backup-sync" fails Begin's first SyncFile call —
// the rollback backup's sync — while later syncs (staging) still run so the
// failure is attributable to the backup alone. "receipt-write" fails the
// pending receipt write. "commit-receipt" is a barrier, not a failure: it
// blocks inside Commit after the rename, before the receipt advances.
func seamsForFailAt(points map[string]bool) selfupdate.TxSeams {
	switch {
	case points["backup-sync"]:
		calls := 0
		return selfupdate.TxSeams{SyncFile: func(path string) error {
			calls++
			if calls == 1 {
				return errors.New("injected backup sync failure")
			}
			return selfupdate.SyncFile(path)
		}}
	case points["receipt-write"]:
		return selfupdate.TxSeams{WriteReceipt: func(string, selfupdate.Receipt) error {
			return errors.New("injected receipt write failure")
		}}
	default:
		if driver.barrier == "commit-receipt" {
			calls := 0
			return selfupdate.TxSeams{WriteReceipt: func(path string, r selfupdate.Receipt) error {
				calls++
				if calls >= 2 {
					// Begin wrote the pending receipt (call 1); this is
					// Commit's advancement after the rename.
					barrierHook("commit-receipt")
				}
				return selfupdate.WriteReceiptDurable(path, r)
			}}
		}
		return selfupdate.TxSeams{}
	}
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
