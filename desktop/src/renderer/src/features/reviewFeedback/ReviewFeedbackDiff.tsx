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
 * Raw server-provided diff hunks, rendered as a labelled, no-wrap,
 * horizontally scrollable text region. Diff text is never parsed as Markdown
 * or HTML: each line is classified (hunk header, add, remove, context, note)
 * and receives a text-readable semantic label plus matching visual treatment,
 * so meaning never rides on color alone.
 */
import type { ReactElement } from 'react';
import type { ReviewFeedbackDraftCommentView } from './reviewFeedbackDraftApi';

/** Collapse thresholds for a card's long content (body and/or diff). */
export const BODY_COLLAPSE_CHARS = 1200;
export const BODY_COLLAPSE_LINES = 12;
export const DIFF_COLLAPSE_LINES = 20;

export function needsExpansion(
  comment: Pick<ReviewFeedbackDraftCommentView, 'body' | 'diffHunk'>,
): boolean {
  const body = comment.body ?? '';
  const bodyLines = body === '' ? 0 : body.split('\n').length;
  const diff = comment.diffHunk ?? '';
  const diffLines = diff === '' ? 0 : diff.split('\n').length;
  return (
    body.length > BODY_COLLAPSE_CHARS ||
    bodyLines > BODY_COLLAPSE_LINES ||
    diffLines > DIFF_COLLAPSE_LINES
  );
}

type DiffLineKind = 'hunk' | 'add' | 'remove' | 'note' | 'context';

function classify(line: string): DiffLineKind {
  if (line.startsWith('@@')) return 'hunk';
  // '+++'/'---' are file headers, not line semantics.
  if (line.startsWith('+') && !line.startsWith('+++')) return 'add';
  if (line.startsWith('-') && !line.startsWith('---')) return 'remove';
  if (line.startsWith('\\')) return 'note';
  return 'context';
}

const KIND_LABEL: Record<DiffLineKind, string> = {
  hunk: 'Hunk header',
  add: 'Added line',
  remove: 'Removed line',
  note: 'Diff note',
  context: 'Context line',
};

export interface ReviewFeedbackDiffProps {
  text: string;
}

export function ReviewFeedbackDiff({ text }: ReviewFeedbackDiffProps): ReactElement {
  const lines = text.split('\n');
  return (
    <div className="review-feedback-diff" role="group" aria-label="Diff">
      <pre className="review-feedback-diff__pre">
        {lines.map((line, index) => {
          const kind = classify(line);
          return (
            <span key={index} className="review-feedback-diff__line" data-kind={kind}>
              <span className="visually-hidden">{KIND_LABEL[kind]}: </span>
              {line}
              {'\n'}
            </span>
          );
        })}
      </pre>
    </div>
  );
}
