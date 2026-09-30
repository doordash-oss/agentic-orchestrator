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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func TestSlackPerFeatureTransitionFirstMuteAfterPostStartupCreation(t *testing.T) {
	j := newPerFeatureJourney(t)
	j.n = j.h.newNotifier(0)
	j.n.Start()
	t.Cleanup(func() { j.n.Stop(context.Background()) })
	j.n.SignalReady()
	waitFor(t, time.Second, func() bool { return j.n.startupDone.Load() })

	extra := testRecipients()[1]
	response := j.request(http.MethodPost, "/api/v1/features", map[string]any{
		"name": "Created after startup",
		"slack_notifications": map[string]any{"recipients": []any{map[string]any{
			"typed_text": extra.TypedText, "kind": extra.Kind,
			"id": extra.ID, "display_name": extra.DisplayName,
		}}},
	})
	j.id, _ = response["feature_id"].(string)
	if j.id == "" {
		t.Fatalf("create response lacks feature ID: %+v", response)
	}
	j.n.DomainEventTap(ports.Event{Type: ports.FeatureStarted, FeatureID: j.id})
	waitFor(t, time.Second, func() bool {
		record, err := loadFeatureRecord(j.h.stateDir, j.id)
		return err == nil &&
			record.Destinations["user:U-ADA"].RootTS != "" &&
			record.Destinations["channel:C-ENG"].RootTS != ""
	})
	if got := len(postsTo(j.h.server, "D-U-ADA")); got != 1 {
		t.Fatalf("global destination root posts = %d; want 1", got)
	}
	if got := len(postsTo(j.h.server, "C-ENG")); got != 1 {
		t.Fatalf("feature destination root posts = %d; want 1", got)
	}
	j.drain()
	beforeMute := len(j.h.server.AllRequests())

	j.configure(map[string]any{"mode": "muted"})
	stored, err := j.h.store.Load(j.id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SlackNotifications == nil || stored.SlackNotifications.Mode != feature.SlackMuted {
		t.Fatalf("mute mutation stored section = %+v", stored.SlackNotifications)
	}
	j.n.sweepDestinations()
	waitFor(t, time.Second, func() bool {
		paused := 0
		for _, request := range j.h.server.Requests("chat.update") {
			if strings.Contains(fieldString(request, "text"), "Updates are paused") {
				paused++
			}
		}
		return paused >= 2
	})
	j.drain()
	updates := map[string]int{"D-U-ADA": 0, "C-ENG": 0}
	for _, request := range j.h.server.AllRequests()[beforeMute:] {
		if request.Path != "/api/chat.update" {
			t.Errorf("mute transition sent %s; want only card edits", request.Path)
			continue
		}
		channel := fieldString(request, "channel")
		updates[channel]++
		if !strings.Contains(fieldString(request, "text"), "Updates are paused") {
			t.Errorf("mute card edit in %s omitted paused line: %q", channel, fieldString(request, "text"))
		}
	}
	for channel, got := range updates {
		if got != 1 {
			t.Errorf("paused card edits in %s = %d; want 1", channel, got)
		}
	}
	before := len(j.h.server.AllRequests())
	j.n.sweepDestinations()
	j.n.sweepDestinations()
	j.n.DomainEventTap(startedEvent(j.id, feature.PhaseResearch))
	j.drain()
	if got := len(j.h.server.AllRequests()); got != before {
		t.Errorf("later muted sweeps and event made %d requests; want 0", got-before)
	}
}

type countingSlackFeatureStore struct {
	*feature.Store
	lists atomic.Int64
}

func (s *countingSlackFeatureStore) List() ([]*feature.Feature, error) {
	s.lists.Add(1)
	return s.Store.List()
}

func TestSlackPerFeatureUnchangedTickDoesNotScanStore(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	h.seedFeature("F-1", nil)
	n := h.newNotifier(0)
	counting := &countingSlackFeatureStore{Store: h.store}
	n.store = counting
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })

	initial := counting.lists.Load()
	n.sweepDestinations()
	if got := counting.lists.Load(); got != initial {
		t.Fatalf("unchanged tick scans = %d; want %d", got, initial)
	}
	if err := h.store.Modify("F-1", func(f *feature.Feature) error {
		f.Name = "Unrelated change"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	n.sweepDestinations()
	if got := counting.lists.Load(); got != initial {
		t.Fatalf("unrelated edit scans = %d; want %d", got, initial)
	}
	if err := h.store.Modify("F-1", func(f *feature.Feature) error {
		f.SlackNotifications = &feature.SlackNotifications{Progress: feature.SlackOff}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	n.sweepDestinations()
	if got := counting.lists.Load(); got != initial+1 {
		t.Fatalf("Slack edit scans = %d; want %d", got, initial+1)
	}
}

func TestSlackPerFeatureTransitionAddMuteUnmuteRemove(t *testing.T) {
	global, extra := testRecipients()[0], testRecipients()[1]
	h := newNotifierHarness(t, defaultTestSettings(testToken, global))
	h.seedFeature("F-1", nil)
	h.seedFeature("F-2", nil)
	n := h.newNotifier(0)
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SignalReady()
	waitFor(t, time.Second, func() bool { return n.startupDone.Load() })
	n.DomainEventTap(startedEvent("F-1", feature.PhaseResearch))
	waitFor(t, time.Second, func() bool { return len(postsTo(h.server, "D-U-ADA")) >= 3 })
	existingPosts := len(postsTo(h.server, "D-U-ADA"))

	edit := func(fn func(*feature.SlackNotifications)) {
		t.Helper()
		if err := h.store.Modify("F-1", func(f *feature.Feature) error {
			if f.SlackNotifications == nil {
				f.SlackNotifications = &feature.SlackNotifications{}
			}
			fn(f.SlackNotifications)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		n.sweepDestinations()
	}
	edit(func(s *feature.SlackNotifications) {
		s.Recipients = []feature.SlackRecipient{{
			TypedText: extra.TypedText, Kind: string(extra.Kind),
			ID: extra.ID, DisplayName: extra.DisplayName,
		}}
	})
	if posts := postsTo(h.server, "C-ENG"); len(posts) != 1 {
		t.Fatalf("new destination posts = %d; want one root", len(posts))
	}
	if posts := postsTo(h.server, "D-U-ADA"); len(posts) != existingPosts {
		t.Fatalf("existing destination posts = %d; want %d", len(posts), existingPosts)
	}
	edit(func(s *feature.SlackNotifications) { s.Mode = feature.SlackMuted })
	waitFor(t, time.Second, func() bool {
		for _, request := range h.server.Requests("chat.update") {
			if strings.Contains(fieldString(request, "text"), "Updates are paused") {
				return true
			}
		}
		return false
	})
	updates := h.server.CallCount("chat.update")
	n.sweepDestinations()
	if h.server.CallCount("chat.update") != updates {
		t.Fatal("unchanged muted tick edited the card")
	}
	edit(func(s *feature.SlackNotifications) { s.Mode = feature.SlackInherit })
	waitFor(t, time.Second, func() bool { return h.server.CallCount("chat.update") > updates })
	edit(func(s *feature.SlackNotifications) { s.Recipients = nil })
	after := len(postsTo(h.server, "C-ENG"))
	n.DomainEventTap(ports.Event{Type: ports.PhaseStarted, FeatureID: "F-1"})
	waitFor(t, time.Second, func() bool { return n.queue.len() == 0 })
	if got := len(postsTo(h.server, "C-ENG")); got != after {
		t.Fatalf("removed destination posts = %d; want %d", got, after)
	}
}
