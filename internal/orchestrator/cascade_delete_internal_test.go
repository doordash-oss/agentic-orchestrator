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
	ancestors   map[string][]string // descendant SHA -> its ancestors
	ancestorErr error
	removeCalls int
	updateCalls int
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
	w.updateCalls++
	if w.refs[ref] != oldSHA {
		return errors.New("ref moved")
	}
	w.refs[ref] = newSHA
	return nil
}
func (w *cascadeTestWorktrees) IsAncestor(_ string, ancestor, descendant string) (bool, error) {
	if w.ancestorErr != nil {
		return false, w.ancestorErr
	}
	if ancestor == descendant {
		return true, nil
	}
	for _, sha := range w.ancestors[descendant] {
		if sha == ancestor {
			return true, nil
		}
	}
	return false, nil
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

func TestReconcileCascadeDeletesIgnoresDeletedFeatureResidue(t *testing.T) {
	t.Parallel()

	store, _, _ := saveCascadeTestRelationship(t)
	residue := filepath.Join(store.BaseDir, "deleted-feature", "runs", "run-001")
	if err := os.MkdirAll(residue, 0o755); err != nil {
		t.Fatalf("mkdir deleted feature residue: %v", err)
	}
	o := New(Deps{Store: store, Worktrees: &cascadeTestWorktrees{store: store}}, Hooks{})

	if err := o.ReconcileCascadeDeletes(); err != nil {
		t.Fatalf("ReconcileCascadeDeletes() error = %v, want deleted residue ignored", err)
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

func TestDeleteCascadeHealsSymlinkSpelledJournalPaths(t *testing.T) {
	t.Parallel()

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "real-root")
	stateDir := filepath.Join(root, "features")
	alias := filepath.Join(tmp, "alias-root")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}

	store, parent, child, cleanupPaths := saveCascadeSymlinkFixture(t, stateDir)

	intent, err := store.BeginCascadeDelete(parent.ID, time.Now())
	if err != nil {
		t.Fatalf("BeginCascadeDelete: %v", err)
	}
	// Simulate a legacy journal recorded under the symlinked spelling.
	for i := range intent.Resources {
		r := &intent.Resources[i]
		switch r.Kind {
		case feature.CascadeResourceCopiedInput, feature.CascadeResourceOverlay,
			feature.CascadeResourceKBWorkspace, feature.CascadeResourcePromotion:
			rel, relErr := filepath.Rel(root, r.Path)
			if relErr != nil {
				t.Fatalf("Rel(%q, %q): %v", root, r.Path, relErr)
			}
			r.Path = filepath.Join(alias, rel)
		}
	}
	if err := store.SaveCascadeDelete(parent.ID, intent); err != nil {
		t.Fatalf("SaveCascadeDelete: %v", err)
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
		t.Fatalf("status = %q diagnostics = %+v, want completed", result.Status, result.Diagnostics)
	}
	for _, path := range cleanupPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
	if _, err := store.Load(child.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child load error = %v, want not exist", err)
	}
	if _, err := store.Load(parent.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent load error = %v, want not exist", err)
	}
}

func TestDeleteCascadeHealsBrokenSymlinkSpelledCopiedInput(t *testing.T) {
	t.Parallel()

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(tmp, "real-root")
	stateDir := filepath.Join(root, "features")
	alias := filepath.Join(tmp, "alias-root")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}

	store, parent, _, cleanupPaths := saveCascadeSymlinkFixture(t, stateDir)

	intent, err := store.BeginCascadeDelete(parent.ID, time.Now())
	if err != nil {
		t.Fatalf("BeginCascadeDelete: %v", err)
	}
	for i := range intent.Resources {
		r := &intent.Resources[i]
		if r.Kind != feature.CascadeResourceCopiedInput {
			continue
		}
		rel, relErr := filepath.Rel(root, r.Path)
		if relErr != nil {
			t.Fatalf("Rel(%q, %q): %v", root, r.Path, relErr)
		}
		r.Path = filepath.Join(alias, rel)
	}
	if err := store.SaveCascadeDelete(parent.ID, intent); err != nil {
		t.Fatalf("SaveCascadeDelete: %v", err)
	}
	// The old spelling no longer resolves; only the re-derived path exists.
	if err := os.Remove(alias); err != nil {
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
		t.Fatalf("status = %q diagnostics = %+v, want completed", result.Status, result.Diagnostics)
	}
	for _, path := range cleanupPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists: %v", path, err)
		}
	}
}

func TestDeleteCascadeStillRefusesOutOfTreeCopiedInput(t *testing.T) {
	t.Parallel()

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(tmp, "features")
	outside := filepath.Join(tmp, "outside", "secrets.txt")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := feature.NewStore(stateDir)
	parent := &feature.Feature{
		ID: "parent", Slug: "parent", SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
	}
	parent.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Tasks: map[string]feature.SetupTask{
		"attachment:1": {
			Key: "attachment:1", Kind: feature.SetupTaskAttachment, Path: outside,
		},
	}}})
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	o := New(Deps{Store: store}, Hooks{})

	result, err := o.DeleteCascade(parent.ID)
	if err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	if result.Status != feature.CascadeDeleteCleanupPending {
		t.Fatalf("status = %q, want cleanup_pending", result.Status)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "resource_cleanup_failed" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("out-of-tree file removed: %v", err)
	}
	if _, err := store.Load(parent.ID); err != nil {
		t.Fatalf("parent deleted: %v", err)
	}
}

// saveCascadeSymlinkFixture persists a parent/child relationship whose
// disposable resources all exist on disk, returning the paths cleanup must
// remove.
func saveCascadeSymlinkFixture(t *testing.T, stateDir string) (*feature.Store, *feature.Feature, *feature.Feature, []string) {
	t.Helper()
	store := feature.NewStore(stateDir)
	parent := &feature.Feature{
		ID: "parent", Slug: "parent", SchemaVersion: feature.SchemaVersionCurrent,
		ActiveRun: 1, RunCount: 1,
		Repos: []feature.FeatureRepo{{
			Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/parent/a",
			Branch: "feature/parent",
		}},
	}
	attachment := filepath.Join(stateDir, parent.ID, "attachments", "notes.txt")
	parent.SetRun(&feature.Run{RunNumber: 1, Setup: &feature.SetupState{Tasks: map[string]feature.SetupTask{
		"attachment:1": {
			Key: "attachment:1", Kind: feature.SetupTaskAttachment, Path: attachment,
		},
	}}})
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
	overlay := feature.ParentOverlayPath(stateDir, parent.ID, "repo-a")
	kbWorkspace := feature.ChildKBWorkspaceDir(stateDir, child.ID, "repo-a")
	promotion := filepath.Join(stateDir, child.ID, "promotion.yaml")
	for _, dir := range []string{filepath.Dir(attachment), overlay, kbWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{attachment, filepath.Join(overlay, "seed.txt"), promotion} {
		if err := os.WriteFile(file, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return store, parent, child, []string{attachment, overlay, kbWorkspace, promotion}
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

func TestDeleteCascadeClassifiesSharedParentRefAcrossChildren(t *testing.T) {
	t.Parallel()

	const ref = "refs/heads/feature/parent"
	// "older" promoted first (anchor -> candidate-1) and "newer" promoted on
	// top of it (candidate-1 -> candidate-2). The IDs sort in the opposite
	// order so classification cannot rely on journal order.
	older := feature.RepoTransactionEntry{
		Repo: "repo-a", ParentBranch: "feature/parent",
		ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
		CandidateSHA: "candidate-1", ApplyState: feature.RepoApplyApplied,
	}
	newer := feature.RepoTransactionEntry{
		Repo: "repo-a", ParentBranch: "feature/parent",
		ParentAnchorSHA: "candidate-1", ExpectedRefSHA: "candidate-1",
		CandidateSHA: "candidate-2", ApplyState: feature.RepoApplyApplied,
	}
	// A rebase pass-through child whose candidate is its anchor.
	passThrough := feature.RepoTransactionEntry{
		Repo: "repo-a", ParentBranch: "feature/parent",
		ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
		CandidateSHA: "anchor", ApplyState: feature.RepoApplyApplied,
	}

	tests := []struct {
		name            string
		children        map[string]feature.RepoTransactionEntry
		observed        string
		ancestors       map[string][]string
		ancestorErr     error
		wantStatus      feature.CascadeDeleteStatus
		wantRef         string
		wantUpdateCalls int
		wantDiagCode    string
	}{
		{
			name:            "single child at candidate restores anchor",
			children:        map[string]feature.RepoTransactionEntry{"older": older},
			observed:        "candidate-1",
			wantStatus:      feature.CascadeDeleteCompleted,
			wantRef:         "anchor",
			wantUpdateCalls: 1,
		},
		{
			name:       "single child at anchor leaves ref alone",
			children:   map[string]feature.RepoTransactionEntry{"older": older},
			observed:   "anchor",
			wantStatus: feature.CascadeDeleteCompleted,
			wantRef:    "anchor",
		},
		{
			name:            "two children at latest candidate restore latest anchor",
			children:        map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:        "candidate-2",
			wantStatus:      feature.CascadeDeleteCompleted,
			wantRef:         "candidate-1",
			wantUpdateCalls: 1,
		},
		{
			name:       "two children at latest anchor leave ref alone",
			children:   map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:   "candidate-1",
			wantStatus: feature.CascadeDeleteCompleted,
			wantRef:    "candidate-1",
		},
		{
			name:       "two children at earliest anchor leave ref alone",
			children:   map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:   "anchor",
			wantStatus: feature.CascadeDeleteCompleted,
			wantRef:    "anchor",
		},
		{
			name:       "two children advanced past latest candidate leave ref alone",
			children:   map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:   "advanced",
			ancestors:  map[string][]string{"advanced": {"candidate-2", "candidate-1", "anchor"}},
			wantStatus: feature.CascadeDeleteCompleted,
			wantRef:    "advanced",
		},
		{
			name:       "pass-through child advanced past anchor leaves ref alone",
			children:   map[string]feature.RepoTransactionEntry{"older": passThrough},
			observed:   "advanced",
			ancestors:  map[string][]string{"advanced": {"anchor"}},
			wantStatus: feature.CascadeDeleteCompleted,
			wantRef:    "advanced",
		},
		{
			name:         "two children unrelated history parks",
			children:     map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:     "external",
			ancestors:    map[string][]string{"external": {"unrelated-root"}},
			wantStatus:   feature.CascadeDeleteAttentionRequired,
			wantRef:      "external",
			wantDiagCode: "external_ref_moved",
		},
		{
			name:         "ancestry check failure parks",
			children:     map[string]feature.RepoTransactionEntry{"older": older, "newer": newer},
			observed:     "advanced",
			ancestorErr:  errors.New("bad object"),
			wantStatus:   feature.CascadeDeleteAttentionRequired,
			wantRef:      "advanced",
			wantDiagCode: "ref_read_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, parent := saveCascadeTestPromotedChildren(t, tt.children)
			worktrees := &cascadeTestWorktrees{
				store: store, refs: map[string]string{ref: tt.observed},
				ancestors: tt.ancestors, ancestorErr: tt.ancestorErr,
			}
			o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

			result, err := o.DeleteCascade(parent.ID)
			if err != nil {
				t.Fatalf("DeleteCascade: %v", err)
			}
			if result.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (diagnostics %+v)", result.Status, tt.wantStatus, result.Diagnostics)
			}
			if worktrees.refs[ref] != tt.wantRef {
				t.Fatalf("ref = %q, want %q", worktrees.refs[ref], tt.wantRef)
			}
			if worktrees.updateCalls != tt.wantUpdateCalls {
				t.Fatalf("UpdateRef calls = %d, want %d", worktrees.updateCalls, tt.wantUpdateCalls)
			}
			if tt.wantStatus == feature.CascadeDeleteCompleted {
				if _, err := store.Load(parent.ID); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("parent load error = %v, want not exist", err)
				}
				return
			}
			if len(result.Diagnostics) != len(tt.children) {
				t.Fatalf("diagnostics = %+v, want one per journaled entry", result.Diagnostics)
			}
			for _, diag := range result.Diagnostics {
				if diag.Code != tt.wantDiagCode || diag.ObservedSHA != tt.observed {
					t.Fatalf("diagnostic = %+v, want %s at %q", diag, tt.wantDiagCode, tt.observed)
				}
			}
			if worktrees.removeCalls != 0 {
				t.Fatalf("cleanup calls = %d, want 0", worktrees.removeCalls)
			}
			if _, err := store.Load(parent.ID); err != nil {
				t.Fatalf("parent deleted: %v", err)
			}
			for id := range tt.children {
				if _, err := store.Load(id); err != nil {
					t.Fatalf("child %s deleted: %v", id, err)
				}
			}
		})
	}
}

func TestDeleteCascadeRecordsRestoredEntryOnlyOnce(t *testing.T) {
	t.Parallel()

	const ref = "refs/heads/feature/parent"
	store, parent := saveCascadeTestPromotedChildren(t, map[string]feature.RepoTransactionEntry{
		"older": {
			Repo: "repo-a", ParentBranch: "feature/parent",
			ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
			CandidateSHA: "candidate-1", ApplyState: feature.RepoApplyApplied,
		},
		"newer": {
			Repo: "repo-a", ParentBranch: "feature/parent",
			ParentAnchorSHA: "candidate-1", ExpectedRefSHA: "candidate-1",
			CandidateSHA: "candidate-2", ApplyState: feature.RepoApplyApplied,
		},
	})
	worktrees := &cascadeTestWorktrees{
		store: store, refs: map[string]string{ref: "candidate-2"}, removeErr: errors.New("device busy"),
	}
	o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

	// Resource cleanup fails so the journal survives with its classification.
	result, err := o.DeleteCascade(parent.ID)
	if err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	if result.Status != feature.CascadeDeleteCleanupPending {
		t.Fatalf("status = %q, want cleanup_pending", result.Status)
	}
	intent, err := store.LoadCascadeDelete(parent.ID)
	if err != nil {
		t.Fatalf("LoadCascadeDelete: %v", err)
	}
	if intent.Step != feature.CascadeStepRefsSafe || len(intent.Refs) != 2 {
		t.Fatalf("intent step = %q refs = %+v", intent.Step, intent.Refs)
	}
	for _, journaled := range intent.Refs {
		if !journaled.Safe || journaled.ObservedSHA != "candidate-1" || journaled.Diagnostic != "" {
			t.Fatalf("ref = %+v, want safe at candidate-1", journaled)
		}
		if journaled.Restored != (journaled.ChildID == "newer") {
			t.Fatalf("ref = %+v, want restored only on the child whose candidate was observed", journaled)
		}
	}
	if worktrees.refs[ref] != "candidate-1" || worktrees.updateCalls != 1 {
		t.Fatalf("ref = %q after %d updates, want candidate-1 after 1", worktrees.refs[ref], worktrees.updateCalls)
	}
}

func TestDeleteCascadeRecordsAdvancedRefWithoutRestoring(t *testing.T) {
	t.Parallel()

	const ref = "refs/heads/feature/parent"
	store, parent := saveCascadeTestPromotedChildren(t, map[string]feature.RepoTransactionEntry{
		"older": {
			Repo: "repo-a", ParentBranch: "feature/parent",
			ParentAnchorSHA: "anchor", ExpectedRefSHA: "anchor",
			CandidateSHA: "candidate-1", ApplyState: feature.RepoApplyApplied,
		},
		"newer": {
			Repo: "repo-a", ParentBranch: "feature/parent",
			ParentAnchorSHA: "candidate-1", ExpectedRefSHA: "candidate-1",
			CandidateSHA: "candidate-2", ApplyState: feature.RepoApplyApplied,
		},
	})
	worktrees := &cascadeTestWorktrees{
		store: store, refs: map[string]string{ref: "advanced"},
		ancestors: map[string][]string{"advanced": {"candidate-2"}},
		removeErr: errors.New("device busy"),
	}
	o := New(Deps{Store: store, Worktrees: worktrees}, Hooks{})

	// Resource cleanup fails so the journal survives with its classification.
	result, err := o.DeleteCascade(parent.ID)
	if err != nil {
		t.Fatalf("DeleteCascade: %v", err)
	}
	if result.Status != feature.CascadeDeleteCleanupPending {
		t.Fatalf("status = %q, want cleanup_pending", result.Status)
	}
	intent, err := store.LoadCascadeDelete(parent.ID)
	if err != nil {
		t.Fatalf("LoadCascadeDelete: %v", err)
	}
	if intent.Step != feature.CascadeStepRefsSafe || len(intent.Refs) != 2 {
		t.Fatalf("intent step = %q refs = %+v", intent.Step, intent.Refs)
	}
	for _, journaled := range intent.Refs {
		if !journaled.Safe || journaled.Restored || journaled.ObservedSHA != "advanced" ||
			journaled.Code != "ref_advanced" || journaled.Diagnostic == "" {
			t.Fatalf("ref = %+v, want safe, unrestored, noted as ref_advanced", journaled)
		}
	}
	if worktrees.refs[ref] != "advanced" || worktrees.updateCalls != 0 {
		t.Fatalf("ref = %q after %d updates, want untouched", worktrees.refs[ref], worktrees.updateCalls)
	}
}

// saveCascadeTestPromotedChildren persists a parent whose closed children
// each promoted one transaction entry into the same parent branch.
func saveCascadeTestPromotedChildren(
	t *testing.T,
	children map[string]feature.RepoTransactionEntry,
) (*feature.Store, *feature.Feature) {
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
	if err := store.Save(parent); err != nil {
		t.Fatal(err)
	}
	closedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for id, entry := range children {
		child := &feature.Feature{
			ID: id, Slug: id, SchemaVersion: feature.SchemaVersionCurrent,
			ActiveRun: 1, RunCount: 1,
			Parent: &feature.ChildRelationship{
				ParentID:     parent.ID,
				CloseOutcome: feature.ChildCloseOutcomeCompleted, ClosedAt: &closedAt,
				Transaction: &feature.TransactionJournal{
					Phase: feature.TransactionPhaseMerged, Entries: []feature.RepoTransactionEntry{entry},
				},
			},
			Repos: []feature.FeatureRepo{{
				Name: "repo-a", Path: "/repos/a", WorktreePath: "/worktrees/" + id + "/a",
				Branch: "feature/" + id,
			}},
		}
		if err := store.Save(child); err != nil {
			t.Fatal(err)
		}
	}
	return store, parent
}
