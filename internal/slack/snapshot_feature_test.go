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
	"reflect"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackAnswerGrammarLiveSnapshotIsFeatureScoped(t *testing.T) {
	h, n, answerPort := newPermissionResponderAdmissionFixture(t, nil, nil)
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.clock.(*gatedDeliveryClock).open()
	h.seedFeature("feature-2", nil)
	h.settings.mutate(func(settings *ports.SlackRuntimeSettings) {
		settings.Recipients = append(settings.Recipients, ports.SlackRecipient{
			TypedText: "#ops", Kind: ports.SlackRecipientChannel,
			ID: "C-OPS", DisplayName: "#ops",
		})
	})

	const requestID = "shared-request"
	first := ports.SlackPendingInput{
		Kind: ports.SlackPendingQuestion, RequestID: requestID, QuestionCount: 1,
		Options: []ports.SlackPendingInputOption{{Label: "First only"}, {Label: "First choice"}},
	}
	second := ports.SlackPendingInput{
		Kind: ports.SlackPendingQuestion, RequestID: requestID, QuestionCount: 2,
		Options: []ports.SlackPendingInputOption{{Label: "Second choice"}},
	}
	third := ports.SlackPendingInput{
		Kind: ports.SlackPendingQuestion, RequestID: requestID, QuestionIndex: 1, QuestionCount: 2,
		Options: []ports.SlackPendingInputOption{{Label: "Not this"}, {Label: "Final choice"}},
	}
	h.pending.set("feature-1", first)
	h.pending.set("feature-2", second, third)

	firstRecord := n.records["feature-1"]
	firstRecord.Pending = []pendingInputRecord{{
		Identity: "question:shared-request:0", SourceFeatureID: "feature-1",
		Kind: "question", RequestID: requestID, Tag: "#1",
		MessageTS: map[string]string{"channel:C-ENG": "100.000002"},
	}}
	d := firstRecord.Destinations["channel:C-ENG"]
	d.PostingIndex[0].Identity = firstRecord.Pending[0].Identity
	firstRecord.Destinations["channel:C-ENG"] = d

	const secondRoot = "200.000001"
	secondRecord := &featureRecord{
		Version: recordVersion,
		Destinations: map[string]destinationRecord{
			"channel:C-OPS": {
				Kind: "channel", SlackID: "C-OPS", ChannelID: "C-OPS", RootTS: secondRoot,
				Ledger: []string{secondRoot, "200.000002", "200.000003"},
				PostingIndex: []postingIndexEntry{
					{Identity: "question:shared-request:0", MessageTS: "200.000002", Tag: "#1"},
					{Identity: "question:shared-request:1", MessageTS: "200.000003", Tag: "#2"},
				},
			},
		},
		Pending: []pendingInputRecord{
			{
				Identity: "question:shared-request:0", SourceFeatureID: "feature-2",
				Kind: "question", RequestID: requestID, Tag: "#1",
				MessageTS: map[string]string{"channel:C-OPS": "200.000002"},
			},
			{
				Identity: "question:shared-request:1", SourceFeatureID: "feature-2",
				Kind: "question", RequestID: requestID, QuestionIndex: 1, Tag: "#2",
				MessageTS: map[string]string{"channel:C-OPS": "200.000003"},
			},
		},
	}
	if err := persistFeatureRecord(h.stateDir, "feature-2", secondRecord); err != nil {
		t.Fatal(err)
	}
	n.records["feature-2"] = secondRecord

	h.server.SeedThread("C-ENG", "100.000001", []testsupport.Message{
		{TS: "100.000001"},
		{TS: "100.000002", ThreadTS: "100.000001", Text: "first question"},
		{TS: "100.000004", ThreadTS: "100.000001", User: "U-ADA", Text: "2"},
	})
	h.server.SeedThread("C-OPS", secondRoot, []testsupport.Message{
		{TS: secondRoot},
		{TS: "200.000002", ThreadTS: secondRoot, Text: "second question",
			Reactions: []testsupport.Reaction{{Name: "one", Count: 1, Users: []string{"U-ADA"}}}},
		{TS: "200.000003", ThreadTS: secondRoot, Text: "third question"},
		{TS: "200.000005", ThreadTS: secondRoot, User: "U-ADA", Text: "#2 2"},
	})

	n.responderTick()

	answerPort.mu.Lock()
	submissions := append([]ports.SlackQuestionAnswer(nil), answerPort.questionAnswers...)
	answerPort.mu.Unlock()
	if len(submissions) != 2 {
		t.Fatalf("question submissions = %+v; want both features answered", submissions)
	}
	byFeature := make(map[string][]ports.SlackQuestionIndexedAnswer)
	for _, submission := range submissions {
		byFeature[submission.SourceFeatureID] = submission.Answers
	}
	for featureID, want := range map[string][]ports.SlackQuestionIndexedAnswer{
		"feature-1": {{Index: 0, Value: "First choice"}},
		"feature-2": {{Index: 0, Value: "Second choice"}, {Index: 1, Value: "Final choice"}},
	} {
		if !reflect.DeepEqual(byFeature[featureID], want) {
			t.Errorf("%s answers = %+v; want %+v", featureID, byFeature[featureID], want)
		}
	}
	waitFor(t, time.Second, func() bool {
		return h.server.CallCount("chat.postMessage") >= 4 &&
			h.server.CallCount("reactions.add") >= 2 && n.queue.len() == 0
	})
}
