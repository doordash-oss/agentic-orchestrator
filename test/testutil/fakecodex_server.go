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

package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// RunFakeCodexIfRequested runs the fake Codex app-server and exits when the
// process was launched as one; otherwise it returns at once. Call it first
// in TestMain of every package that launches FakeCodexProvider sessions.
func RunFakeCodexIfRequested() {
	if os.Getenv(FakeCodexEnv) != "1" {
		return
	}
	os.Exit(runFakeCodex(os.Stdin, os.Stdout, os.Getenv(FakeCodexScriptEnv), os.Args[1:]))
}

type fakeCodexServer struct {
	dir    string
	script FakeCodexScript
	launch int

	outMu sync.Mutex
	out   *bufio.Writer

	mu          sync.Mutex
	threadID    string
	model       string
	effort      string
	rollout     string
	resume      *FakeCodexResume
	sayResumed  bool
	turns       int
	turnID      string
	interrupt   chan int64
	stubborn    bool
	serverReqID int64
	waiters     map[int64]chan json.RawMessage
	initID      *int64
}

type fakeCodexLine struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func runFakeCodex(in io.Reader, out io.Writer, scriptPath string, args []string) int {
	if scriptPath == "" {
		fmt.Fprintln(os.Stderr, "fake codex: "+FakeCodexScriptEnv+" is not set")
		return 2
	}
	s := &fakeCodexServer{
		dir:         filepath.Dir(scriptPath),
		out:         bufio.NewWriter(out),
		waiters:     map[int64]chan json.RawMessage{},
		serverReqID: 1000,
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: read script: %v\n", err)
		return 2
	}
	if err := json.Unmarshal(data, &s.script); err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: decode script: %v\n", err)
		return 2
	}
	s.launch = s.appendLine(FakeSupervisorInvocationsFile, "x")
	_ = os.WriteFile(filepath.Join(s.dir, FakeSupervisorArgvFile), []byte(strings.Join(args, "\n")+"\n"), 0o644)
	if s.script.ExitOnLaunch != 0 {
		return s.script.ExitOnLaunch
	}
	// A protocol-level interrupt must never need a signal; record any SIGINT
	// so tests can prove none arrived, and otherwise ignore it.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT)
	go func() {
		for range sigs {
			s.appendLine(FakeCodexSignalsFile, "SIGINT")
		}
	}()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var line fakeCodexLine
		if err := json.Unmarshal(raw, &line); err != nil {
			fmt.Fprintf(os.Stderr, "fake codex: bad line %q: %v\n", raw, err)
			continue
		}
		s.record(line)
		s.dispatch(line)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "fake codex: read stdin: %v\n", err)
		return 1
	}
	return 0
}

// appendLine appends one line to a file beside the script and returns the
// file's resulting line count.
func (s *fakeCodexServer) appendLine(name, line string) int {
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	data, _ := os.ReadFile(path)
	return strings.Count(string(data), "\n")
}

func (s *fakeCodexServer) record(line fakeCodexLine) {
	raw, _ := json.Marshal(FakeCodexRequest{Launch: s.launch, ID: line.ID, Method: line.Method, Params: line.Params, Result: line.Result, Error: line.Error})
	s.appendLine(FakeCodexRequestsFile, string(raw))
}

func (s *fakeCodexServer) send(v any) {
	raw, _ := json.Marshal(v)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	_, _ = s.out.Write(raw)
	_ = s.out.WriteByte('\n')
	_ = s.out.Flush()
}

func (s *fakeCodexServer) reply(id int64, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *fakeCodexServer) replyError(id int64, code int, message string) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *fakeCodexServer) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// request sends a server-initiated request and waits for the client's
// result.
func (s *fakeCodexServer) request(method string, params any) json.RawMessage {
	s.mu.Lock()
	s.serverReqID++
	id := s.serverReqID
	ch := make(chan json.RawMessage, 1)
	s.waiters[id] = ch
	s.mu.Unlock()
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return <-ch
}

func (s *fakeCodexServer) dispatch(line fakeCodexLine) {
	if line.Method == "" {
		if line.ID == nil {
			return
		}
		s.mu.Lock()
		ch := s.waiters[*line.ID]
		delete(s.waiters, *line.ID)
		s.mu.Unlock()
		if ch != nil {
			result := line.Result
			if len(result) == 0 {
				result = line.Error
			}
			ch <- result
		}
		return
	}
	if line.ID == nil {
		if line.Method == "initialized" {
			s.mu.Lock()
			id := s.initID
			s.initID = nil
			s.mu.Unlock()
			if id != nil {
				s.reply(*id, map[string]any{"userAgent": "fake-codex/0.0.0", "codexHome": os.Getenv("CODEX_HOME")})
			}
		}
		return
	}
	id := *line.ID
	switch line.Method {
	case "initialize":
		s.mu.Lock()
		s.initID = &id
		s.mu.Unlock()
	case "thread/start":
		s.threadStart(id, line.Params)
	case "thread/resume":
		s.threadResume(id, line.Params)
	case "turn/start":
		s.turnStart(id, line.Params)
	case "turn/interrupt":
		s.turnInterrupt(id)
	case "thread/settings/update":
		var params struct {
			ThreadID string `json:"threadId"`
			Model    string `json:"model"`
			Effort   string `json:"effort"`
		}
		if err := json.Unmarshal(line.Params, &params); err != nil {
			s.replyError(id, -32602, "invalid settings update")
			return
		}
		respond := func() {
			if s.script.RejectSettingsUpdate {
				s.replyError(id, -32600, "settings update rejected")
				return
			}
			s.mu.Lock()
			if params.ThreadID != s.threadID {
				s.mu.Unlock()
				s.replyError(id, -32602, "unknown thread")
				return
			}
			s.model, s.effort = params.Model, params.Effort
			s.mu.Unlock()
			s.reply(id, map[string]any{})
		}
		if s.script.DelaySettingsUpdateMS > 0 {
			go func() { time.Sleep(time.Duration(s.script.DelaySettingsUpdateMS) * time.Millisecond); respond() }()
		} else {
			respond()
		}
	case "account/usage/read":
		s.reply(id, map[string]any{})
	default:
		s.replyError(id, -32601, "method not found: "+line.Method)
	}
}

func (s *fakeCodexServer) threadResult(threadID string) map[string]any {
	result := map[string]any{"thread": map[string]any{"id": threadID}, "model": s.model}
	if s.script.ApprovalPolicy != "" {
		result["approvalPolicy"] = s.script.ApprovalPolicy
	}
	return result
}

func (s *fakeCodexServer) threadStart(id int64, raw json.RawMessage) {
	if e := s.script.StartError; e != nil {
		s.replyError(id, e.Code, e.Message)
		return
	}
	var params struct {
		Model string `json:"model"`
		Cwd   string `json:"cwd"`
	}
	_ = json.Unmarshal(raw, &params)
	threadID := uuid.NewString()
	s.appendLine(FakeCodexThreadsFile, threadID)
	s.mu.Lock()
	s.threadID = threadID
	s.model = s.effectiveModel(params.Model)
	s.mu.Unlock()
	s.startRollout(threadID, params.Cwd)
	s.reply(id, s.threadResult(threadID))
	s.notify("thread/started", map[string]any{"thread": map[string]any{"id": threadID}})
}

func (s *fakeCodexServer) effectiveModel(requested string) string {
	if s.script.Model != "" {
		return s.script.Model
	}
	return requested
}

func (s *fakeCodexServer) threadResume(id int64, raw json.RawMessage) {
	var params struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(raw, &params)
	found := readFakeRollout(params.ThreadID)
	found.Launch = s.launch
	encoded, _ := json.Marshal(found)
	s.appendLine(FakeCodexResumesFile, string(encoded))
	if e := s.script.ResumeError; e != nil {
		s.replyError(id, e.Code, e.Message)
		return
	}
	s.mu.Lock()
	s.threadID = params.ThreadID
	s.model = s.effectiveModel("")
	s.rollout = found.RolloutPath
	s.resume = &found
	s.sayResumed = true
	s.mu.Unlock()
	s.reply(id, s.threadResult(params.ThreadID))
}

// readFakeRollout finds the rollout Codex would resume for threadID under
// $CODEX_HOME/sessions and counts its user message items.
func readFakeRollout(threadID string) FakeCodexResume {
	res := FakeCodexResume{ThreadID: threadID}
	home := os.Getenv("CODEX_HOME")
	if home == "" || threadID == "" {
		return res
	}
	_ = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), "-"+threadID+".jsonl") {
			res.RolloutPath = path
			return filepath.SkipAll
		}
		return nil
	})
	if res.RolloutPath == "" {
		return res
	}
	data, err := os.ReadFile(res.RolloutPath)
	if err != nil {
		return res
	}
	res.Found = true
	for line := range strings.SplitSeq(string(data), "\n") {
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
		if json.Unmarshal([]byte(line), &item) != nil {
			continue
		}
		if item.Type == "compacted" {
			res.Compacted = true
			res.UserItems, res.FirstPrompt, res.LastPrompt, res.LastReply = 0, "", "", ""
			continue
		}
		if item.Type != "response_item" ||
			item.Payload.Type != "message" || len(item.Payload.Content) == 0 {
			continue
		}
		if item.Payload.Role == "assistant" {
			res.LastReply = item.Payload.Content[0].Text
			continue
		}
		if item.Payload.Role != "user" {
			continue
		}
		res.UserItems++
		if !strings.HasPrefix(item.Payload.Content[0].Text, "Agentico note:") {
			res.LastPrompt = item.Payload.Content[0].Text
		}
		if res.UserItems == 1 && len(item.Payload.Content) > 0 {
			res.FirstPrompt = item.Payload.Content[0].Text
		}
	}
	return res
}

// startRollout writes the session_meta line Codex writes for a new thread,
// so a later rebuild finds and replaces an existing file as with the real
// CLI.
func (s *fakeCodexServer) startRollout(threadID, cwd string) {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		return
	}
	now := time.Now()
	path := filepath.Join(home, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"),
		"rollout-"+now.Format("2006-01-02T15-04-05")+"-"+threadID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	s.mu.Lock()
	s.rollout = path
	s.mu.Unlock()
	s.appendRollout("session_meta", map[string]any{"id": threadID, "cwd": cwd, "originator": "fake-codex"})
}

func (s *fakeCodexServer) appendRollout(kind string, payload any) {
	s.mu.Lock()
	path := s.rollout
	s.mu.Unlock()
	if path == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "type": kind, "payload": payload})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Close()
}

func (s *fakeCodexServer) appendRolloutMessage(role, text string) {
	kind := "input_text"
	if role == "assistant" {
		kind = "output_text"
	}
	s.appendRollout("response_item", map[string]any{"type": "message", "role": role, "content": []map[string]string{{"type": kind, "text": text}}})
}

type fakeTurn struct {
	s         *fakeCodexServer
	threadID  string
	turnID    string
	n         int
	items     int
	interrupt chan int64
	text      string
}

func (s *fakeCodexServer) turnStart(id int64, raw json.RawMessage) {
	text := FakeCodexRequest{Params: raw}.TurnText()
	var settings struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	_ = json.Unmarshal(raw, &settings)
	s.mu.Lock()
	if settings.Model != "" {
		s.model = settings.Model
	}
	s.effort = settings.Effort
	s.turns++
	t := &fakeTurn{s: s, threadID: s.threadID, n: s.turns, interrupt: make(chan int64, 4), text: text}
	t.turnID = fmt.Sprintf("turn-%d-%d", s.launch, s.turns)
	s.turnID = t.turnID
	s.interrupt = t.interrupt
	s.stubborn = strings.Contains(text, FakeCodexStubborn)
	sayResumed := s.sayResumed || strings.Contains(text, FakeCodexResumed)
	s.sayResumed = false
	resume := s.resume
	s.mu.Unlock()
	s.reply(id, map[string]any{"turn": map[string]any{"id": t.turnID, "status": "inProgress"}})
	s.notify("turn/started", map[string]any{"threadId": t.threadID, "turn": map[string]any{"id": t.turnID, "status": "inProgress"}})
	s.appendRolloutMessage("user", text)
	go t.run(text, sayResumed, resume)
}

func (s *fakeCodexServer) turnInterrupt(id int64) {
	s.mu.Lock()
	ch, stubborn := s.interrupt, s.stubborn
	s.mu.Unlock()
	if stubborn {
		return
	}
	if ch == nil {
		s.reply(id, map[string]any{})
		return
	}
	ch <- id
}

func (t *fakeTurn) itemID() string {
	t.items++
	return fmt.Sprintf("item-%d-%d", t.n, t.items)
}

func (t *fakeTurn) item(method string, item map[string]any) {
	t.s.notify(method, map[string]any{"threadId": t.threadID, "turnId": t.turnID, "item": item})
}

func (t *fakeTurn) message(text string, deltas ...string) {
	id := t.itemID()
	t.item("item/started", map[string]any{"id": id, "type": "agentMessage", "text": ""})
	for _, d := range deltas {
		t.s.notify("item/agentMessage/delta", map[string]any{"threadId": t.threadID, "turnId": t.turnID, "itemId": id, "delta": d})
	}
	t.item("item/completed", map[string]any{"id": id, "type": "agentMessage", "text": text})
	t.s.appendRolloutMessage("assistant", text)
}

func (t *fakeTurn) usage() {
	breakdown := map[string]int{"inputTokens": 100 * t.n, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 10 * t.n, "reasoningOutputTokens": 0, "totalTokens": 110 * t.n}
	last := maps.Clone(breakdown)
	if strings.Contains(t.text, FakeCodexUsageHigh) {
		last["inputTokens"], last["totalTokens"] = 172000, 172000
	}
	if strings.Contains(t.text, FakeCodexCompact) {
		last["inputTokens"], last["totalTokens"] = 1000, 1000
	}
	t.s.notify("thread/tokenUsage/updated", map[string]any{"threadId": t.threadID, "turnId": t.turnID,
		"tokenUsage": map[string]any{"total": breakdown, "last": last, "modelContextWindow": 200000}})
}

func (t *fakeTurn) complete(status string) {
	t.usage()
	t.s.mu.Lock()
	if t.s.turnID == t.turnID {
		t.s.turnID, t.s.interrupt, t.s.stubborn = "", nil, false
	}
	t.s.mu.Unlock()
	t.s.notify("turn/completed", map[string]any{"threadId": t.threadID, "turn": map[string]any{"id": t.turnID, "status": status}})
}

// hold waits for turn/interrupt, answers it and reports the turn
// interrupted. A stubborn turn never gets here: its interrupts are dropped.
func (t *fakeTurn) hold() {
	id := <-t.interrupt
	t.s.reply(id, map[string]any{})
	t.complete("interrupted")
}

func decision(result json.RawMessage) string {
	var d struct {
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(result, &d)
	return d.Decision
}

func (t *fakeTurn) run(text string, sayResumed bool, resume *FakeCodexResume) {
	switch {
	case strings.Contains(text, FakeSupervisorRecallFirst):
		reply := "First user prompt: "
		if resume != nil {
			reply += resume.FirstPrompt
		}
		t.message(reply, reply)
	case strings.Contains(text, FakeSupervisorRecallLast):
		reply := "Last exchange: "
		if resume != nil {
			reply += resume.LastPrompt + " | " + resume.LastReply
		}
		t.message(reply, reply)
	case sayResumed:
		reply := "Not resumed"
		if resume != nil {
			reply = fmt.Sprintf("Resumed with %d prior messages: %s", resume.UserItems, resume.FirstPrompt)
			if resume.Compacted {
				reply = fmt.Sprintf("Resumed after compaction with %d prior messages: %s", resume.UserItems, resume.FirstPrompt)
			}
		}
		t.message(reply, reply)
	case strings.Contains(text, FakeCodexHold), strings.Contains(text, FakeCodexStubborn):
		t.hold()
		return
	case strings.Contains(text, FakeCodexPartial):
		t.message(FakeSupervisorPartialText)
		t.item("item/started", map[string]any{"id": t.itemID(), "type": "commandExecution", "command": "sleep 600", "status": "inProgress"})
		t.hold()
		return
	case strings.Contains(text, FakeCodexPermBash), strings.Contains(text, FakeCodexPermHelper):
		command := FakeCodexBashCommand
		if strings.Contains(text, FakeCodexPermHelper) {
			command = FakeCodexHelperCommand
		}
		id := t.itemID()
		t.item("item/started", map[string]any{"id": id, "type": "commandExecution", "command": command, "status": "inProgress"})
		verdict := decision(t.s.request("item/commandExecution/requestApproval", map[string]any{
			"threadId": t.threadID, "turnId": t.turnID, "itemId": id, "command": command, "reason": "run " + command,
		}))
		if verdict == "accept" {
			t.item("item/completed", map[string]any{"id": id, "type": "commandExecution", "command": command, "status": "completed", "aggregatedOutput": FakeCodexBashOutput, "exitCode": 0})
		} else {
			t.item("item/completed", map[string]any{"id": id, "type": "commandExecution", "command": command, "status": "declined"})
		}
		t.message("Command " + verdict)
	case strings.Contains(text, FakeCodexPermWrite):
		id := t.itemID()
		changes := []map[string]any{{"path": FakeCodexWritePath, "kind": map[string]any{"type": "update"}, "diff": "@@ -1 +1 @@\n-a\n+b\n"}}
		t.item("item/started", map[string]any{"id": id, "type": "fileChange", "status": "inProgress", "changes": changes})
		verdict := decision(t.s.request("item/fileChange/requestApproval", map[string]any{
			"threadId": t.threadID, "turnId": t.turnID, "itemId": id, "reason": "edit " + FakeCodexWritePath,
		}))
		status := "completed"
		if verdict != "accept" {
			status = "declined"
		}
		t.item("item/completed", map[string]any{"id": id, "type": "fileChange", "status": status, "changes": changes})
		t.message("Change " + verdict)
	case strings.Contains(text, FakeCodexAsk):
		result := t.s.request("item/tool/requestUserInput", map[string]any{
			"threadId": t.threadID, "turnId": t.turnID, "itemId": t.itemID(),
			"questions": []map[string]any{{
				"id": "branch", "header": "Branch", "question": "Which branch?",
				"options": []map[string]string{{"label": "main", "description": "default"}, {"label": "dev", "description": "work"}},
			}},
		})
		var answer struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		}
		_ = json.Unmarshal(result, &answer)
		t.message("You chose " + strings.Join(answer.Answers["branch"].Answers, ","))
	default:
		reply := fmt.Sprintf("Hello from turn %d", t.n)
		t.message(reply, "Hello ", fmt.Sprintf("from turn %d", t.n))
	}
	if strings.Contains(text, FakeCodexCompact) {
		id := t.itemID()
		t.item("item/started", map[string]any{"id": id, "type": "contextCompaction"})
		t.s.appendRollout("compacted", map[string]any{
			"message": "", "window_id": fmt.Sprintf("window-%d", t.n),
			"replacement_history": []map[string]any{
				{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Compacted context"}}},
				{"type": "compaction", "encrypted_content": "opaque-fake-compaction"},
			},
		})
		t.item("item/completed", map[string]any{"id": id, "type": "contextCompaction"})
	}
	t.complete("completed")
}
