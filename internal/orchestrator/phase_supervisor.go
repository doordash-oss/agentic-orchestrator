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
	"path/filepath"
	"sync"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const phaseSupervisorStatusSuccess = "SUCCESS"

// PHASE2(single-shot statuses): main dropped the FAILED/API_ERROR supervisor
// status constants together with its loop-based supervisor removal; the
// feature resume loop kept below still classifies session statuses with them.
const (
	phaseSupervisorStatusFailed   = "FAILED"
	phaseSupervisorStatusAPIError = "API_ERROR"
)

// Shared FinalStatus values reported by agent implementation/review
// loops (agent.OrchestratorResult, agent.LoopResult, etc.).
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
	CommitOutcome     func(featureID, sessionID string, phase feature.Phase, intent llm.CompletionIntent) ([]agent.ProtocolViolation, error)
	OnCompletionError func(featureID string, err error)
}

type singleShotResumeDriver interface {
	SingleShotPhaseComplete(sessionID string) bool
	SingleShotSupportsResume(sessionID string) bool
	SingleShotInterrupted(sessionID string) bool
	SingleShotNeedsEstablishment(sessionID string) bool
	CaptureSingleShotProviderSnapshot(sessionID string, sess ports.SessionView) error
	DispatchSingleShotContinuation(previousSessionID, resumeID string, ordinal int, fresh bool) (*agent.SingleShotResumeResult, error)
	CompleteSingleShotResumeEstablishment(sessionID string, sess ports.SessionView, elapsed time.Duration) (bool, error)
	MarkSingleShotCompleted(sessionID string) error
	RetireSingleShotResume(sessionID string)
}

type phaseSupervisor struct {
	completion        phaseCompletionSink
	sessions          ports.SessionManager
	singleShotResumer singleShotResumeDriver
	autoResumeWait    func(time.Duration) bool
	commitOutcome     func(featureID, sessionID string, phase feature.Phase, intent llm.CompletionIntent) ([]agent.ProtocolViolation, error)
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
		commitOutcome:     cfg.CommitOutcome,
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
	// PHASE2(single-shot completion): the feature resume/retry loop and main's
	// commit-based WaitForPhaseOutcome redesign overlap here. The resume loop
	// runs when a single-shot resume driver is configured and completes via
	// s.complete without commit-time outcome validation; otherwise main's
	// commit path runs. Reconciling resume with commit validation is deferred.
	if s.singleShotResumer != nil {
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
	result := agent.WaitForPhaseOutcome(sess, agent.PhaseOutcomeWaitOptions{
		CommitOutcome: func(intent llm.CompletionIntent) ([]agent.ProtocolViolation, error) {
			if s.commitOutcome == nil {
				return []agent.ProtocolViolation{{
					Artifact: "agentico-outcome",
					Reason:   "single-shot completion committer is not configured",
				}}, nil
			}
			return s.commitOutcome(featureID, sessionID, phase, intent)
		},
	})
	s.releaseSingleShotSession(sessionID)
	if result.Status == phaseSupervisorStatusSuccess {
		s.complete(featureID, PhaseCompletionInput{
			Phase:     phase,
			SessionID: sessionID,
			Success:   true,
		})
		return
	}

	detail := singleShotErrorDetail(phase, sess)
	failureType := ""
	if result.Err != nil {
		detail = result.Err.Error()
	} else if len(result.ProtocolViolations) > 0 {
		artifactDir := ""
		if logPath := sess.LogFilePath(); logPath != "" {
			artifactDir = filepath.Dir(logPath)
		}
		detail = formatSingleShotProtocolViolationError(singleShotPhaseRole(phase), artifactDir, result.ProtocolViolations)
		failureType = feature.FailureProtocolViolation
	}
	s.complete(featureID, PhaseCompletionInput{
		Phase:       phase,
		SessionID:   sessionID,
		Success:     false,
		ErrorDetail: detail,
		FailureType: failureType,
	})
}

func singleShotPhaseRole(phase feature.Phase) agent.Role {
	if phase == feature.PhaseKnowledgeBase {
		return agent.RoleKnowledgeBaseBuilder
	}
	role, _ := artifactPhaseRole(phase)
	return role
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
			if err := s.singleShotResumer.CaptureSingleShotProviderSnapshot(sessionID, sess); err != nil {
				s.completeSingleShotPersistenceFailure(featureID, sessionID, phase, err)
				return true
			}
			if establishing {
				established, err := s.singleShotResumer.CompleteSingleShotResumeEstablishment(sessionID, sess, time.Since(startedAt))
				if err != nil {
					s.completeSingleShotPersistenceFailure(featureID, sessionID, phase, err)
					return true
				}
				if !established {
					s.resumeSingleShotFresh(featureID, sessionID, phase, 1)
					return true
				}
			}
			if err := s.singleShotResumer.MarkSingleShotCompleted(sessionID); err != nil {
				s.completeSingleShotPersistenceFailure(featureID, sessionID, phase, err)
				return true
			}
		}
		s.complete(featureID, PhaseCompletionInput{
			Phase:     phase,
			SessionID: sessionID,
			RepoName:  sess.RepoName(),
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
	if err := s.singleShotResumer.CaptureSingleShotProviderSnapshot(sessionID, sess); err != nil {
		s.completeSingleShotPersistenceFailure(featureID, sessionID, phase, err)
		return
	}
	if establishing {
		established, err := s.singleShotResumer.CompleteSingleShotResumeEstablishment(sessionID, sess, time.Since(startedAt))
		if err != nil {
			s.completeSingleShotPersistenceFailure(featureID, sessionID, phase, err)
			return
		}
		if !established {
			s.resumeSingleShotFresh(featureID, sessionID, phase, 1)
			return
		}
	}
	s.runSingleShotAutoResume(featureID, sessionID, phase, sess)
}

func (s *phaseSupervisor) runSingleShotAutoResume(featureID, sessionID string, phase feature.Phase, failed ports.SessionView) {
	runAttempt := func(previous agent.AutoResumeProcess, resumeID string, ordinal int, fresh bool) (agent.AutoResumeAttempt, error) {
		startedAt := time.Now()
		result, err := s.singleShotResumer.DispatchSingleShotContinuation(previous.ID, resumeID, ordinal, fresh)
		if err != nil {
			if agent.IsSingleShotResumeRejection(err) {
				return agent.AutoResumeAttempt{Rejected: true, Reason: string(agent.ResumeReasonSessionRejected)}, nil
			}
			return agent.AutoResumeAttempt{}, err
		}
		status := waitForSingleShotTerminal(result.Session)
		if err := s.singleShotResumer.CaptureSingleShotProviderSnapshot(result.SessionID, result.Session); err != nil {
			return agent.AutoResumeAttempt{}, err
		}
		if !fresh {
			established, err := s.singleShotResumer.CompleteSingleShotResumeEstablishment(result.SessionID, result.Session, time.Since(startedAt))
			if err != nil {
				return agent.AutoResumeAttempt{}, err
			}
			if !established {
				return agent.AutoResumeAttempt{
					Process:  agent.AutoResumeProcess{Session: result.Session, Status: status, ID: result.SessionID},
					Rejected: true,
					Reason:   string(agent.ResumeReasonSessionRejected),
				}, nil
			}
		}
		return agent.AutoResumeAttempt{Process: agent.AutoResumeProcess{
			Session: result.Session,
			Status:  status,
			ID:      result.SessionID,
		}}, nil
	}
	initial := agent.AutoResumeProcess{Session: failed, Status: phaseSupervisorStatusFailed, ID: sessionID}
	result, err := (agent.AutoResumeEngine{}).Run(initial, agent.AutoResumeCallbacks{
		Failed: func(process agent.AutoResumeProcess) bool {
			return process.Status == phaseSupervisorStatusFailed
		},
		SupportsResume: func(process agent.AutoResumeProcess) bool {
			return s.singleShotResumer.SingleShotSupportsResume(process.ID)
		},
		HasCompleted: func(process agent.AutoResumeProcess) bool {
			return s.singleShotResumer.SingleShotPhaseComplete(process.ID)
		},
		ResumeID: func(process agent.AutoResumeProcess) string {
			return singleShotProviderSessionID(process.Session)
		},
		WaitBackoff: func(process agent.AutoResumeProcess, wait time.Duration) bool {
			return s.waitForSingleShotResume(process.ID, wait)
		},
		Resume: func(previous agent.AutoResumeProcess, resumeID string, ordinal int) (agent.AutoResumeAttempt, error) {
			return runAttempt(previous, resumeID, ordinal, false)
		},
		FreshFallback: func(previous agent.AutoResumeProcess, _ string, ordinal int) (agent.AutoResumeAttempt, error) {
			return runAttempt(previous, "", ordinal, true)
		},
		Interrupted: func(process agent.AutoResumeProcess) bool {
			return s.singleShotResumer.SingleShotInterrupted(process.ID)
		},
	})
	if err != nil {
		s.completeSingleShotPersistenceFailure(featureID, result.Process.ID, phase, err)
		return
	}
	if result.Process.Status == phaseSupervisorStatusSuccess {
		if err := s.singleShotResumer.MarkSingleShotCompleted(result.Process.ID); err != nil {
			s.completeSingleShotPersistenceFailure(featureID, result.Process.ID, phase, err)
			return
		}
		s.complete(featureID, PhaseCompletionInput{Phase: phase, SessionID: result.Process.ID, Success: true})
		return
	}
	s.completeSingleShotFailure(featureID, result.Process.ID, phase, result.Process.Session)
}

func (s *phaseSupervisor) completeSingleShotPersistenceFailure(featureID, sessionID string, phase feature.Phase, err error) {
	s.complete(featureID, PhaseCompletionInput{
		Phase:       phase,
		SessionID:   sessionID,
		Success:     false,
		ErrorDetail: fmt.Sprintf("persisting resume lifecycle: %v", err),
	})
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

func singleShotProviderSessionID(sess ports.SessionView) string {
	if providerSession, ok := sess.(interface{ SessionID() string }); ok {
		return providerSession.SessionID()
	}
	return ""
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
	if s.singleShotResumer != nil {
		s.singleShotResumer.RetireSingleShotResume(sessionID)
	}
	s.complete(featureID, PhaseCompletionInput{
		Phase:       phase,
		SessionID:   sessionID,
		RepoName:    sess.RepoName(),
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
