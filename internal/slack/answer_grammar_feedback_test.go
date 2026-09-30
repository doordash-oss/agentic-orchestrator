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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

func TestSlackAnswerGrammarFailedBundleFeedbackDeferredAtCap(t *testing.T) {
	const root = "100.000001"
	messages := make([]testsupport.Message, 0, 12)
	for i := 0; i < responderFeedbackLimit/2; i++ {
		messages = append(messages, testsupport.Message{
			TS: fmt.Sprintf("100.%06d", i+4), ThreadTS: root, User: "U-ADA", Text: "#1 7",
		})
	}
	messages = append(messages,
		testsupport.Message{TS: "100.000020", ThreadTS: root, User: "U-ADA", Text: "#1 1"},
		testsupport.Message{TS: "100.000021", ThreadTS: root, User: "U-BOB", Text: "#2 2"},
	)
	h, n, port := newPermissionResponderAdmissionFixture(t, messages, nil)
	clock := n.clock.(*gatedDeliveryClock)
	t.Cleanup(func() {
		select {
		case <-clock.release:
		default:
			clock.open()
		}
		n.Stop(context.Background())
	})
	record := n.records["feature-1"]
	record.Pending = []pendingInputRecord{
		{Identity: "question:ask:0", SourceFeatureID: "feature-1", Kind: "question",
			RequestID: "ask", Tag: "#1", MessageTS: map[string]string{"channel:C-ENG": "100.000002"}},
		{Identity: "question:ask:1", SourceFeatureID: "feature-1", Kind: "question",
			RequestID: "ask", QuestionIndex: 1, Tag: "#2", MessageTS: map[string]string{"channel:C-ENG": "100.000003"}},
	}
	d := record.Destinations["channel:C-ENG"]
	d.PostingIndex[0].Identity = record.Pending[0].Identity
	d.PostingIndex = append(d.PostingIndex, postingIndexEntry{Identity: record.Pending[1].Identity, Tag: "#2", MessageTS: "100.000003"})
	d.Ledger = append(d.Ledger, "100.000003")
	record.Destinations["channel:C-ENG"] = d
	h.pending.set("feature-1",
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "ask", QuestionCount: 2,
			Options: []ports.SlackPendingInputOption{{Label: "First"}, {Label: "Second"}}},
		ports.SlackPendingInput{Kind: ports.SlackPendingQuestion, RequestID: "ask", QuestionIndex: 1,
			QuestionCount: 2, Options: []ports.SlackPendingInputOption{{Label: "First"}, {Label: "Second"}}},
	)
	port.questionResults = []ports.SlackAnswerResult{
		{Outcome: ports.SlackAnswerFailed, Cause: errors.New("temporary mutation failure")},
	}
	h.server.SeedThread("C-ENG", root, append([]testsupport.Message{
		{TS: root}, {TS: "100.000002", ThreadTS: root}, {TS: "100.000003", ThreadTS: root},
	}, messages...))

	n.responderTick()
	port.mu.Lock()
	if len(port.questionAnswers) != 1 {
		t.Fatalf("question submissions = %d; want 1", len(port.questionAnswers))
	}
	port.mu.Unlock()
	if record.Pending[0].HeldAnswer == nil || record.Pending[1].HeldAnswer != nil {
		t.Fatalf("held answers after failure: %+v", record.Pending)
	}
	n.responderFeedbackMu.Lock()
	deferred := len(n.responderDeferred)
	n.responderFeedbackMu.Unlock()
	if deferred != 1 {
		t.Fatalf("deferred feedback = %d; want 1", deferred)
	}
	if got := len(h.observer.ofKind("slack.answer_rejected")); got != responderFeedbackLimit/2 {
		t.Fatalf("rejections before capacity recovers = %d; want %d", got, responderFeedbackLimit/2)
	}
	clock.open()
	waitFor(t, 2*time.Second, func() bool { return n.responderFeedbackOutstanding("C-ENG") == 0 })
	n.responderTick()
	waitFor(t, 2*time.Second, func() bool {
		return len(h.observer.ofKind("slack.answer_rejected")) == responderFeedbackLimit/2+1
	})
	waitFor(t, 2*time.Second, func() bool {
		for _, request := range h.server.Requests("chat.postMessage") {
			if strings.Contains(fieldString(request, "text"), "#2 could not be submitted") {
				return true
			}
		}
		return false
	})
	n.responderTick()
	port.mu.Lock()
	submissions := len(port.questionAnswers)
	port.mu.Unlock()
	if submissions != 1 {
		t.Fatalf("failed reply resubmitted: %d calls", submissions)
	}
	count := 0
	for _, request := range h.server.Requests("chat.postMessage") {
		if strings.Contains(fieldString(request, "text"), "#2 could not be submitted") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("failure warnings = %d; want 1", count)
	}
	if got := len(h.observer.ofKind("slack.answer_rejected")); got != responderFeedbackLimit/2+1 {
		t.Fatalf("rejection events = %d; want %d", got, responderFeedbackLimit/2+1)
	}
}

func TestSlackAnswerGrammarRetiredReactionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		want       int
	}{
		{name: "question keycap", kind: "question", want: 1},
		{name: "help is silent", kind: "help", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGrammarLifecycle(t, 2)
			record := g.n.records["feature-1"]
			retired := record.Pending[0]
			retired.Kind = tc.kind
			retired.Resolution = &postingResolution{Kind: resolutionSlack, ResponderID: "U-ADA"}
			record.Pending = record.Pending[1:]
			record.Resolved = []pendingInputRecord{retired}
			g.inputs = g.inputs[1:]
			g.messages[1].Reactions = []testsupport.Reaction{
				{Name: "one", Count: 1, Users: []string{"U-BOB"}},
				{Name: "white_check_mark", Count: 1, Users: []string{"U-BOB"}},
				{Name: "x", Count: 1, Users: []string{"U-BOB"}},
			}
			g.sync()
			g.n.responderTick()
			g.drain(t)
			if got := len(g.h.observer.ofKind("slack.answer_rejected")); got != tc.want {
				t.Fatalf("rejections for retired %s = %d; want %d", tc.kind, got, tc.want)
			}
			if got := len(g.submissions()); got != 0 {
				t.Fatalf("retired reaction submitted answer: %+v", g.submissions())
			}
			g.n.responderTick()
			if got := len(g.h.observer.ofKind("slack.answer_rejected")); got != tc.want {
				t.Fatalf("duplicate rejections for retired %s = %d; want %d", tc.kind, got, tc.want)
			}
		})
	}
}
