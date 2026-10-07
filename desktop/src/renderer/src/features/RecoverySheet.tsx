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
 * The Recovery sheet: the window-modal home of the two operational panels,
 * the recovery workspace stacked above bulk resume/retry, reachable from
 * every page (the toolbar button, the Recovery and Bulk Resume / Retry
 * commands, and the attention inbox's recovery jump).
 *
 * The shell mounts one sheet per open, so the recovery workspace's
 * auto-scan runs once per open, and a bulk preview requested by the opening
 * route (`bulkPreviewKey`) belongs to this instance alone — a later plain
 * open starts without one. The Recovery kill confirmation is an
 * `aria-modal` dialog inside the sheet, which the shared dismiss hook defers
 * to: Escape closes the confirmation first and the sheet only after.
 */
import { useId, useRef } from 'react';
import { useModalDismiss } from '../components/useModalDismiss';
import { BulkPreviewPanel } from './BulkPreviewPanel';
import { RecoveryWorkspace } from './RecoveryWorkspace';

export interface RecoverySheetProps {
  /** Non-null when the opening route asked for a bulk preview (⌘⇧B, the bulk command). */
  bulkPreviewKey: number | null;
  onClose(): void;
  /** A recovery item's feature link; the shell closes the sheet and opens the feature. */
  onNavigateToFeature?(featureId: string): void;
}

export function RecoverySheet({
  bulkPreviewKey,
  onClose,
  onNavigateToFeature,
}: RecoverySheetProps) {
  const sheetRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  useModalDismiss(sheetRef, onClose);

  return (
    <div className="sheet-scrim" onMouseDown={onClose}>
      <div
        ref={sheetRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="sheet recovery-sheet"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="recovery-sheet__header">
          <div>
            <h2 id={titleId} className="recovery-sheet__title">
              Recovery
            </h2>
            <p className="recovery-sheet__lede">
              Clean up orphaned sessions, then resume or retry stalled features.
            </p>
          </div>
          <button type="button" className="sheet__footer-secondary" onClick={onClose}>
            Close
          </button>
        </header>
        <div className="sheet__body recovery-sheet__body">
          <RecoveryWorkspace
            {...(onNavigateToFeature === undefined ? {} : { onNavigateToFeature })}
          />
          <BulkPreviewPanel autoPreviewKey={bulkPreviewKey} />
        </div>
      </div>
    </div>
  );
}
