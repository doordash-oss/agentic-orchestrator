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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/claudeconfig"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/claudesession"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/codexsession"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor/opencodesession"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

const supervisorTestToken = "supervisor-test-token"

// supervisorAnswerTarget serves the existing permission and ask-user answer
// routes by answering the session directly, the way the CLI mutation
// target does. Every other mutation is unused by these journeys.
type supervisorAnswerTarget struct {
	server.MutationTarget
	sessions ports.SessionManager
}

func (t supervisorAnswerTarget) AnswerPermission(req server.PermissionAnswerRequest) (server.PermissionAnswerResponse, error) {
	sess := t.sessions.GetSession(req.SessionID)
	if sess == nil {
		return server.PermissionAnswerResponse{}, fmt.Errorf("session %s not found", req.SessionID)
	}
	if err := sess.RespondToControl(req.RequestID, req.Decision != "deny", ""); err != nil {
		return server.PermissionAnswerResponse{}, err
	}
	return server.PermissionAnswerResponse{SessionID: sess.ID(), RequestID: req.RequestID, Decision: req.Decision, Result: "answered"}, nil
}

func (t supervisorAnswerTarget) AnswerAskUser(req server.AskUserAnswerRequest) (server.AskUserAnswerResponse, error) {
	sess := t.sessions.GetSession(req.SessionID)
	if sess == nil {
		return server.AskUserAnswerResponse{}, fmt.Errorf("session %s not found", req.SessionID)
	}
	var questions json.RawMessage
	for _, pending := range sess.PendingControlRequests() {
		if pending.RequestID == req.RequestID {
			questions = pending.Request.Input
		}
	}
	if err := sess.RespondToAskUser(req.RequestID, questions, req.Answers, nil); err != nil {
		return server.AskUserAnswerResponse{}, err
	}
	return server.AskUserAnswerResponse{SessionID: sess.ID(), RequestID: req.RequestID, Result: "answered"}, nil
}

type supervisorHarness struct {
	t        *testing.T
	stateDir string
	// runtimeDir is the runtime parent of stateDir, exported to the
	// supervisor child as agent.RuntimeDirEnv.
	runtimeDir string
	// agenticoBin overrides the helper binary named in the system prompt;
	// empty uses the test binary, as AGENTICO_BIN does.
	agenticoBin string
	script      string
	registry    *llm.Registry
	store       *feature.Store
	sessions    *session.Manager
	runner      *agent.PhaseRunner
	admission   *workadmission.Coordinator
	coord       *supervisor.Coordinator
	srv         *httptest.Server
	model       string
	// claudeConfigDir is CLAUDE_CONFIG_DIR for the server and the child, so
	// rebuilt native sessions never land in the developer's home.
	claudeConfigDir string
	// harness is the provider the journeys choose: "claude" (a fake shell
	// script) or "codex" (the fake app-server through the real adapter).
	harness string
	// codexHome is CODEX_HOME for the server and the child, for the same
	// reason as claudeConfigDir.
	codexHome string
	// abandoned holds coordinators a simulated crash left open.
	abandoned []*supervisor.Coordinator
}

func newSupervisorHarness(t *testing.T, body string, mutate ...func(*supervisor.Options)) *supervisorHarness {
	t.Helper()
	h := newHarnessBase(t, "claude")
	h.script = testutil.WriteFakeClaudeScript(t, body)
	h.registry = testutil.NewFakeClaudeRegistry(t, h.script)
	h.init(mutate...)
	return h
}

// newCodexSupervisorHarness runs the journeys on the fake Codex app-server
// launched through the real Codex provider and adapter.
func newCodexSupervisorHarness(t *testing.T, script testutil.FakeCodexScript, mutate ...func(*supervisor.Options)) *supervisorHarness {
	t.Helper()
	h := newHarnessBase(t, "codex")
	h.script = testutil.WriteFakeCodexScript(t, script)
	h.registry = testutil.NewFakeCodexRegistry(t, h.script)
	h.init(mutate...)
	return h
}

func newHarnessBase(t *testing.T, harness string) *supervisorHarness {
	t.Helper()
	h := &supervisorHarness{t: t, runtimeDir: t.TempDir(), claudeConfigDir: t.TempDir(), codexHome: t.TempDir(), harness: harness}
	t.Setenv(claudeconfig.EnvConfigDir, h.claudeConfigDir)
	t.Setenv("CODEX_HOME", h.codexHome)
	h.stateDir = filepath.Join(h.runtimeDir, "state")
	if err := os.MkdirAll(h.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *supervisorHarness) init(mutate ...func(*supervisor.Options)) {
	t := h.t
	t.Helper()
	h.sessions = session.NewManager(nil)
	h.store = feature.NewStore(h.stateDir)
	h.runner = agent.NewPhaseRunner(h.sessions, h.store, h.stateDir)
	h.runner.Registry = h.registry
	h.runner.Config = config.NewDefault()
	h.runner.SkillsDir = filepath.Join(h.runtimeDir, "skills")
	h.admission = workadmission.New(workadmission.Options{})
	eligible := h.registry.EligibleModelsForPhase(llm.PhaseChat)[h.harness]
	if len(eligible) == 0 {
		t.Fatalf("fake %s has no chat-eligible model", h.harness)
	}
	h.model = eligible[0]
	h.start(mutate...)
	t.Cleanup(func() {
		h.stopServer()
		_ = h.coord.Close()
		for _, c := range h.abandoned {
			_ = c.Close()
		}
		h.sessions.Shutdown()
	})
}

// start builds a coordinator over the harness state directory and serves
// it through the live handler, as a server boot does.
func (h *supervisorHarness) start(mutate ...func(*supervisor.Options)) {
	h.t.Helper()
	opts := supervisor.Options{
		StateDir: h.stateDir,
		WorkDir:  h.stateDir,
		Catalog:  supervisor.RegistryCatalog{Registry: h.registry},
		Launcher: &supervisor.SessionLauncher{
			Runner:        h.runner,
			Sessions:      h.sessions,
			RuntimeDir:    h.runtimeDir,
			ConfigPath:    filepath.Join(h.runtimeDir, "config.yaml"),
			DiscoveryPath: server.DiscoveryPath(h.runtimeDir),
			AgenticoBin:   h.agenticoBin,
		},
		Admission:        h.admission,
		HandshakeTimeout: 10 * time.Second,
		Converters: map[string]supervisor.Converter{
			claudesession.Harness: claudesession.New(claudesession.Options{}),
			codexsession.Harness:  codexsession.New(codexsession.Options{}),
			opencodesession.Harness: opencodesession.New(opencodesession.Options{
				ContextWindow: opencodesession.RegistryContextWindow(h.registry),
			}),
		},
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	coord, err := supervisor.New(opts)
	if err != nil {
		h.t.Fatalf("supervisor.New: %v", err)
	}
	h.coord = coord
	h.srv = httptest.NewServer(server.NewHandler(server.HandlerOptions{
		AuthToken:             supervisorTestToken,
		DisableHostValidation: true,
		Registry:              h.registry,
		FeatureStore:          h.store,
		Features:              h.store,
		Sessions:              h.sessions,
		Mutations:             supervisorAnswerTarget{sessions: h.sessions},
		Supervisor:            coord,
		Admission:             h.admission,
		// The runtime state directory backs the uploads route, so staged
		// attachment references resolve as on a real server.
		Runtime: server.RuntimeIdentity{RuntimeDir: h.runtimeDir, StateDir: h.stateDir, Config: filepath.Join(h.runtimeDir, "config.yaml")},
	}))
}

// restart simulates a server restart over the same state directory.
func (h *supervisorHarness) restart() {
	h.t.Helper()
	h.stopServer()
	_ = h.coord.Close()
	h.start()
}

// crash simulates a server process dying mid-flight: the old coordinator is
// abandoned without Close, so its provider keeps running and its
// turn-in-flight record stays, and a new coordinator boots over the same
// state directory.
func (h *supervisorHarness) crash(mutate ...func(*supervisor.Options)) {
	h.t.Helper()
	h.stopServer()
	h.abandoned = append(h.abandoned, h.coord)
	h.start(mutate...)
}

// stopServer drops open event streams first: Close waits for active
// requests, and an SSE request only ends when its client goes away.
func (h *supervisorHarness) stopServer() {
	h.srv.CloseClientConnections()
	h.srv.Close()
}

func (h *supervisorHarness) do(method, path string, body any, wantStatus int, out any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, reader)
	req.Header.Set("Authorization", "Bearer "+supervisorTestToken)
	if method != http.MethodGet {
		req.Header.Set("X-Agentico-Client", "local")
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		h.t.Fatalf("%s %s status = %d, want %d; body %s", method, path, resp.StatusCode, wantStatus, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			h.t.Fatalf("decode %s: %v", data, err)
		}
	}
}

func (h *supervisorHarness) state() server.SupervisorState {
	h.t.Helper()
	var resp server.SupervisorStateResponse
	h.do(http.MethodGet, "/api/v1/supervisor/state", nil, http.StatusOK, &resp)
	return resp.State
}

func (h *supervisorHarness) waitState(what string, cond func(server.SupervisorState) bool) server.SupervisorState {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		st := h.state()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; last state %+v", what, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *supervisorHarness) waitLifecycle(want server.SupervisorLifecycle) server.SupervisorState {
	h.t.Helper()
	return h.waitState("lifecycle "+string(want), func(st server.SupervisorState) bool { return st.Lifecycle == want })
}

func (h *supervisorHarness) chooseSettings() {
	h.t.Helper()
	h.do(http.MethodPatch, "/api/v1/supervisor/settings", map[string]any{"harness": h.harness, "model": h.model, "request_id": "initial-settings", "expected_generation": 0}, http.StatusOK, nil)
}

func (h *supervisorHarness) send(text, cmid string) server.SupervisorMessageResponse {
	h.t.Helper()
	var resp server.SupervisorMessageResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": text, "client_message_id": cmid}, http.StatusOK, &resp)
	return resp
}

func (h *supervisorHarness) transcript(query string) server.SupervisorTranscriptResponse {
	h.t.Helper()
	var resp server.SupervisorTranscriptResponse
	h.do(http.MethodGet, "/api/v1/supervisor/transcript"+query, nil, http.StatusOK, &resp)
	return resp
}

// userInputs returns the text of every user message that reached the fake
// harness on stdin, in arrival order.
func (h *supervisorHarness) userInputs() []string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.script), testutil.FakeSupervisorUserInputsFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var texts []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var msg struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			h.t.Fatalf("decode user input %q: %v", line, err)
		}
		var text string
		if err := json.Unmarshal(msg.Message.Content, &text); err != nil {
			var blocks []llm.ContentBlock
			if err := json.Unmarshal(msg.Message.Content, &blocks); err != nil {
				h.t.Fatalf("decode user content %s: %v", msg.Message.Content, err)
			}
			for _, block := range blocks {
				text += block.Text
			}
		}
		texts = append(texts, text)
	}
	return texts
}

// systemPrompt returns the system prompt the latest fake harness launch
// received on its launch flag.
func (h *supervisorHarness) systemPrompt() string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.script), testutil.FakeSupervisorSystemPromptFile))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *supervisorHarness) invocations() int {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.script), testutil.FakeSupervisorInvocationsFile))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

type sseEvent struct {
	id   string
	kind string
	data server.SupervisorStreamEvent
}

// supervisorStream is a minimal SSE reader over the supervisor events
// route, authenticating with the access_token query fallback.
type supervisorStream struct {
	t      *testing.T
	events chan sseEvent
	cancel context.CancelFunc
}

func (h *supervisorHarness) openStream(query string) *supervisorStream {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sep := "?"
	if query != "" {
		sep = "&"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/api/v1/supervisor/events"+query+sep+"access_token="+supervisorTestToken+"&heartbeat_ms=200", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		h.t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		h.t.Fatalf("stream status = %d", resp.StatusCode)
	}
	s := &supervisorStream{t: h.t, events: make(chan sseEvent, 1024), cancel: cancel}
	go func() {
		defer resp.Body.Close()
		defer close(s.events)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev.kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev.data)
			case line == "":
				if ev.kind != "" {
					s.events <- ev
				}
				ev = sseEvent{}
			}
		}
	}()
	h.t.Cleanup(cancel)
	return s
}

// until reads events until match returns true, returning everything read.
func (s *supervisorStream) until(what string, match func(sseEvent) bool) []sseEvent {
	s.t.Helper()
	var seen []sseEvent
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				s.t.Fatalf("stream closed waiting for %s; saw %d events", what, len(seen))
			}
			seen = append(seen, ev)
			if match(ev) {
				return seen
			}
		case <-timeout:
			s.t.Fatalf("timed out waiting for %s; saw %d events", what, len(seen))
		}
	}
}

func isState(want server.SupervisorLifecycle) func(sseEvent) bool {
	return func(ev sseEvent) bool {
		return ev.kind == string(server.SupervisorEventState) && ev.data.State != nil && ev.data.State.Lifecycle == want
	}
}

func recordKinds(items []server.SupervisorRecord) string {
	kinds := make([]string, 0, len(items))
	for _, rec := range items {
		kinds = append(kinds, string(rec.Kind))
	}
	return strings.Join(kinds, ",")
}

func TestSupervisorFirstSendStreamsDurableReplyAndReusesProcess(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	st := h.state()
	if st.Lifecycle != server.SupervisorLifecycleStopped || st.HeadSeq != 0 || st.Settings.Model != "" {
		t.Fatalf("boot state = %+v", st)
	}
	var refused server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "hi", "client_message_id": "c0"}, http.StatusConflict, &refused)
	if refused.Error.Code != "settings_required" {
		t.Fatalf("send without settings = %+v", refused.Error)
	}
	h.chooseSettings()
	conversation := h.state().ConversationID

	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))

	first := h.send("hello", "c1")
	if !first.Launched || first.Record.Seq != 1 || first.Record.Kind != server.SupervisorRecordKindUser {
		t.Fatalf("first send = %+v", first)
	}
	events := stream.until("idle after turn 1", isState(server.SupervisorLifecycleIdle))
	var deltas, records int
	var assistant *server.SupervisorRecord
	for _, ev := range events {
		switch ev.kind {
		case string(server.SupervisorEventDelta):
			if assistant != nil {
				t.Fatal("delta arrived after the committed record")
			}
			deltas++
			if ev.id != "" || ev.data.Delta.StreamMessageID != "msg_1" {
				t.Fatalf("delta = id %q %+v", ev.id, ev.data.Delta)
			}
		case string(server.SupervisorEventRecord):
			records++
			if ev.id != fmt.Sprint(ev.data.Seq) {
				t.Fatalf("record event id %q != seq %d", ev.id, ev.data.Seq)
			}
			if ev.data.Record.Kind == server.SupervisorRecordKindAssistant {
				assistant = ev.data.Record
			}
		}
		if ev.data.ConversationID != conversation || ev.data.StreamEpoch == "" {
			t.Fatalf("event envelope = %+v", ev.data)
		}
	}
	if deltas < 2 || assistant == nil || assistant.StreamMessageID != "msg_1" || assistant.Messages[0].Text != "Hello from turn 1" {
		t.Fatalf("turn 1 stream: deltas=%d assistant=%+v", deltas, assistant)
	}

	if second := h.send("again", "c2"); second.Launched {
		t.Fatalf("second send relaunched: %+v", second)
	}
	stream.until("idle after turn 2", isState(server.SupervisorLifecycleIdle))
	st = h.state()
	if st.HeadSeq != 4 || st.Generation != 1 || st.SessionID != supervisor.SessionID(conversation, 1) || st.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
		t.Fatalf("state after two turns = %+v", st)
	}
	if n := h.invocations(); n != 1 {
		t.Fatalf("provider invocations = %d, want 1", n)
	}
	// The supervisor prompt rides the launch channel; stdin carries only the
	// user's own text.
	sysPrompt := h.systemPrompt()
	for _, want := range []string{
		filepath.Join(h.runtimeDir, "skills", "supervisor", "SKILL.md"),
		"Runtime directory: " + h.runtimeDir,
		"Discovery file: " + server.DiscoveryPath(h.runtimeDir),
		" api METHOD /api/v1/",
	} {
		if !strings.Contains(sysPrompt, want) {
			t.Fatalf("launch system prompt lacks %q:\n%s", want, sysPrompt)
		}
	}
	if inputs := h.userInputs(); fmt.Sprint(inputs) != "[hello again]" {
		t.Fatalf("user inputs = %q, want the user's own text", inputs)
	}
	if page := h.transcript(""); recordKinds(page.Items) != "user,assistant,user,assistant" || page.HeadSeq != 4 {
		t.Fatalf("transcript = %s head %d", recordKinds(page.Items), page.HeadSeq)
	}
	// Resume after seq 1 replays exactly the committed records after it.
	resumed := h.openStream("?after=1&epoch=" + st.StreamEpoch)
	replay := resumed.until("replayed head", func(ev sseEvent) bool { return ev.kind == "record" && ev.data.Seq == 4 })
	var seqs []int64
	for _, ev := range replay {
		if ev.kind == "record" {
			seqs = append(seqs, ev.data.Seq)
		}
	}
	if fmt.Sprint(seqs) != "[2 3 4]" || replay[0].kind != "state" {
		t.Fatalf("replay = %v (first event %s)", seqs, replay[0].kind)
	}
	for _, q := range []string{"?after=99&epoch=" + st.StreamEpoch, "?after=1&epoch=stale"} {
		reset := h.openStream(q)
		evs := reset.until("stream.reset", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventStreamReset) })
		if !evs[len(evs)-1].data.SnapshotRequired {
			t.Fatalf("%s: reset without snapshot_required", q)
		}
		// The stream stays live after the reset.
		reset.until("heartbeat after reset", func(ev sseEvent) bool { return ev.kind == string(server.SupervisorEventHeartbeat) })
	}

	// The reply survives a server restart; the restarted server starts
	// nothing until the next send.
	h.restart()
	st = h.state()
	if st.ConversationID != conversation || st.HeadSeq != 4 || st.Lifecycle != server.SupervisorLifecycleStopped || st.Settings.Model != h.model {
		t.Fatalf("state after restart = %+v", st)
	}
	if page := h.transcript("?after=0&limit=2"); recordKinds(page.Items) != "user,assistant" || !page.HasMoreAfter {
		t.Fatalf("paged transcript after restart = %s more=%v", recordKinds(page.Items), page.HasMoreAfter)
	}
	third := h.send("after restart", "c3")
	if !third.Launched || third.Record.Generation != 2 {
		t.Fatalf("send after restart = %+v", third)
	}
	st = h.waitLifecycle(server.SupervisorLifecycleIdle)
	if !strings.HasSuffix(st.SessionID, ".2") || h.invocations() != 2 {
		t.Fatalf("relaunch state = %+v invocations=%d", st, h.invocations())
	}
}

func TestSupervisorConcurrentSendsStartOneProcess(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	const senders = 6
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
	users := 0
	for _, rec := range h.transcript("").Items {
		if rec.Kind == server.SupervisorRecordKindUser {
			users++
		}
	}
	if users != senders {
		t.Fatalf("user records = %d, want %d", users, senders)
	}
}

func TestSupervisorLaunchFailureThenRecovery(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeLaunchFailureScriptBody())
	h.chooseSettings()
	var failed server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "hello", "client_message_id": "c1"}, http.StatusBadGateway, &failed)
	if failed.Error.Code != "supervisor_launch_failed" {
		t.Fatalf("launch failure = %+v", failed.Error)
	}
	h.assertLaunchFailed(failed.Error)
	if err := os.WriteFile(h.script, []byte("#!/bin/sh\n"+testutil.FakeClaudeInteractiveScriptBody()), 0o755); err != nil {
		t.Fatal(err)
	}
	if resp := h.send("hello", "c1"); !resp.Launched || resp.Record.Generation != 2 {
		t.Fatalf("retry = %+v", resp)
	}
	if st := h.waitLifecycle(server.SupervisorLifecycleIdle); st.Failure != nil {
		t.Fatalf("failure survived the retry: %+v", st.Failure)
	}
}

// assertLaunchFailed checks the failed read model and transcript: the
// failure envelope matches what the sender received, an error marker with
// the catalog code is the newest record, no user record was committed and
// the settings are intact.
func (h *supervisorHarness) assertLaunchFailed(sent server.Error) {
	h.t.Helper()
	st := h.state()
	if st.Lifecycle != server.SupervisorLifecycleFailed || st.Failure == nil || st.Failure.Code != sent.Code || st.Failure.Diagnostics != sent.Diagnostics {
		h.t.Fatalf("state after failure = %+v (failure %+v, sent %+v)", st, st.Failure, sent)
	}
	if st.Settings.Harness != h.harness || st.Settings.Model != h.model {
		h.t.Fatalf("settings after failure = %+v", st.Settings)
	}
	page := h.transcript("")
	if len(page.Items) == 0 {
		h.t.Fatal("no error marker after a failed launch")
	}
	last := page.Items[len(page.Items)-1]
	if last.Kind != server.SupervisorRecordKindMarker || last.Marker == nil || last.Marker.Marker != server.SupervisorMarkerError ||
		last.Marker.Code != string(errcat.SupervisorLaunchFailed) || last.Marker.Text == "" {
		h.t.Fatalf("newest record after failure = %+v", last)
	}
	for _, rec := range page.Items {
		if rec.Kind == server.SupervisorRecordKindUser && rec.Generation == st.Generation {
			h.t.Fatalf("the failed launch committed a user record: %+v", rec)
		}
	}
}

func TestSupervisorPermissionAndQuestionRequests(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))
	h.send("warm up", "c0")
	stream.until("idle", isState(server.SupervisorLifecycleIdle))

	for i, tc := range []struct{ marker, tool string }{
		{testutil.FakeSupervisorPermBash, "Bash"},
		{testutil.FakeSupervisorPermEdit, "Edit"},
		{testutil.FakeSupervisorPermAgent, "Task"},
	} {
		h.send("please "+tc.marker, fmt.Sprintf("p%d", i))
		evs := stream.until(tc.tool+" waiting", isState(server.SupervisorLifecycleWaitingPermission))
		sawRequest := false
		for _, ev := range evs {
			if ev.kind == string(server.SupervisorEventRequest) && ev.data.Request.ToolName == tc.tool {
				sawRequest = true
			}
		}
		if !sawRequest {
			t.Fatalf("%s: no request event before the waiting state", tc.tool)
		}
		st := h.state()
		if len(st.PendingRequests) != 1 || st.PendingRequests[0].ToolName != tc.tool || st.PendingRequests[0].SessionID != st.SessionID || st.PendingRequests[0].Origin != server.RequestOriginRoot {
			t.Fatalf("%s pending = %+v", tc.tool, st.PendingRequests)
		}
		h.do(http.MethodPost, "/api/v1/permissions/answer", map[string]string{
			"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID, "decision": "allow_once",
		}, http.StatusOK, nil)
		stream.until(tc.tool+" idle", isState(server.SupervisorLifecycleIdle))
		if st := h.state(); len(st.PendingRequests) != 0 {
			t.Fatalf("%s: pending not cleared: %+v", tc.tool, st.PendingRequests)
		}
	}

	// A read-only tool is auto-allowed and never surfaces.
	h.send("look "+testutil.FakeSupervisorPermRead, "r1")
	for _, ev := range stream.until("read turn idle", isState(server.SupervisorLifecycleIdle)) {
		if ev.kind == string(server.SupervisorEventRequest) || isState(server.SupervisorLifecycleWaitingPermission)(ev) {
			t.Fatalf("read-only tool surfaced: %+v", ev.data)
		}
	}

	h.send("decide "+testutil.FakeSupervisorAsk, "q1")
	stream.until("question", isState(server.SupervisorLifecycleWaitingQuestion))
	st := h.state()
	if len(st.PendingRequests) != 1 || len(st.PendingRequests[0].Questions) == 0 || st.PendingRequests[0].Origin != server.RequestOriginRoot {
		t.Fatalf("question pending = %+v", st.PendingRequests)
	}
	h.do(http.MethodPost, "/api/v1/prompts/ask-user/answer", map[string]any{
		"request_id": st.PendingRequests[0].RequestID, "session_id": st.SessionID, "answers": map[string]string{"Which branch?": "main"},
	}, http.StatusOK, nil)
	stream.until("question idle", isState(server.SupervisorLifecycleIdle))

	var verdicts []string
	for _, rec := range h.transcript("?limit=500").Items {
		if rec.Request != nil && (rec.Request.Origin != server.RequestOriginRoot || rec.Request.ChildSessionID != "") {
			t.Fatalf("root request record origin = %q/%q", rec.Request.Origin, rec.Request.ChildSessionID)
		}
		if rec.Request != nil && rec.Request.Stage == server.SupervisorRequestStageResolved {
			if rec.Visibility != server.SupervisorVisibilityDisplayOnly {
				t.Fatalf("verdict visibility = %s", rec.Visibility)
			}
			verdicts = append(verdicts, rec.Request.ToolName+"/"+string(rec.Request.Outcome))
		}
	}
	if fmt.Sprint(verdicts) != "[Bash/allowed Edit/allowed Task/allowed AskUserQuestion/answered]" {
		t.Fatalf("verdicts = %v", verdicts)
	}
}

func TestSupervisorInterruptEndAndGraceTermination(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody(), func(o *supervisor.Options) {
		o.InterruptGrace = 500 * time.Millisecond
	})
	h.chooseSettings()
	h.send("work "+testutil.FakeSupervisorHold, "h1")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	var action server.SupervisorActionResponse
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, &action)
	if action.Result != server.SupervisorActionAccepted {
		t.Fatalf("interrupt = %+v", action)
	}
	st := h.waitLifecycle(server.SupervisorLifecycleIdle)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted || st.Generation != 1 {
		t.Fatalf("after interrupt = %+v", st)
	}
	// The same process serves the next turn.
	h.send("next", "h2")
	h.waitState("next turn", func(st server.SupervisorState) bool {
		return st.Lifecycle == server.SupervisorLifecycleIdle && st.LastTurnOutcome == server.SupervisorTurnOutcomeCompleted
	})
	if h.invocations() != 1 {
		t.Fatalf("invocations = %d after interrupt", h.invocations())
	}

	// A harness that ignores the interrupt is terminated after the grace.
	h.send("stuck "+testutil.FakeSupervisorStubborn, "h3")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	st = h.waitLifecycle(server.SupervisorLifecycleStopped)
	if st.LastTurnOutcome != server.SupervisorTurnOutcomeInterrupted || st.SessionID != "" {
		t.Fatalf("after grace = %+v", st)
	}

	// End keeps the transcript and settings.
	h.send("again", "h4")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	sessionID := h.state().SessionID
	head := h.state().HeadSeq
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, &action)
	if action.Result != server.SupervisorActionEnded || action.State.Lifecycle != server.SupervisorLifecycleStopped ||
		action.State.HeadSeq != head || action.State.Settings.Model != h.model {
		t.Fatalf("end = %+v", action)
	}
	if sess := h.sessions.GetSession(sessionID); sess != nil && sess.IsActive() {
		t.Fatal("End left the provider process running")
	}
	if page := h.transcript(""); page.HeadSeq != head {
		t.Fatalf("transcript after End head = %d, want %d", page.HeadSeq, head)
	}
}

func TestSupervisorAdmissionAndShutdown(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	h.chooseSettings()
	h.send("work "+testutil.FakeSupervisorHold, "a1")
	h.waitLifecycle(server.SupervisorLifecycleRunning)
	activity, err := h.admission.Detect(context.Background())
	if err != nil || !activity.SupervisorActive {
		t.Fatalf("running supervisor activity = %+v %v", activity, err)
	}
	h.do(http.MethodPost, "/api/v1/supervisor/interrupt", map[string]any{}, http.StatusOK, nil)
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	if activity, _ := h.admission.Detect(context.Background()); activity.SupervisorActive {
		t.Fatal("idle supervisor counted as active work")
	}

	// The stopper's path: close admission for stopping, end the
	// supervisor, and refuse relaunch while closed.
	if !h.admission.CloseForStopping(workadmission.CategoryFeature, workadmission.CategorySupervisor) {
		t.Fatal("admission did not close for stopping")
	}
	sessionID := h.state().SessionID
	h.do(http.MethodPost, "/api/v1/supervisor/end", map[string]any{}, http.StatusOK, nil)
	var refused server.ErrorResponse
	h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]string{"text": "again", "client_message_id": "a2"}, http.StatusServiceUnavailable, &refused)
	if refused.Error.Code != "update_in_progress" || h.invocations() != 1 {
		t.Fatalf("closed send = %+v invocations=%d", refused.Error, h.invocations())
	}
	h.admission.Open()

	// Shutdown ends a live supervisor process.
	h.send("one more", "a3")
	h.waitLifecycle(server.SupervisorLifecycleIdle)
	sessionID = h.state().SessionID
	sess := h.sessions.GetSession(sessionID)
	if sess == nil || !sess.IsActive() {
		t.Fatal("expected a live supervisor session")
	}
	_ = h.coord.Close()
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown left the supervisor process running")
	}
}

// TestSupervisorErrorReferenceDeliversHiddenContextOnlyToHarness drives an
// explain-in-chat message with an error reference on the first send (the
// launch path) and on a follow-up (the live process): the harness receives
// the resolved bundle ahead of the visible text each time, while the
// committed records, the transcript and the stream carry only the visible
// text. A malformed or stale reference is refused before anything reaches
// the harness or the transcript.
func TestSupervisorErrorReferenceDeliversHiddenContextOnlyToHarness(t *testing.T) {
	h := newSupervisorHarness(t, testutil.FakeClaudeInteractiveScriptBody())
	const marker = "diagnostics-only-the-harness-sees"
	failed := &feature.Feature{
		ID: "feat-explain", Name: "Explain Feature", Slug: "feat-explain",
		Status: feature.StatusFailed, CurrentPhase: feature.PhaseImplement,
		ActiveRun: 1, RunCount: 1, SchemaVersion: feature.SchemaVersionCurrent,
	}
	failed.Run().Failure = &errcat.FailureRecord{Code: errcat.IterationBudgetExhausted, Diagnostics: marker}
	if err := h.store.Save(failed); err != nil {
		t.Fatal(err)
	}
	h.chooseSettings()
	ref := map[string]string{"scope": "run", "code": string(errcat.IterationBudgetExhausted), "feature_id": failed.ID}

	for _, tc := range []struct {
		ref    map[string]string
		status int
		code   string
	}{
		{map[string]string{"scope": "run", "code": string(errcat.IterationBudgetExhausted)}, http.StatusBadRequest, "chat_context_invalid"},
		{map[string]string{"scope": "run", "code": string(errcat.WorktreeSetupFailed), "feature_id": failed.ID}, http.StatusNotFound, "chat_context_not_found"},
	} {
		var refused server.ErrorResponse
		h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]any{"text": "Explain", "client_message_id": "bad", "error_reference": tc.ref}, tc.status, &refused)
		if refused.Error.Code != tc.code {
			t.Fatalf("reference %v = %+v, want %s", tc.ref, refused.Error, tc.code)
		}
	}
	if st := h.state(); st.HeadSeq != 0 || st.Lifecycle != server.SupervisorLifecycleStopped || h.invocations() != 0 {
		t.Fatalf("refused references changed state: %+v invocations=%d", st, h.invocations())
	}

	send := func(text, cmid string) server.SupervisorMessageResponse {
		var resp server.SupervisorMessageResponse
		h.do(http.MethodPost, "/api/v1/supervisor/messages", map[string]any{"text": text, "client_message_id": cmid, "error_reference": ref}, http.StatusOK, &resp)
		return resp
	}
	stream := h.openStream("")
	stream.until("initial state", isState(server.SupervisorLifecycleStopped))
	first := send("Explain this failure", "e1")
	if !first.Launched {
		t.Fatalf("first send did not launch: %+v", first)
	}
	events := stream.until("idle after first turn", isState(server.SupervisorLifecycleIdle))
	if second := send("Tell me more", "e2"); second.Launched {
		t.Fatalf("follow-up relaunched: %+v", second)
	}
	events = append(events, stream.until("idle after follow-up", isState(server.SupervisorLifecycleIdle))...)

	inputs := h.userInputs()
	if len(inputs) != 2 {
		t.Fatalf("harness user inputs = %q, want two", inputs)
	}
	for i, visible := range []string{"Explain this failure", "Tell me more"} {
		bundle, text, ok := strings.Cut(inputs[i], "\n\n"+visible)
		if !ok || text != "" || !strings.Contains(bundle, "error[iteration_budget_exhausted]") || !strings.Contains(bundle, marker) || !strings.Contains(bundle, failed.ID) {
			t.Fatalf("harness input %d = %q, want the hidden bundle then %q", i, inputs[i], visible)
		}
	}
	if raw, _ := json.Marshal(first); strings.Contains(string(raw), marker) {
		t.Fatalf("message response leaked the bundle: %s", raw)
	}
	for _, ev := range events {
		if raw, _ := json.Marshal(ev.data); strings.Contains(string(raw), marker) {
			t.Fatalf("stream event leaked the bundle: %s", raw)
		}
	}
	page := h.transcript("")
	var users []string
	for _, rec := range page.Items {
		if raw, _ := json.Marshal(rec); strings.Contains(string(raw), marker) || strings.Contains(string(raw), "iteration_budget_exhausted") {
			t.Fatalf("transcript record leaked the bundle: %s", raw)
		}
		if rec.Kind == server.SupervisorRecordKindUser {
			users = append(users, rec.Messages[0].Text)
		}
	}
	if strings.Join(users, "|") != "Explain this failure|Tell me more" || h.invocations() != 1 {
		t.Fatalf("committed user texts = %q invocations=%d", users, h.invocations())
	}
	durable, err := os.ReadFile(filepath.Join(h.stateDir, "supervisor", "conversations", page.ConversationID, "transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(durable), marker) || !strings.Contains(string(durable), "Tell me more") {
		t.Fatalf("durable transcript = %s", durable)
	}
}
