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

func TestSubscribeSessionOutputDeliversIndexedRecords(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		frames := []struct {
			event string
			data  string
		}{
			{"session.output", `{"api_version":"v1","session_id":"sess-1","index":0,"message":{"index":0,"role":"assistant","type":"text","text":"hi"}}`},
			{"session.output.done", `{"api_version":"v1","session_id":"sess-1","index":0,"done":true}`},
		}
		for _, f := range frames {
			_, _ = w.Write([]byte("event: " + f.event + "\ndata: " + f.data + "\n\n"))
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL, Token: testAuthToken})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	records, errs := c.SubscribeSessionOutput(context.Background(), fixtureSessionID, SessionOutputStreamOptions{})

	select {
	case rec, ok := <-records:
		if !ok || rec.Index != 0 || rec.Message.Text != "hi" {
			t.Fatalf("record = (%+v, %v), want index 0 text hi", rec, ok)
		}
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for record")
	}

	// The stream must close cleanly after session.output.done, with no error.
	select {
	case _, ok := <-records:
		if ok {
			t.Fatal("expected records to close after session.output.done")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for records to close")
	}
}

// TestSubscribeSessionOutputResumesFromAfterIndex confirms AfterIndex is sent
// as the "from" query param — the resume position is now a transcript row
// index, not a byte offset.
func TestSubscribeSessionOutputResumesFromAfterIndex(t *testing.T) {
	t.Parallel()

	var gotFrom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFrom = r.URL.Query().Get("from")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("event: session.output.done\ndata: " + `{"api_version":"v1","session_id":"sess-1","index":4,"done":true}` + "\n\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	c, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	records, errs := c.SubscribeSessionOutput(context.Background(), fixtureSessionID, SessionOutputStreamOptions{AfterIndex: 5})

	select {
	case _, ok := <-records:
		if ok {
			t.Fatal("expected no records before the done event")
		}
	case err, ok := <-errs:
		if ok && err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for records to close")
	}
	if gotFrom != "5" {
		t.Fatalf("from query param = %q, want %q", gotFrom, "5")
	}
}

func TestSubscribeSessionOutputRejectsUnknownEvent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: session.output.unknown\ndata: " + `{"api_version":"v1","session_id":"sess-1","index":0}` + "\n\n"))
	}))
	t.Cleanup(srv.Close)

	c, err := NewClient(ClientOptions{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, errs := c.SubscribeSessionOutput(context.Background(), fixtureSessionID, SessionOutputStreamOptions{})
	if err := <-errs; err == nil {
		t.Fatal("SubscribeSessionOutput() error = nil, want protocol error for unknown event")
	}
}
