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
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func (n *Notifier) processTextResponderReply(
	client slackClient, token string, candidate responderReplyCandidate,
	pending pendingInputRecord, live map[string]ports.SlackPendingInput, claimKey string,
) bool {
	input, ok := live[pending.Identity]
	if !ok {
		n.releaseResponderClaim(claimKey)
		return false
	}
	answer := questionAnswer{answer: strings.TrimSpace(candidate.Message.Text), decision: "help_sent"}
	if pending.Kind == string(ports.SlackPendingQuestion) {
		answer = parseQuestionAnswer(input, candidate.Message.Text)
	} else if len(candidate.Message.Text) > sectionTextLimit || answer.answer == "" {
		answer = questionAnswer{hint: "This reply is too long or empty. Answer in Agentico."}
	}
	if answer.hint != "" {
		judged := n.rejectResponderReply(token, candidate, pending, "unparseable", "question",
			n.responderHintFor(candidate.Thread, answer.hint), claimKey)
		if !judged {
			n.releaseResponderClaim(claimKey)
		}
		return judged
	}
	name := n.responderName(client, token, candidate.Message.User)
	held := heldQuestionAnswer{
		Value: scrub(token, answer.answer), Decision: answer.decision,
		ResponderID: candidate.Message.User, ResponderName: name,
	}
	n.beginResponderSubmission(candidate.Thread.featureID, pending.Identity)
	defer n.endResponderSubmission(candidate.Thread.featureID, pending.Identity)
	if pending.Kind == string(ports.SlackPendingQuestion) {
		return n.acceptQuestionAnswer(token, candidate.Thread, pending, held, live,
			candidate.Message.TS, claimKey, "reply")
	}
	result := n.answer.AnswerSlackHelp(ports.SlackHelpAnswer{
		SourceFeatureID: pending.SourceFeatureID, EntryIdentity: pending.Identity,
		Text: held.Value, Source: ports.AnswerSource{Kind: ports.AnswerSourceSlack, Responder: name},
	})
	n.markResponderReplySubmitted(candidate)
	if result.Outcome != ports.SlackAnswerAccepted {
		if !n.handleRejectedResponderReply(token, candidate, pending, answer.decision, result, claimKey) {
			n.releaseResponderClaim(claimKey)
		}
		return true
	}
	if n.resolveResponderTarget(candidate.Thread.featureID, pending.Identity, held.ResponderID, name) {
		n.enqueueAcceptedResponderWrites(token, pending, candidate.Thread,
			candidate.Message.TS, held.ResponderID, answer.decision, claimKey)
		n.emitResponderAccepted(candidate.Thread, pending, answer.decision, "reply")
		return true
	}
	n.releaseResponderClaim(claimKey)
	return true
}

func (n *Notifier) processQuestionReaction(
	client slackClient, token string, candidate responderReactionCandidate,
	pending pendingInputRecord, live map[string]ports.SlackPendingInput, claimKey string,
) {
	input, ok := live[pending.Identity]
	index := keycapOption(candidate.Name)
	if !ok || input.MultiSelect || index == 0 || index > len(input.Options) {
		n.releaseResponderClaim(claimKey)
		return
	}
	if pending.HeldAnswer != nil {
		resolution := &postingResolution{Kind: resolutionSlack, ResponderID: pending.HeldAnswer.ResponderID}
		if n.rejectResolvedReaction(token, candidate, pending, resolution, claimKey) {
			n.judgeResponderReaction(candidate)
		} else {
			n.releaseResponderClaim(claimKey)
		}
		return
	}
	name := n.responderName(client, token, candidate.UserID)
	label := scrub(token, input.Options[index-1].Label)
	held := heldQuestionAnswer{Value: label,
		Decision: "option_selected", ResponderID: candidate.UserID, ResponderName: name}
	n.beginResponderSubmission(candidate.Thread.featureID, pending.Identity)
	defer n.endResponderSubmission(candidate.Thread.featureID, pending.Identity)
	n.acceptQuestionAnswer(token, candidate.Thread, pending, held, live,
		"", claimKey, "reaction")
	n.judgeResponderReaction(candidate)
}

func (n *Notifier) acceptQuestionAnswer(
	token string, thread responderThread, pending pendingInputRecord,
	held heldQuestionAnswer, live map[string]ports.SlackPendingInput,
	replyTS, claimKey, medium string,
) bool {
	n.recordMu.Lock()
	record := n.records[thread.featureID]
	if record == nil {
		n.recordMu.Unlock()
		n.releaseResponderClaim(claimKey)
		return true
	}
	items := make([]pendingInputRecord, 0)
	for _, item := range record.Pending {
		if item.Kind == string(ports.SlackPendingQuestion) &&
			item.SourceFeatureID == pending.SourceFeatureID && item.RequestID == pending.RequestID &&
			item.Resolution == nil && live[item.Identity].Kind == ports.SlackPendingQuestion {
			if item.Identity == pending.Identity {
				item.HeldAnswer = &held
			}
			items = append(items, item)
		}
	}
	n.recordMu.Unlock()
	count := live[pending.Identity].QuestionCount
	if count == 0 {
		count = len(items)
	}
	if len(items) != count || count == 0 {
		n.releaseResponderClaim(claimKey)
		return false
	}
	answers := make([]ports.SlackQuestionIndexedAnswer, 0, count)
	missing := make([]string, 0)
	for _, item := range items {
		if item.HeldAnswer == nil {
			missing = append(missing, item.Tag)
			continue
		}
		answers = append(answers, ports.SlackQuestionIndexedAnswer{
			Index: item.QuestionIndex, Value: item.HeldAnswer.Value,
		})
	}
	if len(missing) > 0 {
		n.recordMu.Lock()
		record = n.records[thread.featureID]
		for i := range record.Pending {
			if record.Pending[i].Identity == pending.Identity && record.Pending[i].HeldAnswer == nil {
				copy := held
				record.Pending[i].HeldAnswer = &copy
			}
		}
		err := n.persistRecordLocked(thread.featureID, record, "")
		n.recordMu.Unlock()
		n.logPersistError(err, "")
		if medium == "reply" {
			n.markResponderReplySubmitted(responderReplyCandidate{Thread: thread, Message: Message{TS: replyTS}})
		}
		line := fmt.Sprintf("%s was answered%s by <@%s> via Slack - %d of %d collected, not yet submitted; still waiting on %s.",
			pending.Tag, optionEcho(held), held.ResponderID, len(answers), count, strings.Join(missing, ", "))
		n.enqueueAcceptedResponderWrites(token, pending, thread, replyTS,
			held.ResponderID, held.Decision, claimKey, line)
		n.emitResponderAccepted(thread, pending, held.Decision, medium)
		return true
	}
	for _, item := range items {
		if item.Identity != pending.Identity {
			n.beginResponderSubmission(thread.featureID, item.Identity)
			defer n.endResponderSubmission(thread.featureID, item.Identity)
		}
	}
	result := n.answer.AnswerSlackQuestion(ports.SlackQuestionAnswer{
		SourceFeatureID: pending.SourceFeatureID, RequestID: pending.RequestID,
		Answers: answers, Source: ports.AnswerSource{
			Kind: ports.AnswerSourceSlack, Responder: held.ResponderName,
		},
	})
	if medium == "reply" {
		n.markResponderReplySubmitted(responderReplyCandidate{Thread: thread, Message: Message{TS: replyTS}})
	}
	if result.Outcome != ports.SlackAnswerAccepted {
		if result.Outcome == ports.SlackAnswerNoLongerPending {
			for _, item := range items {
				if resolved, ok := n.resolveResponderTargetByAgentico(thread.featureID, item.Identity); ok {
					n.enqueueResponderClosure(token, thread.featureID, resolved)
				}
			}
		}
		if medium == "reply" {
			candidate := responderReplyCandidate{
				Thread: thread, Target: postingIndexEntry{Identity: pending.Identity},
				Message: Message{TS: replyTS},
			}
			if !n.handleRejectedResponderReply(token, candidate, pending, held.Decision, result, claimKey) {
				n.releaseResponderClaim(claimKey)
			}
		} else {
			candidate := responderReactionCandidate{
				Thread: thread, Target: postingIndexEntry{Identity: pending.Identity},
			}
			if !n.handleRejectedResponderReaction(token, candidate, pending, held.Decision, result, claimKey) {
				n.releaseResponderClaim(claimKey)
			}
		}
		return true
	}
	n.recordMu.Lock()
	record = n.records[thread.featureID]
	for i := range record.Pending {
		for _, item := range items {
			if record.Pending[i].Identity == item.Identity {
				record.Pending[i].HeldAnswer = nil
			}
		}
	}
	n.recordMu.Unlock()
	for _, item := range items {
		n.resolveResponderTarget(thread.featureID, item.Identity,
			item.HeldAnswer.ResponderID, item.HeldAnswer.ResponderName)
		line := item.Tag + " was answered" + optionEcho(*item.HeldAnswer) +
			" by <@" + item.HeldAnswer.ResponderID + "> via Slack."
		ts := ""
		if item.Identity == pending.Identity {
			ts = replyTS
		}
		n.enqueueAcceptedResponderWrites(token, item, thread, ts,
			item.HeldAnswer.ResponderID, item.HeldAnswer.Decision, claimKey, line)
	}
	n.emitResponderAccepted(thread, pending, held.Decision, medium)
	return true
}

func optionEcho(answer heldQuestionAnswer) string {
	if answer.Decision != "option_selected" && answer.Decision != "options_selected" {
		return ""
	}
	label := answer.Value
	if answer.Decision == "options_selected" && len(label) > 180 {
		labels := strings.Split(label, ", ")
		if len(labels) > 2 {
			label = strings.Join(labels[:2], ", ") + fmt.Sprintf(" and %d more", len(labels)-2)
		}
	}
	return " '" + compactOptionLabel(label) + "'"
}
