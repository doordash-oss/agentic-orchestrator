import { useCallback, useEffect, useState } from 'react';
import {
  CONNECTION_STAGES,
  isConnectionErrorStatus,
  type ConnectionStage,
  type ConnectionState,
} from '../../../shared/ipc';
import { PhaseSpine } from './PhaseSpine';

const STAGE_LABELS: Record<ConnectionStage, string> = {
  'resolve-runtime': 'Resolve',
  discover: 'Discover',
  connect: 'Connect',
  'wait-health': 'Health',
  authenticate: 'Auth',
  ready: 'Ready',
};

const SPINE_STAGES = CONNECTION_STAGES.map((id) => ({ id, label: STAGE_LABELS[id] }));

/** Every status pairs a text label with a shape cue — never color alone. */
const STATUS_META: Record<ConnectionState['status'], { label: string; icon: string }> = {
  idle: { label: 'Idle', icon: '○' },
  'resolving-runtime': { label: 'Resolving', icon: '◌' },
  discovering: { label: 'Discovering', icon: '◌' },
  attaching: { label: 'Attaching', icon: '◐' },
  launching: { label: 'Launching', icon: '◐' },
  'waiting-health': { label: 'Waiting for health', icon: '◐' },
  connecting: { label: 'Authenticating', icon: '◐' },
  ready: { label: 'Ready', icon: '●' },
  incompatible: { label: 'Incompatible', icon: '⊘' },
  'resources-missing': { label: 'Resources missing', icon: '✕' },
  'launch-failed': { label: 'Launch failed', icon: '✕' },
  crashed: { label: 'Crashed', icon: '✕' },
  error: { label: 'Error', icon: '✕' },
};

const OWNERSHIP_META: Record<
  Exclude<ConnectionState['ownership'], 'none'>,
  { label: string; description: string }
> = {
  external: {
    label: 'External runtime',
    description: 'Managed outside this app; it is never stopped by the app.',
  },
  'app-owned': {
    label: 'App-managed runtime',
    description: 'Started by this app and stopped gracefully when the app quits.',
  },
};

const INITIAL_STATE: ConnectionState = {
  status: 'idle',
  stage: 'resolve-runtime',
  detail: 'Starting…',
  ownership: 'none',
};

function ipcFailureState(err: unknown): ConnectionState {
  // Preload error messages are already redacted/safe.
  const message = err instanceof Error ? err.message : 'The main process is unreachable.';
  return {
    status: 'error',
    stage: 'resolve-runtime',
    detail: 'Could not reach the application core.',
    ownership: 'none',
    error: { code: 'E_IPC', message },
  };
}

export function ConnectionShell() {
  const [state, setState] = useState<ConnectionState>(INITIAL_STATE);

  const refresh = useCallback(() => {
    window.agentico
      .getConnectionStatus()
      .then(setState)
      .catch((err: unknown) => setState(ipcFailureState(err)));
  }, []);

  const retry = useCallback(() => {
    window.agentico
      .retryConnection()
      .then(setState)
      .catch((err: unknown) => setState(ipcFailureState(err)));
  }, []);

  useEffect(() => {
    refresh();
    const unsubscribe = window.agentico.onConnectionChanged(setState);
    return unsubscribe;
  }, [refresh]);

  const meta = STATUS_META[state.status];
  const failed = isConnectionErrorStatus(state.status);
  const activeIndex = CONNECTION_STAGES.indexOf(state.stage);
  const ownership = state.ownership === 'none' ? null : OWNERSHIP_META[state.ownership];

  return (
    <section className="shell-card" aria-label="Agentico connection">
      <header className="shell-card__identity">
        <h1 className="shell-card__title">Agentico</h1>
        <span className="shell-card__version">v{__APP_VERSION__}</span>
        {state.serverBuild !== undefined ? (
          <span className="shell-card__version shell-card__version--server">
            server {state.serverBuild.version}
          </span>
        ) : null}
      </header>

      <PhaseSpine
        stages={SPINE_STAGES}
        activeIndex={activeIndex}
        tone={failed ? 'error' : 'progress'}
      />

      <p className="shell-card__status" role="status" aria-live="polite">
        <span className="shell-card__status-icon" data-status-icon aria-hidden="true">
          {meta.icon}
        </span>
        <span className="shell-card__status-label" data-status={state.status}>
          {meta.label}
        </span>
        {ownership !== null ? (
          <span
            className="shell-card__ownership"
            data-ownership={state.ownership}
            title={ownership.description}
          >
            {ownership.label}
          </span>
        ) : null}
        <span className="shell-card__status-detail">{state.detail}</span>
      </p>

      {failed ? (
        <div className="shell-card__error">
          <div className="shell-card__error-head">
            <span className="shell-card__error-code">{state.error?.code ?? 'E_UNKNOWN'}</span>
          </div>
          <p className="shell-card__error-message">
            {state.error?.message ?? 'The connection failed for an unknown reason.'}
          </p>
          {state.error?.remediation !== undefined ? (
            <p className="shell-card__error-remediation">{state.error.remediation}</p>
          ) : null}
          <button type="button" className="shell-card__retry" onClick={retry}>
            Retry
          </button>
        </div>
      ) : null}
    </section>
  );
}
