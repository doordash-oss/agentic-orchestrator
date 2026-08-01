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
	"time"
)

func TestBeginCascadeDeletePersistsCompleteStableManifest(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	stateDir := filepath.Join(t.TempDir(), "features")
	store := NewStore(stateDir)
	parent := &Feature{
		ID: "parent", Slug: "parent-slug", ActiveRun: 1, RunCount: 1,
		Repos: []FeatureRepo{{
			Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/parent/a",
			Branch: "feature/parent",
		}},
	}
	child := &Feature{
		ID: "child", Slug: "child-slug", ActiveRun: 1, RunCount: 1,
		Parent: &ChildRelationship{
			ParentID: "parent",
			Transaction: &TransactionJournal{Entries: []RepoTransactionEntry{{
				Repo: "repo-a", ParentBranch: "feature/parent",
				ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
				CandidateSHA: "candidate", ApplyState: RepoApplyApplied,
			}}},
		},
		Repos: []FeatureRepo{{
			Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/child/a",
			Branch: "feature/child",
		}},
	}
	child.SetRun(&Run{RunNumber: 1, Setup: &SetupState{Tasks: map[string]SetupTask{
		"attachment:1": {
			Key: "attachment:1", Kind: SetupTaskAttachment,
			Path: filepath.Join(stateDir, "child", "attachments", "notes.txt"),
		},
	}}})
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(child); err != nil {
		t.Fatal(err)
	}

	requestedAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	intent, err := store.BeginCascadeDelete("parent", requestedAt)
	if err != nil {
		t.Fatalf("BeginCascadeDelete: %v", err)
	}
	if intent.ParentID != "parent" || intent.RequestedAt != requestedAt ||
		intent.Status != CascadeDeleteCleanupPending {
		t.Fatalf("intent = %+v", intent)
	}
	if got, want := intent.ChildIDs, []string{"child"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("child ids = %v, want %v", got, want)
	}
	if len(intent.Resources) != 10 {
		t.Fatalf("resources = %d, want 10: %+v", len(intent.Resources), intent.Resources)
	}
	if len(intent.Refs) != 1 || intent.Refs[0].AnchorSHA != "anchor" ||
		intent.Refs[0].CandidateSHA != "candidate" {
		t.Fatalf("refs = %+v", intent.Refs)
	}

	reloaded, err := store.LoadCascadeDelete("parent")
	if err != nil {
		t.Fatalf("LoadCascadeDelete: %v", err)
	}
	if reloaded.OperationID != intent.OperationID || len(reloaded.Resources) != len(intent.Resources) {
		t.Fatalf("reloaded = %+v, initial = %+v", reloaded, intent)
	}

	again, err := store.BeginCascadeDelete("parent", requestedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("repeated BeginCascadeDelete: %v", err)
	}
	if again.OperationID != intent.OperationID || again.RequestedAt != requestedAt {
		t.Fatalf("repeated call created a new intent: first=%+v again=%+v", intent, again)
	}
}

func TestParentOverlayPathIsDeterministicAndRepositoryScoped(t *testing.T) {
	t.Parallel()
	// parallel-candidate: pure path construction has no shared state.

	stateDir := filepath.Join("/runtime", "features")
	got := ParentOverlayPath(stateDir, "parent-1", "repo-a")
	want := filepath.Join("/runtime", "overlays", "parent-1", "repo-a")
	if got != want {
		t.Fatalf("ParentOverlayPath = %q, want %q", got, want)
	}
}

func TestBeginCascadeDeleteFailsClosedOnInvalidRelationship(t *testing.T) {
	t.Parallel()
	// parallel-candidate: per-test temp dirs isolate persisted relationship state.

	store := NewStore(t.TempDir())
	parent := &Feature{ID: "parent", ActiveRun: 1, RunCount: 1}
	child := &Feature{
		ID:        "invalid-child",
		ActiveRun: 1,
		RunCount:  1,
		Parent: &ChildRelationship{
			ParentID:     parent.ID,
			CloseOutcome: ChildCloseOutcomeCompleted,
		},
	}
	for _, record := range []*Feature{parent, child} {
		if err := store.Save(record); err != nil {
			t.Fatalf("Save(%q) error = %v", record.ID, err)
		}
	}

	if _, err := store.BeginCascadeDelete(parent.ID, time.Now()); err == nil {
		t.Fatal("BeginCascadeDelete() error = nil, want invalid relationship error")
	}
	if _, err := store.LoadCascadeDelete(parent.ID); !os.IsNotExist(err) {
		t.Fatalf("LoadCascadeDelete() error = %v, want no persisted destructive intent", err)
	}
}
