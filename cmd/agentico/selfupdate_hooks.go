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
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// runtimeGOOS/runtimeGOARCH are the platform signals of the running binary,
// split out so classification tests can pin them.
func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// updatesBuildInfoVersion and updatesInjectedVersion are the version signals
// feeding eligibility classification, split out for the same reason.
var (
	updatesBuildInfoVersion = buildInfoMainVersion
	updatesInjectedVersion  = buildinfo.InjectedVersion
)

// The selfupdate driver hooks below are nil in ordinary builds and are wired
// only by the agentico_selfupdate_driver-tagged test driver. Activation is
// compile-time: in an ordinarily built binary no flag, environment value, or
// HTTP request can reach driver behavior. Handoff adoption, endpoint
// rebinding, boot-time recovery resolution, the recovery boundary, and
// confirmation are production behavior; the driver only injects failures
// and barriers through the seams below.

// driverArgParseHook lets a tagged build recognize the selfupdate-driver
// subcommand. handled is false (with a nil error) for any other argument
// vector so parseLaunchArgs continues its ordinary parsing; a non-nil error
// is a recognized-but-invalid driver invocation rendered through the normal
// invalid-usage path. Supplied only by the agentico_selfupdate_driver build;
// nil in ordinary builds.
var driverArgParseHook func(args []string) (opts launchOptions, handled bool, err error)

// handoffAdoptHook lets a tagged build validate and adopt an inherited
// handoff from the environment before general bootstrap, with driver
// failure injection. It returns (nil, zero, zero, nil) when no handoff entry
// is present. When nil (ordinary builds), adoptHandoffForLaunch performs the
// same production adoption without injection.
var handoffAdoptHook func() (adopted *selfupdate.Lease, meta selfupdate.HandoffMetadata, receipt selfupdate.Receipt, err error)

// startupDeadlineHook lets a tagged build shrink the adopted-image startup
// deadline to force cooperative deadline expiry. Nil in ordinary builds.
var startupDeadlineHook func() time.Duration

// selfUpdateHealthWaitFn lets a tagged build inject the adopted image's
// self-health wait failure. Nil in ordinary builds.
var selfUpdateHealthWaitFn func(urls []string) error

// selfUpdateConfirmFn lets a tagged build inject the confirmation failure.
// Nil in ordinary builds: production always runs the real write-once
// confirm.
var selfUpdateConfirmFn func(execPath, txID string) (selfupdate.Receipt, error)

// selfUpdateFileOps lets a tagged build inject recovery persistence
// failures and deterministic barriers. The zero value runs the real
// implementation for every seam.
var selfUpdateFileOps selfupdate.FileOps

// selfUpdateRecoveryBarrier lets a tagged build block production recovery at
// deterministic stages for kill journeys. Nil in ordinary builds.
var selfUpdateRecoveryBarrier func(stage string)

// selfUpdateRecoveryExecFn lets a tagged build inject the recovery exec
// failure. Nil in ordinary builds: production always performs the real
// exec(2).
var selfUpdateRecoveryExecFn selfupdate.ExecFunc

// selfUpdateStartupAbortHook lets a tagged build abort the startup of a
// recovered or restarted image deterministically (the recovered build's own
// startup failure terminates nonzero without another automatic exec). Nil in
// ordinary builds.
var selfUpdateStartupAbortHook func() error

// selfUpdateAdoptedStartHook lets a tagged build print its process
// fingerprints once an inherited handoff is adopted (the e2e harness
// compares them across the exec boundary). Nil in ordinary builds.
var selfUpdateAdoptedStartHook func()

// selfUpdateCleanupSeams lets a tagged build inject validated-cleanup
// failures (individual removals, directory sync). The zero value runs the
// real implementation for every seam.
var selfUpdateCleanupSeams selfupdate.CleanupSeams

// serverJourneyHook lets a tagged build run the old-image replace journey
// while the server is fully up; nil means ordinary serving. Supplied only by
// the agentico_selfupdate_driver build; nil in ordinary builds.
var serverJourneyHook func() serverJourney

// publishDiscoveryFn is the discovery publish seam (the real function by
// default) so the tagged driver can inject a publication failure.
var publishDiscoveryFn = serverruntime.PublishDiscovery

// updateFeedHook lets a tagged build route the release-availability feed to a
// local test fixture. Nil in ordinary builds: production always uses the
// fixed production feed configuration, and no flag or environment value of
// an ordinarily built binary can redirect it or send credentials anywhere
// but api.github.com.
var updateFeedHook func() serverruntime.FeedChecker

// updateStopWorkTimeoutHook lets a tagged build shorten the explicit-stop
// dispatch/confirmation budget for deterministic timeout journeys. Nil in
// ordinary builds: production always uses the fixed ten-second budget.
var updateStopWorkTimeoutHook func() time.Duration

// updateStopEntryGateHook lets a tagged build park an explicit-stop install
// between staging and the post-staging protected-work recheck, for deterministic
// cancellation-versus-stop-entry and blocker-injection journeys. Nil in
// ordinary builds: production installs never pause there.
var updateStopEntryGateHook func() func(context.Context)

// updateStopFeatureFailureHook lets a tagged build inject a failure in front
// of a chosen feature stop dispatch, for deterministic partial-stop
// journeys. Nil in ordinary builds: production dispatches every stop for
// real.
var updateStopFeatureFailureHook func() func(featureID string) error

// updateStopDetectionFailHook lets a tagged build arm a detection-failure
// seam that makes the feature-activity detector fail once a trigger file
// exists, for deterministic detection-failure journeys before and during
// stop confirmation. Nil in ordinary builds: production detection always
// probes the real feature store.
var updateStopDetectionFailHook func() func() error

// classifyRuntimeEligibility gathers the real classification signals for the
// running server — captured executable identity, lease state (held, live
// contention, or other failure), build versions, platform, and file
// replaceability — and runs the pure classifier.
func classifyRuntimeEligibility(boot *runtimeBootstrap) selfupdate.Eligibility {
	exec := boot.selfUpdateExec
	leaseState := selfupdate.LeaseStateFromError(boot.updateLease != nil, boot.updateLeaseErr)
	if exec.Path == "" {
		return selfupdate.ClassifyInstallation(selfupdate.ClassifyInputs{Lease: leaseState})
	}
	binaryDir := filepath.Dir(exec.Path)
	return selfupdate.ClassifyInstallation(selfupdate.ClassifyInputs{
		ExecPath:         exec.Path,
		HasExec:          true,
		BinaryDir:        binaryDir,
		GoBinDir:         normalizeDir(resolveGoBinDir(os.Getenv, os.UserHomeDir)),
		BuildInfoVersion: updatesBuildInfoVersion(),
		InjectedVersion:  updatesInjectedVersion(),
		GOOS:             runtimeGOOS(),
		GOARCH:           runtimeGOARCH(),
		EUID:             os.Geteuid(),
		FileUID:          exec.ID.UID,
		FileMode:         exec.ID.Mode,
		DirWritable:      selfupdate.DirWritable(binaryDir),
		Lease:            leaseState,
	})
}

// serverJourney is one driver-owned lifecycle executed while the server is
// fully up: boot completed, discovery published, HTTP server listening.
type serverJourney interface {
	// run executes while the server is fully up. It returns an exit code, or
	// -1 to fall through and keep serving until shutdown.
	run(r serverRun) int
}

// serverRun hands one fully-booted server to a serverJourney. It carries the
// bootstrap, live server handle, and registry entry.
type serverRun struct {
	boot        *runtimeBootstrap
	server      *serverruntime.RuntimeServer
	authToken   string
	registryDir string
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

// adoptHandoffForLaunch validates and adopts an inherited handoff before
// general bootstrap — production behavior in every build, with the tagged
// driver's hook adding compile-time failure injection. No handoff entry
// means an ordinary launch. A present-but-unparseable entry fails closed: an
// arbitrary record must never authorize adoption.
func adoptHandoffForLaunch() (*selfupdate.Lease, selfupdate.HandoffMetadata, selfupdate.Receipt, error) {
	if handoffAdoptHook != nil {
		return handoffAdoptHook()
	}
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
	// Consumed: children and a later replacement must not inherit it.
	_ = os.Unsetenv(selfupdate.HandoffEnvVar)
	return lease, m, receipt, nil
}

// handoffListenAddr renders the rebind address from handoff metadata — the
// concrete bound host (wildcard form preserved) and assigned port.
func handoffListenAddr(m selfupdate.HandoffMetadata) string {
	if m.Bind.Host == "" || m.Bind.Port <= 0 {
		return ""
	}
	return net.JoinHostPort(m.Bind.Host, strconv.Itoa(m.Bind.Port))
}

// liveHandoffError reports a rejected launch during a live handoff: another
// live process holds the executable's update lease with a pending
// transaction, so a competing launch must wait until it completes.
type liveHandoffError struct {
	receipt selfupdate.Receipt
}

func (e *liveHandoffError) Error() string {
	return fmt.Sprintf("update handoff in progress for this executable; try again after it completes (transaction %s, phase %s)", e.receipt.TransactionID, e.receipt.Phase)
}

// checkLiveHandoff returns *liveHandoffError when selfupdate.LiveHandoff
// reports that another live process owns a pending transaction for execPath.
// execPath == "" never probes, and an unopenable lease file is tolerated
// (nil): a failed probe must never block an otherwise valid launch.
func checkLiveHandoff(execPath string) error {
	if execPath == "" {
		return nil
	}
	live, receipt, err := selfupdate.LiveHandoff(execPath)
	if err != nil {
		return nil
	}
	if live {
		return &liveHandoffError{receipt: receipt}
	}
	return nil
}
