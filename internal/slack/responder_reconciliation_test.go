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
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackResponderReconcilesAgenticoResolutionBeforePolling(t *testing.T) {
	harness := newNotifierHarness(t, defaultTestSettings(
		"xoxb-responder",
		ports.SlackRecipient{
			TypedText: "#eng", Kind: ports.SlackRecipientChannel,
			ID: "C-ENG", DisplayName: "#eng",
		},
	))
	harness.seedFeature("feature-1", nil)
	const (
		destinationKey = "channel:C-ENG"
		rootTS         = "100.000001"
		firstTS        = "100.000002"
		secondTS       = "100.000003"
	)
	first := pendingInputRecord{
		Identity: "permission:request-1", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: "request-1",
		Tag: "#1", MessageTS: map[string]string{destinationKey: firstTS},
	}
	second := pendingInputRecord{
		Identity: "permission:request-2", SourceFeatureID: "feature-1",
		Kind: string(ports.SlackPendingPermission), RequestID: "request-2",
		Tag: "#2", MessageTS: map[string]string{destinationKey: secondTS},
	}
	record := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			destinationKey: {
				Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG", RootTS: rootTS,
				Ledger: []string{rootTS, firstTS, secondTS},
				PostingIndex: []postingIndexEntry{
					{Identity: first.Identity, MessageTS: firstTS, Tag: first.Tag},
					{Identity: second.Identity, MessageTS: secondTS, Tag: second.Tag},
				},
			},
		},
		Pending: []pendingInputRecord{first, second},
	}
	if err := persistFeatureRecord(harness.stateDir, "feature-1", record); err != nil {
		t.Fatal(err)
	}
	harness.pending.set("feature-1", ports.SlackPendingInput{
		FeatureID: "feature-1", Kind: ports.SlackPendingPermission,
		RequestID: "request-2",
	})
	harness.server.SeedThread("C-ENG", rootTS, []testsupport.Message{
		{TS: rootTS},
		{
			TS: firstTS, ThreadTS: rootTS, Text: "first permission",
			Reactions: []testsupport.Reaction{{
				Name: "white_check_mark", Count: 1, Users: []string{"U-LATE"},
			}},
		},
		{TS: secondTS, ThreadTS: rootTS, Text: "second permission"},
	})
	answerPort := &fakeSlackAnswerPort{}
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.records["feature-1"] = record
	t.Cleanup(func() { notifier.Stop(context.Background()) })

	notifier.responderTick()

	if submissions := answerPort.permissionSubmissions(); len(submissions) != 0 {
		t.Fatalf("permission submissions = %#v; want no late-reaction mutation", submissions)
	}
	current, err := loadFeatureRecord(harness.stateDir, "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Pending) != 1 || current.Pending[0].Identity != second.Identity {
		t.Fatalf("pending = %#v; want only #2 live", current.Pending)
	}
	if len(current.Resolved) != 1 ||
		current.Resolved[0].Identity != first.Identity ||
		current.Resolved[0].Resolution == nil ||
		current.Resolved[0].Resolution.Kind != resolutionAgentico {
		t.Fatalf("resolved = %#v; want #1 retained as Agentico-resolved", current.Resolved)
	}
	waitFor(t, time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == 2
	})
	var closure, late bool
	for _, request := range harness.server.Requests("chat.postMessage") {
		switch fieldString(request, "text") {
		case "#1 was resolved in Agentico.":
			closure = true
		case "#1 was already answered by Agentico.":
			late = true
		}
	}
	if !closure || !late {
		t.Fatalf("closure=%t late=%t; want both Agentico resolution lines", closure, late)
	}
}
