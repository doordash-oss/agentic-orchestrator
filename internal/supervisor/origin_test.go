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

var childOrigin = llm.EventOrigin{Kind: llm.EventOriginTask, TaskID: "task-1", ChildSessionID: "child-1"}

// childRequest is a control request raised by a sub-agent, carrying its
// origin on both the message and the request as the session layer does.
func childRequest(id, tool, input string) llm.SDKMessage {
	return llm.SDKMessage{Type: "control_request", Origin: childOrigin, ControlRequest: &llm.ControlRequestMessage{
		Type:      "control_request",
		RequestID: id,
		Request:   llm.ControlRequest{Subtype: "can_use_tool", ToolName: tool, Input: json.RawMessage(input)},
		Origin:    childOrigin,
	}}
}

// requestRecords returns the payloads of the transcript's permission and
// question records in order.
func requestRecords(t *testing.T, c *Coordinator) []RequestData {
	t.Helper()
	var out []RequestData
	for _, rec := range allRecords(t, c) {
		if rec.Kind != KindPermission && rec.Kind != KindQuestion {
			continue
		}
		var data RequestData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			t.Fatalf("decode request record: %v", err)
		}
		out = append(out, data)
	}
	return out
}

func TestCoordinator_SubagentPermissionSurfacesWithChildOrigin(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	sub, _ := c.Subscribe(0, false, "")
	defer c.Unsubscribe(sub)
	if _, err := c.Send(context.Background(), "delegate ls", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(childRequest("req-child", "Bash", `{"command":"ls"}`))

	sawRequest := false
	timeout := time.After(5 * time.Second)
	for done := false; !done; {
		var ev Event
		select {
		case ev = <-sub.Events():
		case <-timeout:
			t.Fatal("timed out waiting for the child request and waiting state")
		}
		switch {
		case ev.Kind == EventRequest && ev.Request.RequestID == "req-child":
			if ev.Request.Origin != childOrigin {
				t.Fatalf("request event origin = %+v", ev.Request.Origin)
			}
			sawRequest = true
		case ev.Kind == EventState && ev.State.Lifecycle == LifecycleWaitingPermission:
			if !sawRequest {
				t.Fatal("state change arrived before the request event")
			}
			if len(ev.State.PendingRequests) != 1 || ev.State.PendingRequests[0].Origin != childOrigin {
				t.Fatalf("pending = %+v", ev.State.PendingRequests)
			}
			done = true
		}
	}

	sess.answer(ports.ControlAnswer{RequestID: "req-child", ToolName: "Bash", Allowed: true})
	if st := c.State(); st.Lifecycle != LifecycleRunning || len(st.PendingRequests) != 0 {
		t.Fatalf("state after answer = %+v", st)
	}
	recs := requestRecords(t, c)
	if len(recs) != 2 {
		t.Fatalf("request records = %+v", recs)
	}
	for i, want := range []string{RequestPending, RequestAllowed} {
		if recs[i].Outcome != want || recs[i].Origin != RequestOriginChild || recs[i].ChildSessionID != "child-1" {
			t.Fatalf("record %d = %+v, want outcome %s from child-1", i, recs[i], want)
		}
	}
}

func TestCoordinator_SubagentQuestionWaitsForQuestionAndTurnEndClearsIt(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "delegate a question", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(childRequest("q-child", "AskUserQuestion", `{"questions":[]}`))
	st := c.State()
	if st.Lifecycle != LifecycleWaitingQuestion || len(st.PendingRequests) != 1 {
		t.Fatalf("state after child question = %+v", st)
	}
	sess.answer(ports.ControlAnswer{RequestID: "q-child", ToolName: "AskUserQuestion", Allowed: true, Answers: map[string]string{"Which?": "main"}})
	recs := requestRecords(t, c)
	if len(recs) != 2 || recs[1].Outcome != RequestAnswered || recs[1].Origin != RequestOriginChild || recs[1].ChildSessionID != "child-1" {
		t.Fatalf("question records = %+v", recs)
	}

	// A second child question left open is cleared when the turn ends.
	sess.emit(childRequest("q-child-2", "AskUserQuestion", `{"questions":[]}`))
	if got := c.State().Lifecycle; got != LifecycleWaitingQuestion {
		t.Fatalf("lifecycle = %s", got)
	}
	sess.emit(successResult())
	if st := c.State(); st.Lifecycle != LifecycleIdle || len(st.PendingRequests) != 0 {
		t.Fatalf("turn end did not clear the child request: %+v", st)
	}
}

func TestCoordinator_InterruptClearsSubagentRequest(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher, func(o *Options) { o.InterruptGrace = time.Minute })
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "delegate", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(childRequest("req-child", "Bash", `{"command":"ls"}`))
	if got := c.State().Lifecycle; got != LifecycleWaitingPermission {
		t.Fatalf("lifecycle = %s", got)
	}
	if _, st := c.Interrupt(); len(st.PendingRequests) != 0 {
		t.Fatalf("interrupt left pending = %+v", st.PendingRequests)
	}
}

func TestCoordinator_SubagentOutputOtherThanRequestsIsIgnored(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "delegate", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	before := len(allRecords(t, c))
	child := assistantText("msg-child", "sub-agent chatter")
	child.Origin = childOrigin
	sess.emit(child)
	sess.emit(llm.SDKMessage{Type: "system", Subtype: "task_progress", Origin: childOrigin, TaskProgress: &llm.TaskProgressMessage{TaskID: "task-1"}})
	childResult := successResult()
	childResult.Origin = childOrigin
	sess.emit(childResult)
	if got := len(allRecords(t, c)); got != before {
		t.Fatalf("sub-agent output committed %d records", got-before)
	}
	if got := c.State().Lifecycle; got != LifecycleRunning {
		t.Fatalf("a sub-agent result ended the conversation's turn: %s", got)
	}
}

func TestCoordinator_RootRequestRecordsRootOrigin(t *testing.T) {
	launcher := &fakeLauncher{}
	c := newTestCoordinator(t, t.TempDir(), launcher)
	chooseSettings(t, c)
	if _, err := c.Send(context.Background(), "run ls", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	sess := launcher.session(0)
	sess.emit(llm.SDKMessage{Type: "control_request", ControlRequest: &llm.ControlRequestMessage{
		RequestID: "req-root", Request: llm.ControlRequest{ToolName: "Bash"},
	}})
	sess.answer(ports.ControlAnswer{RequestID: "req-root", ToolName: "Bash"})
	recs := requestRecords(t, c)
	if len(recs) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	for _, rec := range recs {
		if rec.Origin != RequestOriginRoot || rec.ChildSessionID != "" {
			t.Fatalf("root record = %+v", rec)
		}
	}
	if recs[1].Outcome != RequestDenied {
		t.Fatalf("verdict = %+v", recs[1])
	}
}

func TestCoordinator_BootKeepsOriginOnInterruptedSubagentRequest(t *testing.T) {
	dir := t.TempDir()
	launcher := &fakeLauncher{}
	first := openUnmanaged(t, dir, launcher)
	t.Cleanup(func() { _ = first.Close() })
	chooseSettings(t, first)
	if _, err := first.Send(context.Background(), "delegate", "", "cm-1"); err != nil {
		t.Fatal(err)
	}
	launcher.session(0).emit(childRequest("req-child", "Bash", `{"command":"ls"}`))
	waitLifecycle(t, first, LifecycleWaitingPermission)

	second := newTestCoordinator(t, dir, &fakeLauncher{})
	recs := requestRecords(t, second)
	last := recs[len(recs)-1]
	if last.Outcome != RequestInterrupted || last.Origin != RequestOriginChild || last.ChildSessionID != "child-1" {
		t.Fatalf("interrupted resolution = %+v", last)
	}
}

func TestRequestOrigin(t *testing.T) {
	cases := []struct {
		in          llm.EventOrigin
		origin, sid string
	}{
		{llm.EventOrigin{}, RequestOriginRoot, ""},
		{llm.EventOrigin{Kind: llm.EventOriginRoot}, RequestOriginRoot, ""},
		{childOrigin, RequestOriginChild, "child-1"},
		{llm.EventOrigin{Kind: llm.EventOriginTask, TaskID: "agent-7"}, RequestOriginChild, "agent-7"},
	}
	for _, tc := range cases {
		origin, sid := RequestOrigin(tc.in)
		if origin != tc.origin || sid != tc.sid {
			t.Errorf("RequestOrigin(%+v) = %q, %q; want %q, %q", tc.in, origin, sid, tc.origin, tc.sid)
		}
	}
}
