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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

type fakeDeliveryReporter struct {
	mu        sync.Mutex
	failures  []reportedDeliveryFailure
	successes []time.Time
}

type reportedDeliveryFailure struct {
	at        time.Time
	canonical errcat.Error
}

func (r *fakeDeliveryReporter) ReportSlackDeliveryFailure(at time.Time, canonical errcat.Error) {
	r.mu.Lock()
	r.failures = append(r.failures, reportedDeliveryFailure{at: at, canonical: canonical})
	r.mu.Unlock()
}

func (r *fakeDeliveryReporter) ReportSlackDeliverySuccess(at time.Time) {
	r.mu.Lock()
	r.successes = append(r.successes, at)
	r.mu.Unlock()
}

func (r *fakeDeliveryReporter) snapshot() ([]reportedDeliveryFailure, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]reportedDeliveryFailure(nil), r.failures...),
		append([]time.Time(nil), r.successes...)
}

type failureFixture struct {
	harness  *notifierHarness
	notifier *Notifier
	worker   *destinationWorker
	reporter *fakeDeliveryReporter
	item     workItem
	key      string
}

func newFailureFixture(t *testing.T, rootTS string) *failureFixture {
	t.Helper()
	recipient := testRecipients()[1]
	harness := newNotifierHarness(t, defaultTestSettings(testToken, recipient))
	harness.seedFeature("F-1", func(f *feature.Feature) { withRoadmap(f) })
	key := destinationKey(string(recipient.Kind), recipient.ID)
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			key: {
				Kind:        string(recipient.Kind),
				SlackID:     recipient.ID,
				DisplayName: recipient.DisplayName,
				ChannelID:   recipient.ID,
				RootTS:      rootTS,
			},
		},
	}
	if err := persistFeatureRecord(harness.stateDir, "F-1", record); err != nil {
		t.Fatal(err)
	}
	reporter := &fakeDeliveryReporter{}
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings,
		Store:    harness.store,
		StateDir: harness.stateDir,
		Observer: harness.observer,
		Reporter: reporter,
		Clock:    harness.clock,
		Jitter:   func() float64 { return 0 },
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.SetServerName("Local agent")
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	return &failureFixture{
		harness:  harness,
		notifier: notifier,
		worker: &destinationWorker{
			notifier:  notifier,
			channelID: recipient.ID,
			signal:    make(chan struct{}, 1),
		},
		reporter: reporter,
		key:      key,
		item: workItem{
			featureID:       "F-1",
			sourceFeatureID: "F-1",
			destinationKey:  key,
			kind:            string(recipient.Kind),
			channelID:       recipient.ID,
			reply: replyPayload{
				kind:     kindProgress,
				fallback: "Phase started.",
			},
		},
	}
}

func (f *failureFixture) failure(t *testing.T) *destinationFailure {
	t.Helper()
	record, err := loadFeatureRecord(f.harness.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	return record.Destinations[f.key].Failure
}

func (f *failureFixture) waitForFailureEvent(t *testing.T, count int) []observe.Event {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		return len(f.harness.observer.ofKind("slack.delivery_failed")) == count
	})
	return f.harness.observer.ofKind("slack.delivery_failed")
}

func TestSlackDestinationFailurePersistsCountsAndClearsOnSuccess(t *testing.T) {
	fixture := newFailureFixture(t, "1758499200.000001")
	featurePath := filepath.Join(fixture.harness.stateDir, "F-1", "feature.yaml")
	featureBefore, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		fixture.harness.server.Script("chat.postMessage", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "is_archived"},
		})
		if err := fixture.worker.postReply(fixture.item); err == nil {
			t.Fatal("postReply() error = nil; want Slack rejection")
		}
		got := fixture.failure(t)
		if got == nil {
			t.Fatal("destination failure = nil")
		}
		if got.Code != errcat.SlackRecipientNotNotified ||
			got.SlackError != "is_archived" ||
			got.Count != i+1 {
			t.Fatalf("destination failure = %#v", got)
		}
		if i == 0 && !got.FirstFailedAt.Equal(got.LastFailedAt) {
			t.Fatalf("first failure times = %s/%s; want equal", got.FirstFailedAt, got.LastFailedAt)
		}
		if i > 0 && !got.LastFailedAt.After(got.FirstFailedAt) {
			t.Fatalf("failure times = %s/%s; want last after first", got.FirstFailedAt, got.LastFailedAt)
		}
	}
	first := fixture.failure(t).FirstFailedAt
	fixture.waitForFailureEvent(t, 5)

	fresh, err := loadFeatureRecord(fixture.harness.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := fresh.Destinations[fixture.key].Failure; got == nil || got.Count != 5 ||
		!got.FirstFailedAt.Equal(first) {
		t.Fatalf("reloaded destination failure = %#v; want durable count and first time", got)
	}

	otherKey := destinationKey("user", "U-ADA")
	current, err := fixture.notifier.recordFor("F-1")
	if err != nil {
		t.Fatal(err)
	}
	fixture.notifier.recordMu.Lock()
	current.Destinations[otherKey] = destinationRecord{
		Kind:        "user",
		SlackID:     "U-ADA",
		DisplayName: "Ada Lovelace",
		ChannelID:   "D-U-ADA",
		Failure: &destinationFailure{
			Code:          errcat.SlackRecipientNotNotified,
			SlackError:    "user_not_found",
			Count:         1,
			FirstFailedAt: first,
			LastFailedAt:  first,
		},
	}
	if err := fixture.notifier.persistRecordLocked(
		"F-1",
		current,
		ports.SlackRecipientUser,
	); err != nil {
		fixture.notifier.recordMu.Unlock()
		t.Fatal(err)
	}
	fixture.notifier.recordMu.Unlock()

	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{"ok": true, "ts": "1758499200.000002", "channel": "C-ENG"},
	})
	if err := fixture.worker.postReply(fixture.item); err != nil {
		t.Fatalf("postReply() success error = %v", err)
	}
	if got := fixture.failure(t); got != nil {
		t.Fatalf("destination failure after success = %#v; want nil", got)
	}
	reloaded, err := loadFeatureRecord(fixture.harness.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Destinations[otherKey].Failure; got == nil ||
		got.SlackError != "user_not_found" {
		t.Fatalf("other destination failure after success = %#v; want unchanged", got)
	}
	_, successes := fixture.reporter.snapshot()
	if len(successes) != 1 {
		t.Fatalf("reported successes = %d; want 1", len(successes))
	}
	featureAfter, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(featureBefore, featureAfter) {
		t.Fatal("feature.yaml changed during Slack failure recording")
	}
}

func TestSlackPermanentDestinationErrorsAreRecorded(t *testing.T) {
	for _, slackError := range []string{
		"channel_not_found",
		"user_not_found",
		"not_in_channel",
		"is_archived",
		"invalid_blocks",
	} {
		t.Run(slackError, func(t *testing.T) {
			fixture := newFailureFixture(t, "1758499200.000001")
			fixture.harness.server.Script("chat.postMessage", testsupport.Response{
				Body: map[string]any{"ok": false, "error": slackError},
			})
			if err := fixture.worker.postReply(fixture.item); err == nil {
				t.Fatal("postReply() error = nil; want Slack rejection")
			}
			failure := fixture.failure(t)
			if failure == nil ||
				failure.Code != errcat.SlackRecipientNotNotified ||
				failure.SlackError != slackError {
				t.Fatalf("destination failure = %#v", failure)
			}
		})
	}
}

func TestSlackGiveUpClassificationAtEveryWritePoint(t *testing.T) {
	tests := []struct {
		name      string
		run       func(*failureFixture) error
		script    string
		responses []testsupport.Response
		code      errcat.Code
		cause     string
		itemKind  string
		attempts  int
	}{
		{
			name: "root card post",
			run: func(f *failureFixture) error {
				return f.worker.ensureCard(f.item)
			},
			script: "chat.postMessage",
			responses: []testsupport.Response{{
				Body: map[string]any{"ok": false, "error": "channel_not_found"},
			}},
			code: errcat.SlackRecipientNotNotified, cause: "channel_not_found",
			itemKind: "root_card", attempts: 1,
		},
		{
			name: "thread reply unknown envelope",
			run: func(f *failureFixture) error {
				return f.worker.postReply(f.item)
			},
			script: "chat.postMessage",
			responses: []testsupport.Response{{
				Body: map[string]any{"ok": false, "error": "invalid_blocks"},
			}},
			code: errcat.SlackRecipientNotNotified, cause: "invalid_blocks",
			itemKind: "progress", attempts: 1,
		},
		{
			name: "root card update",
			run: func(f *failureFixture) error {
				f.worker.flushOne("F-1")
				return errors.New("expected update failure")
			},
			script: "chat.update",
			responses: []testsupport.Response{{
				Body: map[string]any{"ok": false, "error": "is_archived"},
			}},
			code: errcat.SlackRecipientNotNotified, cause: "is_archived",
			itemKind: "root_card", attempts: 1,
		},
		{
			name: "rate limit exhausted",
			run: func(f *failureFixture) error {
				return f.worker.postReply(f.item)
			},
			script: "chat.postMessage",
			responses: repeatedResponses(4, testsupport.Response{
				Status:  http.StatusTooManyRequests,
				Headers: http.Header{"Retry-After": []string{"1"}},
			}),
			code: errcat.SlackDeliveryRetriesExhausted, cause: "rate_limited",
			itemKind: "progress", attempts: 4,
		},
		{
			name: "transport retries exhausted",
			run: func(f *failureFixture) error {
				return f.worker.postReply(f.item)
			},
			script: "chat.postMessage",
			responses: repeatedResponses(4, testsupport.Response{
				Status: http.StatusServiceUnavailable,
			}),
			code: errcat.SlackDeliveryRetriesExhausted, cause: "retries_exhausted",
			itemKind: "progress", attempts: 4,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rootTS := "1758499200.000001"
			if tc.name == "root card post" {
				rootTS = ""
			}
			fixture := newFailureFixture(t, rootTS)
			fixture.harness.server.Script(tc.script, tc.responses...)
			_ = tc.run(fixture)
			failure := fixture.failure(t)
			if failure == nil || failure.Code != tc.code || failure.SlackError != tc.cause {
				t.Fatalf("destination failure = %#v; want code %q cause %q", failure, tc.code, tc.cause)
			}
			events := fixture.waitForFailureEvent(t, 1)
			data := events[0].Data
			want := map[string]any{
				"destination_kind": "channel",
				"item_kind":        tc.itemKind,
				"failure_class":    "destination",
				"slack_error":      tc.cause,
				"error_code":       string(tc.code),
				"attempts":         tc.attempts,
			}
			for key, value := range want {
				if fmt.Sprint(data[key]) != fmt.Sprint(value) {
					t.Errorf("delivery_failed[%q] = %#v; want %#v", key, data[key], value)
				}
			}
		})
	}
}

func TestSlackCredentialGiveUpsReportCanonicalErrorsWithoutDestinationFailure(t *testing.T) {
	tests := []struct {
		slackError string
		needed     string
		code       errcat.Code
		summary    string
	}{
		{slackError: "invalid_auth", code: errcat.SlackTokenRejected, summary: "invalid_auth"},
		{slackError: "token_revoked", code: errcat.SlackTokenRejected, summary: "token_revoked"},
		{slackError: "account_inactive", code: errcat.SlackTokenRejected, summary: "account_inactive"},
		{slackError: "not_authed", code: errcat.SlackTokenRejected, summary: "not_authed"},
		{slackError: "token_expired", code: errcat.SlackTokenRejected, summary: "token_expired"},
		{
			slackError: "missing_scope",
			needed:     "chat:write",
			code:       errcat.SlackScopesRevoked,
			summary:    "chat:write",
		},
	}
	for _, tc := range tests {
		t.Run(tc.slackError, func(t *testing.T) {
			fixture := newFailureFixture(t, "1758499200.000001")
			fixture.harness.server.Script("chat.postMessage", testsupport.Response{
				Body: map[string]any{
					"ok": false, "error": tc.slackError, "needed": tc.needed,
				},
			})
			if err := fixture.worker.postReply(fixture.item); err == nil {
				t.Fatal("postReply() error = nil; want credential rejection")
			}
			if got := fixture.failure(t); got != nil {
				t.Fatalf("destination failure = %#v; want nil for credential error", got)
			}
			failures, successes := fixture.reporter.snapshot()
			if len(failures) != 1 || len(successes) != 0 {
				t.Fatalf("reporter failures/successes = %d/%d; want 1/0", len(failures), len(successes))
			}
			if failures[0].canonical.Code != tc.code ||
				!strings.Contains(failures[0].canonical.Summary, tc.summary) {
				t.Fatalf("reported canonical error = %#v", failures[0].canonical)
			}
			events := fixture.waitForFailureEvent(t, 1)
			if events[0].Data["failure_class"] != "credential" ||
				events[0].Data["error_code"] != string(tc.code) {
				t.Fatalf("delivery_failed data = %#v", events[0].Data)
			}
		})
	}
}

func TestSlackFailureSurfacingRedaction(t *testing.T) {
	const secondSecret = "xoxb-second-secret-1234567890"
	logs := captureLogs(t)
	fixture := newFailureFixture(t, "1758499200.000001")
	fixture.harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Recipients[0].DisplayName = "#eng " + testToken + " " + secondSecret
	})
	record, err := fixture.notifier.recordFor("F-1")
	if err != nil {
		t.Fatal(err)
	}
	fixture.notifier.recordMu.Lock()
	entry := record.Destinations[fixture.key]
	entry.DisplayName = "#eng " + testToken + " " + secondSecret
	record.Destinations[fixture.key] = entry
	fixture.notifier.recordMu.Unlock()

	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{
			"ok":     false,
			"error":  "is_archived: " + testToken + " " + secondSecret,
			"needed": "chat:write " + testToken + " " + secondSecret,
		},
	})
	if err := fixture.worker.postReply(fixture.item); err == nil {
		t.Fatal("postReply() error = nil; want destination rejection")
	}
	fixture.waitForFailureEvent(t, 1)

	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{
			"ok":     false,
			"error":  "missing_scope",
			"needed": "chat:write " + testToken + " " + secondSecret,
		},
	})
	if err := fixture.worker.postReply(fixture.item); err == nil {
		t.Fatal("postReply() error = nil; want credential rejection")
	}
	fixture.waitForFailureEvent(t, 2)

	_, _ = sendWithRetry(
		fixture.worker,
		"thread reply",
		fixture.item,
		"progress",
		func() (struct{}, error) {
			return struct{}{}, &APIError{
				SlackError: "is_archived: " + testToken + " " + secondSecret,
			}
		},
	)
	fixture.waitForFailureEvent(t, 3)

	persisted, err := loadFeatureRecord(fixture.harness.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	failures, _ := fixture.reporter.snapshot()
	reported := make([]errcat.Error, 0, len(failures))
	for _, failure := range failures {
		reported = append(reported, failure.canonical)
	}
	encoded, err := json.Marshal(struct {
		Record   *featureRecord
		Events   []observe.Event
		Failures []errcat.Error
		Warnings []errcat.Error
		Logs     string
	}{
		Record:   persisted,
		Events:   fixture.harness.observer.all(),
		Failures: reported,
		Warnings: fixture.notifier.SlackWarnings("F-1"),
		Logs:     logs.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{testToken, secondSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("failure boundary leaked %q: %s", secret, text)
		}
	}
	for _, want := range []string{"is_archived", "chat:write", "#eng"} {
		if !strings.Contains(text, want) {
			t.Errorf("failure boundary output = %s; want %q", text, want)
		}
	}
}

func TestSlackFailureSurfacingEvidence(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir == "" {
		t.Skip("AGENTICO_EVIDENCE_DIR is not set")
	}
	const secondSecret = "xoxb-evidence-second-secret-123456"
	fixture := newFailureFixture(t, "1758499200.000001")
	featurePath := filepath.Join(fixture.harness.stateDir, "F-1", "feature.yaml")
	featureBefore, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatal(err)
	}

	for range 5 {
		fixture.harness.server.Script("chat.postMessage", testsupport.Response{
			Body: map[string]any{"ok": false, "error": "is_archived"},
		})
		_ = fixture.worker.postReply(fixture.item)
	}
	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{"ok": false, "error": "token_revoked"},
	})
	_ = fixture.worker.postReply(fixture.item)
	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{"ok": true, "ts": "1758499200.000002", "channel": "C-ENG"},
	})
	if err := fixture.worker.postReply(fixture.item); err != nil {
		t.Fatalf("self-healing write: %v", err)
	}
	fixture.harness.server.Script("chat.postMessage", testsupport.Response{
		Body: map[string]any{
			"ok":    false,
			"error": "is_archived: " + testToken + " " + secondSecret,
		},
	})
	_ = fixture.worker.postReply(fixture.item)
	fixture.waitForFailureEvent(t, 7)

	finalRecord, err := loadFeatureRecord(fixture.harness.stateDir, "F-1")
	if err != nil {
		t.Fatal(err)
	}
	featureAfter, err := os.ReadFile(featurePath)
	if err != nil {
		t.Fatal(err)
	}
	failures, successes := fixture.reporter.snapshot()
	reported := make([]errcat.Error, 0, len(failures))
	for _, failure := range failures {
		reported = append(reported, failure.canonical)
	}
	transcript := struct {
		Requests         []testsupport.Request `json:"requests"`
		DeliveryFailures []observe.Event       `json:"delivery_failures"`
		ReporterFailures []errcat.Error        `json:"reporter_failures"`
		ReporterSuccess  []time.Time           `json:"reporter_successes"`
		Warnings         []errcat.Error        `json:"warnings"`
		FinalRecord      *featureRecord        `json:"final_record"`
		FeatureUnchanged bool                  `json:"feature_unchanged"`
	}{
		Requests:         fixture.harness.server.AllRequests(),
		DeliveryFailures: fixture.harness.observer.ofKind("slack.delivery_failed"),
		ReporterFailures: reported,
		ReporterSuccess:  successes,
		Warnings:         fixture.notifier.SlackWarnings("F-1"),
		FinalRecord:      finalRecord,
		FeatureUnchanged: bytes.Equal(featureBefore, featureAfter),
	}
	encoded, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{testToken, secondSecret} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("evidence transcript leaked %q", secret)
		}
	}
	if !transcript.FeatureUnchanged {
		t.Fatal("feature record changed during Slack delivery failures")
	}
	target := filepath.Join(dir, "behaviors")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(target, "slack-failure-surfacing.json"),
		append(encoded, '\n'),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func TestSlackWarningsLoadsDurableFailuresAndFiltersRecipients(t *testing.T) {
	fixture := newFailureFixture(t, "1758499200.000001")
	firstFailure := time.Date(2026, time.September, 22, 10, 2, 0, 0, time.UTC)
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			fixture.key: {
				Kind:        "channel",
				SlackID:     "C-ENG",
				DisplayName: "#eng",
				ChannelID:   "C-ENG",
				RootTS:      "1758499200.000001",
				Failure: &destinationFailure{
					Code:          errcat.SlackRecipientNotNotified,
					SlackError:    "is_archived",
					Count:         5,
					FirstFailedAt: firstFailure,
					LastFailedAt:  firstFailure.Add(time.Hour),
				},
			},
			destinationKey("user", "U-REMOVED"): {
				Kind:        "user",
				SlackID:     "U-REMOVED",
				DisplayName: "Removed User",
				ChannelID:   "D-REMOVED",
				Failure: &destinationFailure{
					Code:          errcat.SlackDeliveryRetriesExhausted,
					SlackError:    "rate_limited",
					Count:         2,
					FirstFailedAt: firstFailure,
					LastFailedAt:  firstFailure,
				},
			},
		},
	}
	if err := persistFeatureRecord(fixture.harness.stateDir, "F-1", record); err != nil {
		t.Fatal(err)
	}

	cold := NewNotifier(NotifierOptions{
		Settings: fixture.harness.settings,
		Store:    fixture.harness.store,
		StateDir: fixture.harness.stateDir,
		Clock:    fixture.harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(fixture.harness.server.URL()))
		},
	})
	t.Cleanup(func() { cold.Stop(context.Background()) })
	warnings := cold.SlackWarnings("F-1")
	if len(warnings) != 1 {
		t.Fatalf("SlackWarnings() = %#v; want one configured destination", warnings)
	}
	warning := warnings[0]
	for _, want := range []string{
		"#eng", "is_archived", "5", firstFailure.Format(time.RFC3339),
	} {
		if !strings.Contains(warning.Summary, want) {
			t.Errorf("SlackWarnings()[0].Summary = %q; want %q", warning.Summary, want)
		}
	}

	fixture.harness.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Recipients = nil
	})
	if warnings := cold.SlackWarnings("F-1"); len(warnings) != 0 {
		t.Fatalf("SlackWarnings() after recipient removal = %#v; want none", warnings)
	}
}

func TestSlackSuccessfulWriteReportsEveryWritePoint(t *testing.T) {
	tests := []struct {
		name   string
		rootTS string
		script string
		run    func(*failureFixture) error
	}{
		{
			name: "root post", script: "chat.postMessage",
			run: func(f *failureFixture) error { return f.worker.ensureCard(f.item) },
		},
		{
			name: "reply", rootTS: "1758499200.000001", script: "chat.postMessage",
			run: func(f *failureFixture) error { return f.worker.postReply(f.item) },
		},
		{
			name: "update", rootTS: "1758499200.000001", script: "chat.update",
			run: func(f *failureFixture) error {
				f.worker.flushOne("F-1")
				return nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newFailureFixture(t, tc.rootTS)
			fixture.harness.server.Script(tc.script, testsupport.Response{
				Body: map[string]any{
					"ok": true, "ts": "1758499200.000002", "channel": "C-ENG",
				},
			})
			if err := tc.run(fixture); err != nil {
				t.Fatalf("%s write error = %v", tc.name, err)
			}
			failures, successes := fixture.reporter.snapshot()
			if len(failures) != 0 || len(successes) != 1 {
				t.Fatalf("reporter failures/successes = %d/%d; want 0/1", len(failures), len(successes))
			}
		})
	}
}

func TestSlackSilentCancellationDoesNotEmitDeliveryFailure(t *testing.T) {
	stopped := make(chan struct{})
	close(stopped)
	observer := &fakeObserver{}
	notifier := &Notifier{
		stopCh:     stopped,
		observer:   observer,
		dropEvents: make(chan observe.Event),
		clock:      newFakeClock(),
	}
	worker := &destinationWorker{notifier: notifier}
	_, _ = sendWithRetry(
		worker,
		"thread reply",
		workItem{kind: "channel"},
		"progress",
		func() (struct{}, error) {
			return struct{}{}, &APIError{SlackError: "is_archived"}
		},
	)
	if got := len(observer.ofKind("slack.delivery_failed")); got != 0 {
		t.Fatalf("delivery_failed events after stop = %d; want 0", got)
	}

	activeNotifier := &Notifier{
		stopCh:     make(chan struct{}),
		observer:   observer,
		dropEvents: make(chan observe.Event),
		clock:      newFakeClock(),
	}
	activeWorker := &destinationWorker{notifier: activeNotifier}
	_, _ = sendWithRetry(
		activeWorker,
		"thread reply",
		workItem{kind: "channel"},
		"progress",
		func() (struct{}, error) {
			return struct{}{}, errDeliveryIneligible
		},
	)
	if got := len(observer.ofKind("slack.delivery_failed")); got != 0 {
		t.Fatalf("delivery_failed events after ineligibility = %d; want 0", got)
	}
}

func repeatedResponses(count int, response testsupport.Response) []testsupport.Response {
	result := make([]testsupport.Response, count)
	for i := range result {
		result[i] = response
	}
	return result
}
