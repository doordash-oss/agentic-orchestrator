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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func assistantText(text string) llm.SDKMessage {
	return llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{Message: llm.ConversationMsg{
		Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: text}},
	}}}
}

func writeTranscriptFile(t *testing.T, path string, texts ...string) {
	t.Helper()
	var b strings.Builder
	for i, text := range texts {
		msg := assistantText(text)
		line, err := json.Marshal(map[string]any{"index": i, "message": map[string]any{"type": msg.Type, "assistant": msg.Assistant}})
		if err != nil {
			t.Fatalf("marshal transcript row: %v", err)
		}
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func transcriptFileName(id, suffix string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("session-transcript-%x%s.jsonl", sum[:8], suffix)
}

func runTranscriptTexts(t *testing.T, handler http.Handler, featureID string, run int, sessionID string, wantStatus int) []string {
	t.Helper()
	path := "/api/v1/features/" + featureID + "/runs/" + strconv.Itoa(run) + "/sessions/" + sessionID + "/transcript"
	body := requestJSONMap(t, handler, path, wantStatus)
	if wantStatus != http.StatusOK {
		return nil
	}
	texts := []string{}
	for _, raw := range body["messages"].([]any) {
		texts = append(texts, fmt.Sprint(raw.(map[string]any)["text"]))
	}
	return texts
}

func TestRunSessionTranscriptResolvesEachRunsOwnSegments(t *testing.T) {
	t.Parallel()
	store, f := seedHistoryFeature(t,
		sealedRun(t, 2, withSessionCosts(
			feature.SessionCostRecord{SessionID: "f-impl", PhaseKey: "phase-1-impl", CostUSD: 1},
			feature.SessionCostRecord{SessionID: "f-plan", PhaseKey: "plan", CostUSD: 1},
			feature.SessionCostRecord{SessionID: "f-review", PhaseKey: "review", CostUSD: 1},
		)),
		sealedRun(t, 3, withSessionCosts(
			feature.SessionCostRecord{SessionID: "f-review", PhaseKey: "review", CostUSD: 1},
		)),
	)
	dir := filepath.Join(store.BaseDir, f.ID)
	writeTranscriptFile(t, filepath.Join(dir, transcriptFileName("f-impl", "-run2-0000000000000000001")), "before pause")
	writeTranscriptFile(t, filepath.Join(dir, transcriptFileName("f-impl", "-run2-0000000000000000002")), "after resume")
	writeTranscriptFile(t, filepath.Join(dir, transcriptFileName("f-impl", "")), "legacy shadow")
	writeTranscriptFile(t, filepath.Join(dir, transcriptFileName("f-plan", "")), "legacy plan")
	writeTranscriptFile(t, filepath.Join(dir, transcriptFileName("f-review", "")), "legacy review")

	opts := baseReadHandlerOptions(store)
	opts.Sessions = fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
		MockSessionView: &mocks.MockSessionView{IDVal: "f-impl", FeatureIDVal: f.ID, PhaseVal: feature.PhaseImplement, KindVal: ports.KindPhase, StatusVal: ports.SessionRunning},
		runNumber:       3,
		messages:        []llm.SDKMessage{assistantText("live run 3")},
	}}}
	handler := NewHandler(opts)

	tests := []struct {
		name       string
		run        int
		sessionID  string
		wantStatus int
		want       []string
	}{
		{name: "sealed run joins its segments", run: 2, sessionID: "f-impl", wantStatus: http.StatusOK, want: []string{"before pause", sessionSegmentBoundaryText, "after resume"}},
		{name: "reusing run reads its live session", run: 3, sessionID: "f-impl", wantStatus: http.StatusOK, want: []string{"live run 3"}},
		{name: "legacy file serves its latest run", run: 2, sessionID: "f-plan", wantStatus: http.StatusOK, want: []string{"legacy plan"}},
		{name: "legacy file is not shown for an earlier run", run: 2, sessionID: "f-review", wantStatus: http.StatusNotFound},
		{name: "unknown session", run: 2, sessionID: "f-missing", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runTranscriptTexts(t, handler, f.ID, tt.run, tt.sessionID, tt.wantStatus)
			if tt.wantStatus == http.StatusOK && strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("transcript = %q, want %q", got, tt.want)
			}
		})
	}
}
