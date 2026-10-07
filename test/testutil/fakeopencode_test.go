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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// fakeACP drives the fake OpenCode process directly over ACP and HTTP.
type fakeACP struct {
	*fakeRPC
	baseURL  string
	password string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startFakeACP(t *testing.T, script testutil.FakeOpenCodeScript) (*fakeACP, string) {
	t.Helper()
	path := testutil.WriteFakeOpenCodeScript(t, script)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	const password = "fake-server-password-0123456789"
	cmd := exec.Command(exe, "acp", "--port", fmt.Sprint(port), "--hostname", "127.0.0.1")
	cmd.Env = append(os.Environ(), testutil.FakeOpenCodeEnv+"=1", testutil.FakeOpenCodeScriptEnv+"="+path, "OPENCODE_SERVER_PASSWORD="+password)
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
		close(r.lines)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	a := &fakeACP{fakeRPC: r, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), password: password}
	a.waitHealthy()
	return a, path
}

func (a *fakeACP) http(method, path string, body any, auth bool) (int, []byte) {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, a.baseURL+path, rdr)
	if auth {
		req.SetBasicAuth(testutil.FakeOpenCodeServerUser, a.password)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (a *fakeACP) waitHealthy() {
	a.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, a.baseURL+"/global/health", nil)
		req.SetBasicAuth(testutil.FakeOpenCodeServerUser, a.password)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatal("fake OpenCode HTTP server never became healthy")
}

type fakeEvent struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// events opens the authenticated event stream.
func (a *fakeACP) events() <-chan fakeEvent {
	a.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a.t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/event", nil)
	req.SetBasicAuth(testutil.FakeOpenCodeServerUser, a.password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		a.t.Fatalf("GET /event: %v %v", resp, err)
	}
	ch := make(chan fakeEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			var ev fakeEvent
			if ok && json.Unmarshal([]byte(data), &ev) == nil {
				ch <- ev
			}
		}
	}()
	return ch
}

func nextEvent(t *testing.T, ch <-chan fakeEvent, kind string) fakeEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event stream ended waiting for %s", kind)
			}
			if ev.Type == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("timed out waiting for event %s", kind)
		}
	}
}

func (a *fakeACP) openSession() string {
	a.t.Helper()
	if resp := a.response(a.call("initialize", map[string]any{"protocolVersion": 1})); !strings.Contains(string(resp["result"]), `"loadSession":true`) {
		a.t.Fatalf("initialize = %v", resp)
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	resp := a.response(a.call("session/new", map[string]any{"cwd": a.t.TempDir(), "mcpServers": []any{}}))
	if err := json.Unmarshal(resp["result"], &res); err != nil || !strings.HasPrefix(res.SessionID, "ses_") {
		a.t.Fatalf("session/new = %v", resp)
	}
	return res.SessionID
}

func (a *fakeACP) prompt(sessionID, text string) int {
	return a.call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []map[string]string{{"type": "text", "text": text}}})
}

// chunkText waits for an agent_message_chunk carrying want.
func (a *fakeACP) chunkText(want string) {
	a.t.Helper()
	a.next("chunk "+want, func(l map[string]json.RawMessage) bool {
		return method("session/update")(l) && strings.Contains(string(l["params"]), want)
	})
}

func (a *fakeACP) stopReason(id int) string {
	a.t.Helper()
	var res struct {
		StopReason string `json:"stopReason"`
	}
	_ = json.Unmarshal(a.response(id)["result"], &res)
	return res.StopReason
}

func TestFakeOpenCodeHTTPSurface(t *testing.T) {
	t.Run("auth", func(t *testing.T) {
		a, path := startFakeACP(t, testutil.FakeOpenCodeScript{})
		if code, _ := a.http(http.MethodGet, "/global/health", nil, false); code != http.StatusUnauthorized {
			t.Fatalf("health without password = %d", code)
		}
		if code, body := a.http(http.MethodGet, "/health", nil, true); code != http.StatusOK || !strings.Contains(string(body), `"healthy":true`) {
			t.Fatalf("health with password = %d %s", code, body)
		}
		if code, _ := a.http(http.MethodGet, "/permission", nil, false); code != http.StatusUnauthorized {
			t.Fatalf("permission list without password = %d", code)
		}
		var sawMissing, sawOK bool
		for _, r := range testutil.FakeOpenCodeHTTP(t, path) {
			sawMissing = sawMissing || (r.Auth == "missing" && r.Status == http.StatusUnauthorized)
			sawOK = sawOK || (r.Auth == "ok" && r.Path == "/health" && r.Status == http.StatusOK)
		}
		if !sawMissing || !sawOK {
			t.Fatalf("recorded HTTP = %+v", testutil.FakeOpenCodeHTTP(t, path))
		}
	})

	t.Run("child permission round trip", func(t *testing.T) {
		a, path := startFakeACP(t, testutil.FakeOpenCodeScript{})
		events := a.events()
		nextEvent(t, events, "server.connected")
		root := a.openSession()
		promptID := a.prompt(root, "delegate "+testutil.FakeOpenCodeChildPermBash)

		asked := nextEvent(t, events, "permission.asked")
		var props struct {
			ID         string            `json:"id"`
			SessionID  string            `json:"sessionID"`
			Permission string            `json:"permission"`
			Metadata   map[string]string `json:"metadata"`
		}
		if err := json.Unmarshal(asked.Properties, &props); err != nil {
			t.Fatal(err)
		}
		if props.SessionID == root || props.Permission != "bash" || props.Metadata["command"] != testutil.FakeOpenCodeBashCommand {
			t.Fatalf("child permission = %s (root %s)", asked.Properties, root)
		}
		if _, body := a.http(http.MethodGet, "/permission", nil, true); !strings.Contains(string(body), props.ID) {
			t.Fatalf("GET /permission = %s, want %s", body, props.ID)
		}
		if code, _ := a.http(http.MethodPost, "/permission/"+props.ID+"/reply", map[string]string{"reply": "once"}, true); code != http.StatusOK {
			t.Fatalf("reply = %d", code)
		}
		if replied := nextEvent(t, events, "permission.replied"); !strings.Contains(string(replied.Properties), props.ID) {
			t.Fatalf("permission.replied = %s", replied.Properties)
		}
		a.chunkText("Child allowed")
		if got := a.stopReason(promptID); got != "end_turn" {
			t.Fatalf("stopReason = %q", got)
		}
		for _, line := range testutil.FakeOpenCodeACP(t, path) {
			if line.Method == "session/request_permission" {
				t.Fatalf("child permission leaked onto ACP: %+v", line)
			}
		}
		sessions := testutil.FakeOpenCodeSessions(t, path)
		if len(sessions) != 2 || sessions[1].Kind != "child" || sessions[1].ParentID != root {
			t.Fatalf("sessions = %+v", sessions)
		}
	})

	t.Run("child question round trip", func(t *testing.T) {
		a, _ := startFakeACP(t, testutil.FakeOpenCodeScript{})
		events := a.events()
		root := a.openSession()
		promptID := a.prompt(root, "delegate "+testutil.FakeOpenCodeChildAsk)
		asked := nextEvent(t, events, "question.asked")
		var props struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Questions []struct {
				Question string `json:"question"`
				Multiple bool   `json:"multiple"`
				Custom   bool   `json:"custom"`
				Options  []struct {
					Label string `json:"label"`
				} `json:"options"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(asked.Properties, &props); err != nil {
			t.Fatal(err)
		}
		if props.SessionID == root || len(props.Questions) != 1 || props.Questions[0].Question != testutil.FakeOpenCodeQuestion || len(props.Questions[0].Options) != 2 {
			t.Fatalf("child question = %s", asked.Properties)
		}
		if _, body := a.http(http.MethodGet, "/question", nil, true); !strings.Contains(string(body), props.ID) {
			t.Fatalf("GET /question = %s", body)
		}
		if code, _ := a.http(http.MethodPost, "/question/"+props.ID+"/reply", map[string]any{"answers": [][]string{{"dev"}}}, true); code != http.StatusOK {
			t.Fatalf("reply = %d", code)
		}
		nextEvent(t, events, "question.replied")
		a.chunkText("Child chose dev")
		if got := a.stopReason(promptID); got != "end_turn" {
			t.Fatalf("stopReason = %q", got)
		}
	})

	t.Run("question reject", func(t *testing.T) {
		a, _ := startFakeACP(t, testutil.FakeOpenCodeScript{})
		events := a.events()
		root := a.openSession()
		a.prompt(root, testutil.FakeOpenCodeAsk)
		asked := nextEvent(t, events, "question.asked")
		var props struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
		}
		_ = json.Unmarshal(asked.Properties, &props)
		if props.SessionID != root {
			t.Fatalf("root question session = %s, want %s", props.SessionID, root)
		}
		if code, _ := a.http(http.MethodPost, "/question/"+props.ID+"/reject", nil, true); code != http.StatusOK {
			t.Fatalf("reject = %d", code)
		}
		nextEvent(t, events, "question.rejected")
		a.chunkText("Question rejected")
	})

	t.Run("seed and set model", func(t *testing.T) {
		a, path := startFakeACP(t, testutil.FakeOpenCodeScript{})
		root := a.openSession()
		if resp := a.response(a.call("session/set_model", map[string]string{"sessionId": root, "modelId": "fake/other"})); resp["error"] != nil {
			t.Fatalf("set_model = %v", resp)
		}
		seed := "Prior conversation\nUser: what is the codeword?\nAssistant: it is ALPHA\n"
		if code, body := a.http(http.MethodPost, "/session/"+root+"/prompt", map[string]any{"noReply": true, "parts": []map[string]string{{"type": "text", "text": seed}}}, true); code != http.StatusOK {
			t.Fatalf("seed post = %d %s", code, body)
		}
		seeds := testutil.FakeOpenCodeSeeds(t, path)
		if len(seeds) != 1 || seeds[0].Text != seed || seeds[0].SessionID != root || !seeds[0].NoReply {
			t.Fatalf("seeds = %+v", seeds)
		}
		if n := testutil.FakeOpenCodeModelReplies(t, path); n != 0 {
			t.Fatalf("model replies after seed = %d", n)
		}
		a.prompt(root, "what was it?")
		a.chunkText("Seeded with 2 prior messages: what is the codeword?")
		models := testutil.FakeOpenCodeSetModels(t, path)
		if len(models) != 1 || models[0].ModelID != "fake/other" || models[0].SessionID != root {
			t.Fatalf("set models = %+v", models)
		}
		a.http(http.MethodPost, "/session/"+root+"/prompt", map[string]any{"parts": []map[string]string{{"type": "text", "text": "reply please"}}}, true)
		if n := testutil.FakeOpenCodeModelReplies(t, path); n != 1 {
			t.Fatalf("model replies after a replying prompt = %d", n)
		}
	})

	t.Run("cancel, stubborn and load", func(t *testing.T) {
		a, path := startFakeACP(t, testutil.FakeOpenCodeScript{})
		root := a.openSession()
		held := a.prompt(root, testutil.FakeOpenCodeHold)
		a.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": root}})
		if got := a.stopReason(held); got != "cancelled" {
			t.Fatalf("held prompt stopReason = %q, want cancelled", got)
		}
		stubborn := a.prompt(root, testutil.FakeOpenCodeStubborn)
		a.write(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": root}})
		select {
		case line := <-a.lines:
			if string(line["id"]) == fmt.Sprint(stubborn) {
				t.Fatalf("stubborn prompt answered a cancel: %v", line)
			}
		case <-time.After(200 * time.Millisecond):
		}
		if resp := a.response(a.call("session/load", map[string]any{"sessionId": "ses_prior", "cwd": t.TempDir(), "mcpServers": []any{}})); resp["error"] != nil {
			t.Fatalf("session/load = %v", resp)
		}
		sessions := testutil.FakeOpenCodeSessions(t, path)
		if last := sessions[len(sessions)-1]; last.ID != "ses_prior" || last.Kind != "loaded" {
			t.Fatalf("sessions = %+v", sessions)
		}
	})

	t.Run("reject password and no http", func(t *testing.T) {
		a, _ := startFakeACPWithoutHealth(t, testutil.FakeOpenCodeScript{RejectPassword: true})
		if code, _ := a.http(http.MethodGet, "/global/health", nil, true); code != http.StatusUnauthorized {
			t.Fatalf("health with rejected password = %d", code)
		}
		b, _ := startFakeACPWithoutHealth(t, testutil.FakeOpenCodeScript{NoHTTP: true})
		b.openSession()
		if _, err := http.Get(b.baseURL + "/global/health"); err == nil {
			t.Fatal("HTTP server answered with no_http set")
		}
	})
}

// startFakeACPWithoutHealth is startFakeACP for scripts whose HTTP server
// never reports healthy; it waits for the ACP side instead.
func startFakeACPWithoutHealth(t *testing.T, script testutil.FakeOpenCodeScript) (*fakeACP, string) {
	t.Helper()
	path := testutil.WriteFakeOpenCodeScript(t, script)
	exe, _ := os.Executable()
	port := freePort(t)
	cmd := exec.Command(exe, "acp", "--port", fmt.Sprint(port), "--hostname", "127.0.0.1")
	cmd.Env = append(os.Environ(), testutil.FakeOpenCodeEnv+"=1", testutil.FakeOpenCodeScriptEnv+"="+path, "OPENCODE_SERVER_PASSWORD=pw-0123456789")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &fakeRPC{t: t, stdin: stdin, lines: make(chan map[string]json.RawMessage, 256)}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var line map[string]json.RawMessage
			if json.Unmarshal(scanner.Bytes(), &line) == nil {
				r.lines <- line
			}
		}
		close(r.lines)
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	a := &fakeACP{fakeRPC: r, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), password: "pw-0123456789"}
	// The ACP side answering proves the process is up and past HTTP startup.
	a.response(a.call("initialize", map[string]any{"protocolVersion": 1}))
	return a, path
}

// openCodeSession is one real-adapter session against the fake.
type openCodeSession struct {
	sess    ports.SessionHandle
	proto   llm.Protocol
	obs     *codexObserver
	script  string
	args    []string
	env     []string
	logPath string
}

type openCodeSessionOpts struct {
	script      testutil.FakeOpenCodeScript
	interactive bool
	timeout     time.Duration
}

// startOpenCodeSession launches the real OpenCode adapter against the fake
// through the real session manager.
func startOpenCodeSession(t *testing.T, prompt string, o openCodeSessionOpts) (*openCodeSession, error) {
	t.Helper()
	script := testutil.WriteFakeOpenCodeScript(t, o.script)
	prov := testutil.NewFakeOpenCodeProvider(t, script)
	work := t.TempDir()
	cmd, env, err := prov.BuildCommand(llm.CommandBuildOpts{Model: testutil.FakeOpenCodeModel, WorkDir: work, Interactive: o.interactive})
	if err != nil {
		t.Fatal(err)
	}
	obs := &codexObserver{}
	mgr := session.NewManager(nil)
	t.Cleanup(mgr.Shutdown)
	kind, mode := ports.KindPhase, ports.TurnModeOneShot
	if o.interactive {
		kind, mode = ports.KindSupervisor, ports.TurnModeInteractive
	}
	proto := prov.NewProtocol(llm.ProtocolOpts{
		Model: testutil.FakeOpenCodeModel, WorkDir: work, InitialPrompt: prompt, Interactive: o.interactive,
		WritableRoots: []string{work}, LaunchArgs: cmd, LaunchEnv: env,
	})
	logPath := filepath.Join(t.TempDir(), "session.log")
	sess, err := mgr.StartSession("opencode-fake", "f", feature.PhaseResearch, cmd, work, env, &session.SessionOpts{
		Protocol:              proto,
		LogPath:               logPath,
		PermHandler:           &permission.SupervisorHandler{},
		TurnMode:              mode,
		Kind:                  kind,
		Observer:              obs,
		CodexHandshakeTimeout: o.timeout,
	})
	return &openCodeSession{sess: sess, proto: proto, obs: obs, script: script, args: cmd, env: env, logPath: logPath}, err
}

func mustStartOpenCodeSession(t *testing.T, prompt string, o openCodeSessionOpts) *openCodeSession {
	t.Helper()
	s, err := startOpenCodeSession(t, prompt, o)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	return s
}

func isControl(m llm.SDKMessage) bool { return m.ControlRequest != nil }

func assistantText(m llm.SDKMessage) string {
	if m.Assistant == nil || m.Subtype == "partial" || len(m.Assistant.Message.Content) == 0 {
		return ""
	}
	return m.Assistant.Message.Content[0].Text
}

func TestFakeOpenCodeDrivesRealAdapter(t *testing.T) {
	t.Run("plain turn", func(t *testing.T) {
		s := mustStartOpenCodeSession(t, "hello", openCodeSessionOpts{interactive: true})
		res := s.obs.wait(t, "result", isResult)
		if res.Result.IsError {
			t.Fatalf("turn result = %+v", res.Result)
		}
		s.obs.wait(t, "reply", func(m llm.SDKMessage) bool { return assistantText(m) == "Hello from turn 1" })
		if got := strings.Join(testutil.FakeOpenCodeMethods(t, s.script), ","); got != "initialize,session/new,session/prompt" {
			t.Fatalf("recorded methods = %s", got)
		}
		if n := testutil.FakeOpenCodeInvocations(t, s.script); n != 1 {
			t.Fatalf("invocations = %d", n)
		}
	})

	for _, tc := range []struct {
		marker, tool, inputHas, option, reply string
		allow                                 bool
	}{
		{testutil.FakeOpenCodePermBash, "Bash", testutil.FakeOpenCodeBashCommand, `"optionId":"once"`, "Command allowed", true},
		{testutil.FakeOpenCodePermEdit, "Write", testutil.FakeOpenCodeEditPath, `"optionId":"reject"`, "Edit denied", false},
		{testutil.FakeOpenCodePermTask, "Agent", "general", `"optionId":"once"`, "Task allowed", true},
	} {
		t.Run(tc.tool+" root request", func(t *testing.T) {
			s := mustStartOpenCodeSession(t, "please "+tc.marker, openCodeSessionOpts{interactive: true})
			ctrl := s.obs.wait(t, "control request", isControl)
			req := ctrl.ControlRequest.Request
			if req.ToolName != tc.tool || !strings.Contains(string(req.Input), tc.inputHas) {
				t.Fatalf("control request = %s %s, want %s with %s", req.ToolName, req.Input, tc.tool, tc.inputHas)
			}
			if ctrl.Origin.Kind != llm.EventOriginRoot {
				t.Fatalf("origin = %+v, want root", ctrl.Origin)
			}
			if err := s.sess.RespondToControl(ctrl.ControlRequest.RequestID, tc.allow, ""); err != nil {
				t.Fatal(err)
			}
			s.obs.wait(t, "verdict reply", func(m llm.SDKMessage) bool { return assistantText(m) == tc.reply })
			var answered bool
			for _, line := range testutil.FakeOpenCodeACP(t, s.script) {
				if line.Dir == "in" && line.Method == "" && strings.Contains(string(line.Result), tc.option) {
					answered = true
				}
			}
			if !answered {
				t.Fatalf("fake never recorded %s", tc.option)
			}
		})
	}
}

// TestFakeOpenCodeWritesOnlyBesideScript runs a turn with HOME pointed at an
// empty directory and checks nothing appeared there.
func TestFakeOpenCodeWritesOnlyBesideScript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := mustStartOpenCodeSession(t, "hello", openCodeSessionOpts{interactive: true})
	s.obs.wait(t, "result", isResult)
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fake wrote into HOME: %v", entries)
	}
	files, _ := os.ReadDir(filepath.Dir(s.script))
	if len(files) < 3 {
		t.Fatalf("recordings beside the script = %v", files)
	}
}
