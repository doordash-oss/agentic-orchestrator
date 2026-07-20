/**
 * Global settings/readiness surface: workspace-root management, provider
 * remediation, appearance, advanced runtime-path selection with restart-
 * pending flow, and the resource workspace for editing runtime
 * configuration, skills, and guidelines.  When a provider or root is
 * degraded, inspection and editing remain reachable while affected actions
 * show server-supplied disabled reasons.
 */
import { useCallback, useEffect, useState } from 'react';
import { useConnectionState, useTheme } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import { ResourceWorkspace } from './ResourceWorkspace';
import type {
  ReadinessSnapshot,
  RepositoryState,
  SessionSummary,
  Settings,
} from '../../../shared/ipc';

const TERMINAL_SESSION_STATUSES = new Set(['Done', 'Failed']);

function isRuntimeIdle(sessions: SessionSummary[]): boolean {
  return sessions.every((s) => TERMINAL_SESSION_STATUSES.has(s.status));
}

/** Normalizes a path for comparison by stripping trailing slashes. */
function normalizePath(p: string | null | undefined): string | null {
  if (p === null || p === undefined || p === '') return null;
  return p.replace(/\/+$/, '');
}

export function SettingsPanel() {
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
  const [dismissedPath, setDismissedPath] = useState<string | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [pickingPath, setPickingPath] = useState(false);

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

  useEffect(() => {
    refresh();
    void window.agentico
      .getSettings()
      .then(setSettings)
      .catch((e: unknown) => {
        setError(parseIpcError(e).message);
      });
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type === 'invalidated') {
        if (
          event.kind === 'resync' ||
          event.kind.startsWith('config') ||
          event.kind.startsWith('resource')
        ) {
          refresh();
        }
      }
    });
    return unsub;
  }, [refresh]);

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

      <section className="settings-panel__section" aria-label="Resource editor">
        <h2 className="settings-panel__section-title">Resources</h2>
        <p className="settings-panel__section-desc">
          Edit runtime configuration, feature configurations, skills, and guidelines. Saves are
          revision-checked and validated by the server.
        </p>
        <ResourceWorkspace />
      </section>

      {showPrompt && hasPendingRestart && !restarting && (
        <div
          className="restart-prompt__backdrop"
          role="dialog"
          aria-label="Restart prompt"
          aria-modal="true"
        >
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
        </div>
      )}
    </section>
  );
}
