import { useEffect, useRef } from 'react';
import { useCompletionAction, ResultBox, type CompletionAction } from './completionShared';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

export interface CleanupConfirmProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [k: string]: unknown }>;
  onClose: () => void;
  onDispatched: () => void;
}

export function CleanupConfirm({
  featureId,
  preflight,
  dispatchAction,
  onClose,
  onDispatched,
}: CleanupConfirmProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const cleanupAction = useCompletionAction();
  useEffect(() => {
    dialogRef.current?.focus();
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !cleanupAction.busy) {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [cleanupAction.busy, onClose]);

  const handleCleanup = async () => {
    const ok = await cleanupAction.run(
      () =>
        dispatchAction(featureId, 'cleanup', {
          source_revision: preflight.sourceRevision,
          target: 'worktrees',
        }).then((r) => r.result),
      async () => {
        onDispatched();
      },
    );
    if (ok) onClose();
  };

  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="cleanup-dialog-title"
        className="impact-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="cleanup-dialog-title">Clean worktrees?</h3>
        <div className="completion-workspace__cleanup-consequences">
          <div className="completion-workspace__consequence-group">
            <h4>Removes</h4>
            <ul>
              <li>Completed feature worktrees</li>
            </ul>
          </div>
          <div className="completion-workspace__consequence-group">
            <h4>Preserves</h4>
            <ul>
              <li>Branches</li>
              <li>Feature/run history</li>
              <li>Artifacts</li>
              <li>PR metadata</li>
              <li>Repository-cycle records</li>
            </ul>
          </div>
        </div>
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={cleanupAction.busy} autoFocus>
            Cancel
          </button>
          <button
            type="button"
            className="cockpit__delete-button"
            onClick={() => void handleCleanup()}
            disabled={cleanupAction.busy}
          >
            {cleanupAction.busy ? 'Cleaning…' : 'Clean worktrees'}
          </button>
        </div>
        <ResultBox result={cleanupAction.result} />
      </div>
    </div>
  );
}
