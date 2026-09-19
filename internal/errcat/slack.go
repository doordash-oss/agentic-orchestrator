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

package errcat

import (
	"fmt"
	"sort"
	"strings"
)

const (
	SlackInvalidToken     Code = "slack_invalid_token"
	SlackUnsupportedToken Code = "slack_unsupported_token"
	SlackMissingScopes    Code = "slack_missing_scopes"
	SlackUnreachable      Code = "slack_unreachable"
)

// SlackMissingScopesParams carries the stable missing-scope list.
type SlackMissingScopesParams struct {
	Scopes []string
}

func (SlackMissingScopesParams) params() {}

func slackMissingScopesSummary(p Params) string {
	params, ok := p.(SlackMissingScopesParams)
	if !ok {
		return ""
	}
	scopes := append([]string(nil), params.Scopes...)
	sort.Strings(scopes)
	unique := scopes[:0]
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || (len(unique) > 0 && unique[len(unique)-1] == scope) {
			continue
		}
		unique = append(unique, scope)
	}
	if len(unique) == 0 {
		return ""
	}
	return fmt.Sprintf("The Slack token is missing required scopes: %s.", strings.Join(unique, ", "))
}

func init() {
	catalog[SlackInvalidToken] = Entry{
		Class:       ClassBlocking,
		Title:       "Slack token is invalid",
		Summary:     "Slack rejected the token.",
		Remediation: "Re-copy the OAuth token from the Slack app's install page, then try again.",
	}
	catalog[SlackUnsupportedToken] = Entry{
		Class:       ClassBlocking,
		Title:       "Slack token type is unsupported",
		Summary:     "The token is not a supported Slack bot or user OAuth token.",
		Remediation: "Paste an xoxb bot token or xoxp user token from the Slack app's install page.",
	}
	catalog[SlackMissingScopes] = Entry{
		Class:         ClassBlocking,
		Title:         "Slack token is missing scopes",
		Summary:       "The Slack token does not grant every required scope.",
		Remediation:   "Reinstall the Slack app using the current Agentico manifest, then copy the new OAuth token.",
		summaryParams: slackMissingScopesSummary,
	}
	catalog[SlackUnreachable] = Entry{
		Class:       ClassWarning,
		Title:       "Slack could not be reached",
		Summary:     "Agentico could not validate the Slack token because Slack was unreachable.",
		Remediation: "Check network connectivity and Slack availability, then check the connection again.",
	}
}
