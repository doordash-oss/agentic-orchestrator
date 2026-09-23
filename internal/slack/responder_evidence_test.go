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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/slack/testsupport"
)

const (
	responderEvidenceFeatureID   = "F-responder-evidence"
	responderEvidenceOwnerID     = "U-ADA"
	responderEvidencePeerID      = "U-GRACE"
	responderEvidenceUserKey     = "user:U-ADA"
	responderEvidenceChannelKey  = "channel:C-ENG"
	responderEvidenceUserChannel = "D-U-ADA"
	responderEvidenceChannel     = "C-ENG"
)

type responderEvidenceSubmission struct {
	Kind            string                        `json:"kind"`
	RequestID       string                        `json:"request_id,omitempty"`
	ReviewID        string                        `json:"review_id,omitempty"`
	SourceFeatureID string                        `json:"source_feature_id"`
	SourceRevision  string                        `json:"source_revision,omitempty"`
	Decision        ports.SlackPermissionDecision `json:"decision,omitempty"`
	Source          ports.AnswerSource            `json:"source"`
	Outcome         ports.SlackAnswerOutcome      `json:"outcome"`
}

type responderEvidenceAnswerPort struct {
	mu                sync.Mutex
	permissionResults []ports.SlackAnswerResult
	reviewResults     []ports.SlackAnswerResult
	submissions       []responderEvidenceSubmission
}

func (p *responderEvidenceAnswerPort) AnswerSlackPermission(
	answer ports.SlackPermissionAnswer,
) ports.SlackAnswerResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
	if len(p.permissionResults) > 0 {
		result = p.permissionResults[0]
		p.permissionResults = p.permissionResults[1:]
	}
	p.submissions = append(p.submissions, responderEvidenceSubmission{
		Kind:            "permission",
		RequestID:       answer.RequestID,
		SourceFeatureID: answer.SourceFeatureID,
		Decision:        answer.Decision,
		Source:          answer.Source,
		Outcome:         result.Outcome,
	})
	return result
}

func (p *responderEvidenceAnswerPort) ApproveSlackReview(
	approval ports.SlackReviewApproval,
) ports.SlackAnswerResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := ports.SlackAnswerResult{Outcome: ports.SlackAnswerAccepted}
	if len(p.reviewResults) > 0 {
		result = p.reviewResults[0]
		p.reviewResults = p.reviewResults[1:]
	}
	p.submissions = append(p.submissions, responderEvidenceSubmission{
		Kind:            "review",
		ReviewID:        approval.ReviewID,
		SourceFeatureID: approval.SourceFeatureID,
		SourceRevision:  approval.SourceRevision,
		Source:          approval.Source,
		Outcome:         result.Outcome,
	})
	return result
}

func (p *responderEvidenceAnswerPort) all() []responderEvidenceSubmission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]responderEvidenceSubmission(nil), p.submissions...)
}

type responderEvidenceRequest struct {
	Index                    int              `json:"index"`
	Method                   string           `json:"method"`
	HTTPMethod               string           `json:"http_method"`
	Channel                  string           `json:"channel,omitempty"`
	ThreadOrMessageTimestamp string           `json:"thread_or_message_timestamp,omitempty"`
	Oldest                   string           `json:"oldest,omitempty"`
	Cursor                   string           `json:"cursor,omitempty"`
	ReactionName             string           `json:"reaction_name,omitempty"`
	ReplyBroadcast           string           `json:"reply_broadcast,omitempty"`
	FallbackText             string           `json:"fallback_text,omitempty"`
	Blocks                   []map[string]any `json:"blocks,omitempty"`
	ReturnedTimestamp        string           `json:"returned_timestamp,omitempty"`
}

type responderEvidenceInput struct {
	Identity        string             `json:"identity"`
	Kind            string             `json:"kind"`
	Tag             string             `json:"tag"`
	RequestID       string             `json:"request_id,omitempty"`
	ReviewID        string             `json:"review_id,omitempty"`
	SourceRevision  string             `json:"source_revision,omitempty"`
	MessageTS       map[string]string  `json:"message_timestamps,omitempty"`
	Resolution      *postingResolution `json:"resolution,omitempty"`
	JudgedReactions []judgedReaction   `json:"judged_reactions,omitempty"`
}

type responderEvidenceLedger struct {
	DestinationKey       string                `json:"destination_key"`
	MessageTimestamps    []string              `json:"message_timestamps"`
	IntegrationReactions []reactionLedgerEntry `json:"integration_reactions,omitempty"`
	PostingIndex         []postingIndexEntry   `json:"posting_index,omitempty"`
}

type responderEvidenceTick struct {
	Name     string                    `json:"name"`
	Pending  []responderEvidenceInput  `json:"pending"`
	Resolved []responderEvidenceInput  `json:"resolved"`
	Ledgers  []responderEvidenceLedger `json:"ledgers"`
}

type responderEvidenceTranscript struct {
	Requests    []responderEvidenceRequest    `json:"requests"`
	Submissions []responderEvidenceSubmission `json:"submissions"`
	Ticks       []responderEvidenceTick       `json:"ticks"`
	Events      []observe.Event               `json:"events"`
	FinalRecord featureRecord                 `json:"final_record"`
}

type responderEvidenceHumans struct {
	messages map[string][]testsupport.Message
}

func newResponderEvidenceHumans() *responderEvidenceHumans {
	return &responderEvidenceHumans{messages: map[string][]testsupport.Message{}}
}

func (h *responderEvidenceHumans) addReply(
	channelID, rootTS, timestamp, userID, text string,
) {
	h.messages[channelID] = append(h.messages[channelID], testsupport.Message{
		TS: timestamp, ThreadTS: rootTS, User: userID, Text: text,
	})
}

func (h *responderEvidenceHumans) addReaction(
	channelID, rootTS, messageTS, name, userID string,
) {
	h.messages[channelID] = append(h.messages[channelID], testsupport.Message{
		TS: messageTS, ThreadTS: rootTS,
		Reactions: []testsupport.Reaction{{Name: name, Count: 1, Users: []string{userID}}},
	})
}

func TestSlackResponderEvidence(t *testing.T) {
	const (
		token        = "xoxp-responder-evidence-424242"
		secondSecret = "xoxb-responder-evidence-989898"
	)
	settings := defaultTestSettings(token, testRecipients()...)
	harness := newNotifierHarness(t, settings)
	harness.seedFeature(responderEvidenceFeatureID, func(value *feature.Feature) {
		value.Repos = []feature.FeatureRepo{{Name: "agentic-orchestrator"}}
	})
	harness.server.SetOwnUserID(responderEvidenceOwnerID)

	var sequence atomic.Int64
	harness.server.SetDefault(func(method string, request testsupport.Request) testsupport.Response {
		switch method {
		case "conversations.open":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "channel": map[string]any{"id": responderEvidenceUserChannel},
			}}
		case "chat.postMessage", "chat.update":
			return testsupport.Response{Body: map[string]any{
				"ok": true, "ts": responderEvidenceTimestamp(sequence.Add(1)),
				"channel": fmt.Sprint(request.Fields["channel"]),
			}}
		default:
			return testsupport.Response{Body: map[string]any{
				"ok": false, "error": "unexpected " + method,
			}}
		}
	})
	harness.server.Script(
		"users.info",
		responderEvidenceUserInfo(
			responderEvidencePeerID,
			"Grace Hopper "+token+" "+secondSecret,
		),
		responderEvidenceUserInfo(
			responderEvidenceOwnerID,
			"Ada Lovelace "+token+" "+secondSecret,
		),
	)

	answerPort := &responderEvidenceAnswerPort{
		permissionResults: []ports.SlackAnswerResult{
			{Outcome: ports.SlackAnswerAccepted},
			{Outcome: ports.SlackAnswerAccepted},
			{Outcome: ports.SlackAnswerFailed, Cause: errors.New("permission relay unavailable")},
		},
		reviewResults: []ports.SlackAnswerResult{
			{Outcome: ports.SlackAnswerAccepted},
			{Outcome: ports.SlackAnswerRevisionMoved},
		},
	}
	responderClock := newManualResponderClock()
	notifier := NewNotifier(NotifierOptions{
		Settings: harness.settings, Store: harness.store, StateDir: harness.stateDir,
		Observer: harness.observer, Pending: harness.pending, Answer: answerPort,
		Clock: harness.clock, ResponderClock: responderClock, QueueCapacity: 64,
		NewClient: func(token string) (slackClient, error) {
			return NewClient(token, WithBaseURL(harness.server.URL()))
		},
	})
	notifier.SetServerName("Responder evidence")
	notifier.Start()
	t.Cleanup(func() { notifier.Stop(context.Background()) })
	harness.notifier = notifier

	notifier.DomainEventTap(startedEvent(responderEvidenceFeatureID, feature.PhaseImplement))
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, responderEvidenceFeatureID)
		if !ok || len(record.Destinations) != 2 {
			return false
		}
		for _, destination := range record.Destinations {
			if destination.RootTS == "" || len(destination.Ledger) < 2 {
				return false
			}
		}
		return true
	})

	permissionOne := responderEvidencePermission("permission-1", "git status")
	harness.pending.set(responderEvidenceFeatureID, permissionOne)
	notifier.RuntimeMessageTap(controlRuntimeMessage(
		responderEvidenceFeatureID, "session-evidence", permissionOne.RequestID,
	))
	permissionOneRecord := waitForResponderEvidenceInput(
		t, harness, "permission:"+permissionOne.RequestID, false,
	)
	humans := newResponderEvidenceHumans()
	record := responderEvidenceRecord(t, harness)
	userRoot := record.Destinations[responderEvidenceUserKey].RootTS
	channelRoot := record.Destinations[responderEvidenceChannelKey].RootTS
	humans.addReaction(
		responderEvidenceUserChannel,
		userRoot,
		permissionOneRecord.MessageTS[responderEvidenceUserKey],
		"white_check_mark",
		responderEvidenceOwnerID,
	)
	allowReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, allowReplyTS,
		responderEvidencePeerID, "allow",
	)

	var ticks []responderEvidenceTick
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls := harness.server.CallCount("conversations.replies")
	beforePosts := harness.server.CallCount("chat.postMessage")
	beforeReactions := harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return len(answerPort.all()) == 1 &&
			harness.server.CallCount("chat.postMessage") == beforePosts+3 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(
		t, harness, beforePolls, permissionOneRecord.MessageTS,
	)
	assertResponderEvidenceReaction(t, harness, allowReplyTS, "white_check_mark")
	assertResponderEvidenceLine(t, harness, "#1 was allowed once by <@U-GRACE> via Slack.", 2)
	assertResponderEvidenceLine(t, harness, "#1 was already answered by <@U-GRACE>.", 1)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "reply_wins_reaction"))

	permissionDeny := responderEvidencePermission("permission-deny", "git clean -fd")
	harness.pending.set(responderEvidenceFeatureID, permissionDeny)
	notifier.RuntimeMessageTap(controlRuntimeMessage(
		responderEvidenceFeatureID, "session-evidence", permissionDeny.RequestID,
	))
	permissionDenyRecord := waitForResponderEvidenceInput(
		t, harness, "permission:"+permissionDeny.RequestID, false,
	)
	denyAcceptedReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, denyAcceptedReplyTS,
		responderEvidencePeerID, "deny",
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return len(answerPort.all()) == 2 &&
			harness.server.CallCount("chat.postMessage") == beforePosts+2 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionDenyRecord.MessageTS)
	assertResponderEvidenceReaction(t, harness, denyAcceptedReplyTS, "white_check_mark")
	assertResponderEvidenceLine(t, harness, "#2 was denied by <@U-GRACE> via Slack.", 2)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "permission_denied"))

	permissionTwo := responderEvidencePermission("permission-2", "go test ./...")
	harness.pending.set(responderEvidenceFeatureID, permissionTwo)
	notifier.RuntimeMessageTap(controlRuntimeMessage(
		responderEvidenceFeatureID, "session-evidence", permissionTwo.RequestID,
	))
	permissionTwoRecord := waitForResponderEvidenceInput(
		t, harness, "permission:"+permissionTwo.RequestID, false,
	)
	unparseableReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, unparseableReplyTS,
		responderEvidencePeerID, "hmm, let me check",
	)
	humans.addReaction(
		responderEvidenceUserChannel,
		userRoot,
		permissionTwoRecord.MessageTS[responderEvidenceUserKey],
		"eyes",
		responderEvidenceOwnerID,
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == beforePosts+1 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	assertResponderEvidenceReaction(t, harness, unparseableReplyTS, "question")
	assertResponderEvidenceLine(
		t, harness, "#3 accepts ✅ or ❌, or a reply of allow or deny.", 1,
	)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "unparseable_and_eyes"))

	reviewOne := responderEvidenceReview(
		"review-1", "sha256:review-one", []byte("# Review one\n\nApprove this plan.\n"),
	)
	responderEvidenceScriptReviewShares(harness.server, &sequence, "review-one")
	harness.pending.set(responderEvidenceFeatureID, permissionTwo, reviewOne)
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: responderEvidenceFeatureID,
	})
	reviewOneRecord := waitForResponderEvidenceInput(
		t, harness, responderEvidenceReviewIdentity(reviewOne), true,
	)
	unparseableReviewReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, unparseableReviewReplyTS,
		responderEvidenceOwnerID, "lgtm",
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == beforePosts+1 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	assertResponderEvidenceReaction(t, harness, unparseableReviewReplyTS, "question")
	assertResponderEvidenceLine(
		t, harness, "#4 accepts ✅ or approve. Request changes in Agentico.", 1,
	)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "review_unparseable"))

	approveReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, approveReplyTS,
		responderEvidenceOwnerID, "approve",
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return len(answerPort.all()) == 3 &&
			harness.server.CallCount("chat.postMessage") == beforePosts+2 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	assertResponderEvidenceReaction(t, harness, approveReplyTS, "white_check_mark")
	assertResponderEvidenceLine(t, harness, "#4 was approved by <@U-ADA> via Slack.", 2)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "review_approved"))

	reviewTwo := responderEvidenceReview(
		"review-2", "sha256:review-two", []byte("# Review two\n\nThis draft will move.\n"),
	)
	responderEvidenceScriptReviewShares(harness.server, &sequence, "review-two")
	harness.pending.set(responderEvidenceFeatureID, permissionTwo, reviewTwo)
	notifier.DomainEventTap(ports.Event{
		Type: ports.ReviewRequired, FeatureID: responderEvidenceFeatureID,
	})
	reviewTwoRecord := waitForResponderEvidenceInput(
		t, harness, responderEvidenceReviewIdentity(reviewTwo), true,
	)
	humans.addReaction(
		responderEvidenceUserChannel,
		userRoot,
		reviewTwoRecord.MessageTS[responderEvidenceUserKey],
		"white_check_mark",
		responderEvidenceOwnerID,
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return len(answerPort.all()) == 4 &&
			harness.server.CallCount("chat.postMessage") == beforePosts+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	if got := harness.server.CallCount("reactions.add"); got != beforeReactions {
		t.Fatalf("moved review reactions.add calls = %d; want %d", got, beforeReactions)
	}
	assertResponderEvidenceLine(
		t, harness, "#5 changed in Agentico. Approve the current review there.", 1,
	)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "review_moved"))

	permissionThree := responderEvidencePermission("permission-3", "rm build.tmp")
	harness.pending.set(responderEvidenceFeatureID, permissionTwo, reviewTwo, permissionThree)
	notifier.RuntimeMessageTap(controlRuntimeMessage(
		responderEvidenceFeatureID, "session-evidence", permissionThree.RequestID,
	))
	_ = waitForResponderEvidenceInput(
		t, harness, "permission:"+permissionThree.RequestID, false,
	)
	denyReplyTS := responderEvidenceTimestamp(sequence.Add(1))
	humans.addReply(
		responderEvidenceChannel, channelRoot, denyReplyTS,
		responderEvidencePeerID, "deny",
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return len(answerPort.all()) == 5 &&
			harness.server.CallCount("chat.postMessage") == beforePosts+1 &&
			harness.server.CallCount("reactions.add") == beforeReactions+1
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	assertResponderEvidenceReaction(t, harness, denyReplyTS, "warning")
	assertResponderEvidenceLine(
		t, harness, "#6 could not be submitted. Answer again or in Agentico.", 1,
	)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "permission_failed"))

	harness.pending.set(responderEvidenceFeatureID, reviewTwo, permissionThree)
	humans.addReaction(
		responderEvidenceUserChannel,
		userRoot,
		permissionTwoRecord.MessageTS[responderEvidenceUserKey],
		"white_check_mark",
		"U-LATE",
	)
	responderEvidenceSeedThreads(t, harness, humans)
	beforePolls = harness.server.CallCount("conversations.replies")
	beforePosts = harness.server.CallCount("chat.postMessage")
	beforeReactions = harness.server.CallCount("reactions.add")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == beforePosts+2
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	beforePolls = harness.server.CallCount("conversations.replies")
	responderEvidenceTickClock(t, responderClock)
	waitFor(t, 10*time.Second, func() bool {
		return harness.server.CallCount("chat.postMessage") == beforePosts+3
	})
	assertResponderEvidencePolls(t, harness, beforePolls, permissionTwoRecord.MessageTS)
	if got := harness.server.CallCount("reactions.add"); got != beforeReactions {
		t.Fatalf("late reaction reactions.add calls = %d; want %d", got, beforeReactions)
	}
	assertResponderEvidenceLine(t, harness, "#3 was resolved in Agentico.", 2)
	assertResponderEvidenceLine(t, harness, "#3 was already answered by Agentico.", 1)
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "agentico_resolution_and_late_reaction"))

	harness.pending.set(responderEvidenceFeatureID)
	if err := harness.store.Modify(responderEvidenceFeatureID, func(value *feature.Feature) error {
		value.Status = feature.StatusInterrupted
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	beforePosts = harness.server.CallCount("chat.postMessage")
	notifier.DomainEventTap(ports.Event{
		Type: ports.FeatureInterrupted, FeatureID: responderEvidenceFeatureID,
		Phase: feature.PhaseImplement,
	})
	waitFor(t, 10*time.Second, func() bool {
		record := responderEvidenceRecord(t, harness)
		return harness.server.CallCount("chat.postMessage") == beforePosts+6 &&
			len(record.Pending) == 0 && len(record.Resolved) == 0
	})
	assertResponderEvidenceLine(t, harness, "#5 is no longer pending.", 2)
	assertResponderEvidenceLine(t, harness, "#6 is no longer pending.", 2)
	pollsBeforeIdle := harness.server.CallCount("conversations.replies")
	responderEvidenceTickClock(t, responderClock)
	if got := harness.server.CallCount("conversations.replies"); got != pollsBeforeIdle {
		t.Fatalf("polls after interrupted tick = %d; want %d", got, pollsBeforeIdle)
	}
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "interrupted_idle_tick_one"))
	responderEvidenceTickClock(t, responderClock)
	if got := harness.server.CallCount("conversations.replies"); got != pollsBeforeIdle {
		t.Fatalf("polls after second idle tick = %d; want %d", got, pollsBeforeIdle)
	}
	ticks = append(ticks, captureResponderEvidenceTick(t, harness, "interrupted_idle_tick_two"))

	assertResponderEvidenceOutcome(t, harness, answerPort, reviewOneRecord, reviewTwoRecord)
	writeResponderEvidence(t, harness, answerPort, ticks, token, secondSecret)
}

func responderEvidenceTimestamp(sequence int64) string {
	return fmt.Sprintf("1900000000.%06d", sequence)
}

func responderEvidenceUserInfo(userID, displayName string) testsupport.Response {
	return testsupport.Response{Body: map[string]any{
		"ok": true,
		"user": map[string]any{
			"id": userID,
			"profile": map[string]any{
				"display_name": displayName,
				"real_name":    displayName,
			},
		},
	}}
}

func responderEvidencePermission(requestID, command string) ports.SlackPendingInput {
	return ports.SlackPendingInput{
		Kind:         ports.SlackPendingPermission,
		FeatureID:    responderEvidenceFeatureID,
		RequestID:    requestID,
		ToolName:     "Bash",
		Phase:        feature.PhaseImplement.DirName(),
		RepoName:     "agentic-orchestrator",
		WaitingSince: time.Date(2026, 9, 22, 18, 0, 0, 0, time.UTC),
		Input:        map[string]any{"command": command},
	}
}

func responderEvidenceReview(
	reviewID, revision string,
	body []byte,
) ports.SlackPendingInput {
	return ports.SlackPendingInput{
		Kind:             ports.SlackPendingReview,
		FeatureID:        responderEvidenceFeatureID,
		ReviewID:         reviewID,
		ReviewMode:       "plan",
		TargetPhase:      feature.PhaseImplement.DirName(),
		ArtifactID:       reviewID + "-artifact",
		ArtifactFilename: "phase-plan.md",
		ArtifactBytes:    append([]byte(nil), body...),
		ArtifactSize:     int64(len(body)),
		RunNumber:        1,
		SourceRevision:   revision,
		CanIterate:       true,
		PhasePlan:        true,
	}
}

func responderEvidenceReviewIdentity(input ports.SlackPendingInput) string {
	return "review:" + input.ReviewID + ":" + input.SourceRevision
}

func responderEvidenceScriptReviewShares(
	server *testsupport.Server,
	sequence *atomic.Int64,
	suffix string,
) {
	userTS := responderEvidenceTimestamp(sequence.Add(1))
	channelTS := responderEvidenceTimestamp(sequence.Add(1))
	files := []any{
		map[string]any{
			"id": "F-USER-" + suffix,
			"shares": map[string]any{
				"private": map[string]any{
					responderEvidenceUserChannel: []any{map[string]any{"ts": userTS}},
				},
			},
		},
		map[string]any{
			"id": "F-CHANNEL-" + suffix,
			"shares": map[string]any{
				"public": map[string]any{
					responderEvidenceChannel: []any{map[string]any{"ts": channelTS}},
				},
			},
		},
	}
	response := testsupport.Response{Body: map[string]any{"ok": true, "files": files}}
	server.Script("files.completeUploadExternal", response, response)
}

func responderEvidenceRecord(
	t *testing.T,
	harness *notifierHarness,
) featureRecord {
	t.Helper()
	record, ok := readFeatureRecord(harness.stateDir, responderEvidenceFeatureID)
	if !ok {
		t.Fatal("read responder evidence Slack record")
	}
	return record
}

func waitForResponderEvidenceInput(
	t *testing.T,
	harness *notifierHarness,
	identity string,
	wantFiles bool,
) pendingInputRecord {
	t.Helper()
	var found pendingInputRecord
	waitFor(t, 10*time.Second, func() bool {
		record, ok := readFeatureRecord(harness.stateDir, responderEvidenceFeatureID)
		if !ok {
			return false
		}
		for _, pending := range record.Pending {
			if pending.Identity != identity || len(pending.MessageTS) != 2 {
				continue
			}
			if wantFiles && len(pending.FileIDs) != 2 {
				continue
			}
			found = pending
			return true
		}
		return false
	})
	return found
}

func responderEvidenceTickClock(t *testing.T, clock *manualResponderClock) {
	t.Helper()
	clock.tick(t)
	waitFor(t, time.Second, func() bool {
		return len(clock.sleeps) == 1
	})
}

func responderEvidenceSeedThreads(
	t *testing.T,
	harness *notifierHarness,
	humans *responderEvidenceHumans,
) {
	t.Helper()
	record := responderEvidenceRecord(t, harness)
	requests := harness.server.AllRequests()
	for _, destination := range record.Destinations {
		byTimestamp := map[string]testsupport.Message{
			destination.RootTS: {
				TS: destination.RootTS, ThreadTS: destination.RootTS,
				User: responderEvidenceOwnerID, Text: "root card",
			},
		}
		for _, timestamp := range destination.Ledger {
			if timestamp == destination.RootTS {
				continue
			}
			byTimestamp[timestamp] = testsupport.Message{
				TS: timestamp, ThreadTS: destination.RootTS,
				User: responderEvidenceOwnerID, Subtype: "file_share",
				Text: "integration file share",
			}
		}
		for _, request := range requests {
			if strings.TrimPrefix(request.Path, "/api/") != "chat.postMessage" ||
				fieldString(request, "channel") != destination.ChannelID ||
				fieldString(request, "thread_ts") != destination.RootTS ||
				request.ReturnedTS == "" {
				continue
			}
			byTimestamp[request.ReturnedTS] = testsupport.Message{
				TS: request.ReturnedTS, ThreadTS: destination.RootTS,
				User: responderEvidenceOwnerID, Text: fieldString(request, "text"),
			}
		}
		for _, human := range humans.messages[destination.ChannelID] {
			current, exists := byTimestamp[human.TS]
			if !exists {
				current = human
			} else {
				current.Reactions = mergeResponderEvidenceReactions(
					current.Reactions,
					human.Reactions,
				)
				if human.Text != "" {
					current.Text = human.Text
				}
				if human.User != "" {
					current.User = human.User
				}
				if human.BotID != "" {
					current.BotID = human.BotID
				}
				if human.Subtype != "" {
					current.Subtype = human.Subtype
				}
			}
			byTimestamp[human.TS] = current
		}
		for _, request := range harness.server.Requests("reactions.add") {
			if fieldString(request, "channel") != destination.ChannelID {
				continue
			}
			timestamp := fieldString(request, "timestamp")
			message, exists := byTimestamp[timestamp]
			if !exists {
				continue
			}
			message.Reactions = mergeResponderEvidenceReactions(
				message.Reactions,
				[]testsupport.Reaction{{
					Name: fieldString(request, "name"), Count: 1,
					Users: []string{responderEvidenceOwnerID},
				}},
			)
			byTimestamp[timestamp] = message
		}
		messages := make([]testsupport.Message, 0, len(byTimestamp))
		for _, message := range byTimestamp {
			messages = append(messages, message)
		}
		sort.Slice(messages, func(i, j int) bool {
			return compareSlackTimestamps(messages[i].TS, messages[j].TS) < 0
		})
		harness.server.SeedThread(destination.ChannelID, destination.RootTS, messages)
	}
}

func mergeResponderEvidenceReactions(
	current []testsupport.Reaction,
	additions []testsupport.Reaction,
) []testsupport.Reaction {
	result := append([]testsupport.Reaction(nil), current...)
	for _, addition := range additions {
		found := false
		for index := range result {
			if result[index].Name != addition.Name {
				continue
			}
			found = true
			for _, userID := range addition.Users {
				if !responderEvidenceContains(result[index].Users, userID) {
					result[index].Users = append(result[index].Users, userID)
				}
			}
			result[index].Count = len(result[index].Users)
		}
		if !found {
			cloned := addition
			cloned.Users = append([]string(nil), addition.Users...)
			cloned.Count = len(cloned.Users)
			result = append(result, cloned)
		}
	}
	return result
}

func responderEvidenceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertResponderEvidencePolls(
	t testing.TB,
	harness *notifierHarness,
	start int,
	oldestByDestination map[string]string,
) {
	t.Helper()
	polls := harness.server.Requests("conversations.replies")
	if len(polls) != start+2 {
		t.Fatalf("responder evidence polls = %d; want %d", len(polls), start+2)
	}
	seen := map[string]bool{}
	for _, poll := range polls[start:] {
		channelID := fieldString(poll, "channel")
		destinationKey := responderEvidenceChannelKey
		if channelID == responderEvidenceUserChannel {
			destinationKey = responderEvidenceUserKey
		}
		if seen[destinationKey] {
			t.Errorf("duplicate poll for %s in one tick", destinationKey)
		}
		seen[destinationKey] = true
		if got, want := fieldString(poll, "oldest"), oldestByDestination[destinationKey]; got != want {
			t.Errorf("poll oldest for %s = %q; want %q", destinationKey, got, want)
		}
		if got := fieldString(poll, "inclusive"); got != "true" {
			t.Errorf("poll inclusive for %s = %q; want true", destinationKey, got)
		}
		if got := fieldString(poll, "cursor"); got != "" {
			t.Errorf("poll cursor for %s = %q; want empty", destinationKey, got)
		}
	}
	for _, key := range []string{responderEvidenceUserKey, responderEvidenceChannelKey} {
		if !seen[key] {
			t.Errorf("polls missing destination %s", key)
		}
	}
}

func assertResponderEvidenceReaction(
	t testing.TB,
	harness *notifierHarness,
	timestamp, name string,
) {
	t.Helper()
	for _, request := range harness.server.Requests("reactions.add") {
		if fieldString(request, "timestamp") == timestamp &&
			fieldString(request, "name") == name {
			return
		}
	}
	t.Errorf("reactions.add missing %q on %q", name, timestamp)
}

func assertResponderEvidenceLine(
	t testing.TB,
	harness *notifierHarness,
	text string,
	want int,
) {
	t.Helper()
	got := 0
	for _, request := range harness.server.Requests("chat.postMessage") {
		if fieldString(request, "text") == text {
			got++
		}
	}
	if got != want {
		t.Errorf("chat.postMessage text %q count = %d; want %d", text, got, want)
	}
}

func captureResponderEvidenceTick(
	t *testing.T,
	harness *notifierHarness,
	name string,
) responderEvidenceTick {
	t.Helper()
	record := responderEvidenceRecord(t, harness)
	ledgers := make([]responderEvidenceLedger, 0, len(record.Destinations))
	keys := make([]string, 0, len(record.Destinations))
	for key := range record.Destinations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		destination := record.Destinations[key]
		ledgers = append(ledgers, responderEvidenceLedger{
			DestinationKey:       key,
			MessageTimestamps:    append([]string(nil), destination.Ledger...),
			IntegrationReactions: append([]reactionLedgerEntry(nil), destination.IntegrationReactions...),
			PostingIndex:         append([]postingIndexEntry(nil), destination.PostingIndex...),
		})
	}
	return responderEvidenceTick{
		Name:     name,
		Pending:  responderEvidenceInputs(record.Pending),
		Resolved: responderEvidenceInputs(record.Resolved),
		Ledgers:  ledgers,
	}
}

func responderEvidenceInputs(records []pendingInputRecord) []responderEvidenceInput {
	result := make([]responderEvidenceInput, 0, len(records))
	for _, record := range records {
		messageTS := make(map[string]string, len(record.MessageTS))
		for key, timestamp := range record.MessageTS {
			messageTS[key] = timestamp
		}
		result = append(result, responderEvidenceInput{
			Identity:        record.Identity,
			Kind:            record.Kind,
			Tag:             record.Tag,
			RequestID:       record.RequestID,
			ReviewID:        record.ReviewID,
			SourceRevision:  record.SourceRevision,
			MessageTS:       messageTS,
			Resolution:      record.Resolution,
			JudgedReactions: append([]judgedReaction(nil), record.JudgedReactions...),
		})
	}
	return result
}

func assertResponderEvidenceOutcome(
	t *testing.T,
	harness *notifierHarness,
	answerPort *responderEvidenceAnswerPort,
	reviewOne, reviewTwo pendingInputRecord,
) {
	t.Helper()
	submissions := answerPort.all()
	if len(submissions) != 5 {
		t.Fatalf("answer-port submissions = %#v; want five", submissions)
	}
	wantSubmissions := []struct {
		kind     string
		id       string
		decision ports.SlackPermissionDecision
		revision string
		outcome  ports.SlackAnswerOutcome
	}{
		{
			kind: "permission", id: "permission-1",
			decision: ports.SlackPermissionAllowOnce,
			outcome:  ports.SlackAnswerAccepted,
		},
		{
			kind: "permission", id: "permission-deny",
			decision: ports.SlackPermissionDeny,
			outcome:  ports.SlackAnswerAccepted,
		},
		{
			kind: "review", id: "review-1",
			revision: reviewOne.SourceRevision,
			outcome:  ports.SlackAnswerAccepted,
		},
		{
			kind: "review", id: "review-2",
			revision: reviewTwo.SourceRevision,
			outcome:  ports.SlackAnswerRevisionMoved,
		},
		{
			kind: "permission", id: "permission-3",
			decision: ports.SlackPermissionDeny,
			outcome:  ports.SlackAnswerFailed,
		},
	}
	for index, want := range wantSubmissions {
		got := submissions[index]
		id := got.RequestID
		if got.Kind == "review" {
			id = got.ReviewID
		}
		if got.Kind != want.kind || id != want.id ||
			got.Decision != want.decision ||
			got.SourceRevision != want.revision ||
			got.Outcome != want.outcome ||
			got.Source.Kind != ports.AnswerSourceSlack ||
			got.SourceFeatureID != responderEvidenceFeatureID {
			t.Errorf("submission %d = %#v; want %#v", index, got, want)
		}
	}

	received := harness.observer.ofKind("slack.answer_received")
	rejected := harness.observer.ofKind("slack.answer_rejected")
	if len(received) != 3 {
		t.Errorf("slack.answer_received events = %#v; want three", received)
	}
	if len(rejected) != 6 {
		t.Errorf("slack.answer_rejected events = %#v; want six", rejected)
	}
	reasons := map[string]int{}
	for _, event := range rejected {
		reasons[fmt.Sprint(event.Data["reason"])]++
	}
	for reason, want := range map[string]int{
		"already_resolved": 2,
		"unparseable":      2,
		"stale_revision":   1,
		"submit_failed":    1,
	} {
		if got := reasons[reason]; got != want {
			t.Errorf("rejected reason %q count = %d; want %d", reason, got, want)
		}
	}

	record := responderEvidenceRecord(t, harness)
	if len(record.Pending) != 0 || len(record.Resolved) != 0 {
		t.Errorf("final pending=%#v resolved=%#v; want idle state pruned", record.Pending, record.Resolved)
	}
	for key, destination := range record.Destinations {
		if len(destination.PostingIndex) != 0 {
			t.Errorf("final posting index %s = %#v; want pruned", key, destination.PostingIndex)
		}
	}
}

func writeResponderEvidence(
	t *testing.T,
	harness *notifierHarness,
	answerPort *responderEvidenceAnswerPort,
	ticks []responderEvidenceTick,
	secrets ...string,
) {
	t.Helper()
	record := responderEvidenceRecord(t, harness)
	events := append(
		[]observe.Event(nil),
		harness.observer.ofKind("slack.answer_received")...,
	)
	events = append(events, harness.observer.ofKind("slack.answer_rejected")...)
	transcript := responderEvidenceTranscript{
		Requests:    responderEvidenceRequests(harness.server.AllRequests()),
		Submissions: answerPort.all(),
		Ticks:       ticks,
		Events:      events,
		FinalRecord: record,
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(transcript); err != nil {
		t.Fatal(err)
	}
	encoded := output.Bytes()
	for _, secret := range secrets {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("responder evidence leaked %q", secret)
		}
	}
	for _, replyText := range []string{
		"hmm, let me check",
		"lgtm",
		`"fallback_text": "allow"`,
		`"fallback_text": "approve"`,
		`"fallback_text": "deny"`,
	} {
		if bytes.Contains(encoded, []byte(replyText)) {
			t.Fatalf("responder evidence retained seeded reply text %q", replyText)
		}
	}
	for _, want := range []string{
		"#1 was allowed once by <@U-GRACE> via Slack.",
		"#1 was already answered by <@U-GRACE>.",
		"#2 was denied by <@U-GRACE> via Slack.",
		"#3 accepts ✅ or ❌, or a reply of allow or deny.",
		"#4 accepts ✅ or approve. Request changes in Agentico.",
		"#4 was approved by <@U-ADA> via Slack.",
		"#5 changed in Agentico. Approve the current review there.",
		"#6 could not be submitted. Answer again or in Agentico.",
		"#3 was resolved in Agentico.",
		"#3 was already answered by Agentico.",
		"#5 is no longer pending.",
		"#6 is no longer pending.",
	} {
		if !bytes.Contains(encoded, []byte(want)) {
			t.Errorf("responder evidence missing %q", want)
		}
	}

	dir := strings.TrimSpace(os.Getenv("AGENTICO_EVIDENCE_DIR"))
	if dir == "" {
		return
	}
	target := filepath.Join(dir, "behaviors")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(target, "slack-responder.json"),
		encoded,
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func responderEvidenceRequests(
	requests []testsupport.Request,
) []responderEvidenceRequest {
	result := make([]responderEvidenceRequest, 0, len(requests))
	for index, request := range requests {
		result = append(result, responderEvidenceRequest{
			Index:      index,
			Method:     strings.TrimPrefix(request.Path, "/api/"),
			HTTPMethod: request.Method,
			Channel: firstNonempty(
				fieldString(request, "channel"),
				fieldString(request, "channel_id"),
				fieldString(request, "users"),
			),
			ThreadOrMessageTimestamp: firstNonempty(
				fieldString(request, "thread_ts"),
				fieldString(request, "timestamp"),
				fieldString(request, "ts"),
			),
			Oldest:            fieldString(request, "oldest"),
			Cursor:            fieldString(request, "cursor"),
			ReactionName:      fieldString(request, "name"),
			ReplyBroadcast:    fieldString(request, "reply_broadcast"),
			FallbackText:      fieldString(request, "text"),
			Blocks:            requestBlocks(request),
			ReturnedTimestamp: request.ReturnedTS,
		})
	}
	return result
}
