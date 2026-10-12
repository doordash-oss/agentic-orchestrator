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

package agent

import (
	"strings"
	"sync"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// SandboxStatus is the once-per-process result of functionally probing the
// OS sandbox that wraps verification commands and bounded helpers. The zero
// value means usable (or not applicable on this platform). Unavailable is
// reported only where a sandbox is expected but cannot be applied, e.g. a
// nested macOS seatbelt.
type SandboxStatus struct {
	Unavailable bool
	Reason      string
}

var sandboxProbe struct {
	once   sync.Once
	probe  func() SandboxStatus
	status SandboxStatus
}

// SandboxAvailability returns the cached sandbox probe result, running the
// platform probe on first use.
func SandboxAvailability() SandboxStatus {
	sandboxProbe.once.Do(func() {
		probe := sandboxProbe.probe
		if probe == nil {
			probe = platformSandboxProbe
		}
		sandboxProbe.status = probe()
	})
	return sandboxProbe.status
}

// setSandboxProbeForTest replaces the probe (nil restores the platform probe)
// and clears the cache so the next SandboxAvailability call re-probes.
func setSandboxProbeForTest(probe func() SandboxStatus) {
	sandboxProbe.once = sync.Once{}
	sandboxProbe.probe = probe
	sandboxProbe.status = SandboxStatus{}
}

// recordSandboxUnavailable persists the run-level note that this run's
// verification commands or review validators ran unconfined. The first
// record wins; later calls are no-ops.
func recordSandboxUnavailable(store FeatureStore, featureID, reason string) {
	if store == nil || strings.TrimSpace(featureID) == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		run := f.Run()
		if run.Sandbox == nil {
			run.Sandbox = &feature.SandboxRecord{Available: false, Reason: reason}
		}
		return nil
	})
}
