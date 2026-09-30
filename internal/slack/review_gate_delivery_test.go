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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type reviewGateHarness struct {
	t        *testing.T
	stateDir string
	store    *feature.Store
	server   *testsupport.Server
	settings *fakeSettings
	clock    *fakeClock
	observer *fakeObserver
	pending  ports.SlackPendingInputSource
	notifier *Notifier
}

func newReviewGateHarness(
	t *testing.T,
	settings ports.SlackRuntimeSettings,
) *reviewGateHarness {
	t.Helper()
	stateDir := t.TempDir()
	store := feature.NewStore(stateDir)
	var pending ports.SlackPendingInputSource
	_ = serverruntime.NewHandler(serverruntime.HandlerOptions{
		Runtime:               serverruntime.RuntimeIdentity{StateDir: stateDir},
		Features:              store,
		FeatureStore:          store,
		Config:                nil,
		DisableHostValidation: true,
		BindSlackPendingInputSource: func(source ports.SlackPendingInputSource) {
			pending = source
		},
	})
	if pending == nil {
		t.Fatal("real Slack pending-input source was not bound")
	}

	fake := testsupport.New(t)
	var sequence atomic.Int64
	fake.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{
					"id": "D-" + fmt.Sprint(request.Fields["users"]),
				},
			}}
		case "chat.postMessage", "chat.update":
			n := sequence.Add(1)
			return testsupport.Response{Body: map[string]any{
				"ok":      true,
				"ts":      fmt.Sprintf("1790000000.%06d", n),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{
				"ok": false, "error": "unexpected " + method,
			}}
		}
	})
	return &reviewGateHarness{
		t:        t,
		stateDir: stateDir,
		store:    store,
		server:   fake,
		settings: newFakeSettings(settings),
		clock:    newFakeClock(),
		observer: &fakeObserver{},
		pending:  pending,
	}
}

func (h *reviewGateHarness) start() *Notifier {
	h.t.Helper()
	notifier := NewNotifier(NotifierOptions{
		Settings: h.settings,
		Store:    h.store,
		StateDir: h.stateDir,
		Observer: h.observer,
		Pending:  h.pending,
		Clock:    h.clock,
		Jitter:   func() float64 { return 0 },
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	notifier.SetServerName("Review gate test")
	notifier.Start()
	h.notifier = notifier
	h.t.Cleanup(func() {
		notifier.Stop(h.t.Context())
	})
	return notifier
}

func (h *reviewGateHarness) restart() {
	h.t.Helper()
	h.notifier.Stop(h.t.Context())
	h.notifier = nil
	h.start()
}

func (h *reviewGateHarness) seedReview(
	featureID string,
	status feature.Status,
	artifactID, filename string,
	body []byte,
	roadmapPhase, roadmapTotal int,
	mutate func(*feature.Feature),
) string {
	h.t.Helper()
	artifactPath := filepath.Join(
		h.store.RunDir(featureID, 1),
		artifactID,
		filename,
	)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		h.t.Fatalf("create review artifact directory: %v", err)
	}
	if err := os.WriteFile(artifactPath, body, 0o644); err != nil {
		h.t.Fatalf("write review artifact: %v", err)
	}
	relativePath, err := filepath.Rel(h.store.RunDir(featureID, 1), artifactPath)
	if err != nil {
		h.t.Fatalf("make review artifact relative: %v", err)
	}
	f := &feature.Feature{
		ID:                  featureID,
		Name:                "Slack review gate",
		Slug:                "slack-review-gate",
		Description:         "review gate delivery test",
		Created:             h.clock.Now().Add(-time.Hour),
		Status:              status,
		CurrentPhase:        feature.PhasePlan,
		SchemaVersion:       feature.SchemaVersionCurrent,
		Pipeline:            feature.PipelineLarge,
		ActiveRun:           1,
		RunCount:            1,
		CurrentRoadmapPhase: roadmapPhase,
		TotalRoadmapPhases:  roadmapTotal,
		Artifacts:           map[string]string{artifactID: relativePath},
		Repos:               []feature.FeatureRepo{{Name: "agentic-orchestrator"}},
	}
	if mutate != nil {
		mutate(f)
	}
	f.SetRun(&feature.Run{
		RunNumber:           1,
		Artifacts:           f.Artifacts,
		CurrentRoadmapPhase: f.CurrentRoadmapPhase,
		TotalRoadmapPhases:  f.TotalRoadmapPhases,
	})
	if err := h.store.Save(f); err != nil {
		h.t.Fatalf("save review feature: %v", err)
	}
	return artifactPath
}

func (h *reviewGateHarness) modifyFeature(
	featureID string,
	modify func(*feature.Feature),
) {
	h.t.Helper()
	if err := h.store.Modify(featureID, func(f *feature.Feature) error {
		modify(f)
		return nil
	}); err != nil {
		h.t.Fatalf("modify review feature: %v", err)
	}
}

func (h *reviewGateHarness) installArtifact(
	featureID, artifactID, filename string,
	body []byte,
) string {
	h.t.Helper()
	artifactPath := filepath.Join(
		h.store.RunDir(featureID, 1),
		artifactID,
		filename,
	)
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		h.t.Fatalf("create review artifact directory: %v", err)
	}
	if err := os.WriteFile(artifactPath, body, 0o644); err != nil {
		h.t.Fatalf("write review artifact: %v", err)
	}
	relativePath, err := filepath.Rel(h.store.RunDir(featureID, 1), artifactPath)
	if err != nil {
		h.t.Fatalf("make review artifact relative: %v", err)
	}
	h.modifyFeature(featureID, func(f *feature.Feature) {
		if f.Artifacts == nil {
			f.Artifacts = map[string]string{}
		}
		f.Artifacts[artifactID] = relativePath
	})
	return artifactPath
}

func scriptReviewShareResponses(server *testsupport.Server, count int) {
	files := make([]any, 0, count)
	for i := 0; i < count; i++ {
		files = append(files, map[string]any{
			"id": fmt.Sprintf("F%08d", i+1),
			"shares": map[string]any{
				"public": map[string]any{
					"C-ENG": []any{map[string]any{
						"ts": fmt.Sprintf("1790000100.%06d", i+1),
					}},
				},
				"private": map[string]any{
					"D-U-ADA": []any{map[string]any{
						"ts": fmt.Sprintf("1790000200.%06d", i+1),
					}},
				},
			},
		})
	}
	for i := 0; i < count; i++ {
		server.Script("files.completeUploadExternal", testsupport.Response{
			Body: map[string]any{
				"ok":    true,
				"files": files,
			},
		})
	}
}

func reviewThreadPosts(server *testsupport.Server) []testsupport.Request {
	var posts []testsupport.Request
	for _, request := range server.Requests("chat.postMessage") {
		if fieldString(request, "thread_ts") == "" {
			continue
		}
		if strings.Contains(fieldString(request, "text"), "Review:") {
			posts = append(posts, request)
		}
	}
	return posts
}

func readReviewGateRecord(t *testing.T, stateDir, featureID string) *featureRecord {
	t.Helper()
	record, err := loadFeatureRecord(stateDir, featureID)
	if err != nil {
		t.Fatalf("load Slack review record: %v", err)
	}
	return record
}

func TestSlackReviewGateDeliveryRecordsEvidenceAndSuppressesDuplicates(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()...)
	settings.Categories.Progress = false
	harness := newReviewGateHarness(t, settings)
	body := []byte("# Phase 3 plan\n\n## Tasks\n\n- Deliver the review gate.\n")
	artifactPath := harness.seedReview(
		"F-review-delivery",
		feature.StatusPlanNeedsReview,
		"phase-3-plan",
		"phase-plan.md",
		body,
		3,
		11,
		nil,
	)
	scriptReviewShareResponses(harness.server, 2)
	notifier := harness.start()
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-delivery",
	})

	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-delivery")
		return len(record.Pending) == 1 &&
			len(record.Pending[0].MessageTS) == 2 &&
			len(record.Pending[0].FileIDs) == 2
	})
	record := readReviewGateRecord(t, harness.stateDir, "F-review-delivery")
	pending := record.Pending[0]
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	if pending.Kind != string(ports.SlackPendingReview) ||
		pending.ReviewID == "" ||
		pending.ReviewMode != "plan" ||
		pending.TargetPhase != feature.PhaseImplement.DirName() ||
		pending.ArtifactID != "phase-3-plan" ||
		pending.RunNumber != 1 ||
		pending.SourceRevision != revision ||
		pending.Tag != "#1" ||
		pending.PostedAt.IsZero() {
		t.Fatalf("pending review record = %+v; want complete review metadata", pending)
	}

	for _, destination := range []struct {
		key       string
		channel   string
		broadcast string
	}{
		{destinationKey("user", "U-ADA"), "D-U-ADA", ""},
		{destinationKey("channel", "C-ENG"), "C-ENG", "true"},
	} {
		entry := record.Destinations[destination.key]
		if entry.RootTS == "" || len(entry.Ledger) != 3 {
			t.Errorf("%s ledger = %+v; want root, file share, and review message", destination.key, entry)
		}
		if pending.MessageTS[destination.key] == "" || pending.FileIDs[destination.key] == "" {
			t.Errorf("%s pending metadata = messages %v files %v", destination.key, pending.MessageTS, pending.FileIDs)
		}
		var post testsupport.Request
		for _, candidate := range reviewThreadPosts(harness.server) {
			if fieldString(candidate, "channel") == destination.channel {
				post = candidate
				break
			}
		}
		if post.Path == "" {
			t.Fatalf("review post for %s was not recorded", destination.channel)
		}
		if fieldString(post, "reply_broadcast") != destination.broadcast {
			t.Errorf("%s reply_broadcast = %q; want %q", destination.channel, fieldString(post, "reply_broadcast"), destination.broadcast)
		}
		for _, want := range []string{
			"#1", "Review: Phase 3 plan", "phase-plan.md", "roadmap phase 3 of 11",
			"attached above", "request changes in Agentico", "reply approve",
		} {
			if !strings.Contains(fieldString(post, "text"), want) &&
				!strings.Contains(fmt.Sprint(post.Fields["blocks"]), want) {
				t.Errorf("%s review post missing %q: %#v", destination.channel, want, post.Fields)
			}
		}
		assertReviewUploadBeforeMessage(t, harness.server.AllRequests(), destination.channel, entry.RootTS)
	}

	if got := harness.server.Requests("upload"); len(got) != 2 {
		t.Fatalf("raw uploads = %d; want one per destination", len(got))
	} else {
		wantDigest := fmt.Sprintf("%x", sha256.Sum256(body))
		for _, upload := range got {
			if upload.BearerPresent || upload.ContentLength != int64(len(body)) || upload.Digest != wantDigest {
				t.Errorf("raw upload = %+v; want bearer-less exact artifact bytes", upload)
			}
		}
	}

	beforeUploads := len(harness.server.Requests("files.getUploadURLExternal"))
	beforePosts := len(reviewThreadPosts(harness.server))
	beforeRecord, err := os.ReadFile(recordPath(harness.stateDir, "F-review-delivery"))
	if err != nil {
		t.Fatal(err)
	}
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-delivery",
	})
	notifier.DomainEventTap(startedEvent("F-review-delivery", feature.PhaseImplement))
	waitFor(t, 5*time.Second, func() bool { return notifier.queue.len() == 0 })
	time.Sleep(20 * time.Millisecond)
	afterRecord, err := os.ReadFile(recordPath(harness.stateDir, "F-review-delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(harness.server.Requests("files.getUploadURLExternal")); got != beforeUploads {
		t.Errorf("uploads after duplicate events = %d; want %d", got, beforeUploads)
	}
	if got := len(reviewThreadPosts(harness.server)); got != beforePosts {
		t.Errorf("review posts after duplicate events = %d; want %d", got, beforePosts)
	}
	if string(afterRecord) != string(beforeRecord) {
		t.Error("Slack review record changed after duplicate events")
	}
	if filepath.Base(artifactPath) != "phase-plan.md" {
		t.Fatalf("artifact basename = %q; want phase-plan.md", filepath.Base(artifactPath))
	}
}

func assertReviewUploadBeforeMessage(
	t *testing.T,
	requests []testsupport.Request,
	channel, rootTS string,
) {
	t.Helper()
	completion := -1
	message := -1
	for i, request := range requests {
		switch {
		case strings.HasSuffix(request.Path, "files.completeUploadExternal") &&
			fieldString(request, "channel_id") == channel &&
			fieldString(request, "thread_ts") == rootTS:
			completion = i
		case strings.HasSuffix(request.Path, "chat.postMessage") &&
			fieldString(request, "channel") == channel &&
			fieldString(request, "thread_ts") == rootTS &&
			strings.Contains(fieldString(request, "text"), "Review:"):
			message = i
		}
	}
	if completion < 2 || message <= completion {
		t.Fatalf("request order for %s = completion %d, message %d; want upload URL/raw/completion before message", channel, completion, message)
	}
	var completedFiles []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(fieldString(requests[completion], "files")), &completedFiles); err != nil ||
		len(completedFiles) != 1 {
		t.Fatalf("%s completion files = %#v: %v", channel, requests[completion].Fields["files"], err)
	}
	rawUpload := -1
	uploadURL := -1
	for i := 0; i < completion; i++ {
		if requests[i].Path == "/upload/"+completedFiles[0].ID {
			rawUpload = i
		}
		if strings.HasSuffix(requests[i].Path, "files.getUploadURLExternal") &&
			requests[i].ReturnedFileID == completedFiles[0].ID {
			uploadURL = i
		}
	}
	if rawUpload < 0 || uploadURL < 0 || uploadURL >= rawUpload {
		t.Errorf("%s upload sequence indexes = URL %d, raw %d, completion %d", channel, uploadURL, rawUpload, completion)
	}
}

func TestSlackReviewGateNeedsInputToggle(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.Progress = false
	settings.Categories.NeedsInput = false
	harness := newReviewGateHarness(t, settings)
	harness.seedReview(
		"F-review-toggle",
		feature.StatusPlanNeedsReview,
		"phase-2-plan",
		"phase-plan.md",
		[]byte("# Phase 2 plan\n"),
		2,
		5,
		nil,
	)
	notifier := harness.start()
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-toggle",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-toggle")
		return len(record.Pending) == 1 && record.Pending[0].Tag == ""
	})
	if len(harness.server.Requests("files.getUploadURLExternal")) != 0 ||
		len(reviewThreadPosts(harness.server)) != 0 {
		t.Fatal("review uploaded or posted while Needs input was off")
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, update := range harness.server.Requests("chat.update") {
			if strings.Contains(fmt.Sprint(update.Fields["blocks"]), "1 review") {
				return true
			}
		}
		return false
	})

	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Categories.NeedsInput = true
	})
	notifier.DomainEventTap(startedEvent("F-review-toggle", feature.PhasePlan))
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-toggle")
		return len(record.Pending) == 1 &&
			record.Pending[0].Tag == "#1" &&
			len(record.Pending[0].MessageTS) == 1 &&
			len(record.Pending[0].FileIDs) == 1
	})
}

func TestSlackReviewGateUploadFailureStillPostsWithoutDestinationFailure(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.Progress = false
	harness := newReviewGateHarness(t, settings)
	harness.seedReview(
		"F-review-upload-failure",
		feature.StatusPlanNeedsReview,
		"phase-1-plan",
		"phase-plan.md",
		[]byte("# Phase 1 plan\n"),
		1,
		4,
		nil,
	)
	harness.server.Script("files.getUploadURLExternal", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "missing_scope", "needed": "files:write"},
	})
	harness.start().DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-upload-failure",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-upload-failure")
		return len(record.Pending) == 1 && len(record.Pending[0].MessageTS) == 1
	})
	record := readReviewGateRecord(t, harness.stateDir, "F-review-upload-failure")
	key := destinationKey("channel", "C-ENG")
	if record.Destinations[key].Failure != nil {
		t.Fatalf("review upload failure created destination failure: %+v", record.Destinations[key].Failure)
	}
	if len(record.Pending[0].FileIDs) != 0 {
		t.Fatalf("review upload failure recorded file IDs: %v", record.Pending[0].FileIDs)
	}
	if len(harness.server.Requests("upload")) != 0 ||
		len(harness.server.Requests("files.completeUploadExternal")) != 0 {
		t.Fatal("upload sequence continued after upload-URL failure")
	}
	posts := reviewThreadPosts(harness.server)
	if len(posts) != 1 ||
		!strings.Contains(fieldString(posts[0], "text"), "could not be attached by Slack") {
		t.Fatalf("failure review posts = %#v; want one message with attachment note", posts)
	}
	failures := harness.observer.ofKind("slack.delivery_failed")
	if len(failures) != 1 ||
		failures[0].Data["item_kind"] != "review_artifact" ||
		failures[0].Data["tag"] != "#1" {
		t.Fatalf("review upload failure events = %#v", failures)
	}
}

func TestSlackReviewGateUnavailableArtifactSkipsUpload(t *testing.T) {
	tests := []struct {
		name string
		path func(*testing.T) (string, int64)
		note string
	}{
		{
			name: "missing",
			path: func(t *testing.T) (string, int64) {
				return filepath.Join(t.TempDir(), "missing-plan.md"), 123
			},
			note: "file could not be read",
		},
		{
			name: "too large",
			path: func(t *testing.T) (string, int64) {
				path := filepath.Join(t.TempDir(), "large-plan.md")
				data := make([]byte, reviewArtifactSizeLimit+1)
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
				return path, int64(len(data))
			},
			note: "too large to attach",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, size := test.path(t)
			settings := defaultTestSettings(testToken, testRecipients()[1])
			settings.Categories.Progress = false
			harness := newNotifierHarness(t, settings)
			harness.seedFeature("F-review-skip", nil)
			harness.pending.set("F-review-skip", ports.SlackPendingInput{
				Kind:           ports.SlackPendingReview,
				FeatureID:      "F-review-skip",
				ReviewID:       "review-skip",
				ReviewMode:     "plan",
				TargetPhase:    feature.PhaseImplement.DirName(),
				ArtifactID:     "phase-1-plan",
				ArtifactPath:   path,
				ArtifactSize:   size,
				RunNumber:      1,
				SourceRevision: "revision-skip",
				PhasePlan:      true,
				RoadmapPhase:   1,
			})
			harness.start(8).DomainEventTap(ports.Event{
				Type: ports.ReviewRequired, FeatureID: "F-review-skip",
			})
			waitFor(t, 10*time.Second, func() bool {
				return len(reviewThreadPosts(harness.server)) == 1
			})
			if len(harness.server.Requests("files.getUploadURLExternal")) != 0 {
				t.Fatal("unavailable artifact attempted an upload")
			}
			post := reviewThreadPosts(harness.server)[0]
			if !strings.Contains(fieldString(post, "text"), test.note) {
				t.Errorf("review fallback = %q; want %q", fieldString(post, "text"), test.note)
			}
			if len(harness.observer.ofKind("slack.delivery_failed")) != 0 {
				t.Fatal("unavailable artifact emitted a delivery failure")
			}
		})
	}
}
