import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';

import { runLinuxRelease } from './package-linux-release.mjs';

const GiB = 1024 ** 3;
const scriptPath = fileURLToPath(new URL('./package-linux-release.mjs', import.meta.url));
const tempRoots = [];

afterEach(() => {
  for (const root of tempRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function makeTempRoot() {
  const root = mkdtempSync(join(tmpdir(), 'agentico-linux-release-'));
  tempRoots.push(root);
  mkdirSync(join(root, 'desktop', 'dist'), { recursive: true });
  return root;
}

function writeReleaseOutputs(root, arch) {
  const distDir = join(root, 'desktop', 'dist');
  const debArch = arch === 'x64' ? 'amd64' : arch;
  for (const name of [
    `Agentico-${arch}.AppImage`,
    `agentico_0.150.0_${debArch}.deb`,
    `package-verification-linux-${arch}.json`,
  ]) {
    writeFileSync(join(distDir, name), `${arch}\n`);
  }
}

function validFixtureOptions(overrides = {}) {
  const repoRoot = makeTempRoot();
  return {
    repoRoot,
    gitCommonDir: join(repoRoot, '.git'),
    gitStatus: '',
    exactTag: 'v0.150.0',
    freeBytes: 20 * GiB,
    dockerAvailable: true,
    execute: (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch !== undefined) writeReleaseOutputs(repoRoot, arch);
    },
    ...overrides,
  };
}

describe('runLinuxRelease', () => {
  it('rejects a dirty checkout before attempting a container build', () => {
    const repoRoot = makeTempRoot();
    expect(() =>
      runLinuxRelease({
        repoRoot,
        gitStatus: ' M Makefile',
        exactTag: 'v0.150.0',
        freeBytes: 20 * GiB,
        dockerAvailable: true,
        execute: () => {
          throw new Error('must not execute');
        },
      }),
    ).toThrow(/working tree is dirty/);
  });

  it('returns x64 then arm64 receipts only after both builds succeed', () => {
    const options = validFixtureOptions();
    expect(runLinuxRelease(options)).toEqual({
      tag: 'v0.150.0',
      completed: ['x64', 'arm64'],
      receipts: ['package-verification-linux-x64.json', 'package-verification-linux-arm64.json'],
    });
    expect(
      existsSync(join(options.repoRoot, 'desktop', 'dist', 'package-verification-linux-x64.json')),
    ).toBe(true);
    expect(
      existsSync(
        join(options.repoRoot, 'desktop', 'dist', 'package-verification-linux-arm64.json'),
      ),
    ).toBe(true);
  });

  it('rejects an unavailable Docker daemon without running a command', () => {
    expect(() =>
      runLinuxRelease(
        validFixtureOptions({
          dockerAvailable: false,
          execute: () => {
            throw new Error('must not execute');
          },
        }),
      ),
    ).toThrow(/Docker daemon is unavailable/);
  });

  it('rejects a workspace with less than 12 GiB free', () => {
    expect(() =>
      runLinuxRelease(
        validFixtureOptions({
          freeBytes: 12 * GiB - 1,
          execute: () => {
            throw new Error('must not execute');
          },
        }),
      ),
    ).toThrow(/at least 12 GiB/);
  });

  it('rejects a non-exact release tag', () => {
    expect(() =>
      runLinuxRelease(
        validFixtureOptions({
          exactTag: 'v0.150.0-rc.1',
          execute: () => {
            throw new Error('must not execute');
          },
        }),
      ),
    ).toThrow(/invalid release tag/);
  });

  it('does not begin the arm64 build when the x64 build fails', () => {
    const options = validFixtureOptions();
    const arm64AttemptPath = join(options.repoRoot, 'arm64-attempted');
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'x64') throw new Error('x64 package verification failed');
      if (arch === 'arm64') writeFileSync(arm64AttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/x64 package verification failed/);
    expect(existsSync(arm64AttemptPath)).toBe(false);
  });

  it('rejects a completed container that does not write its x64 receipt', () => {
    const options = validFixtureOptions({ execute: () => {} });

    expect(() => runLinuxRelease(options)).toThrow(
      /x64 package verification did not produce required release files/,
    );
  });
});

describe('--print-plan', () => {
  it('prints the two Docker invocations as JSON without a Docker daemon', () => {
    const repoRoot = makeTempRoot();
    execFileSync('git', ['init', '--quiet'], { cwd: repoRoot });

    const result = spawnSync(process.execPath, [scriptPath, '--print-plan'], {
      cwd: repoRoot,
      encoding: 'utf8',
    });

    expect(result.status).toBe(0);
    expect(result.stderr).toBe('');
    const plan = JSON.parse(result.stdout);
    expect(plan.map(({ arch }) => arch)).toEqual(['x64', 'arm64']);
    expect(plan[0].args).toContain('AGENTICO_PACKAGE_ARCH=x64');
    expect(plan[1].args).toContain('AGENTICO_PACKAGE_ARCH=arm64');
  });
});
