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

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// wire captures what the protocol writes, one JSON-RPC message per line.
type wire struct {
	mu    sync.Mutex
	lines []map[string]json.RawMessage
}

func (w *wire) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		var m map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &m) == nil {
			w.lines = append(w.lines, m)
		}
	}
	return len(p), nil
}

func (w *wire) methods() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, l := range w.lines {
		var m string
		_ = json.Unmarshal(l["method"], &m)
		out = append(out, m)
	}
	return out
}

// await returns the id and params of the first written request for method.
func (w *wire) await(t *testing.T, method string) (string, json.RawMessage) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, l := range w.lines {
			if string(l["method"]) == `"`+method+`"` {
				w.mu.Unlock()
				return string(l["id"]), l["params"]
			}
		}
		w.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("protocol never wrote %s; wrote %v", method, w.methods())
	return "", nil
}

func parse(t *testing.T, p *Protocol, line string) []llm.SDKMessage {
	t.Helper()
	msgs, err := p.ParseLine([]byte(line))
	if err != nil {
		t.Fatalf("ParseLine(%s): %v", line, err)
	}
	return msgs
}

// handshake runs Handshake against scripted responses: thread answers the
// thread request (by method) and returns the line to feed back.
func handshake(t *testing.T, opts llm.ProtocolOpts, thread func(method, id string) string) (*Protocol, *wire, []llm.SDKMessage, error) {
	t.Helper()
	p := NewProtocol(opts)
	w := &wire{}
	p.SetStdin(w)
	done := make(chan error, 1)
	go func() { done <- p.Handshake(context.Background()) }()
	id, _ := w.await(t, "initialize")
	var msgs []llm.SDKMessage
	msgs = append(msgs, parse(t, p, `{"id":`+id+`,"result":{"userAgent":"fake"}}`)...)
	method := "thread/start"
	if opts.ResumeSessionID != "" {
		method = "thread/resume"
	}
	id, _ = w.await(t, method)
	msgs = append(msgs, parse(t, p, thread(method, id))...)
	if method == "thread/resume" {
		if starts := w.methods(); strings.Contains(strings.Join(starts, ","), "thread/start") {
			id, _ = w.await(t, "thread/start")
			msgs = append(msgs, parse(t, p, thread("thread/start", id))...)
		}
	}
	select {
	case err := <-done:
		return p, w, msgs, err
	case <-time.After(5 * time.Second):
		t.Fatal("handshake did not return")
	}
	return nil, nil, nil, nil
}

func threadOK(threadID, model string) func(string, string) string {
	return func(_, id string) string {
		return `{"id":` + id + `,"result":{"thread":{"id":"` + threadID + `"},"model":"` + model + `"}}`
	}
}

func inits(msgs []llm.SDKMessage) []*llm.SystemInitMessage {
	var out []*llm.SystemInitMessage
	for _, m := range msgs {
		if m.Init != nil {
			out = append(out, m.Init)
		}
	}
	return out
}

func TestCodexInteractiveHandshakeSkipsEmptyTurnAndReportsThread(t *testing.T) {
	p, w, msgs, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "gpt-x[200K]", Interactive: true}, threadOK("th-1", "gpt-effective"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.methods(), ","); got != "initialize,initialized,thread/start" {
		t.Fatalf("wrote %s, want no turn/start before the first user message", got)
	}
	got := inits(msgs)
	if len(got) != 1 || got[0].SessionID != "th-1" || got[0].Model != "gpt-effective" || got[0].PermissionMode != "" || got[0].ResumeOutcome != "" {
		t.Fatalf("init messages = %+v", got)
	}
	if p.SessionID() != "th-1" {
		t.Fatalf("SessionID = %q", p.SessionID())
	}
	if err := p.SendUserMessage("first"); err != nil {
		t.Fatal(err)
	}
	_, params := w.await(t, "turn/start")
	if !strings.Contains(string(params), `"text":"first"`) || !strings.Contains(string(params), `"model":"gpt-x"`) {
		t.Fatalf("first turn/start = %s", params)
	}
}

func TestCodexNonInteractiveHandshakeKeepsInitialTurnAndNoInit(t *testing.T) {
	_, w, msgs, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "gpt-x", InitialPrompt: "do it"}, threadOK("th-1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(w.methods(), ","); got != "initialize,initialized,thread/start,turn/start" {
		t.Fatalf("wrote %s", got)
	}
	if len(inits(msgs)) != 0 || len(msgs) != 0 {
		t.Fatalf("non-interactive handshake emitted %+v", msgs)
	}
}

func TestCodexResumeOutcomes(t *testing.T) {
	storeErr := func(method, id string) string {
		if method == "thread/resume" {
			return `{"id":` + id + `,"error":{"code":-32603,"message":"failed to load thread"}}`
		}
		return `{"id":` + id + `,"result":{"thread":{"id":"th-fresh"}}}`
	}
	t.Run("resumed", func(t *testing.T) {
		_, w, msgs, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "m", Interactive: true, ResumeSessionID: "th-old"}, threadOK("th-old", ""))
		if err != nil {
			t.Fatal(err)
		}
		got := inits(msgs)
		if len(got) != 1 || got[0].SessionID != "th-old" || got[0].ResumeOutcome != llm.ResumeOutcomeResumed || got[0].Model != "m" {
			t.Fatalf("init = %+v", got)
		}
		if strings.Contains(strings.Join(w.methods(), ","), "turn/start") {
			t.Fatalf("resume sent a turn before the first message: %v", w.methods())
		}
	})
	t.Run("thread store failure falls back to a fresh thread", func(t *testing.T) {
		p, w, msgs, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "m", Interactive: true, ResumeSessionID: "th-old"}, storeErr)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(w.methods(), ","); got != "initialize,initialized,thread/resume,thread/start" {
			t.Fatalf("wrote %s", got)
		}
		got := inits(msgs)
		if len(got) != 1 || got[0].SessionID != "th-fresh" || got[0].ResumeOutcome != llm.ResumeOutcomeFallback {
			t.Fatalf("init = %+v", got)
		}
		for _, m := range msgs {
			if m.Result != nil {
				t.Fatalf("fallback surfaced a result: %+v", m.Result)
			}
		}
		if p.SessionID() != "th-fresh" {
			t.Fatalf("SessionID = %q", p.SessionID())
		}
	})
	t.Run("thread store failure fails a non-interactive resume", func(t *testing.T) {
		_, _, _, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "m", ResumeSessionID: "th-old"}, storeErr)
		if err == nil || !strings.Contains(err.Error(), "thread/resume") {
			t.Fatalf("handshake error = %v", err)
		}
	})
	t.Run("other resume error fails the handshake promptly", func(t *testing.T) {
		_, _, _, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "m", Interactive: true, ResumeSessionID: "th-old"}, func(_, id string) string {
			return `{"id":` + id + `,"error":{"code":-32600,"message":"no such thread"}}`
		})
		if err == nil || !strings.Contains(err.Error(), "code -32600") || !strings.Contains(err.Error(), "no such thread") {
			t.Fatalf("handshake error = %v", err)
		}
	})
	t.Run("thread start error fails the handshake promptly", func(t *testing.T) {
		_, _, _, err := handshake(t, llm.ProtocolOpts{WorkDir: "/w", Model: "m", Interactive: true}, func(_, id string) string {
			return `{"id":` + id + `,"error":{"code":-32600,"message":"bad model"}}`
		})
		if err == nil || !strings.Contains(err.Error(), "thread/start") {
			t.Fatalf("handshake error = %v", err)
		}
	})
}

const (
	cmdStarted   = `{"method":"item/started","params":{"threadId":"th","turnId":"tu","item":{"id":"c1","type":"commandExecution","command":"make test","status":"inProgress"}}}`
	cmdCompleted = `{"method":"item/completed","params":{"threadId":"th","turnId":"tu","item":{"id":"c1","type":"commandExecution","command":"make test","status":"completed","aggregatedOutput":"ok\n","exitCode":2}}}`
	fcStarted    = `{"method":"item/started","params":{"threadId":"th","turnId":"tu","item":{"id":"f1","type":"fileChange","status":"inProgress","changes":[{"path":"main.go","kind":{"type":"update"}}]}}}`
	fcCompleted  = `{"method":"item/completed","params":{"threadId":"th","turnId":"tu","item":{"id":"f1","type":"fileChange","status":"completed","changes":[{"path":"main.go","kind":{"type":"update"}}]}}}`
	childStarted = `{"method":"item/started","params":{"threadId":"child","turnId":"tu","item":{"id":"c9","type":"commandExecution","command":"ls"}}}`
)

func toolBlocks(msgs []llm.SDKMessage) []llm.ContentBlock {
	var out []llm.ContentBlock
	for _, m := range msgs {
		switch {
		case m.Assistant != nil:
			out = append(out, m.Assistant.Message.Content...)
		case m.User != nil:
			out = append(out, m.User.Message.Content...)
		}
	}
	return out
}

func TestCodexInteractiveToolActivityBecomesToolHistory(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w", Interactive: true})
	p.SetThreadIDForTest("th")
	var msgs []llm.SDKMessage
	for _, line := range []string{cmdStarted, cmdCompleted, fcStarted, fcCompleted, childStarted} {
		msgs = append(msgs, parse(t, p, line)...)
	}
	progress := 0
	for _, m := range msgs {
		if m.ToolProgress != nil {
			progress++
		}
	}
	if progress != 4 {
		t.Fatalf("progress messages = %d, want the four root items' progress kept", progress)
	}
	blocks := toolBlocks(msgs)
	if len(blocks) != 4 {
		t.Fatalf("tool blocks = %+v", blocks)
	}
	if b := blocks[0]; b.Type != "tool_use" || b.ID != "c1" || b.Name != "Bash" || string(b.Input) != `{"command":"make test"}` {
		t.Fatalf("command tool_use = %+v", b)
	}
	if b := blocks[1]; b.Type != "tool_result" || b.ToolUseID != "c1" || string(b.Content) != `"ok\n"` || !b.IsError {
		t.Fatalf("command tool_result = %+v (%s)", b, b.Content)
	}
	if b := blocks[2]; b.Type != "tool_use" || b.Name != "Write" || !strings.Contains(string(b.Input), `"file_path":"main.go"`) {
		t.Fatalf("file tool_use = %+v (%s)", b, b.Input)
	}
	if b := blocks[3]; b.Type != "tool_result" || b.ToolUseID != "f1" || string(b.Content) != `"update main.go"` || b.IsError {
		t.Fatalf("file tool_result = %+v (%s)", b, b.Content)
	}
}

func TestCodexNonInteractiveToolActivityStaysProgressOnly(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w"})
	p.SetThreadIDForTest("th")
	for _, line := range []string{cmdStarted, cmdCompleted, fcStarted, fcCompleted} {
		msgs := parse(t, p, line)
		if len(msgs) != 1 || msgs[0].ToolProgress == nil {
			t.Fatalf("%s produced %+v, want exactly one progress message", line, msgs)
		}
	}
}

func TestCodexInterruptSendsTurnInterruptForInteractiveTurn(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w", Interactive: true})
	w := &wire{}
	p.SetStdin(w)
	p.SetThreadIDForTest("th")
	if err := p.Interrupt(); !errors.Is(err, llm.ErrNotSupported) {
		t.Fatalf("idle Interrupt = %v, want ErrNotSupported", err)
	}
	if err := p.SendUserMessage("hold"); err != nil {
		t.Fatal(err)
	}
	turnReq, _ := w.await(t, "turn/start")
	parse(t, p, `{"id":`+turnReq+`,"result":{"turn":{"id":"tu-7","status":"inProgress"}}}`)
	if err := p.Interrupt(); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}
	id, params := w.await(t, "turn/interrupt")
	if string(params) != `{"threadId":"th","turnId":"tu-7"}` {
		t.Fatalf("turn/interrupt params = %s", params)
	}
	if msgs := parse(t, p, `{"id":`+id+`,"result":{}}`); len(msgs) != 0 {
		t.Fatalf("interrupt response emitted %+v", msgs)
	}
	msgs := parse(t, p, `{"method":"turn/completed","params":{"threadId":"th","turn":{"id":"tu-7","status":"interrupted"}}}`)
	if len(msgs) != 1 || msgs[0].Result == nil || msgs[0].Result.Result != "Turn interrupted" {
		t.Fatalf("interrupted completion = %+v", msgs)
	}
	if err := p.Interrupt(); !errors.Is(err, llm.ErrNotSupported) {
		t.Fatalf("Interrupt after the turn = %v, want ErrNotSupported", err)
	}
}

func TestCodexNonInteractiveInterruptIsNotSupportedDuringTurn(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{WorkDir: "/w"})
	p.SetStdin(&wire{})
	p.SetThreadIDForTest("th")
	if err := p.SendUserMessage("work"); err != nil {
		t.Fatal(err)
	}
	if err := p.Interrupt(); !errors.Is(err, llm.ErrNotSupported) {
		t.Fatalf("Interrupt = %v, want ErrNotSupported", err)
	}
}
