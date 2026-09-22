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
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestReviewGateUploadsBeforeMessageAndPersistsFile(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	artifact := []byte("# Phase 3 plan\n\nShip the review gate.\n")
	artifactPath := t.TempDir() + "/phase-plan.md"
	if err := os.WriteFile(artifactPath, artifact, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	harness.pending.set("F-1", ports.SlackPendingInput{
		Kind:               ports.SlackPendingReview,
		FeatureID:          "F-1",
		ReviewID:           "review-phase-3",
		ReviewMode:         "plan",
		TargetPhase:        "implement",
		ArtifactID:         "phase-3-plan",
		ArtifactPath:       artifactPath,
		ArtifactSize:       int64(len(artifact)),
		RunNumber:          1,
		SourceRevision:     "sha256:revision",
		PhasePlan:          true,
		RoadmapPhase:       3,
		TotalRoadmapPhases: 11,
	})

	harness.start(0)
	harness.feed(ports.Event{Type: ports.ReviewRequired, FeatureID: "F-1"})
	waitFor(t, 5*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 &&
			record.Pending[0].MessageTS["channel:C-ENG"] != "" &&
			record.Pending[0].FileIDs["channel:C-ENG"] != ""
	})

	requests := harness.server.AllRequests()
	var ordered []testsupport.Request
	for _, request := range requests {
		if request.Path == "/api/files.getUploadURLExternal" ||
			strings.HasPrefix(request.Path, "/upload/") ||
			request.Path == "/api/files.completeUploadExternal" ||
			(request.Path == "/api/chat.postMessage" && fieldString(request, "thread_ts") != "") {
			ordered = append(ordered, request)
		}
	}
	if len(ordered) != 4 {
		t.Fatalf("review requests = %+v; want upload URL, bytes, completion, message", ordered)
	}
	wantPaths := []string{
		"/api/files.getUploadURLExternal",
		"/upload/",
		"/api/files.completeUploadExternal",
		"/api/chat.postMessage",
	}
	for i, want := range wantPaths {
		if i == 1 {
			if !strings.HasPrefix(ordered[i].Path, want) {
				t.Errorf("request %d path = %q; want prefix %q", i, ordered[i].Path, want)
			}
		} else if ordered[i].Path != want {
			t.Errorf("request %d path = %q; want %q", i, ordered[i].Path, want)
		}
	}
	sum := sha256.Sum256(artifact)
	if ordered[1].BearerPresent ||
		ordered[1].ContentLength != int64(len(artifact)) ||
		ordered[1].Digest != fmt.Sprintf("%x", sum[:]) {
		t.Errorf("raw upload = %+v; want bearer-less matching artifact", ordered[1])
	}
	if fieldString(ordered[3], "reply_broadcast") != "true" {
		t.Errorf("review message reply_broadcast = %q; want true", fieldString(ordered[3], "reply_broadcast"))
	}

	record, _ := readFeatureRecord(harness.stateDir, "F-1")
	pending := record.Pending[0]
	if pending.ReviewID != "review-phase-3" ||
		pending.ReviewMode != "plan" ||
		pending.TargetPhase != "implement" ||
		pending.ArtifactID != "phase-3-plan" ||
		pending.RunNumber != 1 ||
		pending.SourceRevision != "sha256:revision" ||
		pending.Tag != "#1" {
		t.Errorf("pending review record = %+v; want review identity and tag", pending)
	}
	posted := harness.observer.ofKind("slack.message_posted")
	if len(posted) != 2 {
		t.Fatalf("message_posted events = %+v; want artifact and review message", posted)
	}
}

func TestReviewGateUploadFailureStillPostsWithoutDestinationWarning(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(testToken, testRecipients()[1]))
	harness.seedFeature("F-1", nil)
	artifactPath := t.TempDir() + "/phase-plan.md"
	if err := os.WriteFile(artifactPath, []byte("# Plan\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	harness.pending.set("F-1", ports.SlackPendingInput{
		Kind:           ports.SlackPendingReview,
		FeatureID:      "F-1",
		ReviewID:       "review-failure",
		ReviewMode:     "plan",
		TargetPhase:    "implement",
		ArtifactID:     "plan",
		ArtifactPath:   artifactPath,
		ArtifactSize:   7,
		RunNumber:      1,
		SourceRevision: "sha256:failure",
	})
	harness.server.Script("files.getUploadURLExternal", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "file_uploads_disabled"},
	})

	harness.start(0)
	harness.feed(ports.Event{Type: ports.ReviewRequired, FeatureID: "F-1"})
	waitFor(t, 5*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 &&
			record.Pending[0].MessageTS["channel:C-ENG"] != ""
	})

	if got := harness.server.CallCount("upload"); got != 0 {
		t.Errorf("raw upload calls = %d; want 0", got)
	}
	if got := harness.server.CallCount("files.completeUploadExternal"); got != 0 {
		t.Errorf("completion calls = %d; want 0", got)
	}
	posts := reviewThreadPostsTo(harness.server, "C-ENG")
	if len(posts) != 1 ||
		!strings.Contains(fieldString(posts[0], "text"), "could not be attached by Slack") ||
		!strings.Contains(fieldString(posts[0], "text"), "Read it in Agentico") {
		t.Errorf("review failure message = %+v; want actionable attachment note", posts)
	}
	failures := harness.observer.ofKind("slack.delivery_failed")
	if len(failures) != 1 ||
		failures[0].Data["item_kind"] != "review_artifact" ||
		failures[0].Data["tag"] != "#1" {
		t.Errorf("delivery_failed events = %+v; want tagged review artifact failure", failures)
	}
	record, _ := readFeatureRecord(harness.stateDir, "F-1")
	if record.Destinations["channel:C-ENG"].Failure != nil {
		t.Errorf("destination failure = %+v; want no feature warning for artifact upload", record.Destinations["channel:C-ENG"].Failure)
	}
}

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

func TestReenabledNeedsInputDeliverySurvivesProgressReservationEviction(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.NeedsInput = false
	harness := newNotifierHarness(t, settings)
	harness.seedFeature("F-1", nil)
	harness.pending.set("F-1", testPendingPermission("perm-1"))

	notifier := harness.start(3)
	notifier.RuntimeMessageTap(controlRuntimeMessage("F-1", "session-1", "perm-1"))
	waitFor(t, 5*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 && record.Pending[0].Tag == "" &&
			notifier.queue.len() == 0
	})

	problemStarted := make(chan struct{}, 1)
	problemRelease := make(chan struct{})
	var held atomic.Bool
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		response, _ := defaultOKResponder()
		result := response(method, request)
		if method == "chat.postMessage" &&
			fieldString(request, "thread_ts") != "" &&
			held.CompareAndSwap(false, true) {
			result.Started = problemStarted
			result.Release = problemRelease
		}
		return result
	})

	problem := errcat.New(errcat.SessionCrashed)
	harness.feed(ports.Event{
		Type: ports.FeatureFailed, FeatureID: "F-1", CanonicalError: &problem,
	})
	select {
	case <-problemStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("protected Problems reply did not reach the held destination")
	}

	harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Categories.NeedsInput = true
	})
	harness.feed(startedEvent("F-1", feature.PhaseImplement))
	worker := reviewWaitForWorker(t, notifier, "C-ENG")
	waitFor(t, 2*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && record.Pending[0].Tag == "#1" &&
			reviewWorkerInboxLen(worker) == 2
	})

	harness.feed(startedEvent("F-1", feature.PhaseDesign))
	waitFor(t, 2*time.Second, func() bool { return notifier.queue.len() == 3 })
	harness.feed(ports.Event{Type: ports.NeedUserInputRequired, FeatureID: "F-1"})
	waitFor(t, 2*time.Second, func() bool {
		droppedProgress := 0
		for _, event := range harness.observer.ofKind("slack.event_dropped") {
			if event.Data["event_type"] == "phase.started" {
				droppedProgress++
			}
		}
		return droppedProgress == 2
	})

	close(problemRelease)
	waitFor(t, 5*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, "F-1")
		return ok && len(record.Pending) == 1 &&
			len(record.Pending[0].MessageTS) == 1 &&
			reviewWorkerSettled(notifier, worker)
	})

	needsInputPosts := 0
	for _, post := range reviewThreadPostsTo(harness.server, "C-ENG") {
		if strings.Contains(fieldString(post, "text"), "#1") {
			needsInputPosts++
		}
	}
	if needsInputPosts != 1 {
		t.Fatalf("Needs-input replies after Progress eviction = %d; want exactly 1", needsInputPosts)
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

func TestPendingDeliveryEligibleHandlesConcurrentTimestampUpdates(t *testing.T) {
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
