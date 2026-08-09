import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  fileIsExecutable,
  resolveServerBinary,
  serverBinaryCandidates,
  type ResourceContext,
} from '../gateway/resources';

const cleanups: Array<() => void> = [];

afterEach(() => {
  while (cleanups.length > 0) {
    cleanups.pop()?.();
  }
});

function tempDir(name: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-res-'));
  const target = path.join(dir, name);
  fs.mkdirSync(target, { recursive: true });
  cleanups.push(() => {
    // Restore write permission so cleanup can remove read-only roots.
    for (const entry of fs.readdirSync(dir, { recursive: true, withFileTypes: true })) {
      if (entry.isDirectory()) {
        try {
          fs.chmodSync(path.join(entry.parentPath, entry.name), 0o755);
        } catch {
          // best-effort
        }
      }
    }
    fs.rmSync(dir, { recursive: true, force: true });
  });
  return target;
}

function writeBinary(dir: string): string {
  const bin = path.join(dir, 'agentico');
  fs.writeFileSync(bin, '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  return bin;
}

function ctx(overrides: Partial<ResourceContext>): ResourceContext {
  return {
    platform: 'darwin',
    isPackaged: true,
    resourcesPath: '/Applications/Agentico.app/Contents/Resources',
    appRoot: '/repo',
    env: {},
    ...overrides,
  };
}

describe('serverBinaryCandidates', () => {
  it('prefers the packaged resources bin/ layout on macOS', () => {
    const candidates = serverBinaryCandidates(
      ctx({ platform: 'darwin', resourcesPath: '/Applications/Agentico.app/Contents/Resources' }),
    );
    expect(candidates[0]).toBe('/Applications/Agentico.app/Contents/Resources/bin/agentico');
    expect(candidates).toContain('/Applications/Agentico.app/Contents/Resources/agentico');
  });

  it('covers the linux AppImage/deb resources layout', () => {
    const candidates = serverBinaryCandidates(
      ctx({ platform: 'linux', resourcesPath: '/opt/Agentico/resources' }),
    );
    expect(candidates[0]).toBe('/opt/Agentico/resources/bin/agentico');
    expect(candidates).toContain('/opt/Agentico/resources/agentico');
  });

  it('in development honors AGENTICO_SERVER_BIN then the repo bin/', () => {
    const candidates = serverBinaryCandidates(
      ctx({
        isPackaged: false,
        appRoot: '/repo',
        env: { AGENTICO_SERVER_BIN: '/custom/agentico' },
      }),
    );
    expect(candidates[0]).toBe('/custom/agentico');
    expect(candidates).toContain('/repo/bin/agentico');
  });

  it('ignores the env override for packaged builds', () => {
    const candidates = serverBinaryCandidates(
      ctx({ isPackaged: true, env: { AGENTICO_SERVER_BIN: '/custom/agentico' } }),
    );
    expect(candidates).not.toContain('/custom/agentico');
  });
});

describe('resolveServerBinary (real filesystem)', () => {
  it('resolves inside a resources path containing spaces and non-ASCII characters', () => {
    const resources = tempDir(path.join('Ágentico Files', '資源 resources'));
    fs.mkdirSync(path.join(resources, 'bin'));
    const bin = writeBinary(path.join(resources, 'bin'));

    const result = resolveServerBinary(
      ctx({ platform: 'darwin', isPackaged: true, resourcesPath: resources }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result).toEqual({ ok: true, path: bin });
  });

  it('resolves from a read-only installation root', () => {
    const resources = tempDir('resources');
    const binDir = path.join(resources, 'bin');
    fs.mkdirSync(binDir);
    const bin = writeBinary(binDir);
    fs.chmodSync(binDir, 0o555);
    fs.chmodSync(resources, 0o555);

    const result = resolveServerBinary(
      ctx({ platform: 'linux', isPackaged: true, resourcesPath: resources }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result).toEqual({ ok: true, path: bin });
  });

  it('falls back to the flat resources layout when bin/ is absent', () => {
    const resources = tempDir('resources with space');
    const bin = writeBinary(resources);
    const result = resolveServerBinary(
      ctx({ platform: 'linux', isPackaged: true, resourcesPath: resources }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result).toEqual({ ok: true, path: bin });
  });

  it('resolves the dev env override in a non-ASCII path', () => {
    const dir = tempDir('béta çalışma alanı');
    const bin = writeBinary(dir);
    const result = resolveServerBinary(
      ctx({ isPackaged: false, env: { AGENTICO_SERVER_BIN: bin }, appRoot: '/nonexistent' }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result).toEqual({ ok: true, path: bin });
  });

  it('reports every candidate tried when nothing resolves', () => {
    const resources = tempDir('empty');
    const result = resolveServerBinary(
      ctx({ platform: 'darwin', isPackaged: true, resourcesPath: resources }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.tried).toEqual([
        path.join(resources, 'bin', 'agentico'),
        path.join(resources, 'agentico'),
      ]);
    }
  });

  it('rejects a present but non-executable file', () => {
    const resources = tempDir('resources');
    const bin = path.join(resources, 'agentico');
    fs.writeFileSync(bin, 'data', { mode: 0o644 });
    const result = resolveServerBinary(
      ctx({ platform: 'linux', isPackaged: true, resourcesPath: resources }),
      { isExecutableFile: fileIsExecutable },
    );
    expect(result.ok).toBe(false);
  });
});
