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
	apiPathSupervisor               = "/api/v1/supervisor"
	apiPathSupervisorState          = apiPathSupervisor + "/state"
	apiPathSupervisorSettings       = apiPathSupervisor + "/settings"
	apiPathSupervisorPendingChange  = apiPathSupervisor + "/pending-change/"
	apiPathSupervisorPersistFailure = apiPathSupervisor + "/persist-failure"
	apiPathSupervisorTranscript     = apiPathSupervisor + "/transcript"
	apiPathSupervisorMessages       = apiPathSupervisor + "/messages"
	apiPathSupervisorInterrupt      = apiPathSupervisor + "/interrupt"
	apiPathSupervisorEnd            = apiPathSupervisor + "/end"
	apiPathSupervisorReset          = apiPathSupervisor + "/reset"
	apiPathSupervisorEvents         = apiPathSupervisor + "/events"
)

// maxSupervisorMessageRunes bounds one supervisor message's text.
const maxSupervisorMessageRunes = 100000

var supervisorClientMessageID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// SupervisorService is the supervisor conversation surface the REST and
// SSE routes drive. *supervisor.Coordinator implements it; tests inject
// recording doubles.
type SupervisorService interface {
	State() supervisor.State
	// Lifecycle reports the current lifecycle alone, cheaply, for the
	// work-admission detector.
	Lifecycle() supervisor.Lifecycle
	Transcript(supervisor.PageQuery) (supervisor.Page, error)
	UpdateSettings(supervisor.Settings) (supervisor.State, error)
	ChangeSettings(supervisor.SettingsChange) (supervisor.State, error)
	CancelPendingChange(string) (supervisor.State, error)
	AcknowledgePersistFailure() supervisor.State
	SendMessage(ctx context.Context, msg supervisor.Message) (supervisor.SendResult, error)
	Interrupt() (supervisor.ActionResult, supervisor.State)
	End() (supervisor.ActionResult, supervisor.State)
	Reset() (supervisor.ResetResult, error)
	Subscribe(after int64, hasAfter bool, epoch string) (*supervisor.Subscription, error)
	Unsubscribe(*supervisor.Subscription)
}

// supervisorMutationMethods allowlists the supervisor mutation routes for
// the trusted-mutation preflight.
func supervisorMutationMethods(path string) ([]string, bool) {
	if supervisorPendingChangeID(path) != "" {
		return []string{http.MethodDelete}, true
	}
	switch path {
	case apiPathSupervisorSettings:
		return []string{http.MethodPatch}, true
	case apiPathSupervisorPersistFailure:
		return []string{http.MethodDelete}, true
	case apiPathSupervisorMessages, apiPathSupervisorInterrupt, apiPathSupervisorEnd, apiPathSupervisorReset:
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
	if supervisorPendingChangeID(path) != "" {
		return (*apiHandler).handleSupervisorPendingChange, http.MethodDelete
	}
	switch path {
	case apiPathSupervisorState:
		return (*apiHandler).handleSupervisorState, http.MethodGet
	case apiPathSupervisorSettings:
		return (*apiHandler).handleSupervisorSettings, http.MethodPatch
	case apiPathSupervisorPersistFailure:
		return (*apiHandler).handleSupervisorPersistFailure, http.MethodDelete
	case apiPathSupervisorTranscript:
		return (*apiHandler).handleSupervisorTranscript, http.MethodGet
	case apiPathSupervisorMessages:
		return (*apiHandler).handleSupervisorMessage, http.MethodPost
	case apiPathSupervisorInterrupt:
		return (*apiHandler).handleSupervisorInterrupt, http.MethodPost
	case apiPathSupervisorEnd:
		return (*apiHandler).handleSupervisorEnd, http.MethodPost
	case apiPathSupervisorReset:
		return (*apiHandler).handleSupervisorReset, http.MethodPost
	case apiPathSupervisorEvents:
		return (*apiHandler).handleSupervisorEvents, http.MethodGet
	}
	return nil, ""
}

func (h *apiHandler) handleSupervisorState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(h.supervisor.State())})
}

func (h *apiHandler) handleSupervisorSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Harness            *string `json:"harness"`
		Model              *string `json:"model"`
		Effort             *string `json:"effort"`
		RequestID          string  `json:"request_id"`
		ExpectedGeneration *int64  `json:"expected_generation"`
	}
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if !supervisorClientMessageID.MatchString(req.RequestID) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("request_id is required and must match ^[A-Za-z0-9._-]{1,128}$"))
		return
	}
	if req.ExpectedGeneration == nil || *req.ExpectedGeneration < 0 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("expected_generation is required and must be non-negative"))
		return
	}
	st, err := h.supervisor.ChangeSettings(supervisor.SettingsChange{Harness: req.Harness, Model: req.Model, Effort: req.Effort, RequestID: req.RequestID, ExpectedGeneration: *req.ExpectedGeneration})
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(st)})
}

func supervisorPendingChangeID(path string) string {
	if !strings.HasPrefix(path, apiPathSupervisorPendingChange) {
		return ""
	}
	id := strings.TrimPrefix(path, apiPathSupervisorPendingChange)
	if !supervisorClientMessageID.MatchString(id) || id == "." || id == ".." {
		return ""
	}
	return id
}

func (h *apiHandler) handleSupervisorPendingChange(w http.ResponseWriter, r *http.Request) {
	st, err := h.supervisor.CancelPendingChange(supervisorPendingChangeID(r.URL.Path))
	if err != nil {
		h.writeSupervisorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(st)})
}

func (h *apiHandler) handleSupervisorPersistFailure(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, SupervisorStateResponse{APIVersion: APIVersion, State: supervisorStateDTO(h.supervisor.AcknowledgePersistFailure())})
}

func (h *apiHandler) handleSupervisorTranscript(w http.ResponseWriter, r *http.Request) {
	q, ok := supervisorPageQuery(r)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("before must be a positive integer, after a non-negative integer and limit 1 to 500; before and after are exclusive"))
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
	if q.Before, q.HasBefore, ok = parse("before"); !ok || (q.HasBefore && q.Before < 1) {
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
	attachmentCount := len(req.Images) + len(req.ImageUploads) + len(req.Attachments) + len(req.AttachmentUploads)
	if (strings.TrimSpace(req.Text) == "" && attachmentCount == 0) || utf8.RuneCountInString(req.Text) > maxSupervisorMessageRunes {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("text is required unless the message has attachments and must be at most 100000 characters"))
		return
	}
	if !supervisorClientMessageID.MatchString(req.ClientMessageID) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("client_message_id is required and must match ^[A-Za-z0-9._-]{1,128}$"))
		return
	}
	// Attachments are validated (counts, paths, sizes, references) before
	// anything is copied or sent; the copies are made by the coordinator's
	// staging step and committed only with the user record.
	sources, err := h.supervisorAttachmentSources(req)
	if err != nil {
		writeSupervisorAttachmentError(w, err)
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
	msg := supervisor.Message{Text: req.Text, HiddenContext: hiddenContext, ClientMessageID: req.ClientMessageID}
	if len(sources) > 0 {
		for _, src := range sources {
			msg.Attachments = append(msg.Attachments, src.descriptor)
		}
		msg.Stage = h.stageSupervisorAttachments(sources)
	}
	res, err := h.supervisor.SendMessage(r.Context(), msg)
	if err != nil {
		if !writeSupervisorAttachmentError(w, err) {
			h.writeSupervisorError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, SupervisorMessageResponse{
		APIVersion:   APIVersion,
		Record:       supervisorRecordDTO(res.Record, h.supervisor.State().WorkDir),
		Launched:     res.Launched,
		Deduplicated: res.Deduplicated,
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

// handleSupervisorReset starts a new conversation. Like End it launches
// nothing, so a closed admission boundary does not refuse it.
func (h *apiHandler) handleSupervisorReset(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeMutationJSON(w, r, &body) {
		return
	}
	res, err := h.supervisor.Reset()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError, errcat.WithDiagnostics(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, SupervisorResetResponse{
		APIVersion:             APIVersion,
		Result:                 SupervisorResetResponseResult(res.Result),
		PreviousConversationID: res.PreviousConversationID,
		State:                  supervisorStateDTO(res.State),
	})
}

// writeSupervisorError maps the coordinator's typed refusals to their
// catalog codes.
func (h *apiHandler) writeSupervisorError(w http.ResponseWriter, err error) {
	if h.writeAdmissionRefusal(w, err) {
		return
	}
	var invalid *supervisor.SettingsInvalidError
	var stale *supervisor.StaleGenerationError
	var pending *supervisor.ChangePendingError
	var launch *supervisor.LaunchFailedError
	var conflict *supervisor.ClientMessageConflictError
	var outOfRange *supervisor.CursorOutOfRangeError
	switch {
	case errors.Is(err, supervisor.ErrSettingsLocked):
		writeAPIError(w, http.StatusConflict, errcat.SupervisorSettingsLocked)
	case errors.As(err, &invalid):
		writeAPIError(w, http.StatusBadRequest, errcat.SupervisorSettingsInvalid, errcat.WithDiagnostics(invalid.Reason))
	case errors.As(err, &stale):
		writeAPIError(w, http.StatusConflict, errcat.StaleGeneration, errcat.WithDiagnostics(fmt.Sprintf("current_generation=%d", stale.Current)))
	case errors.As(err, &pending):
		writeAPIError(w, http.StatusConflict, errcat.ChangePending, errcat.WithDiagnostics("pending_request_id="+pending.RequestID))
	case errors.Is(err, supervisor.ErrPendingChangeNotFound):
		writeAPIError(w, http.StatusNotFound, errcat.PendingChangeNotFound)
	case errors.Is(err, supervisor.ErrSettingsRequired):
		writeAPIError(w, http.StatusConflict, errcat.SettingsRequired)
	case errors.Is(err, supervisor.ErrTurnActive):
		writeAPIError(w, http.StatusConflict, errcat.TurnActive)
	case errors.As(err, &conflict):
		writeAPIError(w, http.StatusConflict, errcat.ClientMessageConflict, errcat.WithDiagnostics(fmt.Sprintf("committed_seq=%d", conflict.CommittedSeq)))
	case errors.As(err, &outOfRange):
		writeAPIError(w, http.StatusConflict, errcat.CursorOutOfRange, errcat.WithDiagnostics(fmt.Sprintf("head_seq=%d", outOfRange.HeadSeq)))
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
	failure := wireError(errcat.New(errcat.SupervisorLaunchFailed, errcat.WithDiagnostics(SafeDisplayText(launch.Err.Error(), 400))))
	if launch.AttemptedSettings != nil {
		attempted := launch.AttemptedSettings
		failure.AttemptedSettings = &SupervisorSettings{Harness: attempted.Harness, Model: attempted.Model, Effort: attempted.Effort}
	}
	return failure
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
	dto.BackgroundTasks = make([]SupervisorBackgroundTask, 0, len(st.BackgroundTasks))
	for _, task := range st.BackgroundTasks {
		activity := make([]SupervisorBackgroundActivity, 0, len(task.Activity))
		for _, event := range task.Activity {
			activity = append(activity, SupervisorBackgroundActivity{At: event.At, Detail: SafeDisplayText(event.Detail, 400)})
		}
		dto.BackgroundTasks = append(dto.BackgroundTasks, SupervisorBackgroundTask{
			Activity: activity,
			ID:       task.ID, ProviderID: task.ProviderID, Generation: task.Generation,
			Kind: SupervisorBackgroundTaskKind(task.Kind), State: SupervisorBackgroundTaskState(task.State),
			Title: SafeDisplayText(task.Title, 160), Detail: SafeDisplayText(task.Detail, 400),
			Schedule: SafeDisplayText(task.Schedule, 120), StartedAt: task.StartedAt, UpdatedAt: task.UpdatedAt, ExpiresAt: task.ExpiresAt,
		})
	}
	if st.ContextUsage != nil {
		dto.ContextUsage = &SupervisorContextUsage{Percent: st.ContextUsage.Percent, UsedTokens: st.ContextUsage.UsedTokens, WindowTokens: st.ContextUsage.WindowTokens}
	}
	if st.PendingChange != nil {
		dto.PendingChange = &SupervisorPendingChange{RequestID: st.PendingChange.RequestID, Kind: SupervisorPendingChangeKind(st.PendingChange.Kind), Target: SupervisorSettings{Harness: st.PendingChange.Target.Harness, Model: st.PendingChange.Target.Model, Effort: st.PendingChange.Target.Effort}, RequestedAt: st.PendingChange.RequestedAt}
	}
	if dto.PermissionMode.Requested == "" {
		dto.PermissionMode.Requested = supervisor.RequestedPermissionMode
	}
	if st.Failure != nil && st.Lifecycle == supervisor.LifecycleFailed {
		failure := supervisorLaunchFailure(st.Failure)
		dto.Failure = &failure
	}
	if st.PersistFailure != nil {
		failure := wireError(errcat.New(errcat.SupervisorHistoryIncomplete, errcat.WithDiagnostics(SafeDisplayText(st.PersistFailure.Error(), 400))))
		dto.PersistFailure = &failure
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
		dto.Attachments = supervisorAttachmentDTOs(data.Attachments)
	case supervisor.KindAssistant, supervisor.KindToolUse:
		var data supervisor.ContentData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Messages = conversationDTOs(index, roleAssistant, data.Content, workDir, false, false, "", 0)
		if data.TaskStarted != nil || data.TaskProgress != nil || data.TaskNotification != nil {
			dto.Messages = transcriptDTOs([]llm.SDKMessage{{TaskStarted: data.TaskStarted, TaskProgress: data.TaskProgress, TaskNotification: data.TaskNotification}}, index, workDir)
		}
	case supervisor.KindToolResult:
		var data supervisor.ContentData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Messages = conversationDTOs(index, roleUser, data.Content, workDir, false, false, "", 0)
		if data.ObservedFiles {
			dto.Messages = nil
		}
		for _, row := range fileChangeDTOsFromSDKFileChanges(index, "Write", data.FileChanges, workDir) {
			row.BlockIndex = len(dto.Messages)
			dto.Messages = append(dto.Messages, row)
		}
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
		summary, truncated := boundedSummary(data.Summary)
		dto.Marker = &SupervisorMarkerRecord{
			Marker:      SupervisorMarkerRecordMarker(data.Marker),
			Text:        SafeDisplayText(data.Text, markerTextLimit(data.Marker)),
			Code:        data.Code,
			FromHarness: data.FromHarness,
			ToHarness:   data.ToHarness,
			Summary:     summary,
			Truncated:   truncated,
		}
	case supervisor.KindCheckpoint:
		var data supervisor.CheckpointData
		_ = json.Unmarshal(rec.Data, &data)
		summary, truncated := boundedSummary(data.Summary)
		dto.Checkpoint = &SupervisorCheckpointRecord{CoversThroughSeq: data.CoversThroughSeq, Reason: SupervisorCheckpointRecordReason(data.Reason), Model: data.Model, Summary: summary, Truncated: truncated, HasNativeBaseline: data.NativeBaseline != nil}
	case supervisor.KindNote:
		var data supervisor.NoteData
		_ = json.Unmarshal(rec.Data, &data)
		dto.Note = SafeDisplayText(data.Text, 400)
	}
	if dto.Messages == nil {
		dto.Messages = []TranscriptMessage{}
	}
	return dto
}

// markerTextLimit bounds marker prose for display. The transcript-recovery
// marker ends with the preserved transcript's path, which users copy to find
// the backup, so its bound leaves room for a full filesystem path.
func markerTextLimit(marker string) int {
	if marker == supervisor.MarkerTranscriptRecovered {
		return 400 + 4096
	}
	return 400
}

func boundedSummary(input string) (string, bool) {
	const limit = 16 * 1024
	value := SafeDisplayText(input, 0)
	if len(value) <= limit {
		return value, false
	}
	end := limit - len("...")
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "...", true
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
				// A consumer that fell behind and a conversation reset both
				// re-snapshot; any other close ends the stream.
				if !(sub.Overflowed() || sub.ConversationReset()) || !reset() {
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
// is starting, running, or waiting on a permission or question, and as
// waiting in the two waiting states: a waiting supervisor does not hold up
// an unattended install. Both facts come from one lifecycle read.
func (h *apiHandler) detectSupervisorActivity(context.Context) (workadmission.Activity, error) {
	if h.supervisor == nil {
		return workadmission.Activity{}, nil
	}
	lifecycle := h.supervisor.Lifecycle()
	return workadmission.Activity{SupervisorActive: lifecycle.Active(), SupervisorWaiting: lifecycle.Waiting()}, nil
}
