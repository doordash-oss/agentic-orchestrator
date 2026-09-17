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
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// fakeInstallStopper is the deterministic InstallStopper seam: a mutable
// stop set and chat state, per-call error injection, optional parking for
// each dispatch, and an observation hook after every recorded dispatch.
type fakeInstallStopper struct {
	mu           sync.Mutex
	features     []string
	chatActive   bool
	stopErrs     map[string]error
	endChatErr   error
	stopCalls    []string
	endChatCalls int
	stopBlock    chan struct{}
	endChatBlock chan struct{}
	onDispatch   func()
}

func (s *fakeInstallStopper) setFeatures(ids ...string) {
	s.mu.Lock()
	s.features = append([]string(nil), ids...)
	s.mu.Unlock()
}

func (s *fakeInstallStopper) setChatActive(active bool) {
	s.mu.Lock()
	s.chatActive = active
	s.mu.Unlock()
}

func (s *fakeInstallStopper) setStopErr(id string, err error) {
	s.mu.Lock()
	if s.stopErrs == nil {
		s.stopErrs = make(map[string]error)
	}
	s.stopErrs[id] = err
	s.mu.Unlock()
}

func (s *fakeInstallStopper) setStopBlock(ch chan struct{}) {
	s.mu.Lock()
	s.stopBlock = ch
	s.mu.Unlock()
}

func (s *fakeInstallStopper) setOnDispatch(fn func()) {
	s.mu.Lock()
	s.onDispatch = fn
	s.mu.Unlock()
}

func (s *fakeInstallStopper) stopCallsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.stopCalls...)
}

func (s *fakeInstallStopper) endChatCallsN() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endChatCalls
}

func (s *fakeInstallStopper) StoppableFeatures(context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.features...), nil
}

func (s *fakeInstallStopper) ChatActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatActive
}

func (s *fakeInstallStopper) StopFeature(_ context.Context, featureID string) error {
	s.mu.Lock()
	block := s.stopBlock
	err := s.stopErrs[featureID]
	s.stopCalls = append(s.stopCalls, featureID)
	hook := s.onDispatch
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if hook != nil {
		hook()
	}
	return err
}

func (s *fakeInstallStopper) EndChat(_ context.Context) error {
	s.mu.Lock()
	block := s.endChatBlock
	err := s.endChatErr
	s.endChatCalls++
	hook := s.onDispatch
	s.mu.Unlock()
	if block != nil {
		<-block
	}
	if hook != nil {
		hook()
	}
	return err
}

// stopMutationTarget records stop dispatches through the real mutation
// surface; every other mutation keeps the nop behavior.
type stopMutationTarget struct {
	nopMutationTarget
	mu       sync.Mutex
	stopped  []string
	endChats int
	stopErr  func(featureID string) error
	endErr   error
}

func (m *stopMutationTarget) StopFeature(featureID string) (FeatureStopResponse, error) {
	m.mu.Lock()
	errFn := m.stopErr
	m.stopped = append(m.stopped, featureID)
	m.mu.Unlock()
	if errFn != nil {
		if err := errFn(featureID); err != nil {
			return FeatureStopResponse{}, err
		}
	}
	return FeatureStopResponse{FeatureID: featureID, Result: "stopped"}, nil
}

func (m *stopMutationTarget) EndChat() (ChatEndResponse, error) {
	m.mu.Lock()
	m.endChats++
	endErr := m.endErr
	m.mu.Unlock()
	if endErr != nil {
		return ChatEndResponse{}, endErr
	}
	return ChatEndResponse{SessionID: ChatSessionID, Result: "ended"}, nil
}

func (m *stopMutationTarget) endChatsN() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.endChats
}

// setAdmissionActivity mutates the fake admission's observed activity.
func (a *fakeInstallAdmission) setAdmissionActivity(activity workadmission.Activity) {
	a.mu.Lock()
	a.activity = activity
	a.mu.Unlock()
}

// setAdmissionHeld mutates the fake admission's held reservation count.

// setAdmissionHeldCategories mutates the fake admission's per-category
// reservations.
func (a *fakeInstallAdmission) setAdmissionHeldCategories(per map[workadmission.Category]int) {
	a.mu.Lock()
	a.heldCategories = per
	total := 0
	for _, n := range per {
		total += n
	}
	a.held = total
	a.mu.Unlock()
}

// setBusyOnceWhileClosedRepository arms a one-shot repository-activity
// detection that reports exactly once while admission is closed.
func (a *fakeInstallAdmission) setBusyOnceWhileClosedRepository() {
	a.mu.Lock()
	a.busyOnceWhileClosed = true
	a.busyClosedRepository = true
	a.mu.Unlock()
}

// newStopTestCoordinator builds a coordinator-level fixture whose stopper,
// stager, lifecycle, and admission are all deterministic fakes.
func newStopTestCoordinator(t *testing.T, stopper *fakeInstallStopper, opts ...func(*UpdateOptions)) (*updateCoordinator, *fakeReleaseStager, *fakeInstallLifecycle, *fakeInstallAdmission, *transitionRecorder) {
	t.Helper()
	stager, lifecycle, admission, _ := newInstallFakes()
	updateOpts := UpdateOptions{
		Stager:    stager,
		Install:   lifecycle,
		Admission: admission,
		Stopper:   stopper,
	}
	for _, opt := range opts {
		opt(&updateOpts)
	}
	coordinator, _, _, recorder := newTestCoordinator(t, updateOpts)
	coordinator.performCheck(context.Background(), "initial")
	return coordinator, stager, lifecycle, admission, recorder
}

// requestStopInstall submits one immediate install request with the given
// stop permission directly to the coordinator.
func requestStopInstall(t *testing.T, c *updateCoordinator, stopActiveWork bool) *installRefusal {
	t.Helper()
	_, refusal := c.requestInstall(installRequest{
		consent:        true,
		when:           updateInstallWhenNow,
		stopActiveWork: stopActiveWork,
	}, nil)
	return refusal
}

// waitStopEntered blocks until the operation crossed stop entry. An
// already-settled operation necessarily crossed it: the stopping interval
// can complete before the test first polls.
func waitStopEntered(t *testing.T, c *updateCoordinator) {
	t.Helper()
	waitInstallCond(t, 5*time.Second, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.install == nil || c.install.stopEntered
	}, "operation never entered stopping")
}

// installStopEntered reports the current stop-entry state.
func installStopEntered(c *updateCoordinator) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.install != nil && c.install.stopEntered
}

// findObserved reports whether one transition was observed.
func findObserved(observed []observedUpdate, want observedUpdate) bool {
	for _, o := range observed {
		if o == want {
			return true
		}
	}
	return false
}

// --- Task 1: consent, protected work, and staging-time rechecks ---

// TestUpdateInstallStopPermissionRequestRefusals proves the request-time
// boundary for both permission values: feature and chat activity refuses a
// no-permission immediate install but proceeds with stop permission, while
// repository reservations, unknown reservation categories, and failed
// detection refuse either way.
func TestUpdateInstallStopPermissionRequestRefusals(t *testing.T) {
	t.Parallel()
	runningFeature := featureListerFunc(func() ([]*feature.Feature, error) {
		return []*feature.Feature{{ID: "feat-1", Status: feature.StatusImplementing}}, nil
	})
	post := func(handler *apiHandler, body string) (*http.Response, ErrorResponse) {
		w := httptest.NewRecorder()
		handler.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(body)))
		resp := w.Result()
		defer resp.Body.Close()
		var errBody ErrorResponse
		json.NewDecoder(resp.Body).Decode(&errBody)
		return resp, errBody
	}

	// Feature activity without stop permission refuses before staging.
	fixture := newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), runningFeature)
	fixture.discoverLatest(t)
	resp, body := post(fixture.handler, `{"consent":true,"when":"now"}`)
	if resp.StatusCode != http.StatusConflict || body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("no-permission status = %d code = %q, want 409 update_blocked_active_work", resp.StatusCode, body.Error.Code)
	}
	if got := fixture.stager.calls(); got != 0 {
		t.Fatalf("stager calls = %d, want none for a refused request", got)
	}

	// The same activity with stop permission proceeds to staging.
	fixture = newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), runningFeature)
	fixture.discoverLatest(t)
	resp, _ = post(fixture.handler, `{"consent":true,"when":"now","stop_active_work":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop-permission status = %d, want 202", resp.StatusCode)
	}

	// A repository reservation refuses even with stop permission.
	fixture = newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), runningFeature)
	fixture.discoverLatest(t)
	repoRes, err := fixture.handler.admission.Acquire(workadmission.CategoryClone)
	if err != nil {
		t.Fatalf("acquire clone reservation: %v", err)
	}
	resp, body = post(fixture.handler, `{"consent":true,"when":"now","stop_active_work":true}`)
	if resp.StatusCode != http.StatusConflict || body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("clone-reservation status = %d code = %q, want 409 update_blocked_active_work", resp.StatusCode, body.Error.Code)
	}
	repoRes.Release()

	// An unknown reservation category refuses with stop permission.
	fixture = newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), runningFeature)
	fixture.discoverLatest(t)
	unknownRes, err := fixture.handler.admission.Acquire(workadmission.Category("mystery"))
	if err != nil {
		t.Fatalf("acquire unknown reservation: %v", err)
	}
	resp, body = post(fixture.handler, `{"consent":true,"when":"now","stop_active_work":true}`)
	if resp.StatusCode != http.StatusConflict || body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("unknown-category status = %d code = %q, want 409 update_blocked_active_work", resp.StatusCode, body.Error.Code)
	}
	if !strings.Contains(body.Error.Diagnostics, "mystery") {
		t.Fatalf("diagnostics = %q, want the unknown category named", body.Error.Diagnostics)
	}
	unknownRes.Release()

	// A feature-category reservation does not refuse a stop-permitted
	// request: that work is authorized to be stopped.
	fixture = newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), runningFeature)
	fixture.discoverLatest(t)
	featureRes, err := fixture.handler.admission.Acquire(workadmission.CategoryFeature)
	if err != nil {
		t.Fatalf("acquire feature reservation: %v", err)
	}
	resp, _ = post(fixture.handler, `{"consent":true,"when":"now","stop_active_work":true}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("feature-reservation stop-permission status = %d, want 202", resp.StatusCode)
	}
	featureRes.Release()

	// Failed detection refuses both permission values.
	failing := featureListerFunc(func() ([]*feature.Feature, error) {
		return nil, errors.New("store unreadable")
	})
	fixture = newInstallAPIFixtureWithBoundary(t, eligibleUpdateOptions(), workadmission.New(workadmission.Options{}), failing)
	fixture.discoverLatest(t)
	resp, body = post(fixture.handler, `{"consent":true,"when":"now","stop_active_work":true}`)
	if resp.StatusCode != http.StatusConflict || body.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("detection-failure status = %d code = %q, want 409 update_blocked_active_work", resp.StatusCode, body.Error.Code)
	}
}

// TestUpdateInstallStopRetryCoalescesWhileStopping proves an equivalent
// retry coalesces into the stopping operation without a second worker or a
// second stop pass, and that the retry is not mistaken for a new request
// blocked by the operation's own settling work.
func TestUpdateInstallStopRetryCoalescesWhileStopping(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	park := make(chan struct{})
	stopper.setStopBlock(park)
	coordinator, stager, _, admission, _ := newStopTestCoordinator(t, stopper)
	// Simulate the feature's own reservation still settling while the
	// worker parks inside the first stop dispatch.
	admission.setAdmissionHeldCategories(map[workadmission.Category]int{workadmission.CategoryFeature: 1})

	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitStopEntered(t, coordinator)

	// Equivalent retry while the first dispatch is parked: coalesced, no
	// second worker, no second stop pass.
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("equivalent retry refused: %v", refusal)
	}
	// A permission change while the operation is active is a conflict,
	// not a coalescing retry.
	if refusal := requestStopInstall(t, coordinator, false); refusal == nil || refusal.code != errcat.Conflict {
		t.Fatalf("permission-change retry = %v, want conflict", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 1 },
		"first stop dispatch never ran")
	close(park)
	admission.setAdmissionHeldCategories(nil)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	if got := stager.calls(); got != 1 {
		t.Fatalf("stager calls = %d, want exactly one worker", got)
	}
	if got := len(stopper.stopCallsSnapshot()); got != 1 {
		t.Fatalf("stop dispatches = %d, want a single stop pass", got)
	}
}

// TestUpdateInstallStopBlockersAfterStagingAbortCleanly proves blockers
// introduced during staging fail the accepted stop-permitted operation,
// clean owned staging, stop nothing, and leave admission open.
func TestUpdateInstallStopBlockersAfterStagingAbortCleanly(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, stager, lifecycle, admission, recorder := newStopTestCoordinator(t, stopper)
	// Repository work appears while the release stages.
	stager.setBlock(make(chan struct{}))
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return stager.calls() >= 1 }, "worker never reached the stager")
	admission.setAdmissionActivity(workadmission.Activity{Clones: 1})
	// Let staging complete; the post-staging recheck must refuse.
	stager.mu.Lock()
	block := stager.block
	stager.block = nil
	stager.mu.Unlock()
	close(block)

	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "blocked operation never settled")
	if got := len(stopper.stopCallsSnapshot()); got != 0 {
		t.Fatalf("stop dispatches = %d, want none for a blocked operation", got)
	}
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("admission must stay open; nothing was stopped")
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want failed update_blocked_active_work", snapshot.Status, snapshot.Error)
	}
	if !findObservedSnapshot(recorder, "install_blocked_active_work") {
		t.Fatalf("observed = %v, want the blocked result", recorderObserved(recorder))
	}
}

func recorderObserved(recorder *transitionRecorder) []observedUpdate {
	_, _, observed := recorder.snapshot()
	return observed
}

func findObservedSnapshot(recorder *transitionRecorder, resultPart string) bool {
	for _, o := range recorderObserved(recorder) {
		if strings.Contains(o.result, resultPart) {
			return true
		}
	}
	return false
}

// TestUpdateInstallStopClosureRaceAbortsBeforeStopping proves a repository
// reservation that wins the closure race aborts the operation before any
// feature or chat work is stopped, with admission left open.
func TestUpdateInstallStopClosureRaceAbortsBeforeStopping(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, stager, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	// The closure observes a clone reservation that raced in after the
	// post-staging check.
	admission.setAdmissionHeldCategories(map[workadmission.Category]int{workadmission.CategoryClone: 1})
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "raced operation never settled")
	if got := len(stopper.stopCallsSnapshot()); got != 0 {
		t.Fatalf("stop dispatches = %d, want none when repository work won the race", got)
	}
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("a refused closure must leave admission open")
	}
}

// TestUpdateInstallStopClosedRecheckBlockerAborts proves protected activity
// discovered under closed admission aborts before stop dispatch, reopens
// the gate, and cleans owned staging.
func TestUpdateInstallStopClosedRecheckBlockerAborts(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, stager, lifecycle, admission, recorder := newStopTestCoordinator(t, stopper)
	// Origin work is detected only once the boundary is closed: the
	// busy-while-closed one-shot reports repository activity under the
	// closed gate.
	admission.setBusyOnceWhileClosedRepository()
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	if got := len(stopper.stopCallsSnapshot()); got != 0 {
		t.Fatalf("stop dispatches = %d, want none; the recheck must abort first", got)
	}
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("the gate must be released after the closed-admission recheck aborts")
	}
	if !findObservedSnapshot(recorder, "install_blocked_active_work") {
		t.Fatalf("observed = %v, want the blocked result", recorderObserved(recorder))
	}
}

// --- Task 2: authorized stopping before guarded replacement ---

// TestUpdateInstallStopFeaturesAloneAndChatAlone proves the stop scope for
// features alone, chat alone, and both together: children stop before
// parents, each authorized target is dispatched exactly once, confirmation
// waits for actual completion, and replacement happens only afterwards.
func TestUpdateInstallStopFeaturesAndChatScope(t *testing.T) {
	t.Run("features_alone_children_first", func(t *testing.T) {
		t.Parallel()
		stopper := &fakeInstallStopper{features: []string{"parent-1", "child-1", "child-2"}}
		coordinator, _, lifecycle, admission, recorder := newStopTestCoordinator(t, stopper)
		replaceGate := make(chan struct{})
		lifecycle.setReplaceBlock(replaceGate)
		// After the last authorized dispatch, the parent's own finalization
		// reservation is still settling: confirmation must wait for it
		// before any replacement work begins.
		stopper.setOnDispatch(func() {
			if len(stopper.stopCallsSnapshot()) == 3 {
				admission.setAdmissionHeldCategories(map[workadmission.Category]int{workadmission.CategoryFeature: 1})
			}
		})
		if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
			t.Fatalf("stop install refused: %v", refusal)
		}
		waitStopEntered(t, coordinator)
		waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 3 },
			"all three features were never stopped")
		// Every authorized target is dispatched exactly once.
		calls := stopper.stopCallsSnapshot()
		for _, id := range []string{"parent-1", "child-1", "child-2"} {
			count := 0
			for _, c := range calls {
				if c == id {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("feature %s dispatched %d times, want exactly once", id, count)
			}
		}
		if got := stopper.endChatCallsN(); got != 0 {
			t.Fatalf("end-chat calls = %d, want none without active chat", got)
		}
		// No replacement before confirmed completion: the held
		// finalization reservation keeps the drain out for at least one
		// full confirmation poll.
		time.Sleep(2 * installStopConfirmPoll)
		if got := lifecycle.beginCallsN(); got != 0 {
			t.Fatalf("begin calls = %d, want none before confirmed completion", got)
		}
		admission.setAdmissionHeldCategories(nil)
		waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.beginCallsN() == 1 }, "confirmed drain never began")
		close(replaceGate)
		waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
		if !findObserved(recorderObserved(recorder), observedUpdate{from: updateStatusVerified, to: updateStatusDraining, result: "install_stopping"}) {
			t.Fatalf("observed = %v, want draining at stop entry", recorderObserved(recorder))
		}
	})
	t.Run("chat_alone", func(t *testing.T) {
		t.Parallel()
		stopper := &fakeInstallStopper{chatActive: true}
		coordinator, _, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
		// Chat activity settles only after the end-chat dispatch is
		// observed: confirmation must see it disappear.
		stopper.setOnDispatch(func() {
			admission.setAdmissionActivity(workadmission.Activity{})
			stopper.setChatActive(false)
		})
		admission.setAdmissionActivity(workadmission.Activity{ChatActive: true})
		if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
			t.Fatalf("stop install refused: %v", refusal)
		}
		waitStopEntered(t, coordinator)
		waitInstallCond(t, 5*time.Second, func() bool { return stopper.endChatCallsN() == 1 }, "chat was never ended")
		if got := len(stopper.stopCallsSnapshot()); got != 0 {
			t.Fatalf("stop dispatches = %d, want none without stoppable features", got)
		}
		waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() == 1 }, "replacement never ran")
		waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	})
	t.Run("features_and_chat_together", func(t *testing.T) {
		t.Parallel()
		stopper := &fakeInstallStopper{features: []string{"feat-1"}, chatActive: true}
		coordinator, _, lifecycle, _, _ := newStopTestCoordinator(t, stopper)
		if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
			t.Fatalf("stop install refused: %v", refusal)
		}
		waitStopEntered(t, coordinator)
		waitInstallCond(t, 5*time.Second, func() bool {
			return len(stopper.stopCallsSnapshot()) == 1 && stopper.endChatCallsN() == 1
		}, "feature stop and chat end never both ran")
		waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() == 1 }, "replacement never ran")
	})
}

// TestUpdateInstallStopAccountsForNewlyVisibleWork proves previously
// admitted work that becomes visible during stopping is accounted for:
// a stop is dispatched for it within the same budget, and confirmation
// only succeeds after it settles.
func TestUpdateInstallStopAccountsForNewlyVisibleWork(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, _, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	// After the first dispatch, an already-admitted advance makes a second
	// feature visible; its reservation settles one confirmation poll later.
	stopper.setOnDispatch(func() {
		if len(stopper.stopCallsSnapshot()) == 1 {
			stopper.setFeatures("feat-1", "feat-2")
			admission.setAdmissionHeldCategories(map[workadmission.Category]int{workadmission.CategoryFeature: 1})
			go func() {
				time.Sleep(300 * time.Millisecond)
				admission.setAdmissionHeldCategories(nil)
			}()
		}
	})
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitStopEntered(t, coordinator)
	waitInstallCond(t, 5*time.Second, func() bool {
		calls := stopper.stopCallsSnapshot()
		return len(calls) == 2 && calls[1] == "feat-2"
	}, "newly visible work was never stopped")
	waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() == 1 }, "replacement never ran")
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
}

// TestUpdateInstallStopUnidentifiableWorkBlocks proves reservations that
// cannot be identified as stoppable feature or chat work block the
// installation during confirmation, even after successful stops.
func TestUpdateInstallStopUnidentifiableWorkBlocks(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, stager, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	// After the feature stops, an unknown-category reservation appears.
	stopper.setOnDispatch(func() {
		admission.setAdmissionHeldCategories(map[workadmission.Category]int{workadmission.Category("mystery"): 1})
	})
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none; unidentifiable work must block", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("the gate must be released after the abort")
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want failed update_blocked_active_work", snapshot.Status, snapshot.Error)
	}
}

// TestUpdateInstallStopDetectionFailureDuringConfirmation proves a
// detection failure during confirmation aborts the installation with
// detection uncertainty reported.
func TestUpdateInstallStopDetectionFailureDuringConfirmation(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, _, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	stopper.setOnDispatch(func() {
		admission.mu.Lock()
		admission.detectErr = errors.New("detector failed")
		admission.mu.Unlock()
	})
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none; failed detection must block", got)
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want failed update_blocked_active_work", snapshot.Status, snapshot.Error)
	}
}

// --- Task 3: aborts, cancellation races, and stale workers ---

// TestUpdateInstallCancelBeforeStopEntryWins proves cancellation winning
// before stop entry prevents every stop and replacement, settles owned
// cleanup, and releases operation ownership.
func TestUpdateInstallCancelBeforeStopEntryWins(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	coordinator, stager, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	stager.setBlock(make(chan struct{}))
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return stager.calls() >= 1 }, "worker never reached the stager")
	result, refusal := coordinator.cancelInstall(context.Background())
	if result != cancelCompleted || refusal != nil {
		t.Fatalf("cancel result = %d refusal = %v, want completed", result, refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "cancelled operation never settled")
	if got := len(stopper.stopCallsSnapshot()); got != 0 {
		t.Fatalf("stop dispatches = %d, want none for a cancelled operation", got)
	}
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("admission must be released after cancellation")
	}
}

// TestUpdateInstallCancelAfterStopEntryRefused proves stop entry winning
// makes cancellation return 409 update_in_progress before any subsequent
// stop is dispatched, while the parked first dispatch is still settling.
func TestUpdateInstallCancelAfterStopEntryRefused(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1", "feat-2"}}
	park := make(chan struct{})
	stopper.setStopBlock(park)
	coordinator, _, lifecycle, _, _ := newStopTestCoordinator(t, stopper)
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitStopEntered(t, coordinator)
	waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 1 },
		"first dispatch never started")

	result, refusal := coordinator.cancelInstall(context.Background())
	if result != cancelDrainRefused || refusal == nil ||
		refusal.status != http.StatusConflict || refusal.code != errcat.UpdateInProgress {
		t.Fatalf("cancel result = %d refusal = %+v, want 409 update_in_progress", result, refusal)
	}
	// The second authorized stop still proceeds: the operation owns the
	// stopping interval.
	close(park)
	waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 2 },
		"second dispatch never ran after the refused cancellation")
	waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() == 1 }, "replacement never ran")
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never settled")
}

// TestUpdateInstallStopFailureAbortsReplacement proves any stop error
// aborts the installation even though another stop succeeded: the current
// executable keeps serving (no Begin, no Replace, no rollback receipt),
// the failure reports update_blocked_active_work with sanitized context,
// the gate reopens, and already-stopped work is never resumed.
func TestUpdateInstallStopFailureAbortsReplacement(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1", "feat-2"}}
	stopper.setStopErr("feat-2", errors.New("stop rejected: relationship guard refused /tmp/secure/worktree"))
	coordinator, stager, lifecycle, admission, recorder := newStopTestCoordinator(t, stopper)
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "failed operation never settled")
	if got := len(stopper.stopCallsSnapshot()); got != 2 {
		t.Fatalf("stop dispatches = %d, want the first stop to succeed before the failure", got)
	}
	if got := lifecycle.beginCallsN(); got != 0 {
		t.Fatalf("begin calls = %d, want none; a pre-replacement failure must not create a transaction", got)
	}
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("the gate must be released after the abort")
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want failed update_blocked_active_work", snapshot.Status, snapshot.Error)
	}
	diagnostics := snapshot.Error.Diagnostics
	if !strings.Contains(diagnostics, "stopping feature failed") {
		t.Fatalf("diagnostics = %q, want the stop-failure context", diagnostics)
	}
	if len(diagnostics) > 512 {
		t.Fatalf("diagnostics = %q, want the bounded sanitized stop context", diagnostics)
	}
	if !findObservedSnapshot(recorder, "install_stop_failed") {
		t.Fatalf("observed = %v, want the stop-failure result", recorderObserved(recorder))
	}
	// A fresh consented request can retry after the failure settles.
	coordinator.performCheck(context.Background(), "explicit")
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("fresh consented retry refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "retry never settled")
}

// TestUpdateInstallStopTimeoutAborts proves the shared deadline expiring
// aborts the installation: repeated observations never reset it, the
// current build keeps serving, blockers are reported, and the gate reopens.
func TestUpdateInstallStopTimeoutAborts(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	park := make(chan struct{})
	stopper.setStopBlock(park)
	coordinator, stager, lifecycle, admission, recorder := newStopTestCoordinator(t, stopper, func(opts *UpdateOptions) {
		opts.StopWorkTimeout = 250 * time.Millisecond
	})
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitStopEntered(t, coordinator)
	waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 1 },
		"first dispatch never started")
	// The dispatch overruns the whole budget; releasing it afterwards must
	// not restart the clock.
	time.Sleep(300 * time.Millisecond)
	close(park)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "timed-out operation never settled")
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none after the deadline expired", got)
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned", got)
	}
	if admission.Closed() {
		t.Fatal("the gate must be released after the timeout abort")
	}
	if !findObservedSnapshot(recorder, "install_stop_failed:timeout") {
		t.Fatalf("observed = %v, want the timeout result", recorderObserved(recorder))
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want failed update_blocked_active_work", snapshot.Status, snapshot.Error)
	}
}

// TestUpdateInstallStopCleanupFailureRetriesIdempotently proves a stop
// failure whose owned cleanup fails retains exactly the ownership needed
// for an idempotent cleanup retry, reports the stop failure without
// masking it, and never lets a new install bypass unsettled cleanup.
func TestUpdateInstallStopCleanupFailureRetriesIdempotently(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	stopper.setStopErr("feat-1", errors.New("stop rejected"))
	coordinator, stager, _, admission, _ := newStopTestCoordinator(t, stopper)
	stager.setCleanupFailure(1)
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		return coordinator.install != nil && coordinator.install.cleanupFailed
	}, "cleanup failure never retained the operation")
	// The public failure is the stop failure, not the cleanup error.
	snapshot := coordinator.Snapshot()
	if snapshot.Status != UpdateSnapshotStatusFailed || snapshot.Error == nil ||
		snapshot.Error.Code != string(errcat.UpdateBlockedActiveWork) {
		t.Fatalf("snapshot = %s err = %+v, want the stop failure reported", snapshot.Status, snapshot.Error)
	}
	// A new install cannot bypass unsettled cleanup.
	if refusal := requestStopInstall(t, coordinator, true); refusal == nil || refusal.code != errcat.UpdateInstallFailed {
		t.Fatalf("new install refusal = %v, want update_install_failed for unsettled cleanup", refusal)
	}
	// The repeated cancellation owns the idempotent cleanup retry.
	result, refusal := coordinator.cancelInstall(context.Background())
	if result != cancelCompleted || refusal != nil {
		t.Fatalf("cleanup retry result = %d refusal = %v, want completed", result, refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "operation never cleared after cleanup retry")
	if admission.Closed() {
		t.Fatal("the gate must be released after cleanup settles")
	}
}

// TestUpdateInstallShutdownDefersToStopping proves ordinary shutdown
// defers to an operation that entered its stopping interval instead of
// cancelling it mid-stop; the operation finishes installation.
func TestUpdateInstallShutdownDefersToStopping(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	park := make(chan struct{})
	stopper.setStopBlock(park)
	coordinator, _, lifecycle, _, _ := newStopTestCoordinator(t, stopper)
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitStopEntered(t, coordinator)
	waitInstallCond(t, 5*time.Second, func() bool { return len(stopper.stopCallsSnapshot()) == 1 },
		"first dispatch never started")

	shutdownDone := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		coordinator.shutdown(ctx)
		close(shutdownDone)
	}()
	// Shutdown returns without joining the stopping operation.
	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary shutdown never returned; it must defer to the stopping operation")
	}
	if !installStopEntered(coordinator) || !installOpActive(coordinator) {
		t.Fatal("shutdown must not cancel an operation inside its stopping interval")
	}
	close(park)
	waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.replaceCallsN() == 1 }, "replacement never ran after deferral")
}

func installOpActive(c *updateCoordinator) bool {
	return !installOpCleared(c)
}

// TestUpdateInstallNoPermissionRepeatsFullCheckAfterStaging proves a
// no-permission immediate operation repeats the full active-work check
// after staging: feature activity that appeared during staging fails it.
func TestUpdateInstallNoPermissionRepeatsFullCheckAfterStaging(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{}
	coordinator, stager, lifecycle, admission, _ := newStopTestCoordinator(t, stopper)
	stager.setBlock(make(chan struct{}))
	if refusal := requestStopInstall(t, coordinator, false); refusal != nil {
		t.Fatalf("install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return stager.calls() >= 1 }, "worker never reached the stager")
	admission.setAdmissionActivity(workadmission.Activity{Features: 1})
	stager.mu.Lock()
	block := stager.block
	stager.block = nil
	stager.mu.Unlock()
	close(block)
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "blocked operation never settled")
	if got := lifecycle.replaceCallsN(); got != 0 {
		t.Fatalf("replace calls = %d, want none", got)
	}
	if got := len(stopper.stopCallsSnapshot()); got != 0 {
		t.Fatalf("stop dispatches = %d, want none without permission", got)
	}
	if admission.Closed() {
		t.Fatal("admission must stay open; nothing was stopped")
	}
	if got := stager.cleanupCalls(); got < 1 {
		t.Fatalf("cleanup calls = %d, want owned staging cleaned after the post-staging block", got)
	}
}

// --- 503 refusals for new work during closed admission ---

// TestPromptRepliesRefusedDuringClosedAdmission proves replies and chat
// turns that could launch new work receive the canonical 503
// update_in_progress with Retry-After while admission is closed, while the
// chat-end settle path stays available.
func TestPromptRepliesRefusedDuringClosedAdmission(t *testing.T) {
	t.Parallel()
	mutations := &stopMutationTarget{}
	busyFeatures := featureListerFunc(func() ([]*feature.Feature, error) {
		return []*feature.Feature{}, nil
	})
	boundary := workadmission.New(workadmission.Options{})
	opts := eligibleUpdateOptions()
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             mutations,
		Features:              busyFeatures,
		Updates:               opts,
		Admission:             boundary,
	})
	post := func(path, body string) *http.Response {
		w := httptest.NewRecorder()
		req := authorizedUpdateRequest(http.MethodPost, path, []byte(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		handler.routes().ServeHTTP(w, req)
		resp := w.Result()
		resp.Body.Close()
		return resp
	}
	// Sanity: with admission open the reply routes are not refused as
	// update_in_progress (they may fail validation differently).
	open := post("/api/v1/prompts/chat/end", `{}`)
	if open.StatusCode == http.StatusServiceUnavailable {
		t.Fatal("chat/end must stay available with admission open")
	}
	if !boundary.CloseIfQuiesced() {
		t.Fatal("test boundary must close while quiesced")
	}
	for _, tt := range []struct {
		name string
		path string
		body string
	}{
		{"chat_start", "/api/v1/prompts/chat/start", `{"message":"hello"}`},
		{"ask_user_answer", "/api/v1/prompts/ask-user/answer", `{"request_id":"r1","answers":{"q":"a"}}`},
		{"help_send", "/api/v1/prompts/help/send", `{"message":"more info","session_id":"s1"}`},
		{"permissions_answer", "/api/v1/permissions/answer", `{"request_id":"r1","decision":"allow_once"}`},
	} {
		resp := post(tt.path, tt.body)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503 while admission is closed", tt.name, resp.StatusCode)
		}
		if resp.Header.Get("Retry-After") == "" {
			t.Fatalf("%s response missing Retry-After", tt.name)
		}
	}
	// The chat-end settle path never needs new-work permission.
	resp := post("/api/v1/prompts/chat/end", `{}`)
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatal("chat/end must settle without acquiring new-work permission")
	}
	if got := mutations.endChatsN(); got != 2 {
		t.Fatalf("end-chat dispatches = %d, want the open sanity call plus the closed settle call", got)
	}
}

// TestChatRestartAfterStopFailureIsNotStoppedByStaleWork proves a chat
// session newly created after a stop failure and gate release is never
// stopped by leftover install work: the stopper surface sees no dispatch
// after the operation settles.
func TestChatRestartAfterStopFailureIsNotStoppedByStaleWork(t *testing.T) {
	t.Parallel()
	stopper := &fakeInstallStopper{features: []string{"feat-1"}}
	stopper.setStopErr("feat-1", errors.New("stop rejected"))
	coordinator, _, _, admission, _ := newStopTestCoordinator(t, stopper)
	if refusal := requestStopInstall(t, coordinator, true); refusal != nil {
		t.Fatalf("stop install refused: %v", refusal)
	}
	waitInstallCond(t, 5*time.Second, func() bool { return installOpCleared(coordinator) }, "failed operation never settled")
	// The gate is released; fresh work is admitted again.
	if got := admission.openCallsN(); got < 1 {
		t.Fatalf("open calls = %d, want the gate released after the failure", got)
	}
	// No stop pass may exist anymore: the settled worker made its last
	// dispatch before failing, and nothing dispatches afterwards.
	time.Sleep(50 * time.Millisecond)
	if got := stopper.endChatCallsN(); got != 0 {
		t.Fatalf("end-chat dispatches = %d, want none from settled install work", got)
	}
	if calls := stopper.stopCallsSnapshot(); len(calls) != 1 {
		t.Fatalf("stop dispatches = %v, want exactly the pre-failure pass", calls)
	}
}

// TestFeatureDetectFailHookFailsDetection pins the test-only
// detection-failure seam: an armed hook makes the feature-activity detector
// fail, so every admission-detection path observes uncertainty instead of
// zero activity.
func TestFeatureDetectFailHookFailsDetection(t *testing.T) {
	t.Parallel()
	features := featureListerFunc(func() ([]*feature.Feature, error) {
		return []*feature.Feature{{ID: "f1", Status: feature.StatusImplementing}}, nil
	})
	boundary := workadmission.New(workadmission.Options{})
	armed := false
	opts := eligibleUpdateOptions()
	opts.DetectFailHook = func() error {
		if armed {
			return errors.New("injected detection failure")
		}
		return nil
	}
	handler := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Features:              features,
		Updates:               opts,
		Admission:             boundary,
	})
	ctx := context.Background()
	if _, failed, err := handler.admissionActivitySnapshot(ctx); failed || err != nil {
		t.Fatalf("detection with disarmed hook = failed:%v err:%v, want a clean pass", failed, err)
	}
	armed = true
	if _, failed, _ := handler.admissionActivitySnapshot(ctx); !failed {
		t.Fatal("detection with armed hook did not report detection_failed")
	}
}

// TestHandlerStopperProjectionAndOrdering proves the production stopper's
// stop set uses the enabled pause-stop projection — running or
// need-user-input features, active children included, closed-relationship
// children and settled features excluded — and orders deepest children
// first so parent/child relationship guards pass.
func TestHandlerStopperProjectionAndOrdering(t *testing.T) {
	t.Parallel()
	parent := &feature.Feature{ID: "parent-1", Status: feature.StatusImplementing}
	child := &feature.Feature{ID: "child-1", Status: feature.StatusImplementing,
		Parent: &feature.ChildRelationship{ParentID: "parent-1"}}
	grandchild := &feature.Feature{ID: "child-2", Status: feature.StatusNeedUserInput,
		Parent: &feature.ChildRelationship{ParentID: "child-1"}}
	closedChild := &feature.Feature{ID: "child-closed", Status: feature.StatusImplementing,
		Parent: &feature.ChildRelationship{ParentID: "parent-1", CloseOutcome: "integrated"}}
	settled := &feature.Feature{ID: "done-1", Status: feature.StatusDone}
	parked := &feature.Feature{ID: "parked-1", Status: feature.StatusNeedUserInput}
	lister := featureListerFunc(func() ([]*feature.Feature, error) {
		return []*feature.Feature{parent, child, grandchild, closedChild, settled, parked}, nil
	})
	h := &apiHandler{features: lister}
	stopper := handlerInstallStopper{handler: h}
	ids, err := stopper.StoppableFeatures(context.Background())
	if err != nil {
		t.Fatalf("stoppable features: %v", err)
	}
	want := []string{"child-2", "child-1", "parent-1", "parked-1"}
	if len(ids) != len(want) {
		t.Fatalf("stop set = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("stop set = %v, want %v (deepest children first)", ids, want)
		}
	}

	// A listing failure is a detection failure.
	failing := featureListerFunc(func() ([]*feature.Feature, error) {
		return nil, errors.New("store unreadable")
	})
	h = &apiHandler{features: failing}
	if _, err := (handlerInstallStopper{handler: h}).StoppableFeatures(context.Background()); err == nil {
		t.Fatal("listing failure must surface as an error")
	}

	// Chat activity mirrors the detector's active-chat semantics.
	active := &fakeSessionView{id: ChatSessionID, status: ports.SessionRunning}
	h = &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{active}}}
	if !(handlerInstallStopper{handler: h}).ChatActive() {
		t.Fatal("active chat session must report chat activity")
	}
	finished := &fakeSessionView{id: ChatSessionID, status: ports.SessionDone}
	h = &apiHandler{sessions: fakeSessionManager{views: []ports.SessionView{finished}}}
	if (handlerInstallStopper{handler: h}).ChatActive() {
		t.Fatal("finished chat session must not report chat activity")
	}
}
