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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
)

type updateAPIFixture struct {
	handler *apiHandler
	feed    *fakeUpdateFeed
}

// nopMutationTarget satisfies MutationTarget without implementing anything:
// update-check requests never dispatch feature mutations, and the tests that
// do reach a mutation path expect the strict decoder to reject first.
type nopMutationTarget struct {
	MutationTarget
}

func newUpdateAPIFixture(t *testing.T, opts UpdateOptions) *updateAPIFixture {
	t.Helper()
	feed := &fakeUpdateFeed{selection: selfupdate.ReleaseSelection{Version: "9.9.9", TagName: "v9.9.9"}}
	opts.Feed = feed
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             nopMutationTarget{},
		Updates:               opts,
	})
	return &updateAPIFixture{handler: handler, feed: feed}
}

func authorizedUpdateRequest(method, path string, body []byte) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func decodeUpdateSnapshot(t *testing.T, resp *http.Response) UpdateSnapshot {
	t.Helper()
	var body UpdateSnapshotResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	return body.Update
}

func eligibleUpdateOptions() UpdateOptions {
	return UpdateOptions{
		Policy:         selfupdate.PolicyNotify,
		Settings:       selfupdate.StartupSettings{Policy: selfupdate.PolicyNotify, Channel: "stable", CheckInterval: 6 * time.Hour, Strategy: "quiesce"},
		CurrentVersion: "1.0.0",
		Eligibility:    selfupdate.Eligibility{Supported: true, Install: selfupdate.InstallTarball},
	}
}

func TestGetUpdateSnapshotRequiresBearer(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())

	req := httptest.NewRequest(http.MethodGet, apiPathUpdate, nil)
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}

	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestGetUpdateSnapshotDisabledDefaultAndNoTraffic(t *testing.T) {
	t.Parallel()
	// A handler without update wiring serves the safe disabled snapshot.
	fixture := newUpdateAPIFixture(t, UpdateOptions{})
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	resp := w.Result()
	defer resp.Body.Close()
	snapshot := decodeUpdateSnapshot(t, resp)
	if snapshot.Status != UpdateSnapshotStatusDisabled {
		t.Fatalf("status = %q, want disabled", snapshot.Status)
	}
	if snapshot.Policy != Off {
		t.Fatalf("policy = %q, want off", snapshot.Policy)
	}
	if fixture.feed.calls() != 0 {
		t.Fatalf("GET triggered %d feed calls, want none", fixture.feed.calls())
	}
}

func TestGetUpdateSnapshotEligibleNotifyIdle(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	resp := w.Result()
	defer resp.Body.Close()
	snapshot := decodeUpdateSnapshot(t, resp)
	if snapshot.Status != UpdateSnapshotStatusIdle {
		t.Fatalf("status = %q, want idle before any check", snapshot.Status)
	}
	if snapshot.Policy != Notify {
		t.Fatalf("policy = %q, want notify", snapshot.Policy)
	}
	if snapshot.Channel != "stable" || snapshot.Strategy != "quiesce" {
		t.Fatalf("channel/strategy = %q/%q", snapshot.Channel, snapshot.Strategy)
	}
	if snapshot.CurrentVersion != "1.0.0" {
		t.Fatalf("current_version = %q", snapshot.CurrentVersion)
	}
	if snapshot.Installation != UpdateSnapshotInstallationTarball {
		t.Fatalf("installation = %q", snapshot.Installation)
	}
	if snapshot.Signature != Unverified {
		t.Fatalf("signature = %q, want unverified", snapshot.Signature)
	}
	if snapshot.TargetVersion != nil {
		t.Fatalf("target_version = %v, want null: no feed target is trusted", *snapshot.TargetVersion)
	}
	if snapshot.LatestVersion != nil {
		t.Fatalf("latest_version = %v, want null before discovery", *snapshot.LatestVersion)
	}
	if snapshot.CheckIntervalSeconds != 21600 {
		t.Fatalf("check_interval_seconds = %d, want 21600", snapshot.CheckIntervalSeconds)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Fatal("snapshot response missing ETag")
	}
	if seq := resp.Header.Get("X-Agentico-Seq"); seq == "" {
		t.Fatal("snapshot response missing X-Agentico-Seq")
	}
	if fixture.feed.calls() != 0 {
		t.Fatal("GET must never trigger a check")
	}
}

func TestGetUpdateSnapshotNotModifiedByRevision(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	first := w.Result()
	revision := strings.Trim(first.Header.Get("ETag"), `"`)
	first.Body.Close()

	req := authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil)
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusNotModified {
		t.Fatalf("revision-matched status = %d, want 304", w.Code)
	}
}

func TestPostUpdateCheckRefusedUnderOffWithoutTraffic(t *testing.T) {
	t.Parallel()
	opts := eligibleUpdateOptions()
	opts.Policy = selfupdate.PolicyOff
	opts.Settings.Policy = selfupdate.PolicyOff
	fixture := newUpdateAPIFixture(t, opts)

	w := httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, []byte(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	fixture.handler.routes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 under off", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.Forbidden) {
		t.Fatalf("code = %q, want forbidden", body.Error.Code)
	}
	if fixture.feed.calls() != 0 {
		t.Fatal("refused check must make no feed request")
	}

	// The disabled snapshot stays visible: off wins for status.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot := decodeUpdateSnapshot(t, w.Result())
	if snapshot.Status != UpdateSnapshotStatusDisabled {
		t.Fatalf("status = %q, want disabled", snapshot.Status)
	}
}

func TestPostUpdateCheckRefusedUnsupportedInstall(t *testing.T) {
	t.Parallel()
	opts := eligibleUpdateOptions()
	opts.Eligibility = selfupdate.Eligibility{
		Supported:   false,
		Install:     selfupdate.InstallAppBundle,
		Reason:      selfupdate.UnsupportedBundled,
		Remediation: "install a standalone binary",
	}
	fixture := newUpdateAPIFixture(t, opts)

	w := httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, []byte(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	fixture.handler.routes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.UpdateUnsupportedInstall) {
		t.Fatalf("code = %q, want update_unsupported_install", body.Error.Code)
	}
	if body.Error.Remediation == nil || body.Error.Remediation.Hint != "install a standalone binary" {
		t.Fatalf("remediation = %+v, want the eligibility remediation", body.Error.Remediation)
	}
	if fixture.feed.calls() != 0 {
		t.Fatal("unsupported installation must make no feed request")
	}

	// The visible status under an enabled policy is unsupported.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot := decodeUpdateSnapshot(t, w.Result())
	if snapshot.Status != UpdateSnapshotStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", snapshot.Status)
	}
	if snapshot.UnsupportedReason == nil || *snapshot.UnsupportedReason != "bundled" {
		t.Fatalf("unsupported_reason = %v", snapshot.UnsupportedReason)
	}
}

func TestPostUpdateCheckAccepted(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())

	w := httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, []byte(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	fixture.handler.routes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	// 202 returns promptly with the current snapshot, before the async
	// worker necessarily finished.
	snapshot := decodeUpdateSnapshot(t, resp)
	if snapshot.Status != UpdateSnapshotStatusIdle && snapshot.Status != UpdateSnapshotStatusChecking {
		t.Fatalf("status = %q, want idle or checking", snapshot.Status)
	}
}

func TestPostUpdateCheckMutationValidation(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())
	post := func(mutate func(*http.Request)) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, []byte(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		mutate(req)
		fixture.handler.routes().ServeHTTP(w, req)
		return w
	}
	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{name: "missing trusted client header", mutate: func(r *http.Request) { r.Header.Del("X-Agentico-Client") }, status: http.StatusForbidden},
		{name: "wrong content type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, status: http.StatusUnsupportedMediaType},
		{name: "unknown field rejected", mutate: func(r *http.Request) { r.Body = ioNopCloser(bytes.NewReader([]byte(`{"force":true}`))) }, status: http.StatusBadRequest},
		{name: "non-object body rejected", mutate: func(r *http.Request) { r.Body = ioNopCloser(bytes.NewReader([]byte(`"check"`))) }, status: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := post(tt.mutate)
			if w.Result().StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if fixture.feed.calls() != 0 {
				t.Fatal("rejected request must make no feed request")
			}
		})
	}

	// Oversized body rejects with 413 through the shared streamed limit.
	w := httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	req.Body = http.NoBody
	req.ContentLength = MaxMutationBodyBytes + 1
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", w.Code)
	}
}

func ioNopCloser(r *bytes.Reader) io.ReadCloser { return io.NopCloser(r) }

func TestPostUpdateCheckRefusedDuringRetryDeadline(t *testing.T) {
	t.Parallel()
	opts := eligibleUpdateOptions()
	fixture := newUpdateAPIFixture(t, opts)
	notBefore := time.Now().UTC().Add(time.Hour)
	feedErr := &selfupdate.FeedError{Reason: "feed rate limited", Retry: true, NotBefore: notBefore}
	fixture.feed.err = feedErr

	// Drive one failed check through the coordinator directly, then stop
	// the scheduler so the refusal below is evaluated against the settled
	// retry deadline.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.handler.updates.start(ctx)
	waitForFeedCalls(t, fixture.feed, 1)
	waitStatus(t, fixture.handler.updates, updateStatusFailed)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	fixture.handler.updates.shutdown(shutdownCtx)

	w := httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPost, apiPathUpdateCheck, []byte(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	fixture.handler.routes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 inside the retry deadline", resp.StatusCode)
	}
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(errcat.UpdateCheckFailed) {
		t.Fatalf("code = %q, want update_check_failed", body.Error.Code)
	}
	// No additional feed request was made by the refused manual check.
	if fixture.feed.calls() != 1 {
		t.Fatalf("feed calls = %d, want 1 (the refused manual check makes no request)", fixture.feed.calls())
	}
}

func TestUpdateSettingsExcludedFromRuntimeConfigREST(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathConfigRuntime, nil))
	resp := w.Result()
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["server"]; ok {
		t.Fatal("runtime config must not expose the server startup section")
	}
	if _, ok := raw["updates"]; ok {
		t.Fatal("runtime config must not expose update startup settings")
	}

	// A write attempt through the runtime-config surface rejects the unknown
	// server section: update startup settings are never REST-writable.
	w = httptest.NewRecorder()
	req := authorizedUpdateRequest(http.MethodPatch, apiPathConfigRuntime, []byte(`{"server":{"updates":{"policy":"notify"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("runtime-config write of server.updates status = %d, want 400", w.Code)
	}
}

func TestUpdateRouteMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodPut, apiPathUpdate, nil))
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", w.Code)
	}
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdateCheck, nil))
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET check status = %d, want 405", w.Code)
	}
}

func TestUpdateEventEmittedOnVisibleChange(t *testing.T) {
	t.Parallel()
	fixture := newUpdateAPIFixture(t, eligibleUpdateOptions())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.handler.updates.start(ctx)
	waitForFeedCalls(t, fixture.feed, 1)
	// The checking and completion transitions each published one
	// update.updated invalidation into the broker's replay ring.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fixture.handler.broker.mu.Lock()
		count := 0
		for _, evt := range fixture.handler.broker.ring {
			if evt.Kind == sseEventUpdateUpdated {
				count++
			}
		}
		fixture.handler.broker.mu.Unlock()
		if count >= 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("update.updated events missing from the broker ring")
}
