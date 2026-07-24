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
	"encoding/json"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
)

// MockSessionView implements session.SessionView with configurable fields.
// All identity and state fields are set via exported struct fields.
// Interaction methods record their calls for assertion.
//
// Type boundaries follow the post-Phase-5 layout:
//   - llm.*: SDKMessage, ResultMessage, Usage, ControlRequestMessage
//   - session.*: SessionStatus, MessageLog, QAPair
type MockSessionView struct {
	// Identity
	IDVal             string
	FeatureIDVal      string
	PhaseVal          feature.Phase
	RepoNameVal       string
	PermCacheScopeVal string
	KindVal           ports.SessionKind
	LabelVal          string

	// State
	StatusVal          session.SessionStatus
	IsActiveVal        bool
	IterationVal       int
	StartedAtVal       time.Time
	InitialPromptVal   string
	ProviderNameVal    string
	ModelVal           string
	WorkDirVal         string
	EffectiveEffortVal llm.EffortLevel
	EffortSourceVal    llm.EffortSource

	// Data — types from internal/llm (moved in Phase 5)
	CostVal                   *llm.ResultMessage
	LatestUsageVal            *llm.Usage
	AccumulatedUsageVal       llm.Usage
	LastControlRequestVal     *llm.ControlRequestMessage
	PendingControlRequestsVal []*llm.ControlRequestMessage

	// Data — types that remain in internal/session
	MessageLogVal        *session.MessageLog
	QALogVal             []session.QAPair
	LogFilePathVal       string
	ContextPercentageVal int
	ErrorDetailVal       string
	ExitCodeDetailVal    string
	LastStdoutAtVal      time.Time

	// Query
	HasPendingAskUserQuestionVal bool

	// Channels
	StatusChVal chan string
	AttachChVal chan llm.SDKMessage
	DoneChVal   chan struct{}

	// Interaction call recording
	SentMessages       []string
	ControlResponses   []ControlResponseCall
	AskUserResponses   []AskUserResponseCall
	ClearedQuestions   []string
	ResetWaitingCalled int
	StopCalled         int
	InterruptCalled    int
	WaitCalled         int
	StopError          error
	InterruptError     error
}

// ControlResponseCall records a RespondToControl call.
type ControlResponseCall struct {
	RequestID string
	Allow     bool
	Reason    string
}

// AskUserResponseCall records a RespondToAskUser call.
type AskUserResponseCall struct {
	RequestID   string
	Questions   json.RawMessage
	Answers     map[string]string
	Annotations map[string]llm.AskUserAnnotation
}

// NewMockSessionView creates a MockSessionView with sensible defaults
// (running status, initialized channels, empty message log).
func NewMockSessionView(id, featureID string) *MockSessionView {
	return &MockSessionView{
		IDVal:         id,
		FeatureIDVal:  featureID,
		StatusVal:     session.SessionRunning,
		IsActiveVal:   true,
		StartedAtVal:  time.Now(),
		MessageLogVal: session.NewMessageLog(),
		StatusChVal:   make(chan string, 10),
		AttachChVal:   make(chan llm.SDKMessage, 10),
		DoneChVal:     make(chan struct{}),
	}
}

// --- Identity ---

func (m *MockSessionView) ID() string              { return m.IDVal }
func (m *MockSessionView) FeatureID() string       { return m.FeatureIDVal }
func (m *MockSessionView) Phase() feature.Phase    { return m.PhaseVal }
func (m *MockSessionView) RepoName() string        { return m.RepoNameVal }
func (m *MockSessionView) PermCacheScope() string  { return m.PermCacheScopeVal }
func (m *MockSessionView) Kind() ports.SessionKind { return m.KindVal }
func (m *MockSessionView) Label() string           { return m.LabelVal }

// --- State ---

func (m *MockSessionView) Status() session.SessionStatus    { return m.StatusVal }
func (m *MockSessionView) IsActive() bool                   { return m.IsActiveVal }
func (m *MockSessionView) Iteration() int                   { return m.IterationVal }
func (m *MockSessionView) StartedAt() time.Time             { return m.StartedAtVal }
func (m *MockSessionView) InitialPrompt() string            { return m.InitialPromptVal }
func (m *MockSessionView) ProviderName() string             { return m.ProviderNameVal }
func (m *MockSessionView) Model() string                    { return m.ModelVal }
func (m *MockSessionView) WorkDir() string                  { return m.WorkDirVal }
func (m *MockSessionView) EffectiveEffort() llm.EffortLevel { return m.EffectiveEffortVal }
func (m *MockSessionView) EffortSource() llm.EffortSource   { return m.EffortSourceVal }

// --- Data access ---

func (m *MockSessionView) MessageLog() ports.MessageLog { return m.MessageLogVal }
func (m *MockSessionView) Cost() *llm.ResultMessage     { return m.CostVal }
func (m *MockSessionView) LatestUsage() *llm.Usage      { return m.LatestUsageVal }
func (m *MockSessionView) AccumulatedUsage() llm.Usage  { return m.AccumulatedUsageVal }
func (m *MockSessionView) LastControlRequest() *llm.ControlRequestMessage {
	if m.LastControlRequestVal != nil {
		return m.LastControlRequestVal
	}
	if len(m.PendingControlRequestsVal) == 0 {
		return nil
	}
	return m.PendingControlRequestsVal[len(m.PendingControlRequestsVal)-1]
}

func (m *MockSessionView) PendingControlRequests() []*llm.ControlRequestMessage {
	if len(m.PendingControlRequestsVal) > 0 {
		out := make([]*llm.ControlRequestMessage, len(m.PendingControlRequestsVal))
		copy(out, m.PendingControlRequestsVal)
		return out
	}
	if m.LastControlRequestVal != nil {
		return []*llm.ControlRequestMessage{m.LastControlRequestVal}
	}
	return nil
}
func (m *MockSessionView) QALog() []session.QAPair { return m.QALogVal }
func (m *MockSessionView) LogFilePath() string     { return m.LogFilePathVal }
func (m *MockSessionView) ContextPercentage() int  { return m.ContextPercentageVal }
func (m *MockSessionView) ErrorDetail() string     { return m.ErrorDetailVal }
func (m *MockSessionView) ExitCodeDetail() string  { return m.ExitCodeDetailVal }
func (m *MockSessionView) LastStdoutAt() time.Time { return m.LastStdoutAtVal }

// --- Channels ---

func (m *MockSessionView) StatusCh() <-chan string         { return m.StatusChVal }
func (m *MockSessionView) AttachCh() <-chan llm.SDKMessage { return m.AttachChVal }
func (m *MockSessionView) Done() <-chan struct{}           { return m.DoneChVal }

// --- Query ---

func (m *MockSessionView) HasPendingAskUserQuestion() bool { return m.HasPendingAskUserQuestionVal }

// --- Interaction ---

func (m *MockSessionView) SendUserMessage(text string) error {
	m.SentMessages = append(m.SentMessages, text)
	return nil
}

func (m *MockSessionView) RespondToControl(requestID string, allow bool, reason string) error {
	m.ControlResponses = append(m.ControlResponses, ControlResponseCall{
		RequestID: requestID,
		Allow:     allow,
		Reason:    reason,
	})
	return nil
}

func (m *MockSessionView) RespondToAskUser(requestID string, questions json.RawMessage, answers map[string]string, annotations map[string]llm.AskUserAnnotation) error {
	m.AskUserResponses = append(m.AskUserResponses, AskUserResponseCall{
		RequestID:   requestID,
		Questions:   questions,
		Answers:     answers,
		Annotations: annotations,
	})
	return nil
}

func (m *MockSessionView) ClearPendingQuestion(requestID string) {
	m.ClearedQuestions = append(m.ClearedQuestions, requestID)
}

func (m *MockSessionView) ResetWaitingStatus() {
	m.ResetWaitingCalled++
}

func (m *MockSessionView) Stop() error {
	m.StopCalled++
	return m.StopError
}

func (m *MockSessionView) Interrupt() error {
	m.InterruptCalled++
	return m.InterruptError
}

func (m *MockSessionView) Wait() {
	m.WaitCalled++
}
