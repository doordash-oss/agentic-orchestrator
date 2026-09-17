// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not this file except in compliance with the License.
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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// fakeInstallAdmission is the deterministic InstallAdmission seam: it can
// park WaitForIdle on a gate, close atomically when quiet, refuse or admit
// the stopping closure by held category, and report a one-shot busy
// detection while closed (work that raced in under the update lock).
type fakeInstallAdmission struct {
	mu        sync.Mutex
	closed    bool
	activity  workadmission.Activity
	detectErr error
	held      int
	// heldCategories simulates per-category reservations for the
	// permission-aware stopping checks.
	heldCategories map[workadmission.Category]int
	closeQuiet     bool

	busyOnceWhileClosed bool
	// busyClosedRepository switches the one-shot closed detection from
	// feature activity to repository activity (origin checks).
	busyClosedRepository bool
	busyClosedDetects    int

	waitIdleGate chan struct{}
	waitIdleFrom int
	waitIdleErr  error

	waitCalls     int
	closeCalls    int
	stopCloseWins int
	openCalls     int
	detectCalls   int
}

func (a *fakeInstallAdmission) setWaitIdleGate(gate chan struct{}, from int) {
	a.mu.Lock()
	a.waitIdleGate = gate
	a.waitIdleFrom = from
	a.mu.Unlock()
}

func (a *fakeInstallAdmission) setBusyOnceWhileClosed() {
	a.mu.Lock()
	a.busyOnceWhileClosed = true
	a.mu.Unlock()
}

func (a *fakeInstallAdmission) WaitForIdle(ctx context.Context) error {
	a.mu.Lock()
	a.waitCalls++
	gate, from, err := a.waitIdleGate, a.waitIdleFrom, a.waitIdleErr
	call := a.waitCalls
	a.mu.Unlock()
	if gate != nil && call >= from {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (a *fakeInstallAdmission) CloseIfQuiesced() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closeCalls++
	if !a.closeQuiet || a.closed || a.held > 0 {
		return false
	}
	a.closed = true
	return true
}

func (a *fakeInstallAdmission) CloseForStopping(stoppable ...workadmission.Category) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopCloseWins++
	permitted := make(map[workadmission.Category]bool, len(stoppable))
	for _, cat := range stoppable {
		permitted[cat] = true
	}
	for cat, n := range a.heldCategories {
		if n > 0 && !permitted[cat] {
			return false
		}
	}
	a.closed = true
	return true
}

func (a *fakeInstallAdmission) Open() {
	a.mu.Lock()
	a.closed = false
	a.openCalls++
	a.mu.Unlock()
}

func (a *fakeInstallAdmission) Closed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed
}

func (a *fakeInstallAdmission) Detect(context.Context) (workadmission.Activity, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.detectCalls++
	if a.detectErr != nil {
		return workadmission.Activity{}, a.detectErr
	}
	if a.closed && a.busyOnceWhileClosed && a.busyClosedDetects == 0 {
		a.busyClosedDetects++
		if a.busyClosedRepository {
			return workadmission.Activity{OriginChecks: 1}, nil
		}
		return workadmission.Activity{Features: 1}, nil
	}
	return a.activity, nil
}

func (a *fakeInstallAdmission) Held() (int, map[workadmission.Category]int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	per := make(map[workadmission.Category]int, len(a.heldCategories))
	for cat, n := range a.heldCategories {
		per[cat] = n
	}
	if len(per) == 0 && a.held > 0 {
		// Category-less fakes keep the pre-Phase-6 total-only shape.
		per[workadmission.CategoryFeature] = a.held
	}
	return a.held, per
}

func (a *fakeInstallAdmission) waitCallsN() int { a.mu.Lock(); defer a.mu.Unlock(); return a.waitCalls }
func (a *fakeInstallAdmission) openCallsN() int { a.mu.Lock(); defer a.mu.Unlock(); return a.openCalls }
func (a *fakeInstallAdmission) closeCallsN() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closeCalls
}
func (a *fakeInstallAdmission) detectCallsN() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.detectCalls
}

// fakeReleaseStager stages a canned candidate, optionally blocking on a
// barrier or failing at a chosen pipeline stage so the coordinator's
// download/signature classification is exercised. Cleanup is always owned
// and can fail a configurable number of leading attempts.
type fakeReleaseStager struct {
	mu             sync.Mutex
	callsN         int
	block          chan struct{}
	failStage      string
	err            error
	contract       *selfupdate.ServerContract
	cleanupCallsN  int
	cleanupFailure int
}

func (s *fakeReleaseStager) setBlock(ch chan struct{}) {
	s.mu.Lock()
	s.block = ch
	s.mu.Unlock()
}

func (s *fakeReleaseStager) setFailStage(stage string) {
	s.mu.Lock()
	s.failStage = stage
	s.mu.Unlock()
}

func (s *fakeReleaseStager) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.callsN }
func (s *fakeReleaseStager) cleanupCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cleanupCallsN
}
func (s *fakeReleaseStager) setCleanupFailure(n int) {
	s.mu.Lock()
	s.cleanupFailure = n
	s.mu.Unlock()
}

func (s *fakeReleaseStager) StageCandidate(ctx context.Context, version string, progress func(stage string)) (selfupdate.VerifiedCandidate, *selfupdate.ServerContract, func() error, error) {
	s.mu.Lock()
	s.callsN++
	block, failStage, err, contract := s.block, s.failStage, s.err, s.contract
	s.mu.Unlock()
	cleanup := func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cleanupCallsN++
		if s.cleanupCallsN <= s.cleanupFailure {
			return errors.New("staging cleanup failed")
		}
		return nil
	}
	for _, stage := range []string{"resolve", "download", "verify", "probe", "admit"} {
		progress(stage)
		if failStage == stage {
			if err == nil {
				err = errors.New("staging failed at " + stage)
			}
			return selfupdate.VerifiedCandidate{}, nil, cleanup, err
		}
		if block != nil && stage == "download" {
			select {
			case <-block:
			case <-ctx.Done():
				return selfupdate.VerifiedCandidate{}, nil, cleanup, ctx.Err()
			}
		}
	}
	return selfupdate.VerifiedCandidate{}, contract, cleanup, nil
}

// fakeInstallLifecycle records the process-level replacement sequence and
// can block Begin/Replace on barriers or return configured errors.
type fakeInstallLifecycle struct {
	mu           sync.Mutex
	lockAcquired bool
	lockCalls    int
	beginCalls   int
	beginBlock   chan struct{}
	beginErr     error
	replaceCalls int
	replaceBlock chan struct{}
	replaceErr   error
	notifyStatus []string
	tx           *fakeInstallTransaction
}

func (l *fakeInstallLifecycle) setBeginBlock(ch chan struct{}) {
	l.mu.Lock()
	l.beginBlock = ch
	l.mu.Unlock()
}

func (l *fakeInstallLifecycle) setReplaceBlock(ch chan struct{}) {
	l.mu.Lock()
	l.replaceBlock = ch
	l.mu.Unlock()
}

func (l *fakeInstallLifecycle) setReplaceErr(err error) {
	l.mu.Lock()
	l.replaceErr = err
	l.mu.Unlock()
}

func (l *fakeInstallLifecycle) lockCallsN() int { l.mu.Lock(); defer l.mu.Unlock(); return l.lockCalls }
func (l *fakeInstallLifecycle) beginCallsN() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.beginCalls
}
func (l *fakeInstallLifecycle) replaceCallsN() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.replaceCalls
}
func (l *fakeInstallLifecycle) notifiedStatuses() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.notifyStatus...)
}

func (l *fakeInstallLifecycle) AcquireUpdateLock() (func(), bool) {
	l.mu.Lock()
	l.lockCalls++
	acquired := l.lockAcquired
	l.mu.Unlock()
	return func() {}, acquired
}

func (l *fakeInstallLifecycle) Begin(selfupdate.VerifiedCandidate) (InstallTransaction, error) {
	l.mu.Lock()
	l.beginCalls++
	block, err, tx := l.beginBlock, l.beginErr, l.tx
	l.mu.Unlock()
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	if tx == nil {
		tx = &fakeInstallTransaction{id: "tx-install"}
	}
	return tx, nil
}

func (l *fakeInstallLifecycle) Replace(_ InstallTransaction, notify func(string), _ <-chan struct{}) error {
	l.mu.Lock()
	l.replaceCalls++
	l.notifyStatus = append(l.notifyStatus, updateStatusRestarting)
	block, err := l.replaceBlock, l.replaceErr
	l.mu.Unlock()
	notify(updateStatusRestarting)
	if block != nil {
		<-block
	}
	return err
}

// fakeInstallTransaction records Cancel calls and can fail a configurable
// number of leading attempts (the cleanup-retry journey).
type fakeInstallTransaction struct {
	mu             sync.Mutex
	id             string
	cancelCallsN   int
	cancelFailures int
}

func (t *fakeInstallTransaction) ID() string { return t.id }

func (t *fakeInstallTransaction) Cancel() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cancelCallsN++
	if t.cancelCallsN <= t.cancelFailures {
		return errors.New("transaction cancel failed")
	}
	return nil
}

func (t *fakeInstallTransaction) cancelCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelCallsN
}

func (t *fakeInstallTransaction) setCancelFailures(n int) {
	t.mu.Lock()
	t.cancelFailures = n
	t.mu.Unlock()
}

func newInstallFakes() (*fakeReleaseStager, *fakeInstallLifecycle, *fakeInstallAdmission, *fakeInstallTransaction) {
	stager := &fakeReleaseStager{contract: &selfupdate.ServerContract{APIVersion: 2, SchemaVersion: 3, MinClientSchema: 1}}
	tx := &fakeInstallTransaction{id: "tx-install"}
	lifecycle := &fakeInstallLifecycle{lockAcquired: true, tx: tx}
	admission := &fakeInstallAdmission{closeQuiet: true}
	return stager, lifecycle, admission, tx
}

// installAPIFixture wires a handler whose Updates options carry the install
// fakes, following newUpdateAPIFixture's conventions.
type installAPIFixture struct {
	handler   *apiHandler
	feed      *fakeUpdateFeed
	stager    *fakeReleaseStager
	lifecycle *fakeInstallLifecycle
	admission *fakeInstallAdmission
	tx        *fakeInstallTransaction
}

func newInstallAPIFixture(t *testing.T, opts UpdateOptions) *installAPIFixture {
	t.Helper()
	return newInstallAPIFixtureWithBoundary(t, opts, nil, nil)
}

func newInstallAPIFixtureWithBoundary(t *testing.T, opts UpdateOptions, boundary *workadmission.Coordinator, features FeatureLister) *installAPIFixture {
	t.Helper()
	stager, lifecycle, admission, tx := newInstallFakes()
	feed := &fakeUpdateFeed{selection: selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0"}}
	opts.Feed = feed
	opts.Stager = stager
	opts.Install = lifecycle
	opts.Admission = admission
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             nopMutationTarget{},
		Features:              features,
		Updates:               opts,
		Admission:             boundary,
	})
	return &installAPIFixture{
		handler:   handler,
		feed:      feed,
		stager:    stager,
		lifecycle: lifecycle,
		admission: admission,
		tx:        tx,
	}
}

// discoverLatest drives one synchronous metadata check so the coordinator
// has a pinned latest release without starting the scheduler loop.
func (f *installAPIFixture) discoverLatest(t *testing.T) {
	t.Helper()
	f.handler.updates.performCheck(context.Background(), "explicit")
}

func trustedInstallRequest(method string, body []byte) *http.Request {
	req := authorizedUpdateRequest(method, apiPathUpdateInstall, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	return req
}

func decodeInstallError(t *testing.T, resp *http.Response) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	return body
}

func waitInstallCond(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

func installOpCleared(c *updateCoordinator) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.install == nil
}

func installOpStatus(c *updateCoordinator) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.install == nil {
		return ""
	}
	return c.install.status
}

func installDrainEntered(c *updateCoordinator) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.install != nil && c.install.drainEntered
}

func installCancelling(c *updateCoordinator) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.install != nil && c.install.cancelling
}

func TestUpdateInstallRouteRequiresBearer(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)

	req := httptest.NewRequest(http.MethodPost, apiPathUpdateInstall, bytes.NewReader([]byte(`{"consent":true,"when":"idle"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", w.Code)
	}

	// Authenticated but without the trusted local client header: 403 before
	// any install machinery runs.
	w = httptest.NewRecorder()
	req = authorizedUpdateRequest(http.MethodPost, apiPathUpdateInstall, []byte(`{"consent":true,"when":"idle"}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.handler.routes().ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusForbidden {
		t.Fatalf("untrusted status = %d, want 403", w.Code)
	}
}

func TestUpdateInstallPostRequestValidation(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(body)))
		return w
	}
	tests := []struct {
		name string
		body string
		code errcat.Code
	}{
		{name: "missing consent", body: `{"when":"now"}`, code: errcat.UpdateConsentRequired},
		{name: "false consent", body: `{"consent":false,"when":"now"}`, code: errcat.UpdateConsentRequired},
		{name: "invalid when", body: `{"consent":true,"when":"later"}`, code: errcat.BadRequest},
		{name: "stop_active_work with idle", body: `{"consent":true,"when":"idle","stop_active_work":true}`, code: errcat.BadRequest},
		{name: "blank version selector", body: `{"consent":true,"when":"now","version":"  "}`, code: errcat.BadRequest},
		{name: "oversized version selector", body: `{"consent":true,"when":"now","version":"` + strings.Repeat("1", 65) + `"}`, code: errcat.BadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := post(tt.body)
			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			body := decodeInstallError(t, resp)
			if body.Error.Code != string(tt.code) {
				t.Fatalf("code = %q, want %q", body.Error.Code, tt.code)
			}
		})
	}
}

func TestUpdateInstallPostRefusedDisabledPolicy(t *testing.T) {
	t.Parallel()
	opts := eligibleUpdateOptions()
	opts.Policy = selfupdate.PolicyOff
	opts.Settings.Policy = selfupdate.PolicyOff
	fixture := newInstallAPIFixture(t, opts)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := decodeInstallError(t, resp)
	if body.Error.Code != string(errcat.Forbidden) {
		t.Fatalf("code = %q, want forbidden (not a update_disabled code)", body.Error.Code)
	}
}

func TestUpdateInstallPostRefusedUnsupportedInstall(t *testing.T) {
	t.Parallel()
	opts := eligibleUpdateOptions()
	opts.Eligibility = selfupdate.Eligibility{
		Supported:   false,
		Install:     selfupdate.InstallAppBundle,
		Reason:      selfupdate.UnsupportedBundled,
		Remediation: "install a standalone binary",
	}
	fixture := newInstallAPIFixture(t, opts)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeInstallError(t, resp)
	if body.Error.Code != string(errcat.UpdateUnsupportedInstall) {
		t.Fatalf("code = %q, want update_unsupported_install", body.Error.Code)
	}
}

func TestUpdateInstallPostFeedUnavailable(t *testing.T) {
	t.Parallel()
	// The install seam needs a feed, stager, lifecycle, and admission; a
	// runtime without a feed refuses with 503 before any staging work.
	stager, lifecycle, admission, _ := newInstallFakes()
	opts := eligibleUpdateOptions()
	opts.Stager = stager
	opts.Install = lifecycle
	opts.Admission = admission
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             nopMutationTarget{},
		Updates:               opts,
	})

	w := httptest.NewRecorder()
	handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body := decodeInstallError(t, resp)
	if body.Error.Code != string(errcat.Unavailable) {
		t.Fatalf("code = %q, want unavailable", body.Error.Code)
	}
}

func TestUpdateInstallPostTargetRefusals(t *testing.T) {
	t.Parallel()
	t.Run("no discovery yet", func(t *testing.T) {
		t.Parallel()
		fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		body := decodeInstallError(t, resp)
		if body.Error.Code != string(errcat.Conflict) {
			t.Fatalf("code = %q, want conflict", body.Error.Code)
		}
		if !strings.Contains(body.Error.Diagnostics, "no release has been discovered") {
			t.Fatalf("diagnostics = %q", body.Error.Diagnostics)
		}
	})

	t.Run("version selector is not the latest", func(t *testing.T) {
		t.Parallel()
		fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
		fixture.discoverLatest(t)
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle","version":"1.5.0"}`)))
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		body := decodeInstallError(t, resp)
		if body.Error.Code != string(errcat.Conflict) {
			t.Fatalf("code = %q, want conflict", body.Error.Code)
		}
	})

	t.Run("latest is not newer than current", func(t *testing.T) {
		t.Parallel()
		fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
		fixture.feed.mu.Lock()
		fixture.feed.selection = selfupdate.ReleaseSelection{Version: "1.0.0", TagName: "v1.0.0"}
		fixture.feed.mu.Unlock()
		fixture.discoverLatest(t)
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		body := decodeInstallError(t, resp)
		if body.Error.Code != string(errcat.Conflict) {
			t.Fatalf("code = %q, want conflict", body.Error.Code)
		}
		if !strings.Contains(body.Error.Diagnostics, "not newer") {
			t.Fatalf("diagnostics = %q", body.Error.Diagnostics)
		}
	})

	t.Run("suppressed target", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		binary := dir + "/agentico"
		if err := writeFileTest(binary, "#!/bin/sh\n", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := selfupdate.SuppressTarget(binary, selfupdate.SuppressedTarget{
			Version: "2.0.0", TransactionID: strings.Repeat("a", 32), SuppressedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		opts := eligibleUpdateOptions()
		opts.ExecPath = binary
		fixture := newInstallAPIFixture(t, opts)
		fixture.discoverLatest(t)
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
		resp := w.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("status = %d, want 409", resp.StatusCode)
		}
		body := decodeInstallError(t, resp)
		if body.Error.Code != string(errcat.UpdateRolledBack) {
			t.Fatalf("code = %q, want update_rolled_back", body.Error.Code)
		}
	})
}

func TestUpdateInstallPostAcceptedIdleSnapshot(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	// Parking the stager keeps the accepted operation observably in
	// downloading for the 202 snapshot.
	fixture.stager.setBlock(make(chan struct{}))
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"scheduled_for":null`) {
		t.Fatalf("response must carry an explicit null scheduled_for: %s", raw)
	}
	var body UpdateSnapshotResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	snapshot := body.Update
	if snapshot.Status != UpdateSnapshotStatusDownloading {
		t.Fatalf("status = %q, want downloading (or later) while staging runs", snapshot.Status)
	}
	if snapshot.Method == nil || *snapshot.Method != UpdateSnapshotMethodIdle {
		t.Fatalf("method = %v, want idle", snapshot.Method)
	}
	if snapshot.StopActiveWork == nil || *snapshot.StopActiveWork {
		t.Fatalf("stop_active_work = %v, want false", snapshot.StopActiveWork)
	}
	if snapshot.TargetVersion == nil || *snapshot.TargetVersion != "2.0.0" {
		t.Fatalf("target_version = %v, want 2.0.0", snapshot.TargetVersion)
	}
	if snapshot.Signature != Unverified {
		t.Fatalf("signature = %q, want unverified before verification", snapshot.Signature)
	}
	if snapshot.ActiveWorkSummary.FeatureCount != 0 || snapshot.ActiveWorkSummary.ChatActive ||
		snapshot.ActiveWorkSummary.CloneCount != 0 || snapshot.ActiveWorkSummary.UploadCount != 0 ||
		snapshot.ActiveWorkSummary.OriginCheckCount != 0 || snapshot.ActiveWorkSummary.PendingAdmissions != 0 {
		t.Fatalf("active_work_summary = %+v, want zero counts", snapshot.ActiveWorkSummary)
	}
	if snapshot.ActiveWorkSummary.DetectionFailed {
		t.Fatal("detection_failed must be false with no boundary configured")
	}

	// Cancellation settles the parked operation and returns the snapshot to
	// availability.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	resp = w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", resp.StatusCode)
	}
	if got := fixture.stager.cleanupCalls(); got != 1 {
		t.Fatalf("cleanup calls = %d, want 1", got)
	}
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	snapshot = decodeUpdateSnapshot(t, w.Result())
	if snapshot.Status != UpdateSnapshotStatusAvailable {
		t.Fatalf("post-cancel status = %q, want available", snapshot.Status)
	}
}

func TestUpdateInstallPostCoalescesEquivalentRequests(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.stager.setBlock(make(chan struct{}))
	fixture.discoverLatest(t)

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(body)))
		return w
	}

	w := post(`{"consent":true,"when":"now"}`)
	if w.Result().StatusCode != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", w.Code)
	}
	// Equivalent retries — with and without a matching version selector —
	// return the same operation and never launch a second worker.
	for _, body := range []string{
		`{"consent":true,"when":"now"}`,
		`{"consent":true,"when":"now","version":"2.0.0"}`,
	} {
		w = post(body)
		resp := w.Result()
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("coalesced status = %d, want 202", resp.StatusCode)
		}
	}
	// The accepted worker may not have been scheduled onto the blocked
	// stager yet: wait for the single worker to reach it before asserting
	// no second worker launched.
	waitInstallCond(t, 5*time.Second, func() bool { return fixture.stager.calls() >= 1 }, "accepted worker never reached the stager")
	if got := fixture.stager.calls(); got != 1 {
		t.Fatalf("stager calls = %d, want exactly one worker", got)
	}
	// Changing the waiting method, stop permission, or pinned target is a
	// conflict that requires cancellation first.
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "different when", body: `{"consent":true,"when":"idle"}`},
		{name: "different stop permission", body: `{"consent":true,"when":"now","stop_active_work":true}`},
		{name: "different version", body: `{"consent":true,"when":"now","version":"1.5.0"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := post(tt.body)
			resp := w.Result()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", resp.StatusCode)
			}
			if body := decodeInstallError(t, resp); body.Error.Code != string(errcat.Conflict) {
				t.Fatalf("code = %q, want conflict", body.Error.Code)
			}
		})
	}
	if got := fixture.stager.calls(); got != 1 {
		t.Fatalf("stager calls after conflicts = %d, want 1", got)
	}

	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", w.Code)
	}
}

func TestUpdateInstallPostNowBlockedByActiveWork(t *testing.T) {
	t.Parallel()
	// The handler-owned detectors feed the real boundary: one running
	// feature refuses an immediate install before staging.
	busyFeatures := featureListerFunc(func() ([]*feature.Feature, error) {
		return []*feature.Feature{{ID: "feat-1", Status: feature.StatusImplementing}}, nil
	})
	fixture := newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), busyFeatures)
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"now"}`)))
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	body := decodeInstallError(t, resp)
	if body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("code = %q, want update_blocked_active_work", body.Error.Code)
	}
	if blockers := fixture.handler.updates.installRequestBlockers(context.Background(), fixture.handler.admission, false); blockers == nil {
		t.Fatal("request-time blockers must report the busy feature")
	}
	if got := fixture.stager.calls(); got != 0 {
		t.Fatalf("stager calls = %d, want none for a refused immediate install", got)
	}

	// An idle boundary with no observed work admits the request-time check.
	quietFixture := newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), nil)
	quietFixture.discoverLatest(t)
	if blockers := quietFixture.handler.updates.installRequestBlockers(context.Background(), quietFixture.handler.admission, false); blockers != nil {
		t.Fatalf("quiet runtime blockers = %v, want nil", blockers)
	}

	// Failed detection never reads as zero activity.
	failingFeatures := featureListerFunc(func() ([]*feature.Feature, error) {
		return nil, errors.New("store unreadable")
	})
	failingFixture := newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), failingFeatures)
	failingFixture.discoverLatest(t)
	if blockers := failingFixture.handler.updates.installRequestBlockers(context.Background(), failingFixture.handler.admission, false); blockers == nil {
		t.Fatal("detection failure must block an immediate install")
	}
	w = httptest.NewRecorder()
	failingFixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"now"}`)))
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("detection-failure status = %d, want 409", resp.StatusCode)
	}
	if body := decodeInstallError(t, resp); body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("detection-failure code = %q, want update_blocked_active_work", body.Error.Code)
	}
}

func TestUpdateInstallDeleteWithoutOperation(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)
	for range 2 {
		w := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
		resp := w.Result()
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("idempotent cancel status = %d, want 200", resp.StatusCode)
		}
	}
}

func TestUpdateInstallRouteMethodNotAllowed(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)
	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodPut, apiPathUpdateInstall, nil))
	resp := w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "DELETE") {
		t.Fatalf("Allow = %q, want POST and DELETE", allow)
	}
}

func TestUpdateInstallMutationNeverNotModified(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.stager.setBlock(make(chan struct{}))
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
	first := w.Result()
	revision := strings.Trim(first.Header.Get("ETag"), `"`)
	first.Body.Close()

	// A carried If-None-Match can never turn an accepted install mutation
	// into a 304: conditional-GET handling applies to reads only.
	req := trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`))
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, req)
	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("install status = %d, want 202 (never 304)", resp.StatusCode)
	}
	snapshot := decodeUpdateSnapshot(t, resp)
	if snapshot.Status != UpdateSnapshotStatusDownloading {
		t.Fatalf("status = %q, want downloading", snapshot.Status)
	}

	req = trustedInstallRequest(http.MethodDelete, []byte(`{}`))
	req.Header.Set("If-None-Match", `"`+revision+`"`)
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, req)
	resp = w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200 (never 304)", resp.StatusCode)
	}
}

func TestUpdateInstallEventEmittedOnVisibleTransitions(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)
	countEvents := func() int {
		fixture.handler.broker.mu.Lock()
		defer fixture.handler.broker.mu.Unlock()
		count := 0
		for _, evt := range fixture.handler.broker.ring {
			if evt.Kind == sseEventUpdateUpdated {
				count++
			}
		}
		return count
	}
	discoveryEvents := countEvents()
	if discoveryEvents < 2 {
		t.Fatalf("discovery events = %d, want the checking and completion invalidations", discoveryEvents)
	}

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	// The install's downloading, verified, and scheduled transitions each
	// publish one update.updated invalidation.
	waitInstallCond(t, 5*time.Second, func() bool { return countEvents() >= discoveryEvents+3 },
		"install transitions did not publish update.updated events")
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(fixture.handler.updates) },
		"install operation never settled")
}

func newInstallTestCoordinator(t *testing.T, stager *fakeReleaseStager, lifecycle *fakeInstallLifecycle, admission *fakeInstallAdmission) (*updateCoordinator, *fakeUpdateFeed, *transitionRecorder) {
	t.Helper()
	coordinator, _, feed, recorder := newTestCoordinator(t, UpdateOptions{
		Stager:    stager,
		Install:   lifecycle,
		Admission: admission,
	})
	return coordinator, feed, recorder
}

func TestUpdateInstallWorkerTransitionsToScheduled(t *testing.T) {
	t.Parallel()
	stager, lifecycle, admission, _ := newInstallFakes()
	gate := make(chan struct{})
	admission.setWaitIdleGate(gate, 1)
	lifecycle.setReplaceErr(errors.New("replacement returned early"))
	coordinator, _, recorder := newInstallTestCoordinator(t, stager, lifecycle, admission)
	coordinator.performCheck(context.Background(), "initial")
	waitStatus(t, coordinator, updateStatusAvailable)

	if _, refusal := coordinator.requestInstall(installRequest{consent: true, when: updateInstallWhenIdle}, nil); refusal != nil {
		t.Fatalf("idle install refused: %v", refusal)
	}
	waitStatus(t, coordinator, updateStatusScheduled)

	// The staged candidate is verified and its contract published while the
	// operation waits for idle.
	snapshot := coordinator.Snapshot()
	if snapshot.Signature != Verified {
		t.Fatalf("signature = %q, want verified after staging", snapshot.Signature)
	}
	if snapshot.TargetContract == nil || snapshot.TargetContract.APIVersion != "2" ||
		snapshot.TargetContract.SchemaVersion != 3 || snapshot.TargetContract.MinClientSchema != 1 {
		t.Fatalf("target_contract = %+v, want the stager's verified contract", snapshot.TargetContract)
	}
	if snapshot.TargetVersion == nil || *snapshot.TargetVersion != "2.0.0" {
		t.Fatalf("target_version = %v, want 2.0.0", snapshot.TargetVersion)
	}

	_, _, observed := recorder.snapshot()
	want := []observedUpdate{
		{from: updateStatusDownloading, to: updateStatusDownloading, result: "install_started:idle"},
		{from: updateStatusDownloading, to: updateStatusVerified, result: "install_verified"},
		{from: updateStatusVerified, to: updateStatusScheduled, result: "install_scheduled"},
	}
	for _, w := range want {
		found := false
		for _, o := range observed {
			if o == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("transition %+v missing from %v", w, observed)
		}
	}
	if recorder.eventCount() < 3 {
		t.Fatalf("publish events = %d, want one per visible transition", recorder.eventCount())
	}

	close(gate)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want at least 1", got)
	}
}

func TestUpdateInstallIdleFlowDrainsAndReplaces(t *testing.T) {
	t.Parallel()
	stager, lifecycle, admission, tx := newInstallFakes()
	gate := make(chan struct{})
	admission.setWaitIdleGate(gate, 1)
	replaceBlock := make(chan struct{})
	lifecycle.setReplaceBlock(replaceBlock)
	coordinator, _, recorder := newInstallTestCoordinator(t, stager, lifecycle, admission)
	coordinator.performCheck(context.Background(), "initial")
	if _, refusal := coordinator.requestInstall(installRequest{consent: true, when: updateInstallWhenIdle}, nil); refusal != nil {
		t.Fatalf("idle install refused: %v", refusal)
	}
	waitStatus(t, coordinator, updateStatusScheduled)

	// The worker parks in the idle wait; releasing it closes the boundary,
	// takes the update lock, and begins the durable transaction.
	waitInstallCond(t, 5*time.Second, func() bool { return admission.waitCallsN() >= 1 }, "worker never waited for idle")
	close(gate)
	waitInstallCond(t, 5*time.Second, func() bool {
		status := installOpStatus(coordinator)
		return status == updateStatusDraining || status == updateStatusRestarting
	}, "drain never began")
	if got := lifecycle.lockCallsN(); got != 1 {
		t.Fatalf("update-lock acquisitions = %d, want 1", got)
	}
	if got := lifecycle.beginCallsN(); got != 1 {
		t.Fatalf("Begin calls = %d, want 1", got)
	}
	_, _, observed := recorder.snapshot()
	draining := observedUpdate{from: updateStatusScheduled, to: updateStatusDraining, result: "install_draining"}
	found := false
	for _, o := range observed {
		if o == draining {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("draining transition missing from %v", observed)
	}

	// Replace runs with the restart notification and blocks in the final
	// drain until released.
	waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() >= 1 }, "Replace never called")
	if notified := lifecycle.notifiedStatuses(); len(notified) == 0 || notified[0] != updateStatusRestarting {
		t.Fatalf("notified statuses = %v, want restarting", notified)
	}
	waitStatus(t, coordinator, updateStatusRestarting)

	close(replaceBlock)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed {
		t.Fatalf("status = %q, want failed after a returned replacement", snapshot.Status)
	}
	if snapshot.Error == nil || snapshot.Error.Code != string(errcat.UpdateInstallFailed) {
		t.Fatalf("error = %+v, want update_install_failed", snapshot.Error)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want at least 1", got)
	}
	if got := tx.cancelCalls(); got < 1 {
		t.Fatalf("transaction cancels = %d, want the begun transaction settled", got)
	}
	if got := admission.openCallsN(); got < 1 {
		t.Fatalf("Open calls = %d, want admission reopened after the aborted drain", got)
	}
}

func TestUpdateInstallBlockerUnderLockReschedulesIdleOperation(t *testing.T) {
	t.Parallel()
	stager, lifecycle, admission, _ := newInstallFakes()
	gate := make(chan struct{})
	admission.setWaitIdleGate(gate, 2)
	// Work races in under the update lock: the first detection observed
	// while the boundary is closed reports it once.
	admission.setBusyOnceWhileClosed()
	lifecycle.setReplaceErr(errors.New("replacement returned early"))
	coordinator, _, _ := newInstallTestCoordinator(t, stager, lifecycle, admission)
	coordinator.performCheck(context.Background(), "initial")
	if _, refusal := coordinator.requestInstall(installRequest{consent: true, when: updateInstallWhenIdle}, nil); refusal != nil {
		t.Fatalf("idle install refused: %v", refusal)
	}
	waitStatus(t, coordinator, updateStatusScheduled)

	// The blocker under the lock reopens admission and parks the operation
	// back in the idle wait without consuming Begin.
	waitInstallCond(t, 5*time.Second, func() bool {
		return admission.openCallsN() >= 1 && admission.waitCallsN() >= 2
	}, "worker never reevaluated after the under-lock blocker")
	if admission.Closed() {
		t.Fatal("admission must be reopened after the blocker")
	}
	if status := installOpStatus(coordinator); status != updateStatusScheduled {
		t.Fatalf("status = %q, want scheduled again", status)
	}
	if got := lifecycle.beginCallsN(); got != 0 {
		t.Fatalf("Begin calls = %d, want 0 until idle again", got)
	}

	// Once the racer settles, the same operation drains and replaces.
	close(gate)
	waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.beginCallsN() >= 1 }, "Begin never called after idle")
	if got := lifecycle.beginCallsN(); got != 1 {
		t.Fatalf("Begin calls = %d, want exactly 1", got)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
}

func TestUpdateInstallCancelDuringStaging(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.stager.setBlock(make(chan struct{}))
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	// Cancellation interrupts the parked stager, settles its owned cleanup,
	// and waits for the worker before answering.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d, want 200", resp.StatusCode)
	}
	snapshot := decodeUpdateSnapshot(t, resp)
	if snapshot.Status != UpdateSnapshotStatusAvailable {
		t.Fatalf("status = %q, want availability restored", snapshot.Status)
	}
	if snapshot.Method != nil || snapshot.TargetVersion != nil {
		t.Fatalf("operation fields survived cancellation: %+v", snapshot)
	}
	if got := fixture.stager.cleanupCalls(); got != 1 {
		t.Fatalf("cleanup calls = %d, want exactly 1", got)
	}
	if !installOpCleared(fixture.handler.updates) {
		t.Fatal("operation must be cleared after cancellation")
	}

	// A second cancellation is idempotent.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	resp = w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second cancel status = %d, want 200", resp.StatusCode)
	}
	if got := fixture.stager.cleanupCalls(); got != 1 {
		t.Fatalf("cleanup calls after second cancel = %d, want 1 (idempotent)", got)
	}
}

func TestUpdateInstallCancelAfterDrainRefused(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	replaceBlock := make(chan struct{})
	fixture.lifecycle.setReplaceBlock(replaceBlock)
	fixture.discoverLatest(t)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
	resp := w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installDrainEntered(fixture.handler.updates) }, "drain never entered")

	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel status = %d, want 409 after drain entry", resp.StatusCode)
	}
	body := decodeInstallError(t, resp)
	if body.Error.Code != string(errcat.UpdateInProgress) {
		t.Fatalf("code = %q, want update_in_progress", body.Error.Code)
	}
	if got := fixture.lifecycle.replaceCallsN(); got != 1 {
		t.Fatalf("Replace calls = %d, want the replacement to proceed", got)
	}

	// The refused cancellation leaves the drain owner in control; releasing
	// it settles the truthful failure.
	close(replaceBlock)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(fixture.handler.updates) }, "operation never settled")
	snapshot := fixture.handler.updates.Snapshot()
	if snapshot.Error == nil || snapshot.Error.Code != string(errcat.UpdateInstallFailed) {
		t.Fatalf("error = %+v, want update_install_failed after the returned replacement", snapshot.Error)
	}
}

func TestUpdateInstallStagerFailureClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		stage string
		code  errcat.Code
	}{
		{stage: "download", code: errcat.UpdateDownloadFailed},
		{stage: "verify", code: errcat.UpdateSignatureFailed},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			t.Parallel()
			fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
			fixture.stager.setFailStage(tt.stage)
			fixture.discoverLatest(t)

			w := httptest.NewRecorder()
			fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
			resp := w.Result()
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", resp.StatusCode)
			}
			waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(fixture.handler.updates) }, "failed operation never settled")

			w = httptest.NewRecorder()
			fixture.handler.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
			snapshot := decodeUpdateSnapshot(t, w.Result())
			if snapshot.Status != UpdateSnapshotStatusFailed {
				t.Fatalf("status = %q, want failed", snapshot.Status)
			}
			if snapshot.Error == nil || snapshot.Error.Code != string(tt.code) {
				t.Fatalf("error = %+v, want %q", snapshot.Error, tt.code)
			}
			if !strings.Contains(snapshot.Error.Summary, "2.0.0") {
				t.Fatalf("summary = %q, want the target version named", snapshot.Error.Summary)
			}
			if got := fixture.stager.cleanupCalls(); got != 1 {
				t.Fatalf("cleanup calls = %d, want 1", got)
			}
			if got := fixture.admission.openCallsN(); got < 1 {
				t.Fatalf("Open calls = %d, want admission reopened after the failure", got)
			}

			// A failed install requires fresh consent: a new request is
			// accepted again.
			fixture.stager.setFailStage("")
			w = httptest.NewRecorder()
			fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
			resp = w.Result()
			resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("fresh install status = %d, want 202", resp.StatusCode)
			}
			waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(fixture.handler.updates) }, "fresh operation never settled")
		})
	}
}

func TestUpdateInstallCleanupFailureRetainedAndRetried(t *testing.T) {
	t.Parallel()
	fixture := newInstallAPIFixture(t, eligibleUpdateOptions())
	fixture.discoverLatest(t)
	// The transaction cancel fails for the worker's settle and the first
	// cancellation retry; the repeated cancellation then succeeds.
	fixture.tx.setCancelFailures(2)
	beginBlock := make(chan struct{})
	fixture.lifecycle.setBeginBlock(beginBlock)

	w := httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"now"}`)))
	resp := w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return fixture.lifecycle.beginCallsN() >= 1 }, "Begin never entered")

	// The first cancellation interrupts Begin; the worker settles with a
	// failed transaction cancel and the cancellation's own retry fails too.
	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		dw := httptest.NewRecorder()
		fixture.handler.routes().ServeHTTP(dw, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
		deleteDone <- dw
	}()
	waitInstallCond(t, 5*time.Second, func() bool { return installCancelling(fixture.handler.updates) }, "cancellation never started")
	close(beginBlock)

	select {
	case dw := <-deleteDone:
		resp := dw.Result()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("first cancel status = %d, want 409 while cleanup keeps failing", resp.StatusCode)
		}
		if body := decodeInstallError(t, resp); body.Error.Code != string(errcat.UpdateInstallFailed) {
			t.Fatalf("first cancel code = %q, want update_install_failed", body.Error.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first cancellation never settled")
	}
	if installOpCleared(fixture.handler.updates) {
		t.Fatal("operation with failed cleanup must be retained")
	}

	// A new install is refused while the failed cleanup holds the machinery.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"now"}`)))
	resp = w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("new install status = %d, want 409", resp.StatusCode)
	}
	if body := decodeInstallError(t, resp); body.Error.Code != string(errcat.UpdateInstallFailed) {
		t.Fatalf("new install code = %q, want update_install_failed", body.Error.Code)
	}

	// The repeated DELETE retries the idempotent cleanup, succeeds, and
	// clears the retained operation.
	w = httptest.NewRecorder()
	fixture.handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodDelete, []byte(`{}`)))
	resp = w.Result()
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry cancel status = %d, want 200", resp.StatusCode)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(fixture.handler.updates) }, "retained operation never cleared")
	if got := fixture.tx.cancelCalls(); got != 3 {
		t.Fatalf("transaction cancels = %d, want worker settle + failed retry + successful retry", got)
	}
}

func TestUpdateInstallShutdownJoinsPreDrainAndDefersToDrain(t *testing.T) {
	t.Parallel()
	// Pre-drain: shutdown cancels the worker and joins it.
	stager, lifecycle, admission, _ := newInstallFakes()
	stager.setBlock(make(chan struct{}))
	coordinator, _, _ := newInstallTestCoordinator(t, stager, lifecycle, admission)
	coordinator.performCheck(context.Background(), "initial")
	if _, refusal := coordinator.requestInstall(installRequest{consent: true, when: updateInstallWhenNow}, nil); refusal != nil {
		t.Fatalf("install refused: %v", refusal)
	}
	coordinator.mu.Lock()
	op := coordinator.install
	coordinator.mu.Unlock()
	if op == nil {
		t.Fatal("operation missing before shutdown")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	coordinator.shutdown(shutdownCtx)
	select {
	case <-op.done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join the cancelled install worker")
	}
	if !installOpCleared(coordinator) {
		t.Fatal("cancelled operation must be cleared by shutdown")
	}
	if got := stager.cleanupCalls(); got != 1 {
		t.Fatalf("cleanup calls = %d, want 1", got)
	}

	// Drain-entered: the operation owns shutdown and is left in control.
	stager2, lifecycle2, admission2, _ := newInstallFakes()
	replaceBlock := make(chan struct{})
	lifecycle2.setReplaceBlock(replaceBlock)
	coordinator2, _, _ := newInstallTestCoordinator(t, stager2, lifecycle2, admission2)
	coordinator2.performCheck(context.Background(), "initial")
	if _, refusal := coordinator2.requestInstall(installRequest{consent: true, when: updateInstallWhenIdle}, nil); refusal != nil {
		t.Fatalf("idle install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installDrainEntered(coordinator2) }, "drain never entered")
	shutdownCtx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	coordinator2.shutdown(shutdownCtx2)
	if installOpCleared(coordinator2) {
		t.Fatal("ordinary shutdown must not cancel a drain-entered operation")
	}
	if got := lifecycle2.replaceCallsN(); got != 1 {
		t.Fatalf("Replace calls = %d, want the drain owner still in control", got)
	}
	coordinator2.mu.Lock()
	op2 := coordinator2.install
	coordinator2.mu.Unlock()
	close(replaceBlock)
	select {
	case <-op2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("drain-entered worker never settled after release")
	}
}

func TestUpdateInstallCheckDuringOperationKeepsTargetPinned(t *testing.T) {
	t.Parallel()
	stager, lifecycle, admission, _ := newInstallFakes()
	gate := make(chan struct{})
	admission.setWaitIdleGate(gate, 1)
	lifecycle.setReplaceErr(errors.New("replacement returned early"))
	coordinator, feed, _ := newInstallTestCoordinator(t, stager, lifecycle, admission)
	coordinator.performCheck(context.Background(), "initial")
	waitStatus(t, coordinator, updateStatusAvailable)

	if _, refusal := coordinator.requestInstall(installRequest{consent: true, when: updateInstallWhenIdle}, nil); refusal != nil {
		t.Fatalf("idle install refused: %v", refusal)
	}
	waitStatus(t, coordinator, updateStatusScheduled)

	// A later check discovers a newer release; the pinned target stays on
	// the accepted operation while latest_version refreshes.
	feed.mu.Lock()
	feed.selection = selfupdate.ReleaseSelection{Version: "3.0.0", TagName: "v3.0.0"}
	feed.mu.Unlock()
	coordinator.performCheck(context.Background(), "explicit")
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusScheduled {
		t.Fatalf("status = %q, want the operation's scheduled status", snapshot.Status)
	}
	if snapshot.LatestVersion == nil || *snapshot.LatestVersion != "3.0.0" {
		t.Fatalf("latest_version = %v, want the refreshed 3.0.0", snapshot.LatestVersion)
	}
	if snapshot.TargetVersion == nil || *snapshot.TargetVersion != "2.0.0" {
		t.Fatalf("target_version = %v, want the pinned 2.0.0", snapshot.TargetVersion)
	}

	close(gate)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
}
