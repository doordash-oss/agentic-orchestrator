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
 * Settings ▸ Updates, second card: the connected server's own update state.
 * Same head, grid, verbs, and consent dialog as the app card above it, backed
 * by the server's update endpoints instead of the app updater.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ServerUpdateInstallRequest, ServerUpdateState } from '../../../shared/ipc';
import {
  serverUpdatePolicyLabel,
  serverUpdateStatusLabel,
  serverUpdateSummary,
  serverUpdateTone,
  serverUpdateVerbs,
} from '../../../shared/serverUpdateState';
import { ErrorSurface } from '../components/ErrorSurface';
import { parseIpcError } from '../wizard/ipcError';
import { SettingsConfirmationDialog } from './SettingsConfirmationDialog';

const MANAGED_SUMMARY =
  'This server ships with the desktop app and is replaced when the app updates. Its update state is the app card above.';

export function ServerUpdateCard({
  serverLabel,
  managed = false,
  revision,
  onOpenExternal,
}: {
  serverLabel: string;
  /** App-owned child runtime: versions are shown, update verbs are not. */
  managed?: boolean;
  revision?: string | undefined;
  onOpenExternal(url: string): void;
}) {
  const [state, setState] = useState<ServerUpdateState | null>(null);
  const [loadError, setLoadError] = useState<ReturnType<typeof parseIpcError> | null>(null);
  const [busy, setBusy] = useState<'check' | 'idle' | 'now' | 'cancel' | null>(null);
  const [confirming, setConfirming] = useState(false);
  const installNowTrigger = useRef<HTMLButtonElement | null>(null);

  const refresh = useCallback(() => {
    void window.agentico
      .getServerUpdate()
      .then((next) => {
        setState(next);
        setLoadError(null);
      })
      .catch((e: unknown) => setLoadError(parseIpcError(e)));
  }, []);

  useEffect(() => {
    refresh();
    return window.agentico.onAppEvent((event) => {
      if (
        event.type === 'invalidated' &&
        (event.kind === 'update.updated' || event.kind === 'resync')
      ) {
        refresh();
      }
    });
  }, [refresh]);

  const run = useCallback(
    async (kind: NonNullable<typeof busy>, action: () => Promise<ServerUpdateState>) => {
      try {
        setBusy(kind);
        setState(await action());
        setLoadError(null);
      } catch (e: unknown) {
        setLoadError(parseIpcError(e));
      } finally {
        setBusy(null);
      }
    },
    [],
  );

  const install = useCallback(
    (request: ServerUpdateInstallRequest) =>
      run(request.when, () => window.agentico.installServerUpdate(request)),
    [run],
  );

  const closeConfirm = useCallback(() => {
    setConfirming(false);
    requestAnimationFrame(() => installNowTrigger.current?.focus());
  }, []);

  const verbs = managed ? null : serverUpdateVerbs(state);
  const version = state?.currentVersion === undefined ? '' : ` (v${state.currentVersion})`;

  return (
    <section
      className="settings-panel__section settings-panel__section--updates"
      aria-label="Server updates"
    >
      <div className="settings-panel__section-head">
        <div>
          <h2 className="settings-panel__section-title">
            Server: {serverLabel}
            {version}
          </h2>
          <p className="settings-panel__section-desc">
            {managed ? 'Managed by the app' : serverUpdatePolicyLabel(state)}
            {revision !== undefined && ` · build ${revision.slice(0, 8)}`}
          </p>
        </div>
        <span
          className="settings-panel__status-pill"
          data-tone={managed ? 'neutral' : serverUpdateTone(state)}
        >
          {managed ? 'Bundled' : serverUpdateStatusLabel(state)}
        </span>
      </div>
      <div className="settings-panel__update-grid">
        <span>Current</span>
        <strong>{state?.currentVersion ?? 'Unknown'}</strong>
        <span>Latest</span>
        <strong>{managed ? 'Tracks the app' : (state?.latestVersion ?? 'Unknown')}</strong>
        <span>Install</span>
        <strong>{state?.installation ?? 'unknown'}</strong>
        <span>Signature</span>
        <strong>{state?.signature ?? 'unverified'}</strong>
      </div>
      {loadError !== null ? (
        <ErrorSurface
          error={loadError}
          variant="compact"
          localAction={{ label: 'Retry', onAction: refresh }}
        />
      ) : managed ? (
        <p className="settings-panel__section-desc">{MANAGED_SUMMARY}</p>
      ) : state?.status === 'failed' && state.error !== undefined ? (
        <ErrorSurface
          error={state.error}
          variant="compact"
          localAction={
            busy === 'check'
              ? { label: 'Check now', disabledReason: 'Checking…' }
              : {
                  label: 'Check now',
                  onAction: () => void run('check', () => window.agentico.checkServerUpdate()),
                }
          }
        />
      ) : (
        <p className="settings-panel__section-desc">{serverUpdateSummary(state)}</p>
      )}
      {state?.activeWorkSummary !== undefined && (
        <p className="settings-panel__update-work">{state.activeWorkSummary}</p>
      )}
      <div className="settings-panel__button-row">
        {managed && (
          <button
            type="button"
            className="setup-wizard__action"
            disabled
            title="Bundled servers update with the app"
          >
            Check now
          </button>
        )}
        {verbs?.check && (
          <button
            type="button"
            className="setup-wizard__action"
            onClick={() => void run('check', () => window.agentico.checkServerUpdate())}
            disabled={busy !== null}
          >
            {busy === 'check' ? 'Checking…' : 'Check now'}
          </button>
        )}
        {state?.releaseUrl !== undefined && (
          <button
            type="button"
            className="setup-wizard__action"
            onClick={() => onOpenExternal(state.releaseUrl ?? '')}
          >
            Release notes
          </button>
        )}
        {verbs?.installWhenIdle && (
          <button
            type="button"
            className="setup-wizard__action"
            onClick={() => void install({ when: 'idle' })}
            disabled={busy !== null}
          >
            {busy === 'idle' ? 'Scheduling…' : 'Install when idle'}
          </button>
        )}
        {verbs?.installNow && (
          <button
            type="button"
            ref={installNowTrigger}
            className={
              verbs?.installNowStopsWork
                ? 'settings-panel__root-btn settings-panel__root-btn--danger'
                : 'setup-wizard__action setup-wizard__action--primary'
            }
            onClick={() =>
              verbs?.installNowStopsWork ? setConfirming(true) : void install({ when: 'now' })
            }
            disabled={busy !== null}
          >
            {busy === 'now'
              ? 'Installing…'
              : verbs?.installNowStopsWork
                ? 'Stop work and install now'
                : 'Install now'}
          </button>
        )}
        {verbs?.cancel && (
          <button
            type="button"
            className="setup-wizard__action"
            onClick={() => void run('cancel', () => window.agentico.cancelServerUpdate())}
            disabled={busy !== null}
          >
            {busy === 'cancel' ? 'Cancelling…' : 'Cancel install'}
          </button>
        )}
      </div>

      {confirming && state !== null && !managed && (
        <SettingsConfirmationDialog
          ariaLabel="Install server update confirmation"
          onCancel={closeConfirm}
        >
          <div className="restart-prompt">
            <h2 className="restart-prompt__title">Stop work and install now?</h2>
            <p className="restart-prompt__summary">
              The server sends stop requests before restarting into{' '}
              <code>{state.targetVersion ?? state.latestVersion}</code>. Workflows and chat may be
              interrupted if they do not stop cleanly; repository work blocks the install.
            </p>
            <p className="restart-prompt__summary">
              {state.activeWorkSummary ?? 'Live work is rechecked before anything is stopped.'}
            </p>
            <div className="restart-prompt__actions">
              <button type="button" className="setup-wizard__action" onClick={closeConfirm}>
                Cancel
              </button>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => {
                  setConfirming(false);
                  void install({ when: 'now', stopActiveWork: true });
                }}
              >
                Stop work and install now
              </button>
            </div>
          </div>
        </SettingsConfirmationDialog>
      )}
    </section>
  );
}
