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
 * The creation sheet's nested clone view. It embeds the same shared clone
 * form and operation-state presentation as Settings. The owning sheet
 * tracks the associated operation and adopts usable results into its own
 * draft; this view only presents state and drives the existing server
 * actions (cancel, cleanup, fresh retry).
 *
 * Close only detaches the view: the clone keeps running on the server, the
 * association stays with the draft, and background completion still adopts.
 * Escape is handled here, never by the enclosing sheet.
 */
import { useEffect, useRef, useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { parseIpcError } from '../wizard/ipcError';
import type {
  CanonicalError,
  CloneOperation,
  ConnectionState,
  ReadinessSnapshot,
  WorkspaceRootState,
} from '../../../shared/ipc';
import {
  CloneOperationView,
  CloneRepositoryForm,
  RETRYABLE_STATES,
  serverDescriptor,
  type CloneStartInput,
} from './cloneViews';
import type { CloneAssociation } from './creationDrafts';

export interface PickerCloneDialogProps {
  connection: ConnectionState;
  workspaceRoots: readonly WorkspaceRootState[];
  association: CloneAssociation | null;
  /** The authoritative snapshot of the associated operation, from the sheet. */
  operation: CloneOperation | null;
  /** Records association mutations (pre-flight, recovered id, fresh retry). */
  onAssociate(association: CloneAssociation): void;
  /** Re-resolves the tracked operation after an action. */
  onRefresh(): void;
  /** Applies an authoritative readiness snapshot (e.g. after a root addition). */
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
  /** Close detaches the view; the draft and association stay. */
  onClose(): void;
}

export function PickerCloneDialog({
  connection,
  workspaceRoots,
  association,
  operation,
  onAssociate,
  onRefresh,
  onReadinessChanged,
  onClose,
}: PickerCloneDialogProps) {
  const [actionError, setActionError] = useState<CanonicalError | null>(null);
  const [actionPending, setActionPending] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    dialogRef.current?.focus();
  }, []);

  const handleStart = (input: CloneStartInput): Promise<void> => {
    // Record the association before the flight: a lost acceptance is
    // recovered later by idempotency key (the sheet's resolver does that).
    onAssociate({
      idempotencyKey: input.idempotencyKey,
      operationId: null,
      adopted: false,
      startInFlight: true,
    });
    setAnnouncement(
      `Clone started into ${input.rootPath}/${input.destination} on ${serverDescriptor(connection)}. It keeps running on the server when this view closes.`,
    );
    return window.agentico.startClone(input).then(
      (started) => {
        onAssociate({
          idempotencyKey: input.idempotencyKey,
          operationId: started.id,
          adopted: false,
          startInFlight: false,
        });
      },
      (err: unknown) => {
        // The acceptance is unknown: switch to recovering it by key.
        onAssociate({
          idempotencyKey: input.idempotencyKey,
          operationId: null,
          adopted: false,
          startInFlight: false,
        });
        throw err;
      },
    );
  };

  const handleAction = (action: 'cancel' | 'cleanup' | 'retry', tracked: CloneOperation): void => {
    if (actionPending !== null || association === null) return;
    setActionPending(tracked.id);
    const call =
      action === 'cancel'
        ? window.agentico.cancelCloneOperation(tracked.id)
        : action === 'cleanup'
          ? window.agentico.retryCloneCleanup(tracked.id)
          : window.agentico.retryCloneOperation(tracked.id);
    void call
      .then((fresh) => {
        setActionPending(null);
        setAnnouncement(
          action === 'cancel'
            ? `Cancellation requested for ${tracked.destination}.`
            : action === 'cleanup'
              ? `Cleanup rechecked for ${tracked.destination}.`
              : `Fresh retry started for ${tracked.destination}.`,
        );
        if (action === 'retry') {
          // A fresh attempt gets a new operation and idempotency key while
          // remaining associated with the same live draft.
          onAssociate({
            idempotencyKey: fresh.idempotencyKey,
            operationId: fresh.id,
            adopted: false,
            startInFlight: false,
          });
        }
        onRefresh();
      })
      .catch((e: unknown) => {
        setActionPending(null);
        setActionError(parseIpcError(e));
      });
  };

  const tracking = operation !== null;
  const showForm = operation === null || RETRYABLE_STATES.includes(operation.state);

  return (
    <div className="impact-dialog__backdrop creation-clone__backdrop">
      <div
        ref={dialogRef}
        className="impact-dialog creation-clone"
        role="dialog"
        aria-modal="true"
        aria-label="Clone a repository"
        tabIndex={-1}
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.stopPropagation();
            onClose();
          }
        }}
      >
        <h2>Clone a repository</h2>
        <p className="creation-clone__desc">
          The connected server clones into one of its workspace roots and the finished repository is
          selected here. Work keeps running on {serverDescriptor(connection)} after this view
          closes; explicit Cancel stops it.
        </p>

        {showForm ? (
          <CloneRepositoryForm
            workspaceRoots={workspaceRoots}
            connection={connection}
            idPrefix="picker-clone"
            onStart={handleStart}
            onReadinessChanged={onReadinessChanged}
          />
        ) : null}

        {tracking ? (
          <div className="creation-clone__operation">
            <CloneOperationView
              operation={operation as CloneOperation}
              serverLabel={serverDescriptor(connection)}
              actionPending={actionPending}
              onAction={handleAction}
            />
          </div>
        ) : null}

        {actionError !== null ? (
          <ErrorSurface
            error={actionError}
            variant="compact"
            localAction={{ label: 'Retry', onAction: () => onRefresh() }}
          />
        ) : null}

        <p className="creation-clone__help">
          Close leaves the clone running on {serverDescriptor(connection)}; Cancel clone stops it
          and cleans up only the server's own incomplete data.
        </p>

        {announcement !== null ? (
          <p role="status" aria-live="polite">
            {announcement}
          </p>
        ) : null}

        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
