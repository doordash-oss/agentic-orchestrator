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

/**
 * Composition fence: the real RuntimeGateway's identity/generation
 * transitions driving the production repository services through their
 * registered transport. The per-service tests prove the shared fence against
 * synthetic identity sources; this file proves the composed system — that
 * the gateway transitions the renderer can trigger (explicit bundled-runtime
 * selection, switching away and back, a failed bundled start with Retry or
 * Back) actually move the identity the services capture, so a delayed
 * repository response can never authorize state on the changed connection.
 */

import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import type { ConnectionState, KnownServer, ServersPrefs } from '../../shared/ipc';
import { CloneService } from '../cloneService';
import { CreateService } from '../createService';
import { FeatureService } from '../features';
import { InitializeService } from '../initializeService';
import type { RegistryScan } from '../gateway/registry';
import {
  RuntimeGateway,
  type GatewayDeps,
  type GatewayTimeouts,
  type HttpResult,
  type SelectedRuntime,
  type ServerChildLike,
} from '../gateway/runtimeGateway';
import { type ChildExit } from '../gateway/serverProcess';
import type { ServerIdentitySource } from '../serverFence';

const EMPTY_SCAN: RegistryScan = { candidates: [], pruned: 0, rejected: [] };

const SELECTED: SelectedRuntime = {
  runtimeDir: '/home/ü ser/.agentic-orchestrator',
  stateDir: '/home/ü ser/.agentic-orchestrator/features',
  configPath: '/home/ü ser/.agentic-orchestrator/config.yaml',
};

// The selected runtime dir IS the bundled runtime ("This machine"); beta is
// another local server the user can switch to and back from.
const BUNDLED_KEY = serverKeyFor(SELECTED.runtimeDir);
const BETA_RUNTIME_DIR = '/srv/runtimes/beta';
const BETA_KEY = serverKeyFor(BETA_RUNTIME_DIR);
const ALPHA_BASE = 'http://127.0.0.1:51001';
const BETA_BASE = 'http://127.0.0.1:51002';
const LAUNCH_BASE = 'http://127.0.0.1:50505';
const ALPHA_TOKEN = 'tok-alpha-secret-aaa';
const BETA_TOKEN = 'tok-beta-secret-bbb';
const LAUNCH_TOKEN = 'tok-launched-secret-def';

function serverKeyFor(runtimeDir: string): string {
  return createHash('sha256').update(runtimeDir).digest('hex').slice(0, 32);
}

function compatibleDeclaration(): Record<string, unknown> {
  return {
    api_version: 'v1',
    schema_version: 1,
    min_client_schema: 1,
    runtime_policy: 'loopback-bearer-v1',
    server_build: { version: 'v9.9.9-other-build', revision: 'deadbeef' },
  };
}

function healthFor(runtimeDir: string, name?: string): Record<string, unknown> {
  return {
    api_version: 'v1',
    status: 'ok',
    runtime: {
      runtime_dir: runtimeDir,
      state_dir: `${runtimeDir}/features`,
      config_path: `${runtimeDir}/config.yaml`,
    },
    launch_policy: { resolved: true, providers: [], dangerously_skip_permissions: false },
    started_at: '2026-07-14T00:00:00Z',
    owner: { pid: 4242, started_at: '2026-07-14T00:00:00Z' },
    server_time: '2026-07-14T00:00:01Z',
    compatibility: compatibleDeclaration(),
    ...(name === undefined ? {} : { name }),
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
  spawned: FakeChild[];
  setDiscovery(content: string | null): void;
  alive: Set<number>;
  /** Ordered registry scans consumed by the gateway; the last one repeats. */
  setRegistryScans(scans: RegistryScan[]): void;
  /** Live view of the persisted known-servers prefs. */
  servers(): ServersPrefs;
}

interface EnvOptions {
  /** health body served per base URL; an Error value means network failure */
  health?: Record<string, Record<string, unknown> | Error>;
  /** expected bearer per base URL for a 200 readiness answer */
  readinessTokens?: Record<string, string>;
  binary?: { ok: true; path: string } | { ok: false; tried: string[] };
  /** Scripted registry scans (last repeats when exhausted). Default: empty. */
  registryScans?: RegistryScan[];
}

function makeEnv(options: EnvOptions = {}): Env {
  let discovery: string | null = null;
  let registryScans = [...(options.registryScans ?? [])];
  let serversPrefs: ServersPrefs = { known: [], lastUsed: null };
  const alive = new Set<number>([4242]);
  const health = options.health ?? {
    [ALPHA_BASE]: healthFor(SELECTED.runtimeDir, 'alpha'),
    [BETA_BASE]: healthFor(BETA_RUNTIME_DIR, 'beta'),
    [LAUNCH_BASE]: healthFor(SELECTED.runtimeDir, 'alpha'),
  };
  const readinessTokens = options.readinessTokens ?? {
    [ALPHA_BASE]: ALPHA_TOKEN,
    [BETA_BASE]: BETA_TOKEN,
    [LAUNCH_BASE]: LAUNCH_TOKEN,
  };
  const env = {} as Env;
  const spawned: FakeChild[] = [];

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
    spawnServer: (_binaryPath, _args) => {
      const child = new FakeChild();
      spawned.push(child);
      alive.add(child.pid ?? -1);
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
        started_at: '2026-07-14T00:00:00Z',
      });
      return child;
    },
    registerSecret: () => {},
    sleep: () => Promise.resolve(),
    log: () => {},
    readDiagnosticLines: () => [],
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
    recordAttachedServer: (entry: KnownServer) => {
      serversPrefs = {
        known: [entry, ...serversPrefs.known.filter((k) => k.serverKey !== entry.serverKey)],
        lastUsed: entry.serverKey,
      };
    },
    timeouts: { pollIntervalMs: 1, launchReadyMs: 20, shutdownGraceMs: 50 } as GatewayTimeouts,
  };

  const gateway = new RuntimeGateway(deps);
  const states: ConnectionState[] = [];
  gateway.subscribe((state) => states.push(state));

  Object.assign(env, {
    deps,
    gateway,
    states,
    spawned,
    setDiscovery: (content: string | null) => {
      discovery = content;
    },
    alive,
    setRegistryScans: (scans: RegistryScan[]) => {
      registryScans = [...scans];
    },
    servers: () => serversPrefs,
  });
  return env;
}

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

function betaCandidate() {
  return registryCandidate({
    runtimeDir: BETA_RUNTIME_DIR,
    baseUrl: BETA_BASE,
    token: BETA_TOKEN,
    name: 'beta',
  });
}

function liveBundledCandidate() {
  return registryCandidate({
    runtimeDir: SELECTED.runtimeDir,
    baseUrl: ALPHA_BASE,
    token: ALPHA_TOKEN,
    name: 'alpha',
  });
}

/** Attaches to beta, the server every held operation originates from. */
async function startAtBeta(options: EnvOptions = {}): Promise<Env> {
  const env = makeEnv({
    registryScans: [{ candidates: [betaCandidate()], pruned: 0, rejected: [] }],
    ...options,
  });
  await env.gateway.start();
  expect(env.gateway.getState()).toMatchObject({ status: 'ready', serverKey: BETA_KEY });
  return env;
}

/** Spawns and connects to the bundled runtime, as "Start This machine" does. */
async function startAtBundled(): Promise<Env> {
  const env = makeEnv({ registryScans: [EMPTY_SCAN] });
  await env.gateway.start();
  expect(env.gateway.getState()).toMatchObject({ status: 'ready', serverKey: BUNDLED_KEY });
  return env;
}

interface Services {
  clone: CloneService;
  create: CreateService;
  initialize: InitializeService;
  features: FeatureService;
}

/**
 * The production wiring from main/index.ts: every repository service reads
 * its fence identity from the same gateway getters.
 */
function makeServices(env: Env): Services {
  const identity: ServerIdentitySource = () => ({
    serverKey: env.gateway.connectedServerKey,
    generation: env.gateway.connectionGeneration,
  });
  return {
    clone: new CloneService({ transport: env.gateway, identity }),
    create: new CreateService({ transport: env.gateway, identity }),
    initialize: new InitializeService({ transport: env.gateway, identity }),
    features: new FeatureService({
      transport: env.gateway,
      // Only the source-update and reconciliation paths run here; the
      // creation-composition deps are never invoked by them.
      readReadiness: async () => {
        throw new Error('readReadiness is not part of this composition');
      },
      resolveRepositoryFiles: async () => {
        throw new Error('resolveRepositoryFiles is not part of this composition');
      },
      identity,
    }),
  };
}

const REPO_IDENTITY = {
  path: '/work/space/repo-a',
  commonDir: '/work/space/repo-a/.git',
  device: '1',
  inode: '2',
};
const WIRE_IDENTITY = {
  path: REPO_IDENTITY.path,
  common_dir: REPO_IDENTITY.commonDir,
  device: REPO_IDENTITY.device,
  inode: REPO_IDENTITY.inode,
};

function cloneOperation(): Record<string, unknown> {
  return {
    id: 'clone-0123456789abcdef',
    state: 'accepted',
    stage: 'preparing',
    remote_url: 'https://example.com/acme/widget.git',
    root_path: '/work/space',
    destination: 'widget',
    destination_path: '/work/space/widget',
    idempotency_key: 'key-1-abcdefgh',
    cancel_requested: false,
    created_at: '2026-09-04T12:00:00Z',
    updated_at: '2026-09-04T12:00:00Z',
  };
}

interface OperationCase {
  name: string;
  /** The repository API path this operation posts to. */
  path: string;
  /** The canned success result the held response settles with. */
  success: HttpResult;
  invoke(services: Services): Promise<unknown>;
  assertResult(value: unknown): void;
}

const OPERATIONS: readonly OperationCase[] = [
  {
    name: 'clone',
    path: '/api/v1/workspace/repositories/clone',
    success: {
      status: 202,
      body: { api_version: 'v1', result: 'accepted', operation: cloneOperation() },
    },
    invoke: (services) =>
      services.clone.startClone({
        remoteUrl: 'https://example.com/acme/widget.git',
        rootPath: '/work/space',
        destination: 'widget',
        idempotencyKey: 'key-1-abcdefgh',
      }),
    assertResult: (value) => {
      expect(value).toMatchObject({ id: 'clone-0123456789abcdef', state: 'accepted' });
    },
  },
  {
    name: 'create',
    path: '/api/v1/workspace/repositories/create',
    success: {
      status: 200,
      body: {
        api_version: 'v1',
        result: 'created',
        repository: {
          repo_key: 'widget',
          path: '/work/space/widget',
          has_head: true,
          root: '/work/space',
          identity: WIRE_IDENTITY,
        },
      },
    },
    invoke: (services) =>
      services.create.createRepository({
        rootPath: '/work/space',
        destination: 'widget',
        idempotencyKey: 'key-1-abcdefgh',
        consent: true,
      }),
    assertResult: (value) => {
      expect(value).toMatchObject({ repoKey: 'widget', hasHead: true });
    },
  },
  {
    name: 'initialize',
    path: '/api/v1/workspace/repositories/initialize',
    success: {
      status: 200,
      body: {
        api_version: 'v1',
        result: 'initialized',
        repository: {
          repo_key: 'widget',
          path: '/work/space/widget',
          has_head: true,
          root: '/work/space',
          identity: WIRE_IDENTITY,
        },
      },
    },
    invoke: (services) =>
      services.initialize.initializeRepository({
        repoKey: 'widget',
        identity: REPO_IDENTITY,
        consent: true,
      }),
    assertResult: (value) => {
      expect(value).toMatchObject({ result: 'initialized', repoKey: 'widget' });
    },
  },
  {
    name: 'source-update',
    path: '/api/v1/workspace/repositories/update-source',
    success: {
      status: 200,
      body: {
        api_version: 'v1',
        result: 'updated',
        repo_key: 'repo-a',
        identity: WIRE_IDENTITY,
        mode: 'default',
        branch: 'release/2026/q3',
        origin_branch: 'upstream-main',
        previous_sha: 'a'.repeat(40),
        local_sha: 'c'.repeat(40),
        fetched_sha: 'c'.repeat(40),
      },
    },
    invoke: (services) =>
      services.features.updateRepositorySource({
        repoKey: 'repo-a',
        identity: REPO_IDENTITY,
        mode: 'default',
        branch: 'release/2026/q3',
        originBranch: 'upstream-main',
        expectedLocalSha: 'a'.repeat(40),
        expectedOriginSha: 'c'.repeat(40),
        checkoutHeadRef: 'refs/heads/main',
        checkoutHeadSha: 'e'.repeat(40),
      }),
    assertResult: (value) => {
      expect(value).toMatchObject({ result: 'updated', repoKey: 'repo-a' });
    },
  },
  {
    name: 'reconciliation',
    path: '/api/v1/workspace/repositories/reconcile-source-update',
    success: {
      status: 200,
      body: {
        api_version: 'v1',
        outcome: 'expected_target_present',
        repo_key: 'repo-a',
        identity: WIRE_IDENTITY,
        mode: 'default',
        branch: 'release/2026/q3',
        origin_branch: 'upstream-main',
        local_sha: 'c'.repeat(40),
        selection: {
          repo_key: 'repo-a',
          identity: WIRE_IDENTITY,
          mode: 'default',
          kind: 'branch',
          branch: 'release/2026/q3',
          observed_sha: 'c'.repeat(40),
        },
      },
    },
    invoke: (services) =>
      services.features.reconcileSourceUpdate({
        repoKey: 'repo-a',
        identity: REPO_IDENTITY,
        mode: 'default',
        branch: 'release/2026/q3',
        originBranch: 'upstream-main',
        expectedLocalSha: 'a'.repeat(40),
        expectedOriginSha: 'c'.repeat(40),
      }),
    assertResult: (value) => {
      expect(value).toMatchObject({ outcome: 'expected_target_present', repoKey: 'repo-a' });
    },
  },
];

interface HeldOperation {
  promise: Promise<unknown>;
  release(): void;
  /** Repository API URLs the held request was dispatched to. */
  hits: string[];
}

/**
 * Starts one repository operation and parks its HTTP response on a gate, so
 * the test can move the gateway's connection identity while the request is
 * genuinely in flight — controllable settlement, not a mocked counter.
 */
function holdOperation(env: Env, op: OperationCase): HeldOperation {
  const realFetch = env.deps.fetchJson;
  const hits: string[] = [];
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  env.deps.fetchJson = async (url, opts) => {
    if (url.includes('/api/v1/workspace/repositories/')) {
      hits.push(url);
      await gate;
      return op.success;
    }
    return realFetch(url, opts);
  };
  const services = makeServices(env);
  const promise = op.invoke(services);
  // The request reaches the transport synchronously, so it is already
  // settled-pending on the gate before any transition runs.
  expect(hits).toHaveLength(1);
  expect(hits[0]).toContain(op.path);
  return { promise, release, hits };
}

async function expectDiscarded(held: HeldOperation): Promise<void> {
  held.release();
  await expect(held.promise).rejects.toMatchObject({
    canonical: { code: 'E_SERVER_SWITCHED' },
  });
  // A fenced discard is never retried behind the renderer's back.
  expect(held.hits).toHaveLength(1);
}

describe('repository services fenced by real gateway transitions', () => {
  it.each(OPERATIONS)(
    '$name: a delayed response cannot authorize results across an explicit bundled-runtime selection',
    async (op) => {
      const env = await startAtBeta();
      const generationBefore = env.gateway.connectionGeneration;
      const held = holdOperation(env, op);

      const state = await env.gateway.startLocal();

      expect(state).toMatchObject({ status: 'ready', serverKey: BUNDLED_KEY, kind: 'local' });
      expect(env.gateway.connectedServerKey).toBe(BUNDLED_KEY);
      // The explicit local start moved the identity the services capture.
      expect(env.gateway.connectionGeneration).toBeGreaterThan(generationBefore);
      await expectDiscarded(held);
    },
  );

  it.each(OPERATIONS)(
    '$name: a delayed response cannot authorize results after switching away and back to the same server',
    async (op) => {
      const env = await startAtBeta();
      env.setRegistryScans([
        { candidates: [betaCandidate(), liveBundledCandidate()], pruned: 0, rejected: [] },
      ]);
      const generationBefore = env.gateway.connectionGeneration;
      const held = holdOperation(env, op);

      const away = await env.gateway.switchServer({ serverKey: BUNDLED_KEY });
      expect(away).toMatchObject({ status: 'ready', serverKey: BUNDLED_KEY });
      const back = await env.gateway.switchServer({ serverKey: BETA_KEY });
      expect(back).toMatchObject({ status: 'ready', serverKey: BETA_KEY });

      // Same server key as the request started on, but a later generation:
      // only the fence's generation half can catch this A → B → A flight.
      expect(env.gateway.connectedServerKey).toBe(BETA_KEY);
      expect(env.gateway.connectionGeneration).toBeGreaterThan(generationBefore);
      await expectDiscarded(held);
    },
  );

  it.each(OPERATIONS)(
    '$name: a delayed response cannot authorize results after a failed bundled start and Back',
    async (op) => {
      const env = await startAtBeta({ binary: { ok: false, tried: ['/nowhere'] } });
      const held = holdOperation(env, op);

      const failed = await env.gateway.startLocal();
      expect(failed.status).toBe('error');
      if (failed.status !== 'error' || failed.switchContext === undefined) {
        throw new Error('expected a switch failure context');
      }
      expect(failed.switchContext.startLocal).toBe(true);
      expect(failed.switchContext.previous).toMatchObject({ serverKey: BETA_KEY });

      // Back re-attaches the originating server through the standard path.
      const back = await env.gateway.switchServer({ serverKey: BETA_KEY });
      expect(back).toMatchObject({ status: 'ready', serverKey: BETA_KEY });
      await expectDiscarded(held);

      // Recovery on the re-established connection is a fresh, succeeding
      // request — the discard never blocks the retry the user can see.
      const services = makeServices(env);
      op.assertResult(await op.invoke(services));
    },
  );

  it.each(OPERATIONS)(
    '$name: a delayed response cannot authorize results after a failed bundled start and Retry',
    async (op) => {
      const env = await startAtBeta({ binary: { ok: false, tried: ['/nowhere'] } });
      const held = holdOperation(env, op);

      const failed = await env.gateway.startLocal();
      expect(failed.status).toBe('error');

      // Retry re-invokes the bundled start once the runtime is available.
      env.deps.resolveServerBinary = () => ({ ok: true, path: '/rés dir/bin/agentico' });
      const retried = await env.gateway.startLocal();
      expect(retried).toMatchObject({ status: 'ready', serverKey: BUNDLED_KEY });
      await expectDiscarded(held);
    },
  );

  it.each(OPERATIONS)(
    '$name: selecting the already-connected bundled runtime is a no-op that preserves the result',
    async (op) => {
      const env = await startAtBundled();
      const statesBefore = env.states.length;
      const generationBefore = env.gateway.connectionGeneration;
      const held = holdOperation(env, op);

      const ready = env.gateway.getState();
      const state = await env.gateway.startLocal();

      // The existing no-op contract: same state, no transition, no bump.
      expect(state).toBe(ready);
      expect(env.states.length).toBe(statesBefore);
      expect(env.gateway.connectionGeneration).toBe(generationBefore);

      held.release();
      op.assertResult(await held.promise);
    },
  );

  it.each(OPERATIONS)('$name: normal same-connection results still succeed', async (op) => {
    const env = await startAtBeta();
    const held = holdOperation(env, op);

    held.release();
    op.assertResult(await held.promise);
    expect(held.hits).toHaveLength(1);
  });
});
