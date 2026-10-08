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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
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
	hidden    []string
	interrupt int
	ends      int
	resets    int
	err       error
	busy      bool
	// waiting reports the supervisor waiting on a permission answer; it
	// takes precedence over busy.
	waiting bool
	// lifecycle, when set, overrides busy and waiting.
	lifecycle supervisor.Lifecycle
	dedup     bool
}

func (s *recordingSupervisor) State() supervisor.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *recordingSupervisor) Lifecycle() supervisor.Lifecycle {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.lifecycle != "":
		return s.lifecycle
	case s.waiting:
		return supervisor.LifecycleWaitingPermission
	case s.busy:
		return supervisor.LifecycleRunning
	default:
		return supervisor.LifecycleIdle
	}
}

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

func (s *recordingSupervisor) ChangeSettings(change supervisor.SettingsChange) (supervisor.State, error) {
	st := s.State().Settings
	if change.Harness != nil {
		st.Harness = *change.Harness
	}
	if change.Model != nil {
		st.Model = *change.Model
	}
	if change.Effort != nil {
		st.Effort = *change.Effort
	}
	return s.UpdateSettings(st)
}

func (s *recordingSupervisor) CancelPendingChange(_ string) (supervisor.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return supervisor.State{}, s.err
	}
	s.state.PendingChange = nil
	return s.state, nil
}

func TestSupervisorRoutes_CancelPendingChange(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1", PendingChange: &supervisor.PendingChange{RequestID: "change-1", Kind: "model", Target: supervisor.Settings{Harness: "claude", Model: "sonnet"}}}}
	h := newSupervisorTestHandler(svc, nil)
	var body SupervisorStateResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodDelete, apiPathSupervisorPendingChange+"change-1", ""), http.StatusOK, &body)
	if body.State.PendingChange != nil {
		t.Fatalf("pending change survived cancel: %+v", body.State.PendingChange)
	}
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorPendingChange+"change-1", ""), http.StatusMethodNotAllowed, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodDelete, apiPathSupervisorPendingChange+"bad/id", ""), http.StatusNotFound, nil)
}

func (s *recordingSupervisor) SendMessage(ctx context.Context, msg supervisor.Message) (supervisor.SendResult, error) {
	return s.Send(ctx, msg.Text, msg.HiddenContext, msg.ClientMessageID)
}

func (s *recordingSupervisor) Send(_ context.Context, text, hiddenContext, cmid string) (supervisor.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sends = append(s.sends, cmid+":"+text)
	s.hidden = append(s.hidden, hiddenContext)
	if s.err != nil {
		return supervisor.SendResult{}, s.err
	}
	data, _ := json.Marshal(supervisor.UserData{Text: text})
	return supervisor.SendResult{Launched: !s.dedup, Deduplicated: s.dedup, Record: supervisor.Record{
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

func (s *recordingSupervisor) Reset() (supervisor.ResetResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resets++
	if s.err != nil {
		return supervisor.ResetResult{}, s.err
	}
	previous := s.state.ConversationID
	if s.state.HeadSeq == 0 && s.state.Lifecycle == supervisor.LifecycleStopped {
		return supervisor.ResetResult{Result: supervisor.ResetNoop, PreviousConversationID: previous, State: s.state}, nil
	}
	s.state = supervisor.State{ConversationID: previous + "-next", Lifecycle: supervisor.LifecycleStopped, LastTurnOutcome: supervisor.OutcomeNone, Settings: s.state.Settings, StreamEpoch: "epoch-next"}
	return supervisor.ResetResult{Result: supervisor.ResetDone, PreviousConversationID: previous, State: s.state}, nil
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

	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude","model":"haiku","request_id":"change-1","expected_generation":0}`), http.StatusOK, &state)
	if state.State.Settings != (SupervisorSettings{Harness: "claude", Model: "haiku"}) {
		t.Fatalf("settings = %+v", state.State.Settings)
	}
	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"harness":"claude"}`), http.StatusBadRequest, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodPatch, apiPathSupervisorSettings, `{"request_id":"change-2","model":"sonnet"}`), http.StatusBadRequest, nil)
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
		{&supervisor.StaleGenerationError{Current: 7}, http.StatusConflict, errcat.StaleGeneration},
		{&supervisor.ChangePendingError{RequestID: "change-0"}, http.StatusConflict, errcat.ChangePending},
		{supervisor.ErrPendingChangeNotFound, http.StatusNotFound, errcat.PendingChangeNotFound},
		{&supervisor.SettingsInvalidError{Reason: "model is not available"}, http.StatusBadRequest, errcat.SupervisorSettingsInvalid},
		{supervisor.ErrSettingsRequired, http.StatusConflict, errcat.SettingsRequired},
		{supervisor.ErrTurnActive, http.StatusConflict, errcat.TurnActive},
		{&supervisor.ClientMessageConflictError{CommittedSeq: 4}, http.StatusConflict, errcat.ClientMessageConflict},
		{&supervisor.CursorOutOfRangeError{HeadSeq: 12}, http.StatusConflict, errcat.CursorOutOfRange},
		{&supervisor.LaunchFailedError{Err: errors.New("exec failed")}, http.StatusBadGateway, errcat.SupervisorLaunchFailed},
		{&workadmission.ClosedError{Category: workadmission.CategorySupervisor}, http.StatusServiceUnavailable, errcat.UpdateInProgress},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			h := newSupervisorTestHandler(&recordingSupervisor{err: tc.err}, nil)
			path, body := apiPathSupervisorMessages, `{"text":"hi","client_message_id":"cm-1"}`
			if tc.code == errcat.SupervisorSettingsLocked || tc.code == errcat.SupervisorSettingsInvalid || tc.code == errcat.StaleGeneration || tc.code == errcat.ChangePending {
				path, body = apiPathSupervisorSettings, `{"harness":"claude","model":"haiku","request_id":"change-1","expected_generation":0}`
			}
			if tc.code == errcat.PendingChangeNotFound {
				path, body = apiPathSupervisorPendingChange+"change-1", ""
			}
			method := http.MethodPost
			if path == apiPathSupervisorSettings {
				method = http.MethodPatch
			}
			if tc.code == errcat.PendingChangeNotFound {
				method = http.MethodDelete
			}
			if tc.code == errcat.CursorOutOfRange {
				path, body, method = apiPathSupervisorTranscript+"?after=13", "", http.MethodGet
			}
			var resp ErrorResponse
			serveSupervisor(t, h, supervisorRequest(method, path, body), tc.status, &resp)
			if string(resp.Error.Code) != string(tc.code) || resp.Error.Title == "" {
				t.Fatalf("error = %+v, want code %s", resp.Error, tc.code)
			}
			wantDiagnostics := map[errcat.Code]string{errcat.ClientMessageConflict: "committed_seq=4", errcat.CursorOutOfRange: "head_seq=12"}[tc.code]
			if wantDiagnostics != "" && resp.Error.Diagnostics != wantDiagnostics {
				t.Fatalf("diagnostics = %q, want %q", resp.Error.Diagnostics, wantDiagnostics)
			}
		})
	}
}

func TestSupervisorRoutes_MessageResponseCarriesDeduplicated(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1"}}
	h := newSupervisorTestHandler(svc, nil)
	var sent SupervisorMessageResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, `{"text":"hello","client_message_id":"cm-1"}`), http.StatusOK, &sent)
	if sent.Deduplicated {
		t.Fatalf("first send reported deduplicated: %+v", sent)
	}
	svc.dedup = true
	var raw map[string]any
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, `{"text":"hello","client_message_id":"cm-1"}`), http.StatusOK, &raw)
	if raw["deduplicated"] != true || raw["launched"] != false {
		t.Fatalf("resend response = %v, want deduplicated true", raw)
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
	for _, q := range []string{"?before=1&after=1", "?limit=501", "?limit=0", "?after=-1", "?before=x", "?before=0"} {
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

// newSupervisorErrorReferenceHandler serves the supervisor routes over a
// feature store holding one run failure an error reference can address.
func newSupervisorErrorReferenceHandler(t *testing.T, svc SupervisorService) http.Handler {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	chatContextSeedRunFailure(t, store)
	return NewHandler(HandlerOptions{AuthToken: testAuthToken, DisableHostValidation: true, Supervisor: svc, FeatureStore: store, Features: store})
}

func TestSupervisorRoutes_MessageErrorReferenceResolvesIntoHiddenContext(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1"}}
	h := newSupervisorErrorReferenceHandler(t, svc)

	body := `{"text":"Explain this error","client_message_id":"cm-1","error_reference":{"scope":"run","code":"iteration_budget_exhausted","feature_id":"feat-run-failed"}}`
	w := httptest.NewRecorder()
	h.ServeHTTP(w, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, body))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(svc.sends) != 1 || svc.sends[0] != "cm-1:Explain this error" {
		t.Fatalf("sends = %v; want only the visible text", svc.sends)
	}
	hidden := svc.hidden[0]
	if !strings.Contains(hidden, "error[iteration_budget_exhausted]") || !strings.Contains(hidden, "end run") ||
		!strings.Contains(hidden, "/tmp/chat-context-run.log") || !strings.Contains(hidden, "feat-run-failed") {
		t.Fatalf("hidden context = %q; want the resolved run-failure bundle", hidden)
	}
	if strings.Contains(w.Body.String(), "raw failure detail") {
		t.Fatalf("response leaked the hidden bundle: %s", w.Body.String())
	}

	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, `{"text":"plain","client_message_id":"cm-2"}`), http.StatusOK, nil)
	if len(svc.hidden) != 2 || svc.hidden[1] != "" {
		t.Fatalf("message without a reference carried hidden context %q", svc.hidden)
	}
}

func TestSupervisorRoutes_MessageMalformedOrStaleErrorReferenceSendsNothing(t *testing.T) {
	cases := []struct {
		name   string
		ref    string
		status int
		code   errcat.Code
	}{
		{"unknown scope", `{"scope":"galaxy","code":"iteration_budget_exhausted","feature_id":"feat-run-failed"}`, http.StatusBadRequest, errcat.ChatContextInvalid},
		{"missing feature", `{"scope":"run","code":"iteration_budget_exhausted"}`, http.StatusBadRequest, errcat.ChatContextInvalid},
		{"foreign key", `{"scope":"run","code":"iteration_budget_exhausted","feature_id":"feat-run-failed","task_key":"t"}`, http.StatusBadRequest, errcat.ChatContextInvalid},
		{"unknown feature", `{"scope":"run","code":"iteration_budget_exhausted","feature_id":"feat-gone"}`, http.StatusBadRequest, errcat.ChatContextInvalid},
		{"stale code", `{"scope":"run","code":"worktree_setup_failed","feature_id":"feat-run-failed"}`, http.StatusNotFound, errcat.ChatContextNotFound},
		{"stale home", `{"scope":"repository","code":"publish_pull_request_failed","feature_id":"feat-run-failed","repository":"repo-a"}`, http.StatusNotFound, errcat.ChatContextNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1"}}
			h := newSupervisorErrorReferenceHandler(t, svc)
			body := `{"text":"Explain this error","client_message_id":"cm-1","error_reference":` + tc.ref + `}`
			var resp ErrorResponse
			serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, body), tc.status, &resp)
			if string(resp.Error.Code) != string(tc.code) || resp.Error.Title == "" {
				t.Fatalf("error = %+v, want code %s", resp.Error, tc.code)
			}
			if len(svc.sends) != 0 {
				t.Fatalf("refused reference reached the coordinator: %v", svc.sends)
			}
		})
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
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorReset, `{}`), http.StatusOK, nil)
	if svc.resets != 1 {
		t.Fatalf("reset under closed admission reached the coordinator %d times, want 1", svc.resets)
	}
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
		apiPathSupervisorReset:     http.MethodPost,
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
	if !activity.SupervisorActive || activity.SupervisorWaiting || !activity.Busy() || !activity.BlocksIdleInstall() || activity.ProtectedBusy() {
		t.Fatalf("busy supervisor activity = %+v", activity)
	}
	// A supervisor waiting on the user is still active work, but it no
	// longer blocks an unattended install.
	svc.waiting = true
	activity, _ = admission.Detect(context.Background())
	if !activity.SupervisorActive || !activity.SupervisorWaiting || !activity.Busy() || activity.BlocksIdleInstall() {
		t.Fatalf("waiting supervisor activity = %+v", activity)
	}
}

// TestUpdateReportsSupervisorActivity pins the update surface's supervisor
// naming: GET /api/v1/update reports supervisor_active and
// supervisor_waiting from the supervisor lifecycle alone, and an immediate install without stop permission is
// refused with a 409 blocker that names the supervisor.
func TestUpdateReportsSupervisorActivity(t *testing.T) {
	t.Parallel()
	svc := &recordingSupervisor{}
	stager, lifecycle, admission, _ := newInstallFakes()
	opts := eligibleUpdateOptions()
	opts.Feed = &fakeUpdateFeed{selection: selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0"}}
	opts.Stager = stager
	opts.Install = lifecycle
	opts.Admission = admission
	h := newAPIHandler(HandlerOptions{
		DisableHostValidation: true,
		AuthToken:             "test-token",
		Mutations:             nopMutationTarget{},
		Updates:               opts,
		Admission:             workadmission.New(workadmission.Options{}),
		Supervisor:            svc,
	})
	h.updates.performCheck(context.Background(), "explicit")
	summary := func() UpdateActiveWorkSummary {
		t.Helper()
		w := httptest.NewRecorder()
		h.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET update status = %d body=%s", w.Code, w.Body.String())
		}
		return decodeUpdateSnapshot(t, w.Result()).ActiveWorkSummary
	}

	if got := summary(); got.SupervisorActive {
		t.Fatalf("idle supervisor summary = %+v, want supervisor_active false", got)
	}
	svc.mu.Lock()
	svc.busy = true
	svc.mu.Unlock()
	if got := summary(); !got.SupervisorActive || got.SupervisorWaiting || got.FeatureCount != 0 {
		t.Fatalf("busy supervisor summary = %+v, want supervisor_active true, not waiting, and no features", got)
	}
	svc.mu.Lock()
	svc.waiting = true
	svc.mu.Unlock()
	if got := summary(); !got.SupervisorActive || !got.SupervisorWaiting {
		t.Fatalf("waiting supervisor summary = %+v, want supervisor_active and supervisor_waiting true", got)
	}
	svc.mu.Lock()
	svc.waiting = false
	svc.mu.Unlock()

	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"now"}`)))
	if w.Code != http.StatusConflict {
		t.Fatalf("install status = %d body=%s, want 409", w.Code, w.Body.String())
	}
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode install refusal: %v", err)
	}
	if body.Error.Code != string(errcat.UpdateBlockedActiveWork) || !strings.Contains(body.Error.Summary, "supervisor") {
		t.Fatalf("install refusal = %+v, want update_blocked_active_work naming the supervisor", body.Error)
	}
	if strings.Contains(strings.ToLower(w.Body.String()), "chat") {
		t.Fatalf("install refusal mentions chat: %s", w.Body.String())
	}
}

// TestUpdateIdleInstallProceedsPastWaitingSupervisor drives an
// install-when-idle through the real admission boundary and the handler's
// supervisor detector: a supervisor waiting on a permission or question
// with no other work lets the install begin at once, while a running
// supervisor holds it scheduled until the turn ends.
func TestUpdateIdleInstallProceedsPastWaitingSupervisor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		lifecycle supervisor.Lifecycle
		waits     bool
	}{
		{supervisor.LifecycleWaitingPermission, false},
		{supervisor.LifecycleWaitingQuestion, false},
		{supervisor.LifecycleRunning, true},
		{supervisor.LifecycleStarting, true},
	} {
		t.Run(string(tc.lifecycle), func(t *testing.T) {
			t.Parallel()
			svc := &recordingSupervisor{lifecycle: tc.lifecycle}
			stager, lifecycle, _, _ := newInstallFakes()
			replaceBlock := make(chan struct{})
			t.Cleanup(func() { close(replaceBlock) })
			lifecycle.setReplaceBlock(replaceBlock)
			boundary := workadmission.New(workadmission.Options{FallbackPoll: 10 * time.Millisecond})
			opts := eligibleUpdateOptions()
			opts.Feed = &fakeUpdateFeed{selection: selfupdate.ReleaseSelection{Version: "2.0.0", TagName: "v2.0.0"}}
			opts.Stager = stager
			opts.Install = lifecycle
			opts.Admission = boundary
			h := newAPIHandler(HandlerOptions{
				DisableHostValidation: true,
				AuthToken:             "test-token",
				Mutations:             nopMutationTarget{},
				Updates:               opts,
				Admission:             boundary,
				Supervisor:            svc,
			})
			t.Cleanup(func() { h.updates.shutdownInstall(context.Background()) })
			h.updates.performCheck(context.Background(), "explicit")

			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, authorizedUpdateRequest(http.MethodGet, apiPathUpdate, nil))
			summary := decodeUpdateSnapshot(t, w.Result()).ActiveWorkSummary
			if !summary.SupervisorActive || summary.SupervisorWaiting != !tc.waits {
				t.Fatalf("summary = %+v, want supervisor_active and supervisor_waiting=%v", summary, !tc.waits)
			}

			w = httptest.NewRecorder()
			h.routes().ServeHTTP(w, trustedInstallRequest(http.MethodPost, []byte(`{"consent":true,"when":"idle"}`)))
			if w.Code != http.StatusAccepted {
				t.Fatalf("idle install status = %d body=%s", w.Code, w.Body.String())
			}
			if tc.waits {
				waitStatus(t, h.updates, updateStatusScheduled)
				time.Sleep(200 * time.Millisecond)
				if got := lifecycle.beginCallsN(); got != 0 {
					t.Fatalf("Begin calls = %d while the supervisor is %s, want the install held", got, tc.lifecycle)
				}
				svc.mu.Lock()
				svc.lifecycle = supervisor.LifecycleIdle
				svc.mu.Unlock()
				boundary.NotifyChanged()
			}
			waitInstallCond(t, 5*time.Second, func() bool { return lifecycle.beginCallsN() >= 1 }, "the idle install never began")
			if !boundary.Closed() {
				t.Fatal("the committing install must hold admission closed")
			}
		})
	}
}

// TestSupervisorMessageOperationDocumentsErrorReference pins the message
// route's optional error reference on the shared schema and the declared
// chat-context refusals.
func TestSupervisorMessageOperationDocumentsErrorReference(t *testing.T) {
	spec := loadOpenAPISpec(t)
	op := lookupOpenAPIOperation(t, spec, http.MethodPost, apiPathSupervisorMessages)
	declaredOpenAPIResponse(t, op, "400")
	declaredOpenAPIResponse(t, op, "404")
	schema, ok := spec.Components.Schemas["SupervisorMessageRequest"].(map[string]any)
	if !ok {
		t.Fatal("components.schemas.SupervisorMessageRequest missing")
	}
	props, _ := schema["properties"].(map[string]any)
	ref, _ := props["error_reference"].(map[string]any)
	if got := nestedYAMLRef(t, ref); got != "#/components/schemas/ErrorReference" {
		t.Fatalf("error_reference $ref = %q, want ErrorReference", got)
	}
	for _, required := range schema["required"].([]any) {
		if required == "error_reference" {
			t.Fatal("error_reference must stay optional")
		}
	}
	for _, code := range []errcat.Code{errcat.ChatContextInvalid, errcat.ChatContextNotFound} {
		if !strings.Contains(op.Description, string(code)) {
			t.Fatalf("sendSupervisorMessage description does not document %s", code)
		}
	}
}

func TestSupervisorStateProjectsRestartAndFailureFields(t *testing.T) {
	launch := &supervisor.LaunchFailedError{Err: errors.New("exec: claude: not found"), AttemptedSettings: &supervisor.Settings{Harness: "codex", Model: "gpt", Effort: ""}}
	failed := supervisorStateDTO(supervisor.State{
		Lifecycle:       supervisor.LifecycleFailed,
		LastTurnOutcome: supervisor.OutcomeNone,
		Failure:         launch,
		PermissionMode:  supervisor.PermissionMode{Requested: "default", Effective: "plan", RestrictedByPolicy: true},
	})
	sent := supervisorLaunchFailure(launch)
	if failed.Failure == nil || failed.Failure.Code != string(errcat.SupervisorLaunchFailed) || failed.Failure.Diagnostics != sent.Diagnostics || failed.Failure.Title != sent.Title {
		t.Fatalf("failure = %+v, want the sender's envelope %+v", failed.Failure, sent)
	}
	if failed.Failure.AttemptedSettings == nil || failed.Failure.AttemptedSettings.Harness != "codex" || failed.Failure.AttemptedSettings.Model != "gpt" {
		t.Fatalf("attempted settings = %+v", failed.Failure.AttemptedSettings)
	}
	if failed.InterruptedBy != SupervisorInterruptedByNone {
		t.Fatalf("interrupted_by = %q, want none", failed.InterruptedBy)
	}
	if failed.PermissionMode != (SupervisorPermissionMode{Requested: "default", Effective: "plan", RestrictedByPolicy: true}) {
		t.Fatalf("permission_mode = %+v", failed.PermissionMode)
	}
	paused := supervisorStateDTO(supervisor.State{
		Lifecycle:       supervisor.LifecycleStopped,
		LastTurnOutcome: supervisor.OutcomeInterrupted,
		InterruptedBy:   supervisor.InterruptedByShutdown,
		Failure:         launch,
	})
	if paused.Failure != nil || paused.InterruptedBy != SupervisorInterruptedByShutdown || paused.PermissionMode.Requested != "default" {
		t.Fatalf("paused state = %+v", paused)
	}
	payload, err := json.Marshal(paused)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"failure"`) {
		t.Fatalf("failure leaked outside the failed lifecycle: %s", payload)
	}
}

func TestSupervisorMarkerRecordProjectsMarkerTextAndCode(t *testing.T) {
	data, err := json.Marshal(supervisor.MarkerData{Marker: supervisor.MarkerError, Text: "exec failed: private-token", Code: string(errcat.SupervisorLaunchFailed)})
	if err != nil {
		t.Fatal(err)
	}
	dto := supervisorRecordDTO(supervisor.Record{Seq: 4, Kind: supervisor.KindMarker, Visibility: supervisor.VisibilityDisplayOnly, TurnID: "g1.t1", Data: data}, "")
	if dto.Marker == nil || dto.Marker.Marker != SupervisorMarkerError || dto.Marker.Code != string(errcat.SupervisorLaunchFailed) {
		t.Fatalf("marker = %+v", dto.Marker)
	}
	if dto.Marker.Text != "exec failed: [redacted]" {
		t.Fatalf("marker text = %q, want the safe display text", dto.Marker.Text)
	}
	if dto.Kind != SupervisorRecordKindMarker || dto.Messages == nil || dto.Request != nil {
		t.Fatalf("record = %+v", dto)
	}
	payload, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"marker":"error"`, `"text":`, `"code":"supervisor_launch_failed"`} {
		if !strings.Contains(string(payload), key) {
			t.Fatalf("wire marker %s lacks %s", payload, key)
		}
	}
}

func TestSupervisorRecoveryMarkerKeepsPreservedPath(t *testing.T) {
	preserved := "/" + strings.Repeat("deep-scratch-root/", 30) + "supervisor/transcript.jsonl.corrupt-20261007T101500Z"
	text := "3 transcript records after #12 could not be read and are not shown. The original transcript is preserved at " + preserved + "."
	data, err := json.Marshal(supervisor.MarkerData{Marker: supervisor.MarkerTranscriptRecovered, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	dto := supervisorRecordDTO(supervisor.Record{Seq: 1, Kind: supervisor.KindMarker, Visibility: supervisor.VisibilityDisplayOnly, Data: data}, "")
	if dto.Marker == nil || !strings.HasSuffix(dto.Marker.Text, preserved+".") {
		t.Fatalf("recovery marker text = %+v, want the full preserved path", dto.Marker)
	}

	long, err := json.Marshal(supervisor.MarkerData{Marker: supervisor.MarkerError, Text: strings.Repeat("e", 600)})
	if err != nil {
		t.Fatal(err)
	}
	if got := supervisorRecordDTO(supervisor.Record{Seq: 2, Kind: supervisor.KindMarker, Data: long}, "").Marker.Text; len(got) != 403 {
		t.Fatalf("error marker text length = %d, want the 400-character display bound", len(got))
	}
}

func TestSupervisorCheckpointProjectionHidesBaselineAndBoundsSummary(t *testing.T) {
	summary := strings.Repeat("s", 20*1024)
	data, err := json.Marshal(supervisor.CheckpointData{CoversThroughSeq: 17, Summary: summary, NativeBaseline: &supervisor.NativeBaseline{Harness: "codex", Payload: json.RawMessage(`{"encrypted_content":"secret-baseline"}`)}, Reason: "native_auto", Model: "gpt-5"})
	if err != nil {
		t.Fatal(err)
	}
	dto := supervisorRecordDTO(supervisor.Record{Seq: 18, Kind: supervisor.KindCheckpoint, Visibility: supervisor.VisibilityModelOnly, Data: data}, "")
	if dto.Checkpoint == nil || dto.Checkpoint.CoversThroughSeq != 17 || !dto.Checkpoint.HasNativeBaseline || !dto.Checkpoint.Truncated || len(dto.Checkpoint.Summary) > 16*1024 {
		t.Fatalf("checkpoint projection = %+v", dto.Checkpoint)
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-baseline") {
		t.Fatalf("native baseline leaked: %s", encoded)
	}
	markerData, err := json.Marshal(supervisor.MarkerData{Marker: supervisor.MarkerCompacted, Text: "Conversation compacted", Summary: summary})
	if err != nil {
		t.Fatal(err)
	}
	marker := supervisorRecordDTO(supervisor.Record{Seq: 19, Kind: supervisor.KindMarker, Visibility: supervisor.VisibilityDisplayOnly, Data: markerData}, "")
	if marker.Marker == nil || !marker.Marker.Truncated || len(marker.Marker.Summary) > 16*1024 {
		t.Fatalf("marker projection = %+v", marker.Marker)
	}
}

func TestSupervisorRequestOriginProjectsOnStateAndRecords(t *testing.T) {
	sess := &fakeSessionView{id: supervisor.SessionID("c1", 1), featureID: supervisor.FeatureID, status: ports.SessionWaitingPermission}
	child := &llm.ControlRequestMessage{
		RequestID: "req-child",
		Request:   llm.ControlRequest{Subtype: controlSubtypeCanUseTool, ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
		Origin:    llm.EventOrigin{Kind: llm.EventOriginTask, TaskID: "task-1", ChildSessionID: "child-1"},
	}
	root := &llm.ControlRequestMessage{
		RequestID: "req-root",
		Request:   llm.ControlRequest{Subtype: controlSubtypeCanUseTool, ToolName: "Bash", Input: json.RawMessage(`{"command":"pwd"}`)},
		Origin:    llm.EventOrigin{Kind: llm.EventOriginRoot},
	}
	dto := supervisorStateDTO(supervisor.State{Lifecycle: supervisor.LifecycleWaitingPermission, Session: sess, PendingRequests: []*llm.ControlRequestMessage{child, root}})
	if len(dto.PendingRequests) != 2 {
		t.Fatalf("pending = %+v", dto.PendingRequests)
	}
	if got := dto.PendingRequests[0]; got.Origin != RequestOriginChild || got.ChildSessionID != "child-1" {
		t.Fatalf("child pending = %+v", got)
	}
	if got := dto.PendingRequests[1]; got.Origin != RequestOriginRoot || got.ChildSessionID != "" {
		t.Fatalf("root pending = %+v", got)
	}
	payload, err := json.Marshal(dto.PendingRequests)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"origin":"child"`, `"child_session_id":"child-1"`, `"origin":"root"`} {
		if !strings.Contains(string(payload), key) {
			t.Fatalf("wire pending %s lacks %s", payload, key)
		}
	}

	record := func(data supervisor.RequestData) *SupervisorRequestRecord {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return supervisorRecordDTO(supervisor.Record{Seq: 3, Kind: supervisor.KindPermission, Visibility: supervisor.VisibilityDisplayOnly, TurnID: "g1.t1", Data: raw}, "").Request
	}
	if got := record(supervisor.RequestData{RequestID: "req-child", ToolName: "Bash", Stage: supervisor.StageResolved, Outcome: supervisor.RequestAllowed, Origin: supervisor.RequestOriginChild, ChildSessionID: "child-1"}); got.Origin != RequestOriginChild || got.ChildSessionID != "child-1" {
		t.Fatalf("child record = %+v", got)
	}
	// A record written before origins existed reads as root.
	if got := record(supervisor.RequestData{RequestID: "req-old", ToolName: "Bash", Stage: supervisor.StageResolved, Outcome: supervisor.RequestDenied}); got.Origin != RequestOriginRoot || got.ChildSessionID != "" {
		t.Fatalf("legacy record = %+v", got)
	}
}
