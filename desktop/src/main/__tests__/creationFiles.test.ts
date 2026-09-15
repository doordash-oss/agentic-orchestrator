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

import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CreationFilesService, type CreationFilesServiceDeps } from '../creationFiles';
import type { ReadinessSnapshot, RepositoryIdentity } from '../../shared/ipc';

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

function makeService(overrides: Partial<CreationFilesServiceDeps> = {}) {
  const deps: CreationFilesServiceDeps = {
    pickFiles: vi.fn(() => Promise.resolve(['/picked/a.png', '/picked/b.png'])),
    readReadiness: () =>
      Promise.resolve(
        readiness([{ name: 'repo-a', path: '/repo/a', valid: true, featureReady: true }]),
      ),
    ...overrides,
  };
  return { deps, service: new CreationFilesService(deps) };
}

describe('CreationFilesService remote-connection guards', () => {
  const remote = () => 'remote' as const;

  it('still runs the native picker remotely (upload staging happens via the upload channel)', async () => {
    const { deps, service } = makeService({ locality: remote });
    await expect(service.pickFiles('image')).resolves.toStrictEqual({
      paths: ['/picked/a.png', '/picked/b.png'],
    });
    expect(deps.pickFiles).toHaveBeenCalledWith('image');
  });

  it('refuses the repository file search remotely before any walk', async () => {
    const readReadiness = vi.fn(() =>
      Promise.resolve(
        readiness([{ name: 'repo-a', path: '/repo/a', valid: true, featureReady: true }]),
      ),
    );
    const { service } = makeService({ locality: remote, readReadiness });
    await expect(
      service.search({
        requestId: crypto.randomUUID(),
        repositories: [{ key: 'repo-a' }],
        query: 'query',
      }),
    ).rejects.toMatchObject({ canonical: { code: 'E_REQUIRES_LOCAL_SERVER' } });
    expect(readReadiness).not.toHaveBeenCalled();
  });

  it('refuses repository file resolution remotely before touching the filesystem', async () => {
    const readReadiness = vi.fn(() =>
      Promise.resolve(
        readiness([{ name: 'repo-a', path: '/repo/a', valid: true, featureReady: true }]),
      ),
    );
    const { service } = makeService({ locality: remote, readReadiness });
    await expect(
      service.resolve([{ repoKey: 'repo-a', path: 'src/query.ts' }]),
    ).rejects.toMatchObject({ canonical: { code: 'E_REQUIRES_LOCAL_SERVER' } });
    expect(readReadiness).not.toHaveBeenCalled();
  });
});

describe('CreationFilesService local behavior (unchanged)', () => {
  let root: string;

  beforeEach(async () => {
    root = await mkdtemp(path.join(tmpdir(), 'creation-files-'));
  });

  afterEach(async () => {
    await rm(root, { recursive: true, force: true });
  });

  it('returns picked paths exactly as the dialog produced them (limit applied)', async () => {
    const { service } = makeService({ locality: () => 'local' });
    await expect(service.pickFiles('image')).resolves.toStrictEqual({
      paths: ['/picked/a.png', '/picked/b.png'],
    });
  });

  it('searches and resolves real local repositories exactly as before', async () => {
    await mkdir(path.join(root, 'src', 'deps'), { recursive: true });
    await writeFile(path.join(root, 'src', 'deps', 'query.ts'), 'export {};\n');
    await writeFile(path.join(root, 'src', 'other.ts'), 'export {};\n');
    const snapshot = readiness([{ name: 'repo-a', path: root, valid: true, featureReady: true }]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const found = await service.search({
      requestId: crypto.randomUUID(),
      repositories: [{ key: 'repo-a' }],
      query: 'query',
    });
    expect(found.files).toEqual([{ repoKey: 'repo-a', path: 'src/deps/query.ts' }]);

    const resolved = await service.resolve([{ repoKey: 'repo-a', path: 'src/deps/query.ts' }]);
    expect(resolved).toHaveLength(1);
    expect(resolved[0]).toContain(path.join('src', 'deps', 'query.ts'));
  });
});

describe('CreationFilesService repository identity binding', () => {
  let rootA: string;
  let rootB: string;

  const identity = (path: string, inode: string): RepositoryIdentity => ({
    path,
    commonDir: `${path}/.git`,
    device: '16777234',
    inode,
  });

  beforeEach(async () => {
    rootA = await mkdtemp(path.join(tmpdir(), 'creation-files-a-'));
    rootB = await mkdtemp(path.join(tmpdir(), 'creation-files-b-'));
    for (const root of [rootA, rootB]) {
      await mkdir(path.join(root, 'src', 'deps'), { recursive: true });
      await writeFile(path.join(root, 'src', 'deps', 'query.ts'), 'export {};\n');
    }
  });

  afterEach(async () => {
    await rm(rootA, { recursive: true, force: true });
    await rm(rootB, { recursive: true, force: true });
  });

  it("searches the repository the request's identity expects, not whoever holds the key", async () => {
    const expected = identity(rootA, '111');
    const replacement = identity(rootB, '222');
    const snapshot = readiness([
      // A replacement took over the requested key at a different path.
      { name: 'repo-a', path: rootB, valid: true, featureReady: true, identity: replacement },
    ]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const found = await service.search({
      requestId: crypto.randomUUID(),
      repositories: [{ key: 'repo-a', identity: expected }],
      query: 'query',
    });
    expect(found.files).toEqual([]);
  });

  it('returns results bound to the repository identity and its current key', async () => {
    const expected = identity(rootA, '111');
    const snapshot = readiness([
      { name: 'root-x/repo-a', path: rootA, valid: true, featureReady: true, identity: expected },
    ]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const found = await service.search({
      requestId: crypto.randomUUID(),
      repositories: [{ key: 'repo-a', identity: expected }],
      query: 'query',
    });
    expect(found.files).toEqual([
      { repoKey: 'root-x/repo-a', path: 'src/deps/query.ts', identity: expected },
    ]);
  });

  it('resolves a reference through its identity after a rename, retaining the relative path', async () => {
    const expected = identity(rootA, '111');
    const snapshot = readiness([
      { name: 'root-x/repo-a', path: rootA, valid: true, featureReady: true, identity: expected },
      {
        name: 'repo-b',
        path: rootB,
        valid: true,
        featureReady: true,
        identity: identity(rootB, '333'),
      },
    ]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const resolved = await service.resolve([
      { repoKey: 'repo-a', path: 'src/deps/query.ts', identity: expected },
    ]);
    expect(resolved).toHaveLength(1);
    expect(resolved[0]).toContain(rootA);
    expect(resolved[0]).toContain(path.join('src', 'deps', 'query.ts'));
  });

  it('refuses a reference whose repository was replaced at the same key', async () => {
    const original = identity(rootA, '111');
    const replacement = identity(rootB, '222');
    const snapshot = readiness([
      { name: 'repo-a', path: rootB, valid: true, featureReady: true, identity: replacement },
    ]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    // The replacement carries the old key and even the same relative file,
    // but it is a different repository: the reference stays unresolved.
    await expect(
      service.resolve([{ repoKey: 'repo-a', path: 'src/deps/query.ts', identity: original }]),
    ).rejects.toMatchObject({ canonical: { code: 'E_REPOSITORY_FILE_UNRESOLVED' } });
  });

  it('refuses a reference whose repository is missing from fresh discovery', async () => {
    const expected = identity(rootA, '111');
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(readiness([])),
    });

    await expect(
      service.resolve([{ repoKey: 'repo-a', path: 'src/deps/query.ts', identity: expected }]),
    ).rejects.toMatchObject({ canonical: { code: 'E_REPOSITORY_FILE_UNRESOLVED' } });
  });

  it('never lets two repositories with the same relative filename exchange attachments', async () => {
    const repoA = identity(rootA, '111');
    const repoB = identity(rootB, '333');
    const snapshot = readiness([
      { name: 'repo-a', path: rootA, valid: true, featureReady: true, identity: repoA },
      { name: 'repo-b', path: rootB, valid: true, featureReady: true, identity: repoB },
    ]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const resolved = await service.resolve([
      { repoKey: 'repo-b', path: 'src/deps/query.ts', identity: repoA },
    ]);
    // The transmitted key names another repository, but the identity binds
    // the reference to its own: the file resolves inside that repository.
    expect(resolved[0]).toContain(rootA);
    expect(resolved[0]).not.toContain(rootB);
  });
});
