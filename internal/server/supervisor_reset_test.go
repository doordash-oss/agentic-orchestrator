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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

func TestSupervisorRoutes_ResetShapes(t *testing.T) {
	svc := &recordingSupervisor{state: supervisor.State{ConversationID: "conv-1", Lifecycle: supervisor.LifecycleIdle, HeadSeq: 4, Settings: supervisor.Settings{Harness: "claude", Model: "haiku"}}}
	h := newSupervisorTestHandler(svc, nil)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, supervisorRequest(http.MethodPost, apiPathSupervisorReset, `{}`))
	if w.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"api_version", "result", "previous_conversation_id", "state"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("reset response lacks %q: %s", key, w.Body.String())
		}
	}
	var reset SupervisorResetResponse
	_ = json.Unmarshal(w.Body.Bytes(), &reset)
	if reset.Result != SupervisorResetDone || reset.PreviousConversationID != "conv-1" || reset.State.ConversationID != "conv-1-next" ||
		reset.State.Lifecycle != SupervisorLifecycleStopped || reset.State.LastTurnOutcome != SupervisorTurnOutcomeNone || reset.State.Settings.Model != "haiku" {
		t.Fatalf("reset = %+v", reset)
	}

	var noop SupervisorResetResponse
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorReset, `{}`), http.StatusOK, &noop)
	if noop.Result != SupervisorResetNoop || noop.PreviousConversationID != "conv-1-next" || noop.State.ConversationID != "conv-1-next" {
		t.Fatalf("noop = %+v", noop)
	}

	serveSupervisor(t, h, supervisorRequest(http.MethodGet, apiPathSupervisorReset, ""), http.StatusMethodNotAllowed, nil)
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorReset, `[]`), http.StatusBadRequest, nil)
	untrusted := supervisorRequest(http.MethodPost, apiPathSupervisorReset, `{}`)
	untrusted.Header.Del("X-Agentico-Client")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, untrusted)
	if w.Code < 400 {
		t.Fatalf("untrusted reset = %d", w.Code)
	}
	if svc.resets != 2 {
		t.Fatalf("resets reaching the coordinator = %d, want 2", svc.resets)
	}

	svc.err = errors.New("disk full")
	serveSupervisor(t, h, supervisorRequest(http.MethodPost, apiPathSupervisorReset, `{}`), http.StatusInternalServerError, nil)
}

type resetTestCatalog struct{}

func (resetTestCatalog) ValidateSettings(supervisor.Settings) error { return nil }
func (resetTestCatalog) DefaultModel(string) (string, error)        { return "haiku", nil }

type supervisorSSE struct {
	t      *testing.T
	resp   *http.Response
	events chan SupervisorStreamEvent
}

func openSupervisorSSE(t *testing.T, srv *httptest.Server, query string) *supervisorSSE {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+apiPathSupervisorEvents+query, nil)
	req.Header.Set("Authorization", "Bearer "+testAuthToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", resp.StatusCode)
	}
	s := &supervisorSSE{t: t, resp: resp, events: make(chan SupervisorStreamEvent, 64)}
	go func() {
		defer close(s.events)
		r := bufio.NewReader(resp.Body)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if data, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "data: "); ok {
				var ev SupervisorStreamEvent
				if json.Unmarshal([]byte(data), &ev) == nil {
					s.events <- ev
				}
			}
		}
	}()
	return s
}

// until returns the first event matching match, skipping heartbeats and
// anything else.
func (s *supervisorSSE) until(what string, match func(SupervisorStreamEvent) bool) SupervisorStreamEvent {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-s.events:
			if !open {
				s.t.Fatalf("stream closed waiting for %s", what)
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			s.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestSupervisorEvents_ResetReSnapshotsUnderNewConversation(t *testing.T) {
	coord, err := supervisor.New(supervisor.Options{StateDir: t.TempDir(), Catalog: resetTestCatalog{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coord.Close() })
	if _, err := coord.UpdateSettings(supervisor.Settings{Harness: "claude", Model: "haiku"}); err != nil {
		t.Fatal(err)
	}
	model := func(m string) *string { return &m }
	// A committed change on a stopped conversation leaves settings markers,
	// so the transcript is non-empty and the reset is not a noop.
	if _, err := coord.ChangeSettings(supervisor.SettingsChange{Model: model("sonnet"), RequestID: "change-1"}); err != nil {
		t.Fatal(err)
	}
	old := coord.State()
	if old.HeadSeq == 0 {
		t.Fatal("setup left the transcript empty")
	}
	srv := httptest.NewServer(newSupervisorTestHandler(coord, nil))
	t.Cleanup(srv.Close)

	stream := openSupervisorSSE(t, srv, "?heartbeat_ms=50")
	first := stream.until("opening state", func(ev SupervisorStreamEvent) bool { return ev.Kind == SupervisorEventState })
	if first.ConversationID != old.ConversationID || first.StreamEpoch != old.StreamEpoch {
		t.Fatalf("opening state = %+v", first)
	}

	resetReq := supervisorRequest(http.MethodPost, srv.URL+apiPathSupervisorReset, `{}`)
	resetReq.RequestURI = ""
	resp, err := srv.Client().Do(resetReq)
	if err != nil {
		t.Fatal(err)
	}
	var reset SupervisorResetResponse
	if err := json.NewDecoder(resp.Body).Decode(&reset); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("reset = %d %v", resp.StatusCode, err)
	}
	_ = resp.Body.Close()
	next := reset.State
	if reset.Result != SupervisorResetDone || reset.PreviousConversationID != old.ConversationID || next.ConversationID == old.ConversationID || next.HeadSeq != 0 {
		t.Fatalf("reset = %+v", reset)
	}

	ev := stream.until("stream.reset", func(ev SupervisorStreamEvent) bool { return ev.Kind == SupervisorEventStreamReset })
	if !ev.SnapshotRequired || ev.ConversationID != next.ConversationID || ev.StreamEpoch != next.StreamEpoch || ev.Seq != 0 || ev.Generation != 0 {
		t.Fatalf("stream.reset = %+v, want conversation %s epoch %s", ev, next.ConversationID, next.StreamEpoch)
	}
	// The stream continues live: the new conversation's first record
	// arrives under its id and epoch.
	if _, err := coord.ChangeSettings(supervisor.SettingsChange{Model: model("opus"), RequestID: "change-2"}); err != nil {
		t.Fatal(err)
	}
	rec := stream.until("first record of the new conversation", func(ev SupervisorStreamEvent) bool { return ev.Kind == SupervisorEventRecord })
	if rec.ConversationID != next.ConversationID || rec.StreamEpoch != next.StreamEpoch || rec.Seq != 1 || rec.Record == nil || rec.Record.Seq != 1 {
		t.Fatalf("first live record = %+v", rec)
	}

	// A client resuming with the old epoch is told to re-snapshot under the
	// new conversation.
	resumed := openSupervisorSSE(t, srv, fmt.Sprintf("?after=%d&epoch=%s&heartbeat_ms=50", old.HeadSeq, old.StreamEpoch))
	ev = resumed.until("stream.reset on resume", func(ev SupervisorStreamEvent) bool { return ev.Kind == SupervisorEventStreamReset })
	if !ev.SnapshotRequired || ev.ConversationID != next.ConversationID || ev.StreamEpoch != next.StreamEpoch {
		t.Fatalf("resume reset = %+v", ev)
	}
}
