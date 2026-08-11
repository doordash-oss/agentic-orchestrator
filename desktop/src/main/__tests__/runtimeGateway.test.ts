import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import {
  ConnectionStateSchema,
  isConnectionErrorState,
  MAX_KNOWN_SERVERS,
  type ConnectionState,
  type KnownServer,
  type ServersPrefs,
} from '../../shared/ipc';
import type { SafeError } from '../../shared/errors';
import type { RegistryScan } from '../gateway/registry';
import { DEFAULT_STOP_TIMEOUT_MS, type ChildExit } from '../gateway/serverProcess';
import {
  RuntimeGateway,
  type GatewayDeps,
  type GatewayTimeouts,
  type ServerChildLike,
  type SelectedRuntime,
} from '../gateway/runtimeGateway';

const EMPTY_SCAN: RegistryScan = { candidates: [], pruned: 0, rejected: [] };

const SELECTED: SelectedRuntime = {
  runtimeDir: '/home/ü ser/.agentic-orchestrator',
  stateDir: '/home/ü ser/.agentic-orchestrator/features',
  configPath: '/home/ü ser/.agentic-orchestrator/config.yaml',
};

const EXTERNAL_TOKEN = 'tok-external-secret-abc';
const LAUNCH_TOKEN = 'tok-launched-secret-def';
const EXTERNAL_BASE = 'http://127.0.0.1:49152';
const LAUNCH_BASE = 'http://127.0.0.1:50505';

function compatibleDeclaration(): Record<string, unknown> {
  return {
    api_version: 'v1',
    schema_version: 1,
    min_client_schema: 1,
    runtime_policy: 'loopback-bearer-v1',
    server_build: { version: 'v9.9.9-other-build', revision: 'deadbeef' },
  };
}

function discoveryRecord(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1,
    api_version: 'v1',
    base_url: EXTERNAL_BASE,
    auth_token: EXTERNAL_TOKEN,
    runtime: {
      runtime_dir: SELECTED.runtimeDir,
      state_dir: SELECTED.stateDir,
      config_path: SELECTED.configPath,
    },
    pid: 4242,
    started_at: '2026-07-14T00:00:00Z',
    ...overrides,
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
    launch_policy: { resolved: true, providers: [], dangerously_skip_permissions: false },
    started_at: '2026-07-14T00:00:00Z',
    owner: { pid: 4242, started_at: '2026-07-14T00:00:00Z' },
    server_time: '2026-07-14T00:00:01Z',
    compatibility: compatibleDeclaration(),
    ...overrides,
  };
}

class FakeChild implements ServerChildLike {
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
    this.emitExit(0, null, true);
  }

  emitExit(code: number | null, signal: NodeJS.Signals | null, expected = false): void {
    if (this.exited) {
      return;
    }
    this.exited = true;
    for (const listener of [...this.listeners]) {
      listener({ code, signal, expected });
    }
  }
}

interface Env {
  deps: GatewayDeps;
  gateway: RuntimeGateway;
  states: ConnectionState[];
  logs: string[];
  secrets: string[];
  spawned: FakeChild[];
  spawnCalls: Array<{ binaryPath: string; args: readonly string[] }>;
  fetchCalls: Array<{ url: string; token?: string; timeoutMs: number }>;
  setDiscovery(content: string | null): void;
  alive: Set<number>;
  /** Ordered registry scans consumed by the gateway; the last one repeats. */
  setRegistryScans(scans: RegistryScan[]): void;
  /** Successful-attach persistence calls, in order. */
  attachRecords: KnownServer[];
  /** Live view of the persisted known-servers prefs. */
  servers(): ServersPrefs;
}

interface EnvOptions {
  discovery?: string | null;
  /** health body served per base URL; an Error value means network failure */
  health?: Record<string, Record<string, unknown> | Error>;
  /** expected bearer per base URL for a 200 readiness answer */
  readinessTokens?: Record<string, string>;
  binary?: { ok: true; path: string } | { ok: false; tried: string[] };
  /** invoked right after spawn; defaults to publishing a launch discovery record */
  onSpawn?: (env: Env, child: FakeChild) => void;
  spawnError?: Error;
  timeouts?: Partial<GatewayTimeouts>;
  now?: () => number;
  diagnosticLines?: readonly string[];
  useDefaultTimeouts?: boolean;
  sleep?: (ms: number) => Promise<void>;
  /** Scripted registry scans (last repeats when exhausted). Default: empty. */
  registryScans?: RegistryScan[];
  /** Pre-populated known-servers prefs (bounded list + last-used pointer). */
  serversPrefs?: ServersPrefs;
}

function makeEnv(options: EnvOptions = {}): Env {
  let discovery = options.discovery ?? null;
  let registryScans = [...(options.registryScans ?? [])];
  let serversPrefs: ServersPrefs = options.serversPrefs ?? { known: [], lastUsed: null };
  const alive = new Set<number>([4242]);
  const health = options.health ?? { [EXTERNAL_BASE]: healthBody() };
  const readinessTokens = options.readinessTokens ?? {
    [EXTERNAL_BASE]: EXTERNAL_TOKEN,
    [LAUNCH_BASE]: LAUNCH_TOKEN,
  };
  const env = {} as Env;

  const defaultOnSpawn = (innerEnv: Env, child: FakeChild): void => {
    alive.add(child.pid ?? -1);
    health[LAUNCH_BASE] = healthBody({
      owner: { pid: child.pid, started_at: '2026-07-14T00:00:02Z' },
    });
    innerEnv.setDiscovery(
      JSON.stringify(
        discoveryRecord({ base_url: LAUNCH_BASE, auth_token: LAUNCH_TOKEN, pid: child.pid }),
      ),
    );
  };

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
      isProcessAlive: (pid) => alive.has(pid),
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
        if (opts.token !== undefined && opts.token === readinessTokens[base]) {
          return { status: 200, body: { api_version: 'v1' } };
        }
        return { status: 401, body: { code: 'unauthorized' } };
      }
      throw new Error(`unexpected url ${url}`);
    },
    resolveServerBinary: () => options.binary ?? { ok: true, path: '/rés dir/bin/agentico' },
    spawnServer: (binaryPath, args) => {
      if (options.spawnError) {
        throw options.spawnError;
      }
      const child = new FakeChild();
      env.spawned.push(child);
      env.spawnCalls.push({ binaryPath, args });
      (options.onSpawn ?? defaultOnSpawn)(env, child);
      return child;
    },
    registerSecret: (secret) => env.secrets.push(secret),
    sleep: options.sleep ?? (() => Promise.resolve()),
    log: (line) => env.logs.push(line),
    readDiagnosticLines: () => options.diagnosticLines ?? [],
    scanRegistry: () => {
      if (registryScans.length === 0) {
        return EMPTY_SCAN;
      }
      const scan = registryScans[0] ?? EMPTY_SCAN;
      if (registryScans.length > 1) {
        registryScans.shift();
      }
      return scan;
    },
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
    ...(options.now === undefined ? {} : { now: options.now }),
    timeouts: options.useDefaultTimeouts
      ? options.timeouts
      : { pollIntervalMs: 1, launchReadyMs: 20, shutdownGraceMs: 50, ...options.timeouts },
  };

  const gateway = new RuntimeGateway(deps);
  const states: ConnectionState[] = [];
  gateway.subscribe((state) => states.push(state));

  Object.assign(env, {
    deps,
    gateway,
    states,
    logs: [],
    secrets: [],
    spawned: [],
    spawnCalls: [],
    fetchCalls: [],
    setDiscovery: (content: string | null) => {
      discovery = content;
    },
    alive,
    attachRecords: [],
    setRegistryScans: (scans: RegistryScan[]) => {
      registryScans = [...scans];
    },
    servers: () => serversPrefs,
  });
  // Re-run assignment for arrays captured before Object.assign.
  return env;
}

/** Narrows to the failure variant; terminal states always carry an error. */
function requireError(state: ConnectionState): SafeError {
  if (!isConnectionErrorState(state)) {
    throw new Error(`expected a failure state, got ${state.status}`);
  }
  return state.error;
}

function expectNoTokenLeak(env: Env): void {
  for (const state of env.states) {
    ConnectionStateSchema.parse(state); // strict: token-shaped fields fail
    const raw = JSON.stringify(state);
    expect(raw).not.toContain(EXTERNAL_TOKEN);
    expect(raw).not.toContain(LAUNCH_TOKEN);
  }
  expect(JSON.stringify(env.logs)).not.toContain(EXTERNAL_TOKEN);
  expect(JSON.stringify(env.logs)).not.toContain(LAUNCH_TOKEN);
}

describe('RuntimeGateway attach', () => {
  it('attaches to a healthy, explicitly compatible external server without spawning', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.stage).toBe('ready');
    expect(state.ownership).toBe('external');
    // The harmless build difference is exposed, not blocking.
    expect(state.serverBuild).toEqual({ version: 'v9.9.9-other-build', revision: 'deadbeef' });
    expect(env.spawnCalls).toHaveLength(0);
    const statuses = env.states.map((s) => s.status);
    expect(statuses).toContain('discovering');
    expect(statuses).toContain('attaching');
    expect(statuses).toContain('connecting');
    expectNoTokenLeak(env);
  });

  it('threads a health-reported server name into the ready state without gating attach', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord({ name: 'frothy-macchiato' })),
      health: { [EXTERNAL_BASE]: healthBody({ name: 'frothy-macchiato' }) },
    });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.ownership).toBe('external');
    expect(state.serverName).toBe('frothy-macchiato');
    // The name is informational: the compatible server still attaches.
    expect(env.spawnCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });

  it('attaches to a name-less older server with a null display name', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.serverName ?? null).toBeNull();
    expectNoTokenLeak(env);
  });

  it('drops an oversized or malformed health name without blocking attach', async () => {
    for (const bad of ['x'.repeat(65), 42]) {
      const env = makeEnv({
        discovery: JSON.stringify(discoveryRecord()),
        health: { [EXTERNAL_BASE]: healthBody({ name: bad }) },
      });
      await env.gateway.start();

      const state = env.gateway.getState();
      expect(state.status).toBe('ready');
      expect(state.serverName ?? null).toBeNull();
      expectNoTokenLeak(env);
    }
  });

  it('authenticates readiness with the discovery token kept in main-process memory', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      timeouts: { healthProbeMs: 11, apiRequestMs: 22 },
    });
    await env.gateway.start();
    const readiness = env.fetchCalls.find((c) => c.url.endsWith('/api/v1/readiness'));
    expect(readiness?.token).toBe(EXTERNAL_TOKEN);
    expect(readiness?.timeoutMs).toBe(22);
    // The unauthenticated health probe never sends the token.
    const health = env.fetchCalls.find((c) => c.url.endsWith('/api/v1/health'));
    expect(health?.token).toBeUndefined();
    expect(health?.timeoutMs).toBe(11);
    expectNoTokenLeak(env);
  });

  it('honors a longer timeout requested by a trusted main-process service', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      timeouts: { apiRequestMs: 22 },
    });
    await env.gateway.start();

    await expect(
      env.gateway.apiRequest('/api/v1/features', {
        timeoutMs: 6 * 60_000,
      }),
    ).rejects.toThrow('unexpected url');

    const request = env.fetchCalls.find((call) => call.url.endsWith('/api/v1/features'));
    expect(request?.timeoutMs).toBe(6 * 60_000);
  });

  it('hard-blocks on a healthy server with no compatibility declaration', async () => {
    const body = healthBody();
    delete body['compatibility'];
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      health: { [EXTERNAL_BASE]: body },
    });
    await env.gateway.start();
    const state = env.gateway.getState();
    expect(state.status).toBe('incompatible');
    expect(state.ownership).toBe('external');
    const error = requireError(state);
    expect(error.code).toBe('E_INCOMPATIBLE_SERVER');
    expect(error.remediation).toMatch(/update/i);
    // Guided resolution never offers to stop the external process.
    expect(`${error.message} ${error.remediation ?? ''}`).not.toMatch(
      /\b(kill|terminate|stop the)\b/i,
    );
    expect(env.spawnCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });

  it('hard-blocks on an explicitly incompatible declaration and never signals the server', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      health: {
        [EXTERNAL_BASE]: healthBody({
          compatibility: { ...compatibleDeclaration(), schema_version: 99 },
        }),
      },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('incompatible');
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.gateway.hasOwnedChild()).toBe(false);
    expectNoTokenLeak(env);
  });

  it('fails safe when the compatible server rejects the stored credentials', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      readinessTokens: { [EXTERNAL_BASE]: 'a-different-token' },
    });
    await env.gateway.start();
    const state = env.gateway.getState();
    expect(state.status).toBe('error');
    expect(requireError(state).code).toBe('E_ATTACH_AUTH');
    expect(env.spawnCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });
});

describe('RuntimeGateway discovery fallbacks', () => {
  it('treats a dead-pid record as stale and launches', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord({ pid: 999999 })) });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);
    expectNoTokenLeak(env);
  });

  it('treats malformed discovery as absent, records a diagnostic, and launches', async () => {
    const env = makeEnv({ discovery: '{broken json' });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.spawnCalls).toHaveLength(1);
    expect(env.logs.join('\n')).toMatch(/discovery/i);
    expectNoTokenLeak(env);
  });

  it('never probes a non-loopback discovery URL and launches instead', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord({ base_url: 'http://10.1.2.3:8080' })),
    });
    await env.gateway.start();
    expect(env.fetchCalls.every((c) => !c.url.startsWith('http://10.1.2.3'))).toBe(true);
    expect(env.spawnCalls).toHaveLength(1);
    expectNoTokenLeak(env);
  });

  it('treats a wrong-runtime record as not a match and launches', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(
        discoveryRecord({
          runtime: { runtime_dir: '/other', state_dir: '/other/features', config_path: '/o.yaml' },
        }),
      ),
    });
    await env.gateway.start();
    expect(env.spawnCalls).toHaveLength(1);
    expect(env.gateway.getState().status).toBe('ready');
    expectNoTokenLeak(env);
  });

  it('launches when the recorded server no longer answers health probes', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      health: { [EXTERNAL_BASE]: new Error('ECONNREFUSED') },
    });
    await env.gateway.start();
    expect(env.spawnCalls).toHaveLength(1);
    expect(env.gateway.getState().status).toBe('ready');
    expectNoTokenLeak(env);
  });
});

describe('RuntimeGateway launch', () => {
  it('launches the bundled binary with an argv array carrying the selected runtime', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.spawnCalls).toEqual([
      {
        binaryPath: '/rés dir/bin/agentico',
        args: [
          'server',
          '--config',
          '/home/ü ser/.agentic-orchestrator/config.yaml',
          '--state-dir',
          '/home/ü ser/.agentic-orchestrator/features',
        ],
      },
    ]);
    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.ownership).toBe('app-owned');
    const statuses = env.states.map((s) => s.status);
    expect(statuses).toContain('launching');
    expect(statuses).toContain('waiting-health');
    expect(statuses).toContain('connecting');
    expectNoTokenLeak(env);
  });

  it('registers the launched token as a log secret and uses it for readiness', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.secrets).toContain(LAUNCH_TOKEN);
    const readiness = env.fetchCalls.filter((c) => c.url.endsWith('/api/v1/readiness'));
    expect(readiness.at(-1)?.token).toBe(LAUNCH_TOKEN);
    expectNoTokenLeak(env);
  });

  it('allows a healthy bundled runtime to finish a slow provider-discovery startup', async () => {
    let elapsedMs = 0;
    const env = makeEnv({
      useDefaultTimeouts: true,
      onSpawn: (innerEnv, child) => {
        innerEnv.alive.add(child.pid ?? -1);
      },
      sleep: async (ms) => {
        elapsedMs += ms;
        if (elapsedMs < 28_000) {
          return;
        }
        env.setDiscovery(
          JSON.stringify(
            discoveryRecord({ base_url: LAUNCH_BASE, auth_token: LAUNCH_TOKEN, pid: 777 }),
          ),
        );
      },
      health: {
        [LAUNCH_BASE]: healthBody({
          owner: { pid: 777, started_at: '2026-07-14T00:00:02Z' },
        }),
      },
    });

    await env.gateway.start();

    expect(elapsedMs).toBeGreaterThanOrEqual(28_000);
    expect(env.gateway.getState()).toMatchObject({ status: 'ready', ownership: 'app-owned' });
    expect(env.spawned[0]?.stopCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });

  it('surfaces missing bundled resources as an actionable state without spawning', async () => {
    const env = makeEnv({ binary: { ok: false, tried: ['/a/agentico', '/b/agentico'] } });
    await env.gateway.start();
    const state = env.gateway.getState();
    expect(state.status).toBe('resources-missing');
    const error = requireError(state);
    expect(error.code).toBe('E_RESOURCES_MISSING');
    expect(error.remediation).toBeTruthy();
    expect(env.spawnCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });

  it('maps a spawn failure (e.g. permissions) to launch-failed with remediation', async () => {
    const env = makeEnv({ spawnError: new Error('EACCES: permission denied') });
    await env.gateway.start();
    const state = env.gateway.getState();
    expect(state.status).toBe('launch-failed');
    expect(requireError(state).code).toBe('E_LAUNCH_FAILED');
    expectNoTokenLeak(env);
  });

  it('reports an early child exit during startup as launch-failed', async () => {
    const env = makeEnv({
      onSpawn: (_env, child) => {
        child.emitExit(1, null);
      },
    });
    await env.gateway.start();
    const state = env.gateway.getState();
    expect(state.status).toBe('launch-failed');
    expect(requireError(state).message).toMatch(/exited/i);
    expectNoTokenLeak(env);
  });

  it('stops the child and fails when readiness never becomes healthy in the bound', async () => {
    const env = makeEnv({
      onSpawn: () => {
        // never publish discovery: startup hangs
      },
      timeouts: { launchReadyMs: 5, pollIntervalMs: 1 },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('launch-failed');
    // The child is reaped — no leaked processes.
    expect(env.spawned[0]?.stopCalls.length).toBeGreaterThan(0);
    expect(env.spawned[0]?.exited).toBe(true);
    expect(env.gateway.hasOwnedChild()).toBe(false);
    expectNoTokenLeak(env);
  });
});

describe('RuntimeGateway supervision', () => {
  it('surfaces only bounded, path- and token-redacted app-owned crash diagnostics', async () => {
    const diagnosticLines = Array.from(
      { length: 25 },
      (_, index) =>
        `line ${String(index)} ${LAUNCH_TOKEN} /private/runtime/secret-${String(index)} ${'x'.repeat(600)}`,
    );
    const env = makeEnv({ diagnosticLines });
    await env.gateway.start();
    env.spawned[0]!.emitExit(1, null);

    const state = env.gateway.getState();
    expect(state.status).toBe('crashed');
    if (state.status !== 'crashed') throw new Error('expected crash state');
    expect(state.diagnostics?.commandContext).toBe(
      'bundled agentico server --config [path] --state-dir [path]',
    );
    expect(state.diagnostics?.logTail).toHaveLength(20);
    expect(state.diagnostics?.logTail[0]).toContain('line 5');
    expect(state.diagnostics?.logTail.every((line) => line.length <= 512)).toBe(true);
    const rendered = JSON.stringify(state.diagnostics);
    expect(rendered).not.toContain(LAUNCH_TOKEN);
    expect(rendered).not.toContain('/private/runtime');
    expect(rendered).toContain('[redacted]');
    expect(rendered).toContain('[path]');
  });

  it('drops a lost external runtime after stale-stream verification without taking ownership', async () => {
    const health = { [EXTERNAL_BASE]: healthBody() as Record<string, unknown> | Error };
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()), health });
    await env.gateway.start();
    expect(env.gateway.getState()).toMatchObject({ status: 'ready', ownership: 'external' });

    health[EXTERNAL_BASE] = new Error('connection refused');
    await env.gateway.handleGlobalStreamStale();

    const state = env.gateway.getState();
    expect(state).toMatchObject({ status: 'error', ownership: 'external' });
    expect(requireError(state).code).toBe('E_EXTERNAL_SERVER_LOST');
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.gateway.hasOwnedChild()).toBe(false);
    await expect(env.gateway.apiRequest('/api/v1/readiness')).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
  });

  it('keeps an external connection when stale-stream verification finds it healthy', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    await env.gateway.handleGlobalStreamStale();
    expect(env.gateway.getState()).toMatchObject({ status: 'ready', ownership: 'external' });
    expect(env.spawnCalls).toHaveLength(0);
  });

  it('automatically relaunches an app-owned runtime after an unexpected ready-state exit', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');

    env.setDiscovery(null);
    env.spawned[0]!.emitExit(1, null);

    await vi.waitFor(() => {
      expect(env.spawnCalls).toHaveLength(2);
      expect(env.gateway.getState().status).toBe('ready');
    });
    expectNoTokenLeak(env);
  });

  it('lands in a retryable crashed state when the recovery delay rejects', async () => {
    const env = makeEnv();
    await env.gateway.start();
    env.deps.sleep = vi.fn(() => Promise.reject(new Error('timer unavailable')));

    env.spawned[0]!.emitExit(1, null);

    await vi.waitFor(() => {
      expect(env.logs).toContainEqual(
        expect.stringContaining('automatic recovery delay failed: timer unavailable'),
      );
    });
    expect(env.gateway.getState()).toMatchObject({
      status: 'crashed',
      detail: 'Automatic recovery could not be scheduled.',
      error: {
        code: 'E_SERVER_CRASHED',
        remediation: 'Use Retry to start a fresh supervised cycle.',
      },
    });
  });

  it('does not stomp or charge a manual retry when automatic recovery finds it busy', async () => {
    const env = makeEnv({ timeouts: { crashRestartInitialMs: 10 } });
    await env.gateway.start();

    let releaseRecoveryDelay = (): void => undefined;
    const recoveryDelay = new Promise<void>((resolve) => {
      releaseRecoveryDelay = resolve;
    });
    const sleep = vi.fn((_delay: number) => recoveryDelay);
    env.deps.sleep = sleep;
    env.setDiscovery(null);
    env.spawned[0]!.emitExit(1, null);

    const originalFetchJson = env.deps.fetchJson;
    let releaseHealth = (_result: Awaited<ReturnType<GatewayDeps['fetchJson']>>): void => undefined;
    const pendingHealth = new Promise<Awaited<ReturnType<GatewayDeps['fetchJson']>>>((resolve) => {
      releaseHealth = resolve;
    });
    env.deps.fetchJson = (url, options) =>
      url === `${LAUNCH_BASE}/api/v1/health` ? pendingHealth : originalFetchJson(url, options);

    const retry = env.gateway.retry();
    await vi.waitFor(() => expect(env.spawnCalls).toHaveLength(2));
    releaseRecoveryDelay();
    await Promise.resolve();
    await Promise.resolve();

    expect(env.gateway.getState().status).not.toBe('crashed');
    expect(sleep).toHaveBeenCalledTimes(1);

    releaseHealth({
      status: 200,
      body: healthBody({ owner: { pid: 777, started_at: '2026-07-14T00:00:02Z' } }),
    });
    await retry;
    expect(env.gateway.getState().status).toBe('ready');

    const nextRecoveryDelay = new Promise<void>(() => undefined);
    sleep.mockImplementation((_delay: number) => nextRecoveryDelay);
    env.setDiscovery(null);
    env.spawned.at(-1)!.emitExit(1, null);
    expect(sleep.mock.calls.map(([delay]) => delay)).toStrictEqual([10, 10]);
  });

  it('stops after three automatic restart attempts in the rolling crash window', async () => {
    let launches = 0;
    const env = makeEnv({
      health: { [LAUNCH_BASE]: healthBody() },
      onSpawn: (innerEnv, child) => {
        launches += 1;
        innerEnv.alive.add(child.pid ?? -1);
        if (launches === 1) {
          innerEnv.setDiscovery(
            JSON.stringify(
              discoveryRecord({ base_url: LAUNCH_BASE, auth_token: LAUNCH_TOKEN, pid: child.pid }),
            ),
          );
        } else {
          innerEnv.setDiscovery(null);
          child.emitExit(1, null);
        }
      },
    });
    await env.gateway.start();
    env.setDiscovery(null);
    env.spawned[0]!.emitExit(1, null);

    await vi.waitFor(() => {
      expect(env.spawnCalls).toHaveLength(4);
      expect(requireError(env.gateway.getState()).code).toBe('E_SERVER_CRASH_LOOP');
    });
    expect(env.gateway.hasOwnedChild()).toBe(false);
  });

  it('resets the restart budget after one healthy minute', async () => {
    let now = 1_000;
    const env = makeEnv({ now: () => now });
    await env.gateway.start();

    for (let expectedSpawns = 2; expectedSpawns <= 4; expectedSpawns += 1) {
      env.setDiscovery(null);
      env.spawned.at(-1)!.emitExit(1, null);
      await vi.waitFor(() => {
        expect(env.spawnCalls).toHaveLength(expectedSpawns);
        expect(env.gateway.getState().status).toBe('ready');
      });
    }

    now += 60_001;
    env.setDiscovery(null);
    env.spawned.at(-1)!.emitExit(1, null);
    await vi.waitFor(() => {
      expect(env.spawnCalls).toHaveLength(5);
      expect(env.gateway.getState().status).toBe('ready');
    });
  });

  it('marks an unexpected exit after ready as crashed with a retry path', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');

    env.spawned[0]!.emitExit(1, null);
    const state = env.gateway.getState();
    expect(state.status).toBe('crashed');
    expect(state.ownership).toBe('none');
    expect(requireError(state).code).toBe('E_SERVER_CRASHED');
    expect(env.gateway.hasOwnedChild()).toBe(false);
    expectNoTokenLeak(env);
  });

  it('retry after a crash relaunches and reaches ready again', async () => {
    const env = makeEnv();
    await env.gateway.start();
    env.spawned[0]!.emitExit(1, null);
    expect(env.gateway.getState().status).toBe('crashed');

    env.setDiscovery(null);
    await env.gateway.retry();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.spawnCalls).toHaveLength(2);
    expectNoTokenLeak(env);
  });

  it('retry while healthy is a no-op', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    const before = env.states.length;
    await env.gateway.retry();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.states.length).toBe(before);
  });

  it('restart from a healthy app-owned connection stops the child and relaunches', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);

    env.setDiscovery(null);
    await env.gateway.restart();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(2);
    expect(env.spawned[0]!.stopCalls.length).toBeGreaterThan(0);
    expectNoTokenLeak(env);
  });

  it('restart from a healthy external connection detaches without signalling the server', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    expect(env.gateway.getState().ownership).toBe('external');
    expect(env.spawnCalls).toHaveLength(0);

    await env.gateway.restart();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('external');
    expect(env.spawnCalls).toHaveLength(0);
    expectNoTokenLeak(env);
  });

  it('shutdown gracefully stops only the app-owned child within the bound', async () => {
    const env = makeEnv();
    await env.gateway.start();
    expect(env.gateway.hasOwnedChild()).toBe(true);

    await env.gateway.shutdown();
    expect(env.spawned[0]!.stopCalls).toEqual([{ timeoutMs: 50 }]);
    expect(env.spawned[0]!.exited).toBe(true);
    expect(env.gateway.hasOwnedChild()).toBe(false);
    // Expected exit: shutdown never produces a crashed state.
    expect(env.gateway.getState().status).not.toBe('crashed');
  });

  it('gives the server enough time to reap managed provider process groups by default', async () => {
    const env = makeEnv({ useDefaultTimeouts: true });
    await env.gateway.start();

    await env.gateway.shutdown();

    expect(env.spawned[0]!.stopCalls).toEqual([{ timeoutMs: DEFAULT_STOP_TIMEOUT_MS }]);
  });

  it('shutdown leaves externally owned servers untouched', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    expect(env.gateway.getState().ownership).toBe('external');
    await env.gateway.shutdown();
    expect(env.spawned).toHaveLength(0); // nothing to signal, nothing spawned
  });

  it('stops notifying an unsubscribed listener', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    const listener = vi.fn();
    const unsubscribe = env.gateway.subscribe(listener);
    unsubscribe();
    await env.gateway.start();
    expect(listener).not.toHaveBeenCalled();
  });
});

describe('RuntimeGateway apiRequest', () => {
  it('rejects with E_NOT_CONNECTED before the gateway is ready', async () => {
    const env = makeEnv();
    await expect(env.gateway.apiRequest('/api/v1/readiness')).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
    expect(env.fetchCalls).toHaveLength(0);
  });

  it('sends authenticated requests to the attached runtime once ready', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    const result = await env.gateway.apiRequest('/api/v1/readiness');
    expect(result.status).toBe(200);
    const call = env.fetchCalls.at(-1);
    expect(call?.url).toBe(`${EXTERNAL_BASE}/api/v1/readiness`);
    expect(call?.token).toBe(EXTERNAL_TOKEN);
    expectNoTokenLeak(env);
  });

  it('permits bounded history and rewind-preview requests through the allowlist', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    env.deps.fetchJson = vi.fn(() => Promise.resolve({ status: 200, body: { api_version: 'v1' } }));

    for (const path of [
      '/api/v1/features/feature-1/runs?page=1&page_size=20',
      '/api/v1/features/feature-1/runs/6/artifacts/history-6?offset=0&limit=262144',
      '/api/v1/features/feature-1/runs/6/logs/session-1?limit=4096',
      '/api/v1/features/feature-1/rewind/preview?target_phase=implement&roadmap_phase=2&upgrade_pipeline=large',
      '/api/v1/features/feature-1/repositories/repo-a/diff?file_path=src%2FREADME.md',
    ]) {
      await expect(env.gateway.apiRequest(path)).resolves.toMatchObject({ status: 200 });
    }
  });

  it('allows only bounded transcript pagination query parameters', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    env.deps.fetchJson = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: { api_version: 'v1', cursor: { total: 0, start: 0, end: 0 }, messages: [] },
      }),
    );

    await expect(
      env.gateway.apiRequest('/api/v1/sessions/session-1/transcript?offset=0&limit=500'),
    ).resolves.toMatchObject({ status: 200 });
    expect(env.deps.fetchJson).toHaveBeenCalledWith(
      `${EXTERNAL_BASE}/api/v1/sessions/session-1/transcript?offset=0&limit=500`,
      expect.objectContaining({ token: EXTERNAL_TOKEN }),
    );
  });

  it('accepts dotted session IDs for authenticated reads and output streams', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    const streamCalls: Array<{ url: string; token: string }> = [];
    env.deps.openSse = (url, options) => {
      streamCalls.push({ url, token: options.token });
      return Promise.resolve({
        status: 200,
        lines: (async function* () {})(),
        close: () => undefined,
      });
    };
    await env.gateway.start();
    env.deps.fetchJson = vi.fn(() =>
      Promise.resolve({ status: 200, body: { api_version: 'v1', id: 'session.a-1' } }),
    );

    await expect(env.gateway.apiRequest('/api/v1/sessions/session.a-1')).resolves.toMatchObject({
      status: 200,
    });
    await expect(env.gateway.openSessionOutputStream('session.a-1')).resolves.toMatchObject({
      status: 200,
    });

    expect(env.deps.fetchJson).toHaveBeenCalledWith(
      `${EXTERNAL_BASE}/api/v1/sessions/session.a-1`,
      expect.objectContaining({ token: EXTERNAL_TOKEN }),
    );
    expect(streamCalls).toStrictEqual([
      {
        url: `${EXTERNAL_BASE}/api/v1/sessions/session.a-1/output/stream`,
        token: EXTERNAL_TOKEN,
      },
    ]);
  });

  it('rejects paths outside /api/v1 and traversal shapes fail-closed', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    for (const path of [
      'http://evil.example.com/api/v1/readiness',
      '/etc/passwd',
      '/api/v1/../secrets',
      '/api/v1/readiness?token=x',
      '//evil.example.com/api/v1/readiness',
      '/api/v1/features/feature-1/repositories/repo-a/diff?file_path=../README.md',
      '/api/v1/features/feature-1/repositories/repo-a/diff?file_path=/etc/passwd',
      '/api/v1/features/feature-1/repositories/repo-a/diff?file_path=README.md&limit=1',
      '/api/v1/features/feature-1/repositories/repo-a/diff?file_path=',
    ]) {
      await expect(env.gateway.apiRequest(path)).rejects.toMatchObject({
        safe: { code: 'E_BAD_API_PATH' },
      });
    }
  });

  it('rejects with E_NOT_CONNECTED again after the owned runtime crashes', async () => {
    const env = makeEnv();
    await env.gateway.start();
    await expect(env.gateway.apiRequest('/api/v1/readiness')).resolves.toMatchObject({
      status: 200,
    });
    env.spawned[0]!.emitExit(1, null);
    await expect(env.gateway.apiRequest('/api/v1/readiness')).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
  });

  it('rejects after shutdown so no request can carry a stale credential', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    await env.gateway.shutdown();
    await expect(env.gateway.apiRequest('/api/v1/readiness')).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
  });
});

describe('RuntimeGateway openEventStream', () => {
  function withSse(env: Env) {
    const calls: Array<{ url: string; token: string }> = [];
    env.deps.openSse = (url, options) => {
      calls.push({ url, token: options.token });
      return Promise.resolve({
        status: 200,
        lines: (async function* () {})(),
        close: () => undefined,
      });
    };
    return calls;
  }

  it('rejects with E_NOT_CONNECTED before the gateway is ready', async () => {
    const env = makeEnv();
    env.deps.openSse = () => Promise.reject(new Error('must not be called'));
    await expect(env.gateway.openEventStream()).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
  });

  it('opens the fixed events path with the bearer as a header option, never in the URL', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    const calls = withSse(env);
    await env.gateway.start();
    const stream = await env.gateway.openEventStream();
    expect(stream.status).toBe(200);
    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toBe(`${EXTERNAL_BASE}/api/v1/events`);
    expect(calls[0]!.token).toBe(EXTERNAL_TOKEN);
    expectNoTokenLeak(env);
  });

  it('resumes with after/epoch query parameters and no credential material', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    const calls = withSse(env);
    await env.gateway.start();
    await env.gateway.openEventStream({ afterSeq: 42, epoch: 'epoch-a' });
    expect(calls[0]!.url).toBe(`${EXTERNAL_BASE}/api/v1/events?after=42&epoch=epoch-a`);
    expect(calls[0]!.url).not.toContain(EXTERNAL_TOKEN);
  });

  it('fails closed when no SSE transport is wired', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    await expect(env.gateway.openEventStream()).rejects.toMatchObject({
      safe: { code: 'E_SSE_UNAVAILABLE' },
    });
  });

  it('rejects after shutdown so a stream can never carry a stale credential', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    withSse(env);
    await env.gateway.start();
    await env.gateway.shutdown();
    await expect(env.gateway.openEventStream()).rejects.toMatchObject({
      safe: { code: 'E_NOT_CONNECTED' },
    });
  });
});

// --- Registry-first startup selection (Phase 2) ------------------------------

const ALPHA_RUNTIME_DIR = '/home/ü ser/.agentic-orchestrator';
const BETA_RUNTIME_DIR = '/srv/runtimes/beta';
const ALPHA_BASE = 'http://127.0.0.1:51001';
const BETA_BASE = 'http://127.0.0.1:51002';
const ALPHA_TOKEN = 'tok-alpha-secret-aaa';
const BETA_TOKEN = 'tok-beta-secret-bbb';

function registryCandidate(options: {
  runtimeDir: string;
  baseUrl: string;
  token: string;
  name?: string;
  pid?: number;
}): {
  serverKey: string;
  runtimeDir: string;
  record: RegistryScan['candidates'][number]['record'];
} {
  const runtimeDir = options.runtimeDir;
  return {
    serverKey: serverKeyFor(runtimeDir),
    runtimeDir,
    record: {
      schema_version: 1,
      api_version: 'v1',
      base_url: options.baseUrl,
      auth_token: options.token,
      ...(options.name === undefined ? {} : { name: options.name }),
      runtime: {
        runtime_dir: runtimeDir,
        state_dir: `${runtimeDir}/features`,
        config_path: `${runtimeDir}/config.yaml`,
      },
      pid: options.pid ?? 4242,
    },
  };
}

function serverKeyFor(runtimeDir: string): string {
  // The scanner owns key derivation; tests compute it the same way the
  // candidate construction in the gateway expects.
  return createHash('sha256').update(runtimeDir).digest('hex').slice(0, 32);
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

/** Two-registry-server world: alpha and beta, both healthy and attachable. */
function makeMultiServerEnv(options: EnvOptions = {}): Env {
  return makeEnv({
    health: {
      [ALPHA_BASE]: healthFor(ALPHA_RUNTIME_DIR, 'alpha'),
      [BETA_BASE]: healthFor(BETA_RUNTIME_DIR, 'beta'),
      [LAUNCH_BASE]: healthBody({
        owner: { pid: 777, started_at: '2026-07-14T00:00:02Z' },
      }),
      ...options.health,
    },
    readinessTokens: {
      [ALPHA_BASE]: ALPHA_TOKEN,
      [BETA_BASE]: BETA_TOKEN,
      [LAUNCH_BASE]: LAUNCH_TOKEN,
      ...options.readinessTokens,
    },
    ...options,
  });
}

describe('RuntimeGateway registry-first startup selection', () => {
  it('keeps the spawn path byte-identical when the registry has no live entries', async () => {
    const env = makeMultiServerEnv({ registryScans: [EMPTY_SCAN] });
    await env.gateway.start();

    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);
    expect(env.attachRecords).toHaveLength(0); // only attach persists
    expectNoTokenLeak(env);
  });

  it('exactly one live registry candidate attaches silently without a discovery file', async () => {
    const env = makeMultiServerEnv({
      registryScans: [
        {
          candidates: [
            registryCandidate({
              runtimeDir: ALPHA_RUNTIME_DIR,
              baseUrl: ALPHA_BASE,
              token: ALPHA_TOKEN,
              name: 'alpha',
            }),
          ],
          pruned: 0,
          rejected: [],
        },
      ],
    });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.ownership).toBe('external');
    expect(state.serverName).toBe('alpha');
    expect(state.connectedRuntimeDir).toBe(ALPHA_RUNTIME_DIR);
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.attachRecords).toHaveLength(1);
    expect(env.attachRecords[0]!.serverKey).toBe(serverKeyFor(ALPHA_RUNTIME_DIR));
    expect(env.attachRecords[0]!.name).toBe('alpha');
    expect(env.servers().lastUsed).toBe(serverKeyFor(ALPHA_RUNTIME_DIR));
    expectNoTokenLeak(env);
  });

  it('last-used live among many -> silent reconnect to the remembered server', async () => {
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const env = makeMultiServerEnv({
      registryScans: [
        {
          candidates: [
            registryCandidate({
              runtimeDir: ALPHA_RUNTIME_DIR,
              baseUrl: ALPHA_BASE,
              token: ALPHA_TOKEN,
              name: 'alpha',
            }),
            beta,
          ],
          pruned: 0,
          rejected: [],
        },
      ],
      serversPrefs: {
        known: [
          {
            serverKey: beta.serverKey,
            name: 'beta',
            baseUrl: BETA_BASE,
            runtimeDir: BETA_RUNTIME_DIR,
            lastSeenAt: '2026-07-14T00:00:00Z',
          },
        ],
        lastUsed: beta.serverKey,
      },
    });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('ready');
    expect(state.serverName).toBe('beta');
    expect(state.connectedRuntimeDir).toBe(BETA_RUNTIME_DIR);
    expect(env.spawnCalls).toHaveLength(0);
    // No pick step was rendered: no awaiting state ever appeared.
    expect(env.states.some((s) => s.status === 'awaiting-server-choice')).toBe(false);
    expectNoTokenLeak(env);
  });

  it('multiple live without a usable last-used -> picker; choosing attaches and persists', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 0, rejected: [] }],
    });
    await env.gateway.start();

    const state = env.gateway.getState();
    expect(state.status).toBe('awaiting-server-choice');
    if (state.status !== 'awaiting-server-choice') {
      throw new Error('unreachable');
    }
    expect(state.candidates).toEqual([
      { serverKey: alpha.serverKey, name: 'alpha', runtimeDir: ALPHA_RUNTIME_DIR },
      { serverKey: beta.serverKey, name: 'beta', runtimeDir: BETA_RUNTIME_DIR },
    ]);
    // Snapshot-based: nothing was probed or spawned before the choice.
    expect(env.fetchCalls).toHaveLength(0);
    expect(env.spawnCalls).toHaveLength(0);
    // The snapshot never carries tokens.
    expect(JSON.stringify(state)).not.toContain(ALPHA_TOKEN);
    expect(JSON.stringify(state)).not.toContain(BETA_TOKEN);

    await env.gateway.chooseServer({ serverKey: alpha.serverKey });
    const attached = env.gateway.getState();
    expect(attached.status).toBe('ready');
    expect(attached.ownership).toBe('external');
    expect(attached.serverName).toBe('alpha');
    expect(attached.connectedRuntimeDir).toBe(ALPHA_RUNTIME_DIR);
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.attachRecords).toHaveLength(1);
    expect(env.attachRecords[0]!.serverKey).toBe(alpha.serverKey);
    expect(env.servers().lastUsed).toBe(alpha.serverKey);
    expectNoTokenLeak(env);
  });

  it('last-used dead with others live -> picker (the dead entry is already pruned by the scanner)', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 1, rejected: [] }],
      serversPrefs: {
        known: [],
        lastUsed: 'dead0000000000000000000000000000',
      },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('awaiting-server-choice');
  });

  it('last-used dead with no live servers -> spawn fallback', async () => {
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [], pruned: 1, rejected: [] }],
      serversPrefs: {
        known: [],
        lastUsed: 'dead0000000000000000000000000000',
      },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);
  });

  it('relaunch auto-reconnects silently to the server chosen last launch', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const first = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 0, rejected: [] }],
    });
    await first.gateway.start();
    await first.gateway.chooseServer({ serverKey: beta.serverKey });
    expect(first.gateway.getState().serverName).toBe('beta');

    const relaunch = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 0, rejected: [] }],
      serversPrefs: first.servers(),
    });
    await relaunch.gateway.start();
    expect(relaunch.gateway.getState().status).toBe('ready');
    expect(relaunch.gateway.getState().serverName).toBe('beta');
    expect(relaunch.states.some((s) => s.status === 'awaiting-server-choice')).toBe(false);
  });

  it('an unknown choice rescans from scratch (then spawns when nothing is live)', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 0, rejected: [] }],
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('awaiting-server-choice');

    env.setRegistryScans([EMPTY_SCAN]);
    const state = await env.gateway.chooseServer({ serverKey: 'nobodys-key' });
    expect(state.status).toBe('ready');
    expect(state.ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);
  });

  it('a server dying mid-pick lands in the error state; retry rescans', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const beta = registryCandidate({
      runtimeDir: BETA_RUNTIME_DIR,
      baseUrl: BETA_BASE,
      token: BETA_TOKEN,
      name: 'beta',
    });
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha, beta], pruned: 0, rejected: [] }],
      // alpha dies between the scan and the choice.
      health: {
        [ALPHA_BASE]: new Error('connection refused'),
        [BETA_BASE]: healthFor(BETA_RUNTIME_DIR, 'beta'),
      },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('awaiting-server-choice');

    const state = await env.gateway.chooseServer({ serverKey: alpha.serverKey });
    expect(requireError(state).code).toBe('E_ATTACH_UNREACHABLE');

    // Retry rescans from scratch: alpha is gone from the new scan, beta alone
    // attaches silently.
    env.setRegistryScans([{ candidates: [beta], pruned: 1, rejected: [] }]);
    const retried = await env.gateway.retry();
    expect(retried.status).toBe('ready');
    expect(retried.serverName).toBe('beta');
  });

  it('a last-used candidate dying between scan and probe falls back to spawn', async () => {
    const alpha = registryCandidate({
      runtimeDir: ALPHA_RUNTIME_DIR,
      baseUrl: ALPHA_BASE,
      token: ALPHA_TOKEN,
      name: 'alpha',
    });
    const env = makeMultiServerEnv({
      registryScans: [{ candidates: [alpha], pruned: 0, rejected: [] }],
      health: {
        [ALPHA_BASE]: new Error('connection refused'),
        [BETA_BASE]: healthFor(BETA_RUNTIME_DIR, 'beta'),
      },
      serversPrefs: { known: [], lastUsed: alpha.serverKey },
    });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('app-owned');
    expect(env.spawnCalls).toHaveLength(1);
  });

  it('old-binary world: registry empty, legacy discovery candidate attaches and persists last-used', async () => {
    const env = makeEnv({
      discovery: JSON.stringify(discoveryRecord()),
      registryScans: [EMPTY_SCAN],
    });
    await env.gateway.start();

    expect(env.gateway.getState().status).toBe('ready');
    expect(env.gateway.getState().ownership).toBe('external');
    expect(env.spawnCalls).toHaveLength(0);
    expect(env.attachRecords).toHaveLength(1);
    expect(env.attachRecords[0]!.serverKey).toBe(serverKeyFor(SELECTED.runtimeDir));
    expect(env.servers().lastUsed).toBe(serverKeyFor(SELECTED.runtimeDir));
  });

  it('chooseServer is a no-op outside the awaiting-server-choice state', async () => {
    const env = makeMultiServerEnv({ registryScans: [EMPTY_SCAN] });
    await env.gateway.start();
    expect(env.gateway.getState().status).toBe('ready');

    const state = await env.gateway.chooseServer({ serverKey: 'nobodys-key' });
    expect(state.status).toBe('ready');
    expect(env.spawnCalls).toHaveLength(1);
  });
});
