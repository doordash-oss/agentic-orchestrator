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
