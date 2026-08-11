import { describe, expect, it, vi } from 'vitest';
import { createHash } from 'node:crypto';
import {
  registryEntryKey,
  registryPathForHome,
  scanRegistry,
  type RegistryDeps,
} from '../gateway/registry';

const HOME = '/home/user';
const FRESH = `${HOME}/.agentic-orchestrator`;
const LEGACY = `${HOME}/.agentic-workflow`;
const FRESH_SERVERS = `${FRESH}/servers`;
const LEGACY_SERVERS = `${LEGACY}/servers`;
const RUNTIME_DIR = '/home/user/.agentic-orchestrator';

function record(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    schema_version: 1,
    api_version: 'v1',
    base_url: 'http://127.0.0.1:49152',
    auth_token: 'tok-abc123',
    runtime: {
      runtime_dir: RUNTIME_DIR,
      state_dir: `${RUNTIME_DIR}/features`,
      config_path: `${RUNTIME_DIR}/config.yaml`,
    },
    launch_policy: { resolved: true, providers: ['claude'], dangerously_skip_permissions: false },
    start_mode: 'server',
    pid: 4242,
    started_at: '2026-07-14T00:00:00Z',
    published_at: '2026-07-14T00:00:00Z',
    owner: { pid: 4242, started_at: '2026-07-14T00:00:00Z' },
    ...overrides,
  };
}

/** Builds deps from a fake registry: serverKey → file content. */
function deps(
  overrides: Partial<RegistryDeps> = {},
  files: Record<string, string> | null = {},
  dirListing = files === null ? null : Object.keys(files).map((key) => `${key}.json`),
): RegistryDeps {
  return {
    homeDir: HOME,
    dirExists: (p) => p === FRESH,
    listDir: () => dirListing,
    readFile: (filePath) => {
      const base = filePath.split('/').pop() ?? '';
      const key = base.replace(/\.json$/, '');
      const content = files?.[key];
      if (content === undefined) {
        throw new Error('missing');
      }
      return content;
    },
    statFile: () => ({ mode: 0o100600, uid: 501 }),
    euid: 501,
    isProcessAlive: () => true,
    removeFile: () => undefined,
    ...overrides,
  };
}

function keyOf(dir: string): string {
  return registryEntryKey(dir);
}

describe('registryPathForHome', () => {
  it('prefers the fresh install dir when it exists', () => {
    expect(registryPathForHome(HOME, () => true)).toBe(FRESH);
  });

  it('falls back to the legacy dir when only it exists', () => {
    expect(registryPathForHome(HOME, (p) => p === LEGACY)).toBe(LEGACY);
  });

  it('defaults to the fresh dir when neither exists', () => {
    expect(registryPathForHome(HOME, () => false)).toBe(FRESH);
  });
});

describe('registryEntryKey', () => {
  it('matches the Go sha256-truncation rule for a known runtime dir', () => {
    const expected = createHash('sha256').update('/x/runtime').digest('hex').slice(0, 32);
    expect(registryEntryKey('/x/runtime')).toBe(expected);
    expect(registryEntryKey('/x/runtime')).toMatch(/^[0-9a-f]{32}$/);
  });
});

describe('scanRegistry', () => {
  it('returns zero candidates when the registry dir is missing and prunes nothing', () => {
    const removeFile = vi.fn();
    const scan = scanRegistry(deps({ removeFile }, null));
    expect(scan).toEqual({ candidates: [], pruned: 0, rejected: [] });
    expect(removeFile).not.toHaveBeenCalled();
  });

  it('returns zero candidates for an empty registry', () => {
    expect(scanRegistry(deps()).candidates).toEqual([]);
  });

  it('returns one live candidate with record, serverKey, and runtimeDir', () => {
    const key = keyOf(RUNTIME_DIR);
    const scan = scanRegistry(deps({}, { [key]: JSON.stringify(record()) }));
    expect(scan.candidates).toHaveLength(1);
    expect(scan.candidates[0]!.serverKey).toBe(key);
    expect(scan.candidates[0]!.runtimeDir).toBe(RUNTIME_DIR);
    expect(scan.candidates[0]!.record.auth_token).toBe('tok-abc123');
    expect(scan.candidates[0]!.record.pid).toBe(4242);
    expect(scan.pruned).toBe(0);
  });

  it('returns token-free rejection diagnostics', () => {
    const key = '1234abcd';
    const scan = scanRegistry(
      deps({}, { [key]: JSON.stringify(record({ base_url: 'http://evil.example.com:1' })) }),
    );
    expect(JSON.stringify(scan.rejected)).not.toContain('tok-abc123');
    expect(JSON.stringify(scan)).not.toContain('tok-abc123'.replace('tok-', ''));
  });

  it('sorts many candidates deterministically by serverKey', () => {
    const keys = ['dddd0000', 'aaaa1111', 'bbbb2222'];
    const files = Object.fromEntries(keys.map((k) => [k, JSON.stringify(record())]));
    const scan = scanRegistry(deps({}, files));
    expect(scan.candidates.map((c) => c.serverKey)).toEqual(['aaaa1111', 'bbbb2222', 'dddd0000']);
  });

  it('prunes entries with unsafe permissions', () => {
    const key = '604604604604604604604604604604604';
    const removed: string[] = [];
    const scan = scanRegistry(
      deps(
        {
          statFile: () => ({ mode: 0o100604, uid: 501 }),
          removeFile: (p) => {
            removed.push(p);
          },
        },
        { [key]: JSON.stringify(record()) },
      ),
    );
    expect(scan.candidates).toEqual([]);
    expect(scan.pruned).toBe(1);
    expect(removed).toEqual([`${FRESH_SERVERS}/${key}.json`]);
  });

  it('rejects foreign-owned entries and never deletes them', () => {
    const key = 'f0e1d2c3b4a5968778695a4b3c2d1e0f';
    const removeFile = vi.fn();
    const scan = scanRegistry(
      deps(
        { statFile: () => ({ mode: 0o100600, uid: 0 }), removeFile },
        {
          [key]: JSON.stringify(record()),
        },
      ),
    );
    expect(scan.candidates).toEqual([]);
    expect(scan.pruned).toBe(0);
    expect(scan.rejected).toEqual([{ serverKey: key, reason: 'foreign owner' }]);
    expect(removeFile).not.toHaveBeenCalled();
  });

  it('prunes corrupted JSON without echoing contents', () => {
    const key = 'badbadbadbadbadbadbadbadbadbadbad';
    const scan = scanRegistry(deps({}, { [key]: '{not json, token: super-secret' }));
    expect(scan.pruned).toBe(1);
    expect(JSON.stringify(scan)).not.toContain('super-secret');
  });

  it('prunes schema-mismatched records missing required fields', () => {
    const key = 'schema0000000000000000000000000001';
    const scan = scanRegistry(
      deps({}, { [key]: JSON.stringify({ schema_version: 1, api_version: 'v1' }) }),
    );
    expect(scan.pruned).toBe(1);
    expect(scan.candidates).toEqual([]);
  });

  it('accepts records carrying unknown future fields after stripping', () => {
    const key = 'future0000000000000000000000000001';
    const scan = scanRegistry(
      deps({}, { [key]: JSON.stringify(record({ future_field: { nested: 'values' } })) }),
    );
    expect(scan.candidates).toHaveLength(1);
    expect(scan.candidates[0]!.record).not.toHaveProperty('future_field');
  });

  it('prunes non-loopback base URLs', () => {
    const key = 'noloop0000000000000000000000000001';
    const scan = scanRegistry(
      deps({}, { [key]: JSON.stringify(record({ base_url: 'http://192.168.7.7:8080' })) }),
    );
    expect(scan.pruned).toBe(1);
    expect(scan.candidates).toEqual([]);
  });

  it('prunes dead-PID records', () => {
    const key = 'deadpid000000000000000000000000001';
    const scan = scanRegistry(
      deps({ isProcessAlive: () => false }, { [key]: JSON.stringify(record()) }),
    );
    expect(scan.pruned).toBe(1);
    expect(scan.candidates).toEqual([]);
  });

  it('treats entries whose stat throws as rejected without deleting them', () => {
    const key = 'racyracyracyracyracyracyracyrac';
    const removeFile = vi.fn();
    const scan = scanRegistry(
      deps(
        {
          statFile: () => {
            throw new Error('racy');
          },
          removeFile,
        },
        { [key]: JSON.stringify(record()) },
      ),
    );
    expect(scan.pruned).toBe(0);
    expect(scan.rejected).toEqual([{ serverKey: key, reason: 'unreadable' }]);
    expect(removeFile).not.toHaveBeenCalled();
  });

  it('keeps scanning after a poisoned entry and returns the valid one', () => {
    const good = 'goodgoodgoodgoodgoodgoodgoodgoodgo';
    const scan = scanRegistry(deps({}, { poisoned: '{corrupt', [good]: JSON.stringify(record()) }));
    expect(scan.pruned).toBe(1);
    expect(scan.candidates.map((c) => c.serverKey)).toEqual([good]);
  });

  it('swallows prune failures without breaking the scan', () => {
    const good = 'goodgoodgoodgoodgoodgoodgoodgoodgo';
    const scan = scanRegistry(
      deps(
        {
          removeFile: () => {
            throw new Error('eperm');
          },
        },
        { poisoned: '{corrupt', [good]: JSON.stringify(record()) },
      ),
    );
    expect(scan.pruned).toBe(1);
    expect(scan.candidates.map((c) => c.serverKey)).toEqual([good]);
  });

  it('reads entries from the legacy install when only it exists', () => {
    const key = keyOf(RUNTIME_DIR);
    const readPaths: string[] = [];
    const scan = scanRegistry(
      deps(
        {
          dirExists: (p) => p === LEGACY,
          readFile: (filePath) => {
            readPaths.push(filePath);
            return JSON.stringify(record());
          },
        },
        { [key]: JSON.stringify(record()) },
      ),
    );
    expect(scan.candidates).toHaveLength(1);
    expect(readPaths.every((p) => p.startsWith(`${LEGACY_SERVERS}/`))).toBe(true);
  });

  it('prefers the fresh install when both dirs exist', () => {
    const key = keyOf(RUNTIME_DIR);
    const listDirs: string[] = [];
    scanRegistry(
      deps(
        {
          dirExists: () => true,
          listDir: (p) => {
            listDirs.push(p);
            return [`${key}.json`];
          },
        },
        { [key]: JSON.stringify(record()) },
      ),
    );
    expect(listDirs[0]!.startsWith(FRESH)).toBe(true);
  });

  it('ignores non-json entries in the registry dir', () => {
    const removeFile = vi.fn();
    const scan = scanRegistry(deps({ listDir: () => ['README', 'notjson.txt'], removeFile }));
    expect(scan).toEqual({ candidates: [], pruned: 0, rejected: [] });
  });
});
