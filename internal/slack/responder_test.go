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
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type manualResponderClock struct {
	mu      sync.Mutex
	now     time.Time
	sleeps  chan time.Duration
	advance chan struct{}
}

func newManualResponderClock() *manualResponderClock {
	return &manualResponderClock{
		now:     time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		sleeps:  make(chan time.Duration, 1),
		advance: make(chan struct{}),
	}
}

func (c *manualResponderClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualResponderClock) Sleep(ctx context.Context, d time.Duration) bool {
	select {
	case c.sleeps <- d:
	case <-ctx.Done():
		return false
	}
	select {
	case <-c.advance:
		c.mu.Lock()
		c.now = c.now.Add(d)
		c.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *manualResponderClock) tick(t *testing.T) {
	t.Helper()
	select {
	case duration := <-c.sleeps:
		if duration != responderPollInterval {
			t.Fatalf("responder sleep = %s; want %s", duration, responderPollInterval)
		}
	case <-time.After(time.Second):
		t.Fatal("responder did not wait for its polling interval")
	}
	c.advance <- struct{}{}
}

type fakeSlackAnswerPort struct {
	mu sync.Mutex
}

func (*fakeSlackAnswerPort) AnswerSlackPermission(
	ports.SlackPermissionAnswer,
) ports.SlackAnswerResult {
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func (*fakeSlackAnswerPort) ApproveSlackReview(
	ports.SlackReviewApproval,
) ports.SlackAnswerResult {
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func TestSlackResponderReplyGrammar(t *testing.T) {
	permissionCases := []struct {
		reply    string
		decision ports.SlackPermissionDecision
		ok       bool
	}{
		{reply: "allow", decision: ports.SlackPermissionAllowOnce, ok: true},
		{reply: " APPROVE!!! ", decision: ports.SlackPermissionAllowOnce, ok: true},
		{reply: "Yes.", decision: ports.SlackPermissionAllowOnce, ok: true},
		{reply: "deny?", decision: ports.SlackPermissionDeny, ok: true},
		{reply: " NO ", decision: ports.SlackPermissionDeny, ok: true},
		{reply: "allow this", ok: false},
		{reply: "✅", ok: false},
		{reply: "#1 allow", ok: false},
	}
	for _, testCase := range permissionCases {
		t.Run("permission_"+testCase.reply, func(t *testing.T) {
			decision, ok := parsePermissionReply(testCase.reply)
			if decision != testCase.decision || ok != testCase.ok {
				t.Fatalf(
					"parsePermissionReply(%q) = (%q, %t); want (%q, %t)",
					testCase.reply, decision, ok, testCase.decision, testCase.ok,
				)
			}
		})
	}

	for _, testCase := range []struct {
		reply string
		ok    bool
	}{
		{reply: "approve", ok: true},
		{reply: " APPROVE. ", ok: true},
		{reply: "✅", ok: true},
		{reply: "yes", ok: false},
		{reply: "lgtm", ok: false},
		{reply: "#2 approve", ok: false},
	} {
		t.Run("review_"+testCase.reply, func(t *testing.T) {
			if ok := parseReviewReply(testCase.reply); ok != testCase.ok {
				t.Fatalf("parseReviewReply(%q) = %t; want %t", testCase.reply, ok, testCase.ok)
			}
		})
	}
}

func TestCompareSlackTimestamps(t *testing.T) {
	for _, testCase := range []struct {
		left, right string
		want        int
	}{
		{left: "9.900000", right: "10.000000", want: -1},
		{left: "10.010000", right: "10.001000", want: 1},
		{left: "010.1", right: "10.100000", want: 0},
		{left: "bad", right: "worse", want: -1},
	} {
		got := compareSlackTimestamps(testCase.left, testCase.right)
		if got < 0 {
			got = -1
		} else if got > 0 {
			got = 1
		}
		if got != testCase.want {
			t.Errorf(
				"compareSlackTimestamps(%q, %q) = %d; want %d",
				testCase.left, testCase.right, got, testCase.want,
			)
		}
	}
}

func TestSlackResponderPollsOnCadenceOnlyWhilePostedInputIsPending(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxb-responder",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	responderClock := newManualResponderClock()
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			"channel:C-ENG": {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG",
				RootTS: "100.000001",
			},
		},
		Pending: []pendingInputRecord{{
			Identity:  "permission:request-1",
			Kind:      string(ports.SlackPendingPermission),
			RequestID: "request-1",
			Tag:       "#1",
			MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	harness.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "pending"},
	})

	notifier := NewNotifier(NotifierOptions{
		Settings:       harness.settings,
		Store:          harness.store,
		StateDir:       harness.stateDir,
		Observer:       harness.observer,
		Pending:        harness.pending,
		Answer:         &fakeSlackAnswerPort{},
		Clock:          harness.clock,
		ResponderClock: responderClock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	if got := harness.server.CallCount("conversations.replies"); got != 0 {
		t.Fatalf("polls before first interval = %d; want 0", got)
	}
	responderClock.tick(t)
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("conversations.replies") == 1
	})
	request := harness.server.Requests("conversations.replies")[0]
	if oldest := fieldString(request, "oldest"); oldest != "100.000002" {
		t.Fatalf("oldest = %q; want pending message timestamp", oldest)
	}

	notifier.recordMu.Lock()
	notifier.records["feature-1"].Pending = nil
	notifier.recordMu.Unlock()
	responderClock.tick(t)
	time.Sleep(10 * time.Millisecond)
	if got := harness.server.CallCount("conversations.replies"); got != 1 {
		t.Fatalf("polls after feature became idle = %d; want 1", got)
	}
}

func TestSlackResponderResumesCursorAfterPerTickPageBudget(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxb-responder",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	responderClock := newManualResponderClock()
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			"channel:C-ENG": {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG",
				RootTS: "100.000001",
			},
		},
		Pending: []pendingInputRecord{{
			Identity:  "permission:request-1",
			Kind:      string(ports.SlackPendingPermission),
			RequestID: "request-1",
			Tag:       "#1",
			MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	messages := make([]testsupport.Message, 301)
	for i := range messages {
		messages[i] = testsupport.Message{
			TS:       fmt.Sprintf("100.%06d", i+2),
			ThreadTS: "100.000001",
			Text:     "message",
		}
	}
	harness.server.SeedThread("C-ENG", "100.000001", messages)

	notifier := NewNotifier(NotifierOptions{
		Settings:       harness.settings,
		Store:          harness.store,
		StateDir:       harness.stateDir,
		Observer:       harness.observer,
		Pending:        harness.pending,
		Answer:         &fakeSlackAnswerPort{},
		Clock:          harness.clock,
		ResponderClock: responderClock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	responderClock.tick(t)
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("conversations.replies") == responderPageBudget
	})
	firstTick := harness.server.Requests("conversations.replies")
	for index, want := range []string{"", "100", "200"} {
		if got := fieldString(firstTick[index], "cursor"); got != want {
			t.Fatalf("first tick cursor %d = %q; want %q", index, got, want)
		}
	}

	responderClock.tick(t)
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("conversations.replies") == responderPageBudget+1
	})
	if got := fieldString(
		harness.server.Requests("conversations.replies")[responderPageBudget],
		"cursor",
	); got != "300" {
		t.Fatalf("resumed cursor = %q; want 300", got)
	}
}

func TestSlackResponderExtractsCandidatesByLedgerProvenance(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxp-responder",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	const (
		featureID      = "feature-1"
		destinationKey = "channel:C-ENG"
		rootTS         = "100.000001"
	)
	harness.server.SeedThread("C-ENG", rootTS, []testsupport.Message{
		{TS: rootTS, User: "U-OWNER", Text: "root"},
		{
			TS: "100.000002", ThreadTS: rootTS, User: "U-OWNER", Text: "permission",
			Reactions: []testsupport.Reaction{
				{Name: "white_check_mark", Count: 2, Users: []string{"U-OWNER", "U-OTHER"}},
				{Name: "x", Count: 1, Users: []string{"U-DENY"}},
				{Name: "eyes", Count: 1, Users: []string{"U-OTHER"}},
			},
		},
		{TS: "100.000003", ThreadTS: rootTS, User: "U-OWNER", Text: "allow"},
		{TS: "100.000004", ThreadTS: rootTS, User: "U-OWNER", Text: "integration confirmation"},
		{TS: "100.000005", ThreadTS: rootTS, User: "U-OWNER", Text: "deny"},
		{TS: "100.000006", ThreadTS: rootTS, BotID: "B-AGENTICO", Text: "approve"},
		{TS: "100.000007", ThreadTS: rootTS, User: "U-OWNER", Text: "review"},
	})

	client, err := NewClient("xoxp-responder", WithBaseURL(harness.server.URL()))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ThreadReplies(
		context.Background(), "C-ENG", rootTS, "100.000002", 100, "",
	)
	if err != nil {
		t.Fatal(err)
	}

	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings,
		Store:    harness.store,
		StateDir: harness.stateDir,
		Observer: harness.observer,
		Pending:  harness.pending,
		Answer:   &fakeSlackAnswerPort{},
		Clock:    harness.clock,
	})
	notifier.records[featureID] = &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			destinationKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: rootTS,
				Ledger: []string{rootTS, "100.000002", "100.000004", "100.000007"},
				IntegrationReactions: []reactionLedgerEntry{
					{MessageTS: "100.000002", Name: "white_check_mark"},
					{MessageTS: "100.000005", Name: "white_check_mark"},
				},
				PostingIndex: []postingIndexEntry{
					{Identity: "permission:1", MessageTS: "100.000002", Tag: "#1"},
					{Identity: "review:2", MessageTS: "100.000007", Tag: "#2"},
				},
			},
		},
	}

	replies, reactions := notifier.extractResponderCandidates(responderThread{
		featureID: featureID, destinationKey: destinationKey,
		channelID: "C-ENG", rootTS: rootTS, oldest: "100.000002",
	}, page.Messages)

	if len(replies) != 2 {
		t.Fatalf("reply candidates = %#v; want owner and bot-authored unclaimed replies", replies)
	}
	for index, wantTS := range []string{"100.000003", "100.000006"} {
		if replies[index].Message.TS != wantTS ||
			!replies[index].TargetFound ||
			replies[index].Target.Identity != "permission:1" {
			t.Errorf("reply %d = %#v; want ts %s targeting permission:1", index, replies[index], wantTS)
		}
	}
	if len(reactions) != 1 ||
		reactions[0].Name != "x" ||
		reactions[0].UserID != "U-DENY" ||
		reactions[0].Target.Identity != "permission:1" {
		t.Fatalf("reaction candidates = %#v; want only unclaimed deny", reactions)
	}
}

func TestDestinationRecordPostingAndReactionLedgersAreIdempotent(t *testing.T) {
	var destination destinationRecord
	destination.postingAppend("permission:1", "100.000002", "#1")
	destination.postingAppend("permission:1", "100.000002", "#1")
	destination.reactionAppend("100.000003", "white_check_mark")
	destination.reactionAppend("100.000003", "white_check_mark")

	if len(destination.PostingIndex) != 1 ||
		destination.PostingIndex[0].Identity != "permission:1" {
		t.Fatalf("posting index = %#v; want one permission entry", destination.PostingIndex)
	}
	if len(destination.IntegrationReactions) != 1 ||
		!destination.reactionContains("100.000003", "white_check_mark") {
		t.Fatalf(
			"integration reactions = %#v; want one check-mark entry",
			destination.IntegrationReactions,
		)
	}
}
