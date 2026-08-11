import { describe, expect, it } from 'vitest';

import {
  LINUX_BUILDER_IMAGE,
  LINUX_ARM64_VERIFIER_IMAGE,
  createLinuxDockerPlan,
  expectedDesktopArtifacts,
  releaseVersionFromTag,
  resolvePackageTarget,
  selectPackageArtifact,
  shellQuote,
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
  const stagingDirs = Object.freeze({
    x64: '/repo/.git/agentico-release-staging/run/x64',
    arm64: '/repo/.git/agentico-release-staging/run/arm64',
  });

  function planOptions(overrides = {}) {
    return {
      repoRoot: '/repo/worktree',
      gitCommonDir: '/repo/.git',
      volumePrefix: 'agentico-release',
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
        '-v',
        '/repo/worktree:/agentico-release-source:ro',
        '-v',
        '/repo/.git:/repo/.git:ro',
        '-v',
        '/repo/.git/agentico-release-staging/run/x64:/agentico-release-export',
      ]),
    );
    expect(plan[0].args.join(' ')).toContain(LINUX_BUILDER_IMAGE);
    expect(LINUX_BUILDER_IMAGE).toBe(
      'electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9',
    );
    expect(plan[0].args).toEqual(
      expect.arrayContaining([
        '-v',
        'agentico-release-node-modules:/agentico-release-build/node_modules',
        '-v',
        'agentico-release-npm-cache:/root/.npm',
        '-v',
        'agentico-release-electron:/root/.cache/electron',
        '-v',
        'agentico-release-electron-builder:/root/.cache/electron-builder',
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
    expect(plan[0].args).toContain('/repo/worktrees/linux/.git:/agentico-release-source/.git:ro');
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
    expect(x64).toContain(`${stagingDirs.x64}:/agentico-release-export`);
    expect(x64).not.toContain(`${stagingDirs.arm64}:/agentico-release-export`);
    expect(arm64).toContain(`${stagingDirs.arm64}:/agentico-release-export`);
    expect(arm64).not.toContain(`${stagingDirs.x64}:/agentico-release-export`);
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
    expect(plan[0].args).toContain(`${hostileRoot}:/agentico-release-source:ro`);
    expect(plan[0].args).toContain(`${hostileStages.x64}:/agentico-release-export`);
    expect(plan[0].args.at(-1)).not.toContain(hostileRoot);
    expect(plan[0].args.at(-1)).not.toContain('$()');
    expect(plan[0].args.at(-1)).not.toContain('`tick`');
  });
});

describe('shellQuote', () => {
  it('preserves spaces, substitutions, backticks, and single quotes as one literal word', () => {
    expect(shellQuote("space $() `tick` 'quote'")).toBe(`'space $() \`tick\` '"'"'quote'"'"''`);
  });
});
