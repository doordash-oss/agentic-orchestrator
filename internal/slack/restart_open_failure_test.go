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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

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
	if events := h.observer.ofKind("slack.delivery_failed"); len(events) != 1 ||
		events[0].Data["attempts"] != retryLimit+1 {
		t.Fatalf("rate-limit failure events = %#v; want one after bounded attempts", events)
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
