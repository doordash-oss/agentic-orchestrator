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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

// recordingResponder captures the timestamps the fake returns per post so
// ledger assertions can compare against what Slack actually accepted.
type recordingResponder struct {
	mu    sync.Mutex
	ts    []string
	inner func(method string, request testsupport.Request) testsupport.Response
}

func (r *recordingResponder) respond(method string, request testsupport.Request) testsupport.Response {
	response := r.inner(method, request)
	if method == "chat.postMessage" {
		r.mu.Lock()
		if body, ok := response.Body.(map[string]any); ok {
			if ts, ok := body["ts"].(string); ok {
				r.ts = append(r.ts, ts)
			}
		}
		r.mu.Unlock()
	}
	return response
}

func (r *recordingResponder) timestamps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ts...)
}

func newRecordingHarness(t *testing.T, settings ports.SlackRuntimeSettings) (*notifierHarness, *recordingResponder) {
	t.Helper()
	harness := newNotifierHarness(t, settings)
	inner, _ := defaultOKResponder()
	recorder := &recordingResponder{inner: inner}
	harness.server.SetDefault(recorder.respond)
	return harness, recorder
}

func TestNotifierFullLifecycleLinesAndLedger(t *testing.T) {
	for _, token := range []string{"xoxb-sentinel-4242", "xoxp-sentinel-4242"} {
		t.Run("token family "+token[:4], func(t *testing.T) {
			harness, recorder := newRecordingHarness(t,
				defaultTestSettings(token, testRecipients()[1]))
			harness.seedFeature("F-1", func(f *feature.Feature) {
				withRoadmap(f)
				f.PhaseTimings = map[string]time.Duration{
					"research":     61 * time.Second,
					"design":       59 * time.Second,
					"phase-1-plan": 130 * time.Second,
					"phase-1-impl": 190 * time.Second,
					"phase-2-plan": 100 * time.Second,
					"phase-2-impl": 200 * time.Second,
				}
				f.PhaseCosts = map[string]float64{
					"research":     0.75,
					"design":       1.00,
					"phase-1-plan": 1.24,
					"phase-1-impl": 2.16,
					"phase-2-plan": 0.90,
					"phase-2-impl": 1.10,
				}
				f.RepoStates = map[string]*feature.RepoState{
					"alpha": {PRURL: "https://github.com/org/alpha/pull/7"},
					"beta":  {PRURL: "https://github.com/org/beta/pull/9"},
				}
			})
			harness.start(0)

			feeds := []struct {
				event   ports.Event
				prepare func()
			}{
				{event: ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"}},
				{event: startedEvent("F-1", feature.PhaseResearch)},
				{event: completedEvent("F-1", feature.PhaseResearch)},
				{event: startedEvent("F-1", feature.PhaseDesign)},
				{event: completedEvent("F-1", feature.PhaseDesign)},
				{event: startedEvent("F-1", feature.PhasePlan)},
				{event: completedEvent("F-1", feature.PhasePlan)},
				{event: startedEvent("F-1", feature.PhaseImplement)},
				{event: completedEvent("F-1", feature.PhaseImplement)},
				{event: startedEvent("F-1", feature.PhasePlan), prepare: func() {
					bumpRoadmapPhase(t, harness, 2)
				}},
				{event: completedEvent("F-1", feature.PhasePlan)},
				{event: startedEvent("F-1", feature.PhaseImplement)},
				{event: completedEvent("F-1", feature.PhaseImplement)},
				{event: ports.Event{Type: ports.PublishStarted, FeatureID: "F-1"}},
				{event: ports.Event{Type: ports.PublishCompleted, FeatureID: "F-1"}},
				{event: ports.Event{Type: ports.FeatureCompleted, FeatureID: "F-1"}},
			}
			for i, feed := range feeds {
				if feed.prepare != nil {
					feed.prepare()
				}
				harness.feed(feed.event)
				want := i + 1
				waitFor(t, 10*time.Second, func() bool {
					return len(postsTo(harness.server, "C-ENG")) >= want
				})
			}

			posts := postsTo(harness.server, "C-ENG")
			if len(posts) != 16 {
				t.Fatalf("posts = %d; want the root card plus fifteen lines", len(posts))
			}
			rootTS := fieldString(posts[0], "ts")
			if rootTS == "" {
				rootTS = recorder.timestamps()[0]
			}
			wantLines := []string{
				"🔍 Research started",
				"🔍 Research completed in 1m 1s (cost $0.75)",
				"🎨 Design started",
				"🎨 Design completed in 59s (cost $1.00)",
				"🗺️ Plan started (roadmap phase 1 of 2)",
				"🗺️ Plan completed in 2m 10s (cost $1.24)",
				"🔧 Implementation started",
				"🔧 Roadmap phase 1 of 2 complete in 5m 20s (cost $3.40)",
				"🗺️ Plan started (roadmap phase 2 of 2)",
				"🗺️ Plan completed in 1m 40s (cost $0.90)",
				"🔧 Implementation started",
				"🔧 Roadmap phase 2 of 2 complete in 5m 0s (cost $2.00)",
				"🚢 Publishing started",
				"🚢 Published — <https://github.com/org/alpha/pull/7|alpha> <https://github.com/org/beta/pull/9|beta>",
				"🎉 Feature completed in 12m 20s (cost $7.15)",
			}
			for i, want := range wantLines {
				if got := fieldString(posts[i+1], "text"); got != want {
					t.Fatalf("line %d = %q; want %q", i+1, got, want)
				}
			}
			for _, reply := range posts[1:] {
				if fieldString(reply, "thread_ts") != rootTS {
					t.Fatalf("reply thread_ts = %q; want the root %q",
						fieldString(reply, "thread_ts"), rootTS)
				}
				if _, ok := reply.Fields["reply_broadcast"]; ok {
					t.Fatal("progress reply set reply_broadcast")
				}
			}

			// The record write trails the server's acceptance of a post, so
			// poll until the durable ledger holds every accepted timestamp:
			// chat.update responses never enter the recorder, so an exact
			// match also proves card edits add no ledger entry.
			waitFor(t, 10*time.Second, func() bool {
				ledger, ok := recordLedger(harness.stateDir, "F-1", destinationKey("channel", "C-ENG"))
				return ok && strings.Join(ledger, ",") == strings.Join(recorder.timestamps(), ",")
			})
			if got := len(harness.server.Requests("chat.update")); got == 0 {
				t.Fatal("card was never edited across the lifecycle")
			}
		})
	}
}

func bumpRoadmapPhase(t *testing.T, harness *notifierHarness, phase int) {
	t.Helper()
	if err := harness.store.Modify("F-1", func(f *feature.Feature) error {
		f.CurrentRoadmapPhase = phase
		f.Status = feature.StatusPlanning
		f.CurrentPhase = feature.PhasePlan
		return nil
	}); err != nil {
		t.Fatalf("bump roadmap phase: %v", err)
	}
}

func TestNotifierSentinelTokenNeverLeaks(t *testing.T) {
	logs := captureLogs(t)
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.server.Script("chat.postMessage",
		testsupport.Response{Body: map[string]any{
			"ok": true, "ts": "1758499200.000001", "channel": "C-ENG",
			"warning": testToken,
		}},
		testsupport.Response{Body: map[string]any{"ok": false, "error": testToken}},
		testsupport.Response{Body: map[string]any{
			"ok": true, "ts": "1758499200.000003", "channel": "C-ENG",
		}},
	)
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	harness.feed(completedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})
	time.Sleep(50 * time.Millisecond)

	for _, request := range harness.server.AllRequests() {
		for key, value := range request.Fields {
			if strings.Contains(fmtValue(value), testToken) {
				t.Fatalf("posted form field %q leaked the token", key)
			}
		}
	}
	recordPath := filepath.Join(harness.stateDir, "F-1", recordFilename)
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testToken) {
		t.Fatal("record file leaked the token")
	}
	for _, evt := range harness.observer.all() {
		encoded, err := yaml.Marshal(evt)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), testToken) {
			t.Fatalf("observability event %s leaked the token", evt.EventType)
		}
	}
	if strings.Contains(logs.String(), testToken) {
		t.Fatalf("captured logs leaked the token:\n%s", logs.String())
	}
}

func fmtValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, fmtValue(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func TestNotifierPersistFailureRetainsAndRetries(t *testing.T) {
	logs := captureLogs(t)
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return recordExists(harness.stateDir, "F-1")
	})

	recordPath := filepath.Join(harness.stateDir, "F-1", recordFilename)
	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(recordPath, 0o755); err != nil {
		t.Fatal(err)
	}

	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})
	// The persist failure trails the server's acceptance, so poll the log.
	waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(logs.String(), "persisting the Slack record failed")
	})
	if strings.Contains(logs.String(), testToken) {
		t.Fatal("persist failure log leaked the token")
	}

	harness.feed(completedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 3
	})
	if got := len(postsTo(harness.server, "C-ENG")); got != 3 {
		t.Fatalf("posts = %d; want the reply delivered after the failed persist", got)
	}

	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Categories.Progress = false
	})
	updatesBefore := len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{Type: ports.PublishStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) > updatesBefore
	})
	if got := len(postsTo(harness.server, "C-ENG")); got != 3 {
		t.Fatalf("posts = %d; want no Progress reply from the update-only recovery event", got)
	}
	// The next eligible lifecycle change must retry persistence even when
	// Progress is off and the event only edits the root card.
	waitFor(t, 10*time.Second, func() bool {
		ledger, ok := recordLedger(harness.stateDir, "F-1", destinationKey("channel", "C-ENG"))
		return ok && len(ledger) == 3
	})
	destinations := recordDestinations(t, harness.stateDir, "F-1")
	entry := destinations[destinationKey("channel", "C-ENG")]
	if got := len(entry.Ledger); got != 3 {
		t.Fatalf("persisted ledger = %d entries; want every timestamp accepted while writes were failing", got)
	}
	if entry.RootTS == "" {
		t.Fatal("re-persisted record lost the root timestamp")
	}

	postsBeforeRestart := len(postsTo(harness.server, "C-ENG"))
	first := harness.notifier
	first.Stop(context.Background())
	second := harness.newNotifier(0)
	second.Start()
	t.Cleanup(func() { second.Stop(context.Background()) })
	harness.notifier = second
	second.SignalReady()
	waitFor(t, time.Second, func() bool { return second.startupDone.Load() })
	updatesBefore = len(harness.server.Requests("chat.update"))
	harness.feed(ports.Event{Type: ports.FeatureCompleted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) > updatesBefore
	})
	if got := len(postsTo(harness.server, "C-ENG")); got != postsBeforeRestart {
		t.Fatalf("posts after restart = %d; want %d with the durable root reused", got, postsBeforeRestart)
	}
	update := harness.server.Requests("chat.update")[updatesBefore]
	if got := fieldString(update, "ts"); got != entry.RootTS {
		t.Fatalf("restart update root ts = %q; want persisted %q", got, entry.RootTS)
	}
}

func TestNotifierPacingSpacesWritesPerDestination(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	inner, _ := defaultOKResponder()
	var stampsMu sync.Mutex
	stamps := map[string][]time.Time{}
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method == "chat.postMessage" || method == "chat.update" {
			channel := fieldString(request, "channel")
			stampsMu.Lock()
			stamps[channel] = append(stamps[channel], harness.clock.Now())
			stampsMu.Unlock()
		}
		return inner(method, request)
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	for _, phase := range []feature.Phase{
		feature.PhaseResearch, feature.PhaseDesign, feature.PhaseInquire,
		feature.PhaseKnowledgeBase, feature.PhaseFinalReview,
	} {
		harness.feed(startedEvent("F-1", phase))
	}
	waitFor(t, 10*time.Second, func() bool {
		stampsMu.Lock()
		defer stampsMu.Unlock()
		return len(stamps["D-U-ADA"]) >= 7 && len(stamps["C-ENG"]) >= 7
	})

	stampsMu.Lock()
	dmStamps := append([]time.Time(nil), stamps["D-U-ADA"]...)
	channelStamps := append([]time.Time(nil), stamps["C-ENG"]...)
	stampsMu.Unlock()
	for channel, writes := range map[string][]time.Time{
		"D-U-ADA": dmStamps,
		"C-ENG":   channelStamps,
	} {
		for i := 1; i < len(writes); i++ {
			if gap := writes[i].Sub(writes[i-1]); gap < time.Second {
				t.Fatalf("destination %s writes %d and %d only %s apart; pacing must space posts and updates alike",
					channel, i-1, i, gap)
			}
		}
	}
	if first := dmStamps[0].Sub(channelStamps[0]); first >= time.Second {
		t.Fatalf("second destination waited %s for its first write; workers pace independently", first)
	}
}

func TestNotifierRateLimitPauseIsPerDestination(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	inner, _ := defaultOKResponder()
	var stampsMu sync.Mutex
	stamps := map[string][]time.Time{}
	channelStarted := make(chan struct{})
	channelRelease := make(chan struct{})
	var channelHeld sync.Once
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method == "chat.postMessage" {
			channel := fieldString(request, "channel")
			stampsMu.Lock()
			stamps[channel] = append(stamps[channel], harness.clock.Now())
			stampsMu.Unlock()
			if channel == "C-ENG" {
				var held bool
				channelHeld.Do(func() { held = true })
				if held {
					// Hold the throttled response until the direct
					// message's post has been served, so the two
					// workers' order is deterministic.
					return testsupport.Response{
						Status:  http.StatusTooManyRequests,
						Headers: http.Header{"Retry-After": []string{"3"}},
						Body:    map[string]any{"ok": false},
						Started: channelStarted,
						Release: channelRelease,
					}
				}
			}
		}
		return inner(method, request)
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})

	select {
	case <-channelStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("channel destination never reached its throttled request")
	}
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "D-U-ADA")) == 1
	})
	stampsMu.Lock()
	dmAt := stamps["D-U-ADA"][0]
	stampsMu.Unlock()
	close(channelRelease)
	waitFor(t, 10*time.Second, func() bool {
		return len(postsTo(harness.server, "C-ENG")) == 2
	})

	stampsMu.Lock()
	channelWrites := append([]time.Time(nil), stamps["C-ENG"]...)
	stampsMu.Unlock()
	if len(channelWrites) != 2 {
		t.Fatalf("channel writes = %d; want the throttled request plus its successful retry", len(channelWrites))
	}
	if wait := channelWrites[1].Sub(channelWrites[0]); wait < 3*time.Second {
		t.Fatalf("channel retried only %s after the 429; want the returned three-second wait", wait)
	}
	if delayed := channelWrites[0].Sub(dmAt); delayed > time.Second {
		t.Fatalf("channel's first request was delayed %s behind the direct message; destinations pace independently", delayed)
	}
}

func TestNotifierRateLimitExhaustionPreservesFinalPause(t *testing.T) {
	logs := captureLogs(t)
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	inner, _ := defaultOKResponder()
	var attemptsMu sync.Mutex
	var attempts []time.Time
	var requestCount atomic.Int64
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method != "chat.postMessage" {
			return inner(method, request)
		}
		attemptsMu.Lock()
		attempts = append(attempts, harness.clock.Now())
		attemptsMu.Unlock()
		if requestCount.Add(1) <= 4 {
			return testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Headers: http.Header{"Retry-After": []string{"5"}},
				Body:    map[string]any{"ok": false},
			}
		}
		return inner(method, request)
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	harness.feed(startedEvent("F-1", feature.PhaseResearch))

	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.postMessage")) >= 5 &&
			strings.Contains(logs.String(), "after 3 retries")
	})
	attemptsMu.Lock()
	got := append([]time.Time(nil), attempts...)
	attemptsMu.Unlock()
	if len(got) < 5 {
		t.Fatalf("post attempts = %d; want four exhausted 429s and the next queued write", len(got))
	}
	if pause := got[4].Sub(got[3]); pause < 5*time.Second {
		t.Fatalf("next write followed the exhausted 429 after %s; want the final five-second pause", pause)
	}
}

func TestNotifierRetriesTransientFailures(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	var stampsMu sync.Mutex
	attempts := []time.Time{}
	var requestCount atomic.Int64
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		if method != "chat.postMessage" {
			inner, _ := defaultOKResponder()
			return inner(method, request)
		}
		stampsMu.Lock()
		attempts = append(attempts, harness.clock.Now())
		stampsMu.Unlock()
		n := requestCount.Add(1)
		switch {
		case n <= 3, n >= 5 && n <= 8:
			return testsupport.Response{
				Status: http.StatusInternalServerError,
				Body:   map[string]any{"ok": false},
			}
		default:
			inner, _ := defaultOKResponder()
			return inner(method, request)
		}
	})
	harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.postMessage")) == 4
	})
	if got := len(harness.server.Requests("chat.postMessage")); got != 4 {
		t.Fatalf("post attempts = %d; want delivery on the fourth attempt", got)
	}
	stampsMu.Lock()
	first := append([]time.Time(nil), attempts[:4]...)
	stampsMu.Unlock()
	gaps := []time.Duration{
		first[1].Sub(first[0]),
		first[2].Sub(first[1]),
		first[3].Sub(first[2]),
	}
	for i, gap := range gaps {
		lower := time.Duration(float64(500*time.Millisecond<<(i)) * 0.8)
		upper := time.Duration(float64(500*time.Millisecond<<(i)) * 1.2)
		if gap < lower || gap >= upper {
			t.Fatalf("backoff gap %d = %s; want jittered exponential growth in [%s,%s)",
				i, gap, lower, upper)
		}
	}
	if gaps[1] <= gaps[0] || gaps[2] <= gaps[1] {
		t.Fatalf("backoff gaps %v do not grow and vary", gaps)
	}

	// Four consecutive 500s drop the item after exactly three retries,
	// log the exhaustion, and the destination moves on to the next item.
	logs2 := captureLogs(t)
	harness.server.Script("chat.postMessage",
		testsupport.Response{Status: http.StatusInternalServerError, Body: map[string]any{"ok": false}},
		testsupport.Response{Status: http.StatusInternalServerError, Body: map[string]any{"ok": false}},
		testsupport.Response{Status: http.StatusInternalServerError, Body: map[string]any{"ok": false}},
		testsupport.Response{Status: http.StatusInternalServerError, Body: map[string]any{"ok": false}},
	)
	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	waitFor(t, 10*time.Second, func() bool {
		return len(harness.server.Requests("chat.postMessage")) == 8 &&
			strings.Contains(logs2.String(), "after 3 retries")
	})
	if !strings.Contains(logs2.String(), "after 3 retries") {
		t.Fatalf("exhaustion was not logged:\n%s", logs2.String())
	}
	if !strings.Contains(logs2.String(), "channel") || strings.Contains(logs2.String(), testToken) {
		t.Fatalf("exhaustion log must name the destination kind and never the token:\n%s", logs2.String())
	}
}

func TestNotifierPermanentSlackErrorsDropWithoutRetry(t *testing.T) {
	for _, code := range []string{"channel_not_found", "invalid_auth"} {
		t.Run(code, func(t *testing.T) {
			logs := captureLogs(t)
			harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
			harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
			harness.server.Script("chat.postMessage",
				testsupport.Response{Body: map[string]any{"ok": false, "error": code}},
			)
			harness.start(0)
			harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
			waitFor(t, 10*time.Second, func() bool {
				return len(harness.server.Requests("chat.postMessage")) == 1
			})
			time.Sleep(100 * time.Millisecond)
			if got := len(harness.server.Requests("chat.postMessage")); got != 1 {
				t.Fatalf("post attempts = %d; want no retry for a permanent Slack error", got)
			}
			if !strings.Contains(logs.String(), code) || !strings.Contains(logs.String(), "channel") {
				t.Fatalf("drop was not logged with the Slack error code and destination kind:\n%s", logs.String())
			}
			if strings.Contains(logs.String(), testToken) {
				t.Fatal("drop log leaked the token")
			}

			harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
			waitFor(t, 10*time.Second, func() bool {
				return len(harness.server.Requests("chat.postMessage")) == 2
			})
		})
	}
}

func TestNotifierQueueOverflowPolicy(t *testing.T) {
	t.Run("progress drops when full and tap never waits", func(t *testing.T) {
		harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
		harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
		notifier := harness.newNotifier(3)
		harness.notifier = notifier

		for _, phase := range []feature.Phase{
			feature.PhaseResearch, feature.PhaseDesign, feature.PhaseInquire,
		} {
			harness.feed(startedEvent("F-1", phase))
		}
		if got := notifier.queue.len(); got != 3 {
			t.Fatalf("queued items = %d; want the capacity of three", got)
		}
		start := time.Now()
		harness.feed(startedEvent("F-1", feature.PhaseKnowledgeBase))
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("tap waited %s for the worker; enqueue must not block", elapsed)
		}
		waitFor(t, 10*time.Second, func() bool {
			return len(harness.observer.ofKind("slack.event_dropped")) == 1
		})
		dropped := harness.observer.ofKind("slack.event_dropped")
		if len(dropped) != 1 || dropped[0].Data["event_type"] != "phase.started" ||
			dropped[0].Data["reason"] != "queue_overflow" {
			t.Fatalf("overflow events = %#v; want one naming the dropped phase.started", dropped)
		}

		notifier.Start()
		t.Cleanup(func() { notifier.Stop(context.Background()) })
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == 4
		})
		wants := []string{
			"🔍 Research started",
			"🎨 Design started",
			"❓ Inquire started",
		}
		posts := postsTo(harness.server, "C-ENG")
		if len(posts) != 4 {
			t.Fatalf("posts = %d; want the root card plus three replies", len(posts))
		}
		for i, want := range wants {
			if fieldString(posts[i+1], "text") != want {
				t.Fatalf("delivered order %d = %q; want %q", i+1, fieldString(posts[i+1], "text"), want)
			}
		}
	})

	t.Run("protected evicts the oldest progress item", func(t *testing.T) {
		harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
		harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
		notifier := harness.newNotifier(3)
		harness.notifier = notifier

		for _, phase := range []feature.Phase{
			feature.PhaseResearch, feature.PhaseDesign, feature.PhaseInquire,
		} {
			harness.feed(startedEvent("F-1", phase))
		}
		notifier.queue.enqueue(queueItem{
			kind:  kindNeedsInput,
			event: startedEvent("F-1", feature.PhaseKnowledgeBase),
		})
		if got := notifier.queue.len(); got != 3 {
			t.Fatalf("queued items = %d; want the evicted progress replaced by the protected item", got)
		}
		waitFor(t, 10*time.Second, func() bool {
			return len(harness.observer.ofKind("slack.event_dropped")) == 1
		})
		dropped := harness.observer.ofKind("slack.event_dropped")
		if len(dropped) != 1 || dropped[0].Data["event_type"] != "phase.started" {
			t.Fatalf("overflow events = %#v; want exactly one naming the evicted progress event", dropped)
		}

		notifier.Start()
		t.Cleanup(func() { notifier.Stop(context.Background()) })
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == 4
		})
		wants := []string{
			"🎨 Design started",
			"❓ Inquire started",
			"📚 Knowledge Base started",
		}
		posts := postsTo(harness.server, "C-ENG")
		for i, want := range wants {
			if i+1 >= len(posts) || fieldString(posts[i+1], "text") != want {
				t.Fatalf("delivered order %d = %q; want %q", i+1, fieldString(posts[i+1], "text"), want)
			}
		}
	})

	t.Run("protected never drops and progress never evicts it", func(t *testing.T) {
		harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
		harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
		notifier := harness.newNotifier(3)
		harness.notifier = notifier

		for i := 0; i < 4; i++ {
			notifier.queue.enqueue(queueItem{
				kind:  kindProblems,
				event: startedEvent("F-1", feature.Phase(i)),
			})
		}
		if got := notifier.queue.len(); got != 4 {
			t.Fatalf("queued items = %d; want the protected item accepted over the bound", got)
		}
		if got := len(harness.observer.ofKind("slack.event_dropped")); got != 0 {
			t.Fatalf("overflow events = %d; want none for protected-only items", got)
		}
		notifier.queue.enqueue(queueItem{
			kind:  kindProgress,
			event: startedEvent("F-1", feature.PhaseKnowledgeBase),
		})
		if got := notifier.queue.len(); got != 4 {
			t.Fatalf("queued items = %d; want the progress item dropped against protected-only queue", got)
		}
		waitFor(t, 10*time.Second, func() bool {
			return len(harness.observer.ofKind("slack.event_dropped")) == 1
		})
		dropped := harness.observer.ofKind("slack.event_dropped")
		if len(dropped) != 1 || dropped[0].Data["event_type"] != "phase.started" {
			t.Fatalf("overflow events = %#v; want one for the dropped progress item", dropped)
		}

		notifier.Start()
		t.Cleanup(func() { notifier.Stop(context.Background()) })
		waitFor(t, 10*time.Second, func() bool {
			return len(postsTo(harness.server, "C-ENG")) == 5
		})
	})
}

func TestNotifierShutdownIsBoundedAndIdempotent(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	held := make(chan struct{})
	release := make(chan struct{})
	harness.server.Script("chat.postMessage", testsupport.Response{
		Body:    map[string]any{"ok": true, "ts": "1758499200.000001", "channel": "C-ENG"},
		Started: held,
		Release: release,
	})
	notifier := harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	select {
	case <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never reached the held request")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	stopStart := time.Now()
	notifier.Stop(stopCtx)
	if elapsed := time.Since(stopStart); elapsed > time.Second {
		t.Fatalf("stop took %s; want it bounded by the short deadline", elapsed)
	}

	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	time.Sleep(50 * time.Millisecond)
	if got := len(harness.server.Requests("chat.postMessage")); got != 1 {
		t.Fatalf("requests after stop = %d; want the late tap discarded without a request", got)
	}

	secondStart := time.Now()
	notifier.Stop(context.Background())
	if elapsed := time.Since(secondStart); elapsed > 100*time.Millisecond {
		t.Fatalf("second stop took %s; want it to return immediately", elapsed)
	}

	done := make(chan struct{})
	go func() {
		notifier.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker goroutines still running after the stop")
	}
}
