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
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// Overlay lock seams for promotion. They are package-level variables so
// fault-injection tests can force acquisition, re-acquisition (refresh), and
// release failures deterministically; production code must use these instead
// of calling feature.AcquireOverlayLock / feature.ReleaseOverlayLock directly.
var (
	acquireOverlayLockFn = feature.AcquireOverlayLock
	releaseOverlayLockFn = feature.ReleaseOverlayLock
)

// persistPromotionJournal durably saves the promotion journal with child
// context. Every journal mutation during promotion must flow through this
// helper so a persistence failure is never silently discarded — the journal
// is the only recovery input for an interrupted promotion.
func persistPromotionJournal(store promotionStore, journal *feature.PromotionJournal) error {
	if err := store.SavePromotion(journal.ChildID, journal); err != nil {
		return fmt.Errorf("persisting promotion journal for child %s: %w", journal.ChildID, err)
	}
	return nil
}

// This file owns the child KB workspace lifecycle beyond the initial KB phase:
// the final-KB-refresh boundary before integration, and the promotion of
// successful child knowledge to the stable parent overlay after integration.

// RefreshChildKBWorkspaces is the final-KB-refresh boundary. It runs after
// final review approval and before integration candidate preparation. It
// revalidates overlay and canonical provenance because either may have
// advanced while the child ran, reseeds safely when necessary, then invokes
// the KB builder against each final reviewed child HEAD and waits for the
// session to complete. Only a complete, validated refresh vector may enter
// the integration path; a refresh failure leaves the child restartable at
// this boundary with every parent ref unchanged.
func (o *Orchestrator) RefreshChildKBWorkspaces(childID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child for KB refresh: %w", err)
	}
	if !child.IsChild() {
		return nil
	}

	baseDir := o.stateDir()
	if baseDir == "" || o.deps.PhaseRunner == nil {
		return nil
	}

	for _, repo := range child.Repos {
		paths := agent.ResolveChildKBPaths(baseDir, child, repo)
		repoPath := repo.Path
		if repo.WorktreePath != "" {
			repoPath = repo.WorktreePath
		}

		// Get the current (final reviewed) child HEAD.
		childCommit, err := agent.GetCurrentCommit(context.Background(), o.deps.CmdRunner, repoPath)
		if err != nil {
			return fmt.Errorf("getting child HEAD for KB refresh repo %s: %w", repo.Name, err)
		}

		// Revalidate provenance before trusting the workspace. The
		// canonical KB or parent overlay may have advanced while the
		// child ran, so a workspace that is merely fresh at the child
		// HEAD could still be built from a stale baseline.
		state, stateErr := feature.LoadWorkspaceState(paths.WorkspaceDir)
		if stateErr != nil {
			return fmt.Errorf("loading workspace state for KB refresh repo %s: %w", repo.Name, stateErr)
		}
		needsReseed := false
		if state == nil {
			needsReseed = true
		} else {
			// Check if the canonical commit is still valid. Any movement
			// triggers a reseed so the workspace reflects the current
			// baseline rather than stamping stale knowledge with a
			// newer commit.
			canonCommit := agent.CanonicalKBCommit(paths.CanonicalDir)
			if canonCommit != state.CanonicalCommit && canonCommit != "" {
				needsReseed = true
			}
			// Check if the overlay advanced (if seeded from overlay).
			if state.Source == feature.WorkspaceSourceOverlay {
				if overlayProv, _ := feature.LoadOverlayProvenance(paths.OverlayDir); overlayProv != nil {
					if overlayProv.ParentHEAD != state.ParentHEAD {
						// Parent overlay moved. Reseed from the new overlay if valid.
						if canonState, _ := agent.LoadKBState(paths.CanonicalDir); canonState != nil &&
							canonState.HeadCommit == overlayProv.CanonicalCommit &&
							agent.IsAncestor(context.Background(), o.deps.CmdRunner, repoPath, overlayProv.ParentHEAD, childCommit) {
							needsReseed = true
						}
					}
				}
			}
		}

		if needsReseed {
			// Reseed the workspace from the best available source.
			if err := agent.SeedChildKBWorkspace(context.Background(), o.deps.CmdRunner, paths); err != nil {
				return fmt.Errorf("reseeding child KB workspace for repo %s: %w", repo.Name, err)
			}
		}

		// Check if the (possibly reseeded) workspace is already fresh at
		// the final reviewed HEAD. This must come after provenance
		// revalidation so a workspace built from a stale baseline is
		// never trusted just because the child HEAD didn't change.
		if !needsReseed && agent.IsWorkspaceFresh(context.Background(), o.deps.CmdRunner, paths.WorkspaceDir, repoPath) {
			continue
		}

		// If we reseeded, the workspace is no longer fresh (AnalyzedCommit
		// was cleared). If we didn't reseed and the workspace isn't fresh,
		// the child HEAD changed since the last build. Either way, the KB
		// builder must run.

		// Start the KB builder session against the final reviewed child HEAD.
		// RunChildKnowledgeBaseForRepo seeds if needed and starts an async
		// session, returning a non-empty session ID. It returns ("", nil)
		// when the workspace is already fresh (AnalyzedCommit matches HEAD).
		sessionID, err := o.deps.PhaseRunner.RunChildKnowledgeBaseForRepo(child, repo)
		if err != nil {
			return fmt.Errorf("refreshing child KB for repo %s: %w", repo.Name, err)
		}

		if sessionID == "" {
			// Workspace is already fresh — no session was started.
			continue
		}

		// Wait for the KB session to complete synchronously. The session
		// cleanup releases the KB lock. The phase supervisor is not invoked
		// here, so we must commit the outcome and mark freshness ourselves.
		var intent llm.CompletionIntent
		if o.deps.Sessions != nil {
			sess := o.deps.Sessions.GetSession(sessionID)
			if sess != nil {
				sess.Wait()
				intent = sess.RootCompletionIntent()
			}
		}

		// Validate the root outcome and the KB artifacts, then write the
		// harness-owned completion receipt into the disposable workspace.
		_, _, violations, err := agent.CommitPhaseOutcome(agent.CompletionCommitInput{
			Phase:       feature.PhaseKnowledgeBase,
			Role:        agent.RoleKnowledgeBaseBuilder,
			ArtifactDir: paths.WorkspaceDir,
			SessionID:   sessionID,
			Intent:      intent,
		})
		if err != nil {
			return fmt.Errorf("committing child KB outcome for repo %s: %w", repo.Name, err)
		}
		if len(violations) > 0 {
			return fmt.Errorf("KB refresh session for repo %s did not complete cleanly: %s", repo.Name, violations[0].Reason)
		}

		// Mark the workspace fresh — only the completion path writes freshness.
		if err := agent.MarkWorkspaceFresh(context.Background(), o.deps.CmdRunner, paths.WorkspaceDir, repoPath); err != nil {
			return fmt.Errorf("marking workspace fresh for repo %s: %w", repo.Name, err)
		}
	}

	return nil
}

// PromoteChildKBWorkspaces promotes each refreshed child workspace to the
// stable parent overlay using atomic same-filesystem replacement. It stamps
// the resulting parent merge HEAD and canonical commit. The repository set
// is treated as one logical promotion: the parent overlay remains unavailable
// to later children until the entire vector is complete.
//
// A promotion failure does not reopen the child; the disposable workspace
// and exact pending progress remain available for idempotent recovery.
func (o *Orchestrator) PromoteChildKBWorkspaces(childID, parentID string) error {
	child, err := o.deps.Lifecycle.Get(childID)
	if err != nil {
		return fmt.Errorf("load child for promotion: %w", err)
	}
	if !child.IsChild() {
		return nil
	}

	baseDir := o.stateDir()
	if baseDir == "" {
		return nil
	}

	store, ok := o.deps.Store.(promotionStore)
	if !ok {
		return nil
	}

	// Load or create the promotion journal.
	journal, err := store.LoadPromotion(childID)
	if err != nil {
		return fmt.Errorf("loading promotion journal: %w", err)
	}
	if journal == nil {
		journal = &feature.PromotionJournal{
			ChildID:   childID,
			ParentID:  parentID,
			Phase:     feature.PromotionPhasePending,
			CreatedAt: time.Now().UTC(),
		}
		for _, repo := range child.Repos {
			journal.Entries = append(journal.Entries, feature.PromotionEntry{
				Repo:        repo.Name,
				OverlayPath: feature.ParentOverlayPath(baseDir, parentID, repo.Name),
			})
		}
		if err := persistPromotionJournal(store, journal); err != nil {
			return err
		}
	}

	// Already fully promoted — but "promoted" is only complete once every
	// recorded overlay lock is released. A prior final-release failure can
	// leave the journal promoted with locks still held; retry the release
	// before reporting success so no caller treats a locked journal as
	// settled.
	if journal.Phase == feature.PromotionPhasePromoted {
		if journal.LocksReleased {
			return nil
		}
		if err := releaseJournalOverlayLocks(journal); err != nil {
			return fmt.Errorf("releasing leftover overlay locks for child %s: %w", childID, err)
		}
		journal.LocksReleased = true
		return persistPromotionJournal(store, journal)
	}

	// Load the parent as an existence guard. Merge HEADs are read from
	// the child's transaction journal below; the parent load ensures the
	// parent feature still exists so promotion targets a live relationship.
	parent, err := o.deps.Lifecycle.Get(parentID)
	if err != nil {
		return fmt.Errorf("load parent for promotion: %w", err)
	}
	_ = parent // parent existence validated; not modified during promotion

	// Set phase to promoting.
	journal.Phase = feature.PromotionPhasePromoting
	if err := persistPromotionJournal(store, journal); err != nil {
		return err
	}

	// Acquire overlay locks for ALL repos — including ones already marked
	// Done from a prior partial promotion — before promoting any remaining
	// repo. This ensures a later child cannot consume a partially promoted
	// overlay. The locks are only released when the entire promotion vector
	// succeeds; on failure, the locks remain held so a later child waits on
	// the pending promotion journal rather than seeding from a stale baseline.
	//
	// Including Done repos is critical: a Done repo's lock lives in the
	// stable overlay namespace (CommitStagedOverlay no longer destroys it),
	// so a retry that skipped Done repos would leave their locks held
	// permanently, blocking all later children. AcquireOverlayLock is
	// reentrant for the same childID, so re-acquiring a Done repo's lock
	// simply refreshes the timestamp.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		repo := featureRepoByName(child, entry.Repo)
		if repo == nil {
			continue
		}
		paths := agent.ResolveChildKBPaths(baseDir, child, *repo)
		acquired, lockErr := acquireOverlayLockFn(paths.OverlayDir, childID)
		if lockErr != nil {
			return fmt.Errorf("acquiring overlay lock for repo %s: %w", entry.Repo, lockErr)
		}
		if !acquired {
			return fmt.Errorf("overlay locked by another child for repo %s: %w", entry.Repo, feature.ErrOverlayLocked)
		}
	}

	// Promote each repo's workspace to the parent overlay in two phases:
	// stage all new overlays first, then commit them all. This keeps the
	// overlay locks alive throughout staging so a later child cannot
	// observe a partially promoted overlay set.
	type stagedRepo struct {
		entryIdx  int
		tmpDir    string
		mergeHEAD string
		canonical string
	}
	var staged []stagedRepo
	stagedDirs := make(map[string]bool) // tmpDir -> staged, for cleanup

	// Phase 1: Stage all not-done repos.
	for i := range journal.Entries {
		entry := &journal.Entries[i]
		if entry.Done {
			continue
		}
		repo := featureRepoByName(child, entry.Repo)
		if repo == nil {
			// The child no longer resolves this repository, so the entry
			// can never be promoted. Record the failure durably and surface
			// it via the terminal incomplete-journal error below.
			entry.Error = fmt.Sprintf("child no longer has repository %s", entry.Repo)
			if err := persistPromotionJournal(store, journal); err != nil {
				return err
			}
			continue
		}
		paths := agent.ResolveChildKBPaths(baseDir, child, *repo)

		mergeHEAD := ""
		if child.Parent.Transaction != nil {
			if txEntry := child.Parent.Transaction.EntryByRepo(entry.Repo); txEntry != nil {
				mergeHEAD = txEntry.MergeHEAD
				if mergeHEAD == "" {
					mergeHEAD = txEntry.CandidateSHA
				}
			}
		}

		// Stamp the canonical commit the workspace was actually built
		// from, not the current canonical commit. This prevents stamping
		// stale knowledge with a newer commit when the canonical KB
		// advanced during child execution.
		canonicalCommit := agent.CanonicalKBCommit(paths.CanonicalDir)
		if _, statErr := os.Stat(paths.WorkspaceDir); statErr != nil {
			if !os.IsNotExist(statErr) {
				entry.Error = statErr.Error()
				if perr := persistPromotionJournal(store, journal); perr != nil {
					return errors.Join(fmt.Errorf("checking workspace for promotion repo %s: %w", entry.Repo, statErr), perr)
				}
				return fmt.Errorf("checking workspace for promotion repo %s: %w", entry.Repo, statErr)
			}
			// Legacy and partially cleaned child records can reach closure
			// without a disposable KB workspace. Retrying cannot recreate the
			// reviewed workspace after its worktree has been removed. For a
			// durably closed child, invalidate the old parent overlay so future
			// children rebuild from canonical knowledge instead of consuming a
			// stale overlay, then settle this journal entry.
			if child.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
				entry.Error = statErr.Error()
				if perr := persistPromotionJournal(store, journal); perr != nil {
					return errors.Join(fmt.Errorf("missing workspace for active child repo %s: %w", entry.Repo, statErr), perr)
				}
				return fmt.Errorf("missing workspace for active child repo %s: %w", entry.Repo, statErr)
			}
			if removeErr := agent.RemoveOverlay(paths.OverlayDir); removeErr != nil {
				entry.Error = removeErr.Error()
				if perr := persistPromotionJournal(store, journal); perr != nil {
					return errors.Join(fmt.Errorf("invalidating stale overlay for repo %s: %w", entry.Repo, removeErr), perr)
				}
				return fmt.Errorf("invalidating stale overlay for repo %s: %w", entry.Repo, removeErr)
			}
			entry.MergeHEAD = mergeHEAD
			entry.CanonicalCommit = canonicalCommit
			entry.Done = true
			entry.Error = ""
			if err := persistPromotionJournal(store, journal); err != nil {
				return err
			}
			o.emitEvent(ports.Event{
				Type:      ports.RepoStatusChanged,
				FeatureID: childID,
				ParentID:  parentID,
				ChildID:   childID,
				RepoName:  entry.Repo,
				Message:   "missing child KB workspace; invalidated stale parent overlay",
			})
			continue
		}

		wsState, wsErr := feature.LoadWorkspaceState(paths.WorkspaceDir)
		if wsErr != nil {
			entry.Error = wsErr.Error()
			if perr := persistPromotionJournal(store, journal); perr != nil {
				return errors.Join(fmt.Errorf("loading workspace state for promotion repo %s: %w", entry.Repo, wsErr), perr)
			}
			return fmt.Errorf("loading workspace state for promotion repo %s: %w", entry.Repo, wsErr)
		}
		if wsState != nil {
			canonicalCommit = wsState.CanonicalCommit
		}

		tmpDir, stageErr := agent.StageWorkspaceToOverlay(
			paths.WorkspaceDir, paths.OverlayDir, mergeHEAD, canonicalCommit,
		)
		if stageErr != nil {
			entry.Error = stageErr.Error()
			if perr := persistPromotionJournal(store, journal); perr != nil {
				return errors.Join(fmt.Errorf("staging workspace for repo %s: %w", entry.Repo, stageErr), perr)
			}
			// Clean up all staged temp dirs.
			for dir := range stagedDirs {
				_ = os.RemoveAll(dir)
			}
			return fmt.Errorf("staging workspace for repo %s: %w", entry.Repo, stageErr)
		}
		stagedDirs[tmpDir] = true
		staged = append(staged, stagedRepo{
			entryIdx:  i,
			tmpDir:    tmpDir,
			mergeHEAD: mergeHEAD,
			canonical: canonicalCommit,
		})
	}

	// Phase 2: Commit all staged overlays, then refresh the overlay lock
	// after each commit. The lock lives in the stable sibling namespace so
	// CommitStagedOverlay does not destroy it, but the refresh keeps the
	// timestamp current and re-registers the overlay in the release set.
	// The locks are released only after the entire vector is complete,
	// preventing a later child from consuming a partial promotion.
	for _, s := range staged {
		entry := &journal.Entries[s.entryIdx]
		repo := featureRepoByName(child, entry.Repo)
		if repo == nil {
			continue
		}
		paths := agent.ResolveChildKBPaths(baseDir, child, *repo)

		if err := agent.CommitStagedOverlay(s.tmpDir, paths.OverlayDir); err != nil {
			entry.Error = err.Error()
			if perr := persistPromotionJournal(store, journal); perr != nil {
				return errors.Join(fmt.Errorf("committing staged overlay for repo %s: %w", entry.Repo, err), perr)
			}
			return fmt.Errorf("committing staged overlay for repo %s: %w", entry.Repo, err)
		}

		// Refresh the overlay lock after the replacement, failing closed:
		// without an exclusive lock the freshly committed overlay would be
		// exposed to seeding by a later child mid-promotion.
		reacquired, lockErr := acquireOverlayLockFn(paths.OverlayDir, childID)
		if lockErr != nil || !reacquired {
			if lockErr == nil {
				lockErr = feature.ErrOverlayLocked
			}
			entry.Error = fmt.Sprintf("re-acquiring overlay lock after commit: %v", lockErr)
			if perr := persistPromotionJournal(store, journal); perr != nil {
				return errors.Join(fmt.Errorf("re-acquiring overlay lock for repo %s after commit: %w", entry.Repo, lockErr), perr)
			}
			return fmt.Errorf("re-acquiring overlay lock for repo %s after commit: %w", entry.Repo, lockErr)
		}

		entry.MergeHEAD = s.mergeHEAD
		entry.CanonicalCommit = s.canonical
		entry.Done = true
		entry.Error = ""
		if err := persistPromotionJournal(store, journal); err != nil {
			return err
		}
	}

	// All promoted — release every recorded lock BEFORE marking the journal
	// promoted. This ordering keeps "promoted" and "unlocked" one durable
	// transition: a failed release leaves the journal in the promoting
	// phase (entries already record the completed overlay commits), so the
	// idempotent retry above re-runs the release instead of skipping it.
	if journal.AllPromoted() {
		if err := releaseJournalOverlayLocks(journal); err != nil {
			return fmt.Errorf("releasing overlay locks after promotion for child %s: %w", childID, err)
		}
		journal.Phase = feature.PromotionPhasePromoted
		journal.LocksReleased = true
		if err := persistPromotionJournal(store, journal); err != nil {
			return err
		}
		o.emitEvent(ports.Event{
			Type:      ports.RepoStatusChanged,
			FeatureID: childID,
			ParentID:  parentID,
			ChildID:   childID,
			Message:   "child KB workspaces promoted to parent overlays",
		})
		return nil
	}

	// The journal remains incomplete (for example, a repository no longer
	// resolves on the child). Returning nil here would let the closure tail
	// treat the promotion vector as settled and delete the recovery inputs,
	// so surface the pending entries as an error instead.
	var pending []string
	for i := range journal.Entries {
		if journal.Entries[i].Done {
			continue
		}
		msg := journal.Entries[i].Repo
		if journal.Entries[i].Error != "" {
			msg += ": " + journal.Entries[i].Error
		}
		pending = append(pending, msg)
	}
	return fmt.Errorf("promotion journal for child %s remains incomplete: %s", childID, strings.Join(pending, "; "))
}

// releaseJournalOverlayLocks releases every overlay lock recorded in the
// promotion journal. It is idempotent: ReleaseOverlayLock is a no-op for an
// absent or foreign lock file, so a retry after a partial release only
// re-attempts the locks that are still held. All failures are joined so one
// stubborn lock never masks the outcome of the others.
func releaseJournalOverlayLocks(journal *feature.PromotionJournal) error {
	var errs []error
	for i := range journal.Entries {
		if journal.Entries[i].OverlayPath == "" {
			continue
		}
		if err := releaseOverlayLockFn(journal.Entries[i].OverlayPath, journal.ChildID); err != nil {
			errs = append(errs, fmt.Errorf("repo %s: %w", journal.Entries[i].Repo, err))
		}
	}
	return errors.Join(errs...)
}

// promotionStore is the subset of Store used for promotion journal persistence.
type promotionStore interface {
	LoadPromotion(childID string) (*feature.PromotionJournal, error)
	SavePromotion(childID string, journal *feature.PromotionJournal) error
	DeletePromotion(childID string) error
}

// ReconcilePromotions is the idempotent startup reconciliation pass for
// promotion journals. It runs after integration reconciliation so a merged
// child with a pending promotion can be recovered.
func (o *Orchestrator) ReconcilePromotions() error {
	if o.deps.Store == nil {
		return nil
	}
	store, ok := o.deps.Store.(promotionStore)
	if !ok {
		return nil
	}
	features, listErr := o.deps.Store.List()
	var partialIDs []string
	if listErr != nil {
		var ple *feature.PartialLoadError
		if !errors.As(listErr, &ple) {
			return fmt.Errorf("list features: %w", listErr)
		}
		for _, w := range ple.Warnings {
			partialIDs = append(partialIDs, w.ID)
		}
	}
	var errs []error
	for _, f := range features {
		if err := o.reconcileOnePromotion(f, store); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f.ID, err))
		}
	}
	for _, id := range partialIDs {
		f, err := o.deps.Store.Load(id)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: load: %w", id, err))
			continue
		}
		if f == nil {
			continue
		}
		if err := o.reconcileOnePromotion(f, store); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (o *Orchestrator) reconcileOnePromotion(f *feature.Feature, store promotionStore) error {
	if f == nil || !f.IsChild() {
		return nil
	}
	journal, err := store.LoadPromotion(f.ID)
	if err != nil {
		return fmt.Errorf("loading promotion journal for %s: %w", f.ID, err)
	}
	if journal == nil {
		return nil
	}
	// A promoted journal is only settled once every recorded lock is
	// verifiably released. Establish that invariant idempotently through
	// the promotion owner — a failed final release leaves the journal
	// promoted with locks held, and only this retry frees later children.
	if journal.Phase == feature.PromotionPhasePromoted {
		if err := o.PromoteChildKBWorkspaces(f.ID, f.Parent.ParentID); err != nil {
			return err
		}
		// Only clean up the journal if the child is closed and cleanup is done.
		if f.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
			return nil
		}
		if err := store.DeletePromotion(f.ID); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("deleting promoted journal: %w", err)
		}
		return nil
	}
	// A pending or promoting journal on a closed (Completed) child should be
	// resumed. A pending journal on an active child means the integration
	// hasn't completed yet — leave it.
	if f.Parent.CloseOutcome != feature.ChildCloseOutcomeCompleted {
		return nil
	}
	// Resume promotion for a completed child with an unfinished journal.
	return o.PromoteChildKBWorkspaces(f.ID, f.Parent.ParentID)
}
