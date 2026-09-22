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

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackResponderReviewReplyAccepted(t *testing.T) {
	harness, notifier, answerPort := newReviewResponderFixture(
		t,
		ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted},
		"approve",
	)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()

	submissions := answerPort.reviewSubmissions()
	if len(submissions) != 1 {
		t.Fatalf("review submissions = %#v; want one", submissions)
	}
	if submissions[0].SourceFeatureID != "feature-1" ||
		submissions[0].ReviewID != "review-1" ||
		submissions[0].SourceRevision != "revision-1" ||
		submissions[0].Source.Kind != ports.AnswerSourceSlack ||
		submissions[0].Source.Responder != "Ada" {
		t.Fatalf("review submission = %#v; want exact Slack review revision", submissions[0])
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("reactions.add") == 1 &&
			harness.server.CallCount("chat.postMessage") == 1
	})
	if got := fieldString(harness.server.Requests("reactions.add")[0], "name"); got != "white_check_mark" {
		t.Fatalf("reaction = %q; want white_check_mark", got)
	}
	if got := fieldString(harness.server.Requests("chat.postMessage")[0], "text"); got !=
		"#4 was approved by <@U-ADA> via Slack." {
		t.Fatalf("confirmation = %q; want attributed review approval", got)
	}
	events := harness.observer.ofKind("slack.answer_received")
	if len(events) != 1 ||
		events[0].Data["input_kind"] != string(ports.SlackPendingReview) ||
		events[0].Data["decision"] != "approve" {
		t.Fatalf("answer events = %#v; want accepted review approval", events)
	}
}

func TestSlackResponderReviewMovedGetsWarningAndPointerOnce(t *testing.T) {
	harness, notifier, answerPort := newReviewResponderFixture(
		t,
		ports.SlackAnswerResult{Outcome: ports.SlackAnswerRevisionMoved},
		"approve",
	)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()
	if submissions := answerPort.reviewSubmissions(); len(submissions) != 1 {
		t.Fatalf("review submissions = %#v; want one moved result", submissions)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) &&
		(harness.server.CallCount("reactions.add") != 1 ||
			harness.server.CallCount("chat.postMessage") != 1) {
		time.Sleep(time.Millisecond)
	}
	if reactions := harness.server.CallCount("reactions.add"); reactions != 1 {
		t.Fatalf(
			"reactions.add calls = %d, chat.postMessage calls = %d; want one each",
			reactions,
			harness.server.CallCount("chat.postMessage"),
		)
	}
	if got := fieldString(harness.server.Requests("reactions.add")[0], "name"); got != "warning" {
		t.Fatalf("reaction = %q; want warning", got)
	}
	if got := fieldString(harness.server.Requests("chat.postMessage")[0], "text"); got !=
		"#4 changed in Agentico. Approve the current review there." {
		t.Fatalf("pointer = %q; want moved-review guidance", got)
	}
	events := harness.observer.ofKind("slack.answer_rejected")
	if len(events) != 1 || events[0].Data["reason"] != "stale_revision" {
		t.Fatalf("rejection events = %#v; want stale_revision", events)
	}
	notifier.responderTick()
	if got := len(answerPort.reviewSubmissions()); got != 1 {
		t.Fatalf("review submissions after claimed reply = %d; want 1", got)
	}
}

func TestSlackResponderUnparseableReviewGetsQuestionAndHint(t *testing.T) {
	harness, notifier, answerPort := newReviewResponderFixture(
		t,
		ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted},
		"lgtm",
	)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("reactions.add") == 1 &&
			harness.server.CallCount("chat.postMessage") == 1
	})
	if got := fieldString(harness.server.Requests("reactions.add")[0], "name"); got != "question" {
		t.Fatalf("reaction = %q; want question", got)
	}
	if got := fieldString(harness.server.Requests("chat.postMessage")[0], "text"); got !=
		"#4 accepts ✅ or approve. Request changes in Agentico." {
		t.Fatalf("hint = %q; want review grammar", got)
	}
	if got := len(answerPort.reviewSubmissions()); got != 0 {
		t.Fatalf("review submissions = %d; want 0", got)
	}
	events := harness.observer.ofKind("slack.answer_rejected")
	if len(events) != 1 || events[0].Data["reason"] != "unparseable" {
		t.Fatalf("rejection events = %#v; want unparseable", events)
	}
}

func newReviewResponderFixture(
	t *testing.T,
	result ports.SlackAnswerResult,
	reply string,
) (*notifierHarness, *Notifier, *fakeSlackAnswerPort) {
	t.Helper()
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxb-review-responder",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	harness.seedFeature("feature-1", nil)
	harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id":      "U-ADA",
			"profile": map[string]any{"display_name": "Ada"},
		},
	}})
	const (
		destinationKey = "channel:C-ENG"
		rootTS         = "100.000001"
		messageTS      = "100.000002"
	)
	harness.server.SeedThread("C-ENG", rootTS, []testsupport.Message{
		{TS: rootTS},
		{TS: messageTS, ThreadTS: rootTS, Text: "review"},
		{TS: "100.000003", ThreadTS: rootTS, User: "U-ADA", Text: reply},
	})
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			destinationKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: rootTS,
				Ledger: []string{rootTS, messageTS},
				PostingIndex: []postingIndexEntry{{
					Identity: "review:review-1:revision-1", MessageTS: messageTS, Tag: "#4",
				}},
			},
		},
		Pending: []pendingInputRecord{{
			Identity: "review:review-1:revision-1", SourceFeatureID: "feature-1",
			Kind: string(ports.SlackPendingReview), ReviewID: "review-1",
			SourceRevision: "revision-1", Tag: "#4",
			MessageTS: map[string]string{destinationKey: messageTS},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	answerPort := &fakeSlackAnswerPort{reviewResults: []ports.SlackAnswerResult{result}}
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.records["feature-1"] = record
	return harness, notifier, answerPort
}
