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
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const sessionSegmentBoundaryText = "Session resumed"

// handleRunSessionTranscript serves GET
// /api/v1/features/{feature_id}/runs/{run_number}/sessions/{session_id}/transcript:
// every segment the session wrote in that run, read from disk and joined with
// a boundary row, so reused IDs never show another run's rows.
func (h *apiHandler) handleRunSessionTranscript(w http.ResponseWriter, r *http.Request, featureID string, runNumber int, sessionID string) {
	if !validEntityID(sessionID) {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest, errcat.WithDiagnostics("invalid session id"))
		return
	}
	if _, _, err := h.featureAndRun(featureID, runNumber); err != nil {
		writeStoreError(w, err, featureID)
		return
	}
	messages, workDir, ok := h.runSessionTranscript(featureID, runNumber, sessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, errcat.NotFound,
			errcat.WithParams(errcat.SubjectParams{Subject: "Session", Name: sessionID}))
		return
	}
	h.writeTranscriptPage(w, r, sessionID, messages, workDir)
}

func (h *apiHandler) runSessionTranscript(featureID string, runNumber int, sessionID string) ([]llm.SDKMessage, string, bool) {
	var live []llm.SDKMessage
	workDir := ""
	liveFound := false
	if sess := h.getSession(sessionID); sess != nil && sess.FeatureID() == featureID && sessionRunNumber(sess) == runNumber {
		live, workDir, liveFound = sess.MessageLog().Messages(), sess.WorkDir(), true
	}
	featureDir := filepath.Dir(filepath.Dir(h.store.RunDir(featureID, runNumber)))
	paths, err := session.TranscriptSegments(featureDir, sessionID, runNumber)
	if err != nil {
		log.Printf("server: %v", err)
	}
	if len(paths) == 0 {
		if liveFound {
			return live, workDir, true
		}
		legacy := session.LegacyTranscriptPath(featureDir, sessionID)
		if _, err := os.Stat(legacy); err != nil || h.laterRunHasSession(featureID, runNumber, sessionID) {
			return nil, "", false
		}
		paths = []string{legacy}
	}
	var messages []llm.SDKMessage
	for i, path := range paths {
		rows, err := session.ReadPersistedTranscript(path)
		if err != nil {
			log.Printf("server: %v", err)
			continue
		}
		if i > 0 && len(messages) > 0 {
			messages = append(messages, llm.SDKMessage{
				Type:   "status",
				Status: &llm.StatusMessage{Type: "status", Message: sessionSegmentBoundaryText},
			})
		}
		messages = append(messages, rows...)
	}
	return messages, workDir, true
}

// laterRunHasSession reports whether a run after runNumber recorded
// sessionID, in which case a pre-segment transcript file holds that run's
// rows instead.
func (h *apiHandler) laterRunHasSession(featureID string, runNumber int, sessionID string) bool {
	runs, err := h.store.ListRuns(featureID)
	if err != nil {
		return true
	}
	for _, n := range runs {
		if n <= runNumber {
			continue
		}
		run, err := h.store.LoadRun(featureID, n)
		if err != nil {
			return true
		}
		for _, rec := range run.SessionCosts {
			if strings.TrimSpace(rec.SessionID) == sessionID {
				return true
			}
		}
	}
	if sess := h.getSession(sessionID); sess != nil && sessionRunNumber(sess) > runNumber {
		return true
	}
	return false
}

func (h *apiHandler) writeTranscriptPage(w http.ResponseWriter, r *http.Request, sessionID string, messages []llm.SDKMessage, workDir string) {
	total := len(messages)
	offset := int(parseInt64Query(r, "offset", 0))
	limit := int(parseInt64Query(r, "limit", 100))
	if offset < 0 || limit <= 0 || limit > 500 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics(fmt.Sprintf("invalid transcript bounds for session %q", sessionID)))
		return
	}
	offset = min(offset, total)
	end := min(offset+limit, total)
	resp := TranscriptResponse{
		APIVersion: APIVersion,
		Cursor:     Cursor{Total: total, Start: offset, End: end},
		Messages:   transcriptDTOs(messages[offset:end], offset, workDir),
	}
	h.writeRevisionedResponse(w, r, resp, func(meta ResponseMeta) any { resp.Meta = meta; return resp })
}
