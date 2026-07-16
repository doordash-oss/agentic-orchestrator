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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Harness verification progress rides the same feature-level surface the
// multi-axis validators use: CurrentPhaseStatus flags the substep and
// ValidatorStatuses carries per-command states, which the TUI renders in the
// detail status line and the live-preview banner.

const verifyingPhaseStatus = "verifying"

// beginVerificationStatuses marks the feature as running harness
// verification and seeds one pending entry per harness-owned command item.
func beginVerificationStatuses(store ports.FeatureStore, featureID string, contract *TestingContract) {
	if store == nil || contract == nil {
		return
	}
	statuses := make(map[string]string)
	for _, item := range contract.Items {
		if item.Owner != TestingContractOwnerHarness || item.Run == nil || strings.TrimSpace(item.Run.Shell) == "" {
			continue
		}
		if IsTestingContractItemWaived(item) {
			continue
		}
		statuses[item.Name] = "pending"
	}
	if len(statuses) == 0 {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		f.CurrentPhaseStatus = verifyingPhaseStatus
		f.ValidatorStatuses = statuses
		return nil
	})
}

// updateVerificationStatus records one command's progress state.
func updateVerificationStatus(store ports.FeatureStore, featureID, name, state string) {
	if store == nil || strings.TrimSpace(name) == "" {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		if f.ValidatorStatuses == nil {
			f.ValidatorStatuses = make(map[string]string)
		}
		f.ValidatorStatuses[name] = state
		return nil
	})
}

// clearVerificationStatuses removes the verification substep markers.
func clearVerificationStatuses(store ports.FeatureStore, featureID string) {
	if store == nil {
		return
	}
	_ = store.Modify(featureID, func(f *feature.Feature) error {
		if f.CurrentPhaseStatus == verifyingPhaseStatus {
			f.CurrentPhaseStatus = ""
		}
		f.ValidatorStatuses = nil
		return nil
	})
}
