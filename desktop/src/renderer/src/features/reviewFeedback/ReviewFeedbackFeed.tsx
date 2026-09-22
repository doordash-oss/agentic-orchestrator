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
 * The review-feedback feed: the feedbar (active-filter chips, clear-all, and
 * the polite visible/scoped summary), the filtered-empty state, and one
 * labelled section per repository in the server's stable order. Inside each
 * repository section, one sub-section per open layer pull request in
 * position order — a header naming the layer position and title with an
 * "Open pull request" action for the PR's URL, followed by that PR's cards,
 * comments oldest-first. All selection and filtering logic lives in the
 * workspace and its draft controller; this component only renders it.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { ReviewFeedbackCard } from './ReviewFeedbackCard';
import type { ReviewFeedbackFilters } from './feedbackFilters';
import type { PendingSelection } from './useReviewFeedbackDraft';
import type {
  ReviewFeedbackDraftCommentView,
  ReviewFeedbackDraftPullRequestGroup,
  ReviewFeedbackDraftRepoGroup,
} from './reviewFeedbackDraftApi';

/** One rendered pull-request sub-section: the PR group plus its filter-matched comments. */
export interface FeedPullRequestSection {
  pr: ReviewFeedbackDraftPullRequestGroup;
  comments: ReviewFeedbackDraftCommentView[];
}

/** One rendered repository section: the group plus its PR sub-sections. */
export interface FeedSection {
  group: ReviewFeedbackDraftRepoGroup;
  pullRequests: FeedPullRequestSection[];
}

export interface ReviewFeedbackFeedProps {
  sections: FeedSection[];
  filters: ReviewFeedbackFilters;
  activeFilters: boolean;
  visibleCount: number;
  scopedCount: number;
  /** Overlay-aware selection lookup for cards and section ledgers. */
  selectedOf(comment: ReviewFeedbackDraftCommentView): boolean;
  /** Unacknowledged overlay, for the unsaved badge while a save failure is frozen. */
  pending: ReadonlyMap<string, PendingSelection>;
  saveFailed: boolean;
  /** Freezes every selection checkbox (launch in flight or unresolved recovery). */
  selectionDisabled: boolean;
  onToggle(comment: ReviewFeedbackDraftCommentView, checked: boolean): void;
  onOpen(comment: ReviewFeedbackDraftCommentView): void;
  onToggleAuthor(author: string): void;
  onToggleType(type: ReviewFeedbackDraftCommentView['type']): void;
  onPathChange(path: string): void;
  onClearFilters(): void;
  feedRef: React.RefObject<HTMLElement | null>;
}

export function ReviewFeedbackFeed({
  sections,
  filters,
  activeFilters,
  visibleCount,
  scopedCount,
  selectedOf,
  pending,
  saveFailed,
  selectionDisabled,
  onToggle,
  onOpen,
  onToggleAuthor,
  onToggleType,
  onPathChange,
  onClearFilters,
  feedRef,
}: ReviewFeedbackFeedProps): React.ReactElement {
  return (
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
                    onClick={() => onToggleAuthor(author)}
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
                    onClick={() => onToggleType(type)}
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
            <button
              type="button"
              className="review-feedback-feedbar__clear"
              onClick={onClearFilters}
            >
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
          <button type="button" onClick={onClearFilters}>
            Clear all filters
          </button>
        </div>
      ) : null}
      {sections.map(({ group, pullRequests }) => {
        // The repository ledger counts across every pull-request group —
        // the scope rail's per-repository counts use the same flattening.
        const repoTotal = group.pullRequests.reduce((sum, pr) => sum + pr.comments.length, 0);
        const matched = pullRequests.flatMap((section) => section.comments);
        const sectionSelected = matched.filter(selectedOf).length;
        return (
          <section key={group.repo} className="review-feedback-section" aria-label={group.repo}>
            <header className="review-feedback-section__header">
              <h3 className="review-feedback-section__title">{group.repo}</h3>
              <span className="review-feedback-section__ledger">
                {sectionSelected} of {repoTotal} selected
              </span>
            </header>
            {pullRequests.map(({ pr, comments }) => {
              const prSelected = pr.comments.filter(selectedOf).length;
              return (
                <div
                  key={pr.url !== '' ? pr.url : `layer-${pr.position}`}
                  className="review-feedback-pr"
                >
                  <header className="review-feedback-pr__header">
                    <h4 className="review-feedback-pr__title">
                      Layer {pr.position}
                      {pr.title !== '' ? (
                        <>
                          {` — `}
                          <span className="review-feedback-pr__name">{pr.title}</span>
                        </>
                      ) : null}
                    </h4>
                    <span className="review-feedback-pr__ledger">
                      {prSelected} of {pr.comments.length} selected
                    </span>
                    {pr.url !== '' ? (
                      <button
                        type="button"
                        className="review-feedback-pr__open"
                        aria-label={`Open pull request: layer ${pr.position}${pr.title !== '' ? ` ${pr.title}` : ''}`}
                        onClick={() => void window.agentico.openExternal({ url: pr.url })}
                      >
                        Open pull request
                      </button>
                    ) : null}
                  </header>
                  {comments.map((comment) => (
                    <ReviewFeedbackCard
                      key={comment.stableRef}
                      comment={comment}
                      checked={selectedOf(comment)}
                      unsaved={saveFailed && pending.has(comment.stableRef)}
                      disabled={selectionDisabled}
                      onToggle={onToggle}
                      onOpen={onOpen}
                    />
                  ))}
                </div>
              );
            })}
          </section>
        );
      })}
    </main>
  );
}
