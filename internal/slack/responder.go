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
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	responderPollInterval = 10 * time.Second
	responderPageSize     = 100
	responderPageBudget   = 3
)

type responderThread struct {
	featureID        string
	destinationKey   string
	destinationOrder int
	channelID        string
	rootTS           string
	oldest           string
}

type responderPollState struct {
	oldest              string
	cursor              string
	consecutiveFailures int
	pauseUntil          time.Time
	suspended           bool
	credentialFailed    bool
	generation          uint64
}

type responderReplyCandidate struct {
	Thread      responderThread
	Message     Message
	Target      postingIndexEntry
	TargetFound bool
}

type responderReactionCandidate struct {
	Thread        responderThread
	MessageTS     string
	Name          string
	UserID        string
	Target        postingIndexEntry
	reactionOrder int
	userOrder     int
}

func parsePermissionReply(text string) (ports.SlackPermissionDecision, bool) {
	switch normalizeResponderReply(text) {
	case "allow", "approve", "yes":
		return ports.SlackPermissionAllowOnce, true
	case "deny", "no":
		return ports.SlackPermissionDeny, true
	default:
		return "", false
	}
}

func parseReviewReply(text string) bool {
	normalized := normalizeResponderReply(text)
	return normalized == "approve" || normalized == "✅"
}

func normalizeResponderReply(text string) string {
	text = strings.TrimSpace(strings.ToLower(text))
	return strings.TrimRightFunc(text, unicode.IsPunct)
}

func (n *Notifier) runResponder() {
	defer n.responderWG.Done()
	for n.responderClock.Sleep(n.responderBase, responderPollInterval) {
		n.responderTick()
	}
}

func (n *Notifier) responderTick() {
	settings := n.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" || !settings.Categories.NeedsInput {
		return
	}

	threads := n.responderThreads(settings)
	if len(threads) == 0 {
		return
	}
	client, err := n.newClient(settings.Token)
	if err != nil {
		return
	}
	active := make(map[string]bool, len(threads))
	var replies []responderReplyCandidate
	var reactions []responderReactionCandidate
	for _, thread := range threads {
		key := responderPollKey(thread.featureID, thread.destinationKey)
		active[key] = true
		n.responderPollMu.Lock()
		state := n.responderPolls[key]
		n.responderPollMu.Unlock()
		if state.oldest != thread.oldest {
			state = responderPollState{oldest: thread.oldest}
		}
		if state.suspended ||
			n.responderClock.Now().Before(state.pauseUntil) ||
			n.responderClock.Now().Before(n.workerPauseUntil(thread.channelID)) {
			n.storeResponderPollState(key, state)
			continue
		}
		for pageNumber := 0; pageNumber < responderPageBudget; pageNumber++ {
			page, err := client.ThreadReplies(
				n.responderBase,
				thread.channelID,
				thread.rootTS,
				thread.oldest,
				responderPageSize,
				state.cursor,
			)
			if err != nil {
				n.handleResponderPollFailure(
					settings,
					thread,
					&state,
					err,
				)
				break
			}
			n.handleResponderPollSuccess(settings, thread, &state)
			pageReplies, pageReactions := n.extractResponderCandidates(thread, page.Messages)
			replies = append(replies, pageReplies...)
			reactions = append(reactions, pageReactions...)
			state.cursor = page.NextCursor
			if state.cursor == "" {
				break
			}
		}
		n.storeResponderPollState(key, state)
	}
	sort.SliceStable(replies, func(i, j int) bool {
		return compareSlackTimestamps(replies[i].Message.TS, replies[j].Message.TS) < 0
	})
	sort.SliceStable(reactions, func(i, j int) bool {
		if reactions[i].Thread.featureID != reactions[j].Thread.featureID {
			return reactions[i].Thread.featureID < reactions[j].Thread.featureID
		}
		if reactions[i].Target.Identity != reactions[j].Target.Identity {
			return reactions[i].Target.Identity < reactions[j].Target.Identity
		}
		if reactions[i].Thread.destinationOrder != reactions[j].Thread.destinationOrder {
			return reactions[i].Thread.destinationOrder < reactions[j].Thread.destinationOrder
		}
		leftName := responderReactionOrder(reactions[i].Name)
		rightName := responderReactionOrder(reactions[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		if reactions[i].reactionOrder != reactions[j].reactionOrder {
			return reactions[i].reactionOrder < reactions[j].reactionOrder
		}
		return reactions[i].userOrder < reactions[j].userOrder
	})
	for _, candidate := range replies {
		n.processResponderReply(client, settings.Token, candidate)
	}
	for _, candidate := range reactions {
		n.processResponderReaction(client, settings.Token, candidate)
	}
	n.responderPollMu.Lock()
	for key := range n.responderPolls {
		if !active[key] {
			delete(n.responderPolls, key)
		}
	}
	n.responderPollMu.Unlock()
}

func responderPollKey(featureID, destinationKey string) string {
	return featureID + "\x00" + destinationKey
}

func (n *Notifier) storeResponderPollState(key string, state responderPollState) {
	n.responderPollMu.Lock()
	if current := n.responderPolls[key]; current.generation != state.generation {
		state.consecutiveFailures = 0
		state.pauseUntil = time.Time{}
		state.suspended = false
		state.credentialFailed = false
		state.generation = current.generation
	}
	n.responderPolls[key] = state
	n.responderPollMu.Unlock()
}

func (n *Notifier) rearmResponderPoll(featureID, destinationKey string) {
	if featureID == "" || destinationKey == "" {
		return
	}
	key := responderPollKey(featureID, destinationKey)
	n.responderPollMu.Lock()
	state, ok := n.responderPolls[key]
	if ok {
		state.consecutiveFailures = 0
		state.pauseUntil = time.Time{}
		state.suspended = false
		state.credentialFailed = false
		state.generation++
		n.responderPolls[key] = state
	}
	n.responderPollMu.Unlock()
}

func (n *Notifier) handleResponderPollSuccess(
	settings ports.SlackRuntimeSettings,
	thread responderThread,
	state *responderPollState,
) {
	item := responderPollWorkItem(thread)
	n.reportWriteSuccess(item, settings.CredentialGeneration)
	state.consecutiveFailures = 0
	state.pauseUntil = time.Time{}
	state.credentialFailed = false
}

func (n *Notifier) handleResponderPollFailure(
	settings ports.SlackRuntimeSettings,
	thread responderThread,
	state *responderPollState,
	err error,
) {
	credential := deliveryCredential{
		token: settings.Token, generation: settings.CredentialGeneration,
	}
	item := responderPollWorkItem(thread)
	failure := classifyWriteFailure(settings.Token, err, false)
	if failure.class == deliveryFailureCredential {
		if !state.credentialFailed {
			n.reportWriteFailure(item, "poll", 1, credential, failure)
			state.credentialFailed = true
		}
		return
	}

	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		n.reportWriteFailure(item, "poll", 1, credential, failure)
		state.suspended = true
		return
	}
	state.consecutiveFailures++
	if transportErr.StatusCode == http.StatusTooManyRequests {
		wait := maxDuration(transportErr.RetryAfter, time.Second)
		state.pauseUntil = n.responderClock.Now().Add(wait)
		n.workerFor(thread.channelID).pauseUntil(state.pauseUntil)
	}
	if !transientStatus(transportErr.StatusCode) &&
		transportErr.StatusCode != http.StatusTooManyRequests {
		n.reportWriteFailure(
			item,
			"poll",
			state.consecutiveFailures,
			credential,
			failure,
		)
		state.suspended = true
		return
	}
	if state.consecutiveFailures <= retryLimit {
		return
	}
	n.reportWriteFailure(
		item,
		"poll",
		state.consecutiveFailures,
		credential,
		classifyWriteFailure(settings.Token, err, true),
	)
	state.suspended = true
}

func responderPollWorkItem(thread responderThread) workItem {
	return workItem{
		featureID:      thread.featureID,
		destinationKey: thread.destinationKey,
		kind:           candidateDestinationKind(thread.destinationKey),
		channelID:      thread.channelID,
		poll:           true,
	}
}

func (n *Notifier) responderThreads(settings ports.SlackRuntimeSettings) []responderThread {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()

	destinationOrders := make(map[string]int, len(settings.Recipients))
	for index, recipient := range settings.Recipients {
		destinationOrders[destinationKey(string(recipient.Kind), recipient.ID)] = index
	}
	var threads []responderThread
	for featureID, record := range n.records {
		active := false
		for _, pending := range record.Pending {
			if pending.Resolution != nil {
				continue
			}
			for _, messageTS := range pending.MessageTS {
				if messageTS != "" {
					active = true
					break
				}
			}
			if active {
				break
			}
		}
		if !active {
			continue
		}
		for key, destination := range record.Destinations {
			if destination.ChannelID == "" || destination.RootTS == "" {
				continue
			}
			oldest := ""
			for _, pending := range record.Pending {
				messageTS := pending.MessageTS[key]
				if messageTS != "" &&
					(oldest == "" || compareSlackTimestamps(messageTS, oldest) < 0) {
					oldest = messageTS
				}
			}
			if oldest != "" {
				threads = append(threads, responderThread{
					featureID:        featureID,
					destinationKey:   key,
					destinationOrder: destinationOrders[key],
					channelID:        destination.ChannelID,
					rootTS:           destination.RootTS,
					oldest:           oldest,
				})
			}
		}
	}
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].featureID != threads[j].featureID {
			return threads[i].featureID < threads[j].featureID
		}
		if threads[i].destinationOrder != threads[j].destinationOrder {
			return threads[i].destinationOrder < threads[j].destinationOrder
		}
		return threads[i].destinationKey < threads[j].destinationKey
	})
	return threads
}

func (n *Notifier) extractResponderCandidates(
	thread responderThread,
	messages []Message,
) ([]responderReplyCandidate, []responderReactionCandidate) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()

	record := n.records[thread.featureID]
	if record == nil {
		return nil, nil
	}
	destination, ok := record.Destinations[thread.destinationKey]
	if !ok {
		return nil, nil
	}

	postingsByTimestamp := make(map[string]postingIndexEntry, len(destination.PostingIndex))
	for _, posting := range destination.PostingIndex {
		postingsByTimestamp[posting.MessageTS] = posting
	}

	var replies []responderReplyCandidate
	var reactions []responderReactionCandidate
	for _, message := range messages {
		if posting, posted := postingsByTimestamp[message.TS]; posted {
			for reactionOrder, reaction := range message.Reactions {
				if reaction.Name != "white_check_mark" && reaction.Name != "x" {
					continue
				}
				if destination.reactionContains(message.TS, reaction.Name) {
					continue
				}
				for userOrder, userID := range reaction.Users {
					reactions = append(reactions, responderReactionCandidate{
						Thread:        thread,
						MessageTS:     message.TS,
						Name:          reaction.Name,
						UserID:        userID,
						Target:        posting,
						reactionOrder: reactionOrder,
						userOrder:     userOrder,
					})
				}
			}
			continue
		}
		if message.TS == "" ||
			destination.ledgerContains(message.TS) ||
			destination.hasReactionForMessage(message.TS) {
			continue
		}
		target, found := newestPostingBefore(destination.PostingIndex, message.TS)
		replies = append(replies, responderReplyCandidate{
			Thread:      thread,
			Message:     message,
			Target:      target,
			TargetFound: found,
		})
	}
	return replies, reactions
}

func responderReactionOrder(name string) int {
	if name == "white_check_mark" {
		return 0
	}
	return 1
}

func (n *Notifier) processResponderReply(
	client slackClient,
	token string,
	candidate responderReplyCandidate,
) {
	if !candidate.TargetFound {
		n.rejectResponderReply(token, candidate, pendingInputRecord{}, "not_answerable", "question",
			"This reply does not target an answerable item. Answer in Agentico.")
		return
	}
	pending, ok := n.pendingResponderTarget(candidate.Thread.featureID, candidate.Target.Identity)
	if !ok {
		return
	}
	if pending.Resolution != nil {
		n.rejectResolvedReply(token, candidate, pending, pending.Resolution)
		return
	}
	decision, parsed := responderReplyDecision(pending.Kind, candidate.Message.Text)
	if !parsed {
		n.rejectResponderReply(
			token,
			candidate,
			pending,
			responderUnparseableReason(pending.Kind),
			"question",
			responderHint(pending),
		)
		return
	}
	responderName := n.responderName(client, token, candidate.Message.User)
	result := n.submitResponderAnswer(pending, decision, responderName)
	if result.Outcome != ports.SlackAnswerAccepted {
		n.handleRejectedResponderReply(token, candidate, pending, decision, result)
		return
	}
	if n.resolveResponderTarget(
		candidate.Thread.featureID,
		candidate.Target.Identity,
		candidate.Message.User,
		responderName,
	) {
		n.enqueueAcceptedResponderWrites(
			token,
			pending,
			candidate.Thread,
			candidate.Message.TS,
			candidate.Message.User,
			decision,
		)
		n.emitResponderAccepted(candidate.Thread, pending, decision, "reply")
	}
}

func (n *Notifier) processResponderReaction(
	client slackClient,
	token string,
	candidate responderReactionCandidate,
) {
	pending, ok := n.pendingResponderTarget(candidate.Thread.featureID, candidate.Target.Identity)
	if !ok || pending.judgedReactionContains(
		candidate.Thread.destinationKey,
		candidate.MessageTS,
		candidate.Name,
		candidate.UserID,
	) {
		return
	}
	if pending.Resolution != nil {
		n.judgeResponderReaction(candidate)
		n.rejectResolvedReaction(token, candidate, pending, pending.Resolution)
		return
	}
	decision, parsed := responderReactionDecision(pending.Kind, candidate.Name)
	if !parsed {
		return
	}
	responderName := n.responderName(client, token, candidate.UserID)
	result := n.submitResponderAnswer(pending, decision, responderName)
	n.judgeResponderReaction(candidate)
	if result.Outcome != ports.SlackAnswerAccepted {
		n.handleRejectedResponderReaction(token, candidate, pending, decision, result)
		return
	}
	if n.resolveResponderTarget(
		candidate.Thread.featureID,
		candidate.Target.Identity,
		candidate.UserID,
		responderName,
	) {
		n.enqueueAcceptedResponderWrites(
			token,
			pending,
			candidate.Thread,
			"",
			candidate.UserID,
			decision,
		)
		n.emitResponderAccepted(candidate.Thread, pending, decision, "reaction")
	}
}

func responderReplyDecision(inputKind, text string) (string, bool) {
	switch inputKind {
	case string(ports.SlackPendingPermission):
		decision, ok := parsePermissionReply(text)
		return string(decision), ok
	case string(ports.SlackPendingReview):
		return "approve", parseReviewReply(text)
	default:
		return "", false
	}
}

func responderReactionDecision(inputKind, reaction string) (string, bool) {
	switch inputKind {
	case string(ports.SlackPendingPermission):
		if reaction == "white_check_mark" {
			return string(ports.SlackPermissionAllowOnce), true
		}
		if reaction == "x" {
			return string(ports.SlackPermissionDeny), true
		}
	case string(ports.SlackPendingReview):
		if reaction == "white_check_mark" {
			return "approve", true
		}
	}
	return "", false
}

func (n *Notifier) submitResponderAnswer(
	pending pendingInputRecord,
	decision, responderName string,
) ports.SlackAnswerResult {
	source := ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: responderName}
	switch pending.Kind {
	case string(ports.SlackPendingPermission):
		return n.answer.AnswerSlackPermission(ports.SlackPermissionAnswer{
			RequestID: pending.RequestID, SourceFeatureID: pending.SourceFeatureID,
			Decision: ports.SlackPermissionDecision(decision), Source: source,
		})
	case string(ports.SlackPendingReview):
		return n.answer.ApproveSlackReview(ports.SlackReviewApproval{
			SourceFeatureID: pending.SourceFeatureID, ReviewID: pending.ReviewID,
			SourceRevision: pending.SourceRevision, Source: source,
		})
	default:
		return ports.SlackAnswerResult{Outcome: ports.SlackAnswerFailed}
	}
}

func (n *Notifier) handleRejectedResponderReply(
	token string,
	candidate responderReplyCandidate,
	pending pendingInputRecord,
	decision string,
	result ports.SlackAnswerResult,
) {
	switch result.Outcome {
	case ports.SlackAnswerRevisionMoved:
		n.rejectResponderReply(token, candidate, pending, "stale_revision", "warning",
			pending.Tag+" changed in Agentico. Approve the current review there.")
	case ports.SlackAnswerNoLongerPending:
		n.rejectResponderReply(token, candidate, pending, "already_resolved", "warning",
			pending.Tag+" was already answered by Agentico.")
	case ports.SlackAnswerFailed:
		n.logResponderFailure(token, result.Cause)
		n.rejectResponderReply(token, candidate, pending, "submit_failed", "warning",
			pending.Tag+" could not be submitted. Answer again or in Agentico.")
	default:
		n.emitResponderRejected(candidate.Thread, pending, decision, "reply", "submit_failed")
	}
}

func (n *Notifier) handleRejectedResponderReaction(
	token string,
	candidate responderReactionCandidate,
	pending pendingInputRecord,
	decision string,
	result ports.SlackAnswerResult,
) {
	line := ""
	reason := ""
	switch result.Outcome {
	case ports.SlackAnswerRevisionMoved:
		reason = "stale_revision"
		line = pending.Tag + " changed in Agentico. Approve the current review there."
	case ports.SlackAnswerNoLongerPending:
		reason = "already_resolved"
		line = pending.Tag + " was already answered by Agentico."
	case ports.SlackAnswerFailed:
		reason = "submit_failed"
		line = pending.Tag + " could not be submitted. Answer again or in Agentico."
		n.logResponderFailure(token, result.Cause)
	default:
		return
	}
	n.enqueueResponderFeedback(token, candidate.Thread, pending.SourceFeatureID, "", "", line)
	n.emitResponderRejected(candidate.Thread, pending, decision, "reaction", reason)
}

func (n *Notifier) rejectResolvedReply(
	token string,
	candidate responderReplyCandidate,
	pending pendingInputRecord,
	resolution *postingResolution,
) {
	n.rejectResponderReply(token, candidate, pending, "already_resolved", "warning",
		alreadyAnsweredLine(pending.Tag, resolution))
}

func (n *Notifier) rejectResolvedReaction(
	token string,
	candidate responderReactionCandidate,
	pending pendingInputRecord,
	resolution *postingResolution,
) {
	n.enqueueResponderFeedback(
		token,
		candidate.Thread,
		pending.SourceFeatureID,
		"",
		"",
		alreadyAnsweredLine(pending.Tag, resolution),
	)
	n.emitResponderRejected(candidate.Thread, pending, "", "reaction", "already_resolved")
}

func alreadyAnsweredLine(tag string, resolution *postingResolution) string {
	if resolution != nil && resolution.Kind == resolutionSlack && resolution.ResponderID != "" {
		return tag + " was already answered by <@" + resolution.ResponderID + ">."
	}
	return tag + " was already answered by Agentico."
}

func responderUnparseableReason(inputKind string) string {
	if inputKind == string(ports.SlackPendingPermission) ||
		inputKind == string(ports.SlackPendingReview) {
		return "unparseable"
	}
	return "not_answerable"
}

func responderHint(pending pendingInputRecord) string {
	switch pending.Kind {
	case string(ports.SlackPendingPermission):
		return pending.Tag + " accepts ✅ or ❌, or a reply of allow or deny."
	case string(ports.SlackPendingReview):
		return pending.Tag + " accepts ✅ or approve. Request changes in Agentico."
	default:
		return pending.Tag + " is answered in Agentico."
	}
}

func (n *Notifier) rejectResponderReply(
	token string,
	candidate responderReplyCandidate,
	pending pendingInputRecord,
	reason, reaction, line string,
) {
	n.enqueueResponderFeedback(
		token,
		candidate.Thread,
		firstNonempty(pending.SourceFeatureID, candidate.Thread.featureID),
		candidate.Message.TS,
		reaction,
		line,
	)
	decision, _ := responderReplyDecision(pending.Kind, candidate.Message.Text)
	n.emitResponderRejected(candidate.Thread, pending, decision, "reply", reason)
}

func (n *Notifier) enqueueResponderFeedback(
	token string,
	source responderThread,
	sourceFeatureID string,
	messageTS, reaction, line string,
) {
	work := make([]workItem, 0, 2)
	if messageTS != "" && reaction != "" {
		work = append(work, workItem{
			featureID: source.featureID, sourceFeatureID: sourceFeatureID,
			destinationKey: source.destinationKey,
			kind:           candidateDestinationKind(source.destinationKey), channelID: source.channelID,
			responder: true, reaction: reactionPayload{messageTS: messageTS, name: reaction},
		})
	}
	if line != "" {
		work = append(work, workItem{
			featureID: source.featureID, sourceFeatureID: sourceFeatureID,
			destinationKey: source.destinationKey,
			kind:           candidateDestinationKind(source.destinationKey), channelID: source.channelID,
			responder: true, reply: replyPayload{
				kind: kindNeedsInput, fallback: scrub(token, line),
			},
		})
	}
	if len(work) == 0 {
		return
	}
	item := queueItem{
		kind:  kindNeedsInput,
		event: ports.Event{Type: ports.SessionOutput, FeatureID: source.featureID},
	}
	item.reservation = n.queue.reserveProtected(item.event)
	n.dispatchDeliveryGroup(item, work)
}

func (n *Notifier) emitResponderAccepted(
	thread responderThread,
	pending pendingInputRecord,
	decision, medium string,
) {
	n.emitEvent(thread.featureID, "slack.answer_received", map[string]any{
		"destination_kind": candidateDestinationKind(thread.destinationKey),
		"input_kind":       pending.Kind, "tag": pending.Tag,
		"decision": decision, "medium": medium,
	})
}

func (n *Notifier) emitResponderRejected(
	thread responderThread,
	pending pendingInputRecord,
	decision, medium, reason string,
) {
	data := map[string]any{
		"destination_kind": candidateDestinationKind(thread.destinationKey),
		"input_kind":       pending.Kind, "tag": pending.Tag,
		"medium": medium, "reason": reason,
	}
	if decision != "" {
		data["decision"] = decision
	}
	n.emitEvent(thread.featureID, "slack.answer_rejected", data)
}

func (n *Notifier) logResponderFailure(token string, cause error) {
	if cause != nil {
		log.Printf("slack-notifier: submitting a Slack answer failed: %s", scrub(token, cause.Error()))
	}
}

func (n *Notifier) enqueueAcceptedResponderWrites(
	token string,
	pending pendingInputRecord,
	source responderThread,
	replyTS, responderID, decision string,
) {
	confirmation := responderConfirmation(
		token,
		pending.Tag,
		pending.Kind,
		decision,
		responderID,
	)
	n.recordMu.Lock()
	record := n.records[source.featureID]
	if record == nil {
		n.recordMu.Unlock()
		return
	}
	work := make([]workItem, 0, len(record.Destinations)+1)
	if replyTS != "" {
		work = append(work, workItem{
			featureID:       source.featureID,
			sourceFeatureID: pending.SourceFeatureID,
			destinationKey:  source.destinationKey,
			kind:            candidateDestinationKind(source.destinationKey),
			channelID:       source.channelID,
			responder:       true,
			reaction: reactionPayload{
				messageTS: replyTS,
				name:      "white_check_mark",
			},
		})
	}
	for key, destination := range record.Destinations {
		if destination.ChannelID == "" || destination.RootTS == "" {
			continue
		}
		work = append(work, workItem{
			featureID:       source.featureID,
			sourceFeatureID: pending.SourceFeatureID,
			destinationKey:  key,
			kind:            destination.Kind,
			channelID:       destination.ChannelID,
			responder:       true,
			reply: replyPayload{
				kind:     kindNeedsInput,
				fallback: confirmation,
			},
		})
	}
	n.recordMu.Unlock()
	if len(work) == 0 {
		return
	}
	item := queueItem{
		kind: kindNeedsInput,
		event: ports.Event{
			Type:      ports.SessionOutput,
			FeatureID: source.featureID,
		},
	}
	item.reservation = n.queue.reserveProtected(item.event)
	n.dispatchDeliveryGroup(item, work)
}

func responderConfirmation(token, tag, inputKind, decision, responderID string) string {
	action := decision
	switch {
	case inputKind == string(ports.SlackPendingPermission) &&
		decision == string(ports.SlackPermissionAllowOnce):
		action = "allowed once"
	case inputKind == string(ports.SlackPendingPermission) &&
		decision == string(ports.SlackPermissionDeny):
		action = "denied"
	case inputKind == string(ports.SlackPendingReview):
		action = "approved"
	}
	return scrub(token, tag+" was "+action+" by <@"+responderID+"> via Slack.")
}

func (n *Notifier) pendingResponderTarget(
	featureID, identity string,
) (pendingInputRecord, bool) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[featureID]
	if record == nil {
		return pendingInputRecord{}, false
	}
	for _, pending := range record.Pending {
		if pending.Identity == identity {
			return pending, true
		}
	}
	return pendingInputRecord{}, false
}

func (n *Notifier) resolveResponderTarget(
	featureID, identity, responderID, responderName string,
) bool {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[featureID]
	if record == nil {
		return false
	}
	var resolution *postingResolution
	for i := range record.Pending {
		if record.Pending[i].Identity != identity {
			continue
		}
		if record.Pending[i].Resolution != nil {
			return false
		}
		record.Pending[i].Resolution = &postingResolution{
			Kind:          resolutionSlack,
			ResponderID:   responderID,
			ResponderName: responderName,
			ResolvedAt:    n.responderClock.Now().UTC(),
		}
		resolution = record.Pending[i].Resolution
		break
	}
	if resolution == nil {
		return false
	}
	for key, destination := range record.Destinations {
		for i := range destination.PostingIndex {
			if destination.PostingIndex[i].Identity == identity {
				copy := *resolution
				destination.PostingIndex[i].Resolution = &copy
			}
		}
		record.Destinations[key] = destination
	}
	err := n.persistRecordLocked(featureID, record, "")
	n.logPersistError(err, "")
	return true
}

func (n *Notifier) judgeResponderReaction(candidate responderReactionCandidate) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[candidate.Thread.featureID]
	if record == nil {
		return
	}
	for i := range record.Pending {
		if record.Pending[i].Identity != candidate.Target.Identity {
			continue
		}
		record.Pending[i].judgedReactionAppend(
			candidate.Thread.destinationKey,
			candidate.MessageTS,
			candidate.Name,
			candidate.UserID,
		)
		break
	}
	err := n.persistRecordLocked(candidate.Thread.featureID, record, "")
	n.logPersistError(err, "")
}

func (n *Notifier) responderName(client slackClient, token, userID string) string {
	n.responderNameMu.Lock()
	defer n.responderNameMu.Unlock()
	if name, ok := n.responderNames[userID]; ok {
		return name
	}
	name := userID
	if user, err := client.UserInfo(n.responderBase, userID); err == nil {
		name = firstNonempty(user.DisplayName, user.RealName, user.Name, userID)
	}
	name = scrub(token, name)
	n.responderNames[userID] = name
	return name
}

func candidateDestinationKind(destinationKey string) string {
	kind, _, _ := strings.Cut(destinationKey, ":")
	return kind
}

func newestPostingBefore(postings []postingIndexEntry, messageTS string) (postingIndexEntry, bool) {
	var newest postingIndexEntry
	found := false
	for _, posting := range postings {
		if compareSlackTimestamps(posting.MessageTS, messageTS) >= 0 {
			continue
		}
		if !found || compareSlackTimestamps(posting.MessageTS, newest.MessageTS) > 0 {
			newest = posting
			found = true
		}
	}
	return newest, found
}

func compareSlackTimestamps(left, right string) int {
	leftWhole, leftFraction, leftOK := splitSlackTimestamp(left)
	rightWhole, rightFraction, rightOK := splitSlackTimestamp(right)
	if !leftOK || !rightOK {
		return strings.Compare(left, right)
	}
	if len(leftWhole) != len(rightWhole) {
		if len(leftWhole) < len(rightWhole) {
			return -1
		}
		return 1
	}
	if compared := strings.Compare(leftWhole, rightWhole); compared != 0 {
		return compared
	}
	width := max(len(leftFraction), len(rightFraction))
	leftFraction += strings.Repeat("0", width-len(leftFraction))
	rightFraction += strings.Repeat("0", width-len(rightFraction))
	return strings.Compare(leftFraction, rightFraction)
}

func splitSlackTimestamp(value string) (string, string, bool) {
	whole, fraction, found := strings.Cut(value, ".")
	if !found || whole == "" || fraction == "" {
		return "", "", false
	}
	for _, part := range []string{whole, fraction} {
		if strings.IndexFunc(part, func(r rune) bool {
			return r < '0' || r > '9'
		}) >= 0 {
			return "", "", false
		}
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole, fraction, true
}
