import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CreationFilesService, type CreationFilesServiceDeps } from '../creationFiles';
import type { ReadinessSnapshot } from '../../shared/ipc';

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
      Promise.resolve(readiness([{ name: 'repo-a', path: '/repo/a', valid: true }])),
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
      Promise.resolve(readiness([{ name: 'repo-a', path: '/repo/a', valid: true }])),
    );
    const { service } = makeService({ locality: remote, readReadiness });
    await expect(
      service.search({ requestId: crypto.randomUUID(), repoKeys: ['repo-a'], query: 'query' }),
    ).rejects.toMatchObject({ safe: { code: 'E_REQUIRES_LOCAL_SERVER' } });
    expect(readReadiness).not.toHaveBeenCalled();
  });

  it('refuses repository file resolution remotely before touching the filesystem', async () => {
    const readReadiness = vi.fn(() =>
      Promise.resolve(readiness([{ name: 'repo-a', path: '/repo/a', valid: true }])),
    );
    const { service } = makeService({ locality: remote, readReadiness });
    await expect(
      service.resolve([{ repoKey: 'repo-a', path: 'src/query.ts' }]),
    ).rejects.toMatchObject({ safe: { code: 'E_REQUIRES_LOCAL_SERVER' } });
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
    const snapshot = readiness([{ name: 'repo-a', path: root, valid: true }]);
    const { service } = makeService({
      locality: () => 'local',
      readReadiness: () => Promise.resolve(snapshot),
    });

    const found = await service.search({
      requestId: crypto.randomUUID(),
      repoKeys: ['repo-a'],
      query: 'query',
    });
    expect(found.files).toEqual([{ repoKey: 'repo-a', path: 'src/deps/query.ts' }]);

    const resolved = await service.resolve([{ repoKey: 'repo-a', path: 'src/deps/query.ts' }]);
    expect(resolved).toHaveLength(1);
    expect(resolved[0]).toContain(path.join('src', 'deps', 'query.ts'));
  });
});
