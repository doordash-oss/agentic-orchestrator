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

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import {
  applyServersPatch,
  defaultSettings,
  type ConnectionState,
  type KnownServer,
  type ServerListRow,
  type ServerListSnapshot,
  type Settings,
} from '../../../shared/ipc';
import {
  installAgenticoMock,
  ipcError,
  readySnapshot,
  type AgenticoMock,
} from '../test/agenticoMock';
import { SettingsPanel } from './SettingsPanel';

afterEach(cleanup);

const LOCAL: KnownServer = {
  serverKey: 'a'.repeat(32),
  kind: 'local',
  name: 'alpha',
  nickname: 'main box',
  baseUrl: 'http://127.0.0.1:51001',
  runtimeDir: '/rt/alpha',
  lastSeenAt: '2026-08-09T09:00:00.000Z',
};

const REMOTE: KnownServer = {
  serverKey: 'b'.repeat(32),
  kind: 'remote',
  name: 'far-box',
  baseUrl: 'http://10.1.2.3:8080',
  lastSeenAt: '2026-08-08T08:00:00.000Z',
};

function serversRows(overrides: { currentKey?: string } = {}): { rows: ServerListRow[] } {
  return {
    rows: [
      {
        serverKey: LOCAL.serverKey,
        kind: 'local',
        name: 'alpha',
        nickname: 'main box',
        runtimeDir: '/rt/alpha',
        current: overrides.currentKey === LOCAL.serverKey,
        health: 'healthy',
      },
      {
        serverKey: REMOTE.serverKey,
        kind: 'remote',
        name: 'far-box',
        current: overrides.currentKey === REMOTE.serverKey,
        health: 'unreachable',
      },
    ],
  };
}

function installServersMock(options: { settings?: Settings; rows?: ServerListSnapshot }): {
  mock: AgenticoMock;
  settings: { current: Settings };
} {
  const store = { current: options.settings ?? serversSettings() };
  const mock = installAgenticoMock({ readiness: readySnapshot(), settings: store.current });
  mock.api.getSettings.mockImplementation(() => Promise.resolve(store.current));
  mock.api.probeServers.mockResolvedValue(options.rows ?? serversRows());
  return { mock, settings: store };
}

function serversSettings(known: KnownServer[] = [LOCAL, REMOTE], lastUsed: string | null = null) {
  const base = defaultSettings();
  return { ...base, servers: { known, lastUsed } };
}

/** Applies servers patches in-test so nickname edits read back visibly. */
function livePatching(mock: AgenticoMock, store: { current: Settings }): void {
  mock.api.updateSettings.mockImplementation((patch) => {
    if (patch.servers !== undefined) {
      store.current = {
        ...store.current,
        servers: applyServersPatch(store.current.servers, patch.servers),
      };
    }
    return Promise.resolve(store.current);
  });
}

const READY_CONNECTION: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected.',
  ownership: 'external',
  kind: 'remote',
  serverKey: REMOTE.serverKey,
};

describe('SettingsPanel servers pane', () => {
  it('renders local and remote servers with kind badges, names, endpoints, and joined status', async () => {
    installServersMock({ rows: serversRows({ currentKey: LOCAL.serverKey }) });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const items = await within(pane).findAllByRole('listitem');
    expect(items).toHaveLength(2);

    const [localRow, remoteRow] = items;
    // The nickname wins over the base name; kinds read as clear badges.
    expect(within(localRow!).getByText('main box')).toBeVisible();
    expect(localRow!).toHaveTextContent('Local');
    expect(localRow!).toHaveTextContent('http://127.0.0.1:51001');
    expect(localRow!).toHaveTextContent('Last seen 2026-08-09');

    expect(within(remoteRow!).getByText('far-box')).toBeVisible();
    expect(remoteRow!).toHaveTextContent('Remote');
    expect(remoteRow!).toHaveTextContent('http://10.1.2.3:8080');
    expect(remoteRow!).toHaveTextContent('Last seen 2026-08-08');

    // Joined probe health/current arrives a hop after the rows render.
    await waitFor(() => expect(localRow!).toHaveTextContent('Connected'));
    await waitFor(() => expect(remoteRow!).toHaveTextContent('Unreachable'));
  });

  it('edits a nickname inline and persists it via a servers upsertKnown patch', async () => {
    const user = userEvent.setup();
    const { mock, settings } = installServersMock({});
    livePatching(mock, settings);
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    await user.click(await within(pane).findByRole('button', { name: 'Rename far-box' }));

    const input = screen.getByRole('textbox', { name: 'Nickname for far-box' });
    expect(input).toHaveFocus();
    await user.type(input, 'rack mount');
    await user.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        servers: { upsertKnown: { ...REMOTE, nickname: 'rack mount' } },
      }),
    );
    expect(await within(pane).findByText('rack mount')).toBeVisible();
    expect(
      settings.current.servers.known.find((entry) => entry.serverKey === REMOTE.serverKey)
        ?.nickname,
    ).toBe('rack mount');
  });

  it('clears the nickname when the inline edit saves an empty name', async () => {
    const user = userEvent.setup();
    const { mock, settings } = installServersMock({});
    livePatching(mock, settings);
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    await user.click(await within(pane).findByRole('button', { name: 'Rename main box' }));
    const input = screen.getByRole('textbox', { name: 'Nickname for main box' });
    await user.clear(input);
    await user.keyboard('{Enter}');

    const { nickname: _dropped, ...withoutNickname } = LOCAL;
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        servers: { upsertKnown: withoutNickname },
      }),
    );
    // The base name returns once the nickname is cleared.
    expect(await within(pane).findByText('alpha')).toBeVisible();
    expect(
      settings.current.servers.known.find((entry) => entry.serverKey === LOCAL.serverKey)?.nickname,
    ).toBeUndefined();
  });

  it('removes a server only through the confirmation dialog', async () => {
    const user = userEvent.setup();
    const { mock, settings } = installServersMock({});
    mock.api.removeServer.mockImplementation(() => {
      settings.current = {
        ...settings.current,
        servers: {
          ...settings.current.servers,
          known: settings.current.servers.known.filter(
            (entry) => entry.serverKey !== REMOTE.serverKey,
          ),
        },
      };
      return Promise.resolve(READY_CONNECTION);
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    await user.click(await within(pane).findByRole('button', { name: 'Remove far-box' }));

    const dialog = screen.getByRole('dialog', { name: 'Remove server confirmation' });
    expect(within(dialog).getByRole('heading', { name: 'Remove far-box?' })).toBeVisible();
    expect(within(dialog).getByText(/the stored credential from the OS keychain/)).toBeVisible();
    expect(within(dialog).queryByText(/You are connected to this server/)).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('dialog', { name: 'Remove server confirmation' })).toBeNull();
    expect(mock.api.removeServer).not.toHaveBeenCalled();

    await user.click(within(pane).getByRole('button', { name: 'Remove far-box' }));
    await user.click(
      within(screen.getByRole('dialog', { name: 'Remove server confirmation' })).getByRole(
        'button',
        { name: 'Remove' },
      ),
    );

    await waitFor(() =>
      expect(mock.api.removeServer).toHaveBeenCalledWith({ serverKey: REMOTE.serverKey }),
    );
    // The pane re-reads settings after the removal; the row is gone.
    await waitFor(() => expect(within(pane).queryByText('far-box')).not.toBeInTheDocument());
  });

  it('notes the impending disconnect when the removed server is the connected one', async () => {
    const user = userEvent.setup();
    const { mock } = installServersMock({
      rows: serversRows({ currentKey: REMOTE.serverKey }),
    });
    mock.api.removeServer.mockResolvedValue(READY_CONNECTION);
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const remoteRow = (await within(pane).findAllByRole('listitem'))[1]!;
    await waitFor(() => expect(remoteRow).toHaveTextContent('Connected'));

    await user.click(within(pane).getByRole('button', { name: 'Remove far-box' }));
    const dialog = screen.getByRole('dialog', { name: 'Remove server confirmation' });
    expect(within(dialog).getByText(/You are connected to this server/)).toBeVisible();

    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    await waitFor(() =>
      expect(mock.api.removeServer).toHaveBeenCalledWith({ serverKey: REMOTE.serverKey }),
    );
  });

  it('details shows kind, base URL, server name, last seen, and token status with re-paste remediation', async () => {
    const user = userEvent.setup();
    const { mock } = installServersMock({});
    mock.api.getServerTokenStatus.mockResolvedValue({ status: 're-paste-required' });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const remoteRow = within(pane).getByText('far-box').closest('li')!;
    await user.click(within(remoteRow).getByRole('button', { name: 'Details' }));

    const kindBadges = remoteRow.querySelectorAll(
      '.settings-panel__server-kind[data-kind="remote"]',
    );
    expect(kindBadges).toHaveLength(1);
    expect(remoteRow).toHaveTextContent('http://10.1.2.3:8080');
    expect(remoteRow).toHaveTextContent('far-box');
    expect(remoteRow).toHaveTextContent('2026-08-08T08:00:00.000Z');
    await waitFor(() =>
      expect(mock.api.getServerTokenStatus).toHaveBeenCalledWith({ serverKey: REMOTE.serverKey }),
    );
    expect(
      await within(remoteRow).findByText(
        /Re-paste required — the stored credential cannot be read on this machine/,
      ),
    ).toBeVisible();

    // The re-paste action hands off to the add form, which overwrites the blob.
    await user.click(
      within(remoteRow).getByRole('button', { name: 'Re-paste the connection string for far-box' }),
    );
    expect(screen.getByRole('textbox', { name: /add a remote server/i })).toHaveFocus();
  });

  it('reports a saved token without a re-paste action', async () => {
    const { mock } = installServersMock({});
    const store = { current: serversSettings([REMOTE]) };
    mock.api.getSettings.mockImplementation(() => Promise.resolve(store.current));
    mock.api.getServerTokenStatus.mockResolvedValueOnce({ status: 'saved' });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const row = within(pane).getByText('far-box').closest('li')!;
    await userEvent.click(within(row).getByRole('button', { name: 'Details' }));
    expect(await within(row).findByText(/Saved in the OS keychain/)).toBeVisible();
    expect(within(row).queryByRole('button', { name: /Re-paste/ })).not.toBeInTheDocument();
  });

  describe('add server form', () => {
    it('focuses the paste field for the add-server route intent', async () => {
      installServersMock({});
      render(<SettingsPanel pane="servers" focusIntent={{ intent: 'add-server', seq: 1 }} />);

      await screen.findByRole('region', { name: 'Servers' });
      await waitFor(() =>
        expect(screen.getByRole('textbox', { name: /add a remote server/i })).toHaveFocus(),
      );
    });

    it.each([
      {
        code: 'E_CONNECTION_STRING_TOKEN',
        lead: 'The connection string is missing its token',
        message: 'The connection string is missing its token. Copy the whole agentico:// string.',
      },
      {
        code: 'E_REMOTE_UNREACHABLE',
        lead: 'The server could not be reached',
        message: 'The server did not answer. Check the host and port, then paste it again.',
      },
      {
        code: 'E_REMOTE_INCOMPATIBLE',
        lead: 'The server is not compatible with this app',
        message: 'The server runs an unsupported API version. Update the remote server.',
      },
      {
        code: 'E_REMOTE_AUTH_REJECTED',
        lead: 'The token was rejected',
        message: 'The server rejected this token. Copy a fresh connection string.',
      },
    ])(
      'renders a distinct inline error for $code and clears the paste field',
      async ({ code, lead, message }) => {
        const user = userEvent.setup();
        const { mock } = installServersMock({});
        mock.api.addRemoteServer.mockRejectedValue(ipcError(code, message, { title: lead }));
        render(<SettingsPanel pane="servers" />);

        const pane = await screen.findByRole('region', { name: 'Servers' });
        const pasted = 'agentico://tok-layered-123@10.1.2.3:8080';
        const field = within(pane).getByRole('textbox', { name: /add a remote server/i });
        await user.type(field, pasted);
        await user.click(within(pane).getByRole('button', { name: 'Probe and connect' }));

        // The canonical rejection renders as one compact ErrorSurface with
        // the code tag, catalog title, and summary.
        const alert = await within(pane).findByRole('alert');
        expect(alert).toHaveClass('error-surface', 'error-surface--compact');
        expect(within(alert).getByText(code)).toHaveClass('error-surface__code');
        expect(alert).toHaveTextContent(lead);
        expect(alert).toHaveTextContent(message);
        // The pasted string (which embeds the token) is out of the DOM.
        expect(field).toHaveValue('');
        expect(pane).not.toHaveTextContent(pasted);
      },
    );

    it('switches immediately and clears the field on added', async () => {
      const user = userEvent.setup();
      const { mock } = installServersMock({});
      mock.api.addRemoteServer.mockResolvedValue({ status: 'added', serverKey: 'c'.repeat(32) });
      render(<SettingsPanel pane="servers" />);

      const pane = await screen.findByRole('region', { name: 'Servers' });
      await user.type(
        within(pane).getByRole('textbox', { name: /add a remote server/i }),
        'agentico://tok-layered-123@10.4.5.6:9090',
      );
      await user.click(within(pane).getByRole('button', { name: 'Probe and connect' }));

      await waitFor(() =>
        expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({
          serverKey: 'c'.repeat(32),
        }),
      );
      expect(await within(pane).findByText(/Server added; switching to it now/)).toBeVisible();
      expect(within(pane).getByRole('textbox', { name: /add a remote server/i })).toHaveValue('');
    });

    it('shows the session-only notice without switching', async () => {
      const user = userEvent.setup();
      const { mock } = installServersMock({});
      mock.api.addRemoteServer.mockResolvedValue({
        status: 'session-only',
        serverKey: 'c'.repeat(32),
      });
      render(<SettingsPanel pane="servers" />);

      const pane = await screen.findByRole('region', { name: 'Servers' });
      await user.type(
        within(pane).getByRole('textbox', { name: /add a remote server/i }),
        'agentico://tok-layered-123@10.4.5.6:9090',
      );
      await user.click(within(pane).getByRole('button', { name: 'Probe and connect' }));

      expect(
        await within(pane).findByText(/the OS keychain on this machine is unavailable/),
      ).toBeVisible();
      expect(mock.api.switchConnectionServer).not.toHaveBeenCalled();
    });

    it('shows the duplicate-local steering notice with a switch shortcut', async () => {
      const user = userEvent.setup();
      const { mock } = installServersMock({});
      mock.api.addRemoteServer.mockResolvedValue({
        status: 'duplicate-local',
        serverKey: LOCAL.serverKey,
      });
      render(<SettingsPanel pane="servers" />);

      const pane = await screen.findByRole('region', { name: 'Servers' });
      await user.type(
        within(pane).getByRole('textbox', { name: /add a remote server/i }),
        'agentico://tok-layered-123@127.0.0.1:51001',
      );
      await user.click(within(pane).getByRole('button', { name: 'Probe and connect' }));

      expect(
        await within(pane).findByText(/one of your local servers, already in the list below/),
      ).toBeVisible();
      await user.click(within(pane).getByRole('button', { name: 'Switch to it' }));
      await waitFor(() =>
        expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({
          serverKey: LOCAL.serverKey,
        }),
      );
    });

    it('disables the probe button while a probe is in flight', async () => {
      const user = userEvent.setup();
      const { mock } = installServersMock({});
      let release: (() => void) | undefined;
      mock.api.addRemoteServer.mockReturnValue(
        new Promise((resolve) => {
          release = () => resolve({ status: 'added', serverKey: 'c'.repeat(32) });
        }),
      );
      render(<SettingsPanel pane="servers" />);

      const pane = await screen.findByRole('region', { name: 'Servers' });
      const field = within(pane).getByRole('textbox', { name: /add a remote server/i });
      await user.type(field, 'agentico://tok@10.4.5.6:9090');
      const button = within(pane).getByRole('button', { name: 'Probe and connect' });
      await user.click(button);

      expect(within(pane).getByRole('button', { name: 'Probing…' })).toBeDisabled();
      expect(field).toBeDisabled();

      await act(async () => {
        release!();
      });
      // The in-flight state lifts: the label reverts and the field re-enables;
      // the button stays disabled only because the paste field was cleared.
      expect(await within(pane).findByRole('button', { name: 'Probe and connect' })).toBeDisabled();
      expect(field).toBeEnabled();
    });
  });
});

describe('SettingsPanel servers pane: This machine', () => {
  const BUNDLED_KEY = 'c'.repeat(32);

  it('always lists the bundled runtime, with Start when it is not running', async () => {
    const user = userEvent.setup();
    const { mock } = installServersMock({
      rows: {
        ...serversRows(),
        bundled: { serverKey: BUNDLED_KEY, runtimeDir: '/rt/bundled', running: false },
      },
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const items = await within(pane).findAllByRole('listitem');
    // The permanent row comes first, ahead of the two persisted entries.
    expect(items).toHaveLength(3);
    const machine = items[0]!;
    await waitFor(() => expect(machine).toHaveTextContent('This machine'));
    expect(machine).toHaveTextContent('Local');
    expect(machine).toHaveTextContent('/rt/bundled');
    expect(within(machine).getByRole('status')).toHaveTextContent('Not running');
    expect(machine).not.toHaveTextContent('Unreachable');

    await user.click(within(machine).getByRole('button', { name: 'Start This machine' }));
    await waitFor(() => expect(mock.api.startLocalRuntime).toHaveBeenCalledTimes(1));
    expect(mock.api.switchConnectionServer).not.toHaveBeenCalled();
  });

  it('folds the persisted local entry for the app runtime into one row and offers Switch when it is running', async () => {
    const user = userEvent.setup();
    const { mock } = installServersMock({
      rows: {
        ...serversRows(),
        bundled: { serverKey: LOCAL.serverKey, runtimeDir: '/rt/alpha', running: true },
      },
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const items = await within(pane).findAllByRole('listitem');
    // One row for the machine, one for the remote — never a second local row.
    expect(items).toHaveLength(2);
    const machine = items[0]!;
    await waitFor(() => expect(machine).toHaveTextContent('This machine'));
    expect(within(machine).getByRole('status')).toHaveTextContent('Running');
    // The folded entry keeps its endpoint and its management actions.
    expect(machine).toHaveTextContent('http://127.0.0.1:51001');
    expect(within(machine).getByRole('button', { name: 'Rename This machine' })).toBeVisible();
    expect(within(machine).getByRole('button', { name: 'Remove This machine' })).toBeVisible();

    await user.click(within(machine).getByRole('button', { name: 'Switch to This machine' }));
    await waitFor(() =>
      expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({ serverKey: LOCAL.serverKey }),
    );
    expect(mock.api.startLocalRuntime).not.toHaveBeenCalled();
  });

  it('reads Connected with no action while the bundled runtime is the current server', async () => {
    installServersMock({
      rows: {
        ...serversRows({ currentKey: LOCAL.serverKey }),
        bundled: { serverKey: LOCAL.serverKey, runtimeDir: '/rt/alpha', running: true },
      },
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const machine = (await within(pane).findAllByRole('listitem'))[0]!;
    await waitFor(() => expect(within(machine).getByRole('status')).toHaveTextContent('Connected'));
    expect(within(machine).queryByRole('button', { name: /^Start/ })).toBeNull();
    expect(within(machine).queryByRole('button', { name: /^Switch/ })).toBeNull();
  });

  it('never shows the folded entry as Unreachable: a dead bundled runtime reads Not running with Start', async () => {
    const dead = serversRows();
    dead.rows[0] = { ...dead.rows[0]!, health: 'unreachable' };
    installServersMock({
      rows: {
        ...dead,
        bundled: { serverKey: LOCAL.serverKey, runtimeDir: '/rt/alpha', running: false },
      },
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    const machine = (await within(pane).findAllByRole('listitem'))[0]!;
    await waitFor(() =>
      expect(within(machine).getByRole('status')).toHaveTextContent('Not running'),
    );
    expect(machine).not.toHaveTextContent('Unreachable');
    expect(within(machine).getByRole('button', { name: 'Start This machine' })).toBeVisible();
  });

  it('shows the bundled runtime even when no server is persisted yet', async () => {
    installServersMock({
      settings: serversSettings([]),
      rows: {
        rows: [],
        bundled: { serverKey: BUNDLED_KEY, runtimeDir: '/rt/bundled', running: false },
      },
    });
    render(<SettingsPanel pane="servers" />);

    const pane = await screen.findByRole('region', { name: 'Servers' });
    expect(await within(pane).findByText('This machine')).toBeVisible();
    expect(within(pane).queryByText('No servers known yet.')).toBeNull();
  });
});
