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

package permission

import (
	"errors"
	"fmt"
)

const (
	DecisionAllow         = "allow"
	DecisionAllowOnce     = "allow_once"
	DecisionAllowRemember = "allow_remember"
	DecisionDeny          = "deny"
)

const (
	rememberAuditResultSuccess       = "success"
	rememberAuditResultPersistFailed = "persist_failed"
	rememberAuditResultAnswerFailed  = "answer_failed"
)

var (
	errMissingAnswerCallback  = errors.New("permission answer callback is required")
	errMissingPermissionCache = errors.New("persistent permission cache is required for allow_remember")
	errMissingRememberPattern = errors.New("remember_pattern is required for allow_remember")
	errMissingRememberScope   = errors.New("remember_scope is required for allow_remember")
)

type AnswerFunc func(requestID string, allow bool, reason string) error

type AnswerRequest struct {
	RequestID        string
	SessionID        string
	FeatureID        string
	ToolName         string
	ToolInput        string
	Decision         string
	RememberPattern  string
	RememberScope    string
	RememberScopeSet bool
}

type AnswerResult struct {
	Decision       string
	Pattern        string
	Scope          string
	Persisted      bool
	AlreadyExisted bool
	Answered       bool
	AuditPath      string
	AuditWarning   string
}

type AnswerService struct {
	cache *Cache
	audit *AuditSink
}

func NewAnswerService(cache *Cache, audit *AuditSink) *AnswerService {
	return &AnswerService{cache: cache, audit: audit}
}

func (s *AnswerService) Answer(req AnswerRequest, answer AnswerFunc) (AnswerResult, error) {
	result := AnswerResult{Decision: req.Decision, Pattern: req.RememberPattern, Scope: req.RememberScope}
	if answer == nil {
		return result, errMissingAnswerCallback
	}

	switch req.Decision {
	case DecisionAllowOnce:
		if err := answer(req.RequestID, true, ""); err != nil {
			return result, fmt.Errorf("answer permission: %w", err)
		}
		result.Answered = true
		return result, nil
	case DecisionDeny:
		if err := answer(req.RequestID, false, "denied by user"); err != nil {
			return result, fmt.Errorf("answer permission: %w", err)
		}
		result.Answered = true
		return result, nil
	case DecisionAllowRemember:
		return s.answerRemember(req, answer, result)
	default:
		return result, fmt.Errorf("unknown permission decision %q", req.Decision)
	}
}

func (s *AnswerService) answerRemember(req AnswerRequest, answer AnswerFunc, result AnswerResult) (AnswerResult, error) {
	if s == nil || s.cache == nil || s.cache.StoreRef() == nil {
		return result, errMissingPermissionCache
	}
	if req.RememberPattern == "" {
		return result, errMissingRememberPattern
	}
	if !req.RememberScopeSet {
		return result, errMissingRememberScope
	}

	remember, err := s.cache.RememberAllowPattern(req.RememberPattern, req.RememberScope)
	result.Pattern = remember.Pattern
	result.Scope = remember.Scope
	result.Persisted = remember.Persisted
	result.AlreadyExisted = remember.AlreadyExisted
	if err != nil {
		result = s.appendRememberAudit(req, result, rememberAuditResultPersistFailed, err)
		return result, fmt.Errorf("remember permission rule: %w", err)
	}

	if err := answer(req.RequestID, true, ""); err != nil {
		result = s.appendRememberAudit(req, result, rememberAuditResultAnswerFailed, err)
		return result, fmt.Errorf("answer permission: %w", err)
	}
	result.Answered = true

	if !remember.AlreadyExisted {
		result = s.appendRememberAudit(req, result, rememberAuditResultSuccess, nil)
	}
	return result, nil
}

func (s *AnswerService) appendRememberAudit(req AnswerRequest, result AnswerResult, auditResult string, cause error) AnswerResult {
	if s == nil || s.audit == nil {
		return result
	}
	event := RememberAuditEvent{
		SessionID:    req.SessionID,
		RequestID:    req.RequestID,
		FeatureID:    req.FeatureID,
		ToolName:     req.ToolName,
		Decision:     req.Decision,
		Pattern:      req.RememberPattern,
		Scope:        req.RememberScope,
		InputSummary: req.ToolInput,
		Result:       auditResult,
		Persisted:    result.Persisted,
		Answered:     result.Answered,
	}
	if cause != nil {
		event.Error = cause.Error()
	}
	audit, err := s.audit.Append(event)
	if err != nil {
		result.AuditWarning = err.Error()
		return result
	}
	result.AuditPath = audit.Path
	return result
}
