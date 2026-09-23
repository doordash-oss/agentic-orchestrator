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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const (
	resolutionFeatureID   = "feature-resolution"
	resolutionUserKey     = "user:U-ADA"
	resolutionChannelKey  = "channel:C-ENG"
	resolutionUserChannel = "D-ADA"
	resolutionChannel     = "C-ENG"
	resolutionUserRoot    = "100.000001"
	resolutionChannelRoot = "200.000001"
)

func resolutionRecipients() []ports.SlackRecipient {
	return []ports.SlackRecipient{
		{
			TypedText: "@ada", Kind: ports.SlackRecipientUser,
			ID: "U-ADA", DisplayName: "Ada Lovelace",
		},
		{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	}
}

func resolutionPendingInput(index int) pendingInputRecord {
	return pendingInputRecord{
		Identity:        fmt.Sprintf("permission:request-%d", index),
		SourceFeatureID: resolutionFeatureID,
		Kind:            string(ports.SlackPendingPermission),
		RequestID:       fmt.Sprintf("request-%d", index),
		Tag:             fmt.Sprintf("#%d", index),
		MessageTS: map[string]string{
			resolutionUserKey:    fmt.Sprintf("100.%06d", index+1),
			resolutionChannelKey: fmt.Sprintf("200.%06d", index+1),
		},
	}
}

func resolutionRecord(inputs ...pendingInputRecord) *featureRecord {
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			resolutionUserKey: {
				Kind: "user", SlackID: "U-ADA", ChannelID: resolutionUserChannel,
				RootTS: resolutionUserRoot, Ledger: []string{resolutionUserRoot},
			},
			resolutionChannelKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: resolutionChannel,
				RootTS: resolutionChannelRoot, Ledger: []string{resolutionChannelRoot},
			},
		},
		Pending: append([]pendingInputRecord(nil), inputs...),
	}
	for _, input := range inputs {
		for key, messageTS := range input.MessageTS {
			destination := record.Destinations[key]
			destination.ledgerAppend(messageTS)
			destination.postingAppend(input.Identity, messageTS, input.Tag)
			record.Destinations[key] = destination
		}
	}
	return record
}

func newResolutionResponder(
	t *testing.T,
	record *featureRecord,
	answerPort *fakeSlackAnswerPort,
) (*notifierHarness, *Notifier) {
	t.Helper()
	harness := newNotifierHarness(t, defaultTestSettings("xoxb-resolution", resolutionRecipients()...))
	harness.seedFeature(resolutionFeatureID, nil)
	if err := persistFeatureRecord(harness.stateDir, resolutionFeatureID, record); err != nil {
		t.Fatal(err)
	}
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.records[resolutionFeatureID] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	return harness, notifier
}

func seedResolutionThread(
	harness *notifierHarness,
	channelID, rootTS, messageTS string,
	extras ...testsupport.Message,
) {
	harness.t.Helper()
	messages := []testsupport.Message{
		{TS: rootTS},
		{TS: messageTS, ThreadTS: rootTS, Text: "permission"},
	}
	messages = append(messages, extras...)
	harness.server.SeedThread(channelID, rootTS, messages)
}

func TestSlackResponderFirstValidReplyWinsAcrossDestinationTimestampOrder(t *testing.T) {
	testCases := []struct {
		name             string
		userReplyTS      string
		userReply        string
		userID           string
		channelReplyTS   string
		channelReply     string
		channelUserID    string
		wantDecision     ports.SlackPermissionDecision
		wantWinnerID     string
		wantWinnerName   string
		wantLoserChannel string
		wantLoserTS      string
	}{
		{
			name:        "channel allow is earlier",
			userReplyTS: "300.000020", userReply: "deny", userID: "U-LATE",
			channelReplyTS: "300.000010", channelReply: "allow", channelUserID: "U-EARLY",
			wantDecision: ports.SlackPermissionAllowOnce, wantWinnerID: "U-EARLY",
			wantWinnerName: "Early User", wantLoserChannel: resolutionUserChannel,
			wantLoserTS: "300.000020",
		},
		{
			name:        "direct message deny is earlier",
			userReplyTS: "300.000010", userReply: "deny", userID: "U-EARLY",
			channelReplyTS: "300.000020", channelReply: "allow", channelUserID: "U-LATE",
			wantDecision: ports.SlackPermissionDeny, wantWinnerID: "U-EARLY",
			wantWinnerName: "Early User", wantLoserChannel: resolutionChannel,
			wantLoserTS: "300.000020",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			input := resolutionPendingInput(1)
			record := resolutionRecord(input)
			answerPort := &fakeSlackAnswerPort{}
			harness, notifier := newResolutionResponder(t, record, answerPort)
			harness.pending.setFromRecord(resolutionFeatureID, record)
			harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
				"ok": true,
				"user": map[string]any{
					"id":      testCase.wantWinnerID,
					"profile": map[string]any{"display_name": testCase.wantWinnerName},
				},
			}})
			seedResolutionThread(
				harness, resolutionUserChannel, resolutionUserRoot, input.MessageTS[resolutionUserKey],
				testsupport.Message{
					TS: testCase.userReplyTS, ThreadTS: resolutionUserRoot,
					User: testCase.userID, Text: testCase.userReply,
				},
			)
			seedResolutionThread(
				harness, resolutionChannel, resolutionChannelRoot, input.MessageTS[resolutionChannelKey],
				testsupport.Message{
					TS: testCase.channelReplyTS, ThreadTS: resolutionChannelRoot,
					User: testCase.channelUserID, Text: testCase.channelReply,
				},
			)

			notifier.responderTick()

			submissions := answerPort.permissionSubmissions()
			if len(submissions) != 1 {
				t.Fatalf("permission submissions = %#v; want one winner", submissions)
			}
			if submissions[0].Decision != testCase.wantDecision ||
				submissions[0].Source.Responder != testCase.wantWinnerName {
				t.Fatalf(
					"permission submission = %#v; want %q by %q",
					submissions[0], testCase.wantDecision, testCase.wantWinnerName,
				)
			}
			waitFor(t, time.Second, func() bool {
				return harness.server.CallCount("chat.postMessage") == 3 &&
					harness.server.CallCount("reactions.add") == 2
			})
			var confirmations, lateAttempts int
			for _, request := range harness.server.Requests("chat.postMessage") {
				text := fieldString(request, "text")
				switch {
				case strings.Contains(text, "via Slack."):
					confirmations++
					if !strings.Contains(text, "<@"+testCase.wantWinnerID+">") {
						t.Errorf("confirmation = %q; want winner mention", text)
					}
				case strings.Contains(text, "already answered by"):
					lateAttempts++
					if fieldString(request, "channel") != testCase.wantLoserChannel {
						t.Errorf(
							"late-attempt channel = %q; want %q",
							fieldString(request, "channel"), testCase.wantLoserChannel,
						)
					}
					if !strings.Contains(text, "<@"+testCase.wantWinnerID+">") {
						t.Errorf("late-attempt line = %q; want winner mention", text)
					}
				}
			}
			if confirmations != 2 || lateAttempts != 1 {
				t.Fatalf(
					"confirmations=%d lateAttempts=%d; want 2 and 1",
					confirmations, lateAttempts,
				)
			}
			var loserWarned bool
			for _, request := range harness.server.Requests("reactions.add") {
				if fieldString(request, "timestamp") == testCase.wantLoserTS &&
					fieldString(request, "name") == "warning" {
					loserWarned = true
				}
			}
			if !loserWarned {
				t.Fatalf("reactions = %#v; want warning on losing reply", harness.server.Requests("reactions.add"))
			}
			if got := len(harness.observer.ofKind("slack.answer_received")); got != 1 {
				t.Fatalf("answer_received events = %d; want 1", got)
			}
			rejected := harness.observer.ofKind("slack.answer_rejected")
			if len(rejected) != 1 || rejected[0].Data["reason"] != "already_resolved" {
				t.Fatalf("answer_rejected events = %#v; want one already_resolved", rejected)
			}
		})
	}
}

func TestSlackRestartResponderSameThreadDifferentUserReplyAndReaction(t *testing.T) {
	first := resolutionPendingInput(1)
	second := resolutionPendingInput(2)
	record := resolutionRecord(first, second)
	answerPort := &fakeSlackAnswerPort{}
	harness, notifier := newResolutionResponder(t, record, answerPort)
	harness.pending.setFromRecord(resolutionFeatureID, record)
	harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id":      "U-REPLY",
			"profile": map[string]any{"display_name": "Reply User"},
		},
	}})
	seedResolutionThread(harness, resolutionUserChannel, resolutionUserRoot, first.MessageTS[resolutionUserKey],
		testsupport.Message{TS: second.MessageTS[resolutionUserKey], ThreadTS: resolutionUserRoot},
	)
	harness.server.SeedThread(resolutionChannel, resolutionChannelRoot, []testsupport.Message{
		{TS: resolutionChannelRoot},
		{
			TS: first.MessageTS[resolutionChannelKey], ThreadTS: resolutionChannelRoot,
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-REACT"},
			}},
		},
		{TS: "200.0000025", ThreadTS: resolutionChannelRoot, User: "U-REPLY", Text: "allow"},
		{TS: second.MessageTS[resolutionChannelKey], ThreadTS: resolutionChannelRoot},
	})

	notifier.responderTick()
	if got := answerPort.permissionSubmissions(); len(got) != 1 ||
		got[0].RequestID != first.RequestID ||
		got[0].Decision != ports.SlackPermissionAllowOnce {
		t.Fatalf("permission submissions = %#v; want reply to win once", got)
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == 3 &&
			harness.server.CallCount("reactions.add") == 1
	})
	var confirmations, losingReactions int
	for _, request := range harness.server.Requests("chat.postMessage") {
		text := fieldString(request, "text")
		switch {
		case strings.Contains(text, "via Slack."):
			confirmations++
		case strings.Contains(text, "already answered by"):
			losingReactions++
			if got := fieldString(request, "channel"); got != resolutionChannel {
				t.Errorf("losing-reaction channel = %q; want %q", got, resolutionChannel)
			}
			if !strings.Contains(text, "<@U-REPLY>") {
				t.Errorf("losing-reaction notification = %q; want winner mention", text)
			}
		}
	}
	if confirmations != 2 || losingReactions != 1 {
		t.Fatalf("confirmations=%d losingReactions=%d; want 2 and 1", confirmations, losingReactions)
	}
	if got := harness.observer.ofKind("slack.answer_received"); len(got) != 1 ||
		got[0].Data["medium"] != "reply" {
		t.Errorf("answer_received events = %#v; want one reply", got)
	}
	if got := harness.observer.ofKind("slack.answer_rejected"); len(got) != 1 ||
		got[0].Data["medium"] != "reaction" ||
		got[0].Data["reason"] != "already_resolved" {
		t.Errorf("answer_rejected events = %#v; want one losing reaction", got)
	}

	reloaded, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Pending) != 2 || reloaded.Pending[1].Identity != second.Identity {
		t.Fatalf("pending = %#v; want second item to keep polling active", reloaded.Pending)
	}
	if !reloaded.Pending[0].judgedReactionContains(
		resolutionChannelKey, first.MessageTS[resolutionChannelKey],
		"white_check_mark", "U-REACT",
	) {
		t.Fatalf("judged reactions = %#v; want losing action persisted", reloaded.Pending[0].JudgedReactions)
	}
	notifier.Stop(context.Background())
	next := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock, ResponderClock: newManualResponderClock(),
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	next.Start()
	t.Cleanup(func() { next.Stop(context.Background()) })
	beforePolls := harness.server.CallCount("conversations.replies")
	next.responderTick()
	next.responderTick()
	if got := harness.server.CallCount("conversations.replies"); got <= beforePolls {
		t.Fatalf("polls after reload = %d; want more than %d", got, beforePolls)
	}
	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Errorf("permission submissions after reload = %d; want 1", got)
	}
	if got := harness.server.CallCount("chat.postMessage"); got != 3 {
		t.Errorf("posts after reload = %d; want 3", got)
	}
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != 1 {
		t.Errorf("answer_rejected events after reload = %d; want 1", got)
	}
}

func TestSlackResponderNoLongerPendingClosesEveryDestinationImmediately(t *testing.T) {
	input := resolutionPendingInput(1)
	record := resolutionRecord(input)
	answerPort := &fakeSlackAnswerPort{
		permissionResults: []ports.SlackAnswerResult{{
			Outcome: ports.SlackAnswerNoLongerPending,
		}},
	}
	harness, notifier := newResolutionResponder(t, record, answerPort)
	harness.pending.setFromRecord(resolutionFeatureID, record)
	harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id":      "U-REPLIER",
			"profile": map[string]any{"display_name": "Reply User"},
		},
	}})
	seedResolutionThread(harness, resolutionUserChannel, resolutionUserRoot, input.MessageTS[resolutionUserKey])
	seedResolutionThread(
		harness, resolutionChannel, resolutionChannelRoot, input.MessageTS[resolutionChannelKey],
		testsupport.Message{
			TS: "300.000010", ThreadTS: resolutionChannelRoot,
			User: "U-REPLIER", Text: "allow",
		},
	)

	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions = %d; want 1", got)
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == 3 &&
			harness.server.CallCount("reactions.add") == 1
	})
	closures := map[string]int{}
	var lateAttempt int
	for _, request := range harness.server.Requests("chat.postMessage") {
		text := fieldString(request, "text")
		switch text {
		case "#1 was resolved in Agentico.":
			closures[fieldString(request, "channel")]++
		case "#1 was already answered by Agentico.":
			lateAttempt++
			if got := fieldString(request, "channel"); got != resolutionChannel {
				t.Errorf("late-attempt channel = %q; want %q", got, resolutionChannel)
			}
		default:
			t.Errorf("unexpected responder line %q", text)
		}
	}
	if closures[resolutionUserChannel] != 1 || closures[resolutionChannel] != 1 ||
		lateAttempt != 1 {
		t.Fatalf(
			"closures=%v lateAttempt=%d; want one closure per destination and one rejection",
			closures, lateAttempt,
		)
	}
	reaction := harness.server.Requests("reactions.add")[0]
	if fieldString(reaction, "timestamp") != "300.000010" ||
		fieldString(reaction, "name") != "warning" {
		t.Fatalf("reaction = %#v; want warning on submitted reply", reaction)
	}
	persisted, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 ||
		persisted.Pending[0].Resolution == nil ||
		persisted.Pending[0].Resolution.Kind != resolutionAgentico ||
		!persisted.Pending[0].Resolution.ClosureAcknowledged[resolutionChannelKey] ||
		!persisted.Pending[0].Resolution.ClosureAcknowledged[resolutionUserKey] {
		t.Fatalf("pending = %#v; want immediate durable Agentico resolution", persisted.Pending)
	}
	beforePosts := harness.server.CallCount("chat.postMessage")
	beforeReactions := harness.server.CallCount("reactions.add")
	notifier.responderTick()
	if got := harness.server.CallCount("chat.postMessage"); got != beforePosts {
		t.Fatalf("posts after resolved tick = %d; want %d", got, beforePosts)
	}
	if got := harness.server.CallCount("reactions.add"); got != beforeReactions {
		t.Fatalf("reactions after resolved tick = %d; want %d", got, beforeReactions)
	}
}

func TestSlackResponderInterruptionAndRewindCloseAllPendingItemsThenIdle(t *testing.T) {
	testCases := []struct {
		name          string
		event         ports.Event
		mutateFeature func(*feature.Feature)
		lifecycleText string
	}{
		{
			name: "interrupted",
			event: ports.Event{
				Type: ports.FeatureInterrupted, FeatureID: resolutionFeatureID,
				Phase: feature.PhaseImplement,
			},
			mutateFeature: func(value *feature.Feature) {
				value.Status = feature.StatusInterrupted
			},
			lifecycleText: "Interrupted.",
		},
		{
			name: "rewound",
			event: ports.Event{
				Type: ports.FeatureRewound, FeatureID: resolutionFeatureID,
				Phase: feature.PhasePlan,
			},
			lifecycleText: "Rewound to Plan",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			first := resolutionPendingInput(1)
			second := resolutionPendingInput(2)
			record := resolutionRecord(first, second)
			harness := newNotifierHarness(
				t,
				defaultTestSettings("xoxb-resolution", resolutionRecipients()...),
			)
			harness.seedFeature(resolutionFeatureID, testCase.mutateFeature)
			if err := persistFeatureRecord(harness.stateDir, resolutionFeatureID, record); err != nil {
				t.Fatal(err)
			}
			responderClock := newManualResponderClock()
			notifier := NewNotifier(NotifierOptions{
				Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
				Observer: harness.observer, Pending: harness.pending,
				Answer: &fakeSlackAnswerPort{}, Clock: harness.clock,
				ResponderClock: responderClock,
				NewClient: func(token string) (slackClient, error) {
					return NewClient(token, WithBaseURL(harness.server.URL()))
				},
			})
			notifier.Start()
			t.Cleanup(func() { notifier.Stop(context.Background()) })
			// This fixture exercises the lifecycle event in isolation, not
			// startup reconciliation over its preloaded record.
			notifier.startupDone.Store(true)
			close(notifier.startupReady)

			notifier.DomainEventTap(testCase.event)

			waitFor(t, time.Second, func() bool {
				if harness.server.CallCount("chat.postMessage") != 6 {
					return false
				}
				current, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
				if err != nil {
					return false
				}
				return len(current.Destinations[resolutionUserKey].Ledger) == 6 &&
					len(current.Destinations[resolutionChannelKey].Ledger) == 6
			})
			for _, channelID := range []string{resolutionUserChannel, resolutionChannel} {
				posts := postsTo(harness.server, channelID)
				if len(posts) != 3 {
					t.Fatalf("posts to %s = %#v; want lifecycle plus two closures", channelID, posts)
				}
				if !strings.Contains(fieldString(posts[0], "text"), testCase.lifecycleText) {
					t.Errorf(
						"first post to %s = %q; want lifecycle %q",
						channelID, fieldString(posts[0], "text"), testCase.lifecycleText,
					)
				}
				for index, want := range []string{
					"#1 is no longer pending.",
					"#2 is no longer pending.",
				} {
					if got := fieldString(posts[index+1], "text"); got != want {
						t.Errorf("closure %d to %s = %q; want %q", index, channelID, got, want)
					}
				}
			}
			persisted, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
			if err != nil {
				t.Fatal(err)
			}
			if len(persisted.Pending) != 0 || len(persisted.Resolved) != 0 {
				t.Fatalf(
					"pending=%#v resolved=%#v; want idle record pruned",
					persisted.Pending, persisted.Resolved,
				)
			}
			for key, destination := range persisted.Destinations {
				if len(destination.PostingIndex) != 0 {
					t.Errorf("posting index %s = %#v; want pruned", key, destination.PostingIndex)
				}
				if len(destination.Ledger) != 6 {
					t.Errorf("ledger %s = %#v; want roots, items, lifecycle, and closures", key, destination.Ledger)
				}
			}
			notifier.responderTick()
			if got := harness.server.CallCount("conversations.replies"); got != 0 {
				t.Fatalf("polls after %s = %d; want 0", testCase.name, got)
			}
		})
	}
}

func TestSlackResponderRetentionCapBoundsFullRecordsAndPollRange(t *testing.T) {
	inputs := make([]pendingInputRecord, 0, responderResolvedRetentionLimit+2)
	for index := 1; index <= responderResolvedRetentionLimit+2; index++ {
		inputs = append(inputs, resolutionPendingInput(index))
	}
	record := resolutionRecord(inputs...)
	answerPort := &fakeSlackAnswerPort{}
	harness, notifier := newResolutionResponder(t, record, answerPort)
	live := inputs[len(inputs)-1]
	harness.pending.set(resolutionFeatureID, ports.SlackPendingInput{
		FeatureID: resolutionFeatureID, Kind: ports.SlackPendingPermission,
		RequestID: live.RequestID,
	})
	userMessages := []testsupport.Message{{TS: resolutionUserRoot}}
	channelMessages := []testsupport.Message{{TS: resolutionChannelRoot}}
	for index, input := range inputs {
		userMessage := testsupport.Message{
			TS: input.MessageTS[resolutionUserKey], ThreadTS: resolutionUserRoot,
			Text: input.Tag,
		}
		channelMessage := testsupport.Message{
			TS: input.MessageTS[resolutionChannelKey], ThreadTS: resolutionChannelRoot,
			Text: input.Tag,
		}
		if index == 0 {
			channelMessage.Reactions = []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-LATE"},
			}}
		}
		userMessages = append(userMessages, userMessage)
		channelMessages = append(channelMessages, channelMessage)
	}
	harness.server.SeedThread(resolutionUserChannel, resolutionUserRoot, userMessages)
	harness.server.SeedThread(resolutionChannel, resolutionChannelRoot, channelMessages)

	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 0 {
		t.Fatalf("permission submissions = %d; want no mutation from reduced reaction", got)
	}
	persisted, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 || persisted.Pending[0].Identity != live.Identity {
		t.Fatalf("pending = %#v; want only newest live item", persisted.Pending)
	}
	if len(persisted.Resolved) != len(inputs)-1 {
		t.Fatalf(
			"resolved count = %d; want %d closures retained until acknowledged",
			len(persisted.Resolved), len(inputs)-1,
		)
	}
	if got, want := persisted.Resolved[0].Identity, inputs[0].Identity; got != want {
		t.Fatalf("oldest retained identity = %q; want %q", got, want)
	}
	if got, want := persisted.Resolved[len(persisted.Resolved)-1].Identity,
		inputs[len(inputs)-2].Identity; got != want {
		t.Fatalf("newest retained identity = %q; want %q", got, want)
	}
	for key, destination := range persisted.Destinations {
		if len(destination.PostingIndex) != len(inputs) {
			t.Errorf(
				"posting index %s count = %d; want %d",
				key, len(destination.PostingIndex), len(inputs),
			)
		}
		if destination.PostingIndex[0].Resolution == nil ||
			destination.PostingIndex[0].Resolution.Kind != resolutionAgentico {
			t.Errorf(
				"reduced posting %s = %#v; want Agentico resolution",
				key, destination.PostingIndex[0],
			)
		}
	}
	polls := harness.server.Requests("conversations.replies")
	if len(polls) != 2 {
		t.Fatalf("polls = %#v; want one per destination", polls)
	}
	for _, poll := range polls {
		channelID := fieldString(poll, "channel")
		wantOldest := inputs[0].MessageTS[resolutionChannelKey]
		if channelID == resolutionUserChannel {
			wantOldest = inputs[0].MessageTS[resolutionUserKey]
		}
		if got := fieldString(poll, "oldest"); got != wantOldest {
			t.Errorf("oldest for %s = %q; want %q", channelID, got, wantOldest)
		}
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") ==
			2*(len(inputs)-1)
	})
	waitFor(t, time.Second, func() bool {
		updated, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
		return err == nil && len(updated.Resolved) == responderResolvedRetentionLimit
	})
	for _, request := range harness.server.Requests("chat.postMessage") {
		if strings.Contains(fieldString(request, "text"), "already answered") {
			t.Errorf("reduced reaction produced feedback: %q", fieldString(request, "text"))
		}
	}
}

func TestSlackResponderRetentionCapKeepsReducedReplyTargeting(t *testing.T) {
	inputs := make([]pendingInputRecord, 0, responderResolvedRetentionLimit+2)
	for index := 1; index <= responderResolvedRetentionLimit+2; index++ {
		inputs = append(inputs, resolutionPendingInput(index))
	}
	record := resolutionRecord(inputs...)
	harness, notifier := newResolutionResponder(t, record, &fakeSlackAnswerPort{})
	live := inputs[len(inputs)-1]
	harness.pending.set(resolutionFeatureID, ports.SlackPendingInput{
		FeatureID: resolutionFeatureID, Kind: ports.SlackPendingPermission,
		RequestID: live.RequestID,
	})
	userMessages := []testsupport.Message{{TS: resolutionUserRoot}}
	channelMessages := []testsupport.Message{{TS: resolutionChannelRoot}}
	for _, input := range inputs {
		userMessages = append(userMessages, testsupport.Message{
			TS: input.MessageTS[resolutionUserKey], ThreadTS: resolutionUserRoot,
		})
		channelMessages = append(channelMessages, testsupport.Message{
			TS: input.MessageTS[resolutionChannelKey], ThreadTS: resolutionChannelRoot,
		})
	}
	harness.server.SeedThread(resolutionUserChannel, resolutionUserRoot, userMessages)
	harness.server.SeedThread(resolutionChannel, resolutionChannelRoot, channelMessages)
	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") ==
			2*(len(inputs)-1)
	})
	persisted, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	reduced := persisted.Destinations[resolutionChannelKey].PostingIndex[0]
	if reduced.Resolution == nil {
		t.Fatal("reduced posting resolution = nil; want historical resolver")
	}
	target, found := newestPostingBefore(
		persisted.Destinations[resolutionChannelKey].PostingIndex,
		"200.0000025",
	)
	if !found || target.Identity != reduced.Identity {
		t.Fatalf(
			"newestPostingBefore(reduced reply) = (%#v, %t); want %q",
			target, found, reduced.Identity,
		)
	}
	client, err := NewClient(
		"xoxb-resolution",
		WithBaseURL(harness.server.URL()),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate := responderReplyCandidate{
		Thread: responderThread{
			featureID: resolutionFeatureID, destinationKey: resolutionChannelKey,
			destinationOrder: 1, channelID: resolutionChannel, rootTS: resolutionChannelRoot,
		},
		Message: Message{
			TS: "200.0000025", ThreadTS: resolutionChannelRoot,
			User: "U-LATE", Text: "allow",
		},
		Target:      target,
		TargetFound: true,
	}
	// Exercise historical targeting as with a legacy record that predates the
	// reply watermark; normal polling skips already-seen earlier replies.
	notifier.recordMu.Lock()
	destination := notifier.records[resolutionFeatureID].Destinations[resolutionChannelKey]
	destination.LastSeenReplyTS = ""
	notifier.records[resolutionFeatureID].Destinations[resolutionChannelKey] = destination
	notifier.recordMu.Unlock()

	waitFor(t, time.Second, func() bool {
		notifier.processResponderReply(client, "xoxb-resolution", candidate)
		return len(harness.observer.ofKind("slack.answer_rejected")) == 1
	})
	rejected := harness.observer.ofKind("slack.answer_rejected")
	if len(rejected) != 1 || rejected[0].Data["reason"] != "already_resolved" {
		t.Fatalf(
			"answer_rejected events = %#v; want reduced reply rejected as already_resolved",
			rejected,
		)
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") ==
			2*(len(inputs)-1)+1 &&
			harness.server.CallCount("reactions.add") == 1
	})
	lastPost := harness.server.Requests("chat.postMessage")
	if got := fieldString(lastPost[len(lastPost)-1], "text"); got !=
		"#1 was already answered by Agentico." {
		t.Fatalf("reduced reply feedback = %q; want historical Agentico answer", got)
	}
}

func TestSlackResponderIdlePrunesTwentyResolvedItemsAndPostingIndex(t *testing.T) {
	inputs := make([]pendingInputRecord, 0, 21)
	for index := 1; index <= 21; index++ {
		inputs = append(inputs, resolutionPendingInput(index))
	}
	record := resolutionRecord(inputs...)
	record.Pending = append([]pendingInputRecord(nil), inputs[20])
	record.Resolved = append([]pendingInputRecord(nil), inputs[:20]...)
	for index := range record.Resolved {
		record.Resolved[index].Resolution = &postingResolution{
			Kind: resolutionAgentico, ResolvedAt: time.Date(
				2026, 9, 22, 12, 0, index, 0, time.UTC,
			), ClosureAcknowledged: map[string]bool{
				resolutionChannelKey: true, resolutionUserKey: true,
			},
		}
	}
	for key, destination := range record.Destinations {
		for index := 0; index < 20; index++ {
			resolution := *record.Resolved[index].Resolution
			destination.PostingIndex[index].Resolution = &resolution
		}
		record.Destinations[key] = destination
	}
	harness, notifier := newResolutionResponder(t, record, &fakeSlackAnswerPort{})
	harness.pending.set(resolutionFeatureID)
	seedResolutionThread(
		harness, resolutionUserChannel, resolutionUserRoot,
		record.Pending[0].MessageTS[resolutionUserKey],
	)
	seedResolutionThread(
		harness, resolutionChannel, resolutionChannelRoot,
		record.Pending[0].MessageTS[resolutionChannelKey],
	)

	notifier.responderTick()

	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == 2
	})
	waitFor(t, time.Second, func() bool {
		current, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
		return err == nil && len(current.Resolved) == 0
	})
	persisted, err := loadFeatureRecord(harness.stateDir, resolutionFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 0 || len(persisted.Resolved) != 0 {
		t.Fatalf(
			"pending=%#v resolved=%#v; want idle input state pruned after both acknowledgements",
			persisted.Pending, persisted.Resolved,
		)
	}
	for key, destination := range persisted.Destinations {
		if len(destination.PostingIndex) != 0 {
			t.Errorf("posting index %s = %#v; want pruned", key, destination.PostingIndex)
		}
		if len(destination.SubmittedReplies) != 0 {
			t.Errorf(
				"submitted replies %s = %#v; want pruned",
				key,
				destination.SubmittedReplies,
			)
		}
	}
	if got := harness.server.CallCount("conversations.replies"); got != 0 {
		t.Fatalf("polls after idle pruning = %d; want 0", got)
	}
}
