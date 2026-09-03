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

import { describe, expect, it } from 'vitest';
import { SafeErrorException } from '../../shared/errors';
import { applyServersPatch, type ServersPrefs } from '../../shared/ipc';
import { serverKeyForBaseUrl } from '../connectionString';
import {
  ADD_REMOTE_PROBE_MS,
  addRemoteServer,
  type AddRemoteServerDeps,
} from '../gateway/addRemoteServer';
import type { HttpResult } from '../gateway/runtimeGateway';

const TOKEN = 'tok-secret-xyz';
const BASE_URL = 'http://10.1.2.3:8080';
const CONNECTION_STRING = `agentico://${TOKEN}@10.1.2.3:8080`;
const SERVER_KEY = serverKeyForBaseUrl(BASE_URL);
const STATE_DIR = '/srv/remote/features';

const COMPATIBILITY = {
  api_version: 'v1',
  schema_version: 1,
  min_client_schema: 1,
  runtime_policy: 'network-bearer-v1',
  server_build: { version: 'v1.2.3' },
};

function healthyBody(extra: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    status: 'ok',
    compatibility: COMPATIBILITY,
    runtime: { state_dir: STATE_DIR },
    ...extra,
  };
}

type Handler = (options: { token?: string }) => HttpResult | Promise<never>;

interface Harness {
  prefs: ServersPrefs;
  blobs: Map<string, string>;
  secrets: string[];
  logs: string[];
  answers: Map<string, Handler>;
  encryptionAvailable: boolean;
  deps: AddRemoteServerDeps;
  run(connectionString?: string): Promise<unknown>;
}

function makeHarness(): Harness {
  const harness = {} as Harness;
  harness.prefs = { known: [], lastUsed: null };
  harness.blobs = new Map();
  harness.secrets = [];
  harness.logs = [];
  harness.answers = new Map();
  harness.encryptionAvailable = true;

  const deps: AddRemoteServerDeps = {
    fetchJson: async (url, options) => {
      const handler = harness.answers.get(url);
      if (handler === undefined) {
        throw new Error('connection refused');
      }
      return handler(options);
    },
    scanRegistry: () => ({ candidates: [], pruned: 0, rejected: [] }),
    knownServers: () => harness.prefs,
    upsertRemoteEntry: (entry) => {
      harness.prefs = applyServersPatch(harness.prefs, { upsertKnown: entry });
    },
    remoteTokens: {
      save: (serverKey, token) => {
        harness.secrets.push(token);
        if (!harness.encryptionAvailable) {
          return { status: 'unavailable' };
        }
        harness.blobs.set(serverKey, token);
        return { status: 'saved' };
      },
    },
    registerSecret: (secret) => harness.secrets.push(secret),
    log: (line) => harness.logs.push(line),
    now: () => Date.parse('2026-08-10T12:00:00.000Z'),
    timeouts: { healthProbeMs: ADD_REMOTE_PROBE_MS },
  };
  harness.deps = deps;
  harness.run = (connectionString = CONNECTION_STRING) =>
    addRemoteServer({ connectionString }, deps);
  return harness;
}

/** Happy-path answers: healthy probe with an optional name, token-checked readiness. */
function scriptHappyPath(harness: Harness, options: { name?: string } = {}): void {
  harness.answers.set(`${BASE_URL}/api/v1/health`, () => ({
    status: 200,
    body: healthyBody(options.name === undefined ? {} : { name: options.name }),
  }));
  harness.answers.set(`${BASE_URL}/api/v1/readiness`, ({ token }) => ({
    status: token === TOKEN ? 200 : 401,
    body: {},
  }));
}

function registryCandidate(serverKey: string, stateDir: string) {
  return {
    serverKey,
    runtimeDir: '/rt/alpha',
    record: {
      schema_version: 1 as const,
      api_version: 'v1' as const,
      base_url: 'http://127.0.0.1:51001',
      auth_token: 'tok-registry',
      name: 'alpha',
      runtime: {
        runtime_dir: '/rt/alpha',
        state_dir: stateDir,
        config_path: '/rt/alpha/config.yaml',
      },
      pid: 4242,
      started_at: '2026-07-14T00:00:00Z',
    },
  };
}

describe('addRemoteServer', () => {
  it('happy path: probe name wins, entry + token blob persisted, added returned', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness, { name: 'buildbox' });

    const result = await harness.run();

    expect(result).toEqual({ status: 'added', serverKey: SERVER_KEY });
    expect(harness.prefs.known).toEqual([
      {
        serverKey: SERVER_KEY,
        kind: 'remote',
        name: 'buildbox',
        baseUrl: BASE_URL,
        lastSeenAt: '2026-08-10T12:00:00.000Z',
      },
    ]);
    expect(harness.blobs.get(SERVER_KEY)).toBe(TOKEN);
    expect(harness.prefs.lastUsed).toBeNull();
  });

  it('happy path: connection-string name wins over the probe name', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness, { name: 'buildbox' });

    await harness.run(`${CONNECTION_STRING}?name=pasted-name`);

    expect(harness.prefs.known[0]?.name).toBe('pasted-name');
  });

  it('happy path: host-derived fallback name when neither string nor probe carry one', async () => {
    const harness = makeHarness();
    harness.answers.set(`${BASE_URL}/api/v1/health`, () => ({
      status: 200,
      body: { status: 'ok', compatibility: COMPATIBILITY },
    }));
    harness.answers.set(`${BASE_URL}/api/v1/readiness`, () => ({ status: 200, body: {} }));

    await harness.run();

    expect(harness.prefs.known[0]?.name).toBe('10.1.2.3:8080');
  });

  it('wrong token (401): E_REMOTE_AUTH_REJECTED with remediation, nothing persisted', async () => {
    const harness = makeHarness();
    harness.answers.set(`${BASE_URL}/api/v1/health`, () => ({ status: 200, body: healthyBody() }));
    harness.answers.set(`${BASE_URL}/api/v1/readiness`, () => ({ status: 401, body: {} }));

    await expect(harness.run()).rejects.toMatchObject({
      safe: {
        code: 'E_REMOTE_AUTH_REJECTED',
        remediation: expect.stringContaining('FULL connection string'),
      },
    });
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
  });

  it('dead host: E_REMOTE_UNREACHABLE with remediation, nothing persisted', async () => {
    const harness = makeHarness();
    // No answers scripted: every fetch throws.

    await expect(harness.run()).rejects.toSatisfy(
      (err: unknown) =>
        err instanceof SafeErrorException &&
        err.safe.code === 'E_REMOTE_UNREACHABLE' &&
        err.safe.remediation !== undefined,
    );
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
  });

  it('incompatible server (unknown runtime policy): E_REMOTE_INCOMPATIBLE, nothing persisted', async () => {
    const harness = makeHarness();
    harness.answers.set(`${BASE_URL}/api/v1/health`, () => ({
      status: 200,
      body: healthyBody({
        compatibility: { ...COMPATIBILITY, runtime_policy: 'mystery-v9' },
      }),
    }));
    harness.answers.set(`${BASE_URL}/api/v1/readiness`, () => ({ status: 200, body: {} }));

    await expect(harness.run()).rejects.toMatchObject({
      safe: { code: 'E_REMOTE_INCOMPATIBLE' },
    });
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
  });

  it('malformed string: parse error code surfaces as-is, nothing persisted', async () => {
    const harness = makeHarness();

    await expect(harness.run('https://no-scheme.example')).rejects.toMatchObject({
      safe: { code: 'E_CONNECTION_STRING_SCHEME' },
    });
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
  });

  it('duplicate guard: registry candidate with matching state_dir → duplicate-local steering', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness);
    const localKey = 'a'.repeat(32);
    harness.deps.scanRegistry = () => ({
      candidates: [registryCandidate(localKey, STATE_DIR)],
      pruned: 0,
      rejected: [],
    });

    const result = await harness.run();

    expect(result).toEqual({ status: 'duplicate-local', serverKey: localKey });
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
  });

  it('duplicate guard: persisted known-local entry with matching state dir → duplicate-local', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness);
    const localKey = 'b'.repeat(32);
    harness.prefs = {
      known: [
        {
          serverKey: localKey,
          kind: 'local',
          name: 'alpha',
          baseUrl: 'http://127.0.0.1:51001',
          runtimeDir: '/srv/remote',
          lastSeenAt: '2026-07-14T00:00:00Z',
        },
      ],
      lastUsed: null,
    };

    const result = await harness.run();

    expect(result).toEqual({ status: 'duplicate-local', serverKey: localKey });
    expect(harness.prefs.known).toHaveLength(1);
    expect(harness.blobs.size).toBe(0);
  });

  it('session-only: keystore unavailable → connects but persists nothing', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness);
    harness.encryptionAvailable = false;

    const result = await harness.run();

    expect(result).toEqual({ status: 'session-only', serverKey: SERVER_KEY });
    expect(harness.prefs.known).toEqual([]);
    expect(harness.blobs.size).toBe(0);
    // The token was still registered for log redaction on receipt.
    expect(harness.secrets).toContain(TOKEN);
  });

  it('security: the pasted string and token appear in no result, log, or persisted shape', async () => {
    const harness = makeHarness();
    scriptHappyPath(harness);

    const added = await harness.run();
    const duplicateHarness = makeHarness();
    duplicateHarness.deps.scanRegistry = () => ({
      candidates: [registryCandidate('c'.repeat(32), STATE_DIR)],
      pruned: 0,
      rejected: [],
    });
    duplicateHarness.answers.set(`${BASE_URL}/api/v1/health`, () => ({
      status: 200,
      body: healthyBody(),
    }));
    const duplicate = await duplicateHarness.run();
    const sessionOnlyHarness = makeHarness();
    sessionOnlyHarness.encryptionAvailable = false;
    scriptHappyPath(sessionOnlyHarness);
    const sessionOnly = await sessionOnlyHarness.run();

    for (const outcome of [added, duplicate, sessionOnly]) {
      expect(JSON.stringify(outcome)).not.toContain(TOKEN);
      expect(JSON.stringify(outcome)).not.toContain(CONNECTION_STRING);
    }
    for (const harnessToCheck of [harness, duplicateHarness, sessionOnlyHarness]) {
      const emitted = JSON.stringify({
        logs: harnessToCheck.logs,
        prefs: harnessToCheck.prefs,
      });
      expect(emitted).not.toContain(TOKEN);
      expect(emitted).not.toContain(CONNECTION_STRING);
    }

    // Failure surfaces also never echo: the malformed-string error name-checks
    // the scheme only.
    const failing = makeHarness();
    const error = await failing.run('agentico://tok-secret-xyz@').catch((err: unknown) => err);
    expect(JSON.stringify(error)).not.toContain(TOKEN);
  });
});
