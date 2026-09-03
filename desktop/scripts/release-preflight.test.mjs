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

import { afterEach, describe, expect, it } from 'vitest';
import { spawnSync } from 'node:child_process';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  cleanupReleaseWorkspace,
  readReleaseEvidence,
  runReleasePreflight,
  verifyReleaseProvenance,
  writeReleaseEvidence,
} from './release-preflight.mjs';

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
    dockerInfo: () => true,
    ensureImage: (image) => state.docker.push(image),
    goreleaserVersion: () => 'goreleaser version 2.10.2',
    verifySigningKey: () => true,
    evidencePath: join(root, 'desktop', 'dist', 'release-preflight.json'),
    createWorkspace: ({ commit, runId }) => {
      const path = join(root, `workspace-${runId}`);
      mkdirSync(path);
      const canonicalPath = realpathSync(path);
      const stat = statSync(canonicalPath);
      return {
        operatorRoot: root,
        commonDir: join(root, '.git'),
        path: canonicalPath,
        commit,
        runId,
        token: { path: canonicalPath, kind: 'directory', dev: stat.dev, ino: stat.ino },
      };
    },
    compileCleanupHelper: ({ workspace }) => ({
      path: join(
        root,
        '.git',
        'agentico-release-cleanup-helpers',
        workspace.runId,
        'release-cleanup',
      ),
      sha256: 'c'.repeat(64),
      size: 123,
    }),
    state,
    ...overrides,
  };
}

describe('release preflight', () => {
  it('runs its CLI through a symlinked script path', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-preflight-cli-'));
    roots.push(root);
    const script = fileURLToPath(new URL('./release-preflight.mjs', import.meta.url));
    const aliasedScript = join(root, 'release-preflight.mjs');
    symlinkSync(script, aliasedScript);

    const result = spawnSync(process.execPath, [aliasedScript, 'unexpected'], {
      encoding: 'utf8',
    });

    expect(result.status).toBe(1);
    expect(result.stderr).toContain('usage: release-preflight.mjs [verify]');
  });

  it('rejects a non-macOS host before contacting Docker', () => {
    const options = fixture({ platform: 'linux' });
    expect(() => runReleasePreflight(options)).toThrow(/must run on macOS/);
    expect(options.state.docker).toEqual([]);
  });

  it('records the exact clean tag and commit only after all prerequisites pass', () => {
    const options = fixture();
    const evidence = runReleasePreflight(options);
    expect(evidence).toMatchObject({
      tag: 'v0.150.0',
      commit: 'a'.repeat(40),
      operator_root: options.cwd,
      workspace_root: expect.stringContaining('workspace-'),
      evidence_path: options.evidencePath,
      cleanup_helper: {
        path: expect.stringContaining('agentico-release-cleanup-helpers/'),
        sha256: 'c'.repeat(64),
        size: 123,
      },
    });
    expect(options.state.docker).toHaveLength(2);
    const persistedEvidence = { ...evidence };
    delete persistedEvidence.evidence_sha256;
    expect(JSON.parse(readFileSync(options.evidencePath, 'utf8'))).toMatchObject(persistedEvidence);
  });

  it('rejects a Docker daemon that reports unavailable before inspecting images', () => {
    const options = fixture({ dockerInfo: () => false });
    expect(() => runReleasePreflight(options)).toThrow(/Docker daemon is unavailable/);
    expect(options.state.docker).toEqual([]);
  });

  it('requires a supported GoReleaser version before contacting Docker', () => {
    const options = fixture({ goreleaserVersion: () => 'goreleaser version 2.9.0' });
    expect(() => runReleasePreflight(options)).toThrow(/GoReleaser v2.10 or later/);
    expect(options.state.docker).toEqual([]);
  });

  it('surfaces a missing GoReleaser binary before contacting Docker', () => {
    const options = fixture({
      goreleaserVersion: () => {
        const error = new Error('spawnSync goreleaser ENOENT');
        error.code = 'ENOENT';
        throw error;
      },
    });
    expect(() => runReleasePreflight(options)).toThrow(/goreleaser ENOENT/);
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

  it('atomically replaces an existing evidence symlink without writing through it', () => {
    const options = fixture();
    const victim = join(options.cwd, 'evidence-victim');
    writeFileSync(victim, 'unchanged');
    mkdirSync(join(options.cwd, 'desktop', 'dist'), { recursive: true });
    symlinkSync(victim, options.evidencePath);
    runReleasePreflight(options);
    expect(lstatSync(options.evidencePath).isSymbolicLink()).toBe(false);
    expect(readFileSync(victim, 'utf8')).toBe('unchanged');
    expect(statSync(options.evidencePath).mode & 0o777).toBe(0o600);
  });

  it('removes its same-directory temporary evidence file when atomic rename fails', () => {
    const options = fixture();
    const temporary = join(options.cwd, 'desktop', 'dist', '.release-preflight.json.fixed.tmp');
    expect(() =>
      writeReleaseEvidence(
        options.evidencePath,
        { ok: true },
        {
          randomId: () => 'fixed',
          renameFile: () => {
            throw new Error('rename failed');
          },
        },
      ),
    ).toThrow(/rename failed/);
    expect(existsSync(temporary)).toBe(false);
    expect(existsSync(options.evidencePath)).toBe(false);
  });

  it('fsyncs the parent directory after atomically publishing durable evidence', () => {
    const options = fixture();
    const synced = [];
    writeReleaseEvidence(
      options.evidencePath,
      { ok: true },
      { syncDirectory: (path) => synced.push(path) },
    );
    expect(synced).toEqual([join(options.cwd, 'desktop', 'dist')]);
  });

  it('binds detached evidence consumers to the exact evidence-file bytes', () => {
    const options = fixture();
    const evidence = runReleasePreflight(options);
    expect(evidence.evidence_sha256).toMatch(/^[0-9a-f]{64}$/);
    expect(
      readReleaseEvidence({
        evidencePath: options.evidencePath,
        expectedDigest: evidence.evidence_sha256,
      }).evidence_sha256,
    ).toBe(evidence.evidence_sha256);
    writeFileSync(options.evidencePath, `${readFileSync(options.evidencePath, 'utf8')} `);
    expect(() =>
      readReleaseEvidence({
        evidencePath: options.evidencePath,
        expectedDigest: evidence.evidence_sha256,
      }),
    ).toThrow(/digest changed/);
  });

  it('fails closed when its owned temporary inode is replaced by a victim symlink before rename', () => {
    const options = fixture();
    const victim = join(options.cwd, 'temporary-swap-victim');
    writeFileSync(victim, 'victim bytes');
    chmodSync(victim, 0o644);
    expect(() =>
      writeReleaseEvidence(
        options.evidencePath,
        { owned: true },
        {
          randomId: () => 'swap',
          renameFile: (temporary, destination) => {
            renameSync(temporary, `${temporary}.owned`);
            symlinkSync(victim, temporary);
            renameSync(temporary, destination);
          },
        },
      ),
    ).toThrow(/temporary file changed/);
    expect(readFileSync(victim, 'utf8')).toBe('victim bytes');
    expect(statSync(victim).mode & 0o777).toBe(0o644);
    expect(() => readReleaseEvidence({ evidencePath: options.evidencePath })).toThrow(
      /not a regular file/,
    );
  });

  it('rejects forged incomplete evidence before comparing provenance', () => {
    const options = fixture();
    expect(() => verifyReleaseProvenance({ ...options, evidence: { tag: 'v0.150.0' } })).toThrow(
      /unsupported schema/,
    );
  });

  it('removes standalone evidence after its temporary workspace is cleaned', () => {
    const options = fixture();
    const evidence = runReleasePreflight(options);
    expect(existsSync(options.evidencePath)).toBe(true);
    cleanupReleaseWorkspace({
      ...options,
      evidence,
      removeWorkspace: () => {},
      git: (_cwd, ...args) => {
        if (args.join(' ') === 'rev-parse --path-format=absolute --git-common-dir') {
          return join(options.cwd, '.git');
        }
        if (args.join(' ') === 'rev-parse --git-path agentico-release-preflight.json') {
          return options.evidencePath;
        }
        throw new Error(`unexpected cleanup Git command: ${args.join(' ')}`);
      },
    });
    expect(existsSync(options.evidencePath)).toBe(false);
  });
});
