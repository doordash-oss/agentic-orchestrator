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
 * "Address review feedback" full-screen workspace. Replaces the old modal: it
 * swaps the cockpit content for a review inbox backed by the server-owned
 * durable pending draft. A repository/filter rail (wide) or drawer (narrow)
 * owns repository scope, author/type/path filters, and the visible-only bulk
 * actions; the feed renders one labelled section per repository in the
 * server's stable order, with comments oldest-first. `All feedback` shows
 * every section; a repository scope shows just that section.
 *
 * Selection semantics: toggles and bulk actions apply to the visible choice
 * immediately and are serialized through a single promise-queue lane — each
 * request carries the revision acknowledged by the previous successful
 * mutation, unacknowledged edits sit in an overlay the acknowledgement never
 * overwrites, and bulk targets are split into deterministic batches of at
 * most 512 reference-only updates, each chained onto the previous batch's
 * returned revision. That persistence protocol — queue, revision chaining,
 * overlay, epoch guard, and every recovery transition — lives in
 * `useReviewFeedbackDraft`; this component only composes view state around
 * it. A visible choice is therefore always committed, saving, or unsaved: a
 * non-conflict save failure keeps the failed batch and every unsent batch
 * visible as unsaved local choices over the last acknowledged revision, stops
 * the queue, freezes selection/bulk/gate/Launch/Back, and focuses a recovery
 * alert offering `Retry save` (outstanding stable references in their
 * original order, bounded batches, from the latest acknowledged revision) or
 * `Reload saved selections` (fetch the authoritative draft first, then
 * deliberately discard the overlay and reset the view). A typed revision
 * conflict never replays local choices over the other writer: it bumps the
 * epoch, refetches through an explicit reloading transition (polite status
 * only, never focused), and publishes the focused conflict explanation only
 * after the committed view has been adopted; a failed reload surfaces its own
 * focused, actionable alert with `Retry reload`, keeping mutations frozen
 * until reconciliation succeeds. Selections hidden by scope or filters are
 * never part of a bulk target set. Launch sends only
 * `{expected_revision, gate}` and stays disabled while a save is pending;
 * a replayed launch response (same child, original counts) is handled
 * identically to the original dispatch. Back waits for the pending save to
 * commit before leaving, so a fresh re-entry restores exactly the selection
 * the user last saw. Scope, filters, scroll, and the drawer reset to their
 * defaults on every fresh entry.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FeatureSnapshot } from '../../../../shared/ipc';
import { useMediaQuery } from '../../hooks';
import { parseIpcError } from '../../wizard/ipcError';
import type { CanonicalError } from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { ScopeDrawer } from './ScopeDrawer';
import { ScopePanel, type ScopeLedgerEntry } from './ScopePanel';
import { ReviewFeedbackFeed, type FeedSection } from './ReviewFeedbackFeed';
import { ReviewFeedbackDetailDialog } from './ReviewFeedbackDetailDialog';
import { useReviewFeedbackDraft } from './useReviewFeedbackDraft';
import {
  EMPTY_FILTERS,
  facetOptions,
  filtersActive,
  matchesFilters,
  pruneFiltersForScope,
  scopeGroups,
  type ReviewFeedbackFilters,
} from './feedbackFilters';
import {
  launchReceiptText,
  launchReviewFeedbackDraft,
  type ReviewFeedbackDraftCommentView,
} from './reviewFeedbackDraftApi';

/**
 * The launch was structurally sound but every selected comment disappeared
 * since the workspace loaded (addressed or deleted upstream). This is its own
 * recovery transition — never the generic launch-failure path: the draft is
 * preserved server-side, so the workspace fetches and adopts the latest
 * authoritative feedback and only then focuses the explanatory notice.
 */
const ZERO_LAUNCHABLE_CODE = 'review_feedback_zero_launchable_selection';

export interface ReviewFeedbackWorkspaceProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onBack(): void;
  onDispatched(launch: { childId: string; receipt?: string }): void;
}

export function ReviewFeedbackWorkspace({
  featureId,
  snapshot,
  onBack,
  onDispatched,
}: ReviewFeedbackWorkspaceProps): React.ReactElement {
  const [scope, setScope] = useState<string>('all');
  const [filters, setFilters] = useState<ReviewFeedbackFilters>(EMPTY_FILTERS);
  // A launch dispatch that failed before durable creation; Launch re-enables.
  const [launchError, setLaunchError] = useState<CanonicalError | null>(null);
  const [gate, setGate] = useState(true);
  const [launching, setLaunching] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [detailComment, setDetailComment] = useState<ReviewFeedbackDraftCommentView | null>(null);
  const feedRef = useRef<HTMLElement | null>(null);
  const openerRef = useRef<HTMLButtonElement | null>(null);
  const saveFailureAlertRef = useRef<HTMLDivElement | null>(null);
  const conflictAlertRef = useRef<HTMLDivElement | null>(null);
  const conflictReloadAlertRef = useRef<HTMLDivElement | null>(null);
  const launchAlertRef = useRef<HTMLDivElement | null>(null);

  // Server order/scope is authoritative; a freshly adopted view resets every
  // view-only piece of state to its documented default (All scope, no
  // filters, no drawer, collapsed cards, feed top).
  const resetView = useCallback((): void => {
    setScope('all');
    setFilters(EMPTY_FILTERS);
    setDrawerOpen(false);
    setDetailComment(null);
    feedRef.current?.scrollTo?.(0, 0);
  }, []);

  const draft = useReviewFeedbackDraft({ featureId, onAdoptView: resetView });
  const { lifecycle, recovery, pending } = draft;
  const saveFailure = recovery?.kind === 'saveFailed' ? recovery.error : null;
  const frozen = recovery !== null;

  // Actionable alerts own focus; background saving and the in-flight conflict
  // reload only announce politely.
  useEffect(() => {
    if (recovery?.kind === 'saveFailed') saveFailureAlertRef.current?.focus();
    if (recovery?.kind === 'conflictReloadFailed') conflictReloadAlertRef.current?.focus();
  }, [recovery]);
  useEffect(() => {
    if (draft.conflictNotice !== null) conflictAlertRef.current?.focus();
  }, [draft.conflictNotice]);
  useEffect(() => {
    if (launchError !== null) launchAlertRef.current?.focus();
  }, [launchError]);

  const isNarrow = useMediaQuery('(max-width: 900px)');

  // Any recovery overlay interrupts a pending leave: unsaved choices can
  // never leave, and the conflict transitions drop the queue definitionally.
  useEffect(() => {
    if (recovery !== null) setLeaving(false);
  }, [recovery]);

  // Seed the gate toggle from the parent's current Roadmap-review setting.
  useEffect(() => {
    window.agentico
      .getFeatureConfig(featureId)
      .then((config) => setGate(config.current.checkpoints.roadmapReview))
      .catch(() => {});
  }, [featureId]);

  // Widening the window dismisses the drawer; its state resets next entry.
  useEffect(() => {
    if (!isNarrow) setDrawerOpen(false);
  }, [isNarrow]);

  const selectedOf = draft.selectedOf;

  const counts = useMemo(() => {
    let selected = 0;
    let total = 0;
    for (const group of draft.repos) {
      for (const comment of group.comments) {
        total += 1;
        if (selectedOf(comment)) selected += 1;
      }
    }
    return { selected, total };
  }, [draft.repos, selectedOf]);

  const ledger = useMemo<ScopeLedgerEntry[]>(
    () => [
      { scope: 'all', label: 'All feedback', selected: counts.selected, total: counts.total },
      ...draft.repos.map((group) => ({
        scope: group.repo,
        label: group.repo,
        total: group.comments.length,
        selected: group.comments.filter(selectedOf).length,
      })),
    ],
    [draft.repos, counts, selectedOf],
  );

  /** Groups in the active repository scope, before filters. */
  const scopedGroups = useMemo(() => scopeGroups(draft.repos, scope), [draft.repos, scope]);
  const scopedCount = useMemo(
    () => scopedGroups.reduce((sum, group) => sum + group.comments.length, 0),
    [scopedGroups],
  );
  const options = useMemo(() => facetOptions(scopedGroups), [scopedGroups]);

  /** Per-section matches: filtering never reorders what it keeps. */
  const matchedSections = useMemo<FeedSection[]>(
    () =>
      scopedGroups
        .map((group) => ({
          group,
          comments: group.comments.filter((comment) => matchesFilters(comment, filters)),
        }))
        .filter((section) => section.comments.length > 0),
    [scopedGroups, filters],
  );

  const visibleComments = useMemo(
    () => matchedSections.flatMap((section) => section.comments),
    [matchedSections],
  );
  const visibleCount = visibleComments.length;

  // Bulk targets are exactly the visible comments whose state must change.
  const selectTargets = useMemo(
    () => visibleComments.filter((comment) => !selectedOf(comment)),
    [visibleComments, selectedOf],
  );
  const clearTargets = useMemo(
    () => visibleComments.filter((comment) => selectedOf(comment)),
    [visibleComments, selectedOf],
  );

  /** Repository scope is view state; selections in any scope remain untouched. */
  const changeScope = useCallback(
    (nextScope: string): void => {
      setScope(nextScope);
      setFilters((prev) => pruneFiltersForScope(scopeGroups(draft.repos, nextScope), prev));
    },
    [draft.repos],
  );

  const toggleAuthor = useCallback((author: string): void => {
    setFilters((prev) => ({
      ...prev,
      authors: prev.authors.includes(author)
        ? prev.authors.filter((value) => value !== author)
        : [...prev.authors, author],
    }));
  }, []);

  const toggleType = useCallback((type: ReviewFeedbackDraftCommentView['type']): void => {
    setFilters((prev) => ({
      ...prev,
      types: prev.types.includes(type)
        ? prev.types.filter((value) => value !== type)
        : [...prev.types, type],
    }));
  }, []);

  const onPathChange = useCallback((path: string): void => {
    setFilters((prev) => ({ ...prev, path }));
  }, []);

  /** Clears filter constraints only: scope, selections, and focus are kept. */
  const clearFilters = useCallback((): void => {
    setFilters(EMPTY_FILTERS);
  }, []);

  const closeDrawer = useCallback((): void => {
    setDrawerOpen(false);
    openerRef.current?.focus();
  }, []);

  const saving = pending.size > 0;

  // Back waits for the pending save: the effect fires once the queue drains
  // successfully. A failed save clears `leaving` via the recovery effect
  // above instead, and unresolved unsaved choices can never leave.
  useEffect(() => {
    if (leaving && pending.size === 0 && recovery === null) onBack();
  }, [leaving, pending, recovery, onBack]);

  const requestBack = useCallback((): void => {
    if (saveFailure !== null) return;
    if (pending.size === 0) {
      onBack();
      return;
    }
    setLeaving(true);
  }, [pending, saveFailure, onBack]);

  const launch = useCallback((): void => {
    if (saving || launching || frozen || lifecycle.phase !== 'ready') return;
    setLaunchError(null);
    draft.dismissNotices();
    setLaunching(true);
    launchReviewFeedbackDraft({
      parentId: featureId,
      expectedRevision: draft.getRevision(),
      gate,
    })
      .then((result) => {
        // A replayed response (interrupted original dispatch) carries the same
        // shape — the original child ID and original counts — and transitions
        // identically.
        const receipt = launchReceiptText(result);
        onDispatched({ childId: result.childId, ...(receipt === undefined ? {} : { receipt }) });
        onBack();
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        // A launch conflict means this draft view was stale; the draft itself
        // (and the committed selection) is preserved — reconcile through the
        // same conflict-reload transition as a conflicting save. A
        // zero-launchable rejection recovers through the same adoption
        // pipeline: repeat launches against the unchanged stale view would
        // only repeat the error.
        if (
          parsed.code === 'review_feedback_revision_conflict' ||
          parsed.code === 'review_feedback_draft_not_found' ||
          parsed.code === ZERO_LAUNCHABLE_CODE
        ) {
          draft.recoverFromConflict(parsed);
          return;
        }
        setLaunchError(parsed);
      })
      .finally(() => setLaunching(false));
  }, [featureId, gate, saving, launching, frozen, lifecycle.phase, onBack, onDispatched, draft]);

  const launchLabel =
    saveFailure !== null
      ? 'Unsaved choices — retry or reload'
      : launching
        ? 'Addressing…'
        : counts.selected === 0
          ? 'Select comments to address'
          : `Address comments (${counts.selected})`;

  const activeFilters = filtersActive(filters);

  const selectVisible = useCallback(
    (): void =>
      draft.applyBulk(
        selectTargets.map((comment) => ({ stableRef: comment.stableRef, selected: true })),
      ),
    [draft, selectTargets],
  );
  const clearVisible = useCallback(
    (): void =>
      draft.applyBulk(
        clearTargets.map((comment) => ({ stableRef: comment.stableRef, selected: false })),
      ),
    [draft, clearTargets],
  );

  const panel = (
    <ScopePanel
      ledger={ledger}
      scope={scope}
      onScope={changeScope}
      authors={options.authors}
      types={options.types}
      filters={filters}
      onToggleAuthor={toggleAuthor}
      onToggleType={toggleType}
      onPathChange={onPathChange}
      visibleCount={visibleCount}
      scopedCount={scopedCount}
      selectVisibleCount={selectTargets.length}
      clearVisibleCount={clearTargets.length}
      onSelectVisible={selectVisible}
      onClearVisible={clearVisible}
      launching={launching}
      mutationsDisabled={frozen}
    />
  );

  const feed = (
    <ReviewFeedbackFeed
      sections={matchedSections}
      filters={filters}
      activeFilters={activeFilters}
      visibleCount={visibleCount}
      scopedCount={scopedCount}
      selectedOf={selectedOf}
      pending={pending}
      saveFailed={saveFailure !== null}
      selectionDisabled={launching || frozen}
      onToggle={draft.toggle}
      onOpen={setDetailComment}
      onToggleAuthor={toggleAuthor}
      onToggleType={toggleType}
      onPathChange={onPathChange}
      onClearFilters={clearFilters}
      feedRef={feedRef}
    />
  );

  return (
    <div
      className="review-feedback-workspace"
      role="dialog"
      aria-modal="true"
      aria-label="Address review feedback"
      aria-busy={launching}
    >
      <header className="review-feedback-workspace__header">
        <button
          type="button"
          className="review-feedback-workspace__back"
          onClick={requestBack}
          disabled={launching || leaving || saveFailure !== null}
        >
          {leaving ? 'Saving…' : 'Back'}
        </button>
        <div className="review-feedback-workspace__heading">
          <h2 className="review-feedback-workspace__title">Address review feedback</h2>
          <p className="review-feedback-workspace__subtitle">
            Review unaddressed pull-request feedback across {snapshot.name}&rsquo;s repositories and
            address the comments you keep selected in a new child pass.
          </p>
        </div>
        <p className="review-feedback-workspace__ledger" aria-live="polite">
          {counts.selected} of {counts.total} selected
          {saving && saveFailure === null ? ' · saving…' : ''}
          {saveFailure !== null ? ' · includes unsaved choices' : ''}
        </p>
      </header>

      {launching ? (
        <p className="sr-only" role="status">
          Addressing selected comments…
        </p>
      ) : null}

      {saveFailure !== null ? (
        <>
          {/* The frozen recovery card: the canonical error owns the text, the
           * hardcoded lead rides as the caption, Retry save is the primary
           * in-card action, and Reload saved selections stays a sibling. */}
          <ErrorSurface
            error={saveFailure}
            variant="compact"
            caption="Choices not saved"
            rootRef={saveFailureAlertRef}
            rootTabIndex={-1}
            localAction={
              draft.retrying
                ? { label: 'Retry save', disabledReason: 'Retrying…' }
                : { label: 'Retry save', onAction: draft.retrySave }
            }
          />
          <button
            type="button"
            disabled={draft.retrying}
            onClick={() => {
              setLaunchError(null);
              draft.reloadSavedSelections();
            }}
          >
            Reload saved selections
          </button>
        </>
      ) : null}

      {recovery?.kind === 'conflictReloading' ? (
        <p role="status" className="review-feedback-workspace__status">
          Reloading selections…
        </p>
      ) : null}

      {recovery?.kind === 'conflictReloadFailed' ? (
        <ErrorSurface
          error={recovery.error}
          variant="compact"
          caption="Selections could not be reloaded"
          rootRef={conflictReloadAlertRef}
          rootTabIndex={-1}
          localAction={{ label: 'Retry reload', onAction: draft.retryConflictReload }}
        />
      ) : null}

      {draft.conflictNotice !== null ? (
        <ErrorSurface
          error={draft.conflictNotice}
          variant="compact"
          caption={
            draft.conflictNotice.code === ZERO_LAUNCHABLE_CODE
              ? 'Selected feedback is gone'
              : 'Selections reloaded'
          }
          rootRef={conflictAlertRef}
          rootTabIndex={-1}
        />
      ) : null}

      {launchError !== null ? (
        /* The footer's Address comments button is the retry; it stays where
         * the user already is instead of being duplicated in the card. */
        <ErrorSurface
          error={launchError}
          variant="compact"
          caption="Comments could not be addressed"
          rootRef={launchAlertRef}
          rootTabIndex={-1}
        />
      ) : null}

      {draft.reloaded ? (
        <p role="status" className="review-feedback-workspace__status">
          Saved selections reloaded; view reset to All feedback.
        </p>
      ) : null}

      {lifecycle.phase === 'loading' ? (
        <p role="status" className="review-feedback-workspace__status">
          Fetching review feedback…
        </p>
      ) : null}

      {lifecycle.phase === 'error' ? (
        <ErrorSurface
          error={lifecycle.error}
          variant="compact"
          caption="Review feedback could not be loaded"
          localAction={{
            label: 'Retry',
            onAction: () => {
              setLaunchError(null);
              draft.reload();
            },
          }}
        />
      ) : null}

      {lifecycle.phase === 'ready' ? (
        counts.total === 0 ? (
          <div className="review-feedback-empty">
            <p className="review-feedback-empty__title" role="status">
              No unaddressed comments. Every repository is up to date.
            </p>
            <p className="review-feedback-empty__hint">
              Feedback can arrive at any time — refresh to check the pull requests again, or go back
              to the feature.
            </p>
            <div className="review-feedback-empty__actions">
              <button
                type="button"
                className="launcher-modal__primary"
                disabled={launching}
                onClick={() => {
                  setLaunchError(null);
                  draft.reload();
                }}
              >
                Refresh feedback
              </button>
              <button
                type="button"
                className="review-feedback-empty__back"
                disabled={launching || leaving}
                onClick={requestBack}
              >
                {leaving ? 'Saving…' : 'Back'}
              </button>
            </div>
          </div>
        ) : (
          <>
            <div className="review-feedback-workspace__body" data-narrow={isNarrow}>
              {isNarrow ? (
                <>
                  <button
                    ref={openerRef}
                    type="button"
                    className="review-feedback-workspace__opener"
                    aria-expanded={drawerOpen}
                    aria-controls="review-feedback-scope-drawer"
                    onClick={() => setDrawerOpen(true)}
                  >
                    Repositories and filters
                  </button>
                  {feed}
                  {drawerOpen ? (
                    <ScopeDrawer title="Repositories and filters" onClose={closeDrawer}>
                      {panel}
                    </ScopeDrawer>
                  ) : null}
                </>
              ) : (
                <>
                  <nav className="review-feedback-workspace__rail" aria-label="Feedback scope">
                    {panel}
                  </nav>
                  {feed}
                </>
              )}
            </div>
            <footer className="review-feedback-workspace__footer">
              <label className="config-editor__gate review-feedback-workspace__gate">
                <input
                  type="checkbox"
                  checked={gate}
                  disabled={launching || frozen}
                  onChange={(event) => setGate(event.target.checked)}
                />
                <span className="config-editor__gate-text">
                  <b>Pause for Roadmap and Phase plan review</b>
                  <span>
                    Enabling this pauses the child at roadmap approval and again before
                    implementation, and applies to the parent and child together.
                  </span>
                </span>
              </label>
              <button
                type="button"
                className="launcher-modal__primary"
                disabled={saving || launching || frozen || counts.selected === 0}
                onClick={launch}
              >
                {launchLabel}
              </button>
            </footer>
          </>
        )
      ) : null}
      {detailComment !== null ? (
        <ReviewFeedbackDetailDialog
          comment={detailComment}
          onClose={() => setDetailComment(null)}
        />
      ) : null}
    </div>
  );
}
