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

// Harness verification progress is its own presentation surface, distinct
// from the review validators: CurrentPhaseStatus flags the substep and
// VerificationItems carries ordered per-command states that the desktop app
// renders as a high-level execution log.

const verifyingPhaseStatus = "verifying"

// beginVerificationStatuses marks the feature as running harness
// verification and seeds one pending entry per harness-owned command item,
// preserving contract order.
func beginVerificationStatuses(store ports.FeatureStore, featureID string, contract *TestingContract, onProgress ...func(string)) {
	if store == nil || contract == nil {
		return
	}
	items := make([]feature.VerificationItemStatus, 0, len(contract.Items))
	for _, item := range contract.Items {
		if item.Owner != TestingContractOwnerHarness || item.Run == nil || strings.TrimSpace(item.Run.Shell) == "" {
			continue
		}
		if IsTestingContractItemWaived(item) {
			continue
		}
		items = append(items, feature.VerificationItemStatus{Name: item.Name, State: "pending"})
	}
	if len(items) == 0 {
		return
	}
	if err := store.Modify(featureID, func(f *feature.Feature) error {
		f.CurrentPhaseStatus = verifyingPhaseStatus
		f.VerificationItems = items
		return nil
	}); err == nil {
		notifyVerificationProgress(featureID, onProgress)
	}
}

// updateVerificationStatus records one command's progress state.
func updateVerificationStatus(store ports.FeatureStore, featureID, name, state string, onProgress ...func(string)) {
	if store == nil || strings.TrimSpace(name) == "" {
		return
	}
	if err := store.Modify(featureID, func(f *feature.Feature) error {
		for i := range f.VerificationItems {
			if f.VerificationItems[i].Name == name {
				f.VerificationItems[i].State = state
				return nil
			}
		}
		f.VerificationItems = append(f.VerificationItems, feature.VerificationItemStatus{Name: name, State: state})
		return nil
	}); err == nil {
		notifyVerificationProgress(featureID, onProgress)
	}
}

// clearVerificationStatuses removes the verification substep markers.
func clearVerificationStatuses(store ports.FeatureStore, featureID string, onProgress ...func(string)) {
	if store == nil {
		return
	}
	if err := store.Modify(featureID, func(f *feature.Feature) error {
		if f.CurrentPhaseStatus == verifyingPhaseStatus {
			f.CurrentPhaseStatus = ""
		}
		f.VerificationItems = nil
		return nil
	}); err == nil {
		notifyVerificationProgress(featureID, onProgress)
	}
}

func notifyVerificationProgress(featureID string, callbacks []func(string)) {
	for _, callback := range callbacks {
		if callback != nil {
			callback(featureID)
		}
	}
}
