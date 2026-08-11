import { describe, expect, it, vi } from 'vitest';
import {
  ConnectionStateSchema,
  MAX_KNOWN_SERVERS,
  type ConnectionState,
  type KnownServer,
  type ServersPrefs,
} from '../../shared/ipc';
import type { SafeError } from '../../shared/errors';
import { serverKeyForBaseUrl } from '../connectionString';
import type { RegistryScan } from '../gateway/registry';
import type { LoadResult } from '../gateway/remoteTokenStore';
import type { ChildExit } from '../gateway/serverProcess';
import {
  RuntimeGateway,
  type GatewayDeps,
  type GatewayTimeouts,
  type SelectedRuntime,
} from '../gateway/runtimeGateway';

const EMPTY_SCAN: RegistryScan = { candidates: [], pruned: 0, rejected: [] };

const SELECTED: SelectedRuntime = {
  runtimeDir: '/home/ü ser/.agentic-orchestrator',
  stateDir: '/home/ü ser/.agentic-orchestrator/features',
  configPath: '/home/ü ser/.agentic-orchestrator/config.yaml',
};

const ALPHA_RUNTIME_DIR = '/srv/runtimes/alpha';
const ALPHA_KEY = 'a'.repeat(32);
const ALPHA_BASE = 'http://127.0.0.1:51001';
const ALPHA_TOKEN = 'tok-alpha-secret-aaa';
const LAUNCH_BASE = 'http://127.0.0.1:50505';
const LAUNCH_TOKEN = 'tok-launched-secret-def';

const REMOTE_BASE = 'http://10.9.8.7:8080';
const REMOTE_TOKEN = 'tok-remote-secret-zzz';

function remoteKey(): string {
  return serverKeyForBaseUrl(REMOTE_BASE);
}

function compatibleDeclaration(): Record<string, unknown> {
  return {
    api_version: 'v1',
    schema_version: 1,
    min_client_schema: 1,
    runtime_policy: 'loopback-bearer-v1',
    server_build: { version: 'v9.9.9-remote', revision: 'deadbeef' },
  };
}

function healthBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    status: 'ok',
    runtime: {
      runtime_dir: SELECTED.runtimeDir,
      state_dir: SELECTED.stateDir,
      config_path: SELECTED.configPath,
    },
    server_time: '2026-07-14T00:00:01Z',
    compatibility: compatibleDeclaration(),
    ...overrides,
  };
}

function healthFor(runtimeDir: string, name?: string): Record<string, unknown> {
  return healthBody({
    runtime: {
      runtime_dir: runtimeDir,
      state_dir: `${runtimeDir}/features`,
      config_path: `${runtimeDir}/config.yaml`,
    },
    ...(name === undefined ? {} : { name }),
  });
}

function remoteEntry(overrides: Partial<KnownServer> = {}): KnownServer {
  return {
    serverKey: remoteKey(),
    kind: 'remote',
    name: 'far-box',
    baseUrl: REMOTE_BASE,
    lastSeenAt: '2026-07-14T00:00:00Z',
    ...overrides,
  };
}

function alphaScan(): RegistryScan {
  return {
    candidates: [
      {
        serverKey: ALPHA_KEY,
        runtimeDir: ALPHA_RUNTIME_DIR,
        record: {
          schema_version: 1,
          api_version: 'v1',
          base_url: ALPHA_BASE,
          auth_token: ALPHA_TOKEN,
          runtime: {
            runtime_dir: ALPHA_RUNTIME_DIR,
            state_dir: `${ALPHA_RUNTIME_DIR}/features`,
            config_path: `${ALPHA_RUNTIME_DIR}/config.yaml`,
          },
          pid: 4242,
        },
      },
    ],
    pruned: 0,
    rejected: [],
  };
}

class FakeChild {
  pid: number | undefined = 777;
  exited = false;
  stopCalls: Array<{ timeoutMs?: number }> = [];
  private readonly listeners = new Set<(info: ChildExit) => void>();

  onExit(listener: (info: ChildExit) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  async stop(options: { timeoutMs?: number } = {}): Promise<void> {
    this.stopCalls.push(options);
    this.exited = true;
    for (const listener of [...this.listeners]) {
      listener({ code: 0, signal: null, expected: true });
    }
  }
}

/** In-memory RemoteTokenStore stand-in with a scriptable load result. */
class FakeRemoteTokens {
  private readonly saved = new Map<string, string>();
  /** When set, every load reports this outcome instead of the saved map. */
  forcedStatus: 'absent' | 're-paste-required' | null = null;
  loads: string[] = [];

  constructor(private readonly register: (secret: string) => void) {}

  save(serverKey: string, token: string): void {
    this.register(token);
    this.saved.set(serverKey, token);
  }

  load(serverKey: string): LoadResult {
    this.loads.push(serverKey);
    if (this.forcedStatus !== null) {
      return { status: this.forcedStatus };
    }
    const token = this.saved.get(serverKey);
    if (token === undefined) {
      return { status: 'absent' };
    }
    this.register(token);
    return { status: 'ok', token };
  }
}

interface Env {
  deps: GatewayDeps;
  gateway: RuntimeGateway;
  states: ConnectionState[];
  logs: string[];
  secrets: string[];
  spawnCalls: number;
  spawned: FakeChild[];
  fetchCalls: Array<{ url: string; token?: string; timeoutMs: number }>;
  attachRecords: KnownServer[];
  servers(): ServersPrefs;
  updateServers(patch: { upsertKnown?: KnownServer; lastUsed?: string | null }): void;
  /** Mirrors the removal path: entry deleted, last-used pointer dropped with it. */
  removeServersEntry(serverKey: string): void;
  tokens: FakeRemoteTokens;
  setTokenMode(status: 'absent' | 're-paste-required' | null): void;
  /** Resolve handlers handed to the fake clock; resolving advances the re-probe. */
  pendingSleeps: Array<() => void>;
}

interface EnvOptions {
  serversPrefs?: ServersPrefs;
  remoteHealth?: Record<string, unknown> | Error;
  health?: Record<string, Record<string, unknown> | Error>;
  readinessErrors?: Record<string, boolean>;
  discovery?: string | null;
  registryScan?: () => RegistryScan;
  timeouts?: Partial<GatewayTimeouts>;
}

function makeEnv(options: EnvOptions = {}): Env {
  let serversPrefs: ServersPrefs = options.serversPrefs ?? { known: [], lastUsed: null };
  const health: Record<string, Record<string, unknown> | Error> = options.health ?? {
    [ALPHA_BASE]: healthFor(ALPHA_RUNTIME_DIR, 'alpha'),
    [REMOTE_BASE]: options.remoteHealth ?? healthFor('remote', 'remote'),
    [LAUNCH_BASE]: healthBody({ owner: { pid: 777, started_at: '2026-07-14T00:00:02Z' } }),
  };
  const readinessErrors = options.readinessErrors ?? {};
  let discovery = options.discovery ?? null;

  const env = {} as Env;

  const deps: GatewayDeps = {
    selectRuntime: () => SELECTED,
    discovery: {
      readFile: () => {
        if (discovery === null) {
          throw new Error('ENOENT');
        }
        return discovery;
      },
      statFile: () => (discovery === null ? null : { mode: 0o100600, uid: 501 }),
      euid: 501,
      isProcessAlive: () => true,
    },
    fetchJson: async (url, opts) => {
      env.fetchCalls.push(
        opts.token === undefined
          ? { url, timeoutMs: opts.timeoutMs }
          : { url, token: opts.token, timeoutMs: opts.timeoutMs },
      );
      const base = url.replace(/\/api\/v1\/.*$/, '');
      if (url.endsWith('/api/v1/health')) {
        const entry = health[base];
        if (entry === undefined || entry instanceof Error) {
          throw entry ?? new Error('connection refused');
        }
        return { status: 200, body: entry };
      }
      if (url.endsWith('/api/v1/readiness')) {
        if (readinessErrors[base] === true) {
          return { status: 401, body: { code: 'unauthorized' } };
        }
        if (opts.token !== undefined) {
          return { status: 200, body: { api_version: 'v1' } };
        }
        return { status: 401, body: { code: 'unauthorized' } };
      }
      throw new Error(`unexpected url ${url}`);
    },
    resolveServerBinary: () => ({ ok: true, path: '/rés dir/bin/agentico' }),
    spawnServer: (() => {
      const child = new FakeChild();
      env.spawned.push(child);
      env.spawnCalls += 1;
      // A real child publishes its own discovery record; mirror that so the
      // launch path re-reads the spawned identity and token.
      health[LAUNCH_BASE] = healthBody({
        owner: { pid: child.pid, started_at: '2026-07-14T00:00:02Z' },
      });
      discovery = JSON.stringify({
        schema_version: 1,
        api_version: 'v1',
        base_url: LAUNCH_BASE,
        auth_token: LAUNCH_TOKEN,
        runtime: {
          runtime_dir: SELECTED.runtimeDir,
          state_dir: SELECTED.stateDir,
          config_path: SELECTED.configPath,
        },
        pid: child.pid,
        started_at: '2026-07-14T00:00:02Z',
      });
      return child;
    }) as GatewayDeps['spawnServer'],
    registerSecret: (secret) => {
      env.secrets.push(secret);
    },
    sleep: () =>
      new Promise((resolve) => {
        env.pendingSleeps.push(resolve);
      }),
    log: (line) => {
      env.logs.push(line);
    },
    scanRegistry: options.registryScan ?? (() => EMPTY_SCAN),
    knownServers: () => serversPrefs,
    recordAttachedServer: (entry) => {
      env.attachRecords.push(entry);
      serversPrefs = {
        known: [entry, ...serversPrefs.known.filter((k) => k.serverKey !== entry.serverKey)].slice(
          0,
          MAX_KNOWN_SERVERS,
        ),
        lastUsed: entry.serverKey,
      };
    },
    remoteTokens: {
      load: (serverKey) => env.tokens.load(serverKey),
    },
    timeouts: { pollIntervalMs: 1, launchReadyMs: 20, shutdownGraceMs: 50, ...options.timeouts },
  };

  Object.assign(env, {
    deps,
    gateway: new RuntimeGateway(deps),
    states: [],
    logs: [],
    secrets: [],
    spawnCalls: 0,
    spawned: [],
    fetchCalls: [],
    attachRecords: [],
    servers: () => serversPrefs,
    updateServers: (patch: { upsertKnown?: KnownServer; lastUsed?: string | null }) => {
      if (patch.upsertKnown !== undefined) {
        const entry = patch.upsertKnown;
        serversPrefs = {
          known: [entry, ...serversPrefs.known.filter((k) => k.serverKey !== entry.serverKey)],
          lastUsed: serversPrefs.lastUsed,
        };
      }
      if (patch.lastUsed !== undefined) {
        serversPrefs = { known: serversPrefs.known, lastUsed: patch.lastUsed };
      }
    },
    removeServersEntry: (serverKey: string) => {
      serversPrefs = {
        known: serversPrefs.known.filter((entry) => entry.serverKey !== serverKey),
        lastUsed: serversPrefs.lastUsed === serverKey ? null : serversPrefs.lastUsed,
      };
    },
    tokens: undefined as unknown as FakeRemoteTokens,
    setTokenMode: (status: 'absent' | 're-paste-required' | null) => {
      env.tokens.forcedStatus = status;
    },
    pendingSleeps: [],
  });
  env.tokens = new FakeRemoteTokens((secret) => {
    env.secrets.push(secret);
  });
  env.gateway.subscribe((state) => env.states.push(state));
  return env;
}

function requireError(state: ConnectionState): SafeError {
  if (state.status !== 'error' && state.status !== 'incompatible') {
    throw new Error(`expected a failure state, got ${state.status}`);
  }
  return state.error;
}

function expectNoTokenLeak(env: Env): void {
  for (const state of env.states) {
    ConnectionStateSchema.parse(state);
  }
  const surface = JSON.stringify(env.states) + JSON.stringify(env.logs);
  expect(surface).not.toContain(REMOTE_TOKEN);
  expect(surface).not.toContain(ALPHA_TOKEN);
}

describe('RuntimeGateway remote attach profile', () => {
  it('attaches to a remote last-used entry without registry, discovery, PID checks, or spawn', async () => {
    const scanRegistry = vi.fn(() => EMPTY_SCAN);
    const readFile = vi.fn(() => {
      throw new Error('ENOENT');
    });
    const isProcessAlive = vi.fn(() => true);
    const spawnServer = vi.fn(() => new FakeChild());
    const env = makeEnv({ registryScan: scanRegistry });
    env.deps.discovery.readFile = readFile;
    env.deps.discovery.isProcessAlive = isProcessAlive;
    env.deps.spawnServer = spawnServer as unknown as GatewayDeps['spawnServer'];
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    env.secrets.length = 0; // discount the save-time registration

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state).toMatchObject({
      status: 'ready',
      ownership: 'external',
      kind: 'remote',
      serverKey: remoteKey(),
    });
    expect(scanRegistry).not.toHaveBeenCalled();
    expect(readFile).not.toHaveBeenCalled();
    expect(isProcessAlive).not.toHaveBeenCalled();
    expect(spawnServer).not.toHaveBeenCalled();
    expect(env.spawnCalls).toBe(0);
    // The token store registered the secret itself; no double registration.
    expect(env.secrets.filter((s) => s === REMOTE_TOKEN)).toHaveLength(1);
    // lastSeenAt refreshed + last-used pointer moved.
    expect(env.attachRecords).toHaveLength(1);
    expect(env.attachRecords[0]!.kind).toBe('remote');
    expect(env.attachRecords[0]!.lastSeenAt).not.toBe('2026-07-14T00:00:00Z');
    expect(env.servers().lastUsed).toBe(remoteKey());
    expectNoTokenLeak(env);
  });

  it('an incompatible remote lands in the incompatible state', async () => {
    const env = makeEnv({});
    env.deps.fetchJson = async (url) => {
      if (url.endsWith('/api/v1/health')) {
        return {
          status: 200,
          body: { ...healthFor('remote'), compatibility: { api_version: 'v9' } },
        };
      }
      throw new Error(`unexpected url ${url}`);
    };
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('incompatible');
    expect(requireError(state).code).toBe('E_INCOMPATIBLE_SERVER');
    expect(env.spawnCalls).toBe(0);
  });

  it('an unreachable remote lands in E_EXTERNAL_SERVER_LOST with retry remediation and no spawn', async () => {
    const env = makeEnv({ remoteHealth: new Error('connection refused') });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    const error = requireError(state);
    expect(error.code).toBe('E_EXTERNAL_SERVER_LOST');
    expect(error.remediation ?? '').toContain('Retry');
    expect(env.spawnCalls).toBe(0);
    expectNoTokenLeak(env);
  });

  it('absent token lands in E_REMOTE_TOKEN_REPASTE; after save + retry the attach succeeds', async () => {
    const env = makeEnv({});
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });

    await env.gateway.start();

    const failed = env.gateway.getState();
    expect(failed.status).toBe('error');
    const error = requireError(failed);
    expect(error.code).toBe('E_REMOTE_TOKEN_REPASTE');
    expect(error.remediation ?? '').toContain('Re-enter the remote server token');
    expect(env.spawnCalls).toBe(0);

    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    const retried = await env.gateway.retry();
    expect(retried.status).toBe('ready');
    expect(retried.serverKey).toBe(remoteKey());
    expectNoTokenLeak(env);
  });

  it('re-paste-required token lands in E_REMOTE_TOKEN_REPASTE and never spawns', async () => {
    const env = makeEnv({});
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    env.setTokenMode('re-paste-required');

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    expect(requireError(state).code).toBe('E_REMOTE_TOKEN_REPASTE');
    expect(env.spawnCalls).toBe(0);
  });

  it('a remote rejecting the stored token lands in E_REMOTE_TOKEN_REPASTE', async () => {
    const env = makeEnv({ readinessErrors: { [REMOTE_BASE]: true } });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    const error = requireError(state);
    expect(error.code).toBe('E_REMOTE_TOKEN_REPASTE');
    expect(error.remediation ?? '').toContain('Re-enter the remote server token');
    // The rejected token was still registered for redaction by the store.
    expect(env.secrets).toContain(REMOTE_TOKEN);
    expectNoTokenLeak(env);
  });

  it('a successful attach refreshes the stored name only when no nickname is set', async () => {
    const env = makeEnv({ remoteHealth: healthFor('remote', 'probe-name') });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.attachRecords[0]!.name).toBe('probe-name');

    const withNick = makeEnv({ remoteHealth: healthFor('remote', 'probe-name') });
    withNick.updateServers({
      upsertKnown: remoteEntry({ name: 'original', nickname: 'my box' }),
      lastUsed: remoteKey(),
    });
    withNick.tokens.save(remoteKey(), REMOTE_TOKEN);
    await withNick.gateway.start();
    expect(withNick.gateway.getState().status).toBe('ready');
    expect(withNick.attachRecords[0]!.name).toBe('original');
    expect(withNick.attachRecords[0]!.nickname).toBe('my box');
    expectNoTokenLeak(withNick);
  });
});

describe('RuntimeGateway remote loss and re-probe', () => {
  async function startRemote(env: Env): Promise<void> {
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
  }

  function pumpSleep(env: Env): void {
    for (const resolve of env.pendingSleeps.splice(0)) {
      resolve();
    }
  }

  async function flush(env: Env): Promise<void> {
    // Run pending microtasks and any newly queued fake sleeps to completion.
    for (let i = 0; i < 50; i += 1) {
      await Promise.resolve();
      pumpSleep(env);
    }
  }

  it('remote loss lands in E_EXTERNAL_SERVER_LOST with retry remediation; no spawn, no crash loop', async () => {
    const env = makeEnv({});
    await startRemote(env);
    const realFetch = env.deps.fetchJson;
    env.deps.fetchJson = async (url, opts) => {
      if (url.startsWith(REMOTE_BASE) && url.endsWith('/api/v1/health')) {
        throw new Error('connection refused');
      }
      return realFetch(url, opts);
    };

    await env.gateway.handleGlobalStreamStale();

    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    const error = requireError(state);
    expect(error.code).toBe('E_EXTERNAL_SERVER_LOST');
    expect(error.remediation ?? '').toContain('Retry');
    expect(state.ownership).toBe('external');
    expect(env.spawnCalls).toBe(0);
    expect(env.states.some((s) => s.status === 'crashed')).toBe(false);
  });

  it('the background re-probe reconnects once the remote is healthy again', async () => {
    const env = makeEnv({});
    await startRemote(env);

    let remoteDown = true;
    const realFetch = env.deps.fetchJson;
    env.deps.fetchJson = async (url, opts) => {
      if (remoteDown && url.startsWith(REMOTE_BASE) && url.endsWith('/api/v1/health')) {
        throw new Error('connection refused');
      }
      return realFetch(url, opts);
    };

    await env.gateway.handleGlobalStreamStale();
    expect(env.gateway.getState().status).toBe('error');
    expect(requireError(env.gateway.getState()).code).toBe('E_EXTERNAL_SERVER_LOST');

    // First re-probe: server still down, state unchanged.
    pumpSleep(env);
    for (let i = 0; i < 10; i += 1) {
      await Promise.resolve();
    }
    expect(env.gateway.getState().status).toBe('error');

    // Server recovers; the next probe re-attaches with the stored token.
    remoteDown = false;
    pumpSleep(env);
    await flush(env);

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.ownership).toBe('external');
    expect(state.serverKey).toBe(remoteKey());
    expect(env.spawnCalls).toBe(0);
    // No crash restart was ever attempted for the remote.
    expect(env.logs.some((line) => line.includes('relaunched'))).toBe(false);
    expectNoTokenLeak(env);
  });

  it('a switch away from the lost remote quiesces the re-probe', async () => {
    const env = makeEnv({});
    await startRemote(env);
    const realFetch = env.deps.fetchJson;
    env.deps.fetchJson = async (url, opts) => {
      if (url.startsWith(REMOTE_BASE) && url.endsWith('/api/v1/health')) {
        throw new Error('connection refused');
      }
      return realFetch(url, opts);
    };

    await env.gateway.handleGlobalStreamStale();
    expect(env.gateway.getState().status).toBe('error');

    env.deps.scanRegistry = () => alphaScan();
    env.updateServers({
      upsertKnown: {
        serverKey: ALPHA_KEY,
        kind: 'local',
        name: 'alpha',
        baseUrl: ALPHA_BASE,
        runtimeDir: ALPHA_RUNTIME_DIR,
        lastSeenAt: '2026-07-14T00:00:00Z',
      },
    });
    const switched = await env.gateway.switchServer({ serverKey: ALPHA_KEY });
    expect(switched.status).toBe('ready');
    expect(switched.serverKey).toBe(ALPHA_KEY);

    const before = env.states.length;
    await flush(env);
    // The fenced re-probe emitted nothing.
    expect(env.states.length).toBe(before);
    expect(env.gateway.getState().serverKey).toBe(ALPHA_KEY);
  });
});

describe('RuntimeGateway remote switching', () => {
  it('local→remote→local switches restore the per-server context', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().connectedRuntimeDir).toBe(ALPHA_RUNTIME_DIR);

    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    const remote = await env.gateway.switchServer({ serverKey: remoteKey() });
    expect(remote.status).toBe('ready');
    expect(remote.ownership).toBe('external');
    expect(remote.serverKey).toBe(remoteKey());
    expect(env.servers().lastUsed).toBe(remoteKey());

    const back = await env.gateway.switchServer({ serverKey: ALPHA_KEY });
    expect(back.status).toBe('ready');
    expect(back.serverKey).toBe(ALPHA_KEY);
    expect(back.connectedRuntimeDir).toBe(ALPHA_RUNTIME_DIR);
    expect(env.servers().lastUsed).toBe(ALPHA_KEY);
    expect(env.spawnCalls).toBe(0);
    expectNoTokenLeak(env);
  });

  it('the app-owned child survives a switch to a remote and is stopped on shutdown', async () => {
    // Start with no servers at all: the bundled runtime spawns.
    const env = makeEnv({
      discovery: JSON.stringify({
        schema_version: 1,
        api_version: 'v1',
        base_url: LAUNCH_BASE,
        auth_token: LAUNCH_TOKEN,
        runtime: {
          runtime_dir: SELECTED.runtimeDir,
          state_dir: SELECTED.stateDir,
          config_path: SELECTED.configPath,
        },
        // Stale pid: startup treats the discovery record as dead and spawns.
        pid: 999999,
        started_at: '2026-07-14T00:00:02Z',
      }),
    });
    // The fake treats every pid as alive by default; make the stale one dead.
    env.deps.discovery.isProcessAlive = (pid) => pid !== 999999;
    // The queued-sleep fake is for re-probe control; the launch poll needs a
    // self-resolving sleep or the readiness wait never advances.
    env.deps.sleep = () => Promise.resolve();
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toBe(1);
    const child = env.spawned[0]!;

    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    const remote = await env.gateway.switchServer({ serverKey: remoteKey() });
    expect(remote.status).toBe('ready');
    expect(remote.serverKey).toBe(remoteKey());
    // The left-behind child was never stopped or signalled.
    expect(child.stopCalls).toHaveLength(0);
    expect(child.exited).toBe(false);

    await env.gateway.shutdown();
    expect(child.stopCalls).toHaveLength(1);
  });

  it('generation fencing: a restart mid-remote-attach cancels the in-flight attach', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    let releaseHealth: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      releaseHealth = resolve;
    });
    const realFetch = env.deps.fetchJson;
    env.deps.fetchJson = async (url, opts) => {
      if (url.startsWith(REMOTE_BASE) && url.endsWith('/api/v1/health')) {
        await gate;
      }
      return realFetch(url, opts);
    };

    const switching = env.gateway.switchServer({ serverKey: remoteKey() });
    await Promise.resolve();
    // Restart bumps the generation; the gated remote attach must be fenced.
    await env.gateway.restart();
    releaseHealth();
    await switching;

    // The fenced remote attach never reached ready; restart reset to idle and
    // its start() was fenced while the in-flight switch still held busy.
    const state = env.gateway.getState();
    expect(state.status).toBe('idle');
    expect(
      env.states.filter((s) => s.status === 'ready' && s.serverKey === remoteKey()),
    ).toHaveLength(0);

    // A fresh start after the generation bump attaches to the local server.
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().serverKey).toBe(ALPHA_KEY);
    expectNoTokenLeak(env);
  });

  it('stream cursors reset per server: each attach opens streams against its own base', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    await env.gateway.start();
    const opens: string[] = [];
    env.deps.openSse = async (url) => {
      opens.push(url);
      return {
        status: 200,
        lines: (async function* () {
          // No events in this test.
        })(),
        close: () => {},
      };
    };

    await env.gateway.openEventStream({ afterSeq: 5, epoch: 'ep-alpha' });
    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    await env.gateway.switchServer({ serverKey: remoteKey() });
    await env.gateway.openEventStream();

    expect(opens[0]).toBe(`${ALPHA_BASE}/api/v1/events?after=5&epoch=ep-alpha`);
    expect(opens[1]).toBe(`${REMOTE_BASE}/api/v1/events`);
    expectNoTokenLeak(env);
  });

  it('disconnectServer on the active remote re-enters selection and attaches to a live local', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().serverKey).toBe(remoteKey());

    // The Servers pane's removal path updates settings (entry and last-used
    // pointer gone) before the teardown.
    env.removeServersEntry(remoteKey());
    const after = await env.gateway.disconnectServer({ serverKey: remoteKey() });

    expect(after.status).toBe('ready');
    expect(after.serverKey).toBe(ALPHA_KEY);
    expect(after.ownership).toBe('external');
    // The removal spawned nothing: a live local server was attached instead.
    expect(env.spawnCalls).toBe(0);
  });

  it('disconnectServer spawns the local runtime when nothing else is live', async () => {
    const env = makeEnv({});
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');

    env.removeServersEntry(remoteKey());
    const after = await env.gateway.disconnectServer({ serverKey: remoteKey() });

    expect(after.status).toBe('ready');
    expect(after.ownership).toBe('app-owned');
    expect(env.spawnCalls).toBe(1);
  });

  it('disconnectServer for a server that is not the active connection is a no-op', async () => {
    const env = makeEnv({});
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();
    expect(env.gateway.getState().serverKey).toBe(remoteKey());

    const fetchCount = env.fetchCalls.length;
    const after = await env.gateway.disconnectServer({ serverKey: ALPHA_KEY });
    expect(after.status).toBe('ready');
    expect(after.serverKey).toBe(remoteKey());
    expect(env.fetchCalls.length).toBe(fetchCount);
    expectNoTokenLeak(env);
  });

  it('a dead last-used remote with others live lands in the picker listing remote entries', async () => {
    const env = makeEnv({
      registryScan: () => alphaScan(),
      remoteHealth: new Error('connection refused'),
    });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('awaiting-server-choice');
    if (state.status !== 'awaiting-server-choice') {
      throw new Error('unreachable');
    }
    expect(state.candidates).toEqual([
      {
        kind: 'local',
        serverKey: ALPHA_KEY,
        name: null,
        runtimeDir: ALPHA_RUNTIME_DIR,
      },
      { kind: 'remote', serverKey: remoteKey(), name: 'far-box', health: 'unreachable' },
    ]);
    expect(env.spawnCalls).toBe(0);

    // The dead remote stays pickable-until-attach: the healthy local wins.
    const chosen = await env.gateway.chooseServer({ serverKey: ALPHA_KEY });
    expect(chosen.status).toBe('ready');
    expect(chosen.serverKey).toBe(ALPHA_KEY);
    expect(env.spawnCalls).toBe(0);
    expectNoTokenLeak(env);
  });

  it('a dead last-used remote alone keeps the E_EXTERNAL_SERVER_LOST surface (no 1-row picker)', async () => {
    const env = makeEnv({ remoteHealth: new Error('connection refused') });
    env.updateServers({ upsertKnown: remoteEntry(), lastUsed: remoteKey() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    expect(requireError(state).code).toBe('E_EXTERNAL_SERVER_LOST');
    expect(env.states.some((s) => s.status === 'awaiting-server-choice')).toBe(false);
    expect(env.spawnCalls).toBe(0);
  });

  it('the multi-local picker also lists remote entries with kind and probe health', async () => {
    const betaScan = alphaScan();
    betaScan.candidates.push({
      serverKey: 'b'.repeat(32),
      runtimeDir: '/srv/runtimes/beta',
      record: {
        schema_version: 1,
        api_version: 'v1',
        base_url: 'http://127.0.0.1:51002',
        auth_token: 'tok-beta-secret-bbb',
        runtime: {
          runtime_dir: '/srv/runtimes/beta',
          state_dir: '/srv/runtimes/beta/features',
          config_path: '/srv/runtimes/beta/config.yaml',
        },
        pid: 4243,
      },
    });
    const DEAD_BASE = 'http://10.9.8.9:8080';
    const deadKey = serverKeyForBaseUrl(DEAD_BASE);
    const env = makeEnv({ registryScan: () => betaScan });
    env.updateServers({
      upsertKnown: remoteEntry({ nickname: 'my box' }),
    });
    env.updateServers({
      upsertKnown: { ...remoteEntry(), serverKey: deadKey, name: 'dead-box', baseUrl: DEAD_BASE },
    });

    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('awaiting-server-choice');
    if (state.status !== 'awaiting-server-choice') {
      throw new Error('unreachable');
    }
    ConnectionStateSchema.parse(state);
    const remote = state.candidates.find(
      (candidate) => candidate.kind === 'remote' && candidate.serverKey === remoteKey(),
    );
    // Locals come from the live scan: no probe, no health verdict carried.
    expect(state.candidates.filter((c) => c.kind === 'local')).toEqual([
      { kind: 'local', serverKey: ALPHA_KEY, name: null, runtimeDir: ALPHA_RUNTIME_DIR },
      { kind: 'local', serverKey: 'b'.repeat(32), name: null, runtimeDir: '/srv/runtimes/beta' },
    ]);
    expect(remote).toMatchObject({
      serverKey: remoteKey(),
      health: 'healthy',
      // The nickname wins over the stored base name.
      name: 'my box',
    });
    expect(remote && 'runtimeDir' in remote).toBe(false);
    const dead = state.candidates.find((candidate) => candidate.serverKey === deadKey);
    expect(dead).toMatchObject({ kind: 'remote', health: 'unreachable', name: 'dead-box' });
    // The snapshot carries no base URLs, ever.
    expect(JSON.stringify(state)).not.toContain('http://');
    expect(env.spawnCalls).toBe(0);
    expectNoTokenLeak(env);
  });

  it('chooseServer resolves a remote key from settings when it is not in the snapshot', async () => {
    const betaScan = alphaScan();
    betaScan.candidates.push({
      serverKey: 'b'.repeat(32),
      runtimeDir: '/srv/runtimes/beta',
      record: {
        schema_version: 1,
        api_version: 'v1',
        base_url: 'http://127.0.0.1:51002',
        auth_token: 'tok-beta-secret-bbb',
        runtime: {
          runtime_dir: '/srv/runtimes/beta',
          state_dir: '/srv/runtimes/beta/features',
          config_path: '/srv/runtimes/beta/config.yaml',
        },
        pid: 4243,
      },
    });
    const env = makeEnv({
      registryScan: () => betaScan,
      health: {
        [ALPHA_BASE]: healthFor(ALPHA_RUNTIME_DIR, 'alpha'),
        'http://127.0.0.1:51002': healthFor('/srv/runtimes/beta', 'beta'),
        [REMOTE_BASE]: healthFor('remote', 'remote'),
      },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('awaiting-server-choice');

    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    const chosen = await env.gateway.chooseServer({ serverKey: remoteKey() });
    expect(chosen.status).toBe('ready');
    expect(chosen.serverKey).toBe(remoteKey());
    expect(env.spawnCalls).toBe(0);
    expectNoTokenLeak(env);
  });
});

describe('RuntimeGateway ready-state locality', () => {
  it('kind flips local→remote→local with the switch, never emitting a ready state under the wrong kind', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    expect(env.gateway.connectedLocality).toBeNull();
    await env.gateway.start();
    expect(env.gateway.getState()).toMatchObject({
      status: 'ready',
      kind: 'local',
      serverKey: ALPHA_KEY,
    });
    expect(env.gateway.connectedLocality).toBe('local');

    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);
    env.states.length = 0;
    const remote = await env.gateway.switchServer({ serverKey: remoteKey() });
    expect(remote).toMatchObject({ status: 'ready', kind: 'remote', serverKey: remoteKey() });
    expect(env.gateway.connectedLocality).toBe('remote');
    // Transitional states carry no locality; ready states never the stale one.
    for (const emitted of env.states) {
      ConnectionStateSchema.parse(emitted);
      if (emitted.status === 'ready') {
        expect(emitted.kind).toBe('remote');
      } else {
        expect('kind' in emitted).toBe(false);
      }
    }

    env.states.length = 0;
    const back = await env.gateway.switchServer({ serverKey: ALPHA_KEY });
    expect(back).toMatchObject({ status: 'ready', kind: 'local', serverKey: ALPHA_KEY });
    expect(env.gateway.connectedLocality).toBe('local');
    for (const emitted of env.states) {
      ConnectionStateSchema.parse(emitted);
      if (emitted.status === 'ready') {
        expect(emitted.kind).toBe('local');
      } else {
        expect('kind' in emitted).toBe(false);
      }
    }
    expectNoTokenLeak(env);
  });

  it('a restart mid-remote-attach fences the generation: no ready state ever carries the fenced kind', async () => {
    const env = makeEnv({ registryScan: () => alphaScan() });
    await env.gateway.start();
    expect(env.gateway.getState()).toMatchObject({ status: 'ready', kind: 'local' });
    env.updateServers({ upsertKnown: remoteEntry() });
    env.tokens.save(remoteKey(), REMOTE_TOKEN);

    let releaseHealth: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      releaseHealth = resolve;
    });
    const realFetch = env.deps.fetchJson;
    env.deps.fetchJson = async (url, opts) => {
      if (url.startsWith(REMOTE_BASE) && url.endsWith('/api/v1/health')) {
        await gate;
      }
      return realFetch(url, opts);
    };

    const switching = env.gateway.switchServer({ serverKey: remoteKey() });
    await Promise.resolve();
    // Restart bumps the generation; the gated remote attach must be fenced.
    await env.gateway.restart();
    releaseHealth();
    await switching;

    // No ready state was ever emitted under the fenced remote kind.
    expect(env.states.filter((s) => s.status === 'ready' && s.kind === 'remote')).toHaveLength(0);

    // A fresh start re-attaches locally and stamps kind afresh.
    await env.gateway.start();
    expect(env.gateway.getState()).toMatchObject({
      status: 'ready',
      kind: 'local',
      serverKey: ALPHA_KEY,
    });
    expectNoTokenLeak(env);
  });
});
