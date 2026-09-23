package slack

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

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
