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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
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

func TestSessionOutputStreamEmitsIndexedTranscriptRecordsAndTerminates(t *testing.T) {
	t.Parallel()

	sess := &fakeSessionView{
		MockSessionView: &mocks.MockSessionView{
			IDVal:     fixtureSessionID,
			StatusVal: ports.SessionDone,
		},
	}
	sess.log = session.NewMessageLog()
	sess.log.Append(llm.SDKMessage{Type: roleAssistant, Assistant: &llm.AssistantMessage{
		Type:    roleAssistant,
		Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "hi"}}},
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

// TestSessionOutputStreamReincludesMutatingTailRow: a final assistant message
// can replace the last streaming partial in place without growing Len(), so
// the changed tail row must be re-sent.
func TestSessionOutputStreamReincludesMutatingTailRow(t *testing.T) {
	t.Parallel()

	log := session.NewMessageLog()
	log.Append(llm.SDKMessage{Type: roleAssistant, Subtype: "partial", Assistant: &llm.AssistantMessage{
		Type:    roleAssistant,
		Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "partial"}}},
	}})
	sess := &raceSafeActiveSessionView{fakeSessionView: &fakeSessionView{
		MockSessionView: &mocks.MockSessionView{
			IDVal: fixtureSessionID,
		},
	}}
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
	// tail in place (the production path: a final assistant message replaces
	// the in-flight streaming partial) and mark the session finished so the
	// handler terminates.
	time.Sleep(300 * time.Millisecond)
	log.UpdateLastAssistantPartial(llm.SDKMessage{Type: roleAssistant, Assistant: &llm.AssistantMessage{
		Type:    roleAssistant,
		Message: llm.ConversationMsg{Role: roleAssistant, Content: []llm.ContentBlock{{Type: blockTypeText, Text: "final"}}},
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

func newActiveStreamSession(log *session.MessageLog) *raceSafeActiveSessionView {
	sess := &raceSafeActiveSessionView{fakeSessionView: &fakeSessionView{
		MockSessionView: &mocks.MockSessionView{IDVal: fixtureSessionID},
	}}
	sess.log = log
	sess.active.Store(true)
	return sess
}

// runOutputStream serves path against sess, calls during while the stream is
// live, then ends the session and returns the full body.
func runOutputStream(t *testing.T, sess *raceSafeActiveSessionView, path string, during func()) string {
	t.Helper()
	handler := NewHandler(HandlerOptions{
		Sessions:              fakeSessionManager{views: []ports.SessionView{sess}},
		DisableHostValidation: true,
	})
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		close(done)
	}()
	during()
	sess.active.Store(false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not terminate after session finished")
	}
	return w.Body.String()
}

func outputFrameCount(body string, index int) int {
	return strings.Count(body, fmt.Sprintf("id: %d\nevent: session.output\n", index))
}

func TestSessionOutputStreamSendsIdleRowsOnce(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from int
		want map[int]int
	}{
		{name: "from start", from: 0, want: map[int]int{0: 1, 1: 1, 2: 1}},
		{name: "resume refreshes last seen row", from: 2, want: map[int]int{0: 0, 1: 1, 2: 1}},
		{name: "caught up", from: 3, want: map[int]int{0: 0, 1: 0, 2: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := session.NewMessageLog()
			for _, text := range []string{"a", "b", "c"} {
				log.Append(assistantText(text))
			}
			sess := newActiveStreamSession(log)
			body := runOutputStream(t, sess, fmt.Sprintf("/api/v1/sessions/sess-1/output/stream?from=%d", tc.from), func() {
				time.Sleep(time.Second)
			})
			for index, want := range tc.want {
				if got := outputFrameCount(body, index); got != want {
					t.Errorf("row %d sent %d times; want %d\n%s", index, got, want, body)
				}
			}
			if !strings.Contains(body, "event: session.output.done") {
				t.Fatalf("stream missing terminal event:\n%s", body)
			}
		})
	}
}

func TestSessionOutputStreamResendsTailOncePerChange(t *testing.T) {
	t.Parallel()

	log := session.NewMessageLog()
	partial := func(text string) llm.SDKMessage {
		msg := assistantText(text)
		msg.Subtype = "partial"
		return msg
	}
	log.Append(partial("one"))
	sess := newActiveStreamSession(log)
	body := runOutputStream(t, sess, "/api/v1/sessions/sess-1/output/stream?from=0", func() {
		time.Sleep(600 * time.Millisecond)
		log.UpdateLastAssistantPartial(partial("two"))
		time.Sleep(600 * time.Millisecond)
		log.UpdateLastAssistantPartial(assistantText("three"))
		time.Sleep(600 * time.Millisecond)
		log.Append(assistantText("four"))
		time.Sleep(600 * time.Millisecond)
	})
	if got := outputFrameCount(body, 0); got != 3 {
		t.Errorf("row 0 sent %d times; want 3 (initial + two changes)\n%s", got, body)
	}
	if got := outputFrameCount(body, 1); got != 1 {
		t.Errorf("row 1 sent %d times; want 1\n%s", got, body)
	}
	for _, text := range []string{"one", "two", "three", "four"} {
		if got := strings.Count(body, fmt.Sprintf(`"text":%q`, text)); got != 1 {
			t.Errorf("text %q sent %d times; want 1\n%s", text, got, body)
		}
	}
}

func TestSessionOutputStreamSendsKeepaliveWhenIdle(t *testing.T) {
	t.Parallel()

	log := session.NewMessageLog()
	log.Append(assistantText("a"))
	sess := newActiveStreamSession(log)
	body := runOutputStream(t, sess, "/api/v1/sessions/sess-1/output/stream?from=0&heartbeat_ms=100", func() {
		time.Sleep(700 * time.Millisecond)
	})
	if !strings.Contains(body, ": keepalive\n\n") {
		t.Fatalf("idle stream missing keepalive comment:\n%s", body)
	}
	if got := outputFrameCount(body, 0); got != 1 {
		t.Errorf("row 0 sent %d times; want 1\n%s", got, body)
	}
}

func TestSessionOutputStreamErrorIsRetriableForActiveSession(t *testing.T) {
	t.Parallel()

	handler := NewHandler(HandlerOptions{
		Sessions: fakeSessionManager{views: []ports.SessionView{&fakeSessionView{
			MockSessionView: &mocks.MockSessionView{
				IDVal:     fixtureSessionID,
				StatusVal: ports.SessionRunning,
			},
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
