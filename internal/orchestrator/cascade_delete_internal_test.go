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

package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type cascadeTestWorktrees struct {
	store       *feature.Store
	refs        map[string]string
	removeCalls int
	removeErr   error
}

func (w *cascadeTestWorktrees) Create(string, string, string, string) (string, error) {
	return "", nil
}
func (w *cascadeTestWorktrees) Remove(string, bool) error {
	w.removeCalls++
	if _, err := w.store.LoadCascadeDelete("parent"); err != nil {
		return errors.New("cleanup ran before durable intent")
	}
	return w.removeErr
}
func (w *cascadeTestWorktrees) RemoveRef(string, string, string) error {
	return w.Remove("", true)
}
func (*cascadeTestWorktrees) ExpectedPath(string, string) string    { return "" }
func (*cascadeTestWorktrees) ResetToBase(string, string) error      { return nil }
func (*cascadeTestWorktrees) ResetToBaseLocal(string, string) error { return nil }
func (*cascadeTestWorktrees) ResetToCommit(string, string) error    { return nil }
func (*cascadeTestWorktrees) CurrentHeadSHA(string) (string, error) { return "", nil }
func (*cascadeTestWorktrees) CurrentBranch(string) string           { return "" }
func (w *cascadeTestWorktrees) RefSHA(_ string, ref string) (string, error) {
	return w.refs[ref], nil
}
func (w *cascadeTestWorktrees) UpdateRef(_ string, ref, oldSHA, newSHA string) error {
	if w.refs[ref] != oldSHA {
		return errors.New("ref moved")
	}
	w.refs[ref] = newSHA
	return nil
}
func (*cascadeTestWorktrees) CreateMergeCandidate(string, string, string, string) (*git.MergeCandidateResult, error) {
	return nil, nil
}
func (*cascadeTestWorktrees) InspectCleanliness(string, int) (*git.CleanlinessReport, error) {
	return &git.CleanlinessReport{}, nil
}

func TestDeleteCascadePreservesExternallyMovedRefAndRecords(t *testing.T) {
	t.Parallel()

	store, parent, child := saveCascadeTestRelationship(t)
	worktrees := &cascadeTestWorktrees{
		store: store,
		refs:  map[string]string{"refs/heads/feature/parent": "external"},
	}
	o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

	result, err := o.DeleteCascade(parent.ID)
	if err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	if result.Status != feature.CascadeDeleteAttentionRequired {
		t.Fatalf("status = %q, want attention_required", result.Status)
	}
	if len(result.Diagnostics) != 1 ||
		result.Diagnostics[0].Code != "external_ref_moved" ||
		result.Diagnostics[0].ObservedSHA != "external" ||
		result.Diagnostics[0].AnchorSHA != "anchor" ||
		result.Diagnostics[0].CandidateSHA != "candidate" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if worktrees.removeCalls != 0 {
		t.Fatalf("cleanup calls = %d, want 0", worktrees.removeCalls)
	}
	if _, err := store.Load(parent.ID); err != nil {
		t.Fatalf("parent deleted: %v", err)
	}
	if _, err := store.Load(child.ID); err != nil {
		t.Fatalf("child deleted: %v", err)
	}
	intent, err := store.LoadCascadeDelete(parent.ID)
	if err != nil {
		t.Fatalf("durable intent: %v", err)
	}
	if intent.Status != feature.CascadeDeleteAttentionRequired {
		t.Fatalf("durable status = %q", intent.Status)
	}
}

func TestReconcileCascadeDeletesEmitsChangedNonTerminalRelationshipState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		refs       map[string]string
		removeErr  error
		wantStatus feature.CascadeDeleteStatus
	}{
		{
			name:       "attention required",
			refs:       map[string]string{"refs/heads/feature/parent": "external"},
			wantStatus: feature.CascadeDeleteAttentionRequired,
		},
		{
			name:       "cleanup pending",
			refs:       map[string]string{"refs/heads/feature/parent": "candidate"},
			removeErr:  errors.New("device busy"),
			wantStatus: feature.CascadeDeleteCleanupPending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, parent, child := saveCascadeTestRelationship(t)
			if _, err := store.BeginCascadeDelete(parent.ID, time.Now()); err != nil {
				t.Fatalf("BeginCascadeDelete() error = %v", err)
			}
			worktrees := &cascadeTestWorktrees{
				store: store, refs: tt.refs, removeErr: tt.removeErr,
			}
			o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

			if err := o.ReconcileCascadeDeletes(); err != nil {
				t.Fatalf("ReconcileCascadeDeletes() error = %v", err)
			}
			ev := receiveCascadeEvent(t, o.Events())
			if ev.Type != ports.RelationshipCascadeProgress ||
				ev.FeatureID != parent.ID ||
				ev.ParentID != parent.ID ||
				ev.ChildID != child.ID {
				t.Fatalf("cascade recovery event = %+v, want correlated relationship progress", ev)
			}
			intent, err := store.LoadCascadeDelete(parent.ID)
			if err != nil {
				t.Fatalf("LoadCascadeDelete() error = %v", err)
			}
			if intent.Status != tt.wantStatus {
				t.Fatalf("cascade status = %q, want %q", intent.Status, tt.wantStatus)
			}
			if _, err := store.Load(parent.ID); err != nil {
				t.Fatalf("Load(parent) error = %v, want retained record", err)
			}
			if _, err := store.Load(child.ID); err != nil {
				t.Fatalf("Load(child) error = %v, want retained record", err)
			}

			if err := o.ReconcileCascadeDeletes(); err != nil {
				t.Fatalf("second ReconcileCascadeDeletes() error = %v", err)
			}
			select {
			case ev := <-o.Events():
				t.Fatalf("second reconciliation event = %+v, want replayed no-op", ev)
			default:
			}
		})
	}
}

func TestDeleteCascadeCleansChildrenThenParentAndConverges(t *testing.T) {
	t.Parallel()

	store, parent, child := saveCascadeTestRelationship(t)
	overlay := feature.ParentOverlayPath(store.BaseDir, parent.ID, "repo-a")
	if err := os.MkdirAll(overlay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "seed.txt"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	worktrees := &cascadeTestWorktrees{
		store: store,
		refs:  map[string]string{"refs/heads/feature/parent": "candidate"},
	}
	o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

	result, err := o.DeleteCascade(parent.ID)
	if err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	if result.Status != feature.CascadeDeleteCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if worktrees.refs["refs/heads/feature/parent"] != "anchor" {
		t.Fatalf("ref = %q, want anchor", worktrees.refs["refs/heads/feature/parent"])
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatalf("overlay still exists: %v", err)
	}
	if _, err := store.Load(child.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child load error = %v, want not exist", err)
	}
	if _, err := store.Load(parent.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent load error = %v, want not exist", err)
	}

	again, err := o.DeleteCascade(parent.ID)
	if err != nil || again.Status != feature.CascadeDeleteCompleted {
		t.Fatalf("repeated delete = %+v, %v", again, err)
	}
}

func TestDeleteCascadeRejectsClosedChildWithRelationshipConflict(t *testing.T) {
	t.Parallel()

	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	closedAt := time.Now()
	child := &feature.Feature{
		ID:            "closed-child",
		SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun:     1,
		RunCount:      1,
		Parent: &feature.ChildRelationship{
			ParentID:     "parent",
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
			ClosedAt:     &closedAt,
		},
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	o := New(Deps{Store: store}, Hooks{})

	_, err := o.DeleteCascade(child.ID)
	if !errors.Is(err, feature.ErrChildRelationshipClosed) {
		t.Fatalf("DeleteCascade() error = %v, want ErrChildRelationshipClosed", err)
	}
}

func saveCascadeTestRelationship(t *testing.T) (*feature.Store, *feature.Feature, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(filepath.Join(t.TempDir(), "features"))
	parent := &feature.Feature{
		ID: "parent", Slug: "parent", SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{
			Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/parent/a",
			Branch: "feature/parent",
		}},
	}
	child := &feature.Feature{
		ID: "child", Slug: "child", SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
		Parent: &feature.ChildRelationship{
			ParentID: parent.ID,
			Transaction: &feature.TransactionJournal{Entries: []feature.RepoTransactionEntry{{
				Repo: "repo-a", ParentBranch: "feature/parent",
				ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
				CandidateSHA: "candidate", ApplyState: feature.RepoApplyApplied,
			}}},
		},
		Repos: []feature.FeatureRepo{{
			Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/child/a",
			Branch: "feature/child",
		}},
	}
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(child); err != nil {
		t.Fatal(err)
	}
	return store, parent, child
}

func receiveCascadeEvent(t *testing.T, events <-chan ports.Event) ports.Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	default:
		t.Fatal("cascade recovery emitted no relationship event")
		return ports.Event{}
	}
}
