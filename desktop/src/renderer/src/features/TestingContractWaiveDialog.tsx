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

import { useCallback, useEffect, useRef, useState } from 'react';
import type { TestingContractItem, TestingContractSnapshot } from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import { ResultBox, useCompletionAction, type ActionResult } from './completion/completionShared';

export interface TestingContractWaiveDialogProps {
  featureId: string;
  onClose(): void;
  onWaived(): Promise<void> | void;
}

type ContractLoad =
  | { state: 'loading' }
  | { state: 'failed'; result: ActionResult }
  | { state: 'ready'; contract: TestingContractSnapshot };

/** Why a row cannot be ticked, or undefined when it can. */
export function itemLock(item: TestingContractItem): 'waived' | 'unwaivable' | undefined {
  if (item.disposition?.status === 'waived') return 'waived';
  if (!item.allowWaiver) return 'unwaivable';
  return undefined;
}

/**
 * Records user-authorized waivers on the current phase's testing contract
 * outside the verification gate: the harness skips waived rows at its next
 * pass. The checklist is the contract itself, read from the server when the
 * dialog opens.
 */
export function TestingContractWaiveDialog({
  featureId,
  onClose,
  onWaived,
}: TestingContractWaiveDialogProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  // The cockpit recreates onClose every render; reading it through a ref keeps
  // the focus and key effects from re-running on identity changes.
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const waiveAction = useCompletionAction();
  const [load, setLoad] = useState<ContractLoad>({ state: 'loading' });
  const [selected, setSelected] = useState<string[]>([]);
  const [reason, setReason] = useState('');

  useEffect(() => {
    dialogRef.current?.focus();
  }, []);

  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape' && !waiveAction.busy) {
        e.preventDefault();
        onCloseRef.current();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [waiveAction.busy]);

  const cancelledRef = useRef(false);
  const fetchContract = useCallback(async () => {
    try {
      const contract = await window.agentico.getTestingContract({ featureId });
      if (cancelledRef.current) return;
      setLoad({ state: 'ready', contract });
      // Drop ticks for rows the reloaded contract no longer offers.
      const waivable = new Set(
        contract.available
          ? contract.items.filter((item) => itemLock(item) === undefined).map((i) => i.itemId)
          : [],
      );
      setSelected((current) => current.filter((id) => waivable.has(id)));
    } catch (err) {
      if (cancelledRef.current) return;
      setLoad({ state: 'failed', result: { ok: false, error: parseIpcError(err) } });
    }
  }, [featureId]);

  useEffect(() => {
    cancelledRef.current = false;
    setLoad({ state: 'loading' });
    void fetchContract();
    return () => {
      cancelledRef.current = true;
    };
  }, [fetchContract]);

  const ready = selected.length > 0 && reason.trim() !== '';

  const toggle = (itemId: string) =>
    setSelected((current) =>
      current.includes(itemId) ? current.filter((id) => id !== itemId) : [...current, itemId],
    );

  const handleWaive = async () => {
    if (load.state !== 'ready' || !load.contract.available) return;
    const { roadmapPhase, revision } = load.contract;
    const ok = await waiveAction.run(
      async () => {
        const outcome = await window.agentico
          .waiveTestingContract({
            featureId,
            itemIds: selected,
            reason: reason.trim(),
            roadmapPhase,
            contractRevision: revision,
          })
          .catch((err: unknown) => {
            // The contract moved under the selection: show the current one so
            // the user re-selects against what the server will accept.
            if (parseIpcError(err).code === 'conflict') void fetchContract();
            throw err;
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

  const renderItems = () => {
    if (load.state === 'loading') {
      return (
        <p role="status" aria-live="polite" className="cockpit__loading">
          Loading contract…
        </p>
      );
    }
    if (load.state === 'failed') return <ResultBox result={load.result} />;
    if (!load.contract.available) {
      return <p className="setup-step__empty">This phase has no testing contract yet.</p>;
    }
    if (load.contract.items.length === 0) {
      return <p className="setup-step__empty">The contract has no items.</p>;
    }
    return (
      <fieldset className="contract-waive-dialog__items">
        <legend>
          Phase {load.contract.roadmapPhase} contract · revision {load.contract.revision}
        </legend>
        {load.contract.items.map((item) => {
          const lock = itemLock(item);
          const title =
            lock === 'waived'
              ? (item.disposition?.reason ?? 'Already waived')
              : lock === 'unwaivable'
                ? 'Cannot be waived'
                : item.command;
          return (
            <label
              key={item.itemId}
              className="contract-waive-dialog__item"
              data-lock={lock}
              title={title}
            >
              <input
                type="checkbox"
                aria-label={item.itemId}
                checked={selected.includes(item.itemId)}
                disabled={waiveAction.busy || lock !== undefined}
                onChange={() => toggle(item.itemId)}
              />
              <span>
                <strong>{item.name}</strong>
                <code>{item.itemId}</code>
                <span className="contract-waive-dialog__tags">
                  <span className="contract-waive-dialog__tag">{item.source}</span>
                  <span className="contract-waive-dialog__tag">{item.owner}</span>
                  {item.required ? null : (
                    <span className="contract-waive-dialog__tag">optional</span>
                  )}
                  {item.allowSubstitution ? (
                    <span className="contract-waive-dialog__tag">Substitute accepted</span>
                  ) : null}
                  {lock === 'waived' ? (
                    <span className="contract-waive-dialog__tag contract-waive-dialog__tag--state">
                      Waived
                    </span>
                  ) : null}
                  {lock === 'unwaivable' ? (
                    <span className="contract-waive-dialog__tag contract-waive-dialog__tag--state">
                      Cannot be waived
                    </span>
                  ) : null}
                </span>
                {lock === 'waived' && item.disposition?.reason !== undefined ? (
                  <small>{item.disposition.reason}</small>
                ) : null}
                {item.capabilities.length === 0 ? null : (
                  <small>Needs {item.capabilities.join(', ')}</small>
                )}
              </span>
            </label>
          );
        })}
      </fieldset>
    );
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
        {renderItems()}
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
