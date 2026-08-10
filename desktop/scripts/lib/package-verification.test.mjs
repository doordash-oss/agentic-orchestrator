import { afterEach, describe, expect, it } from 'vitest';

import {
  createPackageVerificationPlan,
  packageIdentityTargetError,
} from './package-verification.mjs';
import { resolvePackageTarget } from './release-artifacts.mjs';

const originalPackageArch = process.env.AGENTICO_PACKAGE_ARCH;

afterEach(() => {
  if (originalPackageArch === undefined) {
    delete process.env.AGENTICO_PACKAGE_ARCH;
  } else {
    process.env.AGENTICO_PACKAGE_ARCH = originalPackageArch;
  }
});

describe('createPackageVerificationPlan', () => {
  it('verifies an explicit arm64 Linux package independently from an x64 host', () => {
    process.env.AGENTICO_PACKAGE_ARCH = 'arm64';
    const target = resolvePackageTarget('linux', 'x64', process.env.AGENTICO_PACKAGE_ARCH);
    const plan = createPackageVerificationPlan({
      desktopDir: '/repo/desktop',
      target,
      files: [
        'Agentico-x64.AppImage',
        'Agentico-arm64.AppImage',
        'agentico_0.150.0_amd64.deb',
        'agentico_0.150.0_arm64.deb',
      ],
    });

    expect(plan.target).toEqual({ os: 'linux', arch: 'arm64' });
    expect(plan.artifacts).toEqual([
      { format: 'AppImage', path: '/repo/desktop/dist/Agentico-arm64.AppImage' },
      { format: 'deb', path: '/repo/desktop/dist/agentico_0.150.0_arm64.deb' },
    ]);
    expect(plan.unpackedApp).toBe('/repo/desktop/dist/linux-arm64-unpacked/agentico');
    expect(plan.receipts).toEqual({
      compatibility: '/repo/desktop/dist/package-verification.json',
      target: '/repo/desktop/dist/package-verification-linux-arm64.json',
    });
    expect(packageIdentityTargetError({ os: 'linux', arch: 'arm64' }, plan.target)).toBeNull();
    expect(packageIdentityTargetError({ os: 'linux', arch: 'x64' }, plan.target)).toMatch(
      /expected arm64 for package target/,
    );
  });
});
