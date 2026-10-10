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

package e2e

import (
	"net/http"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// requestEventBefore reports whether a `request` event for the tool arrived
// among evs, and returns it.
func requestEventBefore(evs []sseEvent, tool string) *server.ControlRequest {
	for _, ev := range evs {
		if ev.kind == string(server.SupervisorEventRequest) && ev.data.Request != nil && ev.data.Request.ToolName == tool {
			return ev.data.Request
		}
	}
	return nil
}

// requestRecordsFor returns the transcript's request records for one
// request id, in order.
func requestRecordsFor(items []server.SupervisorRecord, requestID string) []server.SupervisorRecord {
	var out []server.SupervisorRecord
	for _, rec := range items {
		if rec.Request != nil && rec.Request.RequestID == requestID {
			out = append(out, rec)
		}
	}
	return out
}

// assertChildRecords checks the requested and resolved records of a child
// request: both tagged with the child origin and the sub-agent's id, the
// verdict carrying wantOutcome.
func assertChildRecords(t *testing.T, items []server.SupervisorRecord, requestID string, kind server.SupervisorRecordKind, wantOutcome server.SupervisorRequestRecordOutcome) {
	t.Helper()
	recs := requestRecordsFor(items, requestID)
	if len(recs) != 2 {
		t.Fatalf("records for %s = %d, want requested and resolved", requestID, len(recs))
	}
	for _, rec := range recs {
		if rec.Kind != kind || rec.Request.Origin != server.RequestOriginChild || rec.Request.ChildSessionID != testutil.FakeSupervisorSubagentID {
			t.Fatalf("%s record = %+v (request %+v)", requestID, rec, rec.Request)
		}
	}
	if recs[0].Request.Stage != server.SupervisorRequestStageRequested || recs[1].Request.Stage != server.SupervisorRequestStageResolved || recs[1].Request.Outcome != wantOutcome {
		t.Fatalf("%s stages = %+v then %+v", requestID, recs[0].Request, recs[1].Request)
	}
}

// lastAssistantText returns the newest assistant record's text.
func lastAssistantText(items []server.SupervisorRecord) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == server.SupervisorRecordKindAssistant {
			return recordText(items[i])
		}
	}
	return ""
}

func TestSupervisorSubagentPermissionSurfacesTaggedAndReleasesTheTool(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))

	for i, tc := range []struct {
		decision    string
		wantOutcome server.SupervisorRequestRecordOutcome
		wantReply   string
	}{
		{"allow_once", server.SupervisorRequestOutcomeAllowed, "Sub-agent allowed"},
		{"deny", server.SupervisorRequestOutcomeDenied, "Sub-agent denied"},
	} {
		h.send("delegate "+testutil.FakeSupervisorSubagentPermBash, "s"+tc.decision)
		evs := stream.until(tc.decision+" waiting", isState(server.SupervisorLifecycleWaitingPermission))
		req := requestEventBefore(evs, "Bash")
		if req == nil {
			t.Fatalf("%s: no request event before the waiting state", tc.decision)
		}
		if req.Origin != server.RequestOriginChild || req.ChildSessionID != testutil.FakeSupervisorSubagentID {
			t.Fatalf("%s: request event origin = %q/%q", tc.decision, req.Origin, req.ChildSessionID)
		}
		st := h.state()
		if len(st.PendingRequests) != 1 {
			t.Fatalf("%s pending = %+v", tc.decision, st.PendingRequests)
		}
		pending := st.PendingRequests[0]
		if pending.ToolName != "Bash" || pending.Origin != server.RequestOriginChild || pending.ChildSessionID != testutil.FakeSupervisorSubagentID || pending.SessionID != st.SessionID {
			t.Fatalf("%s pending = %+v", tc.decision, pending)
		}
		// The inbox lists the same request, tagged, through the global
		// permission snapshot.
		var perms server.PermissionSnapshotResponse
		h.do(http.MethodGet, "/api/v1/permissions", nil, http.StatusOK, &perms)
		found := false
		for _, p := range perms.Requests {
			if p.RequestID == pending.RequestID {
				found = p.Origin == server.RequestOriginChild && p.ChildSessionID == testutil.FakeSupervisorSubagentID
			}
		}
		if !found {
			t.Fatalf("%s: permission snapshot lacks the tagged child request: %+v", tc.decision, perms.Requests)
		}

		h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
			"request_id": pending.RequestID, "session_id": st.SessionID, "decision": tc.decision,
		}, http.StatusOK, nil)
		stream.until(tc.decision+" idle", isState(server.SupervisorLifecycleIdle))
		if st := h.state(); len(st.PendingRequests) != 0 {
			t.Fatalf("%s: pending not cleared: %+v", tc.decision, st.PendingRequests)
		}
		items := h.transcript("?limit=500").Items
		assertChildRecords(t, items, pending.RequestID, server.SupervisorRecordKindPermission, tc.wantOutcome)
		if got := lastAssistantText(items); got != tc.wantReply {
			t.Fatalf("%s: reply %d = %q, want %q", tc.decision, i, got, tc.wantReply)
		}
	}
}

// This journey needs the session layer to let a sub-agent's question
// through on supervisor sessions (feature workers still deny it).
func TestSupervisorSubagentQuestionSurfacesTaggedAndAnswerReturnsToTheSubagent(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))

	h.send("delegate "+testutil.FakeSupervisorSubagentAsk, "q1")
	evs := stream.until("child question", isState(server.SupervisorLifecycleWaitingQuestion))
	req := requestEventBefore(evs, "AskUserQuestion")
	if req == nil || req.Origin != server.RequestOriginChild || req.ChildSessionID != testutil.FakeSupervisorSubagentID {
		t.Fatalf("request event = %+v", req)
	}
	st := h.state()
	if len(st.PendingRequests) != 1 || len(st.PendingRequests[0].Questions) == 0 || st.PendingRequests[0].Origin != server.RequestOriginChild {
		t.Fatalf("question pending = %+v", st.PendingRequests)
	}
	requestID := st.PendingRequests[0].RequestID
	h.do(http.MethodPost, "/api/v1/prompts/ask-user/answer", map[string]any{
		"request_id": requestID, "session_id": st.SessionID, "answers": map[string]string{"1": "dev"},
	}, http.StatusOK, nil)
	stream.until("question idle", isState(server.SupervisorLifecycleIdle))
	items := h.transcript("?limit=500").Items
	assertChildRecords(t, items, requestID, server.SupervisorRecordKindQuestion, server.SupervisorRequestOutcomeAnswered)
	if got := lastAssistantText(items); got != "Sub-agent answered dev" {
		t.Fatalf("reply = %q, want the sub-agent to receive the chosen label", got)
	}
}

func TestSupervisorSubagentRequestIsClearedByInterrupt(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))
	h.send("delegate "+testutil.FakeSupervisorSubagentPermBash, "i1")
	stream.until("child waiting", isState(server.SupervisorLifecycleWaitingPermission))
	var res server.SupervisorActionResponse
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, &res)
	if len(res.State.PendingRequests) != 0 {
		t.Fatalf("interrupt left pending = %+v", res.State.PendingRequests)
	}
}
