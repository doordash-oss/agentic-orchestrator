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
 * returned revision. A 409 conflict (or any failed save) stops later batches,
 * refetches, and adopts the server's view. Selections hidden by scope or
 * filters are never part of a bulk target set. Launch sends only
 * `{expected_revision, gate}` and stays disabled while a save is pending;
 * Back waits for the pending save to commit before leaving, so a fresh
 * re-entry restores exactly the selection the user last saw. Scope, filters,
 * scroll, and the drawer reset to their defaults on every fresh entry.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FeatureSnapshot } from '../../../../shared/ipc';
import { useMediaQuery } from '../../hooks';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { bucketElapsedSince } from '../phaseRail';
import { ScopeDrawer } from './ScopeDrawer';
import { ScopePanel, type ScopeLedgerEntry } from './ScopePanel';
import {
  EMPTY_FILTERS,
  chunkSelectionUpdates,
  facetOptions,
  filtersActive,
  matchesFilters,
  pruneFiltersForScope,
  scopeGroups,
  type ReviewFeedbackFilters,
} from './feedbackFilters';
import {
  fetchReviewFeedbackDraft,
  launchReceiptText,
  launchReviewFeedbackDraft,
  saveReviewFeedbackSelection,
  type ReviewFeedbackDraftCommentView,
  type ReviewFeedbackDraftRepoGroup,
  type ReviewFeedbackSelectionUpdate,
} from './reviewFeedbackDraftApi';

type FetchState =
  { phase: 'loading' } | { phase: 'ready' } | { phase: 'error'; error: WizardError };

/** One unacknowledged edit; the sequence number orders stacked edits per ref. */
interface PendingSelection {
  value: boolean;
  seq: number;
}

export interface ReviewFeedbackWorkspaceProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onBack(): void;
  onDispatched(launch: { childId: string; receipt?: string }): void;
}

/** Humanized creation time, agreeing with the attention surfaces' wording. */
function formatCreatedAgo(createdAt: string | undefined): string | null {
  const bucket = bucketElapsedSince(createdAt);
  if (bucket === null) return null;
  switch (bucket.unit) {
    case 'sub-minute':
      return 'moments ago';
    case 'minutes':
      return `${bucket.value} minute${bucket.value === 1 ? '' : 's'} ago`;
    case 'hours':
      return `${bucket.value} hour${bucket.value === 1 ? '' : 's'} ago`;
    case 'days':
      return `${bucket.value} day${bucket.value === 1 ? '' : 's'} ago`;
  }
}

export function ReviewFeedbackWorkspace({
  featureId,
  snapshot,
  onBack,
  onDispatched,
}: ReviewFeedbackWorkspaceProps): React.ReactElement {
  const [fetchState, setFetchState] = useState<FetchState>({ phase: 'loading' });
  const [repos, setRepos] = useState<ReviewFeedbackDraftRepoGroup[]>([]);
  const [pending, setPending] = useState<ReadonlyMap<string, PendingSelection>>(new Map());
  const [scope, setScope] = useState<string>('all');
  const [filters, setFilters] = useState<ReviewFeedbackFilters>(EMPTY_FILTERS);
  const [laneError, setLaneError] = useState<WizardError | null>(null);
  const [gate, setGate] = useState(true);
  const [launching, setLaunching] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const feedRef = useRef<HTMLElement | null>(null);
  const openerRef = useRef<HTMLButtonElement | null>(null);

  const revisionRef = useRef(0);
  const pendingRef = useRef<ReadonlyMap<string, PendingSelection>>(new Map());
  const seqRef = useRef(0);
  // Bumped on every conflict/refetch so queued sends based on a stale draft
  // no-op instead of writing against a revision the user has already lost.
  const epochRef = useRef(0);
  const queueRef = useRef<Promise<void>>(Promise.resolve());

  const isNarrow = useMediaQuery('(max-width: 900px)');

  const mirrorPending = (next: ReadonlyMap<string, PendingSelection>): void => {
    pendingRef.current = next;
    setPending(next);
  };
  const mirrorRepos = setRepos;

  /** Adopt a server draft view wholesale: revision, repos, cleared overlay. */
  const adoptServerView = useCallback(
    (view: { revision: number; repos: ReviewFeedbackDraftRepoGroup[] }) => {
      revisionRef.current = view.revision;
      mirrorRepos(view.repos);
      mirrorPending(new Map());
      // Server order/scope is authoritative; a fresh view resets every
      // view-only piece of state to its documented default (All scope, no
      // filters, no drawer).
      setScope('all');
      setFilters(EMPTY_FILTERS);
      setDrawerOpen(false);
    },
    [],
  );

  const load = useCallback(() => {
    epochRef.current += 1;
    setFetchState({ phase: 'loading' });
    setLaneError(null);
    fetchReviewFeedbackDraft(featureId)
      .then((view) => {
        adoptServerView(view);
        setFetchState({ phase: 'ready' });
        feedRef.current?.scrollTo?.(0, 0);
      })
      .catch((err: unknown) => setFetchState({ phase: 'error', error: parseIpcError(err) }));
  }, [featureId, adoptServerView]);

  useEffect(load, [load]);

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

  /**
   * A failed save is never presented as committed: drop the overlay for every
   * edit based on the lost draft, surface the error, stop any queued bulk
   * batches, and reconcile the visible view against the server.
   */
  const handleSaveFailure = useCallback(
    (err: unknown, epoch: number): void => {
      if (epoch !== epochRef.current) return;
      epochRef.current += 1;
      const parsed = parseIpcError(err);
      setLaneError(parsed);
      setLeaving(false);
      fetchReviewFeedbackDraft(featureId)
        .then((view) => adoptServerView(view))
        .catch(() => {});
    },
    [featureId, adoptServerView],
  );

  const toggleComment = useCallback(
    (comment: ReviewFeedbackDraftCommentView, checked: boolean): void => {
      const seq = ++seqRef.current;
      const epoch = epochRef.current;
      const next = new Map(pendingRef.current);
      next.set(comment.stableRef, { value: !checked, seq });
      mirrorPending(next);
      setLaneError(null);
      queueRef.current = queueRef.current.then(async () => {
        if (epoch !== epochRef.current) return;
        await saveReviewFeedbackSelection({
          featureId,
          expectedRevision: revisionRef.current,
          updates: [{ stableRef: comment.stableRef, selected: !checked }],
        })
          .then((ack) => {
            if (epoch !== epochRef.current) return;
            revisionRef.current = ack.revision;
            mirrorRepos(ack.repos);
            const entry = pendingRef.current.get(comment.stableRef);
            if (entry?.seq === seq) {
              const cleared = new Map(pendingRef.current);
              cleared.delete(comment.stableRef);
              mirrorPending(cleared);
            }
          })
          .catch((err: unknown) => handleSaveFailure(err, epoch));
      });
    },
    [featureId, handleSaveFailure],
  );

  const selectedOf = useCallback(
    (comment: ReviewFeedbackDraftCommentView): boolean =>
      pending.get(comment.stableRef)?.value ?? comment.selected,
    [pending],
  );

  const counts = useMemo(() => {
    let selected = 0;
    let total = 0;
    for (const group of repos) {
      for (const comment of group.comments) {
        total += 1;
        if (selectedOf(comment)) selected += 1;
      }
    }
    return { selected, total };
  }, [repos, selectedOf]);

  const ledger = useMemo<ScopeLedgerEntry[]>(
    () => [
      { scope: 'all', label: 'All feedback', selected: counts.selected, total: counts.total },
      ...repos.map((group) => ({
        scope: group.repo,
        label: group.repo,
        total: group.comments.length,
        selected: group.comments.filter(selectedOf).length,
      })),
    ],
    [repos, counts, selectedOf],
  );

  /** Groups in the active repository scope, before filters. */
  const scopedGroups = useMemo(() => scopeGroups(repos, scope), [repos, scope]);
  const scopedCount = useMemo(
    () => scopedGroups.reduce((sum, group) => sum + group.comments.length, 0),
    [scopedGroups],
  );
  const options = useMemo(() => facetOptions(scopedGroups), [scopedGroups]);

  /** Per-section matches: filtering never reorders what it keeps. */
  const matchedSections = useMemo(
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

  /**
   * Bulk apply over the visible set, snapshotted at activation time: every
   * target flips optimistically, then bounded batches commit strictly in
   * order, each on the revision returned by the previous acknowledgement. A
   * failure stops later batches; already-acked batches stay authoritative.
   */
  const applyBulk = useCallback(
    (targets: ReviewFeedbackDraftCommentView[], selected: boolean): void => {
      if (targets.length === 0) return;
      const epoch = epochRef.current;
      const seqs = new Map<string, number>();
      const updates: ReviewFeedbackSelectionUpdate[] = [];
      const next = new Map(pendingRef.current);
      for (const comment of targets) {
        const seq = ++seqRef.current;
        seqs.set(comment.stableRef, seq);
        next.set(comment.stableRef, { value: selected, seq });
        updates.push({ stableRef: comment.stableRef, selected });
      }
      mirrorPending(next);
      setLaneError(null);
      const batches = chunkSelectionUpdates(updates);
      queueRef.current = queueRef.current.then(async () => {
        if (epoch !== epochRef.current) return;
        for (const batch of batches) {
          try {
            const ack = await saveReviewFeedbackSelection({
              featureId,
              expectedRevision: revisionRef.current,
              updates: batch,
            });
            if (epoch !== epochRef.current) return;
            revisionRef.current = ack.revision;
            mirrorRepos(ack.repos);
            const cleared = new Map(pendingRef.current);
            for (const update of batch) {
              if (cleared.get(update.stableRef)?.seq === seqs.get(update.stableRef)) {
                cleared.delete(update.stableRef);
              }
            }
            mirrorPending(cleared);
          } catch (err) {
            handleSaveFailure(err, epoch);
            return;
          }
        }
      });
    },
    [featureId, handleSaveFailure],
  );

  /** Repository scope is view state; selections in any scope remain untouched. */
  const changeScope = useCallback(
    (nextScope: string): void => {
      setScope(nextScope);
      setFilters((prev) => pruneFiltersForScope(scopeGroups(repos, nextScope), prev));
    },
    [repos],
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
  // successfully. A failed save clears `leaving` in the catch path instead.
  useEffect(() => {
    if (leaving && pending.size === 0 && laneError === null) onBack();
  }, [leaving, pending, laneError, onBack]);

  const requestBack = useCallback((): void => {
    if (pending.size === 0) {
      onBack();
      return;
    }
    setLeaving(true);
  }, [pending, onBack]);

  const launch = useCallback((): void => {
    if (saving || launching || fetchState.phase !== 'ready') return;
    setLaneError(null);
    setLaunching(true);
    launchReviewFeedbackDraft({
      parentId: featureId,
      expectedRevision: revisionRef.current,
      gate,
    })
      .then((result) => {
        const receipt = launchReceiptText(result);
        onDispatched({ childId: result.childId, ...(receipt === undefined ? {} : { receipt }) });
        onBack();
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        setLaneError(parsed);
        // A launch conflict means this draft view was stale; the draft itself
        // (and the committed selection) is preserved — refetch and adopt it.
        if (
          parsed.code === 'review_feedback_revision_conflict' ||
          parsed.code === 'review_feedback_draft_not_found'
        ) {
          fetchReviewFeedbackDraft(featureId)
            .then((view) => adoptServerView(view))
            .catch(() => {});
        }
      })
      .finally(() => setLaunching(false));
  }, [featureId, gate, saving, launching, fetchState.phase, onBack, onDispatched, adoptServerView]);

  const launchLabel = launching
    ? 'Launching…'
    : counts.selected === 0
      ? 'Select comments to launch'
      : `Launch child (${counts.selected})`;

  const activeFilters = filtersActive(filters);

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
      onSelectVisible={() => applyBulk(selectTargets, true)}
      onClearVisible={() => applyBulk(clearTargets, false)}
      launching={launching}
    />
  );

  const feed = (
    <main className="review-feedback-workspace__feed" aria-label="Review feedback" ref={feedRef}>
      <div className="review-feedback-feedbar">
        {activeFilters ? (
          <div className="review-feedback-feedbar__chips">
            <ul className="review-feedback-chips" aria-label="Active filters">
              {filters.authors.map((author) => (
                <li key={`author:${author}`}>
                  <button
                    type="button"
                    className="review-feedback-chip"
                    aria-label={`Remove author filter: ${author}`}
                    onClick={() => toggleAuthor(author)}
                  >
                    Author: {author}
                  </button>
                </li>
              ))}
              {filters.types.map((type) => (
                <li key={`type:${type}`}>
                  <button
                    type="button"
                    className="review-feedback-chip"
                    aria-label={`Remove comment type filter: ${COMMENT_TYPE_LABEL[type]}`}
                    onClick={() => toggleType(type)}
                  >
                    Type: {COMMENT_TYPE_LABEL[type]}
                  </button>
                </li>
              ))}
              {filters.path.trim() !== '' ? (
                <li key="path">
                  <button
                    type="button"
                    className="review-feedback-chip"
                    aria-label={`Remove path filter: ${filters.path.trim()}`}
                    onClick={() => onPathChange('')}
                  >
                    Path: {filters.path.trim()}
                  </button>
                </li>
              ) : null}
            </ul>
            <button type="button" className="review-feedback-feedbar__clear" onClick={clearFilters}>
              Clear all filters
            </button>
          </div>
        ) : null}
        <p className="review-feedback-feedbar__summary" aria-live="polite">
          {visibleCount} of {scopedCount} comments visible
        </p>
      </div>
      {visibleCount === 0 && activeFilters ? (
        <div className="review-feedback-feedbar__empty" role="status">
          <p>No comments match the active filters.</p>
          <button type="button" onClick={clearFilters}>
            Clear all filters
          </button>
        </div>
      ) : null}
      {matchedSections.map(({ group, comments }) => {
        const sectionSelected = comments.filter(selectedOf).length;
        return (
          <section key={group.repo} className="review-feedback-section" aria-label={group.repo}>
            <header className="review-feedback-section__header">
              <h3 className="review-feedback-section__title">{group.repo}</h3>
              <span className="review-feedback-section__ledger">
                {sectionSelected} of {group.comments.length} selected
              </span>
              {group.prUrl !== '' ? (
                <button
                  type="button"
                  className="review-feedback-section__pr"
                  onClick={() => void window.agentico.openExternal({ url: group.prUrl })}
                >
                  Open pull request
                </button>
              ) : null}
            </header>
            {comments.map((comment) => {
              const checked = selectedOf(comment);
              const created = formatCreatedAgo(comment.createdAt);
              return (
                <article
                  key={comment.stableRef}
                  className="review-feedback-card"
                  data-selected={checked}
                >
                  <label className="review-feedback-card__label">
                    <input
                      type="checkbox"
                      checked={checked}
                      disabled={launching}
                      onChange={() => toggleComment(comment, checked)}
                    />
                    <span className="review-feedback-card__body">
                      <span className="review-feedback-modal__comment-meta">
                        <b className="review-feedback-modal__comment-type">
                          {COMMENT_TYPE_LABEL[comment.type]}
                        </b>
                        {comment.author !== undefined ? (
                          <span className="review-feedback-modal__comment-author">
                            {comment.author}
                          </span>
                        ) : null}
                        {created !== null ? (
                          <span className="review-feedback-card__created">{created}</span>
                        ) : null}
                        {comment.path !== undefined ? (
                          <code className="review-feedback-modal__comment-path">
                            {comment.path}
                            {comment.line !== undefined ? `:${comment.line}` : ''}
                          </code>
                        ) : null}
                      </span>
                      {comment.body !== undefined ? (
                        <p className="review-feedback-modal__comment-text">{comment.body}</p>
                      ) : null}
                      {comment.diffHunk !== undefined ? (
                        <pre className="review-feedback-card__diff">{comment.diffHunk}</pre>
                      ) : null}
                      {comment.inReplyToId !== undefined ? (
                        <p className="review-feedback-modal__comment-reply">
                          Reply to comment {comment.inReplyToId}
                        </p>
                      ) : null}
                    </span>
                  </label>
                </article>
              );
            })}
          </section>
        );
      })}
    </main>
  );

  return (
    <div
      className="review-feedback-workspace"
      role="dialog"
      aria-modal="true"
      aria-label="Address review feedback"
    >
      <header className="review-feedback-workspace__header">
        <button
          type="button"
          className="review-feedback-workspace__back"
          onClick={requestBack}
          disabled={launching || leaving}
        >
          {leaving ? 'Saving…' : 'Back'}
        </button>
        <div className="review-feedback-workspace__heading">
          <h2 className="review-feedback-workspace__title">Address review feedback</h2>
          <p className="review-feedback-workspace__subtitle">
            Review unaddressed pull-request feedback across {snapshot.name}&rsquo;s repositories and
            launch a child pass with the comments you keep selected.
          </p>
          <p className="review-feedback-workspace__ledger" aria-live="polite">
            {counts.selected} of {counts.total} selected
            {saving ? ' · saving…' : ''}
          </p>
        </div>
      </header>

      {laneError !== null ? (
        <div role="alert" className="create-form__error">
          <b className="create-form__error-code">{laneError.code}</b>
          <p className="create-form__error-message">{laneError.message}</p>
        </div>
      ) : null}

      {fetchState.phase === 'loading' ? (
        <p role="status" className="review-feedback-modal__status">
          Fetching review feedback…
        </p>
      ) : null}

      {fetchState.phase === 'error' ? (
        <div className="review-feedback-modal__error">
          <p className="form-field__error" role="alert">
            {fetchState.error.message}
          </p>
          <button
            type="button"
            className="review-feedback-modal__retry"
            disabled={launching}
            onClick={load}
          >
            Try again
          </button>
        </div>
      ) : null}

      {fetchState.phase === 'ready' ? (
        counts.total === 0 ? (
          <div className="review-feedback-modal__empty" role="status">
            <p>No unaddressed comments. Every repository is up to date.</p>
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
              <label className="config-editor__gate review-feedback-modal__gate">
                <input
                  type="checkbox"
                  checked={gate}
                  disabled={launching}
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
                disabled={saving || launching || counts.selected === 0}
                onClick={launch}
              >
                {launchLabel}
              </button>
            </footer>
          </>
        )
      ) : null}
    </div>
  );
}
