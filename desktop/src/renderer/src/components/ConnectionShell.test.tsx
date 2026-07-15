import { render, screen, waitFor, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ConnectionShell } from './ConnectionShell';
import { installAgenticoMock } from '../test/agenticoMock';

describe('ConnectionShell', () => {
  it('shows the app identity with a mono build version', async () => {
    installAgenticoMock();
    render(<ConnectionShell />);
    expect(screen.getByRole('heading', { name: /agentico/i })).toBeInTheDocument();
    expect(screen.getByText(/v\d+\.\d+\.\d+/)).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/standby/i));
  });

  it('renders the initial state fetched over the preload API', async () => {
    installAgenticoMock({
      connection: { status: 'awaiting-gateway', stage: 'resolve-runtime', detail: 'Waiting.' },
    });
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Waiting.'));
  });

  it('gives every status a text label and a non-color icon cue', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/standby/i));

    const cases = [
      { status: 'idle', label: /idle/i },
      { status: 'connecting', label: /connecting/i },
      { status: 'connected', label: /connected/i },
      { status: 'error', label: /error/i },
    ] as const;
    for (const { status, label } of cases) {
      act(() => {
        mock.emitConnection({
          status,
          stage: 'discover',
          detail: `now ${status}`,
          ...(status === 'error'
            ? { error: { code: 'E_X', message: 'failed', remediation: 'retry' } }
            : {}),
        });
      });
      const region = screen.getByRole('status');
      expect(region).toHaveTextContent(label);
      expect(region.querySelector('[data-status-icon]')).toBeInTheDocument();
      expect(region.querySelector('[data-status-icon]')).toHaveAttribute('aria-hidden', 'true');
    }
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
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/standby/i));
    act(() => {
      mock.emitConnection({ status: 'connecting', stage: 'authenticate', detail: 'auth' });
    });
    const items = screen.getAllByRole('listitem');
    expect(items[3]).toHaveAttribute('aria-current', 'step');
  });

  it('shows the error panel with remediation and retries on demand', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/standby/i));

    act(() => {
      mock.emitConnection({
        status: 'error',
        stage: 'discover',
        detail: 'Discovery failed.',
        error: { code: 'E_DISCOVERY', message: 'No runtime found.', remediation: 'Start one.' },
      });
    });
    expect(screen.getByText('E_DISCOVERY')).toBeInTheDocument();
    expect(screen.getByText(/no runtime found/i)).toBeInTheDocument();
    expect(screen.getByText(/start one/i)).toBeInTheDocument();

    const before = mock.api.getConnectionStatus.mock.calls.length;
    await userEvent.click(screen.getByRole('button', { name: /retry/i }));
    expect(mock.api.getConnectionStatus.mock.calls.length).toBe(before + 1);
  });

  it('keeps the retry button reachable and focusable by keyboard', async () => {
    const mock = installAgenticoMock();
    render(<ConnectionShell />);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/standby/i));
    act(() => {
      mock.emitConnection({
        status: 'error',
        stage: 'discover',
        detail: 'x',
        error: { code: 'E_X', message: 'm' },
      });
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
