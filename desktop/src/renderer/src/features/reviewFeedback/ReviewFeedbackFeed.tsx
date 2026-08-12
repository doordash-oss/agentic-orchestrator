/**
 * The review-feedback feed: the feedbar (active-filter chips, clear-all, and
 * the polite visible/scoped summary), the filtered-empty state, and one
 * labelled section per repository in the server's stable order, comments
 * oldest-first. All selection and filtering logic lives in the workspace and
 * its draft controller; this component only renders it.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import { ReviewFeedbackCard } from './ReviewFeedbackCard';
import type { ReviewFeedbackFilters } from './feedbackFilters';
import type { PendingSelection } from './useReviewFeedbackDraft';
import type {
  ReviewFeedbackDraftCommentView,
  ReviewFeedbackDraftRepoGroup,
} from './reviewFeedbackDraftApi';

/** One rendered repository section: the group plus its filter-matched comments. */
export interface FeedSection {
  group: ReviewFeedbackDraftRepoGroup;
  comments: ReviewFeedbackDraftCommentView[];
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
  expandedRefs: ReadonlySet<string>;
  /** Freezes every selection checkbox (launch in flight or unresolved recovery). */
  selectionDisabled: boolean;
  onToggle(comment: ReviewFeedbackDraftCommentView, checked: boolean): void;
  onToggleExpanded(stableRef: string): void;
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
  expandedRefs,
  selectionDisabled,
  onToggle,
  onToggleExpanded,
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
      {sections.map(({ group, comments }) => {
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
            {comments.map((comment) => (
              <ReviewFeedbackCard
                key={comment.stableRef}
                comment={comment}
                checked={selectedOf(comment)}
                unsaved={saveFailed && pending.has(comment.stableRef)}
                expanded={expandedRefs.has(comment.stableRef)}
                disabled={selectionDisabled}
                onToggle={onToggle}
                onToggleExpanded={onToggleExpanded}
              />
            ))}
          </section>
        );
      })}
    </main>
  );
}
