/**
 * Global settings/readiness surface: workspace-root management, provider
 * remediation, appearance, advanced runtime-path selection with restart-
 * pending flow, and the resource workspace for editing runtime
 * configuration, skills, and guidelines.  When a provider or root is
 * degraded, inspection and editing remain reachable while affected actions
 * show server-supplied disabled reasons.
 */
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { useConnectionState, useTheme } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import { WorkspaceDefaultsPanel } from './ConfigEditor';
import type {
  ReadinessSnapshot,
  RepositoryState,
  RoutedRequest,
  SessionSummary,
  Settings,
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

export function SettingsPanel({ routeRequest = null }: { routeRequest?: RoutedRequest | null }) {
  const connection = useConnectionState();
  const [readiness, setReadiness] = useState<ReadinessSnapshot | null>(null);
  const [repos, setRepos] = useState<RepositoryState[]>([]);
  const { preference: themePref, setPreference: setThemePref } = useTheme();
  const [error, setError] = useState<string | null>(null);
  const [addingRoot, setAddingRoot] = useState(false);
  const [removingRoot, setRemovingRoot] = useState<string | null>(null);
  const [reordering, setReordering] = useState(false);
  const [rechecking, setRechecking] = useState(false);
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
  const updatesRef = useRef<HTMLElement | null>(null);
  const diagnosticsRef = useRef<HTMLElement | null>(null);
  const installNowTriggerRef = useRef<HTMLButtonElement | null>(null);
  const clearDiagnosticsTriggerRef = useRef<HTMLButtonElement | null>(null);

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

  useEffect(() => {
    if (routeRequest?.event.target !== 'settings') return;
    const target =
      routeRequest.event.settingsSection === 'diagnostics' ? diagnosticsRef : updatesRef;
    if (routeRequest.event.settingsSection !== undefined) {
      requestAnimationFrame(() =>
        target.current?.scrollIntoView({ block: 'start', behavior: 'auto' }),
      );
    }
  }, [routeRequest]);

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

  const handleRecheckProvider = useCallback(async () => {
    try {
      setRechecking(true);
      await window.agentico.refreshReadiness();
      refresh();
    } catch (e: unknown) {
      setError(parseIpcError(e).message);
    } finally {
      setRechecking(false);
    }
  }, [refresh]);

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
      <header className="settings-panel__header">
        <div>
          <p className="home-surface__eyebrow">Runtime configuration and readiness</p>
          <h1>Settings</h1>
        </div>
      </header>

      {error && (
        <div className="settings-panel__error" role="alert">
          <p className="form-field__error">{error}</p>
          <button type="button" className="setup-wizard__action" onClick={refresh}>
            Try again
          </button>
        </div>
      )}

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
        <button
          type="button"
          className="setup-wizard__action"
          onClick={() => void handleAddRoot()}
          disabled={addingRoot || rechecking}
        >
          {addingRoot ? 'Adding…' : 'Add workspace root'}
        </button>
      </section>

      <section className="settings-panel__section" aria-label="Provider readiness">
        <h2 className="settings-panel__section-title">Providers</h2>
        <p className="settings-panel__section-desc">
          Provider readiness determines which workflow actions are available. Unready providers show
          a cause and a recheck action.
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
                {!p.ready && (
                  <button
                    type="button"
                    className="setup-wizard__action"
                    onClick={() => void handleRecheckProvider()}
                    disabled={rechecking}
                  >
                    {rechecking ? 'Rechecking…' : 'Recheck'}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="settings-panel__section" aria-label="Appearance">
        <h2 className="settings-panel__section-title">Appearance</h2>
        <p className="settings-panel__section-desc">
          Theme applies immediately and persists across restarts. System follows your OS appearance.
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

      <section
        ref={updatesRef}
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

      <section className="settings-panel__section" aria-label="Notifications">
        <h2 className="settings-panel__section-title">Notifications</h2>
        <label className="settings-panel__toggle">
          <input
            type="checkbox"
            checked={settings?.notifications.previewEnabled ?? false}
            onChange={(event) => void handleNotificationPreviewChange(event.currentTarget.checked)}
          />
          <span>
            <strong>Show attention previews</strong>
            <span>
              Off keeps native notifications generic. On includes feature name, attention type, and
              a bounded summary.
            </span>
          </span>
        </label>
      </section>

      <section
        ref={diagnosticsRef}
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

      <section className="settings-panel__section" aria-label="Workspace defaults">
        <h2 className="settings-panel__section-title">Workspace defaults</h2>
        <p className="settings-panel__section-desc">
          Default models per phase, inquireness, and gates for new work. Features can override each
          setting in their own configuration.
        </p>
        <WorkspaceDefaultsPanel />
      </section>

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
