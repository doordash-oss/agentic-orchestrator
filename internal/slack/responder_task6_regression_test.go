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
	task6FeatureID   = "feature-task-6"
	task6UserKey     = "user:U-ADA"
	task6ChannelKey  = "channel:C-ENG"
	task6UserChannel = "D-ADA"
	task6Channel     = "C-ENG"
	task6UserRoot    = "100.000001"
	task6ChannelRoot = "200.000001"
)

func task6Recipients() []ports.SlackRecipient {
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

func task6PendingInput(index int) pendingInputRecord {
	return pendingInputRecord{
		Identity:        fmt.Sprintf("permission:request-%d", index),
		SourceFeatureID: task6FeatureID,
		Kind:            string(ports.SlackPendingPermission),
		RequestID:       fmt.Sprintf("request-%d", index),
		Tag:             fmt.Sprintf("#%d", index),
		MessageTS: map[string]string{
			task6UserKey:    fmt.Sprintf("100.%06d", index+1),
			task6ChannelKey: fmt.Sprintf("200.%06d", index+1),
		},
	}
}

func task6Record(inputs ...pendingInputRecord) *featureRecord {
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			task6UserKey: {
				Kind: "user", SlackID: "U-ADA", ChannelID: task6UserChannel,
				RootTS: task6UserRoot, Ledger: []string{task6UserRoot},
			},
			task6ChannelKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: task6Channel,
				RootTS: task6ChannelRoot, Ledger: []string{task6ChannelRoot},
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

func newTask6Responder(
	t *testing.T,
	record *featureRecord,
	answerPort *fakeSlackAnswerPort,
) (*notifierHarness, *Notifier) {
	t.Helper()
	harness := newNotifierHarness(t, defaultTestSettings("xoxb-task-6", task6Recipients()...))
	harness.seedFeature(task6FeatureID, nil)
	if err := persistFeatureRecord(harness.stateDir, task6FeatureID, record); err != nil {
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
	notifier.records[task6FeatureID] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	return harness, notifier
}

func seedTask6Thread(
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
			wantWinnerName: "Early User", wantLoserChannel: task6UserChannel,
			wantLoserTS: "300.000020",
		},
		{
			name:        "direct message deny is earlier",
			userReplyTS: "300.000010", userReply: "deny", userID: "U-EARLY",
			channelReplyTS: "300.000020", channelReply: "allow", channelUserID: "U-LATE",
			wantDecision: ports.SlackPermissionDeny, wantWinnerID: "U-EARLY",
			wantWinnerName: "Early User", wantLoserChannel: task6Channel,
			wantLoserTS: "300.000020",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			input := task6PendingInput(1)
			record := task6Record(input)
			answerPort := &fakeSlackAnswerPort{}
			harness, notifier := newTask6Responder(t, record, answerPort)
			harness.pending.setFromRecord(task6FeatureID, record)
			harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
				"ok": true,
				"user": map[string]any{
					"id":      testCase.wantWinnerID,
					"profile": map[string]any{"display_name": testCase.wantWinnerName},
				},
			}})
			seedTask6Thread(
				harness, task6UserChannel, task6UserRoot, input.MessageTS[task6UserKey],
				testsupport.Message{
					TS: testCase.userReplyTS, ThreadTS: task6UserRoot,
					User: testCase.userID, Text: testCase.userReply,
				},
			)
			seedTask6Thread(
				harness, task6Channel, task6ChannelRoot, input.MessageTS[task6ChannelKey],
				testsupport.Message{
					TS: testCase.channelReplyTS, ThreadTS: task6ChannelRoot,
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

func TestSlackResponderNoLongerPendingClosesEveryDestinationImmediately(t *testing.T) {
	input := task6PendingInput(1)
	record := task6Record(input)
	answerPort := &fakeSlackAnswerPort{
		permissionResults: []ports.SlackAnswerResult{{
			Outcome: ports.SlackAnswerNoLongerPending,
		}},
	}
	harness, notifier := newTask6Responder(t, record, answerPort)
	harness.pending.setFromRecord(task6FeatureID, record)
	harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id":      "U-REPLIER",
			"profile": map[string]any{"display_name": "Reply User"},
		},
	}})
	seedTask6Thread(harness, task6UserChannel, task6UserRoot, input.MessageTS[task6UserKey])
	seedTask6Thread(
		harness, task6Channel, task6ChannelRoot, input.MessageTS[task6ChannelKey],
		testsupport.Message{
			TS: "300.000010", ThreadTS: task6ChannelRoot,
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
			if got := fieldString(request, "channel"); got != task6Channel {
				t.Errorf("late-attempt channel = %q; want %q", got, task6Channel)
			}
		default:
			t.Errorf("unexpected responder line %q", text)
		}
	}
	if closures[task6UserChannel] != 1 || closures[task6Channel] != 1 ||
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
	persisted, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 ||
		persisted.Pending[0].Resolution == nil ||
		persisted.Pending[0].Resolution.Kind != resolutionAgentico ||
		!persisted.Pending[0].Resolution.ClosureSent {
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
				Type: ports.FeatureInterrupted, FeatureID: task6FeatureID,
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
				Type: ports.FeatureRewound, FeatureID: task6FeatureID,
				Phase: feature.PhasePlan,
			},
			lifecycleText: "Rewound to Plan",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			first := task6PendingInput(1)
			second := task6PendingInput(2)
			record := task6Record(first, second)
			harness := newNotifierHarness(
				t,
				defaultTestSettings("xoxb-task-6", task6Recipients()...),
			)
			harness.seedFeature(task6FeatureID, testCase.mutateFeature)
			if err := persistFeatureRecord(harness.stateDir, task6FeatureID, record); err != nil {
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

			notifier.DomainEventTap(testCase.event)

			waitFor(t, time.Second, func() bool {
				if harness.server.CallCount("chat.postMessage") != 6 {
					return false
				}
				current, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
				if err != nil {
					return false
				}
				return len(current.Destinations[task6UserKey].Ledger) == 6 &&
					len(current.Destinations[task6ChannelKey].Ledger) == 6
			})
			for _, channelID := range []string{task6UserChannel, task6Channel} {
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
			persisted, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
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
		inputs = append(inputs, task6PendingInput(index))
	}
	record := task6Record(inputs...)
	answerPort := &fakeSlackAnswerPort{}
	harness, notifier := newTask6Responder(t, record, answerPort)
	live := inputs[len(inputs)-1]
	harness.pending.set(task6FeatureID, ports.SlackPendingInput{
		FeatureID: task6FeatureID, Kind: ports.SlackPendingPermission,
		RequestID: live.RequestID,
	})
	userMessages := []testsupport.Message{{TS: task6UserRoot}}
	channelMessages := []testsupport.Message{{TS: task6ChannelRoot}}
	for index, input := range inputs {
		userMessage := testsupport.Message{
			TS: input.MessageTS[task6UserKey], ThreadTS: task6UserRoot,
			Text: input.Tag,
		}
		channelMessage := testsupport.Message{
			TS: input.MessageTS[task6ChannelKey], ThreadTS: task6ChannelRoot,
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
	harness.server.SeedThread(task6UserChannel, task6UserRoot, userMessages)
	harness.server.SeedThread(task6Channel, task6ChannelRoot, channelMessages)

	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 0 {
		t.Fatalf("permission submissions = %d; want no mutation from reduced reaction", got)
	}
	persisted, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 1 || persisted.Pending[0].Identity != live.Identity {
		t.Fatalf("pending = %#v; want only newest live item", persisted.Pending)
	}
	if len(persisted.Resolved) != responderResolvedRetentionLimit {
		t.Fatalf(
			"resolved count = %d; want retention cap %d",
			len(persisted.Resolved), responderResolvedRetentionLimit,
		)
	}
	if got, want := persisted.Resolved[0].Identity, inputs[1].Identity; got != want {
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
		wantOldest := inputs[1].MessageTS[task6ChannelKey]
		if channelID == task6UserChannel {
			wantOldest = inputs[1].MessageTS[task6UserKey]
		}
		if got := fieldString(poll, "oldest"); got != wantOldest {
			t.Errorf("oldest for %s = %q; want %q", channelID, got, wantOldest)
		}
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") ==
			2*(len(inputs)-1)
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
		inputs = append(inputs, task6PendingInput(index))
	}
	record := task6Record(inputs...)
	harness, notifier := newTask6Responder(t, record, &fakeSlackAnswerPort{})
	live := inputs[len(inputs)-1]
	harness.pending.set(task6FeatureID, ports.SlackPendingInput{
		FeatureID: task6FeatureID, Kind: ports.SlackPendingPermission,
		RequestID: live.RequestID,
	})
	userMessages := []testsupport.Message{{TS: task6UserRoot}}
	channelMessages := []testsupport.Message{{TS: task6ChannelRoot}}
	for _, input := range inputs {
		userMessages = append(userMessages, testsupport.Message{
			TS: input.MessageTS[task6UserKey], ThreadTS: task6UserRoot,
		})
		channelMessages = append(channelMessages, testsupport.Message{
			TS: input.MessageTS[task6ChannelKey], ThreadTS: task6ChannelRoot,
		})
	}
	harness.server.SeedThread(task6UserChannel, task6UserRoot, userMessages)
	harness.server.SeedThread(task6Channel, task6ChannelRoot, channelMessages)
	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") ==
			2*(len(inputs)-1)
	})
	persisted, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	reduced := persisted.Destinations[task6ChannelKey].PostingIndex[0]
	if reduced.Resolution == nil {
		t.Fatal("reduced posting resolution = nil; want historical resolver")
	}
	target, found := newestPostingBefore(
		persisted.Destinations[task6ChannelKey].PostingIndex,
		"200.0000025",
	)
	if !found || target.Identity != reduced.Identity {
		t.Fatalf(
			"newestPostingBefore(reduced reply) = (%#v, %t); want %q",
			target, found, reduced.Identity,
		)
	}
	client, err := NewClient(
		"xoxb-task-6",
		WithBaseURL(harness.server.URL()),
	)
	if err != nil {
		t.Fatal(err)
	}
	notifier.processResponderReply(client, "xoxb-task-6", responderReplyCandidate{
		Thread: responderThread{
			featureID: task6FeatureID, destinationKey: task6ChannelKey,
			destinationOrder: 1, channelID: task6Channel, rootTS: task6ChannelRoot,
		},
		Message: Message{
			TS: "200.0000025", ThreadTS: task6ChannelRoot,
			User: "U-LATE", Text: "allow",
		},
		Target:      target,
		TargetFound: true,
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
		inputs = append(inputs, task6PendingInput(index))
	}
	record := task6Record(inputs...)
	record.Pending = append([]pendingInputRecord(nil), inputs[20])
	record.Resolved = append([]pendingInputRecord(nil), inputs[:20]...)
	for index := range record.Resolved {
		record.Resolved[index].Resolution = &postingResolution{
			Kind: resolutionAgentico, ResolvedAt: time.Date(
				2026, 9, 22, 12, 0, index, 0, time.UTC,
			), ClosureSent: true,
		}
	}
	for key, destination := range record.Destinations {
		for index := 0; index < 20; index++ {
			resolution := *record.Resolved[index].Resolution
			destination.PostingIndex[index].Resolution = &resolution
		}
		record.Destinations[key] = destination
	}
	harness, notifier := newTask6Responder(t, record, &fakeSlackAnswerPort{})
	harness.pending.set(task6FeatureID)
	seedTask6Thread(
		harness, task6UserChannel, task6UserRoot,
		record.Pending[0].MessageTS[task6UserKey],
	)
	seedTask6Thread(
		harness, task6Channel, task6ChannelRoot,
		record.Pending[0].MessageTS[task6ChannelKey],
	)

	notifier.responderTick()

	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == 2
	})
	persisted, err := loadFeatureRecord(harness.stateDir, task6FeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Pending) != 0 || len(persisted.Resolved) != 0 {
		t.Fatalf(
			"pending=%#v resolved=%#v; want all idle input state pruned",
			persisted.Pending, persisted.Resolved,
		)
	}
	for key, destination := range persisted.Destinations {
		if len(destination.PostingIndex) != 0 {
			t.Errorf("posting index %s = %#v; want pruned", key, destination.PostingIndex)
		}
	}
	if got := harness.server.CallCount("conversations.replies"); got != 0 {
		t.Fatalf("polls after idle pruning = %d; want 0", got)
	}
}
