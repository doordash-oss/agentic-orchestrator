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

import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ConnectionStateSchema, type ConnectionState } from '../../../shared/ipc';
import { buildCanonicalError, CANONICAL_ERROR_MESSAGE_PREFIX } from '../../../shared/errors';
import { ConnectionShell } from './ConnectionShell';
import { installAgenticoMock } from '../test/agenticoMock';

/** Builds a state through the schema, so tests can only emit valid variants. */
function state(overrides: Record<string, unknown>): ConnectionState {
  return ConnectionStateSchema.parse({
    status: 'discovering',
    stage: 'discover',
    detail: 'Looking for a running Agentico runtime.',
    ownership: 'none',
    // Ready states carry main-owned locality; default it so tests that do not
    // pin locality still emit a schema-valid ready state.
    ...(overrides.status === 'ready' ? { kind: 'local' } : {}),
    ...overrides,
  });
}

describe('ConnectionShell', () => {
  it('shows the app identity with a mono build version', async () => {
    installAgenticoMock();
    render(<ConnectionShell />);
    expect(screen.getByRole('heading', { name: /agentico/i })).toBeInTheDocument();
    expect(screen.getByText(/v\d+\.\d+\.\d+/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
  });

  it('renders the initial state fetched over the preload API', async () => {
    installAgenticoMock({
      connection: state({ detail: 'Waiting.' }),
    });
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Waiting.'));
  });

  it('gives every status a text label and a non-color icon cue', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));

    const failure = {
      error: { code: 'E_X', class: 'blocking', title: 'Failed', summary: 'failed' },
    };
    const cases = [
      { status: 'idle', stage: 'resolve-runtime', label: /idle/i },
      { status: 'resolving-runtime', stage: 'resolve-runtime', label: /resolving/i },
      { status: 'discovering', stage: 'discover', label: /discovering/i },
      { status: 'attaching', stage: 'connect', label: /attaching/i },
      { status: 'launching', stage: 'connect', label: /launching/i },
      {
        status: 'waiting-health',
        stage: 'wait-health',
        label: /waiting for health/i,
        extra: { ownership: 'app-owned' },
      },
      {
        status: 'connecting',
        stage: 'authenticate',
        label: /authenticating/i,
        extra: { ownership: 'app-owned' },
      },
      { status: 'ready', stage: 'ready', label: /ready/i, extra: { ownership: 'app-owned' } },
      {
        status: 'incompatible',
        label: /incompatible/i,
        stage: 'connect',
        extra: { ownership: 'external', ...failure },
      },
      {
        status: 'resources-missing',
        stage: 'connect',
        label: /resources missing/i,
        extra: failure,
      },
      { status: 'launch-failed', stage: 'connect', label: /launch failed/i, extra: failure },
      { status: 'crashed', stage: 'connect', label: /crashed/i, extra: failure },
      { status: 'error', stage: 'connect', label: /error/i, extra: failure },
    ] as const;
    for (const { status, stage, label, ...rest } of cases) {
      act(() => {
        mock.emitConnection(
          state({
            status,
            stage,
            detail: `now ${status}`,
            ...('extra' in rest ? rest.extra : {}),
          }),
        );
      });
      const region = screen.getByRole('status');
      expect(region).toHaveTextContent(label);
      expect(region.querySelector('[data-status-icon]')).toBeInTheDocument();
      expect(region.querySelector('[data-status-icon]')).toHaveAttribute('aria-hidden', 'true');
    }
  });

  it('labels external versus app-owned server ownership', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));

    act(() => {
      mock.emitConnection(state({ status: 'ready', stage: 'ready', ownership: 'external' }));
    });
    expect(screen.getByRole('status')).toHaveTextContent(/external runtime/i);

    act(() => {
      mock.emitConnection(state({ status: 'ready', stage: 'ready', ownership: 'app-owned' }));
    });
    expect(screen.getByRole('status')).toHaveTextContent(/app-managed runtime/i);
  });

  it('shows the server build identity beside the desktop build when known', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'ready',
          stage: 'ready',
          ownership: 'external',
          serverBuild: { version: 'v9.9.9-other' },
        }),
      );
    });
    expect(screen.getByText(/server v9\.9\.9-other/i)).toBeInTheDocument();
  });

  it('shows the reported server name beside the build chip', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'ready',
          stage: 'ready',
          ownership: 'external',
          serverBuild: { version: 'v9.9.9-other' },
          serverName: 'frothy-macchiato',
        }),
      );
    });
    expect(screen.getByText('frothy-macchiato')).toBeInTheDocument();
    expect(screen.getByText(/server v9\.9\.9-other/i)).toBeInTheDocument();
  });

  it('omits the name chip cleanly when the server reports no name', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'ready',
          stage: 'ready',
          ownership: 'external',
          serverBuild: { version: 'v9.9.9-other' },
        }),
      );
    });
    expect(screen.getByText(/server v9\.9\.9-other/i)).toBeInTheDocument();
    // Only the desktop and server-build chips render; no empty name chip.
    expect(document.querySelectorAll('.shell-card__version--server')).toHaveLength(1);
  });

  it('announces changes politely via a live region', async () => {
    installAgenticoMock();
    render(<ConnectionShell />);
    const region = await screen.findByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
  });

  it('renders the lifecycle rail track with the current stage active', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'connecting',
          stage: 'authenticate',
          detail: 'auth',
          ownership: 'app-owned',
        }),
      );
    });
    const track = screen.getByRole('group', { name: 'Connection lifecycle' });
    const items = track.querySelectorAll('.phase-rail__segment');
    expect(items).toHaveLength(6);
    expect(items[4]).toHaveAttribute('aria-current', 'step');
    expect(items[4]).toHaveAttribute('aria-label', 'Auth, current');
    expect(items[0]).toHaveAttribute('data-state', 'completed');
  });

  it('presents incompatible servers with guidance and no way to stop them', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));

    act(() => {
      mock.emitConnection(
        state({
          status: 'incompatible',
          stage: 'connect',
          ownership: 'external',
          detail: 'A running Agentico runtime is not compatible with this app.',
          error: {
            code: 'E_INCOMPATIBLE_SERVER',
            class: 'blocking',
            title: 'The server is not compatible with this app',
            summary: 'The server declares schema series 9.',
            remediation: { hint: 'Update the Agentico desktop app and the agentico runtime.' },
          },
        }),
      );
    });
    expect(screen.getByText('E_INCOMPATIBLE_SERVER')).toBeInTheDocument();
    expect(screen.getByText(/update the agentico desktop app/i)).toBeInTheDocument();
    // The only action offered is Retry — never stop/kill of the external server.
    const buttons = screen.getAllByRole('button');
    expect(buttons).toHaveLength(1);
    expect(buttons[0]).toHaveTextContent(/retry/i);
  });

  it('offers retry on a crash and routes it through the retry IPC op', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));

    act(() => {
      mock.emitConnection(
        state({
          status: 'crashed',
          stage: 'connect',
          detail: 'The app-managed runtime exited unexpectedly.',
          error: {
            code: 'E_SERVER_CRASHED',
            class: 'blocking',
            title: 'The app-managed runtime crashed',
            summary: 'The app-managed Agentico runtime exited unexpectedly.',
          },
        }),
      );
    });
    expect(screen.getByText('E_SERVER_CRASHED')).toBeInTheDocument();

    // One compact ErrorSurface carries the whole failure: code tag, catalog
    // title, and Retry as the surface's own local action.
    const surface = screen.getByText('E_SERVER_CRASHED').closest('.error-surface');
    expect(surface).not.toBeNull();
    expect(surface).toHaveClass('error-surface--compact', 'error-surface--blocking');
    expect(screen.getByText('The app-managed runtime crashed')).toBeInTheDocument();
    expect(document.querySelectorAll('.error-surface')).toHaveLength(1);
    // The legacy hand-rolled failure markup is gone entirely.
    expect(
      document.querySelectorAll(
        '.shell-card__error, .shell-card__error-head, .shell-card__error-code, ' +
          '.shell-card__error-message, .shell-card__error-remediation, .shell-card__diagnostics, ' +
          '.shell-card__retry',
      ),
    ).toHaveLength(0);

    await userEvent.click(screen.getByRole('button', { name: /retry/i }));
    expect(mock.api.retryConnection).toHaveBeenCalledTimes(1);
  });

  it('shows bounded app-runtime diagnostics behind a disclosure', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'crashed',
          stage: 'connect',
          detail: 'The app-managed runtime exited unexpectedly.',
          error: {
            code: 'E_SERVER_CRASHED',
            class: 'blocking',
            title: 'The app-managed runtime crashed',
            summary: 'The app-managed Agentico runtime exited unexpectedly.',
            // The gateway folds the launch command context and bounded log
            // tail into the canonical error's diagnostics string.
            diagnostics:
              'bundled agentico server --config [path] --state-dir [path]\n' +
              'startup failed at [path]\n' +
              'credential=[redacted]',
          },
        }),
      );
    });

    // The gateway folds the launch command context and bounded log tail into
    // the canonical error's diagnostics string; the surface's own disclosure
    // is the only place it renders.
    const disclosure = screen.getByText('More detail');
    expect(disclosure).toBeInTheDocument();
    await userEvent.click(disclosure);
    const diagnostics = screen.getByText(/bundled agentico server/);
    expect(diagnostics).toHaveTextContent(/startup failed at \[path\]/);
    expect(diagnostics).toHaveTextContent('credential=[redacted]');
  });

  it('keeps the retry button reachable and focusable by keyboard', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(
        state({
          status: 'launch-failed',
          stage: 'connect',
          detail: 'x',
          error: { code: 'E_X', class: 'blocking', title: 'Failed', summary: 'm' },
        }),
      );
    });
    await userEvent.tab();
    expect(screen.getByRole('button', { name: /retry/i })).toHaveFocus();
  });

  it('unsubscribes from connection events on unmount', async () => {
    const mock = installAgenticoMock();
    const { unmount } = render(<ConnectionShell />);
    await waitFor(() => expect(mock.listenerCount()).toBe(1));
    unmount();
    expect(mock.listenerCount()).toBe(0);
  });

  it('fails to a safe error state when the preload API rejects', async () => {
    const mock = installAgenticoMock();
    // The preload rethrows envelope errors as sentinel-prefixed canonical
    // payloads; the shell recovers the canonical from the message.
    mock.api.getConnectionStatus.mockRejectedValueOnce(
      new Error(
        CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(buildCanonicalError('E_IPC_PROTOCOL')),
      ),
    );
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/error/i));
    expect(screen.getByText(/E_IPC_PROTOCOL/)).toBeInTheDocument();
  });
});

describe('ConnectionShell server picker', () => {
  const candidates = [
    {
      serverKey: 'key-alpha',
      kind: 'local',
      name: 'alpha',
      runtimeDir: '/home/u/.agentic-orchestrator',
    },
    { serverKey: 'key-beta', kind: 'local', name: 'beta', runtimeDir: '/srv/runtimes/beta' },
    { serverKey: 'key-gamma', kind: 'local', name: null, runtimeDir: '/srv/runtimes/gamma' },
  ] as const;

  function awaiting(): ConnectionState {
    return state({
      status: 'awaiting-server-choice',
      stage: 'connect',
      detail: 'Choose which Agentico server to connect to.',
      candidates: [...candidates],
    });
  }

  it('renders every candidate with its name (or fallback) and runtime dir, marked running', async () => {
    installAgenticoMock({ connection: awaiting() });
    render(<ConnectionShell />);

    await screen.findByRole('listbox', { name: /running agentico servers/i });
    expect(screen.getByRole('status')).toHaveTextContent(/choose a server/i);
    const options = screen.getAllByRole('option');
    expect(options).toHaveLength(3);
    expect(options[0]).toHaveAccessibleName(/alpha/);
    expect(options[0]).toHaveAccessibleName(/\.agentic-orchestrator/);
    expect(options[0]).toHaveTextContent(/running/i);
    expect(options[2]).toHaveTextContent(/unnamed server/i);
    expect(options[2]).toHaveTextContent('/srv/runtimes/gamma');
  });

  it('choosing a candidate sends its identity key over IPC', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ connection: awaiting() });
    render(<ConnectionShell />);

    await screen.findByRole('listbox');
    await user.click(screen.getByRole('option', { name: /beta/ }));
    expect(mock.api.chooseConnectionServer).toHaveBeenCalledWith({ serverKey: 'key-beta' });
  });

  it('arrow keys move the highlighted option with wrap-around and roving tabindex', async () => {
    const user = userEvent.setup();
    installAgenticoMock({ connection: awaiting() });
    render(<ConnectionShell />);

    const options = await screen.findAllByRole('option');
    expect(options[0]).toHaveAttribute('aria-selected', 'true');
    expect(options[0]).toHaveAttribute('tabindex', '0');
    expect(options[1]).toHaveAttribute('aria-selected', 'false');
    expect(options[1]).toHaveAttribute('tabindex', '-1');

    (options[0] as HTMLElement).focus();
    await user.keyboard('{ArrowDown}');
    expect(options[1]).toHaveAttribute('aria-selected', 'true');
    expect(options[1]).toHaveFocus();

    await user.keyboard('{ArrowDown}');
    expect(options[2]).toHaveAttribute('aria-selected', 'true');

    // Wraps around at both ends.
    await user.keyboard('{ArrowDown}');
    expect(options[0]).toHaveAttribute('aria-selected', 'true');
    await user.keyboard('{ArrowUp}');
    expect(options[2]).toHaveAttribute('aria-selected', 'true');
  });

  it('renders a remote candidate with its kind badge, probe health, and no runtime dir', async () => {
    installAgenticoMock({
      connection: state({
        status: 'awaiting-server-choice',
        stage: 'connect',
        candidates: [
          {
            serverKey: 'key-alpha',
            kind: 'local',
            name: 'alpha',
            runtimeDir: '/home/u/.agentic-orchestrator',
          },
          { serverKey: 'key-remote', kind: 'remote', name: 'far-box', health: 'unreachable' },
        ],
      }),
    });
    render(<ConnectionShell />);

    await screen.findByRole('listbox', { name: /running agentico servers/i });
    const remote = screen.getByRole('option', { name: /far-box/ });
    expect(remote).toHaveAccessibleName(/on a remote host/);
    const badge = remote.querySelector('.settings-panel__server-kind');
    expect(badge).not.toBeNull();
    expect(badge).toHaveAttribute('data-kind', 'remote');
    expect(badge).toHaveTextContent('Remote');
    expect(remote).toHaveTextContent('Unreachable');
    expect(remote.querySelector('.shell-card__picker-runtime')).toHaveTextContent('');
    // The local row keeps its established "Running" state.
    expect(screen.getByRole('option', { name: /alpha/ })).toHaveTextContent(/running/i);
  });

  it('offers no add-server affordance in the picker', async () => {
    installAgenticoMock({ connection: awaiting() });
    render(<ConnectionShell />);
    await screen.findByRole('listbox', { name: /running agentico servers/i });
    expect(screen.queryByRole('button', { name: /add server/i })).not.toBeInTheDocument();
  });

  it('keyboard selection attaches the highlighted server and no spawn affordance exists', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ connection: awaiting() });
    render(<ConnectionShell />);

    const options = await screen.findAllByRole('option');
    (options[0] as HTMLElement).focus();
    await user.keyboard('{ArrowDown}{Enter}');
    expect(mock.api.chooseConnectionServer).toHaveBeenCalledWith({ serverKey: 'key-beta' });

    // Attach-only: nothing in the picker offers starting a new server.
    expect(screen.queryByRole('button', { name: /start|launch|spawn/i })).not.toBeInTheDocument();
  });
});

describe('ConnectionShell failed switch', () => {
  function failedSwitch(): ConnectionState {
    return state({
      status: 'error',
      stage: 'connect',
      error: {
        code: 'E_SWITCH_UNAVAILABLE',
        class: 'blocking',
        title: 'The selected server is unavailable',
        summary: 'The selected Agentico server is no longer running.',
      },
      switchContext: {
        attempted: { serverKey: 'key-beta', kind: 'local', name: 'beta', runtimeDir: '/rt/beta' },
        previous: { serverKey: 'key-alpha', kind: 'local', name: 'alpha', runtimeDir: '/rt/alpha' },
      },
    });
  }

  it('offers retry-the-target and back-to-previous, both through switchConnectionServer', async () => {
    const mock = installAgenticoMock({ connection: failedSwitch() });
    render(<ConnectionShell />);

    await userEvent.click(await screen.findByRole('button', { name: 'Retry' }));
    expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({ serverKey: 'key-beta' });

    await userEvent.click(screen.getByRole('button', { name: 'Back to alpha' }));
    expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({ serverKey: 'key-alpha' });
    // The plain retry path stays out of a switch failure.
    expect(mock.api.retryConnection).not.toHaveBeenCalled();
  });

  it('offers only retry when there is no previous server to return to', async () => {
    const orphan = failedSwitch();
    if (orphan.status !== 'error' || orphan.switchContext === undefined) throw new Error('shape');
    installAgenticoMock({
      connection: { ...orphan, switchContext: { ...orphan.switchContext, previous: null } },
    });
    render(<ConnectionShell />);

    expect(await screen.findByRole('button', { name: 'Retry' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Back to/ })).not.toBeInTheDocument();
  });

  it('keeps the plain retry button for non-switch failures', async () => {
    const mock = installAgenticoMock({
      connection: state({
        status: 'error',
        stage: 'connect',
        error: { code: 'E_X', class: 'blocking', title: 'Failed', summary: 'failed' },
      }),
    });
    render(<ConnectionShell />);

    await userEvent.click(await screen.findByRole('button', { name: 'Retry' }));
    expect(mock.api.retryConnection).toHaveBeenCalled();
    expect(mock.api.switchConnectionServer).not.toHaveBeenCalled();
  });
});
