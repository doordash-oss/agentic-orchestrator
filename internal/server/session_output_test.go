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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// raceSafeActiveSessionView overrides fakeSessionView.IsActive with an
// atomic flag so a test can flip session liveness from a different
// goroutine than the one driving handleSessionOutputStream's poll loop,
// without racing on fakeSessionView's plain status field.
type raceSafeActiveSessionView struct {
	*fakeSessionView
	active atomic.Bool
}

func (s *raceSafeActiveSessionView) IsActive() bool { return s.active.Load() }

func TestSessionOutputBackfillReadsFromByteOffset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(logPath, []byte("first\nsecond\nthird\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			id:      "sess-1",
			logPath: logPath,
			status:  ports.SessionRunning,
		}}},
		DisableHostValidation: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/output?from=6&limit=6", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 body=%s", resp.StatusCode, w.Body.String())
	}
	var body SessionOutputResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Offset != 6 || body.NextOffset != 12 || body.Data != "second" || !body.Truncated {
		t.Fatalf("SessionOutputResponse = %+v, want offset 6 next 12 data second truncated", body)
	}
}

func TestSessionOutputStreamEmitsIndexedTranscriptRecordsAndTerminates(t *testing.T) {
	t.Parallel()

	sess := &fakeSessionView{id: "sess-1", status: ports.SessionDone}
	sess.log = session.NewMessageLog()
	sess.log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Type:    "assistant",
		Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "hi"}}},
	}})
	handler := NewHandler(HandlerOptions{
		Sessions:              fakeSessionManager{views: []ports.SessionView{sess}},
		DisableHostValidation: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/output/stream?from=0", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 body=%s", resp.StatusCode, w.Body.String())
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"index":0`) || !strings.Contains(got, `"text":"hi"`) {
		t.Fatalf("stream missing indexed transcript record:\n%s", got)
	}
	if !strings.Contains(got, "event: session.output.done") {
		t.Fatalf("stream missing terminal event:\n%s", got)
	}
}

// TestSessionOutputStreamReincludesMutatingTailRow guards the poll loop's
// "re-include the previously-sent tail index every tick" behavior: UpdateLast
// can mutate the last message in place without growing Len(), so a client
// resuming from the row it already has must still receive that row again
// once it changes, not be starved of the update.
func TestSessionOutputStreamReincludesMutatingTailRow(t *testing.T) {
	t.Parallel()

	log := session.NewMessageLog()
	log.Append(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Type:    "assistant",
		Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "partial"}}},
	}})
	sess := &raceSafeActiveSessionView{fakeSessionView: &fakeSessionView{id: "sess-1"}}
	sess.log = log
	sess.active.Store(true)
	handler := NewHandler(HandlerOptions{
		Sessions:              fakeSessionManager{views: []ports.SessionView{sess}},
		DisableHostValidation: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/output/stream?from=0", nil)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(w, req)
		close(done)
	}()

	// Give the handler time to emit the first (partial) row, then mutate the
	// tail in place and mark the session finished so the handler terminates.
	time.Sleep(300 * time.Millisecond)
	log.UpdateLast(llm.SDKMessage{Type: "assistant", Assistant: &llm.AssistantMessage{
		Type:    "assistant",
		Message: llm.ConversationMsg{Role: "assistant", Content: []llm.ContentBlock{{Type: "text", Text: "final"}}},
	}})
	sess.active.Store(false)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not terminate after session finished")
	}

	got := w.Body.String()
	if !strings.Contains(got, `"text":"partial"`) {
		t.Fatalf("stream missing initial partial row:\n%s", got)
	}
	if !strings.Contains(got, `"text":"final"`) {
		t.Fatalf("stream missing re-sent mutated tail row (index re-included on tick):\n%s", got)
	}
	if !strings.Contains(got, "event: session.output.done") {
		t.Fatalf("stream missing terminal event:\n%s", got)
	}
}

func TestSessionOutputStreamErrorIsRetriableForActiveSession(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			id:     "sess-1",
			status: ports.SessionRunning,
		}}},
		DisableHostValidation: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/sess-1/output/stream?from=-1", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	resp := w.Result()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 body=%s", resp.StatusCode, w.Body.String())
	}
}
