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
	UserName     string `json:"user_name"`
	DisplayName  string `json:"display_name"`
	WorkspaceURL string `json:"workspace_url,omitempty"`
	BotID        string `json:"bot_id,omitempty"`
}

// SlackRecipientKind identifies a resolved Slack destination.
type SlackRecipientKind string

const (
	SlackRecipientUser    SlackRecipientKind = "user"
	SlackRecipientChannel SlackRecipientKind = "channel"
)

// SlackRecipient is a user-entered destination resolved to a Slack ID.
type SlackRecipient struct {
	TypedText   string             `json:"typed_text"`
	Kind        SlackRecipientKind `json:"kind"`
	ID          string             `json:"id"`
	DisplayName string             `json:"display_name"`
}

// SlackDeliveryResult reports one recipient's test-message outcome.
type SlackDeliveryResult struct {
	Recipient SlackRecipient `json:"recipient"`
	Delivered bool           `json:"delivered"`
	Error     *errcat.Error  `json:"error,omitempty"`
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
	SlackNotConfigured   SlackStatusState = "not_configured"
	SlackConnected       SlackStatusState = "connected"
	SlackWarning         SlackStatusState = "warning"
	SlackCredentialError SlackStatusState = "credential_error"
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

// SlackValidationError carries a canonical Slack operation error.
type SlackValidationError struct {
	Canonical errcat.Error
}

func (e *SlackValidationError) Error() string {
	return string(e.Canonical.Code) + ": " + e.Canonical.Summary
}

// SlackRuntimeSettings is the live Slack configuration the notifier reads
// at processing time. It is derived per read from the durable config so a
// stored token is never cached.
type SlackRuntimeSettings struct {
	Enabled    bool
	Token      string
	Recipients []SlackRecipient
	Categories SlackCategoryDefaults
}

// SlackCategoryDefaults holds the effective per-category notification
// defaults; every category posts until a stored mapping opts one out.
type SlackCategoryDefaults struct {
	Progress   bool
	NeedsInput bool
	Problems   bool
}

// SlackSettingsSource supplies the live Slack configuration under its own
// locking. Implementations must not cache the token across reads.
type SlackSettingsSource interface {
	SlackSettings() SlackRuntimeSettings
}

// SlackService is the server-facing Slack setup and validation boundary.
type SlackService interface {
	Manifest() string
	RequiredScopes() []string
	Validate(ctx context.Context, token string) (SlackValidation, error)
	ResolveRecipient(ctx context.Context, token, text string) (SlackRecipient, error)
	SendTestMessage(
		ctx context.Context,
		token string,
		serverName string,
		recipients []SlackRecipient,
	) ([]SlackDeliveryResult, error)
	Status(input SlackStatusInput) SlackStatusSnapshot
	SetPublishHook(func())
	RecordValidationSuccess(checkedAt time.Time) bool
	RecordValidationFailure(checkedAt time.Time, canonical errcat.Error) bool
	RecordDeliveryFailure(failedAt time.Time, canonical errcat.Error) bool
	RecordDeliverySuccess(succeededAt time.Time) bool
	ClearStatus() bool
}

// SlackDeliveryReporter receives notifier delivery outcomes without exposing
// configuration persistence to the notifier.
type SlackDeliveryReporter interface {
	ReportSlackDeliveryFailure(at time.Time, canonical errcat.Error)
	ReportSlackDeliverySuccess(at time.Time)
}

// SlackWarningSource supplies read-time Slack delivery warnings for a feature.
type SlackWarningSource interface {
	SlackWarnings(featureID string) []errcat.Error
}
