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
	"errors"
	"net/http"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type slackValidationStore interface {
	StoreSlackValidation(validation *ports.SlackValidation, checkedAt time.Time) error
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
	stored := req.Token == nil
	if stored {
		cfg := h.configOrDefault()
		if cfg.Slack != nil {
			token = cfg.Slack.Token
		}
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
			h.slack.RecordValidationFailure(checkedAt, validationErr.Canonical)
			if h.broker != nil {
				h.broker.publish(snapshotRequiredEventDTO(sseEventConfigUpdated, Resource{Type: resourceTypeRuntime}))
			}
		}
		writeRenderedSlackError(w, validationErr.Canonical)
		return
	}

	if stored {
		store, ok := h.mutations.(slackValidationStore)
		if !ok {
			writeAPIError(w, http.StatusInternalServerError, errcat.InternalError)
			return
		}
		if err := store.StoreSlackValidation(&validation, checkedAt); err != nil {
			writeMutationError(w, err)
			return
		}
		h.slack.RecordValidationSuccess(checkedAt)
		if h.broker != nil {
			h.broker.publish(snapshotRequiredEventDTO(sseEventConfigUpdated, Resource{Type: resourceTypeRuntime}))
		}
	}
	writeJSON(w, http.StatusOK, SlackValidateResponse{
		APIVersion:    APIVersion,
		TokenType:     SlackValidateResponseTokenType(validation.TokenType),
		Identity:      slackIdentityDTO(validation.Identity),
		GrantedScopes: append([]string(nil), validation.GrantedScopes...),
		MissingScopes: append([]string(nil), validation.MissingScopes...),
	})
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
