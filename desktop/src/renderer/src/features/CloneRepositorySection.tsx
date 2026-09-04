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
 * Settings repository area: the clone form and the connected server's
 * active/recent clone operations. Snapshots are authoritative — the list
 * refreshes from SSE invalidations and re-reads, never from transport
 * completion — and a failed list request never erases known work.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { parseIpcError } from '../wizard/ipcError';
import type { CanonicalError } from '../../../shared/api/parse';
import type { CloneOperation, ConnectionState, ReadinessSnapshot } from '../../../shared/ipc';

const STATE_LABELS: Record<CloneOperation['state'], string> = {
  accepted: 'Starting',
  running: 'Cloning',
  finalizing: 'Publishing',
  cancelling: 'Cancelling',
  succeeded: 'Succeeded',
  failed: 'Failed',
  cancelled: 'Cancelled',
  interrupted: 'Interrupted',
  cleanup_pending: 'Cleanup pending',
};

const ACTIVE_STATES: CloneOperation['state'][] = [
  'accepted',
  'running',
  'finalizing',
  'cancelling',
];

const RETRYABLE_STATES: CloneOperation['state'][] = ['failed', 'cancelled', 'interrupted'];

/** Strips a trailing ".git" and trailing slashes, mirroring the server's suggestion rule. */
function suggestDestination(remote: string): string {
  let trimmed = remote
    .trim()
    .replace(/[?#].*$/, '')
    .replace(/\/+$/, '');
  const lastSegment = trimmed.split('/').pop() ?? '';
  trimmed = lastSegment;
  if (trimmed.includes(':')) {
    trimmed =
      trimmed
        .slice(trimmed.lastIndexOf(':') + 1)
        .split('/')
        .pop() ?? '';
  }
  trimmed = trimmed.replace(/\.git$/, '');
  return trimmed.slice(0, 128);
}

function serverDescriptor(connection: ConnectionState): string {
  if (connection.status === 'ready') {
    return connection.kind === 'remote' ? 'the remote server' : 'this computer';
  }
  return 'the connected server';
}

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

  const [remote, setRemote] = useState('');
  const [rootPath, setRootPath] = useState('');
  const [destination, setDestination] = useState('');
  const [destinationEdited, setDestinationEdited] = useState(false);
  const [remoteError, setRemoteError] = useState<string | null>(null);
  const [destinationError, setDestinationError] = useState<string | null>(null);
  const [formError, setFormError] = useState<CanonicalError | null>(null);
  const [starting, setStarting] = useState(false);
  const [announcement, setAnnouncement] = useState<string | null>(null);

  const [operations, setOperations] = useState<CloneOperation[]>([]);
  const [operationsError, setOperationsError] = useState<CanonicalError | null>(null);
  const [operationsLoaded, setOperationsLoaded] = useState(false);
  const [actionPending, setActionPending] = useState<string | null>(null);
  const fetchSeq = useRef(0);

  // Keep the root selection valid as readiness changes.
  useEffect(() => {
    if (cloneableRoots.length === 0) {
      setRootPath('');
      return;
    }
    if (!cloneableRoots.some((root) => root.path === rootPath)) {
      setRootPath(cloneableRoots[0]?.path ?? '');
    }
  }, [cloneableRoots, rootPath]);

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

  const handleRemoteChange = (value: string): void => {
    setRemote(value);
    setRemoteError(null);
    if (!destinationEdited) {
      setDestination(suggestDestination(value));
    }
  };

  const handleStart = (): void => {
    if (starting) return;
    setFormError(null);
    const trimmedRemote = remote.trim();
    const trimmedDestination = destination.trim();
    let valid = true;
    if (trimmedRemote === '') {
      setRemoteError('A repository URL is required.');
      valid = false;
    } else if (
      !/^(https?:\/\/|ssh:\/\/)|^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:[^/]/.test(trimmedRemote)
    ) {
      setRemoteError('Use an https://, http://, ssh:// or SCP-style repository URL.');
      valid = false;
    } else {
      setRemoteError(null);
    }
    if (rootPath === '') {
      setFormError(null);
      valid = false;
      setDestinationError('Choose a destination root.');
    } else {
      setDestinationError(null);
    }
    if (trimmedDestination === '') {
      setDestinationError('A destination folder name is required.');
      valid = false;
    } else if (/[\\/\s]|^\.|\.$|^-/.test(trimmedDestination)) {
      setDestinationError(
        'The destination must be a single new folder name (no separators, dots at the edges, or leading dash).',
      );
      valid = false;
    } else {
      if (destinationError !== null && !valid) {
        // keep the root error; destination is fine
      }
      setDestinationError(null);
    }
    if (!valid) return;
    setStarting(true);
    void window.agentico
      .startClone({
        remoteUrl: trimmedRemote,
        rootPath,
        destination: trimmedDestination,
        idempotencyKey: crypto.randomUUID(),
      })
      .then(() => {
        setStarting(false);
        setRemote('');
        setDestination('');
        setDestinationEdited(false);
        setAnnouncement(
          `Clone started into ${rootPath}/${trimmedDestination} on ${serverDescriptor(connection)}. It keeps running on the server when this view closes.`,
        );
        refreshOperations();
      })
      .catch((e: unknown) => {
        setStarting(false);
        const parsed = parseIpcError(e);
        // Field-associated rejections land on their control; everything
        // else renders once at the form level.
        if (parsed.code === 'clone_remote_invalid') {
          setRemoteError(parsed.summary);
        } else if (
          parsed.code === 'clone_destination_invalid' ||
          parsed.code === 'clone_destination_exists' ||
          parsed.code === 'clone_destination_reserved' ||
          parsed.code === 'clone_destination_shadowed'
        ) {
          setDestinationError(parsed.summary);
        } else {
          setFormError(parsed);
        }
      });
  };

  const runAction = (
    operationId: string,
    action: 'cancel' | 'cleanup' | 'retry',
    announce: string,
  ): void => {
    if (actionPending !== null) return;
    setActionPending(operationId);
    const call =
      action === 'cancel'
        ? window.agentico.cancelCloneOperation(operationId)
        : action === 'cleanup'
          ? window.agentico.retryCloneCleanup(operationId)
          : window.agentico.retryCloneOperation(operationId);
    void call
      .then(() => {
        setActionPending(null);
        setAnnouncement(announce);
        refreshOperations();
      })
      .catch((e: unknown) => {
        setActionPending(null);
        setOperationsError(parseIpcError(e));
      });
  };

  const rootSelectId = 'clone-root-select';
  const destinationPreview =
    rootPath !== '' && destination.trim() !== ''
      ? `${rootPath.replace(/\/+$/, '')}/${destination.trim()}`
      : null;

  return (
    <section className="settings-panel__section" aria-label="Clone repositories">
      <h2 className="settings-panel__section-title">Clone a repository</h2>
      <p className="settings-panel__section-desc">
        The connected server clones into one of its workspace roots. Work keeps running on{' '}
        {serverDescriptor(connection)} after this view closes; explicit Cancel stops it.
      </p>

      <div className="settings-panel__clone-form">
        <div className="settings-panel__clone-field">
          <label htmlFor="clone-remote">Repository URL</label>
          <input
            id="clone-remote"
            type="text"
            value={remote}
            onChange={(event) => handleRemoteChange(event.target.value)}
            placeholder="https://github.com/owner/repository.git"
            autoComplete="off"
            spellCheck={false}
            aria-invalid={fieldAriaInvalid(remoteError !== null)}
            aria-describedby={fieldAriaDescribedBy('clone-remote-error', remoteError !== null)}
          />
          <FieldError id="clone-remote-error" message={remoteError} />
        </div>

        <div className="settings-panel__clone-field">
          <label htmlFor={rootSelectId}>Destination root</label>
          {cloneableRoots.length === 0 ? (
            <p className="settings-panel__clone-no-roots" id={rootSelectId}>
              No clone-eligible workspace root on {serverDescriptor(connection)}. Ask the server
              administrator to configure a writable, non-repository folder as a workspace root.
            </p>
          ) : (
            <select
              id={rootSelectId}
              value={rootPath}
              onChange={(event) => setRootPath(event.target.value)}
            >
              {cloneableRoots.map((root) => (
                <option key={root.path} value={root.path}>
                  {root.path}
                </option>
              ))}
            </select>
          )}
        </div>

        <div className="settings-panel__clone-field">
          <label htmlFor="clone-destination">Folder name</label>
          <input
            id="clone-destination"
            type="text"
            value={destination}
            onChange={(event) => {
              setDestinationEdited(true);
              setDestination(event.target.value);
              setDestinationError(null);
            }}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={fieldAriaInvalid(destinationError !== null)}
            aria-describedby={fieldAriaDescribedBy(
              'clone-destination-error',
              destinationError !== null,
            )}
          />
          <FieldError id="clone-destination-error" message={destinationError} />
        </div>

        {destinationPreview !== null ? (
          <p className="settings-panel__clone-preview">
            Will clone into <code>{destinationPreview}</code> on {serverDescriptor(connection)}.
          </p>
        ) : null}

        {formError !== null ? <ErrorSurface error={formError} variant="compact" /> : null}

        <button
          type="button"
          className="setup-wizard__action"
          onClick={handleStart}
          disabled={starting || cloneableRoots.length === 0}
        >
          {starting ? 'Starting…' : 'Clone repository'}
        </button>
      </div>

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
            <li key={operation.id} className="settings-panel__clone-operation">
              <div className="settings-panel__clone-operation-head">
                <b className="settings-panel__clone-operation-destination">
                  {operation.destination}
                </b>
                <span className="settings-panel__clone-operation-path">
                  <code>{operation.destinationPath}</code>
                </span>
                <span
                  className={`settings-panel__clone-operation-state is-${operation.state}`}
                  data-state={operation.state}
                >
                  {STATE_LABELS[operation.state]}
                </span>
              </div>
              {operation.stage !== undefined && ACTIVE_STATES.includes(operation.state) ? (
                <p className="settings-panel__clone-operation-progress" role="status">
                  {operation.progress !== undefined && operation.progress !== ''
                    ? operation.progress
                    : STATE_LABELS[operation.state]}
                </p>
              ) : null}
              {operation.state === 'succeeded' && operation.published !== undefined ? (
                <p className="settings-panel__clone-operation-progress">
                  Published as {operation.published.repoKey}
                  {operation.published.hasHead
                    ? ''
                    : ' — no commits yet; an initial commit is required before feature use'}
                </p>
              ) : null}
              {operation.state === 'cleanup_pending' ? (
                <p className="settings-panel__clone-operation-issue">
                  {operation.cleanupIssue ?? 'Cleanup could not be proved safe yet.'} The staged
                  data is kept untouched on {serverDescriptor(connection)}.
                </p>
              ) : null}
              {operation.error !== undefined && operation.state !== 'cleanup_pending' ? (
                <p className="settings-panel__clone-operation-issue">
                  {operation.error.title}: {operation.error.summary}
                </p>
              ) : null}
              <div className="settings-panel__clone-operation-actions">
                {ACTIVE_STATES.includes(operation.state) ? (
                  <button
                    type="button"
                    className="settings-panel__clone-action"
                    disabled={actionPending !== null || operation.cancelRequested}
                    onClick={() =>
                      runAction(
                        operation.id,
                        'cancel',
                        `Cancellation requested for ${operation.destination}.`,
                      )
                    }
                  >
                    {operation.cancelRequested ? 'Cancelling…' : 'Cancel clone'}
                  </button>
                ) : null}
                {operation.state === 'cleanup_pending' ? (
                  <button
                    type="button"
                    className="settings-panel__clone-action"
                    disabled={actionPending !== null}
                    onClick={() =>
                      runAction(
                        operation.id,
                        'cleanup',
                        `Cleanup rechecked for ${operation.destination}.`,
                      )
                    }
                  >
                    Retry cleanup
                  </button>
                ) : null}
                {RETRYABLE_STATES.includes(operation.state) ? (
                  <button
                    type="button"
                    className="settings-panel__clone-action"
                    disabled={actionPending !== null}
                    onClick={() =>
                      runAction(
                        operation.id,
                        'retry',
                        `Fresh retry started for ${operation.destination}.`,
                      )
                    }
                  >
                    Retry clone
                  </button>
                ) : null}
              </div>
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
