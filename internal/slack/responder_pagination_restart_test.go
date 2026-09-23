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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackRestartResponderWatermarkAcrossCursorTicks(t *testing.T) {
	for _, mode := range []string{"feedback_cap", "thread_lock"} {
		t.Run(mode, func(t *testing.T) {
			testSlackRestartResponderWatermarkAcrossCursorTicks(t, mode)
		})
	}
}

func testSlackRestartResponderWatermarkAcrossCursorTicks(t *testing.T, mode string) {
	t.Helper()
	const (
		featureID = "feature-1"
		key       = "channel:C-ENG"
		rootTS    = "100.000001"
	)
	messages := make([]testsupport.Message, 301)
	for index := range messages {
		messages[index] = testsupport.Message{
			TS: fmt.Sprintf("100.%06d", index+2), ThreadTS: rootTS,
			User: "U-ADA", Text: "integration output",
		}
	}
	for _, index := range []int{150, 300} {
		messages[index].Text = "not an answer"
	}
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(t, messages, nil)
	record := notifier.records[featureID]
	destination := record.Destinations[key]
	for index, message := range messages {
		if index != 150 && index != 300 {
			destination.Ledger = append(destination.Ledger, message.TS)
		}
	}
	record.Destinations[key] = destination
	if err := persistFeatureRecord(harness.stateDir, featureID, record); err != nil {
		t.Fatal(err)
	}
	if mode == "feedback_cap" {
		notifier.responderFeedbackMu.Lock()
		notifier.responderFeedback["C-ENG"] = responderFeedbackLimit
		notifier.responderFeedbackMu.Unlock()
	} else {
		notifier.responderThreadLock(featureID, key).Lock()
	}

	notifier.responderTick()
	if got := notifier.responderPolls[responderPollKey(featureID, key)].cursor; got != "300" {
		t.Fatalf("first tick cursor = %q; want 300", got)
	}
	seen, err := loadFeatureRecord(harness.stateDir, featureID)
	if err != nil {
		t.Fatal(err)
	}
	if got := seen.Destinations[key].LastSeenReplyTS; got != "100.000151" {
		t.Fatalf("first tick mark = %q; want last judged before deferred page-two reply", got)
	}
	notifier.responderTick()
	seen, err = loadFeatureRecord(harness.stateDir, featureID)
	if err != nil {
		t.Fatal(err)
	}
	if got := seen.Destinations[key].LastSeenReplyTS; got != "100.000151" {
		t.Fatalf("continuation mark = %q; want held before deferred page-two reply", got)
	}
	if mode == "thread_lock" {
		notifier.responderThreadLock(featureID, key).Unlock()
	}
	notifier.Stop(context.Background())

	reloaded := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	t.Cleanup(func() { reloaded.Stop(context.Background()) })
	if _, err := reloaded.recordFor(featureID); err != nil {
		t.Fatal(err)
	}
	reloaded.responderTick()
	waitFor(t, 2*time.Second, func() bool {
		return harness.server.CallCount("reactions.add") >= 1 &&
			harness.server.CallCount("chat.postMessage") >= 1 &&
			reloaded.responderFeedbackOutstanding("C-ENG") == 0
	})
	reloaded.responderTick()
	waitFor(t, 2*time.Second, func() bool {
		return harness.server.CallCount("reactions.add") >= 2 &&
			harness.server.CallCount("chat.postMessage") >= 2 &&
			reloaded.responderFeedbackOutstanding("C-ENG") == 0
	})
	reloaded.responderTick()
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != 2 {
		t.Fatalf("reloaded rejected replies = %d; want each deferred reply judged exactly once", got)
	}
	if got := len(answerPort.permissionSubmissions()); got != 0 {
		t.Fatalf("permission submissions = %d; want none for invalid replies", got)
	}
}

func TestSlackRestartResponderPollSuspensionResetsOnReload(t *testing.T) {
	harness, notifier, answerPort := newReviewResponderFixture(
		t, ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}, "",
	)
	failure := testsupport.Response{
		Body: map[string]any{"ok": false, "error": "channel_not_found"},
	}
	harness.server.Script("conversations.replies", failure)
	notifier.responderTick()
	notifier.responderTick()
	if got := harness.server.CallCount("conversations.replies"); got != 1 {
		t.Fatalf("polls while suspended = %d; want 1", got)
	}
	notifier.Stop(context.Background())

	reloaded := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	t.Cleanup(func() { reloaded.Stop(context.Background()) })
	if _, err := reloaded.recordFor("feature-1"); err != nil {
		t.Fatal(err)
	}
	harness.server.Script("conversations.replies", failure)
	reloaded.responderTick()
	reloaded.responderTick()
	if got := harness.server.CallCount("conversations.replies"); got != 2 {
		t.Fatalf("polls across reload = %d; want one attempt per process", got)
	}
	if got := len(harness.observer.ofKind("slack.delivery_failed")); got != 2 {
		t.Fatalf("poll failure events across reload = %d; want 2", got)
	}
}

func TestSlackRestartResponderOwnerReactionAfterReload(t *testing.T) {
	for _, token := range []string{"xoxp-owner", "xoxb-bot"} {
		t.Run(token, func(t *testing.T) {
			const rootTS = "100.000001"
			author := testsupport.Message{User: "U-ADA"}
			if token == "xoxb-bot" {
				author = testsupport.Message{BotID: "B-AGENTICO"}
			}
			thread := []testsupport.Message{
				{TS: rootTS, User: author.User, BotID: author.BotID},
				{
					TS: "100.000002", ThreadTS: rootTS, User: author.User,
					BotID: author.BotID, Text: "review",
					Reactions: []testsupport.Reaction{{
						Name: "x", Count: 1, Users: []string{"U-ADA"},
					}},
				},
				{
					TS: "100.000003", ThreadTS: rootTS, User: author.User,
					BotID: author.BotID, Text: "approve",
				},
			}
			harness, original, answerPort := newReviewResponderFixtureWithThread(
				t, ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted},
				thread, []string{rootTS, "100.000002", "100.000003"},
			)
			harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
				settings.Token = token
			})
			record := original.records["feature-1"]
			destination := record.Destinations["channel:C-ENG"]
			destination.IntegrationReactions = []reactionLedgerEntry{{
				MessageTS: "100.000002", Name: "x",
			}}
			record.Destinations["channel:C-ENG"] = destination
			if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
				t.Fatal(err)
			}
			original.Stop(context.Background())

			reloaded := NewNotifier(NotifierOptions{
				Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
				Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
				Clock: harness.clock,
				NewClient: func(token string) (slackClient, error) {
					return NewClient(token, WithBaseURL(harness.server.URL()))
				},
			})
			t.Cleanup(func() { reloaded.Stop(context.Background()) })
			if _, err := reloaded.recordFor("feature-1"); err != nil {
				t.Fatal(err)
			}
			reloaded.responderTick()
			if got := len(answerPort.reviewSubmissions()); got != 0 {
				t.Fatalf("integration output submissions after reload = %d; want 0", got)
			}
			if got := harness.server.CallCount("chat.postMessage"); got != 0 {
				t.Fatalf("feedback for integration output = %d; want 0", got)
			}
			thread[1].Reactions = append(thread[1].Reactions, testsupport.Reaction{
				Name: "white_check_mark", Count: 1, Users: []string{"U-ADA"},
			})
			harness.server.SeedThread("C-ENG", rootTS, thread)
			reloaded.responderTick()
			waitFor(t, 2*time.Second, func() bool {
				return harness.server.CallCount("chat.postMessage") == 1
			})
			reloaded.responderTick()
			if got := len(answerPort.reviewSubmissions()); got != 1 {
				t.Fatalf("owner reaction submissions after reload = %d; want 1", got)
			}
		})
	}
}
