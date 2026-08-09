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

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const phaseSupervisorStatusSuccess = "SUCCESS"

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
	CommitOutcome     func(featureID, sessionID string, phase feature.Phase, intent llm.CompletionIntent) ([]agent.ProtocolViolation, error)
	OnCompletionError func(featureID string, err error)
}

type phaseSupervisor struct {
	completion        phaseCompletionSink
	sessions          ports.SessionManager
	commitOutcome     func(featureID, sessionID string, phase feature.Phase, intent llm.CompletionIntent) ([]agent.ProtocolViolation, error)
	onCompletionError func(featureID string, err error)

	singleShotMu       sync.Mutex
	singleShotSessions map[string]struct{}
}

func newPhaseSupervisor(cfg phaseSupervisorConfig) *phaseSupervisor {
	return &phaseSupervisor{
		completion:        cfg.Completion,
		sessions:          cfg.Sessions,
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
	if result.Err != nil {
		detail = result.Err.Error()
	} else if len(result.ProtocolViolations) > 0 {
		detail = formatSingleShotProtocolViolationError("", "", result.ProtocolViolations)
	}
	s.complete(featureID, PhaseCompletionInput{
		Phase:       phase,
		SessionID:   sessionID,
		Success:     false,
		ErrorDetail: detail,
	})
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
