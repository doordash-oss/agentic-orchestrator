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
	"fmt"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// The selfupdate driver hooks below are nil in ordinary builds and are wired
// only by the agentico_selfupdate_driver-tagged test driver. Activation is
// compile-time: in an ordinarily built binary no flag, environment value, or
// HTTP request can reach driver behavior.

// driverArgParseHook lets a tagged build recognize the selfupdate-driver
// subcommand. handled is false (with a nil error) for any other argument
// vector so parseLaunchArgs continues its ordinary parsing; a non-nil error
// is a recognized-but-invalid driver invocation rendered through the normal
// invalid-usage path. Supplied only by the agentico_selfupdate_driver build;
// nil in ordinary builds.
var driverArgParseHook func(args []string) (opts launchOptions, handled bool, err error)

// handoffAdoptHook lets a tagged build validate and adopt an inherited
// handoff from the environment before general bootstrap. It returns
// (nil, zero, zero, nil) when no handoff entry is present. Supplied only by
// the agentico_selfupdate_driver build; nil in ordinary builds.
var handoffAdoptHook func() (adopted *selfupdate.Lease, meta selfupdate.HandoffMetadata, receipt selfupdate.Receipt, err error)

// handoffListenOverrideHook lets a tagged build rebind the handoff's
// recorded endpoint. It returns "host:port" to override argv's --listen, or
// "" to keep it. Supplied only by the agentico_selfupdate_driver build; nil
// in ordinary builds.
var handoffListenOverrideHook func() string

// serverJourneyHook lets a tagged build run the old-image replace journey
// or the new-image confirm journey while the server is fully up; nil means
// ordinary serving. Supplied only by the agentico_selfupdate_driver build;
// nil in ordinary builds.
var serverJourneyHook func() serverJourney

// publishDiscoveryFn is the discovery publish seam (the real function by
// default) so the tagged driver can inject a publication failure.
var publishDiscoveryFn = serverruntime.PublishDiscovery

// serverJourney is one driver-owned lifecycle executed while the server is
// fully up: boot completed, discovery published, HTTP server listening.
type serverJourney interface {
	// run executes while the server is fully up. It returns an exit code, or
	// -1 to fall through and keep serving until shutdown.
	run(r serverRun) int
}

// serverRun hands one fully-booted server to a serverJourney. It carries the
// bootstrap, the live server handle, and — for the confirm journey — the
// adopted handoff state validated before bootstrap.
type serverRun struct {
	boot           *runtimeBootstrap
	server         *serverruntime.RuntimeServer
	authToken      string
	resolvedName   string
	policy         serverruntime.LaunchPolicy
	registryDir    string
	listen         serverruntime.ListenResolution
	adoptedLease   *selfupdate.Lease
	adoptedHandoff selfupdate.HandoffMetadata
	adoptedReceipt selfupdate.Receipt
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
