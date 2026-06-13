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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	defaultTextLimit = int64(64 * 1024)
	maxTextLimit     = int64(256 * 1024)
)

func (h *apiHandler) handleArtifactList(w http.ResponseWriter, r *http.Request, featureID string, runNumber int) {
	_, run, err := h.featureAndRun(featureID, runNumber)
	if err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	runDir := h.store.RunDir(featureID, runNumber)
	ids := make([]string, 0, len(run.Artifacts))
	for id := range run.Artifacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	artifacts := make([]ArtifactDTO, 0, len(ids))
	for _, id := range ids {
		rel := run.Artifacts[id]
		dto := ArtifactDTO{ID: id, Type: artifactType(rel), Category: artifactCategory(rel), RunNumber: runNumber, Phase: artifactPhase(rel)}
		if path, ok := safeJoin(runDir, rel); ok {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				dto.Size = info.Size()
				dto.ModifiedAt = info.ModTime().UTC()
				dto.ContentAvailable = textArtifact(rel, info.Size())
			}
		}
		artifacts = append(artifacts, dto)
	}
	revision := revisionForAny(artifacts)
	writeRevisionedJSON(w, r, http.StatusOK, revision, ArtifactListResponse{
		APIVersion: APIVersion,
		Meta:       responseMeta(revision),
		Artifacts:  artifacts,
	})
}

func (h *apiHandler) handleArtifactContent(w http.ResponseWriter, r *http.Request, featureID string, runNumber int, artifactID string) {
	if !validEntityID(artifactID) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid artifact id", nil)
		return
	}
	_, run, err := h.featureAndRun(featureID, runNumber)
	if err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	rel, ok := run.Artifacts[artifactID]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "artifact not found", map[string]any{"artifact_id": artifactID})
		return
	}
	path, ok := safeJoin(h.store.RunDir(featureID, runNumber), rel)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid artifact target", map[string]any{"artifact_id": artifactID})
		return
	}
	h.writeTextFileSlice(w, r, artifactID, path)
}

func (h *apiHandler) handleLogContent(w http.ResponseWriter, r *http.Request, featureID string, runNumber int, logID string) {
	if !validEntityID(logID) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid log id", nil)
		return
	}
	if _, _, err := h.featureAndRun(featureID, runNumber); err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	logs := map[string]string{
		"session": filepath.Join("logs", "session.log"),
		"phase":   filepath.Join("logs", "phase.log"),
		"observe": "events.jsonl",
	}
	rel, ok := logs[logID]
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "log not found", map[string]any{"log_id": logID})
		return
	}
	path, ok := safeJoin(h.store.RunDir(featureID, runNumber), rel)
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid log target", map[string]any{"log_id": logID})
		return
	}
	h.writeTextFileSlice(w, r, logID, path)
}

func (h *apiHandler) writeTextFileSlice(w http.ResponseWriter, r *http.Request, id, path string) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, http.StatusNotFound, "not_found", "content not found", map[string]any{"id": id})
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "read content metadata", map[string]any{"id": id})
		return
	}
	if info.IsDir() || !textArtifact(path, info.Size()) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "content is not available as bounded text", map[string]any{"id": id})
		return
	}
	offset := parseInt64Query(r, "offset", 0)
	limit := parseInt64Query(r, "limit", defaultTextLimit)
	if offset < 0 || limit <= 0 || limit > maxTextLimit {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid slice bounds", map[string]any{"id": id})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "open content", map[string]any{"id": id})
		return
	}
	defer file.Close()
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "invalid slice offset", map[string]any{"id": id})
		return
	}
	remaining := info.Size() - offset
	if remaining < 0 {
		remaining = 0
	}
	buf := make([]byte, min(limit, remaining))
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "read content", map[string]any{"id": id})
		return
	}
	text := safeDisplayText(string(buf[:n]), int(limit))
	resp := TextContentResponse{
		APIVersion: APIVersion,
		ID:         id,
		Offset:     offset,
		Limit:      limit,
		Size:       info.Size(),
		Text:       text,
		Truncated:  offset+int64(n) < info.Size(),
	}
	revision := revisionForAny(resp)
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
}

func (h *apiHandler) handleLivePreview(w http.ResponseWriter, r *http.Request, featureID string) {
	f, err := h.loadFeature(featureID)
	if err != nil {
		writeStoreError(w, err, "feature", featureID)
		return
	}
	sess := h.sessionForFeature(featureID)
	resp := LivePreviewResponse{
		APIVersion: APIVersion,
		Feature:    summarizeFeature(f),
		Activity:   f.Status.String(),
		Timing:     timingDTO(f),
		Cost:       costDTO(f),
		Context:    ContextDTO{Percentage: -1},
	}
	if sess != nil {
		summary := sessionSummaryDTO(sess)
		resp.Session = &summary
		resp.Activity = sess.Status().String()
		resp.Context = ContextDTO{Percentage: sess.ContextPercentage()}
		for _, req := range sess.PendingControlRequests() {
			resp.Attention = append(resp.Attention, controlRequestDTO(sess, req))
		}
		resp.Transcript = transcriptDTOs(sess.MessageLog().LastN(5), 0)
	}
	revision := revisionForAny(resp)
	resp.Meta = responseMeta(revision)
	writeRevisionedJSON(w, r, http.StatusOK, revision, resp)
}

func (h *apiHandler) sessionForFeature(featureID string) ports.SessionView {
	if h.sessions == nil {
		return nil
	}
	var active []ports.SessionView
	for _, sess := range h.sessions.ActiveSessions() {
		if sess != nil && sess.FeatureID() == featureID {
			active = append(active, sess)
		}
	}
	if len(active) > 0 {
		sortSessionsForPreview(active)
		return active[0]
	}
	var recent []ports.SessionView
	for _, sess := range h.sessions.RecentSessions(maxSessionListRecentSessions) {
		if sess != nil && sess.FeatureID() == featureID {
			recent = append(recent, sess)
		}
	}
	if len(recent) == 0 {
		return nil
	}
	sortSessionsForPreview(recent)
	return recent[0]
}

func sortSessionsForPreview(sessions []ports.SessionView) {
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].StartedAt().Equal(sessions[j].StartedAt()) {
			return sessions[i].ID() < sessions[j].ID()
		}
		return sessions[i].StartedAt().After(sessions[j].StartedAt())
	})
}

func safeJoin(base, rel string) (string, bool) {
	if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", false
	}
	path := filepath.Join(base, rel)
	cleanBase, err := filepath.Abs(base)
	if err != nil {
		return "", false
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if cleanPath != cleanBase && !strings.HasPrefix(cleanPath, cleanBase+string(os.PathSeparator)) {
		return "", false
	}
	return cleanPath, true
}

func artifactType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	case ".txt", ".log", ".jsonl":
		return "text"
	default:
		return "unknown"
	}
}

func artifactCategory(path string) string {
	if strings.Contains(path, "log") {
		return "log"
	}
	return "artifact"
}

func artifactPhase(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func textArtifact(path string, size int64) bool {
	if size > maxTextLimit*4 {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".yaml", ".yml", ".txt", ".log", ".jsonl":
		return true
	default:
		return false
	}
}

func parseInt64Query(r *http.Request, name string, fallback int64) int64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
