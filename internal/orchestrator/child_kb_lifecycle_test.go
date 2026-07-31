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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// promotionFaultFixture builds an orchestrator with a real feature store and
// manager (no git) so PromoteChildKBWorkspaces can be exercised against
// filesystem-level and seam-injected faults.
type promotionFaultFixture struct {
	t        *testing.T
	o        *Orchestrator
	store    *feature.Store
	stateDir string
	baseDir  string
	parent   *feature.Feature
	child    *feature.Feature
}

func newPromotionFaultFixture(t *testing.T) *promotionFaultFixture {
	t.Helper()
	baseDir := t.TempDir()
	stateDir := filepath.Join(baseDir, "features")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := feature.NewStore(stateDir)
	mgr := feature.NewManager(store, config.NewDefault())

	parent := &feature.Feature{
		ID:            "parent-fx",
		Name:          "Parent",
		Slug:          "parent-fx",
		Status:        feature.StatusCodeReady,
		SchemaVersion: feature.SchemaVersionCurrent,
	}
	child := &feature.Feature{
		ID:            "child-fx",
		Name:          "Child",
		Slug:          "child-fx",
		Status:        feature.StatusReviewPassed,
		Pipeline:      feature.PipelineLarge,
		SchemaVersion: feature.SchemaVersionCurrent,
		Repos: []feature.FeatureRepo{
			{Name: "repoA", Path: filepath.Join(baseDir, "repoA"), Branch: "b", BaseBranch: "main"},
		},
		Parent: &feature.ChildRelationship{
			ParentID:     parent.ID,
			Kind:         feature.ChildKindRefactor,
			CloseOutcome: feature.ChildCloseOutcomeCompleted,
		},
	}
	if err := store.Save(parent); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	if err := store.Save(child); err != nil {
		t.Fatalf("save child: %v", err)
	}
	// A workspace with content so staging has something to copy.
	wsDir := feature.ChildKBWorkspaceDir(stateDir, child.ID, "repoA")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "index.md"), []byte("kb\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &promotionFaultFixture{
		t:        t,
		o:        New(Deps{Lifecycle: mgr, Store: store}, Hooks{}),
		store:    store,
		stateDir: stateDir,
		baseDir:  baseDir,
		parent:   parent,
		child:    child,
	}
}

func (fx *promotionFaultFixture) saveJournal(t *testing.T, journal *feature.PromotionJournal) {
	t.Helper()
	if err := fx.store.SavePromotion(fx.child.ID, journal); err != nil {
		t.Fatalf("SavePromotion: %v", err)
	}
}

func (fx *promotionFaultFixture) loadJournal(t *testing.T) *feature.PromotionJournal {
	t.Helper()
	journal, err := fx.store.LoadPromotion(fx.child.ID)
	if err != nil {
		t.Fatalf("LoadPromotion: %v", err)
	}
	if journal == nil {
		t.Fatal("promotion journal should exist")
	}
	return journal
}

func newPromotingJournal(childID, parentID string, entries ...feature.PromotionEntry) *feature.PromotionJournal {
	return &feature.PromotionJournal{
		ChildID:   childID,
		ParentID:  parentID,
		Phase:     feature.PromotionPhasePromoting,
		Entries:   entries,
		CreatedAt: time.Now().UTC(),
	}
}

// TestPromoteMissingJournalRepositoryFailsClosed proves that a journal entry
// whose repository no longer resolves on the child cannot silently strand the
// promotion: the failure is recorded durably on the entry, the promoted
// repository still commits, and the terminal return is an error so the
// closure tail blocks cleanup and recovery of the incomplete vector.
func TestPromoteMissingJournalRepositoryFailsClosed(t *testing.T) {
	fx := newPromotionFaultFixture(t)
	fx.saveJournal(t, newPromotingJournal(fx.child.ID, fx.parent.ID,
		feature.PromotionEntry{
			Repo:        "repoA",
			OverlayPath: feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA"),
		},
		feature.PromotionEntry{
			Repo:        "ghost",
			OverlayPath: feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "ghost"),
		},
	))

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err == nil {
		t.Fatal("promotion with an unresolvable journal repository must return an error")
	}
	if !strings.Contains(err.Error(), "remains incomplete") {
		t.Fatalf("expected incomplete-journal error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error should name the missing repository, got: %v", err)
	}

	journal := fx.loadJournal(t)
	if journal.Phase == feature.PromotionPhasePromoted {
		t.Fatal("journal must not be marked promoted while a repository is missing")
	}
	ghost := journal.EntryByRepo("ghost")
	if ghost == nil || !strings.Contains(ghost.Error, "no longer has repository") {
		t.Fatalf("ghost entry should record the missing-repository error durably, got %+v", ghost)
	}
	repoA := journal.EntryByRepo("repoA")
	if repoA == nil || !repoA.Done {
		t.Fatalf("resolvable repository should still be promoted, got %+v", repoA)
	}
	// The committed overlay exists but is still locked because the vector
	// is incomplete.
	owner, lockErr := feature.ReadOverlayLockOwner(repoA.OverlayPath)
	if lockErr != nil {
		t.Fatalf("ReadOverlayLockOwner: %v", lockErr)
	}
	if owner != fx.child.ID {
		t.Fatalf("repoA overlay lock should be held while the journal is incomplete, owner=%q", owner)
	}
}

func TestPromoteMissingClosedChildWorkspaceInvalidatesStaleOverlay(t *testing.T) {
	fx := newPromotionFaultFixture(t)
	workspaceDir := feature.ChildKBWorkspaceDir(fx.stateDir, fx.child.ID, "repoA")
	overlayDir := feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "index.md"), []byte("stale parent knowledge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := feature.SaveOverlayProvenance(overlayDir, &feature.OverlayProvenance{
		CanonicalCommit: "old-canonical",
		ParentHEAD:      "old-parent",
		Generation:      1,
		CreatedAt:       time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspaceDir); err != nil {
		t.Fatal(err)
	}

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err != nil {
		t.Fatalf("PromoteChildKBWorkspaces() error = %v, want missing closed-child workspace settled", err)
	}
	if _, err := os.Stat(overlayDir); !os.IsNotExist(err) {
		t.Fatalf("stale overlay should be invalidated, err=%v", err)
	}
	journal := fx.loadJournal(t)
	entry := journal.EntryByRepo("repoA")
	if entry == nil || !entry.Done || entry.Error != "" {
		t.Fatalf("missing-workspace entry should be settled, got %+v", entry)
	}
	if journal.Phase != feature.PromotionPhasePromoted || !journal.LocksReleased {
		t.Fatalf("journal = phase %s, locks_released=%v; want promoted and unlocked",
			journal.Phase, journal.LocksReleased)
	}
	if owner, err := feature.ReadOverlayLockOwner(overlayDir); err != nil || owner != "" {
		t.Fatalf("overlay lock owner = %q, err=%v; want released", owner, err)
	}
}

// TestPromoteJournalPersistenceFailureFailsClosed proves that a failed
// journal save aborts the promotion with contextual error rather than
// proceeding against recovery state that was never written.
func TestPromoteJournalPersistenceFailureFailsClosed(t *testing.T) {
	fx := newPromotionFaultFixture(t)
	fx.saveJournal(t, newPromotingJournal(fx.child.ID, fx.parent.ID,
		feature.PromotionEntry{
			Repo:        "repoA",
			OverlayPath: feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA"),
		},
	))

	// Make the child state directory unwritable so the journal phase-flip
	// persistence fails.
	childStateDir := filepath.Join(fx.stateDir, fx.child.ID)
	if err := os.Chmod(childStateDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(childStateDir, 0o755) }()

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err == nil {
		t.Fatal("promotion must fail when journal persistence fails")
	}
	if !strings.Contains(err.Error(), "persisting promotion journal") {
		t.Fatalf("expected journal persistence error, got: %v", err)
	}
}

// TestPromoteOverlayLockAcquisitionFailure proves that a lock creation
// failure during the acquisition loop aborts the promotion and names the
// repository.
func TestPromoteOverlayLockAcquisitionFailure(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	original := acquireOverlayLockFn
	acquireOverlayLockFn = func(overlayDir, childID string) (bool, error) {
		return false, fmt.Errorf("injected lock creation fault")
	}
	defer func() { acquireOverlayLockFn = original }()

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err == nil {
		t.Fatal("promotion must fail when lock acquisition fails")
	}
	if !strings.Contains(err.Error(), "acquiring overlay lock for repo repoA") {
		t.Fatalf("expected acquisition error naming repoA, got: %v", err)
	}
}

// TestPromoteOverlayLockReacquisitionFailure proves that a refresh failure
// after a staged overlay commit fails closed: the committed overlay stays
// locked by the original acquisition, the entry records the error, and the
// journal remains pending for recovery.
func TestPromoteOverlayLockReacquisitionFailure(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	original := acquireOverlayLockFn
	calls := 0
	acquireOverlayLockFn = func(overlayDir, childID string) (bool, error) {
		calls++
		if calls > 1 {
			// The second call is the post-commit refresh.
			return false, fmt.Errorf("injected lock refresh fault")
		}
		return original(overlayDir, childID)
	}
	defer func() { acquireOverlayLockFn = original }()

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err == nil {
		t.Fatal("promotion must fail when the post-commit lock refresh fails")
	}
	if !strings.Contains(err.Error(), "re-acquiring overlay lock for repo repoA") {
		t.Fatalf("expected re-acquisition error naming repoA, got: %v", err)
	}

	journal := fx.loadJournal(t)
	entry := journal.EntryByRepo("repoA")
	if entry == nil {
		t.Fatal("repoA entry should exist")
	}
	if entry.Done {
		t.Fatal("entry must not be marked done when the lock refresh failed")
	}
	if !strings.Contains(entry.Error, "re-acquiring overlay lock") {
		t.Fatalf("entry should record the refresh failure durably, got %q", entry.Error)
	}
	if journal.Phase == feature.PromotionPhasePromoted {
		t.Fatal("journal must remain pending after a refresh failure")
	}

	// The originally acquired lock is still held, so a later child cannot
	// seed from the committed-but-unpromoted overlay.
	owner, lockErr := feature.ReadOverlayLockOwner(entry.OverlayPath)
	if lockErr != nil {
		t.Fatalf("ReadOverlayLockOwner: %v", lockErr)
	}
	if owner != fx.child.ID {
		t.Fatalf("overlay lock should remain held after refresh failure, owner=%q", owner)
	}
}

// TestPromoteOverlayLockReleaseFailure proves the final lock release is
// part of the promoted transition itself: a failed release surfaces an
// error AND leaves the journal unpromoted, so the idempotent retry runs the
// release again instead of treating the journal as settled.
func TestPromoteOverlayLockReleaseFailure(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	original := releaseOverlayLockFn
	releaseOverlayLockFn = func(overlayDir, childID string) error {
		return errors.New("injected lock release fault")
	}
	defer func() { releaseOverlayLockFn = original }()

	err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID)
	if err == nil {
		t.Fatal("promotion must return an error when the final lock release fails")
	}
	if !strings.Contains(err.Error(), "releasing overlay locks") {
		t.Fatalf("expected release error, got: %v", err)
	}

	// The vector completed on disk (entry Done) but the journal must NOT be
	// promoted: promoted without LocksReleased is only ever a transient,
	// retried state, never the settled one.
	journal := fx.loadJournal(t)
	if journal.Phase == feature.PromotionPhasePromoted {
		t.Fatal("journal must not be promoted while its locks are still held")
	}
	if journal.LocksReleased {
		t.Fatal("journal must not record released locks after a failed release")
	}
	entry := journal.EntryByRepo("repoA")
	if entry == nil || !entry.Done {
		t.Fatalf("repoA overlay commit should be durably done, got %+v", entry)
	}

	// The overlay is committed but still locked, so the overlay stays
	// unavailable to later children until the release succeeds.
	owner, lockErr := feature.ReadOverlayLockOwner(entry.OverlayPath)
	if lockErr != nil {
		t.Fatalf("ReadOverlayLockOwner: %v", lockErr)
	}
	if owner != fx.child.ID {
		t.Fatalf("overlay lock should remain held after failed release, owner=%q", owner)
	}

	// Clear the fault: the retry must re-drive the release and settle the
	// journal in one idempotent step.
	releaseOverlayLockFn = original
	if err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID); err != nil {
		t.Fatalf("promotion retry after clearing the release fault: %v", err)
	}
	journal = fx.loadJournal(t)
	if journal.Phase != feature.PromotionPhasePromoted || !journal.LocksReleased {
		t.Fatalf("journal should be promoted+unlocked after retry, got phase=%s locks_released=%v",
			journal.Phase, journal.LocksReleased)
	}
	if owner, _ := feature.ReadOverlayLockOwner(entry.OverlayPath); owner != "" {
		t.Fatalf("overlay lock should be released after retry, owner=%q", owner)
	}
}

// TestPromotedJournalRetriesLeftoverLockRelease proves that a journal which
// reached the promoted phase without its locks released (the legacy stuck
// state a failed final release used to leave behind) is not treated as
// settled: the promotion owner re-runs every recorded release before
// returning success.
func TestPromotedJournalRetriesLeftoverLockRelease(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	overlayDir := feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA")
	fx.saveJournal(t, &feature.PromotionJournal{
		ChildID:   fx.child.ID,
		ParentID:  fx.parent.ID,
		Phase:     feature.PromotionPhasePromoted,
		CreatedAt: time.Now().UTC(),
		Entries: []feature.PromotionEntry{
			{Repo: "repoA", OverlayPath: overlayDir, Done: true},
		},
	})
	if acquired, err := feature.AcquireOverlayLock(overlayDir, fx.child.ID); err != nil || !acquired {
		t.Fatalf("AcquireOverlayLock = %v, %v", acquired, err)
	}

	// While the release still fails, even a promoted journal must report
	// an error instead of the old immediate success.
	original := releaseOverlayLockFn
	releaseOverlayLockFn = func(overlayDir, childID string) error {
		return errors.New("injected lock release fault")
	}
	defer func() { releaseOverlayLockFn = original }()

	if err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID); err == nil {
		t.Fatal("promoted journal with leftover locks must not return success while release fails")
	}
	if owner, _ := feature.ReadOverlayLockOwner(overlayDir); owner != fx.child.ID {
		t.Fatalf("overlay lock should remain held while release fails, owner=%q", owner)
	}

	// Once the release can succeed, the same call settles the journal.
	releaseOverlayLockFn = original
	if err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID); err != nil {
		t.Fatalf("release retry on promoted journal: %v", err)
	}
	journal := fx.loadJournal(t)
	if !journal.LocksReleased {
		t.Fatal("promoted journal should record locks_released after the retry")
	}
	if owner, _ := feature.ReadOverlayLockOwner(overlayDir); owner != "" {
		t.Fatalf("overlay lock should be released after the retry, owner=%q", owner)
	}

	// A final call is a clean no-op.
	if err := fx.o.PromoteChildKBWorkspaces(fx.child.ID, fx.parent.ID); err != nil {
		t.Fatalf("settled journal should be a no-op: %v", err)
	}
}

// TestPromotionReleaseFailureStartupRecoveryOrder models the exact startup
// sequence from the stranded-lock report: integration reconciliation runs
// FIRST and re-enters the closure tail for the merged, already-closed child
// (promotion hits the injected final-release failure), then promotion
// reconciliation runs. Proves the tail keeps the journal and workspaces
// while the release fails, reconciliation retries the release until the
// lock is gone, and a subsequent tail finally removes both workspaces and
// journal.
func TestPromotionReleaseFailureStartupRecoveryOrder(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	overlayDir := feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA")
	workspaceDir := feature.ChildKBWorkspaceDir(fx.stateDir, fx.child.ID, "repoA")

	failRelease := true
	original := releaseOverlayLockFn
	releaseOverlayLockFn = func(dir, childID string) error {
		if failRelease {
			return errors.New("injected final release fault")
		}
		return original(dir, childID)
	}
	defer func() { releaseOverlayLockFn = original }()

	// Pass 1 — integration reconciliation: the closure tail promotes, the
	// overlay commits, and the injected final-release failure must block
	// the tail WITHOUT deleting the journal or the workspace.
	if err := fx.o.settleChildClosureTail(fx.child.ID, fx.parent.ID); err == nil {
		t.Fatal("closure tail must fail while the final lock release fails")
	}
	journal := fx.loadJournal(t)
	if journal.Phase == feature.PromotionPhasePromoted {
		t.Fatal("journal must not reach promoted while its lock release fails")
	}
	if owner, _ := feature.ReadOverlayLockOwner(overlayDir); owner != fx.child.ID {
		t.Fatalf("overlay lock should remain held after failed release, owner=%q", owner)
	}
	if _, err := os.Stat(workspaceDir); err != nil {
		t.Fatalf("workspace must be preserved for recovery: %v", err)
	}

	// Pass 2 — promotion reconciliation with the fault cleared: the
	// unfinished journal is resumed and must release the leftover lock
	// even though every entry was already Done before the retry.
	failRelease = false
	if err := fx.o.ReconcilePromotions(); err != nil {
		t.Fatalf("ReconcilePromotions: %v", err)
	}
	if owner, _ := feature.ReadOverlayLockOwner(overlayDir); owner != "" {
		t.Fatalf("reconciliation should release the leftover lock, owner=%q", owner)
	}
	journal = fx.loadJournal(t)
	if journal.Phase != feature.PromotionPhasePromoted || !journal.LocksReleased {
		t.Fatalf("journal should be promoted+unlocked after reconciliation, got phase=%s locks_released=%v",
			journal.Phase, journal.LocksReleased)
	}
	// The journal itself still exists: reconciliation of a promoting
	// journal resumes it; terminal cleanup belongs to the closure tail.
	if _, err := os.Stat(workspaceDir); err != nil {
		t.Fatalf("workspace should survive until the closure tail settles: %v", err)
	}

	// Pass 3 — the next startup's integration pass (the tail re-entered
	// for the already-closed child) now observes the durable
	// promoted+unlocked invariant and performs the terminal cleanup.
	if err := fx.o.settleChildClosureTail(fx.child.ID, fx.parent.ID); err != nil {
		t.Fatalf("settled closure tail: %v", err)
	}
	if _, err := os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("workspace should be removed after settlement, err=%v", err)
	}
	if journal, err := fx.store.LoadPromotion(fx.child.ID); err != nil || journal != nil {
		t.Fatalf("promotion journal should be deleted after settlement, journal=%v err=%v", journal, err)
	}
	if owner, _ := feature.ReadOverlayLockOwner(overlayDir); owner != "" {
		t.Fatalf("overlay lock should stay released after settlement, owner=%q", owner)
	}
}

// TestReconcilePromotionsReleasesLeftoverLocks proves the startup pass frees
// locks stranded by a failed release on a promoted, completed child.
func TestReconcilePromotionsReleasesLeftoverLocks(t *testing.T) {
	fx := newPromotionFaultFixture(t)

	overlayDir := feature.ParentOverlayPath(fx.stateDir, fx.parent.ID, "repoA")
	fx.saveJournal(t, &feature.PromotionJournal{
		ChildID:   fx.child.ID,
		ParentID:  fx.parent.ID,
		Phase:     feature.PromotionPhasePromoted,
		CreatedAt: time.Now().UTC(),
		Entries: []feature.PromotionEntry{
			{Repo: "repoA", OverlayPath: overlayDir, Done: true},
		},
	})
	acquired, err := feature.AcquireOverlayLock(overlayDir, fx.child.ID)
	if err != nil || !acquired {
		t.Fatalf("AcquireOverlayLock = %v, %v", acquired, err)
	}

	if err := fx.o.ReconcilePromotions(); err != nil {
		t.Fatalf("ReconcilePromotions: %v", err)
	}
	owner, lockErr := feature.ReadOverlayLockOwner(overlayDir)
	if lockErr != nil {
		t.Fatalf("ReadOverlayLockOwner: %v", lockErr)
	}
	if owner != "" {
		t.Fatalf("leftover lock should be released during reconciliation, owner=%q", owner)
	}
	if journal, err := fx.store.LoadPromotion(fx.child.ID); err != nil || journal != nil {
		t.Fatalf("promoted journal should be deleted during reconciliation, journal=%v err=%v", journal, err)
	}
}
