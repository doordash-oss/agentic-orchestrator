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

package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type slackValidationStore interface {
	LoadSlackCredential() (token string, generation uint64)
	StoreSlackValidation(
		token string,
		generation uint64,
		validation *ports.SlackValidation,
		checkedAt time.Time,
	) (bool, error)
	SlackCredentialCurrent(token string, generation uint64) bool
}

type slackDeliveryStore interface {
	LoadSlackDeliveryConfig() (string, []ports.SlackRecipient)
}

func (h *apiHandler) handleSlackValidateRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req SlackValidateRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if h.slack == nil {
		writeAPIError(w, http.StatusBadGateway, errcat.SlackUnreachable)
		return
	}
	token := ""
	var generation uint64
	stored := req.Token == nil
	if stored {
		store, ok := h.mutations.(slackValidationStore)
		if !ok {
			writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
			return
		}
		token, generation = store.LoadSlackCredential()
		if token == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("no stored Slack token is configured"))
			return
		}
	} else {
		token = *req.Token
		if token == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("slack token must not be empty"))
			return
		}
	}

	checkedAt := time.Now().UTC()
	validation, err := h.slack.Validate(r.Context(), token)
	if err != nil {
		var validationErr *ports.SlackValidationError
		if !errors.As(err, &validationErr) {
			writeAPIError(w, http.StatusBadGateway, errcat.SlackUnreachable)
			return
		}
		if stored {
			store := h.mutations.(slackValidationStore)
			h.slackCredentialMu.Lock()
			if store.SlackCredentialCurrent(token, generation) {
				h.slack.RecordValidationFailure(checkedAt, validationErr.Canonical)
			}
			h.slackCredentialMu.Unlock()
		}
		writeRenderedSlackError(w, validationErr.Canonical)
		return
	}

	if stored {
		store := h.mutations.(slackValidationStore)
		h.slackCredentialMu.Lock()
		applied, err := store.StoreSlackValidation(token, generation, &validation, checkedAt)
		if err != nil {
			h.slackCredentialMu.Unlock()
			writeMutationError(w, err)
			return
		}
		if applied {
			h.slack.RecordValidationSuccess(checkedAt)
		}
		h.slackCredentialMu.Unlock()
	}
	writeJSON(w, http.StatusOK, SlackValidateResponse{
		APIVersion:         APIVersion,
		TokenType:          SlackValidateResponseTokenType(validation.TokenType),
		Identity:           slackIdentityDTO(validation.Identity),
		GrantedScopes:      append(make([]string, 0, len(validation.GrantedScopes)), validation.GrantedScopes...),
		MissingScopes:      append(make([]string, 0, len(validation.MissingScopes)), validation.MissingScopes...),
		SuggestedRecipient: suggestedSlackRecipient(validation),
	})
}

// ReportSlackDeliveryFailure records a credential-class delivery failure and
// refreshes cached identity once at the start of each failure episode.
func (h *apiHandler) ReportSlackDeliveryFailure(
	at time.Time,
	credentialGeneration uint64,
	canonical errcat.Error,
) {
	if h.slack == nil {
		return
	}
	h.slackCredentialMu.Lock()
	defer h.slackCredentialMu.Unlock()

	store, ok := h.mutations.(slackValidationStore)
	if !ok {
		return
	}
	token, currentGeneration := store.LoadSlackCredential()
	if token == "" || currentGeneration != credentialGeneration {
		return
	}
	status := h.slack.Status(ports.SlackStatusInput{Token: "configured"})
	if status.State != ports.SlackCredentialError {
		if validation, err := h.slack.Validate(context.Background(), token); err == nil {
			_, _ = store.StoreSlackValidation(
				token,
				credentialGeneration,
				&validation,
				at,
			)
		}
	}
	if !store.SlackCredentialCurrent(token, credentialGeneration) {
		return
	}
	h.slack.RecordDeliveryFailure(at, canonical)
}

// ReportSlackDeliverySuccess clears credential status after any successful
// Slack write.
func (h *apiHandler) ReportSlackDeliverySuccess(at time.Time, credentialGeneration uint64) {
	if h.slack == nil {
		return
	}
	h.slackCredentialMu.Lock()
	store, ok := h.mutations.(slackValidationStore)
	if !ok {
		h.slackCredentialMu.Unlock()
		return
	}
	token, currentGeneration := store.LoadSlackCredential()
	if token == "" || currentGeneration != credentialGeneration {
		h.slackCredentialMu.Unlock()
		return
	}
	h.slack.RecordDeliverySuccess(at)
	h.slackCredentialMu.Unlock()
}

func (h *apiHandler) handleSlackRecipientResolveRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req SlackRecipientResolveRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if h.slack == nil {
		writeAPIError(w, http.StatusBadGateway, errcat.SlackUnreachable)
		return
	}
	if req.Input == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("recipient text must not be empty"))
		return
	}
	token := ""
	if req.Token != nil {
		token = *req.Token
		if token == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("slack token must not be empty"))
			return
		}
	} else {
		store, ok := h.mutations.(slackValidationStore)
		if !ok {
			writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
			return
		}
		token, _ = store.LoadSlackCredential()
		if token == "" {
			writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
				errcat.WithDiagnostics("no stored Slack token is configured"))
			return
		}
	}
	recipient, err := h.slack.ResolveRecipient(r.Context(), token, req.Input)
	if err != nil {
		writeSlackOperationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SlackRecipientResolveResponse{
		APIVersion: APIVersion,
		Recipient:  slackRecipientDTO(recipient),
	})
}

func (h *apiHandler) handleSlackTestMessageRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, errcat.MethodNotAllowed)
		return
	}
	if !h.requireTrustedMutation(w, r) {
		return
	}
	var req SlackTestMessageRequest
	if !decodeMutationJSON(w, r, &req) {
		return
	}
	if req.Recipients != nil && !validateSlackRecipients(w, *req.Recipients) {
		return
	}
	if h.slack == nil {
		writeAPIError(w, http.StatusBadGateway, errcat.SlackUnreachable)
		return
	}
	store, ok := h.mutations.(slackDeliveryStore)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
		return
	}
	token, storedRecipients := store.LoadSlackDeliveryConfig()
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("no stored Slack token is configured"))
		return
	}
	recipients := storedRecipients
	if req.Recipients != nil {
		recipients = slackRecipientsFromDTO(*req.Recipients)
	}
	if len(recipients) == 0 {
		writeAPIError(w, http.StatusBadRequest, errcat.BadRequest,
			errcat.WithDiagnostics("at least one Slack recipient is required"))
		return
	}
	results, err := h.slack.SendTestMessage(r.Context(), token, h.name, recipients)
	if err != nil {
		writeSlackOperationError(w, err)
		return
	}
	wireResults := make([]SlackDeliveryResult, 0, len(results))
	for _, result := range results {
		wireResult := SlackDeliveryResult{
			Recipient: slackRecipientDTO(result.Recipient),
			Delivered: result.Delivered,
		}
		if result.Error != nil {
			rendered := wireError(*result.Error)
			wireResult.Error = &rendered
		}
		wireResults = append(wireResults, wireResult)
	}
	writeJSON(w, http.StatusOK, SlackTestMessageResponse{
		APIVersion: APIVersion,
		Results:    wireResults,
	})
}

func writeSlackOperationError(w http.ResponseWriter, err error) {
	var slackErr *ports.SlackValidationError
	if !errors.As(err, &slackErr) {
		writeAPIError(w, http.StatusBadGateway, errcat.SlackUnreachable)
		return
	}
	writeRenderedSlackError(w, slackErr.Canonical)
}

func suggestedSlackRecipient(validation ports.SlackValidation) *SlackRecipient {
	if validation.TokenType != ports.SlackTokenUser {
		return nil
	}
	recipient := slackRecipientDTO(ports.SlackRecipient{
		TypedText:   "@" + validation.Identity.UserName,
		Kind:        ports.SlackRecipientUser,
		ID:          validation.Identity.UserID,
		DisplayName: validation.Identity.DisplayName,
	})
	return &recipient
}

func slackRecipientDTO(recipient ports.SlackRecipient) SlackRecipient {
	return SlackRecipient{
		TypedText:   recipient.TypedText,
		Kind:        SlackRecipientKind(recipient.Kind),
		ID:          recipient.ID,
		DisplayName: recipient.DisplayName,
	}
}

func slackRecipientsFromDTO(recipients []SlackRecipient) []ports.SlackRecipient {
	result := make([]ports.SlackRecipient, 0, len(recipients))
	for _, recipient := range recipients {
		result = append(result, ports.SlackRecipient{
			TypedText:   recipient.TypedText,
			Kind:        ports.SlackRecipientKind(recipient.Kind),
			ID:          recipient.ID,
			DisplayName: recipient.DisplayName,
		})
	}
	return result
}

func slackIdentityDTO(identity ports.SlackIdentity) SlackIdentity {
	return SlackIdentity{
		TeamID:      identity.TeamID,
		TeamName:    identity.TeamName,
		UserID:      identity.UserID,
		DisplayName: identity.DisplayName,
		BotID:       identity.BotID,
	}
}
