/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Draft-selection controller for the review-feedback workspace. Owns the
 * complete persistence protocol against the server-owned durable draft: the
 * revision acknowledged by the last successful mutation, the overlay of
 * unacknowledged local choices (`ref -> {value, seq}`), the single
 * promise-queue lane, the epoch guard that invalidates queued sends after a
 * conflict/refetch, and every recovery transition.
 *
 * Every selection write — a single toggle, a visible-set bulk action, or a
 * recovery retry — goes through ONE shared enqueue/acknowledge path:
 * `enqueueBatches` splits the edits into deduplicated batches of at most 512
 * reference-only updates, sends them strictly in order each chained onto the
 * previous batch's acknowledged revision, and `acknowledgeBatch` adopts each
 * acknowledgement (revision, repos, overlay entries whose seq still matches).
 * Only the failure/settlement callbacks differ per caller.
 *
 * Lifecycle is a discriminated union (`loading | ready | error`) and recovery
 * is a single nullable union (`saveFailed | conflictReloading |
 * conflictReloadFailed`) instead of loose booleans: a typed revision conflict
 * or draft-not-found never replays local choices over the other writer — the
 * epoch bumps, queued sends no-op, and the authoritative draft is refetched
 * through an explicit reloading/failed transition. Only after the server view
 * is adopted does the caller-visible conflict notice appear; a failed reload
 * stays frozen with an actionable retry.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { parseIpcError } from '../../wizard/ipcError';
import type { CanonicalError } from '../../../../shared/ipc';
import { chunkSelectionUpdates } from './feedbackFilters';
import {
  fetchReviewFeedbackDraft,
  saveReviewFeedbackSelection,
  type ReviewFeedbackDraftCommentView,
  type ReviewFeedbackDraftRepoGroup,
  type ReviewFeedbackDraftView,
  type ReviewFeedbackSelectionUpdate,
} from './reviewFeedbackDraftApi';

export type DraftLifecycle =
  { phase: 'loading' } | { phase: 'ready' } | { phase: 'error'; error: CanonicalError };

/** One unacknowledged edit; the sequence number orders stacked edits per ref. */
export interface PendingSelection {
  value: boolean;
  seq: number;
}

/**
 * Recovery overlays over the ready draft. `saveFailed` keeps the failed and
 * unsent batches in the overlay over the last acknowledged revision and
 * freezes mutations until the user retries or deliberately reloads.
 * `conflictReloading` / `conflictReloadFailed` are the two phases of
 * reconciling after another writer committed first: the overlay is already
 * invalidated by the epoch bump; the reload must succeed before the
 * workspace is usable again.
 */
export type DraftRecovery =
  | { kind: 'saveFailed'; error: CanonicalError }
  | { kind: 'conflictReloading'; conflict: CanonicalError }
  | { kind: 'conflictReloadFailed'; conflict: CanonicalError; error: CanonicalError };

/** `review_feedback` conflict codes that always resolve via a refetch, never a replay. */
const CONFLICT_CODES = new Set([
  'review_feedback_revision_conflict',
  'review_feedback_draft_not_found',
]);

interface QueuedEdit extends ReviewFeedbackSelectionUpdate {
  seq: number;
}

export interface UseReviewFeedbackDraftOptions {
  featureId: string;
  /**
   * Fires after every wholesale server-view adoption (fresh load, deliberate
   * reload, conflict reconciliation) so the owner can reset view-only state —
   * scope, filters, drawer, card expansion, feed scroll — to its defaults.
   */
  onAdoptView(): void;
}

export interface UseReviewFeedbackDraft {
  lifecycle: DraftLifecycle;
  repos: ReviewFeedbackDraftRepoGroup[];
  pending: ReadonlyMap<string, PendingSelection>;
  /** Active recovery overlay, or null when the draft is fully reconciled. */
  recovery: DraftRecovery | null;
  /** Explanatory notice published only after a conflict reload succeeded. */
  conflictNotice: CanonicalError | null;
  /** True while an explicit recovery action (retry/reload) is in flight. */
  retrying: boolean;
  /** Polite one-shot flag after a deliberate reload succeeded. */
  reloaded: boolean;
  /** Selection with the overlay applied: pending choice else committed value. */
  selectedOf(comment: ReviewFeedbackDraftCommentView): boolean;
  /** The latest acknowledged revision, for constant-size launch requests. */
  getRevision(): number;
  /** Fresh (re)load of the draft; invalidates all queued sends. */
  reload(): void;
  /** Optimistic single-toggle through the shared lane. */
  toggle(comment: ReviewFeedbackDraftCommentView, checked: boolean): void;
  /** Optimistic multi-update through the shared lane, batched at 512. */
  applyBulk(updates: ReviewFeedbackSelectionUpdate[]): void;
  /** Re-enqueue every outstanding overlay entry from the acknowledged revision. */
  retrySave(): void;
  /** Deliberately abandon the overlay and adopt the authoritative draft. */
  reloadSavedSelections(): void;
  /** Begin conflict reconciliation (also used by a stale-revision launch). */
  recoverFromConflict(conflict: CanonicalError): void;
  /** Re-attempt a failed conflict reload; recovers the workspace. */
  retryConflictReload(): void;
  /** Clear the explanatory/polite notices without touching the draft. */
  dismissNotices(): void;
}

export function useReviewFeedbackDraft({
  featureId,
  onAdoptView,
}: UseReviewFeedbackDraftOptions): UseReviewFeedbackDraft {
  const [lifecycle, setLifecycle] = useState<DraftLifecycle>({ phase: 'loading' });
  const [repos, setRepos] = useState<ReviewFeedbackDraftRepoGroup[]>([]);
  const [pending, setPending] = useState<ReadonlyMap<string, PendingSelection>>(new Map());
  const [recovery, setRecovery] = useState<DraftRecovery | null>(null);
  const [conflictNotice, setConflictNotice] = useState<CanonicalError | null>(null);
  const [retrying, setRetrying] = useState(false);
  const [reloaded, setReloaded] = useState(false);

  const revisionRef = useRef(0);
  const pendingRef = useRef<ReadonlyMap<string, PendingSelection>>(new Map());
  const seqRef = useRef(0);
  // Bumped on every conflict/refetch so queued sends based on a stale draft
  // no-op instead of writing against a revision the user has already lost.
  const epochRef = useRef(0);
  const queueRef = useRef<Promise<void>>(Promise.resolve());
  // Mirrored so queued lane steps and guarded callbacks see the freeze
  // synchronously, without re-creating every callback per recovery change.
  const recoveryRef = useRef<DraftRecovery | null>(null);
  const onAdoptViewRef = useRef(onAdoptView);
  onAdoptViewRef.current = onAdoptView;

  const mirrorRecovery = (next: DraftRecovery | null): void => {
    recoveryRef.current = next;
    setRecovery(next);
  };
  const mirrorPending = (next: ReadonlyMap<string, PendingSelection>): void => {
    pendingRef.current = next;
    setPending(next);
  };

  /** Adopt a server draft view wholesale: revision, repos, cleared overlay. */
  const adoptServerView = useCallback(
    (view: { revision: number; repos: ReviewFeedbackDraftRepoGroup[] }) => {
      revisionRef.current = view.revision;
      setRepos(view.repos);
      mirrorPending(new Map());
      onAdoptViewRef.current();
    },
    [],
  );

  /**
   * Adopt one save acknowledgement: chain the revision, replace the committed
   * view, and drop exactly the overlay entries this batch settled (a newer
   * edit on the same reference keeps its higher seq and stays pending).
   * Returns false when the epoch moved on, meaning the lane step must stop.
   */
  const acknowledgeBatch = (
    ack: Pick<ReviewFeedbackDraftView, 'revision' | 'repos'>,
    batch: ReviewFeedbackSelectionUpdate[],
    seqs: ReadonlyMap<string, number>,
    epoch: number,
  ): boolean => {
    if (epoch !== epochRef.current) return false;
    revisionRef.current = ack.revision;
    setRepos(ack.repos);
    const cleared = new Map(pendingRef.current);
    for (const update of batch) {
      if (cleared.get(update.stableRef)?.seq === seqs.get(update.stableRef)) {
        cleared.delete(update.stableRef);
      }
    }
    mirrorPending(cleared);
    return true;
  };

  /**
   * The single mutate path: every selection write funnels here. Edits carry
   * their overlay seq so acknowledgements never clear a newer stacked edit.
   * The lane step no-ops when the epoch moved on or a recovery froze the
   * queue (`skipFreezeGuard` is reserved for `retrySave`, which is itself the
   * recovery action and must run while `saveFailed` is still displayed).
   */
  const enqueueBatches = useCallback(
    (
      edits: QueuedEdit[],
      epoch: number,
      handlers: {
        onFailure(err: unknown): void;
        onAllCommitted?(): void;
        skipFreezeGuard?: boolean;
      },
    ): void => {
      const seqs = new Map(edits.map((edit) => [edit.stableRef, edit.seq]));
      // The wire payload is strictly reference + choice; `seq` is local
      // overlay bookkeeping and never leaves the renderer.
      const updates: ReviewFeedbackSelectionUpdate[] = edits.map(({ stableRef, selected }) => ({
        stableRef,
        selected,
      }));
      const batches = chunkSelectionUpdates(updates);
      queueRef.current = queueRef.current.then(async () => {
        if (epoch !== epochRef.current) return;
        if (!handlers.skipFreezeGuard && recoveryRef.current !== null) return;
        for (const batch of batches) {
          try {
            const ack = await saveReviewFeedbackSelection({
              featureId,
              expectedRevision: revisionRef.current,
              updates: batch,
            });
            if (!acknowledgeBatch(ack, batch, seqs, epoch)) return;
          } catch (err) {
            handlers.onFailure(err);
            return;
          }
        }
        handlers.onAllCommitted?.();
      });
    },
    [featureId],
  );

  /**
   * Conflict reconciliation: the epoch bump invalidates every queued send and
   * the unacknowledged overlay is never replayed over the other writer's
   * committed view. The conflict notice is published only after the
   * authoritative view has been adopted; a failed refetch lands in
   * `conflictReloadFailed`, which keeps mutations frozen and offers a retry,
   * so the user is never left with a stale view under a "ready" claim.
   */
  const recoverFromConflict = useCallback(
    (conflict: CanonicalError): void => {
      epochRef.current += 1;
      const epoch = epochRef.current;
      setRetrying(false);
      setReloaded(false);
      setConflictNotice(null);
      mirrorRecovery({ kind: 'conflictReloading', conflict });
      fetchReviewFeedbackDraft(featureId)
        .then((view) => {
          if (epoch !== epochRef.current) return;
          adoptServerView(view);
          mirrorRecovery(null);
          setConflictNotice(conflict);
        })
        .catch((err: unknown) => {
          if (epoch !== epochRef.current) return;
          mirrorRecovery({ kind: 'conflictReloadFailed', conflict, error: parseIpcError(err) });
        });
    },
    [featureId, adoptServerView],
  );

  /**
   * Two distinct failure contracts: a typed conflict reconciles through
   * `recoverFromConflict`; anything else keeps the failed and unsent batches
   * visible as unsaved choices over the last acknowledged revision, stops the
   * queue (queued sends check `recoveryRef` and no-op), and freezes mutations
   * until the user retry-saves or deliberately reloads.
   */
  const handleSaveFailure = useCallback(
    (err: unknown, epoch: number): void => {
      if (epoch !== epochRef.current) return;
      const parsed = parseIpcError(err);
      if (CONFLICT_CODES.has(parsed.code)) {
        recoverFromConflict(parsed);
        return;
      }
      mirrorRecovery({ kind: 'saveFailed', error: parsed });
    },
    [recoverFromConflict],
  );

  const toggle = useCallback(
    (comment: ReviewFeedbackDraftCommentView, checked: boolean): void => {
      if (recoveryRef.current !== null) return;
      const seq = ++seqRef.current;
      const epoch = epochRef.current;
      const next = new Map(pendingRef.current);
      next.set(comment.stableRef, { value: !checked, seq });
      mirrorPending(next);
      setReloaded(false);
      enqueueBatches([{ stableRef: comment.stableRef, selected: !checked, seq }], epoch, {
        onFailure: (err) => handleSaveFailure(err, epoch),
      });
    },
    [enqueueBatches, handleSaveFailure],
  );

  const applyBulk = useCallback(
    (updates: ReviewFeedbackSelectionUpdate[]): void => {
      if (updates.length === 0 || recoveryRef.current !== null) return;
      const epoch = epochRef.current;
      const next = new Map(pendingRef.current);
      const edits: QueuedEdit[] = updates.map((update) => ({
        ...update,
        seq: ++seqRef.current,
      }));
      for (const edit of edits) next.set(edit.stableRef, { value: edit.selected, seq: edit.seq });
      mirrorPending(next);
      setReloaded(false);
      enqueueBatches(edits, epoch, {
        onFailure: (err) => handleSaveFailure(err, epoch),
      });
    },
    [enqueueBatches, handleSaveFailure],
  );

  /**
   * Retry the outstanding unsaved choices: overlay entries ordered by edit
   * sequence (later edits win per reference, original ordering otherwise),
   * re-batched at the 512-update bound, from the latest acknowledged
   * revision. A transient failure lands back in `saveFailed`; a conflict
   * converges via `handleSaveFailure`.
   */
  const retrySave = useCallback((): void => {
    const outstanding: QueuedEdit[] = [...pendingRef.current.entries()]
      .map(([stableRef, entry]) => ({ stableRef, selected: entry.value, seq: entry.seq }))
      .sort((a, b) => a.seq - b.seq);
    if (outstanding.length === 0) {
      mirrorRecovery(null);
      return;
    }
    const epoch = epochRef.current;
    setRetrying(true);
    enqueueBatches(outstanding, epoch, {
      skipFreezeGuard: true,
      onFailure: (err) => {
        setRetrying(false);
        handleSaveFailure(err, epoch);
      },
      onAllCommitted: () => {
        if (epoch !== epochRef.current) return;
        setRetrying(false);
        mirrorRecovery(null);
      },
    });
  }, [enqueueBatches, handleSaveFailure]);

  /**
   * Deliberately abandon the unsaved overlay: fetch the authoritative draft
   * first, and only on success discard the overlay, adopt the view, and
   * announce politely. A failed fetch keeps the `saveFailed` recovery — alert
   * and both actions — exactly as it was, so the unsaved choices and the way
   * forward survive intact.
   */
  const reloadSavedSelections = useCallback((): void => {
    const epoch = epochRef.current;
    setRetrying(true);
    fetchReviewFeedbackDraft(featureId)
      .then((view) => {
        if (epoch !== epochRef.current) return;
        // Invalidate queued sends that still reference the abandoned overlay.
        epochRef.current += 1;
        adoptServerView(view);
        mirrorRecovery(null);
        setConflictNotice(null);
        setReloaded(true);
      })
      .catch(() => {
        // Nothing to restore: the save-failure recovery state still on screen
        // is the actionable surface; only `retrying` unwinds in `finally`.
      })
      .finally(() => setRetrying(false));
  }, [featureId, adoptServerView]);

  const retryConflictReload = useCallback((): void => {
    const current = recoveryRef.current;
    if (current?.kind !== 'conflictReloadFailed') return;
    recoverFromConflict(current.conflict);
  }, [recoverFromConflict]);

  const dismissNotices = useCallback((): void => {
    setConflictNotice(null);
    setReloaded(false);
  }, []);

  /** Fresh load: resets every recovery surface and invalidates the lane. */
  const reload = useCallback((): void => {
    epochRef.current += 1;
    setLifecycle({ phase: 'loading' });
    mirrorRecovery(null);
    setConflictNotice(null);
    setRetrying(false);
    setReloaded(false);
    fetchReviewFeedbackDraft(featureId)
      .then((view) => {
        adoptServerView(view);
        setLifecycle({ phase: 'ready' });
      })
      .catch((err: unknown) => setLifecycle({ phase: 'error', error: parseIpcError(err) }));
  }, [featureId, adoptServerView]);

  useEffect(reload, [reload]);

  const selectedOf = useCallback(
    (comment: ReviewFeedbackDraftCommentView): boolean =>
      pending.get(comment.stableRef)?.value ?? comment.selected,
    [pending],
  );

  const getRevision = useCallback((): number => revisionRef.current, []);

  return {
    lifecycle,
    repos,
    pending,
    recovery,
    conflictNotice,
    retrying,
    reloaded,
    selectedOf,
    getRevision,
    reload,
    toggle,
    applyBulk,
    retrySave,
    reloadSavedSelections,
    recoverFromConflict,
    retryConflictReload,
    dismissNotices,
  };
}
