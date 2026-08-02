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

package mocks

import (
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// MockStartSessionCall records arguments passed to StartSession.
type MockStartSessionCall struct {
	ID, FeatureID string
	Phase         feature.Phase
	Command       []string
}

// MockSessionManager implements ports.SessionManager with configurable
// function overrides and call tracking.
type MockSessionManager struct {
	// Function overrides
	StartSessionFn func(id, featureID string, phase feature.Phase,
		command []string, workdir string, env []string,
		opts ...*session.SessionOpts) (ports.SessionHandle, error)
	StopSessionFn     func(id string) error
	GetSessionFn      func(id string) ports.SessionView
	FeatureSessionsFn func(featureID string) []ports.SessionView
	ActiveSessionsFn  func() []ports.SessionView
	RecentSessionsFn  func(limit int) []ports.SessionView

	// Default return value for methods without an override
	DefaultError error

	// Call tracking
	StartSessionCalls    []MockStartSessionCall
	StopCalls            []string
	FeatureSessionsCalls []string
	RecentSessionsCalls  []int

	// State
	ShuttingDownVal bool
}

// NewMockSessionManager creates a MockSessionManager with zero-value defaults.
func NewMockSessionManager() *MockSessionManager {
	return &MockSessionManager{}
}

// --- SessionManager interface ---

func (m *MockSessionManager) StartSession(id, featureID string, phase feature.Phase,
	command []string, workdir string, env []string,
	opts ...*session.SessionOpts) (ports.SessionHandle, error) {

	m.StartSessionCalls = append(m.StartSessionCalls, MockStartSessionCall{
		ID:        id,
		FeatureID: featureID,
		Phase:     phase,
		Command:   command,
	})
	if m.StartSessionFn != nil {
		return m.StartSessionFn(id, featureID, phase, command, workdir, env, opts...)
	}
	return nil, m.DefaultError
}

func (m *MockSessionManager) StopSession(id string) error {
	m.StopCalls = append(m.StopCalls, id)
	if m.StopSessionFn != nil {
		return m.StopSessionFn(id)
	}
	return m.DefaultError
}

func (m *MockSessionManager) GetSession(id string) ports.SessionView {
	if m.GetSessionFn != nil {
		return m.GetSessionFn(id)
	}
	return nil
}

func (m *MockSessionManager) ActiveSessions() []ports.SessionView {
	if m.ActiveSessionsFn != nil {
		return m.ActiveSessionsFn()
	}
	return nil
}

func (m *MockSessionManager) RecentSessions(limit int) []ports.SessionView {
	m.RecentSessionsCalls = append(m.RecentSessionsCalls, limit)
	if m.RecentSessionsFn != nil {
		return m.RecentSessionsFn(limit)
	}
	return nil
}

func (m *MockSessionManager) FeatureSessions(featureID string) []ports.SessionView {
	m.FeatureSessionsCalls = append(m.FeatureSessionsCalls, featureID)
	if m.FeatureSessionsFn != nil {
		return m.FeatureSessionsFn(featureID)
	}
	return nil
}

func (m *MockSessionManager) SendInput(sessionID string, data []byte) error {
	return m.DefaultError
}

func (m *MockSessionManager) Attach(sessionID string) (ports.SessionView, error) {
	return nil, m.DefaultError
}

func (m *MockSessionManager) Detach() {}

func (m *MockSessionManager) Shutdown() {}

func (m *MockSessionManager) IsShuttingDown() bool {
	return m.ShuttingDownVal
}
