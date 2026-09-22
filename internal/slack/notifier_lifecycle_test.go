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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const testToken = "xoxb-sentinel-4242"

func startedEvent(featureID string, phase feature.Phase) ports.Event {
	return ports.Event{Type: ports.PhaseStarted, FeatureID: featureID, Phase: phase}
}

func completedEvent(featureID string, phase feature.Phase) ports.Event {
	return ports.Event{Type: ports.PhaseCompleted, FeatureID: featureID, Phase: phase}
}

func TestNotifierRootCardPerDefaultRecipient(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	_ = harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})

	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.postMessage")) == 2 &&
			len(harness.server.Requests("chat.update")) == 2
	})

	if got := len(harness.server.Requests("conversations.open")); got != 1 {
		t.Fatalf("conversations.open calls = %d; want one for the user", got)
	}
	open := harness.server.Requests("conversations.open")[0]
	if fieldString(open, "users") != "U-ADA" {
		t.Fatalf("conversations.open users = %q; want U-ADA", fieldString(open, "users"))
	}

	dmPosts := postsTo(harness.server, "D-U-ADA")
	channelPosts := postsTo(harness.server, "C-ENG")
	if len(dmPosts) != 1 || len(channelPosts) != 1 {
		t.Fatalf("root posts = %d dm, %d channel; want one each", len(dmPosts), len(channelPosts))
	}
	for _, post := range [][]testsupport.Request{dmPosts, channelPosts} {
		blocks := requestBlocks(post[0])
		if len(blocks) != 3 {
			t.Fatalf("root card blocks = %d; want header, section, context: %#v", len(blocks), blocks)
		}
		if blocks[0]["type"] != "header" {
			t.Fatalf("first block = %#v; want header", blocks[0])
		}
		if blocks[2]["type"] != "context" || !strings.Contains(blockText(blocks[2]), "<!date^") {
			t.Fatalf("context block = %#v; want a Slack date token", blocks[2])
		}
		fields := sectionFields(blocks[1])
		for _, want := range []string{
			"*Server:* Local agent", "*Pipeline:* Moonshot", "*Repositories:* alpha, beta",
			"*Phase:* Implementation", "*Status:* Implementing",
		} {
			if !strings.Contains(fields, want) {
				t.Fatalf("card fields %q missing %q", fields, want)
			}
		}
		if fieldString(post[0], "text") == "" {
			t.Fatal("root post is missing its plain-text fallback")
		}
	}

	destinations := recordDestinations(t, harness.stateDir, "F-1")
	if len(destinations) != 2 {
		t.Fatalf("record destinations = %#v; want user and channel", destinations)
	}
	userEntry, ok := destinations[destinationKey("user", "U-ADA")]
	if !ok || userEntry.ChannelID != "D-U-ADA" || userEntry.RootTS == "" ||
		len(userEntry.Ledger) != 1 || userEntry.Ledger[0] != userEntry.RootTS {
		t.Fatalf("user destination = %#v; want cached channel, root ts, one-entry ledger", userEntry)
	}
	channelEntry, ok := destinations[destinationKey("channel", "C-ENG")]
	if !ok || channelEntry.ChannelID != "C-ENG" || channelEntry.RootTS == "" || len(channelEntry.Ledger) != 1 {
		t.Fatalf("channel destination = %#v; want channel, root ts, one-entry ledger", channelEntry)
	}

	posted := harness.observer.ofKind("slack.root_card_updated")
	postedCount := 0
	for _, evt := range posted {
		if evt.Data["action"] == "posted" {
			postedCount++
			if evt.Data["destination_kind"] != "user" && evt.Data["destination_kind"] != "channel" {
				t.Fatalf("root card event destination_kind = %#v", evt.Data)
			}
		}
	}
	if postedCount != 2 {
		t.Fatalf("root card posted events = %d; want one per destination", postedCount)
	}
}

func blockText(block map[string]any) string {
	var sources []any
	if text, ok := block["text"]; ok {
		sources = append(sources, text)
	}
	if elements, ok := block["elements"]; ok {
		sources = append(sources, elements)
	}
	var parts []string
	for _, source := range sources {
		switch value := source.(type) {
		case string:
			parts = append(parts, value)
		case map[string]any:
			if text, ok := value["text"].(string); ok {
				parts = append(parts, text)
			}
		case []any:
			for _, element := range value {
				if m, ok := element.(map[string]any); ok {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

func sectionFields(block map[string]any) string {
	var fields []string
	switch value := block["fields"].(type) {
	case []any:
		for _, field := range value {
			if m, ok := field.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					fields = append(fields, text)
				}
			}
		}
	}
	return strings.Join(fields, "\n")
}

func TestNotifierRestartEditsSameCard(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	first := harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 2
	})
	first.Stop(context.Background())

	destinations := recordDestinations(t, harness.stateDir, "F-1")
	userEntry := destinations[destinationKey("user", "U-ADA")]
	channelEntry := destinations[destinationKey("channel", "C-ENG")]

	second := harness.newNotifier(0)
	second.Start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	harness.notifier = second
	harness.feed(startedEvent("F-1", feature.PhaseResearch))

	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 4
	})

	if got := len(harness.server.Requests("conversations.open")); got != 1 {
		t.Fatalf("conversations.open calls = %d; want the cached channel reused", got)
	}
	if got := len(harness.server.Requests("chat.postMessage")); got != 4 {
		t.Fatalf("chat.postMessage calls = %d; want two roots plus two replies, no second card", got)
	}
	updates := harness.server.Requests("chat.update")
	updateByChannel := map[string]testsupport.Request{}
	for _, update := range updates {
		updateByChannel[fieldString(update, "channel")] = update
	}
	if got := updateByChannel[userEntry.ChannelID]; fieldString(got, "ts") != userEntry.RootTS {
		t.Fatalf("restart update for the user = %#v; want the stored root timestamp %q",
			got.Fields, userEntry.RootTS)
	}
	if got := updateByChannel[channelEntry.ChannelID]; fieldString(got, "ts") != channelEntry.RootTS {
		t.Fatalf("restart update for the channel = %#v; want the stored root timestamp %q",
			got.Fields, channelEntry.RootTS)
	}
}

func TestNotifierChildEventsReportIntoParentThread(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.seedFeature("F-2", func(f *feature.Feature) {
		f.Parent = &feature.ChildRelationship{ParentID: "F-1", Kind: feature.ChildKindRefactor}
		f.ActiveTimingKey = "implement"
		f.PhaseTimings = map[string]time.Duration{"implement": 42 * time.Second}
		f.PhaseCosts = map[string]float64{"implement": 1.5}
	})
	harness.start(0)
	harness.feed(startedEvent("F-2", feature.PhaseImplement))
	harness.feed(completedEvent("F-2", feature.PhaseImplement))
	harness.feed(ports.Event{Type: ports.FeatureCompleted, FeatureID: "F-2"})

	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "D-U-ADA")) == 4 &&
			len(postsTo(harness.server, "C-ENG")) == 4 &&
			len(harness.server.Requests("chat.update")) >= 2
	})
	dmReplies := postsTo(harness.server, "D-U-ADA")
	channelReplies := postsTo(harness.server, "C-ENG")
	if len(dmReplies) != 4 || len(channelReplies) != 4 {
		t.Fatalf("child posts = %d dm, %d channel; want the parent card plus three lines each",
			len(dmReplies), len(channelReplies))
	}
	wantLines := []string{
		"♻️ Refactor: Implementation started",
		"♻️ Refactor: Implementation completed in 42s (cost $1.50)",
		"♻️ Refactor: pass completed in 42s (cost $1.50)",
	}
	for i, want := range wantLines {
		if got := fieldString(dmReplies[i+1], "text"); got != want {
			t.Fatalf("child reply %d = %q; want %q", i, got, want)
		}
	}
	parentRoot := recordDestinations(t, harness.stateDir, "F-1")[destinationKey("channel", "C-ENG")].RootTS
	for _, reply := range channelReplies[1:] {
		if fieldString(reply, "thread_ts") != parentRoot {
			t.Fatalf("child reply thread_ts = %q; want the parent's root %q",
				fieldString(reply, "thread_ts"), parentRoot)
		}
	}
	if recordExists(harness.stateDir, "F-2") {
		t.Fatal("child feature got a record of its own")
	}
}

func TestNotifierDiscardsEventsOutsideThePhaseSet(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 2 &&
			len(harness.observer.ofKind("slack.root_card_updated")) == 4
	})
	recordPath := harness.stateDir + "/F-1/" + recordFilename
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	requestsBefore := len(harness.server.AllRequests())
	eventsBefore := len(harness.observer.all())

	for _, ev := range []ports.Event{
		{Type: ports.FeatureCreated, FeatureID: "F-1"},
		{Type: ports.RelationshipChildCreated, FeatureID: "F-1", ParentID: "F-0", ChildID: "F-1"},
		{Type: ports.RelationshipIntegrationChanged, FeatureID: "F-1"},
		{Type: ports.RecoveryScanned, FeatureID: "F-1"},
		{Type: ports.RecoveryExecuted, FeatureID: "F-1"},
		{Type: ports.RuntimeShutdownStarted},
		{Type: ports.FeatureConfigChanged, FeatureID: "F-1"},
		{Type: ports.NeedUserInputRequired, FeatureID: "F-1"},
		{Type: ports.ReviewRequired, FeatureID: "F-1"},
		{Type: ports.SessionOutput, FeatureID: "F-1"},
	} {
		harness.feed(ev)
	}
	time.Sleep(50 * time.Millisecond)

	if got := len(harness.server.AllRequests()); got != requestsBefore {
		t.Fatalf("requests after discarded events = %d; want %d", got, requestsBefore)
	}
	after, err := os.ReadFile(recordPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("record changed after discarded events")
	}
	if got := len(harness.observer.all()); got != eventsBefore {
		t.Fatalf("observability events after discarded events = %d; want %d", got, eventsBefore)
	}
	if got := harness.notifier.queue.len(); got != 0 {
		t.Fatalf("queue length after discarded events = %d; want 0", got)
	}
}

func TestNotifierUnusableConfigurationMakesNoRequests(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ports.SlackRuntimeSettings)
	}{
		{"slack disabled", func(s *ports.SlackRuntimeSettings) { s.Enabled = false }},
		{"no token", func(s *ports.SlackRuntimeSettings) { s.Token = "" }},
		{"no recipients", func(s *ports.SlackRuntimeSettings) { s.Recipients = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := defaultTestSettings(testToken, testRecipients()...)
			tc.mutate(&settings)
			harness := newNotifierHarness(t, settings)
			harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
			harness.start(0)
			for _, ev := range []ports.Event{
				{Type: ports.FeatureStarted, FeatureID: "F-1"},
				startedEvent("F-1", feature.PhaseResearch),
				completedEvent("F-1", feature.PhaseResearch),
				{Type: ports.PublishStarted, FeatureID: "F-1"},
				{Type: ports.FeatureCompleted, FeatureID: "F-1"},
			} {
				harness.feed(ev)
			}
			time.Sleep(50 * time.Millisecond)
			if got := len(harness.server.AllRequests()); got != 0 {
				t.Fatalf("requests = %d; want zero for an unusable configuration", got)
			}
			if recordExists(harness.stateDir, "F-1") {
				t.Fatal("record file written for an unusable configuration")
			}
		})
	}
}

func TestNotifierSkipsFeatureThatCannotBeLoaded(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.start(0)
	harness.feed(startedEvent("F-missing", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.observer.ofKind("slack.event_dropped")) == 1
	})
	if got := len(harness.server.AllRequests()); got != 0 {
		t.Fatalf("requests = %d; want zero for an unloadable feature", got)
	}
}

func TestNotifierSkipsUnreadableSlackRecord(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	const malformed = "version: [not valid yaml"
	if err := os.WriteFile(recordPath(harness.stateDir, "F-1"), []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}

	harness.start(0)
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.observer.ofKind("slack.event_dropped")) == 1
	})

	if got := len(harness.server.AllRequests()); got != 0 {
		t.Fatalf("requests = %d; want zero for an unreadable Slack record", got)
	}
	data, err := os.ReadFile(recordPath(harness.stateDir, "F-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != malformed {
		t.Fatalf("Slack record = %q; want malformed record left unchanged", got)
	}
}

func TestNotifierProgressGateSilencesRepliesOnly(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()...)
	settings.Categories.Progress = false
	harness := newNotifierHarness(t, settings)
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	harness.feed(completedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 2
	})
	if got := len(harness.server.Requests("chat.postMessage")); got != 2 {
		t.Fatalf("posts = %d; want only the two root cards with Progress off", got)
	}
	for _, post := range harness.server.Requests("chat.postMessage") {
		if _, ok := post.Fields["thread_ts"]; ok {
			t.Fatalf("thread reply posted with Progress off: %#v", post.Fields)
		}
	}
	if got := len(harness.observer.ofKind("slack.message_posted")); got != 0 {
		t.Fatalf("message_posted events = %d; want zero with Progress off", got)
	}
}

func TestNotifierSlackDisabledMidRunStopsRequests(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 2
	})
	harness.settings.mutate(func(s *ports.SlackRuntimeSettings) { s.Enabled = false })
	requestsBefore := len(harness.server.AllRequests())
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	time.Sleep(50 * time.Millisecond)
	if got := len(harness.server.AllRequests()); got != requestsBefore {
		t.Fatalf("requests after disabling Slack = %d; want %d", got, requestsBefore)
	}
}

func TestNotifierErrorCompletionsRefreshAndPublishFailurePostsProblem(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 1
	})
	postsBefore := len(harness.server.Requests("chat.postMessage"))

	failure := errcat.New(errcat.InternalError)
	harness.feed(ports.Event{
		Type: ports.PhaseCompleted, FeatureID: "F-1",
		Phase: feature.PhaseResearch, Error: errString("boom"),
	})
	harness.feed(ports.Event{
		Type: ports.PublishCompleted, FeatureID: "F-1",
		CanonicalError: &failure,
	})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) >= 2 &&
			len(harness.server.Requests("chat.postMessage")) == postsBefore+1
	})
	if got := len(harness.server.Requests("chat.postMessage")); got != postsBefore+1 {
		t.Fatalf("posts = %d; want %d: phase error is refresh-only and publish error posts one Problem",
			got, postsBefore+1)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestNotifierFeatureAdvancedPostsNoSecondLine(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	harness.feed(ports.Event{Type: ports.FeatureAdvanced, FeatureID: "F-1", Phase: feature.PhaseResearch})
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})
	time.Sleep(50 * time.Millisecond)
	if got := len(postsTo(harness.server, "C-ENG")); got != 2 {
		t.Fatalf("posts = %d; want root plus one reply only", got)
	}
}

func TestNotifierCoalescesCardUpdates(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	// Hold the first card update at the fake so the whole burst is
	// dispatched and marked dirty before the worker can flush again: the
	// injectable clock makes the one-second trailing edge free in real
	// time, so an unheld worker could coalesce across two flushes.
	updateStarted := make(chan struct{})
	updateRelease := make(chan struct{})
	harness.server.Script("chat.update", testsupport.Response{
		Body:    map[string]any{"ok": true},
		Started: updateStarted,
		Release: updateRelease,
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	select {
	case <-updateStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the first card update never arrived")
	}

	for i := 0; i < 10; i++ {
		harness.feed(ports.Event{Type: ports.FeatureAdvanced, FeatureID: "F-1", Phase: feature.PhaseImplement})
	}
	close(updateRelease)
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == 2
	})
	time.Sleep(50 * time.Millisecond)
	if got := len(harness.server.Requests("chat.update")); got != 2 {
		t.Fatalf("chat.update calls = %d; want exactly two for the burst", got)
	}

	// The edited event trails the update's response, so poll for it.
	var editedTimes []time.Time
	waitFor(t, 10*time.Second, func() bool {
		editedTimes = nil
		for _, evt := range harness.observer.ofKind("slack.root_card_updated") {
			if evt.Data["action"] == "edited" {
				editedTimes = append(editedTimes, evt.Timestamp)
			}
		}
		return len(editedTimes) == 2
	})
	if len(editedTimes) != 2 {
		t.Fatalf("edited events = %d; want two", len(editedTimes))
	}
	if gap := editedTimes[1].Sub(editedTimes[0]); gap < time.Second {
		t.Fatalf("coalesced updates %s apart; want at least one second", gap)
	}
}
