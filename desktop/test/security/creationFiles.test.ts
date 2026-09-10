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

import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CreationFilesService } from '../../src/main/creationFiles';
import {
  CreationFileSearchRequestSchema,
  RepositoryFileRefSchema,
  sameRepositoryIdentity,
  type ReadinessSnapshot,
  type RepositoryIdentity,
} from '../../src/shared/ipc';

const identity = (repositoryPath: string, inode: string): RepositoryIdentity => ({
  path: repositoryPath,
  commonDir: `${repositoryPath}/.git`,
  device: '16777234',
  inode,
});

function readiness(repositories: ReadinessSnapshot['repositories']): ReadinessSnapshot {
  return {
    ready: true,
    providers: [],
    models: { available: true },
    configuration: { valid: true },
    workspaceRoots: [],
    repositories,
    issues: [],
  };
}

describe('repository identity contract at the security boundary', () => {
  it('rejects malformed identity data in search requests fail-closed', () => {
    const base = { requestId: crypto.randomUUID(), query: 'query' };
    expect(() =>
      CreationFileSearchRequestSchema.parse({
        ...base,
        repositories: [{ key: 'repo-a', identity: identity('/repo/a', '1') }],
      }),
    ).not.toThrow();
    expect(() =>
      CreationFileSearchRequestSchema.parse({
        ...base,
        repositories: [{ key: 'repo-a', identity: { ...identity('/repo/a', '1'), device: '-1' } }],
      }),
    ).toThrow();
    expect(() =>
      CreationFileSearchRequestSchema.parse({
        ...base,
        repositories: [{ key: 'repo-a', identity: { ...identity('/repo/a', '1'), inode: 5 } }],
      }),
    ).toThrow();
  });

  it('rejects malformed identity data in repository-file references fail-closed', () => {
    expect(() =>
      RepositoryFileRefSchema.parse({
        repoKey: 'repo-a',
        path: 'src/a.ts',
        identity: identity('/repo/a', '1'),
      }),
    ).not.toThrow();
    expect(() =>
      RepositoryFileRefSchema.parse({
        repoKey: 'repo-a',
        path: 'src/a.ts',
        identity: { ...identity('/repo/a', '1'), common_dir: '' },
      }),
    ).toThrow();
    expect(() =>
      RepositoryFileRefSchema.parse({
        repoKey: 'repo-a',
        path: 'src/a.ts',
        identity: { ...identity('/repo/a', '1'), extra: 'no' },
      }),
    ).toThrow();
  });

  it('treats identity equality as the whole server-resolved tuple', () => {
    const base = identity('/repo/a', '1');
    expect(sameRepositoryIdentity(base, identity('/repo/a', '1'))).toBe(true);
    expect(sameRepositoryIdentity(base, identity('/repo/a', '2'))).toBe(false);
    expect(sameRepositoryIdentity(base, { ...base, path: '/repo/b' })).toBe(false);
    expect(
      sameRepositoryIdentity({ ...base, birthTime: '1:2' }, { ...base, birthTime: '2:3' }),
    ).toBe(false);
    expect(sameRepositoryIdentity(base, { ...base, birthTime: '1:2' })).toBe(false);
  });
});

describe('repository file resolution cannot be redirected by a reused key', () => {
  let root: string;

  afterEach(async () => {
    if (root !== undefined) await rm(root, { recursive: true, force: true });
  });

  it('refuses a reference whose repository was replaced, without touching the replacement tree', async () => {
    root = await mkdtemp(path.join(tmpdir(), 'security-creation-files-'));
    const original = identity('/gone/repo-a', '111');
    const replacement = identity(root, '222');
    const readReadiness = vi.fn(() =>
      Promise.resolve(
        readiness([
          { name: 'repo-a', path: root, valid: true, featureReady: true, identity: replacement },
        ]),
      ),
    );
    const service = new CreationFilesService({
      pickFiles: vi.fn(() => Promise.resolve([])),
      readReadiness,
      locality: () => 'local',
    });

    // The replacement tree is never opened: resolution stops at discovery.
    await expect(
      service.resolve([{ repoKey: 'repo-a', path: 'src/query.ts', identity: original }]),
    ).rejects.toMatchObject({ canonical: { code: 'E_REPOSITORY_FILE_UNRESOLVED' } });
    expect(readReadiness).toHaveBeenCalledOnce();
  });
});
