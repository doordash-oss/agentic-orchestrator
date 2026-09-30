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
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type reviewStatusReporter struct {
	mu             sync.Mutex
	service        *Service
	failures       []reportedDeliveryFailure
	successes      int
	failureStarted chan<- struct{}
	failureRelease <-chan struct{}
}

func (r *reviewStatusReporter) ReportSlackDeliveryFailure(
	at time.Time,
	generation uint64,
	canonical errcat.Error,
) {
	r.mu.Lock()
	r.failures = append(r.failures, reportedDeliveryFailure{
		at: at, credentialGeneration: generation, canonical: canonical,
	})
	r.mu.Unlock()
	r.service.RecordDeliveryFailure(at, canonical)
	if r.failureStarted != nil {
		r.failureStarted <- struct{}{}
	}
	if r.failureRelease != nil {
		<-r.failureRelease
	}
}

func (r *reviewStatusReporter) ReportSlackDeliverySuccess(at time.Time, _ uint64) {
	r.mu.Lock()
	r.successes++
	r.mu.Unlock()
	r.service.RecordDeliverySuccess(at)
}

func (r *reviewStatusReporter) snapshot() ([]reportedDeliveryFailure, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reportedDeliveryFailure(nil), r.failures...), r.successes
}

func startReviewGateHarnessWithReporter(
	t *testing.T,
	harness *reviewGateHarness,
	reporter DeliveryReporter,
) *Notifier {
	t.Helper()
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings,
		Store:    harness.store,
		StateDir: harness.stateDir,
		Observer: harness.observer,
		Reporter: reporter,
		Pending:  harness.pending,
		Clock:    harness.clock,
		Jitter:   func() float64 { return 0 },
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.SetServerName("Review upload failure test")
	notifier.Start()
	harness.notifier = notifier
	t.Cleanup(func() {
		notifier.Stop(context.Background())
	})
	return notifier
}

func TestSlackReviewCompletionRateLimitPausesOnlyChannelDestination(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()...)
	settings.Categories.Progress = false
	harness := newNotifierHarness(t, settings)
	source := harness.seedFeature("F-review-completion-rate-limit", func(f *feature.Feature) {
		f.Status = feature.StatusPlanNeedsReview
		f.CurrentPhase = feature.PhasePlan
		f.Pipeline = feature.PipelineLarge
		f.CurrentRoadmapPhase = 3
		f.TotalRoadmapPhases = 11
	})
	notifier := harness.newNotifier(8)
	t.Cleanup(func() {
		notifier.Stop(context.Background())
	})

	identity := "review:completion-rate-limit"
	userKey := destinationKey(string(ports.SlackRecipientUser), "U-ADA")
	channelKey := destinationKey(string(ports.SlackRecipientChannel), "C-ENG")
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			userKey: {
				Kind: string(ports.SlackRecipientUser), SlackID: "U-ADA",
				DisplayName: "Ada Lovelace", ChannelID: "D-U-ADA",
				RootTS: "1791000000.000001", Ledger: []string{"1791000000.000001"},
			},
			channelKey: {
				Kind: string(ports.SlackRecipientChannel), SlackID: "C-ENG",
				DisplayName: "#eng", ChannelID: "C-ENG",
				RootTS: "1791000000.000002", Ledger: []string{"1791000000.000002"},
			},
		},
		TagCounter: 1,
		Pending: []pendingInputRecord{{
			Identity: identity, SourceFeatureID: source.ID,
			Kind: string(ports.SlackPendingReview), ReviewID: "review-rate-limit",
			ReviewMode: "plan", TargetPhase: feature.PhaseImplement.DirName(),
			ArtifactID: "phase-3-plan", RunNumber: 1,
			SourceRevision: "sha256:rate-limit", Tag: "#1",
			MessageTS: map[string]string{}, FileIDs: map[string]string{},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, source.ID, record); err != nil {
		t.Fatal(err)
	}
	if _, err := notifier.recordFor(source.ID); err != nil {
		t.Fatal(err)
	}
	if !notifier.reservePendingDelivery(source.ID, identity, channelKey) ||
		!notifier.reservePendingDelivery(source.ID, identity, userKey) {
		t.Fatal("reservePendingDelivery() = false; want both destinations reserved")
	}

	input := ports.SlackPendingInput{
		Kind: ports.SlackPendingReview, FeatureID: source.ID,
		ReviewID: "review-rate-limit", ReviewMode: "plan",
		TargetPhase: feature.PhaseImplement.DirName(),
		ArtifactID:  "phase-3-plan", ArtifactFilename: "phase-plan.md",
		ArtifactBytes: []byte("# Phase 3 plan\n"), ArtifactSize: 15,
		RunNumber: 1, SourceRevision: "sha256:rate-limit",
		PhasePlan: true, RoadmapPhase: 3, TotalRoadmapPhases: 11,
	}
	itemFor := func(key, kind, channelID string) workItem {
		return workItem{
			featureID: source.ID, sourceFeatureID: source.ID,
			destinationKey: key, kind: kind, channelID: channelID,
			reply: replyPayload{
				kind: kindNeedsInput, inputKind: string(ports.SlackPendingReview),
				identity: identity, tag: "#1",
				review: &reviewDelivery{input: input, source: source},
			},
		}
	}
	channelItem := itemFor(channelKey, string(ports.SlackRecipientChannel), "C-ENG")
	userItem := itemFor(userKey, string(ports.SlackRecipientUser), "D-U-ADA")
	channelWorker := &destinationWorker{notifier: notifier, channelID: "C-ENG", signal: make(chan struct{}, 1)}
	userWorker := &destinationWorker{notifier: notifier, channelID: "D-U-ADA", signal: make(chan struct{}, 1)}

	channelRetryStarted := make(chan struct{}, 1)
	releaseChannelRetry := make(chan struct{})
	harness.server.Script(
		"files.completeUploadExternal",
		testsupport.Response{
			Status:  http.StatusTooManyRequests,
			Body:    map[string]any{"ok": false, "error": "rate_limited"},
			Headers: http.Header{"Retry-After": []string{"2"}},
		},
		testsupport.Response{
			Body: map[string]any{
				"ok": true, "files": []any{map[string]any{"id": "F-channel"}},
			},
			Started: channelRetryStarted,
			Release: releaseChannelRetry,
		},
	)

	startedAt := harness.clock.Now()
	channelDone := make(chan error, 1)
	go func() {
		channelDone <- channelWorker.postReply(channelItem)
	}()
	select {
	case <-channelRetryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("channel completion retry did not start")
	}
	if elapsed := harness.clock.Now().Sub(startedAt); elapsed < 2*time.Second {
		t.Fatalf("channel rate-limit pause = %s; want at least 2s", elapsed)
	}

	userDone := make(chan error, 1)
	go func() {
		userDone <- userWorker.postReply(userItem)
	}()
	select {
	case err := <-userDone:
		if err != nil {
			t.Fatalf("direct-message postReply() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("direct-message delivery did not proceed while channel retry was held")
	}
	configuredRecipients := harness.settings.SlackSettings().Recipients
	if got := len(configuredRecipients); got != 2 {
		t.Fatalf("configured recipients = %d; want both retained", got)
	}
	configuredIDs := map[string]bool{}
	for _, recipient := range configuredRecipients {
		configuredIDs[recipient.ID] = true
	}
	if !configuredIDs["U-ADA"] || !configuredIDs["C-ENG"] {
		t.Fatalf("configured recipients = %#v; want user and channel retained", configuredRecipients)
	}
	if len(postsTo(harness.server, "D-U-ADA")) != 1 {
		t.Fatal("direct-message review did not post before channel retry was released")
	}
	if len(postsTo(harness.server, "C-ENG")) != 0 {
		t.Fatal("channel review posted before its completion retry was released")
	}

	close(releaseChannelRetry)
	select {
	case err := <-channelDone:
		if err != nil {
			t.Fatalf("channel postReply() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel delivery did not finish after retry release")
	}

	for _, destination := range []string{"D-U-ADA", "C-ENG"} {
		posts := postsTo(harness.server, destination)
		if len(posts) != 1 ||
			!strings.Contains(fieldString(posts[0], "text"), "attached above") {
			t.Errorf("%s review posts = %#v; want one attached-above message", destination, posts)
		}
	}
	if got := len(harness.observer.ofKind("slack.delivery_failed")); got != 0 {
		t.Fatalf("slack.delivery_failed events = %d; want 0", got)
	}
	persisted := readReviewGateRecord(t, harness.stateDir, source.ID)
	if len(persisted.Pending) != 1 ||
		len(persisted.Pending[0].MessageTS) != 2 ||
		len(persisted.Pending[0].FileIDs) != 2 {
		t.Fatalf("persisted review delivery = %+v; want both destinations complete", persisted.Pending)
	}
	completions := harness.server.Requests("files.completeUploadExternal")
	var channelCompletions, userCompletions int
	for _, completion := range completions {
		switch fieldString(completion, "channel_id") {
		case "C-ENG":
			channelCompletions++
		case "D-U-ADA":
			userCompletions++
		}
	}
	if channelCompletions != 2 || userCompletions != 1 {
		t.Fatalf(
			"completion calls channel/direct = %d/%d; want 2/1",
			channelCompletions,
			userCompletions,
		)
	}
}

func TestSlackReviewUploadMissingScopeProjectsCredentialErrorAndSelfHeals(t *testing.T) {
	settings := defaultTestSettings(testToken, testRecipients()[1])
	settings.Categories.Progress = false
	settings.CredentialGeneration = 7
	harness := newReviewGateHarness(t, settings)
	harness.seedReview(
		"F-review-missing-scope",
		feature.StatusPlanNeedsReview,
		"phase-1-plan",
		"phase-plan.md",
		[]byte("# Phase 1 plan\n"),
		1,
		4,
		nil,
	)
	service := NewService()
	failureStarted := make(chan struct{}, 1)
	failureRelease := make(chan struct{})
	var releaseFailure sync.Once
	releaseReporter := func() {
		releaseFailure.Do(func() { close(failureRelease) })
	}
	t.Cleanup(releaseReporter)
	reporter := &reviewStatusReporter{
		service: service, failureStarted: failureStarted, failureRelease: failureRelease,
	}
	harness.server.Script("files.getUploadURLExternal", testsupport.Response{
		Body: map[string]any{
			"ok": false, "error": "missing_scope", "needed": "files:write",
		},
	})
	notifier := startReviewGateHarnessWithReporter(t, harness, reporter)
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: "F-review-missing-scope",
	})
	select {
	case <-failureStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("credential reporter did not receive missing-scope failure")
	}
	failures, successesBeforeRecovery := reporter.snapshot()
	if len(failures) != 1 {
		t.Fatalf("credential reporter failures = %d; want exactly 1", len(failures))
	}
	if failures[0].credentialGeneration != 7 ||
		failures[0].canonical.Code != errcat.SlackScopesRevoked ||
		!strings.Contains(failures[0].canonical.Summary, "files:write") {
		t.Fatalf("credential reporter failure = %#v; want canonical scopes-revoked error", failures[0])
	}
	status := service.Status(ports.SlackStatusInput{Token: testToken, HasIdentity: true})
	if status.State != ports.SlackCredentialError ||
		status.LastError == nil ||
		status.LastError.Code != errcat.SlackScopesRevoked {
		t.Fatalf("Slack status after missing scope = %#v; want credential_error", status)
	}

	releaseReporter()
	waitFor(t, 10*time.Second, func() bool {
		record := readReviewGateRecord(t, harness.stateDir, "F-review-missing-scope")
		return len(record.Pending) == 1 && len(record.Pending[0].MessageTS) == 1
	})

	posts := reviewThreadPosts(harness.server)
	if len(posts) != 1 ||
		!strings.Contains(fieldString(posts[0], "text"), "could not be attached by Slack") {
		t.Fatalf("missing-scope review posts = %#v; want fallback message", posts)
	}
	waitFor(t, 10*time.Second, func() bool {
		return service.Status(ports.SlackStatusInput{
			Token: testToken, HasIdentity: true,
		}).State == ports.SlackConnected
	})
	failures, successesAfterRecovery := reporter.snapshot()
	if len(failures) != 1 || successesAfterRecovery <= successesBeforeRecovery {
		t.Fatalf(
			"credential reporter failures/successes after recovery = %d/%d; want one failure and a later success",
			len(failures),
			successesAfterRecovery,
		)
	}
	status = service.Status(ports.SlackStatusInput{Token: testToken, HasIdentity: true})
	if status.State != ports.SlackConnected || status.LastError != nil {
		t.Fatalf("Slack status after successful write = %#v; want connected", status)
	}
	if got := len(harness.observer.ofKind("slack.delivery_failed")); got != 1 {
		t.Fatalf("slack.delivery_failed events = %d; want one missing-scope upload failure", got)
	}
	for _, event := range harness.observer.ofKind("slack.delivery_failed") {
		if event.Data["failure_class"] != "credential" ||
			event.Data["error_code"] != string(errcat.SlackScopesRevoked) ||
			event.Data["item_kind"] != "review_artifact" {
			t.Fatalf("missing-scope delivery event = %#v", event.Data)
		}
	}
	if len(reviewThreadPosts(harness.server)) != 1 {
		t.Fatalf("review thread posts = %d; want one fallback", len(reviewThreadPosts(harness.server)))
	}
	configuredRecipients := harness.settings.SlackSettings().Recipients
	if len(configuredRecipients) != 1 || configuredRecipients[0].ID != "C-ENG" {
		t.Fatalf("configured channel recipient changed: %#v", configuredRecipients)
	}
}
