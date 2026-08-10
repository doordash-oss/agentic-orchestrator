import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ConnectionStateSchema, type ConnectionState } from '../../../shared/ipc';
import { ConnectionShell } from './ConnectionShell';
import { installAgenticoMock } from '../test/agenticoMock';

/** Builds a state through the schema, so tests can only emit valid variants. */
function state(overrides: Record<string, unknown>): ConnectionState {
  return ConnectionStateSchema.parse({
    status: 'discovering',
    stage: 'discover',
    detail: 'Looking for a running Agentico runtime.',
    ownership: 'none',
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

    const failure = { error: { code: 'E_X', message: 'failed', remediation: 'retry' } };
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
            message: 'The server declares schema series 9.',
            remediation: 'Update the Agentico desktop app and the agentico runtime.',
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
          error: { code: 'E_SERVER_CRASHED', message: 'exited', remediation: 'Retry.' },
        }),
      );
    });
    expect(screen.getByText('E_SERVER_CRASHED')).toBeInTheDocument();

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
          error: { code: 'E_SERVER_CRASHED', message: 'exited' },
          diagnostics: {
            commandContext: 'bundled agentico server --config [path] --state-dir [path]',
            logTail: ['startup failed at [path]', 'credential=[redacted]'],
          },
        }),
      );
    });

    const disclosure = screen.getByText('App runtime diagnostics');
    expect(disclosure).toBeInTheDocument();
    await userEvent.click(disclosure);
    expect(screen.getByText(/bundled agentico server/)).toBeInTheDocument();
    expect(screen.getByLabelText('Redacted runtime log tail')).toHaveTextContent(
      /startup failed at \[path\]/,
    );
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
          error: { code: 'E_X', message: 'm' },
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
    mock.api.getConnectionStatus.mockRejectedValueOnce(
      new Error('E_IPC_PROTOCOL: The main process returned an unrecognized response.'),
    );
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/error/i));
    expect(screen.getByText(/E_IPC_PROTOCOL/)).toBeInTheDocument();
  });
});
