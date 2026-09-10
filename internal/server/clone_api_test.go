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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/clone"
)

// fakeCloneService is a scripted CloneService for handler tests.
type fakeCloneService struct {
	startInput   clone.StartInput
	startErr     error
	startRecord  clone.Record
	createInput  clone.CreateStartInput
	createErr    error
	createRecord clone.Record
	snapshotID   string
	snapshotErr  error
	snapshotRec  clone.Record
	listQuery    clone.ListQuery
	listResult   clone.ListResult
	cancelID     string
	cancelErr    error
	cancelRec    clone.Record
	cleanupID    string
	cleanupErr   error
	cleanupRec   clone.Record
	retryID      string
	retryErr     error
	retryRec     clone.Record
	recovered    bool
	shutdown     bool
}

func (f *fakeCloneService) Start(_ context.Context, input clone.StartInput) (clone.Record, error) {
	f.startInput = input
	return f.startRecord, f.startErr
}

func (f *fakeCloneService) Create(_ context.Context, input clone.CreateStartInput) (clone.Record, error) {
	f.createInput = input
	return f.createRecord, f.createErr
}

func (f *fakeCloneService) Snapshot(id string) (clone.Record, error) {
	f.snapshotID = id
	return f.snapshotRec, f.snapshotErr
}

func (f *fakeCloneService) List(q clone.ListQuery) (clone.ListResult, error) {
	f.listQuery = q
	return f.listResult, nil
}

func (f *fakeCloneService) Cancel(id string) (clone.Record, error) {
	f.cancelID = id
	return f.cancelRec, f.cancelErr
}

func (f *fakeCloneService) Cleanup(id string) (clone.Record, error) {
	f.cleanupID = id
	return f.cleanupRec, f.cleanupErr
}

func (f *fakeCloneService) Retry(id string) (clone.Record, error) {
	f.retryID = id
	return f.retryRec, f.retryErr
}

func (f *fakeCloneService) Recover() error {
	f.recovered = true
	return nil
}

func (f *fakeCloneService) Shutdown(_ context.Context) error {
	f.shutdown = true
	return nil
}

func (f *fakeCloneService) Sweep() []string { return nil }

func newCloneHandler(t *testing.T, svc *fakeCloneService) http.Handler {
	t.Helper()
	return NewHandler(HandlerOptions{
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
		Mutations:             &preflightMutationTarget{},
		Clones:                svc,
	})
}

func sampleCloneRecord() clone.Record {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	return clone.Record{
		ID:              "clone-0123456789abcdef",
		IdempotencyKey:  "key-1",
		RemoteURL:       "https://example.com/acme/widget.git",
		RootPath:        "/roots/main",
		Destination:     "widget",
		DestinationPath: "/roots/main/widget",
		State:           clone.StateAccepted,
		Stage:           clone.StagePreparing,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestWorkspaceCloneStartReturnsAcceptedSnapshot(t *testing.T) {
	t.Parallel()
	svc := &fakeCloneService{startRecord: sampleCloneRecord()}
	handler := newCloneHandler(t, svc)
	w := postTrustedAuthedJSON(handler, apiPathWorkspaceClone, map[string]any{
		"remote_url":      "https://example.com/acme/widget.git",
		"root_path":       "/roots/main",
		"destination":     "widget",
		"idempotency_key": "key-1",
	})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s; want 202", w.Code, w.Body.String())
	}
	if svc.startInput.Remote != "https://example.com/acme/widget.git" ||
		svc.startInput.Destination != "widget" || svc.startInput.IdempotencyKey != "key-1" {
		t.Errorf("service input = %+v", svc.startInput)
	}
	var resp CloneActionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result != "accepted" || resp.Operation.ID != "clone-0123456789abcdef" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Operation.State != CloneOperationStateAccepted {
		t.Errorf("state = %s", resp.Operation.State)
	}
}

func TestWorkspaceCloneStartRequiresTrustedMutation(t *testing.T) {
	t.Parallel()
	svc := &fakeCloneService{startRecord: sampleCloneRecord()}
	handler := newCloneHandler(t, svc)
	// Missing trusted-client header.
	w := authedPostJSON(handler, apiPathWorkspaceClone, map[string]any{
		"remote_url": "https://example.com/acme/widget.git", "root_path": "/r",
		"destination": "w", "idempotency_key": "k",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("untrusted start status = %d, want 403", w.Code)
	}
	// Missing auth.
	payload, _ := json.Marshal(map[string]any{})
	req := httptest.NewRequest(http.MethodPost, apiPathWorkspaceClone, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", contentTypeJSON)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated start status = %d, want 401", rec.Code)
	}
	// Malformed/unknown fields are rejected by the strict decoder.
	w = postTrustedAuthedJSON(handler, apiPathWorkspaceClone, map[string]any{
		"remote_url": "x", "root_path": "r", "destination": "d",
		"idempotency_key": "k", "unknown_field": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field start status = %d, want 400", w.Code)
	}
}

func TestWorkspaceCloneStartErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code     string
		wantHTTP int
		wantCode string
	}{
		{clone.ValidationRemoteInvalid, http.StatusBadRequest, "clone_remote_invalid"},
		{clone.ValidationDestinationInvalid, http.StatusBadRequest, "clone_destination_invalid"},
		{clone.CodeRootIneligible, http.StatusBadRequest, "clone_root_ineligible"},
		{clone.CodeIdempotencyConflict, http.StatusConflict, "clone_idempotency_conflict"},
		{clone.CodeDestinationExists, http.StatusConflict, "clone_destination_exists"},
		{clone.CodeDestinationReserved, http.StatusConflict, "clone_destination_reserved"},
		{clone.CodeDestinationShadowed, http.StatusConflict, "clone_destination_shadowed"},
		{clone.CodeUnavailable, http.StatusServiceUnavailable, "clone_unavailable"},
		{clone.CodeNotFound, http.StatusNotFound, "clone_operation_not_found"},
		{clone.CodeHistoryUnavailable, http.StatusNotFound, "clone_history_unavailable"},
		{clone.CodeNotRetryable, http.StatusConflict, "clone_not_retryable"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			svc := &fakeCloneService{startErr: &clone.ServiceError{Code: tc.code, Detail: "detail"}}
			handler := newCloneHandler(t, svc)
			w := postTrustedAuthedJSON(handler, apiPathWorkspaceClone, map[string]any{
				"remote_url": "https://example.com/acme/widget.git", "root_path": "/r",
				"destination": "w", "idempotency_key": "k",
			})
			if w.Code != tc.wantHTTP {
				t.Fatalf("status = %d, want %d body=%s", w.Code, tc.wantHTTP, w.Body.String())
			}
			var body struct {
				Error struct {
					Code  string `json:"code"`
					Class string `json:"class"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.wantCode {
				t.Errorf("error code = %s, want %s", body.Error.Code, tc.wantCode)
			}
			if body.Error.Class == "" {
				t.Error("canonical error class missing")
			}
		})
	}
}

func TestWorkspaceCloneListBoundedAndOrdered(t *testing.T) {
	t.Parallel()
	rec := sampleCloneRecord()
	rec2 := rec
	rec2.ID = "clone-fedcba9876543210"
	rec2.State = clone.StateCleanupPending
	svc := &fakeCloneService{listResult: clone.ListResult{
		Operations: []clone.Record{rec, rec2},
		NextToken:  "2|123|clone-fedcba9876543210",
	}}
	handler := newCloneHandler(t, svc)
	w := authedGet(handler, apiPathWorkspaceClone+"?limit=2&after=1%7C42%7Cclone-0123456789abcdef")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.listQuery.Limit != 2 || svc.listQuery.After != "1|42|clone-0123456789abcdef" {
		t.Errorf("list query = %+v", svc.listQuery)
	}
	var resp CloneOperationListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Operations) != 2 || resp.Operations[1].State != CloneOperationStateCleanupPending {
		t.Errorf("operations = %+v", resp.Operations)
	}
	if resp.NextPageToken == nil || *resp.NextPageToken != "2|123|clone-fedcba9876543210" {
		t.Errorf("next page token = %v", resp.NextPageToken)
	}

	for _, raw := range []string{"0", "-1", "201", "abc", "9999"} {
		w := authedGet(handler, apiPathWorkspaceClone+"?limit="+raw)
		if w.Code != http.StatusBadRequest {
			t.Errorf("limit=%s status = %d, want 400", raw, w.Code)
		}
	}
	w = authedGet(handler, apiPathWorkspaceClone+"?after="+strings.Repeat("x", 300))
	if w.Code != http.StatusBadRequest {
		t.Errorf("overlong after status = %d, want 400", w.Code)
	}
}

func TestWorkspaceCloneSnapshotRoute(t *testing.T) {
	t.Parallel()
	rec := sampleCloneRecord()
	rec.State = clone.StateSucceeded
	rec.Published = &clone.Publication{RepoKey: "widget", Path: "/roots/main/widget", HasHead: true, PublishedAt: time.Now().UTC()}
	svc := &fakeCloneService{snapshotRec: rec}
	handler := newCloneHandler(t, svc)
	w := authedGet(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.snapshotID != "clone-0123456789abcdef" {
		t.Errorf("snapshot id = %s", svc.snapshotID)
	}
	var resp CloneOperationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Operation.State != CloneOperationStateSucceeded || resp.Operation.Published == nil || !resp.Operation.Published.HasHead {
		t.Errorf("operation = %+v", resp.Operation)
	}
	if w.Header().Get("ETag") == "" {
		t.Error("snapshot read missing revision ETag")
	}

	svcNotFound := &fakeCloneService{snapshotErr: &clone.ServiceError{Code: clone.CodeNotFound}}
	handlerNotFound := newCloneHandler(t, svcNotFound)
	w = authedGet(handlerNotFound, apiPathWorkspaceClone+"/clone-missing")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing snapshot status = %d, want 404", w.Code)
	}
}

func TestWorkspaceCloneOperationMutations(t *testing.T) {
	t.Parallel()
	cancelling := sampleCloneRecord()
	cancelling.State = clone.StateCancelling
	cancelling.CancelRequested = true
	svc := &fakeCloneService{cancelRec: cancelling}
	handler := newCloneHandler(t, svc)
	w := postTrustedAuthedJSON(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef/cancel", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body=%s", w.Code, w.Body.String())
	}
	if svc.cancelID != "clone-0123456789abcdef" {
		t.Errorf("cancel id = %s", svc.cancelID)
	}
	var resp CloneActionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Result != "cancellation_requested" || !resp.Operation.CancelRequested {
		t.Errorf("cancel response = %+v", resp)
	}
	if resp.Operation.State != CloneOperationStateCancelling {
		t.Errorf("cancel must not claim cancelled immediately; state = %s", resp.Operation.State)
	}

	cleanupRec := sampleCloneRecord()
	cleanupRec.State = clone.StateFailed
	svc2 := &fakeCloneService{cleanupRec: cleanupRec}
	handler2 := newCloneHandler(t, svc2)
	w = postTrustedAuthedJSON(handler2, apiPathWorkspaceClone+"/clone-0123456789abcdef/cleanup", map[string]any{})
	if w.Code != http.StatusOK || svc2.cleanupID != "clone-0123456789abcdef" {
		t.Fatalf("cleanup = %d %+v", w.Code, svc2.cleanupID)
	}

	retryRec := sampleCloneRecord()
	svc3 := &fakeCloneService{retryRec: retryRec}
	handler3 := newCloneHandler(t, svc3)
	w = postTrustedAuthedJSON(handler3, apiPathWorkspaceClone+"/clone-0123456789abcdef/retry", map[string]any{})
	if w.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body=%s", w.Code, w.Body.String())
	}
	var retryResp CloneActionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &retryResp)
	if retryResp.Result != "retry_started" {
		t.Errorf("retry result = %s", retryResp.Result)
	}

	// Unknown sub-action and invalid IDs are 404s, and mutations require
	// the trusted header.
	w = authedGet(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef/explode")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown subaction status = %d", w.Code)
	}
	w = authedGet(handler, apiPathWorkspaceClone+"/not/an/id")
	if w.Code != http.StatusNotFound {
		t.Errorf("nested path status = %d", w.Code)
	}
	w = authedPostJSON(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef/cancel", map[string]any{})
	if w.Code != http.StatusForbidden {
		t.Errorf("untrusted cancel status = %d", w.Code)
	}
	w = authedGet(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef/cancel")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET cancel status = %d", w.Code)
	}
}

func TestWorkspaceCloneRoutesRequireService(t *testing.T) {
	t.Parallel()
	handler := NewHandler(HandlerOptions{
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
	})
	w := authedGet(handler, apiPathWorkspaceClone)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("list without service status = %d, want 503", w.Code)
	}
	w = authedGet(handler, apiPathWorkspaceClone+"/clone-0123456789abcdef")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("snapshot without service status = %d, want 503", w.Code)
	}
}

func TestCloneOperationDTOHidesInternalState(t *testing.T) {
	t.Parallel()
	rec := sampleCloneRecord()
	rec.State = clone.StateCleanupPending
	rec.PendingOutcome = clone.StateFailed
	rec.Error = &clone.OpError{Code: clone.FailureExecution, Diagnostics: "boom"}
	rec.CleanupIssue = "staging ownership could not be verified"
	dto := cloneOperationDTO(rec)
	if dto.State != CloneOperationStateCleanupPending || dto.PendingOutcome != "failed" {
		t.Errorf("dto state = %s/%s", dto.State, dto.PendingOutcome)
	}
	if dto.Error == nil || dto.Error.Code != "clone_execution_failed" {
		t.Errorf("dto error = %+v", dto.Error)
	}
	if dto.CleanupIssue == "" {
		t.Error("cleanup issue missing")
	}
	// The staging path and process identity never appear on the wire.
	var buf []byte
	buf, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	body := string(buf)
	for _, banned := range []string{`"staging_path"`, `"process"`, `"pid"`, `"start_identity"`, `.agentico-clone`} {
		if strings.Contains(body, banned) {
			t.Errorf("wire model leaks internal state %q: %s", banned, body)
		}
	}
}
