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
  SlackRecipient,
  SlackSettingsDraft,
  SlackSettingsSnapshot,
  SlackTestMessageResult,
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

type RecipientRowStatus = 'idle' | 'resolving' | 'resolved' | 'error';

interface RecipientRow {
  key: number;
  input: string;
  resolved: SlackRecipient | null;
  status: RecipientRowStatus;
  error: CanonicalError | null;
  message: string | null;
}

function rowsFromRecipients(recipients: readonly SlackRecipient[], nextKey: () => number) {
  return recipients.map((recipient): RecipientRow => ({
    key: nextKey(),
    input: recipient.typedText,
    resolved: recipient,
    status: 'resolved',
    error: null,
    message: null,
  }));
}

function recipientListsEqual(
  left: readonly SlackRecipient[],
  right: readonly SlackRecipient[],
): boolean {
  return (
    left.length === right.length &&
    left.every((recipient, index) => {
      const other = right[index];
      return (
        other !== undefined &&
        recipient.typedText === other.typedText &&
        recipient.kind === other.kind &&
        recipient.id === other.id &&
        recipient.displayName === other.displayName
      );
    })
  );
}

function recipientErrorMessage(row: RecipientRow): string | null {
  if (row.message !== null) return row.message;
  if (row.error === null) return null;
  const hint = row.error.remediation?.hint;
  return hint === undefined ? row.error.summary : `${row.error.summary} ${hint}`;
}

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
  const nextRecipientKey = useRef(0);
  const recipientRevisions = useRef(new Map<number, number>());
  const recipientInputs = useRef(new Map<number, HTMLInputElement>());
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
  const [recipientRows, setRecipientRows] = useState<RecipientRow[]>([]);
  const [focusRecipientKey, setFocusRecipientKey] = useState<number | null>(null);
  const [sendingTest, setSendingTest] = useState(false);
  const [testResult, setTestResult] = useState<SlackTestMessageResult | null>(null);
  const [testError, setTestError] = useState<CanonicalError | null>(null);

  const baselineEnabled = snapshot?.supported === true ? snapshot.enabled : false;
  const baselineRecipients =
    snapshot?.supported === true ? snapshot.defaultRecipients : ([] as SlackRecipient[]);
  const tokenSet = snapshot?.supported === true && snapshot.tokenSet && !clearToken;
  const resolvedRecipients = recipientRows.flatMap((row) =>
    row.input.trim() !== '' && row.resolved !== null ? [row.resolved] : [],
  );
  const hasInvalidRecipients = recipientRows.some(
    (row) => row.input.trim() !== '' && row.resolved === null,
  );
  const recipientsDirty = !recipientListsEqual(resolvedRecipients, baselineRecipients);
  const dirty = enabled !== baselineEnabled || token !== '' || clearToken || recipientsDirty;

  const clearTestFeedback = useCallback(() => {
    setSendingTest(false);
    setTestResult(null);
    setTestError(null);
  }, []);

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
      recipientRevisions.current.clear();
      setRecipientRows(
        next.supported
          ? rowsFromRecipients(next.defaultRecipients, () => ++nextRecipientKey.current)
          : [],
      );
      setFocusRecipientKey(null);
      clearTestFeedback();
      setSaveError(null);
      setLoadError(null);
    },
    [clearCheckFeedback, clearTestFeedback],
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
    setRecipientRows([]);
    recipientRevisions.current.clear();
    clearTestFeedback();
    clearCheckFeedback();
    setSaved(false);
    if (connection.status === 'ready') reload();
  }, [clearCheckFeedback, clearTestFeedback, connection.serverKey, connection.status, reload]);

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
    clearTestFeedback();
    setToken(value);
    setSaved(false);
    setSaveError(null);
    if (snapshot?.supported === true && !snapshot.tokenSet) setEnabled(value !== '');
  };

  useEffect(() => {
    if (tokenFieldError !== null) tokenInputRef.current?.focus();
  }, [tokenFieldError]);

  useEffect(() => {
    if (focusRecipientKey === null) return;
    const input = recipientInputs.current.get(focusRecipientKey);
    if (input !== undefined && !input.disabled) input.focus();
    setFocusRecipientKey(null);
  }, [focusRecipientKey, recipientRows]);

  const draft = useMemo<SlackSettingsDraft>(
    () => ({
      enabled,
      ...(token === '' ? {} : { token }),
      ...(clearToken ? { clearToken: true } : {}),
      ...(recipientsDirty ? { defaultRecipients: resolvedRecipients } : {}),
    }),
    [clearToken, enabled, recipientsDirty, resolvedRecipients, token],
  );

  const updateRecipientInput = (key: number, value: string) => {
    recipientRevisions.current.set(key, (recipientRevisions.current.get(key) ?? 0) + 1);
    clearTestFeedback();
    setSaved(false);
    setSaveError(null);
    setRecipientRows((rows) =>
      rows.map((row) =>
        row.key === key
          ? {
              ...row,
              input: value,
              resolved: null,
              status: 'idle',
              error: null,
              message: null,
            }
          : row,
      ),
    );
  };

  const resolveRecipient = (key: number) => {
    const row = recipientRows.find((candidate) => candidate.key === key);
    const input = row?.input.trim() ?? '';
    if (
      row === undefined ||
      input === '' ||
      row.status === 'resolving' ||
      (row.resolved !== null && row.resolved.typedText === input) ||
      (!tokenSet && token === '')
    ) {
      return;
    }
    const epoch = operationEpoch.current;
    const revision = (recipientRevisions.current.get(key) ?? 0) + 1;
    recipientRevisions.current.set(key, revision);
    setRecipientRows((rows) =>
      rows.map((candidate) =>
        candidate.key === key
          ? {
              ...candidate,
              input,
              resolved: null,
              status: 'resolving',
              error: null,
              message: null,
            }
          : candidate,
      ),
    );
    void window.agentico
      .resolveSlackRecipient({ input, ...(token === '' ? {} : { token }) })
      .then((recipient) => {
        if (epoch !== operationEpoch.current || recipientRevisions.current.get(key) !== revision) {
          return;
        }
        setRecipientRows((rows) => {
          const duplicate = rows.some(
            (candidate) =>
              candidate.key !== key &&
              candidate.resolved?.kind === recipient.kind &&
              candidate.resolved.id === recipient.id,
          );
          return rows.map((candidate) =>
            candidate.key === key
              ? {
                  ...candidate,
                  resolved: duplicate ? null : recipient,
                  status: duplicate ? 'error' : 'resolved',
                  error: null,
                  message: duplicate ? 'Already in the list' : null,
                }
              : candidate,
          );
        });
      })
      .catch((error: unknown) => {
        if (epoch !== operationEpoch.current || recipientRevisions.current.get(key) !== revision) {
          return;
        }
        setRecipientRows((rows) =>
          rows.map((candidate) =>
            candidate.key === key
              ? {
                  ...candidate,
                  resolved: null,
                  status: 'error',
                  error: parseIpcError(error),
                  message: null,
                }
              : candidate,
          ),
        );
      });
  };

  const addRecipient = () => {
    const key = ++nextRecipientKey.current;
    recipientRevisions.current.set(key, 0);
    setRecipientRows((rows) => [
      ...rows,
      { key, input: '', resolved: null, status: 'idle', error: null, message: null },
    ]);
    setFocusRecipientKey(key);
  };

  const removeRecipient = (key: number) => {
    recipientRevisions.current.delete(key);
    clearTestFeedback();
    setSaved(false);
    setSaveError(null);
    setRecipientRows((rows) => rows.filter((row) => row.key !== key));
  };

  const save = () => {
    const epoch = operationEpoch.current;
    setSaving(true);
    setSaved(false);
    setSaveError(null);
    clearTestFeedback();
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
        if (checksStoredToken && !dirty) reload(revision, true);
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

  const sendTestMessage = () => {
    const epoch = operationEpoch.current;
    setSendingTest(true);
    setTestResult(null);
    setTestError(null);
    void window.agentico
      .sendSlackTestMessage({ recipients: resolvedRecipients })
      .then((result) => {
        if (epoch === operationEpoch.current) setTestResult(result);
      })
      .catch((error: unknown) => {
        if (epoch === operationEpoch.current) setTestError(parseIpcError(error));
      })
      .finally(() => {
        if (epoch === operationEpoch.current) setSendingTest(false);
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

  const canResolveRecipients = token !== '' || tokenSet;
  const testDisabledHint =
    !snapshot.tokenSet || clearToken
      ? clearToken
        ? 'Save or undo the pending token removal before sending.'
        : 'Save a Slack token before sending a test message.'
      : token !== ''
        ? 'Save or discard the replacement token before sending.'
        : resolvedRecipients.length === 0
          ? 'Add and resolve at least one recipient before sending.'
          : hasInvalidRecipients
            ? 'Resolve or remove every recipient before sending.'
            : null;
  const canSendTest = testDisabledHint === null && !sendingTest;

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

        <section className="slack-settings__recipients" aria-labelledby="slack-recipients-title">
          <div className="slack-settings__subsection-head">
            <div>
              <h3 id="slack-recipients-title">Notify by default</h3>
              <p>
                These people and channels receive every feature&apos;s updates from this server.
              </p>
            </div>
            <button type="button" className="setup-wizard__action" onClick={addRecipient}>
              Add recipient
            </button>
          </div>
          <div className="slack-settings__recipient-list">
            {recipientRows.map((row, index) => {
              const rowNumber = index + 1;
              const inputId = `slack-recipient-${row.key}`;
              const errorId = `${inputId}-error`;
              const errorMessage = recipientErrorMessage(row);
              const isOwner =
                row.resolved?.kind === 'user' &&
                snapshot.tokenType === 'user' &&
                snapshot.identity?.userId === row.resolved.id;
              return (
                <div className="slack-settings__recipient-row" key={row.key}>
                  <div className="slack-settings__recipient-field">
                    <label className="sr-only" htmlFor={inputId}>
                      Recipient {rowNumber}
                    </label>
                    <input
                      ref={(element) => {
                        if (element === null) recipientInputs.current.delete(row.key);
                        else recipientInputs.current.set(row.key, element);
                      }}
                      id={inputId}
                      value={row.input}
                      placeholder="Email, @handle, #channel, or Slack ID"
                      disabled={!canResolveRecipients}
                      aria-invalid={fieldAriaInvalid(errorMessage !== null)}
                      aria-describedby={fieldAriaDescribedBy(errorId, errorMessage !== null)}
                      onChange={(event) => updateRecipientInput(row.key, event.currentTarget.value)}
                      onBlur={() => resolveRecipient(row.key)}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter') {
                          event.preventDefault();
                          resolveRecipient(row.key);
                        }
                      }}
                    />
                    {row.status === 'resolving' ? (
                      <span className="slack-settings__recipient-status" role="status">
                        Resolving...
                      </span>
                    ) : row.resolved !== null ? (
                      <span
                        className="slack-settings__recipient-status slack-settings__recipient-status--resolved"
                        role="status"
                      >
                        <span aria-hidden="true">✓</span>
                        {row.resolved.displayName}
                        {isOwner ? <span className="slack-settings__you-pill">you</span> : null}
                      </span>
                    ) : null}
                    <FieldError id={errorId} message={errorMessage} />
                  </div>
                  <button
                    type="button"
                    className="settings-panel__root-btn"
                    aria-label={`Remove recipient ${rowNumber}`}
                    onClick={() => removeRecipient(row.key)}
                  >
                    Remove
                  </button>
                </div>
              );
            })}
          </div>
          {!canResolveRecipients && recipientRows.length > 0 ? (
            <p className="slack-settings__hint" role="status">
              Add a Slack token before resolving recipients.
            </p>
          ) : null}
        </section>

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
          <>
            <p role="status">
              Connected to {checkResult.identity.teamName} as {checkResult.identity.displayName} (
              {checkResult.tokenType}); {checkResult.grantedScopes.length} scopes granted.
            </p>
            {checkResult.suggestedRecipient !== null && resolvedRecipients.length === 0 ? (
              <p role="status">You will be notified by default.</p>
            ) : null}
          </>
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
              : hasInvalidRecipients
                ? 'Resolve or remove every recipient before saving.'
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
            disabled={
              !dirty || saving || hasInvalidRecipients || (!snapshot.tokenSet && token === '')
            }
            onClick={save}
          >
            Save changes
          </button>
        </div>
      </footer>

      <section className="slack-settings__test" aria-labelledby="slack-test-title">
        <div className="slack-settings__subsection-head">
          <div>
            <h3 id="slack-test-title">Test delivery</h3>
            <p>Send one test message to every resolved recipient above.</p>
          </div>
          <button
            type="button"
            className="setup-wizard__action"
            disabled={!canSendTest}
            onClick={sendTestMessage}
          >
            {sendingTest ? 'Sending...' : 'Send test message'}
          </button>
        </div>
        {testDisabledHint !== null ? (
          <p className="slack-settings__hint">{testDisabledHint}</p>
        ) : null}
        {testError ? <ErrorSurface error={testError} variant="compact" /> : null}
        {testResult ? (
          <ul className="slack-settings__test-results" aria-label="Test message results">
            {testResult.results.map((result) => (
              <li key={`${result.recipient.kind}:${result.recipient.id}`}>
                <strong>{result.recipient.displayName}</strong>
                {result.delivered ? (
                  <span className="slack-settings__test-sent">
                    <span aria-hidden="true">✓</span>
                    Sent
                  </span>
                ) : result.error !== null ? (
                  <ErrorSurface error={result.error} variant="compact" />
                ) : null}
              </li>
            ))}
          </ul>
        ) : null}
      </section>
    </section>
  );
}
