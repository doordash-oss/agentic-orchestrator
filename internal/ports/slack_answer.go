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

package ports

import (
	"crypto/sha256"
	"fmt"
	"time"
)

// AnswerSourceKind identifies the client that supplied an answer.
type AnswerSourceKind string

const (
	AnswerSourceSlack   AnswerSourceKind = "slack"
	AnswerSourceDesktop AnswerSourceKind = "desktop"
)

// Valid reports whether kind is accepted by the answer-source contract.
func (kind AnswerSourceKind) Valid() bool {
	switch kind {
	case AnswerSourceSlack, AnswerSourceDesktop:
		return true
	default:
		return false
	}
}

// AnswerSource records answer provenance without changing the agent-facing
// answer text.
type AnswerSource struct {
	Kind      AnswerSourceKind `json:"kind" yaml:"kind"`
	Responder string           `json:"responder,omitempty" yaml:"responder,omitempty"`
}

// SlackPermissionDecision is the permission grammar supported by Slack.
type SlackPermissionDecision string

const (
	SlackPermissionAllowOnce SlackPermissionDecision = "allow_once"
	SlackPermissionDeny      SlackPermissionDecision = "deny"
)

// SlackPermissionAnswer identifies one pending permission and its decision.
type SlackPermissionAnswer struct {
	RequestID       string
	SourceFeatureID string
	Decision        SlackPermissionDecision
	Source          AnswerSource
}

// SlackReviewApproval identifies the exact review revision a reader approved.
type SlackReviewApproval struct {
	SourceFeatureID string
	ReviewID        string
	SourceRevision  string
	Source          AnswerSource
}

// SlackQuestionIndexedAnswer identifies a question by position, without copying its text.
type SlackQuestionIndexedAnswer struct {
	Index int
	Value string
}

type SlackQuestionAnswer struct {
	SourceFeatureID string
	RequestID       string
	Answers         []SlackQuestionIndexedAnswer
	Source          AnswerSource
}

type SlackHelpAnswer struct {
	SourceFeatureID string
	EntryIdentity   string
	Text            string
	Source          AnswerSource
}

// SlackHelpEntryIdentity matches the identity of a posted pending help item.
func SlackHelpEntryIdentity(featureID string, waitingSince time.Time, question string) string {
	digest := sha256.Sum256([]byte(question))
	return fmt.Sprintf("help:%s:%s:%x", featureID,
		waitingSince.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), digest[:8])
}

// SlackAnswerOutcome classifies the result of an in-process Slack mutation.
type SlackAnswerOutcome string

const (
	SlackAnswerAccepted        SlackAnswerOutcome = "accepted"
	SlackAnswerNoLongerPending SlackAnswerOutcome = "no_longer_pending"
	SlackAnswerRevisionMoved   SlackAnswerOutcome = "revision_moved"
	SlackAnswerFailed          SlackAnswerOutcome = "failed"
)

// SlackAnswerResult reports a typed outcome and retains a cause only for logs.
type SlackAnswerResult struct {
	Outcome SlackAnswerOutcome
	Cause   error
}

// SlackAnswerPort submits answerable Slack items to their in-process mutations.
type SlackAnswerPort interface {
	AnswerSlackPermission(SlackPermissionAnswer) SlackAnswerResult
	ApproveSlackReview(SlackReviewApproval) SlackAnswerResult
	AnswerSlackQuestion(SlackQuestionAnswer) SlackAnswerResult
	AnswerSlackHelp(SlackHelpAnswer) SlackAnswerResult
}
