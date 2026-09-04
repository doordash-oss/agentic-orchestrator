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

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { ServerListRow, ServerListSnapshot } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { ServerSwitcher } from './ServerSwitcher';

const ALPHA_KEY = 'a'.repeat(64);
const BETA_KEY = 'b'.repeat(64);
const GAMMA_KEY = 'c'.repeat(64);

function row(overrides: Partial<ServerListRow> & { serverKey: string }): ServerListRow {
  return {
    kind: 'local',
    name: null,
    runtimeDir: '/rt/server',
    current: false,
    health: 'healthy',
    ...overrides,
  };
}

/** Remote rows carry no runtimeDir — build them without the local default. */
function remoteRow(overrides: Partial<ServerListRow> & { serverKey: string }): ServerListRow {
  const { runtimeDir, ...rest } = row({ kind: 'remote', ...overrides });
  void runtimeDir;
  return rest;
}

function renderSwitcher(
  openRequest: { id: number } | null = null,
  bundled?: ServerListSnapshot['bundled'],
) {
  const mock = installAgenticoMock();
  const snapshot: ServerListSnapshot = {
    rows: [
      row({ serverKey: ALPHA_KEY, name: 'alpha', runtimeDir: '/rt/alpha', current: true }),
      row({ serverKey: BETA_KEY, name: 'beta', runtimeDir: '/rt/beta' }),
      row({ serverKey: GAMMA_KEY, name: 'gamma', runtimeDir: '/rt/gamma', health: 'unreachable' }),
    ],
    ...(bundled === undefined ? {} : { bundled }),
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

  it('marks remote rows with the kind badge and no runtime dir', async () => {
    const mock = installAgenticoMock();
    const snapshot = {
      rows: [
        row({ serverKey: ALPHA_KEY, name: 'alpha', runtimeDir: '/rt/alpha', current: true }),
        remoteRow({ serverKey: BETA_KEY, name: 'far-box' }),
      ],
    };
    mock.api.listServers.mockResolvedValue(snapshot);
    mock.api.probeServers.mockResolvedValue(snapshot);
    render(<ServerSwitcher currentLabel="alpha" tone="ready" enabled />);
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));

    const remote = await screen.findByRole('option', { name: 'far-box — Available' });
    const badge = remote.querySelector('.settings-panel__server-kind');
    expect(badge).not.toBeNull();
    expect(badge).toHaveAttribute('data-kind', 'remote');
    expect(badge).toHaveTextContent('Remote');
    // No runtime-dir slot is rendered for a remote row.
    expect(remote.querySelector('.server-switcher__runtime')).toHaveTextContent('');

    await userEvent.click(remote);
    expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({ serverKey: BETA_KEY });
  });

  it('offers a fixed "Add Server…" row that deep-links to Settings → Servers', async () => {
    const mock = renderSwitcher();
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));
    await screen.findByRole('listbox', { name: 'Servers' });

    await userEvent.click(screen.getByRole('button', { name: 'Add Server…' }));
    expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({
      section: 'servers',
      focus: 'add-server',
    });
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: 'alpha — switch server' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
  });

  it('keeps the "Add Server…" row while the list is still loading', async () => {
    const mock = installAgenticoMock();
    mock.api.listServers.mockReturnValue(new Promise(() => {}));
    mock.api.probeServers.mockReturnValue(new Promise(() => {}));
    render(<ServerSwitcher currentLabel="alpha" tone="ready" enabled />);
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));
    expect(screen.getByRole('button', { name: 'Add Server…' })).toBeInTheDocument();
    // The add row is not a pickable option.
    expect(screen.queryByRole('option', { name: /add server/i })).not.toBeInTheDocument();
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

describe('ServerSwitcher bundled runtime', () => {
  const BUNDLED_KEY = 'd'.repeat(64);

  it('lists the stopped bundled runtime as a Start action that never switches', async () => {
    const mock = renderSwitcher(null, {
      serverKey: BUNDLED_KEY,
      runtimeDir: '/rt/bundled',
      running: false,
    });
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));

    const machine = await screen.findByRole('option', {
      name: 'This machine at /rt/bundled — Not running',
    });
    expect(machine).toHaveAttribute('aria-disabled', 'false');
    expect(machine).toHaveTextContent('Start');
    expect(screen.getAllByRole('option')).toHaveLength(4);

    await userEvent.click(machine);
    await waitFor(() => expect(mock.api.startLocalRuntime).toHaveBeenCalledTimes(1));
    expect(mock.api.switchConnectionServer).not.toHaveBeenCalled();
    // Choosing closes the popover like any switch.
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
  });

  it('adds no extra row when the bundled runtime is already listed as a live server', async () => {
    renderSwitcher(null, { serverKey: BETA_KEY, runtimeDir: '/rt/beta', running: true });
    await userEvent.click(screen.getByRole('button', { name: 'alpha — switch server' }));

    await screen.findByRole('option', { name: 'beta at /rt/beta — Available' });
    expect(screen.getAllByRole('option')).toHaveLength(3);
    expect(screen.queryByRole('option', { name: /This machine/ })).toBeNull();
  });
});
