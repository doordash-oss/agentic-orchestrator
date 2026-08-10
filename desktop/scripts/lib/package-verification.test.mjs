import { afterEach, describe, expect, it } from 'vitest';

import {
  createPackageVerificationPlan,
  debArchitectureError,
  executableArchitectureError,
  inspectExecutableArchitecture,
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

function elf(machine) {
  const bytes = Buffer.alloc(64);
  bytes.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]);
  bytes.writeUInt16LE(machine, 18);
  return bytes;
}

function fatMachO(cpuTypes) {
  const bytes = Buffer.alloc(8 + cpuTypes.length * 20);
  bytes.writeUInt32BE(0xcafebabe, 0);
  bytes.writeUInt32BE(cpuTypes.length, 4);
  for (const [index, cpuType] of cpuTypes.entries()) bytes.writeUInt32BE(cpuType, 8 + index * 20);
  return bytes;
}

describe('inspectExecutableArchitecture', () => {
  it('identifies Linux x64 and arm64 ELF executables without trusting the host', () => {
    expect(inspectExecutableArchitecture(elf(62))).toEqual({
      format: 'ELF',
      architectures: ['amd64'],
    });
    expect(inspectExecutableArchitecture(elf(183))).toEqual({
      format: 'ELF',
      architectures: ['arm64'],
    });
  });

  it('requires a macOS universal Mach-O executable to contain both architectures', () => {
    const universal = inspectExecutableArchitecture(fatMachO([0x01000007, 0x0100000c]));
    expect(universal).toEqual({ format: 'Mach-O fat', architectures: ['amd64', 'arm64'] });
    expect(executableArchitectureError(universal, { os: 'darwin', arch: 'universal' })).toBeNull();
    expect(
      executableArchitectureError(inspectExecutableArchitecture(fatMachO([0x0100000c])), {
        os: 'darwin',
        arch: 'universal',
      }),
    ).toContain('requires amd64 and arm64');
  });

  it('rejects an executable architecture that does not match the selected target', () => {
    expect(
      executableArchitectureError(inspectExecutableArchitecture(elf(62)), {
        os: 'linux',
        arch: 'arm64',
      }),
    ).toContain('requires arm64, found amd64');
    expect(inspectExecutableArchitecture(Buffer.from('not-an-executable'))).toEqual({
      format: 'unknown',
      architectures: [],
    });
  });
});

describe('debArchitectureError', () => {
  it('rejects a DEB control architecture that does not match the selected target', () => {
    expect(debArchitectureError('amd64', { os: 'linux', arch: 'x64' })).toBeNull();
    expect(debArchitectureError('amd64', { os: 'linux', arch: 'arm64' })).toBe(
      'deb control Architecture=amd64, expected arm64 for arm64',
    );
  });
});
