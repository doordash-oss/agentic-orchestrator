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
	"gopkg.in/yaml.v3"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

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
// preparation state, and apply/rollback state. Parking conditions live on the
// journal's single attention record, not on entries; entries carry progress
// state, the two optional stored warning records, and the typed pending-sync
// flag only.
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
	// PendingSync marks an applied entry whose parent worktree sync failed
	// after the ref update; closure retries the sync automatically and clears
	// the flag on success. It carries no attention record.
	PendingSync bool `yaml:"pending_sync,omitempty"`
	// Cleanup is the optional stored canonical warning record for this
	// repository's cleanup after a child pass: the
	// child_cleanup_incomplete catalog code, the repositories context
	// block, and the raw cause as diagnostics. Nil means cleanup finished
	// cleanly; a successful retry clears it back to nil.
	Cleanup *errcat.FailureRecord `yaml:"cleanup,omitempty"`
	// Tail is the optional stored canonical warning record for this
	// repository's review-feedback integration tail failures: the
	// review_feedback_tail_incomplete catalog code, the repositories
	// context block, and one raw diagnostics line per failure. Nil means
	// no tail failure was recorded.
	Tail *errcat.FailureRecord `yaml:"tail,omitempty"`
}

// TransactionJournal is the ordered per-repository transaction record for
// all child integration. It preserves inherited repository order and durably
// records transaction identity, aggregate phase, per-repository state, and —
// when integration parks — the single canonical attention record.
type TransactionJournal struct {
	// Phase is the aggregate transaction phase.
	Phase TransactionPhase `yaml:"phase"`
	// Entries is the ordered per-repository journal, preserving the
	// inherited parent repository order.
	Entries []RepoTransactionEntry `yaml:"entries"`
	// Attention is the single stored canonical record classifying the parked
	// integration condition: a needs_action catalog code, the repositories
	// context block listing every affected repository with its branch,
	// conflict files, dirty files, and SHAs, and raw diagnostics. Rendered
	// text is never persisted; the catalog stays authoritative.
	Attention *errcat.FailureRecord `yaml:"attention,omitempty"`
	// TailSettled is the durable marker that the review-feedback integration
	// tail has finished attempting all steps. The startup reconciler skips
	// settled tails entirely so historical children trigger no pushes, no
	// gh invocations, and no journal churn on later startups. Refactor
	// children never set this marker.
	TailSettled bool `yaml:"tail_settled,omitempty"`
}

// UnmarshalYAML tolerates legacy journals: a free-form attention string (the
// pre-catalog shape) and deleted entry keys (diagnostics, gate codes,
// conflict files, dirty diagnostics, and the pre-record cleanup_warning and
// tail_warning strings) are ignored on load and never written back. The
// non-strict decoder drops unknown entry keys; only the attention shape
// needs a guard.
func (t *TransactionJournal) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Phase       TransactionPhase       `yaml:"phase"`
		Entries     []RepoTransactionEntry `yaml:"entries"`
		Attention   yaml.Node              `yaml:"attention"`
		TailSettled bool                   `yaml:"tail_settled"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	t.Phase = raw.Phase
	t.Entries = raw.Entries
	t.TailSettled = raw.TailSettled
	if raw.Attention.Kind == yaml.MappingNode {
		var record errcat.FailureRecord
		if err := raw.Attention.Decode(&record); err != nil {
			return err
		}
		t.Attention = &record
	}
	return nil
}

// AttentionRecord returns the journal's stored canonical attention record,
// or nil when integration is not parked.
func (t *TransactionJournal) AttentionRecord() *errcat.FailureRecord {
	if t == nil {
		return nil
	}
	return t.Attention
}

// AttentionCode returns the record's catalog code, or "" when the journal
// carries no attention record.
func (t *TransactionJournal) AttentionCode() errcat.Code {
	if rec := t.AttentionRecord(); rec != nil {
		return rec.Code
	}
	return ""
}

// HasAttention reports whether the journal is parked at integration
// attention: the phase is attention, the journal carries an attention record
// (including a closure-time sync failure on an applied journal), or any
// entry carries apply attention.
func (t *TransactionJournal) HasAttention() bool {
	if t == nil {
		return false
	}
	return t.Phase == TransactionPhaseAttention || t.Attention != nil || t.AnyApplyAttention()
}

// IntegrationAttentionRecord returns the child's stored integration
// attention record, or nil when integration is not parked.
func (f *Feature) IntegrationAttentionRecord() *errcat.FailureRecord {
	if f == nil || f.Parent == nil {
		return nil
	}
	return f.Parent.Transaction.AttentionRecord()
}

// HasIntegrationAttention reports whether the child's integration
// transaction is parked at attention and needs the operator's action.
func (f *Feature) HasIntegrationAttention() bool {
	if f == nil || f.Parent == nil {
		return false
	}
	return f.Parent.Transaction.HasAttention()
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
