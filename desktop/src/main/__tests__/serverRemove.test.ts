import { describe, expect, it, vi } from 'vitest';
import { SafeErrorException } from '../../shared/errors';
import {
  applyServersPatch,
  type ConnectionState,
  type KnownServer,
  type ServersPrefs,
} from '../../shared/ipc';
import {
  E_SERVER_UNKNOWN,
  removeKnownServer,
  serverTokenStatus,
  type RemoveServerDeps,
  type ServerTokenStatusDeps,
} from '../gateway/removeServer';
import type { LoadResult } from '../gateway/remoteTokenStore';

const REMOTE_KEY = 'c'.repeat(32);
const LOCAL_KEY = 'd'.repeat(32);

const READY_LOCAL: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected.',
  ownership: 'app-owned',
  serverKey: LOCAL_KEY,
};

function remoteEntry(overrides: Partial<KnownServer> = {}): KnownServer {
  return {
    serverKey: REMOTE_KEY,
    kind: 'remote',
    name: 'far-box',
    baseUrl: 'http://10.1.2.3:8080',
    lastSeenAt: '2026-08-10T12:00:00.000Z',
    ...overrides,
  };
}

function localEntry(overrides: Partial<KnownServer> = {}): KnownServer {
  return {
    serverKey: LOCAL_KEY,
    kind: 'local',
    name: 'alpha',
    baseUrl: 'http://127.0.0.1:51001',
    runtimeDir: '/rt/alpha',
    lastSeenAt: '2026-08-10T12:00:00.000Z',
    ...overrides,
  };
}

interface Harness {
  prefs: ServersPrefs;
  removedTokens: string[];
  disconnectCalls: string[];
  logs: string[];
  disconnectResult: ConnectionState;
  run(serverKey: string): Promise<ConnectionState>;
}

function makeHarness(initial: ServersPrefs): Harness {
  const harness = {
    prefs: initial,
    removedTokens: [] as string[],
    disconnectCalls: [] as string[],
    logs: [] as string[],
    disconnectResult: READY_LOCAL,
  } as Harness;
  const deps: RemoveServerDeps = {
    knownServers: () => harness.prefs,
    removeRemoteToken: (serverKey) => {
      harness.removedTokens.push(serverKey);
    },
    // Mirrors index.ts: removeKnown plus last-used cleanup for the removed server.
    removeKnownEntry: (serverKey) => {
      harness.prefs = applyServersPatch(harness.prefs, {
        removeKnown: serverKey,
        ...(harness.prefs.lastUsed === serverKey ? { lastUsed: null } : {}),
      });
    },
    disconnectServer: (request) => {
      harness.disconnectCalls.push(request.serverKey);
      return Promise.resolve(harness.disconnectResult);
    },
    log: (line) => harness.logs.push(line),
  };
  harness.run = (serverKey: string) => removeKnownServer({ serverKey }, deps);
  return harness;
}

describe('removeKnownServer', () => {
  it('removes a remote server: blob deleted, entry dropped, teardown invoked', async () => {
    const harness = makeHarness({
      known: [remoteEntry(), localEntry()],
      lastUsed: LOCAL_KEY,
    });

    const state = await harness.run(REMOTE_KEY);

    expect(harness.removedTokens).toEqual([REMOTE_KEY]);
    expect(harness.prefs.known.map((entry) => entry.serverKey)).toEqual([LOCAL_KEY]);
    expect(harness.prefs.lastUsed).toBe(LOCAL_KEY);
    expect(harness.disconnectCalls).toEqual([REMOTE_KEY]);
    expect(state).toBe(READY_LOCAL);
  });

  it('removing the active server clears the last-used pointer before the teardown', async () => {
    const harness = makeHarness({ known: [remoteEntry()], lastUsed: REMOTE_KEY });

    await harness.run(REMOTE_KEY);

    expect(harness.prefs.known).toEqual([]);
    expect(harness.prefs.lastUsed).toBeNull();
    expect(harness.disconnectCalls).toEqual([REMOTE_KEY]);
  });

  it('removes a local server without touching the token store', async () => {
    const harness = makeHarness({ known: [remoteEntry(), localEntry()], lastUsed: LOCAL_KEY });

    await harness.run(LOCAL_KEY);

    expect(harness.removedTokens).toEqual([]);
    expect(harness.prefs.known.map((entry) => entry.serverKey)).toEqual([REMOTE_KEY]);
    expect(harness.disconnectCalls).toEqual([LOCAL_KEY]);
  });

  it('an unknown key fails closed: nothing is removed and no teardown runs', async () => {
    const harness = makeHarness({ known: [remoteEntry()], lastUsed: REMOTE_KEY });

    const error = await harness.run('e'.repeat(32)).catch((err: unknown) => err);

    expect(error).toBeInstanceOf(SafeErrorException);
    expect((error as SafeErrorException).safe.code).toBe(E_SERVER_UNKNOWN);
    expect(harness.removedTokens).toEqual([]);
    expect(harness.disconnectCalls).toEqual([]);
    expect(harness.prefs.known).toHaveLength(1);
  });

  it('logs only kind and a key prefix — never the URL or any credential', async () => {
    const harness = makeHarness({ known: [remoteEntry()], lastUsed: REMOTE_KEY });

    await harness.run(REMOTE_KEY);

    const surface = JSON.stringify(harness.logs);
    expect(surface).not.toContain('10.1.2.3');
    expect(surface).not.toContain('http');
    expect(harness.logs.some((line) => line.includes('remote'))).toBe(true);
  });
});

function tokenStatusHarness(
  load: LoadResult,
  entry: KnownServer | null,
): {
  run(serverKey: string): unknown;
} {
  const prefs: ServersPrefs = { known: entry === null ? [] : [entry], lastUsed: null };
  const loads = vi.fn((_: string) => load);
  const deps: ServerTokenStatusDeps = {
    knownServers: () => prefs,
    loadRemoteToken: loads,
  };
  return { run: (serverKey: string) => serverTokenStatus({ serverKey }, deps) };
}

describe('serverTokenStatus', () => {
  it('reports local entries as local without consulting the token store', () => {
    const harness = tokenStatusHarness({ status: 'ok', token: 'tok' }, localEntry());
    expect(harness.run(LOCAL_KEY)).toEqual({ status: 'local' });
  });

  it('reports remote entries per blob health', () => {
    expect(
      tokenStatusHarness({ status: 'ok', token: 'tok' }, remoteEntry()).run(REMOTE_KEY),
    ).toEqual({ status: 'saved' });
    expect(
      tokenStatusHarness({ status: 're-paste-required' }, remoteEntry()).run(REMOTE_KEY),
    ).toEqual({ status: 're-paste-required' });
    expect(tokenStatusHarness({ status: 'absent' }, remoteEntry()).run(REMOTE_KEY)).toEqual({
      status: 'session-only',
    });
  });

  it('an unknown key throws E_SERVER_UNKNOWN', () => {
    expect(() => tokenStatusHarness({ status: 'absent' }, null).run(REMOTE_KEY)).toThrowError(
      SafeErrorException,
    );
  });
});
