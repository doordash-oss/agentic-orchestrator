import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';

import {
  prepareReleaseDirectories,
  runLinuxRelease,
  runLocalLinuxRelease,
  resolveLinuxReleaseEvidence,
} from './package-linux-release.mjs';
import { LINUX_ARM64_VERIFIER_IMAGE } from './lib/release-artifacts.mjs';
import { LINUX_BUILDER_IMAGE } from './lib/release-artifacts.mjs';
import { evidencePathFor } from './release-preflight.mjs';
import {
  createDetachedReleaseWorkspace,
  removeDetachedReleaseWorkspace,
} from './release-workspace.mjs';

const GiB = 1024 ** 3;
const scriptPath = fileURLToPath(new URL('./package-linux-release.mjs', import.meta.url));
const tempRoots = [];

afterEach(() => {
  for (const root of tempRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function makeTempRoot(name) {
  const base = mkdtempSync(join(tmpdir(), 'agentico-linux-release-'));
  tempRoots.push(base);
  const root = name === undefined ? base : join(base, name);
  if (name !== undefined) mkdirSync(root);
  mkdirSync(join(root, 'desktop', 'dist'), { recursive: true });
  mkdirSync(join(root, '.git'), { recursive: true });
  return root;
}

function stagingDir(root, arch) {
  return join(root, '.git', 'agentico-release-staging', 'run-123', arch);
}

function writeReleaseOutputs(root, arch, { receipt = true } = {}) {
  const distDir = stagingDir(root, arch);
  mkdirSync(distDir, { recursive: true });
  const debArch = arch === 'x64' ? 'amd64' : arch;
  const names = [`Agentico-${arch}.AppImage`, `agentico_0.150.0_${debArch}.deb`];
  const evidence = new Map();
  for (const name of names) {
    const bytes = Buffer.from(`${arch}-${name}\n`);
    writeFileSync(join(distDir, name), bytes);
    evidence.set(name, {
      sha256: createHash('sha256').update(bytes).digest('hex'),
      size: bytes.length,
    });
  }
  if (receipt) {
    const artifacts = names.map((name, index) => ({
      target: { os: 'linux', arch },
      format: index === 0 ? 'AppImage' : 'deb',
      path: `/agentico-release-build/desktop/dist/${name}`,
      sha256: evidence.get(name).sha256,
      size: evidence.get(name).size,
      identity: {},
    }));
    writeFileSync(
      join(distDir, `package-verification-linux-${arch}.json`),
      `${JSON.stringify({ schema_version: 2, target: { os: 'linux', arch }, artifacts })}\n`,
    );
  }
}

function writeInvocationOutputs(root, args) {
  const arch = args
    .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
    ?.split('=')
    .at(-1);
  if (arch === 'x64') writeReleaseOutputs(root, arch);
  if (arch === 'arm64') {
    writeReleaseOutputs(root, arch, { receipt: args.includes(LINUX_ARM64_VERIFIER_IMAGE) });
  }
}

function validFixtureOptions(overrides = {}) {
  const repoRoot = overrides.repoRoot ?? makeTempRoot();
  return {
    repoRoot,
    gitCommonDir: join(repoRoot, '.git'),
    gitWorktreeDir: join(repoRoot, '.git'),
    gitEntry: join(repoRoot, '.git'),
    runId: 'run-123',
    gitStatus: '',
    exactTag: 'v0.150.0',
    freeBytes: 20 * GiB,
    dockerAvailable: true,
    execute: (_command, args) => {
      writeInvocationOutputs(repoRoot, args);
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
      if (command === 'rev-parse --git-dir') return '.git';
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

  it('cleans its three per-run executable volumes and staging tree after success', () => {
    const options = validFixtureOptions({ volumePrefix: 'agentico-release-run-123' });
    const calls = [];
    options.execute = (_command, args) => {
      calls.push(args);
      writeInvocationOutputs(options.repoRoot, args);
    };

    runLinuxRelease(options);

    expect(calls.filter((args) => args[0] === 'volume')).toEqual([
      ['volume', 'rm', '--force', 'agentico-release-run-123-node-modules'],
      ['volume', 'rm', '--force', 'agentico-release-run-123-electron'],
      ['volume', 'rm', '--force', 'agentico-release-run-123-electron-builder'],
    ]);
    expect(existsSync(join(options.repoRoot, '.git', 'agentico-release-staging', 'run-123'))).toBe(
      false,
    );
  });

  it('cleans exact per-run resources after a failed x64 build', () => {
    const options = validFixtureOptions({ volumePrefix: 'agentico-release-run-123' });
    const calls = [];
    options.execute = (_command, args) => {
      calls.push(args);
      if (args.includes('AGENTICO_PACKAGE_ARCH=x64')) throw new Error('x64 failed');
    };

    expect(() => runLinuxRelease(options)).toThrow(/x64 failed/);
    expect(calls.filter((args) => args[0] === 'volume')).toEqual([
      ['volume', 'rm', '--force', 'agentico-release-run-123-node-modules'],
      ['volume', 'rm', '--force', 'agentico-release-run-123-electron'],
      ['volume', 'rm', '--force', 'agentico-release-run-123-electron-builder'],
    ]);
    expect(existsSync(join(options.repoRoot, '.git', 'agentico-release-staging', 'run-123'))).toBe(
      false,
    );
  });

  it('surfaces cleanup failure alongside the primary build failure', () => {
    const options = validFixtureOptions({ volumePrefix: 'agentico-release-run-123' });
    options.execute = (_command, args) => {
      if (args.includes('AGENTICO_PACKAGE_ARCH=x64')) throw new Error('primary x64 failure');
      if (args[0] === 'volume') throw new Error('volume cleanup failure');
    };

    let observed;
    try {
      runLinuxRelease(options);
    } catch (error) {
      observed = error;
    }
    expect(observed).toBeInstanceOf(AggregateError);
    expect(observed.cause).toMatchObject({ message: 'primary x64 failure' });
    expect(observed.errors.map(({ message }) => message)).toEqual(
      expect.arrayContaining(['primary x64 failure', 'volume cleanup failure']),
    );
    expect(
      observed.errors.filter(({ message }) => message === 'volume cleanup failure'),
    ).toHaveLength(3);
  });

  it('surfaces cleanup failure after successful packaging', () => {
    const options = validFixtureOptions({ volumePrefix: 'agentico-release-run-123' });
    options.execute = (_command, args) => {
      writeInvocationOutputs(options.repoRoot, args);
      if (args[0] === 'volume') throw new Error('volume cleanup failure');
    };
    expect(() => runLinuxRelease(options)).toThrow(/volume cleanup failure/);
  });

  it('passes the validated Git entry through to each Docker build checkout', () => {
    const options = validFixtureOptions();
    const dockerInvocations = [];
    options.execute = (_command, args) => {
      dockerInvocations.push(args);
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch !== undefined) writeInvocationOutputs(options.repoRoot, args);
    };

    runLinuxRelease(options);

    expect(
      dockerInvocations
        .filter((args) => args.includes('linux/amd64'))
        .some((args) =>
          args.includes(
            `type=bind,src=${options.gitEntry},dst=/agentico-release-build/.git,readonly`,
          ),
        ),
    ).toBe(true);
  });

  it('assembles exact staged outputs into final dist and rewrites receipt paths canonically', () => {
    const options = validFixtureOptions();
    runLinuxRelease(options);
    const receipt = JSON.parse(
      readFileSync(
        join(options.repoRoot, 'desktop', 'dist', 'package-verification-linux-x64.json'),
        'utf8',
      ),
    );
    const canonicalRoot = realpathSync(options.repoRoot);
    expect(receipt.artifacts.map(({ path }) => path)).toEqual([
      join(canonicalRoot, 'desktop', 'dist', 'Agentico-x64.AppImage'),
      join(canonicalRoot, 'desktop', 'dist', 'agentico_0.150.0_amd64.deb'),
    ]);
  });

  it('rejects an unexpected staging file before assembling target outputs', () => {
    const options = validFixtureOptions();
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'x64') {
        writeReleaseOutputs(options.repoRoot, arch);
        writeFileSync(join(stagingDir(options.repoRoot, arch), 'unexpected'), 'hostile\n');
      }
    };
    expect(() => runLinuxRelease(options)).toThrow(/unexpected staging entries.*unexpected/);
    expect(readdirSync(join(options.repoRoot, 'desktop', 'dist'))).toEqual([]);
  });

  it('rejects a symlinked staging artifact', () => {
    const options = validFixtureOptions();
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'x64') {
        writeReleaseOutputs(options.repoRoot, arch);
        const artifact = join(stagingDir(options.repoRoot, arch), 'Agentico-x64.AppImage');
        rmSync(artifact);
        symlinkSync('/etc/passwd', artifact);
      }
    };
    expect(() => runLinuxRelease(options)).toThrow(/staging entry is not a regular file/);
  });

  it('rejects symlinked staging parents before Docker', () => {
    const options = validFixtureOptions();
    const stagingRoot = join(options.repoRoot, '.git', 'agentico-release-staging');
    symlinkSync(join(options.repoRoot, 'desktop'), stagingRoot);
    let contacted = false;
    options.execute = () => {
      contacted = true;
    };
    expect(() => runLinuxRelease(options)).toThrow(/staging.*symbolic link/i);
    expect(contacted).toBe(false);
  });

  it('rejects stale staging entries before Docker', () => {
    const options = validFixtureOptions();
    const target = stagingDir(options.repoRoot, 'x64');
    mkdirSync(target, { recursive: true });
    writeFileSync(join(target, 'stale.AppImage'), 'stale\n');
    let contacted = false;
    options.execute = () => {
      contacted = true;
    };
    expect(() => runLinuxRelease(options)).toThrow(/stale or unexpected entries/);
    expect(contacted).toBe(false);
  });

  it('rejects a symlinked final dist directory before Docker', () => {
    const options = validFixtureOptions();
    rmSync(join(options.repoRoot, 'desktop', 'dist'), { recursive: true });
    symlinkSync(join(options.repoRoot, '.git'), join(options.repoRoot, 'desktop', 'dist'));
    let contacted = false;
    options.execute = () => {
      contacted = true;
    };
    expect(() => runLinuxRelease(options)).toThrow(/final dist.*symbolic link/i);
    expect(contacted).toBe(false);
  });

  it('rejects staging directories outside the per-worktree Git metadata root', () => {
    const root = makeTempRoot();
    expect(() =>
      prepareReleaseDirectories({
        repoRoot: root,
        gitWorktreeDir: join(root, '.git'),
        runId: '../../desktop/dist',
      }),
    ).toThrow(/invalid release run id/);
  });

  it.each(['comma,path', 'double"quote', 'carriage\rreturn', 'new\nline'])(
    'rejects Docker --mount delimiter path %j before contacting Docker',
    (name) => {
      const repoRoot = makeTempRoot(name);
      let dockerContacted = false;
      const options = validFixtureOptions({
        repoRoot,
        dockerAvailable: () => {
          dockerContacted = true;
          return true;
        },
      });
      expect(() => runLinuxRelease(options)).toThrow(
        /cannot contain comma, double quote, or newline/,
      );
      expect(dockerContacted).toBe(false);
    },
  );

  it('accepts a colon in host bind paths', () => {
    const options = validFixtureOptions({ repoRoot: makeTempRoot('colon:path') });
    expect(runLinuxRelease(options).completed).toEqual(['x64', 'arm64']);
  });

  it('detects target-stage replacement between x64 and arm64 before the arm bind', () => {
    const options = validFixtureOptions();
    const redirected = join(options.repoRoot, 'redirected-arm-stage');
    mkdirSync(redirected);
    let provenanceChecks = 0;
    let armBuildStarted = false;
    options.execute = (_command, args) => {
      if (args.includes('AGENTICO_PACKAGE_ARCH=arm64')) armBuildStarted = true;
      writeInvocationOutputs(options.repoRoot, args);
    };
    options.verifyProvenance = () => {
      provenanceChecks += 1;
      if (provenanceChecks === 1) {
        const armStage = stagingDir(options.repoRoot, 'arm64');
        renameSync(armStage, `${armStage}-original`);
        symlinkSync(redirected, armStage);
      }
    };
    expect(() => runLinuxRelease(options)).toThrow(/directory changed|symbolic link/i);
    expect(armBuildStarted).toBe(false);
    expect(readdirSync(redirected)).toEqual([]);
  });

  it('detects final-dist replacement before assembly and writes nothing redirected', () => {
    const options = validFixtureOptions();
    const redirected = join(options.repoRoot, 'redirected-final-dist');
    mkdirSync(redirected);
    options.execute = (_command, args) => writeInvocationOutputs(options.repoRoot, args);
    options.verifyProvenance = () => {
      const dist = join(options.repoRoot, 'desktop', 'dist');
      if (!existsSync(`${dist}-original`)) {
        renameSync(dist, `${dist}-original`);
        symlinkSync(redirected, dist);
      }
    };
    expect(() => runLinuxRelease(options)).toThrow(/directory changed|symbolic link/i);
    expect(readdirSync(redirected)).toEqual([]);
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
      if (arch === 'arm64' && !args.includes(LINUX_ARM64_VERIFIER_IMAGE)) {
        writeReleaseOutputs(options.repoRoot, 'arm64', { receipt: false });
      }
      if (arch === 'arm64' && args.includes(LINUX_ARM64_VERIFIER_IMAGE)) {
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

  it('stops before arm64 when the x64 container changes release provenance', () => {
    const options = validFixtureOptions();
    const arm64AttemptPath = join(options.repoRoot, 'arm64-attempted');
    options.verifyProvenance = () => {
      throw new Error('release provenance changed');
    };
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'x64') writeReleaseOutputs(options.repoRoot, 'x64');
      if (arch === 'arm64') writeFileSync(arm64AttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/release provenance changed/);
    expect(existsSync(arm64AttemptPath)).toBe(false);
  });

  it('rejects a completed container that does not write its x64 receipt', () => {
    const options = validFixtureOptions({ execute: () => {} });

    expect(() => runLinuxRelease(options)).toThrow(/missing required entries/);
  });

  it('removes stale x64 outputs before requiring the current build to produce them', () => {
    const options = validFixtureOptions();
    const finalDist = join(options.repoRoot, 'desktop', 'dist');
    for (const name of [
      'Agentico-x64.AppImage',
      'agentico_0.150.0_amd64.deb',
      'package-verification-linux-x64.json',
    ]) {
      writeFileSync(join(finalDist, name), 'stale\n');
    }
    const arm64AttemptPath = join(options.repoRoot, 'arm64-attempted');
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'arm64') writeFileSync(arm64AttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/missing required entries/);
    expect(readdirSync(finalDist)).toEqual([]);
    expect(existsSync(arm64AttemptPath)).toBe(false);
  });

  it('removes stale x86_64 intermediate output before a non-producing x64 build', () => {
    const options = validFixtureOptions();
    const staleIntermediate = join(options.repoRoot, 'desktop', 'dist', 'Agentico-x86_64.AppImage');
    const arm64AttemptPath = join(options.repoRoot, 'arm64-attempted');
    writeFileSync(staleIntermediate, 'stale x64 intermediate\n');
    options.execute = (_command, args) => {
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch === 'arm64') writeFileSync(arm64AttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/missing required entries/);
    expect(existsSync(staleIntermediate)).toBe(false);
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
      if (arch !== undefined) writeInvocationOutputs(options.repoRoot, args);
    };

    expect(runLinuxRelease(options).completed).toEqual(['x64', 'arm64']);
    expect(existsSync(pullPath)).toBe(true);
  });

  it('does not pull the arm64 verifier image when its inspection has a daemon error', () => {
    const options = validFixtureOptions();
    const pullAttemptPath = join(options.repoRoot, 'pull-attempted');
    options.execute = (_command, args) => {
      if (args.join(' ') === `image inspect ${LINUX_ARM64_VERIFIER_IMAGE}`) {
        const error = new Error('Cannot connect to the Docker daemon');
        error.stderr = 'Cannot connect to the Docker daemon';
        throw error;
      }
      if (args[0] === 'pull') writeFileSync(pullAttemptPath, 'attempted\n');
    };

    expect(() => runLinuxRelease(options)).toThrow(/Cannot connect to the Docker daemon/);
    expect(existsSync(pullAttemptPath)).toBe(false);
  });

  it('pulls the arm64 verifier image after an image-not-found inspection result', () => {
    const options = validFixtureOptions();
    const pullPath = join(options.repoRoot, 'pulled-arm64-verifier-image');
    options.execute = (_command, args) => {
      if (args.join(' ') === `image inspect ${LINUX_ARM64_VERIFIER_IMAGE}`) {
        const error = new Error('No such image');
        error.stderr = 'Error response from daemon: No such image';
        throw error;
      }
      if (args.join(' ') === `pull ${LINUX_ARM64_VERIFIER_IMAGE}`) {
        writeFileSync(pullPath, 'pulled\n');
      }
      const arch = args
        .find((arg) => arg.startsWith('AGENTICO_PACKAGE_ARCH='))
        ?.split('=')
        .at(-1);
      if (arch !== undefined) writeInvocationOutputs(options.repoRoot, args);
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

describe('detached release evidence boundary', () => {
  it('requires the explicit absolute evidence file and binds its path, run, tag, and commit', () => {
    const evidence = {
      evidence_path: '/git/agentico-release-preflight.json',
      run_id: '11111111-1111-4111-8111-111111111111',
      tag: 'v0.150.0',
      commit: 'a'.repeat(40),
    };
    expect(
      resolveLinuxReleaseEvidence({
        env: { AGENTICO_RELEASE_EVIDENCE_FILE: evidence.evidence_path },
        readEvidence: (path) => {
          expect(path).toBe(evidence.evidence_path);
          return evidence;
        },
        verifyEvidence: (value) => value,
      }),
    ).toEqual(evidence);
    expect(() => resolveLinuxReleaseEvidence({ env: {}, readEvidence: () => evidence })).toThrow(
      /must be an absolute validated path/,
    );
    expect(() =>
      resolveLinuxReleaseEvidence({
        env: { AGENTICO_RELEASE_EVIDENCE_FILE: '/different/evidence.json' },
        readEvidence: () => evidence,
        verifyEvidence: (value) => value,
      }),
    ).toThrow(/does not match captured/);
  });

  it.each(['normal checkout', 'linked worktree'])(
    'reads matching run/tag/commit evidence from a real %s release workspace',
    (kind) => {
      const main = mkdtempSync(join(tmpdir(), 'agentico-evidence-integration-'));
      tempRoots.push(main);
      execFileSync('git', ['init', '--quiet'], { cwd: main });
      execFileSync('git', ['config', 'user.email', 'test@example.com'], { cwd: main });
      execFileSync('git', ['config', 'user.name', 'Test'], { cwd: main });
      writeFileSync(join(main, 'tracked'), 'committed\n');
      execFileSync('git', ['add', 'tracked'], { cwd: main });
      execFileSync('git', ['commit', '--quiet', '-m', 'fixture'], { cwd: main });
      execFileSync('git', ['tag', 'v0.150.0'], { cwd: main });
      let operatorRoot = main;
      if (kind === 'linked worktree') {
        operatorRoot = mkdtempSync(join(tmpdir(), 'agentico-evidence-linked-'));
        rmSync(operatorRoot, { recursive: true });
        tempRoots.push(operatorRoot);
        execFileSync('git', ['worktree', 'add', '--quiet', '--detach', operatorRoot, 'HEAD'], {
          cwd: main,
        });
      }
      const commit = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: operatorRoot,
        encoding: 'utf8',
      }).trim();
      const runId =
        kind === 'linked worktree'
          ? '22222222-2222-4222-8222-222222222222'
          : '11111111-1111-4111-8111-111111111111';
      const workspace = createDetachedReleaseWorkspace({ operatorRoot, commit, runId });
      const evidencePath = evidencePathFor(operatorRoot);
      const evidence = {
        schema_version: 3,
        tag: 'v0.150.0',
        commit,
        platform: 'darwin',
        captured_at: '2026-08-10T00:00:00.000Z',
        run_id: runId,
        images: [LINUX_BUILDER_IMAGE, LINUX_ARM64_VERIFIER_IMAGE],
        operator_root: realpathSync(operatorRoot),
        workspace_root: workspace.path,
        workspace_token: workspace.token,
        evidence_path: evidencePath,
      };
      writeFileSync(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`);
      try {
        expect(
          resolveLinuxReleaseEvidence({
            env: { AGENTICO_RELEASE_EVIDENCE_FILE: evidencePath },
          }),
        ).toMatchObject({ run_id: runId, tag: 'v0.150.0', commit });
      } finally {
        removeDetachedReleaseWorkspace(workspace);
      }
    },
    20_000,
  );
});
