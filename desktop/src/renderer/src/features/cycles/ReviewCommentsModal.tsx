import { useCallback, useEffect, useMemo, useState } from 'react';
import type { AttentionItem, FeatureSnapshot, ReviewCommentView } from '../../../../shared/ipc';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { CycleFooter, CycleGateNotice, type CyclePhase } from './cycleShared';

const COMMENTS_PREVIEW_LIMIT = 10;
const COMMENT_BODY_LIMIT = 200;

function humanizeMode(mode: string): string {
  if (mode === 'auto') return 'Auto';
  if (mode === 'address_all') return 'Address all';
  return mode.charAt(0).toUpperCase() + mode.slice(1).replace(/_/g, ' ');
}

function feedbackType(type?: string): { label: string; variant: string } {
  switch (type) {
    case 'issue':
      return { label: 'Conversation', variant: 'issue' };
    case 'review_body':
      return { label: 'Review', variant: 'review_body' };
    case '':
    case undefined:
    case 'review':
      return { label: 'Inline', variant: 'review' };
    default:
      return { label: humanizeMode(type), variant: 'unknown' };
  }
}

export interface ReviewCommentsModalProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onCancel(): void;
  onDispatched(): void;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}

export function ReviewCommentsModal({
  featureId,
  snapshot,
  onCancel,
  onDispatched,
  attentionItems,
  onOpenGate,
}: ReviewCommentsModalProps): React.ReactElement {
  const singleRepo = snapshot.repos.length === 1 ? snapshot.repos[0] : undefined;
  const [repo, setRepo] = useState(singleRepo ?? '');
  const [comments, setComments] = useState<ReviewCommentView[]>([]);
  const [phase, setPhase] = useState<CyclePhase>('idle');
  const [error, setError] = useState<WizardError | null>(null);
  const action = snapshot.actions.find((candidate) => candidate.id === 'review-comments');
  const modes = useMemo(() => {
    const published = action?.inputs?.find((input) => input.name === 'mode')?.options;
    return published !== undefined && published.length > 0 ? published : ['auto'];
  }, [action]);
  const [mode, setMode] = useState(modes.includes('auto') ? 'auto' : (modes[0] ?? 'auto'));

  const fetchComments = useCallback(
    async (selectedRepo: string) => {
      if (selectedRepo === '') return;
      setPhase('loading');
      setError(null);
      setComments([]);
      try {
        const result = await window.agentico.fetchReviewComments({
          featureId,
          repo: selectedRepo,
        });
        setRepo(result.repo);
        setComments(result.comments);
        setPhase('ready');
      } catch (err) {
        setError(parseIpcError(err));
        setPhase('error');
      }
    },
    [featureId],
  );

  useEffect(() => {
    if (singleRepo !== undefined) void fetchComments(singleRepo);
  }, [fetchComments, singleRepo]);

  const start = useCallback(async () => {
    if (repo === '' || comments.length === 0) return;
    setPhase('dispatching');
    setError(null);
    try {
      await window.agentico.startReviewComments({ featureId, repo, mode });
      onDispatched();
      onCancel();
    } catch (err) {
      setError(parseIpcError(err));
      setPhase('error');
    }
  }, [comments.length, featureId, mode, onCancel, onDispatched, repo]);

  const allClear = phase === 'ready' && comments.length === 0;

  return (
    <div className="cycle-modal" data-phase={phase}>
      <CycleGateNotice
        featureId={featureId}
        snapshot={snapshot}
        attentionItems={attentionItems}
        onOpenGate={onOpenGate}
      />
      {singleRepo !== undefined ? (
        <p className="cycle-modal__context">Repository / {singleRepo}</p>
      ) : (
        <label className="form-field">
          <span className="form-field__label">Repository</span>
          <select
            aria-label="Repository"
            value={repo}
            disabled={phase === 'dispatching'}
            onChange={(event) => {
              const selectedRepo = event.target.value;
              setRepo(selectedRepo);
              void fetchComments(selectedRepo);
            }}
          >
            <option value="">Select a repository…</option>
            {snapshot.repos.map((repoName) => (
              <option key={repoName} value={repoName}>
                {repoName}
              </option>
            ))}
          </select>
        </label>
      )}
      {phase === 'loading' ? (
        <p className="cycle-journey__preflight-note" role="status">
          Fetching current review feedback…
        </p>
      ) : null}
      {comments.length > 0 ? (
        <div className="cycle-journey__comments-preview" aria-label="Review comments preview">
          <p className="cycle-journey__comments-count">
            {comments.length} comment{comments.length === 1 ? '' : 's'} ready
          </p>
          <ul className="cycle-journey__comments-list">
            {comments.slice(0, COMMENTS_PREVIEW_LIMIT).map((comment) => {
              const type = feedbackType(comment.type);
              return (
                <li key={comment.id} className="cycle-journey__comment">
                  <div className="cycle-journey__comment-heading">
                    {comment.author ? (
                      <span className="cycle-journey__comment-author">{comment.author}</span>
                    ) : null}
                    <span className="cycle-modal__comment-type" data-type={type.variant}>
                      {type.label}
                    </span>
                  </div>
                  {comment.file ? (
                    <code className="cycle-journey__comment-file">
                      {comment.file}
                      {comment.line !== undefined ? `:${comment.line}` : ''}
                    </code>
                  ) : null}
                  {comment.body ? (
                    <p className="cycle-journey__comment-body">
                      {comment.body.slice(0, COMMENT_BODY_LIMIT)}
                      {comment.body.length > COMMENT_BODY_LIMIT ? '…' : ''}
                    </p>
                  ) : null}
                </li>
              );
            })}
          </ul>
          {comments.length > COMMENTS_PREVIEW_LIMIT ? (
            <p className="cycle-journey__comments-more">
              {comments.length - COMMENTS_PREVIEW_LIMIT} more…
            </p>
          ) : null}
          <label className="form-field">
            <span className="form-field__label">Mode</span>
            <select
              aria-label="Mode"
              value={mode}
              disabled={phase === 'dispatching'}
              onChange={(event) => setMode(event.target.value)}
            >
              {modes.map((option) => (
                <option key={option} value={option}>
                  {humanizeMode(option)}
                </option>
              ))}
            </select>
          </label>
        </div>
      ) : null}
      {allClear ? (
        <div className="cycle-modal__all-clear" role="status">
          <span aria-hidden="true">✓</span>
          <h4>All comments addressed</h4>
          <p>Nothing to do for {repo}.</p>
        </div>
      ) : null}
      {error !== null ? (
        <p className="form-field__error" role="alert">
          {error.message}
        </p>
      ) : null}
      {allClear ? (
        <footer className="cycle-modal__footer">
          <button type="button" className="cycle-modal__primary" onClick={onCancel}>
            Close
          </button>
        </footer>
      ) : (
        <CycleFooter
          onCancel={onCancel}
          primaryLabel={
            phase === 'dispatching' ? 'Starting…' : `Start review comments (${comments.length})`
          }
          primaryDisabled={comments.length === 0}
          busy={phase === 'dispatching'}
          onPrimary={() => void start()}
        />
      )}
    </div>
  );
}
