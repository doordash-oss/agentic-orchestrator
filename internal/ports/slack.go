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
	"context"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// SlackTokenType identifies the supported Slack OAuth token families.
type SlackTokenType string

const (
	SlackTokenBot         SlackTokenType = "bot"
	SlackTokenUser        SlackTokenType = "user"
	SlackTokenUnsupported SlackTokenType = "unsupported"
)

// SlackIdentity is the workspace identity returned by Slack auth.test.
type SlackIdentity struct {
	TeamID       string `json:"team_id"`
	TeamName     string `json:"team_name"`
	UserID       string `json:"user_id"`
	DisplayName  string `json:"display_name"`
	WorkspaceURL string `json:"workspace_url,omitempty"`
	BotID        string `json:"bot_id,omitempty"`
}

// SlackValidation is the credential metadata established by auth.test.
type SlackValidation struct {
	TokenType     SlackTokenType `json:"token_type"`
	Identity      SlackIdentity  `json:"identity"`
	GrantedScopes []string       `json:"granted_scopes"`
	MissingScopes []string       `json:"missing_scopes"`
}

// SlackStatusState is the derived connection state exposed by the service.
type SlackStatusState string

const (
	SlackNotConfigured SlackStatusState = "not_configured"
	SlackConnected     SlackStatusState = "connected"
	SlackWarning       SlackStatusState = "warning"
)

// SlackStatusInput contains only durable facts needed to derive status.
type SlackStatusInput struct {
	Token       string
	HasIdentity bool
}

// SlackStatusSnapshot combines derived state with transient validation detail.
type SlackStatusSnapshot struct {
	State       SlackStatusState
	LastError   *errcat.Error
	LastChecked *time.Time
}

// SlackValidationError carries the canonical error returned by validation.
type SlackValidationError struct {
	Canonical errcat.Error
}

func (e *SlackValidationError) Error() string {
	return string(e.Canonical.Code) + ": " + e.Canonical.Summary
}

// SlackService is the server-facing Slack setup and validation boundary.
type SlackService interface {
	Manifest() string
	RequiredScopes() []string
	Validate(ctx context.Context, token string) (SlackValidation, error)
	Status(input SlackStatusInput) SlackStatusSnapshot
	RecordValidationSuccess(checkedAt time.Time)
	RecordValidationFailure(checkedAt time.Time, canonical errcat.Error)
	ClearStatus()
}
