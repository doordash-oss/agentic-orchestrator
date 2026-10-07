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
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

const (
	apiPathSupervisor           = "/api/v1/supervisor"
	apiPathSupervisorState      = apiPathSupervisor + "/state"
	apiPathSupervisorSettings   = apiPathSupervisor + "/settings"
	apiPathSupervisorTranscript = apiPathSupervisor + "/transcript"
	apiPathSupervisorMessages   = apiPathSupervisor + "/messages"
	apiPathSupervisorInterrupt  = apiPathSupervisor + "/interrupt"
	apiPathSupervisorEnd        = apiPathSupervisor + "/end"
	apiPathSupervisorEvents     = apiPathSupervisor + "/events"
)

// maxSupervisorMessageRunes bounds one supervisor message's text.
const maxSupervisorMessageRunes = 100000

var supervisorClientMessageID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// SupervisorService is the supervisor conversation surface the REST and
// SSE routes drive. *supervisor.Coordinator implements it; tests inject
// recording doubles.
type SupervisorService interface {
	State() supervisor.State
	Busy() bool
	Transcript(supervisor.PageQuery) (supervisor.Page, error)
	UpdateSettings(supervisor.Settings) (supervisor.State, error)
	Send(ctx context.Context, text, hiddenContext, clientMessageID string) (supervisor.SendResult, error)
	Interrupt() (supervisor.ActionResult, supervisor.State)
	End() (supervisor.ActionResult, supervisor.State)
	Subscribe(after int64, hasAfter bool, epoch string) (*supervisor.Subscription, error)
	Unsubscribe(*supervisor.Subscription)
}

// supervisorMutationMethods allowlists the supervisor mutation routes for
// the trusted-mutation preflight.
func supervisorMutationMethods(path string) ([]string, bool) {
	switch path {
	case apiPathSupervisorSettings:
		return []string{http.MethodPatch}, true
	case apiPathSupervisorMessages, apiPathSupervisorInterrupt, apiPathSupervisorEnd:
		return []string{http.MethodPost}, true
	}
	return nil, false
}

// handleSupervisorRoutes dispatches the /api/v1/supervisor/ namespace.
func (h *apiHandler) handleSupervisorRoutes(w http.ResponseWriter, r *http.Request) {
	route, method := supervisorRoute(r.URL.Path)
	if route == nil {
		writeAPIError(w, http.StatusNotFound, errcat.NotFound, errcat.WithParams(errcat.SubjectParams{Subject: "Endpoint"}))
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if h.supervisor == nil {
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
		return
	}
	if method != http.MethodGet && !h.requireTrustedClient(w, r) {
		return
	}
	route(h, w, r)
}

func supervisorRoute(path string) (func(*apiHandler, http.ResponseWriter, *http.Request), string) {
	switch path {
	case apiPathSupervisorState:
		return (*apiHandler).handleSupervisorState, http.MethodGet
	case apiPathSupervisorSettings:
		return (*apiHandler).handleSupervisorSettings, http.MethodPatch
	case apiPathSupervisorTranscript:
		return (*apiHandler).handleSupervisorTranscript, http.MethodGet
	case apiPathSupervisorMessages:
		return (*apiHandler).handleSupervisorMessage, http.MethodPost
	case apiPathSupervisorInterrupt:
		return (*apiHandler).handleSupervisorInterrupt, http.MethodPost
	case apiPathSupervisorEnd:
		return (*apiHandler).handleSupervisorEnd, http.MethodPost
	case apiPathSupervisorEvents:
		return (*apiHandler).handleSupervisorEvents, http.MethodGet
	}
	return nil, ""
}

func (h *apiHandler) handleSupervisorState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(h.supervisor.State())})
}

func (h *apiHandler) handleSupervisorSettings(w http.ResponseWriter, r *http.Request) {
	var req SupervisorSettingsRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	settings := supervisor.Settings{
		Harness: strings.TrimSpace(req.Harness),
		Model:   strings.TrimSpace(req.Model),
		Effort:  strings.TrimSpace(req.Effort),
	}
	if !settings.Complete() {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("harness and model are required"))
		return
	}
	st, err := h.supervisor.UpdateSettings(settings)
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(st)})
}

func (h *apiHandler) handleSupervisorTranscript(w http.ResponseWriter, r *http.Request) {
	q, ok := supervisorPageQuery(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("before, after and limit must be non-negative integers; before and after are exclusive"))
		return
	}
	page, err := h.supervisor.Transcript(q)
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	st := h.supervisor.State()
	items := make([]SupervisorRecord, 0, len(page.Items))
	for _, rec := range page.Items {
		items = append(items, supervisorRecordDTO(rec, st.WorkDir))
	}
	writeJSON(w, http.StatusOK, SupervisorTranscriptResponse{
		APIVersion:     APIVersion,
		ConversationID: st.ConversationID,
		Items:          items,
		FirstSeq:       page.FirstSeq,
		LastSeq:        page.LastSeq,
		HasMoreBefore:  page.HasMoreBefore,
		HasMoreAfter:   page.HasMoreAfter,
		HeadSeq:        page.HeadSeq,
	})
}

func supervisorPageQuery(r *http.Request) (supervisor.PageQuery, bool) {
	var q supervisor.PageQuery
	values := r.URL.Query()
	parse := func(name string) (int64, bool, bool) {
		raw := values.Get(name)
		if raw == "" {
			return 0, false, true
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return 0, false, false
		}
		return n, true, true
	}
	var ok bool
	if q.Before, q.HasBefore, ok = parse("before"); !ok {
		return q, false
	}
	if q.After, q.HasAfter, ok = parse("after"); !ok {
		return q, false
	}
	limit, hasLimit, ok := parse("limit")
	if !ok || (hasLimit && (limit < 1 || limit > 500)) || (q.HasBefore && q.HasAfter) {
		return q, false
	}
	q.Limit = int(limit)
	return q, true
}

func (h *apiHandler) handleSupervisorMessage(w http.ResponseWriter, r *http.Request) {
	// A send may launch the supervisor process: a closed admission
	// boundary refuses it with the canonical 503 before anything commits.
	if h.refuseAdmissionClosed(w) {
		return
	}
	var req SupervisorMessageRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" || utf8.RuneCountInString(req.Text) > maxSupervisorMessageRunes {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("text is required and must be at most 100000 characters"))
		return
	}
	if !supervisorClientMessageID.MatchString(req.ClientMessageID) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("client_message_id is required and must match ^[A-Za-z0-9._-]{1,128}$"))
		return
	}
	// The error reference is validated and resolved against durable state
	// before anything is sent or appended; the bundle reaches the harness
	// as hidden context and never enters the record or the response.
	if field := validateChatContextReference(req.ErrorReference); field != "" {
		writeChatContextInvalid(w, req.ErrorReference, field)
		return
	}
	hiddenContext := ""
	if !chatContextAbsent(req.ErrorReference) {
		bundle, rejection := h.resolveChatContext(req.ErrorReference)
		if rejection != nil {
			rejection.write(w, req.ErrorReference)
			return
		}
		hiddenContext = bundle
	}
	res, err := h.supervisor.Send(r.Context(), req.Text, hiddenContext, req.ClientMessageID)
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SupervisorMessageResponse{
		APIVersion: APIVersion,
		Record:     supervisorRecordDTO(res.Record, h.supervisor.State().WorkDir),
		Launched:   res.Launched,
	})
}

func (h *apiHandler) handleSupervisorInterrupt(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeMutationJSON(w, r, &body) {
		return
	}
	result, st := h.supervisor.Interrupt()
	writeJSON(w, http.StatusOK, SupervisorActionResponse{APIVersion: APIVersion, Result: SupervisorActionResponseResult(result), State: supervisorStateDTO(st)})
}

func (h *apiHandler) handleSupervisorEnd(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeMutationJSON(w, r, &body) {
		return
	}
	result, st := h.supervisor.End()
	writeJSON(w, http.StatusOK, SupervisorActionResponse{APIVersion: APIVersion, Result: SupervisorActionResponseResult(result), State: supervisorStateDTO(st)})
}

// writeSupervisorError maps the coordinator's typed refusals to their
// catalog codes.
func (h *apiHandler) writeSupervisorError(w http.ResponseWriter, err error) {
	if h.writeAdmissionRefusal(w, err) {
		return
	}
	var invalid *supervisor.SettingsInvalidError
	var launch *supervisor.LaunchFailedError
	switch {
	case errors.Is(err, supervisor.ErrSettingsLocked):
		writeAPIError(w, http.StatusConflict, errcat.SupervisorSettingsLocked)
	case errors.As(err, &invalid):
		writeAPIError(w, http.StatusBadRequest, errcat.SupervisorSettingsInvalid, errcat.WithDiagnostics(invalid.Reason))
	case errors.Is(err, supervisor.ErrSettingsRequired):
		writeAPIError(w, http.StatusConflict, errcat.SettingsRequired)
	case errors.Is(err, supervisor.ErrTurnActive):
		writeAPIError(w, http.StatusConflict, errcat.TurnActive)
	case errors.As(err, &launch):
		writeJSON(w, http.StatusBadGateway, ErrorResponse{APIVersion: APIVersion, Error: supervisorLaunchFailure(launch)})
	case errors.Is(err, supervisor.ErrClosed):
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable)
	case errors.Is(err, supervisor.ErrInvalidPageQuery):
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("invalid transcript page query"))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeAPIError(w, http.StatusServiceUnavailable, errcat.Unavailable, errcat.WithDiagnostics("request ended before the supervisor accepted the message"))
	default:
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
	}
}

// supervisorLaunchFailure renders a launch failure as the canonical error
// both the failed send and the read model's failure field carry.
func supervisorLaunchFailure(launch *supervisor.LaunchFailedError) Error {
	return wireError(errcat.New(errcat.SupervisorLaunchFailed, errcat.WithDiagnostics(SafeDisplayText(launch.Err.Error(), 400))))
}

// supervisorStateDTO projects the read model; pending requests reuse the
// session control-request projection so they answer through the existing
// permission and question routes.
func supervisorStateDTO(st supervisor.State) SupervisorState {
	pending := make([]ControlRequest, 0, len(st.PendingRequests))
	if st.Session != nil {
		for _, req := range st.PendingRequests {
			pending = append(pending, controlRequestDTO(st.Session, req))
		}
	}
	interruptedBy := st.InterruptedBy
	if interruptedBy == "" {
		interruptedBy = supervisor.InterruptedByNone
	}
	dto := SupervisorState{
		ConversationID:  st.ConversationID,
		Generation:      st.Generation,
		SessionID:       st.SessionID,
		Lifecycle:       SupervisorLifecycle(st.Lifecycle),
		StartingStep:    SupervisorStartingStep(st.StartingStep),
		LastTurnOutcome: SupervisorTurnOutcome(st.LastTurnOutcome),
		InterruptedBy:   SupervisorInterruptedBy(interruptedBy),
		Settings:        SupervisorSettings{Harness: st.Settings.Harness, Model: st.Settings.Model, Effort: st.Settings.Effort},
		EffectiveModel:  st.EffectiveModel,
		PermissionMode: SupervisorPermissionMode{
			Requested:          st.PermissionMode.Requested,
			Effective:          st.PermissionMode.Effective,
			RestrictedByPolicy: st.PermissionMode.RestrictedByPolicy,
		},
		PendingRequests: pending,
		HeadSeq:         st.HeadSeq,
		StreamEpoch:     st.StreamEpoch,
	}
	if dto.PermissionMode.Requested == "" {
		dto.PermissionMode.Requested = supervisor.RequestedPermissionMode
	}
	if st.Failure != nil && st.Lifecycle == supervisor.LifecycleFailed {
		failure := supervisorLaunchFailure(st.Failure)
		dto.Failure = &failure
	}
	return dto
}

// supervisorRecordDTO projects one committed record with the same
// redaction the session transcript applies; message rows carry
// index = seq.
func supervisorRecordDTO(rec supervisor.Record, workDir string) SupervisorRecord {
	dto := SupervisorRecord{
		Seq:             rec.Seq,
		ID:              rec.ID,
		ConversationID:  rec.ConversationID,
		Generation:      rec.Generation,
		TurnID:          rec.TurnID,
		Kind:            SupervisorRecordKind(rec.Kind),
		Visibility:      SupervisorRecordVisibility(rec.Visibility),
		CreatedAt:       rec.CreatedAt,
		ClientMessageID: rec.ClientMessageID,
		StreamMessageID: rec.StreamMessageID,
	}
	index := int(rec.Seq)
	switch rec.Kind {
	case supervisor.KindUser:
		var data supervisor.UserData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Messages = conversationDTOs(index, roleUser, []llm.ContentBlock{{Type: blockTypeText, Text: data.Text}}, workDir, true, false, "", 0)
	case supervisor.KindAssistant, supervisor.KindToolUse:
		var data supervisor.ContentData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Messages = conversationDTOs(index, roleAssistant, data.Content, workDir, false, false, "", 0)
	case supervisor.KindToolResult:
		var data supervisor.ContentData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Messages = conversationDTOs(index, roleUser, data.Content, workDir, false, false, "", 0)
	case supervisor.KindPermission, supervisor.KindQuestion:
		var data supervisor.RequestData
		_ = json.Unmarshal(rec.Data, &data)
		summary := safeControlSummary(&llm.ControlRequestMessage{
			RequestID: data.RequestID,
			Request:   llm.ControlRequest{ToolName: data.ToolName, Input: data.Input},
		})
		dto.Request = &SupervisorRequestRecord{
			RequestID: data.RequestID,
			ToolName:  data.ToolName,
			Stage:     SupervisorRequestRecordStage(data.Stage),
			Outcome:   SupervisorRequestRecordOutcome(data.Outcome),
			Summary:   summary,
			Origin:    RequestOriginRoot,
		}
		if data.Origin == supervisor.RequestOriginChild {
			dto.Request.Origin, dto.Request.ChildSessionID = RequestOriginChild, data.ChildSessionID
		}
		dto.Messages = []TranscriptMessage{{Index: index, Role: roleSystem, Type: transcriptTypeControlRequest, Tool: data.ToolName, Status: data.Outcome, Redacted: true}}
	case supervisor.KindMarker:
		var data supervisor.MarkerData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Marker = &SupervisorMarkerRecord{
			Marker: SupervisorMarkerRecordMarker(data.Marker),
			Text:   SafeDisplayText(data.Text, 400),
			Code:   data.Code,
		}
	}
	if dto.Messages == nil {
		dto.Messages = []TranscriptMessage{}
	}
	return dto
}

// safeDeltaText applies the display redactions without trimming, so
// whitespace between streamed chunks survives.
func safeDeltaText(s string) string {
	s = strings.ReplaceAll(s, "private-token", "[redacted]")
	return strings.ReplaceAll(s, "raw initial prompt", "[redacted prompt]")
}

// handleSupervisorEvents streams supervisor events: the current state
// first, then committed records after the cursor, then live events. A
// cursor beyond the head, a stale epoch, or a consumer that falls behind
// receives stream.reset and must re-snapshot; the stream then continues
// live from the head, so the client never has to reconnect.
func (h *apiHandler) handleSupervisorEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics("streaming unavailable"))
		return
	}
	after, hasAfter, valid := supervisorEventCursor(r)
	sub, err := h.supervisor.Subscribe(after, hasAfter, r.URL.Query().Get("epoch"))
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	defer func() { h.supervisor.Unsubscribe(sub) }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	st := sub.State
	envelope := func(kind SupervisorStreamEventKind, generation int64) SupervisorStreamEvent {
		return SupervisorStreamEvent{Kind: kind, ConversationID: st.ConversationID, Generation: generation, StreamEpoch: st.StreamEpoch}
	}
	head := st.HeadSeq
	write := func(ev SupervisorStreamEvent) bool {
		if err := writeSupervisorSSE(w, ev); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	// reset tells the client to re-snapshot and re-subscribes live from
	// the head; records committed meanwhile reach the client through the
	// snapshot, the live stream, or both (the client dedupes by seq).
	reset := func() bool {
		h.supervisor.Unsubscribe(sub)
		next, err := h.supervisor.Subscribe(0, false, "")
		if err != nil {
			return false
		}
		sub, st = next, next.State
		head = st.HeadSeq
		ev := envelope(SupervisorEventStreamReset, st.Generation)
		ev.Seq = head
		ev.SnapshotRequired = true
		return write(ev)
	}
	stateEv := envelope(SupervisorEventState, st.Generation)
	stateDTO := supervisorStateDTO(st)
	stateEv.State = &stateDTO
	stateEv.Seq = head
	if !write(stateEv) {
		return
	}
	if sub.Reset || !valid {
		if !reset() {
			return
		}
	}
	for _, rec := range sub.Replay {
		if !write(supervisorRecordEvent(envelope(SupervisorEventRecord, rec.Generation), rec, st.WorkDir)) {
			return
		}
	}
	ticker := time.NewTicker(heartbeatInterval(r))
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-sub.Events():
			if !open {
				if !sub.Overflowed() || !reset() {
					return
				}
				continue
			}
			var out SupervisorStreamEvent
			switch ev.Kind {
			case supervisor.EventRecord:
				head = ev.Record.Seq
				out = supervisorRecordEvent(envelope(SupervisorEventRecord, ev.Generation), *ev.Record, st.WorkDir)
			case supervisor.EventDelta:
				out = envelope(SupervisorEventDelta, ev.Generation)
				out.Seq = head
				out.Delta = &SupervisorDelta{
					TurnID:          ev.Delta.TurnID,
					StreamMessageID: ev.Delta.StreamMessageID,
					ChunkIndex:      ev.Delta.ChunkIndex,
					Text:            safeDeltaText(ev.Delta.Text),
				}
			case supervisor.EventState:
				head = ev.State.HeadSeq
				out = envelope(SupervisorEventState, ev.Generation)
				out.Seq = head
				dto := supervisorStateDTO(*ev.State)
				out.State = &dto
			case supervisor.EventRequest:
				if ev.Session == nil || ev.Request == nil {
					continue
				}
				out = envelope(SupervisorEventRequest, ev.Generation)
				out.Seq = head
				req := controlRequestDTO(ev.Session, ev.Request)
				out.Request = &req
			default:
				continue
			}
			if !write(out) {
				return
			}
		case <-ticker.C:
			ev := envelope(SupervisorEventHeartbeat, st.Generation)
			ev.Seq = head
			if !write(ev) {
				return
			}
		}
	}
}

func supervisorRecordEvent(ev SupervisorStreamEvent, rec supervisor.Record, workDir string) SupervisorStreamEvent {
	dto := supervisorRecordDTO(rec, workDir)
	ev.Seq = rec.Seq
	ev.Record = &dto
	return ev
}

// supervisorEventCursor reads the resume cursor from ?after or
// Last-Event-ID. A malformed cursor is reported invalid so the stream
// resets rather than silently replaying from the start.
func supervisorEventCursor(r *http.Request) (after int64, hasAfter, valid bool) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, false, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false, false
	}
	return n, true, true
}

// writeSupervisorSSE writes one event; only record events carry an SSE id,
// so Last-Event-ID always names a committed record seq.
func writeSupervisorSSE(w http.ResponseWriter, ev SupervisorStreamEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	setStreamWriteDeadline(w)
	if ev.Kind == SupervisorEventRecord {
		_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, payload)
	} else {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload)
	}
	return err
}

// detectSupervisorActivity reports the supervisor as active work while it
// is starting, running, or waiting on a permission or question.
func (h *apiHandler) detectSupervisorActivity(context.Context) (workadmission.Activity, error) {
	if h.supervisor == nil {
		return workadmission.Activity{}, nil
	}
	return workadmission.Activity{SupervisorActive: h.supervisor.Busy()}, nil
}
