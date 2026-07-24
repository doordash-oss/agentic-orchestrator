/**
 * Cycle journeys: rebase, review-comments, and refactor flows that reuse
 * the server's authoritative action catalogue and preflight/fetch endpoints.
 * Each journey confirms scope and impact before dispatch, shows per-repo
 * progress, and resolves NEED_USER_INPUT through the existing attention surface.
 */
import { useState, useCallback, useEffect, useRef } from 'react';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import type {
  AttentionItem,
  FeatureSnapshot,
  RebasePreflightResult,
  RebaseResult,
  ReviewCommentView,
  ReviewCommentsFetchResult,
  ReviewCommentsStartResult,
  RefactorResult,
} from '../../../shared/ipc';
import type { AftercareCycleId } from './aftercareModel';

type CyclePhase = 'idle' | 'dispatching' | 'active' | 'error';

const COMMENTS_PREVIEW_LIMIT = 10;
const COMMENT_BODY_LIMIT = 200;

function humanizeMode(mode: string): string {
  if (mode === 'auto') return 'Auto';
  if (mode === 'address_all') return 'Address all';
  return mode.charAt(0).toUpperCase() + mode.slice(1).replace(/_/g, ' ');
}

function humanizeFreshness(freshness: string): string {
  switch (freshness) {
    case 'up_to_date':
      return 'Up to date';
    case 'behind':
      return 'Behind main';
    case 'unknown':
      return 'Unknown';
    default:
      return freshness.charAt(0).toUpperCase() + freshness.slice(1).replace(/_/g, ' ');
  }
}

interface RebaseJourneyState {
  phase: CyclePhase;
  error: WizardError | null;
  result: RebaseResult | null;
}

interface ReviewCommentsJourneyState {
  phase: CyclePhase | 'fetching' | 'fetched';
  error: WizardError | null;
  repo: string;
  comments: ReviewCommentView[];
  revision?: string;
  modes: string[];
  selectedMode: string;
  result: ReviewCommentsStartResult | null;
}

interface RefactorJourneyState {
  phase: CyclePhase;
  error: WizardError | null;
  scope: 'one' | 'all' | '';
  repo: string;
  prompt: string;
  pipeline: string;
  result: RefactorResult | null;
}

export interface CycleJourneysProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onComplete: () => void;
  initialCycle?: AftercareCycleId;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}

export function CycleJourneys({
  featureId,
  snapshot,
  onComplete,
  initialCycle,
  attentionItems = [],
  onOpenGate,
}: CycleJourneysProps) {
  const featuredHeadingRef = useRef<HTMLHeadingElement>(null);
  const rebaseAction = snapshot.actions.find((a) => a.id === 'rebase');
  const reviewCommentsAction = snapshot.actions.find((a) => a.id === 'review-comments');
  const refactorAction = snapshot.actions.find((a) => a.id === 'refactor');

  const cycleGateItems = attentionItems.filter(
    (item): item is Extract<AttentionItem, { kind: 'gate' }> =>
      item.kind === 'gate' && item.cycleType !== undefined,
  );
  const hasCycleGate = cycleGateItems.length > 0 || snapshot.cycle?.status === 'need_user_input';

  const [rebaseState, setRebaseState] = useState<RebaseJourneyState>({
    phase: 'idle',
    error: null,
    result: null,
  });
  const [rebasePreflight, setRebasePreflight] = useState<RebasePreflightResult | null>(null);
  const [rebasePreflightLoading, setRebasePreflightLoading] = useState(false);
  const [rebasePreflightError, setRebasePreflightError] = useState<WizardError | null>(null);
  const [reviewState, setReviewState] = useState<ReviewCommentsJourneyState>({
    phase: 'idle',
    error: null,
    repo: '',
    comments: [],
    modes: [],
    selectedMode: 'auto',
    result: null,
  });
  const [refactorState, setRefactorState] = useState<RefactorJourneyState>({
    phase: 'idle',
    error: null,
    scope: '',
    repo: '',
    prompt: '',
    pipeline: '',
    result: null,
  });

  const refreshRebasePreflight = useCallback(async () => {
    setRebasePreflightLoading(true);
    setRebasePreflightError(null);
    try {
      const preflight = await window.agentico.preflightRebase({ featureId });
      setRebasePreflight(preflight);
    } catch (err) {
      setRebasePreflight(null);
      setRebasePreflightError(parseIpcError(err));
    } finally {
      setRebasePreflightLoading(false);
    }
  }, [featureId]);

  // Always refresh the authoritative rebase preflight before impact
  // confirmation so the user confirms fresh repository state, not a stale
  // snapshot. Execution rejects a mismatched source_revision before any
  // repository mutation.
  useEffect(() => {
    if (
      rebaseAction?.enabled === true &&
      rebaseState.phase === 'idle' &&
      rebasePreflight === null
    ) {
      void refreshRebasePreflight();
    }
  }, [rebaseAction, rebaseState.phase, rebasePreflight, refreshRebasePreflight]);

  const startRebase = useCallback(async () => {
    setRebaseState({ phase: 'dispatching', error: null, result: null });
    try {
      const result = await window.agentico.startRebase({
        featureId,
        ...(rebasePreflight !== null ? { sourceRevision: rebasePreflight.sourceRevision } : {}),
      });
      setRebaseState({ phase: 'active', error: null, result });
      onComplete();
    } catch (err) {
      setRebaseState({ phase: 'error', error: parseIpcError(err), result: null });
      // A stale or failed preflight should refresh so the user can retry
      // against fresh state instead of resubmitting the same stale snapshot.
      void refreshRebasePreflight();
    }
  }, [featureId, onComplete, rebasePreflight, refreshRebasePreflight]);

  const fetchComments = useCallback(async () => {
    setReviewState((prev) => ({ ...prev, phase: 'fetching', error: null }));
    try {
      const result: ReviewCommentsFetchResult = await window.agentico.fetchReviewComments({
        featureId,
        repo: reviewState.repo,
      });
      setReviewState({
        phase: 'fetched',
        error: null,
        repo: result.repo,
        comments: result.comments,
        revision: result.revision,
        modes: result.modes ?? ['auto'],
        selectedMode: result.modes?.[0] ?? 'auto',
        result: null,
      });
    } catch (err) {
      setReviewState((prev) => ({ ...prev, phase: 'error', error: parseIpcError(err) }));
    }
  }, [featureId, reviewState.repo]);

  const startReviewComments = useCallback(async () => {
    setReviewState((prev) => ({ ...prev, phase: 'dispatching', error: null }));
    try {
      const result = await window.agentico.startReviewComments({
        featureId,
        repo: reviewState.repo,
        mode: reviewState.selectedMode,
      });
      setReviewState((prev) => ({ ...prev, phase: 'active', error: null, result }));
      onComplete();
    } catch (err) {
      setReviewState((prev) => ({ ...prev, phase: 'error', error: parseIpcError(err) }));
    }
  }, [featureId, reviewState.repo, reviewState.selectedMode, onComplete]);

  const startRefactor = useCallback(async () => {
    setRefactorState((prev) => ({ ...prev, phase: 'dispatching', error: null }));
    try {
      const result = await window.agentico.startRefactor({
        featureId,
        prompt: refactorState.prompt,
        ...(refactorState.scope === 'one' ? { repo: refactorState.repo } : {}),
        ...(refactorState.pipeline !== '' ? { pipeline: refactorState.pipeline } : {}),
      });
      setRefactorState((prev) => ({ ...prev, phase: 'active', error: null, result }));
      onComplete();
    } catch (err) {
      setRefactorState((prev) => ({ ...prev, phase: 'error', error: parseIpcError(err) }));
    }
  }, [featureId, refactorState, onComplete]);

  const canRebase = rebaseAction?.enabled === true;
  const canReviewComments = reviewCommentsAction?.enabled === true;
  const canRefactor = refactorAction?.enabled === true;
  const pipelineOptions =
    refactorAction?.inputs?.find((input) => input.name === 'pipeline')?.options ?? [];

  useEffect(() => {
    if (initialCycle !== undefined) featuredHeadingRef.current?.focus();
  }, [initialCycle]);

  return (
    <section className="cockpit__cycles" aria-label="Repository cycles">
      {hasCycleGate ? (
        <div className="cycle-journey__gate" role="alert">
          <p className="cycle-journey__gate-heading">
            Waiting for your answers —{' '}
            {cycleGateItems.length > 0
              ? `${cycleGateItems.length} shared gate${cycleGateItems.length === 1 ? '' : 's'}`
              : 'shared gate'}{' '}
            across repositories
          </p>
          <p className="cycle-journey__gate-detail">
            A repository cycle is paused waiting for answers. Resolving this gate resumes the
            aggregate cycle across all participating repositories without duplicating work.
          </p>
          {onOpenGate !== undefined ? (
            <button
              type="button"
              className="cycle-journey__action"
              onClick={() => onOpenGate(featureId)}
            >
              Open gate resolution
            </button>
          ) : null}
        </div>
      ) : null}
      {canRebase ? (
        <div
          role="region"
          aria-label="Rebase cycle"
          className="cycle-journey cycle-journey--rebase"
          data-phase={rebaseState.phase}
          data-featured={initialCycle === 'rebase'}
        >
          <h4
            ref={initialCycle === 'rebase' ? featuredHeadingRef : undefined}
            className="cycle-journey__title"
            tabIndex={-1}
          >
            Rebase
          </h4>
          <p className="cycle-journey__description">
            Rebase every repository onto its target branch with a fresh guarded preflight.
          </p>
          {rebaseState.phase === 'idle' ? (
            <div className="cycle-journey__preflight" aria-label="Rebase preflight">
              <p className="cycle-journey__preflight-heading">
                {rebasePreflightLoading
                  ? 'Refreshing guarded preflight…'
                  : 'Affected repositories (fresh guarded preflight):'}
              </p>
              {rebasePreflight !== null ? (
                <ul className="cycle-journey__preflight-list">
                  {rebasePreflight.repos.map((repo) => (
                    <li key={repo.repo} className="cycle-journey__preflight-repo">
                      <span className="cycle-journey__preflight-repo-name">{repo.repo}</span>
                      <code className="cycle-journey__preflight-target">{repo.target}</code>
                      <span
                        className="cycle-journey__preflight-freshness"
                        data-freshness={repo.freshness}
                      >
                        {humanizeFreshness(repo.freshness)}
                      </span>
                      {repo.blocker !== undefined && repo.blocker !== '' ? (
                        <span className="cycle-journey__preflight-blocker">{repo.blocker}</span>
                      ) : null}
                      {repo.conflictFiles !== undefined && repo.conflictFiles.length > 0 ? (
                        <span className="cycle-journey__preflight-blocker">
                          {repo.conflictFiles.length} conflict
                          {repo.conflictFiles.length === 1 ? '' : 's'}
                        </span>
                      ) : null}
                    </li>
                  ))}
                </ul>
              ) : rebasePreflightError !== null ? (
                <div className="cycle-journey__preflight-error">
                  <p className="form-field__error" role="alert">
                    {rebasePreflightError.message}
                  </p>
                  <button
                    type="button"
                    className="cycle-journey__preflight-retry"
                    disabled={rebasePreflightLoading}
                    onClick={() => void refreshRebasePreflight()}
                  >
                    {rebasePreflightLoading ? 'Refreshing…' : 'Refresh preflight'}
                  </button>
                </div>
              ) : !rebasePreflightLoading ? (
                <div className="cycle-journey__preflight-error">
                  <p className="cycle-journey__preflight-note">
                    Preflight unavailable. Refresh to load the current repository state.
                  </p>
                  <button
                    type="button"
                    className="cycle-journey__preflight-retry"
                    onClick={() => void refreshRebasePreflight()}
                  >
                    Refresh preflight
                  </button>
                </div>
              ) : null}
              <p className="cycle-journey__preflight-note">
                Confirm to rebase all listed repositories onto their target branches. Execution
                rejects a stale preflight before any mutation.
              </p>
            </div>
          ) : null}
          {rebaseState.error !== null ? (
            <p className="form-field__error" role="alert">
              {rebaseState.error.message}
            </p>
          ) : null}
          {rebaseState.result !== null ? (
            <p className="cycle-journey__result" role="status">
              Rebase dispatched: {rebaseState.result.cycleType} ({rebaseState.result.result})
            </p>
          ) : null}
          <button
            type="button"
            className="cycle-journey__action"
            disabled={rebaseState.phase === 'dispatching' || rebasePreflight === null}
            onClick={() => void startRebase()}
          >
            {rebaseState.phase === 'dispatching'
              ? 'Dispatching…'
              : rebasePreflight === null
                ? 'Loading preflight…'
                : 'Start rebase'}
          </button>
        </div>
      ) : null}

      {canReviewComments ? (
        <div
          role="region"
          aria-label="Review comments cycle"
          className="cycle-journey cycle-journey--review-comments"
          data-phase={reviewState.phase}
          data-featured={initialCycle === 'review-comments'}
        >
          <h4
            ref={initialCycle === 'review-comments' ? featuredHeadingRef : undefined}
            className="cycle-journey__title"
            tabIndex={-1}
          >
            Review comments
          </h4>
          <p className="cycle-journey__description">
            Select a repository, fetch its current review comments, inspect the bounded preview, and
            confirm the exact scope before start.
          </p>
          <div className="cycle-journey__form">
            <label className="form-field">
              <span className="form-field__label">Repository</span>
              <select
                value={reviewState.repo}
                onChange={(e) =>
                  setReviewState((prev) => ({
                    ...prev,
                    repo: e.target.value,
                    phase: 'idle',
                    comments: [],
                  }))
                }
                disabled={reviewState.phase === 'fetching' || reviewState.phase === 'dispatching'}
              >
                <option value="">Select a repository…</option>
                {snapshot.repos.map((repo) => (
                  <option key={repo} value={repo}>
                    {repo}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className="cycle-journey__fetch"
              disabled={
                reviewState.repo === '' ||
                reviewState.phase === 'fetching' ||
                reviewState.phase === 'dispatching'
              }
              onClick={() => void fetchComments()}
            >
              {reviewState.phase === 'fetching' ? 'Fetching…' : 'Fetch comments'}
            </button>
          </div>
          {reviewState.comments.length > 0 ? (
            <div className="cycle-journey__comments-preview" aria-label="Review comments preview">
              <p className="cycle-journey__comments-count">
                {reviewState.comments.length} comment
                {reviewState.comments.length === 1 ? '' : 's'}
                {reviewState.revision !== undefined
                  ? ` at ${reviewState.revision.slice(0, 8)}`
                  : ''}
              </p>
              <ul className="cycle-journey__comments-list">
                {reviewState.comments.slice(0, COMMENTS_PREVIEW_LIMIT).map((comment) => (
                  <li key={comment.id} className="cycle-journey__comment">
                    {comment.file !== undefined ? (
                      <code className="cycle-journey__comment-file">
                        {comment.file}
                        {comment.line !== undefined ? `:${comment.line}` : ''}
                      </code>
                    ) : null}
                    {comment.author !== undefined ? (
                      <span className="cycle-journey__comment-author">{comment.author}</span>
                    ) : null}
                    {comment.body !== undefined ? (
                      <p className="cycle-journey__comment-body">
                        {comment.body.slice(0, COMMENT_BODY_LIMIT)}
                        {comment.body.length > COMMENT_BODY_LIMIT ? '…' : ''}
                      </p>
                    ) : null}
                  </li>
                ))}
              </ul>
              {reviewState.comments.length > COMMENTS_PREVIEW_LIMIT ? (
                <p className="cycle-journey__comments-more">
                  {reviewState.comments.length - COMMENTS_PREVIEW_LIMIT} more…
                </p>
              ) : null}
              <label className="form-field">
                <span className="form-field__label">Mode</span>
                <select
                  value={reviewState.selectedMode}
                  onChange={(e) =>
                    setReviewState((prev) => ({ ...prev, selectedMode: e.target.value }))
                  }
                  disabled={reviewState.phase === 'dispatching'}
                >
                  {reviewState.modes.map((mode) => (
                    <option key={mode} value={mode}>
                      {humanizeMode(mode)}
                    </option>
                  ))}
                </select>
              </label>
              <button
                type="button"
                className="cycle-journey__action"
                disabled={reviewState.phase === 'dispatching'}
                onClick={() => void startReviewComments()}
              >
                {reviewState.phase === 'dispatching'
                  ? 'Starting…'
                  : `Start review-comments on ${reviewState.repo}`}
              </button>
            </div>
          ) : reviewState.phase === 'fetched' ? (
            <p className="cycle-journey__empty">No review comments found for this repository.</p>
          ) : null}
          {reviewState.error !== null ? (
            <p className="form-field__error" role="alert">
              {reviewState.error.message}
            </p>
          ) : null}
          {reviewState.result !== null ? (
            <p className="cycle-journey__result" role="status">
              Review-comments dispatched: {reviewState.result.cycleType} (
              {reviewState.result.result})
            </p>
          ) : null}
        </div>
      ) : null}

      {canRefactor ? (
        <div
          role="region"
          aria-label="Refactor cycle"
          className="cycle-journey cycle-journey--refactor"
          data-phase={refactorState.phase}
          data-featured={initialCycle === 'refactor'}
        >
          <h4
            ref={initialCycle === 'refactor' ? featuredHeadingRef : undefined}
            className="cycle-journey__title"
            tabIndex={-1}
          >
            Refactor
          </h4>
          <p className="cycle-journey__description">
            Requires an explicit single-repository or all-repositories choice, a prompt, and
            optional pipeline. All-repositories resolves to named repositories before confirmation.
          </p>
          <div className="cycle-journey__form">
            <fieldset className="cycle-journey__scope">
              <legend>Scope</legend>
              <label>
                <input
                  type="radio"
                  name="refactor-scope"
                  value="one"
                  checked={refactorState.scope === 'one'}
                  onChange={() => setRefactorState((prev) => ({ ...prev, scope: 'one' }))}
                  disabled={refactorState.phase === 'dispatching'}
                />
                One repository
              </label>
              <label>
                <input
                  type="radio"
                  name="refactor-scope"
                  value="all"
                  checked={refactorState.scope === 'all'}
                  onChange={() => setRefactorState((prev) => ({ ...prev, scope: 'all' }))}
                  disabled={refactorState.phase === 'dispatching'}
                />
                All repositories ({snapshot.repos.length})
              </label>
            </fieldset>
            {refactorState.scope === 'one' ? (
              <label className="form-field">
                <span className="form-field__label">Repository</span>
                <select
                  value={refactorState.repo}
                  onChange={(e) => setRefactorState((prev) => ({ ...prev, repo: e.target.value }))}
                  disabled={refactorState.phase === 'dispatching'}
                >
                  <option value="">Select a repository…</option>
                  {snapshot.repos.map((repo) => (
                    <option key={repo} value={repo}>
                      {repo}
                    </option>
                  ))}
                </select>
              </label>
            ) : null}
            {refactorState.scope === 'all' ? (
              <p className="cycle-journey__resolved-repos">
                Applies to: {snapshot.repos.join(', ')}
              </p>
            ) : null}
            <label className="form-field">
              <span className="form-field__label">Prompt</span>
              <textarea
                value={refactorState.prompt}
                onChange={(e) => setRefactorState((prev) => ({ ...prev, prompt: e.target.value }))}
                disabled={refactorState.phase === 'dispatching'}
                rows={3}
                maxLength={4000}
                placeholder="Describe the refactoring work…"
              />
            </label>
            <label className="form-field">
              <span className="form-field__label">Pipeline (optional)</span>
              <select
                value={refactorState.pipeline}
                onChange={(e) =>
                  setRefactorState((prev) => ({ ...prev, pipeline: e.target.value }))
                }
                disabled={refactorState.phase === 'dispatching'}
              >
                <option value="">Default</option>
                {pipelineOptions.map((option) => (
                  <option key={option} value={option}>
                    {option.charAt(0).toUpperCase() + option.slice(1)}
                  </option>
                ))}
              </select>
            </label>
          </div>
          {refactorState.error !== null ? (
            <p className="form-field__error" role="alert">
              {refactorState.error.message}
            </p>
          ) : null}
          {refactorState.result !== null ? (
            <p className="cycle-journey__result" role="status">
              Refactor dispatched: {refactorState.result.cycleType} ({refactorState.result.result})
            </p>
          ) : null}
          <button
            type="button"
            className="cycle-journey__action"
            disabled={
              refactorState.phase === 'dispatching' ||
              refactorState.scope === '' ||
              refactorState.prompt.trim() === '' ||
              (refactorState.scope === 'one' && refactorState.repo === '')
            }
            onClick={() => void startRefactor()}
          >
            {refactorState.phase === 'dispatching'
              ? 'Dispatching…'
              : `Start refactor${refactorState.scope === 'all' ? ' (all repos)' : ''}`}
          </button>
        </div>
      ) : null}
    </section>
  );
}
