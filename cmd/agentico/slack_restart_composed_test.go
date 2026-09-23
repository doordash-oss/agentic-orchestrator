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
	"context"
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

func TestSlackRestartComposedRealSessionAndReviewDecision(t *testing.T) {
	const permissionID = "restart-permission"
	const reviewID = composedResponderFeatureID
	stateDir := t.TempDir()
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Repos[composedNeedsInputRepo] = config.RepoConfig{Path: repoPath}
	cfg.Slack = &config.SlackConfig{
		Enabled: true, Token: "xoxp-restart-composed",
		DefaultRecipients: []config.SlackRecipient{
			{TypedText: "@owner", Kind: string(ports.SlackRecipientUser), ID: composedResponderOwnerID, DisplayName: "Owner"},
			{TypedText: "#eng", Kind: string(ports.SlackRecipientChannel), ID: "C-ENG", DisplayName: "#eng"},
		},
	}
	store := feature.NewStore(stateDir)
	seedComposedResponderFeature(t, store, repoPath)
	planPath := parkComposedResponderReview(t, store)
	permissionFeature := &feature.Feature{
		ID: permissionID, Name: "Permission in flight", Slug: permissionID,
		SchemaVersion: feature.SchemaVersionCurrent, Status: feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement, Pipeline: feature.PipelineLarge,
		Repos: []feature.FeatureRepo{{Name: composedNeedsInputRepo, Path: repoPath}},
	}
	if err := store.Save(permissionFeature); err != nil {
		t.Fatal(err)
	}
	events := make(chan interface{}, 64)
	sessions := session.NewManager(events)
	t.Cleanup(sessions.Shutdown)
	orch := orchestrator.New(orchestrator.Deps{Lifecycle: feature.NewManager(store, cfg), Store: store}, orchestrator.Hooks{})
	implementationResults := make(chan *agent.OrchestratorResult)
	t.Cleanup(func() { close(implementationResults) })
	var dispatches atomic.Int64
	orch.SetRunMultiRepoImplFn(func(_ *feature.Feature, path string, _ ...agent.KBInfo) (chan *agent.OrchestratorResult, error) {
		if path != planPath {
			t.Errorf("dispatched plan = %q, want %q", path, planPath)
		}
		dispatches.Add(1)
		return implementationResults, nil
	})
	target := &composedResponderMutationTarget{serverMutationTarget: &serverMutationTarget{
		cfg: cfg, store: store, sessions: sessions, permissionCache: permission.NewCache(nil), orch: orch,
	}}
	fake := testsupport.New(t)
	fake.SetOwnUserID(composedResponderOwnerID)
	api := composedResponderSlackAPI(cfg.Slack.Token)
	fake.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method == "files.completeUploadExternal" {
			return testsupport.Response{Body: map[string]any{"ok": true}}
		}
		return api(method, request)
	})
	t.Setenv(slackintegration.EnvSlackAPIBase, fake.URL())
	observer := observe.New(true, stateDir, false, "", false, "")
	deliveryClock := &slackRestartDeliveryClock{now: time.Now()}
	makeNotifier := func(pending *slackPendingInputRelay, answer *slackAnswerRelay, clock *composedResponderClock) *slackintegration.Notifier {
		n := slackintegration.NewNotifier(slackintegration.NotifierOptions{
			Settings: target, Store: store, StateDir: stateDir, Observer: observer,
			Pending: pending, Answer: answer, Clock: deliveryClock, ResponderClock: clock,
		})
		n.Start()
		return n
	}
	bindPorts := func(n *slackintegration.Notifier, pending *slackPendingInputRelay, answer *slackAnswerRelay) *httptest.Server {
		handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
			Runtime:  serverruntime.RuntimeIdentity{StateDir: stateDir},
			Features: store, FeatureStore: store, Config: cfg, Sessions: sessions,
			Events: events, RuntimeEventTap: n.RuntimeMessageTap, DomainEventTap: n.DomainEventTap,
			Mutations: target, BindSlackPendingInputSource: pending.bind, BindSlackAnswerPort: answer.bind,
			DisableHostValidation: true,
		})
		n.SetServerName("Composed restart")
		n.SignalReady()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	pending := &slackPendingInputRelay{}
	answer := &slackAnswerRelay{}
	first := makeNotifier(pending, answer, newComposedResponderClock())
	httpServer := bindPorts(first, pending, answer)
	sse := openComposedNeedsInputSSE(t, httpServer)
	t.Cleanup(func() {
		sse.cancel()
		_ = sse.body.Close()
	})
	first.DomainEventTap(ports.Event{Type: ports.FeatureStarted, FeatureID: permissionID})
	first.DomainEventTap(ports.Event{Type: ports.ReviewRequired, FeatureID: reviewID})
	script := writeSlackRestartBlockedProvider(t)
	sess, err := sessions.StartSession("restart-session", permissionID, feature.PhaseImplement,
		[]string{"bash", script}, filepath.Dir(script), nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		if !hasPendingRequest(sess, "permission-restart") {
			return false
		}
		pendingRecord, reviewRecord := restartComposedRecord(t, stateDir, permissionID), restartComposedRecord(t, stateDir, reviewID)
		return len(pendingRecord.Pending) == 1 && len(reviewRecord.Pending) == 1 &&
			len(pendingRecord.Pending[0].MessageTS) == 2 && len(reviewRecord.Pending[0].MessageTS) == 2
	})
	if err := firstStop(first); err != nil {
		t.Fatal(err)
	}
	initialRoots := rootSlackPosts(fake)
	initialEdits := fake.CallCount("chat.update")
	if initialRoots != 4 {
		t.Fatalf("root cards before restart = %d, want four", initialRoots)
	}
	if err := sessions.StopSession("restart-session"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("permission session did not stop")
	}
	actualSSE := collectComposedNeedsInputSSE(t, sse.blocks, "restart-session")
	baselineSSE := slackRestartStoppedSessionSSEBaseline(t, script)
	if !slices.Equal(actualSSE, baselineSSE) {
		t.Fatalf("SSE across restart = %v; without Slack = %v", actualSSE, baselineSSE)
	}
	permissionBefore := readTestFile(t, filepath.Join(stateDir, permissionID, "feature.yaml"))
	reviewBefore := readTestFile(t, filepath.Join(stateDir, reviewID, "feature.yaml"))
	runBefore := readTestFile(t, filepath.Join(store.RunDir(reviewID, 1), "run.yaml"))
	pending = &slackPendingInputRelay{}
	answer = &slackAnswerRelay{}
	clock := newComposedResponderClock()
	second := makeNotifier(pending, answer, clock)
	t.Cleanup(func() { _ = firstStop(second) })
	if got := fake.CallCount("chat.update"); got != initialEdits {
		t.Fatalf("restart ran before ports were bound: %d edits, want %d", got, initialEdits)
	}
	bindPorts(second, pending, answer)
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		return composedSlackPostTextCount(fake, "#1 is no longer pending.") == 2 &&
			fake.CallCount("chat.update") >= initialEdits+4
	})
	if got := rootSlackPosts(fake); got != initialRoots {
		t.Fatalf("restart posted duplicate root cards: %d, want %d", got, initialRoots)
	}
	reloadedPermission := restartComposedRecord(t, stateDir, permissionID)
	reloadedReview := restartComposedRecord(t, stateDir, reviewID)
	if len(reloadedPermission.Pending) != 0 || len(reloadedReview.Pending) != 1 ||
		reloadedReview.Pending[0].Kind != string(ports.SlackPendingReview) {
		t.Fatalf("reconciled pending: permission=%+v review=%+v", reloadedPermission.Pending, reloadedReview.Pending)
	}
	if !slices.Equal(permissionBefore, readTestFile(t, filepath.Join(stateDir, permissionID, "feature.yaml"))) ||
		!slices.Equal(reviewBefore, readTestFile(t, filepath.Join(stateDir, reviewID, "feature.yaml"))) ||
		!slices.Equal(runBefore, readTestFile(t, filepath.Join(store.RunDir(reviewID, 1), "run.yaml"))) {
		t.Fatal("reconciliation changed a feature or run record")
	}
	channel := reloadedReview.Destinations["channel:C-ENG"]
	const approveTS = "1999999999.000991"
	var messages []testsupport.Message
	for _, ts := range channel.Ledger {
		messages = append(messages, testsupport.Message{TS: ts, ThreadTS: channel.RootTS, User: composedResponderOwnerID})
	}
	messages = append(messages, testsupport.Message{
		TS: approveTS, ThreadTS: channel.RootTS, User: composedResponderOwnerID, Text: "approve",
	})
	fake.SeedThread("C-ENG", channel.RootTS, messages)
	clock.tick(t)
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		_, reviews := target.captured()
		return len(reviews) == 1 && hasSlackReaction(fake, approveTS, "white_check_mark") &&
			composedSlackPostTextCount(fake, "#1 was approved by <@U-OWNER> via Slack.") == 2
	})
	_, reviews := target.captured()
	if len(reviews) != 1 || reviews[0].Decision != "proceed" || !reviews[0].PhasePlan ||
		reloadedReview.Pending[0].SourceRevision == "" || dispatches.Load() != 1 {
		t.Fatalf("review decision = %+v, dispatches=%d", reviews, dispatches.Load())
	}
	if got := composedSlackPostTextCount(fake, "#1 is no longer pending."); got != 2 {
		t.Fatalf("permission closure count = %d, want two", got)
	}
	if got := fake.CallCount("chat.update") - initialEdits; got != 4 {
		t.Fatalf("restart card refreshes = %d, want both features in both destinations", got)
	}
	reviewed, err := store.Load(reviewID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.Status != feature.StatusImplementing || reviewed.PendingReviewPhase != nil {
		t.Fatalf("review decision did not clear the real review gate: status=%s pending=%v",
			reviewed.Status, reviewed.PendingReviewPhase)
	}
}

// Delivery pacing is virtual here; the responder's polling clock stays manually stepped.
type slackRestartDeliveryClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *slackRestartDeliveryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *slackRestartDeliveryClock) Sleep(ctx context.Context, duration time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	return ctx.Err() == nil
}

func slackRestartStoppedSessionSSEBaseline(t *testing.T, script string) []composedNeedsInputSSEEvent {
	t.Helper()
	events := make(chan interface{}, 64)
	sessions := session.NewManager(events)
	t.Cleanup(sessions.Shutdown)
	target := &serverMutationTarget{sessions: sessions, permissionCache: permission.NewCache(nil)}
	server := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Sessions: sessions, Events: events, Mutations: target, DisableHostValidation: true,
	}))
	t.Cleanup(server.Close)
	sse := openComposedNeedsInputSSE(t, server)
	t.Cleanup(func() {
		sse.cancel()
		_ = sse.body.Close()
	})
	sess, err := sessions.StartSession("restart-session-baseline", "restart-permission",
		feature.PhaseImplement, []string{"bash", script}, filepath.Dir(script), nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(sess, "permission-restart")
	})
	if err := sessions.StopSession("restart-session-baseline"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("baseline session did not stop")
	}
	return collectComposedNeedsInputSSE(t, sse.blocks, "restart-session-baseline")
}

func restartComposedRecord(t *testing.T, stateDir, id string) struct {
	Destinations map[string]struct {
		RootTS string   `yaml:"root_ts"`
		Ledger []string `yaml:"ledger"`
	} `yaml:"destinations"`
	Pending []struct {
		Kind           string            `yaml:"kind"`
		SourceRevision string            `yaml:"source_revision"`
		MessageTS      map[string]string `yaml:"message_timestamps"`
	} `yaml:"pending_inputs"`
} {
	t.Helper()
	var record struct {
		Destinations map[string]struct {
			RootTS string   `yaml:"root_ts"`
			Ledger []string `yaml:"ledger"`
		} `yaml:"destinations"`
		Pending []struct {
			Kind           string            `yaml:"kind"`
			SourceRevision string            `yaml:"source_revision"`
			MessageTS      map[string]string `yaml:"message_timestamps"`
		} `yaml:"pending_inputs"`
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, id, "slack.yaml"))
	if err == nil {
		err = yaml.Unmarshal(raw, &record)
	}
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return record
}

func firstStop(n *slackintegration.Notifier) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n.Stop(ctx)
	return ctx.Err()
}

func writeSlackRestartBlockedProvider(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blocked-provider.sh")
	body := `#!/usr/bin/env bash
printf '%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%s\n' '{"type":"control_request","request_id":"permission-restart","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"echo composed"}}}'
while IFS= read -r response; do
  [[ "$response" == *'"request_id":"permission-restart"'* ]] && break
done
`
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
