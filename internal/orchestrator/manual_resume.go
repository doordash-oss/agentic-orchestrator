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
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// ErrResumeConflict reports that a feature already has an active session or
// another resumer currently owns its bookkeeping-plus-dispatch window.
var ErrResumeConflict = errors.New("resume already in progress")

// ErrResumeNotAvailable reports that the feature's status does not admit a
// manual resume, such as an already-running, completed, or otherwise
// non-resumable feature.
var ErrResumeNotAvailable = errors.New("resume is not available for the feature's current status")

// ResumeFeature dispatches existing provider sessions when a failed sequential
// or composite phase has strict-match resume records. Ineligible records retain
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
	case feature.StatusInterrupted:
		if f.CurrentPhase == feature.PhaseKnowledgeBase {
			claims, err := o.claimInterruptedKBResumes(f)
			if err != nil {
				if errors.Is(err, agent.ErrResumeAlreadyClaimed) {
					return ErrResumeConflict
				}
				return err
			}
			if err := o.StartFeature(featureID); err != nil {
				return errors.Join(err, releaseResumeClaims(claims, time.Now()))
			}
			for _, claim := range claims {
				claim.DispatchStarted()
			}
			return nil
		}
		if !phaseUsesParentResumeClaim(f.CurrentPhase) {
			return o.StartFeature(featureID)
		}
		claim, dispatch := o.claimSequentialResume(featureID, f.CurrentPhase)
		if !dispatch {
			return ErrResumeConflict
		}
		resumeImplementation := claim != nil
		if f.CurrentPhase == feature.PhaseImplement && !resumeImplementation {
			resumeImplementation = o.implementationReviewResumeEligibility(f).Eligible
		}
		if f.CurrentPhase == feature.PhaseImplement && resumeImplementation {
			if err := o.startFeatureForImplementationResume(featureID, feature.StatusInterrupted); err != nil {
				if claim != nil {
					return errors.Join(err, claim.Release(time.Now()))
				}
				return err
			}
			if claim != nil {
				claim.DispatchStarted()
			}
			return nil
		}
		if err := o.StartFeature(featureID); err != nil {
			if claim != nil {
				return errors.Join(err, claim.Release(time.Now()))
			}
			return err
		}
		if claim != nil {
			claim.DispatchStarted()
		}
		return nil
	case feature.StatusNeedUserInput:
		return o.StartFeature(featureID)
	case feature.StatusFailed:
		// Continue below: Failed is the only status whose action-catalog
		// eligibility selects between provider continuation and fresh retry.
	default:
		return ErrResumeNotAvailable
	}

	if f.CurrentPhase == feature.PhaseReview || f.CurrentPhase == feature.PhaseFinalReview {
		eligibility := o.compositeResumeEligibility(f)
		if eligibility.Eligible {
			return o.resumeEligibleFailedFeature(featureID, feature.PhaseFinalReview)
		}
		return o.resumeFailedFeature(featureID, feature.PhaseFinalReview)
	}

	coordinator := o.resumeCoordinatorForFeature(f)
	if coordinator != nil && o.deps.PhaseRunner != nil {
		model := resumeModelForFeature(o.deps.PhaseRunner, f)
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
			if err := o.resumeEligibleFailedFeature(featureID, f.CurrentPhase); err != nil {
				return errors.Join(err, claim.Release(time.Now()))
			}
			claim.DispatchStarted()
			return nil
		}
		if f.CurrentPhase == feature.PhaseImplement && o.implementationReviewResumeEligibility(f).Eligible {
			return o.resumeEligibleFailedFeature(featureID, feature.PhaseImplement)
		}
	}

	return o.resumeFailedFeature(featureID, f.CurrentPhase)
}

func phaseUsesParentResumeClaim(phase feature.Phase) bool {
	switch phase {
	case feature.PhaseInquire,
		feature.PhaseResearch,
		feature.PhaseDesign,
		feature.PhasePlan,
		feature.PhaseImplement:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) claimInterruptedKBResumes(f *feature.Feature) ([]*agent.ResumeClaim, error) {
	return o.claimKBResumes(f, nil)
}

func (o *Orchestrator) claimKBResumes(f *feature.Feature, repoFilter map[string]struct{}) ([]*agent.ResumeClaim, error) {
	if o == nil || f == nil || o.deps.PhaseRunner == nil {
		return nil, nil
	}
	runner := o.deps.PhaseRunner
	model := resumeModelForFeature(runner, f)
	parent := agent.ResumeParentContext{PhaseKey: feature.PhaseKnowledgeBase.DirName()}
	claims := make([]*agent.ResumeClaim, 0, len(f.Repos))
	for _, repo := range f.Repos {
		if repoFilter != nil {
			if _, selected := repoFilter[repo.Name]; !selected {
				continue
			}
		}
		coordinator := agent.NewChildResumeCoordinator(
			agent.KBResumeDir(o.stateDir(), f, repo.Name),
			repo.Name,
			parent,
		)
		claim, eligibility, err := coordinator.Claim(
			f.ID,
			f,
			model,
			runner.Registry,
			time.Now(),
		)
		if err != nil {
			return nil, errors.Join(err, releaseResumeClaims(claims, time.Now()))
		}
		if eligibility.Eligible && claim != nil {
			claims = append(claims, claim)
			continue
		}
		if coordinator.Snapshot() != nil {
			if err := coordinator.MarkFreshFallback(string(eligibility.Reason), time.Now()); err != nil {
				return nil, errors.Join(err, releaseResumeClaims(claims, time.Now()))
			}
		}
	}
	return claims, nil
}

func releaseResumeClaims(claims []*agent.ResumeClaim, at time.Time) error {
	var releaseErr error
	for _, claim := range claims {
		releaseErr = errors.Join(releaseErr, claim.Release(at))
	}
	return releaseErr
}

func (o *Orchestrator) implementationReviewResumeEligibility(f *feature.Feature) agent.ResumeEligibility {
	if o == nil || f == nil || o.deps.PhaseRunner == nil {
		return agent.ResumeEligibility{}
	}
	dir, ok := agent.ResumeUnitDir(o.stateDir(), f)
	if !ok {
		return agent.ResumeEligibility{}
	}
	runner := o.deps.PhaseRunner
	return agent.EvaluateCompositeResumeEligibility(
		dir,
		f,
		runner.Registry,
		agent.ResumeParentContext{
			PhaseKey:  agent.ResumePhaseKey(f),
			Iteration: f.CurrentIteration,
		},
		func(string) string {
			return runner.ModelForRole(f.Models.Review, llm.PhaseReview)
		},
	)
}

func (o *Orchestrator) compositeResumeEligibility(f *feature.Feature) agent.ResumeEligibility {
	if o == nil || f == nil || o.deps.PhaseRunner == nil {
		return agent.EvaluateCompositeResumeEligibility("", f, nil, agent.ResumeParentContext{}, nil)
	}
	dir, ok := agent.ResumeUnitDir(o.stateDir(), f)
	if !ok {
		return agent.EvaluateCompositeResumeEligibility("", f, o.deps.PhaseRunner.Registry, agent.ResumeParentContext{}, nil)
	}
	runner := o.deps.PhaseRunner
	return agent.EvaluateCompositeResumeEligibility(
		dir,
		f,
		runner.Registry,
		agent.ResumeParentContext{
			PhaseKey:  feature.PhaseFinalReview.DirName(),
			Iteration: f.ReviewIteration,
		},
		func(childKey string) string {
			if childKey == string(agent.RoleFinalReviewFixer) {
				return runner.ModelForRole(f.Models.Implementation, llm.PhaseImplementation)
			}
			return runner.ModelForRole(f.Models.Review, llm.PhaseReview)
		},
	)
}

func (o *Orchestrator) resumeEligibleFailedFeature(featureID string, phase feature.Phase) error {
	if phase != feature.PhaseImplement {
		return o.restartFailedSequentialPhase(featureID, phase)
	}
	if err := o.RetryPhase(featureID); err != nil {
		return fmt.Errorf("reset failed phase for resume: %w", err)
	}
	if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
		return fmt.Errorf("prepare failed phase for resume: %w", err)
	}
	return o.startFeatureForImplementationResume(featureID, feature.StatusFailed)
}

// startFeatureForImplementationResume transitions the feature to implementing
// for a provider continuation and dispatches it, reverting the transition when
// dispatch fails. Without the revert, a failed StartFeature strands the
// feature in StatusImplementing — a status ResumeFeature refuses — so every
// subsequent resume attempt would return ErrResumeNotAvailable with no recovery
// path. Mirrors the rollback contract of the need-user-input dispatch path.
func (o *Orchestrator) startFeatureForImplementationResume(featureID string, revertTo feature.Status) error {
	if err := o.transitionImplementationResume(featureID); err != nil {
		return err
	}
	if err := o.StartFeature(featureID); err != nil {
		if rbErr := o.deps.Store.Modify(featureID, func(f *feature.Feature) error {
			if f.Status != feature.StatusImplementing {
				return nil
			}
			return f.Transition(revertTo)
		}); rbErr != nil {
			return errors.Join(err, fmt.Errorf("reverting provider-resume transition to %s: %w", revertTo, rbErr))
		}
		return err
	}
	return nil
}

func (o *Orchestrator) transitionImplementationResume(featureID string) error {
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
	return nil
}

func (o *Orchestrator) resumeFailedFeature(featureID string, phase feature.Phase) error {
	if phase != feature.PhaseImplement {
		return o.restartFailedSequentialPhase(featureID, phase)
	}
	if err := o.RetryPhase(featureID); err != nil {
		return fmt.Errorf("reset failed phase for resume: %w", err)
	}
	if err := o.TransitionTo(featureID, feature.StatusImplementReady); err != nil {
		return fmt.Errorf("prepare failed phase for resume: %w", err)
	}
	return o.StartFeature(featureID)
}

func (o *Orchestrator) restartFailedSequentialPhase(featureID string, phase feature.Phase) error {
	outcome, err := o.RestartPhase(featureID, 0, 0)
	if err != nil {
		return fmt.Errorf("prepare failed phase for restart: %w", err)
	}
	if outcome.Action != RestartDispatchPhase || outcome.Phase != phase {
		return fmt.Errorf("prepare failed phase for restart: unexpected outcome action=%d phase=%s", outcome.Action, outcome.Phase)
	}
	if _, _, err := o.startPhaseGuarded(featureID, phase); err != nil {
		return fmt.Errorf("restart failed phase: %w", err)
	}
	return nil
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
	if o == nil || f == nil || o.stateDir() == "" {
		return nil
	}
	dir, ok := agent.ResumeUnitDir(o.stateDir(), f)
	if !ok {
		return nil
	}
	return agent.NewResumeCoordinator(dir)
}

func resumeModelForFeature(runner *agent.PhaseRunner, f *feature.Feature) string {
	if runner == nil || f == nil {
		return ""
	}
	switch f.CurrentPhase {
	case feature.PhaseKnowledgeBase:
		return runner.ModelForRole(f.Models.KBBuild, llm.PhaseKBBuild)
	case feature.PhaseInquire:
		configured := f.Models.Inquiry
		if configured == "" {
			configured = f.Models.Research
		}
		return runner.ModelForRole(configured, llm.PhaseInquiry)
	case feature.PhaseResearch:
		return runner.ModelForRole(f.Models.Research, llm.PhaseResearch)
	case feature.PhaseDesign, feature.PhasePlan:
		return runner.ModelForRole(f.Models.Planning, llm.PhasePlanning)
	case feature.PhaseImplement:
		return runner.ModelForRole(f.Models.Implementation, llm.PhaseImplementation)
	case feature.PhaseReview, feature.PhaseFinalReview:
		return runner.ModelForRole(f.Models.Review, llm.PhaseReview)
	default:
		return ""
	}
}
