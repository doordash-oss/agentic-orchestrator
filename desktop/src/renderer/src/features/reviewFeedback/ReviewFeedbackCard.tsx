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
 * One review-feedback card: the standalone selection checkbox (a sibling of —
 * never an ancestor of — the rich content, so links, image actions, task
 * items, and the expansion control can never toggle selection), the unsaved
 * badge, the type/author/age/path meta line, the collapsible markdown/diff
 * body, and the expansion control.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { MaximizeIcon } from '../../components/icons';
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
  /** Frozen while launching or while a recovery overlay is unresolved. */
  disabled: boolean;
  onToggle(comment: ReviewFeedbackDraftCommentView, checked: boolean): void;
  onOpen(comment: ReviewFeedbackDraftCommentView): void;
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
  disabled,
  onToggle,
  onOpen,
}: ReviewFeedbackCardProps): React.ReactElement {
  const created = formatCreatedAgo(comment.createdAt);
  const collapsible = needsExpansion(comment);
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
        <span className="review-feedback-card__meta">
          <b className="review-feedback-card__type">{COMMENT_TYPE_LABEL[comment.type]}</b>
          {comment.author !== undefined ? (
            <span className="review-feedback-card__author">{comment.author}</span>
          ) : null}
          {created !== null ? (
            <span className="review-feedback-card__created">{created}</span>
          ) : null}
          {comment.path !== undefined ? (
            <code className="review-feedback-card__path">
              {comment.path}
              {comment.line !== undefined ? `:${comment.line}` : ''}
            </code>
          ) : null}
        </span>
        <div className="review-feedback-card__content" data-collapsed={collapsible}>
          {comment.body !== undefined ? <ReviewFeedbackMarkdown text={comment.body} /> : null}
          {comment.diffHunk !== undefined ? <ReviewFeedbackDiff text={comment.diffHunk} /> : null}
        </div>
        <button
          type="button"
          className="review-feedback-card__expand"
          aria-label="View full comment"
          data-hint="Full comment"
          onClick={(event) => {
            event.stopPropagation();
            onOpen(comment);
          }}
        >
          <MaximizeIcon />
        </button>
        {comment.inReplyToId !== undefined ? (
          <p className="review-feedback-card__reply">Reply to comment {comment.inReplyToId}</p>
        ) : null}
      </div>
    </article>
  );
}
