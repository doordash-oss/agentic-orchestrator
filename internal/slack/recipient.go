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
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	directoryPageSize = 200
	maxDirectoryPages = 20
)

var (
	memberIDPattern  = regexp.MustCompile(`^[UW][A-Z0-9]{8,}$`)
	channelIDPattern = regexp.MustCompile(`^[CG][A-Z0-9]{8,}$`)
)

// ResolveRecipient resolves user-entered text to a Slack member or channel.
func (s *Service) ResolveRecipient(
	ctx context.Context,
	token string,
	text string,
) (ports.SlackRecipient, error) {
	typedText := strings.TrimSpace(text)
	if typedText == "" {
		return ports.SlackRecipient{}, canonicalError(errcat.New(
			errcat.BadRequest,
			errcat.WithDiagnostics("Slack recipient text must not be empty"),
		))
	}
	if typedText == "@" || typedText == "#" ||
		strings.IndexFunc(typedText, unicode.IsSpace) >= 0 {
		return ports.SlackRecipient{}, unrecognizedRecipientError()
	}

	client, err := s.newClient(token)
	if err != nil {
		return ports.SlackRecipient{}, canonicalError(errcat.New(
			errcat.SlackUnreachable,
			errcat.WithDiagnostics(scrub(token, err.Error())),
		))
	}

	switch {
	case len(typedText) > 1 && strings.Contains(typedText[1:], "@"):
		return resolveEmail(ctx, client, token, typedText)
	case memberIDPattern.MatchString(typedText):
		return resolveMemberID(ctx, client, token, typedText)
	case channelIDPattern.MatchString(typedText):
		return resolveChannelID(ctx, client, token, typedText)
	case strings.HasPrefix(typedText, "#"):
		return resolveChannelName(ctx, client, token, typedText)
	case strings.HasPrefix(typedText, "@"):
		return resolveHandle(ctx, client, token, typedText, typedText[1:])
	default:
		return resolveHandle(ctx, client, token, typedText, typedText)
	}
}

func resolveEmail(
	ctx context.Context,
	client slackClient,
	token string,
	typedText string,
) (ports.SlackRecipient, error) {
	user, err := client.LookupUserByEmail(ctx, typedText)
	if err != nil {
		return ports.SlackRecipient{}, classifyResolveError(token, err, errcat.SlackUserNotFound)
	}
	return resolvedUser(typedText, user)
}

func resolveMemberID(
	ctx context.Context,
	client slackClient,
	token string,
	typedText string,
) (ports.SlackRecipient, error) {
	user, err := client.UserInfo(ctx, typedText)
	if err != nil {
		return ports.SlackRecipient{}, classifyResolveError(token, err, errcat.SlackUserNotFound)
	}
	return resolvedUser(typedText, user)
}

func resolveHandle(
	ctx context.Context,
	client slackClient,
	token string,
	typedText string,
	handle string,
) (ports.SlackRecipient, error) {
	// Display names are not unique, so a match is only accepted once the
	// whole directory has been scanned for a second one.
	var matches []User
	cursor := ""
	for pageNumber := 0; pageNumber < maxDirectoryPages; pageNumber++ {
		page, err := client.UsersList(ctx, cursor, directoryPageSize)
		if err != nil {
			return ports.SlackRecipient{}, classifyResolveError(token, err, errcat.SlackUserNotFound)
		}
		matches = append(matches, matchingUsers(page.Users, handle)...)
		if len(matches) > 1 {
			return ports.SlackRecipient{}, canonicalError(errcat.New(
				errcat.SlackAmbiguousHandle,
				errcat.WithParams(errcat.SlackAmbiguousHandleParams{
					Handle:     typedText,
					MatchCount: len(matches),
				}),
			))
		}
		if page.NextCursor == "" {
			if len(matches) == 1 {
				return resolvedUser(typedText, matches[0])
			}
			return ports.SlackRecipient{}, canonicalError(errcat.New(errcat.SlackUserNotFound))
		}
		cursor = page.NextCursor
	}
	if len(matches) == 1 {
		return resolvedUser(typedText, matches[0])
	}
	return ports.SlackRecipient{}, canonicalError(errcat.New(
		errcat.SlackScanCapReached,
		errcat.WithParams(errcat.SlackScanCapReachedParams{
			Name:    typedText,
			Kind:    "members",
			Scanned: directoryPageSize * maxDirectoryPages,
		}),
	))
}

func matchingUsers(users []User, handle string) []User {
	matches := make([]User, 0, 1)
	for _, user := range users {
		if user.Deleted || user.IsBot {
			continue
		}
		if strings.EqualFold(user.Name, handle) ||
			strings.EqualFold(user.DisplayName, handle) {
			matches = append(matches, user)
		}
	}
	return matches
}

func resolvedUser(typedText string, user User) (ports.SlackRecipient, error) {
	if user.Deleted || user.IsBot || strings.TrimSpace(user.ID) == "" {
		return ports.SlackRecipient{}, canonicalError(errcat.New(errcat.SlackUserNotFound))
	}
	displayName := firstNonempty(user.RealName, user.DisplayName, user.Name)
	if displayName == "" {
		displayName = user.ID
	}
	return ports.SlackRecipient{
		TypedText:   typedText,
		Kind:        ports.SlackRecipientUser,
		ID:          user.ID,
		DisplayName: displayName,
	}, nil
}

func resolveChannelName(
	ctx context.Context,
	client slackClient,
	token string,
	typedText string,
) (ports.SlackRecipient, error) {
	name := strings.TrimPrefix(typedText, "#")
	cursor := ""
	for pageNumber := 0; pageNumber < maxDirectoryPages; pageNumber++ {
		page, err := client.ConversationsList(ctx, cursor, directoryPageSize)
		if err != nil {
			return ports.SlackRecipient{}, classifyResolveError(token, err, errcat.SlackChannelNotFound)
		}
		for _, channel := range page.Conversations {
			if strings.EqualFold(channel.Name, name) {
				return resolvedChannel(typedText, channel)
			}
		}
		if page.NextCursor == "" {
			return ports.SlackRecipient{}, canonicalError(errcat.New(errcat.SlackChannelNotFound))
		}
		cursor = page.NextCursor
	}
	return ports.SlackRecipient{}, canonicalError(errcat.New(
		errcat.SlackScanCapReached,
		errcat.WithParams(errcat.SlackScanCapReachedParams{
			Name:    typedText,
			Kind:    "channels",
			Scanned: directoryPageSize * maxDirectoryPages,
		}),
	))
}

func resolveChannelID(
	ctx context.Context,
	client slackClient,
	token string,
	typedText string,
) (ports.SlackRecipient, error) {
	channel, err := client.ConversationInfo(ctx, typedText)
	if err != nil {
		return ports.SlackRecipient{}, classifyResolveError(token, err, errcat.SlackChannelNotFound)
	}
	return resolvedChannel(typedText, channel)
}

func resolvedChannel(typedText string, channel Conversation) (ports.SlackRecipient, error) {
	displayName := "#" + strings.TrimPrefix(channel.Name, "#")
	if channel.IsArchived {
		return ports.SlackRecipient{}, canonicalError(errcat.New(
			errcat.SlackChannelArchived,
			errcat.WithParams(errcat.SlackRecipientParams{Recipient: displayName}),
		))
	}
	if !channel.IsMember {
		return ports.SlackRecipient{}, canonicalError(errcat.New(
			errcat.SlackNotInChannel,
			errcat.WithParams(errcat.SlackRecipientParams{Recipient: displayName}),
			errcat.WithRemediationHint(fmt.Sprintf(
				"Invite the Agentico app to %s in Slack, then try again.",
				displayName,
			)),
		))
	}
	return ports.SlackRecipient{
		TypedText:   typedText,
		Kind:        ports.SlackRecipientChannel,
		ID:          channel.ID,
		DisplayName: displayName,
	}, nil
}

func classifyResolveError(token string, err error, notFoundCode errcat.Code) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		code := slackErrorCode(apiErr.SlackError)
		switch {
		case credentialSlackError(code):
			return invalidTokenError(token, apiErr)
		case code == "missing_scope":
			return canonicalError(missingScopeError(apiErr))
		case notFoundCode == errcat.SlackUserNotFound &&
			(code == "user_not_found" || code == "users_not_found"):
			return canonicalError(errcat.New(errcat.SlackUserNotFound))
		case notFoundCode == errcat.SlackChannelNotFound && code == "channel_not_found":
			return canonicalError(errcat.New(errcat.SlackChannelNotFound))
		default:
			return canonicalError(errcat.New(
				errcat.SlackUnreachable,
				errcat.WithDiagnostics("Slack returned "+scrub(token, apiErr.SlackError)),
			))
		}
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return canonicalError(errcat.New(
			errcat.SlackUnreachable,
			errcat.WithDiagnostics(scrub(token, transportErr.Error())),
		))
	}
	return canonicalError(errcat.New(
		errcat.SlackUnreachable,
		errcat.WithDiagnostics(scrub(token, err.Error())),
	))
}

func invalidTokenError(token string, apiErr *APIError) error {
	return canonicalError(errcat.New(
		errcat.SlackInvalidToken,
		errcat.WithDiagnostics("Slack returned "+scrub(token, apiErr.SlackError)),
	))
}

func missingScopeError(apiErr *APIError) errcat.Error {
	scopes := strings.FieldsFunc(apiErr.Needed, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	return errcat.New(
		errcat.SlackMissingScopes,
		errcat.WithParams(errcat.SlackMissingScopesParams{Scopes: scopes}),
	)
}

func credentialSlackError(code string) bool {
	switch code {
	case "invalid_auth", "token_revoked", "account_inactive":
		return true
	default:
		return false
	}
}

func slackErrorCode(value string) string {
	code, _, _ := strings.Cut(strings.TrimSpace(value), ":")
	return strings.TrimSpace(code)
}

func unrecognizedRecipientError() error {
	return canonicalError(errcat.New(errcat.SlackUnrecognizedRecipient))
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
