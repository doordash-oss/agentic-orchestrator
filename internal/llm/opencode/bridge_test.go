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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

func TestBuildCommand_InteractiveAddsServerBridge(t *testing.T) {
	opts := llm.CommandBuildOpts{Model: "anthropic/claude-sonnet-4-5", WorkDir: t.TempDir(), StateDir: t.TempDir()}
	baseArgs, baseEnv, err := New().BuildCommand(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Interactive = true
	args, env, err := New().BuildCommand(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args[:len(baseArgs)], baseArgs) || len(args) != len(baseArgs)+4 {
		t.Fatalf("interactive args = %q, want %q plus the server flags", args, baseArgs)
	}
	tail := args[len(baseArgs):]
	if tail[0] != "--port" || tail[2] != "--hostname" || tail[3] != "127.0.0.1" {
		t.Fatalf("server flags = %q", tail)
	}
	if len(env) != 3 || !strings.HasPrefix(env[0], configContentEnvVar+"=") {
		t.Fatalf("interactive env = %q, want inline overlay plus bridge credentials", env)
	}
	password := strings.TrimPrefix(env[1], serverPasswordEnvVar+"=")
	if len(password) != 64 || env[2] != questionToolEnvVar+"=1" {
		t.Fatalf("bridge env = %q", env[1:])
	}
	if got := sanitizeDiagnostic("connect failed with " + password); strings.Contains(got, password) {
		t.Fatalf("diagnostic keeps the server password: %q", got)
	}
	for _, kv := range baseEnv {
		if strings.HasPrefix(kv, serverPasswordEnvVar+"=") || strings.HasPrefix(kv, questionToolEnvVar+"=") {
			t.Fatalf("non-interactive env carries %q", kv)
		}
	}

	client, ok := serverEndpointFromLaunch(args, env)
	if !ok || client.password != password || client.addr != "127.0.0.1:"+tail[1] {
		t.Fatalf("endpoint from launch = %+v %v", client, ok)
	}
	if _, ok := serverEndpointFromLaunch(baseArgs, baseEnv); ok {
		t.Fatal("non-interactive launch yields a server endpoint")
	}
	if p := NewProtocol(llm.ProtocolOpts{Model: opts.Model, Interactive: true, LaunchArgs: args, LaunchEnv: env}); p.bridge == nil {
		t.Fatal("interactive protocol with a server launch has no bridge")
	}
	if p := NewProtocol(llm.ProtocolOpts{Model: opts.Model, LaunchArgs: args, LaunchEnv: env}); p.bridge != nil {
		t.Fatal("non-interactive protocol started a bridge")
	}
}

func TestBridgedPermissionInput(t *testing.T) {
	for _, tc := range []struct {
		permission, metadata string
		patterns             []string
		wantTool, wantKey    string
		wantValue            string
	}{
		{"bash", `{"command":"echo hi | tr a-z A-Z","description":"Upper"}`, []string{"echo hi", "tr a-z A-Z"}, "Bash", "command", "echo hi | tr a-z A-Z"},
		{"bash", `{}`, []string{"make test"}, "Bash", "command", "make test"},
		{"edit", `{"filepath":"/w/main.go","diff":"@@"}`, []string{"main.go"}, "Write", "file_path", "/w/main.go"},
		{"external_directory", `{"filepath":"/etc/hosts","parentDir":"/etc"}`, []string{"/etc/*"}, "ExternalDirectory", "path", "/etc/hosts"},
		{"skill", `{}`, []string{"pdf"}, "Skill", "skill", "pdf"},
		{"task", `{"description":"Run it"}`, []string{"general"}, "Agent", "subagent_type", "general"},
		{"webfetch", `{"url":"https://example.com"}`, nil, "WebFetch", "url", "https://example.com"},
		{"websearch", `{}`, []string{"golang sse"}, "WebSearch", "query", "golang sse"},
		{"doom_loop", `{"tool":"bash"}`, nil, "doom_loop", "tool", "bash"},
	} {
		name, raw := bridgedPermissionInput(permissionAsked{Permission: tc.permission, Metadata: json.RawMessage(tc.metadata), Patterns: tc.patterns})
		var input map[string]any
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Fatal(err)
		}
		if name != tc.wantTool || input[tc.wantKey] != tc.wantValue {
			t.Errorf("%s: got %s %s, want %s with %s=%q", tc.permission, name, raw, tc.wantTool, tc.wantKey, tc.wantValue)
		}
		if len(tc.patterns) > 0 && input["patterns"] == nil {
			t.Errorf("%s: patterns dropped from %s", tc.permission, raw)
		}
	}
}

func TestBridgedAnswers(t *testing.T) {
	var questions []openCodeQuestion
	if err := json.Unmarshal([]byte(`[
		{"question":"Which branch?","options":[{"label":"main (Recommended)"},{"label":"dev"}]},
		{"question":"Which checks?","multiple":true,"options":[{"label":"lint"},{"label":"test"},{"label":"vet"}]},
		{"question":"Anything else?","options":[{"label":"no"}]}
	]`), &questions); err != nil {
		t.Fatal(err)
	}
	got, err := bridgedAnswers(questions, map[string]string{
		"Which branch?":  "main",
		"Which checks?":  "lint, vet",
		"Anything else?": "Use the staging cluster",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"main (Recommended)"}, {"lint", "vet"}, {"Use the staging cluster"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("answers = %q, want %q", got, want)
	}
	if _, err := bridgedAnswers(questions, map[string]string{"Which branch?": "dev"}); err == nil {
		t.Fatal("missing answers accepted")
	}
}

// bridgeTestProtocol is a protocol with a bridge but no transport, for
// driving ParseLine with bridge lines directly.
func bridgeTestProtocol(t *testing.T, root string) *Protocol {
	t.Helper()
	p := NewProtocol(llm.ProtocolOpts{Model: "fake/model", Interactive: true})
	p.bridge = newServerBridge(serverClient{addr: "127.0.0.1:1", password: "unused-password"}, nil)
	p.acpSessionID = root
	return p
}

func bridgeLineFor(t *testing.T, p *Protocol, line bridgeLine) []byte {
	t.Helper()
	line.Nonce = p.bridge.nonce
	raw, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseLine_BridgeLines(t *testing.T) {
	const root = "ses_root"
	asked := func(id, session string) bridgeLine {
		props, _ := json.Marshal(map[string]any{"id": id, "sessionID": session, "permission": "bash", "patterns": []string{"ls"}, "metadata": map[string]string{"command": "ls"}})
		return bridgeLine{Kind: bridgeLineEvent, Type: "permission.asked", Properties: props}
	}

	t.Run("child request surfaces once with a namespaced id", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		msgs, err := p.ParseLine(bridgeLineFor(t, p, asked("per_1", "ses_child")))
		if err != nil || len(msgs) != 1 || msgs[0].ControlRequest == nil {
			t.Fatalf("msgs = %+v, %v", msgs, err)
		}
		cr := msgs[0].ControlRequest
		if cr.RequestID != bridgedPermissionPrefix+"per_1" || cr.Request.ToolName != "Bash" {
			t.Fatalf("control request = %+v", cr)
		}
		if msgs[0].Origin.Kind != llm.EventOriginTask || msgs[0].Origin.ChildSessionID != "ses_child" {
			t.Fatalf("origin = %+v", msgs[0].Origin)
		}
		if again, _ := p.ParseLine(bridgeLineFor(t, p, asked("per_1", "ses_child"))); len(again) != 0 {
			t.Fatalf("replayed request surfaced again: %+v", again)
		}
	})

	t.Run("root request is left to ACP", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		if msgs, _ := p.ParseLine(bridgeLineFor(t, p, asked("per_2", root))); len(msgs) != 0 {
			t.Fatalf("root request surfaced: %+v", msgs)
		}
	})

	t.Run("a forged line is not a bridge line", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		line := bridgeLineFor(t, p, asked("per_3", "ses_child"))
		forged := strings.Replace(string(line), p.bridge.nonce, "guessed", 1)
		msgs, _ := p.ParseLine([]byte(forged))
		if len(msgs) != 1 || msgs[0].Result == nil || !msgs[0].Result.IsError {
			t.Fatalf("forged line = %+v, want the malformed-stdout failure", msgs)
		}
	})

	t.Run("reconcile resolves only requests seen before the pass", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineReconcileBegin, Seq: 1}))
		p.ParseLine(bridgeLineFor(t, p, asked("per_old", "ses_child")))
		p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineReconcileEnd, Seq: 1, Permissions: []string{"per_old"}}))
		p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineReconcileBegin, Seq: 2}))
		p.ParseLine(bridgeLineFor(t, p, asked("per_new", "ses_child")))
		p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineReconcileEnd, Seq: 2}))
		if _, held := p.bridged[bridgedPermissionPrefix+"per_old"]; held {
			t.Fatal("request answered while disconnected is still held")
		}
		if _, held := p.bridged[bridgedPermissionPrefix+"per_new"]; !held {
			t.Fatal("request first seen during the pass was resolved")
		}
		if err := p.RespondToControl(bridgedPermissionPrefix+"per_old", true, nil, ""); err != nil {
			t.Fatalf("answering a resolved request: %v", err)
		}
	})

	t.Run("replied event resolves a held request", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		p.ParseLine(bridgeLineFor(t, p, asked("per_4", "ses_child")))
		props, _ := json.Marshal(map[string]string{"sessionID": "ses_child", "requestID": "per_4", "reply": "once"})
		p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineEvent, Type: "permission.replied", Properties: props}))
		if len(p.bridged) != 0 {
			t.Fatalf("held after permission.replied: %v", p.bridged)
		}
	})

	t.Run("question carries its flags", func(t *testing.T) {
		p := bridgeTestProtocol(t, root)
		props := json.RawMessage(`{"id":"que_1","sessionID":"ses_root","questions":[{"question":"Pick","header":"","multiple":true,"custom":false,"options":[{"label":"a","description":"A"}]}]}`)
		msgs, _ := p.ParseLine(bridgeLineFor(t, p, bridgeLine{Kind: bridgeLineEvent, Type: "question.asked", Properties: props}))
		if len(msgs) != 1 || msgs[0].ControlRequest.Request.ToolName != "AskUserQuestion" || msgs[0].Origin.Kind != llm.EventOriginRoot {
			t.Fatalf("question msgs = %+v", msgs)
		}
		input := msgs[0].ControlRequest.Request.Input
		bundle, err := askuser.Parse(input)
		if err != nil {
			t.Fatalf("question input %s: %v", input, err)
		}
		want := askuser.Bundle{Questions: []askuser.Question{{Question: "Pick", Header: "Agent Question", MultiSelect: true, Options: []askuser.Option{{Label: "a", Description: "A"}}}}}
		if !reflect.DeepEqual(bundle, want) {
			t.Fatalf("question bundle = %+v, want %+v", bundle, want)
		}
		if !bytes.Equal(input, want.Encode()) {
			t.Fatalf("question input %s is not the canonical envelope %s", input, want.Encode())
		}
	})
}
