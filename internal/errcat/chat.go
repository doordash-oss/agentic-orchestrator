// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"fmt"
	"strings"
)

// Chat-context codes. An explain-in-chat turn can attach a structured
// reference to the durable home of the error it asks about; these codes
// report the two ways that reference fails before any chat turn is sent.
// Neither references an action or declares context blocks: the only path
// forward is acting on the card or view the user is already looking at.
const (
	// ChatContextInvalid marks a malformed reference: an unknown scope, a
	// key missing or foreign to the scope, or an unknown feature.
	ChatContextInvalid Code = "chat_context_invalid"
	// ChatContextNotFound marks a well-formed reference whose home no
	// longer holds the referenced error.
	ChatContextNotFound Code = "chat_context_not_found"
)

// ChatContextParams carries the scope and code a chat-context summary
// names, so the summary says what was referenced.
type ChatContextParams struct {
	Scope string
	Code  string
}

func (ChatContextParams) params() {}

// chatContextScope renders the reference's scope clause, or "" when the
// scope is absent.
func chatContextScope(scope string) string {
	if scope = strings.TrimSpace(scope); scope == "" {
		return ""
	}
	return fmt.Sprintf("scope %q", scope)
}

// chatContextCode renders the referenced error's code clause, or "" when
// the code is absent.
func chatContextCode(code string) string {
	if code = strings.TrimSpace(code); code == "" {
		return ""
	}
	return fmt.Sprintf("code %q", code)
}

// chatContextClause joins the present clauses as `(scope "x", code "y")`,
// or "" when neither applies.
func chatContextClause(scope, code string) string {
	clauses := make([]string, 0, 2)
	if clause := chatContextScope(scope); clause != "" {
		clauses = append(clauses, clause)
	}
	if clause := chatContextCode(code); clause != "" {
		clauses = append(clauses, clause)
	}
	if len(clauses) == 0 {
		return ""
	}
	return " (" + strings.Join(clauses, ", ") + ")"
}

// chatContextInvalidSummary names the scope and code of the reference that
// could not be understood.
func chatContextInvalidSummary(p Params) string {
	params, ok := p.(ChatContextParams)
	if !ok {
		return ""
	}
	clause := chatContextClause(params.Scope, params.Code)
	if clause == "" {
		return ""
	}
	return "The chat context reference" + clause + " could not be understood."
}

// chatContextNotFoundSummary names the scope and code of the reference
// whose error is no longer present.
func chatContextNotFoundSummary(p Params) string {
	params, ok := p.(ChatContextParams)
	if !ok {
		return ""
	}
	clause := chatContextClause(params.Scope, params.Code)
	if clause == "" {
		return ""
	}
	return "The referenced error" + clause + " is no longer present."
}
