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
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/orchestrator"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	slackintegration "github.com/doordash-oss/agentic-orchestrator/internal/slack"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

const (
	composedResponderFeatureID = "slack-responder-composed"
	composedResponderSessionID = "slack-responder-composed-implement"
	composedResponderOwnerID   = "U-OWNER"
	composedResponderChannelID = "D-U-OWNER"
	composedResponderSecret    = "https://alice:second-secret@example.com/private"
)

type composedResponderClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  chan time.Duration
	advance chan struct{}
}

func newComposedResponderClock() *composedResponderClock {
	return &composedResponderClock{
		now:     time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC),
		sleeps:  make(chan time.Duration, 1),
		advance: make(chan struct{}),
	}
}

func (c *composedResponderClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *composedResponderClock) Sleep(ctx context.Context, duration time.Duration) bool {
	select {
	case c.sleeps <- duration:
	case <-ctx.Done():
		return false
	}
	select {
	case <-c.advance:
		c.mu.Lock()
		c.now = c.now.Add(duration)
		c.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *composedResponderClock) tick(t *testing.T) {
	t.Helper()
	select {
	case <-c.sleeps:
	case <-time.After(time.Second):
		t.Fatal("Slack responder did not wait for its polling interval")
	}
	c.advance <- struct{}{}
}

// tickUntil re-polls until condition holds: a poll that lands while a delivery
// holds the thread lock defers judgment to the next poll, as in production.
func (c *composedResponderClock) tickUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.tick(t)
		settle := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(settle) {
			if condition() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
	}
}

type composedResponderMutationTarget struct {
	*serverMutationTarget

	mu          sync.Mutex
	permissions []serverruntime.PermissionAnswerRequest
	reviews     []serverruntime.ReviewDecisionRequest
}

func (t *composedResponderMutationTarget) AnswerPermission(
	req serverruntime.PermissionAnswerRequest,
) (serverruntime.PermissionAnswerResponse, error) {
	t.mu.Lock()
	t.permissions = append(t.permissions, req)
	t.mu.Unlock()
	return t.serverMutationTarget.AnswerPermission(req)
}

func (t *composedResponderMutationTarget) ReviewDecision(
	featureID string,
	req serverruntime.ReviewDecisionRequest,
) error {
	t.mu.Lock()
	t.reviews = append(t.reviews, req)
	t.mu.Unlock()
	return t.serverMutationTarget.ReviewDecision(featureID, req)
}

func (t *composedResponderMutationTarget) captured() (
	[]serverruntime.PermissionAnswerRequest,
	[]serverruntime.ReviewDecisionRequest,
) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.permissions), slices.Clone(t.reviews)
}

type composedResponderSlackRecord struct {
	TagCounter   int `yaml:"tag_counter"`
	Destinations map[string]struct {
		RootTS string   `yaml:"root_ts"`
		Ledger []string `yaml:"ledger"`
	} `yaml:"destinations"`
	Pending []struct {
		Kind      string            `yaml:"kind"`
		Tag       string            `yaml:"tag"`
		MessageTS map[string]string `yaml:"message_timestamps"`
	} `yaml:"pending_inputs"`
}

type composedResponderIdentity struct {
	userID  string
	botID   string
	subtype string
}

func TestSlackResponderRedactionComposedJourney(t *testing.T) {
	tests := []struct {
		name        string
		token       string
		integration composedResponderIdentity
	}{
		{
			name:  "user token owner remains answerable",
			token: "xoxp-real-session-sentinel-4242",
			integration: composedResponderIdentity{
				userID: composedResponderOwnerID,
			},
		},
		{
			name:  "bot token owner remains answerable",
			token: "xoxb-real-session-sentinel-4242",
			integration: composedResponderIdentity{
				userID: "U-APP", botID: "B-APP", subtype: "bot_message",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runSlackResponderComposedJourney(t, test.token, test.integration)
		})
	}
}

func runSlackResponderComposedJourney(
	t *testing.T,
	token string,
	integration composedResponderIdentity,
) {
	stateDir := t.TempDir()
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repository fixture: %v", err)
	}
	cfg := config.NewDefault()
	cfg.Repos[composedNeedsInputRepo] = config.RepoConfig{Path: repoPath}
	cfg.Slack = &config.SlackConfig{
		Enabled: true,
		Token:   token,
		DefaultRecipients: []config.SlackRecipient{{
			TypedText: "@owner", Kind: string(ports.SlackRecipientUser),
			ID: composedResponderOwnerID, DisplayName: "Owner",
		}},
	}

	store := feature.NewStore(stateDir)
	seedComposedResponderFeature(t, store, repoPath)
	manager := feature.NewManager(store, cfg)
	eventCh := make(chan interface{}, 64)
	domainCh := make(chan ports.Event, 16)
	sessions := session.NewManager(eventCh)
	t.Cleanup(sessions.Shutdown)

	implementationResults := make(chan *agent.OrchestratorResult)
	var dispatchMu sync.Mutex
	dispatches := 0
	dispatchedPlan := ""
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: manager, Store: store}, orchestrator.Hooks{})
	orch.SetRunMultiRepoImplFn(func(
		_ *feature.Feature,
		planPath string,
		_ ...agent.KBInfo,
	) (chan *agent.OrchestratorResult, error) {
		dispatchMu.Lock()
		dispatches++
		dispatchedPlan = planPath
		dispatchMu.Unlock()
		return implementationResults, nil
	})
	t.Cleanup(func() { close(implementationResults) })

	target := &composedResponderMutationTarget{serverMutationTarget: &serverMutationTarget{
		cfg:             cfg,
		store:           store,
		sessions:        sessions,
		permissionCache: permission.NewCache(nil),
		orch:            orch,
	}}
	pendingRelay := &slackPendingInputRelay{}
	answerRelay := &slackAnswerRelay{}
	responderClock := newComposedResponderClock()
	fake := testsupport.New(t)
	fake.SetOwnUserID(integration.userID)
	fake.SetDefault(composedResponderSlackAPI(token))
	t.Setenv(slackintegration.EnvSlackAPIBase, fake.URL())

	observer := observe.New(true, stateDir, false, "", false, "")
	notifier := slackintegration.NewNotifier(slackintegration.NotifierOptions{
		Settings:       target,
		Store:          store,
		StateDir:       stateDir,
		Observer:       observer,
		Pending:        pendingRelay,
		Answer:         answerRelay,
		ResponderClock: responderClock,
	})
	notifier.SetServerName("Composed responder test")
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
		BindSlackPendingInputSource: pendingRelay.bind,
		BindSlackAnswerPort:         answerRelay.bind,
		DisableHostValidation:       true,
	})
	notifier.SignalReady()
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
		Type: ports.FeatureStarted, FeatureID: composedResponderFeatureID,
	})
	notifier.DomainEventTap(ports.Event{
		Type: ports.PhaseStarted, FeatureID: composedResponderFeatureID, Phase: feature.PhaseImplement,
	})
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		return rootSlackPosts(fake) == 1 && len(threadPostsForChannel(fake, composedResponderChannelID)) >= 1
	})

	providerResult := filepath.Join(t.TempDir(), "permission-response.jsonl")
	script := writeComposedResponderProvider(t, providerResult)
	sess, err := sessions.StartSession(
		composedResponderSessionID,
		composedResponderFeatureID,
		feature.PhaseImplement,
		[]string{"bash", script},
		filepath.Dir(script),
		nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"},
	)
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		record := readComposedResponderRecord(t, stateDir)
		return pendingTag(record, "permission") == "#1" &&
			pendingMessageTSValue(record, "permission") != ""
	})
	record := readComposedResponderRecord(t, stateDir)
	permissionTS := pendingMessageTS(t, record, "permission")
	seedComposedResponderThread(
		t, fake, record, integration,
		testsupport.Message{
			TS: permissionTS, ThreadTS: recordRootTS(t, record),
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{composedResponderOwnerID},
			}},
		},
	)

	responderClock.tickUntil(t, 10*time.Second, func() bool {
		raw, readErr := os.ReadFile(providerResult)
		return readErr == nil &&
			bytes.Contains(raw, []byte(`"behavior":"allow"`)) &&
			hasSlackPostText(fake, "#1 was allowed once by <@U-OWNER> via Slack.")
	})
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for real permission session")
	}
	actualSSE := collectComposedNeedsInputSSE(t, sse.blocks, composedResponderSessionID)
	baselineSSE := runComposedResponderSSEBaseline(t)
	if !slices.Equal(actualSSE, baselineSSE) {
		t.Fatalf("SSE permission events = %v; want responder-free baseline %v", actualSSE, baselineSSE)
	}

	planPath := parkComposedResponderReview(t, store)
	fake.Script("files.completeUploadExternal", testsupport.Response{Body: map[string]any{
		"ok": true,
		"files": []any{map[string]any{
			"id": "F00000001",
			"shares": map[string]any{"private": map[string]any{
				composedResponderChannelID: []any{map[string]any{"ts": "1900000000.000100"}},
			}},
		}},
	}})
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: composedResponderFeatureID,
	})
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		current := readComposedResponderRecord(t, stateDir)
		return pendingTag(current, "review") == "#2" &&
			pendingMessageTSValue(current, "review") != "" &&
			len(fake.Requests("files.completeUploadExternal")) == 1
	})
	record = readComposedResponderRecord(t, stateDir)
	rootTS := recordRootTS(t, record)
	const lgtmTS = "1999999999.000001"
	seedComposedResponderThread(
		t, fake, record, integration,
		testsupport.Message{
			TS: lgtmTS, ThreadTS: rootTS, User: composedResponderOwnerID, Text: "lgtm",
		},
	)
	responderClock.tickUntil(t, 10*time.Second, func() bool {
		return hasSlackReaction(fake, lgtmTS, "question") &&
			hasSlackPostContaining(fake, "approve") &&
			hasSlackPostContaining(fake, "Agentico")
	})
	dispatchMu.Lock()
	firstDispatchCount := dispatches
	dispatchMu.Unlock()
	if firstDispatchCount != 0 {
		t.Fatalf("review dispatches after lgtm = %d; want 0", firstDispatchCount)
	}

	record = readComposedResponderRecord(t, stateDir)
	const approveTS = "1999999999.000002"
	seedComposedResponderThread(
		t, fake, record, integration,
		testsupport.Message{
			TS: lgtmTS, ThreadTS: rootTS, User: composedResponderOwnerID, Text: "lgtm",
			Reactions: []testsupport.Reaction{{
				Name: "question", Count: 1, Users: []string{integration.userID},
			}},
		},
		testsupport.Message{
			TS: approveTS, ThreadTS: rootTS, User: composedResponderOwnerID, Text: "approve",
		},
	)
	responderClock.tickUntil(t, 10*time.Second, func() bool {
		dispatchMu.Lock()
		defer dispatchMu.Unlock()
		return dispatches == 1 &&
			hasSlackReaction(fake, approveTS, "white_check_mark") &&
			hasSlackPostText(fake, "#2 was approved by <@U-OWNER> via Slack.")
	})
	dispatchMu.Lock()
	gotDispatchedPlan := dispatchedPlan
	dispatchMu.Unlock()
	if gotDispatchedPlan != planPath {
		t.Fatalf("review dispatch plan = %q; want %q", gotDispatchedPlan, planPath)
	}
	updated, err := store.Load(composedResponderFeatureID)
	if err != nil {
		t.Fatalf("load reviewed feature: %v", err)
	}
	if updated.Status != feature.StatusImplementing || updated.PendingReviewPhase != nil {
		t.Fatalf("reviewed feature = status %s pending %v; want implementing with gate cleared", updated.Status, updated.PendingReviewPhase)
	}

	permissions, reviews := target.captured()
	if len(permissions) != 1 || permissions[0].Source == nil ||
		permissions[0].Decision != permission.DecisionAllowOnce {
		t.Fatalf("permission mutations = %+v; want one Slack allow-once", permissions)
	}
	if len(reviews) != 1 || reviews[0].Decision != "proceed" ||
		reviews[0].Phase != feature.PhaseImplement.DirName() ||
		!reviews[0].PhasePlan || reviews[0].Roadmap || reviews[0].IsRewind ||
		reviews[0].Source == nil {
		t.Fatalf("review mutations = %+v; want one phase-plan proceed with session metadata", reviews)
	}
	for _, source := range []*ports.AnswerSource{permissions[0].Source, reviews[0].Source} {
		if source.Kind != ports.AnswerSourceSlack || source.Responder == "" {
			t.Fatalf("answer source = %+v; want named Slack responder", source)
		}
		if strings.Contains(source.Responder, token) ||
			strings.Contains(source.Responder, "second-secret") {
			t.Fatalf("answer source leaked credential material: %+v", source)
		}
	}

	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		raw, readErr := os.ReadFile(filepath.Join(stateDir, composedResponderFeatureID, "events.jsonl"))
		return readErr == nil &&
			bytes.Count(raw, []byte(`"event_type":"slack.answer_received"`)) == 2 &&
			bytes.Count(raw, []byte(`"event_type":"slack.answer_rejected"`)) == 1
	})
	if hasSlackPostContaining(fake, "Resolved in Agentico") ||
		hasSlackPostContaining(fake, "No longer pending") {
		t.Fatal("Slack-resolved permission or review produced a closure line")
	}
	if got := fake.CallCount("users.info"); got != 1 {
		t.Fatalf("users.info calls = %d; want one cached owner lookup", got)
	}

	requestRaw, err := json.Marshal(fake.AllRequests())
	if err != nil {
		t.Fatalf("marshal fake Slack requests: %v", err)
	}
	recordRaw := readTestFile(t, filepath.Join(stateDir, composedResponderFeatureID, "slack.yaml"))
	eventsRaw := readTestFile(t, filepath.Join(stateDir, composedResponderFeatureID, "events.jsonl"))
	providerRaw := readTestFile(t, providerResult)
	allSurfaces := bytes.Join(
		[][]byte{requestRaw, logs.Bytes(), recordRaw, eventsRaw, providerRaw},
		[]byte("\n"),
	)
	for _, secret := range []string{token, composedResponderSecret, "alice:second-secret"} {
		if bytes.Contains(allSurfaces, []byte(secret)) {
			t.Fatalf("composed responder surface leaked %q:\n%s", secret, allSurfaces)
		}
	}
	for _, want := range []string{
		"U-OWNER", "allowed once", "approved", "white_check_mark", "question",
	} {
		if !bytes.Contains(requestRaw, []byte(want)) {
			t.Errorf("fake Slack requests missing non-sensitive marker %q", want)
		}
	}
}

func seedComposedResponderFeature(t *testing.T, store *feature.Store, repoPath string) {
	t.Helper()
	f := &feature.Feature{
		ID:            composedResponderFeatureID,
		Name:          "Composed Slack responder",
		Slug:          composedResponderFeatureID,
		Description:   "real-session Slack responder test",
		Created:       time.Now().Add(-time.Hour),
		Status:        feature.StatusImplementing,
		CurrentPhase:  feature.PhaseImplement,
		SchemaVersion: feature.SchemaVersionCurrent,
		Pipeline:      feature.PipelineLarge,
		ActiveRun:     1,
		RunCount:      1,
		Repos: []feature.FeatureRepo{{
			Name: composedNeedsInputRepo, Path: repoPath,
		}},
	}
	f.SetRun(&feature.Run{RunNumber: 1})
	if err := store.Save(f); err != nil {
		t.Fatalf("seed composed responder feature: %v", err)
	}
}

func parkComposedResponderReview(t *testing.T, store *feature.Store) string {
	t.Helper()
	planPath := filepath.Join(store.RunDir(composedResponderFeatureID, 1), "plan", "phase-plan.md")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatalf("create review artifact directory: %v", err)
	}
	if err := os.WriteFile(
		planPath,
		[]byte("# Phase plan\n\n## Tasks\n\n### Task 1: Ship\n\n**Repo:** agentic-orchestrator\n"),
		0o644,
	); err != nil {
		t.Fatalf("write review artifact: %v", err)
	}
	if err := store.Modify(composedResponderFeatureID, func(f *feature.Feature) error {
		f.Status = feature.StatusPlanNeedsReview
		f.CurrentPhase = feature.PhasePlan
		f.CurrentRoadmapPhase = 1
		f.TotalRoadmapPhases = 1
		f.PendingReviewPhase = nil
		f.Artifacts = map[string]string{"phase-1-plan": planPath}
		f.SetRun(&feature.Run{
			RunNumber:           1,
			Artifacts:           f.Artifacts,
			CurrentRoadmapPhase: 1,
			TotalRoadmapPhases:  1,
		})
		return nil
	}); err != nil {
		t.Fatalf("park feature on plan review: %v", err)
	}
	return planPath
}

func writeComposedResponderProvider(t *testing.T, resultPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "responder-provider.sh")
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":"permission-human","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo composed"}}}'
: > %q
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":"permission-human"'* ]]; then
    printf '%%s\n' "$response" >> %q
    break
  fi
done
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"permission completed"}]}}'
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"scripted","total_cost_usd":0}'
`, resultPath, resultPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write composed responder provider: %v", err)
	}
	return path
}

func runComposedResponderSSEBaseline(t *testing.T) []composedNeedsInputSSEEvent {
	t.Helper()
	eventCh := make(chan interface{}, 32)
	sessions := session.NewManager(eventCh)
	t.Cleanup(sessions.Shutdown)
	target := &serverMutationTarget{
		sessions:        sessions,
		permissionCache: permission.NewCache(nil),
	}
	server := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Sessions:              sessions,
		Events:                eventCh,
		Mutations:             target,
		DisableHostValidation: true,
	}))
	t.Cleanup(server.Close)
	sse := openComposedNeedsInputSSE(t, server)
	t.Cleanup(func() {
		sse.cancel()
		_ = sse.body.Close()
	})
	resultPath := filepath.Join(t.TempDir(), "baseline-response.jsonl")
	script := writeComposedResponderProvider(t, resultPath)
	sessionID := composedResponderSessionID + "-baseline"
	sess, err := sessions.StartSession(
		sessionID,
		composedResponderFeatureID,
		feature.PhaseImplement,
		[]string{"bash", script},
		filepath.Dir(script),
		nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"},
	)
	if err != nil {
		t.Fatalf("baseline StartSession() error = %v", err)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "permission-human")
	})
	if _, err := target.AnswerPermission(serverruntime.PermissionAnswerRequest{
		SessionID: sessionID,
		RequestID: "permission-human",
		Decision:  permission.DecisionAllowOnce,
	}); err != nil {
		t.Fatalf("baseline AnswerPermission() error = %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for baseline permission session")
	}
	return collectComposedNeedsInputSSE(t, sse.blocks, sessionID)
}

func composedResponderSlackAPI(token string) func(string, testsupport.Request) testsupport.Response {
	var sequence atomic.Int64
	return func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": composedResponderChannelID},
			}}
		case "users.info":
			return testsupport.Response{Body: map[string]any{
				"ok": true,
				"user": map[string]any{
					"id": composedResponderOwnerID,
					"profile": map[string]any{
						"display_name": "Ada " + token + " " + composedResponderSecret,
						"real_name":    "Ada Lovelace",
					},
				},
			}}
		case "chat.postMessage", "chat.update":
			n := sequence.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": fmt.Sprint(request.Fields["channel"]),
				"ts": fmt.Sprintf("1900000000.%06d", n),
			}}
		default:
			return testsupport.Response{Body: map[string]any{
				"ok": false, "error": "unexpected " + method,
			}}
		}
	}
}

func readComposedResponderRecord(t *testing.T, stateDir string) composedResponderSlackRecord {
	t.Helper()
	var record composedResponderSlackRecord
	raw, err := os.ReadFile(filepath.Join(stateDir, composedResponderFeatureID, "slack.yaml"))
	if err != nil {
		return record
	}
	if err := yaml.Unmarshal(raw, &record); err != nil {
		t.Fatalf("decode composed Slack responder record: %v", err)
	}
	return record
}

func recordRootTS(t *testing.T, record composedResponderSlackRecord) string {
	t.Helper()
	destination, ok := record.Destinations["user:"+composedResponderOwnerID]
	if !ok || destination.RootTS == "" {
		t.Fatalf("Slack destination = %+v; want owner DM root", record.Destinations)
	}
	return destination.RootTS
}

func pendingTag(record composedResponderSlackRecord, kind string) string {
	for _, pending := range record.Pending {
		if pending.Kind == kind {
			return pending.Tag
		}
	}
	return ""
}

func pendingMessageTS(
	t *testing.T,
	record composedResponderSlackRecord,
	kind string,
) string {
	t.Helper()
	if ts := pendingMessageTSValue(record, kind); ts != "" {
		return ts
	}
	t.Fatalf("pending %s message timestamp missing from %+v", kind, record.Pending)
	return ""
}

func pendingMessageTSValue(record composedResponderSlackRecord, kind string) string {
	for _, pending := range record.Pending {
		if pending.Kind == kind {
			if ts := pending.MessageTS["user:"+composedResponderOwnerID]; ts != "" {
				return ts
			}
		}
	}
	return ""
}

func seedComposedResponderThread(
	t *testing.T,
	fake *testsupport.Server,
	record composedResponderSlackRecord,
	integration composedResponderIdentity,
	extra ...testsupport.Message,
) {
	t.Helper()
	rootTS := recordRootTS(t, record)
	seen := map[string]bool{}
	var messages []testsupport.Message
	for _, request := range fake.Requests("chat.postMessage") {
		if requestFieldString(request, "channel") != composedResponderChannelID ||
			request.ReturnedTS == "" {
			continue
		}
		threadTS := requestFieldString(request, "thread_ts")
		message := testsupport.Message{
			TS: request.ReturnedTS, ThreadTS: threadTS,
			User: integration.userID, BotID: integration.botID, Subtype: integration.subtype,
			Text: requestFieldString(request, "text"),
		}
		messages = append(messages, message)
		seen[message.TS] = true
	}
	destination := record.Destinations["user:"+composedResponderOwnerID]
	for _, ts := range destination.Ledger {
		if seen[ts] {
			continue
		}
		messages = append(messages, testsupport.Message{
			TS: ts, ThreadTS: rootTS,
			User: integration.userID, BotID: integration.botID,
			Subtype: "file_share", Text: "integration file share",
		})
		seen[ts] = true
	}
	for _, message := range extra {
		replaced := false
		for i := range messages {
			if messages[i].TS != message.TS {
				continue
			}
			message.User = firstNonemptyTest(message.User, messages[i].User)
			message.BotID = firstNonemptyTest(message.BotID, messages[i].BotID)
			message.Subtype = firstNonemptyTest(message.Subtype, messages[i].Subtype)
			message.Text = firstNonemptyTest(message.Text, messages[i].Text)
			messages[i] = message
			replaced = true
			break
		}
		if !replaced {
			messages = append(messages, message)
		}
	}
	fake.SeedThread(composedResponderChannelID, rootTS, messages)
}

func firstNonemptyTest(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func requestFieldString(request testsupport.Request, key string) string {
	return strings.TrimSpace(fmt.Sprint(request.Fields[key]))
}

func threadPostsForChannel(
	fake *testsupport.Server,
	channel string,
) []testsupport.Request {
	var posts []testsupport.Request
	for _, request := range fake.Requests("chat.postMessage") {
		if requestFieldString(request, "channel") == channel &&
			requestFieldString(request, "thread_ts") != "" {
			posts = append(posts, request)
		}
	}
	return posts
}

func hasSlackPostText(fake *testsupport.Server, text string) bool {
	for _, request := range fake.Requests("chat.postMessage") {
		if requestFieldString(request, "text") == text {
			return true
		}
	}
	return false
}

func hasSlackPostContaining(fake *testsupport.Server, fragment string) bool {
	for _, request := range fake.Requests("chat.postMessage") {
		if strings.Contains(requestFieldString(request, "text"), fragment) ||
			strings.Contains(fmt.Sprint(request.Fields["blocks"]), fragment) {
			return true
		}
	}
	return false
}

func hasSlackReaction(fake *testsupport.Server, timestamp, name string) bool {
	for _, request := range fake.Requests("reactions.add") {
		if requestFieldString(request, "timestamp") == timestamp &&
			requestFieldString(request, "name") == name {
			return true
		}
	}
	return false
}
