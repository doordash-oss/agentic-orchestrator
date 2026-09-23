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

package slack

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type restartPendingSource struct {
	items map[string][]ports.SlackPendingInput
	errs  map[string]error
}

func (s *restartPendingSource) PendingSlackInputs(id string) ([]ports.SlackPendingInput, error) {
	if err := s.errs[id]; err != nil {
		return nil, err
	}
	return s.items[id], nil
}

func TestSlackRestartPendingSourceUnavailableDoesNotPollOrRetire(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", ports.SlackRecipient{
		Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng",
	}))
	h.seedFeature("feature-1", nil)
	const key = "channel:C-ENG"
	const root = "100.000001"
	first := pendingInputRecord{
		Identity: "permission:one", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: "one",
		Tag: "#1", MessageTS: map[string]string{key: "100.000002"},
	}
	second := pendingInputRecord{
		Identity: "permission:two", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: "two",
		Tag: "#2", MessageTS: map[string]string{key: "100.000003"},
	}
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{key: {
			Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: root,
			Ledger: []string{root, "100.000002", "100.000003"},
		}},
		Pending: []pendingInputRecord{first, second},
	}
	if err := persistFeatureRecord(h.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	path := recordPath(h.stateDir, "feature-1")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h.server.SeedThread("C-ENG", root, []testsupport.Message{
		{TS: root},
		{TS: "100.000002", ThreadTS: root, Text: "permission one"},
		{TS: "100.000003", ThreadTS: root, Text: "permission two"},
		{TS: "100.000004", ThreadTS: root, User: "U-HUMAN", Text: "allow"},
	})
	source := &restartPendingSource{errs: map[string]error{"feature-1": errors.New("pending source not ready")}}
	answer := &fakeSlackAnswerPort{}
	notifier := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: source, Answer: answer, Clock: h.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	notifier.records["feature-1"] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	var logs bytes.Buffer
	original := log.Writer()
	log.SetOutput(&logs)
	notifier.responderTick()
	log.SetOutput(original)
	t.Cleanup(func() { log.SetOutput(original) })
	if got := logs.String(); strings.Count(got, "pending inputs for feature feature-1") != 1 {
		t.Fatalf("pending source logs = %q; want one source-unavailable line", got)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("record changed while pending source unavailable:\n%s", after)
	}
	if got := h.server.AllRequests(); len(got) != 0 {
		t.Fatalf("Slack requests while pending source unavailable = %#v", got)
	}
	if got := answer.permissionSubmissions(); len(got) != 0 {
		t.Fatalf("answer submissions while pending source unavailable = %#v", got)
	}
	if got := h.observer.all(); len(got) != 0 {
		t.Fatalf("events while pending source unavailable = %#v", got)
	}

	source.errs = nil
	source.items = map[string][]ports.SlackPendingInput{"feature-1": {
		{Kind: ports.SlackPendingPermission, RequestID: "one"},
		{Kind: ports.SlackPendingPermission, RequestID: "two"},
	}}
	h.server.SeedThread("C-ENG", root, []testsupport.Message{{TS: root},
		{TS: "100.000002", ThreadTS: root}, {TS: "100.000003", ThreadTS: root}})
	notifier.responderTick()
	if got := h.server.Requests("chat.postMessage"); len(got) != 0 {
		t.Fatalf("posts after binding with both inputs live = %#v", got)
	}
	source.items["feature-1"] = source.items["feature-1"][1:]
	notifier.responderTick()
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 1 })
	current, err := loadFeatureRecord(h.stateDir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Pending) != 1 || current.Pending[0].Identity != second.Identity {
		t.Fatalf("pending after resolution = %#v; want only second", current.Pending)
	}
	if got := fieldString(h.server.Requests("chat.postMessage")[0], "text"); got != "#1 was resolved in Agentico." {
		t.Fatalf("closure = %q", got)
	}
}

func TestSlackRestartPendingReadFailureIsPerFeature(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", ports.SlackRecipient{
		Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng",
	}))
	source := &restartPendingSource{errs: map[string]error{"feature-1": errors.New("unavailable")}}
	notifier := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: source, Clock: h.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	const key = "channel:C-ENG"
	for i, id := range []string{"feature-1", "feature-2"} {
		h.seedFeature(id, nil)
		root := "100.000001"
		item := pendingInputRecord{
			Identity: "permission:" + id, SourceFeatureID: id,
			Kind: string(ports.SlackPendingPermission), RequestID: id,
			Tag: "#1", MessageTS: map[string]string{key: "100.000002"},
		}
		record := &featureRecord{
			Version: recordVersion,
			Destinations: map[string]destinationRecord{key: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: root,
				Ledger: []string{root, "100.000002"},
			}},
			Pending: []pendingInputRecord{item},
		}
		if err := persistFeatureRecord(h.stateDir, id, record); err != nil {
			t.Fatal(err)
		}
		notifier.records[id] = record
		if i == 0 {
			h.server.SeedThread("C-ENG", root, []testsupport.Message{
				{TS: root}, {TS: "100.000002", ThreadTS: root, Text: "permission"},
			})
		}
	}
	before, err := os.ReadFile(recordPath(h.stateDir, "feature-1"))
	if err != nil {
		t.Fatal(err)
	}
	notifier.responderTick()
	after, err := os.ReadFile(recordPath(h.stateDir, "feature-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed feature's record changed")
	}
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 1 })
	if got := fieldString(h.server.Requests("chat.postMessage")[0], "text"); got != "#1 was resolved in Agentico." {
		t.Fatalf("other feature's closure = %q", got)
	}
	current, err := loadFeatureRecord(h.stateDir, "feature-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Pending) != 0 {
		t.Fatalf("readable feature still pending = %#v", current.Pending)
	}
}
