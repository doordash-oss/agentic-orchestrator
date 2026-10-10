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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// The helper journeys boot the real built agentico binary as a plain
// `server` with a scripted claude on PATH. The supervisor child it launches
// runs the same binary's `agentico api` helper from $AGENTICO_BIN, so every
// feature mutation travels through the shipped helper, the server's own
// discovery file and the live REST surface.

// helperJourneyTurnTimeout bounds one supervisor turn: each helper call
// carries its own 30-second request timeout.
const helperJourneyTurnTimeout = 2 * time.Minute

type helperJourney struct {
	t          *testing.T
	bin        string
	root       string
	runtimeDir string
	stateDir   string
	fakeDir    string
	proc       *driverProcess
	baseURL    string
	token      string
	model      string
	events     *globalEventStream
}

// newHelperJourney builds the production binary (cached per package run),
// writes the scripted claude CLI, and boots `agentico server` on a free
// port with an isolated HOME, runtime directory and working directory.
func newHelperJourney(t *testing.T, body string) *helperJourney {
	t.Helper()
	selfupdateJourneyGuard(t)
	bin := selfupdateBinary(t, selfupdateVersionProd, false).path
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	h := &helperJourney{
		t:          t,
		bin:        bin,
		root:       root,
		runtimeDir: filepath.Join(root, "runtime"),
		fakeDir:    filepath.Join(root, "fakebin"),
	}
	h.stateDir = filepath.Join(h.runtimeDir, "features")
	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	for _, dir := range []string{h.runtimeDir, h.fakeDir, home, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h.fakeDir, "claude"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}

	port := selfupdateFreePort(t, "127.0.0.1")
	h.baseURL = "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	h.proc = startHelperServerProcess(t, bin, work, installStopEnv(home, h.fakeDir),
		"server",
		"--config", filepath.Join(h.runtimeDir, "config.yaml"),
		"--state-dir", h.stateDir,
		"--listen", "127.0.0.1:"+strconv.Itoa(port))
	probe := &selfupdateJourney{t: t, runtimeDir: h.runtimeDir}
	probe.waitHealthy(h.proc, h.baseURL, selfupdateVersionProd, 90*time.Second)
	disc := probe.waitDiscovery(h.proc, 20*time.Second)
	if disc.AuthToken == "" {
		t.Fatal("discovery record carries no token")
	}
	h.token = disc.AuthToken
	h.model = stopJourneySupervisorModel(t, h.baseURL, h.token)
	return h
}

// startHelperServerProcess mirrors startDriverProcess with an explicit
// working directory (the supervisor's working directory is the server's),
// and reaps the whole process group on cleanup so no scripted provider
// outlives the journey.
func startHelperServerProcess(t *testing.T, bin, dir string, env []string, args ...string) *driverProcess {
	t.Helper()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stderrR.Close()
		_ = stderrW.Close()
		t.Fatalf("start %s %v: %v", bin, args, err)
	}
	_ = stderrW.Close()
	p := &driverProcess{cmd: cmd, scanDone: make(chan struct{})}
	go p.scanStderr(stderrR)
	pgid := cmd.Process.Pid
	t.Cleanup(func() {
		p.terminate()
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	})
	return p
}

// request performs one authenticated API call and returns status and body.
func (h *helperJourney) request(method, path, body string) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.baseURL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	if method != http.MethodGet {
		req.Header.Set("X-Agentico-Client", "local")
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := installStopHTTPClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v\n%s", method, path, err, h.diagnostics())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, raw
}

// get reads one route that must answer 200 and decodes it into out when
// out is non-nil, returning the raw body.
func (h *helperJourney) get(path string, out any) []byte {
	h.t.Helper()
	status, raw := h.request(http.MethodGet, path, "")
	if status != http.StatusOK {
		h.t.Fatalf("GET %s status = %d body %s", path, status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			h.t.Fatalf("decode GET %s: %v\n%s", path, err, raw)
		}
	}
	return raw
}

func (h *helperJourney) supervisorState() (server.SupervisorState, []byte) {
	h.t.Helper()
	var resp server.SupervisorStateResponse
	raw := h.get("/api/v1/supervisor/state", &resp)
	return resp.State, raw
}

// chooseSupervisor selects the scripted claude harness and its first
// chat-eligible catalog model.
func (h *helperJourney) chooseSupervisor() {
	h.t.Helper()
	status, raw := h.request(http.MethodPatch, "/api/v1/supervisor/settings", `{"harness":"claude","model":"`+h.model+`","request_id":"initial-settings","expected_generation":0}`)
	if status != http.StatusOK {
		h.t.Fatalf("supervisor settings status = %d body %s", status, raw)
	}
}

// sendAndAwait sends one supervisor message and waits for its turn to
// complete with at least wantRecords new committed records.
func (h *helperJourney) sendAndAwait(text, cmid string, wantRecords int64) server.SupervisorState {
	h.t.Helper()
	before, _ := h.supervisorState()
	body, _ := json.Marshal(map[string]string{"text": text, "client_message_id": cmid})
	status, raw := h.request(http.MethodPost, "/api/v1/supervisor/messages", string(body))
	if status != http.StatusOK {
		h.t.Fatalf("supervisor message status = %d body %s\n%s", status, raw, h.diagnostics())
	}
	deadline := time.Now().Add(helperJourneyTurnTimeout)
	for {
		st, _ := h.supervisorState()
		if st.Lifecycle == server.SupervisorLifecycleIdle && st.HeadSeq >= before.HeadSeq+wantRecords {
			if st.LastTurnOutcome != server.SupervisorTurnOutcomeCompleted {
				h.t.Fatalf("turn %s outcome = %q; state %+v\n%s", cmid, st.LastTurnOutcome, st, h.diagnostics())
			}
			return st
		}
		if st.Lifecycle == server.SupervisorLifecycleFailed || h.proc.exited() {
			h.t.Fatalf("supervisor failed during turn %s; state %+v\n%s", cmid, st, h.diagnostics())
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("turn %s never completed; last state %+v\n%s", cmid, st, h.diagnostics())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// transcript returns the wire transcript page and its raw JSON.
func (h *helperJourney) transcript() (server.SupervisorTranscriptResponse, []byte) {
	h.t.Helper()
	var page server.SupervisorTranscriptResponse
	raw := h.get("/api/v1/supervisor/transcript?limit=500", &page)
	return page, raw
}

func (h *helperJourney) conversationDir(conversationID string) string {
	return filepath.Join(h.stateDir, "supervisor", "conversations", conversationID)
}

// durableRecords reads the conversation's durable transcript.
func (h *helperJourney) durableRecords(conversationID string) []supervisor.Record {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.conversationDir(conversationID), "transcript.jsonl"))
	if err != nil {
		h.t.Fatalf("read durable transcript: %v", err)
	}
	var out []supervisor.Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var rec supervisor.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			h.t.Fatalf("decode durable record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

// helperCall is one durable tool_use record paired with its tool_result.
type helperCall struct {
	command string
	output  string
	isError bool
}

// durableHelperCalls pairs every durable Bash tool_use block with the
// tool_result block answering it, in transcript order.
func (h *helperJourney) durableHelperCalls(records []supervisor.Record) []helperCall {
	h.t.Helper()
	var calls []helperCall
	index := map[string]int{}
	for _, rec := range records {
		if rec.Kind != supervisor.KindToolUse && rec.Kind != supervisor.KindToolResult {
			continue
		}
		var data supervisor.ContentData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			h.t.Fatalf("decode %s record data %s: %v", rec.Kind, rec.Data, err)
		}
		for _, block := range data.Content {
			switch {
			case block.IsToolUse():
				var input struct {
					Command string `json:"command"`
				}
				if block.Name != "Bash" || json.Unmarshal(block.Input, &input) != nil {
					h.t.Fatalf("tool_use block = %+v", block)
				}
				index[block.ID] = len(calls)
				calls = append(calls, helperCall{command: input.Command})
			case block.IsToolResult():
				i, ok := index[block.ToolUseID]
				if !ok {
					h.t.Fatalf("tool_result for unknown tool_use %q", block.ToolUseID)
				}
				var text string
				if err := json.Unmarshal(block.Content, &text); err != nil {
					h.t.Fatalf("tool_result content %s: %v", block.Content, err)
				}
				calls[i].output = text
				calls[i].isError = block.IsError
			}
		}
	}
	return calls
}

// fakeFile reads one file the scripted claude writes next to itself.
func (h *helperJourney) fakeFile(name string) string {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.fakeDir, name))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

// diagnostics renders the server stderr tail and the fake's logs.
func (h *helperJourney) diagnostics() string {
	var b strings.Builder
	fmt.Fprintf(&b, "server stderr tail:\n%s\n", h.proc.stderrTail())
	for _, name := range []string{testutil.FakeHelperCommandsFile, "helper_out", testutil.FakeHelperPhaseInvocationsFile, testutil.FakeHelperPhaseAnswersFile} {
		data, _ := os.ReadFile(filepath.Join(h.fakeDir, name))
		fmt.Fprintf(&b, "fake %s:\n%s\n", name, data)
	}
	return b.String()
}

// requireNoToken fails when the bearer token appears in content.
func (h *helperJourney) requireNoToken(what string, content []byte) {
	h.t.Helper()
	if strings.Contains(string(content), h.token) {
		h.t.Fatalf("bearer token leaked into %s", what)
	}
}

// requireConversationDirTokenFree walks every file under the conversation
// directory (durable transcript, index, per-generation output and stderr
// logs) and fails on the first one holding the token. It returns the
// relative paths it searched.
func (h *helperJourney) requireConversationDirTokenFree(conversationID string) []string {
	h.t.Helper()
	dir := h.conversationDir(conversationID)
	var searched []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		searched = append(searched, rel)
		h.requireNoToken("conversation file "+rel, data)
		return nil
	})
	if err != nil {
		h.t.Fatalf("walk conversation dir: %v", err)
	}
	return searched
}

// globalEventStream collects every event of the global /api/v1/events
// stream, authenticated with the bearer header.
type globalEventStream struct {
	mu     sync.Mutex
	events []server.SSEEvent
	raw    strings.Builder
	cancel context.CancelFunc
}

func (h *helperJourney) openGlobalEvents() *globalEventStream {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+"/api/v1/events?heartbeat_ms=500", nil)
	if err != nil {
		cancel()
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := selfupdateStreamClient.Do(req)
	if err != nil {
		cancel()
		h.t.Fatalf("open global events: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		h.t.Fatalf("global events status = %d", resp.StatusCode)
	}
	s := &globalEventStream{cancel: cancel}
	connected := make(chan struct{})
	var once sync.Once
	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev server.SSEEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			s.mu.Lock()
			s.events = append(s.events, ev)
			s.raw.WriteString(data)
			s.raw.WriteByte('\n')
			s.mu.Unlock()
			once.Do(func() { close(connected) })
		}
	}()
	h.t.Cleanup(cancel)
	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		h.t.Fatal("global events stream sent nothing within 15s")
	}
	return s
}

// waitFeatureEvent waits for an event of kind naming featureID and returns
// it.
func (s *globalEventStream) waitFeatureEvent(t *testing.T, kind, featureID string) server.SSEEvent {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if ev.Kind == kind && ev.Resource.FeatureID == featureID {
				s.mu.Unlock()
				return ev
			}
		}
		kinds := make([]string, 0, len(s.events))
		for _, ev := range s.events {
			kinds = append(kinds, ev.Kind+"/"+ev.Resource.FeatureID)
		}
		s.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no %s event for %s; saw %v", kind, featureID, kinds)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// featureKinds lists the distinct kinds of events naming featureID.
func (s *globalEventStream) featureKinds(featureID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var kinds []string
	for _, ev := range s.events {
		if ev.Resource.FeatureID == featureID && !seen[ev.Kind] {
			seen[ev.Kind] = true
			kinds = append(kinds, ev.Kind)
		}
	}
	return kinds
}

func (s *globalEventStream) rawData() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []byte(s.raw.String())
}

// features lists the features the public read model reports.
func (h *helperJourney) features() []server.FeatureSummary {
	h.t.Helper()
	var list server.FeatureListResponse
	h.get("/api/v1/features", &list)
	return list.Features
}

// waitAsk polls the prompts snapshot until match finds an entry and
// returns it.
func (h *helperJourney) waitAsk(what string, match func([]server.ControlRequest) (server.ControlRequest, bool)) server.ControlRequest {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		var prompts server.PromptSnapshotResponse
		h.get("/api/v1/prompts", &prompts)
		if req, ok := match(prompts.AskUserQuestions); ok {
			return req
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; asks %+v\n%s", what, prompts.AskUserQuestions, h.diagnostics())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func recordKindList(items []server.SupervisorRecord) string {
	kinds := make([]string, 0, len(items))
	for _, rec := range items {
		kinds = append(kinds, string(rec.Kind))
	}
	return strings.Join(kinds, ",")
}

// requireRedactedToolRecords asserts every wire tool_use and tool_result
// record carries redacted, text-free tool rows. Display summaries are
// allowed (including command previews), but raw row text and sub-agent
// prompts are not.
func requireRedactedToolRecords(t *testing.T, items []server.SupervisorRecord) {
	t.Helper()
	for _, rec := range items {
		switch rec.Kind {
		case server.SupervisorRecordKindToolUse:
			for _, msg := range rec.Messages {
				if msg.Type != "tool_use" || msg.Tool != "Bash" || !msg.Redacted || msg.Text != "" {
					t.Fatalf("wire tool_use row not redacted: %+v", msg)
				}
				// Display text is capped at 180 bytes plus a truncation ellipsis.
				if msg.ToolCall == nil || msg.ToolCall.Summary == "" || len(msg.ToolCall.Summary) > 183 || msg.ToolCall.Prompt != "" {
					t.Fatalf("wire tool_use row must contain only a bounded display summary: %+v", msg.ToolCall)
				}
			}
		case server.SupervisorRecordKindToolResult:
			for _, msg := range rec.Messages {
				if msg.Type != "tool_result" || !msg.Redacted || msg.Text != "" {
					t.Fatalf("wire tool_result row not redacted: %+v", msg)
				}
			}
		}
		if (rec.Kind == server.SupervisorRecordKindToolUse || rec.Kind == server.SupervisorRecordKindToolResult) && len(rec.Messages) == 0 {
			t.Fatalf("wire %s record %d has no rows", rec.Kind, rec.Seq)
		}
	}
}

// TestSupervisorHelperOperatesFeatureWithoutLeakingToken drives a real
// server whose supervisor fake creates, configures and starts a feature and
// then answers its phase worker's question, all through the shipped
// `agentico api` helper, and proves the bearer token reaches none of the
// conversation's files, the wire transcript, the pending-request
// projections or the harness's own input log.
func TestSupervisorHelperOperatesFeatureWithoutLeakingToken(t *testing.T) {
	h := newHelperJourney(t, testutil.FakeClaudeSupervisorHelperScriptBody())
	cfg, err := json.Marshal(map[string]any{
		"models": map[string]string{
			"inquiry": h.model, "research": h.model, "planning": h.model,
			"implementation": h.model, "review": h.model, "utilities": h.model, "kb_build": h.model,
		},
		"inquireness": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.fakeDir, testutil.FakeHelperFeatureConfigFile), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	h.chooseSupervisor()
	if got := h.features(); len(got) != 0 {
		t.Fatalf("fresh server already lists features: %+v", got)
	}
	events := h.openGlobalEvents()

	// Turn 1: create, configure and start through the helper.
	h.sendAndAwait("Create and start a feature. "+testutil.FakeHelperOperate, "operate-1", 8)
	st, _ := h.supervisorState()
	conversation := st.ConversationID

	// The supervisor launch carried the role, helper command and skill path
	// on its system prompt channel, and the reconciled skill is on disk.
	sysPrompt := h.fakeFile(testutil.FakeSupervisorSystemPromptFile)
	skillPath := filepath.Join(h.runtimeDir, "skills", "supervisor", "SKILL.md")
	for _, want := range []string{"Agentico supervisor", skillPath, "Discovery file: " + server.DiscoveryPath(h.runtimeDir)} {
		if !strings.Contains(sysPrompt, want) {
			t.Fatalf("supervisor system prompt lacks %q:\n%s", want, sysPrompt)
		}
	}
	// The helper command names the running server binary; the temp build
	// directory may be reached through a symlink.
	resolvedBin, err := filepath.EvalSymlinks(h.bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sysPrompt, h.bin+" api METHOD /api/v1/") && !strings.Contains(sysPrompt, resolvedBin+" api METHOD /api/v1/") {
		t.Fatalf("supervisor system prompt does not name the helper %s api:\n%s", h.bin, sysPrompt)
	}
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("supervisor skill not reconciled: %v", err)
	}
	h.requireNoToken("supervisor system prompt", []byte(sysPrompt))

	records := h.durableRecords(conversation)
	calls := h.durableHelperCalls(records)
	if len(calls) != 3 {
		t.Fatalf("operate helper calls = %d, want 3: %+v\n%s", len(calls), calls, h.diagnostics())
	}
	for i, call := range calls {
		if call.isError || !strings.HasPrefix(call.command, `"$AGENTICO_BIN" api `) {
			t.Fatalf("helper call %d = %+v\n%s", i, call, h.diagnostics())
		}
	}
	var created server.CreateFeatureResponse
	if err := json.Unmarshal([]byte(calls[0].output), &created); err != nil || created.FeatureID == "" {
		t.Fatalf("create tool_result is not the create body: %q (%v)", calls[0].output, err)
	}
	featureID := created.FeatureID
	wantCommands := []string{
		`"$AGENTICO_BIN" api POST /api/v1/features '{"name":"` + testutil.FakeHelperFeatureName + `"}'`,
		`"$AGENTICO_BIN" api POST /api/v1/features/` + featureID + `/config '` + string(cfg) + `'`,
		`"$AGENTICO_BIN" api POST /api/v1/features/` + featureID + `/actions/start '{}'`,
	}
	for i, want := range wantCommands {
		if calls[i].command != want {
			t.Fatalf("helper call %d command = %q, want %q", i, calls[i].command, want)
		}
	}
	for i, call := range calls[1:] {
		if !strings.Contains(call.output, featureID) {
			t.Fatalf("helper call %d result does not name the feature: %q", i+1, call.output)
		}
	}

	// The server reflects the helper's mutations: the list holds the
	// feature with its posted config and the global stream announced it.
	var listed *server.FeatureSummary
	for _, f := range h.features() {
		if f.ID == featureID {
			f := f
			listed = &f
		}
	}
	if listed == nil || listed.Name != testutil.FakeHelperFeatureName {
		t.Fatalf("features list lacks %s (%s): %+v", featureID, testutil.FakeHelperFeatureName, h.features())
	}
	var cfgResp map[string]any
	cfgRaw := h.get("/api/v1/features/"+featureID+"/config", &cfgResp)
	if !strings.Contains(string(cfgRaw), `"high"`) || !strings.Contains(string(cfgRaw), h.model) {
		t.Fatalf("feature config does not carry the posted config: %s", cfgRaw)
	}
	// The create and config publish feature lifecycle events; the start
	// launches the phase worker, which publishes a session event.
	lifecycle := events.waitFeatureEvent(t, "lifecycle.updated", featureID)
	sessionEv := events.waitFeatureEvent(t, "session.updated", featureID)
	t.Logf("global stream events for the feature: %s (resource %+v), %s (resource %+v); kinds seen for it: %v",
		lifecycle.Kind, lifecycle.Resource, sessionEv.Kind, sessionEv.Resource, events.featureKinds(featureID))

	// The wire transcript holds text-free tool records and bounded command
	// summaries. The raw helper outputs remain server-side.
	page, pageRaw := h.transcript()
	if got := recordKindList(page.Items); got != "user,tool_use,tool_result,tool_use,tool_result,tool_use,tool_result,assistant" {
		t.Fatalf("operate transcript = %s", got)
	}
	requireRedactedToolRecords(t, page.Items)
	if summary := page.Items[1].Messages[0].ToolCall.Summary; summary != wantCommands[0] {
		t.Fatalf("create command display summary = %q, want %q", summary, wantCommands[0])
	}

	// The phase worker asks its question; the supervisor answers it through
	// the helper and the prompt clears.
	ask := h.waitAsk("the phase worker's question", func(asks []server.ControlRequest) (server.ControlRequest, bool) {
		for _, a := range asks {
			if a.FeatureID == featureID && len(a.Questions) > 0 && a.Questions[0].Question == testutil.FakeHelperPhaseQuestion {
				return a, true
			}
		}
		return server.ControlRequest{}, false
	})
	h.sendAndAwait("Answer the pending question. "+testutil.FakeHelperAnswer, "answer-1", 6)
	h.waitAsk("the answered question to clear", func(asks []server.ControlRequest) (server.ControlRequest, bool) {
		for _, a := range asks {
			if a.RequestID == ask.RequestID {
				return server.ControlRequest{}, false
			}
		}
		return server.ControlRequest{}, true
	})
	answerCalls := h.durableHelperCalls(h.durableRecords(conversation))[3:]
	if len(answerCalls) != 2 {
		t.Fatalf("answer helper calls = %+v", answerCalls)
	}
	wantAnswer := `"$AGENTICO_BIN" api POST /api/v1/prompts/ask-user/answer '{"request_id":"` + ask.RequestID + `","session_id":"` + ask.SessionID +
		`","answers":{"1":"` + testutil.FakeHelperPhaseOption + `"}}'`
	if answerCalls[0].command != `"$AGENTICO_BIN" api GET /api/v1/prompts` || answerCalls[1].command != wantAnswer {
		t.Fatalf("answer commands = %q / %q, want GET prompts then %q", answerCalls[0].command, answerCalls[1].command, wantAnswer)
	}
	if answerCalls[0].isError || answerCalls[1].isError {
		t.Fatalf("answer helper calls failed: %+v", answerCalls)
	}
	if answers := h.fakeFile(testutil.FakeHelperPhaseAnswersFile); !strings.Contains(answers, testutil.FakeHelperPhaseOption) {
		t.Fatalf("phase worker never received the answer; answers log %q", answers)
	}

	page, pageRaw = h.transcript()
	if got := recordKindList(page.Items); got != "user,tool_use,tool_result,tool_use,tool_result,tool_use,tool_result,assistant,user,tool_use,tool_result,tool_use,tool_result,assistant" {
		t.Fatalf("transcript after answer = %s", got)
	}
	requireRedactedToolRecords(t, page.Items)

	// Every helper call was a bare invocation the supervisor handler allowed
	// without surfacing a permission request.
	if cmds := strings.Split(strings.TrimSpace(h.fakeFile(testutil.FakeHelperCommandsFile)), "\n"); len(cmds) != 5 {
		t.Fatalf("helper commands run = %q", cmds)
	}
	for _, rec := range page.Items {
		if rec.Kind == server.SupervisorRecordKindPermission {
			t.Fatalf("helper call surfaced a permission record: %+v", rec)
		}
	}

	// The token appears nowhere a transcript, projection or log could carry
	// it.
	searched := h.requireConversationDirTokenFree(conversation)
	t.Logf("conversation files searched for the token: %v", searched)
	if len(searched) < 3 {
		t.Fatalf("conversation dir holds too few files to prove anything: %v", searched)
	}
	durable, err := os.ReadFile(filepath.Join(h.conversationDir(conversation), "transcript.jsonl"))
	if err != nil || !strings.Contains(string(durable), featureID) {
		t.Fatalf("durable transcript does not carry the helper output the search covers (err %v)", err)
	}
	h.requireNoToken("wire transcript page", pageRaw)
	stFinal, stRaw := h.supervisorState()
	if len(stFinal.PendingRequests) != 0 {
		t.Fatalf("supervisor pending requests after the journey = %+v", stFinal.PendingRequests)
	}
	h.requireNoToken("supervisor state read model", stRaw)
	h.requireNoToken("prompts snapshot", h.get("/api/v1/prompts", nil))
	h.requireNoToken("permissions snapshot", h.get("/api/v1/permissions", nil))
	h.requireNoToken("global events stream", events.rawData())
	inputs := h.fakeFile(testutil.FakeSupervisorUserInputsFile)
	if strings.Count(inputs, "\n") != 2 {
		t.Fatalf("fake recorded inputs = %q, want the two user turns", inputs)
	}
	h.requireNoToken("fake recorded user inputs", []byte(inputs))
	h.requireNoToken("fake helper command log", []byte(h.fakeFile(testutil.FakeHelperCommandsFile)))
}

// TestSupervisorHelperMissingDiscoveryReachesToolResult points the helper's
// runtime-directory variable at an empty directory: the helper's
// missing-discovery error reaches the tool result and the server creates
// nothing.
func TestSupervisorHelperMissingDiscoveryReachesToolResult(t *testing.T) {
	h := newHelperJourney(t, testutil.FakeClaudeSupervisorHelperMisconfiguredScriptBody())
	h.chooseSupervisor()
	h.sendAndAwait("Create a feature. "+testutil.FakeHelperOperate, "operate-1", 4)
	st, _ := h.supervisorState()

	page, pageRaw := h.transcript()
	if got := recordKindList(page.Items); got != "user,tool_use,tool_result,assistant" {
		t.Fatalf("transcript = %s\n%s", got, h.diagnostics())
	}
	requireRedactedToolRecords(t, page.Items)
	calls := h.durableHelperCalls(h.durableRecords(st.ConversationID))
	if len(calls) != 1 {
		t.Fatalf("helper calls = %+v", calls)
	}
	emptyDir := filepath.Join(h.fakeDir, testutil.FakeHelperEmptyRuntimeDir)
	call := calls[0]
	if !call.isError || !strings.Contains(call.output, "discovery_missing") || !strings.Contains(call.output, emptyDir) {
		t.Fatalf("tool_result = %+v, want the missing-discovery error naming %s", call, emptyDir)
	}
	t.Logf("misconfigured helper tool_result: %q", call.output)
	if strings.Contains(call.output, h.runtimeDir) {
		t.Fatalf("misconfigured helper searched the real runtime dir: %q", call.output)
	}
	if got := h.features(); len(got) != 0 {
		t.Fatalf("misconfigured helper created features: %+v", got)
	}
	h.requireConversationDirTokenFree(st.ConversationID)
	h.requireNoToken("wire transcript page", pageRaw)
	h.requireNoToken("fake recorded user inputs", []byte(h.fakeFile(testutil.FakeSupervisorUserInputsFile)))
}
