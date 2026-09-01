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
	"testing"
)

func draftParent() *Feature {
	return &Feature{
		ID: "parent-1",
		Repos: []FeatureRepo{
			{Name: "api"},
			{Name: "docs"},
			{Name: "web"},
		},
	}
}

func draftComments() map[string][]ReviewFeedbackComment {
	return map[string][]ReviewFeedbackComment{
		"api": {
			{Repo: "api", ID: 12, Type: "issue", Body: "later", CreatedAt: "2026-08-02T09:00:00Z"},
			{Repo: "api", ID: 11, Type: "review", Body: "earlier", CreatedAt: "2026-08-02T08:00:00Z"},
		},
		"web": {
			{Repo: "web", ID: 21, Type: "review_body", Body: "review", CreatedAt: "2026-08-02T08:30:00Z"},
		},
	}
}

func TestStableReviewFeedbackRefValidation(t *testing.T) {
	t.Parallel()

	ref, err := NewStableReviewFeedbackRef("api", "review", 11)
	if err != nil {
		t.Fatalf("NewStableReviewFeedbackRef() error = %v", err)
	}
	repo, commentType, id, err := ParseStableReviewFeedbackRef(string(ref))
	if err != nil || repo != "api" || commentType != "review" || id != 11 {
		t.Fatalf("ParseStableReviewFeedbackRef() = %q,%q,%d,%v", repo, commentType, id, err)
	}

	for _, bad := range []string{
		"",
		"api:review",
		"api:review:11:extra",
		"api:unsupported:11",
		"api:review:0",
		"api:review:not-a-number",
		"bad:repo:review:11",
		":review:11",
	} {
		if _, _, _, err := ParseStableReviewFeedbackRef(bad); err == nil {
			t.Fatalf("ParseStableReviewFeedbackRef(%q) error = nil, want rejection", bad)
		}
	}

	if _, err := NewStableReviewFeedbackRef("api", "issue", -1); err == nil {
		t.Fatal("NewStableReviewFeedbackRef() accepted a negative ID")
	}
}

func TestReconcileFirstFetchSelectsEverything(t *testing.T) {
	t.Parallel()

	draft := ReconcileReviewFeedbackDraft(draftParent(), nil, draftComments())

	if draft.Revision != 1 {
		t.Fatalf("revision = %d, want 1", draft.Revision)
	}
	// Parent repository order wins: api (oldest first, ref tie-break), docs
	// empty, web.
	wantRefs := []StableReviewFeedbackRef{"api:review:11", "api:issue:12", "web:review_body:21"}
	if len(draft.Items) != len(wantRefs) {
		t.Fatalf("items = %+v, want %d", draft.Items, len(wantRefs))
	}
	for i, ref := range wantRefs {
		if draft.Items[i].StableRef != ref {
			t.Fatalf("items[%d] = %q, want %q", i, draft.Items[i].StableRef, ref)
		}
		if !draft.Items[i].Selected {
			t.Fatalf("items[%d] not selected on first fetch", i)
		}
	}
	if draft.SnapshotID == "" {
		t.Fatal("snapshot identity empty")
	}
}

func TestReconcileLaterFetchRetainsSelectsNewPrunesGone(t *testing.T) {
	t.Parallel()

	existing := ReconcileReviewFeedbackDraft(draftParent(), nil, draftComments())
	if err := ApplyReviewFeedbackSelection(existing, map[StableReviewFeedbackRef]bool{"api:review:11": false}); err != nil {
		t.Fatalf("ApplyReviewFeedbackSelection() error = %v", err)
	}

	// api:12 disappears, a new api comment 13 arrives, web:21 stays.
	fetched := map[string][]ReviewFeedbackComment{
		"api": {
			{Repo: "api", ID: 11, Type: "review", Body: "earlier", CreatedAt: "2026-08-02T08:00:00Z"},
			{Repo: "api", ID: 13, Type: "review", Body: "brand new", CreatedAt: "2026-08-02T10:00:00Z"},
		},
		"web": {
			{Repo: "web", ID: 21, Type: "review_body", Body: "review", CreatedAt: "2026-08-02T08:30:00Z"},
		},
	}
	draft := ReconcileReviewFeedbackDraft(draftParent(), existing, fetched)

	if draft.Revision != existing.Revision+1 {
		t.Fatalf("revision = %d, want %d", draft.Revision, existing.Revision+1)
	}
	want := map[StableReviewFeedbackRef]bool{
		"api:review:11":      false, // retained choice
		"api:review:13":      true,  // newly observed selects on
		"web:review_body:21": true,
	}
	if len(draft.Items) != len(want) {
		t.Fatalf("items = %+v, want prune of api:issue:12", draft.Items)
	}
	for _, item := range draft.Items {
		if selected, ok := want[item.StableRef]; !ok || selected != item.Selected {
			t.Fatalf("item %q selected=%v, want %v", item.StableRef, item.Selected, want[item.StableRef])
		}
	}
}

func TestApplyReviewFeedbackSelectionRejectsUnknownWithoutPartialApply(t *testing.T) {
	t.Parallel()

	draft := ReconcileReviewFeedbackDraft(draftParent(), nil, draftComments())
	err := ApplyReviewFeedbackSelection(draft, map[StableReviewFeedbackRef]bool{
		"api:review:11":     false,
		"api:review:999999": true,
	})
	if !errors.Is(err, ErrReviewFeedbackUnknownReference) {
		t.Fatalf("ApplyReviewFeedbackSelection() error = %v, want unknown reference", err)
	}
	for _, item := range draft.Items {
		if !item.Selected {
			t.Fatalf("item %q modified by a partially applied batch", item.StableRef)
		}
	}
}

func TestStoreReviewFeedbackDraftRoundTripAndCAS(t *testing.T) {
	store := NewStore(t.TempDir())

	if got, err := store.LoadReviewFeedbackDraft("parent-1"); err != nil || got != nil {
		t.Fatalf("LoadReviewFeedbackDraft() = %+v (err %v), want nil draft", got, err)
	}

	draft := ReconcileReviewFeedbackDraft(draftParent(), nil, draftComments())
	if err := store.SaveReviewFeedbackDraft("parent-1", draft, 0); err != nil {
		t.Fatalf("SaveReviewFeedbackDraft() error = %v", err)
	}

	got, err := store.LoadReviewFeedbackDraft("parent-1")
	if err != nil {
		t.Fatalf("LoadReviewFeedbackDraft() error = %v", err)
	}
	if got.Revision != 1 || got.SnapshotID != draft.SnapshotID || len(got.Items) != 3 {
		t.Fatalf("loaded draft = %+v", got)
	}

	// Correct expected revision saves; a stale one conflicts.
	if err := ApplyReviewFeedbackSelection(got, map[StableReviewFeedbackRef]bool{"web:review_body:21": false}); err != nil {
		t.Fatal(err)
	}
	got.Revision = 2
	if err := store.SaveReviewFeedbackDraft("parent-1", got, 1); err != nil {
		t.Fatalf("SaveReviewFeedbackDraft(expected 1) error = %v", err)
	}
	var conflict *ReviewFeedbackRevisionConflictError
	if err := store.SaveReviewFeedbackDraft("parent-1", got, 7); !errors.As(err, &conflict) {
		t.Fatalf("stale save error = %v, want revision conflict", err)
	}

	// Drafts isolate by parent.
	if got, err := store.LoadReviewFeedbackDraft("parent-2"); err != nil || got != nil {
		t.Fatalf("parent-2 draft = %+v, want isolation", got)
	}
	// The addressed ledger is untouched by draft operations.
	if addressed, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api"); err != nil || len(addressed) != 0 {
		t.Fatalf("addressed ledger = %v, want unchanged", addressed)
	}

	if err := store.DeleteReviewFeedbackDraft("parent-1"); err != nil {
		t.Fatalf("DeleteReviewFeedbackDraft() error = %v", err)
	}
	if got, err := store.LoadReviewFeedbackDraft("parent-1"); err != nil || got != nil {
		t.Fatalf("draft after delete = %+v, want nil", got)
	}
	if err := store.DeleteReviewFeedbackDraft("parent-1"); err != nil {
		t.Fatalf("idempotent delete error = %v", err)
	}
}
