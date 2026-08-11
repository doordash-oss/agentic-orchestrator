/**
 * The repository-and-filter panel shared by the wide rail and the narrow
 * drawer: repository scope ledger with selected/total ratios and coverage
 * marks, author/type facets, the path query, the visible-result summary, and
 * the visible-only bulk actions. Selection and filter logic lives in the
 * parent workspace; this component only renders it.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import type { ReviewFeedbackFilters } from './feedbackFilters';
import type { ReviewFeedbackDraftCommentView } from './reviewFeedbackDraftApi';

export interface ScopeLedgerEntry {
  /** Scope key: a repository name, or 'all' for the whole draft. */
  scope: string;
  label: string;
  selected: number;
  total: number;
}

export interface ScopePanelProps {
  ledger: ScopeLedgerEntry[];
  scope: string;
  onScope(scope: string): void;
  authors: string[];
  types: ReviewFeedbackDraftCommentView['type'][];
  filters: ReviewFeedbackFilters;
  onToggleAuthor(author: string): void;
  onToggleType(type: ReviewFeedbackDraftCommentView['type']): void;
  onPathChange(path: string): void;
  /** Visible-versus-scoped result summary. */
  visibleCount: number;
  scopedCount: number;
  /** Statechanging targets of each bulk action over the current visible set. */
  selectVisibleCount: number;
  clearVisibleCount: number;
  onSelectVisible(): void;
  onClearVisible(): void;
  /** True while a dispatch is in flight; selection writes stay queued. */
  launching: boolean;
}

function CoverageMark({ selected, total }: { selected: number; total: number }) {
  const percent = total === 0 ? 0 : Math.round((selected / total) * 100);
  return (
    <span className="review-feedback-coverage" aria-hidden="true">
      <span
        className="review-feedback-coverage__fill"
        style={{ width: `${percent}%` }}
        data-percent={percent}
      />
    </span>
  );
}

/** One ledger row: name, accessible ratio, and the slim coverage mark. */
function ScopeRow({
  entry,
  active,
  onScope,
}: {
  entry: ScopeLedgerEntry;
  active: boolean;
  onScope(scope: string): void;
}) {
  return (
    <label className="review-feedback-workspace__scope" data-active={active}>
      <input
        type="radio"
        name="review-feedback-scope"
        checked={active}
        onChange={() => onScope(entry.scope)}
      />
      <span className="review-feedback-workspace__scope-name">{entry.label}</span>
      <span className="sr-only">
        {entry.selected} of {entry.total} selected
      </span>
      <CoverageMark selected={entry.selected} total={entry.total} />
      <span className="review-feedback-workspace__scope-count" aria-hidden="true">
        {entry.selected}/{entry.total}
      </span>
    </label>
  );
}

export function ScopePanel({
  ledger,
  scope,
  onScope,
  authors,
  types,
  filters,
  onToggleAuthor,
  onToggleType,
  onPathChange,
  visibleCount,
  scopedCount,
  selectVisibleCount,
  clearVisibleCount,
  onSelectVisible,
  onClearVisible,
  launching,
}: ScopePanelProps): React.ReactElement {
  return (
    <div className="review-feedback-rail">
      <div className="review-feedback-rail__scopes" role="radiogroup" aria-label="Repository scope">
        {ledger.map((entry) => (
          <ScopeRow
            key={entry.scope}
            entry={entry}
            active={scope === entry.scope}
            onScope={onScope}
          />
        ))}
      </div>

      <fieldset className="review-feedback-facet">
        <legend className="review-feedback-facet__legend">Author</legend>
        {authors.length === 0 ? (
          <p className="review-feedback-facet__empty">No authors in this scope.</p>
        ) : (
          authors.map((author) => (
            <label key={author} className="review-feedback-facet__option">
              <input
                type="checkbox"
                checked={filters.authors.includes(author)}
                onChange={() => onToggleAuthor(author)}
              />
              <span>{author}</span>
            </label>
          ))
        )}
      </fieldset>

      <fieldset className="review-feedback-facet">
        <legend className="review-feedback-facet__legend">Comment type</legend>
        {types.map((type) => (
          <label key={type} className="review-feedback-facet__option">
            <input
              type="checkbox"
              checked={filters.types.includes(type)}
              onChange={() => onToggleType(type)}
            />
            <span>{COMMENT_TYPE_LABEL[type]}</span>
          </label>
        ))}
      </fieldset>

      <div className="review-feedback-facet">
        <label className="review-feedback-facet__path" htmlFor="review-feedback-path-filter">
          File path
        </label>
        <input
          id="review-feedback-path-filter"
          type="search"
          className="review-feedback-facet__path-input"
          value={filters.path}
          placeholder="Filter by path"
          onChange={(event) => onPathChange(event.target.value)}
        />
      </div>

      <p className="review-feedback-rail__summary" aria-live="polite">
        {visibleCount} of {scopedCount} comments visible
      </p>

      <div className="review-feedback-rail__bulk">
        <button
          type="button"
          className="review-feedback-bulk"
          disabled={launching || selectVisibleCount === 0}
          onClick={onSelectVisible}
        >
          Select visible ({selectVisibleCount})
        </button>
        <button
          type="button"
          className="review-feedback-bulk"
          disabled={launching || clearVisibleCount === 0}
          onClick={onClearVisible}
        >
          Clear visible ({clearVisibleCount})
        </button>
      </div>
    </div>
  );
}
