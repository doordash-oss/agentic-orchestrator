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

package tui

import (
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

const (
	legacyWaitingInputHelpMessage = "Agent is waiting for input — attach with 'a' to respond"
	legacyQuestionHelpMessage     = "Agent has a question — attach with 'a' to respond"
	legacyAPIErrorHelpSuffix      = " — attach with 'a' to respond"
	apiErrorHelpPrefix            = "API error:"
	apiErrorHelpSuffix            = " — press 'a' to answer"
	waitingInputHelpMessage       = "Agent is waiting for input — press 'a' to answer"
	questionHelpMessage           = "Agent has a question — press 'a' to answer"
)

func normalizeManagedHelpQuestion(question string) string {
	switch question {
	case legacyWaitingInputHelpMessage:
		return waitingInputHelpMessage
	case legacyQuestionHelpMessage:
		return questionHelpMessage
	default:
		if strings.HasPrefix(question, apiErrorHelpPrefix) && strings.HasSuffix(question, legacyAPIErrorHelpSuffix) {
			return strings.TrimSuffix(question, legacyAPIErrorHelpSuffix) + apiErrorHelpSuffix
		}
		return question
	}
}

func sameManagedHelpMessage(got, want string) bool {
	return normalizeManagedHelpQuestion(got) == normalizeManagedHelpQuestion(want)
}

func normalizeManagedHelpQueue(f *feature.Feature) bool {
	if f == nil {
		return false
	}
	changed := false
	for i := range f.HelpQueue {
		normalized := normalizeManagedHelpQuestion(f.HelpQueue[i].Question)
		if normalized == f.HelpQueue[i].Question {
			continue
		}
		f.HelpQueue[i].Question = normalized
		changed = true
	}
	return changed
}

func hasHelpRequestMessage(f *feature.Feature, question string) bool {
	if f == nil {
		return false
	}
	for _, h := range f.HelpQueue {
		if sameManagedHelpMessage(h.Question, question) {
			return true
		}
	}
	return false
}

func hasPendingHelpRequestMessage(f *feature.Feature, question string) bool {
	if f == nil {
		return false
	}
	for _, h := range f.HelpQueue {
		if sameManagedHelpMessage(h.Question, question) && h.Pending {
			return true
		}
	}
	return false
}
