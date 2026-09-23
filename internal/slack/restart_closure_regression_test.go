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
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackRestartClosureDebtAcrossEventsAndToggle(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()...))
	owner := h.seedFeature("feature-1", nil)
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		"user:U-ADA":    {Kind: "user", SlackID: "U-ADA", ChannelID: "D-U-ADA", RootTS: "100.000001"},
		"channel:C-ENG": {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "200.000001"},
	}, Pending: []pendingInputRecord{{
		Identity: "permission:one", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
		MessageTS: map[string]string{"user:U-ADA": "100.000002", "channel:C-ENG": "200.000002"},
	}}}
	n := h.newNotifier(8)
	n.records[owner.ID] = record
	t.Cleanup(func() { n.Stop(context.Background()) })
	settings := h.settings.SlackSettings()
	first := n.reconcilePending(settings, owner, owner, record, resolutionAgentico)
	if got := closureCount(first); got != 2 {
		t.Fatalf("first closure work = %d, want 2", got)
	}
	// The next event can run while the first two deliveries remain queued.
	if got := closureCount(n.reconcilePending(settings, owner, owner, record, resolutionAgentico)); got != 0 {
		t.Fatalf("concurrent duplicate closure work = %d", got)
	}
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Categories.NeedsInput = false })
	defaultReply, _ := defaultOKResponder()
	var failed atomic.Bool
	h.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method == "chat.postMessage" && request.Fields["channel"] == "C-ENG" &&
			failed.CompareAndSwap(false, true) {
			return testsupport.Response{
				Status: http.StatusOK, Body: map[string]any{"ok": false, "error": "channel_not_found"},
			}
		}
		return defaultReply(method, request)
	})
	for _, item := range first {
		worker := n.workerFor(item.channelID)
		worker.mu.Lock()
		worker.lastWrite = h.clock.Now().Add(-time.Second)
		worker.mu.Unlock()
		_ = worker.postReply(item)
		n.releaseClosureDelivery(item.featureID, item.reply.identity, item.destinationKey)
	}
	if !record.Resolved[0].closureOwed("channel:C-ENG") ||
		record.Resolved[0].closureOwed("user:U-ADA") {
		t.Fatalf("per-destination closure debt = %#v", record.Resolved[0].Resolution)
	}
	if timerWork, _, _ := n.reconcilePendingWithPolicy(
		settings, owner, owner, record, resolutionAgentico, false,
	); closureCount(timerWork) != 0 {
		t.Fatalf("responder tick retried permanent closure failure: %#v", timerWork)
	}
	retry := n.reconcilePending(settings, owner, owner, record, resolutionAgentico)
	if closureCount(retry) != 1 || retry[0].destinationKey != "channel:C-ENG" {
		t.Fatalf("next event closure retry = %#v", retry)
	}
	if err := n.workerFor(retry[0].channelID).postReply(retry[0]); err != nil {
		t.Fatal(err)
	}
	n.releaseClosureDelivery(retry[0].featureID, retry[0].reply.identity, retry[0].destinationKey)
	if got := closureCount(n.reconcilePending(settings, owner, owner, record, resolutionAgentico)); got != 0 {
		t.Fatalf("acknowledged closure queued again = %d", got)
	}
	if got := h.server.CallCount("chat.postMessage"); got != 3 {
		t.Fatalf("closure attempts = %d; want failure and two acknowledged deliveries", got)
	}
}

func TestSlackRestartSweepWaitsForReadableSource(t *testing.T) {
	h := newNotifierHarness(t, ports.SlackRuntimeSettings{Categories: allCategories()})
	h.seedFeature("feature-1", nil)
	source := &restartPendingSource{errs: map[string]error{"feature-1": errors.New("not ready")}}
	n := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Pending: source, Clock: h.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	n.SetServerName("Local agent")
	t.Cleanup(func() { n.Stop(context.Background()) })
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		*s = defaultTestSettings("xoxb-restart", testRecipients()[0])
	})
	source.errs = nil
	n.sweepDestinations()
	if got := h.server.AllRequests(); len(got) != 0 {
		t.Fatalf("readable source before SignalReady caused Slack requests: %#v", got)
	}
	source.errs = map[string]error{"feature-1": errors.New("not ready")}
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
	if got := h.server.AllRequests(); len(got) != 0 {
		t.Fatalf("unreadable startup caused Slack requests: %#v", got)
	}
	source.errs = nil
	n.sweepDestinations()
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 1 })
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		s.Recipients = append(s.Recipients, testRecipients()[1])
	})
	source.errs = map[string]error{"feature-1": errors.New("not ready")}
	n.sweepDestinations()
	if got := h.server.CallCount("chat.postMessage"); got != 1 {
		t.Fatalf("unreadable source caused new Slack posts: %d", got)
	}
	if n.sweepKey == destinationFingerprint(h.settings.SlackSettings()) {
		t.Fatal("unreadable sweep consumed destination change")
	}
	source.errs = nil
	n.sweepDestinations()
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 2 })
}

func TestSlackRestartAlreadyResolvedPendingRetiresOnlyOwedDestination(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()...))
	owner := h.seedFeature("feature-1", nil)
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		"user:U-ADA":    {Kind: "user", SlackID: "U-ADA", ChannelID: "D-U-ADA", RootTS: "100.000001"},
		"channel:C-ENG": {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "200.000001"},
	}, Pending: []pendingInputRecord{{
		Identity: "review:one:", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingReview), ReviewID: "one", Tag: "#1",
		MessageTS: map[string]string{"user:U-ADA": "100.000002", "channel:C-ENG": "200.000002"},
		Resolution: &postingResolution{Kind: resolutionAgentico,
			ClosureAcknowledged: map[string]bool{"user:U-ADA": true}},
	}, {
		Identity: "permission:live", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "live", Tag: "#2",
		MessageTS: map[string]string{"user:U-ADA": "100.000003", "channel:C-ENG": "200.000003"},
	}}}
	if err := persistFeatureRecord(h.stateDir, owner.ID, record); err != nil {
		t.Fatal(err)
	}
	h.pending.set(owner.ID, ports.SlackPendingInput{
		FeatureID: owner.ID, Kind: ports.SlackPendingPermission, RequestID: "live",
	})
	start := func() *Notifier {
		n := h.newNotifier(8)
		n.Start()
		n.SignalReady()
		waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
		return n
	}
	first := start()
	if got := h.server.Requests("chat.postMessage"); len(got) != 1 ||
		fieldString(got[0], "channel") != "C-ENG" ||
		fieldString(got[0], "text") != "#1 was resolved in Agentico." {
		t.Fatalf("closure after reload = %#v; want only owed channel", got)
	}
	after, err := loadFeatureRecord(h.stateDir, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Pending) != 1 || after.Pending[0].Identity != "permission:live" ||
		len(after.Resolved) != 1 ||
		after.Resolved[0].closureOwed("channel:C-ENG") ||
		!after.Resolved[0].Resolution.ClosureAcknowledged["user:U-ADA"] ||
		!after.Resolved[0].Resolution.ClosureAcknowledged["channel:C-ENG"] {
		t.Fatalf("retirement after reload lost acknowledgement: pending=%#v resolved=%#v",
			after.Pending, after.Resolved)
	}
	first.Stop(context.Background())
	second := start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	if got := h.server.Requests("chat.postMessage"); len(got) != 1 {
		t.Fatalf("acknowledged closure repeated on second restart: %#v", got)
	}
}

func closureCount(work []workItem) int {
	count := 0
	for _, item := range work {
		if item.reply.closure {
			count++
		}
	}
	return count
}
