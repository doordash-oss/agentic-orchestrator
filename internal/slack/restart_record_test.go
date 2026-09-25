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
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
	"gopkg.in/yaml.v3"
)

func TestSlackRestartRecordFieldsRoundTripAndLegacyLoad(t *testing.T) {
	stateDir := t.TempDir()
	const featureID = "feature-1"
	const key = "channel:C-ENG"
	legacy := []byte("version: 1\ndestinations:\n  channel:C-ENG:\n    kind: channel\n    slack_id: C-ENG\n    root_ts: \"100.000001\"\n")
	if err := os.MkdirAll(stateDir+"/"+featureID, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath(stateDir, featureID), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := loadFeatureRecord(stateDir, featureID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Destinations[key].LastSeenReplyTS != "" {
		t.Fatal("legacy mark must be empty")
	}
	record.Destinations[key] = destinationRecord{
		Kind: "channel", SlackID: "C-ENG", LastSeenReplyTS: "100.000004",
	}
	record.Resolved = []pendingInputRecord{{
		Identity: "permission:one", MessageTS: map[string]string{key: "100.000002"},
		Resolution: &postingResolution{Kind: resolutionAgentico, ClosureAcknowledged: map[string]bool{key: true}},
	}}
	if err := persistFeatureRecord(stateDir, featureID, record); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadFeatureRecord(stateDir, featureID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Destinations[key].LastSeenReplyTS != "100.000004" ||
		reloaded.Resolved[0].closureOwed(key) {
		t.Fatalf("round trip = %#v; want watermark and acknowledged closure", reloaded)
	}
	if reloaded.Resolved[0].closureOwed("channel:C-OTHER") {
		t.Fatal("unposted destination must not owe closure")
	}
	var previousReader struct {
		Version      int                       `yaml:"version"`
		Destinations map[string]map[string]any `yaml:"destinations"`
		Resolved     []map[string]any          `yaml:"resolved_inputs"`
	}
	data, err := os.ReadFile(recordPath(stateDir, featureID))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &previousReader); err != nil {
		t.Fatalf("older reader could not load additive fields: %v", err)
	}
	if previousReader.Version != recordVersion || strings.Contains(string(data), ".slack-") {
		t.Fatalf("unexpected record encoding: %s", data)
	}
}

func TestSlackRestartReplyMarkStopsBeforeDeferredFeedback(t *testing.T) {
	harness, notifier, _ := newPermissionResponderAdmissionFixture(t,
		[]testsupport.Message{
			{TS: "100.000003", ThreadTS: "100.000001", User: "U-ADA", Text: "not an answer"},
			{TS: "100.000004", ThreadTS: "100.000001", User: "U-ADA", Text: "not an answer"},
		}, nil)
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	notifier.responderFeedbackMu.Lock()
	notifier.responderFeedback["C-ENG"] = responderFeedbackLimit - 2
	notifier.responderFeedbackMu.Unlock()
	notifier.responderTick()
	record, err := loadFeatureRecord(harness.stateDir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := record.Destinations["channel:C-ENG"].LastSeenReplyTS; got != "100.000003" {
		t.Fatalf("last seen with deferred reply = %q; want 100.000003", got)
	}
	// The in-memory feedback cap disappears on reload; only the first reply is skipped.
	clock := notifier.clock.(*gatedDeliveryClock)
	clock.open()
	reloaded := harness.newNotifier(8)
	t.Cleanup(func() { reloaded.Stop(context.Background()) })
	if _, err := reloaded.recordFor("feature-1"); err != nil {
		t.Fatal(err)
	}
	reloaded.responderTick()
	if got := len(harness.observer.ofKind("slack.answer_rejected")); got != 2 {
		t.Fatalf("judged replies across reload = %d; want 2", got)
	}
	if got := fieldString(harness.server.Requests("conversations.replies")[1], "oldest"); got != "100.000002" {
		t.Fatalf("poll oldest = %q; want retained item timestamp", got)
	}
}

func TestSlackRestartClosureAcknowledgedOnDeliveryOnly(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings("xoxb-restart",
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-OPS", DisplayName: "#ops"},
	))
	owner := harness.seedFeature("feature-1", nil)
	const key = "channel:C-ENG"
	const other = "channel:C-OPS"
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		key: {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "100.000001",
			PostingIndex: []postingIndexEntry{{Identity: "permission:one", MessageTS: "100.000002", Tag: "#1"}}},
		other: {Kind: "channel", SlackID: "C-OPS", ChannelID: "C-OPS", RootTS: "200.000001",
			PostingIndex: []postingIndexEntry{{Identity: "permission:one", MessageTS: "200.000002", Tag: "#1"}}},
	}, Pending: []pendingInputRecord{{
		Identity: "permission:one", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
		MessageTS: map[string]string{key: "100.000002", other: "200.000002"},
	}, {
		Identity: "permission:two", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "two", Tag: "#2",
		MessageTS: map[string]string{key: "100.000003"},
	}}}
	if err := persistFeatureRecord(harness.stateDir, owner.ID, record); err != nil {
		t.Fatal(err)
	}
	notifier := harness.newNotifier(8)
	notifier.records[owner.ID] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	harness.pending.set(owner.ID, ports.SlackPendingInput{FeatureID: owner.ID,
		Kind: ports.SlackPendingPermission, RequestID: "two"})
	work := notifier.reconcilePending(harness.settings.SlackSettings(), owner, owner, record, resolutionAgentico)
	var closures []workItem
	for _, item := range work {
		if item.reply.closure {
			closures = append(closures, item)
		}
	}
	if len(closures) != 2 || closures[0].reply.identity != "permission:one" {
		t.Fatalf("closure work = %#v; want identified closure", work)
	}
	if len(record.Resolved) != 1 ||
		!record.Resolved[0].closureOwed(key) || !record.Resolved[0].closureOwed(other) {
		t.Fatal("queued closure must remain owed")
	}
	// The resolution on the posting index remains durable when retention is released.
	if record.Destinations[key].PostingIndex[0].Resolution == nil ||
		record.Destinations[key].PostingIndex[0].Resolution.ClosureAcknowledged[key] {
		t.Fatal("closure acknowledged before delivery")
	}
	harness.server.Script("chat.postMessage", testsupport.Response{
		Status: http.StatusOK, Body: map[string]any{"ok": false, "error": "channel_not_found"},
	})
	failed := notifier.workerFor("C-OPS")
	failed.mu.Lock()
	failed.lastWrite = harness.clock.Now().Add(-time.Second)
	failed.mu.Unlock()
	if err := failed.postReply(closures[1]); err == nil {
		t.Fatal("failed closure post must return an error")
	}
	worker := notifier.workerFor("C-ENG")
	worker.mu.Lock()
	worker.lastWrite = harness.clock.Now().Add(-time.Second)
	worker.mu.Unlock()
	if err := worker.postReply(closures[0]); err != nil {
		t.Fatal(err)
	}
	if record.Destinations[key].PostingIndex[0].Resolution.ClosureAcknowledged[key] != true {
		t.Fatal("closure not acknowledged after post")
	}
	reloaded, err := loadFeatureRecord(harness.stateDir, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Resolved[0].closureOwed(key) || !reloaded.Resolved[0].closureOwed(other) ||
		reloaded.Destinations[other].PostingIndex[0].Resolution.ClosureAcknowledged[other] {
		t.Fatalf("closure debts after reload = %#v; want only OPS owed", reloaded.Resolved[0])
	}
	if got := harness.server.CallCount("chat.postMessage"); got != 2 {
		t.Fatalf("closure attempts = %d; want one per destination", got)
	}
}

func TestSlackRestartLastRetiredItemKeepsOwedClosure(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings("xoxb-restart",
		ports.SlackRecipient{Kind: ports.SlackRecipientChannel, ID: "C-ENG", DisplayName: "#eng"},
	))
	owner := harness.seedFeature("feature-1", nil)
	const key = "channel:C-ENG"
	record := &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{
		key: {Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: "100.000001"},
	}, Pending: []pendingInputRecord{{
		Identity: "permission:one", SourceFeatureID: owner.ID,
		Kind: string(ports.SlackPendingPermission), RequestID: "one", Tag: "#1",
		MessageTS: map[string]string{key: "100.000002"},
	}}}
	notifier := harness.newNotifier(8)
	notifier.records[owner.ID] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	work := notifier.reconcilePending(harness.settings.SlackSettings(), owner, owner, record, resolutionAgentico)
	if len(work) == 0 || !work[0].reply.closure {
		t.Fatalf("closure work = %#v; want closure", work)
	}
	loaded, err := loadFeatureRecord(harness.stateDir, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Pending) != 0 || len(loaded.Resolved) != 1 ||
		!loaded.Resolved[0].closureOwed(key) {
		t.Fatalf("last retired item = %#v; want durable owed closure", loaded)
	}
}
