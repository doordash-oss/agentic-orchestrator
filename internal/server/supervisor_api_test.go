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
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// recordingSupervisor is a SupervisorService double that records calls and
// returns scripted results.
type recordingSupervisor struct {
	mu        sync.Mutex
	state     supervisor.State
	page      supervisor.Page
	queries   []supervisor.PageQuery
	settings  []supervisor.Settings
	sends     []string
	interrupt int
	ends      int
	err       error
	busy      bool
}

func (s *recordingSupervisor) State() supervisor.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *recordingSupervisor) Busy() bool { return s.busy }

func (s *recordingSupervisor) Transcript(q supervisor.PageQuery) (supervisor.Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	return s.page, s.err
}

func (s *recordingSupervisor) UpdateSettings(st supervisor.Settings) (supervisor.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = append(s.settings, st)
	if s.err != nil {
		return supervisor.State{}, s.err
	}
	s.state.Settings = st
	return s.state, nil
}

func (s *recordingSupervisor) Send(_ context.Context, text, cmid string) (supervisor.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, cmid+":"+text)
	if s.err != nil {
		return supervisor.SendResult{}, s.err
	}
	data, _ := json.Marshal(supervisor.UserData{Text: text})
	return supervisor.SendResult{Launched: true, Record: supervisor.Record{
		Seq: 1, ID: "r1", ConversationID: s.state.ConversationID, Generation: 1, TurnID: "g1.t1",
		Kind: supervisor.KindUser, Visibility: supervisor.VisibilityContent, ClientMessageID: cmid, Data: data,
		CreatedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
	}}, nil
}

func (s *recordingSupervisor) Interrupt() (supervisor.ActionResult, supervisor.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupt++
	return supervisor.ActionAccepted, s.state
}

func (s *recordingSupervisor) End() (supervisor.ActionResult, supervisor.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends++
	s.state.Lifecycle = supervisor.LifecycleStopped
	return supervisor.ActionEnded, s.state
}

func (s *recordingSupervisor) Subscribe(int64, bool, string) (*supervisor.Subscription, error) {
	return nil, errors.New("not supported by the recording double")
}

func (s *recordingSupervisor) Unsubscribe(*supervisor.Subscription) {}

func newSupervisorTestHandler(svc SupervisorService, admission *workadmission.Coordinator) http.Handler {
	return NewHandler(HandlerOptions{AuthToken: testAuthToken, DisableHostValidation: true, Supervisor: svc, Admission: admission})
}

func supervisorRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	if method != http.MethodGet {
		req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func serveSupervisor(t *testing.T, h http.Handler, req *http.Request, wantStatus int, out any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body %s", req.Method, req.URL.Path, w.Code, wantStatus, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
}

func TestSupervisorRoutes_StateAndSettingsShapes(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{
		ConversationID: "conv-1", Lifecycle: supervisor.LifecycleStopped, LastTurnOutcome: supervisor.OutcomeNone, StreamEpoch: "ep",
	}}
	h := newSupervisorTestHandler(svc, nil)

	var state SupervisorStateResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisorState, ""), http.StatusOK, &state)
	if state.APIVersion != APIVersion || state.State.ConversationID != "conv-1" || state.State.Lifecycle != SupervisorLifecycleStopped ||
		state.State.LastTurnOutcome != SupervisorTurnOutcomeNone || state.State.PendingRequests == nil {
		t.Fatalf("state = %+v", state)
	}

	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude","model":"haiku"}`), http.StatusOK, &state)
	if state.State.Settings != (SupervisorSettings{Harness: "claude", Model: "haiku"}) {
		t.Fatalf("settings = %+v", state.State.Settings)
	}
	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude"}`), http.StatusBadRequest, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude","model":"m","extra":1}`), http.StatusBadRequest, nil)
	if len(svc.settings) != 1 {
		t.Fatalf("invalid bodies reached the coordinator: %+v", svc.settings)
	}

	untrusted := supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude","model":"haiku"}`)
	untrusted.Header.Del("X-Agentico-Client")
	serveSupervisor(t, h, untrusted, http.StatusForbidden, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorSettings, `{}`), http.StatusMethodNotAllowed, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisor+"/bogus", ""), http.StatusNotFound, nil)
}

func TestSupervisorRoutes_ErrorsRenderThroughCanonicalEnvelope(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   errcat.Code
	}{
		{supervisor.ErrSettingsLocked, http.StatusConflict, errcat.SupervisorSettingsLocked},
		{&supervisor.SettingsInvalidError{Reason: "model is not available"}, http.StatusBadRequest, errcat.SupervisorSettingsInvalid},
		{supervisor.ErrSettingsRequired, http.StatusConflict, errcat.SettingsRequired},
		{supervisor.ErrTurnActive, http.StatusConflict, errcat.TurnActive},
		{&supervisor.LaunchFailedError{Err: errors.New("exec failed")}, http.StatusBadGateway, errcat.SupervisorLaunchFailed},
		{&workadmission.ClosedError{Category: workadmission.CategorySupervisor}, http.StatusServiceUnavailable, errcat.UpdateInProgress},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			h := newSupervisorTestHandler(&recordingSupervisor{err: tc.err}, nil)
			path, body := apiPathSupervisorMessages, `{"text":"hi","client_message_id":"cm-1"}`
			if tc.code == errcat.SupervisorSettingsLocked || tc.code == errcat.SupervisorSettingsInvalid {
				path, body = apiPathSupervisorSettings, `{"harness":"claude","model":"haiku"}`
			}
			method := http.MethodPost
			if path == apiPathSupervisorSettings {
				method = http.MethodPatch
			}
			var resp ErrorResponse
			serveSupervisor(t, h, supervisorRequest(method, path, body), tc.status, &resp)
			if string(resp.Error.Code) != string(tc.code) || resp.Error.Title == "" {
				t.Fatalf("error = %+v, want code %s", resp.Error, tc.code)
			}
		})
	}
}

func TestSupervisorRoutes_MessagesTranscriptInterruptEnd(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1", Lifecycle: supervisor.LifecycleRunning}}
	h := newSupervisorTestHandler(svc, nil)

	var sent SupervisorMessageResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, `{"text":"hello","client_message_id":"cm-1"}`), http.StatusOK, &sent)
	if !sent.Launched || sent.Record.Seq != 1 || sent.Record.Kind != SupervisorRecordKindUser || sent.Record.ClientMessageID != "cm-1" ||
		len(sent.Record.Messages) != 1 || sent.Record.Messages[0].Text != "hello" || sent.Record.Messages[0].Index != 1 {
		t.Fatalf("message response = %+v", sent)
	}
	for _, body := range []string{
		`{"text":"   ","client_message_id":"cm-2"}`,
		`{"text":"hi","client_message_id":"bad id"}`,
		`{"text":"hi"}`,
	} {
		serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, body), http.StatusBadRequest, nil)
	}
	if len(svc.sends) != 1 {
		t.Fatalf("invalid sends reached the coordinator: %v", svc.sends)
	}

	tool, _ := json.Marshal(supervisor.ContentData{})
	perm, _ := json.Marshal(supervisor.RequestData{RequestID: "req-1", ToolName: "Bash", Stage: supervisor.StageResolved, Outcome: supervisor.RequestAllowed, Input: json.RawMessage(`{"command":"secret-thing --token=abc"}`)})
	svc.page = supervisor.Page{Items: []supervisor.Record{
		{Seq: 4, Kind: supervisor.KindToolUse, Visibility: supervisor.VisibilityContent, Data: tool},
		{Seq: 5, Kind: supervisor.KindPermission, Visibility: supervisor.VisibilityDisplayOnly, Data: perm},
	}, FirstSeq: 4, LastSeq: 5, HasMoreBefore: true, HeadSeq: 5}
	var page SupervisorTranscriptResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisorTranscript+"?before=6&limit=2", ""), http.StatusOK, &page)
	if q := svc.queries[0]; !q.HasBefore || q.Before != 6 || q.Limit != 2 || q.HasAfter {
		t.Fatalf("page query = %+v", q)
	}
	if page.ConversationID != "conv-1" || len(page.Items) != 2 || page.FirstSeq != 4 || !page.HasMoreBefore || page.HeadSeq != 5 {
		t.Fatalf("page = %+v", page)
	}
	verdict := page.Items[1]
	if verdict.Request == nil || verdict.Request.Outcome != SupervisorRequestOutcomeAllowed || verdict.Visibility != SupervisorVisibilityDisplayOnly ||
		len(verdict.Messages) != 1 || !verdict.Messages[0].Redacted {
		t.Fatalf("verdict record = %+v", verdict)
	}
	// The verdict carries the same safe summary the pending card showed,
	// never the raw tool input object.
	if verdict.Request.Summary == "" {
		t.Fatal("verdict record lost its summary")
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), `"command"`) || strings.Contains(string(raw), `"input"`) {
		t.Fatalf("transcript projection leaked raw tool input: %s", raw)
	}
	for _, q := range []string{"?before=1&after=1", "?limit=501", "?limit=0", "?after=-1", "?before=x"} {
		serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisorTranscript+q, ""), http.StatusBadRequest, nil)
	}

	var action SupervisorActionResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorInterrupt, `{}`), http.StatusOK, &action)
	if action.Result != SupervisorActionAccepted || svc.interrupt != 1 {
		t.Fatalf("interrupt = %+v", action)
	}
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorEnd, `{}`), http.StatusOK, &action)
	if action.Result != SupervisorActionEnded || action.State.Lifecycle != SupervisorLifecycleStopped || svc.ends != 1 {
		t.Fatalf("end = %+v", action)
	}
}

func TestSupervisorRoutes_ClosedAdmissionRefusesMessagesButNotEnd(t *testing.T) {
	admission := workadmission.New(workadmission.Options{})
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1"}}
	h := newSupervisorTestHandler(svc, admission)
	if !admission.CloseIfQuiesced() {
		t.Fatal("admission did not close")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, `{"text":"hi","client_message_id":"cm-1"}`))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), string(errcat.UpdateInProgress)) {
		t.Fatalf("closed send = %d %s", w.Code, w.Body.String())
	}
	if len(svc.sends) != 0 {
		t.Fatal("closed admission reached the coordinator")
	}
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorEnd, `{}`), http.StatusOK, nil)
}

func TestSupervisorRoutes_UnavailableWithoutService(t *testing.T) {
	h := NewHandler(HandlerOptions{AuthToken: testAuthToken, DisableHostValidation: true})
	serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisorState, ""), http.StatusServiceUnavailable, nil)
}

func TestSupervisorMutationPreflightListsOnlyAllowedMethods(t *testing.T) {
	for path, want := range map[string]string{
		apiPathSupervisorSettings:  http.MethodPatch,
		apiPathSupervisorMessages:  http.MethodPost,
		apiPathSupervisorInterrupt: http.MethodPost,
		apiPathSupervisorEnd:       http.MethodPost,
	} {
		methods, ok := mutationRouteMethods(path)
		if !ok || len(methods) != 1 || methods[0] != want {
			t.Fatalf("mutationRouteMethods(%s) = %v %v, want [%s]", path, methods, ok, want)
		}
	}
	for _, path := range []string{apiPathSupervisorState, apiPathSupervisorTranscript, apiPathSupervisorEvents, apiPathSupervisor + "/bogus"} {
		if methods, ok := mutationRouteMethods(path); ok {
			t.Fatalf("read path %s has a mutation allowlist entry %v", path, methods)
		}
	}
	if !sseTokenFallbackAllowed(apiPathSupervisorEvents) || sseTokenFallbackAllowed(apiPathSupervisorState) {
		t.Fatal("access_token fallback must cover the supervisor events stream only")
	}
}

func TestSupervisorActivityDetectorTracksBusy(t *testing.T) {
	admission := workadmission.New(workadmission.Options{})
	svc := &recordingSupervisor{}
	newSupervisorTestHandler(svc, admission)
	activity, err := admission.Detect(context.Background())
	if err != nil || activity.SupervisorActive || activity.Busy() {
		t.Fatalf("idle supervisor activity = %+v %v", activity, err)
	}
	svc.busy = true
	activity, _ = admission.Detect(context.Background())
	if !activity.SupervisorActive || !activity.Busy() || activity.ProtectedBusy() {
		t.Fatalf("busy supervisor activity = %+v", activity)
	}
}
