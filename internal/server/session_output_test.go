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
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

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

func TestSessionOutputStreamBackfillsAndTerminatesForCompletedSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(logPath, []byte("only chunk\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			id:      "sess-1",
			logPath: logPath,
			status:  ports.SessionDone,
		}}},
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
	if !strings.Contains(got, "event: session.output") || !strings.Contains(got, `"data":"only chunk\n"`) {
		t.Fatalf("stream missing output chunk:\n%s", got)
	}
	if !strings.Contains(got, "event: session.output.done") {
		t.Fatalf("stream missing terminal event:\n%s", got)
	}
}
