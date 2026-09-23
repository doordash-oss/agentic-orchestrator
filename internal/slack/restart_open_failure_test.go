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
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type heldRetryClock struct {
	*fakeClock
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	armed   atomic.Bool
}

func (c *heldRetryClock) Sleep(ctx context.Context, d time.Duration) bool {
	if c.armed.Load() && d == sleepChunk {
		blocked := false
		c.once.Do(func() { blocked = true })
		if blocked {
			c.entered <- struct{}{}
			select {
			case <-c.release:
			case <-ctx.Done():
				return false
			}
		}
	}
	return c.fakeClock.Sleep(ctx, d)
}

func TestSlackRestartEventOpeningWaitDoesNotBlockChannel(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()[1]))
	owner := h.seedFeature("feature-1", nil)
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		"channel:C-ENG": {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "100.000001"},
	}, Pending: []pendingInputRecord{{
		Identity: "permission:one", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
		MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
	}}}
	if err := persistFeatureRecord(h.stateDir, owner.ID, record); err != nil {
		t.Fatal(err)
	}
	h.pending.setFromRecord(owner.ID, record)
	n := h.newNotifier(0)
	clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
	n.clock = clock
	n.Start()
	t.Cleanup(func() {
		select {
		case <-clock.release:
		default:
			close(clock.release)
		}
		n.Stop(context.Background())
	})
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
	clock.armed.Store(true)
	baselinePosts := h.server.CallCount("chat.postMessage")
	h.pending.set(owner.ID)
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Recipients = append(s.Recipients, testRecipients()[0])
	})
	h.server.Script("conversations.open", testsupport.Response{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": []string{"60"}},
		Body:    map[string]any{"ok": false, "error": "ratelimited"},
	})
	n.DomainEventTap(ports.Event{Type: ports.PhaseStarted, FeatureID: owner.ID, Phase: feature.PhasePlan})
	select {
	case <-clock.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("event DM did not enter retry wait")
	}
	n.DomainEventTap(ports.Event{Type: ports.PhaseStarted, FeatureID: owner.ID, Phase: feature.PhaseImplement})
	deadline := time.After(5 * time.Second)
	for h.server.CallCount("chat.postMessage") < baselinePosts+3 {
		select {
		case <-deadline:
			t.Fatalf("channel events held: posts=%#v", h.server.Requests("chat.postMessage"))
		case <-time.After(time.Millisecond):
		}
	}
	channel := postsTo(h.server, "C-ENG")
	closures := 0
	for _, post := range channel {
		if fieldString(post, "text") == "#1 was resolved in Agentico." {
			closures++
		}
	}
	if closures != 1 {
		t.Fatalf("channel closure count = %d; want one", closures)
	}
	close(clock.release)
	n.sweepDestinations()
	waitFor(t, 5*time.Second, func() bool { return h.server.CallCount("conversations.open") >= 2 })
	waitFor(t, 5*time.Second, func() bool { return h.server.CallCount("chat.postMessage") >= baselinePosts+6 })
	n.sweepDestinations()
	if got := h.server.CallCount("chat.postMessage"); got != baselinePosts+6 {
		t.Fatalf("posts after sweep = %d; want channel closure/two events and DM root/two events", got)
	}
}

func TestSlackRestartResponderPollsWhileNewDMOpeningWaits(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()[1]))
	owner := h.seedFeature("feature-1", nil)
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		"channel:C-ENG": {
			Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "100.000001",
			PostingIndex: []postingIndexEntry{{Identity: "permission:one", Tag: "#1", MessageTS: "100.000002"}},
		},
	}, Pending: []pendingInputRecord{{
		Identity: "permission:one", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
		MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
	}}}
	if err := persistFeatureRecord(h.stateDir, owner.ID, record); err != nil {
		t.Fatal(err)
	}
	h.pending.setFromRecord(owner.ID, record)
	responderClock := newManualResponderClock()
	answer := &fakeSlackAnswerPort{}
	n := h.newNotifier(0)
	n.responderClock = responderClock
	n.answer = answer
	clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
	n.clock = clock
	n.Start()
	t.Cleanup(func() {
		select {
		case <-clock.release:
		default:
			close(clock.release)
		}
		n.Stop(context.Background())
	})
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
	clock.armed.Store(true)
	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "pending"},
		{TS: "100.000003", ThreadTS: "100.000001", Text: "allow", User: "U-ADA"},
	})
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Recipients = append(s.Recipients, testRecipients()[0])
	})
	h.server.Script("conversations.open", testsupport.Response{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": []string{"60"}},
		Body:    map[string]any{"ok": false, "error": "ratelimited"},
	})
	responderClock.tick(t)
	select {
	case <-clock.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep DM did not enter retry wait")
	}
	deadline := time.After(5 * time.Second)
	for h.server.CallCount("conversations.replies") == 0 || len(answer.permissionSubmissions()) != 1 {
		select {
		case <-deadline:
			t.Fatalf("polls=%#v submissions=%#v", h.server.Requests("conversations.replies"), answer.permissionSubmissions())
		case <-time.After(time.Millisecond):
		}
	}
	close(clock.release)
	waitFor(t, 5*time.Second, func() bool { return !n.sweepRunning.Load() })
	responderClock.tick(t)
	if got := len(answer.permissionSubmissions()); got != 1 {
		t.Fatalf("permission submissions = %d; want one", got)
	}
}

func TestSlackRestartOpeningWaitDoesNotBlockHealthyDestination(t *testing.T) {
	for _, mode := range []string{"startup", "sweep"} {
		t.Run(mode, func(t *testing.T) {
			h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
			h.seedFeature("feature-1", nil)
			h.seedFeature("feature-2", nil)
			record := &featureRecord{
				Version: recordVersion,
				Destinations: map[string]destinationRecord{
					"channel:C-ENG": {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "100.000001"},
				},
				Pending: []pendingInputRecord{{
					Identity: "permission:one", SourceFeatureID: "feature-1",
					Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
					MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
				}},
			}
			if err := persistFeatureRecord(h.stateDir, "feature-1", record); err != nil {
				t.Fatal(err)
			}
			n := h.newNotifier(0)
			t.Cleanup(func() { n.Stop(context.Background()) })
			clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
			n.clock = clock
			clock.armed.Store(true)
			defer func() {
				select {
				case <-clock.release:
				default:
					close(clock.release)
				}
			}()
			h.server.Script("conversations.open", testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Headers: http.Header{"Retry-After": []string{"60"}},
				Body:    map[string]any{"ok": false, "error": "ratelimited"},
			})
			if mode == "sweep" {
				n.startupDone.Store(true)
			}
			done := make(chan struct{})
			go func() {
				if mode == "startup" {
					n.runRestartPass()
				} else {
					n.sweepDestinations()
				}
				close(done)
			}()
			select {
			case <-clock.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("DM did not enter retry wait")
			}
			deadline := time.After(5 * time.Second)
			for h.server.CallCount("chat.postMessage") < 2 || h.server.CallCount("chat.update") == 0 {
				select {
				case <-deadline:
					t.Fatal("healthy channel did not receive its owed closure, card refresh, and new root while DM retry was held")
				case <-time.After(time.Millisecond):
				}
			}
			close(clock.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("destination pass did not finish")
			}
		})
	}
}

func TestSlackRestartOpeningWaitDoesNotBlockAnotherNewDM(t *testing.T) {
	for _, mode := range []string{"startup", "sweep"} {
		t.Run(mode, func(t *testing.T) {
			recipients := []ports.SlackRecipient{
				{Kind: ports.SlackRecipientUser, ID: "U-ADA", DisplayName: "Ada"},
				{Kind: ports.SlackRecipientUser, ID: "U-BOB", DisplayName: "Bob"},
			}
			h := newNotifierHarness(t, defaultTestSettings("xoxb-test", recipients...))
			h.seedFeature("feature-1", nil)
			n := h.newNotifier(0)
			t.Cleanup(func() { n.Stop(context.Background()) })
			clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
			n.clock = clock
			clock.armed.Store(true)
			defer func() {
				select {
				case <-clock.release:
				default:
					close(clock.release)
				}
			}()
			h.server.Script("conversations.open", testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Headers: http.Header{"Retry-After": []string{"60"}},
				Body:    map[string]any{"ok": false, "error": "ratelimited"},
			})
			if mode == "sweep" {
				n.startupDone.Store(true)
			}
			done := make(chan struct{})
			go func() {
				if mode == "startup" {
					n.runRestartPass()
				} else {
					n.sweepDestinations()
				}
				close(done)
			}()
			select {
			case <-clock.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first DM did not enter retry wait")
			}
			deadline := time.After(5 * time.Second)
			for h.server.CallCount("conversations.open") < 2 || h.server.CallCount("chat.postMessage") == 0 {
				select {
				case <-deadline:
					t.Fatal("second DM did not open and receive its root while first retry was held")
				case <-time.After(time.Millisecond):
				}
			}
			close(clock.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("destination pass did not finish")
			}
		})
	}
}

func TestSlackRestartOpeningRetryRechecksEligibility(t *testing.T) {
	for _, change := range []string{"disable", "clear token", "remove user"} {
		t.Run(change, func(t *testing.T) {
			h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
			h.seedFeature("feature-1", nil)
			n := h.newNotifier(0)
			t.Cleanup(func() { n.Stop(context.Background()) })
			clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
			n.clock = clock
			clock.armed.Store(true)
			h.server.Script("conversations.open", testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Headers: http.Header{"Retry-After": []string{"60"}},
				Body:    map[string]any{"ok": false, "error": "ratelimited"},
			})
			done := make(chan struct{})
			go func() { n.runRestartPass(); close(done) }()
			select {
			case <-clock.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("DM did not enter retry wait")
			}
			h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
				switch change {
				case "disable":
					s.Enabled = false
				case "clear token":
					s.Token = ""
				case "remove user":
					s.Recipients = s.Recipients[1:]
				}
			})
			close(clock.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("destination pass did not finish")
			}
			if got := h.server.CallCount("conversations.open"); got != 1 {
				t.Fatalf("conversations.open after %s = %d; want only initial attempt", change, got)
			}
		})
	}
}

func TestSlackRestartOpeningRetryUsesCurrentCredential(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-old", testRecipients()...))
	h.seedFeature("feature-1", nil)
	n := h.newNotifier(0)
	t.Cleanup(func() { n.Stop(context.Background()) })
	clock := &heldRetryClock{fakeClock: h.clock, entered: make(chan struct{}, 1), release: make(chan struct{})}
	n.clock = clock
	clock.armed.Store(true)
	defer func() {
		select {
		case <-clock.release:
		default:
			close(clock.release)
		}
	}()
	var mu sync.Mutex
	var tokens []string
	n.newClient = func(token string) (slackClient, error) {
		mu.Lock()
		tokens = append(tokens, token)
		mu.Unlock()
		return NewClient(token, WithBaseURL(h.server.URL()))
	}
	h.server.Script("conversations.open", testsupport.Response{
		Status:  http.StatusTooManyRequests,
		Headers: http.Header{"Retry-After": []string{"60"}},
		Body:    map[string]any{"ok": false, "error": "ratelimited"},
	})
	done := make(chan struct{})
	go func() { n.runRestartPass(); close(done) }()
	select {
	case <-clock.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("DM did not enter retry wait")
	}
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Token = "xoxb-new"
		s.CredentialGeneration++
	})
	close(clock.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("destination pass did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(tokens) < 2 || tokens[0] != "xoxb-old" || tokens[len(tokens)-1] != "xoxb-new" {
		t.Fatalf("delivery credentials = %#v; want old token then replacement", tokens)
	}
}

func TestSlackRestartOpenPermanentFailureDoesNotRetryWithinPass(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
	h.seedFeature("feature-1", nil)
	h.seedFeature("feature-2", nil)
	h.server.Script("conversations.open", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "not_in_channel"},
	})
	n := h.newNotifier(0)
	t.Cleanup(func() { n.Stop(context.Background()) })
	if !n.runRestartPass() {
		t.Fatal("runRestartPass() = false; want completed pass")
	}
	if got := h.server.CallCount("conversations.open"); got != 1 {
		t.Fatalf("conversations.open calls = %d; want one", got)
	}
	if got := h.server.CallCount("chat.postMessage"); got != 2 {
		t.Fatalf("healthy channel posts = %d; want one root per feature", got)
	}
	record, err := loadFeatureRecord(h.stateDir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if failure := record.Destinations["user:U-ADA"].Failure; failure == nil ||
		failure.Code != errcat.SlackRecipientNotNotified || failure.SlackError != "not_in_channel" {
		t.Fatalf("user destination failure = %#v; want permanent not_in_channel warning", failure)
	}
	if events := h.observer.ofKind("slack.delivery_failed"); len(events) != 1 {
		t.Fatalf("delivery failure events = %d; want one", len(events))
	}
}

func TestSlackRestartOpenCredentialFailureReportsOnce(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
	h.seedFeature("feature-1", nil)
	h.server.Script("conversations.open", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "invalid_auth"},
	})
	n := h.newNotifier(0)
	reporter := &fakeDeliveryReporter{}
	n.reporter = reporter
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.runRestartPass()
	if got := h.server.CallCount("conversations.open"); got != 1 {
		t.Fatalf("conversations.open calls = %d; want one", got)
	}
	if failures, _ := reporter.snapshot(); len(failures) != 1 ||
		failures[0].canonical.Code != errcat.SlackTokenRejected {
		t.Fatalf("credential reports = %#v; want one token rejection", failures)
	}
	if events := h.observer.ofKind("slack.delivery_failed"); len(events) != 1 {
		t.Fatalf("delivery failure events = %d; want one", len(events))
	}
	if got := h.server.CallCount("chat.postMessage"); got != 1 {
		t.Fatalf("healthy channel posts = %d; want one root", got)
	}
}

func TestSlackRestartOpenRateLimitHonorsWaitAndBoundsAttempts(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
	h.seedFeature("feature-1", nil)
	for i := 0; i <= retryLimit; i++ {
		h.server.Script("conversations.open", testsupport.Response{
			Status:  http.StatusTooManyRequests,
			Headers: http.Header{"Retry-After": []string{"2"}},
			Body:    map[string]any{"ok": false, "error": "ratelimited"},
		})
	}
	n := h.newNotifier(0)
	t.Cleanup(func() { n.Stop(context.Background()) })
	before := h.clock.Now()
	n.runRestartPass()
	if got := h.server.CallCount("conversations.open"); got != retryLimit+1 {
		t.Fatalf("conversations.open calls = %d; want %d bounded attempts", got, retryLimit+1)
	}
	if elapsed := h.clock.Now().Sub(before); elapsed < time.Duration(retryLimit)*2*time.Second {
		t.Fatalf("rate-limit elapsed = %v; want at least %v", elapsed, time.Duration(retryLimit)*2*time.Second)
	}
	if got := h.server.CallCount("chat.postMessage"); got != 1 {
		t.Fatalf("healthy channel posts = %d; want one root", got)
	}
	deadline := time.After(5 * time.Second)
	for {
		events := h.observer.ofKind("slack.delivery_failed")
		if len(events) == 1 {
			if events[0].Data["attempts"] != retryLimit+1 {
				t.Fatalf("rate-limit failure events = %#v; want one after bounded attempts", events)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("rate-limit failure events = %#v; want one after bounded attempts", events)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestSlackRestartOpenRateLimitRecoversAfterWait(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-test", testRecipients()...))
	h.seedFeature("feature-1", nil)
	h.server.Script("conversations.open",
		testsupport.Response{
			Status:  http.StatusTooManyRequests,
			Headers: http.Header{"Retry-After": []string{"3"}},
			Body:    map[string]any{"ok": false, "error": "ratelimited"},
		},
		testsupport.Response{Body: map[string]any{
			"ok": true, "channel": map[string]any{"id": "D-ADA"},
		}},
	)
	n := h.newNotifier(0)
	t.Cleanup(func() { n.Stop(context.Background()) })
	before := h.clock.Now()
	n.runRestartPass()
	if got := h.server.CallCount("conversations.open"); got != 2 {
		t.Fatalf("conversations.open calls = %d; want rate limit then success", got)
	}
	if elapsed := h.clock.Now().Sub(before); elapsed < 3*time.Second {
		t.Fatalf("rate-limit elapsed = %v; want at least 3s", elapsed)
	}
	if got := h.server.CallCount("chat.postMessage"); got != 2 {
		t.Fatalf("destination posts = %d; want both roots", got)
	}
	if events := h.observer.ofKind("slack.delivery_failed"); len(events) != 0 {
		t.Fatalf("recovered delivery failure events = %#v; want none", events)
	}
}
