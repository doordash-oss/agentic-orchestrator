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
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackResponderWithholdsReplyWhilePendingPostIsInFlight(t *testing.T) {
	for _, token := range []string{"xoxp-in-flight", "xoxb-in-flight"} {
		t.Run(token[:4], func(t *testing.T) {
			harness := newNotifierHarness(t, defaultTestSettings(
				token,
				ports.SlackRecipient{
					TypedText: "#eng", Kind: ports.SlackRecipientChannel,
					ID: "C-ENG", DisplayName: "#eng",
				},
			))
			harness.seedFeature("feature-1", nil)
			const (
				featureID      = "feature-1"
				destinationKey = "channel:C-ENG"
				rootTS         = "100.000001"
				firstTS        = "100.000002"
				secondTS       = "100.000003"
				replyTS        = "100.000004"
			)
			first := pendingInputRecord{
				Identity: "permission:request-1", SourceFeatureID: featureID,
				Kind: string(ports.SlackPendingPermission), RequestID: "request-1",
				Tag: "#1", MessageTS: map[string]string{destinationKey: firstTS},
			}
			second := pendingInputRecord{
				Identity: "permission:request-2", SourceFeatureID: featureID,
				Kind: string(ports.SlackPendingPermission), RequestID: "request-2", Tag: "#2",
			}
			record := &featureRecord{
				Version: recordVersion,
				Destinations: map[string]destinationRecord{
					destinationKey: {
						Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: rootTS,
						Ledger: []string{rootTS, firstTS},
						PostingIndex: []postingIndexEntry{{
							Identity: first.Identity, MessageTS: firstTS, Tag: first.Tag,
						}},
					},
				},
				Pending: []pendingInputRecord{first, second},
			}
			harness.pending.setFromRecord(featureID, record)
			harness.server.SeedThread("C-ENG", rootTS, []testsupport.Message{
				{TS: rootTS},
				{TS: firstTS, ThreadTS: rootTS, Text: "first permission"},
			})
			postStarted := make(chan struct{}, 1)
			postRelease := make(chan struct{})
			harness.server.Script("chat.postMessage", testsupport.Response{
				Body: map[string]any{
					"ok": true, "channel": "C-ENG", "ts": secondTS,
				},
				Started: postStarted,
				Release: postRelease,
			})
			answerPort := &fakeSlackAnswerPort{}
			notifier := NewNotifier(NotifierOptions{
				Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
				Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
				Clock: harness.clock, ResponderClock: harness.clock,
				NewClient: func(token string) (slackClient, error) {
					return NewClient(token, WithBaseURL(harness.server.URL()))
				},
			})
			notifier.records[featureID] = record
			t.Cleanup(func() { notifier.Stop(context.Background()) })

			item := queueItem{
				kind:  kindNeedsInput,
				event: ports.Event{Type: ports.SessionOutput, FeatureID: featureID},
			}
			if !notifier.reservePendingDelivery(featureID, second.Identity, destinationKey) {
				t.Fatal("reservePendingDelivery() = false")
			}
			item.reservation = notifier.queue.reserveProtected(item.event)
			notifier.dispatchDeliveryGroup(item, []workItem{{
				featureID: featureID, sourceFeatureID: featureID,
				destinationKey: destinationKey, kind: "channel", channelID: "C-ENG",
				reply: replyPayload{
					kind: kindNeedsInput, fallback: "second permission",
					identity: second.Identity, tag: second.Tag,
				},
			}})
			select {
			case <-postStarted:
			case <-time.After(time.Second):
				t.Fatal("pending item post did not reach held Slack response")
			}
			harness.server.SeedThread("C-ENG", rootTS, []testsupport.Message{
				{TS: rootTS},
				{TS: firstTS, ThreadTS: rootTS, Text: "first permission"},
				{TS: secondTS, ThreadTS: rootTS, Text: "second permission"},
				{TS: replyTS, ThreadTS: rootTS, User: "U-ADA", Text: "allow"},
			})

			notifier.responderTick()
			if got := len(answerPort.permissionSubmissions()); got != 0 {
				t.Fatalf("permission submissions while post response held = %d; want 0", got)
			}

			close(postRelease)
			waitFor(t, time.Second, func() bool {
				current, err := loadFeatureRecord(harness.stateDir, featureID)
				return err == nil &&
					len(current.Pending) == 2 &&
					current.Pending[1].MessageTS[destinationKey] == secondTS
			})
			notifier.responderTick()
			submissions := answerPort.permissionSubmissions()
			if len(submissions) != 1 || submissions[0].RequestID != "request-2" {
				t.Fatalf("permission submissions after post completed = %#v; want request-2", submissions)
			}
		})
	}
}

type blockingSlackAnswerPort struct {
	fakeSlackAnswerPort
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingSlackAnswerPort) AnswerSlackPermission(
	ports.SlackPermissionAnswer,
) ports.SlackAnswerResult {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
}

func (p *blockingSlackAnswerPort) ApproveSlackReview(
	ports.SlackReviewApproval,
) ports.SlackAnswerResult {
	return ports.SlackAnswerResult{Outcome: ports.SlackAnswerFailed}
}

func TestSlackResponderSubmissionPreventsConcurrentRetirement(t *testing.T) {
	harness, notifier, _ := newPermissionResponderAdmissionFixture(
		t,
		[]testsupport.Message{{
			TS: "100.000003", ThreadTS: "100.000001", User: "U-ADA", Text: "allow",
		}},
		nil,
	)
	blocking := &blockingSlackAnswerPort{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	notifier.answer = blocking
	t.Cleanup(func() {
		select {
		case <-blocking.release:
		default:
			close(blocking.release)
		}
		notifier.clock.(*gatedDeliveryClock).open()
		notifier.Stop(context.Background())
	})

	tickDone := make(chan struct{})
	go func() {
		notifier.responderTick()
		close(tickDone)
	}()
	select {
	case <-blocking.entered:
	case <-time.After(time.Second):
		t.Fatal("answer port was not reached")
	}

	harness.pending.set("feature-1")
	settings := harness.settings.SlackSettings()
	owner, err := harness.store.Load("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	record, err := notifier.recordFor("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if work := notifier.reconcilePending(
		settings,
		owner,
		owner,
		record,
		resolutionAgentico,
	); len(work) != 0 {
		t.Fatalf("reconciliation work during submission = %#v; want none", work)
	}
	if len(record.Pending) != 1 || record.Pending[0].Resolution != nil {
		t.Fatalf("pending during submission = %#v; want unresolved item retained", record.Pending)
	}

	close(blocking.release)
	select {
	case <-tickDone:
	case <-time.After(time.Second):
		t.Fatal("responder tick did not finish")
	}
	if len(record.Pending) != 1 ||
		record.Pending[0].Resolution == nil ||
		record.Pending[0].Resolution.Kind != resolutionSlack {
		t.Fatalf("pending after accepted submission = %#v; want Slack resolution", record.Pending)
	}
	if got := len(harness.observer.ofKind("slack.answer_received")); got != 1 {
		t.Fatalf("answer-received events = %d; want 1", got)
	}
}
