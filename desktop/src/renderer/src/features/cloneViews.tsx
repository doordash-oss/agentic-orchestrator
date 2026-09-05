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
 * Shared clone surface: the repository-clone form and the operation-state
 * view used by both entry points — the Settings workspace pane and the
 * creation sheet's picker. Both use the same URL/destination validation,
 * editable folder suggestion, ordered configured roots, first usable root
 * selection, server/destination preview, canonical error mapping, and
 * operation-state presentation; only their surrounding chrome differs.
 * The destination-root control (shared with repository creation) also
 * offers the native folder chooser on a local server: choosing or creating
 * a folder persists it as a workspace root before any preparation starts.
 */
import { useEffect, useRef, useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { parseIpcError } from '../wizard/ipcError';
import type {
  CanonicalError,
  CloneOperation,
  ConnectionState,
  ReadinessSnapshot,
  WorkspaceRootState,
} from '../../../shared/ipc';

export const STATE_LABELS: Record<CloneOperation['state'], string> = {
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

export const ACTIVE_STATES: CloneOperation['state'][] = [
  'accepted',
  'running',
  'finalizing',
  'cancelling',
];

export const RETRYABLE_STATES: CloneOperation['state'][] = ['failed', 'cancelled', 'interrupted'];

/** Strips a trailing ".git" and trailing slashes, mirroring the server's suggestion rule. */
export function suggestDestination(remote: string): string {
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

export function serverDescriptor(connection: ConnectionState): string {
  if (connection.status === 'ready') {
    return connection.kind === 'remote' ? 'the remote server' : 'this computer';
  }
  return 'the connected server';
}

export interface CloneStartInput {
  remoteUrl: string;
  rootPath: string;
  destination: string;
  idempotencyKey: string;
}

/** The roots usable as a clone/create destination, in the server's configured order. */
export function cloneEligibleRoots(
  workspaceRoots: readonly WorkspaceRootState[],
): WorkspaceRootState[] {
  return workspaceRoots.filter((root) => root.valid && root.cloneEligible);
}

/**
 * The shared destination-root control: the ordered configured roots with
 * the first usable root as the default, plus the native folder chooser on
 * a local server. Choosing (or creating) a folder through the chooser
 * persists it as a workspace root through the runtime-config mutation and
 * refreshes the authoritative root list before anything is selected: root
 * persistence completes before preparation can start, a failed save
 * changes nothing and retains the form, and cancelling the chooser keeps
 * the current root, every form input, and the feature draft untouched.
 * The whole chooser/config/refresh sequence is bound to the initiating
 * server: a switch or disconnect mid-sequence discards the stale result
 * instead of selecting a root or starting preparation on the new server.
 */
export function DestinationRootControl({
  idPrefix,
  workspaceRoots,
  connection,
  value,
  onValueChange,
  onReadinessChanged,
  serverError = null,
  disabled = false,
}: {
  /** Element-id prefix so two mounted forms never collide. */
  idPrefix: string;
  workspaceRoots: readonly WorkspaceRootState[];
  connection: ConnectionState;
  value: string;
  onValueChange(path: string): void;
  /** Notifies the owner of the authoritative root list after an addition. */
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
  /** A submit-time rejection associated with the root control. */
  serverError?: string | null;
  disabled?: boolean;
}) {
  const cloneable = cloneEligibleRoots(workspaceRoots);
  const localServer = connection.status === 'ready' && connection.kind === 'local';
  const [adding, setAdding] = useState(false);
  const [chooserError, setChooserError] = useState<string | null>(null);
  // The live connection, read by in-flight sequences so a stale result
  // never lands on a different server's UI.
  const connectionRef = useRef(connection);
  useEffect(() => {
    connectionRef.current = connection;
  }, [connection]);
  const attemptRef = useRef(0);

  // Keep the root selection valid as readiness changes; the first usable
  // root is the default, in the server's configured order. An explicit
  // selection of a still-usable root is never clobbered by a refresh.
  useEffect(() => {
    if (cloneable.length === 0) {
      if (value !== '') onValueChange('');
      return;
    }
    if (!cloneable.some((root) => root.path === value)) {
      onValueChange(cloneable[0]?.path ?? '');
    }
  }, [cloneable, value, onValueChange]);

  const handleChooseFolder = (): void => {
    if (adding || disabled) return;
    const attempt = ++attemptRef.current;
    const started = connectionRef.current;
    const startServerKey = started.status === 'ready' ? (started.serverKey ?? null) : null;
    setAdding(true);
    setChooserError(null);
    void (async () => {
      let pickedPath: string | null = null;
      try {
        const picked = await window.agentico.pickWorkspaceDirectory();
        if (attempt !== attemptRef.current) return;
        // Cancelling the chooser preserves the current root, every form
        // input, and the feature draft; no preparation starts.
        if (picked.path === null) return;
        pickedPath = picked.path;
        // The main process fences this read-modify-write by server
        // identity and refuses it on a remote connection; the returned
        // snapshot is the authoritative post-save state.
        const fresh = await window.agentico.addWorkspaceRoot(pickedPath);
        if (attempt !== attemptRef.current) return;
        // The sequence must still belong to the initiating server: a
        // switch or disconnect discards the stale result entirely.
        const now = connectionRef.current;
        if (now.status !== 'ready' || startServerKey === null || now.serverKey !== startServerKey) {
          return;
        }
        onReadinessChanged?.(fresh);
        // Select the authoritative root entry the server reported for the
        // chosen folder — never the raw picked path.
        const entry = fresh.workspaceRoots.find((root) => root.path === pickedPath);
        if (entry === undefined) {
          setChooserError('The server did not report the chosen folder as a workspace root.');
          return;
        }
        if (!entry.valid || !entry.cloneEligible) {
          setChooserError(
            entry.cloneIssue?.summary ??
              entry.issue?.summary ??
              'The chosen folder cannot be used as a destination root.',
          );
          return;
        }
        onValueChange(entry.path);
      } catch (e: unknown) {
        if (attempt !== attemptRef.current) return;
        const parsed = parseIpcError(e);
        // A switch mid-sequence is silent: the stale attempt is simply
        // dropped, and the new server's UI is untouched.
        if (parsed.code === 'E_SERVER_SWITCHED' || parsed.code === 'E_REQUIRES_LOCAL_SERVER') {
          return;
        }
        // A failed save changed nothing: the active configuration is
        // intact, the new root was never authorized, and the form stays
        // exactly as it was for a retry.
        setChooserError(parsed.summary);
      } finally {
        if (attempt === attemptRef.current) setAdding(false);
      }
    })();
  };

  const selectId = `${idPrefix}-root-select`;
  const error = serverError ?? chooserError;
  const describedBy = `${idPrefix}-root-error`;

  return (
    <div className="settings-panel__clone-field">
      <label htmlFor={selectId}>Destination root</label>
      <div className="settings-panel__clone-root-row">
        {cloneable.length === 0 ? (
          <p className="settings-panel__clone-no-roots" id={selectId}>
            {localServer
              ? 'No clone-eligible workspace root yet. Choose a folder to add one.'
              : `No clone-eligible workspace root on ${serverDescriptor(connection)}. Ask the server administrator to configure a writable, non-repository folder as a workspace root.`}
          </p>
        ) : (
          <select
            id={selectId}
            value={value}
            disabled={disabled}
            onChange={(event) => onValueChange(event.target.value)}
            aria-invalid={fieldAriaInvalid(error !== null)}
            aria-describedby={fieldAriaDescribedBy(describedBy, error !== null)}
          >
            {cloneable.map((root) => (
              <option key={root.path} value={root.path}>
                {root.path}
              </option>
            ))}
          </select>
        )}
        {localServer ? (
          <button
            type="button"
            className="settings-panel__clone-choose"
            onClick={handleChooseFolder}
            disabled={disabled || adding}
          >
            {adding ? 'Adding…' : 'Choose folder…'}
          </button>
        ) : null}
      </div>
      <FieldError id={describedBy} message={error} />
    </div>
  );
}

/**
 * The clone form. `onStart` performs the actual start and rejects with the
 * canonical error when the server refuses; the form maps canonical
 * rejections onto their controls and clears itself on acceptance.
 */
export function CloneRepositoryForm({
  workspaceRoots,
  connection,
  idPrefix,
  onStart,
  onStarted,
  onReadinessChanged,
  disabled = false,
}: {
  workspaceRoots: readonly WorkspaceRootState[];
  connection: ConnectionState;
  /** Element-id prefix so two mounted forms never collide. */
  idPrefix: string;
  onStart(input: CloneStartInput): Promise<void>;
  onStarted?(input: CloneStartInput): void;
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
  disabled?: boolean;
}) {
  const cloneableRoots = cloneEligibleRoots(workspaceRoots);
  const [remote, setRemote] = useState('');
  const [rootPath, setRootPath] = useState('');
  const [destination, setDestination] = useState('');
  const [destinationEdited, setDestinationEdited] = useState(false);
  const [remoteError, setRemoteError] = useState<string | null>(null);
  const [rootError, setRootError] = useState<string | null>(null);
  const [destinationError, setDestinationError] = useState<string | null>(null);
  const [formError, setFormError] = useState<CanonicalError | null>(null);
  const [starting, setStarting] = useState(false);

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
      setRootError('Choose a destination root.');
      valid = false;
    } else {
      setRootError(null);
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
      setDestinationError(null);
    }
    if (!valid) return;
    setStarting(true);
    const input: CloneStartInput = {
      remoteUrl: trimmedRemote,
      rootPath,
      destination: trimmedDestination,
      idempotencyKey: crypto.randomUUID(),
    };
    void onStart(input)
      .then(() => {
        setStarting(false);
        setRemote('');
        setDestination('');
        setDestinationEdited(false);
        onStarted?.(input);
      })
      .catch((e: unknown) => {
        setStarting(false);
        const parsed = parseIpcError(e);
        // Field-associated rejections land on their control; everything
        // else renders once at the form level.
        if (parsed.code === 'clone_remote_invalid') {
          setRemoteError(parsed.summary);
        } else if (parsed.code === 'clone_root_ineligible') {
          setRootError(parsed.summary);
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

  const destinationPreview =
    rootPath !== '' && destination.trim() !== ''
      ? `${rootPath.replace(/\/+$/, '')}/${destination.trim()}`
      : null;

  return (
    <div className="settings-panel__clone-form">
      <div className="settings-panel__clone-field">
        <label htmlFor={`${idPrefix}-remote`}>Repository URL</label>
        <input
          id={`${idPrefix}-remote`}
          type="text"
          value={remote}
          onChange={(event) => handleRemoteChange(event.target.value)}
          placeholder="https://github.com/owner/repository.git"
          autoComplete="off"
          spellCheck={false}
          disabled={disabled}
          aria-invalid={fieldAriaInvalid(remoteError !== null)}
          aria-describedby={fieldAriaDescribedBy(`${idPrefix}-remote-error`, remoteError !== null)}
        />
        <FieldError id={`${idPrefix}-remote-error`} message={remoteError} />
      </div>

      <DestinationRootControl
        idPrefix={`${idPrefix}-root`}
        workspaceRoots={workspaceRoots}
        connection={connection}
        value={rootPath}
        onValueChange={setRootPath}
        onReadinessChanged={onReadinessChanged}
        serverError={rootError}
        disabled={disabled || starting}
      />

      <div className="settings-panel__clone-field">
        <label htmlFor={`${idPrefix}-destination`}>Folder name</label>
        <input
          id={`${idPrefix}-destination`}
          type="text"
          value={destination}
          onChange={(event) => {
            setDestinationEdited(true);
            setDestination(event.target.value);
            setDestinationError(null);
          }}
          autoComplete="off"
          spellCheck={false}
          disabled={disabled}
          aria-invalid={fieldAriaInvalid(destinationError !== null)}
          aria-describedby={fieldAriaDescribedBy(
            `${idPrefix}-destination-error`,
            destinationError !== null,
          )}
        />
        <FieldError id={`${idPrefix}-destination-error`} message={destinationError} />
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
        disabled={starting || disabled || cloneableRoots.length === 0}
      >
        {starting ? 'Starting…' : 'Clone repository'}
      </button>
    </div>
  );
}

/**
 * One operation's state presentation: destination, state, progress, the
 * published key, failure/cleanup issues, and the live recovery actions.
 */
export function CloneOperationView({
  operation,
  serverLabel,
  actionPending,
  onAction,
}: {
  operation: CloneOperation;
  serverLabel: string;
  actionPending: string | null;
  onAction(action: 'cancel' | 'cleanup' | 'retry', operation: CloneOperation): void;
}) {
  return (
    <div className="settings-panel__clone-operation">
      <div className="settings-panel__clone-operation-head">
        <b className="settings-panel__clone-operation-destination">{operation.destination}</b>
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
          {operation.cleanupIssue ?? 'Cleanup could not be proved safe yet.'} The staged data is
          kept untouched on {serverLabel}.
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
            onClick={() => onAction('cancel', operation)}
          >
            {operation.cancelRequested ? 'Cancelling…' : 'Cancel clone'}
          </button>
        ) : null}
        {operation.state === 'cleanup_pending' ? (
          <button
            type="button"
            className="settings-panel__clone-action"
            disabled={actionPending !== null}
            onClick={() => onAction('cleanup', operation)}
          >
            Retry cleanup
          </button>
        ) : null}
        {RETRYABLE_STATES.includes(operation.state) ? (
          <button
            type="button"
            className="settings-panel__clone-action"
            disabled={actionPending !== null}
            onClick={() => onAction('retry', operation)}
          >
            Retry clone
          </button>
        ) : null}
      </div>
    </div>
  );
}
