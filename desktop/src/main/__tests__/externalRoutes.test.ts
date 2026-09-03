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

import { describe, expect, it, vi } from 'vitest';

import { SafeErrorException, safeError } from '../../shared/errors';
import type { AppRouteEvent, RemoteServerAddResult } from '../../shared/ipc';
import {
  addServerFromLink,
  routeFromArgv,
  routeFromUrl,
  type AddServerLinkDeps,
} from '../externalRoutes';

const TOKEN = 'tok-secret-xyz';
const CONNECTION_STRING = `agentico://${TOKEN}@10.1.2.3:8080`;
const SERVER_KEY = 'a'.repeat(32);

function appRouteOf(route: ReturnType<typeof routeFromUrl>): AppRouteEvent {
  if (route === null || route.kind !== 'app-route') {
    throw new Error(`expected an app-route, got ${route === null ? 'null' : route.kind}`);
  }
  return route.event;
}

describe('routeFromUrl', () => {
  it('recognizes a full connection string as an add-server intent', () => {
    expect(routeFromUrl(CONNECTION_STRING)).toEqual({
      kind: 'add-server',
      connectionString: CONNECTION_STRING,
    });
  });

  it('recognizes a named connection string with an IPv6 host', () => {
    const raw = `agentico://${TOKEN}@[fe80::1]:9090?name=lab`;
    expect(routeFromUrl(raw)).toEqual({ kind: 'add-server', connectionString: raw });
  });

  it('prefers the connection-string reading when userinfo shadows a named route', () => {
    const raw = `agentico://${TOKEN}@updates:8080`;
    expect(routeFromUrl(raw)).toEqual({ kind: 'add-server', connectionString: raw });
  });

  it.each([
    ['agentico://updates', { target: 'settings', settingsSection: 'updates' }],
    ['agentico://diagnostics', { target: 'settings', settingsSection: 'diagnostics' }],
    ['agentico://servers', { target: 'settings', settingsSection: 'servers' }],
    [
      'agentico://servers/add',
      { target: 'settings', settingsSection: 'servers', settingsFocus: 'add-server' },
    ],
  ] as const)('keeps the named route %s working', (raw, expected) => {
    expect(appRouteOf(routeFromUrl(raw))).toEqual(expected);
  });

  it('keeps the settings fallback for an unknown route', () => {
    expect(appRouteOf(routeFromUrl('agentico://something-else'))).toEqual({ target: 'settings' });
  });

  it('falls back to settings for a near-miss connection string (no port)', () => {
    expect(appRouteOf(routeFromUrl(`agentico://${TOKEN}@10.1.2.3`))).toEqual({
      target: 'settings',
    });
  });

  it('falls back to settings for a wildcard-host connection string', () => {
    expect(appRouteOf(routeFromUrl(`agentico://${TOKEN}@0.0.0.0:8080`))).toEqual({
      target: 'settings',
    });
  });

  it('returns null for foreign schemes and unparseable input', () => {
    expect(routeFromUrl('https://example.com')).toBeNull();
    expect(routeFromUrl('not a url')).toBeNull();
  });
});

describe('routeFromArgv', () => {
  it('maps the relaunch flag to the updates pane', () => {
    expect(routeFromArgv(['app', '--agentico-route=updates'])).toEqual({
      kind: 'app-route',
      event: { target: 'settings', settingsSection: 'updates' },
    });
  });

  it('routes an agentico:// argument through routeFromUrl', () => {
    expect(routeFromArgv(['app', CONNECTION_STRING])).toEqual({
      kind: 'add-server',
      connectionString: CONNECTION_STRING,
    });
  });

  it('returns null when no route is present', () => {
    expect(routeFromArgv(['app', '--flag'])).toBeNull();
  });
});

function makeDeps(overrides: Partial<AddServerLinkDeps> = {}): {
  deps: AddServerLinkDeps;
  routes: AppRouteEvent[];
  notifications: string[];
  logs: string[];
  switched: Array<{ serverKey: string }>;
} {
  const routes: AppRouteEvent[] = [];
  const notifications: string[] = [];
  const logs: string[] = [];
  const switched: Array<{ serverKey: string }> = [];
  const deps: AddServerLinkDeps = {
    addServer: vi.fn(async (): Promise<RemoteServerAddResult> => {
      return { status: 'added', serverKey: SERVER_KEY };
    }),
    switchServer: async (request) => {
      switched.push(request);
      return {};
    },
    route: (event) => {
      routes.push(event);
    },
    notify: (body) => {
      notifications.push(body);
    },
    log: (line) => {
      logs.push(line);
    },
    ...overrides,
  };
  return { deps, routes, notifications, logs, switched };
}

describe('addServerFromLink', () => {
  it('adds, switches to the new server, and routes to the Servers pane', async () => {
    const { deps, routes, notifications, switched } = makeDeps();
    await addServerFromLink(CONNECTION_STRING, deps);
    expect(deps.addServer).toHaveBeenCalledWith({ connectionString: CONNECTION_STRING });
    expect(switched).toEqual([{ serverKey: SERVER_KEY }]);
    expect(routes).toEqual([{ target: 'settings', settingsSection: 'servers' }]);
    expect(notifications).toEqual([]);
  });

  it('treats duplicate-local as success and switches to the existing entry', async () => {
    const { deps, routes, switched } = makeDeps({
      addServer: async () => ({ status: 'duplicate-local', serverKey: SERVER_KEY }),
    });
    await addServerFromLink(CONNECTION_STRING, deps);
    expect(switched).toEqual([{ serverKey: SERVER_KEY }]);
    expect(routes).toEqual([{ target: 'settings', settingsSection: 'servers' }]);
  });

  it('still switches on session-only and says nothing was saved', async () => {
    const { deps, notifications, switched } = makeDeps({
      addServer: async () => ({ status: 'session-only', serverKey: SERVER_KEY }),
    });
    await addServerFromLink(CONNECTION_STRING, deps);
    expect(switched).toEqual([{ serverKey: SERVER_KEY }]);
    expect(notifications).toEqual([
      'Server connected for this session only — the OS keychain is unavailable, so it was not saved.',
    ]);
  });

  it('routes a failed switch to the Servers pane anyway', async () => {
    const { deps, routes } = makeDeps({
      switchServer: async () => {
        throw new Error('switch failed');
      },
    });
    await addServerFromLink(CONNECTION_STRING, deps);
    expect(routes).toEqual([{ target: 'settings', settingsSection: 'servers' }]);
  });

  it('surfaces a pipeline failure on the add form with the pane error copy', async () => {
    const { deps, routes, notifications, switched } = makeDeps({
      addServer: async () => {
        throw new SafeErrorException(
          safeError('E_REMOTE_AUTH_REJECTED', 'The server rejected the token.'),
        );
      },
    });
    await addServerFromLink(CONNECTION_STRING, deps);
    expect(switched).toEqual([]);
    expect(routes).toEqual([
      { target: 'settings', settingsSection: 'servers', settingsFocus: 'add-server' },
    ]);
    expect(notifications).toEqual(['The token was rejected. The server rejected the token.']);
  });

  it('never leaks the link or token into logs, routes, or notifications', async () => {
    const failing = makeDeps({
      addServer: async () => {
        throw new SafeErrorException(
          safeError('E_REMOTE_UNREACHABLE', 'Could not reach the server.'),
        );
      },
    });
    await addServerFromLink(CONNECTION_STRING, failing.deps);
    const succeeding = makeDeps();
    await addServerFromLink(CONNECTION_STRING, succeeding.deps);
    for (const surface of [failing, succeeding]) {
      const serialized = JSON.stringify([surface.logs, surface.notifications, surface.routes]);
      expect(serialized).not.toContain(TOKEN);
      expect(serialized).not.toContain('agentico://');
    }
    expect(failing.logs).toEqual(['add-server link failed: E_REMOTE_UNREACHABLE']);
  });
});
