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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// codexMethods lists the inbound methods the fake Codex recorded.
func (h *supervisorHarness) codexMethods() []string {
	h.t.Helper()
	return testutil.FakeCodexMethods(h.t, h.script)
}

// codexTurns returns the recorded turn/start requests in order.
func (h *supervisorHarness) codexTurns() []testutil.FakeCodexRequest {
	h.t.Helper()
	var out []testutil.FakeCodexRequest
	for _, req := range testutil.FakeCodexRequests(h.t, h.script) {
		if req.Method == "turn/start" {
			out = append(out, req)
		}
	}
	return out
}

// durableRecords reads the conversation's durable transcript records.
func (h *supervisorHarness) durableRecords() []supervisor.Record {
	h.t.Helper()
	page, err := h.coord.Transcript(supervisor.PageQuery{Limit: 500})
	if err != nil {
		h.t.Fatal(err)
	}
	return page.Items
}

func blocksOf(t *testing.T, rec supervisor.Record) []llm.ContentBlock {
	t.Helper()
	var data supervisor.ContentData
	if err := json.Unmarshal(rec.Data, &data); err != nil {
		t.Fatalf("decode record %d: %v", rec.Seq, err)
	}
	return data.Content
}

// rolloutFor finds the rollout file Codex resumes for threadID under the
// harness's CODEX_HOME.
func (h *supervisorHarness) rolloutFor(threadID string) string {
	h.t.Helper()
	matches, _ := filepath.Glob(filepath.Join(h.codexHome, "sessions", "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	if len(matches) != 1 {
		h.t.Fatalf("rollouts for %s = %v", threadID, matches)
	}
	return matches[0]
}

// rolloutUserPrompts lists the user message items of a rollout.
func rolloutUserPrompts(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var item struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("rollout line %q: %v", line, err)
		}
		if item.Type == "response_item" && item.Payload.Type == "message" && item.Payload.Role == "user" && len(item.Payload.Content) > 0 {
			out = append(out, item.Payload.Content[0].Text)
		}
	}
	return out
}

func TestSupervisorCodexFirstSendRecordsThreadAndReusesProcess(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))

	first := h.send("hello codex", "c1")
	if !first.Launched {
		t.Fatalf("first send = %+v", first)
	}
	events := stream.until("idle after turn 1", isState(server.SupervisorLifecycleIdle))
	for _, ev := range events {
		if ev.data.State != nil && ev.data.State.StartingStep == server.SupervisorStartingStepRebuilding {
			t.Fatal("a conversation with no history reported the rebuilding step")
		}
	}
	if second := h.send("again", "c2"); second.Launched {
		t.Fatalf("second send relaunched: %+v", second)
	}
	h.waitState("turn 2 answered", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 4
	})
	if n := h.invocations(); n != 1 {
		t.Fatalf("Codex invocations = %d, want 1", n)
	}
	methods := strings.Join(h.codexMethods(), ",")
	if !strings.HasPrefix(methods, "initialize,initialized,thread/start,turn/start") || strings.Contains(methods, "thread/resume") {
		t.Fatalf("recorded methods = %s", methods)
	}
	turns := h.codexTurns()
	if len(turns) != 2 || turns[0].TurnText() != "hello codex" || turns[1].TurnText() != "again" {
		t.Fatalf("turn texts = %v", turns)
	}
	var params struct {
		Model          string `json:"model"`
		ApprovalPolicy string `json:"approvalPolicy"`
		SandboxPolicy  struct {
			Type          string   `json:"type"`
			WritableRoots []string `json:"writableRoots"`
			NetworkAccess bool     `json:"networkAccess"`
		} `json:"sandboxPolicy"`
	}
	if err := json.Unmarshal(turns[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.model, params.Model) || params.ApprovalPolicy != "on-request" || params.SandboxPolicy.Type != "workspaceWrite" ||
		!params.SandboxPolicy.NetworkAccess || len(params.SandboxPolicy.WritableRoots) == 0 {
		t.Fatalf("first turn launch profile = %+v", params)
	}
	threads := testutil.FakeCodexThreads(t, h.script)
	st := h.coord.State()
	if len(threads) != 1 || st.NativeSessionID != threads[0] || !strings.HasPrefix(h.model, st.EffectiveModel) {
		t.Fatalf("read model native=%q model=%q, fake minted %v", st.NativeSessionID, st.EffectiveModel, threads)
	}
	if got := h.transcript(""); recordKinds(got.Items) != "user,assistant,user,assistant" || recordText(got.Items[1]) != "Hello from turn 1" {
		t.Fatalf("transcript = %s (%q)", recordKinds(got.Items), recordText(got.Items[1]))
	}
	for _, req := range testutil.FakeCodexRequests(t, h.script) {
		if req.Method == "thread/start" && !strings.Contains(string(req.Params), "Runtime directory: "+h.runtimeDir) {
			t.Fatalf("thread/start lacks the supervisor developer instructions: %s", req.Params)
		}
	}
}

func TestSupervisorCodexConcurrentSendsStartOneProcess(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	const senders = 5
	var wg sync.WaitGroup
	results := make([]server.SupervisorMessageResponse, senders)
	for i := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = h.send(fmt.Sprintf("msg %d", i), fmt.Sprintf("c%d", i))
		}()
	}
	wg.Wait()
	launched := 0
	for _, r := range results {
		if r.Launched {
			launched++
		}
	}
	if launched != 1 || h.invocations() != 1 {
		t.Fatalf("launched=%d invocations=%d, want one launch", launched, h.invocations())
	}
	h.waitState("all turns answered", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 2*senders
	})
}

func TestSupervisorCodexToolActivityApprovalsAndQuestions(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))
	h.send("warm up", "c0")
	stream.until("idle", isState(server.SupervisorLifecycleIdle))

	for i, tc := range []struct{ marker, tool string }{
		{testutil.FakeCodexPermBash, "Bash"},
		{testutil.FakeCodexPermWrite, "Write"},
	} {
		h.send("please "+tc.marker, fmt.Sprintf("p%d", i))
		stream.until(tc.tool+" waiting", isState(server.SupervisorLifecycleWaitingPermission))
		st := h.state()
		if len(st.PendingRequests) != 1 || st.PendingRequests[0].ToolName != tc.tool {
			t.Fatalf("%s pending = %+v", tc.tool, st.PendingRequests)
		}
		h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
			"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID, "decision": "allow_once",
		}, http.StatusOK, nil)
		stream.until(tc.tool+" idle", isState(server.SupervisorLifecycleIdle))
	}

	// A bare helper call is auto-allowed and never surfaces.
	h.send("check "+testutil.FakeCodexPermHelper, "r1")
	for _, ev := range stream.until("helper turn idle", isState(server.SupervisorLifecycleIdle)) {
		if ev.kind == string(server.SupervisorEventRequest) || isState(server.SupervisorLifecycleWaitingPermission)(ev) {
			t.Fatalf("the helper command surfaced: %+v", ev.data)
		}
	}

	h.send("decide "+testutil.FakeCodexAsk, "q1")
	stream.until("question", isState(server.SupervisorLifecycleWaitingQuestion))
	st := h.state()
	if len(st.PendingRequests) != 1 || len(st.PendingRequests[0].Questions) == 0 {
		t.Fatalf("question pending = %+v", st.PendingRequests)
	}
	h.do(http.MethodPost, "/api/v1/prompts/ask-user/answer", map[string]any{
		"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID, "answers": map[string]string{"Which branch?": "dev"},
	}, http.StatusOK, nil)
	stream.until("question idle", isState(server.SupervisorLifecycleIdle))

	var decisions []string
	for _, req := range testutil.FakeCodexRequests(t, h.script) {
		if req.Method == "" && len(req.Result) > 0 && req.ID != nil && *req.ID > 1000 {
			decisions = append(decisions, string(req.Result))
		}
	}
	if fmt.Sprint(decisions) != `[{"decision":"accept"} {"decision":"accept"} {"decision":"accept"} {"answers":{"branch":{"answers":["dev"]}}}]` {
		t.Fatalf("answers the fake received = %v", decisions)
	}

	// The durable transcript holds Claude-shaped tool history for the command
	// and the file change, and the projection folds it as activity.
	var uses, results []llm.ContentBlock
	for _, rec := range h.durableRecords() {
		switch rec.Kind {
		case supervisor.KindToolUse:
			uses = append(uses, blocksOf(t, rec)...)
		case supervisor.KindToolResult:
			results = append(results, blocksOf(t, rec)...)
		}
	}
	if len(uses) != 3 || len(results) != 3 {
		t.Fatalf("tool_use=%d tool_result=%d, want three of each", len(uses), len(results))
	}
	if uses[0].Name != "Bash" || !strings.Contains(string(uses[0].Input), testutil.FakeCodexBashCommand) {
		t.Fatalf("command tool_use = %+v (%s)", uses[0], uses[0].Input)
	}
	var output string
	_ = json.Unmarshal(results[0].Content, &output)
	if results[0].ToolUseID != uses[0].ID || output != testutil.FakeCodexBashOutput || results[0].IsError {
		t.Fatalf("command tool_result = %+v (%q)", results[0], output)
	}
	if uses[1].Name != "Write" || !strings.Contains(string(uses[1].Input), testutil.FakeCodexWritePath) || results[1].ToolUseID != uses[1].ID {
		t.Fatalf("file change pair = %+v / %+v", uses[1], results[1])
	}
	if uses[2].Name != "Bash" || !strings.Contains(string(uses[2].Input), "agentico api") {
		t.Fatalf("helper tool_use = %+v", uses[2])
	}
	var folded []string
	for _, rec := range h.transcript("?limit=500").Items {
		if rec.Kind == server.SupervisorRecordKindToolUse {
			for _, m := range rec.Messages {
				folded = append(folded, m.Tool)
			}
		}
	}
	if fmt.Sprint(folded) != "[Bash Write Bash]" {
		t.Fatalf("projected tool activity = %v", folded)
	}
}

func TestSupervisorCodexStopInterruptsThroughProtocol(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{}, func(o *supervisor.Options) {
		o.InterruptGrace = 700 * time.Millisecond
	})
	h.chooseSettings()
	h.send("work "+testutil.FakeCodexHold, "h1")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	var action server.SupervisorActionResponse
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, &action)
	if action.Result != server.SupervisorActionAccepted {
		t.Fatalf("interrupt = %+v", action)
	}
	st := h.waitLifecycle(server.SupervisorLifecycleIdle)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted || st.InterruptedBy != server.SupervisorInterruptedByUser {
		t.Fatalf("after interrupt = %+v", st)
	}
	var interrupts []testutil.FakeCodexRequest
	for _, req := range testutil.FakeCodexRequests(t, h.script) {
		if req.Method == "turn/interrupt" {
			interrupts = append(interrupts, req)
		}
	}
	turns := h.codexTurns()
	if len(interrupts) != 1 || !strings.Contains(string(interrupts[0].Params), fmt.Sprintf(`"turnId":"turn-1-%d"`, len(turns))) {
		t.Fatalf("turn/interrupt = %v after %d turns", interrupts, len(turns))
	}
	if n := testutil.FakeCodexSignals(t, h.script); n != 0 {
		t.Fatalf("the fake received %d SIGINTs", n)
	}
	h.send("next", "h2")
	h.waitState("next turn", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted
	})
	if h.invocations() != 1 {
		t.Fatalf("invocations = %d after interrupt", h.invocations())
	}

	// A Codex that ignores the request is terminated after the grace.
	h.send("stuck "+testutil.FakeCodexStubborn, "h3")
	running := h.waitLifecycle(server.SupervisorLifecycleRunning)
	pid := h.providerPID(running)
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st = h.waitLifecycle(server.SupervisorLifecycleStopped)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted {
		t.Fatalf("after grace = %+v", st)
	}
	waitGone(t, pid)
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for processGroupAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d outlived the supervisor process", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSupervisorCodexAdmissionAndShutdown(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send("work "+testutil.FakeCodexHold, "a1")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	if activity, err := h.admission.Detect(context.Background()); err != nil || !activity.SupervisorActive {
		t.Fatalf("running supervisor activity = %+v %v", activity, err)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st := h.waitLifecycle(server.SupervisorLifecycleIdle)
	if activity, _ := h.admission.Detect(context.Background()); activity.SupervisorActive {
		t.Fatal("idle supervisor counted as active work")
	}
	if !h.admission.CloseForStopping(workadmission.CategoryFeature, workadmission.CategorySupervisor) {
		t.Fatal("admission did not close for stopping")
	}
	pid := h.providerPID(st)
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	waitGone(t, pid)
	var refused server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "again", "client_message_id": "a2"}, http.StatusServiceUnavailable, &refused)
	if refused.Error.Code != "update_in_progress" || h.invocations() != 1 {
		t.Fatalf("closed send = %+v invocations=%d", refused.Error, h.invocations())
	}
	h.admission.Open()

	h.send("one more", "a3")
	st = h.waitLifecycle(server.SupervisorLifecycleIdle)
	pid = h.providerPID(st)
	_ = h.coord.Close()
	waitGone(t, pid)
}

// TestSupervisorCodexRestartMidTurnResumesRebuiltThread walks the restart
// journey on Codex: the server dies while a turn holds after partial text and
// a command; boot terminates the orphan; the next message rebuilds the
// rollout under the adopted thread id and resumes it before the first turn.
func TestSupervisorCodexRestartMidTurnResumesRebuiltThread(t *testing.T) {
	h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
	h.chooseSettings()
	h.send("remember the blue door", "c1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	threads := testutil.FakeCodexThreads(t, h.script)
	if len(threads) != 1 {
		t.Fatalf("minted threads = %v", threads)
	}
	native := threads[0]
	h.send("now work on it "+testutil.FakeCodexPartial, "c2")
	h.waitRecord("partial text", func(rec server.SupervisorRecord) bool {
		return rec.Kind == server.SupervisorRecordKindAssistant && strings.Contains(recordText(rec), testutil.FakeSupervisorPartialText)
	})
	h.waitRecord("partial command", func(rec server.SupervisorRecord) bool { return rec.Kind == server.SupervisorRecordKindToolUse })
	pid := h.providerPID(h.state())

	h.crash()
	st := h.state()
	if processGroupAlive(pid) {
		t.Fatalf("boot reported ready while the orphaned Codex %d is alive", pid)
	}
	if st.Lifecycle != server.SupervisorLifecycleStopped || st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted ||
		st.InterruptedBy != server.SupervisorInterruptedByShutdown {
		t.Fatalf("state after restart = %+v", st)
	}

	stream := h.openStream("")
	h.send("what did I ask you to remember?", "c3")
	evs := stream.until("idle after resume", isState(server.SupervisorLifecycleIdle))
	var steps []server.SupervisorStartingStep
	for _, ev := range evs {
		if ev.data.State != nil && ev.data.State.Lifecycle == server.SupervisorLifecycleStarting {
			if step := ev.data.State.StartingStep; len(steps) == 0 || steps[len(steps)-1] != step {
				steps = append(steps, step)
			}
		}
	}
	if got := strings.Join(stepNames(steps), ","); got != "rebuilding,launching,handshake" {
		t.Fatalf("starting steps = %s", got)
	}
	resumes := testutil.FakeCodexResumes(t, h.script)
	if len(resumes) != 1 || resumes[0].ThreadID != native || !resumes[0].Found || resumes[0].Launch != 2 {
		t.Fatalf("resumes = %+v, want one of %s", resumes, native)
	}
	var launch2 []string
	for _, req := range testutil.FakeCodexRequests(t, h.script) {
		if req.Launch == 2 && req.Method != "" && req.Method != "account/usage/read" {
			launch2 = append(launch2, req.Method)
		}
	}
	if !strings.HasPrefix(strings.Join(launch2, ","), "initialize,initialized,thread/resume,turn/start") {
		t.Fatalf("second launch methods = %v", launch2)
	}
	items := h.transcript("").Items
	if answer := recordText(items[len(items)-1]); answer != "Resumed with 2 prior messages: remember the blue door" {
		t.Fatalf("resumed reply = %q", answer)
	}
	rollout := h.rolloutFor(native)
	data, err := os.ReadFile(rollout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "now work on it") || !strings.Contains(string(data), testutil.FakeSupervisorPartialText) {
		t.Fatalf("rollout lost the cut turn's prompt or partial text:\n%s", data)
	}
	if strings.Contains(string(data), "sleep 600") {
		t.Fatalf("rollout kept the cut turn's command:\n%s", data)
	}

	// Two more generations keep the adopted id and replace the rollout.
	for i, cmid := range []string{"c4", "c5"} {
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)
		h.send("follow-up "+cmid, cmid)
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		if got := h.coord.State().NativeSessionID; got != native {
			t.Fatalf("generation %d native id = %q, want %q", i+3, got, native)
		}
		resumes := testutil.FakeCodexResumes(t, h.script)
		if last := resumes[len(resumes)-1]; last.ThreadID != native {
			t.Fatalf("generation %d resumed %q", i+3, last.ThreadID)
		}
		prompts := rolloutUserPrompts(t, h.rolloutFor(native))
		if n := strings.Count(strings.Join(prompts, "\n"), "remember the blue door"); n != 1 {
			t.Fatalf("generation %d: first prompt appears %d times; the rollout was appended, not replaced: %q", i+3, n, prompts)
		}
	}
	if threads := testutil.FakeCodexThreads(t, h.script); len(threads) != 1 {
		t.Fatalf("resumed generations minted new threads: %v", threads)
	}
}

func TestSupervisorCodexResumeFailures(t *testing.T) {
	t.Run("thread store error falls back to a fresh thread", func(t *testing.T) {
		h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
		h.chooseSettings()
		h.send("hello", "c1")
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		first := h.coord.State().NativeSessionID
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)

		testutil.UpdateFakeCodexScript(t, h.script, testutil.FakeCodexScript{ResumeError: &testutil.FakeCodexRPCError{Code: -32603, Message: "failed to load thread from thread store"}})
		h.send("again", "c2")
		st := h.waitState("fallback turn", func(st server.SupervisorState) bool {
			return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted && st.Generation == 2
		})
		threads := testutil.FakeCodexThreads(t, h.script)
		if len(threads) != 2 || threads[0] != first || h.coord.State().NativeSessionID != threads[1] {
			t.Fatalf("threads %v native %q", threads, h.coord.State().NativeSessionID)
		}
		markers := markerRecords(h.transcript(""), server.SupervisorMarkerHistoryNotRestored)
		if len(markers) != 1 || markers[0].Marker.Text != "Codex could not read the restored thread; continuing on a fresh thread" {
			raw, _ := json.Marshal(markers)
			t.Fatalf("history_not_restored markers = %s (state %+v)", raw, st)
		}

		testutil.UpdateFakeCodexScript(t, h.script, testutil.FakeCodexScript{})
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)
		h.send("third", "c3")
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		resumes := testutil.FakeCodexResumes(t, h.script)
		if last := resumes[len(resumes)-1]; last.ThreadID != threads[1] || !last.Found {
			t.Fatalf("launch after fallback resumed %+v, want %s", last, threads[1])
		}
	})

	t.Run("other resume error fails the launch and retry succeeds", func(t *testing.T) {
		h := newCodexSupervisorHarness(t, testutil.FakeCodexScript{})
		h.chooseSettings()
		h.send("hello", "c1")
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleStopped)
		head := h.state().HeadSeq

		testutil.UpdateFakeCodexScript(t, h.script, testutil.FakeCodexScript{ResumeError: &testutil.FakeCodexRPCError{Code: -32600, Message: "invalid thread"}})
		started := time.Now()
		var failed server.ErrorResponse
		h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "again", "client_message_id": "c2"}, http.StatusBadGateway, &failed)
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("the rejected resume took %s to fail; it must not wait out the handshake timeout", elapsed)
		}
		h.assertLaunchFailed(failed.Error)
		if !strings.Contains(failed.Error.Diagnostics, "invalid thread") {
			t.Fatalf("failure does not name the resume error: %+v", failed.Error)
		}
		if page := h.transcript(""); page.HeadSeq != head+1 {
			t.Fatalf("transcript head = %d, want only the error marker added after %d", page.HeadSeq, head)
		}

		testutil.UpdateFakeCodexScript(t, h.script, testutil.FakeCodexScript{})
		if resp := h.send("again", "c2"); resp.Record.Generation != 3 {
			t.Fatalf("retry = %+v", resp)
		}
		if st := h.waitLifecycle(server.SupervisorLifecycleIdle); st.Failure != nil {
			t.Fatalf("failure survived the retry: %+v", st.Failure)
		}
	})
}
