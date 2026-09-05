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
 */
import { useEffect, useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { parseIpcError } from '../wizard/ipcError';
import type { CanonicalError, CloneOperation, ConnectionState } from '../../../shared/ipc';

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

/**
 * The clone form. `onStart` performs the actual start and rejects with the
 * canonical error when the server refuses; the form maps canonical
 * rejections onto their controls and clears itself on acceptance.
 */
export function CloneRepositoryForm({
  cloneableRoots,
  serverLabel,
  idPrefix,
  onStart,
  onStarted,
  disabled = false,
}: {
  cloneableRoots: readonly { path: string }[];
  serverLabel: string;
  /** Element-id prefix so two mounted forms never collide. */
  idPrefix: string;
  onStart(input: CloneStartInput): Promise<void>;
  onStarted?(input: CloneStartInput): void;
  disabled?: boolean;
}) {
  const [remote, setRemote] = useState('');
  const [rootPath, setRootPath] = useState('');
  const [destination, setDestination] = useState('');
  const [destinationEdited, setDestinationEdited] = useState(false);
  const [remoteError, setRemoteError] = useState<string | null>(null);
  const [destinationError, setDestinationError] = useState<string | null>(null);
  const [formError, setFormError] = useState<CanonicalError | null>(null);
  const [starting, setStarting] = useState(false);

  // Keep the root selection valid as readiness changes; the first usable
  // root is the default, in the server's configured order.
  useEffect(() => {
    if (cloneableRoots.length === 0) {
      setRootPath('');
      return;
    }
    if (!cloneableRoots.some((root) => root.path === rootPath)) {
      setRootPath(cloneableRoots[0]?.path ?? '');
    }
  }, [cloneableRoots, rootPath]);

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
      setDestinationError('Choose a destination root.');
      valid = false;
    } else if (trimmedDestination === '') {
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

  const rootSelectId = `${idPrefix}-root-select`;
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

      <div className="settings-panel__clone-field">
        <label htmlFor={rootSelectId}>Destination root</label>
        {cloneableRoots.length === 0 ? (
          <p className="settings-panel__clone-no-roots" id={rootSelectId}>
            No clone-eligible workspace root on {serverLabel}. Ask the server administrator to
            configure a writable, non-repository folder as a workspace root.
          </p>
        ) : (
          <select
            id={rootSelectId}
            value={rootPath}
            disabled={disabled}
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
          Will clone into <code>{destinationPreview}</code> on {serverLabel}.
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
