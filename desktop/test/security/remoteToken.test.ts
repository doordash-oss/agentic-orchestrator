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
 * Remote-token hygiene: the pasted remote bearer and anything derived from
 * it must never appear in emitted connection states, switcher snapshots,
 * IPC payloads, log buffers, settings.json bytes, or the token-store file
 * bytes. Includes a hostile remote server that echoes the token back in
 * server-controlled text (its health `name` and its error bodies) — those
 * strings are scrubbed at the boundary before they ride state or settings.
 */
import { createCipheriv, createDecipheriv, randomBytes } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  AppRouteEventSchema,
  ConnectionStateSchema,
  RemoteServerAddRequestSchema,
  RemoteServerAddResultSchema,
  ServerRemoveRequestSchema,
  ServerTokenStatusRequestSchema,
  ServerTokenStatusResultSchema,
  type ConnectionState,
  type KnownServer,
  type ServersPrefs,
} from '../../src/shared/ipc';
import { serverKeyForBaseUrl } from '../../src/main/connectionString';
import { addRemoteServer, type AddRemoteServerDeps } from '../../src/main/gateway/addRemoteServer';
import { RedactedLogBuffer } from '../../src/main/gateway/logBuffer';
import type { RegistryScan } from '../../src/main/gateway/registry';
import {
  RemoteTokenStore,
  type LoadResult,
  type SafeStorageLike,
} from '../../src/main/gateway/remoteTokenStore';
import {
  RuntimeGateway,
  type GatewayDeps,
  type HttpResult,
} from '../../src/main/gateway/runtimeGateway';
import { ServerListService } from '../../src/main/gateway/serverListService';
import { SettingsStore } from '../../src/main/settings';

const REMOTE_BASE = 'http://10.9.8.7:8080';
const REMOTE_TOKEN = 'tok-remote-secret-zzz-0123456789';

function remoteKey(): string {
  return serverKeyForBaseUrl(REMOTE_BASE);
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

function healthBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    status: 'ok',
    runtime: { state_dir: '/srv/remote/features' },
    compatibility: {
      api_version: 'v1',
      schema_version: 1,
      min_client_schema: 1,
      runtime_policy: 'network-bearer-v1',
      server_build: { version: 'v9.9.9-remote' },
    },
    ...overrides,
  };
}

/** In-memory RemoteTokenStore stand-in with a scriptable load outcome. */
class FakeRemoteTokens {
  forcedStatus: 'absent' | 're-paste-required' | null = null;

  constructor(
    private readonly token: string,
    private readonly register: (secret: string) => void,
  ) {}

  load(): LoadResult {
    const forced = this.forcedStatus;
    if (forced !== null) {
      return { status: forced };
    }
    return { status: 'ok', token: this.token };
  }
}

interface RemoteEnv {
  gateway: RuntimeGateway;
  states: ConnectionState[];
  logs: string[];
  urls: string[];
  attachRecords: KnownServer[];
  secrets: string[];
  tokens: FakeRemoteTokens;
}

function makeRemoteEnv(options: {
  remoteHealth?: Record<string, unknown> | Error;
  readinessStatus?: number;
  readinessBody?: unknown;
  tokenStatus?: 'absent' | 're-paste-required';
}): RemoteEnv {
  const env = {} as RemoteEnv;
  Object.assign(env, {
    states: [] as ConnectionState[],
    logs: [] as string[],
    urls: [] as string[],
    attachRecords: [] as KnownServer[],
    secrets: [] as string[],
  });
  let prefs: ServersPrefs = { known: [remoteEntry()], lastUsed: remoteKey() };
  env.tokens = new FakeRemoteTokens(REMOTE_TOKEN, (secret) => {
    env.secrets.push(secret);
  });
  if (options.tokenStatus !== undefined) {
    env.tokens.forcedStatus = options.tokenStatus;
  }
  const deps: GatewayDeps = {
    selectRuntime: () => ({
      runtimeDir: '/rt',
      stateDir: '/rt/features',
      configPath: '/rt/c.yaml',
    }),
    discovery: {
      readFile: () => {
        throw new Error('ENOENT');
      },
      statFile: () => null,
      euid: 501,
      isProcessAlive: () => false,
    },
    fetchJson: async (url) => {
      env.urls.push(url);
      const remoteHealth = options.remoteHealth ?? healthBody();
      if (url.endsWith('/api/v1/health')) {
        if (remoteHealth instanceof Error) {
          throw remoteHealth;
        }
        return { status: 200, body: remoteHealth };
      }
      if (url.endsWith('/api/v1/readiness')) {
        return {
          status: options.readinessStatus ?? 200,
          body: options.readinessBody ?? { api_version: 'v1' },
        };
      }
      throw new Error(`unexpected url ${url}`);
    },
    resolveServerBinary: () => ({ ok: true, path: '/bin/agentico' }),
    spawnServer: () => {
      throw new Error('a remote attach must never spawn');
    },
    registerSecret: (secret) => {
      env.secrets.push(secret);
    },
    sleep: () => Promise.resolve(),
    log: (line) => {
      env.logs.push(line);
    },
    scanRegistry: (): RegistryScan => ({ candidates: [], pruned: 0, rejected: [] }),
    knownServers: () => prefs,
    recordAttachedServer: (entry) => {
      env.attachRecords.push(entry);
      prefs = { known: [entry], lastUsed: entry.serverKey };
    },
    remoteTokens: { load: () => env.tokens.load() },
    timeouts: { pollIntervalMs: 1 },
  };
  env.gateway = new RuntimeGateway(deps);
  env.gateway.subscribe((state) => {
    env.states.push(state);
  });
  return env;
}

function expectTokenFree(env: RemoteEnv): void {
  for (const state of env.states) {
    // The strict schema is the IPC contract: parse what actually crosses.
    ConnectionStateSchema.parse(state);
    expect(JSON.stringify(state)).not.toContain(REMOTE_TOKEN);
  }
  expect(JSON.stringify(env.logs)).not.toContain(REMOTE_TOKEN);
  for (const url of env.urls) {
    expect(url).not.toContain(REMOTE_TOKEN);
  }
  expect(JSON.stringify(env.attachRecords)).not.toContain(REMOTE_TOKEN);
}

describe('remote token isolation across attach outcomes', () => {
  const cases: Array<{
    name: string;
    options: Parameters<typeof makeRemoteEnv>[0];
    status: string;
    errorCode?: string;
  }> = [
    { name: 'ready attach', options: {}, status: 'ready' },
    {
      name: 'absent token',
      options: { tokenStatus: 'absent' },
      status: 'error',
      errorCode: 'E_REMOTE_TOKEN_MISSING',
    },
    {
      name: 'undecryptable token',
      options: { tokenStatus: 're-paste-required' },
      status: 'error',
      errorCode: 'E_REMOTE_TOKEN_UNREADABLE',
    },
    {
      name: 'unreachable server',
      options: { remoteHealth: new Error('connection refused') },
      status: 'error',
      errorCode: 'E_REMOTE_HEALTH_UNANSWERED',
    },
    {
      name: 'rejected credentials',
      options: { readinessStatus: 401, readinessBody: { message: 'denied' } },
      status: 'error',
      errorCode: 'E_REMOTE_STORED_TOKEN_REJECTED',
    },
    {
      name: 'incompatible server',
      options: { remoteHealth: healthBody({ compatibility: { api_version: 'v9' } }) },
      status: 'incompatible',
      errorCode: 'E_INCOMPATIBLE_SERVER',
    },
  ];

  for (const entry of cases) {
    it(`stays token-free for a ${entry.name}`, async () => {
      const env = makeRemoteEnv(entry.options);
      await env.gateway.start();
      expect(env.gateway.getState().status).toBe(entry.status);
      if (entry.errorCode !== undefined) {
        const state = env.gateway.getState();
        if (state.status !== 'error' && state.status !== 'incompatible') {
          throw new Error('unreachable');
        }
        expect(state.error.code).toBe(entry.errorCode);
      }
      expect(env.gateway.getState().serverKey ?? '').not.toContain(REMOTE_TOKEN);
      expectTokenFree(env);
    });
  }
});

describe('hostile remote server echoing the token', () => {
  it('a health name echoing the token cannot leak it into states or persisted entries', async () => {
    // The remote server owns its health payload and knows the token the app
    // will present: it reports its display name as the token itself.
    const env = makeRemoteEnv({ remoteHealth: healthBody({ name: REMOTE_TOKEN }) });
    await env.gateway.start();

    expect(env.gateway.getState().status).toBe('ready');
    // The scrubbed boundary: the name survives as display text, the token does not.
    expect(env.gateway.getState().serverName).toBe('[redacted]');
    expect(env.attachRecords).toHaveLength(1);
    expect(env.attachRecords[0]!.name).toBe('[redacted]');
    expectTokenFree(env);
  });

  it('an auth-failure body echoing the token stays out of states and logs', async () => {
    const env = makeRemoteEnv({
      readinessStatus: 401,
      readinessBody: { message: `bad Bearer ${REMOTE_TOKEN}` },
    });
    await env.gateway.start();

    const state = env.gateway.getState();
    if (state.status !== 'error') {
      throw new Error('unreachable');
    }
    expect(state.error.code).toBe('E_REMOTE_STORED_TOKEN_REJECTED');
    // Neither the fixed diagnostics nor the canonical error echo server body text.
    expect(state.error.summary).not.toContain(REMOTE_TOKEN);
    expect(state.error.diagnostics ?? '').not.toContain(REMOTE_TOKEN);
    expectTokenFree(env);
  });

  it('the switcher snapshot and its persisted probe writes never carry token or base URL', async () => {
    const logBuffer = new RedactedLogBuffer(10);
    logBuffer.addSecret(REMOTE_TOKEN);
    const persisted: Array<{ name?: string; lastSeenAt?: string }> = [];
    const snapshots: string[] = [];
    // The hostile server replays the previously presented token as its name.
    const service = new ServerListService({
      scanRegistry: () => ({ candidates: [], pruned: 0, rejected: [] }),
      knownServers: () => ({ known: [remoteEntry()], lastUsed: remoteKey() }),
      currentServerKey: () => remoteKey(),
      fetchJson: async (): Promise<HttpResult> => ({
        status: 200,
        body: { status: 'ok', name: REMOTE_TOKEN },
      }),
      recordProbedServer: (_serverKey, patch) => {
        persisted.push(patch);
      },
      log: () => undefined,
      scrubProbeText: (text) => logBuffer.scrub(text),
    });
    service.subscribe((snapshot) => {
      snapshots.push(JSON.stringify(snapshot));
    });

    service.setOpen(true);
    for (let i = 0; i < 20; i += 1) {
      await Promise.resolve();
    }
    service.dispose();

    const rows = service.list().rows;
    expect(rows).toHaveLength(1);
    expect(rows[0]!.health).toBe('healthy');
    for (const emitted of snapshots) {
      expect(emitted).not.toContain(REMOTE_TOKEN);
      expect(emitted).not.toContain(REMOTE_BASE);
    }
    for (const patch of persisted) {
      expect(JSON.stringify(patch)).not.toContain(REMOTE_TOKEN);
    }
    // The replayed token lands as a scrubbed persisted name, not the token.
    expect(persisted.some((patch) => patch.name === '[redacted]')).toBe(true);
  });
});

describe('add-remote-server paste flow hygiene', () => {
  interface AddRecord {
    logs: string[];
    upserted: KnownServer[];
    savedTokens: Array<{ serverKey: string; token: string }>;
    healthOverrides?: Record<string, unknown>;
  }

  function makeRecord(healthOverrides?: Record<string, unknown>): AddRecord {
    return { logs: [], upserted: [], savedTokens: [], healthOverrides: healthOverrides ?? {} };
  }

  function makeAddDeps(record: AddRecord): AddRemoteServerDeps {
    return {
      fetchJson: async (url): Promise<HttpResult> => {
        if (url.endsWith('/api/v1/health')) {
          return { status: 200, body: healthBody(record.healthOverrides ?? {}) };
        }
        if (url.endsWith('/api/v1/readiness')) {
          return { status: 200, body: { api_version: 'v1' } };
        }
        throw new Error(`unexpected url ${url}`);
      },
      scanRegistry: () => ({ candidates: [], pruned: 0, rejected: [] }),
      knownServers: () => ({ known: [], lastUsed: null }),
      upsertRemoteEntry: (entry) => {
        record.upserted.push(entry);
      },
      remoteTokens: {
        save: (serverKey, token) => {
          record.savedTokens.push({ serverKey, token });
          return { status: 'saved' };
        },
      },
      registerSecret: () => undefined,
      log: (line) => {
        record.logs.push(line);
      },
    };
  }

  it('the result, logs, and persisted entry never echo the connection string or token', async () => {
    const record = makeRecord();
    const result = await addRemoteServer(
      { connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080` },
      makeAddDeps(record),
    );

    expect(result).toEqual({ status: 'added', serverKey: remoteKey() });
    expect(JSON.stringify(result)).not.toContain(REMOTE_TOKEN);
    expect(JSON.stringify(record.logs)).not.toContain(REMOTE_TOKEN);
    expect(JSON.stringify(record.upserted)).not.toContain(REMOTE_TOKEN);
    expect(record.upserted[0]).toMatchObject({ serverKey: remoteKey(), kind: 'remote' });
    // The token reaches only the encrypted store's save call.
    expect(record.savedTokens).toEqual([{ serverKey: remoteKey(), token: REMOTE_TOKEN }]);
  });

  it('a ?name= crafted to echo the token is scrubbed before it reaches settings', async () => {
    const record = makeRecord();
    await addRemoteServer(
      { connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080?name=${REMOTE_TOKEN}` },
      makeAddDeps(record),
    );

    expect(record.upserted[0]!.name).toBe('[redacted]');
    expect(JSON.stringify(record.upserted)).not.toContain(REMOTE_TOKEN);
  });

  it('failure paths log fixed codes, never the pasted string', async () => {
    const record = makeRecord();
    const deps = makeAddDeps(record);
    deps.fetchJson = async () => {
      throw new Error('connection refused');
    };
    await expect(
      addRemoteServer({ connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080` }, deps),
    ).rejects.toMatchObject({ canonical: { code: 'E_REMOTE_UNREACHABLE' } });

    expect(JSON.stringify(record.logs)).not.toContain(REMOTE_TOKEN);
    expect(record.upserted).toHaveLength(0);
    expect(record.savedTokens).toHaveLength(0);
  });
});

describe('on-disk bytes', () => {
  /** real-cipher safeStorage stand-in: ciphertext must never contain the token. */
  function cryptoFakeSafeStorage(): SafeStorageLike {
    const key = randomBytes(32);
    return {
      isEncryptionAvailable: () => true,
      encryptString: (plain) => {
        const iv = randomBytes(12);
        const cipher = createCipheriv('aes-256-gcm', key, iv);
        const enc = Buffer.concat([cipher.update(plain, 'utf8'), cipher.final()]);
        return Buffer.concat([iv, cipher.getAuthTag(), enc]);
      },
      decryptString: (blob) => {
        const iv = blob.subarray(0, 12);
        const tag = blob.subarray(12, 28);
        const enc = blob.subarray(28);
        const decipher = createDecipheriv('aes-256-gcm', key, iv);
        decipher.setAuthTag(tag);
        return Buffer.concat([decipher.update(enc), decipher.final()]).toString('utf8');
      },
    };
  }

  it('settings.json bytes with remote entries carry no token material', () => {
    const dir = mkdtempSync(path.join(tmpdir(), 'agentico-security-'));
    try {
      const store = new SettingsStore(dir, { warn: () => undefined });
      store.update({
        servers: {
          upsertKnown: remoteEntry({ nickname: 'my box' }),
          lastUsed: remoteKey(),
        },
      });

      const bytes = readFileSync(path.join(dir, 'settings.json'), 'utf8');
      expect(bytes).not.toContain(REMOTE_TOKEN);
      expect(bytes).not.toContain('agentico://');
      expect(bytes).toContain(remoteKey());
      expect(bytes).toContain(REMOTE_BASE);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it('the remote token store writes only an encrypted blob and survives a round trip', () => {
    const dir = mkdtempSync(path.join(tmpdir(), 'agentico-security-'));
    try {
      const file = path.join(dir, 'remote-tokens.json');
      const store = new RemoteTokenStore(file, {
        safeStorage: cryptoFakeSafeStorage(),
        registerSecret: () => undefined,
      });

      expect(store.save(remoteKey(), REMOTE_TOKEN)).toEqual({ status: 'saved' });
      const bytes = readFileSync(file, 'utf8');
      const doc = JSON.parse(bytes) as { tokens: Record<string, string> };
      expect(doc.tokens[remoteKey()]).toBeDefined();
      expect(bytes).not.toContain(REMOTE_TOKEN);
      expect(bytes).not.toContain('agentico://');

      // Round trip: decrypts back to the token; removal drops the blob.
      expect(store.load(remoteKey())).toEqual({ status: 'ok', token: REMOTE_TOKEN });
      store.remove(remoteKey());
      expect(JSON.parse(readFileSync(file, 'utf8')).tokens[remoteKey()]).toBeUndefined();
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it('the log buffer scrubs the registered remote token, including retroactively', () => {
    const buffer = new RedactedLogBuffer(10);
    buffer.append(`child printed ${REMOTE_TOKEN} early\n`);
    // The store registers on load, possibly after the child printed the secret.
    buffer.addSecret(REMOTE_TOKEN);
    buffer.append(`child printed ${REMOTE_TOKEN} again`);

    const snapshot = buffer.snapshot().join('\n');
    expect(snapshot).not.toContain(REMOTE_TOKEN);
    expect(snapshot).toContain('[redacted]');
  });
});

describe('remote IPC contracts are fail-closed', () => {
  it('remote requests reject token-shaped extra fields', () => {
    const key = 'a'.repeat(32);
    for (const bad of [
      RemoteServerAddRequestSchema.safeParse({
        connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080`,
        token: REMOTE_TOKEN,
      }),
      RemoteServerAddRequestSchema.safeParse({
        connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080`,
        authToken: REMOTE_TOKEN,
      }),
      ServerRemoveRequestSchema.safeParse({ serverKey: key, token: REMOTE_TOKEN }),
      ServerRemoveRequestSchema.safeParse({ serverKey: key, connectionString: 'agentico://x' }),
      ServerTokenStatusRequestSchema.safeParse({ serverKey: key, token: REMOTE_TOKEN }),
    ]) {
      expect(bad.success).toBe(false);
    }
  });

  it('remote responses cannot represent token material', () => {
    const key = 'a'.repeat(32);
    // Valid shapes parse.
    expect(RemoteServerAddResultSchema.parse({ status: 'added', serverKey: key })).toEqual({
      status: 'added',
      serverKey: key,
    });
    expect(ServerTokenStatusResultSchema.parse({ status: 're-paste-required' })).toEqual({
      status: 're-paste-required',
    });
    // Extra token-shaped fields or out-of-enum statuses never validate.
    for (const bad of [
      RemoteServerAddResultSchema.safeParse({
        status: 'added',
        serverKey: key,
        token: REMOTE_TOKEN,
      }),
      RemoteServerAddResultSchema.safeParse({
        status: 'session-only',
        serverKey: key,
        connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080`,
      }),
      RemoteServerAddResultSchema.safeParse({ status: REMOTE_TOKEN, serverKey: key }),
      ServerTokenStatusResultSchema.safeParse({ status: 'saved', token: REMOTE_TOKEN }),
      ServerTokenStatusResultSchema.safeParse({ status: REMOTE_TOKEN }),
    ]) {
      expect(bad.success).toBe(false);
    }
  });

  it('route-requested focus re-validates the settings intent fail-closed', () => {
    // The deep-link that focuses Servers → Add Server is the one allowed focus.
    expect(AppRouteEventSchema.parse({ target: 'settings', settingsFocus: 'add-server' })).toEqual({
      target: 'settings',
      settingsFocus: 'add-server',
    });
    for (const bad of [
      AppRouteEventSchema.safeParse({ target: 'settings', settingsFocus: REMOTE_TOKEN }),
      AppRouteEventSchema.safeParse({ target: 'settings', settingsFocus: 'secrets' }),
      AppRouteEventSchema.safeParse({ target: 'settings', token: REMOTE_TOKEN }),
      AppRouteEventSchema.safeParse({
        target: 'settings',
        settingsFocus: 'add-server',
        connectionString: `agentico://${REMOTE_TOKEN}@10.9.8.7:8080`,
      }),
    ]) {
      expect(bad.success).toBe(false);
    }
  });
});
