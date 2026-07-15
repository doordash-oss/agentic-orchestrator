import { describe, expect, it, vi } from 'vitest';
import {
  ConnectionStateSchema,
  isConnectionErrorState,
  type ConnectionState,
} from '../../shared/ipc';
import type { SafeError } from '../../shared/errors';
import type { ChildExit } from '../gateway/serverProcess';
import {
  RuntimeGateway,
  type GatewayDeps,
  type ServerChildLike,
  type SelectedRuntime,
} from '../gateway/runtimeGateway';

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
  fetchCalls: Array<{ url: string; token?: string }>;
  setDiscovery(content: string | null): void;
  alive: Set<number>;
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
  timeouts?: Partial<{ launchReadyMs: number; pollIntervalMs: number; shutdownGraceMs: number }>;
}

function makeEnv(options: EnvOptions = {}): Env {
  let discovery = options.discovery ?? null;
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
      env.fetchCalls.push(opts.token === undefined ? { url } : { url, token: opts.token });
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
    sleep: () => Promise.resolve(),
    log: (line) => env.logs.push(line),
    timeouts: { pollIntervalMs: 1, launchReadyMs: 20, shutdownGraceMs: 50, ...options.timeouts },
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

  it('authenticates readiness with the discovery token kept in main-process memory', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    const readiness = env.fetchCalls.find((c) => c.url.endsWith('/api/v1/readiness'));
    expect(readiness?.token).toBe(EXTERNAL_TOKEN);
    // The unauthenticated health probe never sends the token.
    const health = env.fetchCalls.find((c) => c.url.endsWith('/api/v1/health'));
    expect(health?.token).toBeUndefined();
    expectNoTokenLeak(env);
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

  it('rejects paths outside /api/v1 and traversal shapes fail-closed', async () => {
    const env = makeEnv({ discovery: JSON.stringify(discoveryRecord()) });
    await env.gateway.start();
    for (const path of [
      'http://evil.example.com/api/v1/readiness',
      '/etc/passwd',
      '/api/v1/../secrets',
      '/api/v1/readiness?token=x',
      '//evil.example.com/api/v1/readiness',
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
