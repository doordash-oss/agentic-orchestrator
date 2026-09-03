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

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactElement,
} from 'react';
import {
  CONNECTION_STAGES,
  isConnectionErrorState,
  type ConnectionStage,
  type ConnectionState,
  type ServerChoiceCandidate,
} from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import { PhaseRailTrack } from '../features/PhaseRailRow';
import { stepSegments } from '../features/phaseRail';
import { ErrorSurface } from './ErrorSurface';

const STAGE_LABELS: Record<ConnectionStage, string> = {
  'resolve-runtime': 'Resolve',
  discover: 'Discover',
  connect: 'Connect',
  'wait-health': 'Health',
  authenticate: 'Auth',
  ready: 'Ready',
};

const SPINE_STAGES = CONNECTION_STAGES.map((id) => ({ id, label: STAGE_LABELS[id] }));

/**
 * Picker rows for registry-located locals just proved liveness in the scan,
 * so they omit a health verdict and read "Running" as they always have;
 * remote candidates carry the verdict of the probe run while the snapshot
 * was assembled.
 */
function candidateStateLabel(candidate: ServerChoiceCandidate): string {
  if (candidate.health === undefined) {
    return 'Running';
  }
  return candidate.health === 'healthy'
    ? 'Running'
    : candidate.health === 'unreachable'
      ? 'Unreachable'
      : 'Checking…';
}

/** Every status pairs a text label with a shape cue — never color alone. */
const STATUS_META: Record<ConnectionState['status'], { label: string; icon: string }> = {
  idle: { label: 'Idle', icon: '○' },
  'resolving-runtime': { label: 'Resolving', icon: '◌' },
  discovering: { label: 'Discovering', icon: '◌' },
  attaching: { label: 'Attaching', icon: '◐' },
  launching: { label: 'Launching', icon: '◐' },
  'waiting-health': { label: 'Waiting for health', icon: '◐' },
  connecting: { label: 'Authenticating', icon: '◐' },
  // User-decision state (not progress, not failure): the ring pauses on a
  // choice the app cannot make for you.
  'awaiting-server-choice': { label: 'Choose a server', icon: '◉' },
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
  // An unparseable rejection degrades to the catalog's IPC-unreachable
  // canonical; a recovered canonical payload (preload sentinel message) wins.
  return {
    status: 'error',
    stage: 'resolve-runtime',
    detail: 'Could not reach the application core.',
    ownership: 'none',
    error: parseIpcError(err),
  };
}

/**
 * Attach-only, snapshot-based server picker (CommandPalette-style rows: a
 * listbox of option buttons, wrap-around arrow navigation on a roving
 * tabindex). No spawn affordance and no live refresh — Retry rescans.
 */
function ServerChoiceList({
  candidates,
  onChoose,
}: {
  candidates: readonly ServerChoiceCandidate[];
  onChoose: (serverKey: string) => void;
}): ReactElement {
  const [highlight, setHighlight] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  // Follow keyboard navigation into focus; hover-only highlight never steals it.
  useEffect(() => {
    const list = listRef.current;
    if (list?.contains(document.activeElement)) {
      list.querySelectorAll('button')[highlight]?.focus();
    }
  }, [highlight]);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setHighlight((index) => (index + 1) % candidates.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setHighlight((index) => (index + candidates.length - 1) % candidates.length);
    } else if (event.key === 'Home') {
      event.preventDefault();
      setHighlight(0);
    } else if (event.key === 'End') {
      event.preventDefault();
      setHighlight(candidates.length - 1);
    }
  };

  return (
    <div
      className="shell-card__picker"
      role="listbox"
      aria-label="Running Agentico servers"
      onKeyDown={onKeyDown}
      ref={listRef}
    >
      {candidates.map((candidate, index) => {
        const selected = index === highlight;
        // Remote candidates have no local runtime dir to disambiguate by.
        const location =
          candidate.runtimeDir === undefined ? ' on a remote host' : ` at ${candidate.runtimeDir}`;
        return (
          <button
            key={candidate.serverKey}
            type="button"
            role="option"
            aria-selected={selected}
            tabIndex={selected ? 0 : -1}
            aria-label={`Connect to ${candidate.name ?? 'unnamed server'}${location}`}
            className="shell-card__picker-row"
            data-selected={selected}
            onMouseEnter={() => setHighlight(index)}
            onFocus={() => setHighlight(index)}
            onClick={() => onChoose(candidate.serverKey)}
          >
            <span className="shell-card__picker-primary" aria-hidden="true">
              {candidate.name ?? 'Unnamed server'}
              {candidate.kind === 'remote' ? (
                <span className="settings-panel__server-kind" data-kind="remote">
                  Remote
                </span>
              ) : null}
            </span>
            <span className="shell-card__picker-runtime" aria-hidden="true">
              {candidate.runtimeDir ?? ''}
            </span>
            <span
              className="shell-card__picker-state"
              data-health={candidate.health ?? 'healthy'}
              aria-hidden="true"
            >
              {candidateStateLabel(candidate)}
            </span>
          </button>
        );
      })}
    </div>
  );
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

  const chooseServer = useCallback((serverKey: string) => {
    window.agentico
      .chooseConnectionServer({ serverKey })
      .then(setState)
      .catch((err: unknown) => setState(ipcFailureState(err)));
  }, []);

  // Both recovery affordances of a failed switch ride the standard attach
  // path: Retry re-attempts the target, Back re-attaches the previous server.
  const switchTo = useCallback((serverKey: string) => {
    window.agentico
      .switchConnectionServer({ serverKey })
      .then(setState)
      .catch((err: unknown) => setState(ipcFailureState(err)));
  }, []);

  useEffect(() => {
    refresh();
    const unsubscribe = window.agentico.onConnectionChanged(setState);
    return unsubscribe;
  }, [refresh]);

  const meta = STATUS_META[state.status];
  // Narrowing: error detail exists exactly when the status is terminal.
  const failure = isConnectionErrorState(state) ? state : null;
  // A failed switch's recovery affordances both ride the standard attach
  // path — Retry re-attempts the target, Back re-attaches the previous
  // server. There is never an automatic rollback.
  const switchContext = failure?.status === 'error' ? failure.switchContext : undefined;
  const retryTarget = switchContext?.attempted.serverKey ?? null;
  const backTarget = switchContext?.previous ?? null;
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
        {state.serverName !== undefined && state.serverName !== null ? (
          <span className="shell-card__version shell-card__version--server">
            {state.serverName}
          </span>
        ) : null}
      </header>

      <PhaseRailTrack
        segments={stepSegments(SPINE_STAGES, activeIndex)}
        label="Connection lifecycle"
        tone={failure !== null ? 'error' : 'progress'}
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

      {state.status === 'awaiting-server-choice' ? (
        <ServerChoiceList candidates={state.candidates} onChoose={chooseServer} />
      ) : null}

      {failure !== null ? (
        <>
          {/* The terminal failure is one canonical ErrorSurface: the code,
           * title, summary, remediation, and the folded diagnostics
           * disclosure all come from the state's canonical error, and Retry
           * is the surface's own local action. A failed switch keeps the
           * previous server as a sibling control outside the surface. */}
          <ErrorSurface
            error={failure.error}
            variant="compact"
            localAction={{
              label: 'Retry',
              onAction: retryTarget !== null ? () => switchTo(retryTarget) : retry,
            }}
          />
          {backTarget !== null ? (
            <button
              type="button"
              className="setup-wizard__action"
              onClick={() => switchTo(backTarget.serverKey)}
            >
              Back to {backTarget.name ?? 'previous server'}
            </button>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
