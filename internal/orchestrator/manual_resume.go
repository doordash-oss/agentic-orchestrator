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
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// ErrResumeConflict reports that a feature already has active work or another
// resumer currently owns its bookkeeping-plus-dispatch window.
var ErrResumeConflict = errors.New("resume already in progress")

// ResumeFeature dispatches the existing provider session when a Failed
// implementation has a strict-match resume record. Ineligible records retain
// the historical fresh-start recovery behavior.
func (o *Orchestrator) ResumeFeature(featureID string) error {
	f, err := o.deps.Lifecycle.Get(featureID)
	if err != nil {
		return fmt.Errorf("load feature for resume: %w", err)
	}
	if o.featureHasActiveSession(featureID) {
		return ErrResumeConflict
	}
	switch f.Status {
	case feature.StatusInterrupted, feature.StatusNeedUserInput:
		return o.StartFeature(featureID)
	case feature.StatusFailed:
		// Continue below: Failed is the only status whose action-catalog
		// eligibility selects between provider continuation and fresh retry.
	default:
		return ErrResumeConflict
	}

	coordinator := o.resumeCoordinatorForFeature(f)
	if coordinator != nil && o.deps.PhaseRunner != nil {
		model := o.deps.PhaseRunner.ModelForRole(f.Models.Implementation, llm.PhaseImplementation)
		claim, eligibility, claimErr := coordinator.Claim(
			featureID,
			f,
			model,
			o.deps.PhaseRunner.Registry,
			time.Now(),
		)
		if claimErr != nil {
			if errors.Is(claimErr, agent.ErrResumeAlreadyClaimed) {
				return ErrResumeConflict
			}
			return claimErr
		}
		if eligibility.Eligible {
			if err := o.resumeEligibleFailedFeature(featureID); err != nil {
				_ = claim.Release(time.Now())
				return err
			}
			claim.DispatchStarted()
			return nil
		}
	}

	return o.resumeFailedFeature(featureID)
}

func (o *Orchestrator) resumeEligibleFailedFeature(featureID string) error {
	if err := o.RetryPhase(featureID); err != nil {
		return fmt.Errorf("reset failed phase for resume: %w", err)
	}
	if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
		return fmt.Errorf("prepare failed phase for resume: %w", err)
	}
	if err := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
		if err := f.Transition(feature.StatusImplementing); err != nil {
			return err
		}
		// Provider continuation re-enters the same implementation unit.
		// Preserve CurrentIteration and ActiveTimingKey instead of calling
		// StartImplementation, whose fresh-start contract resets iteration 1.
		f.CurrentPhase = feature.PhaseImplement
		now := time.Now()
		f.ActivePhaseStart = &now
		if f.StartedAt == nil {
			f.StartedAt = &now
		}
		return nil
	}); err != nil {
		return fmt.Errorf("transition provider resume to implementing: %w", err)
	}
	return o.StartFeature(featureID)
}

func (o *Orchestrator) resumeFailedFeature(featureID string) error {
	if err := o.RetryPhase(featureID); err != nil {
		return fmt.Errorf("reset failed phase for resume: %w", err)
	}
	if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
		return fmt.Errorf("prepare failed phase for resume: %w", err)
	}
	return o.StartFeature(featureID)
}

func (o *Orchestrator) featureHasActiveSession(featureID string) bool {
	if o == nil || o.deps.Sessions == nil {
		return false
	}
	for _, sess := range o.deps.Sessions.FeatureSessions(featureID) {
		if sess != nil && sess.IsActive() {
			return true
		}
	}
	return false
}

func (o *Orchestrator) resumeCoordinatorForFeature(f *feature.Feature) *agent.ResumeCoordinator {
	if o == nil || f == nil || o.stateDir() == "" || f.CurrentIteration <= 0 {
		return nil
	}
	dir := filepath.Join(
		agent.ActiveImplementDir(o.stateDir(), f),
		fmt.Sprintf("iteration-%02d", f.CurrentIteration),
	)
	return agent.NewResumeCoordinator(dir)
}
