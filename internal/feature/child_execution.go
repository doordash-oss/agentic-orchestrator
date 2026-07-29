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

package feature

import (
	"errors"
	"fmt"
)

// ErrChildExecutionClosed rejects execution of a child whose relationship
// has settled: the closed record stays inspectable, but starting, resuming,
// or retrying pipeline phases would rerun work whose integration outcome is
// already durable (and whose disposable worktree may be gone). The only
// re-entry is the narrowly scoped Restart route for a Completed child whose
// impermanent closure tail is genuinely resumable (IntegrationResumable).
var ErrChildExecutionClosed = errors.New("child relationship is closed")

// Child execution capability reasons. Reason values are stable identifiers
// surfaced through the API so clients can distinguish a temporary profile
// restriction from other limitations.
const (
	// ChildCapabilityProfileUnsupported: Large and Moonshot children cannot
	// execute until child knowledge-base isolation exists.
	ChildCapabilityProfileUnsupported = "unsupported_profile"
)

// ChildCapabilityError rejects execution of a setup-complete child whose
// shape is not yet supported. It never invalidates or closes the child: the
// record stays setup-complete and inspectable, and becomes runnable once the
// phase that retires the restriction lands.
type ChildCapabilityError struct {
	FeatureID string
	Reason    string
	Profile   PipelineProfile
}

func (e *ChildCapabilityError) Error() string {
	switch e.Reason {
	case ChildCapabilityProfileUnsupported:
		return fmt.Sprintf("child feature %s cannot execute yet: %s pipeline execution requires temporary child knowledge-base isolation that is not available", e.FeatureID, e.Profile)
	default:
		return fmt.Sprintf("child feature %s cannot execute yet: %s", e.FeatureID, e.Reason)
	}
}

// ChildSetupComplete reports whether the child's durable setup finished
// successfully — the child is parked at Created (or any later non-failed
// status). Queued/setting-up and failed-setup children report false and stay
// reachable only through the setup and setup-retry entrypoints.
func (f *Feature) ChildSetupComplete() bool {
	if f == nil || !f.IsChild() {
		return false
	}
	if f.Status == StatusSettingUpWorktrees {
		return false
	}
	if f.Status == StatusFailed && f.FailureType == FailureWorktreeSetup {
		return false
	}
	return true
}

// ChildExecutionCapability enforces the supported child execution shape:
// only a Medium refactor child may execute. The repository-count restriction
// is retired for Medium children — multi-repository transactional integration
// is now supported. Large and Moonshot children still require temporary
// child knowledge-base isolation. The check is deterministic from the
// durable record and is enforced identically by start, resume, retry, and
// restart paths.
func (f *Feature) ChildExecutionCapability() error {
	if f == nil || !f.IsChild() {
		return nil
	}
	if profile := f.EffectivePipeline(); profile != PipelineMedium {
		return &ChildCapabilityError{
			FeatureID: f.ID,
			Reason:    ChildCapabilityProfileUnsupported,
			Profile:   profile,
		}
	}
	return nil
}

// Close outcomes recorded on ChildRelationship.CloseOutcome.
const (
	// ChildCloseOutcomeCompleted: the child's work was integrated into the
	// parent and the relationship closed successfully.
	ChildCloseOutcomeCompleted = "completed"
	// ChildCloseOutcomeDiscarded: the child was discarded without integrating
	// its work into the parent. The relationship is closed and the child
	// record is retained for inspection.
	ChildCloseOutcomeDiscarded = "discarded"
)
