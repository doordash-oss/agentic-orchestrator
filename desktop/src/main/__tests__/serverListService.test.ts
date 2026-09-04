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
import type { RegistryScan } from '../gateway/registry';
import type { ServersPrefs } from '../../shared/ipc';
import { ServerListService, type ServerListServiceDeps } from '../gateway/serverListService';

const ALPHA_KEY = 'a'.repeat(32);
const BETA_KEY = 'b'.repeat(32);

function candidate(serverKey: string, name: string, baseUrl: string) {
  return {
    serverKey,
    runtimeDir: `/rt/${name}`,
    record: {
      schema_version: 1 as const,
      api_version: 'v1' as const,
      base_url: baseUrl,
      auth_token: `tok-${serverKey}`,
      name,
      runtime: {
        runtime_dir: `/rt/${name}`,
        state_dir: `/rt/${name}/features`,
        config_path: `/rt/${name}/config.yaml`,
      },
      pid: 4242,
      started_at: '2026-07-14T00:00:00Z',
    },
  };
}

function scan(...candidates: ReturnType<typeof candidate>[]): RegistryScan {
  return { candidates, pruned: 0, rejected: [] };
}

function known(serverKey: string, name: string, baseUrl: string) {
  return {
    serverKey,
    kind: 'local' as const,
    name,
    baseUrl,
    runtimeDir: `/rt/${name}`,
    lastSeenAt: '2026-07-14T00:00:00Z',
  };
}

function remoteKnown(serverKey: string, name: string, baseUrl: string, nickname?: string) {
  return {
    serverKey,
    kind: 'remote' as const,
    name,
    ...(nickname === undefined ? {} : { nickname }),
    baseUrl,
    lastSeenAt: '2026-07-14T00:00:00Z',
  };
}

interface Harness {
  service: ServerListService;
  snapshots: ReturnType<ServerListService['list']>[];
  pushed: ReturnType<ServerListService['list']>[];
  healthAnswers: Map<string, { status: number; body: unknown } | (() => Promise<never>)>;
  fetchCalls: string[];
  intervalFns: Array<() => void>;
  intervalCount(): number;
  prefs: ServersPrefs;
  /** Settings writes the locked name rule caused, in order. */
  probeRecords: Array<{ serverKey: string; patch: { name?: string; lastSeenAt?: string } }>;
}

function makeHarness(overrides: Partial<ServerListServiceDeps> = {}): Harness {
  const pushed: Harness['pushed'] = [];
  const fetchCalls: string[] = [];
  const intervalFns: Array<() => void> = [];
  const timers = new Map<unknown, () => void>();
  let nextHandle = 0;
  const harness = {} as Harness;
  harness.healthAnswers = new Map();
  harness.prefs = { known: [], lastUsed: null };
  harness.probeRecords = [];

  const deps: ServerListServiceDeps = {
    scanRegistry: () => scan(),
    knownServers: () => harness.prefs,
    currentServerKey: () => null,
    recordProbedServer: (serverKey, patch) => {
      harness.probeRecords.push({ serverKey, patch });
      // Mirror the real wiring: the patch lands in the persisted entry.
      harness.prefs = {
        known: harness.prefs.known.map((entry) =>
          entry.serverKey === serverKey ? { ...entry, ...patch } : entry,
        ),
        lastUsed: harness.prefs.lastUsed,
      };
    },
    fetchJson: async (url) => {
      fetchCalls.push(url);
      const answer = harness.healthAnswers.get(url);
      if (answer === undefined) throw new Error('connection refused');
      if (typeof answer === 'function') return answer();
      return answer;
    },
    log: () => {},
    setInterval: (fn) => {
      nextHandle += 1;
      const handle = { id: nextHandle };
      timers.set(handle, fn);
      intervalFns.push(fn);
      return handle;
    },
    clearInterval: (handle) => {
      timers.delete(handle);
    },
    pollIntervalMs: 5000,
    probeTimeoutMs: 100,
    ...overrides,
  };
  const service = new ServerListService(deps);
  service.subscribe((snapshot) => pushed.push(snapshot));
  Object.assign(harness, {
    service,
    pushed,
    fetchCalls,
    intervalFns,
    intervalCount: () => timers.size,
  });
  return harness;
}

const HEALTH = (base: string) => `${base}/api/v1/health`;
const OK = { status: 200, body: { status: 'ok' } };

describe('ServerListService list building', () => {
  it('unions registry and known servers, deduping by serverKey (registry wins)', () => {
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha-live', 'http://127.0.0.1:51001')),
    });
    harness.prefs = {
      known: [
        known(ALPHA_KEY, 'alpha-persisted', 'http://127.0.0.1:59999'),
        known(BETA_KEY, 'beta', 'http://127.0.0.1:51002'),
      ],
      lastUsed: ALPHA_KEY,
    };

    const { rows } = harness.service.list();
    expect(rows.map((row) => row.serverKey)).toEqual([ALPHA_KEY, BETA_KEY]);
    // The live registry record supplies name/dir; the persisted duplicate is dropped.
    expect(rows[0]).toMatchObject({ name: 'alpha-live', runtimeDir: '/rt/alpha-live' });
  });

  it('threads the persisted nickname onto both registry and known rows', () => {
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha-live', 'http://127.0.0.1:51001')),
    });
    harness.prefs = {
      known: [
        { ...known(ALPHA_KEY, 'alpha-persisted', 'http://127.0.0.1:59999'), nickname: 'preferred' },
        { ...known(BETA_KEY, 'beta', 'http://127.0.0.1:51002'), nickname: 'the other box' },
      ],
      lastUsed: null,
    };

    const { rows } = harness.service.list();
    expect(rows[0]).toMatchObject({
      serverKey: ALPHA_KEY,
      name: 'alpha-live',
      nickname: 'preferred',
    });
    expect(rows[1]).toMatchObject({
      serverKey: BETA_KEY,
      name: 'beta',
      nickname: 'the other box',
    });
  });

  it('flags exactly the connected server as current', () => {
    const harness = makeHarness({
      scanRegistry: () =>
        scan(
          candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001'),
          candidate(BETA_KEY, 'beta', 'http://127.0.0.1:51002'),
        ),
      currentServerKey: () => BETA_KEY,
    });
    const { rows } = harness.service.list();
    expect(rows.map((row) => row.current)).toEqual([false, true]);
  });
});

describe('ServerListService bundled runtime', () => {
  const BUNDLED_KEY = 'e'.repeat(32);
  const bundledRuntime = () => ({ serverKey: BUNDLED_KEY, runtimeDir: '/rt/bundled' });

  it('reports the bundled runtime as not running when the registry has no entry for it', () => {
    const harness = makeHarness({ bundledRuntime });
    expect(harness.service.list().bundled).toEqual({
      serverKey: BUNDLED_KEY,
      runtimeDir: '/rt/bundled',
      running: false,
    });
  });

  it('derives liveness from the registry scan, never from a probe', () => {
    const harness = makeHarness({
      bundledRuntime,
      scanRegistry: () => scan(candidate(BUNDLED_KEY, 'bundled', 'http://127.0.0.1:51001')),
    });
    const snapshot = harness.service.list();
    expect(snapshot.bundled?.running).toBe(true);
    expect(snapshot.rows.map((row) => row.serverKey)).toEqual([BUNDLED_KEY]);
    expect(harness.fetchCalls).toEqual([]);
  });

  it('omits the field entirely when no runtime selection is wired', () => {
    const harness = makeHarness();
    expect('bundled' in harness.service.list()).toBe(false);
  });
});

describe('ServerListService probing', () => {
  it('probes every listed server in parallel on open, using registry URLs first', async () => {
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001')),
    });
    harness.prefs = { known: [known(BETA_KEY, 'beta', 'http://127.0.0.1:51002')], lastUsed: null };
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), OK);
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51002'), { status: 500, body: {} });

    harness.service.setOpen(true);
    await vi.waitFor(() => {
      expect(harness.pushed.length).toBeGreaterThan(0);
    });

    expect(harness.fetchCalls.sort()).toEqual([
      HEALTH('http://127.0.0.1:51001'),
      HEALTH('http://127.0.0.1:51002'),
    ]);
    const rows = harness.pushed[harness.pushed.length - 1]!.rows;
    expect(rows.find((row) => row.serverKey === ALPHA_KEY)?.health).toBe('healthy');
    // HTTP failures and unreachable hosts both surface as unreachable rows —
    // nothing is hidden and nothing is deleted from settings.
    expect(rows.find((row) => row.serverKey === BETA_KEY)?.health).toBe('unreachable');
    expect(harness.prefs.known).toHaveLength(1);
  });

  it('never lets one hanging server block the others', async () => {
    const harness = makeHarness({
      scanRegistry: () =>
        scan(
          candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001'),
          candidate(BETA_KEY, 'beta', 'http://127.0.0.1:51002'),
        ),
    });
    let releaseHang: (value: never) => void = () => {};
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), OK);
    harness.healthAnswers.set(
      HEALTH('http://127.0.0.1:51002'),
      () =>
        new Promise<never>((resolve) => {
          releaseHang = resolve;
        }),
    );
    harness.service.setOpen(true);
    await vi.waitFor(() => {
      expect(harness.fetchCalls).toHaveLength(2);
    });
    // Alpha settles while beta still hangs: its row is emitted without waiting.
    await vi.waitFor(() => {
      const last = harness.pushed[harness.pushed.length - 1];
      expect(last).toBeDefined();
      expect(last!.rows.find((row) => row.serverKey === ALPHA_KEY)?.health).toBe('healthy');
    });
    releaseHang({ status: 200, body: { status: 'degraded' } } as never);
    // A non-ok status payload is not healthy.
    await vi.waitFor(() => {
      const rows = harness.pushed[harness.pushed.length - 1]!.rows;
      expect(rows.find((row) => row.serverKey === BETA_KEY)?.health).toBe('unreachable');
    });
    const rows = harness.pushed[harness.pushed.length - 1]!.rows;
    expect(rows.find((row) => row.serverKey === ALPHA_KEY)?.health).toBe('healthy');
  });

  it('polls on the interval only while open; closing stops every timer', async () => {
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001')),
    });
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), OK);

    expect(harness.intervalCount()).toBe(0);
    harness.service.setOpen(true);
    expect(harness.intervalCount()).toBe(1);
    await vi.waitFor(() => expect(harness.pushed).toHaveLength(1));
    expect(harness.fetchCalls).toHaveLength(1);

    harness.intervalFns[0]!();
    await vi.waitFor(() => expect(harness.fetchCalls).toHaveLength(2));

    harness.service.setOpen(false);
    expect(harness.intervalCount()).toBe(0);
    expect(harness.fetchCalls).toHaveLength(2);
  });

  it('reopening starts a fresh round without stacking intervals', async () => {
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001')),
    });
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), OK);
    harness.service.setOpen(true);
    harness.service.setOpen(true);
    expect(harness.intervalCount()).toBe(1);
    harness.service.setOpen(false);
    harness.service.setOpen(true);
    expect(harness.intervalCount()).toBe(1);
    harness.service.dispose();
    expect(harness.intervalCount()).toBe(0);
  });

  it('probes remote entries through their persisted base URL and marks their rows', async () => {
    const harness = makeHarness({});
    harness.prefs = {
      known: [
        remoteKnown(ALPHA_KEY, 'far-box', 'http://10.9.8.7:8080'),
        remoteKnown(BETA_KEY, 'dead-box', 'http://10.9.8.8:8080'),
      ],
      lastUsed: null,
    };
    harness.healthAnswers.set(HEALTH('http://10.9.8.7:8080'), {
      status: 200,
      body: { status: 'ok', name: 'far-box' },
    });

    harness.service.setOpen(true);
    await vi.waitFor(() => {
      const rows = harness.pushed[harness.pushed.length - 1]!.rows;
      expect(rows.find((row) => row.serverKey === ALPHA_KEY)?.health).toBe('healthy');
      expect(rows.find((row) => row.serverKey === BETA_KEY)?.health).toBe('unreachable');
    });

    const { rows } = harness.service.list();
    const alpha = rows.find((row) => row.serverKey === ALPHA_KEY);
    expect(alpha).toMatchObject({ kind: 'remote', name: 'far-box' });
    expect(alpha?.runtimeDir).toBeUndefined();
    // No local runtime dir and no URL ever crossed into a row.
    expect(JSON.stringify(rows)).not.toContain('http://');
  });

  it('refreshes the stored base name on probe success only when it changed', async () => {
    const harness = makeHarness({});
    harness.prefs = {
      known: [remoteKnown(ALPHA_KEY, 'far-box', 'http://10.9.8.7:8080')],
      lastUsed: null,
    };
    harness.healthAnswers.set(HEALTH('http://10.9.8.7:8080'), {
      status: 200,
      body: { status: 'ok', name: 'renamed-box' },
    });

    harness.service.setOpen(true);
    await vi.waitFor(() => expect(harness.probeRecords).toHaveLength(1));
    expect(harness.probeRecords[0]).toEqual({
      serverKey: ALPHA_KEY,
      patch: { name: 'renamed-box', lastSeenAt: expect.any(String) },
    });
    expect(harness.probeRecords[0]!.patch.lastSeenAt).not.toBe('2026-07-14T00:00:00Z');
    expect(harness.prefs.known[0]!.name).toBe('renamed-box');

    // A second round with the same name carries only the last-seen move —
    // the name write never storms.
    harness.probeRecords.length = 0;
    harness.intervalFns[0]!();
    await vi.waitFor(() => expect(harness.probeRecords).toHaveLength(1));
    expect(harness.probeRecords[0]!.patch.name).toBeUndefined();
    expect(harness.probeRecords[0]!.patch.lastSeenAt).toEqual(expect.any(String));
  });

  it('never clobbers a nickname and refreshes nothing when the name already matches', async () => {
    const harness = makeHarness({});
    harness.prefs = {
      known: [remoteKnown(ALPHA_KEY, 'original', 'http://10.9.8.7:8080', 'my box')],
      lastUsed: null,
    };
    harness.healthAnswers.set(HEALTH('http://10.9.8.7:8080'), {
      status: 200,
      body: { status: 'ok', name: 'probe-name' },
    });

    harness.service.setOpen(true);
    await vi.waitFor(() => expect(harness.probeRecords).toHaveLength(1));
    // The nickname locks the name write out; last-seen still moves.
    expect(harness.probeRecords[0]!.patch.name).toBeUndefined();
    expect(harness.probeRecords[0]!.patch.lastSeenAt).toEqual(expect.any(String));
    expect(harness.prefs.known[0]!.name).toBe('original');
    expect(harness.prefs.known[0]!.nickname).toBe('my box');
  });

  it('writes nothing for local entries on a matching probe and nothing on failure', async () => {
    const harness = makeHarness({});
    harness.prefs = {
      known: [
        known(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001'),
        remoteKnown(BETA_KEY, 'dead-box', 'http://10.9.8.8:8080'),
      ],
      lastUsed: null,
    };
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), {
      status: 200,
      body: { status: 'ok', name: 'alpha' },
    });

    harness.service.setOpen(true);
    await vi.waitFor(() => expect(harness.fetchCalls).toHaveLength(2));
    await vi.waitFor(() => {
      const rows = harness.pushed[harness.pushed.length - 1]!.rows;
      expect(rows.find((row) => row.serverKey === ALPHA_KEY)?.health).toBe('healthy');
      expect(rows.find((row) => row.serverKey === BETA_KEY)?.health).toBe('unreachable');
    });
    // Locals don't move last-seen from liveness probes (attach does), and a
    // dead remote yields no write at all.
    expect(harness.probeRecords).toEqual([]);
  });

  it('never leaks tokens or base URLs into rows or logs', async () => {
    const lines: string[] = [];
    const harness = makeHarness({
      scanRegistry: () => scan(candidate(ALPHA_KEY, 'alpha', 'http://127.0.0.1:51001')),
      log: (line) => lines.push(line),
    });
    harness.healthAnswers.set(HEALTH('http://127.0.0.1:51001'), OK);
    harness.service.setOpen(true);
    await vi.waitFor(() => expect(harness.pushed.length).toBeGreaterThan(0));

    for (const snapshot of harness.pushed) {
      for (const row of snapshot.rows) {
        expect(Object.keys(row).sort()).toEqual([
          'current',
          'health',
          'kind',
          'name',
          'runtimeDir',
          'serverKey',
        ]);
      }
    }
    expect(JSON.stringify(harness.pushed)).not.toContain('tok-');
    expect(JSON.stringify(harness.pushed)).not.toContain('http://');
    expect(JSON.stringify(lines)).not.toContain('tok-');
  });
});
