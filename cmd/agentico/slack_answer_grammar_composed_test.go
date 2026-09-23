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
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	slackintegration "github.com/doordash-oss/agentic-orchestrator/internal/slack"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

const (
	grammarComposedChannel = "C-GRAMMAR"
	grammarComposedBundle  = "bundle-grammar"
	grammarComposedFree    = "free-grammar"
	grammarComposedFirst   = "Choose the implementation scope?"
	grammarComposedSecond  = "Choose the rollout plan?"
	grammarComposedText    = "What should happen next?"
)

type grammarComposedTarget struct {
	*serverMutationTarget
	mu        sync.Mutex
	questions []serverruntime.AskUserAnswerRequest
	helps     []ports.SlackHelpAnswer
}

func (t *grammarComposedTarget) AnswerAskUser(req serverruntime.AskUserAnswerRequest) (serverruntime.AskUserAnswerResponse, error) {
	t.mu.Lock()
	t.questions = append(t.questions, req)
	t.mu.Unlock()
	return t.serverMutationTarget.AnswerAskUser(req)
}

func (t *grammarComposedTarget) AnswerSlackHelpEntry(req ports.SlackHelpAnswer) error {
	t.mu.Lock()
	t.helps = append(t.helps, req)
	t.mu.Unlock()
	return t.serverMutationTarget.AnswerSlackHelpEntry(req)
}

func (t *grammarComposedTarget) submissions() ([]serverruntime.AskUserAnswerRequest, []ports.SlackHelpAnswer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.questions), slices.Clone(t.helps)
}

type grammarComposedRecord struct {
	Destinations map[string]struct {
		RootTS string `yaml:"root_ts"`
	} `yaml:"destinations"`
	Pending []struct {
		Identity   string            `yaml:"identity"`
		Tag        string            `yaml:"tag"`
		MessageTS  map[string]string `yaml:"message_timestamps"`
		HeldAnswer any               `yaml:"held_answer"`
	} `yaml:"pending_inputs"`
}

func grammarReadRecord(t *testing.T, stateDir string) grammarComposedRecord {
	t.Helper()
	var result grammarComposedRecord
	raw, err := os.ReadFile(filepath.Join(stateDir, composedResponderFeatureID, "slack.yaml"))
	if err == nil {
		if err := yaml.Unmarshal(raw, &result); err != nil {
			t.Fatalf("read Slack record: %v", err)
		}
	}
	return result
}

func grammarFindItem(record grammarComposedRecord, identity string) (string, map[string]string) {
	for _, item := range record.Pending {
		if item.Identity == identity {
			return item.Tag, item.MessageTS
		}
	}
	return "", nil
}

func grammarSeedThreads(t *testing.T, fake *testsupport.Server, record grammarComposedRecord, identity composedResponderIdentity, replies map[string][]testsupport.Message) {
	t.Helper()
	for key, dest := range record.Destinations {
		if dest.RootTS == "" {
			continue
		}
		channel := strings.TrimPrefix(strings.TrimPrefix(key, "user:"), "channel:")
		if key == "user:"+composedResponderOwnerID {
			channel = composedResponderChannelID
		}
		messages := []testsupport.Message{{TS: dest.RootTS, User: identity.userID, BotID: identity.botID, Subtype: identity.subtype}}
		for _, post := range fake.Requests("chat.postMessage") {
			if requestFieldString(post, "channel") != channel || post.ReturnedTS == "" ||
				requestFieldString(post, "thread_ts") != dest.RootTS {
				continue
			}
			messages = append(messages, testsupport.Message{
				TS: post.ReturnedTS, ThreadTS: dest.RootTS, User: identity.userID,
				BotID: identity.botID, Subtype: identity.subtype, Text: requestFieldString(post, "text"),
			})
		}
		for _, reply := range replies[key] {
			messages = append(messages, reply)
		}
		fake.SeedThread(channel, dest.RootTS, messages)
	}
}

func grammarProvider(t *testing.T, answersPath string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grammar-provider.sh")
	bundle, err := json.Marshal(map[string]any{"questions": []map[string]any{
		{"question": grammarComposedFirst, "options": []map[string]string{{"label": "Focused"}, {"label": "Broad scope"}}},
		{"question": grammarComposedSecond, "options": []map[string]string{{"label": "Stage rollout"}, {"label": "Ship now"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	free := `{"questions":[{"question":"What should happen next?"}]}`
	script := fmt.Sprintf(`#!/usr/bin/env bash
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%%s\n' '{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":%s}}'
: > %q
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":%q'* ]]; then printf '%%s\n' "$response" >> %q; break; fi
done
printf '%%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"bundle received"}]}}'
printf '%%s\n' '{"type":"control_request","request_id":%q,"request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":%s}}'
while IFS= read -r response; do
  if [[ "$response" == *'"request_id":%q'* ]]; then printf '%%s\n' "$response" >> %q; break; fi
done
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"scripted","total_cost_usd":0}'
`, grammarComposedBundle, bundle, answersPath, grammarComposedBundle, answersPath,
		grammarComposedFree, free, grammarComposedFree, answersPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSlackAnswerGrammarComposedRealSession(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		identity    composedResponderIdentity
	}{
		{"user token", "xoxp-grammar-composed-987654", composedResponderIdentity{userID: composedResponderOwnerID}},
		{"bot token", "xoxb-grammar-composed-987654", composedResponderIdentity{userID: "U-APP", botID: "B-APP", subtype: "bot_message"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runSlackAnswerGrammarComposed(t, tc.token, tc.identity)
		})
	}
}

func runSlackAnswerGrammarComposed(t *testing.T, token string, identity composedResponderIdentity) {
	t.Helper()
	stateDir, repo := t.TempDir(), filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewDefault()
	cfg.Repos[composedNeedsInputRepo] = config.RepoConfig{Path: repo}
	cfg.Slack = &config.SlackConfig{Enabled: true, Token: token, DefaultRecipients: []config.SlackRecipient{
		{TypedText: "@owner", Kind: string(ports.SlackRecipientUser), ID: composedResponderOwnerID, DisplayName: "Owner"},
		{TypedText: "#team", Kind: string(ports.SlackRecipientChannel), ID: grammarComposedChannel, DisplayName: "Team"},
	}}
	store := feature.NewStore(stateDir)
	seedComposedResponderFeature(t, store, repo)
	events := make(chan interface{}, 64)
	sessions := session.NewManager(events)
	t.Cleanup(sessions.Shutdown)
	target := &grammarComposedTarget{serverMutationTarget: &serverMutationTarget{cfg: cfg, store: store, sessions: sessions}}
	pendingRelay, answerRelay := &slackPendingInputRelay{}, &slackAnswerRelay{}
	clock := newComposedResponderClock()
	fake := testsupport.New(t)
	fake.SetOwnUserID(identity.userID)
	fake.SetDefault(composedResponderSlackAPI(token))
	t.Setenv(slackintegration.EnvSlackAPIBase, fake.URL())
	notifier := slackintegration.NewNotifier(slackintegration.NotifierOptions{
		Settings: target, Store: store, StateDir: stateDir, Observer: observe.New(true, stateDir, false, "", false, ""),
		Pending: pendingRelay, Answer: answerRelay, ResponderClock: clock,
	})
	notifier.SetServerName("Grammar composed")
	notifier.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		notifier.Stop(ctx)
	})
	handler := serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime: serverruntime.RuntimeIdentity{StateDir: stateDir}, Features: store, FeatureStore: store,
		Config: cfg, Sessions: sessions, Events: events, DomainEvents: make(chan ports.Event, 16),
		RuntimeEventTap: notifier.RuntimeMessageTap, DomainEventTap: notifier.DomainEventTap,
		Mutations: target, BindSlackPendingInputSource: pendingRelay.bind,
		BindSlackAnswerPort: answerRelay.bind, DisableHostValidation: true,
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	sse := openComposedNeedsInputSSE(t, httpServer)
	t.Cleanup(func() { sse.cancel(); _ = sse.body.Close() })
	notifier.SignalReady()
	notifier.DomainEventTap(ports.Event{Type: ports.FeatureStarted, FeatureID: composedResponderFeatureID})
	notifier.DomainEventTap(ports.Event{Type: ports.PhaseStarted, FeatureID: composedResponderFeatureID, Phase: feature.PhaseImplement})
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		record := grammarReadRecord(t, stateDir)
		return len(record.Destinations) == 2 && record.Destinations["user:"+composedResponderOwnerID].RootTS != "" &&
			record.Destinations["channel:"+grammarComposedChannel].RootTS != ""
	})
	resultPath := filepath.Join(t.TempDir(), "answers.jsonl")
	script := grammarProvider(t, resultPath)
	sess, err := sessions.StartSession(composedResponderSessionID, composedResponderFeatureID,
		feature.PhaseImplement, []string{"bash", script}, filepath.Dir(script), nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		record := grammarReadRecord(t, stateDir)
		_, first := grammarFindItem(record, "question:"+grammarComposedBundle+":0")
		_, second := grammarFindItem(record, "question:"+grammarComposedBundle+":1")
		return first["user:"+composedResponderOwnerID] != "" && second["channel:"+grammarComposedChannel] != ""
	})
	record := grammarReadRecord(t, stateDir)
	firstTag, firstTS := grammarFindItem(record, "question:"+grammarComposedBundle+":0")
	secondTag, _ := grammarFindItem(record, "question:"+grammarComposedBundle+":1")
	if firstTag == "" || secondTag == "" || firstTag == secondTag {
		t.Fatalf("bundle tags = %q, %q", firstTag, secondTag)
	}
	replies := map[string][]testsupport.Message{}
	userKey := "user:" + composedResponderOwnerID
	channelKey := "channel:" + grammarComposedChannel
	replies[userKey] = append(replies[userKey], testsupport.Message{
		TS: firstTS[userKey], ThreadTS: record.Destinations[userKey].RootTS,
		Reactions: []testsupport.Reaction{{Name: "one", Count: 1, Users: []string{composedResponderOwnerID}}},
	})
	grammarSeedThreads(t, fake, record, identity, replies)
	clock.tickUntil(t, 10*time.Second, func() bool {
		return composedSlackPostTextCount(fake, firstTag+" was answered 'Focused' by <@U-OWNER> via Slack - 1 of 2 collected, not yet submitted; still waiting on "+secondTag+".") == 2
	})
	if got, _ := target.submissions(); len(got) != 0 {
		t.Fatalf("bundle submitted before second answer: %+v", got)
	}
	const secondReplyTS = "1999999999.000001"
	replies[channelKey] = append(replies[channelKey], testsupport.Message{
		TS: secondReplyTS, ThreadTS: record.Destinations[channelKey].RootTS,
		User: composedResponderOwnerID, Text: secondTag + " 2",
	})
	grammarSeedThreads(t, fake, grammarReadRecord(t, stateDir), identity, replies)
	clock.tickUntil(t, 10*time.Second, func() bool {
		submitted, _ := target.submissions()
		return len(submitted) == 1 && hasSlackReaction(fake, secondReplyTS, "white_check_mark")
	})
	submitted, _ := target.submissions()
	if submitted[0].Answers[grammarComposedFirst] != "Focused" ||
		submitted[0].Answers[grammarComposedSecond] != "Ship now" ||
		submitted[0].Source == nil || submitted[0].Source.Kind != ports.AnswerSourceSlack {
		t.Fatalf("bundle mutation = %+v", submitted[0])
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		r := grammarReadRecord(t, stateDir)
		_, freeTS := grammarFindItem(r, "question:"+grammarComposedFree+":0")
		return freeTS[userKey] != "" &&
			composedSlackPostTextCount(fake, firstTag+" was answered 'Focused' by <@U-OWNER> via Slack.") == 2 &&
			composedSlackPostTextCount(fake, secondTag+" was answered 'Ship now' by <@U-OWNER> via Slack.") == 2
	})
	record = grammarReadRecord(t, stateDir)
	const freeReplyTS = "1999999999.000002"
	const freeText = "Use staging and keep the existing checks"
	replies[userKey] = append(replies[userKey], testsupport.Message{
		TS: freeReplyTS, ThreadTS: record.Destinations[userKey].RootTS,
		User: composedResponderOwnerID, Text: freeText,
	})
	grammarSeedThreads(t, fake, record, identity, replies)
	clock.tickUntil(t, 10*time.Second, func() bool {
		submitted, _ := target.submissions()
		return len(submitted) == 2 && hasSlackReaction(fake, freeReplyTS, "white_check_mark")
	})
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("scripted provider did not finish")
	}
	qa := sess.QALog()
	if len(qa) != 3 {
		t.Fatalf("Q&A pairs = %+v, want three", qa)
	}
	for i, want := range []struct{ question, answer string }{
		{grammarComposedFirst, "Focused"}, {grammarComposedSecond, "Ship now"}, {grammarComposedText, freeText},
	} {
		if qa[i].Question != want.question || qa[i].Answer != want.answer ||
			qa[i].Source == nil || qa[i].Source.Kind != ports.AnswerSourceSlack {
			t.Fatalf("Q&A %d = %+v, want %+v with Slack source", i, qa[i], want)
		}
		if strings.Contains(qa[i].Source.Responder, token) ||
			strings.Contains(qa[i].Source.Responder, "second-secret") {
			t.Fatalf("Q&A %d source leaked credentials: %+v", i, qa[i].Source)
		}
	}
	providerRaw := readTestFile(t, resultPath)
	if len(bytes.Split(bytes.TrimSpace(providerRaw), []byte("\n"))) != 2 ||
		!bytes.Contains(providerRaw, []byte(freeText)) {
		t.Fatalf("provider answers = %q", providerRaw)
	}
	actualSSE := collectComposedNeedsInputSSE(t, sse.blocks, composedResponderSessionID)
	if len(actualSSE) == 0 {
		t.Fatal("no prompt events on composed SSE stream")
	}
	if baseline := grammarBaselineSSE(t); !slices.Equal(actualSSE, baseline) {
		t.Fatalf("SSE events with responder = %+v; responder-free = %+v", actualSSE, baseline)
	}

	activeScript := filepath.Join(t.TempDir(), "active-help-provider.sh")
	if err := os.WriteFile(activeScript, []byte(`#!/usr/bin/env bash
printf '%s\n' '{"type":"system","subtype":"init","session_id":"scripted","model":"test"}'
printf '%s\n' '{"type":"control_request","request_id":"live-help-control","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Unrelated active session?"}]}}}'
while IFS= read -r response; do :; done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	active, err := sessions.StartSession(composedResponderSessionID+"-active-help",
		composedResponderFeatureID, feature.PhaseImplement, []string{"bash", activeScript},
		filepath.Dir(activeScript), nil, &session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	waitForComposedNeedsInput(t, 5*time.Second, func() bool {
		return hasPendingRequest(active, "live-help-control")
	})
	helpAt := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	gatePath := filepath.Join(t.TempDir(), "gate.yaml")
	if err := os.WriteFile(gatePath, []byte("summary: Verification needs a person\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Modify(composedResponderFeatureID, func(f *feature.Feature) error {
		f.HelpQueue = []feature.HelpRequest{
			{Question: "Which stage?", Time: helpAt, Pending: true},
			{Question: "Who owns release?", Time: helpAt.Add(time.Second), Pending: true},
		}
		f.Status = feature.StatusNeedUserInput
		f.PendingNeedUserInputPath = gatePath
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	notifier.DomainEventTap(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: composedResponderFeatureID})
	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: composedResponderSessionID, FeatureID: composedResponderFeatureID,
		Phase: feature.PhaseImplement, Message: llm.SDKMessage{Type: "assistant"},
	})
	firstHelpID := ports.SlackHelpEntryIdentity(composedResponderFeatureID, helpAt, "Which stage?")
	secondHelpID := ports.SlackHelpEntryIdentity(composedResponderFeatureID, helpAt.Add(time.Second), "Who owns release?")
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		r := grammarReadRecord(t, stateDir)
		_, first := grammarFindItem(r, firstHelpID)
		_, second := grammarFindItem(r, secondHelpID)
		return first[channelKey] != "" && second[channelKey] != "" && grammarGateTag(r) != ""
	})
	record = grammarReadRecord(t, stateDir)
	gateTag := grammarGateTag(record)
	const gateReplyTS = "1999999999.000003"
	replies[channelKey] = append(replies[channelKey], testsupport.Message{
		TS: gateReplyTS, ThreadTS: record.Destinations[channelKey].RootTS,
		User: composedResponderOwnerID, Text: gateTag + " acknowledge gate",
	})
	grammarSeedThreads(t, fake, record, identity, replies)
	clock.tickUntil(t, 10*time.Second, func() bool {
		return hasSlackReaction(fake, gateReplyTS, "question")
	})
	if _, got := target.submissions(); len(got) != 0 {
		t.Fatalf("gate reply reached help mutation: %+v", got)
	}
	helpTag, _ := grammarFindItem(record, firstHelpID)
	const helpReplyTS = "1999999999.000004"
	replies[channelKey] = append(replies[channelKey], testsupport.Message{
		TS: helpReplyTS, ThreadTS: record.Destinations[channelKey].RootTS,
		User: composedResponderOwnerID, Text: helpTag + " use staging",
	})
	grammarSeedThreads(t, fake, grammarReadRecord(t, stateDir), identity, replies)
	clock.tickUntil(t, 10*time.Second, func() bool {
		_, submitted := target.submissions()
		return len(submitted) == 1 && hasSlackReaction(fake, helpReplyTS, "white_check_mark")
	})
	_, helps := target.submissions()
	if helps[0].EntryIdentity != firstHelpID || helps[0].Source.Kind != ports.AnswerSourceSlack {
		t.Fatalf("help mutation = %+v, want exact first entry and Slack source", helps[0])
	}
	loaded, err := store.Load(composedResponderFeatureID)
	if err != nil || loaded.HelpQueue[0].Pending || loaded.HelpQueue[0].Answer != "use staging" ||
		!loaded.HelpQueue[1].Pending || loaded.HelpQueue[1].Answer != "" {
		t.Fatalf("help queue = %+v, %v", loaded.HelpQueue, err)
	}
	if tag, _ := grammarFindItem(grammarReadRecord(t, stateDir), secondHelpID); tag == "" {
		t.Fatal("second help entry retired after answering first")
	}
	if !hasPendingRequest(active, "live-help-control") {
		t.Fatal("exact help reply was diverted into the active session")
	}
	waitForComposedNeedsInput(t, 10*time.Second, func() bool {
		return composedSlackPostTextCount(fake, helpTag+" was answered by <@U-OWNER> via Slack.") == 2 &&
			bytes.Count(readTestFile(t, filepath.Join(stateDir, composedResponderFeatureID, "events.jsonl")),
				[]byte(`"event_type":"slack.answer_received"`)) >= 4
	})
	if hasSlackPostContaining(fake, "was resolved in Agentico") {
		t.Fatal("Slack-answered question or help produced an Agentico closure")
	}
	requests, err := json.Marshal(fake.AllRequests())
	if err != nil {
		t.Fatal(err)
	}
	recordRaw := readTestFile(t, filepath.Join(stateDir, composedResponderFeatureID, "slack.yaml"))
	eventsRaw := readTestFile(t, filepath.Join(stateDir, composedResponderFeatureID, "events.jsonl"))
	for _, secret := range []string{token, composedResponderSecret, "alice:second-secret"} {
		for _, surface := range [][]byte{requests, recordRaw, eventsRaw} {
			if bytes.Contains(surface, []byte(secret)) {
				t.Fatalf("secret %q leaked into Slack requests, record, or events", secret)
			}
		}
	}
	if bytes.Contains(requests, []byte(freeText)) || bytes.Contains(eventsRaw, []byte(freeText)) ||
		bytes.Contains(recordRaw, []byte(freeText)) {
		t.Fatal("free-text answer was echoed to Slack or stored in Slack metadata")
	}
}

func grammarGateTag(record grammarComposedRecord) string {
	for _, item := range record.Pending {
		if strings.HasPrefix(item.Identity, "gate:") {
			return item.Tag
		}
	}
	return ""
}

func grammarBaselineSSE(t *testing.T) []composedNeedsInputSSEEvent {
	t.Helper()
	events := make(chan interface{}, 64)
	sessions := session.NewManager(events)
	t.Cleanup(sessions.Shutdown)
	target := &serverMutationTarget{sessions: sessions}
	httpServer := httptest.NewServer(serverruntime.NewHandler(serverruntime.HandlerOptions{
		Sessions: sessions, Events: events, Mutations: target, DisableHostValidation: true,
	}))
	t.Cleanup(httpServer.Close)
	sse := openComposedNeedsInputSSE(t, httpServer)
	t.Cleanup(func() { sse.cancel(); _ = sse.body.Close() })
	path := filepath.Join(t.TempDir(), "baseline.jsonl")
	script := grammarProvider(t, path)
	id := composedResponderSessionID + "-baseline"
	sess, err := sessions.StartSession(id, composedResponderFeatureID, feature.PhaseImplement,
		[]string{"bash", script}, filepath.Dir(script), nil,
		&session.SessionOpts{RepoName: composedNeedsInputRepo, ProviderName: "scripted"})
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []struct {
		requestID string
		values    map[string]string
	}{
		{grammarComposedBundle, map[string]string{grammarComposedFirst: "Focused", grammarComposedSecond: "Ship now"}},
		{grammarComposedFree, map[string]string{grammarComposedText: "Use staging and keep the existing checks"}},
	} {
		waitForComposedNeedsInput(t, 5*time.Second, func() bool { return hasPendingRequest(sess, answer.requestID) })
		if _, err := target.AnswerAskUser(serverruntime.AskUserAnswerRequest{
			SessionID: id, RequestID: answer.requestID, Answers: answer.values,
		}); err != nil {
			t.Fatalf("baseline answer %s: %v", answer.requestID, err)
		}
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("baseline provider did not finish")
	}
	return collectComposedNeedsInputSSE(t, sse.blocks, id)
}
