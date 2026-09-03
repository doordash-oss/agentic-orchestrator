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
        <div className="impact-dialog__preview">
          <p className="impact-dialog__lede">
            This deletes the local working copies left over from completed features. Everything
            under Kept stays.
          </p>
          <div className="impact-dialog__lane impact-dialog__lane--removed">
            <section>
              <h4>Removed</h4>
              <ul>
                <li>Completed feature worktrees</li>
              </ul>
            </section>
          </div>
          <div className="impact-dialog__lane impact-dialog__lane--kept">
            <section>
              <h4>Kept</h4>
              <ul>
                <li>Branches</li>
                <li>Feature/run history</li>
                <li>Artifacts</li>
                <li>PR metadata</li>
              </ul>
            </section>
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
