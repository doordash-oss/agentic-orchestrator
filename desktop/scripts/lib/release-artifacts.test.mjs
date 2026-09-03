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

import { execFileSync } from 'node:child_process';
import {
  existsSync,
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import {
  LINUX_BUILDER_IMAGE,
  LINUX_ARM64_VERIFIER_IMAGE,
  createGitArchiveCommands,
  createLinuxDockerPlan,
  expectedDesktopArtifacts,
  releaseVersionFromTag,
  resolvePackageTarget,
  selectPackageArtifact,
  shellQuote,
} from './release-artifacts.mjs';

const tempRoots = [];

afterEach(() => {
  for (const root of tempRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function tempRoot() {
  const root = mkdtempSync(join(tmpdir(), 'agentico-release-plan-'));
  tempRoots.push(root);
  return root;
}

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function initializeRepository(root) {
  git(root, 'init', '--quiet');
  git(root, 'config', 'user.email', 'release-test@example.com');
  git(root, 'config', 'user.name', 'Release Test');
  writeFileSync(join(root, '.gitignore'), 'ignored.txt\n');
  writeFileSync(join(root, 'tracked.txt'), 'committed\n');
  git(root, 'add', '.gitignore', 'tracked.txt');
  git(root, 'commit', '--quiet', '-m', 'fixture');
  git(root, 'tag', 'v1.2.3');
}

function probeArchivedCheckout(sourceRoot) {
  const buildRoot = join(tempRoot(), 'build');
  mkdirSync(buildRoot);
  symlinkSync(join(sourceRoot, '.git'), join(buildRoot, '.git'));
  execFileSync('bash', ['-lc', createGitArchiveCommands(sourceRoot, buildRoot).join(' && ')]);
  expect(readFileSync(join(buildRoot, 'tracked.txt'), 'utf8')).toBe('committed\n');
  expect(existsSync(join(buildRoot, 'ignored.txt'))).toBe(false);
  expect(existsSync(join(buildRoot, 'local.txt'))).toBe(false);
  expect(git(buildRoot, 'describe', '--tags', '--exact-match')).toBe('v1.2.3');
  expect(git(buildRoot, 'rev-parse', 'HEAD')).toMatch(/^[0-9a-f]{40}$/);
  expect(git(buildRoot, 'status', '--porcelain')).toBe('');
}

describe('releaseVersionFromTag', () => {
  it('strips the v from a strict release tag', () => {
    expect(releaseVersionFromTag('v0.150.0')).toBe('0.150.0');
  });

  it.each(['0.150.0', 'v0.150', 'v0.150.0-rc.1', 'v0.150.0\n'])(
    'rejects non-release tag %j',
    (tag) => {
      expect(() => releaseVersionFromTag(tag)).toThrow(/^invalid release tag:/);
    },
  );
});

describe('expectedDesktopArtifacts', () => {
  it('defines the complete v0.150.0 desktop inventory', () => {
    expect(expectedDesktopArtifacts('v0.150.0').map(({ name }) => name)).toEqual([
      'Agentico-mac-universal.dmg',
      'Agentico-x64.AppImage',
      'Agentico-arm64.AppImage',
      'agentico_0.150.0_amd64.deb',
      'agentico_0.150.0_arm64.deb',
    ]);
  });

  it('returns a frozen inventory', () => {
    expect(Object.isFrozen(expectedDesktopArtifacts('v0.150.0'))).toBe(true);
  });
});

describe('resolvePackageTarget', () => {
  it('resolves an explicit arm64 package target independently of process.arch', () => {
    expect(resolvePackageTarget('linux', 'x64', 'arm64')).toEqual({ os: 'linux', arch: 'arm64' });
    expect(resolvePackageTarget('linux', 'arm64', 'x64')).toEqual({ os: 'linux', arch: 'x64' });
  });

  it('maps macOS packages to the universal target', () => {
    expect(resolvePackageTarget('darwin', 'arm64')).toEqual({ os: 'darwin', arch: 'universal' });
  });

  it('rejects unknown package architectures', () => {
    expect(() => resolvePackageTarget('linux', 'x64', 'ia32')).toThrow(
      /unsupported package architecture: ia32/,
    );
  });
});

describe('selectPackageArtifact', () => {
  it('selects only the requested Linux architecture when dist contains both', () => {
    const files = [
      'Agentico-x64.AppImage',
      'Agentico-arm64.AppImage',
      'agentico_0.150.0_amd64.deb',
      'agentico_0.150.0_arm64.deb',
    ];
    expect(selectPackageArtifact(files, { os: 'linux', arch: 'arm64' }, 'AppImage')).toBe(
      'Agentico-arm64.AppImage',
    );
    expect(selectPackageArtifact(files, { os: 'linux', arch: 'x64' }, 'deb')).toBe(
      'agentico_0.150.0_amd64.deb',
    );
  });

  it('rejects a missing target artifact', () => {
    expect(() => selectPackageArtifact([], { os: 'linux', arch: 'arm64' }, 'AppImage')).toThrow(
      /exactly one arm64 AppImage/,
    );
  });

  it('rejects duplicate target artifacts instead of choosing nondeterministically', () => {
    expect(() =>
      selectPackageArtifact(
        ['Agentico-arm64.AppImage', 'Agentico-arm64.AppImage'],
        { os: 'linux', arch: 'arm64' },
        'AppImage',
      ),
    ).toThrow(/exactly one arm64 AppImage/);
  });
});

describe('createLinuxDockerPlan', () => {
  const stagingDirs = Object.freeze({
    x64: '/repo/.git/agentico-release-staging/run/x64',
    arm64: '/repo/.git/agentico-release-staging/run/arm64',
  });

  function planOptions(overrides = {}) {
    return {
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
      cacheVolumePrefix: 'agentico-release',
      version: '0.150.0',
      stagingDirs,
      ...overrides,
    };
  }

  it('builds sequential pinned Docker invocations with worktree Git metadata', () => {
    const plan = createLinuxDockerPlan(planOptions());
    expect(plan.map(({ arch }) => arch)).toEqual(['x64', 'arm64']);
    expect(plan[0].args).toEqual(
      expect.arrayContaining([
        '--rm',
        '--platform',
        'linux/amd64',
        '-e',
        'AGENTICO_PACKAGE_ARCH=x64',
        '--mount',
        'type=bind,src=/repo/worktree,dst=/agentico-release-source,readonly',
        '--mount',
        'type=bind,src=/repo/.git,dst=/repo/.git,readonly',
        '--mount',
        'type=bind,src=/repo/worktree/.git,dst=/agentico-release-build/.git,readonly',
        '--mount',
        'type=bind,src=/repo/.git/agentico-release-staging/run/x64,dst=/agentico-release-export',
      ]),
    );
    expect(plan[0].args.join(' ')).toContain(LINUX_BUILDER_IMAGE);
    expect(LINUX_BUILDER_IMAGE).toBe(
      'electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9',
    );
    expect(plan[0].args).toEqual(
      expect.arrayContaining([
        '--mount',
        'type=volume,src=agentico-release-node-modules,dst=/agentico-release-build/node_modules',
        '--mount',
        'type=volume,src=agentico-release-npm-cache,dst=/root/.npm',
        '--mount',
        'type=volume,src=agentico-release-electron,dst=/root/.cache/electron',
        '--mount',
        'type=volume,src=agentico-release-electron-builder,dst=/root/.cache/electron-builder',
        'bash',
        '-lc',
        expect.stringContaining(
          'npm ci --fetch-retries=5 --fetch-retry-mintimeout=1000 --fetch-retry-maxtimeout=30000 --fetch-timeout=300000 && npm run package:verify --workspace desktop',
        ),
      ]),
    );
  });

  it('bootstraps the pinned Go toolchain required by the release module', () => {
    const plan = createLinuxDockerPlan(planOptions());

    const command = plan[0].args.at(-1);
    expect(command).toContain('go1.25.0.linux-amd64.tar.gz');
    expect(command).toContain('2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613');
    expect(command).toContain('sha256sum --check');
    expect(command).toContain('PATH=/usr/local/go/bin:$PATH');
  });

  it('verifies the arm64 package in a matching pinned runtime container', () => {
    const arm64 = createLinuxDockerPlan(planOptions())[1];

    expect(arm64.args.at(-1)).toContain('npm run package:build --workspace desktop');
    expect(arm64.verificationArgs).toEqual(
      expect.arrayContaining([
        '--platform',
        'linux/arm64',
        '-e',
        'AGENTICO_PACKAGE_ARCH=arm64',
        LINUX_ARM64_VERIFIER_IMAGE,
      ]),
    );
    expect(LINUX_ARM64_VERIFIER_IMAGE).toBe(
      'node:22.22.2-bookworm@sha256:62e4daa6819762bbd3072af77cc282ab72c631c4aed30dd7980192babaf385b3',
    );
    expect(arm64.verificationArgs.at(-1)).toContain('go1.25.0.linux-arm64.tar.gz');
    expect(arm64.verificationArgs.at(-1)).toContain(
      '05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae',
    );
    expect(arm64.verificationArgs.at(-1)).toContain('node desktop/scripts/verify-package.mjs');
  });

  it('returns a frozen plan', () => {
    expect(Object.isFrozen(createLinuxDockerPlan(planOptions()))).toBe(true);
  });

  it('mounts a linked-worktree .git entry separately while copying no Git metadata', () => {
    const plan = createLinuxDockerPlan(
      planOptions({
        gitEntry: '/repo/worktrees/linux/.git',
      }),
    );
    expect(plan[0].args).toContain(
      'type=bind,src=/repo/worktrees/linux/.git,dst=/agentico-release-source/.git,readonly',
    );
    expect(plan[0].args).toContain(
      'type=bind,src=/repo/worktrees/linux/.git,dst=/agentico-release-build/.git,readonly',
    );
    expect(plan[0].args.at(-1)).toContain(
      "git -C '/agentico-release-source' archive --format=tar HEAD",
    );
  });

  it('extracts exactly committed HEAD into the container-local checkout', () => {
    const plan = createLinuxDockerPlan(planOptions());

    expect(plan[0].args.at(-1)).toContain(
      "git -C '/agentico-release-source' archive --format=tar HEAD | tar -C '/agentico-release-build' -xf -",
    );
  });

  it('mounts only the current target staging directory and no host build outputs', () => {
    const plan = createLinuxDockerPlan(planOptions());
    const x64 = plan[0].args;
    const arm64 = plan[1].args;
    expect(x64).toContain(`type=bind,src=${stagingDirs.x64},dst=/agentico-release-export`);
    expect(x64).not.toContain(`type=bind,src=${stagingDirs.arm64},dst=/agentico-release-export`);
    expect(arm64).toContain(`type=bind,src=${stagingDirs.arm64},dst=/agentico-release-export`);
    expect(arm64).not.toContain(`type=bind,src=${stagingDirs.x64},dst=/agentico-release-export`);
    for (const invocation of plan.flatMap(({ args, verificationArgs = [] }) => [
      args,
      verificationArgs,
    ])) {
      expect(invocation.some((arg) => arg.includes('/desktop/dist:'))).toBe(false);
      expect(invocation.some((arg) => arg.includes('/desktop/out:'))).toBe(false);
      expect(invocation.some((arg) => arg.includes('/desktop/resources:'))).toBe(false);
    }
  });

  it('exports only exact target artifacts and keeps x64 invisible to arm verification', () => {
    const plan = createLinuxDockerPlan(planOptions());
    expect(plan[0].args.at(-1)).toContain(
      "cp -- '/agentico-release-build/desktop/dist/Agentico-x64.AppImage' '/agentico-release-export/Agentico-x64.AppImage'",
    );
    expect(plan[0].args.at(-1)).toContain('package-verification-linux-x64.json');
    expect(plan[1].args.at(-1)).not.toContain('package-verification-linux-arm64.json');
    expect(plan[1].verificationArgs.at(-1)).toContain('Agentico-arm64.AppImage');
    expect(plan[1].verificationArgs.at(-1)).not.toContain('Agentico-x64.AppImage');
    expect(plan[1].verificationArgs.at(-1)).toContain('package-verification-linux-arm64.json');
  });

  it('passes hostile host paths as literal Docker arguments, never shell source', () => {
    const hostileRoot = "/repo/space $() `tick` 'quote'";
    const hostileGit = `${hostileRoot}/.git`;
    const hostileStages = {
      x64: `${hostileGit}/agentico release/x64`,
      arm64: `${hostileGit}/agentico release/arm64`,
    };
    const plan = createLinuxDockerPlan(
      planOptions({ repoRoot: hostileRoot, gitCommonDir: hostileGit, stagingDirs: hostileStages }),
    );
    expect(plan[0].args).toContain(
      `type=bind,src=${hostileRoot},dst=/agentico-release-source,readonly`,
    );
    expect(plan[0].args).toContain(
      `type=bind,src=${hostileStages.x64},dst=/agentico-release-export`,
    );
    expect(plan[0].args.at(-1)).not.toContain(hostileRoot);
    expect(plan[0].args.at(-1)).not.toContain('$()');
    expect(plan[0].args.at(-1)).not.toContain('`tick`');
  });

  it('scopes executable volumes per run while keeping only the npm content cache global', () => {
    const plan = createLinuxDockerPlan(planOptions({ volumePrefix: 'agentico-release-run123' }));
    expect(plan[0].args).toContain(
      'type=volume,src=agentico-release-run123-node-modules,dst=/agentico-release-build/node_modules',
    );
    expect(plan[1].verificationArgs).toContain(
      'type=volume,src=agentico-release-run123-verifier-node-modules,dst=/agentico-release-build/node_modules',
    );
    expect(plan[1].verificationArgs).not.toContain(
      'type=volume,src=agentico-release-run123-node-modules,dst=/agentico-release-build/node_modules',
    );
    expect(plan[1].verificationArgs.at(-1)).toContain('npm ci --ignore-scripts');
    expect(plan[0].args).toContain('type=volume,src=agentico-release-npm-cache,dst=/root/.npm');
    expect(plan[0].args).toContain(
      'type=volume,src=agentico-release-run123-electron,dst=/root/.cache/electron',
    );
    expect(plan[0].args).toContain(
      'type=volume,src=agentico-release-run123-electron-builder,dst=/root/.cache/electron-builder',
    );
    expect(plan[0].args.join('\n')).not.toContain('agentico-release-run123-npm-cache');
  });

  it('accepts colons in bind sources and rejects --mount delimiters', () => {
    const colonRoot = '/repo/colon:path';
    expect(
      createLinuxDockerPlan(
        planOptions({
          repoRoot: colonRoot,
          gitEntry: `${colonRoot}/.git`,
          stagingDirs: {
            x64: '/git/stage:colon/x64',
            arm64: '/git/stage:colon/arm64',
          },
        }),
      )[0].args,
    ).toContain(`type=bind,src=${colonRoot},dst=/agentico-release-source,readonly`);
    for (const invalid of [
      '/repo/comma,path',
      '/repo/double"quote',
      '/repo/carriage\rreturn',
      '/repo/new\nline',
    ]) {
      expect(() => createLinuxDockerPlan(planOptions({ repoRoot: invalid }))).toThrow(
        /cannot contain comma, double quote, or newline/,
      );
    }
  });
});

describe('shellQuote', () => {
  it('preserves spaces, substitutions, backticks, and single quotes as one literal word', () => {
    expect(shellQuote("space $() `tick` 'quote'")).toBe(`'space $() \`tick\` '"'"'quote'"'"''`);
  });
});

describe('committed HEAD archive checkout', () => {
  it('supports release Git commands in a normal checkout without local files', () => {
    const source = tempRoot();
    initializeRepository(source);
    writeFileSync(join(source, 'tracked.txt'), 'modified\n');
    writeFileSync(join(source, 'ignored.txt'), 'ignored\n');
    writeFileSync(join(source, 'local.txt'), 'local\n');
    probeArchivedCheckout(source);
  }, 20_000);

  it('supports release Git commands from a linked worktree gitfile', () => {
    const main = tempRoot();
    initializeRepository(main);
    const linked = join(tempRoot(), 'linked');
    git(main, 'worktree', 'add', '--quiet', linked, 'HEAD');
    writeFileSync(join(linked, 'tracked.txt'), 'modified\n');
    writeFileSync(join(linked, 'ignored.txt'), 'ignored\n');
    probeArchivedCheckout(linked);
  }, 20_000);
});
