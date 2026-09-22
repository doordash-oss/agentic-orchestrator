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

import { useEffect, useRef, useState } from 'react';
import { ResultBox, useCompletionAction } from './completion/completionShared';

export interface TestingContractWaiveCandidate {
  itemId: string;
  name: string;
}

export interface TestingContractWaiveDialogProps {
  featureId: string;
  /** Contract items already named by the feature's blocked verification, if any. */
  candidates: readonly TestingContractWaiveCandidate[];
  onClose(): void;
  onWaived(): Promise<void> | void;
}

/** Splits free-text ids on newlines and commas, dropping blanks and repeats. */
export function parseItemIds(text: string): string[] {
  const ids: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const id = raw.trim();
    if (id !== '' && !ids.includes(id)) ids.push(id);
  }
  return ids;
}

/**
 * Records user-authorized waivers on the current phase's testing contract
 * outside the verification gate: the harness skips waived rows at its next
 * pass. Item ids come from the blocked checks when a gate has named them and
 * from free text otherwise.
 */
export function TestingContractWaiveDialog({
  featureId,
  candidates,
  onClose,
  onWaived,
}: TestingContractWaiveDialogProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const waiveAction = useCompletionAction();
  const [selected, setSelected] = useState<string[]>([]);
  const [extraIds, setExtraIds] = useState('');
  const [reason, setReason] = useState('');

  useEffect(() => {
    dialogRef.current?.focus();
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !waiveAction.busy) {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [waiveAction.busy, onClose]);

  const itemIds = [...selected, ...parseItemIds(extraIds).filter((id) => !selected.includes(id))];
  const ready = itemIds.length > 0 && reason.trim() !== '';

  const toggle = (itemId: string) =>
    setSelected((current) =>
      current.includes(itemId) ? current.filter((id) => id !== itemId) : [...current, itemId],
    );

  const handleWaive = async () => {
    const ok = await waiveAction.run(
      async () => {
        const outcome = await window.agentico.waiveTestingContract({
          featureId,
          itemIds,
          reason: reason.trim(),
        });
        const count = outcome.waivedItems.length;
        return `Waived ${count} ${count === 1 ? 'item' : 'items'} · contract revision ${outcome.contractRevision}`;
      },
      async () => {
        await onWaived();
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
        aria-labelledby="contract-waive-dialog-title"
        className="impact-dialog contract-waive-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow contract-waive-dialog__eyebrow">
          Testing contract
        </span>
        <h3 id="contract-waive-dialog-title">Waive contract items?</h3>
        <p className="impact-dialog__lede">
          Waived items are recorded as user-authorized and skipped at the next verification pass.
          The contract keeps the record.
        </p>
        {candidates.length === 0 ? null : (
          <fieldset className="contract-waive-dialog__items">
            <legend>Blocked checks</legend>
            {candidates.map((candidate) => (
              <label key={candidate.itemId} className="contract-waive-dialog__item">
                <input
                  type="checkbox"
                  aria-label={candidate.itemId}
                  checked={selected.includes(candidate.itemId)}
                  disabled={waiveAction.busy}
                  onChange={() => toggle(candidate.itemId)}
                />
                <span>
                  <strong>{candidate.name}</strong>
                  <code>{candidate.itemId}</code>
                </span>
              </label>
            ))}
          </fieldset>
        )}
        <label className="need-input-sheet__question">
          <span>
            <strong>{candidates.length === 0 ? 'Item ids' : 'Other item ids'}</strong>
            <small>One per line, as written in testing-contract.yaml.</small>
          </span>
          <textarea
            aria-label="Contract item ids"
            value={extraIds}
            disabled={waiveAction.busy}
            onChange={(event) => setExtraIds(event.target.value)}
          />
        </label>
        <label className="need-input-sheet__question">
          <span>
            <strong>Reason</strong>
            <small>Recorded on the contract next to each waiver.</small>
          </span>
          <textarea
            aria-label="Waiver reason"
            value={reason}
            disabled={waiveAction.busy}
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={waiveAction.busy} autoFocus>
            Cancel
          </button>
          <button
            type="button"
            className="attention-button attention-button--primary"
            onClick={() => void handleWaive()}
            disabled={!ready || waiveAction.busy || waiveAction.reconciling}
          >
            {waiveAction.busy ? 'Waiving…' : 'Waive items'}
          </button>
        </div>
        <ResultBox result={waiveAction.result} />
      </div>
    </div>
  );
}
