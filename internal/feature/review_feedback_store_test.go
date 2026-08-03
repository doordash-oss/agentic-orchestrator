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
