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
 * returned revision. A visible choice is therefore always committed, saving,
 * or unsaved: a non-conflict save failure keeps the failed batch and every
 * unsent batch visible as unsaved local choices over the last acknowledged
 * revision, stops the queue, freezes selection/bulk/gate/Launch/Back, and
 * focuses a recovery alert offering `Retry save` (outstanding stable
 * references in their original order, bounded batches, from the latest
 * acknowledged revision) or `Reload saved selections` (fetch the
 * authoritative draft first, then deliberately discard the overlay and reset
 * the view). A typed revision conflict never replays local choices over the
 * other writer: it bumps the epoch, refetches, adopts the committed view,
 * and focuses a conflict explanation. Selections hidden by scope or
 * filters are never part of a bulk target set. Launch sends only
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
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { bucketElapsedSince } from '../phaseRail';
import { ScopeDrawer } from './ScopeDrawer';
import { ScopePanel, type ScopeLedgerEntry } from './ScopePanel';
import { ReviewFeedbackDiff, needsExpansion } from './ReviewFeedbackDiff';
import { ReviewFeedbackMarkdown } from './ReviewFeedbackMarkdown';
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
  // Recoverable save failure: the overlay keeps the failed and unsent local
  // choices visible as unsaved; selection/bulk/gate/Launch/Back stay frozen
  // until `Retry save` or `Reload saved selections` resolves the state.
  const [saveFailure, setSaveFailure] = useState<WizardError | null>(null);
  // A typed revision conflict reloaded the committed view; purely explanatory.
  const [conflictNotice, setConflictNotice] = useState<WizardError | null>(null);
  // A launch dispatch that failed before durable creation; Launch re-enables.
  const [launchError, setLaunchError] = useState<WizardError | null>(null);
  const [retrying, setRetrying] = useState(false);
  // Polite one-shot status after a deliberate reload succeeds.
  const [reloaded, setReloaded] = useState(false);
  const [gate, setGate] = useState(true);
  const [launching, setLaunching] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  // Expanded feedback cards, keyed by stable comment reference for the
  // current entry: survives scope/filter hiding and showing, resets with the
  // rest of the ephemeral card state whenever a fresh server view arrives.
  const [expandedRefs, setExpandedRefs] = useState<ReadonlySet<string>>(new Set());
  const feedRef = useRef<HTMLElement | null>(null);
  const openerRef = useRef<HTMLButtonElement | null>(null);
  const saveFailureAlertRef = useRef<HTMLDivElement | null>(null);
  const conflictAlertRef = useRef<HTMLDivElement | null>(null);
  const launchAlertRef = useRef<HTMLDivElement | null>(null);

  const revisionRef = useRef(0);
  const pendingRef = useRef<ReadonlyMap<string, PendingSelection>>(new Map());
  const seqRef = useRef(0);
  // Bumped on every conflict/refetch so queued sends based on a stale draft
  // no-op instead of writing against a revision the user has already lost.
  const epochRef = useRef(0);
  const queueRef = useRef<Promise<void>>(Promise.resolve());
  // Mirrored so queued lane steps and guarded callbacks see the freeze
  // synchronously, without re-creating every callback per failure.
  const saveFailureRef = useRef<WizardError | null>(null);
  const mirrorSaveFailure = (next: WizardError | null): void => {
    saveFailureRef.current = next;
    setSaveFailure(next);
  };

  // Actionable alerts own focus; background saving only announces politely.
  useEffect(() => {
    if (saveFailure !== null) saveFailureAlertRef.current?.focus();
  }, [saveFailure]);
  useEffect(() => {
    if (conflictNotice !== null) conflictAlertRef.current?.focus();
  }, [conflictNotice]);
  useEffect(() => {
    if (launchError !== null) launchAlertRef.current?.focus();
  }, [launchError]);

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
      setExpandedRefs(new Set());
    },
    [],
  );

  const toggleExpanded = useCallback((stableRef: string): void => {
    setExpandedRefs((prev) => {
      const next = new Set(prev);
      if (next.has(stableRef)) {
        next.delete(stableRef);
      } else {
        next.add(stableRef);
      }
      return next;
    });
  }, []);

  const load = useCallback(() => {
    epochRef.current += 1;
    setFetchState({ phase: 'loading' });
    mirrorSaveFailure(null);
    setConflictNotice(null);
    setLaunchError(null);
    setRetrying(false);
    setReloaded(false);
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
   * Two distinct failure contracts:
   *
   * A typed revision conflict means another writer committed first: the local
   * choices are never replayed over their view — the epoch invalidates queued
   * sends, the overlay is dropped, the committed draft is reloaded, and a
   * conflict explanation takes focus.
   *
   * Any other failure keeps the failed batch and every unsent batch in the
   * overlay as unsaved local choices over the last acknowledged revision.
   * The queue stops (queued sends check `saveFailureRef` and no-op), no
   * refetch adopts the server view, and the recovery alert offers the only
   * two ways forward.
   */
  const handleSaveFailure = useCallback(
    (err: unknown, epoch: number): void => {
      if (epoch !== epochRef.current) return;
      const parsed = parseIpcError(err);
      setLeaving(false);
      if (
        parsed.code === 'review_feedback_revision_conflict' ||
        parsed.code === 'review_feedback_draft_not_found'
      ) {
        epochRef.current += 1;
        mirrorSaveFailure(null);
        setRetrying(false);
        setReloaded(false);
        setConflictNotice(parsed);
        fetchReviewFeedbackDraft(featureId)
          .then((view) => adoptServerView(view))
          .catch(() => {});
        return;
      }
      mirrorSaveFailure(parsed);
    },
    [featureId, adoptServerView],
  );

  /**
   * Retry the outstanding unsaved choices: the overlay ordered by edit
   * sequence (so later edits win per reference, original ordering otherwise),
   * re-batched at the 512-update bound, starting from the latest acknowledged
   * revision. A transient failure lands back in the same recoverable state; a
   * conflict converges via `handleSaveFailure`.
   */
  const retrySave = useCallback((): void => {
    const outstanding = [...pendingRef.current.entries()]
      .map(([stableRef, entry]) => ({ stableRef, selected: entry.value, seq: entry.seq }))
      .sort((a, b) => a.seq - b.seq);
    if (outstanding.length === 0) {
      mirrorSaveFailure(null);
      return;
    }
    const seqs = new Map(outstanding.map((entry) => [entry.stableRef, entry.seq]));
    const epoch = epochRef.current;
    const updates: ReviewFeedbackSelectionUpdate[] = outstanding.map(({ stableRef, selected }) => ({
      stableRef,
      selected,
    }));
    const batches = chunkSelectionUpdates(updates);
    setRetrying(true);
    queueRef.current = queueRef.current.then(async () => {
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
          setRetrying(false);
          handleSaveFailure(err, epoch);
          return;
        }
      }
      if (epoch !== epochRef.current) return;
      setRetrying(false);
      mirrorSaveFailure(null);
    });
  }, [featureId, handleSaveFailure]);

  /**
   * Deliberately abandon the unsaved overlay: fetch the authoritative draft
   * first, and only on success discard the overlay, reset the view (All
   * feedback scope, cleared filters/expansion, feed top), and announce the
   * reload politely. A failed fetch keeps the unsaved choices and recovery
   * actions exactly as they were.
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
        mirrorSaveFailure(null);
        setConflictNotice(null);
        setLaunchError(null);
        setReloaded(true);
        feedRef.current?.scrollTo?.(0, 0);
      })
      .catch(() => {})
      .finally(() => setRetrying(false));
  }, [featureId, adoptServerView]);

  const toggleComment = useCallback(
    (comment: ReviewFeedbackDraftCommentView, checked: boolean): void => {
      if (saveFailureRef.current !== null) return;
      const seq = ++seqRef.current;
      const epoch = epochRef.current;
      const next = new Map(pendingRef.current);
      next.set(comment.stableRef, { value: !checked, seq });
      mirrorPending(next);
      setReloaded(false);
      queueRef.current = queueRef.current.then(async () => {
        if (epoch !== epochRef.current || saveFailureRef.current !== null) return;
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
      if (targets.length === 0 || saveFailureRef.current !== null) return;
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
      setReloaded(false);
      const batches = chunkSelectionUpdates(updates);
      queueRef.current = queueRef.current.then(async () => {
        if (epoch !== epochRef.current || saveFailureRef.current !== null) return;
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
  // successfully. A failed save clears `leaving` in the catch path instead,
  // and unresolved unsaved choices can never leave.
  useEffect(() => {
    if (leaving && pending.size === 0 && saveFailure === null) onBack();
  }, [leaving, pending, saveFailure, onBack]);

  const requestBack = useCallback((): void => {
    if (saveFailure !== null) return;
    if (pending.size === 0) {
      onBack();
      return;
    }
    setLeaving(true);
  }, [pending, saveFailure, onBack]);

  const launch = useCallback((): void => {
    if (saving || launching || saveFailure !== null || fetchState.phase !== 'ready') return;
    setLaunchError(null);
    setConflictNotice(null);
    setReloaded(false);
    setLaunching(true);
    launchReviewFeedbackDraft({
      parentId: featureId,
      expectedRevision: revisionRef.current,
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
        // (and the committed selection) is preserved — refetch and adopt it.
        if (
          parsed.code === 'review_feedback_revision_conflict' ||
          parsed.code === 'review_feedback_draft_not_found'
        ) {
          epochRef.current += 1;
          setConflictNotice(parsed);
          fetchReviewFeedbackDraft(featureId)
            .then((view) => adoptServerView(view))
            .catch(() => {});
          return;
        }
        setLaunchError(parsed);
      })
      .finally(() => setLaunching(false));
  }, [
    featureId,
    gate,
    saving,
    launching,
    saveFailure,
    fetchState.phase,
    onBack,
    onDispatched,
    adoptServerView,
  ]);

  const launchLabel =
    saveFailure !== null
      ? 'Unsaved choices — retry or reload'
      : launching
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
      mutationsDisabled={saveFailure !== null}
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
              const expanded = expandedRefs.has(comment.stableRef);
              const collapsible = needsExpansion(comment);
              const unsaved = saveFailure !== null && pending.has(comment.stableRef);
              const contentId = `review-feedback-content-${comment.stableRef.replace(/[^\w-]/g, '-')}`;
              // The selection checkbox is a sibling of — never an ancestor of
              // — rich content, so links, image actions, task items, and the
              // expansion control can never toggle selection.
              const selectLabel =
                comment.body !== undefined && comment.body.trim() !== ''
                  ? `Select feedback: ${comment.body.length > 160 ? `${comment.body.slice(0, 160)}…` : comment.body}`
                  : `Select feedback: ${COMMENT_TYPE_LABEL[comment.type]}${comment.author !== undefined ? ` by ${comment.author}` : ''}`;
              return (
                <article
                  key={comment.stableRef}
                  className="review-feedback-card"
                  data-selected={checked}
                  data-unsaved={unsaved}
                >
                  <input
                    type="checkbox"
                    className="review-feedback-card__select"
                    aria-label={selectLabel}
                    checked={checked}
                    disabled={launching || saveFailure !== null}
                    onChange={() => toggleComment(comment, checked)}
                  />
                  {unsaved ? (
                    <span className="review-feedback-card__unsaved">Unsaved choice</span>
                  ) : null}
                  <div className="review-feedback-card__body">
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
                    <div
                      id={contentId}
                      className="review-feedback-card__content"
                      data-collapsed={collapsible && !expanded}
                    >
                      {comment.body !== undefined ? (
                        <ReviewFeedbackMarkdown text={comment.body} />
                      ) : null}
                      {comment.diffHunk !== undefined ? (
                        <ReviewFeedbackDiff text={comment.diffHunk} />
                      ) : null}
                    </div>
                    {collapsible ? (
                      <button
                        type="button"
                        className="review-feedback-card__expand"
                        aria-expanded={expanded}
                        aria-controls={contentId}
                        onClick={(event) => {
                          event.stopPropagation();
                          toggleExpanded(comment.stableRef);
                        }}
                      >
                        {expanded ? 'Show less' : 'Show full feedback'}
                      </button>
                    ) : null}
                    {comment.inReplyToId !== undefined ? (
                      <p className="review-feedback-modal__comment-reply">
                        Reply to comment {comment.inReplyToId}
                      </p>
                    ) : null}
                  </div>
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
            launch a child pass with the comments you keep selected.
          </p>
          <p className="review-feedback-workspace__ledger" aria-live="polite">
            {counts.selected} of {counts.total} selected
            {saving && saveFailure === null ? ' · saving…' : ''}
            {saveFailure !== null ? ' · includes unsaved choices' : ''}
          </p>
        </div>
      </header>

      {launching ? (
        <p className="sr-only" role="status">
          Launching child pass…
        </p>
      ) : null}

      {saveFailure !== null ? (
        <div
          role="alert"
          className="create-form__error review-feedback-recovery"
          ref={saveFailureAlertRef}
          tabIndex={-1}
        >
          <b className="create-form__error-code">Choices not saved</b>
          <p className="create-form__error-message">
            Your latest selection changes could not be saved. They stay visible below as unsaved
            choices over the last saved view, and nothing else can change until you save them or
            deliberately reload the saved selections.
          </p>
          <p className="review-feedback-recovery__detail">
            Error detail: {saveFailure.code}
            {saveFailure.message !== '' ? ` — ${saveFailure.message}` : ''}
          </p>
          <div className="review-feedback-recovery__actions">
            <button type="button" disabled={retrying} onClick={retrySave}>
              Retry save
            </button>
            <button type="button" disabled={retrying} onClick={reloadSavedSelections}>
              Reload saved selections
            </button>
          </div>
        </div>
      ) : null}

      {conflictNotice !== null ? (
        <div role="alert" className="create-form__error" ref={conflictAlertRef} tabIndex={-1}>
          <b className="create-form__error-code">Selections reloaded</b>
          <p className="create-form__error-message">
            Another writer committed changes to this draft first. Your selections were reloaded from
            their committed view; any unsent edits were discarded and the workspace is ready for new
            choices.
          </p>
          <p className="review-feedback-recovery__detail">
            Error detail: {conflictNotice.code}
            {conflictNotice.message !== '' ? ` — ${conflictNotice.message}` : ''}
          </p>
        </div>
      ) : null}

      {launchError !== null ? (
        <div role="alert" className="create-form__error" ref={launchAlertRef} tabIndex={-1}>
          <b className="create-form__error-code">Launch failed</b>
          <p className="create-form__error-message">
            The child pass could not be launched. Your saved selections are unchanged; fix the issue
            and choose Launch again. {launchError.message}
          </p>
        </div>
      ) : null}

      {reloaded ? (
        <p role="status" className="review-feedback-workspace__ledger">
          Saved selections reloaded; view reset to All feedback.
        </p>
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
                  disabled={launching || saveFailure !== null}
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
                disabled={saving || launching || saveFailure !== null || counts.selected === 0}
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
