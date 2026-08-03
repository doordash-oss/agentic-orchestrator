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
	"os"
	"path/filepath"
	"testing"
)

func TestStoreLoadAddressedReviewFeedbackIDs(t *testing.T) {
	store := NewStore(t.TempDir())

	seedAddressedReviewFeedbackIDs(t, store, "parent-1", "api", `[11,33]`)
	seedAddressedReviewFeedbackIDs(t, store, "parent-1", "web", `[22]`)
	seedAddressedReviewFeedbackIDs(t, store, "parent-2", "api", `[44]`)

	got, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() error = %v", err)
	}
	if len(got) != 2 || !got[11] || !got[33] {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() = %v, want IDs 11 and 33", got)
	}
	if got[22] || got[44] {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() leaked IDs from another repo or parent: %v", got)
	}
}

func TestStoreLoadAddressedReviewFeedbackIDsMissingLedgerIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())

	got, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() = %v, want empty set", got)
	}
}

func TestStoreLoadAddressedReviewFeedbackIDsRejectsMalformedLedger(t *testing.T) {
	store := NewStore(t.TempDir())
	seedAddressedReviewFeedbackIDs(t, store, "parent-1", "api", `{not-json}`)

	if _, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api"); err == nil {
		t.Fatal("LoadAddressedReviewFeedbackIDs() error = nil, want malformed ledger error")
	}
}

func seedAddressedReviewFeedbackIDs(t *testing.T, store *Store, parentID, repoName, body string) {
	t.Helper()
	dir := filepath.Join(store.BaseDir, parentID, "review-feedback", repoName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addressed-ids.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(addressed IDs): %v", err)
	}
}

func TestStoreAppendAddressedReviewFeedbackIDsCreatesStoreOnFirstWrite(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 22}); err != nil {
		t.Fatalf("AppendAddressedReviewFeedbackIDs() error = %v", err)
	}

	got, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() error = %v", err)
	}
	if len(got) != 2 || !got[11] || !got[22] {
		t.Fatalf("after append: got = %v, want IDs 11 and 22", got)
	}
}

func TestStoreAppendAddressedReviewFeedbackIDsDeduplicates(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 22}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 33}); err != nil {
		t.Fatalf("second append: %v", err)
	}

	got, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() error = %v", err)
	}
	if len(got) != 3 || !got[11] || !got[22] || !got[33] {
		t.Fatalf("after dedup: got = %v, want IDs 11, 22, 33", got)
	}
}

func TestStoreAppendAddressedReviewFeedbackIDsRepeatIsNoop(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 22}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 22}); err != nil {
		t.Fatalf("second append (repeat): %v", err)
	}

	got, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("LoadAddressedReviewFeedbackIDs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("after repeat append: got = %v, want exactly IDs 11 and 22 (no duplicates)", got)
	}
}

func TestStoreAppendAddressedReviewFeedbackIDsIsolatedByRepo(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11}); err != nil {
		t.Fatalf("append api: %v", err)
	}
	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "web", []int{22}); err != nil {
		t.Fatalf("append web: %v", err)
	}

	gotAPI, _ := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if gotAPI[22] || !gotAPI[11] {
		t.Fatalf("api ledger = %v, want only ID 11", gotAPI)
	}
	gotWeb, _ := store.LoadAddressedReviewFeedbackIDs("parent-1", "web")
	if gotWeb[11] || !gotWeb[22] {
		t.Fatalf("web ledger = %v, want only ID 22", gotWeb)
	}
}

func TestStoreAppendAddressedReviewFeedbackIDsIsolatedByParent(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11}); err != nil {
		t.Fatalf("append parent-1: %v", err)
	}
	if err := store.AppendAddressedReviewFeedbackIDs("parent-2", "api", []int{22}); err != nil {
		t.Fatalf("append parent-2: %v", err)
	}

	got1, _ := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if got1[22] || !got1[11] {
		t.Fatalf("parent-1 api ledger = %v, want only ID 11", got1)
	}
	got2, _ := store.LoadAddressedReviewFeedbackIDs("parent-2", "api")
	if got2[11] || !got2[22] {
		t.Fatalf("parent-2 api ledger = %v, want only ID 22", got2)
	}
}

func TestStoreAppendThenLoadExcludesAddressedIDs(t *testing.T) {
	store := NewStore(t.TempDir())

	if err := store.AppendAddressedReviewFeedbackIDs("parent-1", "api", []int{11, 33}); err != nil {
		t.Fatalf("append: %v", err)
	}

	addressed, err := store.LoadAddressedReviewFeedbackIDs("parent-1", "api")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !addressed[11] || !addressed[33] {
		t.Fatalf("addressed = %v, want 11 and 33", addressed)
	}
	if addressed[22] {
		t.Fatalf("addressed = %v, 22 should not be addressed", addressed)
	}
}
