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

package supervisor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// TestLifecycle_ActivityPredicates pins the two supervisor facts work
// admission reads: active work in the four busy states, and blocking an
// unattended install only while starting or running.
func TestLifecycle_ActivityPredicates(t *testing.T) {
	for _, tc := range []struct {
		lifecycle     Lifecycle
		active, block bool
	}{
		{LifecycleStopped, false, false},
		{LifecycleStarting, true, true},
		{LifecycleIdle, false, false},
		{LifecycleRunning, true, true},
		{LifecycleWaitingPermission, true, false},
		{LifecycleWaitingQuestion, true, false},
		{LifecycleFailed, false, false},
	} {
		if got := tc.lifecycle.Active(); got != tc.active {
			t.Errorf("%s.Active() = %v, want %v", tc.lifecycle, got, tc.active)
		}
		if got := tc.lifecycle.BlocksIdleInstall(); got != tc.block {
			t.Errorf("%s.BlocksIdleInstall() = %v, want %v", tc.lifecycle, got, tc.block)
		}
		if got := tc.lifecycle.Waiting(); got != (tc.active && !tc.block) {
			t.Errorf("%s.Waiting() = %v", tc.lifecycle, got)
		}
	}
}

// pendRequest surfaces one request on the current session and waits for the
// matching waiting lifecycle.
func pendRequest(t *testing.T, c *Coordinator, sess *fakeSession, id, tool string) {
	t.Helper()
	sess.emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
		RequestID: id,
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: tool, Input: json.RawMessage(`{}`)},
	}})
	want := LifecycleWaitingPermission
	if tool == askUserQuestionTool {
		want = LifecycleWaitingQuestion
	}
	waitLifecycle(t, c, want)
}

// resolutions returns every resolved-stage request record, in order.
func resolutions(t *testing.T, recs []Record) []RequestData {
	t.Helper()
	var out []RequestData
	for _, rec := range recs {
		if rec.Kind != KindPermission && rec.Kind != KindQuestion {
			continue
		}
		var data RequestData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			t.Fatalf("decode request %d: %v", rec.Seq, err)
		}
		if data.Stage == StageResolved {
			out = append(out, data)
		}
	}
	return out
}

// eventsUntilStopped collects a subscription's events through the first
// stopped state or its close.
func eventsUntilStopped(t *testing.T, sub *Subscription) []Event {
	t.Helper()
	var out []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, open := <-sub.Events():
			if !open {
				return out
			}
			out = append(out, ev)
			if ev.Kind == EventState && ev.State.Lifecycle == LifecycleStopped {
				return out
			}
		case <-timeout:
			t.Fatal("no stopped state was published")
		}
	}
}

// assertResolvedBeforeStopped checks the stream carried one interrupted
// resolution per wanted request, all ahead of the stopped state.
func assertResolvedBeforeStopped(t *testing.T, events []Event, want ...string) {
	t.Helper()
	var got []string
	stopped := false
	for _, ev := range events {
		switch {
		case ev.Kind == EventState && ev.State.Lifecycle == LifecycleStopped:
			stopped = true
		case ev.Kind == EventRecord && (ev.Record.Kind == KindPermission || ev.Record.Kind == KindQuestion):
			var data RequestData
			_ = json.Unmarshal(ev.Record.Data, &data)
			if data.Stage != StageResolved {
				continue
			}
			if stopped {
				t.Fatalf("resolution of %s published after stopped", data.RequestID)
			}
			if data.Outcome != RequestInterrupted || ev.Record.TurnID != "g1.t1" {
				t.Fatalf("resolution = %+v turn %q, want interrupted on g1.t1", data, ev.Record.TurnID)
			}
			got = append(got, data.RequestID)
		}
	}
	if !stopped {
		t.Fatal("stopped state never published")
	}
	if len(got) != len(want) {
		t.Fatalf("resolved %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolved %v, want %v", got, want)
		}
	}
}

// TestCoordinator_StopWhilePendingResolvesOpenRequests drives End and Close
// with a permission or a question pending: each appends one interrupted
// resolution per open request before stopped is published, and a later
// boot neither resolves them a second time nor drops the interrupted
// marker of the cut turn.
func TestCoordinator_StopWhilePendingResolvesOpenRequests(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		stop       func(*Coordinator)
	}{
		{"end permission", "Bash", func(c *Coordinator) { c.End() }},
		{"end question", askUserQuestionTool, func(c *Coordinator) { c.End() }},
		{"close permission", "Bash", func(c *Coordinator) { _ = c.Close() }},
		{"close question", askUserQuestionTool, func(c *Coordinator) { _ = c.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			launcher := &fakeLauncher{}
			c := openUnmanaged(t, dir, launcher)
			t.Cleanup(func() { _ = c.Close() })
			chooseSettings(t, c)
			if _, err := c.Send(context.Background(), "run it", "", "cm-1"); err != nil {
				t.Fatal(err)
			}
			sess := launcher.session(0)
			pendRequest(t, c, sess, "req-1", tc.tool)
			sub, err := c.Subscribe(0, false, "")
			if err != nil {
				t.Fatal(err)
			}
			tc.stop(c)
			assertResolvedBeforeStopped(t, eventsUntilStopped(t, sub), "req-1")
			if st := c.State(); st.Lifecycle != LifecycleStopped || len(st.PendingRequests) != 0 {
				t.Fatalf("state after stop = %+v", st)
			}
			_ = c.Close()

			second := newTestCoordinator(t, dir, &fakeLauncher{})
			recs := allRecords(t, second)
			if got := resolutions(t, recs); len(got) != 1 || got[0].RequestID != "req-1" || got[0].Outcome != RequestInterrupted {
				t.Fatalf("resolutions after boot = %+v, want exactly one interrupted", got)
			}
			if got := markersOf(t, recs, MarkerInterrupted); len(got) != 1 || got[0].TurnID != "g1.t1" {
				t.Fatalf("interrupted markers after boot = %+v", got)
			}
			if st := second.State(); st.Lifecycle != LifecycleStopped || st.LastTurnOutcome != OutcomeInterrupted {
				t.Fatalf("state after boot = %+v", st)
			}
		})
	}
}

// TestCoordinator_StopResolvesEveryOpenRequestOfTheCutTurn proves stop-time
// resolution covers each request still open, and skips one the user
// already answered.
func TestCoordinator_StopResolvesEveryOpenRequestOfTheCutTurn(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "run it", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	pendRequest(t, c, sess, "req-1", "Bash")
	pendRequest(t, c, sess, "req-2", "Edit")
	pendRequest(t, c, sess, "req-3", askUserQuestionTool)
	sess.answer(ports.ControlAnswer{RequestID: "req-2", ToolName: "Edit", Allowed: true})
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	c.End()
	assertResolvedBeforeStopped(t, eventsUntilStopped(t, sub), "req-1", "req-3")
	got := resolutions(t, allRecords(t, c))
	if len(got) != 3 || got[0].RequestID != "req-2" || got[0].Outcome != RequestAllowed {
		t.Fatalf("resolutions = %+v, want req-2 allowed then two interrupted", got)
	}
}

// TestCoordinator_ResetAfterPendingResolvesOnce proves a reset of a pending
// turn resolves each open request exactly once.
func TestCoordinator_ResetAfterPendingResolvesOnce(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, dir, launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "run it", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	pendRequest(t, c, launcher.session(0), "req-1", "Bash")
	previous := c.State().ConversationID
	if _, err := c.Reset(); err != nil {
		t.Fatal(err)
	}
	old := readConversationRecords(t, dir, previous)
	if got := resolutions(t, old); len(got) != 1 || got[0].Outcome != RequestInterrupted {
		t.Fatalf("resolutions after reset = %+v, want exactly one interrupted", got)
	}
	if got := markersOf(t, old, MarkerInterrupted); len(got) != 1 {
		t.Fatalf("interrupted markers after reset = %+v", got)
	}
}

// TestCoordinator_GraceTerminationResolvesOpenRequests proves the interrupt
// grace's termination resolves a request the interrupt cut, even though
// the interrupt already cleared the pending list.
func TestCoordinator_GraceTerminationResolvesOpenRequests(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.InterruptGrace = 50 * time.Millisecond })
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "stubborn", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	pendRequest(t, c, launcher.session(0), "req-1", "Bash")
	sub, err := c.Subscribe(0, false, "")
	if err != nil {
		t.Fatal(err)
	}
	c.Interrupt()
	assertResolvedBeforeStopped(t, eventsUntilStopped(t, sub), "req-1")
}

// TestCoordinator_EndWhileIdleAppendsNothing proves an End with no turn in
// flight commits no record, even when an earlier turn ended with a
// request the harness left unanswered.
func TestCoordinator_EndWhileIdleAppendsNothing(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "run it", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	pendRequest(t, c, sess, "req-1", "Bash")
	sess.emit(successResult())
	waitLifecycle(t, c, LifecycleIdle)
	head := c.State().HeadSeq
	if result, st := c.End(); result != ActionEnded || st.HeadSeq != head {
		t.Fatalf("End while idle = %s head %d, want ended with head %d", result, st.HeadSeq, head)
	}
}
