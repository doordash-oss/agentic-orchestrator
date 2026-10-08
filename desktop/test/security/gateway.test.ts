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
 * Security properties of the runtime gateway:
 *  - bearer material never leaves the main process (not in states, IPC
 *    payloads, URLs, or logs),
 *  - the server child is spawned via argv array without shell interpolation,
 *  - external servers are never signalled.
 */
import { describe, expect, it, vi } from 'vitest';
import { ConnectionStateSchema, type ConnectionState } from '../../src/shared/ipc';
import { isAllowedApiPath } from '../../src/main/gateway/apiPaths';
import { RedactedLogBuffer } from '../../src/main/gateway/logBuffer';
import { ManagedServerProcess } from '../../src/main/gateway/serverProcess';
import {
  RuntimeGateway,
  type GatewayDeps,
  type ServerChildLike,
} from '../../src/main/gateway/runtimeGateway';

const TOKEN = 'tok-super-secret-0123456789';
const BASE = 'http://127.0.0.1:45678';
const SELECTED = {
  runtimeDir: '/rt',
  stateDir: '/rt/features',
  configPath: '/rt/config.yaml',
};

function discoveryContent(): string {
  return JSON.stringify({
    schema_version: 1,
    api_version: 'v1',
    base_url: BASE,
    auth_token: TOKEN,
    runtime: { runtime_dir: '/rt', state_dir: '/rt/features', config_path: '/rt/config.yaml' },
    pid: 4242,
  });
}

function healthBody(): Record<string, unknown> {
  return {
    api_version: 'v1',
    status: 'ok',
    runtime: { runtime_dir: '/rt', state_dir: '/rt/features', config_path: '/rt/config.yaml' },
    compatibility: {
      api_version: 'v1',
      schema_version: 2,
      min_client_schema: 2,
      runtime_policy: 'loopback-bearer-v1',
      server_build: { version: 'v1.0.0' },
    },
  };
}

function attachDeps(record: {
  states: ConnectionState[];
  logs: string[];
  urls: string[];
}): GatewayDeps {
  return {
    selectRuntime: () => SELECTED,
    discovery: {
      readFile: () => discoveryContent(),
      statFile: () => ({ mode: 0o100600, uid: 501 }),
      euid: 501,
      isProcessAlive: () => true,
    },
    fetchJson: async (url) => {
      record.urls.push(url);
      if (url.endsWith('/api/v1/health')) {
        return { status: 200, body: healthBody() };
      }
      return { status: 200, body: { api_version: 'v1' } };
    },
    resolveServerBinary: () => ({ ok: true, path: '/bin/agentico' }),
    spawnServer: () => {
      throw new Error('attach path must not spawn');
    },
    registerSecret: () => undefined,
    sleep: () => Promise.resolve(),
    log: (line) => record.logs.push(line),
    scanRegistry: () => ({ candidates: [], pruned: 0, rejected: [] }),
    knownServers: () => ({ known: [], lastUsed: null }),
    recordAttachedServer: () => undefined,
    timeouts: { pollIntervalMs: 1, launchReadyMs: 5 },
  };
}

describe('gateway token isolation', () => {
  it('never exposes the bearer token in states, logs, or URLs', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const gateway = new RuntimeGateway(attachDeps(record));
    gateway.subscribe((state) => record.states.push(state));
    await gateway.start();

    expect(gateway.getState().status).toBe('ready');
    for (const state of record.states) {
      // Strict schema: token-shaped fields cannot even be represented.
      ConnectionStateSchema.parse(state);
      expect(JSON.stringify(state)).not.toContain(TOKEN);
    }
    expect(JSON.stringify(record.logs)).not.toContain(TOKEN);
    for (const url of record.urls) {
      expect(url).not.toContain(TOKEN);
    }
  });

  it('scrubs the token from IPC-visible status payloads even if embedded in text', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const deps = attachDeps(record);
    // Simulate a hostile/buggy server that echoes the token into health text.
    deps.fetchJson = async (url) => {
      record.urls.push(url);
      if (url.endsWith('/api/v1/health')) {
        return { status: 200, body: healthBody() };
      }
      return { status: 401, body: { message: `bad Bearer ${TOKEN}` } };
    };
    const gateway = new RuntimeGateway(deps);
    gateway.subscribe((state) => record.states.push(state));
    await gateway.start();
    for (const state of record.states) {
      expect(JSON.stringify(state)).not.toContain(TOKEN);
    }
  });
});

describe('gateway spawn hygiene', () => {
  it('spawns via argv array with shell disabled — no shell interpolation of paths', () => {
    const spawn = vi.fn(() => ({
      pid: 1,
      stdout: null,
      stderr: null,
      on: vi.fn(),
      kill: vi.fn(),
    }));
    ManagedServerProcess.launch({
      binaryPath: '/Applications/Ágentico App/resources/bin/agentico',
      args: ['server', '--config', '/x/config.yaml', '--state-dir', '/x dir/$(rm -rf ~)/features'],
      spawn,
      log: new RedactedLogBuffer(10),
    });
    const [file, args, options] = spawn.mock.calls[0]! as unknown as [
      string,
      readonly string[],
      Record<string, unknown>,
    ];
    // Single argv entries, no quoting/joining, shell explicitly off.
    expect(file).toBe('/Applications/Ágentico App/resources/bin/agentico');
    expect(args[4]).toBe('/x dir/$(rm -rf ~)/features');
    expect(options['shell']).toBe(false);
    expect(options['detached']).toBe(false);
  });
});

describe('gateway ownership boundaries', () => {
  it('shutdown never signals anything when attached to an external server', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const deps = attachDeps(record);
    const gateway = new RuntimeGateway(deps);
    await gateway.start();
    expect(gateway.getState().ownership).toBe('external');
    // spawnServer throws if ever called; shutdown must not touch any process.
    await expect(gateway.shutdown()).resolves.toBeUndefined();
  });

  it('stops only the child it spawned, gracefully then within a bound', async () => {
    const stops: Array<{ timeoutMs?: number }> = [];
    const child: ServerChildLike & { exitedFlag: boolean } = {
      pid: 777,
      exitedFlag: false,
      get exited() {
        return this.exitedFlag;
      },
      onExit: () => () => undefined,
      stop: async (options) => {
        stops.push(options ?? {});
        child.exitedFlag = true;
      },
    };
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const deps = attachDeps(record);
    deps.discovery = {
      readFile: () => {
        throw new Error('ENOENT');
      },
      statFile: () => null,
      euid: 501,
      isProcessAlive: () => false,
    };
    let published = false;
    deps.spawnServer = () => {
      published = true;
      deps.discovery = {
        readFile: () =>
          JSON.stringify({
            schema_version: 1,
            api_version: 'v1',
            base_url: BASE,
            auth_token: TOKEN,
            runtime: SELECTED_RECORD_RUNTIME,
            pid: 777,
          }),
        statFile: () => ({ mode: 0o100600, uid: 501 }),
        euid: 501,
        isProcessAlive: () => true,
      };
      return child;
    };
    const gateway = new RuntimeGateway(deps);
    await gateway.start();
    expect(published).toBe(true);
    expect(gateway.getState().ownership).toBe('app-owned');

    await gateway.shutdown();
    expect(stops).toHaveLength(1);
    expect(stops[0]?.timeoutMs).toBeGreaterThan(0);
  });
});

const SELECTED_RECORD_RUNTIME = {
  runtime_dir: '/rt',
  state_dir: '/rt/features',
  config_path: '/rt/config.yaml',
};

describe('gateway API path allowlist for configuration endpoints', () => {
  it('permits the queryless structured-config endpoints', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const gateway = new RuntimeGateway(attachDeps(record));
    await gateway.start();
    expect(gateway.getState().status).toBe('ready');

    for (const path of [
      '/api/v1/config/runtime',
      '/api/v1/catalog/models',
      '/api/v1/features/feat-1/config',
    ]) {
      record.urls.length = 0;
      await gateway.apiRequest(path);
      expect(record.urls.some((u) => u.endsWith(path))).toBe(true);
    }
  });

  it('rejects unknown query keys on config endpoints', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const gateway = new RuntimeGateway(attachDeps(record));
    await gateway.start();

    await expect(gateway.apiRequest('/api/v1/config/runtime?foo=bar')).rejects.toThrow();
  });
});

describe('gateway API path allowlist for the supervisor namespace', () => {
  it('accepts every supervisor route with its exact method', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const gateway = new RuntimeGateway(attachDeps(record));
    await gateway.start();
    expect(gateway.getState().status).toBe('ready');

    const accepted = [
      ['/api/v1/supervisor/state', 'GET'],
      ['/api/v1/supervisor/settings', 'PATCH'],
      ['/api/v1/supervisor/transcript', 'GET'],
      ['/api/v1/supervisor/transcript?before=12&limit=50', 'GET'],
      ['/api/v1/supervisor/transcript?after=0', 'GET'],
      ['/api/v1/supervisor/transcript?limit=500', 'GET'],
      ['/api/v1/supervisor/messages', 'POST'],
      ['/api/v1/supervisor/interrupt', 'POST'],
      ['/api/v1/supervisor/end', 'POST'],
      ['/api/v1/supervisor/reset', 'POST'],
    ] as const;
    for (const [path, method] of accepted) {
      record.urls.length = 0;
      await gateway.apiRequest(path, method === 'GET' ? {} : { method, body: {} });
      expect(record.urls, `${method} ${path}`).toEqual([`${BASE}${path}`]);
    }
  });

  it('accepts the supervisor event stream with its cursor, epoch, and heartbeat', async () => {
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const sseUrls: string[] = [];
    const gateway = new RuntimeGateway({
      ...attachDeps(record),
      openSse: (url) => {
        sseUrls.push(url);
        const lines = (async function* (): AsyncGenerator<string> {})();
        return Promise.resolve({ status: 200, lines, close: () => undefined });
      },
    });
    await gateway.start();

    await gateway.openSupervisorEventStream();
    await gateway.openSupervisorEventStream({ afterSeq: 41, epoch: 'a1b2c3', heartbeatMs: 15000 });
    expect(sseUrls).toEqual([
      `${BASE}/api/v1/supervisor/events`,
      `${BASE}/api/v1/supervisor/events?after=41&epoch=a1b2c3&heartbeat_ms=15000`,
    ]);
    // The bearer never rides the URL.
    expect(sseUrls.join(' ')).not.toContain(TOKEN);
    // A malformed epoch fails the allowlist before any socket opens.
    await expect(
      gateway.openSupervisorEventStream({ afterSeq: 3, epoch: 'bad epoch&after=9' }),
    ).rejects.toThrow();
    expect(sseUrls).toHaveLength(2);

    for (const path of [
      '/api/v1/supervisor/events',
      '/api/v1/supervisor/events?after=0',
      '/api/v1/supervisor/events?after=7&epoch=deadbeef',
      '/api/v1/supervisor/events?heartbeat_ms=5000',
    ]) {
      expect(isAllowedApiPath(path, 'GET'), path).toBe(true);
    }
  });

  it('rejects wrong methods and unknown supervisor routes', async () => {
    expect(isAllowedApiPath('/api/v1/supervisor/pending-change/req-1', 'DELETE')).toBe(true);
    expect(isAllowedApiPath('/api/v1/supervisor/pending-change/req-1', 'GET')).toBe(false);
    expect(isAllowedApiPath('/api/v1/supervisor/persist-failure', 'DELETE')).toBe(true);
    expect(isAllowedApiPath('/api/v1/supervisor/persist-failure', 'POST')).toBe(false);
    expect(isAllowedApiPath('/api/v1/supervisor/pending-change/../state', 'DELETE')).toBe(false);
    const record = { states: [] as ConnectionState[], logs: [] as string[], urls: [] as string[] };
    const gateway = new RuntimeGateway(attachDeps(record));
    await gateway.start();
    record.urls.length = 0;

    const rejected = [
      ['/api/v1/supervisor/state', 'POST'],
      ['/api/v1/supervisor/settings', 'GET'],
      ['/api/v1/supervisor/settings', 'POST'],
      ['/api/v1/supervisor/transcript', 'POST'],
      ['/api/v1/supervisor/messages', 'GET'],
      ['/api/v1/supervisor/messages', 'PUT'],
      ['/api/v1/supervisor/interrupt', 'GET'],
      ['/api/v1/supervisor/end', 'DELETE'],
      ['/api/v1/supervisor/reset', 'GET'],
      ['/api/v1/supervisor/reset', 'PUT'],
      ['/api/v1/supervisor/reset', 'PATCH'],
      ['/api/v1/supervisor/reset', 'DELETE'],
      ['/api/v1/supervisor/reset?force=1', 'POST'],
      ['/api/v1/supervisor/reset/extra', 'POST'],
      ['/api/v1/supervisor/./reset', 'POST'],
      ['/api/v1/supervisor/../supervisor/reset', 'POST'],
      ['/api/v1/supervisor/reset/..', 'POST'],
      ['/api/v1/supervisor/events', 'POST'],
      ['/api/v1/supervisor', 'GET'],
      ['/api/v1/supervisor/unknown', 'GET'],
      ['/api/v1/supervisor/state/extra', 'GET'],
      ['/api/v1/supervisor/state?x=1', 'GET'],
      ['/api/v1/supervisor/messages?client_message_id=x', 'POST'],
    ] as const;
    for (const [path, method] of rejected) {
      expect(isAllowedApiPath(path, method), `${method} ${path}`).toBe(false);
      await expect(
        gateway.apiRequest(path, method === 'GET' ? {} : { method, body: {} }),
      ).rejects.toThrow();
    }
    expect(record.urls).toEqual([]);
  });

  it('rejects malformed, unknown, duplicate, or conflicting transcript query keys', () => {
    for (const query of [
      '',
      'before=1&after=2',
      'before=0',
      'before=-1',
      'after=-1',
      'after=1.5',
      'after=1e3',
      'after=abc',
      'after=',
      'limit=0',
      'limit=501',
      'limit=10&limit=20',
      'before=4&before=5',
      'after=1&after=1',
      'offset=0',
      'before=3&cursor=2',
      'after=99999999999999999999',
    ]) {
      expect(isAllowedApiPath(`/api/v1/supervisor/transcript?${query}`, 'GET'), query).toBe(false);
    }
  });

  it('rejects malformed or duplicate supervisor event-stream query keys', () => {
    for (const query of [
      '',
      'after=-1',
      'after=x',
      'after=1&after=2',
      'epoch=abc',
      'after=1&epoch=',
      'after=1&epoch=a&epoch=b',
      'after=1&epoch=has space',
      'heartbeat_ms=0',
      'heartbeat_ms=600001',
      'access_token=tok',
      'after=1&before=2',
    ]) {
      expect(isAllowedApiPath(`/api/v1/supervisor/events?${query}`, 'GET'), query).toBe(false);
    }
  });
});
