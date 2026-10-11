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

import "github.com/doordash-oss/agentic-orchestrator/internal/feature"

// This file exposes unexported orchestrator steps to the external
// orchestrator_test package so its tests can drive one step in isolation.
// It is compiled only during testing.

func (o *Orchestrator) RunSetupAsync(featureID string) { o.runSetupAsync(featureID) }

func (o *Orchestrator) RunSetup(featureID string) error { return o.runSetup(featureID) }

func (o *Orchestrator) RetrySetup(featureID string) error { return o.retrySetup(featureID) }

// RestartPhase applies the restart transition alone, without the dispatch
// RestartFeature performs.
func (o *Orchestrator) RestartPhase(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) (RestartOutcome, error) {
	unlock := o.lockRelationshipRead()
	defer unlock()
	return o.restartPhaseLocked(featureID, maxIterationsDelta, maxPlanIterationsDelta)
}

func (o *Orchestrator) InterruptFeature(featureID string) error { return o.interruptFeature(featureID) }

func (o *Orchestrator) ExtendFailedPhaseBudget(featureID string, maxIterationsDelta, maxPlanIterationsDelta int) error {
	return o.extendFailedPhaseBudget(featureID, maxIterationsDelta, maxPlanIterationsDelta)
}

func (o *Orchestrator) ProceedFromRewindReview(featureID string, target feature.Phase) error {
	return o.proceedFromRewindReview(featureID, target)
}

func (o *Orchestrator) Publish(featureID string) error { return o.publish(featureID) }

func (o *Orchestrator) StartMultiRepoImplementation(featureID string) error {
	return o.startMultiRepoImplementation(featureID)
}

// SetPublishFn intercepts publish dispatch in place of the publish step.
func (o *Orchestrator) SetPublishFn(fn func(featureID string) error) { o.publishFn = fn }
