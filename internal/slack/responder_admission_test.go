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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type gatedDeliveryClock struct {
	mu      sync.Mutex
	now     time.Time
	release chan struct{}
}

func newGatedDeliveryClock() *gatedDeliveryClock {
	return &gatedDeliveryClock{
		now:     time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
		release: make(chan struct{}),
	}
}

func (c *gatedDeliveryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *gatedDeliveryClock) Sleep(ctx context.Context, duration time.Duration) bool {
	select {
	case <-c.release:
		c.mu.Lock()
		c.now = c.now.Add(duration)
		c.mu.Unlock()
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *gatedDeliveryClock) open() {
	close(c.release)
}

func TestSlackResponderInFlightClaimPreventsFailedReplyResubmission(t *testing.T) {
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(
		t,
		[]testsupport.Message{{
			TS: "100.000003", ThreadTS: "100.000001", User: "U-ADA", Text: "allow",
		}},
		[]ports.SlackAnswerResult{{
			Outcome: ports.SlackAnswerFailed,
			Cause:   errors.New("temporary mutation failure"),
		}},
	)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		notifier.responderFeedbackMu.Lock()
		defer notifier.responderFeedbackMu.Unlock()
		return notifier.responderFeedback["C-ENG"] == 2
	})
	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions while feedback is in flight = %d; want 1", got)
	}
	if got := harness.server.CallCount("reactions.add"); got != 0 {
		t.Fatalf("reactions before delivery release = %d; want 0", got)
	}

	clock.open()
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("reactions.add") == 1 &&
			harness.server.CallCount("chat.postMessage") == 1
	})
	notifier.responderTick()
	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions after warning claim persisted = %d; want 1", got)
	}
}

func TestSlackResponderFeedbackAdmissionDefersAtPerDestinationCap(t *testing.T) {
	messages := make([]testsupport.Message, 0, responderFeedbackLimit/2+1)
	for index := 0; index < responderFeedbackLimit/2+1; index++ {
		messages = append(messages, testsupport.Message{
			TS:       fmt.Sprintf("100.%06d", index+3),
			ThreadTS: "100.000001",
			User:     fmt.Sprintf("U-%02d", index),
			Text:     "not an answer",
		})
	}
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(t, messages, nil)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 0 {
		t.Fatalf("permission submissions = %d; want no parsed answers", got)
	}
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != responderFeedbackLimit/2 {
		t.Fatalf("rejection events at cap = %d; want %d", got, responderFeedbackLimit/2)
	}
	notifier.responderFeedbackMu.Lock()
	outstanding := notifier.responderFeedback["C-ENG"]
	claims := len(notifier.responderClaims)
	notifier.responderFeedbackMu.Unlock()
	if outstanding != responderFeedbackLimit || claims != responderFeedbackLimit/2 {
		t.Fatalf(
			"feedback state = (%d outstanding, %d claims); want (%d, %d)",
			outstanding,
			claims,
			responderFeedbackLimit,
			responderFeedbackLimit/2,
		)
	}

	notifier.responderTick()
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != responderFeedbackLimit/2 {
		t.Fatalf("rejection events while capped = %d; want unchanged", got)
	}

	clock.open()
	waitFor(t, 2*time.Second, func() bool {
		return harness.server.CallCount("reactions.add") == responderFeedbackLimit/2 &&
			harness.server.CallCount("chat.postMessage") == responderFeedbackLimit/2
	})
	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return len(harness.observer.ofKind("slack.answer_rejected")) ==
			responderFeedbackLimit/2+1
	})
}

func TestSlackResponderParsedAnswerIsSubmittedAtFeedbackCap(t *testing.T) {
	messages := make([]testsupport.Message, 0, responderFeedbackLimit/2+1)
	for index := 0; index < responderFeedbackLimit/2; index++ {
		messages = append(messages, testsupport.Message{
			TS:       fmt.Sprintf("100.%06d", index+3),
			ThreadTS: "100.000001",
			User:     fmt.Sprintf("U-%02d", index),
			Text:     "not an answer",
		})
	}
	messages = append(messages, testsupport.Message{
		TS: "100.000020", ThreadTS: "100.000001", User: "U-ADA", Text: "allow",
	})
	_, notifier, answerPort := newPermissionResponderAdmissionFixture(
		t,
		messages,
		[]ports.SlackAnswerResult{{
			Outcome: ports.SlackAnswerFailed,
			Cause:   errors.New("temporary mutation failure"),
		}},
	)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions at feedback cap = %d; want 1", got)
	}
	notifier.responderFeedbackMu.Lock()
	outstanding := notifier.responderFeedback["C-ENG"]
	notifier.responderFeedbackMu.Unlock()
	if outstanding != responderFeedbackLimit+2 {
		t.Fatalf(
			"outstanding feedback after parsed failure = %d; want %d",
			outstanding,
			responderFeedbackLimit+2,
		)
	}
	notifier.responderTick()
	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions while failed feedback is in flight = %d; want 1", got)
	}
	clock.open()
}

func newPermissionResponderAdmissionFixture(
	t *testing.T,
	replies []testsupport.Message,
	results []ports.SlackAnswerResult,
) (*notifierHarness, *Notifier, *fakeSlackAnswerPort) {
	t.Helper()
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxb-admission",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	harness.seedFeature("feature-1", nil)
	harness.server.Script("users.info", testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id": "U-ADA", "profile": map[string]any{"display_name": "Ada"},
		},
	}})
	const (
		featureID      = "feature-1"
		destinationKey = "channel:C-ENG"
		rootTS         = "100.000001"
		messageTS      = "100.000002"
	)
	thread := []testsupport.Message{
		{TS: rootTS},
		{TS: messageTS, ThreadTS: rootTS, Text: "permission"},
	}
	thread = append(thread, replies...)
	harness.server.SeedThread("C-ENG", rootTS, thread)
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			destinationKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: rootTS,
				Ledger: []string{rootTS, messageTS},
				PostingIndex: []postingIndexEntry{{
					Identity: "permission:request-1", MessageTS: messageTS, Tag: "#1",
				}},
			},
		},
		Pending: []pendingInputRecord{{
			Identity: "permission:request-1", SourceFeatureID: featureID,
			Kind: string(ports.SlackPendingPermission), RequestID: "request-1", Tag: "#1",
			MessageTS: map[string]string{destinationKey: messageTS},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, featureID, record); err != nil {
		t.Fatal(err)
	}
	harness.pending.setFromRecord(featureID, record)
	answerPort := &fakeSlackAnswerPort{permissionResults: results}
	clock := newGatedDeliveryClock()
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: clock, ResponderClock: clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.records[featureID] = record
	worker := notifier.workerFor("C-ENG")
	worker.mu.Lock()
	worker.lastWrite = clock.Now()
	worker.mu.Unlock()
	return harness, notifier, answerPort
}
