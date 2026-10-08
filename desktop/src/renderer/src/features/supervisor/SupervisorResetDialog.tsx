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
 * Confirms "New conversation" when work would be lost: a turn in progress
 * or queued messages. Shown only then; an idle conversation resets at once.
 */
import { useState } from 'react';
import { SettingsConfirmationDialog } from '../SettingsConfirmationDialog';

export function SupervisorResetDialog({
  turnActive,
  queued,
  onCancel,
  onConfirm,
}: {
  turnActive: boolean;
  queued: number;
  onCancel(): void;
  onConfirm(): Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const close = (): void => {
    if (!busy) onCancel();
  };
  const confirm = async (): Promise<void> => {
    if (busy) return;
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
    }
  };
  return (
    <SettingsConfirmationDialog ariaLabel="Start a new conversation" onCancel={close}>
      <div className="supervisor-switch-dialog">
        <h2>Start a new conversation?</h2>
        {turnActive ? <p>The current turn will be stopped.</p> : null}
        {queued > 0 ? (
          <p>
            {queued === 1
              ? 'The queued message will be discarded.'
              : `${String(queued)} queued messages will be discarded.`}
          </p>
        ) : null}
        <p>The harness and model stay the same, and this conversation stays on disk.</p>
        <div className="supervisor-switch-dialog__actions">
          <button type="button" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button type="button" onClick={() => void confirm()} disabled={busy}>
            New conversation
          </button>
        </div>
      </div>
    </SettingsConfirmationDialog>
  );
}
