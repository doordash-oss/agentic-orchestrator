import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';

import { runLinuxRelease, runLocalLinuxRelease } from './package-linux-release.mjs';
import { LINUX_ARM64_VERIFIER_IMAGE } from './lib/release-artifacts.mjs';

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

function localFixtureOptions({ gitStatus = '', exactTag = 'v0.150.0', freeBytes = 20 * GiB } = {}) {
  const repoRoot = makeTempRoot();
  return {
    cwd: repoRoot,
    gitCommand: (_cwd, ...args) => {
      const command = args.join(' ');
      if (command === 'rev-parse --show-toplevel') return repoRoot;
      if (command === 'rev-parse --git-common-dir') return '.git';
      if (command === 'describe --tags --exact-match') return exactTag;
      if (command === 'status --porcelain') return gitStatus;
      throw new Error(`unexpected Git command: ${command}`);
    },
    statfs: () => ({ bavail: freeBytes, bsize: 1 }),
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

  it('does not accept arm64 outputs until the matching-architecture verifier succeeds', () => {
    const options = validFixtureOptions({ execute: () => {} });
    let arm64VerifierRan = false;
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'x64') writeReleaseOutputs(options.repoRoot, 'x64');
      if (args.includes(LINUX_ARM64_VERIFIER_IMAGE)) {
        arm64VerifierRan = true;
        writeReleaseOutputs(options.repoRoot, 'arm64');
      }
    };

    expect(runLinuxRelease(options).completed).toEqual(['x64', 'arm64']);
    expect(arm64VerifierRan).toBe(true);
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

  it('removes stale x64 outputs before requiring the current build to produce them', () => {
    const options = validFixtureOptions();
    writeReleaseOutputs(options.repoRoot, 'x64');
    const arm64AttemptPath = join(options.repoRoot, 'arm64-attempted');
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'arm64') writeFileSync(arm64AttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(
      /x64 package verification did not produce required release files/,
    );
    expect(existsSync(arm64AttemptPath)).toBe(false);
  });

  it('does not pull when pinned-image inspection fails for a daemon error', () => {
    const options = validFixtureOptions();
    const pullAttemptPath = join(options.repoRoot, 'pull-attempted');
    options.execute = (_command, args) => {
      if (
        args.join(' ') ===
        'image inspect electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9'
      ) {
        const error = new Error('Cannot connect to the Docker daemon');
        error.stderr = 'Cannot connect to the Docker daemon';
        throw error;
      }
      if (args[0] === 'pull') writeFileSync(pullAttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/Cannot connect to the Docker daemon/);
    expect(existsSync(pullAttemptPath)).toBe(false);
  });

  it('pulls the pinned image after an image-not-found inspection result', () => {
    const options = validFixtureOptions();
    const pullPath = join(options.repoRoot, 'pulled-image');
    options.execute = (_command, args) => {
      if (args[0] === 'image') {
        const error = new Error('No such image');
        error.stderr = 'Error response from daemon: No such image';
        error.status = 1;
        throw error;
      }
      if (args[0] === 'pull') writeFileSync(pullPath, 'pulled\n');
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch !== undefined) writeReleaseOutputs(options.repoRoot, arch);
    };

    expect(runLinuxRelease(options).completed).toEqual(['x64', 'arm64']);
    expect(existsSync(pullPath)).toBe(true);
  });
});

describe('runLocalLinuxRelease preflight ordering', () => {
  it.each([
    ['a dirty checkout', { gitStatus: ' M Makefile' }, /working tree is dirty/],
    ['a non-exact tag', { exactTag: 'v0.150.0-rc.1' }, /invalid release tag/],
    ['less than 12 GiB free', { freeBytes: 12 * GiB - 1 }, /at least 12 GiB/],
  ])('rejects %s before contacting Docker', (_description, fixture, expectedError) => {
    const options = localFixtureOptions(fixture);
    const dockerContactPath = join(options.cwd, 'docker-contacted');

    expect(() =>
      runLocalLinuxRelease({
        ...options,
        dockerInfo: () => writeFileSync(dockerContactPath, 'contacted\n'),
      }),
    ).toThrow(expectedError);
    expect(existsSync(dockerContactPath)).toBe(false);
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
