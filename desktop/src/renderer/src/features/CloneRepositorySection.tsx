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
 * Settings repository area: the shared clone form and the connected
 * server's active/recent clone operations. Snapshots are authoritative —
 * the list refreshes from SSE invalidations and re-reads, never from
 * transport completion — and a failed list request never erases known work.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { parseIpcError } from '../wizard/ipcError';
import type {
  CanonicalError,
  CloneOperation,
  ConnectionState,
  ReadinessSnapshot,
} from '../../../shared/ipc';
import {
  CloneOperationView,
  CloneRepositoryForm,
  serverDescriptor,
  type CloneStartInput,
} from './cloneViews';

export function CloneRepositorySection({
  readiness,
  connection,
}: {
  readiness: ReadinessSnapshot | null;
  connection: ConnectionState;
}) {
  const cloneableRoots = useMemo(
    () => (readiness?.workspaceRoots ?? []).filter((root) => root.valid && root.cloneEligible),
    [readiness],
  );

  const [announcement, setAnnouncement] = useState<string | null>(null);

  const [operations, setOperations] = useState<CloneOperation[]>([]);
  const [operationsError, setOperationsError] = useState<CanonicalError | null>(null);
  const [operationsLoaded, setOperationsLoaded] = useState(false);
  const [actionPending, setActionPending] = useState<string | null>(null);
  const fetchSeq = useRef(0);

  const refreshOperations = useCallback(() => {
    const seq = ++fetchSeq.current;
    void window.agentico
      .listCloneOperations()
      .then((list) => {
        // Late replies from a previous server or refresh cannot replace
        // the current list: only the newest request may land.
        if (seq !== fetchSeq.current) return;
        setOperations(list.operations);
        setOperationsError(null);
        setOperationsLoaded(true);
      })
      .catch((e: unknown) => {
        if (seq !== fetchSeq.current) return;
        // A failed list request does not erase known work or label it
        // failed; the error stays visible and recoverable.
        setOperationsError(parseIpcError(e));
      });
  }, []);

  useEffect(() => {
    refreshOperations();
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type === 'invalidated') {
        if (event.kind === 'resync' || event.kind.startsWith('clone.')) {
          refreshOperations();
        }
        if (event.kind === 'resync' || event.kind.startsWith('config')) {
          // Publication changes workspace discovery.
          void window.agentico.getReadiness().catch(() => undefined);
        }
      }
    });
    return unsub;
  }, [refreshOperations]);

  // Switching servers replaces the entire view: the old server's work is
  // not this server's work. The initial mount is not a switch.
  const serverKey = connection.status === 'ready' ? connection.serverKey : null;
  const previousServerKey = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    if (previousServerKey.current === undefined) {
      previousServerKey.current = serverKey;
      return;
    }
    if (previousServerKey.current === serverKey) return;
    previousServerKey.current = serverKey;
    setOperations([]);
    setOperationsLoaded(false);
    refreshOperations();
  }, [serverKey, refreshOperations]);

  const handleStart = (input: CloneStartInput): Promise<void> =>
    window.agentico.startClone(input).then(() => {
      setAnnouncement(
        `Clone started into ${input.rootPath}/${input.destination} on ${serverDescriptor(connection)}. It keeps running on the server when this view closes.`,
      );
      refreshOperations();
    });

  const handleAction = (
    action: 'cancel' | 'cleanup' | 'retry',
    operation: CloneOperation,
  ): void => {
    if (actionPending !== null) return;
    setActionPending(operation.id);
    const call =
      action === 'cancel'
        ? window.agentico.cancelCloneOperation(operation.id)
        : action === 'cleanup'
          ? window.agentico.retryCloneCleanup(operation.id)
          : window.agentico.retryCloneOperation(operation.id);
    void call
      .then(() => {
        setActionPending(null);
        setAnnouncement(
          action === 'cancel'
            ? `Cancellation requested for ${operation.destination}.`
            : action === 'cleanup'
              ? `Cleanup rechecked for ${operation.destination}.`
              : `Fresh retry started for ${operation.destination}.`,
        );
        refreshOperations();
      })
      .catch((e: unknown) => {
        setActionPending(null);
        setOperationsError(parseIpcError(e));
      });
  };

  return (
    <section className="settings-panel__section" aria-label="Clone repositories">
      <h2 className="settings-panel__section-title">Clone a repository</h2>
      <p className="settings-panel__section-desc">
        The connected server clones into one of its workspace roots. Work keeps running on{' '}
        {serverDescriptor(connection)} after this view closes; explicit Cancel stops it.
      </p>

      <CloneRepositoryForm
        cloneableRoots={cloneableRoots}
        serverLabel={serverDescriptor(connection)}
        idPrefix="clone"
        onStart={handleStart}
      />

      <h3 className="settings-panel__section-subtitle">Clone operations</h3>
      <p className="settings-panel__section-desc">
        Active and recent clones on {serverDescriptor(connection)}. Recent history is kept for seven
        days.
      </p>
      {operationsError !== null ? (
        <ErrorSurface
          error={operationsError}
          variant="compact"
          localAction={{ label: 'Retry', onAction: () => refreshOperations() }}
        />
      ) : null}
      {!operationsError && !operationsLoaded ? (
        <p className="settings-panel__clone-empty" role="status">
          Loading clone operations…
        </p>
      ) : operations.length === 0 ? (
        <p className="settings-panel__clone-empty">
          No clone operations yet on {serverDescriptor(connection)}.
        </p>
      ) : (
        <ul className="settings-panel__clone-operations">
          {operations.map((operation) => (
            <li key={operation.id} className="settings-panel__clone-operation-item">
              <CloneOperationView
                operation={operation}
                serverLabel={serverDescriptor(connection)}
                actionPending={actionPending}
                onAction={handleAction}
              />
            </li>
          ))}
        </ul>
      )}

      <details className="settings-panel__clone-help">
        <summary>How clones, Close, Cancel and Retry work</summary>
        <ul>
          <li>
            Closing this view or Settings never stops a clone: work continues on its server.
            Explicit Cancel stops it and cleans up only the server's own incomplete data.
          </li>
          <li>
            Restarting the app-owned server interrupts unfinished clones; they show as interrupted
            (or cleanup pending) and can be retried.
          </li>
          <li>
            Retry cleanup rechecks the server's staged data; fresh Retry is available only after
            cleanup completed, and always starts a new clone from scratch.
          </li>
          <li>
            Accepted requests are remembered: repeating the same request never starts a second
            clone. Recent history is kept for seven days on the server.
          </li>
        </ul>
      </details>

      {announcement !== null ? (
        <p className="settings-panel__clone-announcement" role="status" aria-live="polite">
          {announcement}
        </p>
      ) : null}
    </section>
  );
}
