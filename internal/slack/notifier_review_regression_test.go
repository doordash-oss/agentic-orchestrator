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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestNotifierQueuedWritesRevalidateLiveSettings(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*ports.SlackRuntimeSettings)
		wantUpdates int
	}{
		{
			name: "disabled",
			mutate: func(settings *ports.SlackRuntimeSettings) {
				settings.Enabled = false
			},
		},
		{
			name: "token cleared",
			mutate: func(settings *ports.SlackRuntimeSettings) {
				settings.Token = ""
			},
		},
		{
			name: "recipient removed",
			mutate: func(settings *ports.SlackRuntimeSettings) {
				settings.Recipients = nil
			},
		},
		{
			name: "progress disabled keeps card updates",
			mutate: func(settings *ports.SlackRuntimeSettings) {
				settings.Categories.Progress = false
			},
			wantUpdates: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
			harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })

			rootStarted := make(chan struct{})
			rootRelease := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(rootRelease) }) }
			harness.server.Script("chat.postMessage", testsupport.Response{
				Body: map[string]any{
					"ok": true, "ts": "1758499200.000001", "channel": "C-ENG",
				},
				Started: rootStarted,
				Release: rootRelease,
			})

			notifier := harness.start(0)
			t.Cleanup(release)
			harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
			select {
			case <-rootStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("root card request did not reach the held destination")
			}

			harness.feed(startedEvent("F-1", feature.PhaseResearch))
			worker := reviewWaitForWorker(t, notifier, "C-ENG")
			if !reviewEventually(2*time.Second, func() bool {
				return reviewWorkerInboxLen(worker) == 1
			}) {
				t.Fatal("queued Progress work did not reach the held destination worker")
			}

			harness.settings.mutate(test.mutate)
			release()
			if !reviewEventually(2*time.Second, func() bool {
				return reviewWorkerSettled(notifier, worker)
			}) {
				t.Fatal("destination worker did not settle after release")
			}

			posts := postsTo(harness.server, "C-ENG")
			if len(posts) != 1 {
				t.Errorf("chat.postMessage calls = %d; want only the already-started root card", len(posts))
			}
			for _, post := range posts {
				if fieldString(post, "thread_ts") != "" {
					t.Errorf("queued Progress reply was sent after live settings changed: %#v", post.Fields)
				}
			}
			if got := len(reviewRequestsTo(harness.server, "chat.update", "C-ENG")); got != test.wantUpdates {
				t.Errorf("chat.update calls = %d; want %d", got, test.wantUpdates)
			}
		})
	}
}

func TestNotifierRunningCapacityDropsFourthProgressItem(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	notifier := harness.start(3)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	if !reviewEventually(2*time.Second, func() bool {
		return len(reviewRequestsTo(harness.server, "chat.update", "C-ENG")) == 1
	}) {
		t.Fatal("initial root card update was not delivered")
	}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(firstRelease) }) }
	t.Cleanup(release)
	harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{
			"ok": true, "ts": "1758499200.000100", "channel": "C-ENG",
		},
		Started: firstStarted,
		Release: firstRelease,
	})

	harness.feed(startedEvent("F-1", feature.PhaseResearch))
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first Progress reply did not reach the held destination")
	}

	worker := reviewWaitForWorker(t, notifier, "C-ENG")
	harness.feed(startedEvent("F-1", feature.PhaseDesign))
	harness.feed(startedEvent("F-1", feature.PhaseInquire))
	if !reviewEventually(2*time.Second, func() bool {
		return reviewWorkerInboxLen(worker) == 2 && notifier.queue.len() == 3
	}) {
		t.Fatal("the second and third Progress items did not reach pending delivery")
	}

	tapReturned := make(chan struct{})
	go func() {
		harness.feed(startedEvent("F-1", feature.PhaseKnowledgeBase))
		close(tapReturned)
	}()
	select {
	case <-tapReturned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("DomainEventTap blocked behind a held destination")
	}

	if !reviewEventually(500*time.Millisecond, func() bool {
		return len(harness.observer.ofKind("slack.event_dropped")) == 1
	}) {
		t.Error("fourth Progress item was not dropped while the running worker held capacity")
	}

	release()
	if !reviewEventually(2*time.Second, func() bool {
		return reviewWorkerSettled(notifier, worker)
	}) {
		t.Fatal("destination worker did not settle after release")
	}

	posts := reviewThreadPostsTo(harness.server, "C-ENG")
	if len(posts) != 3 {
		t.Errorf("delivered Progress replies = %d; want three within capacity", len(posts))
	}
	wants := []string{
		"Research started",
		"Design started",
		"Inquire started",
	}
	for i, want := range wants {
		if i >= len(posts) {
			break
		}
		if got := fieldString(posts[i], "text"); !strings.Contains(got, want) {
			t.Errorf("Progress reply %d = %q; want text containing %q", i+1, got, want)
		}
	}
}

func TestNotifierReplyBacklogDoesNotStarveDueCardUpdate(t *testing.T) {
	recipients := []ports.SlackRecipient{
		{TypedText: "#slow", Kind: ports.SlackRecipientChannel, ID: "C-SLOW", DisplayName: "#slow"},
		{TypedText: "#fast", Kind: ports.SlackRecipientChannel, ID: "C-FAST", DisplayName: "#fast"},
	}
	harness := newNotifierHarness(t, defaultTestSettings(testToken, recipients...))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	notifier := harness.start(0)
	harness.feed(ports.Event{Type: ports.FeatureStarted, FeatureID: "F-1"})
	if !reviewEventually(2*time.Second, func() bool {
		return len(reviewRequestsTo(harness.server, "chat.update", "C-SLOW")) == 1 &&
			len(reviewRequestsTo(harness.server, "chat.update", "C-FAST")) == 1
	}) {
		t.Fatal("initial root card updates were not delivered")
	}

	slowStarted := make(chan struct{})
	slowRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(slowRelease) }) }
	t.Cleanup(release)
	var held atomic.Bool
	var counter atomic.Int64
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "chat.postMessage":
			n := counter.Add(1)
			response := testsupport.Response{Body: map[string]any{
				"ok": true, "ts": time.Unix(n, 0).UTC().Format("150405.000000"),
				"channel": fieldString(request, "channel"),
			}}
			if fieldString(request, "channel") == "C-SLOW" &&
				fieldString(request, "thread_ts") != "" &&
				held.CompareAndSwap(false, true) {
				response.Started = slowStarted
				response.Release = slowRelease
			}
			return response
		case "chat.update":
			return testsupport.Response{Body: map[string]any{"ok": true}}
		default:
			return testsupport.Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
	})

	phases := []feature.Phase{
		feature.PhaseResearch,
		feature.PhaseDesign,
		feature.PhaseInquire,
		feature.PhasePlan,
		feature.PhaseImplement,
		feature.PhaseKnowledgeBase,
	}
	for _, phase := range phases {
		harness.feed(startedEvent("F-1", phase))
	}
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow destination did not reach the held Progress reply")
	}

	if !reviewEventually(2*time.Second, func() bool {
		return len(reviewThreadPostsTo(harness.server, "C-FAST")) >= 3 &&
			len(reviewRequestsTo(harness.server, "chat.update", "C-FAST")) >= 2
	}) {
		t.Error("independent destination did not post replies and refresh its card while C-SLOW was held")
	}

	latest, err := harness.store.Load("F-1")
	if err != nil {
		t.Fatal(err)
	}
	latest.Status = feature.StatusPublished
	latest.CurrentPhase = feature.PhaseFinalReview
	if err := harness.store.Save(latest); err != nil {
		t.Fatal(err)
	}

	release()
	slowWorker := reviewWaitForWorker(t, notifier, "C-SLOW")
	fastWorker := reviewWaitForWorker(t, notifier, "C-FAST")
	if !reviewEventually(2*time.Second, func() bool {
		return len(reviewThreadPostsTo(harness.server, "C-SLOW")) == len(phases) &&
			len(reviewThreadPostsTo(harness.server, "C-FAST")) == len(phases) &&
			reviewWorkerSettled(notifier, slowWorker) &&
			reviewWorkerSettled(notifier, fastWorker)
	}) {
		t.Fatal("reply backlog did not drain after releasing the slow destination")
	}

	for _, channel := range []string{"C-SLOW", "C-FAST"} {
		repliesBeforeUpdate, ok := reviewRepliesBeforeSecondUpdate(harness.server, channel)
		if !ok {
			t.Errorf("%s did not send a coalesced card update for the reply backlog", channel)
			continue
		}
		if repliesBeforeUpdate >= len(phases) {
			t.Errorf("%s card update followed all %d replies; a due update must run before the backlog drains",
				channel, repliesBeforeUpdate)
		}
	}

	updates := reviewRequestsTo(harness.server, "chat.update", "C-SLOW")
	if len(updates) < 2 {
		t.Fatalf("C-SLOW updates = %d; want initial and backlog refreshes", len(updates))
	}
	blocks := requestBlocks(updates[1])
	if len(blocks) < 2 {
		t.Fatalf("latest C-SLOW update blocks = %#v; want card section", blocks)
	}
	fields := sectionFields(blocks[1])
	for _, want := range []string{"*Phase:* Final Review", "*Status:* Published"} {
		if !strings.Contains(fields, want) {
			t.Errorf("latest card fields %q missing %q", fields, want)
		}
	}
}

func TestNotifierOverflowObserverCannotDelayDomainEventTap(t *testing.T) {
	observer := &reviewBlockingObserver{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	notifier := NewNotifier(NotifierOptions{
		Observer:      observer,
		QueueCapacity: 1,
		Clock:         newFakeClock(),
	})
	t.Cleanup(func() {
		close(observer.release)
		notifier.Stop(context.Background())
	})

	notifier.DomainEventTap(startedEvent("F-1", feature.PhaseResearch))
	tapReturned := make(chan struct{})
	go func() {
		notifier.DomainEventTap(startedEvent("F-1", feature.PhaseDesign))
		close(tapReturned)
	}()

	select {
	case <-observer.started:
	case <-time.After(time.Second):
		t.Fatal("overflow observer was not invoked")
	}
	select {
	case <-tapReturned:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("DomainEventTap waited for the blocked overflow observer")
	}
}

type reviewBlockingObserver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *reviewBlockingObserver) Emit(observe.Event) error {
	o.once.Do(func() { close(o.started) })
	<-o.release
	return nil
}

func reviewWaitForWorker(t *testing.T, notifier *Notifier, channelID string) *destinationWorker {
	t.Helper()
	var worker *destinationWorker
	if !reviewEventually(2*time.Second, func() bool {
		notifier.workerMu.Lock()
		worker = notifier.workers[channelID]
		notifier.workerMu.Unlock()
		return worker != nil
	}) {
		t.Fatalf("destination worker %s was not created", channelID)
	}
	return worker
}

func reviewEventually(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return condition()
}

func reviewWorkerInboxLen(worker *destinationWorker) int {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return len(worker.inbox)
}

func reviewWorkerSettled(notifier *Notifier, worker *destinationWorker) bool {
	worker.mu.Lock()
	settled := len(worker.inbox) == 0 && len(worker.dirty) == 0
	worker.mu.Unlock()
	return settled &&
		len(worker.signal) == 0 &&
		notifier.queue.len() == 0
}

func reviewRequestsTo(server *testsupport.Server, method, channel string) []testsupport.Request {
	var requests []testsupport.Request
	for _, request := range server.Requests(method) {
		if fieldString(request, "channel") == channel {
			requests = append(requests, request)
		}
	}
	return requests
}

func reviewThreadPostsTo(server *testsupport.Server, channel string) []testsupport.Request {
	var posts []testsupport.Request
	for _, request := range reviewRequestsTo(server, "chat.postMessage", channel) {
		if fieldString(request, "thread_ts") != "" {
			posts = append(posts, request)
		}
	}
	return posts
}

func reviewRepliesBeforeSecondUpdate(server *testsupport.Server, channel string) (int, bool) {
	updateCount := 0
	replyCount := 0
	for _, request := range server.AllRequests() {
		if fieldString(request, "channel") != channel {
			continue
		}
		switch {
		case strings.HasSuffix(request.Path, "/chat.update"):
			updateCount++
			if updateCount == 2 {
				return replyCount, true
			}
		case strings.HasSuffix(request.Path, "/chat.postMessage") &&
			fieldString(request, "thread_ts") != "" &&
			updateCount >= 1:
			replyCount++
		}
	}
	return 0, false
}

var _ EventObserver = (*reviewBlockingObserver)(nil)
