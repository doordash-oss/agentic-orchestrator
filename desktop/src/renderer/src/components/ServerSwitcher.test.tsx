import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { ServerListRow } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { ServerSwitcher } from './ServerSwitcher';

const ALPHA_KEY = 'a'.repeat(64);
const BETA_KEY = 'b'.repeat(64);
const GAMMA_KEY = 'c'.repeat(64);

function row(overrides: Partial<ServerListRow> & { serverKey: string }): ServerListRow {
  return {
    name: null,
    runtimeDir: '/rt/server',
    current: false,
    health: 'healthy',
    ...overrides,
  };
}

function renderSwitcher(openRequest: { id: number } | null = null) {
  const mock = installAgenticoMock();
  const snapshot = {
    rows: [
      row({ serverKey: ALPHA_KEY, name: 'alpha', runtimeDir: '/rt/alpha', current: true }),
      row({ serverKey: BETA_KEY, name: 'beta', runtimeDir: '/rt/beta' }),
      row({ serverKey: GAMMA_KEY, name: 'gamma', runtimeDir: '/rt/gamma', health: 'unreachable' }),
    ],
  };
  mock.api.listServers.mockResolvedValue(snapshot);
  mock.api.probeServers.mockResolvedValue(snapshot);
  render(<ServerSwitcher currentLabel="alpha" tone="ready" enabled openRequest={openRequest} />);
  return mock;
}

describe('ServerSwitcher', () => {
  it('renders the current server name and opens the popover from its button', async () => {
    const mock = renderSwitcher();
    const trigger = screen.getByRole('button', { name: 'alpha — switch server' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');

    await userEvent.click(trigger);
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    const listbox = await screen.findByRole('listbox', { name: 'Servers' });
    expect(listbox).toBeInTheDocument();
    expect(mock.api.listServers).toHaveBeenCalled();
  });

  it('renders name, runtime dir, health, and the current marker per row', async () => {
    renderSwitcher();
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));

    const current = await screen.findByRole('option', {
      name: 'alpha at /rt/alpha — Connected',
    });
    expect(current).toHaveAttribute('aria-disabled', 'true');
    expect(within(current).getByText('✓')).toBeInTheDocument();
    expect(within(current).getByText('/rt/alpha')).toBeInTheDocument();

    // Unreachable servers are listed but disabled, with an explicit status.
    const unreachable = screen.getByRole('option', {
      name: 'gamma at /rt/gamma — Unreachable',
    });
    expect(unreachable).toHaveAttribute('aria-disabled', 'true');
    expect(unreachable).toHaveTextContent('Unreachable');

    expect(screen.getByRole('option', { name: 'beta at /rt/beta — Available' })).toHaveAttribute(
      'aria-disabled',
      'false',
    );
  });

  it('kicks a probe on open and stops all polling on close', async () => {
    const mock = renderSwitcher();
    const trigger = screen.getByRole('button', { name: 'alpha — switch server' });

    await userEvent.click(trigger);
    await screen.findByRole('listbox', { name: 'Servers' });
    expect(mock.api.probeServers).toHaveBeenCalledWith({ open: true });

    await userEvent.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
    expect(mock.api.probeServers).toHaveBeenCalledWith({ open: false });
  });

  it('updates rows as probe results stream in', async () => {
    const mock = renderSwitcher();
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));
    await screen.findByRole('option', { name: 'beta at /rt/beta — Available' });

    mock.emitServersChanged({
      rows: [row({ serverKey: BETA_KEY, name: 'beta', runtimeDir: '/rt/beta', health: 'probing' })],
    });
    await screen.findByRole('option', { name: 'beta at /rt/beta — Checking…' });
  });

  it('selecting a healthy other server switches and closes; the current row cannot re-trigger one', async () => {
    const mock = renderSwitcher();
    const trigger = screen.getByRole('button', { name: 'alpha — switch server' });
    await userEvent.click(trigger);

    await userEvent.click(
      await screen.findByRole('option', { name: 'alpha at /rt/alpha — Connected' }),
    );
    expect(mock.api.switchConnectionServer).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole('option', { name: 'beta at /rt/beta — Available' }));
    expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({ serverKey: BETA_KEY });
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
  });

  it('dismisses on an outside pointer-down', async () => {
    renderSwitcher();
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));
    await screen.findByRole('listbox', { name: 'Servers' });
    document.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
  });

  it('opens from the routed command signal and focuses the control', async () => {
    renderSwitcher({ id: 7 });
    await screen.findByRole('listbox', { name: 'Servers' });
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'alpha — switch server' }),
    );
  });

  it('stays closed and disabled while not connected', async () => {
    const mock = installAgenticoMock();
    render(
      <ServerSwitcher
        currentLabel="Connecting"
        tone="progress"
        enabled={false}
        openRequest={{ id: 3 }}
      />,
    );
    const trigger = screen.getByRole('button', { name: 'Connecting — switch server' });
    expect(trigger).toBeDisabled();
    expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument();
    expect(mock.api.listServers).not.toHaveBeenCalled();
  });
});
