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
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	responderPollInterval           = 10 * time.Second
	responderPageSize               = 100
	responderPageBudget             = 3
	responderResolvedRetentionLimit = 16
	responderFeedbackLimit          = 16
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
	oldestUnjudged      string
	consecutiveFailures int
	pauseUntil          time.Time
	suspended           bool
	credentialFailed    bool
	removed             bool
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

type responderDeferredFeedback struct {
	thread          responderThread
	pending         pendingInputRecord
	sourceFeatureID string
	messageTS       string
	reaction        string
	line            string
	decision        string
	medium          string
	reason          string
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
		if !n.startupDone.Load() {
			continue
		}
		if n.sweepRunning.CompareAndSwap(false, true) {
			n.responderWG.Add(1)
			go func() {
				defer n.responderWG.Done()
				defer n.sweepRunning.Store(false)
				n.sweepDestinations()
			}()
		}
		n.responderTick()
	}
}

func (n *Notifier) responderTick() {
	settings := n.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" {
		return
	}
	n.drainDeferredResponderFeedback(settings.Token)

	threads := n.responderThreads(settings, false)
	if len(threads) == 0 {
		return
	}
	featureIDs := make(map[string]struct{}, len(threads))
	for _, thread := range threads {
		featureIDs[thread.featureID] = struct{}{}
	}
	unavailable := make(map[string]bool)
	unavailableItems := make(map[string]map[string]bool)
	liveInputs := make(map[string]map[string]ports.SlackPendingInput, len(featureIDs))
	for featureID := range featureIDs {
		owner, err := n.store.Load(featureID)
		if err != nil || owner == nil {
			continue
		}
		record, err := n.recordFor(featureID)
		if err != nil {
			continue
		}
		ready, effective := n.effectiveSettings(settings, owner)
		if effective.Muted {
			ready.Categories.NeedsInput = false
		}
		ready.Recipients = nil
		n.recordMu.Lock()
		for _, recipient := range effective.Recipients {
			key := destinationKey(string(recipient.Kind), recipient.ID)
			if recipient.Kind != string(ports.SlackRecipientUser) || record.Destinations[key].ChannelID != "" {
				ready.Recipients = append(ready.Recipients, ports.SlackRecipient{
					TypedText: recipient.TypedText, Kind: ports.SlackRecipientKind(recipient.Kind),
					ID: recipient.ID, DisplayName: recipient.DisplayName,
				})
			}
		}
		n.recordMu.Unlock()
		work, readable, unreadable, live := n.reconcilePendingWithPolicy(
			ready,
			owner,
			owner,
			record,
			resolutionAgentico,
			false,
		)
		unavailableItems[featureID] = unreadable
		liveInputs[featureID] = live
		if !readable {
			unavailable[featureID] = true
		}
		if len(work) > 0 {
			item := queueItem{
				kind: kindNeedsInput,
				event: ports.Event{
					Type: ports.SessionOutput, FeatureID: featureID,
				},
			}
			item.reservation = n.queue.reserveProtected(item.event)
			n.dispatchDeliveryGroup(item, work)
		}
	}
	threads = n.responderThreads(settings, true)
	threads = slices.DeleteFunc(threads, func(thread responderThread) bool {
		return unavailable[thread.featureID]
	})
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
	returned := make(map[string][]string, len(threads))
	carriedUnjudged := make(map[string]string, len(threads))
	resumed := make(map[string]bool)
	for _, thread := range threads {
		key := responderPollKey(thread.featureID, thread.destinationKey)
		active[key] = true
		n.responderPollMu.Lock()
		state := n.responderPolls[key]
		n.responderPollMu.Unlock()
		resumed[key] = state.removed
		if state.oldest != thread.oldest {
			state.oldest = thread.oldest
			state.cursor = ""
			state.oldestUnjudged = ""
		}
		carriedUnjudged[key] = state.oldestUnjudged
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
			for _, message := range page.Messages {
				if message.TS != "" && message.TS != thread.rootTS {
					returned[key] = append(returned[key], message.TS)
				}
			}
			pageReplies, pageReactions := n.extractResponderCandidates(thread, page.Messages)
			if state.removed {
				for _, reaction := range pageReactions {
					n.judgeResponderReaction(reaction)
				}
			} else {
				replies = append(replies, pageReplies...)
				reactions = append(reactions, pageReactions...)
			}
			state.cursor = page.NextCursor
			if state.cursor == "" {
				break
			}
		}
		if state.removed && state.cursor == "" {
			state.removed = false
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
	unjudged := make(map[string]string)
	for _, candidate := range replies {
		if candidate.TargetFound && unavailableItems[candidate.Thread.featureID][candidate.Target.Identity] {
			key := responderPollKey(candidate.Thread.featureID, candidate.Thread.destinationKey)
			if unjudged[key] == "" ||
				compareSlackTimestamps(candidate.Message.TS, unjudged[key]) < 0 {
				unjudged[key] = candidate.Message.TS
			}
			continue
		}
		if !n.processResponderReply(client, settings.Token, candidate, liveInputs[candidate.Thread.featureID]) {
			key := responderPollKey(candidate.Thread.featureID, candidate.Thread.destinationKey)
			if unjudged[key] == "" ||
				compareSlackTimestamps(candidate.Message.TS, unjudged[key]) < 0 {
				unjudged[key] = candidate.Message.TS
			}
		}
	}
	for _, candidate := range reactions {
		if unavailableItems[candidate.Thread.featureID][candidate.Target.Identity] {
			continue
		}
		n.processResponderReaction(client, settings.Token, candidate, liveInputs[candidate.Thread.featureID])
	}
	for _, thread := range threads {
		key := responderPollKey(thread.featureID, thread.destinationKey)
		barrier := unjudged[key]
		if resumed[key] {
			n.advanceResponderReplyMark(thread, returned[key], "")
			continue
		}
		if carried := carriedUnjudged[key]; carried != "" &&
			(barrier == "" || compareSlackTimestamps(carried, barrier) < 0) {
			barrier = carried
		}
		n.advanceResponderReplyMark(thread, returned[key], barrier)
		n.responderPollMu.Lock()
		state := n.responderPolls[key]
		if state.cursor != "" {
			state.oldestUnjudged = barrier
		} else {
			state.oldestUnjudged = ""
		}
		n.responderPolls[key] = state
		n.responderPollMu.Unlock()
	}
	n.responderPollMu.Lock()
	for key := range n.responderPolls {
		if !active[key] && !n.responderPolls[key].removed {
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

func (n *Notifier) responderThreads(settings ports.SlackRuntimeSettings, eligibleOnly bool) []responderThread {
	n.recordMu.Lock()
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
			retained := make([]pendingInputRecord, 0, len(record.Pending)+len(record.Resolved))
			retained = append(retained, record.Pending...)
			retained = append(retained, record.Resolved...)
			for _, pending := range retained {
				messageTS := pending.MessageTS[key]
				if messageTS != "" &&
					(oldest == "" || compareSlackTimestamps(messageTS, oldest) < 0) {
					oldest = messageTS
				}
			}
			if oldest != "" {
				threads = append(threads, responderThread{
					featureID:      featureID,
					destinationKey: key,
					channelID:      destination.ChannelID,
					rootTS:         destination.RootTS,
					oldest:         oldest,
				})
			}
		}
	}
	n.recordMu.Unlock()
	if !eligibleOnly {
		return threads
	}

	// A record retains removed destinations for later re-addition. Only the
	// owner's live effective recipients may contribute readable threads.
	orders := make(map[string]map[string]int)
	effectiveKeys := make(map[string]map[string]bool)
	threads = slices.DeleteFunc(threads, func(thread responderThread) bool {
		byDestination, ok := orders[thread.featureID]
		if !ok {
			byDestination = make(map[string]int)
			keys := make(map[string]bool)
			owner, err := n.store.Load(thread.featureID)
			if err != nil {
				owner = nil
			}
			_, effective := n.effectiveSettings(settings, owner)
			for index, recipient := range effective.Recipients {
				key := destinationKey(recipient.Kind, recipient.ID)
				keys[key] = true
				if settings.Categories.NeedsInput || effective.NeedsInput.Enabled {
					byDestination[key] = index + 1
				}
			}
			orders[thread.featureID] = byDestination
			effectiveKeys[thread.featureID] = keys
		}
		if !effectiveKeys[thread.featureID][thread.destinationKey] {
			key := responderPollKey(thread.featureID, thread.destinationKey)
			n.responderPollMu.Lock()
			state := n.responderPolls[key]
			state.removed = true
			state.cursor = ""
			state.oldestUnjudged = ""
			n.responderPolls[key] = state
			n.responderPollMu.Unlock()
		}
		return byDestination[thread.destinationKey] == 0
	})
	for i := range threads {
		threads[i].destinationOrder = orders[threads[i].featureID][threads[i].destinationKey]
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
				var eligible bool
				for _, items := range [][]pendingInputRecord{record.Pending, record.Resolved} {
					for _, item := range items {
						if item.Identity == posting.Identity && responderReactionEligible(item.Kind, reaction.Name) {
							eligible = true
						}
					}
				}
				if !eligible {
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
			message.User == "" || message.BotID != "" || message.AppID != "" ||
			(message.Subtype != "" && message.Subtype != "thread_broadcast" && message.Subtype != "reply_broadcast") ||
			(destination.LastSeenReplyTS != "" &&
				compareSlackTimestamps(message.TS, destination.LastSeenReplyTS) <= 0) ||
			destination.ledgerContains(message.TS) ||
			destination.hasReactionForMessage(message.TS) ||
			destination.submittedReplyContains(message.TS) {
			continue
		}
		target, found := newestPostingBefore(destination.PostingIndex, message.TS)
		if tag, _, tagged := splitResponderTag(message.Text); tagged {
			target, found = taggedPostingBefore(destination.PostingIndex, tag, message.TS)
		}
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

func responderReactionEligible(kind, name string) bool {
	if kind == string(ports.SlackPendingQuestion) {
		return keycapOption(name) > 0
	}
	_, ok := responderReactionDecision(kind, name)
	return ok
}

func (n *Notifier) responderThreadLock(featureID, destinationKey string) *sync.RWMutex {
	key := responderPollKey(featureID, destinationKey)
	n.responderThreadMu.Lock()
	defer n.responderThreadMu.Unlock()
	lock := n.responderThreadLocks[key]
	if lock == nil {
		lock = &sync.RWMutex{}
		n.responderThreadLocks[key] = lock
	}
	return lock
}

func (n *Notifier) refreshResponderReplyCandidate(
	candidate responderReplyCandidate,
) (responderReplyCandidate, bool) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[candidate.Thread.featureID]
	if record == nil {
		return responderReplyCandidate{}, false
	}
	destination, ok := record.Destinations[candidate.Thread.destinationKey]
	if !ok ||
		(destination.LastSeenReplyTS != "" &&
			compareSlackTimestamps(candidate.Message.TS, destination.LastSeenReplyTS) <= 0) ||
		destination.ledgerContains(candidate.Message.TS) ||
		destination.hasReactionForMessage(candidate.Message.TS) ||
		destination.submittedReplyContains(candidate.Message.TS) {
		return responderReplyCandidate{}, false
	}
	candidate.Target, candidate.TargetFound = newestPostingBefore(
		destination.PostingIndex,
		candidate.Message.TS,
	)
	if tag, _, tagged := splitResponderTag(candidate.Message.Text); tagged {
		candidate.Target, candidate.TargetFound = taggedPostingBefore(
			destination.PostingIndex, tag, candidate.Message.TS,
		)
	}
	return candidate, true
}

func (n *Notifier) refreshResponderReactionCandidate(
	candidate responderReactionCandidate,
) (responderReactionCandidate, bool) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[candidate.Thread.featureID]
	if record == nil {
		return responderReactionCandidate{}, false
	}
	destination, ok := record.Destinations[candidate.Thread.destinationKey]
	if !ok || destination.reactionContains(candidate.MessageTS, candidate.Name) {
		return responderReactionCandidate{}, false
	}
	for _, posting := range destination.PostingIndex {
		if posting.MessageTS == candidate.MessageTS {
			candidate.Target = posting
			return candidate, true
		}
	}
	return responderReactionCandidate{}, false
}

func (n *Notifier) processResponderReply(
	client slackClient,
	token string,
	candidate responderReplyCandidate,
	live ...map[string]ports.SlackPendingInput,
) bool {
	claimKey := responderReplyClaimKey(candidate)
	threadLock := n.responderThreadLock(
		candidate.Thread.featureID,
		candidate.Thread.destinationKey,
	)
	if !threadLock.TryRLock() {
		return false
	}
	defer threadLock.RUnlock()
	var current bool
	candidate, current = n.refreshResponderReplyCandidate(candidate)
	if !current {
		return true
	}
	if !n.claimResponderCandidate(claimKey) {
		return false
	}
	if tag, answer, tagged := splitResponderTag(candidate.Message.Text); tagged {
		if !candidate.TargetFound || answer == "" {
			hint := "This tag does not name a pending item in this thread."
			if candidate.TargetFound && answer == "" {
				hint = tag + " needs an answer after the tag."
			}
			judged := n.rejectResponderReply(token, candidate, pendingInputRecord{},
				"not_answerable", "question", n.responderHintFor(candidate.Thread, hint), claimKey)
			if !judged {
				n.releaseResponderClaim(claimKey)
			}
			return judged
		}
		candidate.Message.Text = answer
	}
	if !candidate.TargetFound {
		judged := n.rejectResponderReply(
			token,
			candidate,
			pendingInputRecord{},
			"not_answerable",
			"question",
			n.responderHintFor(candidate.Thread, "This reply does not target an answerable item. Answer in Agentico."),
			claimKey,
		)
		if !judged {
			n.releaseResponderClaim(claimKey)
		}
		return judged
	}
	pending, ok := n.pendingResponderTarget(candidate.Thread.featureID, candidate.Target.Identity)
	if !ok {
		if candidate.Target.Resolution == nil {
			n.releaseResponderClaim(claimKey)
			return false
		}
		pending = pendingInputRecord{
			Identity:        candidate.Target.Identity,
			SourceFeatureID: candidate.Thread.featureID,
			Tag:             candidate.Target.Tag,
			Resolution:      candidate.Target.Resolution,
		}
	}
	if pending.Resolution != nil {
		judged := n.rejectResolvedReply(token, candidate, pending, pending.Resolution, claimKey)
		if !judged {
			n.releaseResponderClaim(claimKey)
		}
		return judged
	}
	if pending.HeldAnswer != nil {
		held := &postingResolution{Kind: resolutionSlack, ResponderID: pending.HeldAnswer.ResponderID}
		judged := n.rejectResolvedReply(token, candidate, pending, held, claimKey)
		if !judged {
			n.releaseResponderClaim(claimKey)
		}
		return judged
	}
	if pending.Kind == string(ports.SlackPendingQuestion) || pending.Kind == string(ports.SlackPendingHelp) {
		var inputs map[string]ports.SlackPendingInput
		if len(live) > 0 {
			inputs = live[0]
		}
		return n.processTextResponderReply(client, token, candidate, pending, inputs, claimKey)
	}
	decision, parsed := responderReplyDecision(pending.Kind, candidate.Message.Text)
	if !parsed {
		judged := n.rejectResponderReply(
			token,
			candidate,
			pending,
			responderUnparseableReason(pending.Kind),
			"question",
			n.responderHintFor(candidate.Thread, responderHint(pending)),
			claimKey,
		)
		if !judged {
			n.releaseResponderClaim(claimKey)
		}
		return judged
	}
	n.beginResponderSubmission(candidate.Thread.featureID, candidate.Target.Identity)
	defer n.endResponderSubmission(candidate.Thread.featureID, candidate.Target.Identity)
	responderName := n.responderName(client, token, candidate.Message.User)
	result := n.submitResponderAnswer(pending, decision, responderName)
	n.markResponderReplySubmitted(candidate)
	if result.Outcome != ports.SlackAnswerAccepted {
		if !n.handleRejectedResponderReply(
			token,
			candidate,
			pending,
			decision,
			result,
			claimKey,
		) {
			n.releaseResponderClaim(claimKey)
		}
		return true
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
			claimKey,
		)
		n.emitResponderAccepted(candidate.Thread, pending, decision, "reply")
		return true
	}
	n.releaseResponderClaim(claimKey)
	return true
}

func (n *Notifier) advanceResponderReplyMark(
	thread responderThread, returned []string, oldestUnjudged string,
) {
	if len(returned) == 0 {
		return
	}
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[thread.featureID]
	if record == nil {
		return
	}
	destination, ok := record.Destinations[thread.destinationKey]
	if !ok {
		return
	}
	newest := destination.LastSeenReplyTS
	for _, ts := range returned {
		if oldestUnjudged != "" && compareSlackTimestamps(ts, oldestUnjudged) >= 0 {
			continue
		}
		if newest == "" || compareSlackTimestamps(ts, newest) > 0 {
			newest = ts
		}
	}
	if newest == destination.LastSeenReplyTS {
		return
	}
	destination.LastSeenReplyTS = newest
	record.Destinations[thread.destinationKey] = destination
	err := n.persistRecordLocked(thread.featureID, record, "")
	n.logPersistError(err, "")
}

func (n *Notifier) processResponderReaction(
	client slackClient,
	token string,
	candidate responderReactionCandidate,
	live ...map[string]ports.SlackPendingInput,
) {
	claimKey := responderReactionClaimKey(candidate)
	threadLock := n.responderThreadLock(
		candidate.Thread.featureID,
		candidate.Thread.destinationKey,
	)
	if !threadLock.TryRLock() {
		return
	}
	defer threadLock.RUnlock()
	var current bool
	candidate, current = n.refreshResponderReactionCandidate(candidate)
	if !current {
		return
	}
	if !n.claimResponderCandidate(claimKey) {
		return
	}
	pending, ok := n.pendingResponderTarget(candidate.Thread.featureID, candidate.Target.Identity)
	if !ok || pending.judgedReactionContains(
		candidate.Thread.destinationKey,
		candidate.MessageTS,
		candidate.Name,
		candidate.UserID,
	) {
		n.releaseResponderClaim(claimKey)
		return
	}
	if !responderReactionEligible(pending.Kind, candidate.Name) {
		n.releaseResponderClaim(claimKey)
		return
	}
	if pending.Resolution != nil {
		if !n.rejectResolvedReaction(
			token,
			candidate,
			pending,
			pending.Resolution,
			claimKey,
		) {
			n.releaseResponderClaim(claimKey)
			return
		}
		n.judgeResponderReaction(candidate)
		return
	}
	if pending.Kind == string(ports.SlackPendingQuestion) {
		var inputs map[string]ports.SlackPendingInput
		if len(live) > 0 {
			inputs = live[0]
		}
		n.processQuestionReaction(client, token, candidate, pending, inputs, claimKey)
		return
	}
	decision, parsed := responderReactionDecision(pending.Kind, candidate.Name)
	if !parsed {
		n.releaseResponderClaim(claimKey)
		return
	}
	n.beginResponderSubmission(candidate.Thread.featureID, candidate.Target.Identity)
	defer n.endResponderSubmission(candidate.Thread.featureID, candidate.Target.Identity)
	responderName := n.responderName(client, token, candidate.UserID)
	result := n.submitResponderAnswer(pending, decision, responderName)
	n.judgeResponderReaction(candidate)
	if result.Outcome != ports.SlackAnswerAccepted {
		if !n.handleRejectedResponderReaction(
			token,
			candidate,
			pending,
			decision,
			result,
			claimKey,
		) {
			n.releaseResponderClaim(claimKey)
		}
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
			claimKey,
		)
		n.emitResponderAccepted(candidate.Thread, pending, decision, "reaction")
		return
	}
	n.releaseResponderClaim(claimKey)
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
	claimKey string,
) bool {
	line := ""
	reason := ""
	switch result.Outcome {
	case ports.SlackAnswerRevisionMoved:
		reason = "stale_revision"
		line = pending.Tag + " changed in Agentico. Approve the current review there."
	case ports.SlackAnswerNoLongerPending:
		reason = "already_resolved"
		line = pending.Tag + " was already answered by Agentico."
		if resolved, ok := n.resolveResponderTargetByAgentico(
			candidate.Thread.featureID,
			candidate.Target.Identity,
		); ok {
			n.enqueueResponderClosure(token, candidate.Thread.featureID, resolved)
		}
	case ports.SlackAnswerFailed:
		reason = "submit_failed"
		line = pending.Tag + " could not be submitted. Answer again or in Agentico."
		n.logResponderFailure(token, result.Cause)
	default:
		n.emitResponderRejected(candidate.Thread, pending, decision, "reply", "submit_failed")
		return false
	}
	if n.rejectResponderReply(
		token,
		candidate,
		pending,
		reason,
		"warning",
		line,
		claimKey,
	) {
		return true
	}
	n.deferResponderFeedback(claimKey, responderDeferredFeedback{
		thread: candidate.Thread, pending: pending,
		sourceFeatureID: pending.SourceFeatureID,
		messageTS:       candidate.Message.TS, reaction: "warning",
		line: line, decision: decision, medium: "reply", reason: reason,
	})
	return true
}

func (n *Notifier) handleRejectedResponderReaction(
	token string,
	candidate responderReactionCandidate,
	pending pendingInputRecord,
	decision string,
	result ports.SlackAnswerResult,
	claimKey string,
) bool {
	line := ""
	reason := ""
	switch result.Outcome {
	case ports.SlackAnswerRevisionMoved:
		reason = "stale_revision"
		line = pending.Tag + " changed in Agentico. Approve the current review there."
	case ports.SlackAnswerNoLongerPending:
		reason = "already_resolved"
		line = pending.Tag + " was already answered by Agentico."
		if resolved, ok := n.resolveResponderTargetByAgentico(
			candidate.Thread.featureID,
			candidate.Target.Identity,
		); ok {
			n.enqueueResponderClosure(token, candidate.Thread.featureID, resolved)
		}
	case ports.SlackAnswerFailed:
		reason = "submit_failed"
		line = pending.Tag + " could not be submitted. Answer again or in Agentico."
		n.logResponderFailure(token, result.Cause)
	default:
		return false
	}
	if !n.enqueueResponderFeedback(
		token,
		candidate.Thread,
		pending.SourceFeatureID,
		"",
		"",
		line,
		claimKey,
	) {
		n.deferResponderFeedback(claimKey, responderDeferredFeedback{
			thread: candidate.Thread, pending: pending,
			sourceFeatureID: pending.SourceFeatureID,
			line:            line, decision: decision, medium: "reaction", reason: reason,
		})
		return true
	}
	n.emitResponderRejected(candidate.Thread, pending, decision, "reaction", reason)
	return true
}

func (n *Notifier) rejectResolvedReply(
	token string,
	candidate responderReplyCandidate,
	pending pendingInputRecord,
	resolution *postingResolution,
	claimKey string,
) bool {
	return n.rejectResponderReply(
		token,
		candidate,
		pending,
		"already_resolved",
		"warning",
		alreadyAnsweredLine(pending.Tag, resolution),
		claimKey,
	)
}

func (n *Notifier) rejectResolvedReaction(
	token string,
	candidate responderReactionCandidate,
	pending pendingInputRecord,
	resolution *postingResolution,
	claimKey string,
) bool {
	if !n.enqueueResponderFeedback(
		token,
		candidate.Thread,
		pending.SourceFeatureID,
		"",
		"",
		alreadyAnsweredLine(pending.Tag, resolution),
		claimKey,
	) {
		return false
	}
	n.emitResponderRejected(candidate.Thread, pending, "", "reaction", "already_resolved")
	return true
}

func alreadyAnsweredLine(tag string, resolution *postingResolution) string {
	if resolution != nil && resolution.Kind == resolutionSlack && resolution.ResponderID != "" {
		return tag + " was already answered by <@" + resolution.ResponderID + ">."
	}
	return tag + " was already answered by Agentico."
}

func responderUnparseableReason(inputKind string) string {
	if inputKind == string(ports.SlackPendingPermission) ||
		inputKind == string(ports.SlackPendingReview) ||
		inputKind == string(ports.SlackPendingQuestion) ||
		inputKind == string(ports.SlackPendingHelp) {
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
	case string(ports.SlackPendingQuestion):
		return pending.Tag + " accepts an option number, its label, or a reply with other text."
	case string(ports.SlackPendingHelp):
		return pending.Tag + " accepts a reply with any text."
	default:
		return pending.Tag + " is answered in Agentico."
	}
}

func (n *Notifier) responderHintFor(thread responderThread, hint string) string {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[thread.featureID]
	if record == nil {
		return hint
	}
	var items []pendingInputRecord
	for _, item := range record.Pending {
		if item.MessageTS[thread.destinationKey] != "" {
			items = append(items, item)
		}
	}
	tags := pendingResponderTags(items)
	if len(tags) <= 1 {
		return hint
	}
	return hint + " Still waiting on " + strings.Join(tags, ", ") + "."
}

func (n *Notifier) rejectResponderReply(
	token string,
	candidate responderReplyCandidate,
	pending pendingInputRecord,
	reason, reaction, line string,
	claimKey string,
) bool {
	if !n.enqueueResponderFeedback(
		token,
		candidate.Thread,
		firstNonempty(pending.SourceFeatureID, candidate.Thread.featureID),
		candidate.Message.TS,
		reaction,
		line,
		claimKey,
	) {
		return false
	}
	decision, _ := responderReplyDecision(pending.Kind, candidate.Message.Text)
	n.emitResponderRejected(candidate.Thread, pending, decision, "reply", reason)
	return true
}

func (n *Notifier) enqueueResponderFeedback(
	token string,
	source responderThread,
	sourceFeatureID string,
	messageTS, reaction, line string,
	claimKey string,
) bool {
	work := make([]workItem, 0, 2)
	if messageTS != "" && reaction != "" {
		work = append(work, workItem{
			featureID: source.featureID, sourceFeatureID: sourceFeatureID,
			destinationKey: source.destinationKey,
			kind:           candidateDestinationKind(source.destinationKey), channelID: source.channelID,
			responder: true, responderFeedback: true,
			reaction: reactionPayload{messageTS: messageTS, name: reaction},
		})
	}
	if line != "" {
		work = append(work, workItem{
			featureID: source.featureID, sourceFeatureID: sourceFeatureID,
			destinationKey: source.destinationKey,
			kind:           candidateDestinationKind(source.destinationKey), channelID: source.channelID,
			responder: true, responderFeedback: true, reply: replyPayload{
				kind: kindNeedsInput, fallback: scrub(token, line),
			},
		})
	}
	if len(work) == 0 {
		return false
	}
	if !n.reserveResponderFeedback(source.channelID, len(work)) {
		return false
	}
	item := queueItem{
		kind:  kindNeedsInput,
		event: ports.Event{Type: ports.SessionOutput, FeatureID: source.featureID},
	}
	item.reservation = n.queue.reserveProtected(item.event)
	n.dispatchDeliveryGroupWithDone(item, work, func() {
		n.releaseResponderClaim(claimKey)
	})
	return true
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
	claimKey string,
	confirmationOverride ...string,
) {
	confirmation := responderConfirmation(
		token,
		pending.Tag,
		pending.Kind,
		decision,
		responderID,
	)
	if len(confirmationOverride) > 0 {
		confirmation = scrub(token, confirmationOverride[0])
	}
	owner, err := n.store.Load(source.featureID)
	if err != nil || owner == nil {
		n.releaseResponderClaim(claimKey)
		return
	}
	_, effective := n.effectiveSettings(n.settings.SlackSettings(), owner)
	allowed := make(map[string]bool, len(effective.Recipients))
	for _, recipient := range effective.Recipients {
		allowed[destinationKey(recipient.Kind, recipient.ID)] = true
	}
	n.recordMu.Lock()
	record := n.records[source.featureID]
	if record == nil {
		n.recordMu.Unlock()
		return
	}
	work := make([]workItem, 0, len(record.Destinations)+1)
	if replyTS != "" && allowed[source.destinationKey] {
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
		if !allowed[key] || destination.ChannelID == "" || destination.RootTS == "" {
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
		n.releaseResponderClaim(claimKey)
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
	n.dispatchDeliveryGroupWithDone(item, work, func() {
		n.releaseResponderClaim(claimKey)
	})
}

func responderReplyClaimKey(candidate responderReplyCandidate) string {
	return strings.Join([]string{
		"reply",
		candidate.Thread.featureID,
		candidate.Thread.destinationKey,
		candidate.Message.TS,
	}, "\x00")
}

func responderReactionClaimKey(candidate responderReactionCandidate) string {
	return strings.Join([]string{
		"reaction",
		candidate.Thread.featureID,
		candidate.Thread.destinationKey,
		candidate.MessageTS,
		candidate.Name,
		candidate.UserID,
	}, "\x00")
}

func (n *Notifier) claimResponderCandidate(key string) bool {
	n.responderFeedbackMu.Lock()
	defer n.responderFeedbackMu.Unlock()
	if _, exists := n.responderClaims[key]; exists {
		return false
	}
	n.responderClaims[key] = struct{}{}
	return true
}

func (n *Notifier) releaseResponderClaim(key string) {
	n.responderFeedbackMu.Lock()
	delete(n.responderClaims, key)
	n.responderFeedbackMu.Unlock()
}

func (n *Notifier) reserveResponderFeedback(channelID string, count int) bool {
	n.responderFeedbackMu.Lock()
	defer n.responderFeedbackMu.Unlock()
	outstanding := n.responderFeedback[channelID]
	if outstanding+count > responderFeedbackLimit {
		return false
	}
	n.responderFeedback[channelID] = outstanding + count
	return true
}

func (n *Notifier) responderFeedbackOutstanding(channelID string) int {
	n.responderFeedbackMu.Lock()
	defer n.responderFeedbackMu.Unlock()
	return n.responderFeedback[channelID]
}

func (n *Notifier) deferResponderFeedback(
	claimKey string,
	feedback responderDeferredFeedback,
) {
	n.responderFeedbackMu.Lock()
	if _, exists := n.responderDeferred[claimKey]; !exists {
		n.responderDeferredOrder = append(n.responderDeferredOrder, claimKey)
	}
	n.responderDeferred[claimKey] = feedback
	n.responderFeedbackMu.Unlock()
}

func (n *Notifier) drainDeferredResponderFeedback(token string) {
	blockedChannels := map[string]struct{}{}
	for index := 0; ; {
		n.responderFeedbackMu.Lock()
		if index >= len(n.responderDeferredOrder) {
			n.responderFeedbackMu.Unlock()
			return
		}
		claimKey := n.responderDeferredOrder[index]
		feedback, ok := n.responderDeferred[claimKey]
		if !ok {
			n.responderDeferredOrder = append(
				n.responderDeferredOrder[:index],
				n.responderDeferredOrder[index+1:]...,
			)
			n.responderFeedbackMu.Unlock()
			continue
		}
		if _, blocked := blockedChannels[feedback.thread.channelID]; blocked {
			index++
			n.responderFeedbackMu.Unlock()
			continue
		}
		n.responderFeedbackMu.Unlock()

		if !n.enqueueResponderFeedback(
			token,
			feedback.thread,
			feedback.sourceFeatureID,
			feedback.messageTS,
			feedback.reaction,
			feedback.line,
			claimKey,
		) {
			blockedChannels[feedback.thread.channelID] = struct{}{}
			index++
			continue
		}
		n.responderFeedbackMu.Lock()
		delete(n.responderDeferred, claimKey)
		for orderIndex, orderedKey := range n.responderDeferredOrder {
			if orderedKey != claimKey {
				continue
			}
			n.responderDeferredOrder = append(
				n.responderDeferredOrder[:orderIndex],
				n.responderDeferredOrder[orderIndex+1:]...,
			)
			if orderIndex < index {
				index--
			}
			break
		}
		n.responderFeedbackMu.Unlock()
		n.emitResponderRejected(
			feedback.thread,
			feedback.pending,
			feedback.decision,
			feedback.medium,
			feedback.reason,
		)
	}
}

func responderSubmissionKey(featureID, identity string) string {
	return featureID + "\x00" + identity
}

func (n *Notifier) beginResponderSubmission(featureID, identity string) {
	key := responderSubmissionKey(featureID, identity)
	n.responderFeedbackMu.Lock()
	n.responderSubmissions[key]++
	n.responderFeedbackMu.Unlock()
}

func (n *Notifier) endResponderSubmission(featureID, identity string) {
	key := responderSubmissionKey(featureID, identity)
	n.responderFeedbackMu.Lock()
	if n.responderSubmissions[key] <= 1 {
		delete(n.responderSubmissions, key)
	} else {
		n.responderSubmissions[key]--
	}
	n.responderFeedbackMu.Unlock()
}

func (n *Notifier) responderSubmissionInFlight(featureID, identity string) bool {
	key := responderSubmissionKey(featureID, identity)
	n.responderFeedbackMu.Lock()
	defer n.responderFeedbackMu.Unlock()
	return n.responderSubmissions[key] > 0
}

func (n *Notifier) releaseResponderFeedback(channelID string) {
	n.responderFeedbackMu.Lock()
	defer n.responderFeedbackMu.Unlock()
	outstanding := n.responderFeedback[channelID]
	if outstanding <= 1 {
		delete(n.responderFeedback, channelID)
		return
	}
	n.responderFeedback[channelID] = outstanding - 1
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
	case inputKind == string(ports.SlackPendingQuestion) ||
		inputKind == string(ports.SlackPendingHelp):
		action = "answered"
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
	// Owed closures retain older records until delivery, but must not expand
	// the reply-target window while those writes are still in flight.
	start := max(0, len(record.Resolved)-responderResolvedRetentionLimit)
	for _, resolved := range record.Resolved[start:] {
		if resolved.Identity == identity {
			return resolved, true
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
	record.propagatePostingResolution(identity, resolution)
	err := n.persistRecordLocked(featureID, record, "")
	n.logPersistError(err, "")
	return true
}

func (n *Notifier) resolveResponderTargetByAgentico(
	featureID, identity string,
) (pendingInputRecord, bool) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[featureID]
	if record == nil {
		return pendingInputRecord{}, false
	}
	var resolved pendingInputRecord
	for i := range record.Pending {
		if record.Pending[i].Identity != identity {
			continue
		}
		if record.Pending[i].Resolution != nil {
			return pendingInputRecord{}, false
		}
		record.Pending[i].Resolution = &postingResolution{
			Kind:       resolutionAgentico,
			ResolvedAt: n.responderClock.Now().UTC(),
		}
		record.Pending[i].HeldAnswer = nil
		resolved = record.Pending[i]
		break
	}
	if resolved.Resolution == nil {
		return pendingInputRecord{}, false
	}
	record.propagatePostingResolution(identity, resolved.Resolution)
	err := n.persistRecordLocked(featureID, record, "")
	n.logPersistError(err, "")
	return resolved, true
}

func (n *Notifier) enqueueResponderClosure(
	token, featureID string,
	pending pendingInputRecord,
) {
	n.recordMu.Lock()
	record := n.records[featureID]
	if record == nil {
		n.recordMu.Unlock()
		return
	}
	work := make([]workItem, 0, len(pending.MessageTS))
	for key, messageTS := range pending.MessageTS {
		if messageTS == "" {
			continue
		}
		destination := record.Destinations[key]
		if destination.ChannelID == "" || destination.RootTS == "" {
			continue
		}
		work = append(work, workItem{
			featureID:       featureID,
			sourceFeatureID: pending.SourceFeatureID,
			destinationKey:  key,
			kind:            destination.Kind,
			channelID:       destination.ChannelID,
			responder:       true,
			reply: replyPayload{
				kind: kindNeedsInput,
				fallback: scrub(
					token,
					pending.Tag+" was resolved in Agentico.",
				),
				identity: pending.Identity,
				closure:  true,
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
			Type: ports.SessionOutput, FeatureID: featureID,
		},
	}
	item.reservation = n.queue.reserveProtected(item.event)
	n.dispatchDeliveryGroup(item, work)
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
	for i := range record.Resolved {
		if record.Resolved[i].Identity != candidate.Target.Identity {
			continue
		}
		record.Resolved[i].judgedReactionAppend(
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

func (n *Notifier) markResponderReplySubmitted(candidate responderReplyCandidate) {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	record := n.records[candidate.Thread.featureID]
	if record == nil {
		return
	}
	destination, ok := record.Destinations[candidate.Thread.destinationKey]
	if !ok {
		return
	}
	destination.submittedReplyAppend(candidate.Message.TS)
	record.Destinations[candidate.Thread.destinationKey] = destination
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
