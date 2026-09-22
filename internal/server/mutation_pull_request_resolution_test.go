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

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// pullRequestResolutionMutationTarget records the reopen/recreate dispatch
// and answers with the canned result, so the dispatcher's decode, validation,
// and error mapping can be asserted without an orchestrator.
type pullRequestResolutionMutationTarget struct {
	MutationTarget
	reopenCalls       int
	reopenFeatureID   string
	reopenReq         ReopenPullRequestRequest
	recreateCalls     int
	recreateFeatureID string
	recreateReq       RecreatePullRequestRequest
	err               error
}

func (t *pullRequestResolutionMutationTarget) ReopenPullRequestFeature(featureID string, req ReopenPullRequestRequest) (ReopenPullRequestResponse, error) {
	t.reopenCalls++
	t.reopenFeatureID = featureID
	t.reopenReq = req
	if t.err != nil {
		return ReopenPullRequestResponse{}, t.err
	}
	return ReopenPullRequestResponse{FeatureID: featureID, Result: "reopened"}, nil
}

func (t *pullRequestResolutionMutationTarget) RecreatePullRequestFeature(featureID string, req RecreatePullRequestRequest) (RecreatePullRequestResponse, error) {
	t.recreateCalls++
	t.recreateFeatureID = featureID
	t.recreateReq = req
	if t.err != nil {
		return RecreatePullRequestResponse{}, t.err
	}
	return RecreatePullRequestResponse{FeatureID: featureID, Result: "recreated"}, nil
}

func TestReopenPullRequestActionDispatchesResolutionRequest(t *testing.T) {
	t.Parallel()
	target := &pullRequestResolutionMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/reopen-pull-request", map[string]any{
		"repository":      "repo-a",
		"layer":           2,
		"source_revision": "rev-7",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if target.reopenCalls != 1 || target.reopenFeatureID != fixtureFeatureID {
		t.Fatalf("reopen calls = %d for %q; want one for %s", target.reopenCalls, target.reopenFeatureID, fixtureFeatureID)
	}
	if target.reopenReq.Repository != "repo-a" || target.reopenReq.Layer != 2 || target.reopenReq.SourceRevision != "rev-7" {
		t.Fatalf("reopen request = %+v; want repo-a layer 2 with the source revision guard", target.reopenReq)
	}

	var body ReopenPullRequestResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.FeatureID != fixtureFeatureID || body.Result != "reopened" {
		t.Fatalf("response = %+v; want reopened for %s", body, fixtureFeatureID)
	}
}

func TestRecreatePullRequestActionDispatchesResolutionRequest(t *testing.T) {
	t.Parallel()
	target := &pullRequestResolutionMutationTarget{}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/recreate-pull-request", map[string]any{
		"repository":      "repo-a",
		"layer":           2,
		"source_revision": "rev-7",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if target.recreateCalls != 1 || target.recreateFeatureID != fixtureFeatureID {
		t.Fatalf("recreate calls = %d for %q; want one for %s", target.recreateCalls, target.recreateFeatureID, fixtureFeatureID)
	}
	if target.recreateReq.Repository != "repo-a" || target.recreateReq.Layer != 2 || target.recreateReq.SourceRevision != "rev-7" {
		t.Fatalf("recreate request = %+v; want repo-a layer 2 with the source revision guard", target.recreateReq)
	}

	var body RecreatePullRequestResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.FeatureID != fixtureFeatureID || body.Result != "recreated" {
		t.Fatalf("response = %+v; want recreated for %s", body, fixtureFeatureID)
	}
}

func TestPullRequestResolutionActionsRejectInvalidRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		action string
		body   map[string]any
	}{
		{name: "reopen missing repository", action: actionReopenPullRequest, body: map[string]any{"layer": 2}},
		{name: "reopen zero layer", action: actionReopenPullRequest, body: map[string]any{"repository": "repo-a", "layer": 0}},
		{name: "reopen negative layer", action: actionReopenPullRequest, body: map[string]any{"repository": "repo-a", "layer": -2}},
		{name: "recreate missing repository", action: actionRecreatePullRequest, body: map[string]any{"layer": 2}},
		{name: "recreate zero layer", action: actionRecreatePullRequest, body: map[string]any{"repository": "repo-a", "layer": 0}},
		{name: "recreate negative layer", action: actionRecreatePullRequest, body: map[string]any{"repository": "repo-a", "layer": -2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target := &pullRequestResolutionMutationTarget{}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				DisableHostValidation: true,
			})

			w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/"+tc.action, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
			}
			body := decodeErrorBody(t, w)
			if body.Error.Code != string(errcat.BadRequest) {
				t.Fatalf("error code = %q; want %q", body.Error.Code, errcat.BadRequest)
			}
			if target.reopenCalls != 0 || target.recreateCalls != 0 {
				t.Fatalf("resolution calls = %d/%d; want none for an invalid request", target.reopenCalls, target.recreateCalls)
			}
		})
	}
}

// TestPullRequestResolutionActionsMapStoredRecordConflicts pins the
// dispatcher's mapping of a stored-record resolution failure: the mutation
// target's ActionConflictError carries the canonical code and the record's
// repository context, and the wire response exposes the layer position,
// title, and pull-request URL the publish modal dispatches from.
func TestPullRequestResolutionActionsMapStoredRecordConflicts(t *testing.T) {
	t.Parallel()
	const prURL = "https://github.com/acme/web/pull/12"
	for _, tc := range []struct {
		name   string
		action string
		code   errcat.Code
	}{
		{name: "reopen refused", action: actionReopenPullRequest, code: errcat.PublishReopenFailed},
		{name: "head branch missing", action: actionReopenPullRequest, code: errcat.PublishHeadBranchMissing},
		{name: "recreate failed", action: actionRecreatePullRequest, code: errcat.PublishRecreateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := errcat.FailureRecord{
				Code: tc.code,
				Context: &errcat.RecordContext{Repositories: []errcat.CodeRepository{{
					Name:           "repo-a",
					Branch:         "feature/stack-api",
					LayerPosition:  2,
					LayerTitle:     "API surface",
					PullRequestURL: prURL,
				}}},
				Diagnostics: "raw resolution refusal",
			}
			target := &pullRequestResolutionMutationTarget{
				err: &ActionConflictError{
					Code:    tc.code,
					Detail:  "raw resolution refusal",
					Options: errcat.RecordOptions(record),
				},
			}
			handler := NewHandler(HandlerOptions{
				Mutations:             target,
				DisableHostValidation: true,
			})

			w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/"+tc.action, map[string]any{
				"repository": "repo-a",
				"layer":      2,
			})
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d body=%s; want 409", w.Code, w.Body.String())
			}
			body := decodeErrorBody(t, w)
			if body.Error.Code != string(tc.code) {
				t.Fatalf("error code = %q; want %q", body.Error.Code, tc.code)
			}
			if body.Error.Context == nil || len(body.Error.Context.Repositories) != 1 {
				t.Fatalf("error context = %+v; want one repository", body.Error.Context)
			}
			repo := body.Error.Context.Repositories[0]
			if repo.Name != "repo-a" || repo.LayerPosition != 2 || repo.LayerTitle != "API surface" || repo.PullRequestURL != prURL {
				t.Fatalf("repository context = %+v; want repo-a layer 2 (API surface) with the pull request URL", repo)
			}
		})
	}
}

// TestPullRequestResolutionActionMapsMootStateConflict pins the moot-state
// refusal: a generic ActionConflictError without a code maps to 409 with the
// generic conflict code and the refusal text as diagnostics.
func TestPullRequestResolutionActionMapsMootStateConflict(t *testing.T) {
	t.Parallel()
	target := &pullRequestResolutionMutationTarget{
		err: &ActionConflictError{Detail: "layer 2's pull request for repo-a is open; there is nothing to recreate"},
	}
	handler := NewHandler(HandlerOptions{
		Mutations:             target,
		DisableHostValidation: true,
	})

	w := postTrustedJSON(handler, "/api/v1/features/"+fixtureFeatureID+"/actions/recreate-pull-request", map[string]any{
		"repository": "repo-a",
		"layer":      2,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s; want 409", w.Code, w.Body.String())
	}
	body := decodeErrorBody(t, w)
	if body.Error.Code != string(errcat.Conflict) {
		t.Fatalf("error code = %q; want %q", body.Error.Code, errcat.Conflict)
	}
	if body.Error.Diagnostics != "layer 2's pull request for repo-a is open; there is nothing to recreate" {
		t.Fatalf("diagnostics = %q; want the moot-state refusal detail", body.Error.Diagnostics)
	}
}

// TestPullRequestResolutionActionCatalog pins the reopen/recreate catalog
// entries: enabled together for a code-ready publishable feature with a
// closed stack layer entry, disabled with no_closed_pull_request when no
// entry is closed, disabled with the status reason while running, locked
// behind the active-child reason, and never offered on a child feature.
func TestPullRequestResolutionActionCatalog(t *testing.T) {
	t.Parallel()
	publishable := true
	resolutionFeature := func(status feature.Status, state feature.StackPRState) *feature.Feature {
		return &feature.Feature{
			ID:     "feat-resolution",
			Status: status,
			Repos:  []feature.FeatureRepo{{Name: repoNameSelf, Publishable: &publishable}},
			Stack: []feature.StackLayer{{
				Position: 2,
				Title:    "API surface",
				Branch:   "feature/feat-resolution/2-api-surface",
				Repos: map[string]feature.StackRepoEntry{
					repoNameSelf: {PRURL: "https://github.com/acme/web/pull/12", PRState: state},
				},
			}},
		}
	}
	resolutionActions := []string{actionReopenPullRequest, actionRecreatePullRequest}

	enabled := resolutionFeature(feature.StatusCodeReady, feature.StackPRStateClosed)
	actions := actionCatalogDTOs(enabled)
	for _, id := range resolutionActions {
		if got := actionDTOByID(t, actions, id); !got.Enabled || len(got.DisabledReasons) != 0 {
			t.Fatalf("%s action = %+v; want enabled with no disabled reasons for a closed stack layer entry", id, got)
		}
	}

	noClosed := resolutionFeature(feature.StatusCodeReady, feature.StackPRStateOpen)
	actions = actionCatalogDTOs(noClosed)
	for _, id := range resolutionActions {
		got := actionDTOByID(t, actions, id)
		if got.Enabled {
			t.Fatalf("%s action = %+v; want disabled without a closed pull request", id, got)
		}
		if len(got.DisabledReasons) == 0 || got.DisabledReasons[0].Code != "no_closed_pull_request" {
			t.Fatalf("%s disabled reasons = %+v; want no_closed_pull_request", id, got.DisabledReasons)
		}
	}

	running := resolutionFeature(feature.StatusImplementing, feature.StackPRStateClosed)
	actions = actionCatalogDTOs(running)
	for _, id := range resolutionActions {
		got := actionDTOByID(t, actions, id)
		if got.Enabled {
			t.Fatalf("%s action = %+v; want disabled while the feature is running", id, got)
		}
		if len(got.DisabledReasons) == 0 || got.DisabledReasons[0].Code != disabledStatusNotAllowed {
			t.Fatalf("%s disabled reasons = %+v; want the status reason while running", id, got.DisabledReasons)
		}
	}

	locked := resolutionFeature(feature.StatusCodeReady, feature.StackPRStateClosed)
	actions = actionCatalogDTOsWithChildGuard(locked, true)
	for _, id := range resolutionActions {
		got := actionDTOByID(t, actions, id)
		if got.Enabled {
			t.Fatalf("%s action = %+v; want locked while a child is active", id, got)
		}
		if len(got.DisabledReasons) == 0 || got.DisabledReasons[0].Code != disabledParentHasActiveChild.Code {
			t.Fatalf("%s disabled reasons = %+v; want the active-child lock first", id, got.DisabledReasons)
		}
	}

	child := &feature.Feature{
		ID:     "child-resolution",
		Status: feature.StatusCreated,
		Parent: &feature.ChildRelationship{ParentID: "parent-1", Kind: feature.ChildKindRefactor},
	}
	actions = actionCatalogDTOs(child)
	for _, id := range resolutionActions {
		for _, action := range actions {
			if action.ID == id {
				t.Fatalf("child catalog offers %s: %+v", id, actions)
			}
		}
	}
}
