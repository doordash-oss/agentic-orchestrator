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
	oldest string
	cursor string
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
		key := thread.channelID + "\x00" + thread.rootTS
		active[key] = true
		state := n.responderPolls[key]
		if state.oldest != thread.oldest {
			state = responderPollState{oldest: thread.oldest}
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
				break
			}
			pageReplies, pageReactions := n.extractResponderCandidates(thread, page.Messages)
			replies = append(replies, pageReplies...)
			reactions = append(reactions, pageReactions...)
			state.cursor = page.NextCursor
			if state.cursor == "" {
				break
			}
		}
		n.responderPolls[key] = state
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
	for key := range n.responderPolls {
		if !active[key] {
			delete(n.responderPolls, key)
		}
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
		return
	}
	pending, ok := n.pendingResponderTarget(candidate.Thread.featureID, candidate.Target.Identity)
	if !ok || pending.Resolution != nil {
		return
	}
	if pending.Kind != string(ports.SlackPendingPermission) {
		return
	}
	decision, parsed := parsePermissionReply(candidate.Message.Text)
	if !parsed {
		return
	}
	responderName := n.responderName(client, token, candidate.Message.User)
	result := n.answer.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       pending.RequestID,
		SourceFeatureID: pending.SourceFeatureID,
		Decision:        decision,
		Source: ports.AnswerSource{
			Kind:      ports.AnswerSourceSlack,
			Responder: responderName,
		},
	})
	if result.Outcome != ports.SlackAnswerAccepted {
		return
	}
	if n.resolveResponderTarget(
		candidate.Thread.featureID,
		candidate.Target.Identity,
		candidate.Message.User,
		responderName,
	) {
		n.emitEvent(candidate.Thread.featureID, "slack.answer_received", map[string]any{
			"destination_kind": candidateDestinationKind(candidate.Thread.destinationKey),
			"input_kind":       pending.Kind,
			"tag":              pending.Tag,
			"decision":         string(decision),
			"medium":           "reply",
		})
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
		return
	}
	if pending.Kind != string(ports.SlackPendingPermission) {
		return
	}
	decision := ports.SlackPermissionAllowOnce
	if candidate.Name == "x" {
		decision = ports.SlackPermissionDeny
	}
	responderName := n.responderName(client, token, candidate.UserID)
	result := n.answer.AnswerSlackPermission(ports.SlackPermissionAnswer{
		RequestID:       pending.RequestID,
		SourceFeatureID: pending.SourceFeatureID,
		Decision:        decision,
		Source: ports.AnswerSource{
			Kind:      ports.AnswerSourceSlack,
			Responder: responderName,
		},
	})
	n.judgeResponderReaction(candidate)
	if result.Outcome != ports.SlackAnswerAccepted {
		return
	}
	if n.resolveResponderTarget(
		candidate.Thread.featureID,
		candidate.Target.Identity,
		candidate.UserID,
		responderName,
	) {
		n.emitEvent(candidate.Thread.featureID, "slack.answer_received", map[string]any{
			"destination_kind": candidateDestinationKind(candidate.Thread.destinationKey),
			"input_kind":       pending.Kind,
			"tag":              pending.Tag,
			"decision":         string(decision),
			"medium":           "reaction",
		})
	}
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
