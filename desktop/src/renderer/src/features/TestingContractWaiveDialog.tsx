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

import { useCallback, useEffect, useId, useRef, useState } from 'react';
import type { TestingContractItem, TestingContractSnapshot } from '../../../shared/ipc';
import { ErrorSurface } from '../components/ErrorSurface';
import { useModalDismiss } from '../components/useModalDismiss';
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

/** The primary button's verb, counting the ticked checks. */
export function waiveLabel(count: number): string {
  if (count === 0) return 'Waive checks';
  return `Waive ${count} ${count === 1 ? 'check' : 'checks'}`;
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
  const titleId = useId();
  const waiveAction = useCompletionAction();
  const [load, setLoad] = useState<ContractLoad>({ state: 'loading' });
  const [selected, setSelected] = useState<string[]>([]);
  const [reason, setReason] = useState('');

  // Escape, the Tab cycle, and the opener restore are the shared modal
  // behavior; a waiver in flight only refuses the dismissal.
  useModalDismiss(dialogRef, () => {
    if (!waiveAction.busy) onClose();
  });

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

  const reload = () => {
    setLoad({ state: 'loading' });
    void fetchContract();
  };

  useEffect(() => {
    cancelledRef.current = false;
    setLoad({ state: 'loading' });
    void fetchContract();
    return () => {
      cancelledRef.current = true;
    };
  }, [fetchContract]);

  const waivableCount =
    load.state === 'ready' && load.contract.available
      ? load.contract.items.filter((item) => itemLock(item) === undefined).length
      : 0;
  // The reason and the verb only exist while there is something to waive.
  const formOpen = waivableCount > 0;
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

  const renderNotice = (text: string, action: 'Reload' | null) => (
    <p className="contract-waive-dialog__notice">
      <span>{text}</span>
      {action === null ? null : (
        <button type="button" className="attention-button" onClick={reload}>
          {action}
        </button>
      )}
    </p>
  );

  const renderItems = () => {
    if (load.state === 'loading') {
      return (
        <p role="status" aria-live="polite" className="cockpit__loading">
          Loading contract…
        </p>
      );
    }
    if (load.state === 'failed') {
      if (load.result.ok) return null;
      return (
        <ErrorSurface
          error={load.result.error}
          variant="compact"
          localAction={{ label: 'Retry', onAction: reload }}
        />
      );
    }
    if (!load.contract.available) {
      return renderNotice('This phase has no testing contract yet.', 'Reload');
    }
    if (load.contract.items.length === 0) {
      return renderNotice('The contract has no items.', 'Reload');
    }
    return (
      <fieldset className="contract-waive-dialog__items">
        <legend>
          Phase {load.contract.roadmapPhase} contract · revision {load.contract.revision}
        </legend>
        {!formOpen ? renderNotice('Every item is already waived or cannot be waived.', null) : null}
        {load.contract.items.map((item) => {
          const lock = itemLock(item);
          // Two labelledby targets so the name reads "Unit tests · api" with a
          // space in every accessible-name implementation.
          const nameId = `${titleId}-${item.itemId}-name`;
          const repoId = `${titleId}-${item.itemId}-repo`;
          const hasRepo = item.repo !== undefined && item.repo !== '';
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
                aria-labelledby={hasRepo ? `${nameId} ${repoId}` : nameId}
                checked={selected.includes(item.itemId)}
                disabled={waiveAction.busy || lock !== undefined}
                onChange={() => toggle(item.itemId)}
              />
              <span>
                <span className="contract-waive-dialog__identity">
                  <strong id={nameId}>{item.name}</strong>
                  {hasRepo ? (
                    <>
                      {' '}
                      <span id={repoId} className="contract-waive-dialog__repo">
                        · {item.repo}
                      </span>
                    </>
                  ) : null}
                </span>
                <span className="contract-waive-dialog__tags">
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
                  {item.required ? null : (
                    <span className="contract-waive-dialog__tag">optional</span>
                  )}
                  {item.allowSubstitution ? (
                    <span className="contract-waive-dialog__tag">Substitution allowed</span>
                  ) : null}
                </span>
                {lock === 'waived' && item.disposition?.reason !== undefined ? (
                  <small>{item.disposition.reason}</small>
                ) : null}
                {item.capabilities.length === 0 ? null : (
                  <small>Needs {item.capabilities.join(', ')}</small>
                )}
                <small className="contract-waive-dialog__meta">
                  <code>{item.itemId}</code> · {item.source} · {item.owner}
                </small>
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
        aria-labelledby={titleId}
        className="impact-dialog contract-waive-dialog"
        tabIndex={-1}
      >
        <header className="contract-waive-dialog__header">
          <span className="impact-dialog__eyebrow contract-waive-dialog__eyebrow">
            Testing contract
          </span>
          <h3 id={titleId}>Waive contract items?</h3>
          <p className="impact-dialog__lede">
            Waived items are recorded as user-authorized and skipped at the next verification pass.
            The contract keeps the record.
          </p>
        </header>
        <div className="contract-waive-dialog__body">{renderItems()}</div>
        <footer className="contract-waive-dialog__footer">
          {formOpen ? (
            <label className="need-input-sheet__question">
              <span>
                <strong>Reason</strong>
                <small>Required · recorded on the contract next to each waiver.</small>
              </span>
              <textarea
                aria-label="Waiver reason"
                required
                value={reason}
                disabled={waiveAction.busy}
                onChange={(event) => setReason(event.target.value)}
              />
            </label>
          ) : null}
          <div className="impact-dialog__actions">
            <button type="button" onClick={onClose} disabled={waiveAction.busy}>
              Cancel
            </button>
            {formOpen ? (
              <button
                type="button"
                className="attention-button attention-button--primary"
                onClick={() => void handleWaive()}
                disabled={!ready || waiveAction.busy || waiveAction.reconciling}
              >
                {waiveAction.busy ? 'Waiving…' : waiveLabel(selected.length)}
              </button>
            ) : null}
          </div>
          <ResultBox result={waiveAction.result} />
        </footer>
      </div>
    </div>
  );
}
