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

// TransactionPhase is the aggregate phase of the multi-repository
// transaction journal. It records the durable lifecycle from candidate
// preparation through application, rollback, and settlement.
type TransactionPhase string

const (
	// TransactionPhasePreparing: candidates are being staged (merge commits
	// created without advancing parent refs). No candidate is durable yet.
	TransactionPhasePreparing TransactionPhase = "preparing"
	// TransactionPhasePrepared: every repository has a durable candidate
	// commit; the candidate vector is complete and ready for application.
	TransactionPhasePrepared TransactionPhase = "prepared"
	// TransactionPhaseApplying: candidates are being applied via
	// compare-and-swap ref updates; some refs may already be at their
	// candidate commits.
	TransactionPhaseApplying TransactionPhase = "applying"
	// TransactionPhaseApplied: every repository's parent ref is confirmed
	// at its candidate commit; closure may proceed.
	TransactionPhaseApplied TransactionPhase = "applied"
	// TransactionPhaseRollingBack: a later apply failure triggered
	// conditional rollback of earlier applied refs.
	TransactionPhaseRollingBack TransactionPhase = "rolling_back"
	// TransactionPhaseRolledBack: rollback completed; all provable
	// earlier applied refs were restored to their old SHAs.
	TransactionPhaseRolledBack TransactionPhase = "rolled_back"
	// TransactionPhaseAttention: a retryable failure (dirty parent, conflict,
	// external race, or unclassifiable state) parked the transaction. The
	// child stays active and restartable.
	TransactionPhaseAttention TransactionPhase = "attention"
	// TransactionPhaseMerged: the transaction is fully applied and the child
	// relationship is durably closed (Completed outcome recorded).
	TransactionPhaseMerged TransactionPhase = "merged"
)

// RepoPrepState records the per-repository preparation outcome.
type RepoPrepState string

const (
	RepoPrepPending  RepoPrepState = "pending"
	RepoPrepPrepared RepoPrepState = "prepared"
	RepoPrepFailed   RepoPrepState = "failed"
)

// RepoApplyState records the per-repository apply or rollback outcome.
type RepoApplyState string

const (
	RepoApplyApplied    RepoApplyState = "applied"
	RepoApplyRolledBack RepoApplyState = "rolled_back"
	RepoApplyAttention  RepoApplyState = "attention"
)

// RepoTransactionEntry is the per-repository journal entry in a transaction.
// It preserves inherited repository order and durably records the per-repo
// target ref, initial anchor, expected ref, child head, candidate commit,
// preparation state, apply/rollback state, and structured diagnostics.
type RepoTransactionEntry struct {
	// Repo is the repository name, matching the inherited parent repo.
	Repo string `yaml:"repo"`
	// ParentBranch is the recorded parent branch the merge targets.
	ParentBranch string `yaml:"parent_branch"`
	// ParentAnchorSHA is the full parent-branch HEAD captured immediately
	// before preparation; the parent ref must never regress past it.
	ParentAnchorSHA string `yaml:"parent_anchor_sha"`
	// ExpectedRefSHA is the latest expected parent tip at apply time; the
	// compare-and-swap ref update must match this SHA.
	ExpectedRefSHA string `yaml:"expected_ref_sha,omitempty"`
	// ChildHeadSHA is the full child HEAD after committing every remaining
	// child change; recorded before any parent branch is touched.
	ChildHeadSHA string `yaml:"child_head_sha"`
	// CandidateSHA is the full SHA staged for this repository without
	// advancing the parent ref. Usually this is an explicit two-parent
	// no-fast-forward merge commit; for a rebase pass-through repo that was
	// already up to date at launch, this can equal ParentAnchorSHA.
	CandidateSHA string `yaml:"candidate_sha,omitempty"`
	// MergeHEAD is the full SHA confirmed on the parent ref after apply. For
	// a successful apply, this equals CandidateSHA.
	MergeHEAD string `yaml:"merge_head,omitempty"`
	// PrepState records whether the candidate is pending, prepared, or failed.
	PrepState RepoPrepState `yaml:"prep_state,omitempty"`
	// ApplyState records the per-repo apply or rollback outcome.
	ApplyState RepoApplyState `yaml:"apply_state,omitempty"`
	// ObservedSHA is the actual ref SHA observed at apply or rollback time;
	// used to diagnose external races.
	ObservedSHA string `yaml:"observed_sha,omitempty"`
	// Dirty carries categorized parent-worktree diagnostics when a dirty
	// preflight blocked preparation.
	Dirty []RepoDirtyDiagnostics `yaml:"dirty,omitempty"`
	// ConflictFiles lists the files that conflicted during merge candidate
	// preparation.
	ConflictFiles []string `yaml:"conflict_files,omitempty"`
	// CleanupWarning records a non-fatal worktree/branch cleanup failure
	// for this repository.
	CleanupWarning string `yaml:"cleanup_warning,omitempty"`
	// TailWarning records a non-fatal review-feedback integration tail
	// failure for this repository (push, reply, or thread-resolution
	// failure). It is projected into the existing warnings list with a
	// distinguishing prefix; no API schema change.
	TailWarning string `yaml:"tail_warning,omitempty"`
	// Diagnostics is a human-readable summary of the per-repo attention
	// condition.
	Diagnostics string `yaml:"diagnostics,omitempty"`
	// GateCode is the stable, typed failure code recorded by the rebase
	// mechanical integration gate when it parks a behind repo at attention
	// before any candidate or ref is touched. Empty for non-gate attention.
	// See the GateCode* constants.
	GateCode string `yaml:"gate_code,omitempty"`
}

// Stable gate-failure codes recorded by integration gates before any
// candidate or ref is touched. They are distinct, machine-stable reason
// strings so review tooling can classify a parked child without parsing
// free-form diagnostics.
const (
	// GateCodeParentDrift: a parent branch tip moved from its creation-time
	// base — parent refs must not move while a pass runs, except through the
	// transaction itself.
	GateCodeParentDrift = "parent_ref_drift"
	// GateCodeNotAncestor: the persisted creation-time target commit is not
	// an ancestor of the child branch head — the child did not merge the
	// creation-time target.
	GateCodeNotAncestor = "rebase_gate_not_ancestor"
	// GateCodeMergeInProgress: a merge or rebase sequencer is underway in
	// the child worktree at gate time (MERGE_HEAD, rebase-merge, or
	// rebase-apply present).
	GateCodeMergeInProgress = "rebase_gate_merge_in_progress"
	// GateCodeConflictMarkers: literal conflict markers remain in one or more
	// tracked files of the child worktree, or the marker scan itself failed
	// (fail closed).
	GateCodeConflictMarkers = "rebase_gate_conflict_markers"
	// GateCodeMissingTargetSHA: a persisted behind-repo target lacks the
	// creation-time target SHA (e.g. an in-flight child created before the
	// gate landed). The child must be discarded and relaunched.
	GateCodeMissingTargetSHA = "rebase_gate_missing_target_sha"
)

// TransactionJournal is the ordered per-repository transaction record for
// all child integration. It preserves inherited repository order and durably
// records transaction identity, aggregate phase, and per-repository state.
type TransactionJournal struct {
	// Phase is the aggregate transaction phase.
	Phase TransactionPhase `yaml:"phase"`
	// Entries is the ordered per-repository journal, preserving the
	// inherited parent repository order.
	Entries []RepoTransactionEntry `yaml:"entries"`
	// Attention is a human-readable summary of the blocking condition when
	// the transaction is parked at attention.
	Attention string `yaml:"attention,omitempty"`
	// TailSettled is the durable marker that the review-feedback integration
	// tail has finished attempting all steps. The startup reconciler skips
	// settled tails entirely so historical children trigger no pushes, no
	// gh invocations, and no journal churn on later startups. Refactor
	// children never set this marker.
	TailSettled bool `yaml:"tail_settled,omitempty"`
}

// AllCandidatesPrepared reports whether every per-repo entry has a durable
// candidate SHA (PrepState == prepared and CandidateSHA non-empty).
func (t *TransactionJournal) AllCandidatesPrepared() bool {
	if t == nil || len(t.Entries) == 0 {
		return false
	}
	for i := range t.Entries {
		e := &t.Entries[i]
		if e.PrepState != RepoPrepPrepared || e.CandidateSHA == "" {
			return false
		}
	}
	return true
}

// AllApplied reports whether every per-repo entry has ApplyState == applied.
func (t *TransactionJournal) AllApplied() bool {
	if t == nil || len(t.Entries) == 0 {
		return false
	}
	for i := range t.Entries {
		if t.Entries[i].ApplyState != RepoApplyApplied {
			return false
		}
	}
	return true
}

// AnyApplied reports whether at least one per-repo entry has been applied.
func (t *TransactionJournal) AnyApplied() bool {
	if t == nil {
		return false
	}
	for i := range t.Entries {
		if t.Entries[i].ApplyState == RepoApplyApplied {
			return true
		}
	}
	return false
}

// AnyApplyAttention reports whether at least one per-repo entry has
// ApplyState == attention, indicating an external race or unclassifiable
// state that must persist as durable integration attention.
func (t *TransactionJournal) AnyApplyAttention() bool {
	if t == nil {
		return false
	}
	for i := range t.Entries {
		if t.Entries[i].ApplyState == RepoApplyAttention {
			return true
		}
	}
	return false
}

// EntryByRepo returns a pointer to the entry for the named repository, or nil.
func (t *TransactionJournal) EntryByRepo(repoName string) *RepoTransactionEntry {
	if t == nil {
		return nil
	}
	for i := range t.Entries {
		if t.Entries[i].Repo == repoName {
			return &t.Entries[i]
		}
	}
	return nil
}
