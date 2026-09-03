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

import { useCallback, useState } from 'react';
import type { CanonicalError, RelationshipChildView } from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { retryAction } from '../../hooks';
import { parseIpcError } from '../../wizard/ipcError';
import { CHILD_KIND_LABEL, relationshipWarningExplain } from './refactorPassModel';

/**
 * Settled passes are immutable history: newest first, inspection
 * only, never a mutation affordance. The preserved diff was captured at close
 * against the launch base, so it stays readable after worktrees are reclaimed.
 *
 * A list projection carries neither the diff bodies nor the passes past its
 * cap, so both are reached on demand through `onLoadFullHistory` — the
 * truncated count is always stated rather than passed off as the whole record.
 */
export function RefactorHistory({
  entries,
  total,
  truncated = false,
  onLoadFullHistory,
}: {
  entries: readonly RelationshipChildView[];
  /** Closed-pass count before the projection's cap, when the server reports one. */
  total?: number;
  truncated?: boolean;
  onLoadFullHistory?: () => Promise<readonly RelationshipChildView[]>;
}): React.ReactElement | null {
  const [loaded, setLoaded] = useState<readonly RelationshipChildView[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState<CanonicalError | null>(null);

  const load = useCallback(() => {
    if (onLoadFullHistory === undefined || loading) return;
    setLoading(true);
    setFailed(null);
    onLoadFullHistory().then(
      (full) => {
        setLoaded(full);
        setLoading(false);
      },
      (reason: unknown) => {
        setFailed(parseIpcError(reason));
        setLoading(false);
      },
    );
  }, [loading, onLoadFullHistory]);

  const shown = loaded ?? entries;
  if (shown.length === 0) return null;
  const shortfall = loaded === null && truncated && total !== undefined && total > shown.length;
  const loadable = onLoadFullHistory !== undefined;
  return (
    <details className="refactor-history">
      <summary>
        <span className="refactor-history__summary-label">Pass history</span>
        <span className="refactor-history__count">
          {shortfall ? `${shown.length} of ${total}` : shown.length}
        </span>
      </summary>
      {shortfall ? (
        // A shortfall statement, not an error: the count is honest and the
        // load affordance is the fix, so no alert semantics ride along.
        <p className="refactor-history__truncation">
          {`Showing the ${shown.length} most recent of ${total} settled passes.`}
          {loadable ? (
            <button type="button" onClick={load} disabled={loading}>
              {loading ? 'Loading…' : 'Load the full history'}
            </button>
          ) : null}
        </p>
      ) : null}
      {failed !== null ? (
        <ErrorSurface error={failed} variant="compact" localAction={retryAction(load)} />
      ) : null}
      <ol className="refactor-history__entries">
        {shown.map((entry) => (
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
            {entry.warnings.map((warning, index) => (
              <ErrorSurface
                key={`${warning.code}:${index}`}
                error={warning}
                variant="compact"
                explain={relationshipWarningExplain(entry, warning)}
              />
            ))}
            {entry.diffSummary !== undefined && entry.diffSummary !== '' ? (
              <details className="refactor-history__diff">
                <summary>Preserved diff (read-only)</summary>
                <pre>{entry.diffSummary}</pre>
              </details>
            ) : entry.hasDiffSummary === true && loadable ? (
              <details className="refactor-history__diff">
                <summary>Preserved diff (read-only)</summary>
                <button type="button" onClick={load} disabled={loading}>
                  {loading ? 'Loading diff…' : 'Load diff'}
                </button>
              </details>
            ) : null}
          </li>
        ))}
      </ol>
    </details>
  );
}

function kindLabel(kind: string): string {
  return CHILD_KIND_LABEL[kind] ?? 'Pass';
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
