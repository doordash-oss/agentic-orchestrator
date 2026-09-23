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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSlackRestartClosesSessionItemAndRefreshesCard(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()...))
	h.seedFeature("feature-1", nil)
	permission := pendingInputRecord{
		Identity: "permission:request-1", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: "request-1",
		Tag: "#1", MessageTS: map[string]string{
			"user:U-ADA": "100.000002", "channel:C-ENG": "200.000002",
		},
	}
	review := pendingInputRecord{
		Identity: "review:review-1:", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingReview), ReviewID: "review-1",
		Tag: "#2", MessageTS: map[string]string{
			"user:U-ADA": "100.000003", "channel:C-ENG": "200.000003",
		},
	}
	record := &featureRecord{
		Version: recordVersion, TagCounter: 2,
		Destinations: map[string]destinationRecord{
			"user:U-ADA": {
				Kind: "user", SlackID: "U-ADA", ChannelID: "D-U-ADA",
				RootTS: "100.000001", Ledger: []string{"100.000001", "100.000002", "100.000003"},
			},
			"channel:C-ENG": {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG",
				RootTS: "200.000001", Ledger: []string{"200.000001", "200.000002", "200.000003"},
			},
		},
		Pending: []pendingInputRecord{permission, review},
	}
	if err := persistFeatureRecord(h.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	h.pending.set("feature-1", ports.SlackPendingInput{
		FeatureID: "feature-1", Kind: ports.SlackPendingReview, ReviewID: "review-1",
	})
	n := h.newNotifier(0)
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SetServerName("Restart test")
	n.SignalReady()
	waitFor(t, time.Second, func() bool {
		return h.server.CallCount("chat.postMessage") == 2 &&
			h.server.CallCount("chat.update") == 2
	})
	for _, call := range h.server.Requests("chat.postMessage") {
		if !strings.Contains(fieldString(call, "text"), "#1 is no longer pending.") {
			t.Fatalf("unexpected closure: %#v", call)
		}
	}
	loaded, err := loadFeatureRecord(h.stateDir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Pending) != 1 || loaded.Pending[0].Tag != "#2" {
		t.Fatalf("pending after restart = %#v", loaded.Pending)
	}
}

func TestSlackRestartDeliversOnlyOwedClosure(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()...))
	h.seedFeature("feature-1", func(f *feature.Feature) { f.Status = feature.StatusDone })
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			"user:U-ADA": {
				Kind: "user", SlackID: "U-ADA", ChannelID: "D-U-ADA", RootTS: "100.000001",
			},
			"channel:C-ENG": {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "200.000001",
			},
		},
		Resolved: []pendingInputRecord{{
			Identity: "permission:request-1", SourceFeatureID: "feature-1",
			Kind: string(ports.SlackPendingPermission), Tag: "#1",
			MessageTS: map[string]string{
				"user:U-ADA": "100.000002", "channel:C-ENG": "200.000002",
			},
			Resolution: &postingResolution{
				Kind:                resolutionCleared,
				ClosureAcknowledged: map[string]bool{"user:U-ADA": true},
			},
		}},
	}
	if err := persistFeatureRecord(h.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	start := func() *Notifier {
		n := h.newNotifier(0)
		n.Start()
		n.SignalReady()
		waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
		return n
	}
	first := start()
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 1 })
	first.Stop(context.Background())
	second := start()
	defer second.Stop(context.Background())
	if got := h.server.CallCount("chat.postMessage"); got != 1 {
		t.Fatalf("closure repeated after second restart: %d", got)
	}
	call := h.server.Requests("chat.postMessage")[0]
	if fieldString(call, "channel") != "C-ENG" {
		t.Fatalf("closure posted to wrong destination: %#v", call)
	}
}

func TestSlackRestartBootstrapsDestinationWithoutFeatureEvent(t *testing.T) {
	h := newNotifierHarness(t, ports.SlackRuntimeSettings{
		Categories: allCategories(),
	})
	h.seedFeature("feature-1", nil)
	h.seedFeature("feature-2", nil)
	n := h.newNotifier(0)
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
	h.settings.mutate(func(s *ports.SlackRuntimeSettings) {
		*s = defaultTestSettings("xoxb-restart", testRecipients()[1])
	})
	n.sweepDestinations()
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.postMessage") == 2 })
	n.sweepDestinations()
	if got := h.server.CallCount("chat.postMessage"); got != 2 {
		t.Fatalf("duplicate root cards after unchanged tick: %d", got)
	}
}

func TestSlackRestartTerminalClosureNeverBootstrapsNewRecipient(t *testing.T) {
	for _, status := range []feature.Status{feature.StatusDone, feature.StatusPublished, feature.StatusFailed} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newNotifierHarness(t, defaultTestSettings("xoxb-restart", testRecipients()...))
			owner := h.seedFeature("feature-1", func(f *feature.Feature) { f.Status = status })
			const key = "user:U-ADA"
			record := &featureRecord{Version: recordVersion,
				Destinations: map[string]destinationRecord{key: {
					Kind: "user", SlackID: "U-ADA", ChannelID: "D-U-ADA", RootTS: "100.000001",
					Ledger: []string{"100.000001", "100.000002"},
				}},
				Pending: []pendingInputRecord{{
					Identity: "permission:stale", SourceFeatureID: owner.ID,
					Kind: string(ports.SlackPendingPermission), RequestID: "stale",
					Tag: "#1", MessageTS: map[string]string{key: "100.000002"},
				}},
			}
			if err := persistFeatureRecord(h.stateDir, owner.ID, record); err != nil {
				t.Fatal(err)
			}
			n := h.newNotifier(0)
			n.Start()
			t.Cleanup(func() { n.Stop(context.Background()) })
			n.SignalReady()
			waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
			waitFor(t, time.Second, func() bool {
				return h.server.CallCount("chat.postMessage") >= 1 &&
					h.server.CallCount("chat.update") >= 1
			})
			if got := h.server.Requests("chat.postMessage"); len(got) != 1 ||
				fieldString(got[0], "channel") != "D-U-ADA" ||
				fieldString(got[0], "text") != "#1 is no longer pending." {
				t.Fatalf("terminal closure posts = %#v", got)
			}
			if got := h.server.Requests("chat.update"); len(got) != 1 ||
				fieldString(got[0], "channel") != "D-U-ADA" {
				t.Fatalf("terminal card edits = %#v", got)
			}
			if got := h.server.Requests("conversations.open"); len(got) != 0 {
				t.Fatalf("new recipient was resolved: %#v", got)
			}
			current, err := loadFeatureRecord(h.stateDir, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := current.Destinations["channel:C-ENG"]; exists {
				t.Fatalf("terminal feature bootstrapped new recipient: %#v", current.Destinations)
			}
		})
	}
}
