/**
 * Global settings/readiness surface: workspace-root management, provider
 * remediation, appearance, advanced runtime-path selection with restart-
 * pending flow, and the resource workspace for editing runtime
 * configuration, skills, and guidelines.  When a provider or root is
 * degraded, inspection and editing remain reachable while affected actions
 * show server-supplied disabled reasons.
 *
 * The Settings window renders exactly one pane at a time, so this component
 * takes the selected pane and shows only that section. Every section's own
 * markup, controls, and behaviour are unchanged; the data fetching and
 * app-event subscriptions stay at the top so switching panes never re-runs
 * them.
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useConnectionState, useTheme } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import { WorkspaceDefaultsPanel } from './ConfigEditor';
import type { PaneFocusIntent } from './settingsPanes';
import type {
  KnownServer,
  ReadinessSnapshot,
  RepositoryState,
  ServerListRow,
  ServerTokenStatus,
  SessionSummary,
  Settings,
  SettingsPaneId,
  ModelCatalogue,
  DiagnosticsSnapshot,
  UpdateState,
} from '../../../shared/ipc';
import { canInstallInApp, hasActiveWork, installWhenIdleLabel } from '../../../shared/updateState';

const TERMINAL_SESSION_STATUSES = new Set(['Done', 'Failed']);

function isRuntimeIdle(sessions: SessionSummary[]): boolean {
  return sessions.every((s) => TERMINAL_SESSION_STATUSES.has(s.status));
}

/** Normalizes a path for comparison by stripping trailing slashes. */
function normalizePath(p: string | null | undefined): string | null {
  if (p === null || p === undefined || p === '') return null;
  return p.replace(/\/+$/, '');
}

export function SettingsPanel({
  pane = 'workspace-roots',
  focusIntent = null,
}: {
  pane?: SettingsPaneId;
  /** Within-pane focus intent delivered by a settings deep link. */
  focusIntent?: PaneFocusIntent | null;
}) {
  const connection = useConnectionState();
  const [readiness, setReadiness] = useState<ReadinessSnapshot | null>(null);
  const [repos, setRepos] = useState<RepositoryState[]>([]);
  const { preference: themePref, setPreference: setThemePref } = useTheme();
  const [error, setError] = useState<string | null>(null);
  const [addingRoot, setAddingRoot] = useState(false);
  /** Typed-path entry for remote servers; the server's PATCH validates it. */
  const [rootDraft, setRootDraft] = useState('');
  const [rootAddError, setRootAddError] = useState<string | null>(null);
  const [removingRoot, setRemovingRoot] = useState<string | null>(null);
  const [reordering, setReordering] = useState(false);
  const [refreshingProviders, setRefreshingProviders] = useState<Set<string>>(() => new Set());
  const [modelCatalogue, setModelCatalogue] = useState<ModelCatalogue | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [showPrompt, setShowPrompt] = useState(false);
  const [showInstallNowPrompt, setShowInstallNowPrompt] = useState(false);
  const [showClearDiagnosticsPrompt, setShowClearDiagnosticsPrompt] = useState(false);
  const [dismissedPath, setDismissedPath] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [pickingPath, setPickingPath] = useState(false);
  const [updateState, setUpdateState] = useState<UpdateState | null>(null);
  const [checkingUpdates, setCheckingUpdates] = useState(false);
  const [schedulingUpdate, setSchedulingUpdate] = useState(false);
  const [installingUpdate, setInstallingUpdate] = useState(false);
  const [updateCopyNotice, setUpdateCopyNotice] = useState<string | null>(null);
  const [diagnostics, setDiagnostics] = useState<DiagnosticsSnapshot | null>(null);
  const [clearingDiagnostics, setClearingDiagnostics] = useState(false);
  const installNowTriggerRef = useRef<HTMLButtonElement | null>(null);
  const clearDiagnosticsTriggerRef = useRef<HTMLButtonElement | null>(null);

  // Locality picks the root-entry affordance: the native directory picker
  // on a local server, typed paths validated by the server on a remote one.
  const remoteServer = connection.status === 'ready' && connection.kind === 'remote';

  const configuredPath = normalizePath(settings?.runtime.selection ?? null);
  const connectedPath = normalizePath(connection.connectedRuntimeDir ?? null);
  const hasPendingRestart =
    configuredPath !== null && connectedPath !== null && configuredPath !== connectedPath;

  const refresh = useCallback(() => {
    void Promise.all([window.agentico.getReadiness(), window.agentico.listRepositories()])
      .then(([snap, repoList]) => {
        setReadiness(snap);
        setRepos(repoList);
        setError(null);
      })
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
  }, []);

  const refreshUpdates = useCallback(() => {
    void window.agentico
      .getUpdates()
      .then(setUpdateState)
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
  }, []);

  const refreshDiagnostics = useCallback(() => {
    void window.agentico
      .getDiagnostics()
      .then(setDiagnostics)
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
  }, []);

  useEffect(() => {
    refresh();
    refreshUpdates();
    refreshDiagnostics();
    void window.agentico
      .getSettings()
      .then(setSettings)
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type === 'invalidated') {
        if (event.kind === 'updates.changed') {
          refreshUpdates();
        }
        if (event.kind === 'resync' || event.kind.startsWith('config')) {
          refresh();
        }
      }
    });
    return unsub;
  }, [refresh, refreshDiagnostics, refreshUpdates]);

  // Idle-poll: when a restart is pending and the runtime becomes idle,
  // present the Restart Now / Later prompt once per pending path.
  useEffect(() => {
    if (!hasPendingRestart || restarting) return;
    if (dismissedPath !== null && dismissedPath === configuredPath) return;
    let cancelled = false;
    const poll = async () => {
      if (cancelled) return;
      try {
        const sessions = await window.agentico.listSessions();
        if (cancelled) return;
        if (isRuntimeIdle(sessions)) {
          setShowPrompt(true);
        }
      } catch {
        // ignore — will retry on next poll
      }
    };
    void poll();
    const interval = setInterval(poll, 3000);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [hasPendingRestart, configuredPath, dismissedPath, restarting]);

  const providers = readiness?.providers ?? [];
  const workspaceRoots = readiness?.workspaceRoots ?? [];
  const updateHasActiveWork = hasActiveWork(updateState);
  const updateCanInstallInApp = canInstallInApp(updateState);
  const updateIsScheduled = updateState?.status === 'scheduled';

  const handleAddRoot = useCallback(async () => {
    try {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path === null) return;
      setAddingRoot(true);
      await window.agentico.addWorkspaceRoot(picked.path);
      refresh();
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setAddingRoot(false);
    }
  }, [refresh]);

  /**
   * Remote-only root entry: the folder lives on the server host, so the
   * typed path goes straight into the tightened runtime-config PATCH and
   * its rejection is the validation the form reports inline.
   */
  const handleAddTypedRoot = useCallback(async () => {
    const path = rootDraft.trim();
    if (path === '') return;
    if (!path.startsWith('/')) {
      setRootAddError('Enter the path exactly as the server sees it, starting with /.');
      return;
    }
    try {
      setAddingRoot(true);
      setRootAddError(null);
      await window.agentico.addWorkspaceRoot(path);
      setRootDraft('');
      refresh();
    } catch (e: unknown) {
      setRootAddError(parseIpcError(e).message);
    } finally {
      setAddingRoot(false);
    }
  }, [rootDraft, refresh]);

  const handleRemoveRoot = useCallback(
    async (rootPath: string) => {
      try {
        setRemovingRoot(rootPath);
        await window.agentico.removeWorkspaceRoot(rootPath);
        refresh();
      } catch (e: unknown) {
        setError(parseIpcError(e).message);
      } finally {
        setRemovingRoot(null);
      }
    },
    [refresh],
  );

  const handleMoveRoot = useCallback(
    async (rootPath: string, direction: 'up' | 'down') => {
      const currentRoots = workspaceRoots.map((r) => r.path);
      const index = currentRoots.indexOf(rootPath);
      if (index === -1) return;
      const swapIndex = direction === 'up' ? index - 1 : index + 1;
      if (swapIndex < 0 || swapIndex >= currentRoots.length) return;
      const reordered = [...currentRoots];
      const a = reordered[index];
      const b = reordered[swapIndex];
      if (a === undefined || b === undefined) return;
      reordered[index] = b;
      reordered[swapIndex] = a;
      try {
        setReordering(true);
        await window.agentico.reorderWorkspaceRoots(reordered);
        refresh();
      } catch (e: unknown) {
        setError(parseIpcError(e).message);
      } finally {
        setReordering(false);
      }
    },
    [workspaceRoots, refresh],
  );

  const handleThemeChange = useCallback(
    (pref: 'system' | 'light' | 'dark') => {
      setThemePref(pref);
    },
    [setThemePref],
  );

  const handleNotificationPreviewChange = useCallback(async (previewEnabled: boolean) => {
    try {
      const updated = await window.agentico.updateSettings({
        notifications: { previewEnabled },
      });
      setSettings(updated);
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    }
  }, []);

  const handleRecheckProvider = useCallback(async (provider: string) => {
    try {
      setRefreshingProviders((current) => new Set(current).add(provider));
      const result = await window.agentico.refreshProviderModels(provider);
      setReadiness(result.readiness);
      setModelCatalogue(result.catalogue);
      setError(null);
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setRefreshingProviders((current) => {
        const next = new Set(current);
        next.delete(provider);
        return next;
      });
    }
  }, []);

  const handlePickRuntimePath = useCallback(async () => {
    try {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path === null) return;
      setPickingPath(true);
      const updated = await window.agentico.updateSettings({
        runtime: { selection: picked.path },
      });
      setSettings(updated);
      setDismissedPath(null);
      setShowPrompt(false);
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setPickingPath(false);
    }
  }, []);

  const handleRestartNow = useCallback(async () => {
    try {
      setRestarting(true);
      setShowPrompt(false);
      await window.agentico.restartConnection();
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setRestarting(false);
    }
  }, []);

  const handleLater = useCallback(() => {
    setShowPrompt(false);
    setDismissedPath(configuredPath);
  }, [configuredPath]);

  const closeInstallNowPrompt = useCallback(() => {
    setShowInstallNowPrompt(false);
    requestAnimationFrame(() => installNowTriggerRef.current?.focus());
  }, []);

  const closeClearDiagnosticsPrompt = useCallback(() => {
    setShowClearDiagnosticsPrompt(false);
    requestAnimationFrame(() => clearDiagnosticsTriggerRef.current?.focus());
  }, []);

  const handleCheckUpdates = useCallback(async () => {
    try {
      setCheckingUpdates(true);
      setUpdateState(await window.agentico.checkForUpdates());
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setCheckingUpdates(false);
    }
  }, []);

  const handleInstallWhenIdle = useCallback(async () => {
    try {
      setSchedulingUpdate(true);
      setUpdateState(await window.agentico.installUpdateWhenIdle());
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setSchedulingUpdate(false);
    }
  }, []);

  const handleRestartToUpdate = useCallback(async () => {
    try {
      setInstallingUpdate(true);
      setUpdateState(await window.agentico.restartToUpdate());
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setInstallingUpdate(false);
    }
  }, []);

  const handleInstallNow = useCallback(async () => {
    try {
      setInstallingUpdate(true);
      setShowInstallNowPrompt(false);
      setUpdateState(
        await window.agentico.installUpdateNow({ consent: true, stopActiveWork: true }),
      );
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setInstallingUpdate(false);
    }
  }, []);

  const handleOpenReleaseNotes = useCallback(async () => {
    const url = updateState?.releaseNotesUrl;
    if (url === undefined) return;
    try {
      await window.agentico.openExternal({ url });
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    }
  }, [updateState?.releaseNotesUrl]);

  const handleCopyUpdateCommand = useCallback((command: string) => {
    const clipboard: Clipboard | undefined = navigator.clipboard;
    if (clipboard === undefined) {
      setUpdateCopyNotice('Copy is unavailable; select the command text manually.');
      return;
    }
    clipboard.writeText(command).then(
      () => setUpdateCopyNotice('Copied the package-manager command.'),
      () => setUpdateCopyNotice('Copy failed; select the command text manually.'),
    );
  }, []);

  const handleRevealDiagnostics = useCallback(async () => {
    try {
      await window.agentico.revealDiagnostics();
      refreshDiagnostics();
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    }
  }, [refreshDiagnostics]);

  const handleClearDiagnostics = useCallback(async () => {
    try {
      setClearingDiagnostics(true);
      setShowClearDiagnosticsPrompt(false);
      setDiagnostics(await window.agentico.clearDiagnostics());
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setClearingDiagnostics(false);
    }
  }, []);

  return (
    <section className="settings-panel" aria-label="Settings and readiness">
      {error && (
        <div className="settings-panel__error" role="alert">
          <p className="form-field__error">{error}</p>
          <button type="button" className="setup-wizard__action" onClick={refresh}>
            Try again
          </button>
        </div>
      )}

      {pane === 'workspace-roots' && (
        <section className="settings-panel__section" aria-label="Workspace roots">
          <h2 className="settings-panel__section-title">Workspace roots</h2>
          <p className="settings-panel__section-desc">
            Repositories are discovered from these directories. Adding a root refreshes discovery
            without affecting existing features.
          </p>
          <ul className="settings-panel__roots">
            {workspaceRoots.length === 0 ? (
              <li className="settings-panel__root-empty">No workspace roots configured.</li>
            ) : (
              workspaceRoots.map((root, index) => {
                const count = repos.filter(
                  (r) => r.path === root.path || r.path.startsWith(root.path + '/'),
                ).length;
                return (
                  <li
                    key={root.path}
                    className={`settings-panel__root ${root.valid ? '' : 'is-invalid'}`}
                  >
                    <code>{root.path}</code>
                    {!root.valid && root.issue && (
                      <span className="settings-panel__root-issue">{root.issue.message}</span>
                    )}
                    <span className="settings-panel__root-count">
                      {count} {count === 1 ? 'repo' : 'repos'}
                    </span>
                    <div className="settings-panel__root-actions">
                      <button
                        type="button"
                        className="settings-panel__root-btn"
                        onClick={() => void handleMoveRoot(root.path, 'up')}
                        disabled={reordering || index === 0}
                        aria-label={`Move ${root.path} up`}
                      >
                        ↑
                      </button>
                      <button
                        type="button"
                        className="settings-panel__root-btn"
                        onClick={() => void handleMoveRoot(root.path, 'down')}
                        disabled={reordering || index === workspaceRoots.length - 1}
                        aria-label={`Move ${root.path} down`}
                      >
                        ↓
                      </button>
                      <button
                        type="button"
                        className="settings-panel__root-btn settings-panel__root-btn--danger"
                        onClick={() => void handleRemoveRoot(root.path)}
                        disabled={removingRoot === root.path || reordering}
                        aria-label={`Remove ${root.path}`}
                      >
                        {removingRoot === root.path ? '…' : 'Remove'}
                      </button>
                    </div>
                  </li>
                );
              })
            )}
          </ul>
          {remoteServer ? (
            <div className="settings-panel__path-entry">
              <label className="form-field" htmlFor="settings-root-path">
                <span className="form-field__label">Folder path on the server</span>
                <input
                  id="settings-root-path"
                  className="form-field__input"
                  type="text"
                  value={rootDraft}
                  placeholder="/srv/work"
                  spellCheck={false}
                  autoComplete="off"
                  disabled={addingRoot}
                  aria-invalid={rootAddError !== null}
                  onChange={(event) => {
                    setRootDraft(event.currentTarget.value);
                    setRootAddError(null);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'Enter') {
                      event.preventDefault();
                      void handleAddTypedRoot();
                    }
                  }}
                />
              </label>
              {rootAddError !== null ? (
                <p className="form-field__error" role="alert">
                  {rootAddError}
                </p>
              ) : null}
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => void handleAddTypedRoot()}
                disabled={addingRoot || refreshingProviders.size > 0 || rootDraft.trim() === ''}
              >
                {addingRoot ? 'Adding…' : 'Add root'}
              </button>
            </div>
          ) : (
            <button
              type="button"
              className="setup-wizard__action"
              onClick={() => void handleAddRoot()}
              disabled={addingRoot || refreshingProviders.size > 0}
            >
              {addingRoot ? 'Adding…' : 'Add workspace root'}
            </button>
          )}
        </section>
      )}

      {pane === 'servers' && (
        <ServersPane settings={settings} onSettingsChange={setSettings} focusIntent={focusIntent} />
      )}

      {pane === 'providers' && (
        <section className="settings-panel__section" aria-label="Provider readiness">
          <h2 className="settings-panel__section-title">Providers</h2>
          <p className="settings-panel__section-desc">
            Provider readiness determines which workflow actions are available. Recheck a provider
            to refresh its readiness and available models.
          </p>
          {providers.length === 0 ? (
            <p className="settings-panel__provider-empty">No providers registered.</p>
          ) : (
            <ul className="settings-panel__providers">
              {providers.map((p) => (
                <li
                  key={p.name}
                  className={`settings-panel__provider ${p.ready ? '' : 'is-degraded'}`}
                  data-ready={p.ready}
                >
                  <div className="settings-panel__provider-header">
                    <span className="settings-panel__provider-name">{p.name}</span>
                    <span
                      className={`settings-panel__provider-status ${p.ready ? 'is-ready' : 'is-degraded'}`}
                    >
                      {p.ready ? 'Ready' : 'Not ready'}
                    </span>
                  </div>
                  {!p.ready && p.issue && (
                    <p className="settings-panel__provider-cause">
                      {p.issue.message}
                      {p.issue.remedy && (
                        <span className="settings-panel__provider-remedy"> — {p.issue.remedy}</span>
                      )}
                    </p>
                  )}
                  {p.installed && p.version && (
                    <p className="settings-panel__provider-version">v{p.version}</p>
                  )}
                  <button
                    type="button"
                    className="setup-wizard__action"
                    onClick={() => void handleRecheckProvider(p.name)}
                    disabled={refreshingProviders.has(p.name)}
                  >
                    {refreshingProviders.has(p.name) ? 'Rechecking…' : 'Recheck'}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

      {pane === 'appearance' && (
        <section className="settings-panel__section" aria-label="Appearance">
          <h2 className="settings-panel__section-title">Appearance</h2>
          <p className="settings-panel__section-desc">
            Theme applies immediately and persists across restarts. System follows your OS
            appearance.
          </p>
          <div className="settings-panel__theme" role="radiogroup" aria-label="Appearance theme">
            {(['system', 'light', 'dark'] as const).map((pref) => (
              <label key={pref} className="settings-panel__theme-option">
                <input
                  type="radio"
                  name="settings-theme"
                  value={pref}
                  checked={themePref === pref}
                  onChange={() => void handleThemeChange(pref)}
                />
                <span>{pref.charAt(0).toUpperCase() + pref.slice(1)}</span>
              </label>
            ))}
          </div>
        </section>
      )}

      {pane === 'updates' && (
        <section
          className="settings-panel__section settings-panel__section--updates"
          aria-label="Updates"
        >
          <div className="settings-panel__section-head">
            <div>
              <h2 className="settings-panel__section-title">Updates</h2>
              <p className="settings-panel__section-desc">
                Stable releases are checked from GitHub Releases. Installing requires an explicit
                action.
              </p>
            </div>
            <span className="settings-panel__status-pill" data-tone={updateTone(updateState)}>
              {updateState === null ? 'Unknown' : updateStatusLabel(updateState)}
            </span>
          </div>
          <div className="settings-panel__update-grid">
            <span>Current</span>
            <strong>{updateState?.currentVersion ?? 'Unknown'}</strong>
            <span>Target</span>
            <strong>{updateState?.targetVersion ?? 'None'}</strong>
            <span>Package</span>
            <strong>{updateState?.packageFormat ?? 'unknown'}</strong>
            <span>Signature</span>
            <strong>{updateState?.signatureStatus ?? 'unknown'}</strong>
          </div>
          <p className="settings-panel__section-desc">
            {updateState?.message ?? 'Update state has not loaded yet.'}
          </p>
          {updateState?.activeWorkSummary && (
            <p className="settings-panel__update-work">{updateState.activeWorkSummary}</p>
          )}
          {updateState?.guidance && (
            <ul className="settings-panel__guidance">
              {updateState.guidance.map((line) => {
                const command = packageManagerCommandFromGuidance(line);
                return (
                  <li key={line}>
                    {command === null ? (
                      line
                    ) : (
                      <span className="settings-panel__copyable-command">
                        <span>{command.label}</span>
                        <code>{command.value}</code>
                        <button
                          type="button"
                          className="settings-panel__copy-command"
                          aria-label="Copy the package-manager command"
                          onClick={() => handleCopyUpdateCommand(command.value)}
                        >
                          Copy
                        </button>
                      </span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
          {updateCopyNotice && (
            <p className="settings-panel__copy-notice" role="status">
              {updateCopyNotice}
            </p>
          )}
          <div className="settings-panel__button-row">
            <button
              type="button"
              className="setup-wizard__action"
              onClick={() => void handleCheckUpdates()}
              disabled={checkingUpdates}
            >
              {checkingUpdates ? 'Checking…' : 'Check for updates'}
            </button>
            {updateState?.releaseNotesUrl && (
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => void handleOpenReleaseNotes()}
              >
                Release notes
              </button>
            )}
            {updateCanInstallInApp && updateHasActiveWork && (
              <>
                <button
                  type="button"
                  className="setup-wizard__action"
                  onClick={() => void handleInstallWhenIdle()}
                  disabled={schedulingUpdate || installingUpdate || updateIsScheduled}
                >
                  {installWhenIdleLabel({
                    scheduling: schedulingUpdate,
                    scheduled: updateIsScheduled,
                  })}
                </button>
                <button
                  type="button"
                  className="settings-panel__root-btn settings-panel__root-btn--danger"
                  ref={installNowTriggerRef}
                  onClick={() => setShowInstallNowPrompt(true)}
                  disabled={installingUpdate}
                >
                  Stop Work and Install Now
                </button>
              </>
            )}
            {updateCanInstallInApp && !updateHasActiveWork && (
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void handleRestartToUpdate()}
                disabled={installingUpdate}
              >
                {installingUpdate ? 'Installing…' : 'Restart to Update'}
              </button>
            )}
          </div>
        </section>
      )}

      {pane === 'notifications' && (
        <section className="settings-panel__section" aria-label="Notifications">
          <h2 className="settings-panel__section-title">Notifications</h2>
          <label className="settings-panel__toggle">
            <input
              type="checkbox"
              checked={settings?.notifications.previewEnabled ?? false}
              onChange={(event) =>
                void handleNotificationPreviewChange(event.currentTarget.checked)
              }
            />
            <span>
              <strong>Show attention previews</strong>
              <span>
                Off keeps native notifications generic. On includes feature name, attention type,
                and a bounded summary.
              </span>
            </span>
          </label>
        </section>
      )}

      {pane === 'diagnostics' && (
        <section
          className="settings-panel__section settings-panel__section--diagnostics"
          aria-label="Diagnostics"
        >
          <div className="settings-panel__section-head">
            <div>
              <h2 className="settings-panel__section-title">Diagnostics</h2>
              <p className="settings-panel__section-desc">
                Local redacted records are retained for seven days with fixed size and crash-count
                limits.
              </p>
            </div>
            <span className="settings-panel__status-pill" data-tone="neutral">
              {diagnostics?.retention.entryCount ?? 0} entries
            </span>
          </div>
          <div className="settings-panel__diagnostic-summary">
            <span>{diagnostics?.retention.maxAgeDays ?? 7} days</span>
            <span>{formatBytes(diagnostics?.retention.currentBytes ?? 0)} used</span>
            <span>{diagnostics?.retention.crashCount ?? 0} crashes</span>
          </div>
          <div className="settings-panel__button-row">
            <button
              type="button"
              className="setup-wizard__action"
              onClick={() => void handleRevealDiagnostics()}
            >
              Reveal Folder
            </button>
            <button
              type="button"
              className="settings-panel__root-btn settings-panel__root-btn--danger"
              ref={clearDiagnosticsTriggerRef}
              onClick={() => setShowClearDiagnosticsPrompt(true)}
              disabled={clearingDiagnostics}
            >
              {clearingDiagnostics ? 'Clearing…' : 'Clear Diagnostics'}
            </button>
          </div>
          <ul className="settings-panel__diagnostics">
            {(diagnostics?.entries ?? []).slice(0, 6).map((entry) => (
              <li key={entry.id} className="settings-panel__diagnostic" data-level={entry.level}>
                <span>{entry.source}</span>
                <strong>{entry.message}</strong>
                {entry.detail && <p>{entry.detail}</p>}
              </li>
            ))}
            {(diagnostics?.entries ?? []).length === 0 && (
              <li className="settings-panel__provider-empty">No diagnostics recorded.</li>
            )}
          </ul>
        </section>
      )}

      {pane === 'advanced' && (
        <section className="settings-panel__section" aria-label="Advanced runtime path">
          <h2 className="settings-panel__section-title">Advanced</h2>
          <p className="settings-panel__section-desc">
            The runtime path selects where Agentico stores its state and configuration. A change
            persists immediately but takes effect only after restarting the runtime.
          </p>
          <div className="settings-panel__runtime-path">
            <span className="settings-panel__runtime-path-label">Connected runtime:</span>
            <code>{connectedPath ?? 'Default (auto-detected)'}</code>
          </div>
          {configuredPath !== null && configuredPath !== connectedPath && (
            <div className="settings-panel__runtime-path">
              <span className="settings-panel__runtime-path-label">Pending change to:</span>
              <code>{configuredPath}</code>
            </div>
          )}
          <button
            type="button"
            className="setup-wizard__action"
            onClick={() => void handlePickRuntimePath()}
            disabled={pickingPath || restarting}
          >
            {pickingPath ? 'Choosing…' : 'Change runtime path'}
          </button>
          {hasPendingRestart && (
            <div className="restart-pending__banner" role="status">
              <span>
                A runtime path change is pending. It will take effect after the next restart.
              </span>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void handleRestartNow()}
                disabled={restarting}
              >
                {restarting ? 'Restarting…' : 'Restart Now'}
              </button>
            </div>
          )}
        </section>
      )}

      {pane === 'workspace-defaults' && (
        <section className="settings-panel__section" aria-label="Workspace defaults">
          <h2 className="settings-panel__section-title">Workspace defaults</h2>
          <p className="settings-panel__section-desc">
            Default models per phase, inquireness, and gates for new work. Features can override
            each setting in their own configuration.
          </p>
          <WorkspaceDefaultsPanel catalogue={modelCatalogue} />
        </section>
      )}

      {showPrompt && hasPendingRestart && !restarting && (
        <SettingsConfirmationDialog ariaLabel="Restart prompt" onCancel={handleLater}>
          <div className="restart-prompt">
            <h2 className="restart-prompt__title">Runtime path change pending</h2>
            <p className="restart-prompt__summary">
              The runtime path was changed to <code title={configuredPath}>{configuredPath}</code>.
              The runtime is now idle. Restart now to apply the change, or choose Later to keep
              working and restart when ready.
            </p>
            <div className="restart-prompt__actions">
              <button type="button" className="setup-wizard__action" onClick={() => handleLater()}>
                Later
              </button>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void handleRestartNow()}
              >
                Restart Now
              </button>
            </div>
          </div>
        </SettingsConfirmationDialog>
      )}

      {showInstallNowPrompt && updateState && (
        <SettingsConfirmationDialog
          ariaLabel="Install update confirmation"
          onCancel={closeInstallNowPrompt}
        >
          <div className="restart-prompt">
            <h2 className="restart-prompt__title">Stop Work and Install Now?</h2>
            <p className="restart-prompt__summary">
              Agentico will send stop requests before restarting to update to{' '}
              <code>{updateState.targetVersion}</code>. Workflows and AMA may be interrupted if they
              do not stop cleanly.
            </p>
            <p className="restart-prompt__summary">
              {updateState.activeWorkSummary ??
                'Fresh workflow and AMA state will be checked before stopping anything.'}
            </p>
            <div className="restart-prompt__actions">
              <button
                type="button"
                className="setup-wizard__action"
                onClick={closeInstallNowPrompt}
              >
                Cancel
              </button>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void handleInstallNow()}
              >
                Stop Work and Install Now
              </button>
            </div>
          </div>
        </SettingsConfirmationDialog>
      )}

      {showClearDiagnosticsPrompt && (
        <SettingsConfirmationDialog
          ariaLabel="Clear diagnostics confirmation"
          onCancel={closeClearDiagnosticsPrompt}
        >
          <div className="restart-prompt">
            <h2 className="restart-prompt__title">Clear Diagnostics?</h2>
            <p className="restart-prompt__summary">
              This removes only Agentico's local diagnostics records. Runtime workflow state is not
              touched.
            </p>
            <div className="restart-prompt__actions">
              <button
                type="button"
                className="setup-wizard__action"
                onClick={closeClearDiagnosticsPrompt}
              >
                Cancel
              </button>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void handleClearDiagnostics()}
              >
                Clear Diagnostics
              </button>
            </div>
          </div>
        </SettingsConfirmationDialog>
      )}
    </section>
  );
}

const SERVERS_PANE_HEALTH_LABEL: Record<ServerListRow['health'], string> = {
  healthy: 'Available',
  unreachable: 'Unreachable',
  probing: 'Checking…',
};

/** Distinct lead-ins per failure class of the add-server flow. */
function addServerErrorTitle(code: string): string {
  if (code.startsWith('E_CONNECTION_STRING_')) return 'The connection string could not be parsed.';
  if (code === 'E_REMOTE_UNREACHABLE') return 'The server could not be reached.';
  if (code === 'E_REMOTE_INCOMPATIBLE') return 'The server is not compatible with this app.';
  if (code === 'E_REMOTE_AUTH_REJECTED') return 'The token was rejected.';
  return 'The server could not be added.';
}

function serverDisplayName(entry: KnownServer): string {
  return entry.nickname ?? (entry.name === '' ? 'Unnamed server' : entry.name);
}

/**
 * The Servers pane: one list of every known server, local and remote, with
 * the inline add-server form at the top. Each row carries its kind badge,
 * endpoint, probe status and last-seen, and offers nickname editing
 * (settings patch upsert), removal (dedicated channel; main deletes the
 * stored credential and tears down the connection when the removed server
 * was active), and an expandable details panel.
 *
 * Token invariants: the paste field's content crosses to main exactly once
 * via addRemoteServer, is cleared on every outcome, and is never written to
 * settings, component state stores, or logs. Re-paste deliberately reuses
 * the full add flow — remoteServerAdd re-probes and overwrites both the
 * credential blob and the settings entry.
 */
function ServersPane({
  settings,
  onSettingsChange,
  focusIntent = null,
}: {
  settings: Settings | null;
  onSettingsChange: (next: Settings) => void;
  focusIntent?: PaneFocusIntent | null;
}) {
  const [rows, setRows] = useState<readonly ServerListRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editingKey, setEditingKey] = useState<string | null>(null);
  const [nicknameDraft, setNicknameDraft] = useState('');
  const [savingNickname, setSavingNickname] = useState(false);
  const [confirmingKey, setConfirmingKey] = useState<string | null>(null);
  const [removingKey, setRemovingKey] = useState<string | null>(null);
  const [detailsKey, setDetailsKey] = useState<string | null>(null);
  const [tokenStatusByKey, setTokenStatusByKey] = useState<ReadonlyMap<string, ServerTokenStatus>>(
    () => new Map(),
  );
  const [paste, setPaste] = useState('');
  const [adding, setAdding] = useState(false);
  const [addFeedback, setAddFeedback] = useState<
    | { kind: 'error'; title: string; message: string }
    | { kind: 'added' }
    | { kind: 'session-only' }
    | { kind: 'duplicate-local'; serverKey: string }
    | null
  >(null);
  const pasteRef = useRef<HTMLTextAreaElement | null>(null);

  // Health probing is bounded by the pane's mounted lifetime, exactly as
  // the footer popover bounds it by its open lifetime.
  useEffect(() => {
    let alive = true;
    void window.agentico
      .probeServers({ open: true })
      .then((snapshot) => {
        if (alive) setRows(snapshot.rows);
      })
      .catch(() => {});
    const unsubscribe = window.agentico.onServersChanged((snapshot) => {
      setRows(snapshot.rows);
    });
    return () => {
      alive = false;
      unsubscribe();
      void window.agentico.probeServers({ open: false }).catch(() => {});
    };
  }, []);

  // The add-server deep-link intent focuses the paste field.
  useEffect(() => {
    if (focusIntent?.intent === 'add-server') {
      pasteRef.current?.focus();
    }
  }, [focusIntent]);

  const reloadSettings = useCallback(async () => {
    try {
      onSettingsChange(await window.agentico.getSettings());
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    }
  }, [onSettingsChange]);

  const known = settings?.servers.known ?? [];
  const rowByKey = new Map((rows ?? []).map((row) => [row.serverKey, row]));
  const confirmingEntry =
    confirmingKey === null ? undefined : known.find((entry) => entry.serverKey === confirmingKey);

  const saveNickname = useCallback(
    async (entry: KnownServer) => {
      const trimmed = nicknameDraft.trim();
      if (trimmed === (entry.nickname ?? '')) {
        setEditingKey(null);
        return;
      }
      setSavingNickname(true);
      try {
        // An empty draft clears the nickname: the field is dropped entirely
        // rather than persisted as an empty string.
        const { nickname: _dropped, ...base } = entry;
        const next: KnownServer = { ...base, ...(trimmed === '' ? {} : { nickname: trimmed }) };
        const updated = await window.agentico.updateSettings({ servers: { upsertKnown: next } });
        onSettingsChange(updated);
        setEditingKey(null);
        setError(null);
      } catch (e: unknown) {
        setError(parseIpcError(e).message);
      } finally {
        setSavingNickname(false);
      }
    },
    [nicknameDraft, onSettingsChange],
  );

  const confirmRemove = useCallback(
    async (entry: KnownServer) => {
      setConfirmingKey(null);
      setRemovingKey(entry.serverKey);
      try {
        // Main-side removes the credential blob (remote), drops the settings
        // entry, and — when this was the active server — tears the
        // connection down into the standard startup selection flow.
        await window.agentico.removeServer({ serverKey: entry.serverKey });
        await reloadSettings();
        setError(null);
      } catch (e: unknown) {
        setError(parseIpcError(e).message);
      } finally {
        setRemovingKey(null);
      }
    },
    [reloadSettings],
  );

  const toggleDetails = useCallback(
    async (entry: KnownServer) => {
      const opening = detailsKey !== entry.serverKey;
      setDetailsKey(opening ? entry.serverKey : null);
      if (opening && entry.kind === 'remote' && !tokenStatusByKey.has(entry.serverKey)) {
        try {
          const result = await window.agentico.getServerTokenStatus({
            serverKey: entry.serverKey,
          });
          setTokenStatusByKey((current) => new Map(current).set(entry.serverKey, result.status));
        } catch {
          // The details panel surfaces the absence generically below.
        }
      }
    },
    [detailsKey, tokenStatusByKey],
  );

  const submitAdd = useCallback(async () => {
    const connectionString = paste;
    if (connectionString.trim() === '') return;
    setAdding(true);
    setAddFeedback(null);
    setError(null);
    try {
      const result = await window.agentico.addRemoteServer({ connectionString });
      // The pasted string is cleared on every outcome: no token remains in
      // the DOM or in component state once the call has resolved.
      setPaste('');
      if (result.status === 'added') {
        // The same explicit switch the footer popover performs, so the app
        // attaches to the new server immediately.
        void window.agentico.switchConnectionServer({ serverKey: result.serverKey }).catch(() => {
          // The connection shell owns the switch's failure surface.
        });
        setAddFeedback({ kind: 'added' });
      } else if (result.status === 'session-only') {
        setAddFeedback({ kind: 'session-only' });
      } else {
        setAddFeedback({ kind: 'duplicate-local', serverKey: result.serverKey });
      }
      await reloadSettings();
    } catch (e: unknown) {
      setPaste('');
      const parsed = parseIpcError(e);
      setAddFeedback({
        kind: 'error',
        title: addServerErrorTitle(parsed.code),
        message: parsed.message,
      });
    } finally {
      setAdding(false);
    }
  }, [paste, reloadSettings]);

  return (
    <section className="settings-panel__section" aria-label="Servers">
      <h2 className="settings-panel__section-title">Servers</h2>
      <p className="settings-panel__section-desc">
        The servers Agentico knows — the ones it runs on this machine and the remote ones you add. A
        nickname is shown everywhere the server appears.
      </p>
      {error && (
        <p className="form-field__error" role="alert">
          {error}
        </p>
      )}

      <div className="settings-panel__server-add">
        <label className="form-field__label" htmlFor="servers-add-string">
          Add a remote server
        </label>
        <textarea
          id="servers-add-string"
          ref={pasteRef}
          className="form-field__input form-field__input--multiline"
          placeholder="agentico://<token>@<host>:<port>?name=…"
          rows={2}
          spellCheck={false}
          autoComplete="off"
          maxLength={2048}
          value={paste}
          disabled={adding}
          onChange={(event) => setPaste(event.currentTarget.value)}
        />
        <p className="settings-panel__section-desc">
          Paste the connection string the remote server printed. It is verified once and its token
          is stored in the OS keychain — never in settings.
        </p>
        <div className="settings-panel__button-row">
          <button
            type="button"
            className="setup-wizard__action setup-wizard__action--primary"
            onClick={() => void submitAdd()}
            disabled={adding || paste.trim() === ''}
          >
            {adding ? 'Probing…' : 'Probe and connect'}
          </button>
        </div>
        {addFeedback?.kind === 'error' && (
          <p className="form-field__error" role="alert">
            <strong>{addFeedback.title}</strong> {addFeedback.message}
          </p>
        )}
        {addFeedback?.kind === 'added' && (
          <p className="settings-panel__copy-notice" role="status">
            Server added; switching to it now.
          </p>
        )}
        {addFeedback?.kind === 'session-only' && (
          <p className="settings-panel__copy-notice" role="status">
            The server answered, but the OS keychain on this machine is unavailable, so nothing was
            saved. It will not appear in the list; add it again when the keychain is available.
          </p>
        )}
        {addFeedback?.kind === 'duplicate-local' && (
          <p className="settings-panel__copy-notice" role="status">
            That address is one of your local servers, already in the list below.{' '}
            <button
              type="button"
              className="settings-panel__root-btn"
              onClick={() =>
                void window.agentico
                  .switchConnectionServer({ serverKey: addFeedback.serverKey })
                  .catch(() => {})
              }
            >
              Switch to it
            </button>
          </p>
        )}
      </div>

      {settings === null ? (
        <p className="settings-panel__server-empty" role="status">
          Loading servers…
        </p>
      ) : known.length === 0 ? (
        <p className="settings-panel__server-empty">No servers known yet.</p>
      ) : (
        <ul className="settings-panel__servers">
          {known.map((entry) => {
            const joined = rowByKey.get(entry.serverKey);
            const current = joined?.current === true;
            const display = serverDisplayName(entry);
            const statusText = current
              ? 'Connected'
              : (SERVERS_PANE_HEALTH_LABEL[joined?.health ?? 'probing'] ?? 'Checking…');
            const editing = editingKey === entry.serverKey;
            return (
              <li
                key={entry.serverKey}
                className="settings-panel__server"
                data-kind={entry.kind}
                data-current={current}
              >
                <div className="settings-panel__server-header">
                  {editing ? (
                    <div className="settings-panel__server-inline-edit">
                      <input
                        className="form-field__input settings-panel__server-nickname-input"
                        aria-label={`Nickname for ${display}`}
                        placeholder={entry.name === '' ? 'Unnamed server' : entry.name}
                        maxLength={64}
                        value={nicknameDraft}
                        autoFocus
                        onChange={(event) => setNicknameDraft(event.currentTarget.value)}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') {
                            event.preventDefault();
                            void saveNickname(entry);
                          } else if (event.key === 'Escape') {
                            event.preventDefault();
                            setEditingKey(null);
                          }
                        }}
                      />
                      <button
                        type="button"
                        className="settings-panel__root-btn"
                        onClick={() => void saveNickname(entry)}
                        disabled={savingNickname}
                      >
                        {savingNickname ? '…' : 'Save'}
                      </button>
                      <button
                        type="button"
                        className="settings-panel__root-btn"
                        onClick={() => setEditingKey(null)}
                        disabled={savingNickname}
                      >
                        Cancel
                      </button>
                    </div>
                  ) : (
                    <>
                      <span className="settings-panel__server-name">{display}</span>
                      <span className="settings-panel__server-kind" data-kind={entry.kind}>
                        {entry.kind === 'remote' ? 'Remote' : 'Local'}
                      </span>
                      <span className="settings-panel__server-status" role="status">
                        {statusText}
                      </span>
                      <div className="settings-panel__root-actions">
                        <button
                          type="button"
                          className="settings-panel__root-btn"
                          aria-label={`Rename ${display}`}
                          onClick={() => {
                            setEditingKey(entry.serverKey);
                            setNicknameDraft(entry.nickname ?? '');
                          }}
                        >
                          Rename
                        </button>
                        <button
                          type="button"
                          className="settings-panel__root-btn"
                          onClick={() => void toggleDetails(entry)}
                        >
                          Details
                        </button>
                        <button
                          type="button"
                          className="settings-panel__root-btn settings-panel__root-btn--danger"
                          onClick={() => setConfirmingKey(entry.serverKey)}
                          disabled={removingKey === entry.serverKey}
                          aria-label={`Remove ${display}`}
                        >
                          {removingKey === entry.serverKey ? '…' : 'Remove'}
                        </button>
                      </div>
                    </>
                  )}
                </div>
                <code className="settings-panel__server-endpoint">{entry.baseUrl}</code>
                <span className="settings-panel__server-last-seen">
                  Last seen {entry.lastSeenAt.slice(0, 10)}
                </span>
                {detailsKey === entry.serverKey && (
                  <dl className="settings-panel__server-details">
                    <dt>Kind</dt>
                    <dd>{entry.kind === 'remote' ? 'Remote' : 'Local'}</dd>
                    <dt>Base URL</dt>
                    <dd>{entry.baseUrl}</dd>
                    {entry.runtimeDir !== undefined && (
                      <>
                        <dt>Runtime dir</dt>
                        <dd>{entry.runtimeDir}</dd>
                      </>
                    )}
                    <dt>Server name</dt>
                    <dd>{entry.name === '' ? 'Unnamed server' : entry.name}</dd>
                    <dt>Last seen</dt>
                    <dd>{entry.lastSeenAt}</dd>
                    {entry.kind === 'remote' && (
                      <>
                        <dt>Token</dt>
                        <dd>
                          {tokenStatusByKey.get(entry.serverKey) === 'saved'
                            ? 'Saved in the OS keychain.'
                            : tokenStatusByKey.get(entry.serverKey) === 're-paste-required'
                              ? 'Re-paste required — the stored credential cannot be read on this machine.'
                              : tokenStatusByKey.get(entry.serverKey) === 'session-only'
                                ? 'Not saved (session-only).'
                                : 'Checking…'}{' '}
                          {tokenStatusByKey.get(entry.serverKey) !== 'saved' &&
                            tokenStatusByKey.get(entry.serverKey) !== undefined && (
                              <button
                                type="button"
                                className="settings-panel__root-btn"
                                aria-label={`Re-paste the connection string for ${display}`}
                                onClick={() => pasteRef.current?.focus()}
                              >
                                Re-paste the connection string
                              </button>
                            )}
                        </dd>
                      </>
                    )}
                  </dl>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {confirmingEntry !== undefined && (
        <SettingsConfirmationDialog
          ariaLabel="Remove server confirmation"
          onCancel={() => setConfirmingKey(null)}
        >
          <div className="restart-prompt">
            <h2 className="restart-prompt__title">Remove {serverDisplayName(confirmingEntry)}?</h2>
            <p className="restart-prompt__summary">
              This deletes the saved entry
              {confirmingEntry.kind === 'remote'
                ? ' and the stored credential from the OS keychain'
                : ''}
              . The server itself is not stopped or touched.
              {rowByKey.get(confirmingEntry.serverKey)?.current === true
                ? ' You are connected to this server; removing it disconnects and selects another one.'
                : ''}
            </p>
            <div className="restart-prompt__actions">
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => setConfirmingKey(null)}
              >
                Cancel
              </button>
              <button
                type="button"
                className="setup-wizard__action setup-wizard__action--primary"
                onClick={() => void confirmRemove(confirmingEntry)}
              >
                Remove
              </button>
            </div>
          </div>
        </SettingsConfirmationDialog>
      )}
    </section>
  );
}

function SettingsConfirmationDialog({
  ariaLabel,
  onCancel,
  children,
}: {
  ariaLabel: string;
  onCancel(): void;
  children: ReactNode;
}) {
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const previousFocusRef = useRef<HTMLElement | null>(null);

  useEffect(() => {
    previousFocusRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    requestAnimationFrame(() => {
      const firstButton =
        dialogRef.current?.querySelector<HTMLButtonElement>('button:not(:disabled)');
      firstButton?.focus();
    });
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onCancel();
        return;
      }
      if (event.key !== 'Tab') return;
      const controls = [
        ...(dialogRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []),
      ];
      const first = controls[0];
      const last = controls.at(-1);
      if (first === undefined || last === undefined) return;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      previousFocusRef.current?.focus();
    };
  }, [onCancel]);

  return (
    <div className="restart-prompt__backdrop" role="presentation">
      <div ref={dialogRef} role="dialog" aria-label={ariaLabel} aria-modal="true">
        {children}
      </div>
    </div>
  );
}

function updateStatusLabel(update: UpdateState): string {
  return update.status.charAt(0).toUpperCase() + update.status.slice(1);
}

function updateTone(update: UpdateState | null): string {
  if (update === null) return 'neutral';
  if (update.status === 'failed') return 'error';
  if (update.status === 'ready' || update.status === 'scheduled') return 'ready';
  if (update.status === 'checking' || update.status === 'downloading') return 'progress';
  return 'neutral';
}

function packageManagerCommandFromGuidance(line: string): { label: string; value: string } | null {
  const match = /^(Install with):\s*(.+)$/.exec(line);
  if (match === null || match[1] === undefined || match[2] === undefined) return null;
  return { label: `${match[1]}:`, value: match[2] };
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}
