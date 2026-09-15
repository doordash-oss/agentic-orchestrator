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
 * Shared repository-creation surface: the create form used by both entry
 * points — the Settings workspace pane and the creation sheet's picker. It
 * embeds the same destination-root control as the clone form (native
 * chooser on a local server, administrator-configured roots on a remote
 * one), an editable child folder name, the connected-server/destination
 * preview, and the explicit initial-commit consent the server requires
 * before any filesystem mutation.
 */
import { useState } from 'react';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { parseIpcError } from '../wizard/ipcError';
import type {
  CanonicalError,
  ConnectionState,
  CreateRepositoryResult,
  ReadinessSnapshot,
  WorkspaceRootState,
} from '../../../shared/ipc';
import { cloneEligibleRoots, DestinationRootControl, serverDescriptor } from './cloneViews';

export interface CreateRepositoryStartInput {
  rootPath: string;
  destination: string;
  idempotencyKey: string;
  consent: true;
}

export const CREATE_CONSENT_COPY =
  "Create makes one empty initial commit on the main branch using Agentico's identity, with no origin remote and no push.";

/**
 * The create form. `onCreate` performs the server-owned creation and
 * rejects with the canonical error when the server refuses; the form maps
 * canonical rejections onto their controls and clears itself on success.
 * Submission requires the explicit consent acknowledgement; the request
 * itself carries it and the server rejects anything less before any
 * filesystem mutation.
 */
export function CreateRepositoryForm({
  workspaceRoots,
  connection,
  idPrefix,
  onCreate,
  onCreated,
  onReadinessChanged,
  disabled = false,
}: {
  workspaceRoots: readonly WorkspaceRootState[];
  connection: ConnectionState;
  /** Element-id prefix so two mounted forms never collide. */
  idPrefix: string;
  onCreate(input: CreateRepositoryStartInput): Promise<CreateRepositoryResult>;
  onCreated?(result: CreateRepositoryResult, input: CreateRepositoryStartInput): void;
  onReadinessChanged?(snapshot: ReadinessSnapshot): void;
  disabled?: boolean;
}) {
  const cloneableRoots = cloneEligibleRoots(workspaceRoots);
  const [rootPath, setRootPath] = useState('');
  const [destination, setDestination] = useState('');
  const [consent, setConsent] = useState(false);
  const [rootError, setRootError] = useState<string | null>(null);
  const [destinationError, setDestinationError] = useState<string | null>(null);
  const [consentError, setConsentError] = useState<string | null>(null);
  const [formError, setFormError] = useState<CanonicalError | null>(null);
  const [creating, setCreating] = useState(false);

  const handleCreate = (): void => {
    if (creating) return;
    setFormError(null);
    const trimmedDestination = destination.trim();
    let valid = true;
    if (rootPath === '') {
      setRootError('Choose a destination root.');
      valid = false;
    } else {
      setRootError(null);
    }
    if (trimmedDestination === '') {
      setDestinationError('A repository folder name is required.');
      valid = false;
    } else if (/[\\/\s]|^\.|\.$|^-/.test(trimmedDestination)) {
      setDestinationError(
        'The repository must be a single new folder name (no separators, dots at the edges, or leading dash).',
      );
      valid = false;
    } else {
      setDestinationError(null);
    }
    if (!consent) {
      // The checkbox is the acknowledgement gate; nothing is sent without
      // it, and the server would refuse the request anyway.
      setConsentError('Acknowledge the initial empty commit before creating.');
      valid = false;
    } else {
      setConsentError(null);
    }
    if (!valid) return;
    setCreating(true);
    const input: CreateRepositoryStartInput = {
      rootPath,
      destination: trimmedDestination,
      idempotencyKey: crypto.randomUUID(),
      consent: true,
    };
    void onCreate(input)
      .then((result) => {
        setCreating(false);
        setDestination('');
        setConsent(false);
        onCreated?.(result, input);
      })
      .catch((e: unknown) => {
        setCreating(false);
        const parsed = parseIpcError(e);
        // Field-associated rejections land on their control; everything
        // else renders once at the form level.
        if (parsed.code === 'clone_root_ineligible') {
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
      <DestinationRootControl
        idPrefix={`${idPrefix}-root`}
        workspaceRoots={workspaceRoots}
        connection={connection}
        value={rootPath}
        onValueChange={setRootPath}
        onReadinessChanged={onReadinessChanged}
        serverError={rootError}
        disabled={disabled || creating}
      />

      <div className="settings-panel__clone-field">
        <label htmlFor={`${idPrefix}-destination`}>Repository folder name</label>
        <input
          id={`${idPrefix}-destination`}
          type="text"
          value={destination}
          onChange={(event) => {
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

      <p className="settings-panel__clone-preview">{CREATE_CONSENT_COPY}</p>
      <div className="settings-panel__clone-field">
        <label className="settings-panel__clone-consent" htmlFor={`${idPrefix}-consent`}>
          <input
            id={`${idPrefix}-consent`}
            type="checkbox"
            checked={consent}
            onChange={(event) => {
              setConsent(event.target.checked);
              setConsentError(null);
            }}
            disabled={disabled}
            aria-invalid={fieldAriaInvalid(consentError !== null)}
            aria-describedby={fieldAriaDescribedBy(
              `${idPrefix}-consent-error`,
              consentError !== null,
            )}
          />
          <span>Create the repository with its initial empty commit</span>
        </label>
        <FieldError id={`${idPrefix}-consent-error`} message={consentError} />
      </div>

      {destinationPreview !== null ? (
        <p className="settings-panel__clone-preview">
          Will create at <code>{destinationPreview}</code> on {serverDescriptor(connection)}.
        </p>
      ) : null}

      {formError !== null ? <ErrorSurface error={formError} variant="compact" /> : null}

      <button
        type="button"
        className="setup-wizard__action"
        onClick={handleCreate}
        disabled={creating || disabled || !consent || cloneableRoots.length === 0}
      >
        {creating ? 'Creating…' : 'Create repository'}
      </button>
    </div>
  );
}
