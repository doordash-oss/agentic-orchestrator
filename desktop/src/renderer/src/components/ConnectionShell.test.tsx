import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { ConnectionState } from '../../../shared/ipc';
import { ConnectionShell } from './ConnectionShell';
import { installAgenticoMock } from '../test/agenticoMock';

function state(overrides: Partial<ConnectionState>): ConnectionState {
  return {
    status: 'discovering',
    stage: 'discover',
    detail: 'Looking for a running Agentico runtime.',
    ownership: 'none',
    ...overrides,
  };
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

    const cases = [
      { status: 'idle', label: /idle/i },
      { status: 'discovering', label: /discovering/i },
      { status: 'attaching', label: /attaching/i },
      { status: 'launching', label: /launching/i },
      { status: 'waiting-health', label: /waiting for health/i },
      { status: 'connecting', label: /authenticating/i },
      { status: 'ready', label: /ready/i },
      { status: 'incompatible', label: /incompatible/i },
      { status: 'resources-missing', label: /resources missing/i },
      { status: 'launch-failed', label: /launch failed/i },
      { status: 'crashed', label: /crashed/i },
      { status: 'error', label: /error/i },
    ] as const;
    for (const { status, label } of cases) {
      act(() => {
        mock.emitConnection(
          state({
            status,
            detail: `now ${status}`,
            ...(status === 'error'
              ? { error: { code: 'E_X', message: 'failed', remediation: 'retry' } }
              : {}),
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

  it('announces changes politely via a live region', async () => {
    installAgenticoMock();
    render(<ConnectionShell />);
    const region = await screen.findByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
  });

  it('renders the phase spine with the current stage active', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/resolving/i));
    act(() => {
      mock.emitConnection(state({ status: 'connecting', stage: 'authenticate', detail: 'auth' }));
    });
    const items = screen.getAllByRole('listitem');
    expect(items[4]).toHaveAttribute('aria-current', 'step');
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
