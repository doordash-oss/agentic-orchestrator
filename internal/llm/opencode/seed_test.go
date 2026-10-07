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

package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestPromptChangesModelBeforeTurnOnlyWhenNeeded(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "opencode:vendor/first"})
	p.acpSessionID = "ses_fresh"
	var out bytes.Buffer
	p.SetStdin(&out)
	if err := p.sendPrompt("first"); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("session/set_model")) {
		t.Fatal("unchanged model sent set_model")
	}
	out.Reset()
	p.SetPromptModel("opencode:vendor/second")
	if err := p.sendPrompt("second"); err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte{'\n'}) {
		var call struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	if len(calls) != 1 || calls[0].Method != "session/set_model" || calls[0].Params["modelId"] != "vendor/second" {
		t.Fatalf("calls = %+v", calls)
	}
	response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.modelChangeID, "result": map[string]any{}})
	if _, err := p.ParseLine(response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("session/prompt")) {
		t.Fatal("prompt was not sent after set_model response")
	}
}

func TestPromptModelRefusalUsesPreviousModel(t *testing.T) {
	p := NewProtocol(llm.ProtocolOpts{Model: "opencode:vendor/first"})
	p.acpSessionID = "ses_fresh"
	var out bytes.Buffer
	p.SetStdin(&out)
	p.SetPromptModel("opencode:vendor/second")
	if err := p.sendPrompt("hello"); err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.modelChangeID, "error": map[string]any{"code": -32000, "message": "rejected"}})
	if _, err := p.ParseLine(response); err != nil {
		t.Fatal(err)
	}
	if p.model != "vendor/first" || p.promptModel != "vendor/first" {
		t.Fatalf("model state after refusal = %q / %q", p.model, p.promptModel)
	}
	if !bytes.Contains(out.Bytes(), []byte("session/prompt")) {
		t.Fatal("prompt was not sent on previous model")
	}
}

func TestSeedHistoryPostsNoReplyToLiveSession(t *testing.T) {
	var got struct {
		NoReply bool `json:"noReply"`
		Parts   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session/ses_fresh/message" {
			t.Errorf("seed path = %q", r.URL.Path)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != serverUsername || pass != "secret" {
			t.Error("seed request lacked server credentials")
		}
		if got := r.Header.Get("x-opencode-directory"); got != url.PathEscape("/tmp/work dir") {
			t.Errorf("directory header = %q", got)
		}
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"info": map[string]string{"role": "user"}, "parts": []any{map[string]string{"type": "text", "text": "Prior history"}}}})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode seed: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]string{"sessionID": "ses_fresh", "role": "user"}})
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "seed.txt")
	if err := os.WriteFile(path, []byte("Prior history"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewProtocol(llm.ProtocolOpts{SeedHistoryPath: path})
	p.acpSessionID = "ses_fresh"
	p.bridge = newServerBridge(serverClient{addr: server.Listener.Addr().String(), password: "secret", directory: "/tmp/work dir", http: &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{}).DialContext}}}, nil)
	if err := p.seedHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !got.NoReply || len(got.Parts) != 1 || got.Parts[0].Type != "text" || got.Parts[0].Text != "Prior history" {
		t.Fatalf("seed body = %+v", got)
	}
}
