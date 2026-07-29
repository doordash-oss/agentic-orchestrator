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
// restriction from a temporary repository-count restriction.
const (
	// ChildCapabilityProfileUnsupported: Large and Moonshot children cannot
	// execute until child knowledge-base isolation exists.
	ChildCapabilityProfileUnsupported = "unsupported_profile"
	// ChildCapabilityRepoCountUnsupported: children spanning more than one
	// repository cannot execute until transactional multi-repository
	// integration exists; only single-repository integration is supported.
	ChildCapabilityRepoCountUnsupported = "unsupported_repo_count"
)

// ChildCapabilityError rejects execution of a setup-complete child whose
// shape is not yet supported. It never invalidates or closes the child: the
// record stays setup-complete and inspectable, and becomes runnable once the
// phase that retires the restriction lands.
type ChildCapabilityError struct {
	FeatureID string
	Reason    string
	Profile   PipelineProfile
	RepoCount int
}

func (e *ChildCapabilityError) Error() string {
	switch e.Reason {
	case ChildCapabilityProfileUnsupported:
		return fmt.Sprintf("child feature %s cannot execute yet: %s pipeline execution requires temporary child knowledge-base isolation that is not available", e.FeatureID, e.Profile)
	case ChildCapabilityRepoCountUnsupported:
		return fmt.Sprintf("child feature %s cannot execute yet: %d repositories require transactional multi-repository integration that is not available", e.FeatureID, e.RepoCount)
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
// only a Medium refactor child with exactly one repository may execute. The
// check is deterministic from the durable record and is enforced identically
// by start, resume, retry, and restart paths.
func (f *Feature) ChildExecutionCapability() error {
	if f == nil || !f.IsChild() {
		return nil
	}
	if profile := f.EffectivePipeline(); profile != PipelineMedium {
		return &ChildCapabilityError{
			FeatureID: f.ID,
			Reason:    ChildCapabilityProfileUnsupported,
			Profile:   profile,
			RepoCount: len(f.Repos),
		}
	}
	if n := len(f.Repos); n != 1 {
		return &ChildCapabilityError{
			FeatureID: f.ID,
			Reason:    ChildCapabilityRepoCountUnsupported,
			Profile:   PipelineMedium,
			RepoCount: n,
		}
	}
	return nil
}

// Close outcomes recorded on ChildRelationship.CloseOutcome.
const (
	// ChildCloseOutcomeCompleted: the child's work was integrated into the
	// parent and the relationship closed successfully.
	ChildCloseOutcomeCompleted = "completed"
)

// Integration phases recorded on ChildIntegration.Phase.
const (
	// ChildIntegrationPhasePending: anchors are durable (child head and
	// parent anchor captured, remaining child changes committed) but the
	// merge has not been attempted yet.
	ChildIntegrationPhasePending = "pending"
	// ChildIntegrationAttention: a preflight or merge failure paused
	// integration; Restart replays preflight and the merge attempt without
	// rerunning Plan, Implement, or an approved Final Review.
	ChildIntegrationPhaseAttention = "attention"
	// ChildIntegrationMerged: the no-fast-forward merge boundary is durable
	// on the parent branch; closure and cleanup still pending.
	ChildIntegrationPhaseMerged = "merged"
)

// ChildIntegration is the durable record of a child-to-parent local
// integration attempt for the single-repository boundary. It captures the
// anchors needed to describe the attempt (parent anchor and child head), the
// resulting merge HEAD once durable, and structured attention when a
// preflight or merge failure leaves the parent ref unchanged and the child
// parked for a retrying Restart.
type ChildIntegration struct {
	// ParentBranch is the recorded parent branch the merge targets.
	ParentBranch string `yaml:"parent_branch"`
	// ParentAnchorSHA is the full parent-branch HEAD captured immediately
	// before preflight; the parent ref must never regress past it.
	ParentAnchorSHA string `yaml:"parent_anchor_sha"`
	// ChildHeadSHA is the full child HEAD after committing every remaining
	// child change; it is recorded before the parent branch is touched.
	ChildHeadSHA string `yaml:"child_head_sha"`
	// MergeHEAD is the full SHA of the two-parent no-fast-forward merge
	// commit created on ParentBranch; empty until the merge is durable.
	MergeHEAD string `yaml:"merge_head,omitempty"`
	// Phase is empty while integration has not been attempted, "attention"
	// after a retryable failure, and "merged" once the boundary is durable.
	Phase string `yaml:"phase,omitempty"`
	// Attention is a human-readable summary of the blocking condition.
	Attention string `yaml:"attention,omitempty"`
	// Dirty carries categorized parent-worktree diagnostics when a dirty
	// preflight blocked the merge; the parent ref is untouched in that case.
	Dirty []RepoDirtyDiagnostics `yaml:"dirty,omitempty"`
	// CleanupWarning records a non-fatal worktree/branch cleanup failure.
	// Cleanup failure never reopens the child or undoes the merge.
	CleanupWarning string `yaml:"cleanup_warning,omitempty"`
}
