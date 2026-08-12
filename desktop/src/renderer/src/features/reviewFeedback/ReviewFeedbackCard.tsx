/**
 * One review-feedback card: the standalone selection checkbox (a sibling of —
 * never an ancestor of — the rich content, so links, image actions, task
 * items, and the expansion control can never toggle selection), the unsaved
 * badge, the type/author/age/path meta line, the collapsible markdown/diff
 * body, and the expansion control.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { bucketElapsedSince } from '../phaseRail';
import { ReviewFeedbackDiff, needsExpansion } from './ReviewFeedbackDiff';
import { ReviewFeedbackMarkdown } from './ReviewFeedbackMarkdown';
import type { ReviewFeedbackDraftCommentView } from './reviewFeedbackDraftApi';

export interface ReviewFeedbackCardProps {
  comment: ReviewFeedbackDraftCommentView;
  /** Visible selection: committed value with the pending overlay applied. */
  checked: boolean;
  /** True while an unsaved-choice freeze keeps this edit unacknowledged. */
  unsaved: boolean;
  expanded: boolean;
  /** Frozen while launching or while a recovery overlay is unresolved. */
  disabled: boolean;
  onToggle(comment: ReviewFeedbackDraftCommentView, checked: boolean): void;
  onToggleExpanded(stableRef: string): void;
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

export function ReviewFeedbackCard({
  comment,
  checked,
  unsaved,
  expanded,
  disabled,
  onToggle,
  onToggleExpanded,
}: ReviewFeedbackCardProps): React.ReactElement {
  const created = formatCreatedAgo(comment.createdAt);
  const collapsible = needsExpansion(comment);
  const contentId = `review-feedback-content-${comment.stableRef.replace(/[^\w-]/g, '-')}`;
  const selectLabel =
    comment.body !== undefined && comment.body.trim() !== ''
      ? `Select feedback: ${comment.body.length > 160 ? `${comment.body.slice(0, 160)}…` : comment.body}`
      : `Select feedback: ${COMMENT_TYPE_LABEL[comment.type]}${comment.author !== undefined ? ` by ${comment.author}` : ''}`;
  return (
    <article className="review-feedback-card" data-selected={checked} data-unsaved={unsaved}>
      <input
        type="checkbox"
        className="review-feedback-card__select"
        aria-label={selectLabel}
        checked={checked}
        disabled={disabled}
        onChange={() => onToggle(comment, checked)}
      />
      {unsaved ? <span className="review-feedback-card__unsaved">Unsaved choice</span> : null}
      <div className="review-feedback-card__body">
        <span className="review-feedback-modal__comment-meta">
          <b className="review-feedback-modal__comment-type">{COMMENT_TYPE_LABEL[comment.type]}</b>
          {comment.author !== undefined ? (
            <span className="review-feedback-modal__comment-author">{comment.author}</span>
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
          {comment.body !== undefined ? <ReviewFeedbackMarkdown text={comment.body} /> : null}
          {comment.diffHunk !== undefined ? <ReviewFeedbackDiff text={comment.diffHunk} /> : null}
        </div>
        {collapsible ? (
          <button
            type="button"
            className="review-feedback-card__expand"
            aria-expanded={expanded}
            aria-controls={contentId}
            onClick={(event) => {
              event.stopPropagation();
              onToggleExpanded(comment.stableRef);
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
}
