import { describe, expect, it } from 'vitest';

import { RELEASE_SEQUENCE, runRelease } from './release-run.mjs';

const evidence = {
  schema_version: 2,
  tag: 'v0.150.0',
  commit: 'a'.repeat(40),
  run_id: '11111111-1111-4111-8111-111111111111',
  workspace_root: '/release/workspace',
  operator_root: '/operator/checkout',
};

function fixture(overrides = {}) {
  const calls = [];
  return {
    calls,
    options: {
      operatorRoot: '/operator/checkout',
      preflight: () => evidence,
      command: (label, _command, _args, options) => calls.push({ label, ...options }),
      verifyProvenance: () => calls.push({ label: 'provenance' }),
      createSnapshot: () => ({ path: '/release/workspace/desktop/dist/publication' }),
      verifyPackages: () => {
        calls.push({ label: 'packages' });
        return { ok: true };
      },
      reserveTag: async () => calls.push({ label: 'reserve' }),
      publish: () => calls.push({ label: 'goreleaser' }),
      verifyManifest: () => {
        calls.push({ label: 'manifest' });
        return { ok: true };
      },
      verifySnapshot: () => {},
      verifyRemote: async () => calls.push({ label: 'remote' }),
      publishCask: () => calls.push({ label: 'cask' }),
      cleanup: () => calls.push({ label: 'cleanup' }),
      ...overrides,
    },
  };
}

describe('release runner', () => {
  it('defines the one audited committed-source publication sequence', () => {
    expect(RELEASE_SEQUENCE).toEqual([
      'preflight',
      'npm-ci',
      'mac-package',
      'linux-packages',
      'package-gate',
      'publication-snapshot',
      'snapshot-gate',
      'provenance-recheck',
      'remote-tag-reservation',
      'goreleaser',
      'manifest-gate',
      'remote-byte-gate',
      'desktop-cask',
      'cleanup',
    ]);
  });

  it('runs native and Linux builds in the detached workspace and reserves immediately before publish', async () => {
    const { calls, options } = fixture();
    await runRelease(options);
    expect(
      calls
        .filter(({ cwd }) => cwd !== undefined)
        .every(({ cwd }) => cwd === evidence.workspace_root),
    ).toBe(true);
    expect(calls.map(({ label }) => label)).toEqual([
      'npm-ci',
      'mac-package',
      'linux-packages',
      'packages',
      'packages',
      'provenance',
      'reserve',
      'goreleaser',
      'manifest',
      'remote',
      'cask',
      'cleanup',
    ]);
  });

  it('removes the exact detached workspace on a build failure', async () => {
    const { calls, options } = fixture({
      command: (label) => {
        calls.push({ label });
        if (label === 'mac-package') throw new Error('package failed');
      },
    });
    await expect(runRelease(options)).rejects.toThrow('package failed');
    expect(calls.at(-1)).toEqual({ label: 'cleanup' });
    expect(calls.some(({ label }) => label === 'reserve')).toBe(false);
  });
});
