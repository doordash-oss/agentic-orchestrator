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

package testutil_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunFakeCodexIfRequested()
	testutil.RunFakeOpenCodeIfRequested()
	os.Exit(m.Run())
}

// fakeRPC drives the fake app-server process directly over JSON-RPC.
type fakeRPC struct {
	t      *testing.T
	stdin  io.WriteCloser
	lines  chan map[string]json.RawMessage
	nextID int
}

func startFakeRPC(t *testing.T, script testutil.FakeCodexScript) (*fakeRPC, string) {
	t.Helper()
	path := testutil.WriteFakeCodexScript(t, script)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "app-server")
	cmd.Env = append(os.Environ(), testutil.FakeCodexEnv+"=1", testutil.FakeCodexScriptEnv+"="+path)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &fakeRPC{t: t, stdin: stdin, lines: make(chan map[string]json.RawMessage, 256)}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var line map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &line) == nil {
				r.lines <- line
			}
		}
		if err := scanner.Err(); err != nil {
			t.Logf("read fake stdout: %v", err)
		}
		close(r.lines)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	return r, path
}

func (r *fakeRPC) write(v any) {
	r.t.Helper()
	raw, _ := json.Marshal(v)
	if _, err := r.stdin.Write(append(raw, '\n')); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fakeRPC) call(method string, params any) int {
	r.nextID++
	r.write(map[string]any{"jsonrpc": "2.0", "id": r.nextID, "method": method, "params": params})
	return r.nextID
}

// next returns the next line matching pred, failing after a timeout.
func (r *fakeRPC) next(what string, pred func(map[string]json.RawMessage) bool) map[string]json.RawMessage {
	r.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-r.lines:
			if !ok {
				r.t.Fatalf("fake exited waiting for %s", what)
			}
			if pred(line) {
				return line
			}
		case <-timeout:
			r.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (r *fakeRPC) response(id int) map[string]json.RawMessage {
	r.t.Helper()
	return r.next(fmt.Sprintf("response %d", id), func(l map[string]json.RawMessage) bool {
		return string(l["id"]) == fmt.Sprint(id) && l["method"] == nil
	})
}

func method(name string) func(map[string]json.RawMessage) bool {
	return func(l map[string]json.RawMessage) bool { return string(l["method"]) == `"`+name+`"` }
}

func (r *fakeRPC) handshake() {
	r.t.Helper()
	id := r.call("initialize", map[string]any{})
	select {
	case line := <-r.lines:
		r.t.Fatalf("initialize answered before initialized: %v", line)
	case <-time.After(100 * time.Millisecond):
	}
	r.write(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	if resp := r.response(id); resp["result"] == nil {
		r.t.Fatalf("initialize response = %v", resp)
	}
}

func threadIDOf(t *testing.T, resp map[string]json.RawMessage) string {
	t.Helper()
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resp["result"], &result); err != nil || result.Thread.ID == "" {
		t.Fatalf("thread response = %v", resp)
	}
	return result.Thread.ID
}

func TestFakeCodexJSONRPC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)

	t.Run("compaction rollout and usage reset", func(t *testing.T) {
		r, path := startFakeRPC(t, testutil.FakeCodexScript{})
		r.handshake()
		thread := threadIDOf(t, r.response(r.call("thread/start", map[string]any{"model": "gpt-fake", "cwd": home})))
		lastTokens := func(marker string) int {
			r.call("turn/start", map[string]any{"threadId": thread, "input": []map[string]string{{"type": "text", "text": marker}}})
			usage := r.next("token usage", method("thread/tokenUsage/updated"))
			var body struct {
				TokenUsage struct {
					Last struct {
						TotalTokens int `json:"totalTokens"`
					} `json:"last"`
				} `json:"tokenUsage"`
			}
			if err := json.Unmarshal(usage["params"], &body); err != nil {
				t.Fatal(err)
			}
			r.next("turn completed", method("turn/completed"))
			return body.TokenUsage.Last.TotalTokens
		}
		if high := lastTokens(testutil.FakeCodexUsageHigh); high < 170000 {
			t.Fatalf("high usage = %d", high)
		}
		if low := lastTokens(testutil.FakeCodexCompact); low >= 170000 {
			t.Fatalf("post-compaction usage = %d", low)
		}
		requests := testutil.FakeCodexRequests(t, path)
		if len(requests) == 0 || requests[len(requests)-1].Method != "turn/start" {
			t.Fatalf("requests = %+v", requests)
		}
		var rollout string
		_ = filepath.WalkDir(filepath.Join(home, "sessions"), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, "-"+thread+".jsonl") {
				rollout = p
			}
			return nil
		})
		if rollout == "" {
			t.Fatal("missing rollout")
		}
		data, err := os.ReadFile(rollout)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"type":"compacted"`) || !strings.Contains(string(data), `"encrypted_content":"opaque-fake-compaction"`) || !strings.Contains(string(data), `"window_id":"window-2"`) {
			t.Fatalf("rollout = %s", data)
		}
	})

	t.Run("start, settings, usage, unknown and turn telemetry", func(t *testing.T) {
		r, path := startFakeRPC(t, testutil.FakeCodexScript{ApprovalPolicy: "on-request"})
		r.handshake()
		thread := threadIDOf(t, r.response(r.call("thread/start", map[string]any{"model": "gpt-fake", "cwd": home})))
		if got := testutil.FakeCodexThreads(t, path); len(got) != 1 || got[0] != thread {
			t.Fatalf("minted threads = %v, want [%s]", got, thread)
		}
		if resp := r.response(r.call("thread/settings/update", map[string]any{"threadId": thread, "model": "gpt-fake-next", "effort": "high"})); resp["error"] != nil {
			t.Fatalf("settings update = %v", resp)
		}
		requests := testutil.FakeCodexRequests(t, path)
		var settings struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		}
		for _, request := range requests {
			if request.Method == "thread/settings/update" {
				_ = json.Unmarshal(request.Params, &settings)
			}
		}
		if settings.Model != "gpt-fake-next" || settings.Effort != "high" {
			t.Fatalf("recorded settings = %+v", settings)
		}
		if resp := r.response(r.call("account/usage/read", map[string]any{})); string(resp["result"]) != "{}" {
			t.Fatalf("usage read = %v", resp)
		}
		if resp := r.response(r.call("no/such", map[string]any{})); !strings.Contains(string(resp["error"]), "-32601") {
			t.Fatalf("unknown request = %v", resp)
		}
		r.call("turn/start", map[string]any{"threadId": thread, "input": []map[string]string{{"type": "text", "text": "hi " + testutil.FakeCodexCompact}}})
		compaction := r.next("compaction item", func(l map[string]json.RawMessage) bool {
			return method("item/completed")(l) && strings.Contains(string(l["params"]), `"contextCompaction"`)
		})
		if compaction == nil {
			t.Fatal("no compaction item")
		}
		r.next("token usage", method("thread/tokenUsage/updated"))
		r.next("turn completed", method("turn/completed"))
		methods := testutil.FakeCodexMethods(t, path)
		if strings.Join(methods, ",") != "initialize,initialized,thread/start,thread/settings/update,account/usage/read,no/such,turn/start" {
			t.Fatalf("recorded methods = %v", methods)
		}
	})

	t.Run("settings update rejected", func(t *testing.T) {
		r, _ := startFakeRPC(t, testutil.FakeCodexScript{RejectSettingsUpdate: true})
		r.handshake()
		if resp := r.response(r.call("thread/settings/update", map[string]any{})); resp["error"] == nil {
			t.Fatalf("rejected settings update = %v", resp)
		}
	})

	t.Run("settings update delayed", func(t *testing.T) {
		r, _ := startFakeRPC(t, testutil.FakeCodexScript{DelaySettingsUpdateMS: 80})
		r.handshake()
		thread := threadIDOf(t, r.response(r.call("thread/start", map[string]any{"model": "gpt-fake", "cwd": home})))
		started := time.Now()
		resp := r.response(r.call("thread/settings/update", map[string]any{"threadId": thread, "model": "gpt-fake-next", "effort": "high"}))
		if resp["error"] != nil || time.Since(started) < 70*time.Millisecond {
			t.Fatalf("delayed settings response = %v after %s", resp, time.Since(started))
		}
	})

	t.Run("resume reads the rollout", func(t *testing.T) {
		const id = "0199aaaa-0000-7000-8000-000000000001"
		dir := filepath.Join(home, "sessions", "2026", "01", "02")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rollout := strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"` + id + `"}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first prompt"}]}}`,
			`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}`,
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}`,
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "rollout-2026-01-02T03-04-05-"+id+".jsonl"), []byte(rollout), 0o600); err != nil {
			t.Fatal(err)
		}
		r, path := startFakeRPC(t, testutil.FakeCodexScript{})
		r.handshake()
		if got := threadIDOf(t, r.response(r.call("thread/resume", map[string]any{"threadId": id}))); got != id {
			t.Fatalf("resumed thread = %s", got)
		}
		r.call("turn/start", map[string]any{"threadId": id, "input": []map[string]string{{"type": "text", "text": "what was it?"}}})
		reply := r.next("resumed reply", func(l map[string]json.RawMessage) bool {
			return method("item/completed")(l) && strings.Contains(string(l["params"]), "Resumed with 2 prior messages: first prompt")
		})
		if reply == nil {
			t.Fatal("no resumed reply")
		}
		resumes := testutil.FakeCodexResumes(t, path)
		if len(resumes) != 1 || !resumes[0].Found || resumes[0].UserItems != 2 || resumes[0].ThreadID != id {
			t.Fatalf("resumes = %+v", resumes)
		}
		r2, path2 := startFakeRPC(t, testutil.FakeCodexScript{})
		r2.handshake()
		r2.response(r2.call("thread/resume", map[string]any{"threadId": "0199aaaa-0000-7000-8000-00000000ffff"}))
		if got := testutil.FakeCodexResumes(t, path2); len(got) != 1 || got[0].Found {
			t.Fatalf("resume of a missing rollout = %+v", got)
		}
	})

	for _, tc := range []testutil.FakeCodexRPCError{
		{Code: -32603, Message: "failed to load thread from thread store"},
		{Code: -32600, Message: "invalid thread id"},
	} {
		t.Run(fmt.Sprintf("resume error %d", tc.Code), func(t *testing.T) {
			r, _ := startFakeRPC(t, testutil.FakeCodexScript{ResumeError: &tc})
			r.handshake()
			resp := r.response(r.call("thread/resume", map[string]any{"threadId": "x"}))
			var rpcErr testutil.FakeCodexRPCError
			if err := json.Unmarshal(resp["error"], &rpcErr); err != nil || rpcErr != tc {
				t.Fatalf("resume error = %s, want %+v", resp["error"], tc)
			}
		})
	}
}

// codexObserver collects one session's messages and answers.
type codexObserver struct {
	mu   sync.Mutex
	msgs []llm.SDKMessage
}

func (o *codexObserver) ObserveSessionMessage(_ string, msg llm.SDKMessage) {
	o.mu.Lock()
	o.msgs = append(o.msgs, msg)
	o.mu.Unlock()
}

func (o *codexObserver) ObserveControlAnswer(string, ports.ControlAnswer) {}

func (o *codexObserver) wait(t *testing.T, what string, pred func(llm.SDKMessage) bool) llm.SDKMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	seen := 0
	for time.Now().Before(deadline) {
		o.mu.Lock()
		msgs := append([]llm.SDKMessage(nil), o.msgs[seen:]...)
		o.mu.Unlock()
		for _, m := range msgs {
			seen++
			if pred(m) {
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return llm.SDKMessage{}
}

func isResult(m llm.SDKMessage) bool { return m.Result != nil }

// startCodexSession launches the real Codex adapter against the fake
// through the real session manager.
func startCodexSession(t *testing.T, prompt string) (ports.SessionHandle, *codexObserver, string) {
	t.Helper()
	t.Setenv("CODEX_HOME", t.TempDir())
	script := testutil.WriteFakeCodexScript(t, testutil.FakeCodexScript{})
	prov := testutil.NewFakeCodexProvider(t, script)
	work := t.TempDir()
	cmd, env, err := prov.BuildCommand(llm.CommandBuildOpts{Model: testutil.FakeCodexModel})
	if err != nil {
		t.Fatal(err)
	}
	obs := &codexObserver{}
	mgr := session.NewManager(nil)
	t.Cleanup(mgr.Shutdown)
	sess, err := mgr.StartSession("codex-fake", "f", feature.PhaseResearch, cmd, work, env, &session.SessionOpts{
		Protocol: prov.NewProtocol(llm.ProtocolOpts{
			Model: testutil.FakeCodexModel, WorkDir: work, InitialPrompt: prompt, Interactive: true, WritableRoots: []string{work},
		}),
		PermHandler: &permission.SupervisorHandler{},
		TurnMode:    ports.TurnModeInteractive,
		Kind:        ports.KindSupervisor,
		Observer:    obs,
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return sess, obs, script
}

func TestFakeCodexDrivesRealAdapter(t *testing.T) {
	t.Run("plain turn", func(t *testing.T) {
		_, obs, script := startCodexSession(t, "hello")
		res := obs.wait(t, "result", isResult)
		if res.Result.IsError {
			t.Fatalf("turn result = %+v", res.Result)
		}
		// The adapter's post-turn billing lookup may follow.
		if got := testutil.FakeCodexMethods(t, script); !strings.HasPrefix(strings.Join(got, ","), "initialize,initialized,thread/start,turn/start") {
			t.Fatalf("recorded methods = %v", got)
		}
	})

	for _, tc := range []struct {
		marker, tool, want string
		allow              bool
	}{
		{testutil.FakeCodexPermBash, "Bash", `"decision":"accept"`, true},
		{testutil.FakeCodexPermWrite, "Write", `"decision":"decline"`, false},
	} {
		t.Run(tc.tool+" approval", func(t *testing.T) {
			sess, obs, script := startCodexSession(t, "please "+tc.marker)
			ctrl := obs.wait(t, "control request", func(m llm.SDKMessage) bool { return m.ControlRequest != nil })
			req := ctrl.ControlRequest.Request
			if req.ToolName != tc.tool {
				t.Fatalf("control request tool = %s, want %s", req.ToolName, tc.tool)
			}
			if tc.tool == "Bash" && !strings.Contains(string(req.Input), testutil.FakeCodexBashCommand) {
				t.Fatalf("Bash input = %s", req.Input)
			}
			if err := sess.RespondToControl(ctrl.ControlRequest.RequestID, tc.allow, ""); err != nil {
				t.Fatal(err)
			}
			obs.wait(t, "result", isResult)
			var answered bool
			for _, r := range testutil.FakeCodexRequests(t, script) {
				if r.Method == "" && strings.Contains(string(r.Result), tc.want) {
					answered = true
				}
			}
			if !answered {
				t.Fatalf("fake never recorded %s", tc.want)
			}
		})
	}

	t.Run("user input", func(t *testing.T) {
		sess, obs, script := startCodexSession(t, "decide "+testutil.FakeCodexAsk)
		ctrl := obs.wait(t, "question", func(m llm.SDKMessage) bool { return m.ControlRequest != nil })
		if ctrl.ControlRequest.Request.ToolName != "AskUserQuestion" {
			t.Fatalf("question tool = %s", ctrl.ControlRequest.Request.ToolName)
		}
		if err := sess.RespondToAskUser(ctrl.ControlRequest.RequestID, ctrl.ControlRequest.Request.Input, map[string]string{"Which branch?": "dev"}, nil); err != nil {
			t.Fatal(err)
		}
		obs.wait(t, "echoed answer", func(m llm.SDKMessage) bool {
			return m.Assistant != nil && m.Subtype != "partial" && len(m.Assistant.Message.Content) > 0 && m.Assistant.Message.Content[0].Text == "You chose dev"
		})
		var recorded bool
		for _, r := range testutil.FakeCodexRequests(t, script) {
			if r.Method == "" && strings.Contains(string(r.Result), `"answers":["dev"]`) {
				recorded = true
			}
		}
		if !recorded {
			t.Fatal("fake never recorded the answer payload")
		}
	})

	t.Run("telemetry and compaction", func(t *testing.T) {
		_, obs, _ := startCodexSession(t, "compact "+testutil.FakeCodexCompact)
		if res := obs.wait(t, "result", isResult); res.Result.IsError {
			t.Fatalf("turn result = %+v", res.Result)
		}
	})
}

// TestFakeCodexWritesOnlyBesideScriptAndCodexHome runs a turn with HOME
// pointed at an empty directory and checks nothing appeared there.
func TestFakeCodexWritesOnlyBesideScriptAndCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, obs, _ := startCodexSession(t, "hello")
	obs.wait(t, "result", isResult)
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fake wrote into HOME: %v", entries)
	}
}
