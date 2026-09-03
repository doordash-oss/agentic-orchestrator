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

import { useEffect, useRef } from 'react';
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { ReviewFeedbackDiff } from './ReviewFeedbackDiff';
import { ReviewFeedbackMarkdown } from './ReviewFeedbackMarkdown';
import type { ReviewFeedbackDraftCommentView } from './reviewFeedbackDraftApi';

const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

export function ReviewFeedbackDetailDialog({
  comment,
  onClose,
}: {
  comment: ReviewFeedbackDraftCommentView;
  onClose(): void;
}): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleId = `review-feedback-detail-${comment.stableRef.replace(/[^\w-]/g, '-')}`;

  useEffect(() => {
    const returnTarget =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const dialog = dialogRef.current;
    dialog?.querySelector<HTMLElement>('.review-feedback-detail__close')?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || dialog === null) return;
      const items = Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (item) => !item.hasAttribute('disabled'),
      );
      const first = items[0];
      const last = items[items.length - 1];
      if (first === undefined || last === undefined) return;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      returnTarget?.focus();
    };
  }, [onClose]);

  return (
    <div className="review-feedback-detail__backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        className="review-feedback-detail"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="review-feedback-detail__header">
          <div>
            <span className="review-feedback-detail__eyebrow">Full comment</span>
            <h3 id={titleId} className="review-feedback-detail__title">
              {COMMENT_TYPE_LABEL[comment.type]}
              {comment.author !== undefined ? ` from ${comment.author}` : ''}
            </h3>
            {comment.path !== undefined ? (
              <code className="review-feedback-detail__path">
                {comment.path}
                {comment.line !== undefined ? `:${comment.line}` : ''}
              </code>
            ) : null}
          </div>
          <button type="button" className="review-feedback-detail__close" onClick={onClose}>
            Close comment
          </button>
        </header>
        <div className="review-feedback-detail__content">
          {comment.body !== undefined ? <ReviewFeedbackMarkdown text={comment.body} /> : null}
          {comment.diffHunk !== undefined ? <ReviewFeedbackDiff text={comment.diffHunk} /> : null}
          {comment.inReplyToId !== undefined ? (
            <p className="review-feedback-card__reply">Reply to comment {comment.inReplyToId}</p>
          ) : null}
        </div>
      </div>
    </div>
  );
}
