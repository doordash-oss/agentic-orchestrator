/**
 * Security properties of the runtime gateway:
 *  - bearer material never leaves the main process (not in states, IPC
 *    payloads, URLs, or logs),
 *  - the server child is spawned via argv array without shell interpolation,
 *  - external servers are never signalled.
 */
import { describe, expect, it, vi } from 'vitest';
import { ConnectionStateSchema, type ConnectionState } from '../../src/shared/ipc';
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
      schema_version: 1,
      min_client_schema: 1,
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
