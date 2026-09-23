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
	"context"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func perFeatureResponderFixture(t *testing.T, section feature.SlackNotifications) (*notifierHarness, *Notifier, *fakeSlackAnswerPort) {
	t.Helper()
	recipients := testRecipients()
	h := newNotifierHarness(t, defaultTestSettings(testToken, recipients...))
	h.seedFeature("F-1", func(f *feature.Feature) { f.SlackNotifications = &section })
	const root = "100.000001"
	record := &featureRecord{
		Version:      recordVersion,
		Destinations: map[string]destinationRecord{},
		Pending: []pendingInputRecord{{
			Identity: "permission:perm-1", SourceFeatureID: "F-1",
			Kind: string(ports.SlackPendingPermission), RequestID: "perm-1", Tag: "#1",
			MessageTS: map[string]string{},
		}},
	}
	for _, recipient := range recipients {
		key := destinationKey(string(recipient.Kind), recipient.ID)
		channel := recipient.ID
		if recipient.Kind == ports.SlackRecipientUser {
			channel = "D-" + recipient.ID
		}
		record.Pending[0].MessageTS[key] = "100.000002"
		record.Destinations[key] = destinationRecord{
			Kind: string(recipient.Kind), SlackID: recipient.ID, ChannelID: channel, RootTS: root,
			Ledger: []string{root, "100.000002"},
			PostingIndex: []postingIndexEntry{{
				Identity: "permission:perm-1", MessageTS: "100.000002", Tag: "#1",
			}},
		}
		h.server.SeedThread(channel, root, []testsupport.Message{
			{TS: root},
			{TS: "100.000002", ThreadTS: root, Text: "permission"},
		})
	}
	if err := persistFeatureRecord(h.stateDir, "F-1", record); err != nil {
		t.Fatal(err)
	}
	h.pending.setFromRecord("F-1", record)
	answers := &fakeSlackAnswerPort{}
	n := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: h.pending, Answer: answers,
		Clock: h.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	n.records["F-1"] = record
	t.Cleanup(func() { n.Stop(context.Background()) })
	return h, n, answers
}

func TestSlackPerFeatureResponderAnswerableWhileMutedOrOff(t *testing.T) {
	for _, tc := range []struct {
		name      string
		section   feature.SlackNotifications
		globalOff bool
	}{
		{name: "muted", section: feature.SlackNotifications{Mode: feature.SlackMuted}},
		{name: "needs input off", section: feature.SlackNotifications{NeedsInput: feature.SlackOff}},
		{name: "override on global off", section: feature.SlackNotifications{NeedsInput: feature.SlackOn}, globalOff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, n, answers := perFeatureResponderFixture(t, tc.section)
			if tc.globalOff {
				h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Categories.NeedsInput = false })
			}
			h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
				{TS: "100.000001"},
				{TS: "100.000002", ThreadTS: "100.000001", Text: "permission",
					Reactions: []testsupport.Reaction{{Name: "white_check_mark", Count: 1, Users: []string{"U-ANSWER"}}}},
			})
			n.responderTick()
			if got := answers.permissionSubmissions(); len(got) != 1 || got[0].Source.Kind != ports.AnswerSourceSlack {
				t.Fatalf("permission submissions = %#v; want one Slack answer", got)
			}
			waitFor(t, 2*time.Second, func() bool {
				return h.server.CallCount("chat.postMessage") == 2
			})
			if got := h.server.CallCount("chat.update"); got != 0 {
				t.Errorf("card edits = %d; want none", got)
			}
		})
	}
}

func TestSlackPerFeatureResponderRemovedDestinationHardStop(t *testing.T) {
	h, n, answers := perFeatureResponderFixture(t, feature.SlackNotifications{})
	n.responderTick()
	initial := h.server.CallCount("conversations.replies")
	f, err := h.store.Load("F-1")
	if err != nil {
		t.Fatal(err)
	}
	f.SlackNotifications = &feature.SlackNotifications{}
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Recipients = s.Recipients[:1] })
	if err := h.store.Save(f); err != nil {
		t.Fatal(err)
	}
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "permission",
			Reactions: []testsupport.Reaction{{Name: "white_check_mark", Count: 1, Users: []string{"U-ANSWER"}}}},
		{TS: "100.000003", ThreadTS: "100.000001", User: "U-ANSWER", Text: "allow"},
	})
	n.responderTick()
	if got := h.server.CallCount("conversations.replies"); got != initial+1 {
		t.Errorf("polls after removal = %d; want %d (remaining recipient only)", got, initial+1)
	}
	if got := answers.permissionSubmissions(); len(got) != 0 {
		t.Errorf("removed destination answers = %#v; want none", got)
	}
	if got := len(postsTo(h.server, "C-ENG")); got != 0 {
		t.Errorf("removed destination replies = %d; want none", got)
	}
	if got := h.server.CallCount("reactions.add"); got != 0 {
		t.Errorf("removed destination reactions = %d; want none", got)
	}
	if got := n.records["F-1"].Destinations["channel:C-ENG"].RootTS; got != "100.000001" {
		t.Errorf("retained root = %q; want original root", got)
	}
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Recipients = append(s.Recipients, testRecipients()[1])
	})
	n.responderTick()
	if got := answers.permissionSubmissions(); len(got) != 0 {
		t.Errorf("answers after re-add = %#v; want no retroactive answer", got)
	}
	if got := h.server.CallCount("chat.postMessage"); got != 0 {
		t.Errorf("posts after re-add = %d; want original card and thread reused", got)
	}
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "permission"},
		{TS: "100.000003", ThreadTS: "100.000001", User: "U-ANSWER", Text: "allow"},
		{TS: "100.000004", ThreadTS: "100.000001", User: "U-ANSWER", Text: "allow"},
	})
	n.responderTick()
	if got := answers.permissionSubmissions(); len(got) != 1 {
		t.Errorf("new reply answers = %#v; want one from resumed thread", got)
	}
}

func TestSlackPerFeatureResponderReconciliationWhileQuiet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section feature.SlackNotifications
	}{
		{"muted", feature.SlackNotifications{Mode: feature.SlackMuted}},
		{"needs input off", feature.SlackNotifications{NeedsInput: feature.SlackOff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, n, answers := perFeatureResponderFixture(t, tc.section)
			h.pending.set("F-1")
			n.responderTick()
			waitFor(t, 2*time.Second, func() bool {
				return h.server.CallCount("chat.postMessage") == 2
			})
			n.responderTick()
			for _, request := range h.server.Requests("chat.postMessage") {
				if got := fieldString(request, "text"); got != "#1 was resolved in Agentico." {
					t.Errorf("closure text = %q; want Agentico resolution", got)
				}
			}
			if got := h.server.CallCount("chat.postMessage"); got != 2 {
				t.Errorf("closure posts after second tick = %d; want exactly two", got)
			}
			if got := h.server.CallCount("chat.update"); got != 0 {
				t.Errorf("card edits = %d; want none", got)
			}
			if got := answers.permissionSubmissions(); len(got) != 0 {
				t.Errorf("permission submissions = %#v; want none", got)
			}
		})
	}
}

func TestSlackPerFeatureResponderNoPollWithoutEffectiveThread(t *testing.T) {
	h, n, _ := perFeatureResponderFixture(t, feature.SlackNotifications{})
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Recipients = nil })
	n.responderTick()
	n.responderTick()
	if got := h.server.CallCount("conversations.replies"); got != 0 {
		t.Errorf("polls without effective destination = %d; want none", got)
	}
	if got := len(h.server.AllRequests()); got != 0 {
		t.Errorf("Slack requests without effective destination = %d; want none", got)
	}
}

func TestSlackPerFeatureResponderQuestionAnswerWhileMuted(t *testing.T) {
	h, n, answers := perFeatureResponderFixture(t, feature.SlackNotifications{Mode: feature.SlackMuted})
	record := n.records["F-1"]
	record.Pending[0].Identity = "question:ask-1:0"
	record.Pending[0].Kind = string(ports.SlackPendingQuestion)
	record.Pending[0].RequestID = "ask-1"
	record.Pending[0].QuestionIndex = 0
	for key, destination := range record.Destinations {
		destination.PostingIndex[0].Identity = record.Pending[0].Identity
		record.Destinations[key] = destination
	}
	if err := persistFeatureRecord(h.stateDir, "F-1", record); err != nil {
		t.Fatal(err)
	}
	h.pending.set("F-1", ports.SlackPendingInput{
		FeatureID: "F-1", Kind: ports.SlackPendingQuestion, RequestID: "ask-1",
		QuestionIndex: 0, QuestionCount: 1,
		Options: []ports.SlackPendingInputOption{{Label: "Focused"}},
	})
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "question"},
		{TS: "100.000003", ThreadTS: "100.000001", User: "U-ANSWER", Text: "1"},
	})
	n.responderTick()
	answers.mu.Lock()
	got := append([]ports.SlackQuestionAnswer(nil), answers.questionAnswers...)
	answers.mu.Unlock()
	if len(got) != 1 || got[0].Source.Kind != ports.AnswerSourceSlack {
		t.Errorf("muted question answers = %#v; want one Slack answer", got)
	}
	waitFor(t, 2*time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 2 })
	if got := h.server.CallCount("chat.update"); got != 0 {
		t.Errorf("card edits = %d; want none", got)
	}
}

func TestSlackPerFeatureResponderConfirmationExcludesRemovedDestination(t *testing.T) {
	h, n, answers := perFeatureResponderFixture(t, feature.SlackNotifications{})
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Recipients = s.Recipients[:1] })
	h.server.SeedThread("D-U-ADA", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "permission",
			Reactions: []testsupport.Reaction{{Name: "white_check_mark", Count: 1, Users: []string{"U-ANSWER"}}}},
	})
	n.responderTick()
	if got := answers.permissionSubmissions(); len(got) != 1 {
		t.Fatalf("remaining destination submissions = %#v; want one", got)
	}
	waitFor(t, 2*time.Second, func() bool { return len(postsTo(h.server, "D-U-ADA")) == 1 })
	if got := len(postsTo(h.server, "C-ENG")); got != 0 {
		t.Errorf("removed destination confirmations = %d; want none", got)
	}
}
