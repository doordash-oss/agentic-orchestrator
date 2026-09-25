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
	"net/http"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type deliveryFailureClass string

const (
	deliveryFailureCredential  deliveryFailureClass = "credential"
	deliveryFailureDestination deliveryFailureClass = "destination"
)

type writeFailure struct {
	class      deliveryFailureClass
	errorCode  errcat.Code
	slackError string
	canonical  *errcat.Error
}

func classifyWriteFailure(token string, err error, retryExhausted bool) writeFailure {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		slackError := scrub(token, apiErr.SlackError)
		code := slackErrorCode(slackError)
		if code == "missing_scope" {
			canonical := errcat.New(
				errcat.SlackScopesRevoked,
				errcat.WithParams(errcat.SlackScopeFailureParams{
					NeededScope: scrub(token, apiErr.Needed),
				}),
			)
			return writeFailure{
				class:      deliveryFailureCredential,
				errorCode:  errcat.SlackScopesRevoked,
				slackError: slackError,
				canonical:  &canonical,
			}
		}
		if workerCredentialSlackError(code) {
			canonical := errcat.New(
				errcat.SlackTokenRejected,
				errcat.WithParams(errcat.SlackCredentialFailureParams{
					SlackError: slackError,
				}),
			)
			return writeFailure{
				class:      deliveryFailureCredential,
				errorCode:  errcat.SlackTokenRejected,
				slackError: slackError,
				canonical:  &canonical,
			}
		}
		return writeFailure{
			class:      deliveryFailureDestination,
			errorCode:  errcat.SlackRecipientNotNotified,
			slackError: slackError,
		}
	}

	var transportErr *TransportError
	if retryExhausted && errors.As(err, &transportErr) {
		cause := "retries_exhausted"
		if transportErr.StatusCode == http.StatusTooManyRequests {
			cause = "rate_limited"
		}
		return writeFailure{
			class:      deliveryFailureDestination,
			errorCode:  errcat.SlackDeliveryRetriesExhausted,
			slackError: cause,
		}
	}
	return writeFailure{
		class:      deliveryFailureDestination,
		errorCode:  errcat.SlackRecipientNotNotified,
		slackError: scrub(token, err.Error()),
	}
}

func workerCredentialSlackError(code string) bool {
	switch code {
	case "invalid_auth", "token_revoked", "account_inactive", "not_authed", "token_expired":
		return true
	default:
		return false
	}
}

// SendTestMessage attempts every recipient sequentially unless credentials fail.
func (s *Service) SendTestMessage(
	ctx context.Context,
	token string,
	serverName string,
	recipients []ports.SlackRecipient,
) ([]ports.SlackDeliveryResult, error) {
	client, err := s.newClient(token)
	if err != nil {
		return nil, canonicalError(errcat.New(
			errcat.SlackUnreachable,
			errcat.WithDiagnostics(scrub(token, err.Error())),
		))
	}
	message := fmt.Sprintf(
		"Slack notifications for %s are working.",
		strings.TrimSpace(serverName),
	)
	results := make([]ports.SlackDeliveryResult, 0, len(recipients))
	for _, recipient := range recipients {
		result := ports.SlackDeliveryResult{Recipient: recipient}
		destination := recipient.ID
		if recipient.Kind == ports.SlackRecipientUser {
			destination, err = client.OpenConversation(ctx, recipient.ID)
			if err != nil {
				canonical, fatal := classifyDeliveryError(token, recipient, err)
				if fatal {
					return nil, canonicalError(canonical)
				}
				result.Error = &canonical
				results = append(results, result)
				continue
			}
		} else if recipient.Kind != ports.SlackRecipientChannel {
			canonical := errcat.New(
				errcat.SlackDeliveryFailed,
				errcat.WithDiagnostics("unsupported Slack recipient kind"),
			)
			result.Error = &canonical
			results = append(results, result)
			continue
		}

		err = client.PostMessage(ctx, destination, message)
		if err != nil {
			canonical, fatal := classifyDeliveryError(token, recipient, err)
			if fatal {
				return nil, canonicalError(canonical)
			}
			result.Error = &canonical
			results = append(results, result)
			continue
		}
		result.Delivered = true
		results = append(results, result)
	}
	return results, nil
}

func classifyDeliveryError(
	token string,
	recipient ports.SlackRecipient,
	err error,
) (errcat.Error, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		code := slackErrorCode(apiErr.SlackError)
		switch {
		case credentialSlackError(code):
			return errcat.New(
				errcat.SlackInvalidToken,
				errcat.WithDiagnostics("Slack returned "+scrub(token, apiErr.SlackError)),
			), true
		case code == "missing_scope":
			return missingScopeError(apiErr), true
		case code == "channel_not_found":
			return recipientError(errcat.SlackChannelNotFound, recipient), false
		case code == "not_in_channel":
			return notInChannelError(recipient), false
		case code == "is_archived":
			return recipientError(errcat.SlackChannelArchived, recipient), false
		case code == "user_not_found":
			return recipientError(errcat.SlackUserNotFound, recipient), false
		default:
			return errcat.New(
				errcat.SlackDeliveryFailed,
				errcat.WithDiagnostics("Slack returned "+scrub(token, apiErr.SlackError)),
			), false
		}
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return errcat.New(
			errcat.SlackUnreachable,
			errcat.WithDiagnostics(scrub(token, transportErr.Error())),
		), false
	}
	return errcat.New(
		errcat.SlackDeliveryFailed,
		errcat.WithDiagnostics(scrub(token, err.Error())),
	), false
}

func recipientError(code errcat.Code, recipient ports.SlackRecipient) errcat.Error {
	return errcat.New(
		code,
		errcat.WithParams(errcat.SlackRecipientParams{
			Recipient: recipient.DisplayName,
		}),
	)
}

func notInChannelError(recipient ports.SlackRecipient) errcat.Error {
	name := firstNonempty(recipient.DisplayName, recipient.TypedText, recipient.ID)
	return errcat.New(
		errcat.SlackNotInChannel,
		errcat.WithParams(errcat.SlackRecipientParams{Recipient: name}),
		errcat.WithRemediationHint(fmt.Sprintf(
			"Invite the Agentico app to %s in Slack, then try again.",
			name,
		)),
	)
}
