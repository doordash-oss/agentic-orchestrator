import type { RelationshipChildView } from '../../../../shared/ipc';

/**
 * Settled passes are immutable history: newest first, inspection
 * only, never a mutation affordance. The preserved diff was captured at close
 * against the launch base, so it stays readable after worktrees are reclaimed.
 */
export function RefactorHistory({
  entries,
}: {
  entries: readonly RelationshipChildView[];
}): React.ReactElement | null {
  if (entries.length === 0) return null;
  return (
    <details className="refactor-history">
      <summary>
        <span className="refactor-history__summary-label">Pass history</span>
        <span className="refactor-history__count">{entries.length}</span>
      </summary>
      <ol className="refactor-history__entries">
        {entries.map((entry) => (
          <li key={entry.id} data-outcome={entry.outcome ?? 'closed'} data-kind={entry.kind}>
            <div className="refactor-history__row">
              <span className="refactor-history__glyph" aria-hidden="true">
                {entry.outcome === 'discarded' ? '✕' : '✓'}
              </span>
              <div className="refactor-history__identity">
                <div className="refactor-history__name-row">
                  <strong>{entry.name}</strong>
                  <span className="refactor-history__kind" data-kind={entry.kind}>
                    {kindLabel(entry.kind)}
                  </span>
                </div>
                <span className="refactor-history__state">{entry.displayState}</span>
              </div>
              <dl className="refactor-history__facts">
                <div>
                  <dt>When</dt>
                  <dd>{historySpan(entry)}</dd>
                </div>
                <div>
                  <dt>Pipeline</dt>
                  <dd>
                    <code>{entry.pipeline}</code>
                  </dd>
                </div>
                <div>
                  <dt>Cost</dt>
                  <dd>
                    <code>${entry.cost.totalUsd.toFixed(2)}</code>
                  </dd>
                </div>
              </dl>
            </div>
            {entry.cleanupWarnings.length > 0 ? (
              <ul className="refactor-history__warnings">
                {entry.cleanupWarnings.map((warning) => (
                  <li key={`${warning.repo ?? ''}:${warning.message}`}>
                    {warning.repo === undefined
                      ? warning.message
                      : `${warning.repo}: ${warning.message}`}
                  </li>
                ))}
              </ul>
            ) : null}
            {entry.diffSummary !== undefined && entry.diffSummary !== '' ? (
              <details className="refactor-history__diff">
                <summary>Preserved diff (read-only)</summary>
                <pre>{entry.diffSummary}</pre>
              </details>
            ) : null}
          </li>
        ))}
      </ol>
    </details>
  );
}

function kindLabel(kind: string): string {
  return kind === 'review-feedback' ? 'Review feedback' : 'Refactor';
}

function historySpan(entry: RelationshipChildView): string {
  const started = shortDate(entry.startedAt);
  const closed = entry.closedAt === undefined ? null : shortDate(entry.closedAt);
  if (started === null) return closed ?? '—';
  return closed === null ? started : `${started} → ${closed}`;
}

function shortDate(value: string): string | null {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return null;
  return parsed.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}
