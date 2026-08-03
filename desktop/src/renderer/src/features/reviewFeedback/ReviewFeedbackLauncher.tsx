/**
 * "Address review feedback" initialization modal. Fetches unaddressed
 * pull-request comments grouped by repository, lets the operator deselect
 * the ones to skip, and launches a review-feedback child pass with the
 * selected comment payloads plus an explicit Roadmap/Plan review gate. The
 * child starts unconditionally once its worktrees are ready — there is no
 * auto-start checkbox, repo dropdown, or mode selector.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  AttentionItem,
  FeatureSnapshot,
  FetchReviewFeedbackResult,
  ReviewFeedbackCommentView,
} from '../../../../shared/ipc';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { CycleFooter } from '../cycles/cycleShared';

type FetchState =
  | { phase: 'loading' }
  | { phase: 'ready'; result: FetchReviewFeedbackResult }
  | { phase: 'error'; error: WizardError };

export interface ReviewFeedbackLauncherProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onCancel(): void;
  onDispatched(launch: { childId: string; autoStart: boolean }): void;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}

function commentKey(comment: ReviewFeedbackCommentView): string {
  return `${comment.repo}:${comment.id}`;
}

const TYPE_LABEL: Record<ReviewFeedbackCommentView['type'], string> = {
  review: 'Review comment',
  issue: 'Issue',
  review_body: 'Review body',
};

export function ReviewFeedbackLauncher({
  featureId,
  snapshot,
  onCancel,
  onDispatched,
}: ReviewFeedbackLauncherProps): React.ReactElement {
  const [fetchState, setFetchState] = useState<FetchState>({ phase: 'loading' });
  // Seeded from the parent's Roadmap-review checkpoint; the launch sends
  // this value explicitly so the server does not fall back to inheriting it.
  const [gate, setGate] = useState(true);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [pending, setPending] = useState(false);
  const [formError, setFormError] = useState<WizardError | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(() => {
    setFetchState({ phase: 'loading' });
    setFormError(null);
    window.agentico
      .fetchReviewFeedback({ featureId })
      .then((result) => {
        setFetchState({ phase: 'ready', result });
        const all = new Set<string>();
        for (const group of result.repos) {
          for (const comment of group.comments) all.add(commentKey(comment));
        }
        setSelected(all);
      })
      .catch((err: unknown) => setFetchState({ phase: 'error', error: parseIpcError(err) }));
  }, [featureId]);

  useEffect(load, [load]);

  // Seed the gate toggle from the parent's current Roadmap-review setting.
  // A failed config fetch leaves the safe default (paused) in place.
  useEffect(() => {
    window.agentico
      .getFeatureConfig(featureId)
      .then((config) => setGate(config.current.checkpoints.roadmapReview))
      .catch(() => {});
  }, [featureId]);

  useEffect(() => {
    if (formError !== null) formErrorRef.current?.focus();
  }, [formError]);

  const readyResult = fetchState.phase === 'ready' ? fetchState.result : null;
  const totalComments = useMemo(
    () => readyResult?.repos.reduce((sum, group) => sum + group.comments.length, 0) ?? 0,
    [readyResult],
  );
  const selectedCount = selected.size;

  const toggleComment = (comment: ReviewFeedbackCommentView): void => {
    setSelected((current) => {
      const next = new Set(current);
      const key = commentKey(comment);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const setRepoSelection = (
    repo: string,
    comments: readonly ReviewFeedbackCommentView[],
    select: boolean,
  ): void => {
    setSelected((current) => {
      const next = new Set(current);
      for (const comment of comments) {
        const key = commentKey(comment);
        if (select) next.add(key);
        else next.delete(key);
      }
      return next;
    });
  };

  const submit = useCallback((): void => {
    if (pending || readyResult === null || selectedCount === 0) return;
    setFormError(null);
    setPending(true);
    const comments: ReviewFeedbackCommentView[] = [];
    for (const group of readyResult.repos) {
      for (const comment of group.comments) {
        if (selected.has(commentKey(comment))) comments.push(comment);
      }
    }
    void (async () => {
      try {
        const launched = await window.agentico.launchReviewFeedbackChild({
          parentId: featureId,
          comments,
          gate,
        });
        onDispatched({ childId: launched.childId, autoStart: true });
        onCancel();
      } catch (err) {
        setFormError(parseIpcError(err));
      } finally {
        setPending(false);
      }
    })();
  }, [featureId, gate, onCancel, onDispatched, pending, readyResult, selected, selectedCount]);

  return (
    <div className="review-feedback-modal" aria-label="Address review feedback">
      <p className="review-feedback-modal__lede">
        Review unaddressed pull-request feedback across {snapshot.name}&rsquo;s repositories and
        launch a child pass to address the comments you keep selected.
      </p>

      {formError !== null ? (
        <div ref={formErrorRef} tabIndex={-1} role="alert" className="create-form__error">
          <b className="create-form__error-code">{formError.code}</b>
          <p className="create-form__error-message">{formError.message}</p>
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
            className="cycle-journey__preflight-retry"
            disabled={pending}
            onClick={load}
          >
            Try again
          </button>
        </div>
      ) : null}

      {fetchState.phase === 'ready' ? (
        totalComments === 0 ? (
          <div className="review-feedback-modal__empty" role="status">
            <p>No unaddressed comments. Every repository is up to date.</p>
          </div>
        ) : (
          <div className="review-feedback-modal__groups" aria-label="Review feedback by repository">
            {fetchState.result.repos.map((group) => {
              const repoSelected = group.comments.filter((c) => selected.has(commentKey(c))).length;
              const allRepo = repoSelected === group.comments.length;
              return (
                <section key={group.repo} className="review-feedback-modal__group">
                  <header className="review-feedback-modal__group-header">
                    <h4 className="review-feedback-modal__group-title">{group.repo}</h4>
                    <a
                      href={group.prUrl}
                      target="_blank"
                      rel="noreferrer"
                      className="review-feedback-modal__pr-link"
                    >
                      View pull request
                    </a>
                    <button
                      type="button"
                      className="review-feedback-modal__select-toggle"
                      onClick={() => setRepoSelection(group.repo, group.comments, !allRepo)}
                      disabled={pending}
                    >
                      {allRepo ? 'Clear repo' : 'Select all'}
                    </button>
                  </header>
                  <ul className="review-feedback-modal__comments">
                    {group.comments.map((comment) => {
                      const key = commentKey(comment);
                      const checked = selected.has(key);
                      return (
                        <li
                          key={key}
                          className="review-feedback-modal__comment"
                          data-selected={checked}
                        >
                          <label className="review-feedback-modal__comment-label">
                            <input
                              type="checkbox"
                              checked={checked}
                              disabled={pending}
                              onChange={() => toggleComment(comment)}
                            />
                            <span className="review-feedback-modal__comment-body">
                              <span className="review-feedback-modal__comment-meta">
                                <b className="review-feedback-modal__comment-type">
                                  {TYPE_LABEL[comment.type]}
                                </b>
                                {comment.path !== undefined ? (
                                  <code className="review-feedback-modal__comment-path">
                                    {comment.path}
                                    {comment.line !== undefined ? `:${comment.line}` : ''}
                                  </code>
                                ) : null}
                                {comment.author !== undefined ? (
                                  <span className="review-feedback-modal__comment-author">
                                    {comment.author}
                                  </span>
                                ) : null}
                              </span>
                              {comment.body !== undefined ? (
                                <p className="review-feedback-modal__comment-text">
                                  {comment.body}
                                </p>
                              ) : null}
                              {comment.inReplyToId !== undefined ? (
                                <p className="review-feedback-modal__comment-reply">
                                  Reply to comment {comment.inReplyToId}
                                </p>
                              ) : null}
                            </span>
                          </label>
                        </li>
                      );
                    })}
                  </ul>
                </section>
              );
            })}
          </div>
        )
      ) : null}

      {fetchState.phase !== 'error' ? (
        <label className="config-editor__gate review-feedback-modal__gate">
          <input
            type="checkbox"
            checked={gate}
            disabled={pending}
            onChange={(event) => setGate(event.target.checked)}
          />
          <span className="config-editor__gate-text">
            <b>Pause for Roadmap and Phase plan review</b>
            <span>
              Enabling this pauses the child at roadmap approval and again before implementation,
              and applies to the parent and child together.
            </span>
          </span>
        </label>
      ) : null}

      <CycleFooter
        onCancel={onCancel}
        primaryLabel={
          pending
            ? 'Launching…'
            : fetchState.phase === 'loading'
              ? 'Fetching…'
              : selectedCount === 0
                ? 'Select comments to launch'
                : `Launch child${selectedCount > 0 ? ` (${selectedCount})` : ''}`
        }
        primaryDisabled={pending || fetchState.phase !== 'ready' || selectedCount === 0}
        busy={pending}
        onPrimary={submit}
      />
    </div>
  );
}
