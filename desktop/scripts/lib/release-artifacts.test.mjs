import { describe, expect, it } from 'vitest';

import {
  LINUX_BUILDER_IMAGE,
  LINUX_ARM64_VERIFIER_IMAGE,
  createLinuxDockerPlan,
  expectedDesktopArtifacts,
  releaseVersionFromTag,
  resolvePackageTarget,
  selectPackageArtifact,
} from './release-artifacts.mjs';

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
  it('builds sequential pinned Docker invocations with worktree Git metadata', () => {
    const plan = createLinuxDockerPlan({
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
    });
    expect(plan.map(({ arch }) => arch)).toEqual(['x64', 'arm64']);
    expect(plan[0].args).toEqual(
      expect.arrayContaining([
        '--rm',
        '--platform',
        'linux/amd64',
        '-e',
        'AGENTICO_PACKAGE_ARCH=x64',
        '-v',
        '/repo/worktree:/agentico-release-source:ro',
        '-v',
        '/repo/.git:/repo/.git:ro',
        '-v',
        '/repo/worktree/desktop/dist:/repo/worktree/desktop/dist',
        '-v',
        '/repo/worktree/desktop/out:/repo/worktree/desktop/out',
        '-v',
        '/repo/worktree/desktop/resources:/repo/worktree/desktop/resources',
      ]),
    );
    expect(plan[0].args.join(' ')).toContain(LINUX_BUILDER_IMAGE);
    expect(LINUX_BUILDER_IMAGE).toBe(
      'electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9',
    );
    expect(plan[0].args).toEqual(
      expect.arrayContaining([
        '-v',
        'agentico-release-node-modules:/repo/worktree/node_modules',
        '-v',
        'agentico-release-electron:/root/.cache/electron',
        '-v',
        'agentico-release-electron-builder:/root/.cache/electron-builder',
        'bash',
        '-lc',
        expect.stringContaining('npm ci && npm run package:verify --workspace desktop'),
      ]),
    );
  });

  it('bootstraps the pinned Go toolchain required by the release module', () => {
    const plan = createLinuxDockerPlan({
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
    });

    const command = plan[0].args.at(-1);
    expect(command).toContain('go1.25.0.linux-amd64.tar.gz');
    expect(command).toContain('2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613');
    expect(command).toContain('sha256sum --check');
    expect(command).toContain('PATH=/usr/local/go/bin:$PATH');
  });

  it('verifies the arm64 package in a matching pinned runtime container', () => {
    const arm64 = createLinuxDockerPlan({
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
    })[1];

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
    expect(
      Object.isFrozen(
        createLinuxDockerPlan({
          repoRoot: '/repo/worktree',
          gitCommonDir: '/repo/.git',
          volumePrefix: 'agentico-release',
        }),
      ),
    ).toBe(true);
  });

  it('mounts a linked-worktree .git entry separately while copying no Git metadata', () => {
    const plan = createLinuxDockerPlan({
      repoRoot: '/repo/worktree',
      gitEntry: '/repo/worktrees/linux/.git',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
    });
    expect(plan[0].args).toContain('/repo/worktrees/linux/.git:/repo/worktree/.git:ro');
    expect(plan[0].args.at(-1)).toContain('--exclude=.git');
  });

  it('extracts the isolated source archive into the release checkout itself', () => {
    const plan = createLinuxDockerPlan({
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
    });

    expect(plan[0].args.at(-1)).toContain(
      'tar -C "/agentico-release-source" --exclude=.git --exclude=node_modules --exclude=desktop/dist --exclude=desktop/out --exclude=desktop/resources -cf - . | tar -C "/repo/worktree" -xf -',
    );
  });
});
