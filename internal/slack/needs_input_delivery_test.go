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
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestNeedsInputDeliveryReservedWhileQueued(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	harness.pending.set("F-1", testPendingPermission("perm-1"))

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var held atomic.Bool
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		response, _ := defaultOKResponder()
		result := response(method, request)
		if method == "chat.postMessage" &&
			fieldString(request, "thread_ts") != "" &&
			held.CompareAndSwap(false, true) {
			result.Started = started
			result.Release = release
		}
		return result
	})

	notifier := harness.start(16)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first Needs-input reply did not reach the held destination")
	}

	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 2*time.Second, func() bool { return notifier.queue.len() == 1 })
	if got := threadReplyCount(harness.server.AllRequests()); got != 1 {
		t.Fatalf("thread posts while first delivery is held = %d; want 1", got)
	}

	close(release)
	waitFor(t, 5*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 && len(record.Pending[0].MessageTS) == 1 &&
			notifier.queue.len() == 0
	})
	if got := threadReplyCount(harness.server.AllRequests()); got != 1 {
		t.Fatalf("thread posts after duplicate delivery drained = %d; want 1", got)
	}
}

func TestNeedsInputRetirementCancelsQueuedReply(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	harness.pending.set("F-1", testPendingPermission("perm-1"))

	rootStarted := make(chan struct{}, 1)
	rootRelease := make(chan struct{})
	var held atomic.Bool
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		response, _ := defaultOKResponder()
		result := response(method, request)
		if method == "chat.postMessage" &&
			fieldString(request, "thread_ts") == "" &&
			held.CompareAndSwap(false, true) {
			result.Started = rootStarted
			result.Release = rootRelease
		}
		return result
	})

	notifier := harness.start(16)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	select {
	case <-rootStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("root card did not reach the held destination")
	}
	waitFor(t, 2*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1
	})

	harness.pending.set("F-1")
	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "session-1",
		FeatureID: "F-1",
		Phase:     feature.PhaseImplement,
		Message:   llm.SDKMessage{Type: "assistant"},
	})
	waitFor(t, 2*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 0
	})

	close(rootRelease)
	waitFor(t, 5*time.Second, func() bool { return notifier.queue.len() == 0 })
	if got := threadReplyCount(harness.server.AllRequests()); got != 0 {
		t.Fatalf("thread posts after pending input retired = %d; want 0", got)
	}
}

func TestRuntimeMessageTapUsesOnlyInMemoryAdmission(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	loadStarted := make(chan struct{}, 1)
	releaseLoad := make(chan struct{})
	loader := &blockingFeatureLoader{
		inner:   harness.store,
		started: loadStarted,
		release: releaseLoad,
	}
	notifier := NewNotifier(NotifierOptions{
		Settings:      harness.settings,
		Store:         loader,
		StateDir:      harness.stateDir,
		Pending:       harness.pending,
		QueueCapacity: 16,
		Clock:         harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.Start()
	t.Cleanup(func() {
		select {
		case <-releaseLoad:
		default:
			close(releaseLoad)
		}
		notifier.Stop(context.Background())
	})

	notifier.RuntimeMessageTap(session.SDKEventMsg{
		SessionID: "session-2",
		FeatureID: "F-2",
		Phase:     feature.PhaseImplement,
		Message:   llm.SDKMessage{Type: "assistant"},
	})
	select {
	case <-loadStarted:
		t.Fatal("untracked runtime message reached the feature loader")
	case <-time.After(20 * time.Millisecond):
	}
	if got := notifier.queue.len(); got != 0 {
		t.Fatalf("queued items after untracked feature = %d; want 0", got)
	}

	notifier.inputMu.Lock()
	notifier.trackedInputs["F-1"] = true
	notifier.inputMu.Unlock()

	done := make(chan struct{})
	go func() {
		notifier.RuntimeMessageTap(session.SDKEventMsg{
			SessionID: "session-1",
			FeatureID: "F-1",
			Phase:     feature.PhaseImplement,
			Message:   llm.SDKMessage{Type: "assistant"},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("RuntimeMessageTap blocked on tracked-input admission")
	}
	if got := notifier.queue.len(); got != 1 {
		t.Fatalf("queued rechecks = %d; want 1", got)
	}
	select {
	case <-loadStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not reach the blocked feature loader")
	}
	harness.pending.mu.Lock()
	defer harness.pending.mu.Unlock()
	if calls := harness.pending.calls["F-2"]; calls != 0 {
		t.Fatalf("PendingSlackInputs(F-2) calls = %d; want 0", calls)
	}
}

type blockingFeatureLoader struct {
	inner   FeatureLoader
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (l *blockingFeatureLoader) Load(id string) (*feature.Feature, error) {
	l.once.Do(func() { l.started <- struct{}{} })
	<-l.release
	return l.inner.Load(id)
}

func TestPendingDeliverySnapshotDoesNotShareMessageTimestampMaps(t *testing.T) {
	record := &featureRecord{Pending: []pendingInputRecord{{
		Identity:  "permission:perm-1",
		MessageTS: map[string]string{"channel:C-ONE": "1.0"},
	}}}
	notifier := &Notifier{
		records:           map[string]*featureRecord{"F-1": record},
		trackedInputs:     map[string]bool{"F-1": true},
		pendingDeliveries: map[string]struct{}{},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			notifier.recordMu.Lock()
			record.Pending[0].MessageTS["channel:C-TWO"] = "2.0"
			delete(record.Pending[0].MessageTS, "channel:C-TWO")
			notifier.recordMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = notifier.pendingDeliveryEligible(
				record, "F-1", "permission:perm-1", "channel:C-TWO",
			)
		}
	}()
	wg.Wait()
}

func testPendingPermission(requestID string) ports.SlackPendingInput {
	return ports.SlackPendingInput{
		Kind:      ports.SlackPendingPermission,
		FeatureID: "F-1",
		RequestID: requestID,
		ToolName:  "Bash",
		Phase:     "implement",
		RepoName:  "agentic-orchestrator",
		Input:     map[string]any{"command": strings.TrimSpace("npm publish")},
	}
}
