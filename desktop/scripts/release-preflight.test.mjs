import { afterEach, describe, expect, it } from 'vitest';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { runReleasePreflight, verifyReleaseProvenance } from './release-preflight.mjs';

const GiB = 1024 ** 3;
const roots = [];

afterEach(() => roots.splice(0).forEach((root) => rmSync(root, { recursive: true, force: true })));

function fixture(overrides = {}) {
  const root = mkdtempSync(join(tmpdir(), 'agentico-release-preflight-'));
  roots.push(root);
  const state = { status: '', head: 'a'.repeat(40), tag: 'v0.150.0', docker: [] };
  return {
    cwd: root,
    platform: 'darwin',
    freeBytes: 20 * GiB,
    env: {
      GITHUB_TOKEN: 'token',
      AGENTICO_RELEASE_SIGNING_KEY: 'key',
    },
    git: (_cwd, ...args) => {
      const command = args.join(' ');
      if (command === 'status --porcelain --untracked-files=all') return state.status;
      if (command === 'rev-parse HEAD') return state.head;
      if (command === 'describe --tags --exact-match') return state.tag;
      throw new Error(`unexpected Git command: ${command}`);
    },
    ensureImage: (image) => state.docker.push(image),
    verifySigningKey: () => true,
    evidencePath: join(root, 'desktop', 'dist', 'release-preflight.json'),
    state,
    ...overrides,
  };
}

describe('release preflight', () => {
  it('rejects a non-macOS host before contacting Docker', () => {
    const options = fixture({ platform: 'linux' });
    expect(() => runReleasePreflight(options)).toThrow(/must run on macOS/);
    expect(options.state.docker).toEqual([]);
  });

  it('records the exact clean tag and commit only after all prerequisites pass', () => {
    const options = fixture();
    const evidence = runReleasePreflight(options);
    expect(evidence).toMatchObject({ tag: 'v0.150.0', commit: 'a'.repeat(40) });
    expect(options.state.docker).toHaveLength(2);
    expect(JSON.parse(readFileSync(options.evidencePath, 'utf8'))).toMatchObject(evidence);
  });

  it('rejects a Docker daemon that reports unavailable before inspecting images', () => {
    const options = fixture({ dockerInfo: () => false });
    expect(() => runReleasePreflight(options)).toThrow(/Docker daemon is unavailable/);
    expect(options.state.docker).toEqual([]);
  });

  it('refuses to continue when provenance drifts after a container step', () => {
    const options = fixture();
    const evidence = runReleasePreflight(options);
    options.state.status = ' M Makefile';
    expect(() => verifyReleaseProvenance({ ...options, evidence })).toThrow(/working tree changed/);
  });

  it('overwrites prior evidence for a new release invocation', () => {
    const options = fixture();
    runReleasePreflight(options);
    options.state.head = 'b'.repeat(40);
    expect(runReleasePreflight(options)).toMatchObject({ commit: 'b'.repeat(40) });
    expect(existsSync(options.evidencePath)).toBe(true);
  });

  it('rejects forged incomplete evidence before comparing provenance', () => {
    const options = fixture();
    expect(() => verifyReleaseProvenance({ ...options, evidence: { tag: 'v0.150.0' } })).toThrow(
      /unsupported schema/,
    );
  });
});
