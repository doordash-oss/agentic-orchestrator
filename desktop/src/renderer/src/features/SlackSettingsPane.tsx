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

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  CanonicalError,
  SlackSettingsDraft,
  SlackSettingsSnapshot,
  SlackValidationResult,
} from '../../../shared/ipc';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { useConnectionState } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';

const SLACK_APP_URL = 'https://api.slack.com/apps?new_app=1';
const TOKEN_ERROR_CODES = new Set([
  'slack_invalid_token',
  'slack_unsupported_token',
  'slack_missing_scopes',
]);

function formatCheckedAt(value: string | null | undefined): string | null {
  if (value === null || value === undefined || value === '') return null;
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString();
}

function statusHeading(snapshot: SlackSettingsSnapshot): string {
  if (!snapshot.supported) return 'Slack';
  if (!snapshot.tokenSet) return 'Not set up';
  if (snapshot.status.state === 'warning') return 'Token saved, but Slack could not be reached';
  if (snapshot.identity === null || snapshot.tokenType === null)
    return 'Slack connection needs attention';
  return `Connected to ${snapshot.identity.teamName} as ${snapshot.identity.displayName} (${snapshot.tokenType})`;
}

function credentialKey(snapshot: SlackSettingsSnapshot): string | null {
  if (!snapshot.supported || !snapshot.tokenSet) return null;
  return `${snapshot.tokenType ?? 'unsupported'}:${snapshot.tokenHint}`;
}

export function SlackSettingsPane() {
  const connection = useConnectionState();
  const serverLabel =
    connection.serverName ??
    (connection.ownership === 'app-owned' ? 'Local runtime' : 'Connected server');
  const request = useRef(0);
  const operationEpoch = useRef(0);
  const checkRevision = useRef(0);
  const checkedCredentialKey = useRef<string | null>(null);
  const tokenInputRef = useRef<HTMLInputElement>(null);
  const [snapshot, setSnapshot] = useState<SlackSettingsSnapshot | null>(null);
  const [loadError, setLoadError] = useState<CanonicalError | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [token, setToken] = useState('');
  const [replacing, setReplacing] = useState(false);
  const [clearToken, setClearToken] = useState(false);
  const [guideOpen, setGuideOpen] = useState(true);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<CanonicalError | null>(null);
  const [checking, setChecking] = useState(false);
  const [checkResult, setCheckResult] = useState<SlackValidationResult | null>(null);
  const [checkError, setCheckError] = useState<CanonicalError | null>(null);
  const [copyNotice, setCopyNotice] = useState<string | null>(null);

  const baselineEnabled = snapshot?.supported === true ? snapshot.enabled : false;
  const tokenSet = snapshot?.supported === true && snapshot.tokenSet && !clearToken;
  const dirty = enabled !== baselineEnabled || token !== '' || clearToken;

  const clearCheckFeedback = useCallback(() => {
    checkRevision.current += 1;
    checkedCredentialKey.current = null;
    setChecking(false);
    setCheckResult(null);
    setCheckError(null);
  }, []);

  const installSnapshot = useCallback(
    (next: SlackSettingsSnapshot, preserveSameCredentialFeedback = false) => {
      const preservesCheckFeedback =
        preserveSameCredentialFeedback &&
        checkedCredentialKey.current !== null &&
        checkedCredentialKey.current === credentialKey(next);
      if (!preservesCheckFeedback) clearCheckFeedback();
      setSnapshot(next);
      setEnabled(next.supported ? next.enabled : false);
      setToken('');
      setReplacing(false);
      setClearToken(false);
      setGuideOpen(next.supported ? !next.tokenSet : false);
      setSaveError(null);
      setLoadError(null);
    },
    [clearCheckFeedback],
  );

  const reload = useCallback(
    (expectedCheckRevision?: number, preserveSameCredentialFeedback = false) => {
      const current = ++request.current;
      void window.agentico
        .getSlackSettings()
        .then((next) => {
          if (
            current === request.current &&
            (expectedCheckRevision === undefined || expectedCheckRevision === checkRevision.current)
          ) {
            installSnapshot(next, preserveSameCredentialFeedback);
          }
        })
        .catch((error: unknown) => {
          if (
            current === request.current &&
            (expectedCheckRevision === undefined || expectedCheckRevision === checkRevision.current)
          ) {
            setLoadError(parseIpcError(error));
          }
        });
    },
    [installSnapshot],
  );

  useEffect(() => {
    request.current += 1;
    operationEpoch.current += 1;
    setSnapshot(null);
    setLoadError(null);
    setToken('');
    setReplacing(false);
    setClearToken(false);
    setSaving(false);
    setSaveError(null);
    clearCheckFeedback();
    setSaved(false);
    if (connection.status === 'ready') reload();
  }, [clearCheckFeedback, connection.serverKey, connection.status, reload]);

  useEffect(() => {
    return window.agentico.onAppEvent((event) => {
      if (
        !dirty &&
        event.type === 'invalidated' &&
        (event.kind === 'resync' || event.kind.startsWith('config'))
      ) {
        reload(undefined, true);
      }
    });
  }, [dirty, reload]);

  const tokenFieldError =
    saveError !== null && TOKEN_ERROR_CODES.has(saveError.code) ? saveError.summary : null;
  const saveBarError = tokenFieldError === null ? saveError : null;
  const canCheck = token !== '' || tokenSet;

  const updateToken = (value: string) => {
    clearCheckFeedback();
    setToken(value);
    setSaved(false);
    setSaveError(null);
    if (snapshot?.supported === true && !snapshot.tokenSet) setEnabled(value !== '');
  };

  useEffect(() => {
    if (tokenFieldError !== null) tokenInputRef.current?.focus();
  }, [tokenFieldError]);

  const draft = useMemo<SlackSettingsDraft>(
    () => ({
      enabled,
      ...(token === '' ? {} : { token }),
      ...(clearToken ? { clearToken: true } : {}),
    }),
    [clearToken, enabled, token],
  );

  const save = () => {
    const epoch = operationEpoch.current;
    setSaving(true);
    setSaved(false);
    setSaveError(null);
    void window.agentico
      .updateSlackSettings(draft)
      .then((next) => {
        if (epoch !== operationEpoch.current) return;
        installSnapshot(next);
        setSaved(true);
      })
      .catch((error: unknown) => {
        if (epoch === operationEpoch.current) setSaveError(parseIpcError(error));
      })
      .finally(() => {
        if (epoch === operationEpoch.current) setSaving(false);
      });
  };

  const check = () => {
    const epoch = operationEpoch.current;
    const revision = checkRevision.current;
    const checksStoredToken = token === '';
    checkedCredentialKey.current =
      checksStoredToken && snapshot !== null ? credentialKey(snapshot) : null;
    setChecking(true);
    setCheckResult(null);
    setCheckError(null);
    void window.agentico
      .validateSlackSettings(checksStoredToken ? {} : { token })
      .then((result) => {
        if (epoch !== operationEpoch.current || revision !== checkRevision.current) return;
        setCheckResult(result);
        if (checksStoredToken) reload(revision, true);
      })
      .catch((error: unknown) => {
        if (epoch === operationEpoch.current && revision === checkRevision.current) {
          setCheckError(parseIpcError(error));
        }
      })
      .finally(() => {
        if (epoch === operationEpoch.current && revision === checkRevision.current) {
          setChecking(false);
        }
      });
  };

  if (loadError !== null) {
    return <ErrorSurface error={loadError} variant="compact" />;
  }
  if (snapshot === null) {
    return <p role="status">Loading Slack settings...</p>;
  }
  if (!snapshot.supported) {
    return (
      <section className="settings-panel__section" aria-label="Slack">
        <h2 className="settings-panel__section-title">Slack</h2>
        <p className="settings-panel__section-desc">
          The connected server does not support Slack and needs an update.
        </p>
      </section>
    );
  }

  return (
    <section className="settings-panel__section slack-settings" aria-label="Slack">
      <div className="settings-panel__section-head">
        <div>
          <h2 className="settings-panel__section-title">Slack</h2>
          <p className="settings-panel__section-desc">{serverLabel}</p>
        </div>
        {snapshot.tokenSet && !snapshot.enabled ? (
          <span className="settings-panel__status-pill" data-tone="neutral">
            Disabled
          </span>
        ) : null}
      </div>
      <div className="slack-settings__status">
        <strong>{statusHeading(snapshot)}</strong>
        {snapshot.status.lastCheckedAt ? (
          <span>Last checked {formatCheckedAt(snapshot.status.lastCheckedAt)}</span>
        ) : null}
      </div>
      {snapshot.status.lastError ? (
        <ErrorSurface error={snapshot.status.lastError} variant="compact" />
      ) : null}

      <div className="slack-settings__guide">
        <button
          type="button"
          className="slack-settings__guide-toggle"
          aria-expanded={guideOpen}
          onClick={() => setGuideOpen((open) => !open)}
        >
          Set up the Slack app
        </button>
        {guideOpen ? (
          <ol>
            <li>
              <strong>Create a Slack app from the manifest</strong>
              <div className="settings-panel__button-row">
                <button
                  type="button"
                  className="setup-wizard__action"
                  onClick={() => {
                    void window.agentico
                      .writeClipboardText(snapshot.manifest)
                      .then(() => setCopyNotice('Manifest copied.'))
                      .catch(() => setCopyNotice('Could not copy the manifest.'));
                  }}
                >
                  Copy manifest
                </button>
                <button
                  type="button"
                  className="setup-wizard__action"
                  onClick={() => void window.agentico.openExternal({ url: SLACK_APP_URL })}
                >
                  Create a Slack app
                </button>
              </div>
            </li>
            <li>
              <strong>Install the app to the workspace</strong>
              <span>Copy the bot or user OAuth token from the app install page.</span>
            </li>
            <li>
              <strong>Paste the token below and save</strong>
              <span>Agentico validates the required scopes before connecting.</span>
            </li>
          </ol>
        ) : null}
        {copyNotice ? (
          <p className="settings-panel__copy-notice" role="status">
            {copyNotice}
          </p>
        ) : null}
      </div>

      <div className="slack-settings__fields">
        {snapshot.tokenSet && !replacing && !clearToken ? (
          <div className="slack-settings__token-display">
            <span>Slack token</span>
            <code>{`••••••••${snapshot.tokenHint}`}</code>
            <div className="settings-panel__button-row">
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => setReplacing(true)}
              >
                Replace
              </button>
              <button
                type="button"
                className="settings-panel__root-btn settings-panel__root-btn--danger"
                onClick={() => {
                  clearCheckFeedback();
                  setClearToken(true);
                  setEnabled(false);
                  setSaved(false);
                }}
              >
                Clear
              </button>
            </div>
          </div>
        ) : clearToken ? (
          <div className="slack-settings__remove-note" role="status">
            <span>The saved token will be removed when you save.</span>
            <button
              type="button"
              className="setup-wizard__action"
              onClick={() => {
                clearCheckFeedback();
                setClearToken(false);
                setEnabled(snapshot.enabled);
              }}
            >
              Undo
            </button>
          </div>
        ) : (
          <div className="slack-settings__token-field">
            <label htmlFor="slack-token">Slack token</label>
            <input
              ref={tokenInputRef}
              id="slack-token"
              type="password"
              value={token}
              autoComplete="off"
              aria-invalid={fieldAriaInvalid(tokenFieldError !== null)}
              aria-describedby={fieldAriaDescribedBy('slack-token-error', tokenFieldError !== null)}
              onChange={(event) => updateToken(event.currentTarget.value)}
            />
            {tokenFieldError ? (
              <FieldError id="slack-token-error" message={tokenFieldError} />
            ) : null}
            {snapshot.tokenSet && replacing ? (
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => {
                  clearCheckFeedback();
                  setReplacing(false);
                  setToken('');
                }}
              >
                Cancel replacement
              </button>
            ) : null}
          </div>
        )}

        <label className="settings-panel__toggle">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => {
              setEnabled(event.currentTarget.checked);
              setSaved(false);
            }}
          />
          <span>
            <strong>Enabled</strong>
            <span>Allow this server to use the saved Slack connection.</span>
          </span>
        </label>
      </div>

      <div className="slack-settings__check">
        <button
          type="button"
          className="setup-wizard__action"
          disabled={!canCheck || checking}
          onClick={check}
        >
          {checking ? 'Checking...' : 'Check connection'}
        </button>
        {checkResult ? (
          <p role="status">
            Connected to {checkResult.identity.teamName} as {checkResult.identity.displayName} (
            {checkResult.tokenType}); {checkResult.grantedScopes.length} scopes granted.
          </p>
        ) : null}
        {checkError ? <ErrorSurface error={checkError} variant="compact" /> : null}
      </div>

      <footer className="config-editor__footer">
        {saveBarError ? (
          <ErrorSurface error={saveBarError} variant="compact" />
        ) : (
          <span className="config-editor__status" role="status">
            {saving
              ? 'Saving...'
              : dirty
                ? 'Unsaved changes'
                : saved
                  ? 'Saved.'
                  : 'Changes apply to this server.'}
          </span>
        )}
        <div className="config-editor__actions">
          <button
            type="button"
            className="config-editor__btn"
            disabled={!dirty || saving}
            onClick={() => installSnapshot(snapshot)}
          >
            Reset
          </button>
          <button
            type="button"
            className="config-editor__btn config-editor__btn--primary"
            disabled={!dirty || saving || (!snapshot.tokenSet && token === '')}
            onClick={save}
          >
            Save changes
          </button>
        </div>
      </footer>
    </section>
  );
}
