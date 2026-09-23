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
	"net/http"
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

func TestSlackResponderFailedPermissionReplyStaysDeduplicatedAfterWarningExhaustion(t *testing.T) {
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(
		t,
		[]testsupport.Message{{
			TS: "100.000003", ThreadTS: "100.000001", User: "U-ADA", Text: "allow",
		}},
		[]ports.SlackAnswerResult{
			{
				Outcome: ports.SlackAnswerFailed,
				Cause:   errors.New("temporary mutation failure"),
			},
			{Outcome: ports.SlackAnswerAccepted},
		},
	)
	clock := notifier.clock.(*gatedDeliveryClock)
	clock.open()
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	for range retryLimit + 1 {
		harness.server.Script("reactions.add", testsupport.Response{
			Status: http.StatusInternalServerError,
			Body:   map[string]any{"ok": false},
		})
	}

	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("reactions.add") == retryLimit+1 &&
			harness.server.CallCount("chat.postMessage") == 1 &&
			notifier.responderFeedbackOutstanding("C-ENG") == 0
	})
	notifier.responderTick()

	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions after warning exhaustion = %d; want 1", got)
	}
	record, err := notifier.recordFor("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	destination := record.Destinations["channel:C-ENG"]
	if destination.hasReactionForMessage("100.000003") {
		t.Fatalf(
			"integration reactions = %#v; want no provenance for failed warning",
			destination.IntegrationReactions,
		)
	}
	if !destination.submittedReplyContains("100.000003") {
		t.Fatalf("submitted replies = %#v; want failed reply timestamp", destination.SubmittedReplies)
	}

	harness.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "permission"},
		{TS: "100.000003", ThreadTS: "100.000001", User: "U-ADA", Text: "allow"},
		{TS: "100.000004", ThreadTS: "100.000001", User: "U-ADA", Text: "deny"},
	})
	notifier.responderTick()

	submissions := answerPort.permissionSubmissions()
	if len(submissions) != 2 || submissions[1].Decision != ports.SlackPermissionDeny {
		t.Fatalf("permission submissions after fresh reply = %#v; want fresh deny", submissions)
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

func TestSlackResponderAcceptedAnswerProceedsAtFeedbackCap(t *testing.T) {
	messages := make([]testsupport.Message, 0, responderFeedbackLimit/2+3)
	for index := 0; index < responderFeedbackLimit/2; index++ {
		messages = append(messages, testsupport.Message{
			TS:       fmt.Sprintf("100.%06d", index+3),
			ThreadTS: "100.000001",
			User:     fmt.Sprintf("U-%02d", index),
			Text:     "not an answer",
		})
	}
	messages = append(messages,
		testsupport.Message{
			TS: "100.000020", ThreadTS: "100.000001",
			User: "U-FAILED", Text: "allow",
		},
		testsupport.Message{
			TS: "100.000021", ThreadTS: "100.000001",
			User: "U-INTEGRATION", Text: "permission two",
		},
		testsupport.Message{
			TS: "100.000022", ThreadTS: "100.000001",
			User: "U-ACCEPTED", Text: "deny",
		},
	)
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(
		t,
		messages,
		[]ports.SlackAnswerResult{
			{
				Outcome: ports.SlackAnswerFailed,
				Cause:   errors.New("temporary mutation failure"),
			},
			{Outcome: ports.SlackAnswerAccepted},
		},
	)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	addAdmissionPendingItem(
		t,
		harness,
		notifier,
		"permission:request-2",
		"request-2",
		"#2",
		"100.000021",
	)

	notifier.responderTick()

	submissions := answerPort.permissionSubmissions()
	if len(submissions) != 2 {
		t.Fatalf("permission submissions at feedback cap = %d; want 2", len(submissions))
	}
	if submissions[0].RequestID != "request-1" ||
		submissions[1].RequestID != "request-2" ||
		submissions[1].Decision != ports.SlackPermissionDeny {
		t.Fatalf("permission submissions = %#v; want failed #1 then accepted deny for #2", submissions)
	}
	notifier.responderFeedbackMu.Lock()
	outstanding := notifier.responderFeedback["C-ENG"]
	deferred := len(notifier.responderDeferred)
	notifier.responderFeedbackMu.Unlock()
	if outstanding != responderFeedbackLimit || deferred != 1 {
		t.Fatalf(
			"feedback after parsed failure = (%d outstanding, %d deferred); want (%d, 1)",
			outstanding,
			deferred,
			responderFeedbackLimit,
		)
	}
	record, err := notifier.recordFor("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if resolution := pendingResolution(record, "permission:request-2"); resolution == nil ||
		resolution.Kind != resolutionSlack {
		t.Fatalf("accepted answer resolution = %#v; want Slack resolution", resolution)
	}

	clock.open()
	waitFor(t, 2*time.Second, func() bool {
		return notifier.responderFeedbackOutstanding("C-ENG") == 0
	})
	waitFor(t, 2*time.Second, func() bool {
		notifier.responderTick()
		return hasAdmissionPost(
			harness.server.Requests("chat.postMessage"),
			"#2 was denied by <@U-ACCEPTED> via Slack.",
		)
	})
	if got := len(answerPort.permissionSubmissions()); got != 2 {
		t.Fatalf("permission submissions after feedback recovery = %d; want 2", got)
	}
}

func TestSlackResponderDeferredFailureDrainsAfterIdleBeforeNewAnswer(t *testing.T) {
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
		TS: "100.000020", ThreadTS: "100.000001",
		User: "U-FAILED", Text: "allow",
	})
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(
		t,
		messages,
		[]ports.SlackAnswerResult{
			{
				Outcome: ports.SlackAnswerFailed,
				Cause:   errors.New("temporary mutation failure"),
			},
			{Outcome: ports.SlackAnswerAccepted},
		},
	)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()
	if got := len(answerPort.permissionSubmissions()); got != 1 {
		t.Fatalf("initial permission submissions = %d; want 1", got)
	}

	harness.pending.set("feature-1")
	notifier.responderTick()
	record, err := notifier.recordFor("feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Pending) != 0 || len(record.Destinations["channel:C-ENG"].PostingIndex) != 0 {
		t.Fatalf(
			"idle record pending/index = (%d, %d); want both pruned",
			len(record.Pending),
			len(record.Destinations["channel:C-ENG"].PostingIndex),
		)
	}

	clock.open()
	waitFor(t, 2*time.Second, func() bool {
		return notifier.responderFeedbackOutstanding("C-ENG") == 0
	})
	notifier.responderTick()
	waitFor(t, 2*time.Second, func() bool {
		return hasAdmissionPost(
			harness.server.Requests("chat.postMessage"),
			"#1 could not be submitted. Answer again or in Agentico.",
		)
	})
	waitFor(t, 2*time.Second, func() bool {
		notifier.responderFeedbackMu.Lock()
		defer notifier.responderFeedbackMu.Unlock()
		return notifier.responderFeedback["C-ENG"] == 0 &&
			len(notifier.responderClaims) == 0
	})

	addAdmissionPendingItem(
		t,
		harness,
		notifier,
		"permission:request-2",
		"request-2",
		"#2",
		"100.000030",
	)
	harness.server.SeedThread("C-ENG", "100.000001", append(messages,
		testsupport.Message{
			TS: "100.000030", ThreadTS: "100.000001",
			User: "U-INTEGRATION", Text: "permission two",
		},
		testsupport.Message{
			TS: "100.000031", ThreadTS: "100.000001",
			User: "U-NEW", Text: "deny",
		},
	))
	notifier.responderTick()

	submissions := answerPort.permissionSubmissions()
	if len(submissions) != 2 || submissions[1].RequestID != "request-2" {
		t.Fatalf("permission submissions after idle recovery = %#v; want new request accepted", submissions)
	}
}

func TestSlackResponderDeferredFeedbackDrainsEligibleDestinationIndependently(t *testing.T) {
	harness, notifier, answerPort := newPermissionResponderAdmissionFixture(t, nil, nil)
	clock := notifier.clock.(*gatedDeliveryClock)
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{RequestID: "request-a"})
	answerPort.AnswerSlackPermission(ports.SlackPermissionAnswer{RequestID: "request-b"})
	harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Recipients = append(settings.Recipients, ports.SlackRecipient{
			TypedText: "#ops", Kind: ports.SlackRecipientChannel,
			ID: "C-OPS", DisplayName: "#ops",
		})
	})

	notifier.recordMu.Lock()
	record := notifier.records["feature-1"]
	record.Destinations["channel:C-OPS"] = destinationRecord{
		Kind: "channel", SlackID: "C-OPS", ChannelID: "C-OPS", RootTS: "200.000001",
		Ledger: []string{"200.000001"},
	}
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		notifier.recordMu.Unlock()
		t.Fatal(err)
	}
	notifier.recordMu.Unlock()

	threadA := responderThread{
		featureID: "feature-1", destinationKey: "channel:C-ENG",
		channelID: "C-ENG", rootTS: "100.000001",
	}
	threadB := responderThread{
		featureID: "feature-1", destinationKey: "channel:C-OPS",
		channelID: "C-OPS", rootTS: "200.000001",
	}
	pendingA := pendingInputRecord{
		Kind: string(ports.SlackPendingPermission), Tag: "#1",
	}
	pendingB := pendingInputRecord{
		Kind: string(ports.SlackPendingReview), Tag: "#2",
	}
	notifier.responderFeedbackMu.Lock()
	notifier.responderFeedback["C-ENG"] = responderFeedbackLimit
	notifier.responderFeedback["C-OPS"] = responderFeedbackLimit
	notifier.responderClaims["claim-a"] = struct{}{}
	notifier.responderClaims["claim-b"] = struct{}{}
	notifier.responderFeedbackMu.Unlock()
	notifier.deferResponderFeedback("claim-a", responderDeferredFeedback{
		thread: threadA, pending: pendingA, sourceFeatureID: "feature-1",
		messageTS: "100.000003", reaction: "warning",
		line:     "#1 could not be submitted. Answer again or in Agentico.",
		decision: string(ports.SlackPermissionAllowOnce),
		medium:   "reply", reason: "submit_failed",
	})
	notifier.deferResponderFeedback("claim-b", responderDeferredFeedback{
		thread: threadB, pending: pendingB, sourceFeatureID: "feature-1",
		line:     "#2 changed in Agentico. Approve the current review there.",
		decision: "approve", medium: "reaction", reason: "stale_revision",
	})

	notifier.responderFeedbackMu.Lock()
	delete(notifier.responderFeedback, "C-OPS")
	notifier.responderFeedbackMu.Unlock()
	worker := notifier.workerFor("C-OPS")
	worker.mu.Lock()
	worker.lastWrite = clock.Now()
	worker.mu.Unlock()

	notifier.drainDeferredResponderFeedback("xoxb-admission")

	notifier.responderFeedbackMu.Lock()
	_, deferredA := notifier.responderDeferred["claim-a"]
	_, deferredB := notifier.responderDeferred["claim-b"]
	notifier.responderFeedbackMu.Unlock()
	if !deferredA || deferredB {
		t.Fatalf(
			"deferred entries after releasing B = (A %t, B %t); want (true, false)",
			deferredA,
			deferredB,
		)
	}
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != 1 {
		t.Fatalf("rejection events after releasing B = %d; want 1", got)
	}
	if got := len(answerPort.permissionSubmissions()); got != 2 {
		t.Fatalf("permission submissions after deferred drain = %d; want 2", got)
	}

	clock.open()
	waitFor(t, 2*time.Second, func() bool {
		return hasAdmissionPost(
			harness.server.Requests("chat.postMessage"),
			"#2 changed in Agentico. Approve the current review there.",
		)
	})
	notifier.drainDeferredResponderFeedback("xoxb-admission")
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != 1 {
		t.Fatalf("rejection events after repeated drain = %d; want 1", got)
	}
	if got := len(answerPort.permissionSubmissions()); got != 2 {
		t.Fatalf("permission submissions after repeated drain = %d; want 2", got)
	}
}

func addAdmissionPendingItem(
	t *testing.T,
	harness *notifierHarness,
	notifier *Notifier,
	identity, requestID, tag, messageTS string,
) {
	t.Helper()
	notifier.recordMu.Lock()
	record := notifier.records["feature-1"]
	record.Pending = append(record.Pending, pendingInputRecord{
		Identity: identity, SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: requestID, Tag: tag,
		MessageTS: map[string]string{"channel:C-ENG": messageTS},
	})
	destination := record.Destinations["channel:C-ENG"]
	destination.Ledger = append(destination.Ledger, messageTS)
	destination.PostingIndex = append(destination.PostingIndex, postingIndexEntry{
		Identity: identity, MessageTS: messageTS, Tag: tag,
	})
	record.Destinations["channel:C-ENG"] = destination
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		notifier.recordMu.Unlock()
		t.Fatal(err)
	}
	notifier.recordMu.Unlock()
	harness.pending.setFromRecord("feature-1", record)
}

func pendingResolution(record *featureRecord, identity string) *postingResolution {
	for _, pending := range record.Pending {
		if pending.Identity == identity {
			return pending.Resolution
		}
	}
	return nil
}

func hasAdmissionPost(requests []testsupport.Request, text string) bool {
	for _, request := range requests {
		if request.Fields["text"] == text {
			return true
		}
	}
	return false
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
