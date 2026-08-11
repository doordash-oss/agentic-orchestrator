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
    name,
    baseUrl,
    runtimeDir: `/rt/${name}`,
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

  const deps: ServerListServiceDeps = {
    scanRegistry: () => scan(),
    knownServers: () => harness.prefs,
    currentServerKey: () => null,
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
