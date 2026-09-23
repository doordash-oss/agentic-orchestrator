package slack

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackPerFeatureDeliveryRecipientsAndOwner(t *testing.T) {
	defaultRecipient := testRecipients()[0]
	extra := testRecipients()[1]
	h := newNotifierHarness(t, defaultTestSettings(testToken, defaultRecipient))
	h.seedFeature("F-owner", func(f *feature.Feature) {
		f.SlackNotifications = &feature.SlackNotifications{Recipients: []feature.SlackRecipient{
			{TypedText: extra.TypedText, Kind: string(extra.Kind), ID: extra.ID, DisplayName: extra.DisplayName},
		}}
	})
	h.seedFeature("F-child", func(f *feature.Feature) {
		f.Parent = &feature.ChildRelationship{ParentID: "F-owner", Kind: feature.ChildKindRefactor}
	})
	h.seedFeature("F-other", nil)
	h.start(0)
	h.feed(startedEvent("F-child", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(h.server, "C-ENG")) == 2 && len(postsTo(h.server, "D-U-ADA")) == 2
	})
	h.feed(startedEvent("F-other", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool { return len(postsTo(h.server, "D-U-ADA")) == 4 })
	if got := len(postsTo(h.server, "C-ENG")); got != 2 {
		t.Fatalf("extra destination posts = %d; want owner only", got)
	}
}

func TestSlackPerFeatureDeliveryOnlyFeatureRecipientAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name        string
		section     feature.SlackNotifications
		wantPosts   int
		wantUpdates int
	}{
		{"progress off", feature.SlackNotifications{Progress: feature.SlackOff}, 2, 1},
		{"problems off", feature.SlackNotifications{Problems: feature.SlackOff}, 2, 1},
		{"muted", feature.SlackNotifications{Mode: feature.SlackMuted}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newNotifierHarness(t, defaultTestSettings(testToken))
			extra := testRecipients()[1]
			tc.section.Recipients = []feature.SlackRecipient{
				{TypedText: extra.TypedText, Kind: string(extra.Kind), ID: extra.ID, DisplayName: extra.DisplayName},
			}
			f := h.seedFeature("F-1", func(f *feature.Feature) { f.SlackNotifications = &tc.section })
			h.start(0)
			started := startedEvent("F-1", feature.PhaseResearch)
			h.feed(started)
			failure := errcat.New(errcat.InternalError)
			failed := ports.Event{Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &failure}
			h.feed(failed)
			waitFor(t, 10*time.Second, func() bool { return h.notifier.queue.len() == 0 })
			if tc.wantPosts > 0 {
				waitFor(t, 10*time.Second, func() bool {
					return len(postsTo(h.server, "C-ENG")) == tc.wantPosts &&
						len(h.server.Requests("chat.update")) >= tc.wantUpdates
				})
			}
			if got := len(postsTo(h.server, "C-ENG")); got != tc.wantPosts {
				t.Fatalf("posts = %d, want %d", got, tc.wantPosts)
			}
			if tc.wantPosts == 2 {
				want := replyForEvent(testToken, failed, f).fallback
				if tc.name == "problems off" {
					want = replyForEvent(testToken, started, f).fallback
				}
				if got := fieldString(postsTo(h.server, "C-ENG")[1], "text"); got != want {
					t.Fatalf("thread reply = %q; want %q", got, want)
				}
			}
			if tc.name == "muted" && len(h.server.AllRequests()) != 0 {
				t.Fatalf("muted requests = %#v", h.server.AllRequests())
			}
		})
	}
}

func TestSlackPerFeatureWarningsFollowRecipients(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings(testToken))
	extra := testRecipients()[1]
	h.seedFeature("F-1", func(f *feature.Feature) {
		f.SlackNotifications = &feature.SlackNotifications{Recipients: []feature.SlackRecipient{
			{TypedText: extra.TypedText, Kind: string(extra.Kind), ID: extra.ID, DisplayName: extra.DisplayName},
		}}
	})
	n := h.newNotifier(0)
	defer n.Stop(context.Background())
	record, err := n.recordFor("F-1")
	if err != nil {
		t.Fatal(err)
	}
	n.recordMu.Lock()
	record.Destinations[destinationKey("channel", "C-ENG")] = destinationRecord{
		Kind: "channel", SlackID: "C-ENG", DisplayName: "#eng",
		Failure: &destinationFailure{
			Code: errcat.SlackRecipientNotNotified, SlackError: "is_archived",
			Count: 5, FirstFailedAt: h.clock.Now(),
		},
	}
	n.recordMu.Unlock()
	if warnings := n.SlackWarnings("F-1"); len(warnings) != 1 || !strings.Contains(warnings[0].Summary, "#eng") {
		t.Fatalf("feature warning = %#v", warnings)
	}
	f, err := h.store.Load("F-1")
	if err != nil {
		t.Fatal(err)
	}
	f.SlackNotifications.Recipients = nil
	if err := h.store.Save(f); err != nil {
		t.Fatal(err)
	}
	if warnings := n.SlackWarnings("F-1"); len(warnings) != 0 {
		t.Fatalf("removed recipient warning = %#v", warnings)
	}
}

func TestSlackPerFeatureEffectiveSettingsKeepsTokenAndDeduplicates(t *testing.T) {
	global := testRecipients()[0]
	h := newNotifierHarness(t, defaultTestSettings(testToken, global))
	n := h.newNotifier(0)
	defer n.Stop(context.Background())
	owner := &feature.Feature{SlackNotifications: &feature.SlackNotifications{
		Progress: feature.SlackOff, NeedsInput: feature.SlackOff,
		Recipients: []feature.SlackRecipient{
			{Kind: "user", ID: global.ID, DisplayName: "duplicate"},
			{Kind: "channel", ID: "C-EXTRA", DisplayName: "#extra"},
		},
	}}
	settings, effective := n.effectiveSettings(h.settings.SlackSettings(), owner)
	if settings.Token != testToken || len(settings.Recipients) != 2 || settings.Recipients[0].DisplayName != global.DisplayName ||
		settings.Categories.Progress || settings.Categories.NeedsInput || !settings.Categories.Problems ||
		len(effective.Recipients) != 2 {
		t.Fatalf("settings = %#v, effective = %#v", settings, effective)
	}
}

func TestSlackPerFeatureNeedsInputOffShowsCountsWithoutPosting(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	h.seedFeature("F-1", func(f *feature.Feature) {
		f.SlackNotifications = &feature.SlackNotifications{NeedsInput: feature.SlackOff}
	})
	h.pending.set("F-1", ports.SlackPendingInput{
		FeatureID: "F-1", Kind: ports.SlackPendingPermission,
		RequestID: "permission-1", WaitingSince: h.clock.Now(),
	})
	h.start(0)
	h.feed(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool { return len(postsTo(h.server, "C-ENG")) == 1 })
	waitFor(t, 10*time.Second, func() bool { return len(h.server.Requests("chat.update")) > 0 })
	post := postsTo(h.server, "C-ENG")[0]
	if text := fieldString(post, "text"); !strings.Contains(text, "1 permission") ||
		!strings.Contains(text, "Needs input replies are off") || strings.Contains(text, "#1 permission") {
		t.Fatalf("root card waiting line = %q", text)
	}
	if got := len(postsTo(h.server, "C-ENG")); got != 1 {
		t.Fatalf("posts = %d; want only root card", got)
	}
}

func TestSlackPerFeatureDMOpenAbandonsRemovedRecipient(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings(testToken))
	recipient := testRecipients()[0]
	h.seedFeature("F-1", func(f *feature.Feature) {
		f.SlackNotifications = &feature.SlackNotifications{Recipients: []feature.SlackRecipient{
			{TypedText: recipient.TypedText, Kind: string(recipient.Kind), ID: recipient.ID, DisplayName: recipient.DisplayName},
		}}
	})
	n := h.newNotifier(0)
	defer n.Stop(context.Background())
	record, err := n.recordFor("F-1")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		if release != nil {
			close(release)
		}
	}()
	h.server.Script("conversations.open", testsupport.Response{
		Body:    map[string]any{"ok": true, "channel": map[string]any{"id": "D-U-ADA"}},
		Started: started, Release: release,
	})
	result := make(chan error, 1)
	go func() {
		_, err := n.resolveDestination(h.settings.SlackSettings(), record, "F-1", recipient)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("DM open did not start")
	}
	f, err := h.store.Load("F-1")
	if err != nil {
		t.Fatal(err)
	}
	f.SlackNotifications.Recipients = nil
	if err := h.store.Save(f); err != nil {
		t.Fatal(err)
	}
	close(release)
	release = nil
	select {
	case err := <-result:
		if err != errDeliveryIneligible {
			t.Fatalf("DM open error = %v; want ineligible", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("DM open did not finish")
	}
	n.recordMu.Lock()
	_, recorded := record.Destinations[destinationKey("user", recipient.ID)]
	n.recordMu.Unlock()
	if recorded {
		t.Fatal("removed DM destination was recorded")
	}
}

func TestSlackPerFeatureDMOpenRecordsActiveFeatureRecipient(t *testing.T) {
	h := newNotifierHarness(t, defaultTestSettings(testToken))
	recipient := testRecipients()[0]
	h.seedFeature("F-1", func(f *feature.Feature) {
		f.SlackNotifications = &feature.SlackNotifications{Recipients: []feature.SlackRecipient{
			{TypedText: recipient.TypedText, Kind: string(recipient.Kind), ID: recipient.ID, DisplayName: recipient.DisplayName},
		}}
	})
	n := h.newNotifier(0)
	defer n.Stop(context.Background())
	record, err := n.recordFor("F-1")
	if err != nil {
		t.Fatal(err)
	}
	channel, err := n.resolveDestination(h.settings.SlackSettings(), record, "F-1", recipient)
	if err != nil || channel != "D-U-ADA" {
		t.Fatalf("feature-only DM open = (%q, %v)", channel, err)
	}
	n.recordMu.Lock()
	entry := record.Destinations[destinationKey("user", recipient.ID)]
	n.recordMu.Unlock()
	if entry.ChannelID != channel || h.server.CallCount("conversations.open") != 1 {
		t.Fatalf("recorded destination = %#v; expected one DM open", entry)
	}
}

func TestSlackPerFeaturePauseCardEditsOnceAndKeepsFallback(t *testing.T) {
	fixture := newFailureFixture(t, "1758499200.000001")
	f, err := fixture.harness.store.Load("F-1")
	if err != nil {
		t.Fatal(err)
	}
	f.SlackNotifications = &feature.SlackNotifications{Mode: feature.SlackMuted}
	if err := fixture.harness.store.Save(f); err != nil {
		t.Fatal(err)
	}
	fixture.worker.flushOne("F-1")
	if got := len(fixture.harness.server.Requests("chat.update")); got != 0 {
		t.Fatalf("ordinary muted refreshes = %d", got)
	}
	fixture.worker.flushMutedCard("F-1")
	updates := fixture.harness.server.Requests("chat.update")
	if len(updates) != 1 || !strings.Contains(fieldString(updates[0], "text"), "Updates are paused") ||
		!strings.Contains(fieldString(updates[0], "blocks"), "Paused because this feature was muted") {
		t.Fatalf("paused card update = %#v", updates)
	}
}
