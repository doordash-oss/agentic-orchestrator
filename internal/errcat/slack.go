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
	SlackInvalidToken          Code = "slack_invalid_token"
	SlackUnsupportedToken      Code = "slack_unsupported_token"
	SlackMissingScopes         Code = "slack_missing_scopes"
	SlackUnreachable           Code = "slack_unreachable"
	SlackUserNotFound          Code = "slack_user_not_found"
	SlackChannelNotFound       Code = "slack_channel_not_found"
	SlackNotInChannel          Code = "slack_not_in_channel"
	SlackChannelArchived       Code = "slack_channel_archived"
	SlackAmbiguousHandle       Code = "slack_ambiguous_handle"
	SlackScanCapReached        Code = "slack_scan_cap_reached"
	SlackUnrecognizedRecipient Code = "slack_unrecognized_recipient"
	SlackDeliveryFailed        Code = "slack_delivery_failed"
)

// SlackMissingScopesParams carries the stable missing-scope list.
type SlackMissingScopesParams struct {
	Scopes []string
}

func (SlackMissingScopesParams) params() {}

// SlackRecipientParams identifies the recipient named in an error.
type SlackRecipientParams struct {
	Recipient string
}

func (SlackRecipientParams) params() {}

// SlackAmbiguousHandleParams describes a non-unique handle match.
type SlackAmbiguousHandleParams struct {
	Handle     string
	MatchCount int
}

func (SlackAmbiguousHandleParams) params() {}

// SlackScanCapReachedParams describes the bounded Slack directory scan.
type SlackScanCapReachedParams struct {
	Name    string
	Kind    string
	Scanned int
}

func (SlackScanCapReachedParams) params() {}

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

func slackRecipientSummary(p Params, format string) string {
	params, ok := p.(SlackRecipientParams)
	if !ok || strings.TrimSpace(params.Recipient) == "" {
		return ""
	}
	return fmt.Sprintf(format, strings.TrimSpace(params.Recipient))
}

func slackAmbiguousHandleSummary(p Params) string {
	params, ok := p.(SlackAmbiguousHandleParams)
	if !ok || strings.TrimSpace(params.Handle) == "" || params.MatchCount < 2 {
		return ""
	}
	return fmt.Sprintf(
		"Slack found %d people matching %s.",
		params.MatchCount,
		strings.TrimSpace(params.Handle),
	)
}

func slackScanCapReachedSummary(p Params) string {
	params, ok := p.(SlackScanCapReachedParams)
	if !ok || strings.TrimSpace(params.Name) == "" ||
		strings.TrimSpace(params.Kind) == "" || params.Scanned <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"Slack did not find %s among the first %s %s scanned.",
		strings.TrimSpace(params.Name),
		formatSlackCount(params.Scanned),
		strings.TrimSpace(params.Kind),
	)
}

func formatSlackCount(value int) string {
	raw := fmt.Sprintf("%d", value)
	for index := len(raw) - 3; index > 0; index -= 3 {
		raw = raw[:index] + "," + raw[index:]
	}
	return raw
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
		Summary:     "Agentico could not reach Slack.",
		Remediation: "Check network connectivity and Slack availability, then try again.",
	}
	catalog[SlackUserNotFound] = Entry{
		Class:       ClassWarning,
		Title:       "Slack user not found",
		Summary:     "Slack could not find that person.",
		Remediation: "Check the person's email address or use their Slack member ID.",
	}
	catalog[SlackChannelNotFound] = Entry{
		Class:       ClassWarning,
		Title:       "Slack channel not found",
		Summary:     "Slack could not find that channel.",
		Remediation: "Check the channel name or use its Slack channel ID.",
	}
	catalog[SlackNotInChannel] = Entry{
		Class:   ClassWarning,
		Title:   "Agentico is not in the Slack channel",
		Summary: "Agentico is not a member of that Slack channel.",
		summaryParams: func(p Params) string {
			return slackRecipientSummary(p, "Agentico is not a member of %s.")
		},
		Remediation: "Invite the Agentico app to the channel in Slack, then try again.",
	}
	catalog[SlackChannelArchived] = Entry{
		Class:   ClassWarning,
		Title:   "Slack channel is archived",
		Summary: "That Slack channel is archived.",
		summaryParams: func(p Params) string {
			return slackRecipientSummary(p, "%s is archived.")
		},
		Remediation: "Choose an active Slack channel.",
	}
	catalog[SlackAmbiguousHandle] = Entry{
		Class:         ClassWarning,
		Title:         "Slack handle is ambiguous",
		Summary:       "More than one Slack member matches that handle.",
		Remediation:   "Use the person's email address or Slack member ID.",
		summaryParams: slackAmbiguousHandleSummary,
	}
	catalog[SlackScanCapReached] = Entry{
		Class:         ClassWarning,
		Title:         "Slack recipient scan limit reached",
		Summary:       "The recipient was not found within the Slack directory scan limit.",
		Remediation:   "Use the person's email address or a Slack ID instead.",
		summaryParams: slackScanCapReachedSummary,
	}
	catalog[SlackUnrecognizedRecipient] = Entry{
		Class:       ClassWarning,
		Title:       "Slack recipient was not recognized",
		Summary:     "Enter an email address, @handle, #channel, or Slack member or channel ID.",
		Remediation: "Correct the recipient text, then try again.",
	}
	catalog[SlackDeliveryFailed] = Entry{
		Class:       ClassWarning,
		Title:       "Slack message was not delivered",
		Summary:     "Slack could not deliver the test message to this recipient.",
		Remediation: "Check that the recipient can receive messages from the Agentico app, then try again.",
	}
}
