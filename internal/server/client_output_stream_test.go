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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSubscribeSessionOutputSplitsLinesAcrossFrames(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		frames := []string{
			`{"api_version":"v1","session_id":"sess-1","offset":0,"next_offset":5,"data":"ab\nc"}`,
			`{"api_version":"v1","session_id":"sess-1","offset":5,"next_offset":9,"data":"de\n"}`,
			`{"api_version":"v1","session_id":"sess-1","offset":9,"next_offset":9,"data":"","done":true}`,
		}
		for _, f := range frames {
			_, _ = w.Write([]byte("event: session.output\ndata: " + f + "\n\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	lines, errs := c.SubscribeSessionOutput(context.Background(), "sess-1", SessionOutputStreamOptions{})

	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("lines closed early, got %v", got)
			}
			got = append(got, line.Text)
		case err, ok := <-errs:
			if ok {
				t.Fatalf("unexpected error: %v", err)
			}
		case <-timeout:
			t.Fatalf("timed out, got %v", got)
		}
	}
	want := []string{"ab", "cde"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("lines = %v, want %v", got, want)
	}
}

// TestSubscribeSessionOutputReadsPastFirstChunkOfFinishedSession guards
// against terminating on resp.Done instead of the "session.output.done"
// event name — the server sets Done true on every chunk of a finished
// session, not just the last one (session_model.go passes
// done=!sess.IsActive() into every call for that session).
func TestSubscribeSessionOutputReadsPastFirstChunkOfFinishedSession(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// First chunk: Done=true (session already finished) but Truncated=true
		// (more data remains) — event name is still "session.output", not
		// "session.output.done".
		_, _ = w.Write([]byte("event: session.output\ndata: " + `{"api_version":"v1","session_id":"sess-1","offset":0,"next_offset":3,"data":"one\n","truncated":true,"done":true}` + "\n\n"))
		flusher.Flush()
		// Second, terminal chunk.
		_, _ = w.Write([]byte("event: session.output.done\ndata: " + `{"api_version":"v1","session_id":"sess-1","offset":4,"next_offset":8,"data":"two\n","truncated":false,"done":true}` + "\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	lines, errs := c.SubscribeSessionOutput(context.Background(), "sess-1", SessionOutputStreamOptions{})

	var got []string
	timeout := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("lines closed early, got %v", got)
			}
			got = append(got, line.Text)
		case err, ok := <-errs:
			if ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		case <-timeout:
			t.Fatalf("timed out, got %v", got)
		}
	}
	want := []string{"one", "two"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("lines = %v, want %v (second chunk must not be dropped)", got, want)
	}
}

// TestSubscribeSessionOutputSurfacesStreamError confirms a
// session.output.error frame reaches the caller on the error channel,
// fulfilling SubscribeSessionOutput's documented reconnect-on-error contract.
func TestSubscribeSessionOutputSurfacesStreamError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("event: session.output.error\ndata: " + `{"api_version":"v1","session_id":"sess-1","offset":0,"next_offset":0,"done":false}` + "\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	_, errs := c.SubscribeSessionOutput(context.Background(), "sess-1", SessionOutputStreamOptions{})

	select {
	case err, ok := <-errs:
		if !ok || err == nil {
			t.Fatalf("errs = (%v, %v), want a non-nil error", err, ok)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the stream error")
	}
}
