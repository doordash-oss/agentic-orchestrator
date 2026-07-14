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
