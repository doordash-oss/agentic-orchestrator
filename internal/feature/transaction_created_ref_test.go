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
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// createdRefJournalFixture builds a journal whose single entry lists one
// rewrite ref and two created refs with empty anchors, records the
// repository's previous top, and carries two appended layer definitions with
// origins — the durable shape refactor integration persists.
func createdRefJournalFixture() *TransactionJournal {
	return &TransactionJournal{
		Phase: TransactionPhasePrepared,
		Entries: []RepoTransactionEntry{{
			Repo: "repo-a",
			Refs: []RepoTransactionRef{
				{Branch: "feature/parent-abc123/2-top", Layer: 2,
					AnchorSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CandidateSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				{Branch: "feature/parent-abc123/3-first", Layer: 3,
					Kind: RepoRefKindCreate, CandidateSHA: "cccccccccccccccccccccccccccccccccccccccc"},
				{Branch: "feature/parent-abc123/4-second", Layer: 4,
					Kind: RepoRefKindCreate, CandidateSHA: "dddddddddddddddddddddddddddddddddddddddd"},
			},
			ChildHeadSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			PrepState:    RepoPrepPrepared,
			PreviousTop: &RepoTransactionPreviousTop{
				Branch: "feature/parent-abc123/2-top",
				Layer:  2,
				TipSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		}},
		AppendedLayers: []AppendedLayer{
			{Position: 3, Title: "First appended", Slug: "first-appended", Branch: "feature/parent-abc123/3-first",
				Origin: &StackLayerOrigin{SourceFeatureID: "child-001", SourceLayerPosition: 1}},
			{Position: 4, Title: "Second appended", Slug: "second-appended", Branch: "feature/parent-abc123/4-second",
				Origin: &StackLayerOrigin{SourceFeatureID: "child-001", SourceLayerPosition: 2}},
		},
	}
}

// TestTransactionJournalRoundTripsCreatedRefsAndAppendedLayers pins the
// durable shape: a journal entry listing one rewrite ref and two created refs
// with empty anchors, a previous-top record, and two appended layer
// definitions with origins survives a store save/load cycle unchanged.
func TestTransactionJournalRoundTripsCreatedRefsAndAppendedLayers(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "features"))
	journal := createdRefJournalFixture()
	f := &Feature{
		ID: "t2", Slug: "t2", Status: StatusReviewPassed, Created: time.Now().UTC().Truncate(time.Second),
		ActiveRun: 1, RunCount: 1, Pipeline: PipelineMedium,
		Repos: []FeatureRepo{{Name: "repo-a", Path: "/tmp/a"}},
		Parent: &ChildRelationship{
			ParentID:    "p1",
			Kind:        ChildKindRefactor,
			Transaction: journal,
		},
		SchemaVersion: SchemaVersionCurrent,
	}
	if err := store.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load(f.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := loaded.Parent.Transaction
	if got == nil {
		t.Fatal("loaded feature lost its transaction journal")
	}
	if !reflect.DeepEqual(got, journal) {
		t.Fatalf("journal did not round-trip unchanged:\ngot  %+v\nwant %+v", got, journal)
	}
	entry := got.Entries[0]
	if entry.Refs[1].RefKind() != RepoRefKindCreate || entry.Refs[2].RefKind() != RepoRefKindCreate {
		t.Fatalf("created refs lost their kind: %+v", entry.Refs)
	}
	if entry.Refs[1].AnchorSHA != "" || entry.Refs[2].AnchorSHA != "" {
		t.Fatalf("created refs must carry empty anchors: %+v", entry.Refs)
	}
	if entry.Refs[0].RefKind() != RepoRefKindRewrite {
		t.Fatalf("explicit rewrite ref lost its kind: %+v", entry.Refs[0])
	}
}

// TestTransactionJournalLegacyRefsLoadAsRewrite pins the legacy load
// contract: a journal written before the kind field existed (refs without a
// kind) still loads with every ref classified as rewrite.
func TestTransactionJournalLegacyRefsLoadAsRewrite(t *testing.T) {
	legacy := `phase: prepared
entries:
  - repo: repo-a
    refs:
      - branch: feature/parent-abc123/1-base
        layer_position: 1
        anchor_sha: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        candidate_sha: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    child_head_sha: cccccccccccccccccccccccccccccccccccccccc
    prep_state: prepared
`
	var got TransactionJournal
	if err := yaml.Unmarshal([]byte(legacy), &got); err != nil {
		t.Fatalf("legacy journal YAML must load: %v", err)
	}
	if len(got.Entries) != 1 || len(got.Entries[0].Refs) != 1 {
		t.Fatalf("legacy journal lost its entries: %+v", got)
	}
	ref := got.Entries[0].Refs[0]
	if ref.RefKind() != RepoRefKindRewrite {
		t.Fatalf("legacy ref kind = %q, want rewrite", ref.RefKind())
	}
}

// TestClassifyCreatedAndRewriteRefs pins the classification contract for the
// ref kind: a created ref treats an absent observation as at-anchor, its
// candidate as at-candidate, and anything else as elsewhere, while a rewrite
// ref with an absent observation is elsewhere — an absent rewrite ref was
// deleted externally — and keeps the at-candidate-first ordering otherwise.
func TestClassifyCreatedAndRewriteRefs(t *testing.T) {
	created := RepoTransactionRef{
		Branch: "feature/parent-abc123/3-first", Layer: 3,
		Kind:         RepoRefKindCreate,
		CandidateSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if got := created.Classify("", true); got != RefAtAnchor {
		t.Fatalf("created ref absent classified %v, want RefAtAnchor", got)
	}
	if got := created.Classify("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false); got != RefAtCandidate {
		t.Fatalf("created ref at its candidate classified %v, want RefAtCandidate", got)
	}
	if got := created.Classify("dddddddddddddddddddddddddddddddddddddddd", false); got != RefElsewhere {
		t.Fatalf("created ref elsewhere classified %v, want RefElsewhere", got)
	}
	if got := created.Classify("", false); got != RefElsewhere {
		t.Fatalf("created ref with an empty but present observation classified %v, want RefElsewhere", got)
	}

	rewrite := RepoTransactionRef{
		Branch: "feature/parent-abc123/2-top", Layer: 2,
		AnchorSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CandidateSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	if got := rewrite.Classify("", true); got != RefElsewhere {
		t.Fatalf("rewrite ref absent classified %v, want RefElsewhere", got)
	}
	if got := rewrite.Classify("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false); got != RefAtCandidate {
		t.Fatalf("rewrite ref at its candidate classified %v, want RefAtCandidate", got)
	}
	if got := rewrite.Classify("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false); got != RefAtAnchor {
		t.Fatalf("rewrite ref at its anchor classified %v, want RefAtAnchor", got)
	}
	if got := rewrite.Classify("dddddddddddddddddddddddddddddddddddddddd", false); got != RefElsewhere {
		t.Fatalf("rewrite ref elsewhere classified %v, want RefElsewhere", got)
	}

	// A pass-through rewrite ref's candidate equals its anchor; at-candidate
	// still wins so apply reports it as applied, not rolled back.
	passThrough := RepoTransactionRef{
		Branch: "feature/parent-abc123/1-base", Layer: 1,
		AnchorSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CandidateSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if got := passThrough.Classify("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false); got != RefAtCandidate {
		t.Fatalf("pass-through rewrite ref classified %v, want RefAtCandidate", got)
	}
}

// TestCreatedRefsNeverPassThrough pins the pass-through contract: an entry
// containing any created ref is never pass-through, while an all-rewrite
// entry whose candidates equal its anchors still is.
func TestCreatedRefsNeverPassThrough(t *testing.T) {
	withCreated := &RepoTransactionEntry{
		Repo: "repo-a",
		Refs: []RepoTransactionRef{
			{Branch: "b1", Layer: 1, AnchorSHA: "aaa", CandidateSHA: "aaa"},
			{Branch: "b2", Layer: 2, Kind: RepoRefKindCreate, CandidateSHA: "bbb"},
		},
	}
	if withCreated.IsPassThrough() {
		t.Fatal("entry with a created ref must never be pass-through")
	}
	allRewritePassThrough := &RepoTransactionEntry{
		Repo: "repo-a",
		Refs: []RepoTransactionRef{
			{Branch: "b1", Layer: 1, AnchorSHA: "aaa", CandidateSHA: "aaa"},
			{Branch: "b2", Layer: 2, AnchorSHA: "bbb", CandidateSHA: "bbb"},
		},
	}
	if !allRewritePassThrough.IsPassThrough() {
		t.Fatal("all-rewrite entry with candidates equal to anchors must stay pass-through")
	}
}

// TestPreviousTopRefAccessor pins the previous-top helper: it returns the
// recorded branch, position, and SHA, and nil for entries without a record
// (rebase, review-feedback, and pre-phase-11 journals).
func TestPreviousTopRefAccessor(t *testing.T) {
	withRecord := &RepoTransactionEntry{
		Repo: "repo-a",
		PreviousTop: &RepoTransactionPreviousTop{
			Branch: "feature/parent-abc123/2-top",
			Layer:  2,
			TipSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	top := withRecord.PreviousTopRef()
	if top == nil {
		t.Fatal("PreviousTopRef() = nil for an entry carrying a record")
	}
	if top.Branch != "feature/parent-abc123/2-top" || top.Layer != 2 ||
		top.TipSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("PreviousTopRef() = %+v, want the recorded previous top", top)
	}
	withoutRecord := &RepoTransactionEntry{Repo: "repo-a"}
	if got := withoutRecord.PreviousTopRef(); got != nil {
		t.Fatalf("PreviousTopRef() = %+v, want nil for an entry without a record", got)
	}
}
