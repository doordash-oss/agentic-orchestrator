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
 * Bulk resume/retry preview and cancellable sequential queue. Presents a
 * fresh server-authored preview of every eligible and ineligible feature
 * in attention-first order, requires an impact confirmation, and streams
 * per-row outcomes as the main-process queue dispatches sequentially.
 */
import { useState, useCallback, useEffect, useRef } from 'react';
import { parseIpcError } from '../wizard/ipcError';
import { ErrorSurface } from '../components/ErrorSurface';
import type { CanonicalError, BulkPreview, BulkPreviewRow } from '../../../shared/ipc';

type QueueOutcome = 'success' | 'ineligible' | 'failed' | 'not-started';

interface QueueRowResult {
  featureId: string;
  featureName?: string;
  action: 'resume' | 'retry';
  outcome: QueueOutcome;
  /** The canonical title for failures; the server's reason otherwise. */
  message?: string;
}

type BulkPhase = 'idle' | 'loading' | 'preview' | 'running' | 'complete';

function humanizeAction(action: 'resume' | 'retry'): string {
  return action.charAt(0).toUpperCase() + action.slice(1);
}

export function BulkPreviewPanel({ autoPreviewKey = null }: { autoPreviewKey?: number | null }) {
  const [phase, setPhase] = useState<BulkPhase>('idle');
  const [preview, setPreview] = useState<BulkPreview | null>(null);
  const [error, setError] = useState<CanonicalError | null>(null);
  const [outcomes, setOutcomes] = useState<QueueRowResult[]>([]);
  const [currentIndex, setCurrentIndex] = useState(-1);
  const [wasCancelled, setWasCancelled] = useState(false);
  const cancelRef = useRef(false);

  const loadPreview = useCallback(async () => {
    setPhase('loading');
    setError(null);
    setOutcomes([]);
    setCurrentIndex(-1);
    setWasCancelled(false);
    try {
      const result = await window.agentico.bulkPreview();
      setPreview(result);
      setPhase('preview');
    } catch (err) {
      setError(parseIpcError(err));
      setPhase('idle');
    }
  }, []);

  useEffect(() => {
    if (autoPreviewKey === null) return;
    void loadPreview();
  }, [autoPreviewKey, loadPreview]);

  const runQueue = useCallback(async (rows: BulkPreviewRow[]) => {
    setPhase('running');
    setWasCancelled(false);
    cancelRef.current = false;
    const results: QueueRowResult[] = [];
    for (let i = 0; i < rows.length; i++) {
      if (cancelRef.current) {
        for (let j = i; j < rows.length; j++) {
          const remainingRow = rows[j];
          if (remainingRow === undefined) continue;
          results.push({
            featureId: remainingRow.featureId,
            featureName: remainingRow.featureName,
            action: remainingRow.action,
            outcome: 'not-started',
          });
        }
        setOutcomes([...results]);
        setCurrentIndex(rows.length);
        setWasCancelled(true);
        break;
      }
      const row = rows[i];
      if (row === undefined) continue;
      setCurrentIndex(i);
      try {
        const snapshot = await window.agentico.getFeature(row.featureId);
        const action = snapshot.actions.find((a) => a.id === row.action);
        if (action?.enabled !== true) {
          results.push({
            featureId: row.featureId,
            featureName: row.featureName,
            action: row.action,
            outcome: 'ineligible',
            message: action?.disabledReasons?.[0]?.message ?? 'No longer eligible.',
          });
        } else {
          const result = await window.agentico.dispatchFeatureAction({
            featureId: row.featureId,
            action: row.action,
          });
          results.push({
            featureId: row.featureId,
            featureName: row.featureName,
            action: row.action,
            outcome: 'success',
            message: result.result,
          });
        }
      } catch (err) {
        results.push({
          featureId: row.featureId,
          featureName: row.featureName,
          action: row.action,
          outcome: 'failed',
          // Per-row outcome text derives from the canonical title.
          message: parseIpcError(err).title,
        });
      }
      setOutcomes([...results]);
    }
    if (!cancelRef.current) {
      setCurrentIndex(rows.length);
    }
    setPhase('complete');
  }, []);

  const cancel = useCallback(() => {
    cancelRef.current = true;
  }, []);

  const eligible = preview?.eligible ?? [];
  const excluded = preview?.excluded ?? [];

  const successCount = outcomes.filter((o) => o.outcome === 'success').length;
  const failedCount = outcomes.filter((o) => o.outcome === 'failed').length;
  const ineligibleCount = outcomes.filter((o) => o.outcome === 'ineligible').length;
  const notStartedCount = outcomes.filter((o) => o.outcome === 'not-started').length;

  return (
    <section className="bulk-preview" aria-label="Bulk resume and retry">
      <header className="bulk-preview__header">
        <h3 className="bulk-preview__title">Bulk resume / retry</h3>
        <button
          type="button"
          className="bulk-preview__refresh"
          disabled={phase === 'loading' || phase === 'running'}
          onClick={() => void loadPreview()}
        >
          {phase === 'loading' ? 'Loading…' : 'Fresh preview'}
        </button>
      </header>

      {error !== null ? (
        <ErrorSurface
          error={error}
          variant="compact"
          localAction={{ label: 'Refresh', onAction: () => void loadPreview() }}
        />
      ) : null}

      {phase === 'preview' || phase === 'running' || phase === 'complete' ? (
        preview !== null ? (
          <div className="bulk-preview__body">
            {eligible.length > 0 ? (
              <div className="bulk-preview__eligible">
                <h4 className="bulk-preview__section-title">Eligible ({eligible.length})</h4>
                <ul className="bulk-preview__rows">
                  {eligible.map((row) => {
                    const outcome = outcomes.find((o) => o.featureId === row.featureId);
                    return (
                      <li
                        key={row.featureId}
                        className="bulk-preview__row"
                        data-outcome={outcome?.outcome ?? 'pending'}
                      >
                        <span className="bulk-preview__row-name">
                          {row.featureName ?? row.featureId}
                        </span>
                        <span className="bulk-preview__row-action">
                          {humanizeAction(row.action)}
                        </span>
                        {outcome !== undefined ? (
                          <span
                            className="bulk-preview__row-outcome"
                            data-outcome={outcome.outcome}
                          >
                            {outcome.outcome === 'success'
                              ? '✓'
                              : outcome.outcome === 'failed'
                                ? '✕'
                                : outcome.outcome === 'not-started'
                                  ? '⊘'
                                  : '!'}
                          </span>
                        ) : null}
                        {outcome !== undefined &&
                        (outcome.outcome === 'failed' || outcome.outcome === 'ineligible') &&
                        outcome.message ? (
                          <span className="bulk-preview__row-reason">{outcome.message}</span>
                        ) : null}
                      </li>
                    );
                  })}
                </ul>
              </div>
            ) : (
              <p className="bulk-preview__empty">No features are eligible for resume or retry.</p>
            )}

            {excluded.length > 0 ? (
              <div className="bulk-preview__excluded">
                <h4 className="bulk-preview__section-title">Excluded ({excluded.length})</h4>
                <ul className="bulk-preview__rows bulk-preview__rows--excluded">
                  {excluded.map((row) => (
                    <li
                      key={row.featureId}
                      className="bulk-preview__row bulk-preview__row--excluded"
                    >
                      <span className="bulk-preview__row-name">
                        {row.featureName ?? row.featureId}
                      </span>
                      <span className="bulk-preview__row-reason">
                        {row.disabledReason ?? 'Not eligible for resume or retry.'}
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            ) : null}

            {phase === 'preview' ? (
              <div className="bulk-preview__confirm">
                <p className="bulk-preview__confirm-text">
                  This will sequentially{' '}
                  {eligible.length > 0 ? `dispatch ${eligible.length}` : 'dispatch'}{' '}
                  {eligible.length === 1 ? 'action' : 'actions'}. Each feature is revalidated
                  immediately before dispatch. Cancel stops after the current action.
                </p>
                <button
                  type="button"
                  className="bulk-preview__run"
                  disabled={eligible.length === 0}
                  onClick={() => void runQueue(eligible)}
                >
                  Run {eligible.length} {eligible.length === 1 ? 'action' : 'actions'}
                </button>
              </div>
            ) : null}

            {phase === 'running' || phase === 'complete' ? (
              <div className="bulk-preview__progress" aria-live="polite">
                <p className="bulk-preview__progress-text">
                  {phase === 'running'
                    ? `Dispatching ${currentIndex + 1} of ${eligible.length}…`
                    : wasCancelled
                      ? `Cancelled after current action — ${notStartedCount} not started`
                      : 'Queue complete.'}
                </p>
                {outcomes.length > 0 ? (
                  <p className="bulk-preview__counts">
                    {successCount} succeeded · {failedCount} failed
                    {ineligibleCount > 0 ? ` · ${ineligibleCount} ineligible` : ''}
                    {notStartedCount > 0 ? ` · ${notStartedCount} not started` : ''}
                  </p>
                ) : null}
                {phase === 'running' ? (
                  <button type="button" className="bulk-preview__cancel" onClick={cancel}>
                    Cancel after current
                  </button>
                ) : null}
                {phase === 'complete' ? (
                  <button
                    type="button"
                    className="bulk-preview__retry"
                    onClick={() => void loadPreview()}
                  >
                    Fresh preview
                  </button>
                ) : null}
              </div>
            ) : null}
          </div>
        ) : null
      ) : null}
    </section>
  );
}
