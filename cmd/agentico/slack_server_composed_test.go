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

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	slackintegration "github.com/doordash-oss/agentic-orchestrator/internal/slack"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const (
	slackRuntimeConfigPath = "/api/v1/config/runtime"
	slackValidatePath      = "/api/v1/integrations/slack/validate"
	slackEventsPath        = "/api/v1/events"
)

type composedSlackRuntime struct {
	t          *testing.T
	token      string
	configPath string
	stateDir   string
	cfg        *config.Config
	fake       *testsupport.Server
	server     *httptest.Server
	sseCancel  context.CancelFunc
	sseBody    io.ReadCloser
	sseBlocks  chan string
	logs       *bytes.Buffer
	oldLogOut  io.Writer
}

func newComposedSlackRuntime(t *testing.T, token string, stored bool) *composedSlackRuntime {
	t.Helper()
	runtimeDir := t.TempDir()
	configPath := filepath.Join(runtimeDir, "config.yaml")
	stateDir := filepath.Join(runtimeDir, "features")
	cfg := config.NewDefault()
	if stored {
		cfg.Slack = &config.SlackConfig{Enabled: true, Token: token}
	}
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	fake := testsupport.New(t)
	t.Setenv(slackintegration.EnvSlackAPIBase, fake.URL())
	service := slackintegration.NewService()
	target := &serverMutationTarget{cfg: cfg, configPath: configPath}
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:               serverruntime.RuntimeIdentity{StateDir: stateDir},
		Config:                cfg,
		Slack:                 service,
		Mutations:             target,
		DisableHostValidation: true,
	})
	httpServer := httptest.NewServer(handler)

	logs := &bytes.Buffer{}
	oldLogOut := log.Writer()
	log.SetOutput(logs)

	runtime := &composedSlackRuntime{
		t:          t,
		token:      token,
		configPath: configPath,
		stateDir:   stateDir,
		cfg:        cfg,
		fake:       fake,
		server:     httpServer,
		sseBlocks:  make(chan string, 32),
		logs:       logs,
		oldLogOut:  oldLogOut,
	}
	runtime.startSSE()
	t.Cleanup(func() {
		runtime.sseCancel()
		_ = runtime.sseBody.Close()
		httpServer.Close()
		log.SetOutput(oldLogOut)
	})
	return runtime
}

func (r *composedSlackRuntime) startSSE() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server.URL+slackEventsPath, nil)
	if err != nil {
		r.t.Fatalf("create SSE request: %v", err)
	}
	resp, err := r.server.Client().Do(req)
	if err != nil {
		r.t.Fatalf("open SSE stream: %v", err)
	}
	r.sseCancel = cancel
	r.sseBody = resp.Body
	go scanSSEBlocks(resp.Body, r.sseBlocks)
	if block := r.waitSSEBlock(); !strings.Contains(block, "event: connected") {
		r.t.Fatalf("initial SSE block = %q; want connected", block)
	}
}

func scanSSEBlocks(body io.Reader, blocks chan<- string) {
	scanner := bufio.NewScanner(body)
	var block strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if block.Len() > 0 {
				blocks <- block.String()
				block.Reset()
			}
			continue
		}
		block.WriteString(line)
		block.WriteByte('\n')
	}
	close(blocks)
}

func (r *composedSlackRuntime) waitSSEBlock() string {
	r.t.Helper()
	select {
	case block := <-r.sseBlocks:
		return block
	case <-time.After(2 * time.Second):
		r.t.Fatal("timed out waiting for SSE block")
		return ""
	}
}

func (r *composedSlackRuntime) request(method, path string, body any) (int, []byte) {
	r.t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		r.t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(method, r.server.URL+path, bytes.NewReader(payload))
	if err != nil {
		r.t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("X-Agentico-Client", "local")
	}
	resp, err := r.server.Client().Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, responseBody
}

func (r *composedSlackRuntime) assertNoTokenDisclosure(response []byte, expectEvent bool) {
	r.t.Helper()
	var captured bytes.Buffer
	captured.Write(response)
	if expectEvent {
		block := r.waitSSEBlock()
		if !strings.Contains(block, "event: config.updated") {
			r.t.Fatalf("SSE block = %q; want config.updated", block)
		}
		captured.WriteString(block)
	}
	time.Sleep(10 * time.Millisecond)
drain:
	for {
		select {
		case block := <-r.sseBlocks:
			captured.WriteString(block)
		default:
			break drain
		}
	}
	captured.Write(r.logs.Bytes())
	observabilityPath := filepath.Join(filepath.Dir(r.stateDir), "events.jsonl")
	if raw, err := os.ReadFile(observabilityPath); err == nil {
		captured.Write(raw)
	} else if !os.IsNotExist(err) {
		r.t.Fatalf("read observability output: %v", err)
	}
	if bytes.Contains(captured.Bytes(), []byte(r.token)) {
		r.t.Fatalf("server surface leaked Slack token:\n%s", captured.Bytes())
	}
}

func fullSlackScopesHeader() http.Header {
	return http.Header{
		"X-OAuth-Scopes": []string{strings.Join(slackintegration.RequiredScopes(), ",")},
	}
}

func validSlackAuthBody(name string) map[string]any {
	return map[string]any{
		"ok":      true,
		"team":    "Acme",
		"team_id": "T123",
		"user":    name,
		"user_id": "U123",
		"bot_id":  "B123",
	}
}

func TestSlackServerComposedNonDisclosure(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		stored      bool
		script      *testsupport.Response
		path        string
		body        map[string]any
		wantStatus  int
		expectEvent bool
	}{
		{
			name:  "successful save",
			token: "xoxb-save-success-distinctive-1234",
			script: &testsupport.Response{
				Body:    validSlackAuthBody("Save Agent"),
				Headers: fullSlackScopesHeader(),
			},
			path:        slackRuntimeConfigPath,
			body:        map[string]any{"slack": map[string]any{"enabled": true}},
			wantStatus:  http.StatusOK,
			expectEvent: true,
		},
		{
			name:  "invalid token rejection",
			token: "xoxb-invalid-distinctive-1234",
			script: &testsupport.Response{
				Body: map[string]any{"ok": false, "error": "invalid_auth: xoxb-invalid-distinctive-1234"},
			},
			path:       slackRuntimeConfigPath,
			body:       map[string]any{"slack": map[string]any{"enabled": true}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unsupported token rejection",
			token:      "xoxa-unsupported-distinctive-1234",
			path:       slackRuntimeConfigPath,
			body:       map[string]any{"slack": map[string]any{"enabled": true}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:  "missing scopes rejection",
			token: "xoxb-missing-scopes-distinctive-1234",
			script: &testsupport.Response{
				Body:    validSlackAuthBody("Under Scoped"),
				Headers: http.Header{"X-OAuth-Scopes": []string{"chat:write"}},
			},
			path:       slackRuntimeConfigPath,
			body:       map[string]any{"slack": map[string]any{"enabled": true}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:  "unreachable save",
			token: "xoxb-save-unreachable-distinctive-1234",
			script: &testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   "echo xoxb-save-unreachable-distinctive-1234",
			},
			path:        slackRuntimeConfigPath,
			body:        map[string]any{"slack": map[string]any{"enabled": true}},
			wantStatus:  http.StatusOK,
			expectEvent: true,
		},
		{
			name:  "draft validation success",
			token: "xoxb-draft-success-distinctive-1234",
			script: &testsupport.Response{
				Body:    validSlackAuthBody("Draft Agent"),
				Headers: fullSlackScopesHeader(),
			},
			path:       slackValidatePath,
			body:       map[string]any{},
			wantStatus: http.StatusOK,
		},
		{
			name:  "draft validation failure",
			token: "xoxb-draft-failure-distinctive-1234",
			script: &testsupport.Response{
				Body: map[string]any{"ok": false, "error": "invalid_auth: xoxb-draft-failure-distinctive-1234"},
			},
			path:       slackValidatePath,
			body:       map[string]any{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "stored validation success",
			token:  "xoxb-stored-success-distinctive-1234",
			stored: true,
			script: &testsupport.Response{
				Body:    validSlackAuthBody("Stored Agent"),
				Headers: fullSlackScopesHeader(),
			},
			path:        slackValidatePath,
			body:        map[string]any{},
			wantStatus:  http.StatusOK,
			expectEvent: true,
		},
		{
			name:   "stored validation failure",
			token:  "xoxb-stored-failure-distinctive-1234",
			stored: true,
			script: &testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   "echo xoxb-stored-failure-distinctive-1234",
			},
			path:        slackValidatePath,
			body:        map[string]any{},
			wantStatus:  http.StatusBadGateway,
			expectEvent: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtime := newComposedSlackRuntime(t, tc.token, tc.stored)
			if tc.script != nil {
				runtime.fake.Script("auth.test", *tc.script)
			}
			body := tc.body
			if tc.path == slackRuntimeConfigPath {
				body["slack"].(map[string]any)["token"] = tc.token
			} else if !tc.stored {
				body["token"] = tc.token
			}

			method := http.MethodPost
			if tc.path == slackRuntimeConfigPath {
				method = http.MethodPatch
			}
			status, response := runtime.request(method, tc.path, body)
			if status != tc.wantStatus {
				t.Fatalf("status = %d body=%s; want %d", status, response, tc.wantStatus)
			}
			runtime.assertNoTokenDisclosure(response, tc.expectEvent)
		})
	}
}

func TestSlackServerDelayedStoredValidationIsCredentialFenced(t *testing.T) {
	tests := []struct {
		name          string
		delayed       testsupport.Response
		wantOldStatus int
		replace       bool
	}{
		{
			name: "success after replacement",
			delayed: testsupport.Response{
				Body:    validSlackAuthBody("Old Agent"),
				Headers: fullSlackScopesHeader(),
			},
			wantOldStatus: http.StatusOK,
			replace:       true,
		},
		{
			name: "failure after replacement",
			delayed: testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   "old credential failed",
			},
			wantOldStatus: http.StatusBadGateway,
			replace:       true,
		},
		{
			name: "success after clearing",
			delayed: testsupport.Response{
				Body:    validSlackAuthBody("Old Agent"),
				Headers: fullSlackScopesHeader(),
			},
			wantOldStatus: http.StatusOK,
		},
		{
			name: "failure after clearing",
			delayed: testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   "old credential failed",
			},
			wantOldStatus: http.StatusBadGateway,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const oldToken = "xoxb-old-delayed-distinctive-1234"
			runtime := newComposedSlackRuntime(t, oldToken, true)
			started := make(chan struct{})
			release := make(chan struct{})
			tc.delayed.Started = started
			tc.delayed.Release = release
			runtime.fake.Script("auth.test", tc.delayed)
			if tc.replace {
				runtime.fake.Script("auth.test", testsupport.Response{
					Body:    validSlackAuthBody("New Agent"),
					Headers: fullSlackScopesHeader(),
				})
			}

			type response struct {
				status int
				body   []byte
			}
			oldResult := make(chan response, 1)
			go func() {
				status, body := runtime.request(http.MethodPost, slackValidatePath, map[string]any{})
				oldResult <- response{status: status, body: body}
			}()
			awaitSignal(t, started, "old Slack validation to start")

			assertRequestCompletes(t, "unrelated write", func() (int, []byte) {
				return runtime.request(http.MethodPatch, slackRuntimeConfigPath, map[string]any{
					"notifications": map[string]any{"mute_feature_input": true},
				})
			})
			assertRequestCompletes(t, "unrelated read", func() (int, []byte) {
				return runtime.request(http.MethodGet, slackRuntimeConfigPath, nil)
			})

			if tc.replace {
				const newToken = "xoxb-new-delayed-distinctive-5678"
				status, body := runtime.request(http.MethodPatch, slackRuntimeConfigPath, map[string]any{
					"slack": map[string]any{"token": newToken},
				})
				if status != http.StatusOK {
					t.Fatalf("replacement status = %d body=%s; want 200", status, body)
				}
			} else {
				status, body := runtime.request(http.MethodPatch, slackRuntimeConfigPath, map[string]any{
					"slack": map[string]any{"clear_token": true},
				})
				if status != http.StatusOK {
					t.Fatalf("clear status = %d body=%s; want 200", status, body)
				}
			}

			close(release)
			result := awaitResponse(t, oldResult, "old Slack validation to finish")
			if result.status != tc.wantOldStatus {
				t.Fatalf("delayed validation status = %d body=%s; want %d", result.status, result.body, tc.wantOldStatus)
			}
			loaded, err := config.Load(runtime.configPath)
			if err != nil {
				t.Fatalf("load config: %v", err)
			}
			if tc.replace {
				if loaded.Slack == nil || loaded.Slack.Token != "xoxb-new-delayed-distinctive-5678" ||
					loaded.Slack.Identity == nil || loaded.Slack.Identity.DisplayName != "New Agent" {
					t.Fatalf("replacement credential was overwritten by delayed result: %#v", loaded.Slack)
				}
			} else if loaded.Slack == nil || loaded.Slack.Token != "" || loaded.Slack.Identity != nil {
				t.Fatalf("cleared credential was overwritten by delayed result: %#v", loaded.Slack)
			}
		})
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertRequestCompletes(t *testing.T, description string, request func() (int, []byte)) {
	t.Helper()
	type response struct {
		status int
		body   []byte
	}
	result := make(chan response, 1)
	go func() {
		status, body := request()
		result <- response{status: status, body: body}
	}()
	got := awaitResponse(t, result, description)
	if got.status != http.StatusOK {
		t.Fatalf("%s status = %d body=%s; want 200", description, got.status, got.body)
	}
}

func awaitResponse[T any](t *testing.T, result <-chan T, description string) T {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}
