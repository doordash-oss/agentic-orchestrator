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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const reviewGateSecondSecret = "xoxp-review-gate-second-secret-987654321"

type reviewGateEvidenceRequest struct {
	Index          int              `json:"index"`
	Method         string           `json:"method"`
	Path           string           `json:"path"`
	Channel        string           `json:"channel,omitempty"`
	ThreadTS       string           `json:"thread_ts,omitempty"`
	ReplyBroadcast string           `json:"reply_broadcast,omitempty"`
	FallbackText   string           `json:"fallback_text,omitempty"`
	Blocks         []map[string]any `json:"blocks,omitempty"`
	Filename       string           `json:"filename,omitempty"`
	DeclaredLength string           `json:"declared_length,omitempty"`
	ContentType    string           `json:"content_type,omitempty"`
	ByteLength     int64            `json:"byte_length,omitempty"`
	Digest         string           `json:"digest,omitempty"`
	Files          string           `json:"files,omitempty"`
	Title          string           `json:"title,omitempty"`
	ReturnedTS     string           `json:"returned_ts,omitempty"`
	ReturnedFileID string           `json:"returned_file_id,omitempty"`
}

type reviewGateEvidenceStage struct {
	Name       string               `json:"name"`
	TagCounter int                  `json:"tag_counter"`
	Pending    []pendingInputRecord `json:"pending"`
}

type reviewGateEvidenceTranscript struct {
	Requests        []reviewGateEvidenceRequest `json:"requests"`
	Stages          []reviewGateEvidenceStage   `json:"stages"`
	MessagePosted   []observe.Event             `json:"message_posted"`
	DeliveryFailed  []observe.Event             `json:"delivery_failed"`
	FinalRecord     *featureRecord              `json:"final_record"`
	FinalRecordYAML string                      `json:"final_record_yaml"`
}

func TestSlackReviewGateRedaction(t *testing.T) {
	logs := captureLogs(t)
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.Progress = false
	harness := newNotifierHarness(t, settings)
	harness.seedFeature("F-review-redaction", nil)

	body := []byte(strings.Join([]string{
		"# Review boundary",
		"repository agentic-orchestrator",
		"token " + testToken,
		"other " + reviewGateSecondSecret,
		"Authorization: Bearer " + reviewGateSecondSecret,
		"https://alice:review-password@example.com/private",
		"path /tmp/review-artifact",
	}, "\n"))
	filename := "plan-" + testToken + "-" + reviewGateSecondSecret + ".md"
	artifactPath := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(artifactPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	harness.pending.set("F-review-redaction", ports.SlackPendingInput{
		Kind:           ports.SlackPendingReview,
		FeatureID:      "F-review-redaction",
		ReviewID:       "redaction-review",
		ReviewMode:     "plan",
		TargetPhase:    feature.PhaseImplement.DirName(),
		ArtifactID:     "release-" + reviewGateSecondSecret,
		ArtifactPath:   artifactPath,
		ArtifactSize:   int64(len(body)),
		RunNumber:      1,
		SourceRevision: "sha256:first",
	})
	notifier := harness.start(8)
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-redaction",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-redaction")
		return len(record.Pending) == 1 &&
			len(record.Pending[0].MessageTS) == 1 &&
			len(record.Pending[0].FileIDs) == 1
	})

	harness.pending.set("F-review-redaction", ports.SlackPendingInput{
		Kind:           ports.SlackPendingReview,
		FeatureID:      "F-review-redaction",
		ReviewID:       "redaction-review",
		ReviewMode:     "plan",
		TargetPhase:    feature.PhaseImplement.DirName(),
		ArtifactID:     "release-" + reviewGateSecondSecret,
		ArtifactPath:   artifactPath,
		ArtifactSize:   int64(len(body)),
		RunNumber:      1,
		SourceRevision: "sha256:second",
	})
	harness.server.Script("files.getUploadURLExternal", testsupport.Response{
		Body: map[string]any{
			"ok":     false,
			"error":  "missing_scope: " + testToken + " " + reviewGateSecondSecret,
			"needed": "files:write " + reviewGateSecondSecret,
		},
	})
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-redaction",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-redaction")
		return record.TagCounter == 2 &&
			len(record.Pending) == 1 &&
			len(record.Pending[0].MessageTS) == 1
	})

	requests, err := json.Marshal(harness.server.AllRequests())
	if err != nil {
		t.Fatal(err)
	}
	recordRaw, err := os.ReadFile(recordPath(harness.stateDir, "F-review-redaction"))
	if err != nil {
		t.Fatal(err)
	}
	events, err := json.Marshal(harness.observer.all())
	if err != nil {
		t.Fatal(err)
	}
	surfaces := map[string][]byte{
		"fake Slack requests":    requests,
		"persisted Slack record": recordRaw,
		"observability events":   events,
		"logs":                   []byte(logs.String()),
	}
	allSurfaces := bytes.Join([][]byte{requests, recordRaw, events, []byte(logs.String())}, []byte("\n"))
	for _, secret := range []string{
		testToken,
		reviewGateSecondSecret,
		"alice:review-password",
	} {
		for name, surface := range surfaces {
			if bytes.Contains(surface, []byte(secret)) {
				t.Errorf("%s leaked review-gate secret %q", name, secret)
			}
		}
	}
	for _, want := range []string{
		"[REDACTED]",
		"missing_scope",
	} {
		if !bytes.Contains(allSurfaces, []byte(want)) {
			t.Errorf("review-gate surfaces lost %q:\n%s", want, allSurfaces)
		}
	}
	if bytes.Contains(requests, []byte("Xoxp Review Gate Second Secret 987654321")) {
		t.Error("fake Slack requests leaked transformed credential text through the artifact label/title")
	}
	uploads := harness.server.Requests("upload")
	if len(uploads) != 1 {
		t.Fatalf("successful redaction uploads = %d; want 1", len(uploads))
	}
	redactedBody := []byte(scrub(testToken, string(body)))
	if uploads[0].ContentLength != int64(len(redactedBody)) ||
		uploads[0].Digest != fmt.Sprintf("%x", sha256.Sum256(redactedBody)) {
		t.Fatalf("redacted upload metadata = %+v; want length/digest of scrubbed body", uploads[0])
	}
	firstReview := reviewThreadPosts(harness.server)[0]
	if !strings.Contains(
		fieldString(firstReview, "text"),
		fmt.Sprintf("%d bytes", len(redactedBody)),
	) {
		t.Errorf(
			"review message size = %q; want redacted byte size %d",
			fieldString(firstReview, "text"),
			len(redactedBody),
		)
	}
	if reviewsPath := filepath.Join(harness.stateDir, "F-review-redaction", "runs", "run-000001", "reviews"); recordExistsAt(reviewsPath) {
		t.Fatalf("Slack review projection created review-session files at %s", reviewsPath)
	}
}

func TestSlackReviewGateEvidence(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()...)
	settings.Categories.Progress = false
	harness := newReviewGateHarness(t, settings)
	designBody := []byte(strings.Join([]string{
		"# Design review",
		"repository agentic-orchestrator",
		"sentinel " + testToken,
		"second " + reviewGateSecondSecret,
		"Authorization: Bearer " + reviewGateSecondSecret,
		"https://alice:review-password@example.com/private",
	}, "\n"))
	designFilename := "design-" + testToken + "-" + reviewGateSecondSecret + ".md"
	designPath := harness.seedReview(
		"F-review-evidence",
		feature.StatusDesignNeedsReview,
		"design",
		designFilename,
		designBody,
		0,
		3,
		nil,
	)
	scriptReviewShareResponses(harness.server, 2)
	notifier := harness.start()
	var stages []reviewGateEvidenceStage
	snapshot := func(name string) {
		t.Helper()
		record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
		stages = append(stages, reviewGateEvidenceStage{
			Name:       name,
			TagCounter: record.TagCounter,
			Pending:    cloneReviewPending(record.Pending),
		})
	}

	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-evidence",
	})
	waitForReviewEvidence(t, harness, "#1", 2, 2)
	snapshot("design_review")
	assertReviewUploadDigest(t, harness.server, designBody)

	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusPlanning
		f.CurrentPhase = feature.PhasePlan
	})
	notifier.DomainEventTap(startedEvent("F-review-evidence", feature.PhasePlan))
	waitForReviewRetired(t, harness, "F-review-evidence")
	snapshot("design_approved")

	roadmapBody := []byte("# Roadmap\n\n1. Phase one\n2. Phase two\n3. Phase three\n")
	harness.installArtifact("F-review-evidence", "roadmap", "roadmap.md", roadmapBody)
	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusPlanNeedsReview
		f.CurrentRoadmapPhase = 0
		f.TotalRoadmapPhases = 3
	})
	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Recipients = []ports.SlackRecipient{testRecipients()[1]}
	})
	for i := 0; i < 4; i++ {
		harness.server.Script("upload", testsupport.Response{
			Status: http.StatusInternalServerError,
			Body:   "upload failed",
		})
	}
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-evidence",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
		return record.TagCounter == 2 &&
			len(record.Pending) == 1 &&
			len(record.Pending[0].MessageTS) == 1 &&
			len(harness.observer.ofKind("slack.delivery_failed")) == 1
	})
	snapshot("roadmap_channel_upload_failed")

	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Recipients = testRecipients()
	})
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-evidence",
	})
	waitForReviewEvidence(t, harness, "#2", 2, 1)
	snapshot("roadmap_user_uploaded")

	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusPlanning
	})
	notifier.DomainEventTap(startedEvent("F-review-evidence", feature.PhasePlan))
	waitForReviewRetired(t, harness, "F-review-evidence")
	revisedRoadmap := append(append([]byte(nil), roadmapBody...), []byte("\nRevised mobile approval flow.\n")...)
	if err := os.WriteFile(
		filepath.Join(harness.store.RunDir("F-review-evidence", 1), "roadmap", "roadmap.md"),
		revisedRoadmap,
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusPlanNeedsReview
	})
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-evidence",
	})
	waitForReviewEvidence(t, harness, "#3", 2, 2)
	snapshot("roadmap_revised")

	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusImplementing
	})
	notifier.DomainEventTap(startedEvent("F-review-evidence", feature.PhaseImplement))
	waitForReviewRetired(t, harness, "F-review-evidence")
	rewindBody := []byte("# Phase 2 plan\n\nRewind from this artifact.\n")
	harness.installArtifact("F-review-evidence", "phase-2-plan", "phase-plan.md", rewindBody)
	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		target := feature.PhaseImplement
		roadmapPhase := 2
		f.Status = feature.StatusPlanNeedsReview
		f.CurrentRoadmapPhase = 2
		f.TotalRoadmapPhases = 3
		f.IsRewind = true
		f.PendingReviewPhase = &target
		f.PendingRewindReviewRoadmapPhase = &roadmapPhase
	})
	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Categories.Progress = true
	})
	notifier.DomainEventTap(ports.Event{
		Type: ports.FeatureRewound, FeatureID: "F-review-evidence", Phase: feature.PhaseImplement,
	})
	waitForReviewEvidence(t, harness, "#4", 2, 2)
	snapshot("rewind_review")

	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Categories.Progress = false
		current.Categories.NeedsInput = false
	})
	harness.modifyFeature("F-review-evidence", func(f *feature.Feature) {
		f.Status = feature.StatusImplementing
		f.IsRewind = false
		f.PendingReviewPhase = nil
		f.PendingRewindReviewRoadmapPhase = nil
	})
	notifier.DomainEventTap(startedEvent("F-review-evidence", feature.PhaseImplement))
	waitForReviewRetired(t, harness, "F-review-evidence")
	harness.seedReview(
		"F-review-child",
		feature.StatusPlanNeedsReview,
		"phase-1-plan",
		"phase-plan.md",
		[]byte("# Child phase plan\n"),
		1,
		1,
		func(f *feature.Feature) {
			f.Parent = &feature.ChildRelationship{
				ParentID: "F-review-evidence",
				Kind:     feature.ChildKindRefactor,
			}
		},
	)
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-child",
	})
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
		return len(record.Pending) == 1 && record.Pending[0].Tag == ""
	})
	snapshot("child_review_needs_input_off")

	harness.settings.mutate(func(current *ports.SlackRuntimeSettings) {
		current.Categories.NeedsInput = true
	})
	notifier.DomainEventTap(startedEvent("F-review-child", feature.PhasePlan))
	waitForReviewEvidence(t, harness, "#5", 2, 2)
	snapshot("child_review_posted")
	userWorker := reviewWaitForWorker(t, notifier, "D-U-ADA")
	channelWorker := reviewWaitForWorker(t, notifier, "C-ENG")
	waitFor(t, 10*time.Second, func() bool {
		return reviewWorkerSettled(notifier, userWorker) &&
			reviewWorkerSettled(notifier, channelWorker)
	})

	requestsBeforeRestart := len(harness.server.AllRequests())
	updatesBeforeRestart := len(harness.server.Requests("chat.update"))
	harness.restart()
	harness.notifier.SignalReady()
	waitFor(t, 5*time.Second, func() bool { return harness.notifier.startupDone.Load() })
	waitFor(t, 5*time.Second, func() bool {
		return len(harness.server.Requests("chat.update")) == updatesBeforeRestart+2
	})
	if got := harness.server.AllRequests()[requestsBeforeRestart:]; len(got) != 2 ||
		got[0].Path != "/api/chat.update" || got[1].Path != "/api/chat.update" {
		t.Fatalf("restart writes = %#v; want only two in-place card refreshes", got)
	}
	harness.notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-child",
	})
	waitFor(t, 5*time.Second, func() bool { return harness.notifier.queue.len() == 0 })
	time.Sleep(20 * time.Millisecond)
	if got := len(reviewThreadPosts(harness.server)); got != 10 {
		t.Fatalf("review posts after restart duplicate = %d; want 10", got)
	}
	snapshot("restart_preserved_review")

	harness.modifyFeature("F-review-child", func(f *feature.Feature) {
		f.Status = feature.StatusInterrupted
	})
	harness.notifier.DomainEventTap(ports.Event{
		Type: ports.FeatureInterrupted, FeatureID: "F-review-child",
	})
	waitForReviewRetired(t, harness, "F-review-evidence")
	snapshot("child_interrupted")
	harness.modifyFeature("F-review-child", func(f *feature.Feature) {
		f.Status = feature.StatusPlanNeedsReview
	})
	harness.notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-child",
	})
	waitForReviewEvidence(t, harness, "#6", 2, 2)
	snapshot("child_review_reopened")

	assertReviewEvidenceContract(t, harness, stages, revisedRoadmap)
	writeReviewGateEvidence(t, harness, stages)
	if recordExists(harness.stateDir, "F-review-child") {
		t.Fatal("refactor child received its own Slack record")
	}
	if filepath.Base(designPath) != designFilename {
		t.Fatalf("design artifact basename = %q; want %q", filepath.Base(designPath), designFilename)
	}
}

func recordExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func cloneReviewPending(input []pendingInputRecord) []pendingInputRecord {
	cloned := make([]pendingInputRecord, len(input))
	for i, item := range input {
		cloned[i] = item
		if item.MessageTS != nil {
			cloned[i].MessageTS = make(map[string]string, len(item.MessageTS))
			for key, value := range item.MessageTS {
				cloned[i].MessageTS[key] = value
			}
		}
		if item.FileIDs != nil {
			cloned[i].FileIDs = make(map[string]string, len(item.FileIDs))
			for key, value := range item.FileIDs {
				cloned[i].FileIDs[key] = value
			}
		}
	}
	return cloned
}

func waitForReviewEvidence(
	t *testing.T,
	harness *reviewGateHarness,
	tag string,
	messageCount, fileCount int,
) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
		return len(record.Pending) == 1 &&
			record.Pending[0].Tag == tag &&
			len(record.Pending[0].MessageTS) == messageCount &&
			len(record.Pending[0].FileIDs) == fileCount
	})
}

func waitForReviewRetired(
	t *testing.T,
	harness *reviewGateHarness,
	featureID string,
) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, featureID)
		return len(record.Pending) == 0
	})
}

func assertReviewUploadDigest(
	t *testing.T,
	server *testsupport.Server,
	body []byte,
) {
	t.Helper()
	uploads := server.Requests("upload")
	if len(uploads) < 2 {
		t.Fatalf("design raw uploads = %d; want one per destination", len(uploads))
	}
	redacted := []byte(scrub(testToken, string(body)))
	wantLength := int64(len(redacted))
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(redacted))
	for _, upload := range uploads[:2] {
		if upload.ContentLength != wantLength ||
			upload.Digest != wantDigest ||
			upload.BearerPresent {
			t.Errorf("design raw upload = %+v; want redacted bearer-less payload", upload)
		}
	}
}

func assertReviewEvidenceContract(
	t *testing.T,
	harness *reviewGateHarness,
	stages []reviewGateEvidenceStage,
	revisedRoadmap []byte,
) {
	t.Helper()
	if len(stages) != 11 {
		t.Fatalf("evidence stages = %d; want 11", len(stages))
	}
	wantStages := []struct {
		name string
		tag  string
	}{
		{"design_review", "#1"},
		{"design_approved", ""},
		{"roadmap_channel_upload_failed", "#2"},
		{"roadmap_user_uploaded", "#2"},
		{"roadmap_revised", "#3"},
		{"rewind_review", "#4"},
		{"child_review_needs_input_off", ""},
		{"child_review_posted", "#5"},
		{"restart_preserved_review", "#5"},
		{"child_interrupted", ""},
		{"child_review_reopened", "#6"},
	}
	for i, want := range wantStages {
		if stages[i].Name != want.name {
			t.Errorf("stage %d name = %q; want %q", i, stages[i].Name, want.name)
		}
		if want.tag == "" {
			if len(stages[i].Pending) != 0 &&
				!(stages[i].Name == "child_review_needs_input_off" &&
					len(stages[i].Pending) == 1 &&
					stages[i].Pending[0].Tag == "") {
				t.Errorf("stage %s pending = %+v; want retired or untagged", stages[i].Name, stages[i].Pending)
			}
			continue
		}
		if len(stages[i].Pending) != 1 || stages[i].Pending[0].Tag != want.tag {
			t.Errorf("stage %s pending = %+v; want tag %s", stages[i].Name, stages[i].Pending, want.tag)
		}
	}
	if stages[4].Pending[0].SourceRevision !=
		fmt.Sprintf("sha256:%x", sha256.Sum256(revisedRoadmap)) {
		t.Errorf("revised roadmap revision = %q; want revised bytes", stages[4].Pending[0].SourceRevision)
	}
	if stages[3].Pending[0].SourceRevision == stages[4].Pending[0].SourceRevision {
		t.Error("roadmap revision did not change after artifact rewrite")
	}

	var (
		channelFailure bool
		userSuccess    bool
		rewoundLine    bool
		childPrefix    bool
		countsOnly     bool
	)
	for _, request := range harness.server.AllRequests() {
		rendered := fieldString(request, "text") + " " + fmt.Sprint(request.Fields["blocks"])
		channel := firstNonempty(
			fieldString(request, "channel"),
			fieldString(request, "channel_id"),
		)
		if strings.Contains(rendered, "#2") &&
			channel == "C-ENG" &&
			strings.Contains(rendered, "could not be attached") {
			channelFailure = true
		}
		if strings.Contains(rendered, "#2") &&
			channel == "D-U-ADA" &&
			strings.Contains(rendered, "attached above") {
			userSuccess = true
		}
		if strings.Contains(rendered, "Rewound to Implementation for roadmap phase 2 in run 1") {
			rewoundLine = true
		}
		if strings.Contains(rendered, "Refactor: Review: Phase 1 plan") {
			childPrefix = true
		}
		if strings.Contains(rendered, "1 review") &&
			strings.Contains(request.Path, "chat.update") {
			countsOnly = true
		}
		if strings.Contains(request.Path, "chat.postMessage") &&
			fieldString(request, "thread_ts") != "" &&
			strings.Contains(rendered, "Review:") {
			if channel == "C-ENG" && fieldString(request, "reply_broadcast") != "true" {
				t.Errorf("channel thread post missing reply broadcast: %#v", request.Fields)
			}
			if channel == "D-U-ADA" && fieldString(request, "reply_broadcast") != "" {
				t.Errorf("direct-message thread post broadcast unexpectedly: %#v", request.Fields)
			}
		}
	}
	if !channelFailure || !userSuccess || !rewoundLine || !childPrefix || !countsOnly {
		t.Fatalf(
			"evidence markers channel-failure/user-success/rewind/child/counts = %t/%t/%t/%t/%t",
			channelFailure,
			userSuccess,
			rewoundLine,
			childPrefix,
			countsOnly,
		)
	}

	failures := harness.observer.ofKind("slack.delivery_failed")
	if len(failures) != 1 ||
		failures[0].Data["item_kind"] != "review_artifact" ||
		fmt.Sprint(failures[0].Data["attempts"]) != "4" {
		t.Fatalf("review artifact failure events = %#v; want one four-attempt failure", failures)
	}
	record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
	if record.TagCounter != 6 ||
		len(record.Pending) != 1 ||
		len(record.Pending[0].MessageTS) != 2 ||
		len(record.Pending[0].FileIDs) != 2 {
		t.Fatalf("final review record = %+v; want reopened #6 with both destinations", record)
	}
	for key, destination := range record.Destinations {
		if destination.Failure != nil || len(destination.Ledger) < 9 {
			t.Errorf("final destination %s = %+v; want no failure and retained ledger", key, destination)
		}
	}
}

func writeReviewGateEvidence(
	t *testing.T,
	harness *reviewGateHarness,
	stages []reviewGateEvidenceStage,
) {
	t.Helper()
	record := readReviewGateRecord(t, harness.stateDir, "F-review-evidence")
	recordRaw, err := os.ReadFile(recordPath(harness.stateDir, "F-review-evidence"))
	if err != nil {
		t.Fatal(err)
	}
	transcript := reviewGateEvidenceTranscript{
		Requests:        reviewGateEvidenceRequests(harness.server.AllRequests()),
		Stages:          stages,
		MessagePosted:   harness.observer.ofKind("slack.message_posted"),
		DeliveryFailed:  harness.observer.ofKind("slack.delivery_failed"),
		FinalRecord:     record,
		FinalRecordYAML: string(recordRaw),
	}
	encoded, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		testToken,
		reviewGateSecondSecret,
		"alice:review-password",
	} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("review-gate evidence leaked %q:\n%s", secret, encoded)
		}
	}
	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir != "" {
		target := filepath.Join(dir, "behaviors")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(target, "slack-review-gate.json"),
			append(encoded, '\n'),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{
		"Review: Design",
		"Review: Roadmap",
		"could not be attached",
		"Run 1 rewind",
		"Refactor: Review",
		"review_artifact",
		"reply_broadcast",
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("review-gate evidence missing %q", want)
		}
	}
}

func reviewGateEvidenceRequests(
	requests []testsupport.Request,
) []reviewGateEvidenceRequest {
	result := make([]reviewGateEvidenceRequest, 0, len(requests))
	for i, request := range requests {
		entry := reviewGateEvidenceRequest{
			Index:          i,
			Method:         request.Method,
			Path:           request.Path,
			Channel:        firstNonempty(fieldString(request, "channel"), fieldString(request, "channel_id"), fieldString(request, "users")),
			ThreadTS:       fieldString(request, "thread_ts"),
			ReplyBroadcast: fieldString(request, "reply_broadcast"),
			FallbackText:   fieldString(request, "text"),
			Blocks:         requestBlocks(request),
			Filename:       fieldString(request, "filename"),
			DeclaredLength: fieldString(request, "length"),
			ContentType:    request.ContentType,
			ByteLength:     request.ContentLength,
			Digest:         request.Digest,
			Files:          fieldString(request, "files"),
			ReturnedTS:     request.ReturnedTS,
			ReturnedFileID: request.ReturnedFileID,
		}
		var files []struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(entry.Files), &files) == nil && len(files) > 0 {
			entry.Title = files[0].Title
		}
		result = append(result, entry)
	}
	return result
}
