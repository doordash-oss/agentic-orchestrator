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

package server

import (
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestFailedPhaseRemediationActionsAreEnabled pins the failure envelope to the
// action catalog: every remediation action a failed-phase envelope references
// is enabled for a Failed feature, and retry (the documented action for a
// failed phase) is among them.
func TestFailedPhaseRemediationActionsAreEnabled(t *testing.T) {
	t.Parallel()
	publishable := true
	for _, code := range []errcat.Code{
		errcat.InfrastructureFailure,
		errcat.SessionCrashed,
		errcat.SafetyRailTripped,
		errcat.IterationBudgetExhausted,
		errcat.ProtocolViolation,
	} {
		f := actionCatalogTestFeature(feature.StatusFailed, feature.Checkpoints{}, &publishable)
		f.Pipeline = feature.PipelineMedium
		rendered := errcat.RenderRecord(errcat.FailureRecord{Code: code, Diagnostics: "boom"})
		if rendered.Remediation == nil {
			t.Fatalf("%s: no remediation", code)
		}
		actions := actionCatalogDTOsWithChildGuard(f, false)
		sawRetry := false
		for _, id := range rendered.Remediation.Actions {
			if id == string(FeatureActionRetry) {
				sawRetry = true
			}
			if action := actionDTOByID(t, actions, id); !action.Enabled {
				t.Errorf("%s: remediation action %q is disabled for a failed feature (%+v)", code, id, action.DisabledReasons)
			}
		}
		if !sawRetry {
			t.Errorf("%s: remediation actions %v omit retry", code, rendered.Remediation.Actions)
		}
	}
}
