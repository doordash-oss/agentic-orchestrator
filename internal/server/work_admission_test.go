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
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

func decodeAdmissionError(t *testing.T, resp *http.Response) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body
}

func TestWorkAdmissionWriteMutationErrorClosedAdmission(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	writeMutationError(w, &workadmission.ClosedError{Category: workadmission.CategoryFeature})
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	body := decodeAdmissionError(t, resp)
	if body.APIVersion != APIVersion {
		t.Fatalf("api_version = %q, want %q", body.APIVersion, APIVersion)
	}
	if body.Error.Code != string(errcat.UpdateInProgress) {
		t.Fatalf("code = %q, want update_in_progress", body.Error.Code)
	}
	if body.Error.Summary != "An update operation is already in progress; retry after 5 seconds." {
		t.Fatalf("summary = %q, want the retry hint interpolated", body.Error.Summary)
	}
	if !strings.Contains(body.Error.Diagnostics, "admission closed for feature work") {
		t.Fatalf("diagnostics = %q, want the closed-category reason", body.Error.Diagnostics)
	}

	// Unrelated errors keep their existing mappings.
	w = httptest.NewRecorder()
	writeMutationError(w, errors.New("plain failure"))
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain error status = %d, want 400", resp.StatusCode)
	}
	w = httptest.NewRecorder()
	writeMutationError(w, nil)
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("nil error status = %d, want 500", resp.StatusCode)
	}
}

func TestWorkAdmissionWriteAdmissionRefusalMapping(t *testing.T) {
	t.Parallel()
	h := &apiHandler{}

	// Non-closed errors are not the handler's to render.
	w := httptest.NewRecorder()
	if h.writeAdmissionRefusal(w, errors.New("unrelated failure")) {
		t.Fatal("non-closed error must not write a refusal")
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("non-closed error wrote a response: %d %q", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	if !h.writeAdmissionRefusal(w, &workadmission.ClosedError{Category: workadmission.CategoryChat}) {
		t.Fatal("closed error must write the refusal")
	}
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	body := decodeAdmissionError(t, resp)
	if body.Error.Code != string(errcat.UpdateInProgress) {
		t.Fatalf("code = %q, want update_in_progress", body.Error.Code)
	}
	if !strings.Contains(body.Error.Diagnostics, "admission closed for chat work") {
		t.Fatalf("diagnostics = %q", body.Error.Diagnostics)
	}
}

func TestWorkAdmissionRefuseClosedBoundary(t *testing.T) {
	t.Parallel()
	boundary := workadmission.New(workadmission.Options{})
	h := &apiHandler{admission: boundary}

	w := httptest.NewRecorder()
	if h.refuseAdmissionClosed(w) {
		t.Fatal("open boundary must not refuse")
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("open boundary wrote a response: %d %q", w.Code, w.Body.String())
	}

	if !boundary.CloseIfQuiesced() {
		t.Fatal("quiesced boundary must close")
	}
	w = httptest.NewRecorder()
	if !h.refuseAdmissionClosed(w) {
		t.Fatal("closed boundary must refuse")
	}
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if body := decodeAdmissionError(t, resp); body.Error.Code != string(errcat.UpdateInProgress) {
		t.Fatalf("code = %q, want update_in_progress", body.Error.Code)
	}

	// A handler without a boundary (no install support) never refuses.
	bare := &apiHandler{}
	w = httptest.NewRecorder()
	if bare.refuseAdmissionClosed(w) {
		t.Fatal("nil boundary must not refuse")
	}
}

func TestWorkAdmissionFeatureDetectorProjection(t *testing.T) {
	t.Parallel()
	features := []*feature.Feature{
		{ID: "running", Status: feature.StatusImplementing},
		{ID: "needs-input", Status: feature.StatusNeedUserInput},
		{ID: "review-paused", Status: feature.StatusPlanNeedsReview},
		{ID: "terminal", Status: feature.StatusDone},
		nil,
		{ID: "active-child", Status: feature.StatusImplementing, Parent: &feature.ChildRelationship{ParentID: "parent-1"}},
		{ID: "closed-child", Status: feature.StatusImplementing, Parent: &feature.ChildRelationship{ParentID: "parent-1", CloseOutcome: "merged"}},
	}
	h := &apiHandler{features: featureListerFunc(func() ([]*feature.Feature, error) { return features, nil })}
	activity, err := h.detectFeatureActivity(context.Background())
	if err != nil {
		t.Fatalf("detection error: %v", err)
	}
	// Running, need-user-input, and an open-relationship running child
	// count; review-paused, terminal, and closed-relationship children do
	// not (the pause-stop projection).
	if activity.Features != 3 {
		t.Fatalf("features = %d, want 3", activity.Features)
	}
	if !activity.Busy() {
		t.Fatal("activity must read busy")
	}

	failing := &apiHandler{features: featureListerFunc(func() ([]*feature.Feature, error) {
		return nil, errors.New("store unreadable")
	})}
	if _, err := failing.detectFeatureActivity(context.Background()); err == nil {
		t.Fatal("lister failure must fail detection")
	}

	bare := &apiHandler{}
	activity, err = bare.detectFeatureActivity(context.Background())
	if err != nil || activity.Features != 0 {
		t.Fatalf("nil lister activity = %+v, err = %v, want zero counts", activity, err)
	}
}

func TestWorkAdmissionChatDetector(t *testing.T) {
	t.Parallel()
	active := &fakeSessionView{id: ChatSessionID, status: ports.SessionRunning}
	h := &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{active}}}
	activity, err := h.detectChatActivity(context.Background())
	if err != nil {
		t.Fatalf("detection error: %v", err)
	}
	if !activity.ChatActive {
		t.Fatal("active chat session must report chat_active")
	}

	finished := &fakeSessionView{id: ChatSessionID, status: ports.SessionDone}
	h = &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{finished}}}
	activity, err = h.detectChatActivity(context.Background())
	if err != nil {
		t.Fatalf("detection error: %v", err)
	}
	if activity.ChatActive {
		t.Fatal("finished chat session must not report chat_active")
	}

	// Only the singleton chat identity counts.
	other := &fakeSessionView{id: "session-1", status: ports.SessionRunning}
	h = &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{other}}}
	activity, _ = h.detectChatActivity(context.Background())
	if activity.ChatActive {
		t.Fatal("a non-chat session must not report chat_active")
	}

	bare := &apiHandler{}
	if activity, err := bare.detectChatActivity(context.Background()); err != nil || activity.ChatActive {
		t.Fatalf("nil sessions activity = %+v, err = %v", activity, err)
	}
}

func TestWorkAdmissionUploadOriginRepositoryDetectors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A bare handler reads zero everywhere.
	bare := &apiHandler{}
	if activity, _ := bare.detectUploadActivity(ctx); activity.Uploads != 0 {
		t.Fatalf("bare uploads = %d, want 0", activity.Uploads)
	}
	if activity, _ := bare.detectOriginActivity(ctx); activity.OriginChecks != 0 {
		t.Fatalf("bare origin checks = %d, want 0", activity.OriginChecks)
	}
	if activity, _ := bare.detectRepositoryWork(ctx); activity.RepositoryWork != 0 {
		t.Fatalf("bare repository work = %d, want 0", activity.RepositoryWork)
	}

	// In-flight uploads: one consumption claim plus two staging requests.
	uploads := &uploadStore{claims: map[string]struct{}{"00000000000000000000000000000000": {}}}
	uploads.staging.Add(2)
	h := &apiHandler{uploads: uploads}
	if activity, _ := h.detectUploadActivity(ctx); activity.Uploads != 3 {
		t.Fatalf("uploads = %d, want 3 (claims + staging)", activity.Uploads)
	}

	// A registered origin-comparison attempt counts until it settles.
	coordinator := newOriginCheckCoordinator()
	coordinator.mu.Lock()
	coordinator.inflight[originCheckKey{}] = &originFlight{done: make(chan struct{})}
	coordinator.mu.Unlock()
	h = &apiHandler{originChecks: coordinator}
	if activity, _ := h.detectOriginActivity(ctx); activity.OriginChecks != 1 {
		t.Fatalf("origin checks = %d, want 1", activity.OriginChecks)
	}

	// Admitted Update-from-origin attempts and read-launched probes both
	// count as repository work, and only while in flight.
	tracker := newSourceUpdateTracker()
	releaseUpdate := tracker.begin(git.RepoIdentity{Path: "/repo", CommonDir: "/repo/.git"})
	probes := NewProbeActivity()
	releaseProbe := probes.enter()
	h = &apiHandler{sourceUpdates: tracker, probeActivity: probes}
	if activity, _ := h.detectRepositoryWork(ctx); activity.RepositoryWork != 2 {
		t.Fatalf("repository work = %d, want 2 (source update + probe)", activity.RepositoryWork)
	}
	releaseUpdate()
	releaseProbe()
	if activity, _ := h.detectRepositoryWork(ctx); activity.RepositoryWork != 0 {
		t.Fatalf("settled repository work = %d, want 0", activity.RepositoryWork)
	}
}

// admissionClosedStartTarget is a mutation target whose StartFeature fails
// with a closed-boundary error, modeling the orchestration layer's
// admission acquisition surfacing through the mutation contract.
type admissionClosedStartTarget struct {
	MutationTarget
	err error
}

func (m *admissionClosedStartTarget) StartFeature(string) (FeatureStartResponse, error) {
	return FeatureStartResponse{}, m.err
}

func TestWorkAdmissionStartFeatureRefusedWhileClosed(t *testing.T) {
	t.Parallel()
	target := &admissionClosedStartTarget{err: &workadmission.ClosedError{Category: workadmission.CategoryFeature}}
	boundary := workadmission.New(workadmission.Options{})
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             target,
		Admission:             boundary,
	})
	if !boundary.CloseIfQuiesced() {
		t.Fatal("quiesced boundary must close")
	}

	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := authorizedUpdateRequest(http.MethodPost, "/api/v1/features/feature-1/actions/start", []byte(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		handler.routes().ServeHTTP(w, req)
		return w
	}

	w := post()
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while admission is closed", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	body := decodeAdmissionError(t, resp)
	if body.Error.Code != string(errcat.UpdateInProgress) {
		t.Fatalf("code = %q, want update_in_progress", body.Error.Code)
	}

	// Once the boundary reopens, the same mutation succeeds.
	boundary.Open()
	target.err = nil
	w = post()
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after reopening", resp.StatusCode)
	}
}
