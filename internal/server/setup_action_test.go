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
	"encoding/json"
	"net/http"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type setupActionMutationTarget struct {
	MutationTarget
	setupCalls []string
	setupErr   error
}

func (t *setupActionMutationTarget) SetupFeature(featureID string) (FeatureSetupResponse, error) {
	t.setupCalls = append(t.setupCalls, featureID)
	if t.setupErr != nil {
		return FeatureSetupResponse{}, t.setupErr
	}
	return FeatureSetupResponse{FeatureID: featureID, Result: "setup_started"}, nil
}

func TestFeatureSetupActionDispatchesServerOwnedSetup(t *testing.T) {
	t.Parallel()
	target := &setupActionMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/setup", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	var body FeatureSetupResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.FeatureID != fixtureFeatureID || body.Result != "setup_started" {
		t.Fatalf("response = %+v; want setup_started for %s", body, fixtureFeatureID)
	}
	if len(target.setupCalls) != 1 || target.setupCalls[0] != fixtureFeatureID {
		t.Fatalf("setup calls = %v; want one for %s", target.setupCalls, fixtureFeatureID)
	}
}

func TestFeatureSetupActionRejectsConflicts(t *testing.T) {
	t.Parallel()
	target := &setupActionMutationTarget{
		setupErr: &ActionConflictError{Message: "feature has no pending setup"},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/setup", map[string]any{})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d; want 409", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != errCodeConflict {
		t.Fatalf("error code = %q; want %q", body.Error.Code, errCodeConflict)
	}
}

func TestActionCatalogSetupAndStartLifecycle(t *testing.T) {
	t.Parallel()
	publishable := true

	settingUp := actionCatalogTestFeature(feature.StatusSettingUpWorktrees, feature.Checkpoints{}, &publishable, nil)
	settingUp.Run().Setup = &feature.SetupState{Status: feature.SetupStatusRunning}
	actions := actionCatalogDTOs(settingUp)
	if got := actionDTOByID(t, actions, actionSetup); !got.Enabled {
		t.Fatalf("setup action = %+v; want enabled while setup is pending", got)
	}
	if got := actionDTOByID(t, actions, actionStart); got.Enabled {
		t.Fatalf("start action = %+v; want disabled while setup is pending", got)
	}

	failedSetup := actionCatalogTestFeature(feature.StatusFailed, feature.Checkpoints{}, &publishable, nil)
	failedSetup.FailureType = feature.FailureWorktreeSetup
	failedSetup.Run().Setup = &feature.SetupState{Status: feature.SetupStatusFailed}
	actions = actionCatalogDTOs(failedSetup)
	if got := actionDTOByID(t, actions, actionSetup); !got.Enabled {
		t.Fatalf("setup action = %+v; want enabled for failed setup (retry)", got)
	}

	// After successful setup the feature is Created: Start is enabled and
	// setup no longer applies.
	created := actionCatalogTestFeature(feature.StatusCreated, feature.Checkpoints{}, &publishable, nil)
	actions = actionCatalogDTOs(created)
	if got := actionDTOByID(t, actions, actionStart); !got.Enabled {
		t.Fatalf("start action = %+v; want enabled once setup completed", got)
	}
	setupAction := actionDTOByID(t, actions, actionSetup)
	if setupAction.Enabled {
		t.Fatalf("setup action = %+v; want disabled without pending setup", setupAction)
	}
	if len(setupAction.DisabledReasons) == 0 || setupAction.DisabledReasons[0].Code != "no_pending_setup" {
		t.Fatalf("setup disabled reasons = %+v; want no_pending_setup", setupAction.DisabledReasons)
	}
}
