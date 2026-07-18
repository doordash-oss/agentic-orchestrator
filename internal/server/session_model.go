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
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	maxSessionListRecentSessions      = 50
	livePreviewTranscriptMessageLimit = 80
)

// roleAssistant, roleSystem and roleUser are the transcript role/message-type
// values for assistant, system and user turns.
const (
	roleAssistant = "assistant"
	roleSystem    = "system"
	roleUser      = "user"
)

// toolUsageFileChangeDetail is the placeholder file-change detail used when a
// tool-use block doesn't carry a more specific description.
const toolUsageFileChangeDetail = "Captured from tool usage."

// transcriptTypeControlRequest, transcriptTypeToolProgress,
// transcriptTypeTaskStarted, transcriptTypeTaskProgress and
// transcriptTypeTaskNotification are TranscriptMessageDTO.Type values for
// redacted system rows.
const (
	transcriptTypeControlRequest   = "control_request"
	transcriptTypeToolProgress     = "tool_progress"
	transcriptTypeTaskStarted      = "task_started"
	transcriptTypeTaskProgress     = "task_progress"
	transcriptTypeTaskNotification = "task_notification"
)

// blockTypeText and blockTypeToolUse are TranscriptMessageDTO.Type values for
// conversational content blocks.
const (
	blockTypeText    = "text"
	blockTypeToolUse = "tool_use"
)

// fileChangeOpUpdate is the FileChangeDTO.Operation value for an in-place
// file update (as opposed to create/delete).
const fileChangeOpUpdate = "update"

// turnStateRunning, turnStateCompleted and turnStateFailed are
// sessionTurnState() values; the latter two are terminal and consumed by
// client_sse.go's isTerminalSessionStatus.
const (
	turnStateRunning   = "running"
	turnStateCompleted = "completed"
	turnStateFailed    = "failed"
)

func (h *apiHandler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	sessions := h.allSessions()
	summaries := make([]SessionSummaryDTO, 0, len(sessions))
	for _, sess := range sessions {
		summaries = append(summaries, sessionSummaryDTO(sess))
	}
	revision := revisionForAny(summaries)
	h.writeRevisionedJSON(w, r, revision, SessionListResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Sessions:   summaries,
	})
}

func (h *apiHandler) handleSessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := h.getSession(sessionID)
	if sess == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session not found", map[string]any{sessionIDErrorField: sessionID})
		return
	}
	detail := sessionDetailFromSummary(sessionSummaryDTO(sess))
	detail.TranscriptCursor = CursorDTO{
		Total: sess.MessageLog().Len(),
		Start: 0,
		End:   sess.MessageLog().Len(),
	}
	detail.PendingControls = pendingControlDTOs(sess)
	detail.InitialPrompt = SafeDisplayText(sess.InitialPrompt(), 2000)
	detail.CanAttach = sess.IsActive()
	detail.LogAvailable = fileExists(sess.LogFilePath())
	detail.SafeError = SafeDisplayText(firstNonEmpty(sess.ErrorDetail(), sess.ExitCodeDetail()), 240)
	revision := revisionForAny(detail)
	h.writeRevisionedJSON(w, r, revision, SessionDetailResponse{
		APIVersion: APIVersion,
		Meta:       h.responseMeta(revision),
		Session:    detail,
	})
}

func (h *apiHandler) handleTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := h.getSession(sessionID)
	if sess == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session not found", map[string]any{sessionIDErrorField: sessionID})
		return
	}
	total := sess.MessageLog().Len()
	offset := int(parseInt64Query(r, "offset", 0))
	limit := int(parseInt64Query(r, "limit", 100))
	if offset < 0 || limit <= 0 || limit > 500 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid transcript bounds", map[string]any{sessionIDErrorField: sessionID})
		return
	}
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	messages := sess.MessageLog().Messages()[offset:end]
	resp := TranscriptResponse{
		APIVersion: APIVersion,
		Cursor:     CursorDTO{Total: total, Start: offset, End: end},
		Messages:   transcriptDTOs(messages, offset, sess.WorkDir()),
	}
	revision := revisionForAny(resp)
	resp.Meta = h.responseMeta(revision)
	h.writeRevisionedJSON(w, r, revision, resp)
}

// handleSessionOutputStream tails a session's transcript live over SSE,
// emitting the same row-indexed TranscriptMessageDTO records /transcript
// returns (see transcriptDTOs) — not raw provider log bytes. This is a
// deliberate choice, not an accident: an earlier raw-byte-tail design let
// this endpoint and the client's separate snapshot-refresh reconciliation
// disagree about what had already been shown, because byte offsets and
// transcript row indices are different coordinate systems with no way to
// reconcile against each other. Serving the same indexed rows both paths
// already agree on eliminates that class of bug at the source. Revisit
// only with a concrete plan for how a byte-oriented consumer would
// reconcile against the index-based snapshot-refresh path — don't reopen
// this without solving that.
func (h *apiHandler) handleSessionOutputStream(w http.ResponseWriter, r *http.Request, sessionID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "streaming unavailable", nil)
		return
	}
	sess := h.getSession(sessionID)
	if sess == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session not found", map[string]any{sessionIDErrorField: sessionID})
		return
	}
	fromOffset := sessionOutputStreamOffset(r)
	if fromOffset < 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid output offset", map[string]any{sessionIDErrorField: sessionID})
		return
	}
	from := int(fromOffset)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		messages := sess.MessageLog().Messages()
		total := len(messages)
		// Re-include the previously-sent tail index on every tick (not just
		// strictly-new ones): UpdateLast/UpdateLastAssistantPartial can mutate
		// the last message in place without growing Len(), so the tail may
		// still be changing. Resending an unchanged row is harmless — the
		// client's row-level dedup (shared with snapshot-refresh
		// reconciliation) treats "same key, same signature" as a no-op.
		tailStart := from
		if tailStart > 0 {
			tailStart--
		}
		if tailStart < total {
			for _, row := range transcriptDTOs(messages[tailStart:], tailStart, sess.WorkDir()) {
				chunk := SessionOutputChunk{APIVersion: APIVersion, SessionID: sessionID, Index: row.Index, Message: row}
				if err := writeSessionOutputSSE(w, "session.output", strconv.Itoa(row.Index), chunk); err != nil {
					return
				}
				flusher.Flush()
			}
			from = total
		}
		if !sess.IsActive() {
			done := SessionOutputChunk{APIVersion: APIVersion, SessionID: sessionID, Index: total - 1, Done: true}
			if err := writeSessionOutputSSE(w, "session.output.done", strconv.Itoa(total-1), done); err != nil {
				return
			}
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// sessionOutputStreamOffset parses the /output/stream resume position from
// the "from" query param or the "Last-Event-ID" header. This is a transcript
// row index (the same index space handleTranscript uses), not a byte offset.
func sessionOutputStreamOffset(r *http.Request) int64 {
	raw := r.URL.Query().Get("from")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return offset
}

func writeSessionOutputSSE(w http.ResponseWriter, event, id string, data SessionOutputChunk) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	setStreamWriteDeadline(w)
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event, payload)
	return err
}

func (h *apiHandler) allSessions() []ports.SessionView {
	if h.sessions == nil {
		return nil
	}
	seen := map[string]bool{}
	var sessions []ports.SessionView
	appendSession := func(sess ports.SessionView) {
		if sess == nil || seen[sess.ID()] {
			return
		}
		seen[sess.ID()] = true
		sessions = append(sessions, sess)
	}
	for _, sess := range h.sessions.ActiveSessions() {
		appendSession(sess)
	}
	for _, sess := range h.sessions.RecentSessions(maxSessionListRecentSessions) {
		appendSession(sess)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].StartedAt().Equal(sessions[j].StartedAt()) {
			return sessions[i].ID() < sessions[j].ID()
		}
		return sessions[i].StartedAt().After(sessions[j].StartedAt())
	})
	return sessions
}

func (h *apiHandler) getSession(sessionID string) ports.SessionView {
	if h.sessions == nil {
		return nil
	}
	return h.sessions.GetSession(sessionID)
}

func sessionSummaryDTO(sess ports.SessionView) SessionSummaryDTO {
	usage := sess.AccumulatedUsage()
	if latest := sess.LatestUsage(); latest != nil {
		usage = *latest
	}
	cost := 0.0
	if result := sess.Cost(); result != nil {
		cost = result.TotalCostUSD
	}
	runNumber := 0
	if runSession, ok := sess.(interface{ RunNumber() int }); ok {
		runNumber = runSession.RunNumber()
	}
	return SessionSummaryDTO{
		ID:           sess.ID(),
		FeatureID:    sess.FeatureID(),
		RunNumber:    runNumber,
		Phase:        sess.Phase().String(),
		Repo:         sess.RepoName(),
		Kind:         sess.Kind().String(),
		Label:        sess.Label(),
		Provider:     sess.ProviderName(),
		Model:        sess.Model(),
		Status:       sess.Status().String(),
		TurnState:    sessionTurnState(sess),
		StartedAt:    sess.StartedAt(),
		Iteration:    sess.Iteration(),
		ContextPct:   max(sess.ContextPercentage(), 0),
		Effort:       string(sess.EffectiveEffort()),
		EffortSource: string(sess.EffortSource()),
		Usage: UsageDTO{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostUSD:      cost,
		},
	}
}

func sessionDetailFromSummary(summary SessionSummaryDTO) SessionDetailDTO {
	return SessionDetailDTO{
		ID:           summary.ID,
		FeatureID:    summary.FeatureID,
		RunNumber:    summary.RunNumber,
		Phase:        summary.Phase,
		Repo:         summary.Repo,
		Kind:         summary.Kind,
		Label:        summary.Label,
		Provider:     summary.Provider,
		Model:        summary.Model,
		Status:       summary.Status,
		TurnState:    summary.TurnState,
		StartedAt:    summary.StartedAt,
		Iteration:    summary.Iteration,
		ContextPct:   summary.ContextPct,
		Effort:       summary.Effort,
		EffortSource: summary.EffortSource,
		Usage:        summary.Usage,
	}
}

func sessionTurnState(sess ports.SessionView) string {
	if sess == nil {
		return ""
	}
	switch sess.Status() {
	case ports.SessionRunning:
		return turnStateRunning
	case ports.SessionWaitingPermission:
		return "waiting_permission"
	case ports.SessionWaitingHelp:
		if sessionHasPendingAskUserControl(sess) {
			return "waiting_question"
		}
		return "waiting_input"
	case ports.SessionDone:
		return turnStateCompleted
	case ports.SessionFailed:
		return turnStateFailed
	default:
		return ""
	}
}

func pendingControlDTOs(sess ports.SessionView) []ControlRequestDTO {
	pending := sess.PendingControlRequests()
	out := make([]ControlRequestDTO, 0, len(pending))
	for _, req := range pending {
		out = append(out, controlRequestDTO(sess, req))
	}
	return out
}

func transcriptDTOs(messages []llm.SDKMessage, start int, workDir ...string) []TranscriptMessageDTO {
	out := make([]TranscriptMessageDTO, 0, len(messages))
	root := ""
	if len(workDir) > 0 {
		root = workDir[0]
	}
	for i, msg := range messages {
		index := start + i
		switch {
		case msg.Assistant != nil:
			out = append(out, conversationDTOs(index, roleAssistant, msg.Assistant.Message.Content, root, false, false, "", 0)...)
		case msg.User != nil:
			out = append(out, conversationDTOs(index, roleUser, msg.User.Message.Content, root, msg.LocallyAppended, msg.AutoPicked, msg.AutoPickQuestion, msg.AutoPickConfidence)...)
		case msg.ControlRequest != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: transcriptTypeControlRequest, Tool: msg.ControlRequest.Request.ToolName, Status: controlRequestStatusPending, Redacted: true})
		case msg.Result != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: "result", Status: msg.Result.Subtype, Redacted: true})
		case msg.Status != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: "status", Text: msg.Status.Message})
		case msg.ToolProgress != nil:
			if rows := fileChangeDTOsFromSDKFileChanges(index, msg.ToolProgress.ToolName, msg.FileChanges, root); len(rows) > 0 {
				out = append(out, rows...)
				continue
			}
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: transcriptTypeToolProgress, Tool: msg.ToolProgress.ToolName, Redacted: true, FileChange: fileChangeDTOFromToolProgress(msg.ToolProgress, root)})
		case msg.TaskStarted != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: transcriptTypeTaskStarted, Redacted: true, Task: taskDTOFromStarted(msg.TaskStarted)})
		case msg.TaskProgress != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: transcriptTypeTaskProgress, Redacted: true, Task: taskDTOFromProgress(msg.TaskProgress)})
		case msg.TaskNotification != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: transcriptTypeTaskNotification, Status: msg.TaskNotification.Status, Redacted: true, Task: taskDTOFromNotification(msg.TaskNotification)})
		default:
			out = append(out, TranscriptMessageDTO{Index: index, Role: roleSystem, Type: msg.Type, Redacted: true})
		}
	}
	return out
}

func conversationDTOs(index int, role string, blocks []llm.ContentBlock, workDir string, locallyAppended bool, autoPicked bool, autoPickQuestion string, autoPickConfidence float64) []TranscriptMessageDTO {
	var out []TranscriptMessageDTO
	for blockIndex, block := range blocks {
		switch {
		case block.IsText():
			userLocal := role == roleUser && locallyAppended
			out = append(out, TranscriptMessageDTO{
				Index:              index,
				BlockIndex:         blockIndex,
				Role:               role,
				Type:               blockTypeText,
				Text:               safeTranscriptText(block.Text, role, locallyAppended),
				LocallyAppended:    userLocal,
				AutoPicked:         userLocal && autoPicked,
				AutoPickQuestion:   autoPickQuestion,
				AutoPickConfidence: autoPickConfidence,
			})
		case block.IsToolUse():
			out = append(out, TranscriptMessageDTO{
				Index:      index,
				BlockIndex: blockIndex,
				Role:       role,
				Type:       blockTypeToolUse,
				Tool:       block.Name,
				Redacted:   true,
				FileChange: fileChangeDTOFromToolUse(block, workDir),
				ToolCall:   toolCallDTOFromToolUse(block),
			})
		case block.IsToolResult():
			out = append(out, TranscriptMessageDTO{Index: index, BlockIndex: blockIndex, Role: role, Type: "tool_result", Redacted: true})
		case block.IsThinking():
			out = append(out, TranscriptMessageDTO{Index: index, BlockIndex: blockIndex, Role: role, Type: "thinking", Redacted: true})
		}
	}
	if len(out) == 0 {
		out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "message", Redacted: true})
	}
	return out
}

func safeTranscriptText(text, role string, locallyAppended bool) string {
	if role == roleUser && !locallyAppended {
		return safeTranscriptPrompt(text)
	}
	if role == roleAssistant {
		return SafeDisplayText(text, 0)
	}
	return SafeDisplayText(text, 500)
}

func toolCallDTOFromToolUse(block llm.ContentBlock) *ToolCallDTO {
	if !block.IsToolUse() {
		return nil
	}
	switch block.Name {
	case "Agent", "Task", "TaskCreate":
	default:
		return nil
	}
	fields := transcriptJSONFields(block.Input)
	summary := firstTranscriptString(fields, "description", "summary", "title")
	prompt := firstTranscriptString(fields, "prompt", "instructions")
	if summary == "" {
		summary = prompt
	}
	if summary == "" && prompt == "" {
		return nil
	}
	return &ToolCallDTO{
		Summary: SafeDisplayText(summary, 500),
		Prompt:  safeTranscriptPrompt(prompt),
	}
}

func taskDTOFromStarted(msg *llm.TaskStartedMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:          SafeDisplayText(msg.TaskID, 200),
		ToolUseID:   SafeDisplayText(msg.ToolUseID, 200),
		Description: SafeDisplayText(msg.Description, 500),
		TaskType:    SafeDisplayText(msg.TaskType, 200),
		Prompt:      safeTranscriptPrompt(msg.Prompt),
	}
}

func taskDTOFromProgress(msg *llm.TaskProgressMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:           SafeDisplayText(msg.TaskID, 200),
		ToolUseID:    SafeDisplayText(msg.ToolUseID, 200),
		Description:  SafeDisplayText(msg.Description, 500),
		LastToolName: SafeDisplayText(msg.LastToolName, 200),
	}
}

func taskDTOFromNotification(msg *llm.TaskNotificationMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:         SafeDisplayText(msg.TaskID, 200),
		ToolUseID:  SafeDisplayText(msg.ToolUseID, 200),
		Status:     SafeDisplayText(msg.Status, 200),
		Summary:    SafeDisplayText(msg.Summary, 1000),
		OutputFile: safeTranscriptPath("", msg.OutputFile),
	}
}

func safeTranscriptPrompt(prompt string) string {
	return SafeDisplayText(truncateTranscriptPrompt(prompt), 4000)
}

func truncateTranscriptPrompt(prompt string) string {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n"))
	if prompt == "" {
		return ""
	}
	const (
		maxChars = 4000
		maxLines = 80
	)
	if len(prompt) > maxChars {
		prompt = strings.TrimSpace(prompt[:maxChars]) + "\n..."
	}
	lines := strings.Split(prompt, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "...")
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func fileChangeDTOFromToolUse(block llm.ContentBlock, workDir string) *FileChangeDTO {
	if !block.IsToolUse() {
		return nil
	}
	input := transcriptJSONFields(block.Input)
	switch block.Name {
	case toolNameEdit, toolNameMultiEdit, toolNameWrite:
		path := firstTranscriptString(input, "file_path", "path", "target_file")
		if path == "" {
			return nil
		}
		op := fileChangeOpUpdate
		if block.Name == toolNameWrite && strings.TrimSpace(firstTranscriptString(input, "old_string")) == "" {
			op = "write"
		}
		detail := transcriptToolUseFileChangeDetail(block.Name, input)
		if detail == "" {
			detail = toolUsageFileChangeDetail
		}
		return &FileChangeDTO{
			Path:      safeTranscriptPath(workDir, path),
			Operation: op,
			Detail:    SafeDisplayText(truncateTranscriptFileChangeDetail(detail), 2000),
		}
	case "Delete":
		path := firstTranscriptString(input, "file_path", "path")
		if path == "" {
			return nil
		}
		return &FileChangeDTO{Path: safeTranscriptPath(workDir, path), Operation: "delete", Detail: toolUsageFileChangeDetail}
	case "Move", "Rename":
		newPath := firstTranscriptString(input, "new_path", "destination_path", "to", "path")
		if newPath == "" {
			return nil
		}
		return &FileChangeDTO{
			Path:      safeTranscriptPath(workDir, newPath),
			OldPath:   safeTranscriptPath(workDir, firstTranscriptString(input, "old_path", "source_path", "from")),
			Operation: "rename",
			Detail:    toolUsageFileChangeDetail,
		}
	default:
		return nil
	}
}

func fileChangeDTOFromToolProgress(progress *llm.ToolProgressMessage, workDir string) *FileChangeDTO {
	if progress == nil || (progress.ToolName != toolNameWrite && progress.ToolName != toolNameEdit) {
		return nil
	}
	path := extractTranscriptFilePathFromProgress(progress.Data)
	if path == "" {
		return nil
	}
	detail := SafeDisplayText(truncateTranscriptFileChangeDetail(progress.Data), 2000)
	if detail == "" {
		detail = "Captured from tool activity."
	}
	return &FileChangeDTO{Path: safeTranscriptPath(workDir, path), Operation: fileChangeOpUpdate, Detail: detail}
}

func fileChangeDTOsFromSDKFileChanges(index int, toolName string, changes []llm.FileChangeEvent, workDir string) []TranscriptMessageDTO {
	if len(changes) == 0 {
		return nil
	}
	rows := make([]TranscriptMessageDTO, 0, len(changes))
	for _, change := range changes {
		dto := fileChangeDTOFromSDKFileChange(change, workDir)
		if dto == nil {
			continue
		}
		rows = append(rows, TranscriptMessageDTO{
			Index:      index,
			Role:       roleSystem,
			Type:       transcriptTypeToolProgress,
			Tool:       firstNonEmpty(toolName, toolNameWrite),
			Redacted:   true,
			FileChange: dto,
		})
	}
	return rows
}

func fileChangeDTOFromSDKFileChange(change llm.FileChangeEvent, workDir string) *FileChangeDTO {
	path := strings.TrimSpace(change.Path)
	if path == "" {
		return nil
	}
	op := strings.TrimSpace(change.Operation)
	if op == "" {
		op = fileChangeOpUpdate
	}
	detail := SafeDisplayText(truncateTranscriptFileChangeDetail(change.Detail), 2000)
	if detail == "" {
		detail = "Captured from provider file change."
	}
	return &FileChangeDTO{
		Path:         safeTranscriptPath(workDir, path),
		OldPath:      safeTranscriptPath(workDir, change.OldPath),
		Operation:    SafeDisplayText(op, 120),
		Detail:       detail,
		AddedLines:   change.AddedLines,
		RemovedLines: change.RemovedLines,
		HasDiffPatch: change.HasDiffPatch,
	}
}

func transcriptToolUseFileChangeDetail(toolName string, fields map[string]interface{}) string {
	oldString := firstTranscriptString(fields, "old_string", "oldText")
	newString := firstTranscriptString(fields, "new_string", "newText", "content")
	if oldString == "" && newString == "" && toolName == toolNameMultiEdit {
		return transcriptMultiEditDetail(fields)
	}
	switch toolName {
	case toolNameEdit, toolNameMultiEdit:
		if oldString == "" && newString == "" {
			return ""
		}
		return formatTranscriptReplacement(oldString, newString)
	case toolNameWrite:
		if newString == "" {
			return ""
		}
		return "+ " + newString
	default:
		return ""
	}
}

func transcriptMultiEditDetail(fields map[string]interface{}) string {
	rawEdits, ok := fields["edits"].([]interface{})
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(rawEdits))
	for _, raw := range rawEdits {
		edit, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		part := formatTranscriptReplacement(firstTranscriptString(edit, "old_string", "oldText"), firstTranscriptString(edit, "new_string", "newText"))
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n")
}

func formatTranscriptReplacement(oldString, newString string) string {
	var lines []string
	if strings.TrimSpace(oldString) != "" {
		for _, line := range strings.Split(strings.TrimSuffix(oldString, "\n"), "\n") {
			lines = append(lines, "- "+line)
		}
	}
	if strings.TrimSpace(newString) != "" {
		for _, line := range strings.Split(strings.TrimSuffix(newString, "\n"), "\n") {
			lines = append(lines, "+ "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func truncateTranscriptFileChangeDetail(detail string) string {
	detail = strings.TrimSpace(strings.ReplaceAll(detail, "\r\n", "\n"))
	if detail == "" {
		return ""
	}
	const (
		maxChars = 2000
		maxLines = 24
	)
	if len(detail) > maxChars {
		detail = strings.TrimSpace(detail[:maxChars]) + "\n..."
	}
	lines := strings.Split(detail, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "...")
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func transcriptJSONFields(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	return fields
}

func firstTranscriptString(fields map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := fields[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func safeTranscriptPath(workDir, filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	if !filepath.IsAbs(filePath) {
		return filepath.Clean(filePath)
	}
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, filePath); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return filepath.Clean(rel)
		}
	}
	return filepath.Base(filePath)
}

var transcriptProgressPathRe = regexp.MustCompile(`(?:^|[\s('"])((?:/|\.?/)?[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+|(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+)`)

func extractTranscriptFilePathFromProgress(data string) string {
	matches := transcriptProgressPathRe.FindStringSubmatch(data)
	if len(matches) < 2 {
		return ""
	}
	return strings.Trim(matches[1], "\"'.,:;)")
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func firstNonEmpty(vals ...string) string {
	for _, val := range vals {
		if val != "" {
			return val
		}
	}
	return ""
}
