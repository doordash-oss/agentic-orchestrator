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
)

// deleteRefJournalFixture builds a journal whose single entry lists two
// rewrite refs and one delete ref with an empty candidate — the durable
// shape a transaction that drops a parent layer records, with the deleted
// layer as the entry's previous top.
func deleteRefJournalFixture() *TransactionJournal {
	return &TransactionJournal{
		Phase: TransactionPhasePrepared,
		Entries: []RepoTransactionEntry{{
			Repo: "repo-a",
			Refs: []RepoTransactionRef{
				{Branch: "feature/parent-abc123/1-dropped", Layer: 1,
					Kind: RepoRefKindDelete, AnchorSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{Branch: "feature/parent-abc123/2-kept", Layer: 2,
					AnchorSHA:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					CandidateSHA: "cccccccccccccccccccccccccccccccccccccccc"},
				{Branch: "feature/parent-abc123/3-top", Layer: 3,
					AnchorSHA:    "dddddddddddddddddddddddddddddddddddddddd",
					CandidateSHA: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
			},
			ChildHeadSHA: "ffffffffffffffffffffffffffffffffffffffff",
			PrepState:    RepoPrepPrepared,
			PreviousTop: &RepoTransactionPreviousTop{
				Branch: "feature/parent-abc123/1-dropped",
				Layer:  1,
				TipSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		}},
	}
}

// TestTransactionJournalRoundTripsDeleteRefs pins the durable shape: a
// journal entry listing two rewrite refs and one delete ref with an empty
// candidate, plus a previous-top record, survives a store save/load cycle
// unchanged.
func TestTransactionJournalRoundTripsDeleteRefs(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "features"))
	journal := deleteRefJournalFixture()
	f := &Feature{
		ID: "t3", Slug: "t3", Status: StatusReviewPassed, Created: time.Now().UTC().Truncate(time.Second),
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
	if entry.Refs[0].RefKind() != RepoRefKindDelete {
		t.Fatalf("delete ref lost its kind: %+v", entry.Refs[0])
	}
	if entry.Refs[0].CandidateSHA != "" {
		t.Fatalf("delete ref must carry an empty candidate: %+v", entry.Refs[0])
	}
	if entry.Refs[1].RefKind() != RepoRefKindRewrite || entry.Refs[2].RefKind() != RepoRefKindRewrite {
		t.Fatalf("rewrite refs lost their kind: %+v", entry.Refs)
	}
}

// TestClassifyDeleteRefs pins the classification contract for the delete
// kind: absence plays the candidate's role — an absent observation is
// at-candidate, the anchor SHA is at-anchor, and anything else is elsewhere.
func TestClassifyDeleteRefs(t *testing.T) {
	deleted := RepoTransactionRef{
		Branch:    "feature/parent-abc123/1-dropped",
		Layer:     1,
		Kind:      RepoRefKindDelete,
		AnchorSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if got := deleted.Classify("", true); got != RefAtCandidate {
		t.Fatalf("delete ref absent classified %v, want RefAtCandidate", got)
	}
	if got := deleted.Classify("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false); got != RefAtAnchor {
		t.Fatalf("delete ref at its anchor classified %v, want RefAtAnchor", got)
	}
	if got := deleted.Classify("dddddddddddddddddddddddddddddddddddddddd", false); got != RefElsewhere {
		t.Fatalf("delete ref elsewhere classified %v, want RefElsewhere", got)
	}
	if got := deleted.Classify("", false); got != RefElsewhere {
		t.Fatalf("delete ref with an empty but present observation classified %v, want RefElsewhere", got)
	}
}

// TestDeleteRefsNeverPassThrough pins the pass-through and candidate
// contracts: an entry containing any delete ref is never pass-through —
// deleting a ref always changes the repository's stack shape — and an entry
// whose refs are all deletes still counts as having candidates, because a
// delete ref's candidate is the ref's absence.
func TestDeleteRefsNeverPassThrough(t *testing.T) {
	withDeleted := &RepoTransactionEntry{
		Repo: "repo-a",
		Refs: []RepoTransactionRef{
			{Branch: "b1", Layer: 1, AnchorSHA: "aaa", CandidateSHA: "aaa"},
			{Branch: "b2", Layer: 2, Kind: RepoRefKindDelete, AnchorSHA: "bbb"},
		},
	}
	if withDeleted.IsPassThrough() {
		t.Fatal("entry with a delete ref must never be pass-through")
	}
	allDeletes := &RepoTransactionEntry{
		Repo: "repo-a",
		Refs: []RepoTransactionRef{
			{Branch: "b1", Layer: 1, Kind: RepoRefKindDelete, AnchorSHA: "aaa"},
			{Branch: "b2", Layer: 2, Kind: RepoRefKindDelete, AnchorSHA: "bbb"},
		},
	}
	if allDeletes.IsPassThrough() {
		t.Fatal("all-delete entry must never be pass-through")
	}
	if !allDeletes.HasCandidateRef() {
		t.Fatal("all-delete entry must count as having candidates (absence is the candidate)")
	}
}
