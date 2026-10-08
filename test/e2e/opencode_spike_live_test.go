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
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// The spike drives `opencode acp` with raw ACP and raw HTTP, using no Agentico
// adapter code, to prove the premise the supervisor's child-session bridge
// rests on: a sub-agent (child session) permission request is invisible to
// ACP, appears on the embedded server's event stream under the child's session
// id, and is released by an HTTP reply.

// TestOpenCodeChildPermissionSpikeLiveMinVersion runs the round trip on the
// pinned minimum release, the gate for the bridge. A missing or mismatched
// binary fails rather than skips once the live variable is set.
func TestOpenCodeChildPermissionSpikeLiveMinVersion(t *testing.T) {
	if os.Getenv(openCodeLiveEnvVar) != "1" {
		t.Skipf("set %s=1 to run the OpenCode child-permission spike on the pinned minimum %s", openCodeLiveEnvVar, openCodeMinVersion())
	}
	bin := resolveOpenCodeMinBin(t)
	if bin == "" {
		t.Fatalf("pinned minimum OpenCode %s not found: %s", openCodeMinVersion(), openCodeMinBinFetchHint())
	}
	box := newOpenCodeSandbox(t, true)
	got, err := openCodeBinaryVersion(t, bin, box.env)
	if err != nil {
		t.Fatalf("%s --version: %v; %s", bin, err, openCodeMinBinFetchHint())
	}
	if got != openCodeMinVersion() {
		t.Fatalf("%s reports OpenCode %s, want the provider minimum %s; %s", bin, got, openCodeMinVersion(), openCodeMinBinFetchHint())
	}
	t.Logf("tested OpenCode version: %s (%s)", got, bin)
	runChildPermissionSpike(t, bin, box)
}

// TestOpenCodeChildPermissionSpikeLiveInstalled runs the same round trip on
// the CLI installed on PATH.
func TestOpenCodeChildPermissionSpikeLiveInstalled(t *testing.T) {
	if os.Getenv(openCodeLiveEnvVar) != "1" {
		t.Skipf("set %s=1 to run the OpenCode child-permission spike on the installed CLI", openCodeLiveEnvVar)
	}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		t.Fatalf("opencode not on PATH: %v", err)
	}
	box := newOpenCodeSandbox(t, true)
	got, err := openCodeBinaryVersion(t, bin, box.env)
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	t.Logf("tested OpenCode version: %s (%s)", got, bin)
	runChildPermissionSpike(t, bin, box)
}

// TestOpenCodeAcpHTTPProbe is the no-inference half of the spike: the HTTP
// server beside ACP answers on the chosen port, enforces the password, streams
// events and goes away with the process when stdin closes. It runs on the
// installed CLI and, when cached, on the pinned minimum.
func TestOpenCodeAcpHTTPProbe(t *testing.T) {
	bins := map[string]string{}
	if bin, err := exec.LookPath("opencode"); err == nil {
		bins["installed"] = bin
	}
	if bin := resolveOpenCodeMinBin(t); bin != "" {
		bins["min"] = bin
	}
	if len(bins) == 0 {
		t.Skip("no OpenCode CLI on PATH or in the repo cache")
	}
	for name, bin := range bins {
		t.Run(name, func(t *testing.T) {
			box := newOpenCodeSandbox(t, false)
			version, err := openCodeBinaryVersion(t, bin, box.env)
			if err != nil {
				t.Fatalf("%s --version: %v", bin, err)
			}
			t.Logf("probing OpenCode %s (%s)", version, bin)
			work := t.TempDir()
			a := startRawACP(t, bin, box.env, work, map[string]any{"autoupdate": false})
			a.waitHealthy(time.Minute)
			if code, _, err := a.httpStatus(http.MethodGet, "/global/health", nil, false); err != nil || code != http.StatusUnauthorized {
				t.Fatalf("health without the password = %d, %v; want 401", code, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			events, err := a.streamEvents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case ev, ok := <-events:
				if !ok || ev.Type != "server.connected" {
					t.Fatalf("first event = %+v (open %v), want server.connected", ev, ok)
				}
			case <-ctx.Done():
				t.Fatal("no server.connected event")
			}
			_ = a.stdin.Close()
			select {
			case <-a.exited:
			case <-time.After(15 * time.Second):
				t.Fatal("opencode acp did not exit after stdin closed")
			}
		})
	}
}

// runChildPermissionSpike asserts the three gate properties on one binary.
func runChildPermissionSpike(t *testing.T, bin string, box *openCodeSandbox) {
	model := openCodeLiveModel(t, bin, box.env)
	t.Logf("model: %s", model)
	work := t.TempDir()
	// bash is a pattern map, not a bare "ask": a managed OpenCode policy (for
	// example a macOS ai.opencode.managed profile) that sets bash patterns
	// outranks OPENCODE_CONFIG_CONTENT and would replace a bare string, leaving
	// unmatched commands allowed. A map merges key-wise, so "*" asks and the
	// managed denies, ordered after it, still win under last-match.
	overlay := map[string]any{
		"model":      model,
		"autoupdate": false,
		"permission": map[string]any{"bash": map[string]any{"*": "ask"}},
		"agent": map[string]any{
			"general": map[string]any{"permission": map[string]any{"task": "deny"}},
			"explore": map[string]any{"permission": map[string]any{"task": "deny"}},
		},
	}
	a := startRawACP(t, bin, box.env, work, overlay)

	var (
		mu          sync.Mutex
		rootID      string
		reply       strings.Builder
		acpPerms    []string
		events      []string
		childAsk    json.RawMessage
		inFlightOK  = map[string]int{}
		replyStatus int
		modelError  string
	)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		t.Logf("HTTP events:\n%s", strings.Join(events, "\n"))
		t.Logf("ACP lines:\n%s", a.dump())
		t.Logf("stderr tail:\n%s", a.stderrTail())
	})

	a.onRequest = func(method string, params json.RawMessage) any {
		mu.Lock()
		acpPerms = append(acpPerms, string(params))
		mu.Unlock()
		if method != "session/request_permission" {
			return map[string]any{}
		}
		var req struct {
			ToolCall struct {
				Kind string `json:"kind"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(params, &req)
		// The root session must delegate rather than run the command itself.
		want := "allow_once"
		if req.ToolCall.Kind == "execute" {
			want = "reject_once"
		}
		for _, o := range req.Options {
			if o.Kind == want {
				return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": o.OptionID}}
			}
		}
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	}
	a.onUpdate = func(params json.RawMessage) {
		var u struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				SessionUpdate string `json:"sessionUpdate"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(params, &u) != nil || u.Update.SessionUpdate != "agent_message_chunk" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if u.SessionID == rootID {
			reply.WriteString(u.Update.Content.Text)
		}
	}

	a.waitHealthy(time.Minute)
	sessionID := a.handshake(work)
	mu.Lock()
	rootID = sessionID
	mu.Unlock()
	t.Logf("ACP session: %s", sessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	stream, err := a.streamEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for ev := range stream {
			mu.Lock()
			events = append(events, ev.raw)
			mu.Unlock()
			var props struct {
				ID         string `json:"id"`
				SessionID  string `json:"sessionID"`
				Permission string `json:"permission"`
			}
			_ = json.Unmarshal(ev.Properties, &props)
			switch ev.Type {
			case "permission.asked":
				if props.SessionID == sessionID {
					continue
				}
				if props.Permission == "bash" {
					// Coexistence: the HTTP surface answers, and enforces the
					// password, while the child is parked on its request.
					c1, _, _ := a.httpStatus(http.MethodGet, "/global/health", nil, true)
					c2, _, _ := a.httpStatus(http.MethodGet, "/global/health", nil, false)
					mu.Lock()
					if childAsk == nil {
						childAsk = append(json.RawMessage{}, ev.Properties...)
					}
					inFlightOK["auth"], inFlightOK["noauth"] = c1, c2
					mu.Unlock()
				}
				code, body, err := a.httpStatus(http.MethodPost, "/permission/"+props.ID+"/reply", map[string]string{"reply": "once"}, true)
				if err != nil || code != http.StatusOK {
					t.Errorf("permission reply %s: %d %s %v", props.ID, code, body, err)
				}
				if props.Permission == "bash" {
					mu.Lock()
					replyStatus = code
					mu.Unlock()
				}
			case "session.error":
				// A model the gateway cannot serve surfaces only here; the
				// prompt itself still ends with end_turn.
				mu.Lock()
				if modelError == "" {
					modelError = string(ev.Properties)
				}
				mu.Unlock()
			case "question.asked":
				_, _, _ = a.httpStatus(http.MethodPost, "/question/"+props.ID+"/reject", nil, true)
			}
		}
	}()

	word := "spike" + randomHex(t, 4)
	prompt := "This is an automated connectivity test. Use the task tool to start a general sub-agent, and have that sub-agent run exactly this shell command with its bash tool:\n\n" +
		"echo " + word + " | tr a-z A-Z\n\n" +
		"Do not run any shell command yourself. When the sub-agent finishes, reply with the exact output it reported and nothing else."
	res := a.await(a.call("session/prompt", map[string]any{
		"sessionId": sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": prompt}},
	}), 7*time.Minute, "session/prompt")

	var result struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(res, &result); err != nil {
		t.Fatalf("prompt result %s: %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if modelError != "" {
		t.Fatalf("OpenCode reported a session error for model %s (set %s to choose another model): %s", model, openCodeLiveModelEnvVar, modelError)
	}
	if childAsk == nil {
		t.Fatal("no child-session bash permission.asked event was observed on GET /event")
	}
	var ask struct {
		SessionID  string `json:"sessionID"`
		Permission string `json:"permission"`
	}
	_ = json.Unmarshal(childAsk, &ask)
	if ask.SessionID == "" || ask.SessionID == sessionID || ask.Permission != "bash" {
		t.Fatalf("child permission.asked = %s; want a bash request under a session other than %s", childAsk, sessionID)
	}
	t.Logf("child permission.asked: %s", childAsk)
	for _, p := range acpPerms {
		if strings.Contains(p, ask.SessionID) {
			t.Fatalf("an ACP request arrived for the child session: %s", p)
		}
	}
	if inFlightOK["auth"] != http.StatusOK || inFlightOK["noauth"] != http.StatusUnauthorized {
		t.Fatalf("health while the child was parked: with password %d, without %d; want 200 and 401", inFlightOK["auth"], inFlightOK["noauth"])
	}
	if replyStatus != http.StatusOK {
		t.Fatalf("child permission reply status %d", replyStatus)
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn", result.StopReason)
	}
	if !strings.Contains(reply.String(), strings.ToUpper(word)) {
		t.Fatalf("reply %q does not contain the command output %s", reply.String(), strings.ToUpper(word))
	}
	t.Logf("reply: %s", strings.TrimSpace(reply.String()))
}
