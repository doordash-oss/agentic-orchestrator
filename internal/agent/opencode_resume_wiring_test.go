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

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	opencode "github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// ocHandshakeDriver feeds inbound ACP lines to a real opencode protocol and
// reads its outbound JSON-RPC requests off the stdin pipe — enough of a client
// to walk a handshake without live OpenCode.
type ocHandshakeDriver struct {
	proto *opencode.Protocol
	out   chan []byte
}

type ocOutboundReq struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params json.RawMessage `json:"params"`
}

func newOCHandshakeDriver(t *testing.T, proto *opencode.Protocol) *ocHandshakeDriver {
	t.Helper()
	pr, pw := io.Pipe()
	proto.SetStdin(pw)
	out := make(chan []byte, 16)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			out <- append([]byte(nil), sc.Bytes()...)
		}
		close(out)
	}()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	return &ocHandshakeDriver{proto: proto, out: out}
}

func (d *ocHandshakeDriver) nextRequest(t *testing.T) ocOutboundReq {
	t.Helper()
	select {
	case b, ok := <-d.out:
		if !ok {
			t.Fatal("opencode protocol stdin closed before expected request")
		}
		var req ocOutboundReq
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatalf("outbound line is not JSON-RPC: %q: %v", b, err)
		}
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for outbound opencode request")
		return ocOutboundReq{}
	}
}

func (d *ocHandshakeDriver) feed(t *testing.T, line []byte) {
	t.Helper()
	if _, err := d.proto.ParseLine(line); err != nil {
		t.Fatalf("ParseLine(%s) error: %v", line, err)
	}
}

func ocResponse(t *testing.T, id json.RawMessage, result any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return b
}

// TestOpenCodeBuildSessionThreadsResumeIntoProtocolHandshake is the production
// boundary regression test for resume identity. It proves that an explicit
// "opencode:" session built with BuildSessionOpts.ResumeSessionID resumes via the
// ACP session/load handshake — the only path OpenCode resumes through. Resume is
// carried solely on llm.ProtocolOpts (OpenCode does not resume via a CLI flag), so
// if BuildSession drops ResumeSessionID at the ProtocolOpts boundary the protocol
// would start a fresh session/new instead, which this test rejects.
func TestOpenCodeBuildSessionThreadsResumeIntoProtocolHandshake(t *testing.T) {
	dir := t.TempDir()
	eventCh := make(chan interface{}, 8)
	sm := session.NewManager(eventCh)
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(sm, store, dir)
	pr.Registry = newRegistryWithOpenCode()

	_, _, sessOpts, err := pr.BuildSession(BuildSessionOpts{
		Model:           "opencode:anthropic/claude-sonnet-4-5",
		Prompt:          "RESUMED PHASE PROMPT",
		SystemPrompt:    "system prompt",
		MarkerPath:      filepath.Join(dir, "phase_complete"),
		WorkDir:         dir,
		ResumeSessionID: "ses_prior",
	})
	if err != nil {
		t.Fatalf("BuildSession() error: %v", err)
	}
	proto, ok := sessOpts.Protocol.(*opencode.Protocol)
	if !ok {
		t.Fatalf("BuildSession() Protocol type = %T, want *opencode.Protocol", sessOpts.Protocol)
	}

	d := newOCHandshakeDriver(t, proto)
	errCh := make(chan error, 1)
	go func() { errCh <- proto.Handshake(context.Background()) }()

	// 1. initialize — answer advertising the loadSession capability.
	initReq := d.nextRequest(t)
	if initReq.Method != "initialize" {
		t.Fatalf("first request method = %q, want initialize", initReq.Method)
	}
	d.feed(t, ocResponse(t, initReq.ID, map[string]any{
		"protocolVersion":   1,
		"agentInfo":         map[string]any{"name": "OpenCode", "version": "1.17.9"},
		"agentCapabilities": map[string]any{"loadSession": true},
	}))

	// 2. The session-establishing request MUST be session/load carrying the
	//    resume id — proving BuildSessionOpts.ResumeSessionID reached the
	//    protocol. A dropped field would surface here as session/new.
	loadReq := d.nextRequest(t)
	if loadReq.Method != "session/load" {
		t.Fatalf("session-establishing request = %q, want session/load (resume id reached the protocol)", loadReq.Method)
	}
	var lp struct {
		SessionID string `json:"sessionId"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(loadReq.Params, &lp); err != nil {
		t.Fatalf("unmarshal session/load params: %v", err)
	}
	if lp.SessionID != "ses_prior" {
		t.Fatalf("session/load sessionId = %q, want ses_prior", lp.SessionID)
	}
	if lp.Cwd != dir {
		t.Fatalf("session/load cwd = %q, want %q", lp.Cwd, dir)
	}
	d.feed(t, ocResponse(t, loadReq.ID, map[string]any{}))

	// 3. The next prompt goes to the resumed identity, not a hidden new session.
	promptReq := d.nextRequest(t)
	if promptReq.Method != "session/prompt" {
		t.Fatalf("third request = %q, want session/prompt", promptReq.Method)
	}
	var pp struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(promptReq.Params, &pp); err != nil {
		t.Fatalf("unmarshal session/prompt params: %v", err)
	}
	if pp.SessionID != "ses_prior" {
		t.Fatalf("resumed prompt sessionId = %q, want ses_prior (no hidden new session)", pp.SessionID)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Handshake() error: %v", err)
	}
	if got := proto.SessionID(); got != "ses_prior" {
		t.Fatalf("SessionID() = %q, want ses_prior", got)
	}
}
