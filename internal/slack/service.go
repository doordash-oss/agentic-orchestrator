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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type slackClient interface {
	AuthTest(ctx context.Context) (AuthTestResponse, error)
	LookupUserByEmail(ctx context.Context, email string) (User, error)
	UserInfo(ctx context.Context, userID string) (User, error)
	UsersList(ctx context.Context, cursor string, limit int) (UsersPage, error)
	ConversationsList(ctx context.Context, cursor string, limit int) (ConversationsPage, error)
	ConversationInfo(ctx context.Context, channelID string) (Conversation, error)
	OpenConversation(ctx context.Context, userID string) (string, error)
	PostMessage(ctx context.Context, channelID, text string) error
}

// ClientFactory constructs the token-bound client used for one operation.
type ClientFactory func(token string) (slackClient, error)

// ServiceOption customizes the Slack service.
type ServiceOption func(*Service)

// WithClientFactory overrides client construction for tests or composition.
func WithClientFactory(factory ClientFactory) ServiceOption {
	return func(service *Service) {
		service.newClient = factory
	}
}

// WithPublishHook installs the status-invalidation hook.
func WithPublishHook(publish func()) ServiceOption {
	return func(service *Service) {
		service.publish = publish
	}
}

// Service owns Slack token validation and its transient status snapshot.
type Service struct {
	newClient ClientFactory
	publish   func()

	mu          sync.RWMutex
	lastError   *errcat.Error
	lastChecked *time.Time
}

var _ ports.SlackService = (*Service)(nil)

// NewService constructs a service without validating or making a network call.
func NewService(opts ...ServiceOption) *Service {
	service := &Service{
		newClient: func(token string) (slackClient, error) {
			return NewClient(token)
		},
		publish: func() {},
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Manifest returns the embedded app manifest.
func (s *Service) Manifest() string {
	return Manifest()
}

// RequiredScopes returns the manifest-derived required scope set.
func (s *Service) RequiredScopes() []string {
	return RequiredScopes()
}

// Validate checks token type, identity, and the complete required scope set.
func (s *Service) Validate(ctx context.Context, token string) (ports.SlackValidation, error) {
	tokenType := TokenTypeOf(token)
	if tokenType == ports.SlackTokenUnsupported {
		return ports.SlackValidation{}, canonicalError(errcat.New(errcat.SlackUnsupportedToken))
	}

	client, err := s.newClient(token)
	if err != nil {
		return ports.SlackValidation{}, canonicalError(errcat.New(
			errcat.SlackUnreachable,
			errcat.WithDiagnostics(scrub(token, err.Error())),
		))
	}
	auth, err := client.AuthTest(ctx)
	if err != nil {
		return ports.SlackValidation{}, classifyValidationError(token, err)
	}

	granted := normalizeScopes(auth.GrantedScopes)
	missing := missingScopes(RequiredScopes(), granted)
	if len(missing) > 0 {
		return ports.SlackValidation{}, canonicalError(errcat.New(
			errcat.SlackMissingScopes,
			errcat.WithParams(errcat.SlackMissingScopesParams{Scopes: missing}),
		))
	}
	return ports.SlackValidation{
		TokenType: tokenType,
		Identity: ports.SlackIdentity{
			TeamID:       auth.TeamID,
			TeamName:     auth.TeamName,
			UserID:       auth.UserID,
			UserName:     auth.UserName,
			DisplayName:  auth.UserName,
			WorkspaceURL: auth.WorkspaceURL,
			BotID:        auth.BotID,
		},
		GrantedScopes: granted,
		MissingScopes: []string{},
	}, nil
}

// Status derives state from durable facts and adds transient validation detail.
func (s *Service) Status(input ports.SlackStatusInput) ports.SlackStatusSnapshot {
	if input.Token == "" {
		return ports.SlackStatusSnapshot{State: ports.SlackNotConfigured}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := ports.SlackWarning
	if input.HasIdentity && s.lastError == nil {
		state = ports.SlackConnected
	}
	return ports.SlackStatusSnapshot{
		State:       state,
		LastError:   cloneCanonicalError(s.lastError),
		LastChecked: cloneTime(s.lastChecked),
	}
}

// RecordValidationSuccess clears the transient error and records the check time.
func (s *Service) RecordValidationSuccess(checkedAt time.Time) {
	s.setSnapshot(checkedAt, nil)
}

// RecordValidationFailure stores a scrubbed canonical failure and check time.
func (s *Service) RecordValidationFailure(checkedAt time.Time, canonical errcat.Error) {
	s.setSnapshot(checkedAt, &canonical)
}

// ClearStatus drops all transient validation state.
func (s *Service) ClearStatus() {
	s.mu.Lock()
	changed := s.lastError != nil || s.lastChecked != nil
	s.lastError = nil
	s.lastChecked = nil
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func (s *Service) setSnapshot(checkedAt time.Time, canonical *errcat.Error) {
	checkedAt = checkedAt.UTC()
	s.mu.Lock()
	changed := !sameCanonicalError(s.lastError, canonical) ||
		s.lastChecked == nil || !s.lastChecked.Equal(checkedAt)
	s.lastError = cloneCanonicalError(canonical)
	s.lastChecked = &checkedAt
	s.mu.Unlock()
	if changed {
		s.publish()
	}
}

func classifyValidationError(token string, err error) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if slackErrorCode(apiErr.SlackError) == "missing_scope" {
			return missingScopeError(apiErr)
		}
		return canonicalError(errcat.New(
			errcat.SlackInvalidToken,
			errcat.WithDiagnostics("Slack auth.test returned "+scrub(token, apiErr.SlackError)),
		))
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

func canonicalError(canonical errcat.Error) error {
	return &ports.SlackValidationError{Canonical: canonical}
}

func normalizeScopes(scopes []string) []string {
	set := make(map[string]struct{})
	for _, scope := range scopes {
		if scope = strings.TrimSpace(scope); scope != "" {
			set[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result
}

func missingScopes(required, granted []string) []string {
	grantedSet := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		grantedSet[scope] = struct{}{}
	}
	missing := make([]string, 0)
	for _, scope := range required {
		if _, ok := grantedSet[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	sort.Strings(missing)
	return missing
}

func cloneCanonicalError(canonical *errcat.Error) *errcat.Error {
	if canonical == nil {
		return nil
	}
	cloned := *canonical
	return &cloned
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameCanonicalError(left, right *errcat.Error) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Code == right.Code &&
		left.Class == right.Class &&
		left.Title == right.Title &&
		left.Summary == right.Summary &&
		left.Diagnostics == right.Diagnostics
}
