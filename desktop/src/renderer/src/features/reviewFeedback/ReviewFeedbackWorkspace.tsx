/**
 * "Address review feedback" full-screen workspace. Replaces the old modal: it
 * swaps the cockpit content for a two-pane picker backed by the server-owned
 * durable pending draft. The left rail filters the feed by repository scope,
 * the feed renders cards in the server's order, and every checkbox toggle
 * commits a reference-only selection mutation against the draft.
 *
 * Selection semantics: toggles apply to the visible choice immediately and
 * are serialized through a promise queue — each request carries the revision
 * acknowledged by the previous successful mutation, and unacknowledged edits
 * sit in an overlay the server acknowledgement never overwrites. A 409
 * conflict (or any failed save) refetches and adopts the server's view. Launch
 * sends only `{expected_revision, gate}` and stays disabled while a save is
 * pending; Back waits for the pending save to commit before leaving, so a
 * fresh re-entry restores exactly the selection the user last saw.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FeatureSnapshot } from '../../../../shared/ipc';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { bucketElapsedSince } from '../phaseRail';
import {
  fetchReviewFeedbackDraft,
  launchReceiptText,
  launchReviewFeedbackDraft,
  saveReviewFeedbackSelection,
  type ReviewFeedbackDraftCommentView,
  type ReviewFeedbackDraftRepoGroup,
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
  const [laneError, setLaneError] = useState<WizardError | null>(null);
  const [gate, setGate] = useState(true);
  const [launching, setLaunching] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const feedRef = useRef<HTMLElement | null>(null);

  const revisionRef = useRef(0);
  const pendingRef = useRef<ReadonlyMap<string, PendingSelection>>(new Map());
  const seqRef = useRef(0);
  // Bumped on every conflict/refetch so queued sends based on a stale draft
  // no-op instead of writing against a revision the user has already lost.
  const epochRef = useRef(0);
  const queueRef = useRef<Promise<void>>(Promise.resolve());

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
      // Server order/scope is authoritative; a fresh view resets to All.
      setScope('all');
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
          .catch((err: unknown) => {
            if (epoch !== epochRef.current) return;
            // A failed save is never presented as committed: drop the overlay
            // for every edit based on the lost draft, surface the error, and
            // reconcile the visible view against the server.
            epochRef.current += 1;
            const parsed = parseIpcError(err);
            setLaneError(parsed);
            setLeaving(false);
            fetchReviewFeedbackDraft(featureId)
              .then((view) => adoptServerView(view))
              .catch(() => {});
          });
      });
    },
    [featureId, adoptServerView],
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

  const repoCounts = useMemo(
    () =>
      repos.map((group) => ({
        repo: group.repo,
        total: group.comments.length,
        selected: group.comments.filter(selectedOf).length,
      })),
    [repos, selectedOf],
  );

  const visibleRepos = useMemo(
    () => (scope === 'all' ? repos : repos.filter((group) => group.repo === scope)),
    [repos, scope],
  );

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
            <div className="review-feedback-workspace__body">
              <nav className="review-feedback-workspace__rail" aria-label="Feedback scope">
                <button
                  type="button"
                  className="review-feedback-workspace__scope"
                  data-active={scope === 'all'}
                  onClick={() => setScope('all')}
                >
                  <span className="review-feedback-workspace__scope-name">All feedback</span>
                  <span className="review-feedback-workspace__scope-count">
                    {counts.selected}/{counts.total}
                  </span>
                </button>
                {repoCounts.map((entry) => (
                  <button
                    key={entry.repo}
                    type="button"
                    className="review-feedback-workspace__scope"
                    data-active={scope === entry.repo}
                    onClick={() => setScope(entry.repo)}
                  >
                    <span className="review-feedback-workspace__scope-name">{entry.repo}</span>
                    <span className="review-feedback-workspace__scope-count">
                      {entry.selected}/{entry.total}
                    </span>
                  </button>
                ))}
              </nav>
              <main
                className="review-feedback-workspace__feed"
                aria-label="Review feedback"
                ref={feedRef}
              >
                {visibleRepos.map((group) =>
                  group.comments.map((comment) => {
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
                              <b className="review-feedback-card__repo">{comment.repo}</b>
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
                  }),
                )}
              </main>
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
