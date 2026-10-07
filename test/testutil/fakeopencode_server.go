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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RunFakeOpenCodeIfRequested runs the fake `opencode acp` and exits when the
// process was launched as one; otherwise it returns at once. Call it first
// in TestMain of every package that launches FakeOpenCodeProvider sessions.
func RunFakeOpenCodeIfRequested() {
	if os.Getenv(FakeOpenCodeEnv) != "1" {
		return
	}
	os.Exit(runFakeOpenCode(os.Stdin, os.Stdout, os.Getenv(FakeOpenCodeScriptEnv), os.Args[1:]))
}

type fakeOpenCode struct {
	dir      string
	script   FakeOpenCodeScript
	launch   int
	password string

	outMu sync.Mutex
	out   *bufio.Writer

	mu          sync.Mutex
	rootID      string
	turns       int
	cancel      chan struct{}
	stubborn    bool
	nextReqID   int64
	waiters     map[int64]chan json.RawMessage
	seed        *FakeOpenCodeSeed
	seedPending bool
	subscribers map[chan []byte]struct{}
	eventsDown  bool
	pendingPerm map[string]*fakeOpenCodeRequest
	pendingQues map[string]*fakeOpenCodeRequest
	order       []string
	// listed closes when GET /question is first served, which ends a
	// client's first catch-up pass.
	listed     chan struct{}
	listedOnce sync.Once
}

// fakeOpenCodeRequest is a permission or question the fake holds until it is
// answered over HTTP.
type fakeOpenCodeRequest struct {
	props map[string]any
	// done receives the reply ("once", "always", "reject") or, for a
	// question, the JSON answers or "reject".
	done chan string
}

type fakeOpenCodeLine struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

func runFakeOpenCode(in io.Reader, out io.Writer, scriptPath string, args []string) int {
	if scriptPath == "" {
		fmt.Fprintln(os.Stderr, "fake opencode: "+FakeOpenCodeScriptEnv+" is not set")
		return 2
	}
	s := &fakeOpenCode{
		dir:         filepath.Dir(scriptPath),
		out:         bufio.NewWriter(out),
		password:    os.Getenv("OPENCODE_SERVER_PASSWORD"),
		waiters:     map[int64]chan json.RawMessage{},
		nextReqID:   1000,
		subscribers: map[chan []byte]struct{}{},
		pendingPerm: map[string]*fakeOpenCodeRequest{},
		pendingQues: map[string]*fakeOpenCodeRequest{},
		listed:      make(chan struct{}),
	}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake opencode: read script: %v\n", err)
		return 2
	}
	if err := json.Unmarshal(data, &s.script); err != nil {
		fmt.Fprintf(os.Stderr, "fake opencode: decode script: %v\n", err)
		return 2
	}
	s.launch = s.appendLine(FakeSupervisorInvocationsFile, "x")
	_ = os.WriteFile(filepath.Join(s.dir, FakeSupervisorArgvFile), []byte(strings.Join(args, "\n")+"\n"), 0o644)
	s.recordEnv()
	if s.script.ExitOnLaunch != 0 {
		return s.script.ExitOnLaunch
	}
	if addr := fakeOpenCodeListenAddr(args); addr != "" && !s.script.NoHTTP {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "fake opencode: listen %s: %v\n", addr, err)
			return 1
		}
		srv := &http.Server{Handler: s.httpHandler(), ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = srv.Serve(ln) }()
		go s.heartbeat()
		defer func() { _ = srv.Close() }()
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		raw := scanner.Bytes()
		var line fakeOpenCodeLine
		if err := json.Unmarshal(raw, &line); err != nil {
			fmt.Fprintf(os.Stderr, "fake opencode: bad line %q: %v\n", raw, err)
			continue
		}
		s.recordACP("in", line)
		s.dispatch(line)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "fake opencode: read stdin: %v\n", err)
		return 1
	}
	return 0
}

// fakeOpenCodeListenAddr returns host:port from --hostname and --port, or ""
// when the launch did not ask for the HTTP server.
func fakeOpenCodeListenAddr(args []string) string {
	var host, port string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--hostname":
			host = args[i+1]
		case "--port":
			port = args[i+1]
		}
	}
	if port == "" {
		return ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// appendLine appends one line to a file beside the script and returns the
// file's resulting line count.
func (s *fakeOpenCode) appendLine(name, line string) int {
	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = f.WriteString(line + "\n")
		_ = f.Close()
	}
	data, _ := os.ReadFile(path)
	return strings.Count(string(data), "\n")
}

func (s *fakeOpenCode) appendJSON(name string, v any) {
	raw, _ := json.Marshal(v)
	s.appendLine(name, string(raw))
}

func (s *fakeOpenCode) recordEnv() {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(key, "OPENCODE_") {
			continue
		}
		if key == "OPENCODE_SERVER_PASSWORD" && value != "" {
			value = FakeOpenCodeRedacted
		}
		env[key] = value
	}
	s.appendJSON(FakeOpenCodeEnvFile, FakeOpenCodeLaunchEnv{Launch: s.launch, Env: env})
}

func (s *fakeOpenCode) recordACP(dir string, line fakeOpenCodeLine) {
	s.appendJSON(FakeOpenCodeACPFile, FakeOpenCodeACPLine{Launch: s.launch, Dir: dir, ID: line.ID, Method: line.Method, Params: line.Params, Result: line.Result, Error: line.Error})
}

func (s *fakeOpenCode) send(v map[string]any) {
	raw, _ := json.Marshal(v)
	var line fakeOpenCodeLine
	_ = json.Unmarshal(raw, &line)
	s.outMu.Lock()
	defer s.outMu.Unlock()
	s.recordACP("out", line)
	_, _ = s.out.Write(raw)
	_ = s.out.WriteByte('\n')
	_ = s.out.Flush()
}

func (s *fakeOpenCode) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *fakeOpenCode) replyError(id json.RawMessage, code int, message string) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
}

func (s *fakeOpenCode) update(sessionID string, update map[string]any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": update}})
}

// request sends a server-initiated ACP request and waits for its result.
func (s *fakeOpenCode) request(method string, params any) json.RawMessage {
	s.mu.Lock()
	s.nextReqID++
	id := s.nextReqID
	ch := make(chan json.RawMessage, 1)
	s.waiters[id] = ch
	s.mu.Unlock()
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return <-ch
}

func (s *fakeOpenCode) mintID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_fake" + hex.EncodeToString(b)
}

func (s *fakeOpenCode) dispatch(line fakeOpenCodeLine) {
	hasID := len(line.ID) > 0 && string(line.ID) != "null"
	if line.Method == "" {
		var id int64
		if !hasID || json.Unmarshal(line.ID, &id) != nil {
			return
		}
		s.mu.Lock()
		ch := s.waiters[id]
		delete(s.waiters, id)
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
	if !hasID {
		if line.Method == "session/cancel" {
			s.mu.Lock()
			ch, stubborn := s.cancel, s.stubborn
			s.mu.Unlock()
			if ch != nil && !stubborn {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
		return
	}
	switch line.Method {
	case "initialize":
		s.reply(line.ID, map[string]any{
			"protocolVersion":   1,
			"agentInfo":         map[string]string{"name": "fake-opencode", "version": FakeOpenCodeVersion},
			"agentCapabilities": map[string]any{"loadSession": true},
			"authMethods":       []any{},
		})
	case "session/new":
		id := s.mintID("ses")
		s.mu.Lock()
		s.rootID = id
		s.mu.Unlock()
		s.appendJSON(FakeOpenCodeSessionsFile, FakeOpenCodeSession{Launch: s.launch, ID: id, Kind: "root"})
		s.reply(line.ID, map[string]any{"sessionId": id})
	case "session/load":
		var params struct {
			SessionID string `json:"sessionId"`
		}
		_ = json.Unmarshal(line.Params, &params)
		s.mu.Lock()
		s.rootID = params.SessionID
		s.mu.Unlock()
		s.appendJSON(FakeOpenCodeSessionsFile, FakeOpenCodeSession{Launch: s.launch, ID: params.SessionID, Kind: "loaded"})
		s.reply(line.ID, map[string]any{})
	case "session/set_model":
		var params struct {
			SessionID string `json:"sessionId"`
			ModelID   string `json:"modelId"`
		}
		_ = json.Unmarshal(line.Params, &params)
		s.appendJSON(FakeOpenCodeSetModelsFile, FakeOpenCodeSetModel{Launch: s.launch, Method: line.Method, SessionID: params.SessionID, ModelID: params.ModelID})
		if s.script.RejectSetModel {
			s.replyError(line.ID, -32000, "model switch rejected by fake OpenCode")
			return
		}
		s.reply(line.ID, map[string]any{})
	case "session/set_config_option":
		var params struct {
			SessionID string `json:"sessionId"`
			ConfigID  string `json:"configId"`
			Value     any    `json:"value"`
		}
		_ = json.Unmarshal(line.Params, &params)
		s.appendJSON(FakeOpenCodeSetModelsFile, FakeOpenCodeSetModel{Launch: s.launch, Method: line.Method, SessionID: params.SessionID, ConfigID: params.ConfigID, Value: fmt.Sprint(params.Value)})
		s.reply(line.ID, map[string]any{"configOptions": []any{}})
	case "session/prompt":
		s.prompt(line.ID, FakeOpenCodeACPLine{Params: line.Params}.PromptText())
	default:
		s.replyError(line.ID, -32601, "method not found: "+line.Method)
	}
}

// --- HTTP surface ---

func (s *fakeOpenCode) httpHandler() http.Handler {
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		writeFakeJSON(w, map[string]any{"healthy": true, "version": FakeOpenCodeVersion})
	}
	mux.HandleFunc("GET /global/health", health)
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /event", s.serveEvents)
	mux.HandleFunc("GET /permission", func(w http.ResponseWriter, _ *http.Request) {
		writeFakeJSON(w, s.pendingList(s.pendingPerm))
	})
	mux.HandleFunc("GET /question", func(w http.ResponseWriter, _ *http.Request) {
		writeFakeJSON(w, s.pendingList(s.pendingQues))
		s.listedOnce.Do(func() { close(s.listed) })
	})
	mux.HandleFunc("GET /session/{id}/message", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		seed := s.seed
		s.mu.Unlock()
		if seed == nil || seed.SessionID != r.PathValue("id") {
			writeFakeJSON(w, []any{})
			return
		}
		writeFakeJSON(w, []any{map[string]any{"info": map[string]string{"role": "user"}, "parts": []any{map[string]string{"type": "text", "text": seed.Text}}}})
	})
	mux.HandleFunc("POST /permission/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reply string `json:"reply"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || (body.Reply != "once" && body.Reply != "always" && body.Reply != "reject") {
			http.Error(w, "bad reply", http.StatusBadRequest)
			return
		}
		if !s.resolve(s.pendingPerm, r.PathValue("id"), body.Reply, "permission.replied", map[string]any{"reply": body.Reply}) {
			http.NotFound(w, r)
			return
		}
		writeFakeJSON(w, true)
	})
	mux.HandleFunc("POST /question/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answers [][]string `json:"answers"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Answers == nil {
			http.Error(w, "bad answers", http.StatusBadRequest)
			return
		}
		raw, _ := json.Marshal(body.Answers)
		if !s.resolve(s.pendingQues, r.PathValue("id"), string(raw), "question.replied", map[string]any{"answers": body.Answers}) {
			http.NotFound(w, r)
			return
		}
		writeFakeJSON(w, true)
	})
	mux.HandleFunc("POST /question/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		if !s.resolve(s.pendingQues, r.PathValue("id"), "reject", "question.rejected", nil) {
			http.NotFound(w, r)
			return
		}
		writeFakeJSON(w, true)
	})
	mux.HandleFunc("POST /session/{id}/message", s.servePrompt)
	return s.withAuth(mux)
}

func writeFakeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// fakeStatusWriter captures the status a handler wrote.
type fakeStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *fakeStatusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *fakeStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *fakeStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *fakeOpenCode) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body []byte
		if r.Body != nil && r.Method != http.MethodGet {
			body, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}
		user, pass, ok := r.BasicAuth()
		auth := "ok"
		switch {
		case !ok:
			auth = "missing"
		case s.script.RejectPassword || user != FakeOpenCodeServerUser || pass != s.password || s.password == "":
			auth = "rejected"
		}
		rec := FakeOpenCodeHTTPRequest{Launch: s.launch, Method: r.Method, Path: r.URL.Path, Auth: auth}
		if json.Valid(body) {
			rec.Body = body
		}
		if auth != "ok" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Secure Area"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			rec.Status = http.StatusUnauthorized
			s.appendJSON(FakeOpenCodeHTTPFile, rec)
			return
		}
		sw := &fakeStatusWriter{ResponseWriter: w}
		if r.URL.Path == "/event" {
			// Record the stream when it opens; it may never end.
			rec.Status = http.StatusOK
			s.mu.Lock()
			down := s.eventsDown
			s.mu.Unlock()
			if down {
				rec.Status = http.StatusServiceUnavailable
			}
			s.appendJSON(FakeOpenCodeHTTPFile, rec)
			next.ServeHTTP(sw, r)
			return
		}
		next.ServeHTTP(sw, r)
		rec.Status = sw.status
		s.appendJSON(FakeOpenCodeHTTPFile, rec)
	})
}

func (s *fakeOpenCode) serveEvents(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.eventsDown {
		s.mu.Unlock()
		http.Error(w, "event stream unavailable", http.StatusServiceUnavailable)
		return
	}
	ch := make(chan []byte, 64)
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	write := func(data []byte) bool {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !write(fakeOpenCodeEvent("server.connected", map[string]any{})) {
		return
	}
	for {
		select {
		case data, ok := <-ch:
			if !ok || !write(data) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func fakeOpenCodeEvent(kind string, props any) []byte {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	raw, _ := json.Marshal(map[string]any{"id": "evt_fake" + hex.EncodeToString(b), "type": kind, "properties": props})
	return raw
}

// publish delivers an event to every open stream.
func (s *fakeOpenCode) publish(kind string, props any) {
	data := fakeOpenCodeEvent(kind, props)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subscribers {
		select {
		case ch <- data:
		default:
		}
	}
}

func (s *fakeOpenCode) heartbeat() {
	for range time.Tick(500 * time.Millisecond) {
		s.publish("server.heartbeat", map[string]any{})
	}
}

// dropStreams ends every open event stream and refuses new ones until
// restoreStreams.
func (s *fakeOpenCode) dropStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventsDown = true
	for ch := range s.subscribers {
		close(ch)
		delete(s.subscribers, ch)
	}
}

func (s *fakeOpenCode) restoreStreams() {
	s.mu.Lock()
	s.eventsDown = false
	s.mu.Unlock()
}

func (s *fakeOpenCode) pendingList(set map[string]*fakeOpenCodeRequest) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, id := range s.order {
		if req, ok := set[id]; ok {
			out = append(out, req.props)
		}
	}
	return out
}

// hold registers a pending request, publishes it and returns its reply
// channel.
func (s *fakeOpenCode) hold(set map[string]*fakeOpenCodeRequest, kind string, props map[string]any) chan string {
	req := &fakeOpenCodeRequest{props: props, done: make(chan string, 1)}
	s.mu.Lock()
	set[props["id"].(string)] = req
	s.order = append(s.order, props["id"].(string))
	s.mu.Unlock()
	s.publish(kind, props)
	return req.done
}

// resolve releases a pending request and publishes its resolution event.
func (s *fakeOpenCode) resolve(set map[string]*fakeOpenCodeRequest, id, verdict, kind string, extra map[string]any) bool {
	s.mu.Lock()
	req, ok := set[id]
	delete(set, id)
	s.mu.Unlock()
	if !ok {
		return false
	}
	props := map[string]any{"sessionID": req.props["sessionID"], "requestID": id}
	for k, v := range extra {
		props[k] = v
	}
	s.publish(kind, props)
	req.done <- verdict
	return true
}

func (s *fakeOpenCode) servePrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NoReply bool `json:"noReply"`
		Parts   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad prompt", http.StatusBadRequest)
		return
	}
	if s.script.SeedError != 0 {
		http.Error(w, "prompt failed", s.script.SeedError)
		return
	}
	var text strings.Builder
	for _, part := range body.Parts {
		if part.Type == "text" {
			text.WriteString(part.Text)
		}
	}
	rec := FakeOpenCodeSeed{Launch: s.launch, SessionID: r.PathValue("id"), Text: text.String(), NoReply: body.NoReply}
	if body.NoReply {
		s.appendJSON(FakeOpenCodeSeedsFile, rec)
		s.mu.Lock()
		s.seed = &rec
		s.seedPending = true
		s.mu.Unlock()
	} else {
		s.appendJSON(FakeOpenCodeModelRepliesFile, rec)
	}
	writeFakeJSON(w, map[string]any{
		"info":  map[string]any{"id": s.mintID("msg"), "sessionID": rec.SessionID, "role": "user"},
		"parts": []any{},
	})
}

// fakeSeedSummary counts the role-labelled lines of a seed ("User:" or
// "Assistant:" at the start of a line, case-insensitive) and returns the
// text after the first user label.
func fakeSeedSummary(text string) (int, string) {
	n, first := 0, ""
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "user:"):
			n++
			if first == "" {
				first = strings.TrimSpace(line[len("user:"):])
			}
		case strings.HasPrefix(lower, "assistant:"):
			n++
		}
	}
	return n, first
}

func fakeSeedLastExchange(text string) (string, string) {
	var user, assistant string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "user:"):
			prompt := strings.TrimSpace(line[len("user:"):])
			if !strings.HasPrefix(prompt, "Agentico note:") {
				user = prompt
			}
		case strings.HasPrefix(lower, "assistant:"):
			assistant = strings.TrimSpace(line[len("assistant:"):])
		}
	}
	return user, assistant
}

// --- turns ---

type fakeOpenCodeTurn struct {
	s       *fakeOpenCode
	id      json.RawMessage
	root    string
	n       int
	cancel  chan struct{}
	toolSeq int
	msgID   string
}

func (s *fakeOpenCode) prompt(id json.RawMessage, text string) {
	s.mu.Lock()
	s.turns++
	t := &fakeOpenCodeTurn{s: s, id: id, root: s.rootID, n: s.turns, cancel: make(chan struct{}, 1)}
	t.msgID = fmt.Sprintf("msg_fake%d_%d", s.launch, s.turns)
	s.cancel = t.cancel
	s.stubborn = strings.Contains(text, FakeOpenCodeStubborn)
	var seed *FakeOpenCodeSeed
	if s.seedPending || strings.Contains(text, FakeOpenCodeSeeded) {
		seed = s.seed
		s.seedPending = false
		if seed == nil {
			seed = &FakeOpenCodeSeed{}
		}
	}
	s.mu.Unlock()
	go t.run(text, seed)
}

func (t *fakeOpenCodeTurn) toolID() string {
	t.toolSeq++
	return fmt.Sprintf("call_fake%d_%d", t.n, t.toolSeq)
}

func (t *fakeOpenCodeTurn) message(text string, chunks ...string) {
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	for _, c := range chunks {
		t.s.update(t.root, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": t.msgID, "content": map[string]string{"type": "text", "text": c}})
	}
}

func (t *fakeOpenCodeTurn) complete(stopReason string) {
	t.s.update(t.root, map[string]any{"sessionUpdate": "usage_update", "used": 1000 * t.n, "size": 200000, "cost": map[string]any{"amount": 0, "currency": "USD"}})
	t.s.mu.Lock()
	if t.s.cancel == t.cancel {
		t.s.cancel, t.s.stubborn = nil, false
	}
	t.s.mu.Unlock()
	t.s.reply(t.id, map[string]any{
		"stopReason": stopReason,
		"usage":      map[string]int{"totalTokens": 110 * t.n, "inputTokens": 100 * t.n, "outputTokens": 10 * t.n},
	})
}

// hold waits for session/cancel and reports the prompt cancelled. A
// stubborn turn never gets here: its cancels are dropped.
func (t *fakeOpenCodeTurn) hold() {
	<-t.cancel
	t.complete("cancelled")
}

func (t *fakeOpenCodeTurn) run(text string, seed *FakeOpenCodeSeed) {
	t.s.mu.Lock()
	history := t.s.seed
	t.s.mu.Unlock()
	switch {
	case strings.Contains(text, FakeSupervisorRecallFirst):
		first := ""
		if history != nil {
			_, first = fakeSeedSummary(history.Text)
		}
		t.message("First user prompt: " + first)
	case strings.Contains(text, FakeSupervisorRecallLast):
		user, assistant := "", ""
		if history != nil {
			user, assistant = fakeSeedLastExchange(history.Text)
		}
		t.message("Last exchange: " + user + " | " + assistant)
	case seed != nil:
		n, first := fakeSeedSummary(seed.Text)
		t.message(fmt.Sprintf("Seeded with %d prior messages: %s", n, first))
	case strings.Contains(text, FakeOpenCodeHold), strings.Contains(text, FakeOpenCodeStubborn):
		t.hold()
		return
	case strings.Contains(text, FakeOpenCodePartial):
		t.message(FakeSupervisorPartialText)
		t.s.update(t.root, map[string]any{"sessionUpdate": "tool_call", "toolCallId": t.toolID(), "title": "bash", "kind": "execute", "status": "in_progress", "rawInput": map[string]string{"command": "sleep 600"}})
		t.hold()
		return
	case strings.Contains(text, FakeOpenCodeChildPermBash):
		t.childPermission()
	case strings.Contains(text, FakeOpenCodeChildAsk):
		t.childQuestion()
	case strings.Contains(text, FakeOpenCodePermBash), strings.Contains(text, FakeOpenCodePermHelper):
		command := FakeOpenCodeBashCommand
		if strings.Contains(text, FakeOpenCodePermHelper) {
			command = FakeOpenCodeHelperCommand
		}
		t.rootPermission("Command", "execute", "bash", map[string]any{"command": command, "description": "Run " + command}, []string{command})
	case strings.Contains(text, FakeOpenCodePermEdit):
		t.rootPermission("Edit", "edit", "edit", map[string]any{"filePath": FakeOpenCodeEditPath}, []string{FakeOpenCodeEditPath})
	case strings.Contains(text, FakeOpenCodePermTask):
		t.rootPermission("Task", "think", "task", map[string]any{"description": "Run the tests", "subagent_type": "general", "prompt": "run " + FakeOpenCodeBashCommand}, []string{"general"})
	case strings.Contains(text, FakeOpenCodeAsk):
		answer := <-t.s.hold(t.s.pendingQues, "question.asked", t.questionProps(t.root))
		if answer == "reject" {
			t.message("Question rejected")
		} else {
			t.message("You chose " + fakeAnswerLabels(answer))
		}
	default:
		t.message(fmt.Sprintf("Hello from turn %d", t.n), "Hello ", fmt.Sprintf("from turn %d", t.n))
	}
	t.complete("end_turn")
}

func fakeAnswerLabels(raw string) string {
	var answers [][]string
	_ = json.Unmarshal([]byte(raw), &answers)
	var labels []string
	for _, a := range answers {
		labels = append(labels, a...)
	}
	return strings.Join(labels, ",")
}

// rootPermission asks over ACP, mirrors the request on the HTTP stream as
// OpenCode does, and reports the verdict.
func (t *fakeOpenCodeTurn) rootPermission(label, kind, permission string, input map[string]any, patterns []string) {
	callID := t.toolID()
	t.s.update(t.root, map[string]any{"sessionUpdate": "tool_call", "toolCallId": callID, "title": permission, "kind": kind, "status": "pending", "rawInput": input})
	httpID := t.s.mintID("per")
	httpProps := map[string]any{
		"id": httpID, "sessionID": t.root, "permission": permission, "patterns": patterns,
		"metadata": input, "always": patterns, "tool": map[string]string{"messageID": t.msgID, "callID": callID},
	}
	t.s.publish("permission.asked", httpProps)
	result := t.s.request("session/request_permission", map[string]any{
		"sessionId": t.root,
		"toolCall":  map[string]any{"toolCallId": callID, "title": permission, "kind": kind, "rawInput": input},
		"options": []map[string]string{
			{"optionId": "once", "kind": "allow_once", "name": "Allow once"},
			{"optionId": "always", "kind": "allow_always", "name": "Always allow"},
			{"optionId": "reject", "kind": "reject_once", "name": "Reject"},
		},
	})
	var outcome struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}
	_ = json.Unmarshal(result, &outcome)
	reply := "reject"
	if outcome.Outcome.Outcome == "selected" && outcome.Outcome.OptionID != "reject" {
		reply = outcome.Outcome.OptionID
	}
	t.s.publish("permission.replied", map[string]any{"sessionID": t.root, "requestID": httpID, "reply": reply})
	status, verdict := "completed", "allowed"
	if reply == "reject" {
		status, verdict = "failed", "denied"
	}
	t.s.update(t.root, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": status})
	t.message(label + " " + verdict)
}

func (t *fakeOpenCodeTurn) questionProps(sessionID string) map[string]any {
	return map[string]any{
		"id": t.s.mintID("que"), "sessionID": sessionID,
		"questions": []map[string]any{{
			"question": FakeOpenCodeQuestion, "header": "Branch", "multiple": false, "custom": true,
			"options": []map[string]string{{"label": "main", "description": "default"}, {"label": "dev", "description": "work"}},
		}},
		"tool": map[string]string{"messageID": t.msgID, "callID": "question:0"},
	}
}

// startTask emits the root task tool call and mints the child session it
// runs in.
func (t *fakeOpenCodeTurn) startTask() (callID, child string) {
	callID = t.toolID()
	t.s.update(t.root, map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": callID, "title": "task", "kind": "think", "status": "in_progress",
		"rawInput": map[string]string{"description": "Run the tests", "subagent_type": "general", "prompt": "run " + FakeOpenCodeBashCommand},
	})
	child = t.s.mintID("ses")
	t.s.appendJSON(FakeOpenCodeSessionsFile, FakeOpenCodeSession{Launch: t.s.launch, ID: child, Kind: "child", ParentID: t.root})
	t.s.publish("session.created", map[string]any{"info": map[string]any{"id": child, "parentID": t.root, "title": "Run the tests"}})
	return callID, child
}

func (t *fakeOpenCodeTurn) finishTask(callID, child, summary string) {
	t.s.update(child, map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg_child", "content": map[string]string{"type": "text", "text": summary}})
	t.s.update(t.root, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "completed", "rawOutput": map[string]string{"output": summary}})
}

// publishChild holds a child request, honouring the disconnect and
// answered-elsewhere switches, and returns its verdict.
func (t *fakeOpenCodeTurn) publishChild(set map[string]*fakeOpenCodeRequest, kind string, props map[string]any, elsewhere string, extra map[string]any) string {
	resolvedKind := "permission.replied"
	if kind == "question.asked" {
		resolvedKind = "question.replied"
	}
	if t.s.script.ChildWhileDisconnected {
		// Publish only after the client's first catch-up, so the request can
		// reach it only through a later one.
		select {
		case <-t.s.listed:
		case <-time.After(5 * time.Second):
		}
		t.s.dropStreams()
		done := t.s.hold(set, kind, props)
		time.Sleep(300 * time.Millisecond)
		t.s.restoreStreams()
		return <-done
	}
	done := t.s.hold(set, kind, props)
	if t.s.script.ResolveChildElsewhere {
		go func() {
			time.Sleep(200 * time.Millisecond)
			t.s.resolve(set, props["id"].(string), elsewhere, resolvedKind, extra)
		}()
	}
	return <-done
}

func (t *fakeOpenCodeTurn) childPermission() {
	callID, child := t.startTask()
	props := map[string]any{
		"id": t.s.mintID("per"), "sessionID": child, "permission": "bash",
		"patterns": []string{FakeOpenCodeBashCommand},
		"metadata": map[string]string{"command": FakeOpenCodeBashCommand, "description": "Run the tests"},
		"always":   []string{"make *"},
		"tool":     map[string]string{"messageID": "msg_child", "callID": "bash:0"},
	}
	reply := t.publishChild(t.s.pendingPerm, "permission.asked", props, "once", map[string]any{"reply": "once"})
	verdict := "allowed"
	if reply == "reject" {
		verdict = "denied"
	}
	t.finishTask(callID, child, "Child "+verdict+" "+FakeOpenCodeBashCommand)
	t.message("Child " + verdict)
}

func (t *fakeOpenCodeTurn) childQuestion() {
	callID, child := t.startTask()
	props := t.questionProps(child)
	answer := t.publishChild(t.s.pendingQues, "question.asked", props, `[["main"]]`, map[string]any{"answers": [][]string{{"main"}}})
	reply := "Child question rejected"
	if answer != "reject" {
		reply = "Child chose " + fakeAnswerLabels(answer)
	}
	t.finishTask(callID, child, reply)
	t.message(reply)
}
