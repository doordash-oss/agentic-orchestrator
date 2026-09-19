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

// RepoTransactionRefKind records what a journal ref update does to its ref:
// rewrite an existing ref the transaction verified at its anchor, create a
// ref that did not exist before the transaction, or delete a ref the
// transaction verified at its anchor.
type RepoTransactionRefKind string

const (
	// RepoRefKindRewrite moves an existing ref from its anchor to its
	// candidate. It is the default: refs recorded before the kind existed
	// load as rewrite.
	RepoRefKindRewrite RepoTransactionRefKind = "rewrite"
	// RepoRefKindCreate creates a ref that must be absent before the
	// transaction. A created ref carries an empty anchor; classification
	// treats an absent observation the way a rewrite ref treats its anchor.
	RepoRefKindCreate RepoTransactionRefKind = "create"
	// RepoRefKindDelete deletes a ref that must sit at its anchor before
	// the transaction. A deleted ref carries an empty candidate; its
	// candidate is the ref's absence, so classification treats an absent
	// observation as at-candidate and the anchor SHA as at-anchor.
	RepoRefKindDelete RepoTransactionRefKind = "delete"
)

// RepoTransactionRef is one parent-layer ref update recorded on a
// repository's journal entry: the layer's branch name and stack position, the
// anchor SHA captured before the transaction staged anything, the candidate
// SHA the transaction plans to move the ref to (equal to the anchor for a
// pass-through ref), and the observed SHA read at apply, rollback, or
// recovery time to diagnose external races. The kind records whether the
// update rewrites an existing ref or creates one that did not exist before
// the transaction; a created ref has an empty anchor.
type RepoTransactionRef struct {
	// Branch is the parent layer's branch name the update targets.
	Branch string `yaml:"branch"`
	// Layer is the stack position of the layer owning the branch; 0 for the
	// single ref of a stackless parent.
	Layer int `yaml:"layer_position"`
	// Kind records whether the update rewrites an existing ref
	// (RepoRefKindRewrite, the default) or creates one that did not exist
	// before the transaction (RepoRefKindCreate).
	Kind RepoTransactionRefKind `yaml:"kind,omitempty"`
	// AnchorSHA is the full ref SHA captured immediately before preparation;
	// the ref must never regress past it outside the transaction. Empty by
	// convention for a created ref, whose anchor is absence.
	AnchorSHA string `yaml:"anchor_sha"`
	// CandidateSHA is the full SHA the transaction plans for the ref. For a
	// merge-candidate ref this is the staged merge commit; for a pass-through
	// ref it equals the anchor.
	CandidateSHA string `yaml:"candidate_sha,omitempty"`
	// ObservedSHA is the actual ref SHA observed at apply or rollback time.
	ObservedSHA string `yaml:"observed_sha,omitempty"`
}

// RefKind returns the ref's effective kind, defaulting an absent or empty
// kind to rewrite so journals recorded before the kind existed keep their
// legacy meaning on load.
func (r *RepoTransactionRef) RefKind() RepoTransactionRefKind {
	if r == nil || r.Kind == "" {
		return RepoRefKindRewrite
	}
	return r.Kind
}

// RefClassification classifies an observed ref SHA against a ref's recorded
// anchor and candidate.
type RefClassification int

const (
	// RefAtCandidate: the observed SHA equals the ref's candidate.
	RefAtCandidate RefClassification = iota
	// RefAtAnchor: the observed SHA equals the ref's anchor.
	RefAtAnchor
	// RefElsewhere: the observed SHA matches neither.
	RefElsewhere
)

// Classify classifies an observed ref state against the ref's recorded kind,
// anchor, and candidate. absent reports that the ref does not exist, which
// must be distinguished from an empty observation. For a rewrite ref,
// at-candidate comes first (a pass-through ref's candidate equals its
// anchor), then at-anchor, then elsewhere; an absent rewrite ref is elsewhere
// — it was deleted externally. For a created ref, absence plays the anchor's
// role: absent is at-anchor, the candidate is at-candidate, and anything
// else is elsewhere. For a deleted ref, absence plays the candidate's role:
// absent is at-candidate, the anchor is at-anchor, and anything else is
// elsewhere.
func (r *RepoTransactionRef) Classify(observedSHA string, absent bool) RefClassification {
	if r.RefKind() == RepoRefKindCreate {
		switch {
		case absent:
			return RefAtAnchor
		case r.CandidateSHA != "" && observedSHA == r.CandidateSHA:
			return RefAtCandidate
		default:
			return RefElsewhere
		}
	}
	if r.RefKind() == RepoRefKindDelete {
		switch {
		case absent:
			return RefAtCandidate
		case observedSHA == r.AnchorSHA:
			return RefAtAnchor
		default:
			return RefElsewhere
		}
	}
	switch {
	case absent:
		return RefElsewhere
	case r.CandidateSHA != "" && observedSHA == r.CandidateSHA:
		return RefAtCandidate
	case observedSHA == r.AnchorSHA:
		return RefAtAnchor
	default:
		return RefElsewhere
	}
}

// RepoTransactionPreviousTop records the repository's top layer branch,
// stack position, and tip immediately before a transaction that appends
// layers: the branch the parent worktree must have checked out at apply, and
// the branch and tip that rollback and discard restore.
type RepoTransactionPreviousTop struct {
	// Branch is the previous top layer's branch name.
	Branch string `yaml:"branch"`
	// Layer is the previous top layer's stack position.
	Layer int `yaml:"layer_position"`
	// TipSHA is the previous top layer's tip SHA captured at preparation.
	TipSHA string `yaml:"tip_sha"`
}

// RepoTransactionEntry is the per-repository journal entry in a transaction.
// It preserves inherited repository order and durably records the per-repo
// ordered list of parent-layer ref updates (branch, stack position, kind,
// anchor, candidate, observed), the repository's previous top, the child
// head, preparation state, apply/rollback state, the anchor/tip remap to
// persist on the parent run at closure, and the map from child commit SHA to
// relocated SHA. Parking conditions live on the journal's single attention
// record, not on entries; entries carry progress state, the two optional
// stored warning records, and the typed pending-sync flag only.
type RepoTransactionEntry struct {
	// Repo is the repository name, matching the inherited parent repo.
	Repo string `yaml:"repo"`
	// Refs is the ordered list of parent-layer refs this entry's transaction
	// rewrites, ascending by layer position. The highest-position ref is the
	// top ref: the one the parent worktree must have checked out and the one
	// the worktree sync resets to. Refactor and rebase children record a
	// one-ref list naming the parent's checked-out branch; review-feedback
	// children record one ref per parent layer at or above the lowest changed
	// layer.
	Refs []RepoTransactionRef `yaml:"refs"`
	// PreviousTop records the repository's top layer (branch, position, and
	// tip) immediately before the transaction. Entries that append layers
	// carry it; rebase, review-feedback, and journals recorded before it
	// existed leave it nil.
	PreviousTop *RepoTransactionPreviousTop `yaml:"previous_top,omitempty"`
	// ChildHeadSHA is the full child HEAD after committing every remaining
	// child change; recorded before any parent branch is touched.
	ChildHeadSHA string `yaml:"child_head_sha"`
	// PrepState records whether the candidate is pending, prepared, or failed.
	PrepState RepoPrepState `yaml:"prep_state,omitempty"`
	// ApplyState records the per-repo apply or rollback outcome.
	ApplyState RepoApplyState `yaml:"apply_state,omitempty"`
	// Remap is the roadmap-phase anchor and layer tip remap to persist on the
	// parent run once closure confirms every ref at its candidate. Absolute
	// SHAs keyed by phase and position, so persisting is idempotent and a
	// crash before it is repaired by the existing merged-phase re-entry.
	Remap RestackRemap `yaml:"remap,omitempty"`
	// Relocated maps every child commit SHA to the SHA it was relocated to in
	// the parent chain; empty for merge-candidate preparation.
	Relocated map[string]string `yaml:"relocated,omitempty"`
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

// AppendedLayer is one layer definition the journal records at the feature
// level so closure and startup recovery can persist appended layers onto the
// parent run without the child record: the layer's position, title, slug,
// branch, and origin.
type AppendedLayer struct {
	// Position is the appended layer's stack position on the parent.
	Position int `yaml:"position"`
	// Title is the appended layer's roadmap row title.
	Title string `yaml:"title,omitempty"`
	// Slug is the appended layer's slug.
	Slug string `yaml:"slug,omitempty"`
	// Branch is the appended layer's branch name on the parent.
	Branch string `yaml:"branch,omitempty"`
	// Origin records the child feature and layer position the appended layer
	// came from.
	Origin *StackLayerOrigin `yaml:"origin,omitempty"`
}

// TransactionJournal is the ordered per-repository transaction record for
// all child integration. It preserves inherited repository order and durably
// records transaction identity, aggregate phase, per-repository state, the
// feature-level appended layer definitions, and — when integration parks —
// the single canonical attention record.
type TransactionJournal struct {
	// Phase is the aggregate transaction phase.
	Phase TransactionPhase `yaml:"phase"`
	// Entries is the ordered per-repository journal, preserving the
	// inherited parent repository order.
	Entries []RepoTransactionEntry `yaml:"entries"`
	// AppendedLayers records the appended layer definitions the transaction
	// plans to persist onto the parent run once closure confirms every ref
	// at its candidate; a crash between apply and closure is finished by the
	// startup scan through this list.
	AppendedLayers []AppendedLayer `yaml:"appended_layers,omitempty"`
	// Attention is the single stored canonical record classifying the parked
	// integration condition: a needs_action catalog code, the repositories
	// context block listing every affected repository with its branch,
	// conflict files, dirty files, and SHAs, and raw diagnostics. Rendered
	// text is never persisted; the catalog stays authoritative.
	Attention *errcat.FailureRecord `yaml:"attention,omitempty"`
	// TailSettled is the durable marker that every step of the review-feedback
	// integration tail succeeded. The startup reconciler skips settled tails
	// entirely so historical children trigger no pushes, no gh invocations,
	// and no journal churn on later startups; an unsettled tail is retried.
	// Refactor children never set this marker.
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
		Phase          TransactionPhase       `yaml:"phase"`
		Entries        []RepoTransactionEntry `yaml:"entries"`
		Attention      yaml.Node              `yaml:"attention"`
		TailSettled    bool                   `yaml:"tail_settled"`
		AppendedLayers []AppendedLayer        `yaml:"appended_layers"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	t.Phase = raw.Phase
	t.Entries = raw.Entries
	t.TailSettled = raw.TailSettled
	t.AppendedLayers = raw.AppendedLayers
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
// candidate (PrepState == prepared and at least one ref carrying a
// candidate SHA).
func (t *TransactionJournal) AllCandidatesPrepared() bool {
	if t == nil || len(t.Entries) == 0 {
		return false
	}
	for i := range t.Entries {
		e := &t.Entries[i]
		if e.PrepState != RepoPrepPrepared || !e.HasCandidateRef() {
			return false
		}
	}
	return true
}

// HasCandidateRef reports whether the entry records at least one ref with a
// non-empty candidate SHA, or a delete ref — whose candidate is the ref's
// absence, so it always counts.
func (e *RepoTransactionEntry) HasCandidateRef() bool {
	if e == nil {
		return false
	}
	for i := range e.Refs {
		if e.Refs[i].CandidateSHA != "" || e.Refs[i].RefKind() == RepoRefKindDelete {
			return true
		}
	}
	return false
}

// TopRef returns the entry's highest-position ref — the parent worktree's
// checked-out layer branch and the sync target — or nil when the entry
// records no refs.
// TopRef returns the entry's top ref: the highest-position ref the parent
// worktree must have checked out and the one the worktree sync resets to.
// Deleted refs never qualify — a deleted branch can never be the worktree's
// post-transaction target — so an entry whose top layer is dropped resolves
// to the highest kept layer's rewrite ref. An entry of only deleted refs
// returns nil.
func (e *RepoTransactionEntry) TopRef() *RepoTransactionRef {
	if e == nil || len(e.Refs) == 0 {
		return nil
	}
	top := (*RepoTransactionRef)(nil)
	for i := range e.Refs {
		if e.Refs[i].RefKind() == RepoRefKindDelete {
			continue
		}
		if top == nil || e.Refs[i].Layer > top.Layer {
			top = &e.Refs[i]
		}
	}
	return top
}

// IsPassThrough reports whether every ref of the entry has its candidate
// equal to its anchor — a repository the transaction rewrites nothing for;
// apply only syncs its worktree. An entry containing any created ref is
// never pass-through: creating a ref always changes the repository's stack
// shape. Neither is an entry containing any deleted ref: deleting a ref
// always changes it too.
func (e *RepoTransactionEntry) IsPassThrough() bool {
	if e == nil || len(e.Refs) == 0 {
		return false
	}
	for i := range e.Refs {
		if e.Refs[i].RefKind() == RepoRefKindCreate {
			return false
		}
		if e.Refs[i].RefKind() == RepoRefKindDelete {
			return false
		}
		if e.Refs[i].CandidateSHA != e.Refs[i].AnchorSHA {
			return false
		}
	}
	return true
}

// PreviousTopRef returns the entry's recorded previous top — the branch,
// position, and tip the parent worktree must restore on rollback or discard —
// or nil when the entry carries no record.
func (e *RepoTransactionEntry) PreviousTopRef() *RepoTransactionPreviousTop {
	if e == nil {
		return nil
	}
	return e.PreviousTop
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
