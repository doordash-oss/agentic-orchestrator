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
 * Recovery workspace: shows the risk-first orphan-session queue with
 * per-item context, bounded logs, and Resume/Kill actions. Recovery
 * receives contextual priority — it appears first in attention and opens
 * a dedicated workspace, while unrelated features remain usable.
 */
import { useState, useCallback, useEffect, useRef } from 'react';
import { ErrorSurface, type ErrorSurfaceAction } from '../components/ErrorSurface';
import { parseIpcError } from '../wizard/ipcError';
import type { RecoverySnapshot, RecoveryItemView } from '../../../shared/ipc';

type RecoveryPhase = 'idle' | 'scanning' | 'ready' | 'executing' | 'complete' | 'error';

interface RecoveryOutcome {
  key: string;
  action: string;
  result: 'submitted' | 'not-started';
}

function humanizeAction(action: string): string {
  switch (action) {
    case 'resume':
      return 'Resume';
    case 'kill':
      return 'Kill';
    default:
      return action.charAt(0).toUpperCase() + action.slice(1);
  }
}

export interface RecoveryWorkspaceProps {
  onNavigateToFeature?: (featureId: string) => void;
}

export function RecoveryWorkspace({ onNavigateToFeature }: RecoveryWorkspaceProps = {}) {
  const [phase, setPhase] = useState<RecoveryPhase>('idle');
  const [snapshot, setSnapshot] = useState<RecoverySnapshot | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [outcomes, setOutcomes] = useState<Map<string, RecoveryOutcome>>(new Map());
  const [batchResult, setBatchResult] = useState<string | null>(null);
  const [expandedLog, setExpandedLog] = useState<string | null>(null);
  const [logText, setLogText] = useState<Map<string, string>>(new Map());
  const [logLoading, setLogLoading] = useState<string | null>(null);
  const [logError, setLogError] = useState<Map<string, string>>(new Map());
  const [killTarget, setKillTarget] = useState<RecoveryItemView | null>(null);
  const [executingKey, setExecutingKey] = useState<string | null>(null);
  const autoScannedRef = useRef(false);
  const killDialogRef = useRef<HTMLDivElement>(null);
  const killTriggerRef = useRef<HTMLButtonElement | null>(null);

  const scan = useCallback(async () => {
    setPhase('scanning');
    setError(null);
    setOutcomes(new Map());
    setBatchResult(null);
    setExpandedLog(null);
    setLogText(new Map());
    setLogError(new Map());
    try {
      const result = await window.agentico.scanRecovery();
      const sortedItems = [...result.items].sort((a, b) => {
        if (a.processAlive !== b.processAlive) return a.processAlive ? -1 : 1;
        // Interim tie-breaker on stable key until start-time is available
        // in the view; the server sorts oldest-start-first before sending.
        return a.key.localeCompare(b.key);
      });
      setSnapshot({ ...result, items: sortedItems });
      setPhase('ready');
    } catch (err) {
      setError(parseIpcError(err).message);
      setPhase('error');
    }
  }, []);

  const loadLog = useCallback(
    async (key: string) => {
      if (snapshot === null) return;
      setLogError((prev) => {
        const next = new Map(prev);
        next.delete(key);
        return next;
      });
      setLogLoading(key);
      try {
        const result = await window.agentico.readRecoveryLog({
          snapshotId: snapshot.snapshotId,
          key,
        });
        setLogText((prev) => {
          const next = new Map(prev);
          next.set(key, result.text);
          return next;
        });
      } catch (err) {
        setLogError((prev) => {
          const next = new Map(prev);
          next.set(key, parseIpcError(err).message);
          return next;
        });
      } finally {
        setLogLoading(null);
      }
    },
    [snapshot],
  );
  const executeSingle = useCallback(
    async (item: RecoveryItemView, action: string) => {
      if (snapshot === null) return;
      setExecutingKey(item.key);
      try {
        const result = await window.agentico.executeRecovery({
          snapshotId: snapshot.snapshotId,
          actions: { [item.key]: action },
        });
        setOutcomes((prev) => {
          const next = new Map(prev);
          next.set(item.key, {
            key: item.key,
            action,
            result: 'submitted',
          });
          return next;
        });
        if (result.result !== undefined && result.result !== '') {
          setBatchResult(result.result);
        }
      } catch (err) {
        setError(parseIpcError(err).message);
      } finally {
        setExecutingKey(null);
      }
    },
    [snapshot],
  );

  const executeKill = useCallback(
    async (item: RecoveryItemView) => {
      if (snapshot === null) return;
      setExecutingKey(item.key);
      try {
        const result = await window.agentico.executeRecovery({
          snapshotId: snapshot.snapshotId,
          actions: { [item.key]: 'kill' },
        });
        setOutcomes((prev) => {
          const next = new Map(prev);
          next.set(item.key, {
            key: item.key,
            action: 'kill',
            result: 'submitted',
          });
          return next;
        });
        if (result.result !== undefined && result.result !== '') {
          setBatchResult(result.result);
        }
      } catch (err) {
        setError(parseIpcError(err).message);
      } finally {
        setExecutingKey(null);
        setKillTarget(null);
        killTriggerRef.current?.focus();
      }
    },
    [snapshot],
  );

  useEffect(() => {
    if (phase === 'idle' && !autoScannedRef.current) {
      autoScannedRef.current = true;
      void scan();
    }
  }, [phase, scan]);

  useEffect(() => {
    if (killTarget === null) return;
    killDialogRef.current?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape' && executingKey === null) {
        event.preventDefault();
        setKillTarget(null);
        killTriggerRef.current?.focus();
        return;
      }
      if (event.key !== 'Tab' || executingKey !== null) return;
      const controls = [
        ...(killDialogRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ??
          []),
      ];
      const first = controls[0];
      const last = controls.at(-1);
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
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [killTarget, executingKey]);

  const items = snapshot?.items ?? [];
  const liveCount = items.filter((i) => i.processAlive).length;
  const deadCount = items.length - liveCount;
  const hasOutcomes = outcomes.size > 0;

  return (
    <section
      className="recovery-workspace"
      aria-label="Recovery workspace"
      data-attention={items.length > 0}
    >
      <header className="recovery-workspace__header">
        <div>
          <h3 className="recovery-workspace__title">Recovery</h3>
          {items.length > 0 ? (
            <p className="recovery-workspace__summary">
              {liveCount} live · {deadCount} dead · {items.length} total
            </p>
          ) : null}
        </div>
        <button
          type="button"
          className="recovery-workspace__scan"
          disabled={phase === 'scanning' || executingKey !== null}
          onClick={() => void scan()}
        >
          {phase === 'scanning'
            ? 'Scanning…'
            : items.length > 0
              ? 'Fresh scan'
              : 'Scan for orphans'}
        </button>
      </header>

      {items.length > 0 ? (
        <div className="recovery-attention" aria-label="Recovery priority attention" role="alert">
          <span className="recovery-attention__priority" role="status">
            Recovery priority —{' '}
            {liveCount > 0
              ? `${liveCount} live orphan process${liveCount === 1 ? '' : 'es'}`
              : `${deadCount} dead orphan session${deadCount === 1 ? '' : 's'}`}
          </span>
        </div>
      ) : null}

      {error !== null ? (
        <p className="form-field__error" role="alert">
          {error}
        </p>
      ) : null}

      {snapshot !== null && items.length > 0 ? (
        <ul className="recovery-workspace__queue" aria-label="Recovery items">
          {items.map((item) => {
            const outcome = outcomes.get(item.key);
            // The orphan condition renders once, as one compact ErrorSurface
            // fed by the item's canonical needs_action error; the surface's
            // primary action is the existing single-item resume dispatch.
            const resolveResumeAction = (actionId: string): ErrorSurfaceAction | undefined => {
              if (actionId !== 'resume') return undefined;
              if (!item.allowedActions.includes('resume')) {
                return {
                  enabled: false,
                  label: 'Resume',
                  disabledReason: 'Resume is not available for this session.',
                };
              }
              if (executingKey === item.key) return { enabled: true, label: 'Resuming…' };
              if (executingKey !== null) {
                return {
                  enabled: false,
                  label: 'Resume',
                  disabledReason: 'Another recovery action is running.',
                };
              }
              return { enabled: true, label: 'Resume' };
            };
            return (
              <li
                key={item.key}
                className="recovery-workspace__item"
                data-alive={item.processAlive}
                data-outcome={outcome?.result ?? 'pending'}
              >
                <header className="recovery-workspace__item-header">
                  <span
                    className="recovery-workspace__item-process"
                    data-alive={item.processAlive}
                    aria-label={item.processAlive ? 'Live process' : 'Dead process'}
                  >
                    {item.processAlive ? '●' : '○'}
                  </span>
                  <span className="recovery-workspace__item-name">
                    {onNavigateToFeature !== undefined ? (
                      <button
                        type="button"
                        className="recovery-workspace__item-link"
                        onClick={() => onNavigateToFeature(item.featureId)}
                      >
                        {item.featureName ?? item.featureId}
                      </button>
                    ) : (
                      (item.featureName ?? item.featureId)
                    )}
                  </span>
                  {item.repoName !== undefined ? (
                    <code className="recovery-workspace__item-repo">{item.repoName}</code>
                  ) : null}
                  {item.phase !== undefined ? (
                    <code className="recovery-workspace__item-phase">{item.phase}</code>
                  ) : null}
                  {item.pid !== undefined && item.processAlive ? (
                    <code className="recovery-workspace__item-pid">PID {item.pid}</code>
                  ) : null}
                </header>
                <ErrorSurface
                  error={item.error}
                  variant="compact"
                  resolveAction={resolveResumeAction}
                  onAction={(actionId) => {
                    if (actionId === 'resume' && executingKey === null) {
                      void executeSingle(item, 'resume');
                    }
                  }}
                  explain={{
                    // The recovery snapshot in state is the durable home:
                    // the same snapshot-id/item-key pair the recovery log
                    // endpoint addresses.
                    reference: {
                      scope: 'recovery',
                      code: item.error.code,
                      snapshotId: snapshot.snapshotId,
                      key: item.key,
                    },
                    featureName: item.featureName ?? item.featureId,
                  }}
                />
                {item.logAvailable === true ? (
                  <div className="recovery-workspace__logs">
                    <button
                      type="button"
                      className="recovery-workspace__logs-toggle"
                      aria-expanded={expandedLog === item.key}
                      onClick={() => {
                        const next = expandedLog === item.key ? null : item.key;
                        setExpandedLog(next);
                        if (next !== null && !logText.has(next)) void loadLog(next);
                      }}
                      disabled={executingKey !== null}
                    >
                      {expandedLog === item.key ? 'Hide logs' : 'View recent logs'}
                    </button>
                    {expandedLog === item.key ? (
                      <pre
                        className="recovery-workspace__logs-body"
                        aria-label="Recent session logs"
                      >
                        {logLoading === item.key
                          ? 'Loading log…'
                          : logError.has(item.key)
                            ? `${logError.get(item.key)}\nRefresh the scan and try again.`
                            : (logText.get(item.key) ?? '').length > 0
                              ? logText.get(item.key)
                              : 'No log content available for this item.'}
                      </pre>
                    ) : null}
                  </div>
                ) : null}
                {outcome !== undefined ? (
                  <p
                    className="recovery-workspace__item-outcome"
                    data-outcome={outcome.result}
                    role="status"
                  >
                    {outcome.result === 'submitted'
                      ? `↳ ${humanizeAction(outcome.action)} submitted`
                      : '⊘ Not started'}
                  </p>
                ) : (
                  <div className="recovery-workspace__item-actions">
                    {item.allowedActions.includes('kill') ? (
                      <button
                        type="button"
                        className="recovery-workspace__action recovery-workspace__action--kill"
                        disabled={executingKey !== null}
                        onClick={(event) => {
                          killTriggerRef.current = event.currentTarget;
                          setKillTarget(item);
                        }}
                      >
                        Kill
                      </button>
                    ) : null}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      ) : phase === 'ready' ? (
        <p className="recovery-workspace__empty">No orphan sessions found.</p>
      ) : null}

      {phase === 'error' ? (
        <div className="recovery-workspace__error-actions">
          <button type="button" className="recovery-workspace__rescan" onClick={() => void scan()}>
            Scan for orphans
          </button>
        </div>
      ) : null}

      {hasOutcomes ? (
        <div className="recovery-workspace__complete">
          <p className="recovery-workspace__complete-text" role="status">
            Snapshot is stale — run a fresh scan to continue.
          </p>
          {batchResult !== null ? (
            <p className="recovery-workspace__batch-result" role="status">
              {batchResult}
            </p>
          ) : null}
          <button type="button" className="recovery-workspace__rescan" onClick={() => void scan()}>
            Fresh scan
          </button>
        </div>
      ) : null}

      {killTarget !== null ? (
        <div
          className="impact-dialog__backdrop"
          role="dialog"
          aria-label="Confirm kill"
          aria-modal="true"
        >
          <div ref={killDialogRef} className="impact-dialog" tabIndex={-1}>
            <span className="impact-dialog__eyebrow">Operational impact</span>
            <h3 className="impact-dialog__title">
              {killTarget.processAlive ? 'Kill live process' : 'Kill dead session'}
            </h3>
            <p className="impact-dialog__body">
              {killTarget.processAlive ? (
                <>
                  This will stop the live orphan process{' '}
                  <strong>PID {killTarget.pid ?? 'unknown'}</strong> for{' '}
                </>
              ) : (
                <>This will clean up the dead orphan session for </>
              )}
              <strong>{killTarget.featureName ?? killTarget.featureId}</strong>
              {killTarget.repoName !== undefined ? (
                <>
                  {' '}
                  on repository <strong>{killTarget.repoName}</strong>
                </>
              ) : null}
              {killTarget.phase !== undefined ? (
                <>
                  {' '}
                  during phase <strong>{killTarget.phase}</strong>
                </>
              ) : null}
              . The feature will be transitioned to an interrupted state and can be resumed later.
            </p>
            <div className="impact-dialog__actions">
              <button
                type="button"
                className="impact-dialog__cancel"
                onClick={() => {
                  setKillTarget(null);
                  killTriggerRef.current?.focus();
                }}
                disabled={executingKey !== null}
                autoFocus
              >
                Cancel
              </button>
              <button
                type="button"
                className="impact-dialog__confirm"
                onClick={() => void executeKill(killTarget)}
                disabled={executingKey !== null}
              >
                {executingKey !== null ? 'Killing…' : 'Kill process'}
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </section>
  );
}
