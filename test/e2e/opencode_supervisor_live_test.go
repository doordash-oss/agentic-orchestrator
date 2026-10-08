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
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// TestOpenCodeSupervisorLive uses the real coordinator and adapter. It is an
// opt-in inference test because the delegated shell command needs a configured
// OpenCode model and a user decision through Agentico's answer route.
func TestOpenCodeSupervisorLive(t *testing.T) {
	if os.Getenv(openCodeLiveEnvVar) != "1" {
		t.Skipf("set %s=1 to run the live OpenCode supervisor journey", openCodeLiveEnvVar)
	}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatalf("opencode not on PATH: %v", err)
	}
	box := newOpenCodeSandbox(t, true)
	model := openCodeLiveModel(t, bin, box.env)
	for _, kv := range box.env {
		key, value, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(key, "XDG_") {
			t.Setenv(key, value)
		}
	}
	p := opencode.NewWithBinary(bin)
	p.SetModelCatalog([]llm.ModelInfo{{ID: model, DisplayName: model, ContextWindow: 200000}})
	h := newHarnessBase(t, "opencode")
	h.registry = llm.NewRegistry()
	h.registry.Register(p)
	h.init(func(o *supervisor.Options) { o.HandshakeTimeout = 2 * time.Minute })
	h.chooseSettings()
	const codeword = "amber-quartz-731"
	h.send("Remember this codeword: "+codeword+". Delegate to a sub-agent: ask it to run `printf "+codeword+"` in a shell, then report the exact command output. Do not run the command yourself.", "live-1")
	deadline := time.Now().Add(4 * time.Minute)
	var rootTaskApproved, childApproved bool
	for !childApproved {
		st := h.state()
		if st.Lifecycle == server.SupervisorLifecycleFailed || time.Now().After(deadline) {
			t.Fatalf("child permission did not arrive: state %+v", st)
		}
		for _, req := range st.PendingRequests {
			if req.ToolName == "Agent" && req.Origin == server.RequestOriginRoot && !rootTaskApproved {
				h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
					"request_id": req.RequestID, "session_id": st.SessionID, "decision": "allow_once",
				}, http.StatusOK, nil)
				rootTaskApproved = true
				break
			}
			if req.ToolName == "Bash" && req.Origin == server.RequestOriginChild {
				h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
					"request_id": req.RequestID, "session_id": st.SessionID, "decision": "allow_once",
				}, http.StatusOK, nil)
				childApproved = true
				break
			}
		}
		if !childApproved {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !rootTaskApproved {
		t.Fatal("child bash request arrived without an approved root task spawn")
	}
	st := waitOpenCodeLiveState(t, h, "delegated turn", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle || st.Lifecycle == server.SupervisorLifecycleFailed
	})
	if st.Lifecycle != server.SupervisorLifecycleIdle || !strings.Contains(lastAssistantText(h.transcript("").Items), codeword) {
		t.Fatalf("delegated reply/state = %q / %+v", lastAssistantText(h.transcript("").Items), st)
	}
	if st := h.coord.State(); st.NativeSessionID != "" {
		t.Fatalf("OpenCode adopted an ephemeral ACP session id: %q", st.NativeSessionID)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	waitOpenCodeLiveState(t, h, "process ended", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleStopped })
	stream := h.openStream("")
	stream.until("stopped state", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventState) })
	beforeSeed := countAssistantRecords(h.transcript("?limit=500").Items)
	h.send("Read the restored prior conversation history in this session. What codeword did I ask you to remember? Answer with the codeword only.", "live-2")
	var sawRebuilding bool
	seedDeadline := time.After(4 * time.Minute)
	seededIdle := false
	for !seededIdle {
		select {
		case ev := <-stream.events:
			if ev.data.State != nil {
				if ev.data.State.StartingStep == server.SupervisorStartingStepRebuilding {
					sawRebuilding = true
				}
				seededIdle = ev.data.State.Lifecycle == server.SupervisorLifecycleIdle
			}
		case <-seedDeadline:
			t.Fatalf("seeded generation timed out: %+v", h.state())
		}
	}
	if !sawRebuilding || !strings.Contains(lastAssistantText(h.transcript("").Items), codeword) {
		t.Fatalf("rebuilding=%t, seeded reply=%q", sawRebuilding, lastAssistantText(h.transcript("").Items))
	}
	if got := countAssistantRecords(h.transcript("?limit=500").Items); got != beforeSeed+1 {
		t.Fatalf("history seed produced a model reply: assistant records %d -> %d", beforeSeed, got)
	}
	if st := h.coord.State(); st.NativeSessionID != "" {
		t.Fatalf("OpenCode adopted an ephemeral ACP session id after seed: %q", st.NativeSessionID)
	}
	h.send("Run `printf "+codeword+"` yourself in the root session, and quote its output.", "live-3")
	deadline = time.Now().Add(4 * time.Minute)
	var rootApproved bool
	for !rootApproved {
		st := h.state()
		if st.Lifecycle == server.SupervisorLifecycleFailed || time.Now().After(deadline) {
			t.Fatalf("root permission did not arrive: state %+v", st)
		}
		for _, req := range st.PendingRequests {
			if req.ToolName == "Bash" && req.Origin == server.RequestOriginRoot {
				h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
					"request_id": req.RequestID, "session_id": st.SessionID, "decision": "allow_once",
				}, http.StatusOK, nil)
				rootApproved = true
				break
			}
		}
		if !rootApproved {
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitOpenCodeLiveState(t, h, "root command", func(st server.SupervisorState) bool { return st.Lifecycle == server.SupervisorLifecycleIdle })
	if got := lastAssistantText(h.transcript("").Items); !strings.Contains(got, codeword) {
		t.Fatalf("root command output absent: %q", got)
	}
}

func waitOpenCodeLiveState(t *testing.T, h *supervisorHarness, what string, ready func(server.SupervisorState) bool) server.SupervisorState {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		st := h.state()
		if ready(st) {
			return st
		}
		if st.Lifecycle == server.SupervisorLifecycleFailed || time.Now().After(deadline) {
			t.Fatalf("%s did not complete: %+v", what, st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func countAssistantRecords(items []server.SupervisorRecord) int {
	var n int
	for _, rec := range items {
		if rec.Kind == server.SupervisorRecordKindAssistant {
			n++
		}
	}
	return n
}
