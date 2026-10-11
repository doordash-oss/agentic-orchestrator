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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func newOpenCodeSupervisorHarness(t *testing.T, script testutil.FakeOpenCodeScript, mutate ...func(*supervisor.Options)) *supervisorHarness {
	t.Helper()
	box := newOpenCodeSandbox(t, false)
	for _, kv := range box.env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, value)
		}
	}
	h := newHarnessBase(t, "opencode")
	h.script = testutil.WriteFakeOpenCodeScript(t, script)
	h.registry = testutil.NewFakeOpenCodeRegistry(t, h.script)
	h.init(mutate...)
	return h
}

func TestSupervisorOpenCodeRootControls(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	for _, tc := range []struct {
		marker, tool, want string
	}{
		{testutil.FakeOpenCodePermBash, "Bash", "Command allowed"},
		{testutil.FakeOpenCodePermEdit, "Write", "Edit allowed"},
		{testutil.FakeOpenCodePermTask, "Agent", "Task allowed"},
	} {
		h.send(tc.marker, tc.marker)
		st := h.waitLifecycle(server.SupervisorLifecycleWaitingPermission)
		if len(st.PendingRequests) != 1 || st.PendingRequests[0].ToolName != tc.tool || st.PendingRequests[0].Origin != server.RequestOriginRoot {
			t.Fatalf("%s pending = %+v", tc.marker, st.PendingRequests)
		}
		req := st.PendingRequests[0]
		h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
			"request_id": req.RequestID, "session_id": st.SessionID, "decision": "allow_once",
		}, http.StatusOK, nil)
		h.waitLifecycle(server.SupervisorLifecycleIdle)
		if got := lastAssistantText(h.transcript("").Items); got != tc.want {
			t.Fatalf("%s reply = %q", tc.marker, got)
		}
	}
	h.send(testutil.FakeOpenCodePermHelper, "helper")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if st := h.state(); len(st.PendingRequests) != 0 {
		t.Fatalf("bare helper request surfaced: %+v", st.PendingRequests)
	}
	h.send(testutil.FakeOpenCodeAsk, "ask")
	st := h.waitLifecycle(server.SupervisorLifecycleWaitingQuestion)
	if len(st.PendingRequests) != 1 || st.PendingRequests[0].Origin != server.RequestOriginRoot {
		t.Fatalf("root question = %+v", st.PendingRequests)
	}
	h.do(http.MethodPost, "/api/v1/prompts/ask-user/answer", map[string]any{
		"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID,
		"answers": map[string]string{"1": "dev"},
	}, http.StatusOK, nil)
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if got := lastAssistantText(h.transcript("").Items); got != "You chose dev" {
		t.Fatalf("question answer = %q", got)
	}
}

func TestSupervisorOpenCodeHTTPFailureAndRetry(t *testing.T) {
	for name, script := range map[string]testutil.FakeOpenCodeScript{
		"no server":      {NoHTTP: true},
		"wrong password": {RejectPassword: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOpenCodeSupervisorHarness(t, script, func(o *supervisor.Options) { o.HandshakeTimeout = 700 * time.Millisecond })
			h.chooseSettings()
			var failed server.ErrorResponse
			h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{
				"text": "hello", "client_message_id": "failed",
			}, http.StatusBadGateway, &failed)
			h.assertLaunchFailed(failed.Error)
			if st := h.state(); st.Lifecycle != server.SupervisorLifecycleFailed {
				t.Fatalf("launch failure state = %+v", st)
			}
			testutil.UpdateFakeOpenCodeScript(t, h.script, testutil.FakeOpenCodeScript{})
			h.send("hello", "failed")
			h.waitLifecycle(server.SupervisorLifecycleIdle)
		})
	}
}

func TestSupervisorOpenCodeRestartSeedsWithoutReply(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	h.send("remember the blue door", "r1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.send("continue "+testutil.FakeOpenCodePartial, "r2")
	deadline := time.Now().Add(15 * time.Second)
	for {
		var sawChunk, sawTool bool
		for _, line := range testutil.FakeOpenCodeACP(t, h.script) {
			if line.Dir == "out" && line.Method == "session/update" {
				sawChunk = sawChunk || strings.Contains(string(line.Params), testutil.FakeSupervisorPartialText)
				sawTool = sawTool || strings.Contains(string(line.Params), "tool_call")
			}
		}
		if sawChunk && sawTool {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake did not stream partial text and tool call: chunk=%t tool=%t", sawChunk, sawTool)
		}
		time.Sleep(20 * time.Millisecond)
	}
	before := h.state()
	pid := h.providerPID(before)
	h.crash()
	if processGroupAlive(pid) {
		t.Fatalf("orphaned OpenCode process group %d remained alive", pid)
	}
	if st := h.state(); st.Lifecycle != server.SupervisorLifecycleStopped || st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted {
		t.Fatalf("restarted state = %+v", st)
	}
	stream := h.openStream("")
	stream.until("initial state", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventState) })
	h.send("what was the door?", "r3")
	events := stream.until("seeded reply", isState(server.SupervisorLifecycleIdle))
	var steps []server.SupervisorStartingStep
	for _, ev := range events {
		if ev.data.State != nil && ev.data.State.Lifecycle == server.SupervisorLifecycleStarting {
			step := ev.data.State.StartingStep
			if len(steps) == 0 || steps[len(steps)-1] != step {
				steps = append(steps, step)
			}
		}
	}
	if got := strings.Join(stepNames(steps), ","); got != "rebuilding,launching,handshake" {
		t.Fatalf("relaunch steps = %s", got)
	}
	seeds := testutil.FakeOpenCodeSeeds(t, h.script)
	if len(seeds) != 1 || !seeds[0].NoReply || seeds[0].SessionID == "" {
		t.Fatalf("history seed = %+v", seeds)
	}
	if !strings.Contains(seeds[0].Text, "remember the blue door") || !strings.Contains(seeds[0].Text, "continue "+testutil.FakeOpenCodePartial) || !strings.Contains(seeds[0].Text, testutil.FakeSupervisorPartialText) {
		t.Fatalf("history seed lost previous text: %s", seeds[0].Text)
	}
	if strings.Contains(seeds[0].Text, "toolu_partial") {
		t.Fatalf("interrupted tool call was seeded: %s", seeds[0].Text)
	}
	if n := testutil.FakeOpenCodeModelReplies(t, h.script); n != 0 {
		t.Fatalf("seed produced %d model replies", n)
	}
	if st := h.coord.State(); st.NativeSessionID != "" {
		t.Fatalf("native id was minted: %q", st.NativeSessionID)
	}
	if got := lastAssistantText(h.transcript("").Items); !strings.Contains(got, "Seeded with") || !strings.Contains(got, "remember the blue door") {
		t.Fatalf("reply did not use history: %q", got)
	}
	sessions := testutil.FakeOpenCodeSessions(t, h.script)
	if len(sessions) < 2 || seeds[0].SessionID != sessions[len(sessions)-1].ID {
		t.Fatalf("seed target %q not new ACP session: %+v", seeds[0].SessionID, sessions)
	}
}

func TestSupervisorOpenCodeSeedFailureRetry(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	h.send("keep this text", "f1")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	h.waitLifecycle(server.SupervisorLifecycleStopped)
	testutil.UpdateFakeOpenCodeScript(t, h.script, testutil.FakeOpenCodeScript{SeedError: http.StatusInternalServerError})
	var failed server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "try the seed", "client_message_id": "f2"}, http.StatusBadGateway, &failed)
	h.assertLaunchFailed(failed.Error)
	h.waitState("failed seed", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleFailed && st.Failure != nil
	})
	if got := lastAssistantText(h.transcript("").Items); got != "Hello from turn 1" {
		t.Fatalf("failed seed changed prior reply: %q", got)
	}
	testutil.UpdateFakeOpenCodeScript(t, h.script, testutil.FakeOpenCodeScript{})
	h.send("try the seed", "f2")
	h.waitState("retry completed", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted
	})
	if got := lastAssistantText(h.transcript("").Items); !strings.Contains(got, "keep this text") {
		t.Fatalf("retry lost history: %q", got)
	}
}

func TestSupervisorOpenCodeStopUsesSessionCancel(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	h.send("hold "+testutil.FakeOpenCodeHold, "h1")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st := h.waitLifecycle(server.SupervisorLifecycleIdle)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted || st.InterruptedBy != server.SupervisorInterruptedByUser {
		t.Fatalf("interrupted state = %+v", st)
	}
	deadline := time.Now().Add(time.Second)
	for {
		if strings.Contains(strings.Join(testutil.FakeOpenCodeMethods(t, h.script), ","), "session/cancel") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session/cancel was not sent")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *supervisorHarness) openCodePrompts() []testutil.FakeOpenCodeACPLine {
	h.t.Helper()
	var prompts []testutil.FakeOpenCodeACPLine
	for _, line := range testutil.FakeOpenCodeACP(h.t, h.script) {
		if line.Dir == "in" && line.Method == "session/prompt" {
			prompts = append(prompts, line)
		}
	}
	return prompts
}

func TestSupervisorOpenCodeFirstSendAndReuse(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))
	h.send("hello opencode", "c1")
	stream.until("first reply", isState(server.SupervisorLifecycleIdle))
	if second := h.send("again", "c2"); second.Launched {
		t.Fatalf("second send relaunched: %+v", second)
	}
	h.waitState("second reply", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 4
	})
	if n := testutil.FakeOpenCodeInvocations(t, h.script); n != 1 {
		t.Fatalf("launches = %d, want one", n)
	}
	methods := strings.Join(testutil.FakeOpenCodeMethods(t, h.script), ",")
	if !strings.HasPrefix(methods, "initialize,session/new,session/prompt") {
		t.Fatalf("ACP methods = %s", methods)
	}
	prompts := h.openCodePrompts()
	if len(prompts) != 2 || prompts[0].PromptText() != "hello opencode" || prompts[1].PromptText() != "again" {
		t.Fatalf("ACP prompts = %+v", prompts)
	}
	st := h.coord.State()
	if st.NativeSessionID != "" || st.EffectiveModel != testutil.FakeOpenCodeModel {
		t.Fatalf("native id/model = %q/%q", st.NativeSessionID, st.EffectiveModel)
	}
	if got := lastAssistantText(h.transcript("").Items); got != "Hello from turn 2" {
		t.Fatalf("reply = %q", got)
	}
	envs := testutil.FakeOpenCodeEnvs(t, h.script)
	if len(envs) != 1 {
		t.Fatalf("launch envs = %+v", envs)
	}
	var overlay map[string]json.RawMessage
	if err := json.Unmarshal([]byte(envs[0].Env["OPENCODE_CONFIG_CONTENT"]), &overlay); err != nil {
		t.Fatalf("overlay JSON: %v", err)
	}
	for _, key := range []string{"model", "instructions", "permission", "agent", "autoupdate"} {
		if _, ok := overlay[key]; !ok {
			t.Fatalf("overlay lacks %s: %s", key, envs[0].Env["OPENCODE_CONFIG_CONTENT"])
		}
	}
	for _, key := range []string{"OPENCODE_CONFIG", "OPENCODE_PURE", "OPENCODE_DISABLE_PLUGINS", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_MCP"} {
		if _, ok := envs[0].Env[key]; ok {
			t.Fatalf("interactive launch carries isolation variable %s", key)
		}
	}
	if len(testutil.FakeOpenCodeSeeds(t, h.script)) != 0 {
		t.Fatal("first generation posted a history seed")
	}
	if argv := strings.Join(testutil.FakeOpenCodeArgv(t, h.script), " "); !strings.Contains(argv, "--port") || !strings.Contains(argv, "--hostname 127.0.0.1") {
		t.Fatalf("ACP argv = %s", argv)
	}
}

func TestSupervisorOpenCodeConcurrentSendsStartOneProcess(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
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
	for _, result := range results {
		if result.Launched {
			launched++
		}
	}
	if n := testutil.FakeOpenCodeInvocations(t, h.script); launched != 1 || n != 1 {
		t.Fatalf("launched=%d invocations=%d, want one process", launched, n)
	}
	h.waitState("all OpenCode turns answered", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq == 2*senders
	})
}

func TestSupervisorOpenCodeStubbornStopAndShutdown(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{}, func(o *supervisor.Options) { o.InterruptGrace = 700 * time.Millisecond })
	h.chooseSettings()
	h.send("stuck "+testutil.FakeOpenCodeStubborn, "s1")
	running := h.waitLifecycle(server.SupervisorLifecycleRunning)
	pid := h.providerPID(running)
	if activity, err := h.admission.Detect(context.Background()); err != nil || !activity.SupervisorActive {
		t.Fatalf("running supervisor activity = %+v %v", activity, err)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st := h.waitLifecycle(server.SupervisorLifecycleStopped)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted {
		t.Fatalf("stubborn stop = %+v", st)
	}
	waitGone(t, pid)
	if !h.admission.CloseForStopping(workadmission.CategoryFeature, workadmission.CategorySupervisor) {
		t.Fatal("admission did not close for stopping")
	}
	var refused server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "again", "client_message_id": "s2"}, http.StatusServiceUnavailable, &refused)
	if refused.Error.Code != "update_in_progress" {
		t.Fatalf("closed send = %+v", refused.Error)
	}
	h.admission.Open()
	h.send("one more", "s3")
	st = h.waitLifecycle(server.SupervisorLifecycleIdle)
	pid = h.providerPID(st)
	_ = h.coord.Close()
	waitGone(t, pid)
}

func TestSupervisorOpenCodeChildPermissionAndQuestion(t *testing.T) {
	h := newOpenCodeSupervisorHarness(t, testutil.FakeOpenCodeScript{})
	h.chooseSettings()
	for _, tc := range []struct {
		marker, tool, lifecycle, answerPath, reply string
	}{
		{testutil.FakeOpenCodeChildPermBash, "Bash", string(server.SupervisorLifecycleWaitingPermission), "/api/v1/permissions/answer", "Child allowed"},
		{testutil.FakeOpenCodeChildAsk, "AskUserQuestion", string(server.SupervisorLifecycleWaitingQuestion), "/api/v1/prompts/ask-user/answer", "Child chose dev"},
	} {
		stream := h.openStream("")
		stream.until("initial state", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventState) })
		h.send("delegate "+tc.marker, tc.marker)
		events := stream.until("child waiting", func(ev sseEvent) bool {
			return ev.data.State != nil && string(ev.data.State.Lifecycle) == tc.lifecycle
		})
		req := requestEventBefore(events, tc.tool)
		st := h.state()
		if req == nil || req.Origin != server.RequestOriginChild || len(st.PendingRequests) != 1 || st.PendingRequests[0].Origin != server.RequestOriginChild || st.PendingRequests[0].ChildSessionID == "" {
			t.Fatalf("child event/pending = %+v / %+v", req, st.PendingRequests)
		}
		pending := st.PendingRequests[0]
		var body any = map[string]string{"request_id": pending.RequestID, "session_id": st.SessionID, "decision": "allow_once"}
		if tc.tool == "AskUserQuestion" {
			body = map[string]any{"request_id": pending.RequestID, "session_id": st.SessionID, "answers": map[string]string{"1": "dev"}}
		}
		h.do(http.MethodPost, tc.answerPath, body, http.StatusOK, nil)
		stream.until("child reply", isState(server.SupervisorLifecycleIdle))
		items := h.transcript("?limit=500").Items
		if got := lastAssistantText(items); got != tc.reply {
			t.Fatalf("%s reply = %q", tc.marker, got)
		}
		if recs := requestRecordsFor(items, pending.RequestID); len(recs) != 2 || recs[0].Request.Origin != server.RequestOriginChild || recs[1].Request.Origin != server.RequestOriginChild {
			t.Fatalf("%s records = %+v", tc.marker, recs)
		}
		stream.cancel()
	}
	for _, line := range testutil.FakeOpenCodeACP(t, h.script) {
		if line.Dir == "out" && line.Method == "session/request_permission" && strings.Contains(string(line.Params), "child") {
			t.Fatalf("child permission leaked onto ACP: %+v", line)
		}
	}
}
