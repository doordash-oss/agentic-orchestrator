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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackResponderRedactionPollingErrorBoundary(t *testing.T) {
	const (
		featureID    = "F-POLL-REDACTION"
		channelID    = "C-POLL-REDACTION"
		token        = "xoxb-responder-poll-sentinel-1234567890"
		secondSecret = "https://alice:poll-secret@example.com/private"
		slackCode    = "channel_not_found"
	)
	logs := captureLogs(t)
	recipient := ports.SlackRecipient{
		TypedText:   "#poll-redaction",
		Kind:        ports.SlackRecipientChannel,
		ID:          channelID,
		DisplayName: "#poll-redaction " + token + " " + secondSecret,
	}
	settings := defaultTestSettings(token, recipient)
	harness := newNotifierHarness(t, settings)
	harness.seedFeature(featureID, nil)

	key := destinationKey(string(recipient.Kind), recipient.ID)
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			key: {
				Kind:        string(recipient.Kind),
				SlackID:     recipient.ID,
				DisplayName: recipient.DisplayName,
				ChannelID:   channelID,
				RootTS:      "100.000001",
				Ledger:      []string{"100.000001", "100.000002"},
				PostingIndex: []postingIndexEntry{{
					Identity:  "permission:request-1",
					MessageTS: "100.000002",
					Tag:       "#1",
				}},
			},
		},
		Pending: []pendingInputRecord{{
			Identity:        "permission:request-1",
			SourceFeatureID: featureID,
			Kind:            string(ports.SlackPendingPermission),
			RequestID:       "request-1",
			Tag:             "#1",
			MessageTS:       map[string]string{key: "100.000002"},
		}},
	}
	if err := persistFeatureRecord(harness.stateDir, featureID, record); err != nil {
		t.Fatal(err)
	}
	harness.pending.setFromRecord(featureID, record)
	harness.server.Script("conversations.replies", testsupport.Response{
		Body: map[string]any{
			"ok":     false,
			"error":  slackCode + ": " + token + " " + secondSecret,
			"needed": "channels:history " + token + " " + secondSecret,
		},
	})

	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings,
		Store:    harness.store,
		StateDir: harness.stateDir,
		Observer: harness.observer,
		Pending:  harness.pending,
		Answer:   &fakeSlackAnswerPort{},
		Clock:    harness.clock,
		Jitter:   func() float64 { return 0 },
		NewClient: func(candidateToken string) (slackClient, error) {
			return NewClient(candidateToken, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.records[featureID] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()
	waitFor(t, time.Second, func() bool {
		return len(harness.observer.ofKind("slack.delivery_failed")) == 1
	})

	warnings := notifier.SlackWarnings(featureID)
	if len(warnings) != 1 {
		t.Fatalf("SlackWarnings() = %#v; want one polling failure warning", warnings)
	}
	if warnings[0].Code != errcat.SlackRecipientNotNotified ||
		!strings.Contains(warnings[0].Summary, slackCode) {
		t.Errorf("SlackWarnings()[0] = %#v; want preserved %q code", warnings[0], slackCode)
	}

	events := harness.observer.ofKind("slack.delivery_failed")
	if got := events[0].Data["slack_error"]; !strings.Contains(got.(string), slackCode) {
		t.Errorf("slack.delivery_failed slack_error = %#v; want preserved %q code", got, slackCode)
	}
	if got := events[0].Data["error_code"]; got != string(errcat.SlackRecipientNotNotified) {
		t.Errorf("slack.delivery_failed error_code = %#v; want %q", got, errcat.SlackRecipientNotNotified)
	}

	persisted, err := loadFeatureRecord(harness.stateDir, featureID)
	if err != nil {
		t.Fatal(err)
	}
	failure := persisted.Destinations[key].Failure
	if failure == nil || !strings.Contains(failure.SlackError, slackCode) {
		t.Fatalf("persisted polling failure = %#v; want preserved %q code", failure, slackCode)
	}

	requests := harness.server.Requests("conversations.replies")
	if len(requests) != 1 {
		t.Fatalf("conversations.replies requests = %d; want one", len(requests))
	}
	if requests[0].Fields["channel"] != channelID ||
		requests[0].Fields["ts"] != "100.000001" ||
		requests[0].Fields["oldest"] != "100.000002" {
		t.Errorf("conversations.replies request fields = %#v; want polling request", requests[0].Fields)
	}

	transcript, err := json.Marshal(struct {
		Warnings []errcat.Error        `json:"warnings"`
		Events   []observe.Event       `json:"events"`
		Record   *featureRecord        `json:"record"`
		Logs     string                `json:"logs"`
		Requests []testsupport.Request `json:"requests"`
	}{
		Warnings: warnings,
		Events:   events,
		Record:   persisted,
		Logs:     logs.String(),
		Requests: harness.server.AllRequests(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, secondSecret, "alice:poll-secret"} {
		if bytes.Contains(transcript, []byte(secret)) {
			t.Fatalf("polling error boundary leaked %q: %s", secret, transcript)
		}
	}
	for _, marker := range []string{
		slackCode,
		string(errcat.SlackRecipientNotNotified),
		"[REDACTED]",
		`"BearerPresent":true`,
	} {
		if !bytes.Contains(transcript, []byte(marker)) {
			t.Errorf("polling error transcript = %s; want marker %q", transcript, marker)
		}
	}
}
