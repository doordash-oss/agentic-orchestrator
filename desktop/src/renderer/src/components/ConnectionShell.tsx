import { useCallback, useEffect, useState } from 'react';
import { CONNECTION_STAGES, type ConnectionStage, type ConnectionState } from '../../../shared/ipc';
import { PhaseSpine } from './PhaseSpine';

const STAGE_LABELS: Record<ConnectionStage, string> = {
  'resolve-runtime': 'Resolve',
  discover: 'Discover',
  attach: 'Attach',
  authenticate: 'Auth',
  ready: 'Ready',
};

const SPINE_STAGES = CONNECTION_STAGES.map((id) => ({ id, label: STAGE_LABELS[id] }));

/** Every status pairs a text label with a shape cue — never color alone. */
const STATUS_META: Record<ConnectionState['status'], { label: string; icon: string }> = {
  idle: { label: 'Idle', icon: '○' },
  'awaiting-gateway': { label: 'Standby', icon: '◌' },
  connecting: { label: 'Connecting', icon: '◐' },
  connected: { label: 'Connected', icon: '●' },
  error: { label: 'Error', icon: '✕' },
};

const INITIAL_STATE: ConnectionState = {
  status: 'idle',
  stage: 'resolve-runtime',
  detail: 'Starting…',
};

function ipcFailureState(err: unknown): ConnectionState {
  // Preload error messages are already redacted/safe.
  const message = err instanceof Error ? err.message : 'The main process is unreachable.';
  return {
    status: 'error',
    stage: 'resolve-runtime',
    detail: 'Could not reach the application core.',
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

  useEffect(() => {
    refresh();
    const unsubscribe = window.agentico.onConnectionChanged(setState);
    return unsubscribe;
  }, [refresh]);

  const meta = STATUS_META[state.status];
  const activeIndex = CONNECTION_STAGES.indexOf(state.stage);

  return (
    <section className="shell-card" aria-label="Agentico connection">
      <header className="shell-card__identity">
        <h1 className="shell-card__title">Agentico</h1>
        <span className="shell-card__version">v{__APP_VERSION__}</span>
      </header>

      <PhaseSpine
        stages={SPINE_STAGES}
        activeIndex={activeIndex}
        tone={state.status === 'error' ? 'error' : 'progress'}
      />

      <p className="shell-card__status" role="status" aria-live="polite">
        <span className="shell-card__status-icon" data-status-icon aria-hidden="true">
          {meta.icon}
        </span>
        <span className="shell-card__status-label" data-status={state.status}>
          {meta.label}
        </span>
        <span className="shell-card__status-detail">{state.detail}</span>
      </p>

      {state.status === 'error' ? (
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
          <button type="button" className="shell-card__retry" onClick={refresh}>
            Retry
          </button>
        </div>
      ) : null}
    </section>
  );
}
