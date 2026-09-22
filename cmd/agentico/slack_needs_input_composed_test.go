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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	slackintegration "github.com/doordash-oss/agentic-orchestrator/internal/slack"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

const (
	composedNeedsInputFeatureID = "slack-real-session"
	composedNeedsInputSessionID = "slack-real-session-implement"
	composedNeedsInputRepo      = "agentic-orchestrator"
	composedNeedsInputToken     = "xoxb-needs-input-sentinel-4242"
	composedNeedsInputSecret    = "https://alice:second-secret@example.com/private"
)

type composedAutoReviewHandler struct {
	inner ports.PermissionHandler
}

func (h composedAutoReviewHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	decision, err := h.inner.CanUseTool(req)
	if err != nil || decision.Behavior != "" {
		return decision, err
	}
	if req.ToolName == "Bash" && strings.Contains(req.Input, "auto-reviewed") {
		return ports.PermissionDecision{Behavior: permission.DecisionAllow}, nil
	}
	return decision, nil
}

type composedNeedsInputSSE struct {
	cancel context.CancelFunc
	body   io.ReadCloser
	blocks chan string
}

func TestSlackNeedsInputRedaction(t *testing.T) {
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	seedComposedNeedsInputFeature(t, store)
	featurePath := filepath.Join(stateDir, composedNeedsInputFeatureID, "feature.yaml")
	featureBefore := readTestFile(t, featurePath)

	script := writeComposedNeedsInputProvider(t)
	eventCh := make(chan interface{}, 64)
	domainCh := make(chan ports.Event, 8)
	sessions := session.NewManager(eventCh)
	t.Cleanup(sessions.Shutdown)

	cache := permission.NewCache(nil)
	rememberedInput := `{"command":"echo remembered"}`
	cache.RememberAllow("Bash", rememberedInput, composedNeedsInputRepo)
	handler := composedAutoReviewHandler{inner: permission.Guarded(&permission.CachingHandler{
		Inner:    &permission.AcceptEditsHandler{},
		Cache:    cache,
		RepoName: composedNeedsInputRepo,
	})}

	cfg := config.NewDefault()
	cfg.Slack = &config.SlackConfig{
		Enabled: true,
		Token:   composedNeedsInputToken,
		DefaultRecipients: []config.SlackRecipient{
			{TypedText: "@ada", Kind: string(ports.SlackRecipientUser), ID: "U-ADA", DisplayName: "Ada"},
			{TypedText: "#eng", Kind: string(ports.SlackRecipientChannel), ID: "C-ENG", DisplayName: "#eng"},
		},
	}
	target := &serverMutationTarget{
		cfg:             cfg,
		store:           store,
		sessions:        sessions,
		permissionCache: cache,
	}
	fake := testsupport.New(t)
	fake.SetDefault(composedNeedsInputSlackResponder())
	t.Setenv(slackintegration.EnvSlackAPIBase, fake.URL())

	observer := observe.New(true, stateDir, false, "", false, "")
	var pending ports.SlackPendingInputSource
	pendingRelay := &slackPendingInputRelay{}
	notifier := slackintegration.NewNotifier(slackintegration.NotifierOptions{
		Settings: target,
		Store:    store,
		StateDir: stateDir,
		Observer: observer,
		Pending:  pendingRelay,
	})
	notifier.SetServerName("Composed test")
	notifier.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		notifier.Stop(ctx)
	})

	httpHandler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:                     serverruntime.RuntimeIdentity{StateDir: stateDir},
		Features:                    store,
		FeatureStore:                store,
		Config:                      cfg,
		Sessions:                    sessions,
		Events:                      eventCh,
		DomainEvents:                domainCh,
		RuntimeEventTap:             notifier.RuntimeMessageTap,
		DomainEventTap:              notifier.DomainEventTap,
		Mutations:                   target,
		BindSlackPendingInputSource: func(source ports.SlackPendingInputSource) { pending = source },
		DisableHostValidation:       true,
	})
	if pending == nil {
		t.Fatal("real pending-input adapter was not bound")
	}
	pendingRelay.bind(pending)

	httpServer := httptest.NewServer(httpHandler)
	t.Cleanup(httpServer.Close)
	sse := openComposedNeedsInputSSE(t, httpServer)
	t.Cleanup(func() {
		sse.cancel()
		_ = sse.body.Close()
	})

	var logs bytes.Buffer
	oldLogOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldLogOutput) })

	notifier.DomainEventTap(ports.Event{
		Type:      ports.FeatureStarted,
		FeatureID: composedNeedsInputFeatureID,
	})
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		return rootSlackPosts(fake) == 2
	})

	sess, err := sessions.StartSession(
		composedNeedsInputSessionID,
		composedNeedsInputFeatureID,
		feature.PhaseImplement,
		[]string{"bash", script},
		filepath.Dir(script),
		nil,
		&session.SessionOpts{
			PermHandler:  handler,
			RepoName:     composedNeedsInputRepo,
			ProviderName: "scripted",
		},
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}

	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		return needsInputSlackPosts(fake) >= 2
	})
	requirePendingRequestIDs(t, sess, "perm-human")
	if _, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		SessionID: composedNeedsInputSessionID,
		RequestID: "perm-human",
		Decision:  permission.DecisionAllowOnce,
	}); err != nil {
		t.Fatalf("AnswerPermission() error = %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for needsInputSlackPosts(fake) < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := needsInputSlackPosts(fake); got < 6 {
		t.Fatalf(
			"Needs-input Slack posts after permission answer = %d; want at least 6; pending=%v requests=%v",
			got, pendingRequestIDs(sess), fake.AllRequests(),
		)
	}
	requirePendingRequestIDs(t, sess, "ask-human")
	if _, err := target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
		SessionID: composedNeedsInputSessionID,
		RequestID: "ask-human",
		Answers: map[string]string{
			"Where should " + composedNeedsInputToken + " be deployed?": "staging",
			"Which checks use " + composedNeedsInputSecret + "?":        "unit, integration",
		},
	}); err != nil {
		t.Fatalf("AnswerAskUser() error = %v", err)
	}

	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for scripted session")
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		record := readComposedSlackRecord(t, stateDir)
		return record.TagCounter == 3 && len(record.PendingInputs) == 0 && cardWithoutWaitingLine(fake)
	})

	gotSSE := collectComposedNeedsInputSSE(t, sse.blocks, 2)
	wantSSE := runComposedNeedsInputSSEBaseline(t, script, handler, cache)
	if fmt.Sprint(gotSSE) != fmt.Sprint(wantSSE) {
		t.Fatalf("SSE permission/prompt events = %v; want baseline %v", gotSSE, wantSSE)
	}

	if got := needsInputSlackPosts(fake); got != 6 {
		t.Fatalf("Needs-input Slack posts = %d; want 3 per destination", got)
	}
	assertComposedNeedsInputTags(t, fake)
	if got := readTestFile(t, featurePath); !bytes.Equal(got, featureBefore) {
		t.Fatal("feature.yaml changed during Slack notification flow")
	}

	recordRaw := readTestFile(t, filepath.Join(stateDir, composedNeedsInputFeatureID, "slack.yaml"))
	eventsRaw := readTestFile(t, filepath.Join(stateDir, composedNeedsInputFeatureID, "events.jsonl"))
	requestRaw, err := json.Marshal(fake.AllRequests())
	if err != nil {
		t.Fatalf("marshal fake Slack requests: %v", err)
	}
	allSurfaces := bytes.Join([][]byte{requestRaw, logs.Bytes(), eventsRaw, recordRaw}, []byte("\n"))
	for _, secret := range []string{composedNeedsInputToken, composedNeedsInputSecret, "alice:second-secret"} {
		if bytes.Contains(allSurfaces, []byte(secret)) {
			t.Fatalf("composed Needs-input surface leaked %q:\n%s", secret, allSurfaces)
		}
	}
	for _, surrounding := range []string{"before-token", "after-url", "Where should", "Which checks"} {
		if !bytes.Contains(requestRaw, []byte(surrounding)) {
			t.Errorf("fake Slack requests lost non-sensitive text %q", surrounding)
		}
	}
}

type composedSlackRecord struct {
	TagCounter    int `yaml:"tag_counter"`
	PendingInputs []struct {
		Kind      string            `yaml:"kind"`
		RequestID string            `yaml:"request_id"`
		Tag       string            `yaml:"tag"`
		MessageTS map[string]string `yaml:"message_timestamps"`
	} `yaml:"pending_inputs"`
}

func seedComposedNeedsInputFeature(t *testing.T, store *feature.Store) {
	t.Helper()
	f := &feature.Feature{
		ID:            composedNeedsInputFeatureID,
		Name:          "Composed Slack real session",
		Slug:          "composed-slack-real-session",
		Description:   "test feature",
		Created:       time.Now().Add(-time.Hour),
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineMoonshot,
		Repos:         []feature.FeatureRepo{{Name: composedNeedsInputRepo}},
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("seed feature: %v", err)
	}
}

func writeComposedNeedsInputProvider(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "needs-input-provider.sh")
	script := fmt.Sprintf(`#!/usr/bin/env bash
sleep 0.1
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":"perm-remembered","request":{"subtype":"can_use_tool","tool_name":"Bash","input":%s}}'
while IFS= read -r response; do [[ "$response" == *'"request_id":"perm-remembered"'* ]] && break; done
printf '%%s\n' '{"type":"control_request","request_id":"perm-auto","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo auto-reviewed"}}}'
while IFS= read -r response; do [[ "$response" == *'"request_id":"perm-auto"'* ]] && break; done
printf '%%s\n' '{"type":"control_request","request_id":"perm-human","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo before-token %s after-token && printf after-url %s done"}}}'
while IFS= read -r response; do [[ "$response" == *'"request_id":"perm-human"'* ]] && break; done
printf '%%s\n' '{"type":"control_request","request_id":"ask-human","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"header":"Destination","question":"Where should %s be deployed?","options":[{"label":"staging","description":"Use staging."},{"label":"production","description":"Use production."}]},{"header":"Checks","question":"Which checks use %s?","multiSelect":true,"options":[{"label":"unit","description":"Run unit tests."},{"label":"integration","description":"Run integration tests."}] }]}}}'
while IFS= read -r response; do [[ "$response" == *'"request_id":"ask-human"'* ]] && break; done
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answers received"}]}}'
sleep 0.1
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"scripted","total_cost_usd":0}'
`, quoteJSONRaw(t, rememberedInputJSON()), composedNeedsInputToken, composedNeedsInputSecret, composedNeedsInputToken, composedNeedsInputSecret)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write scripted provider: %v", err)
	}
	return path
}

func rememberedInputJSON() string {
	return `{"command":"echo remembered"}`
}

func quoteJSONRaw(t *testing.T, raw string) string {
	t.Helper()
	encoded, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("marshal raw JSON: %v", err)
	}
	return string(encoded)
}

func composedNeedsInputSlackResponder() func(string, testsupport.Request) testsupport.Response {
	var sequence atomic.Int64
	return func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": "D-" + fmt.Sprint(request.Fields["users"])},
			}}
		case "chat.postMessage", "chat.update":
			n := sequence.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok": true, "ts": fmt.Sprintf("1780000000.%06d", n),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	}
}

func openComposedNeedsInputSSE(t *testing.T, server *httptest.Server) composedNeedsInputSSE {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+slackEventsPath, nil)
	if err != nil {
		t.Fatalf("create SSE request: %v", err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	blocks := make(chan string, 64)
	go scanSSEBlocks(resp.Body, blocks)
	select {
	case block := <-blocks:
		if !strings.Contains(block, "event: connected") {
			t.Fatalf("initial SSE block = %q; want connected", block)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for connected SSE event")
	}
	return composedNeedsInputSSE{cancel: cancel, body: resp.Body, blocks: blocks}
}

func collectComposedNeedsInputSSE(t *testing.T, blocks <-chan string, count int) []string {
	t.Helper()
	var got []string
	deadline := time.After(5 * time.Second)
	for len(got) < count {
		select {
		case block := <-blocks:
			switch {
			case strings.Contains(block, "event: permission.updated"):
				got = append(got, "permission.updated:snapshot")
			case strings.Contains(block, "event: prompt.updated"):
				got = append(got, "prompt.updated:snapshot")
			}
		case <-deadline:
			t.Fatalf("timed out collecting SSE events; got %v", got)
		}
	}
	return got
}

func runComposedNeedsInputSSEBaseline(
	t *testing.T,
	script string,
	handler ports.PermissionHandler,
	cache *permission.Cache,
) []string {
	t.Helper()
	eventCh := make(chan interface{}, 64)
	sessions := session.NewManager(eventCh)
	t.Cleanup(sessions.Shutdown)
	target := &serverMutationTarget{sessions: sessions, permissionCache: cache}
	httpServer := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Sessions:              sessions,
		Events:                eventCh,
		Mutations:             target,
		DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	sse := openComposedNeedsInputSSE(t, httpServer)
	t.Cleanup(func() {
		sse.cancel()
		_ = sse.body.Close()
	})
	sess, err := sessions.StartSession(
		composedNeedsInputSessionID+"-baseline",
		composedNeedsInputFeatureID,
		feature.PhaseImplement,
		[]string{"bash", script},
		filepath.Dir(script),
		nil,
		&session.SessionOpts{PermHandler: handler, RepoName: composedNeedsInputRepo, ProviderName: "scripted"},
	)
	if err != nil {
		t.Fatalf("baseline StartSession() error = %v", err)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "perm-human")
	})
	if _, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		SessionID: composedNeedsInputSessionID + "-baseline",
		RequestID: "perm-human",
		Decision:  permission.DecisionAllowOnce,
	}); err != nil {
		t.Fatalf("baseline AnswerPermission() error = %v", err)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "ask-human")
	})
	if _, err := target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
		SessionID: composedNeedsInputSessionID + "-baseline",
		RequestID: "ask-human",
		Answers: map[string]string{
			"Where should " + composedNeedsInputToken + " be deployed?": "staging",
			"Which checks use " + composedNeedsInputSecret + "?":        "unit, integration",
		},
	}); err != nil {
		t.Fatalf("baseline AnswerAskUser() error = %v", err)
	}
	return collectComposedNeedsInputSSE(t, sse.blocks, 2)
}

func waitForComposedNeedsInput(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func rootSlackPosts(fake *testsupport.Server) int {
	count := 0
	for _, request := range fake.Requests("chat.postMessage") {
		if _, threaded := request.Fields["thread_ts"]; !threaded {
			count++
		}
	}
	return count
}

func needsInputSlackPosts(fake *testsupport.Server) int {
	count := 0
	for _, request := range fake.Requests("chat.postMessage") {
		if threadTS, threaded := request.Fields["thread_ts"]; threaded && strings.TrimSpace(fmt.Sprint(threadTS)) != "" {
			count++
		}
	}
	return count
}

func requirePendingRequestIDs(t *testing.T, sess ports.SessionView, want ...string) {
	t.Helper()
	got := pendingRequestIDs(sess)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pending request IDs = %v; want %v", got, want)
	}
}

func pendingRequestIDs(sess ports.SessionView) []string {
	var ids []string
	for _, request := range sess.PendingControlRequests() {
		ids = append(ids, request.RequestID)
	}
	return ids
}

func hasPendingRequest(sess ports.SessionView, requestID string) bool {
	for _, request := range sess.PendingControlRequests() {
		if request.RequestID == requestID {
			return true
		}
	}
	return false
}

func readComposedSlackRecord(t *testing.T, stateDir string) composedSlackRecord {
	t.Helper()
	var record composedSlackRecord
	raw, err := os.ReadFile(filepath.Join(stateDir, composedNeedsInputFeatureID, "slack.yaml"))
	if err != nil {
		return record
	}
	if err := yaml.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode Slack record: %v", err)
	}
	return record
}

func cardWithoutWaitingLine(fake *testsupport.Server) bool {
	updates := fake.Requests("chat.update")
	if len(updates) < 2 {
		return false
	}
	for _, update := range updates[len(updates)-2:] {
		if strings.Contains(fmt.Sprint(update.Fields["blocks"]), "Waiting on you") {
			return false
		}
	}
	return true
}

func assertComposedNeedsInputTags(t *testing.T, fake *testsupport.Server) {
	t.Helper()
	counts := map[string]int{}
	for _, request := range fake.Requests("chat.postMessage") {
		if _, threaded := request.Fields["thread_ts"]; !threaded {
			continue
		}
		rendered := fmt.Sprint(request.Fields["blocks"])
		for _, tag := range []string{"#1", "#2", "#3"} {
			if strings.Contains(rendered, tag) {
				counts[tag]++
			}
		}
	}
	for _, tag := range []string{"#1", "#2", "#3"} {
		if counts[tag] != 2 {
			t.Errorf("Slack posts carrying %s = %d; want one per destination", tag, counts[tag])
		}
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
