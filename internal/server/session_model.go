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
	"net/http"
	"os"
	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const maxSessionListRecentSessions = 50

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
		Messages:   transcriptDTOs(messages, offset),
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

func transcriptDTOs(messages []llm.SDKMessage, start int) []TranscriptMessageDTO {
	out := make([]TranscriptMessageDTO, 0, len(messages))
	for i, msg := range messages {
		index := start + i
		switch {
		case msg.Assistant != nil:
			out = append(out, conversationDTOs(index, "assistant", msg.Assistant.Message.Content)...)
		case msg.User != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "user", Type: "text", Text: "[redacted]", Redacted: true})
		case msg.ControlRequest != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "control_request", Tool: msg.ControlRequest.Request.ToolName, Status: "pending", Redacted: true})
		case msg.Result != nil:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: "result", Status: msg.Result.Subtype, Redacted: true})
		default:
			out = append(out, TranscriptMessageDTO{Index: index, Role: "system", Type: msg.Type, Redacted: true})
		}
	}
	return out
}

func conversationDTOs(index int, role string, blocks []llm.ContentBlock) []TranscriptMessageDTO {
	var out []TranscriptMessageDTO
	for _, block := range blocks {
		switch {
		case block.IsText():
			out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "text", Text: safeDisplayText(block.Text, 500)})
		case block.IsToolUse():
			out = append(out, TranscriptMessageDTO{Index: index, Role: role, Type: "tool_use", Tool: block.Name, Redacted: true})
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
