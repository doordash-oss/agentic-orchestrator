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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	maxSessionListRecentSessions      = 50
	livePreviewTranscriptMessageLimit = 80
)

func (h *apiHandler) handleSessionList(w http.ResponseWriter, r *http.Request) {
	sessions := h.allSessions()
	summaries := make([]SessionSummaryDTO, 0, len(sessions))
	for _, sess := range sessions {
		summaries = append(summaries, sessionSummaryDTO(sess))
	}
	revision := revisionForAny(summaries)
	writeRevisionedJSON(w, r, http.StatusOK, revision, SessionListResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revision),
		Sessions:   summaries,
	})
}

func (h *apiHandler) handleSessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := h.getSession(sessionID)
	if sess == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session not found", map[string]any{"session_id": sessionID})
		return
	}
	detail := SessionDetailDTO{
		SessionSummaryDTO: sessionSummaryDTO(sess),
		TranscriptCursor: CursorDTO{
			Total: sess.MessageLog().Len(),
			Start: 0,
			End:   sess.MessageLog().Len(),
		},
		PendingControls: pendingControlDTOs(sess),
		InitialPrompt:   safeDisplayText(sess.InitialPrompt(), 2000),
		CanAttach:       sess.IsActive(),
		LogAvailable:    fileExists(sess.LogFilePath()),
		SafeError:       safeDisplayText(firstNonEmpty(sess.ErrorDetail(), sess.ExitCodeDetail()), 240),
	}
	revision := revisionForAny(detail)
	writeRevisionedJSON(w, r, http.StatusOK, revision, SessionDetailResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revision),
		Session:    detail,
	})
}

func (h *apiHandler) handleTranscript(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess := h.getSession(sessionID)
	if sess == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session not found", map[string]any{"session_id": sessionID})
		return
	}
	total := sess.MessageLog().Len()
	offset := int(parseInt64Query(r, "offset", 0))
	limit := int(parseInt64Query(r, "limit", 100))
	if offset < 0 || limit <= 0 || limit > 500 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid transcript bounds", map[string]any{"session_id": sessionID})
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
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
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
	if sess := h.sessions.GetSession(sessionID); sess != nil {
		return sess
	}
	return nil
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
	return SessionSummaryDTO{
		ID:         sess.ID(),
		FeatureID:  sess.FeatureID(),
		Phase:      sess.Phase().String(),
		Repo:       sess.RepoName(),
		Kind:       sess.Kind().String(),
		Label:      sess.Label(),
		Provider:   sess.ProviderName(),
		Model:      sess.Model(),
		Status:     sess.Status().String(),
		StartedAt:  sess.StartedAt(),
		Iteration:  sess.Iteration(),
		ContextPct: sess.ContextPercentage(),
		Usage: UsageDTO{
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostUSD:      cost,
		},
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
			out = append(out, conversationDTOs(index, "assistant", msg.Assistant.Message.Content, root, false, false, "", 0)...)
		case msg.User != nil:
			out = append(out, conversationDTOs(index, "user", msg.User.Message.Content, root, msg.LocallyAppended, msg.AutoPicked, msg.AutoPickQuestion, msg.AutoPickConfidence)...)
		case msg.ControlRequest != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "control_request", Tool: msg.ControlRequest.Request.ToolName, Status: "pending", Redacted: true})
		case msg.Result != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "result", Status: msg.Result.Subtype, Redacted: true})
		case msg.ToolProgress != nil:
			if rows := fileChangeDTOsFromSDKFileChanges(index, msg.ToolProgress.ToolName, msg.FileChanges, root); len(rows) > 0 {
				out = append(out, rows...)
				continue
			}
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "tool_progress", Tool: msg.ToolProgress.ToolName, Redacted: true, FileChange: fileChangeDTOFromToolProgress(msg.ToolProgress, root)})
		case msg.TaskStarted != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "task_started", Redacted: true, Task: taskDTOFromStarted(msg.TaskStarted)})
		case msg.TaskProgress != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "task_progress", Redacted: true, Task: taskDTOFromProgress(msg.TaskProgress)})
		case msg.TaskNotification != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "task_notification", Status: msg.TaskNotification.Status, Redacted: true, Task: taskDTOFromNotification(msg.TaskNotification)})
		default:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: msg.Type, Redacted: true})
		}
	}
	return out
}

func conversationDTOs(index int, role string, blocks []llm.ContentBlock, workDir string, locallyAppended bool, autoPicked bool, autoPickQuestion string, autoPickConfidence float64) []TranscriptMessageDTO {
	var out []TranscriptMessageDTO
	for _, block := range blocks {
		switch {
		case block.IsText():
			userLocal := role == "user" && locallyAppended
			out = append(out, TranscriptMessageDTO{
				Index:              index,
				Role:               role,
				Type:               "text",
				Text:               safeTranscriptText(block.Text, role, locallyAppended),
				LocallyAppended:    userLocal,
				AutoPicked:         userLocal && autoPicked,
				AutoPickQuestion:   autoPickQuestion,
				AutoPickConfidence: autoPickConfidence,
			})
		case block.IsToolUse():
			out = append(out, TranscriptMessageDTO{
				Index:      index,
				Role:       role,
				Type:       "tool_use",
				Tool:       block.Name,
				Redacted:   true,
				FileChange: fileChangeDTOFromToolUse(block, workDir),
				ToolCall:   toolCallDTOFromToolUse(block),
			})
		case block.IsToolResult():
			out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "tool_result", Redacted: true})
		case block.IsThinking():
			out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "thinking", Redacted: true})
		}
	}
	if len(out) == 0 {
		out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "message", Redacted: true})
	}
	return out
}

func safeTranscriptText(text, role string, locallyAppended bool) string {
	if role == "user" && !locallyAppended {
		return safeTranscriptPrompt(text)
	}
	return safeDisplayText(text, 500)
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
		Summary: safeDisplayText(summary, 500),
		Prompt:  safeTranscriptPrompt(prompt),
	}
}

func taskDTOFromStarted(msg *llm.TaskStartedMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:          safeDisplayText(msg.TaskID, 200),
		ToolUseID:   safeDisplayText(msg.ToolUseID, 200),
		Description: safeDisplayText(msg.Description, 500),
		TaskType:    safeDisplayText(msg.TaskType, 200),
		Prompt:      safeTranscriptPrompt(msg.Prompt),
	}
}

func taskDTOFromProgress(msg *llm.TaskProgressMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:           safeDisplayText(msg.TaskID, 200),
		ToolUseID:    safeDisplayText(msg.ToolUseID, 200),
		Description:  safeDisplayText(msg.Description, 500),
		LastToolName: safeDisplayText(msg.LastToolName, 200),
	}
}

func taskDTOFromNotification(msg *llm.TaskNotificationMessage) *TaskDTO {
	if msg == nil {
		return nil
	}
	return &TaskDTO{
		ID:         safeDisplayText(msg.TaskID, 200),
		ToolUseID:  safeDisplayText(msg.ToolUseID, 200),
		Status:     safeDisplayText(msg.Status, 200),
		Summary:    safeDisplayText(msg.Summary, 1000),
		OutputFile: safeTranscriptPath("", msg.OutputFile),
	}
}

func safeTranscriptPrompt(prompt string) string {
	return safeDisplayText(truncateTranscriptPrompt(prompt), 4000)
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
	case "Edit", "MultiEdit", "Write":
		path := firstTranscriptString(input, "file_path", "path", "target_file")
		if path == "" {
			return nil
		}
		op := "update"
		if block.Name == "Write" && strings.TrimSpace(firstTranscriptString(input, "old_string")) == "" {
			op = "write"
		}
		detail := transcriptToolUseFileChangeDetail(block.Name, input)
		if detail == "" {
			detail = "Captured from tool usage."
		}
		return &FileChangeDTO{
			Path:      safeTranscriptPath(workDir, path),
			Operation: op,
			Detail:    safeDisplayText(truncateTranscriptFileChangeDetail(detail), 2000),
		}
	case "Delete":
		path := firstTranscriptString(input, "file_path", "path")
		if path == "" {
			return nil
		}
		return &FileChangeDTO{Path: safeTranscriptPath(workDir, path), Operation: "delete", Detail: "Captured from tool usage."}
	case "Move", "Rename":
		newPath := firstTranscriptString(input, "new_path", "destination_path", "to", "path")
		if newPath == "" {
			return nil
		}
		return &FileChangeDTO{
			Path:      safeTranscriptPath(workDir, newPath),
			OldPath:   safeTranscriptPath(workDir, firstTranscriptString(input, "old_path", "source_path", "from")),
			Operation: "rename",
			Detail:    "Captured from tool usage.",
		}
	default:
		return nil
	}
}

func fileChangeDTOFromToolProgress(progress *llm.ToolProgressMessage, workDir string) *FileChangeDTO {
	if progress == nil || (progress.ToolName != "Write" && progress.ToolName != "Edit") {
		return nil
	}
	path := extractTranscriptFilePathFromProgress(progress.Data)
	if path == "" {
		return nil
	}
	detail := safeDisplayText(truncateTranscriptFileChangeDetail(progress.Data), 2000)
	if detail == "" {
		detail = "Captured from tool activity."
	}
	return &FileChangeDTO{Path: safeTranscriptPath(workDir, path), Operation: "update", Detail: detail}
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
			Role:       "system",
			Type:       "tool_progress",
			Tool:       firstNonEmpty(toolName, "Write"),
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
		op = "update"
	}
	detail := safeDisplayText(truncateTranscriptFileChangeDetail(change.Detail), 2000)
	if detail == "" {
		detail = "Captured from provider file change."
	}
	return &FileChangeDTO{
		Path:         safeTranscriptPath(workDir, path),
		OldPath:      safeTranscriptPath(workDir, change.OldPath),
		Operation:    safeDisplayText(op, 120),
		Detail:       detail,
		AddedLines:   change.AddedLines,
		RemovedLines: change.RemovedLines,
		HasDiffPatch: change.HasDiffPatch,
	}
}

func transcriptToolUseFileChangeDetail(toolName string, fields map[string]interface{}) string {
	oldString := firstTranscriptString(fields, "old_string", "oldText")
	newString := firstTranscriptString(fields, "new_string", "newText", "content")
	if oldString == "" && newString == "" && toolName == "MultiEdit" {
		return transcriptMultiEditDetail(fields)
	}
	switch toolName {
	case "Edit", "MultiEdit":
		if oldString == "" && newString == "" {
			return ""
		}
		return formatTranscriptReplacement(oldString, newString)
	case "Write":
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
