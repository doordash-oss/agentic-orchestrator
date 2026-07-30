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

package orchestrator

import (
	"fmt"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	sessionruntime "github.com/doordash-oss/agentic-orchestrator/internal/session"
)

const (
	phaseSupervisorStatusSuccess  = "SUCCESS"
	phaseSupervisorStatusFailed   = "FAILED"
	phaseSupervisorStatusAPIError = "API_ERROR"
)

// Shared FinalStatus values reported by agent implementation/rebase/review
// loops (agent.OrchestratorResult, agent.RebaseLoopResult, etc.).
const (
	reviewStatusPassed              = "review_passed"
	finalStatusAllPassed            = "all_passed"
	finalStatusPlanRevisionRequired = "plan_revision_required"
	finalStatusFailed               = "failed"
	finalStatusInterrupted          = "interrupted"
	finalStatusNeedUserInput        = "need_user_input"
	finalStatusNoOp                 = "no_op"
)

type phaseCompletionSink interface {
	HandlePhaseCompletion(featureID string, input PhaseCompletionInput) error
}

type phaseSupervisorConfig struct {
	Completion        phaseCompletionSink
	Sessions          ports.SessionManager
	SingleShotResumer singleShotResumeDriver
	AutoResumeWait    func(time.Duration) bool
	OnCompletionError func(featureID string, err error)
}

type singleShotResumeDriver interface {
	SingleShotPhaseComplete(sessionID string) bool
	SingleShotSupportsResume(sessionID string) bool
	SingleShotInterrupted(sessionID string) bool
	SingleShotNeedsEstablishment(sessionID string) bool
	CaptureSingleShotProviderSnapshot(sessionID string, sess ports.SessionView)
	DispatchSingleShotContinuation(previousSessionID, resumeID string, ordinal int, fresh bool) (*agent.SingleShotResumeResult, error)
	CompleteSingleShotResumeEstablishment(sessionID string, sess ports.SessionView, elapsed time.Duration) bool
	MarkSingleShotCompleted(sessionID string)
}

type phaseSupervisor struct {
	completion        phaseCompletionSink
	sessions          ports.SessionManager
	singleShotResumer singleShotResumeDriver
	autoResumeWait    func(time.Duration) bool
	onCompletionError func(featureID string, err error)

	singleShotMu       sync.Mutex
	singleShotSessions map[string]struct{}
}

func newPhaseSupervisor(cfg phaseSupervisorConfig) *phaseSupervisor {
	return &phaseSupervisor{
		completion:        cfg.Completion,
		sessions:          cfg.Sessions,
		singleShotResumer: cfg.SingleShotResumer,
		autoResumeWait:    cfg.AutoResumeWait,
		onCompletionError: cfg.OnCompletionError,
	}
}

func (s *phaseSupervisor) superviseSingleShotSession(featureID, sessionID string, phase feature.Phase) {
	if s == nil || s.sessions == nil || sessionID == "" {
		return
	}
	if !s.claimSingleShotSession(sessionID) {
		return
	}
	sess := s.sessions.GetSession(sessionID)
	if sess == nil {
		s.releaseSingleShotSession(sessionID)
		return
	}
	go s.runSingleShotSession(featureID, sessionID, phase, sess)
}

func (s *phaseSupervisor) runSingleShotSession(featureID, sessionID string, phase feature.Phase, sess ports.SessionView) {
	startedAt := time.Now()
	establishing := s.singleShotResumer != nil && s.singleShotResumer.SingleShotNeedsEstablishment(sessionID)
	doneCh := sess.Done()
	for {
		select {
		case <-doneCh:
			select {
			case status := <-sess.StatusCh():
				if s.handleSingleShotStatus(featureID, sessionID, phase, sess, status, true, startedAt, establishing) {
					return
				}
				doneCh = nil
				continue
			default:
			}
			if cost := sess.Cost(); cost != nil {
				if s.handleSingleShotStatus(featureID, sessionID, phase, sess, singleShotStatusFromResult(cost), true, startedAt, establishing) {
					return
				}
				doneCh = nil
				continue
			}
			s.handleSingleShotTerminalFailure(featureID, sessionID, phase, sess, startedAt, establishing)
			return

		case status := <-sess.StatusCh():
			if s.handleSingleShotStatus(featureID, sessionID, phase, sess, status, false, startedAt, establishing) {
				return
			}
		}
	}
}

func (s *phaseSupervisor) claimSingleShotSession(sessionID string) bool {
	s.singleShotMu.Lock()
	defer s.singleShotMu.Unlock()
	if s.singleShotSessions == nil {
		s.singleShotSessions = make(map[string]struct{})
	}
	if _, exists := s.singleShotSessions[sessionID]; exists {
		return false
	}
	s.singleShotSessions[sessionID] = struct{}{}
	return true
}

func (s *phaseSupervisor) releaseSingleShotSession(sessionID string) {
	s.singleShotMu.Lock()
	defer s.singleShotMu.Unlock()
	delete(s.singleShotSessions, sessionID)
}

func (s *phaseSupervisor) handleSingleShotStatus(featureID, sessionID string, phase feature.Phase, sess ports.SessionView, status string, sessionDone bool, startedAt time.Time, establishing bool) bool {
	if status == phaseSupervisorStatusAPIError && !sessionDone {
		return false
	}
	_ = sess.Stop()
	s.releaseSingleShotSession(sessionID)
	if status == phaseSupervisorStatusSuccess {
		if s.singleShotResumer != nil {
			s.singleShotResumer.CaptureSingleShotProviderSnapshot(sessionID, sess)
			if establishing && !s.singleShotResumer.CompleteSingleShotResumeEstablishment(sessionID, sess, time.Since(startedAt)) {
				s.resumeSingleShotFresh(featureID, sessionID, phase, 1)
				return true
			}
			s.singleShotResumer.MarkSingleShotCompleted(sessionID)
		}
		s.complete(featureID, PhaseCompletionInput{
			Phase:     phase,
			SessionID: sessionID,
			Success:   true,
		})
		return true
	}
	s.handleSingleShotTerminalFailure(featureID, sessionID, phase, sess, startedAt, establishing)
	return true
}

func (s *phaseSupervisor) handleSingleShotTerminalFailure(featureID, sessionID string, phase feature.Phase, sess ports.SessionView, startedAt time.Time, establishing bool) {
	s.releaseSingleShotSession(sessionID)
	if s.singleShotResumer == nil || s.singleShotResumer.SingleShotPhaseComplete(sessionID) {
		s.completeSingleShotFailure(featureID, sessionID, phase, sess)
		return
	}
	s.singleShotResumer.CaptureSingleShotProviderSnapshot(sessionID, sess)
	if establishing && !s.singleShotResumer.CompleteSingleShotResumeEstablishment(sessionID, sess, time.Since(startedAt)) {
		s.resumeSingleShotFresh(featureID, sessionID, phase, 1)
		return
	}
	classification := sessionruntime.ClassifyFailure(sess)
	if classification.Tier != sessionruntime.TransientRetryable || !s.singleShotResumer.SingleShotSupportsResume(sessionID) {
		s.completeSingleShotFailure(featureID, sessionID, phase, sess)
		return
	}
	s.runSingleShotAutoResume(featureID, sessionID, phase, sess)
}

func (s *phaseSupervisor) runSingleShotAutoResume(featureID, sessionID string, phase feature.Phase, failed ports.SessionView) {
	const (
		consecutiveCap = 3
		totalCap       = 10
	)
	consecutive := 0
	total := 0
	resumeOrdinal := 0
	currentID := sessionID
	current := failed
	for {
		if consecutive >= consecutiveCap || total >= totalCap {
			s.completeSingleShotFailure(featureID, currentID, phase, current)
			return
		}
		classification := sessionruntime.ClassifyFailure(current)
		if classification.Tier != sessionruntime.TransientRetryable {
			s.completeSingleShotFailure(featureID, currentID, phase, current)
			return
		}
		resumeID := singleShotProviderSessionID(current)
		if resumeID == "" {
			s.completeSingleShotFailure(featureID, currentID, phase, current)
			return
		}
		wait := singleShotResumeBackoff(total)
		if classification.RetryHint > wait {
			wait = classification.RetryHint
		}
		if !s.waitForSingleShotResume(currentID, wait) {
			s.completeSingleShotFailure(featureID, currentID, phase, current)
			return
		}

		total++
		resumeOrdinal++
		startedAt := time.Now()
		result, err := s.singleShotResumer.DispatchSingleShotContinuation(currentID, resumeID, resumeOrdinal, false)
		if agent.IsSingleShotResumeRejection(err) {
			total--
			s.resumeSingleShotFresh(featureID, currentID, phase, 1)
			return
		}
		if err != nil {
			s.completeSingleShotFailure(featureID, currentID, phase, current)
			return
		}
		currentID, current = result.SessionID, result.Session
		status := waitForSingleShotTerminal(current)
		s.singleShotResumer.CaptureSingleShotProviderSnapshot(currentID, current)
		if !s.singleShotResumer.CompleteSingleShotResumeEstablishment(currentID, current, time.Since(startedAt)) {
			total--
			s.resumeSingleShotFresh(featureID, currentID, phase, 1)
			return
		}
		if status == phaseSupervisorStatusSuccess {
			s.singleShotResumer.MarkSingleShotCompleted(currentID)
			s.complete(featureID, PhaseCompletionInput{Phase: phase, SessionID: currentID, Success: true})
			return
		}
		if singleShotSessionMadeProgress(current) {
			consecutive = 0
		} else {
			consecutive++
		}
	}
}

func (s *phaseSupervisor) resumeSingleShotFresh(featureID, previousSessionID string, phase feature.Phase, ordinal int) {
	result, err := s.singleShotResumer.DispatchSingleShotContinuation(previousSessionID, "", ordinal, true)
	if err != nil {
		s.complete(featureID, PhaseCompletionInput{
			Phase:       phase,
			SessionID:   previousSessionID,
			Success:     false,
			ErrorDetail: err.Error(),
		})
		return
	}
	s.runSingleShotSession(featureID, result.SessionID, phase, result.Session)
}

func (s *phaseSupervisor) waitForSingleShotResume(sessionID string, wait time.Duration) bool {
	if s.autoResumeWait != nil {
		return s.autoResumeWait(wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return (s.sessions == nil || !s.sessions.IsShuttingDown()) &&
				(s.singleShotResumer == nil || !s.singleShotResumer.SingleShotInterrupted(sessionID))
		case <-ticker.C:
			if (s.sessions != nil && s.sessions.IsShuttingDown()) ||
				(s.singleShotResumer != nil && s.singleShotResumer.SingleShotInterrupted(sessionID)) {
				return false
			}
		}
	}
}

func singleShotResumeBackoff(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 5 * time.Second
	case 1:
		return 20 * time.Second
	default:
		return 60 * time.Second
	}
}

func singleShotProviderSessionID(sess ports.SessionView) string {
	if providerSession, ok := sess.(interface{ SessionID() string }); ok {
		return providerSession.SessionID()
	}
	return ""
}

func singleShotSessionMadeProgress(sess ports.SessionView) bool {
	if sess == nil {
		return false
	}
	usage := sess.AccumulatedUsage()
	if usage.InputTokens > 0 || usage.OutputTokens > 0 || usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 {
		return true
	}
	if sess.MessageLog() == nil {
		return false
	}
	for _, msg := range sess.MessageLog().Messages() {
		if msg.Assistant != nil || msg.ToolProgress != nil || msg.TaskStarted != nil ||
			msg.TaskProgress != nil || msg.TaskNotification != nil ||
			len(msg.FileReads) > 0 || len(msg.FileChanges) > 0 {
			return true
		}
	}
	return false
}

func waitForSingleShotTerminal(sess ports.SessionView) string {
	for {
		select {
		case status := <-sess.StatusCh():
			if status != phaseSupervisorStatusAPIError {
				_ = sess.Stop()
				return status
			}
		case <-sess.Done():
			select {
			case status := <-sess.StatusCh():
				_ = sess.Stop()
				return status
			default:
			}
			return singleShotStatusFromResult(sess.Cost())
		}
	}
}

func (s *phaseSupervisor) completeSingleShotFailure(featureID, sessionID string, phase feature.Phase, sess ports.SessionView) {
	s.releaseSingleShotSession(sessionID)
	s.complete(featureID, PhaseCompletionInput{
		Phase:       phase,
		SessionID:   sessionID,
		Success:     false,
		ErrorDetail: singleShotErrorDetail(phase, sess),
	})
}

func singleShotStatusFromResult(result *llm.ResultMessage) string {
	if result == nil {
		return phaseSupervisorStatusFailed
	}
	if result.IsSuccess() {
		return phaseSupervisorStatusSuccess
	}
	if result.Subtype == "error" || result.IsError { //nolint:goconst // llm.ResultMessage.Subtype wire value, unrelated to this package's FinalStatus vocabulary
		return phaseSupervisorStatusAPIError
	}
	return phaseSupervisorStatusFailed
}

func singleShotErrorDetail(phase feature.Phase, sess ports.SessionView) string {
	if sess != nil {
		if detail := sess.ErrorDetail(); detail != "" {
			return detail
		}
		if detail := sess.ExitCodeDetail(); detail != "" {
			return detail
		}
	}
	return fmt.Sprintf("%s phase session exited without success", phase.String())
}

func (s *phaseSupervisor) supervisePlanLoop(featureID string, resultCh <-chan *agent.PlanLoopResult) {
	if s == nil || resultCh == nil {
		return
	}
	go func() {
		result, ok := <-resultCh
		if !ok {
			return
		}
		s.complete(featureID, PhaseCompletionInput{
			Phase:      feature.PhasePlan,
			PlanResult: result,
		})
	}()
}

func (s *phaseSupervisor) superviseImplementationLoop(featureID string, resultCh <-chan *agent.OrchestratorResult) {
	if s == nil || resultCh == nil {
		return
	}
	go func() {
		for res := range resultCh {
			if res == nil {
				continue
			}
			switch res.FinalStatus {
			case finalStatusAllPassed, "awaiting_final_review", finalStatusFailed, finalStatusNeedUserInput, finalStatusPlanRevisionRequired, finalStatusInterrupted:
				s.complete(featureID, PhaseCompletionInput{
					Phase:           feature.PhaseImplement,
					MultiRepoResult: res,
				})
				return
			}
		}
	}()
}

func (s *phaseSupervisor) complete(featureID string, input PhaseCompletionInput) {
	if s == nil || s.completion == nil {
		return
	}
	if err := s.completion.HandlePhaseCompletion(featureID, input); err != nil && s.onCompletionError != nil {
		s.onCompletionError(featureID, err)
	}
}

func (o *Orchestrator) phaseSupervisor() *phaseSupervisor {
	if o == nil {
		return nil
	}
	return o.supervisor
}

func (o *Orchestrator) superviseSingleShotPhaseSession(featureID, sessionID string, phase feature.Phase) {
	o.phaseSupervisor().superviseSingleShotSession(featureID, sessionID, phase)
}
