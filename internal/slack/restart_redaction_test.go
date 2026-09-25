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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackRestartRedactionPersistedNamesErrorsAndResponder(t *testing.T) {
	const (
		id        = "F-restart-redaction"
		token     = "xoxp-restart-sentinel-654321"
		secondary = "xoxb-restart-secondary-987654"
		root      = "1758499200.000001"
		reviewTS  = "1758499200.000002"
		replyTS   = "1758499200.000003"
		key       = "channel:C-ENG"
	)
	logs := captureLogs(t)
	h := newNotifierHarness(t, defaultTestSettings(token, testRecipients()[1]))
	h.seedFeature(id, nil)
	h.server.SetOwnUserID("U-ADA")
	responder, _ := defaultOKResponder()
	h.server.SetDefault(func(method string, req testsupport.Request) testsupport.Response {
		if method == "users.info" {
			return responderEvidenceUserInfo("U-GRACE", "Grace Hopper "+token+" "+secondary)
		}
		return responder(method, req)
	})
	record := &featureRecord{Version: recordVersion, TagCounter: 1,
		Destinations: map[string]destinationRecord{key: {
			Kind: "channel", SlackID: "C-ENG", ChannelID: "C-ENG",
			DisplayName: "#eng " + token + " " + secondary, RootTS: root,
			Ledger: []string{root, reviewTS},
			Failure: &destinationFailure{
				SlackError: "channel_not_found " + token + " " + secondary, Count: 1,
			},
			PostingIndex: []postingIndexEntry{{
				Identity: "review:review-1:sha256:first", MessageTS: reviewTS, Tag: "#1",
			}},
		}},
		Pending: []pendingInputRecord{{
			Identity: "review:review-1:sha256:first", SourceFeatureID: id,
			Kind: string(ports.SlackPendingReview), ReviewID: "review-1",
			ReviewMode: "plan", TargetPhase: "implement", SourceRevision: "sha256:first",
			ArtifactID: "plan-artifact", RunNumber: 1, Tag: "#1",
			MessageTS: map[string]string{key: reviewTS},
		}},
		Resolved: []pendingInputRecord{{
			Identity: "permission:expired", SourceFeatureID: id,
			Kind: string(ports.SlackPendingPermission), RequestID: "expired",
			Tag: "#2", MessageTS: map[string]string{key: "1758499200.000000"},
			Resolution: &postingResolution{Kind: resolutionCleared},
		}},
	}
	if err := persistFeatureRecord(h.stateDir, id, record); err != nil {
		t.Fatal(err)
	}
	h.pending.set(id, ports.SlackPendingInput{
		Kind: ports.SlackPendingReview, FeatureID: id, ReviewID: "review-1",
		ReviewMode: "plan", TargetPhase: "implement", SourceRevision: "sha256:first",
		ArtifactID: "plan-artifact", RunNumber: 1,
	})
	const seededReplyText = " APPROVE!!! "
	h.server.SeedThread("C-ENG", root, []testsupport.Message{
		{TS: root, ThreadTS: root, User: "U-ADA", Text: "Root card"},
		{TS: reviewTS, ThreadTS: root, User: "U-ADA", Text: "#1 review"},
		{TS: replyTS, ThreadTS: root, User: "U-GRACE", Text: seededReplyText},
	})
	answers := &responderEvidenceAnswerPort{}
	clock := newManualResponderClock()
	n := NewNotifier(NotifierOptions{
		Settings: h.settings, Store: h.store, StateDir: h.stateDir,
		Observer: h.observer, Pending: h.pending, Answer: answers,
		Clock: h.clock, ResponderClock: clock, QueueCapacity: 16,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(h.server.URL()))
		},
	})
	n.Start()
	t.Cleanup(func() { n.Stop(context.Background()) })
	n.SetServerName("Restart redaction")
	n.SignalReady()
	waitFor(t, 5*time.Second, func() bool {
		return n.startupDone.Load() && restartEvidencePostCount(h.server, "#2 is no longer pending.") == 1
	})
	clock.tick(t)
	waitFor(t, 5*time.Second, func() bool {
		return len(answers.all()) == 1 && h.server.CallCount("users.info") == 1 &&
			restartEvidencePostCount(h.server, "#1 was approved by <@U-GRACE> via Slack.") == 1
	})
	loaded, err := os.ReadFile(recordPath(h.stateDir, id))
	if err != nil {
		t.Fatal(err)
	}
	requests, err := json.Marshal(h.server.AllRequests())
	if err != nil {
		t.Fatal(err)
	}
	events, err := json.Marshal(h.observer.all())
	if err != nil {
		t.Fatal(err)
	}
	submissions, err := json.Marshal(answers.all())
	if err != nil {
		t.Fatal(err)
	}
	surfaces := map[string][]byte{
		"requests": requests, "logs": []byte(logs.String()),
		"events": events, "record": loaded, "answer source": submissions,
	}
	for name, data := range surfaces {
		for _, secret := range []string{token, secondary, seededReplyText} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s leaked %q", name, secret)
			}
		}
	}
	if len(answers.all()) != 1 || answers.all()[0].Source.Responder == "" ||
		!strings.Contains(string(submissions), "Grace Hopper") {
		t.Errorf("answer source lost harmless responder name: %s", submissions)
	}
	if restartEvidencePostCount(h.server, "#1 was approved by <@U-GRACE> via Slack.") != 1 ||
		restartEvidencePostCount(h.server, "#2 is no longer pending.") != 1 ||
		!bytes.Contains(requests, []byte("#1")) {
		t.Errorf("scrubbing lost mention, tag, or closure: %s", requests)
	}
}
