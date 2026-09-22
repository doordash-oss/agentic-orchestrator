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
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const (
	pollFailureFeatureID   = "F-POLL-FAILURE"
	pollFailureBadChannel  = "C-POLL-BAD"
	pollFailureGoodChannel = "D-POLL-GOOD"
)

type pollFailureFixture struct {
	harness  *notifierHarness
	notifier *Notifier
	reporter *fakeDeliveryReporter
	answer   *fakeSlackAnswerPort

	mu           sync.Mutex
	badResponses []testsupport.Response
	pollCalls    map[string]int
	postCounter  int
}

type pollFailureClient struct {
	slackClient
	fixture *pollFailureFixture
}

func (c *pollFailureClient) ThreadReplies(
	_ context.Context,
	channelID, _, _ string,
	_ int,
	_ string,
) (RepliesPage, error) {
	c.fixture.mu.Lock()
	defer c.fixture.mu.Unlock()
	c.fixture.pollCalls[channelID]++
	if channelID != pollFailureBadChannel || len(c.fixture.badResponses) == 0 {
		return RepliesPage{}, nil
	}
	response := c.fixture.badResponses[0]
	c.fixture.badResponses = c.fixture.badResponses[1:]
	if response.Status != 0 && response.Status != http.StatusOK {
		retryAfter := time.Duration(0)
		if response.Headers.Get("Retry-After") == "25" {
			retryAfter = 25 * time.Second
		}
		return RepliesPage{}, &TransportError{
			StatusCode: response.Status,
			RetryAfter: retryAfter,
		}
	}
	body, _ := response.Body.(map[string]any)
	if ok, _ := body["ok"].(bool); !ok {
		return RepliesPage{}, &APIError{
			SlackError: fmt.Sprint(body["error"]),
			Needed:     fmt.Sprint(body["needed"]),
		}
	}
	return RepliesPage{}, nil
}

func newPollFailureFixture(t *testing.T) *pollFailureFixture {
	t.Helper()
	recipients := []ports.SlackRecipient{
		{
			TypedText:   "#poll-bad",
			Kind:        ports.SlackRecipientChannel,
			ID:          pollFailureBadChannel,
			DisplayName: "#poll-bad",
		},
		{
			TypedText:   "@poll-good",
			Kind:        ports.SlackRecipientUser,
			ID:          "U-POLL-GOOD",
			DisplayName: "Poll Good",
		},
	}
	settings := defaultTestSettings("xoxb-poll-failure", recipients...)
	settings.CredentialGeneration = 7
	harness := newNotifierHarness(t, settings)
	harness.seedFeature(pollFailureFeatureID, nil)

	badKey := destinationKey(string(ports.SlackRecipientChannel), pollFailureBadChannel)
	goodKey := destinationKey(string(ports.SlackRecipientUser), "U-POLL-GOOD")
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			badKey: {
				Kind:        string(ports.SlackRecipientChannel),
				SlackID:     pollFailureBadChannel,
				DisplayName: "#poll-bad",
				ChannelID:   pollFailureBadChannel,
				RootTS:      "100.000001",
				Ledger:      []string{"100.000001", "100.000002"},
				PostingIndex: []postingIndexEntry{{
					Identity:  "permission:request-1",
					MessageTS: "100.000002",
					Tag:       "#1",
				}},
			},
			goodKey: {
				Kind:        string(ports.SlackRecipientUser),
				SlackID:     "U-POLL-GOOD",
				DisplayName: "Poll Good",
				ChannelID:   pollFailureGoodChannel,
				RootTS:      "200.000001",
				Ledger:      []string{"200.000001", "200.000002"},
				PostingIndex: []postingIndexEntry{{
					Identity:  "permission:request-1",
					MessageTS: "200.000002",
					Tag:       "#1",
				}},
			},
		},
		Pending: []pendingInputRecord{{
			Identity:        "permission:request-1",
			SourceFeatureID: pollFailureFeatureID,
			Kind:            string(ports.SlackPendingPermission),
			RequestID:       "request-1",
			Tag:             "#1",
			MessageTS: map[string]string{
				badKey:  "100.000002",
				goodKey: "200.000002",
			},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, pollFailureFeatureID, record); err != nil {
		t.Fatal(err)
	}

	fixture := &pollFailureFixture{
		harness:   harness,
		reporter:  &fakeDeliveryReporter{},
		answer:    &fakeSlackAnswerPort{},
		pollCalls: map[string]int{},
	}
	harness.server.SetDefault(fixture.respond)
	fixture.notifier = NewNotifier(NotifierOptions{
		Settings: harness.settings,
		Store:    harness.store,
		StateDir: harness.stateDir,
		Observer: harness.observer,
		Reporter: fixture.reporter,
		Pending:  harness.pending,
		Answer:   fixture.answer,
		Clock:    harness.clock,
		Jitter:   func() float64 { return 0 },
		NewClient: func(token string) (slackClient, error) {
			client, err := NewClient(token, WithBaseURL(harness.server.URL()))
			if err != nil {
				return nil, err
			}
			return &pollFailureClient{slackClient: client, fixture: fixture}, nil
		},
	})
	fixture.notifier.records[pollFailureFeatureID] = record
	t.Cleanup(func() { fixture.notifier.Stop(context.Background()) })
	return fixture
}

func (f *pollFailureFixture) respond(
	method string,
	request testsupport.Request,
) testsupport.Response {
	switch method {
	case "chat.postMessage":
		f.mu.Lock()
		f.postCounter++
		ts := fmt.Sprintf("300.%06d", f.postCounter)
		f.mu.Unlock()
		return testsupport.Response{Body: map[string]any{
			"ok":      true,
			"ts":      ts,
			"channel": request.Fields["channel"],
		}}
	default:
		return testsupport.Response{
			Body: map[string]any{"ok": false, "error": "unexpected_" + method},
		}
	}
}

func (f *pollFailureFixture) scriptBad(responses ...testsupport.Response) {
	f.mu.Lock()
	f.badResponses = append(f.badResponses, responses...)
	f.mu.Unlock()
}

func (f *pollFailureFixture) tick() {
	f.notifier.responderTick()
}

func (f *pollFailureFixture) advance(d time.Duration) {
	f.harness.clock.Sleep(context.Background(), d)
}

func (f *pollFailureFixture) pollCount(channelID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pollCalls[channelID]
}

func (f *pollFailureFixture) destinationFailure(
	t *testing.T,
	channelID string,
) *destinationFailure {
	t.Helper()
	record, err := loadFeatureRecord(f.harness.stateDir, pollFailureFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range record.Destinations {
		if destination.ChannelID == channelID {
			return destination.Failure
		}
	}
	t.Fatalf("destination %s not found", channelID)
	return nil
}

func (f *pollFailureFixture) successfulWriteToBad(t *testing.T) {
	t.Helper()
	key := destinationKey(string(ports.SlackRecipientChannel), pollFailureBadChannel)
	worker := &destinationWorker{
		notifier:  f.notifier,
		channelID: pollFailureBadChannel,
		signal:    make(chan struct{}, 1),
	}
	err := worker.postReply(workItem{
		featureID:       pollFailureFeatureID,
		sourceFeatureID: pollFailureFeatureID,
		destinationKey:  key,
		kind:            string(ports.SlackRecipientChannel),
		channelID:       pollFailureBadChannel,
		reply: replyPayload{
			kind:     kindProgress,
			fallback: "Successful write rearms polling.",
		},
	})
	if err != nil {
		t.Fatalf("postReply(rearm) error = %v", err)
	}
}

func (f *pollFailureFixture) assertDomainUnchanged(t *testing.T) {
	t.Helper()
	current, err := f.harness.store.Load(pollFailureFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != feature.StatusImplementing {
		t.Errorf("feature status = %q; want %q", current.Status, feature.StatusImplementing)
	}
	record, err := loadFeatureRecord(f.harness.stateDir, pollFailureFeatureID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Pending) != 1 ||
		record.Pending[0].Identity != "permission:request-1" ||
		record.Pending[0].Resolution != nil {
		t.Errorf("pending inputs = %#v; want the unresolved permission unchanged", record.Pending)
	}
	if got := len(f.answer.permissionSubmissions()); got != 0 {
		t.Errorf("permission submissions = %d; want none from poll failures", got)
	}
}

func assertPollFailureEvent(
	t *testing.T,
	eventData map[string]any,
	wantCode errcat.Code,
	wantCause string,
	wantAttempts int,
) {
	t.Helper()
	want := map[string]any{
		"destination_kind": string(ports.SlackRecipientChannel),
		"item_kind":        "poll",
		"failure_class":    "destination",
		"slack_error":      wantCause,
		"error_code":       string(wantCode),
		"attempts":         wantAttempts,
	}
	for key, value := range want {
		if fmt.Sprint(eventData[key]) != fmt.Sprint(value) {
			t.Errorf("slack.delivery_failed[%q] = %#v; want %#v", key, eventData[key], value)
		}
	}
}

func TestSlackResponderPollFailurePermanentSuspendsOnlyFailedDestinationUntilWrite(t *testing.T) {
	fixture := newPollFailureFixture(t)
	fixture.scriptBad(testsupport.Response{
		Body: map[string]any{"ok": false, "error": "channel_not_found"},
	})

	fixture.tick()

	failure := fixture.destinationFailure(t, pollFailureBadChannel)
	if failure == nil ||
		failure.Code != errcat.SlackRecipientNotNotified ||
		failure.SlackError != "channel_not_found" ||
		failure.Count != 1 {
		t.Fatalf("permanent poll failure = %#v; want one channel_not_found failure", failure)
	}
	for range 5 {
		fixture.advance(responderPollInterval)
		fixture.tick()
	}
	if got := fixture.pollCount(pollFailureBadChannel); got != 1 {
		t.Errorf("failed destination polls = %d; want one while suspended", got)
	}
	if got := fixture.pollCount(pollFailureGoodChannel); got != 6 {
		t.Errorf("healthy destination polls = %d; want one per tick", got)
	}
	events := fixture.harness.observer.ofKind("slack.delivery_failed")
	if len(events) != 1 {
		t.Fatalf("slack.delivery_failed events = %d; want one", len(events))
	}
	assertPollFailureEvent(
		t,
		events[0].Data,
		errcat.SlackRecipientNotNotified,
		"channel_not_found",
		1,
	)

	fixture.successfulWriteToBad(t)
	if failure := fixture.destinationFailure(t, pollFailureBadChannel); failure != nil {
		t.Fatalf("destination failure after successful write = %#v; want nil", failure)
	}
	fixture.advance(responderPollInterval)
	fixture.tick()
	if got := fixture.pollCount(pollFailureBadChannel); got != 2 {
		t.Errorf("failed destination polls after rearm = %d; want two", got)
	}
	fixture.assertDomainUnchanged(t)
}

func TestSlackResponderPollFailureTransientBudgetSuspendsAndReportsEachEpisodeOnce(t *testing.T) {
	fixture := newPollFailureFixture(t)
	transient := testsupport.Response{Status: http.StatusServiceUnavailable}
	fixture.scriptBad(transient, transient, transient, transient)

	for range retryLimit + 1 {
		fixture.tick()
		fixture.advance(responderPollInterval)
	}

	if got := fixture.pollCount(pollFailureBadChannel); got != retryLimit+1 {
		t.Fatalf("transient poll attempts = %d; want initial plus %d retries", got, retryLimit)
	}
	failure := fixture.destinationFailure(t, pollFailureBadChannel)
	if failure == nil ||
		failure.Code != errcat.SlackDeliveryRetriesExhausted ||
		failure.SlackError != "retries_exhausted" ||
		failure.Count != 1 {
		t.Fatalf("exhausted poll failure = %#v; want one retries-exhausted report", failure)
	}
	events := fixture.harness.observer.ofKind("slack.delivery_failed")
	if len(events) != 1 {
		t.Fatalf("first episode events = %d; want one", len(events))
	}
	assertPollFailureEvent(
		t,
		events[0].Data,
		errcat.SlackDeliveryRetriesExhausted,
		"retries_exhausted",
		retryLimit+1,
	)

	for range 10 {
		fixture.tick()
		fixture.advance(responderPollInterval)
	}
	if got := fixture.pollCount(pollFailureBadChannel); got != retryLimit+1 {
		t.Errorf("suspended transient destination polls = %d; want no fifth attempt", got)
	}
	if got := fixture.pollCount(pollFailureGoodChannel); got != retryLimit+1+10 {
		t.Errorf("healthy destination polls = %d; want every tick", got)
	}

	fixture.successfulWriteToBad(t)
	fixture.scriptBad(transient, transient, transient, transient)
	for range retryLimit + 1 {
		fixture.tick()
		fixture.advance(responderPollInterval)
	}
	if got := fixture.pollCount(pollFailureBadChannel); got != 2*(retryLimit+1) {
		t.Errorf("rearmed destination attempts = %d; want a fresh four-attempt budget", got)
	}
	waitFor(t, time.Second, func() bool {
		return len(fixture.harness.observer.ofKind("slack.delivery_failed")) == 2
	})
	events = fixture.harness.observer.ofKind("slack.delivery_failed")
	if len(events) != 2 {
		t.Fatalf("two exhausted episodes events = %d; want two", len(events))
	}
	failure = fixture.destinationFailure(t, pollFailureBadChannel)
	if failure == nil || failure.Count != 1 {
		t.Fatalf("second episode failure = %#v; want one report for the fresh episode", failure)
	}
	fixture.assertDomainUnchanged(t)
}

func TestSlackResponderPollFailureRateLimitPausesOnlyOneDestination(t *testing.T) {
	fixture := newPollFailureFixture(t)
	fixture.scriptBad(testsupport.Response{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": []string{"25"}},
	})

	fixture.tick()
	for range 2 {
		fixture.advance(responderPollInterval)
		fixture.tick()
	}

	if got := fixture.pollCount(pollFailureBadChannel); got != 1 {
		t.Errorf("rate-limited destination polls before retry-after = %d; want one", got)
	}
	if got := fixture.pollCount(pollFailureGoodChannel); got != 3 {
		t.Errorf("healthy destination polls during peer pause = %d; want three", got)
	}
	if failure := fixture.destinationFailure(t, pollFailureBadChannel); failure != nil {
		t.Fatalf("rate-limit failure within budget = %#v; want nil", failure)
	}
	if got := len(fixture.harness.observer.ofKind("slack.delivery_failed")); got != 0 {
		t.Fatalf("rate-limit failure events within budget = %d; want zero", got)
	}

	fixture.advance(responderPollInterval)
	fixture.tick()
	if got := fixture.pollCount(pollFailureBadChannel); got != 2 {
		t.Errorf("rate-limited destination polls after retry-after = %d; want two", got)
	}
	if got := fixture.pollCount(pollFailureGoodChannel); got != 4 {
		t.Errorf("healthy destination polls after peer resumes = %d; want four", got)
	}
	fixture.assertDomainUnchanged(t)
}

func TestSlackResponderPollFailureCredentialReportsOnceAndContinuesPolling(t *testing.T) {
	fixture := newPollFailureFixture(t)
	credentialFailure := testsupport.Response{
		Body: map[string]any{"ok": false, "error": "invalid_auth"},
	}
	fixture.scriptBad(credentialFailure, credentialFailure, credentialFailure)

	for range 3 {
		fixture.tick()
		fixture.advance(responderPollInterval)
	}

	if got := fixture.pollCount(pollFailureBadChannel); got != 3 {
		t.Errorf("credential-failing destination polls = %d; want continued polling", got)
	}
	if got := fixture.pollCount(pollFailureGoodChannel); got != 3 {
		t.Errorf("healthy destination polls = %d; want continued polling", got)
	}
	if failure := fixture.destinationFailure(t, pollFailureBadChannel); failure != nil {
		t.Fatalf("credential destination failure = %#v; want no feature warning", failure)
	}
	failures, _ := fixture.reporter.snapshot()
	if len(failures) != 1 {
		t.Fatalf("credential reporter failures = %d; want one per episode", len(failures))
	}
	if failures[0].canonical.Code != errcat.SlackTokenRejected {
		t.Errorf(
			"credential reporter code = %q; want %q",
			failures[0].canonical.Code,
			errcat.SlackTokenRejected,
		)
	}
	events := fixture.harness.observer.ofKind("slack.delivery_failed")
	if len(events) != 1 {
		t.Fatalf("credential failure events = %d; want one per episode", len(events))
	}
	if got := events[0].Data["item_kind"]; got != "poll" {
		t.Errorf("credential event item_kind = %#v; want poll", got)
	}
	if got := events[0].Data["failure_class"]; got != "credential" {
		t.Errorf("credential event failure_class = %#v; want credential", got)
	}

	fixture.tick()
	failures, successes := fixture.reporter.snapshot()
	if len(failures) != 1 {
		t.Errorf("credential reporter failures after recovery = %d; want one", len(failures))
	}
	if len(successes) == 0 {
		t.Error("credential reporter successes after recovery = 0; want self-healing success")
	}
	fixture.assertDomainUnchanged(t)
}
