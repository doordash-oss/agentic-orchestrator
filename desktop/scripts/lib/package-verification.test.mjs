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

import { afterEach, describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

import {
  createPackageVerificationPlan,
  debArchitectureError,
  executableArchitectureError,
  inspectExecutableArchitecture,
  macAsarIntegrityHash,
  packageIdentityTargetError,
  shouldRequireUnpackedApp,
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

describe('shouldRequireUnpackedApp', () => {
  it('allows a native verifier container to validate imported distributables only', () => {
    expect(shouldRequireUnpackedApp({ unpackedOnly: false, artifactsOnly: true })).toBe(false);
    expect(shouldRequireUnpackedApp({ unpackedOnly: false, artifactsOnly: false })).toBe(true);
    expect(shouldRequireUnpackedApp({ unpackedOnly: true, artifactsOnly: true })).toBe(true);
  });
});

function elf(machine) {
  const bytes = Buffer.alloc(64);
  bytes.set([0x7f, 0x45, 0x4c, 0x46, 2, 1]);
  bytes.writeUInt16LE(machine, 18);
  return bytes;
}

function thinMachO(cpuType) {
  const bytes = Buffer.alloc(32);
  bytes.writeUInt32LE(0xfeedfacf, 0);
  bytes.writeUInt32LE(cpuType, 4);
  return bytes;
}

function fatMachO(cpuTypes) {
  const tableEnd = 8 + cpuTypes.length * 20;
  const bytes = Buffer.alloc(tableEnd + cpuTypes.length * 32);
  bytes.writeUInt32BE(0xcafebabe, 0);
  bytes.writeUInt32BE(cpuTypes.length, 4);
  for (const [index, cpuType] of cpuTypes.entries()) {
    const entry = 8 + index * 20;
    const offset = tableEnd + index * 32;
    bytes.writeUInt32BE(cpuType, entry);
    bytes.writeUInt32BE(offset, entry + 8);
    bytes.writeUInt32BE(32, entry + 12);
    thinMachO(cpuType).copy(bytes, offset);
  }
  return bytes;
}

function fatMachO64(cpuTypes) {
  const tableEnd = 8 + cpuTypes.length * 32;
  const bytes = Buffer.alloc(tableEnd + cpuTypes.length * 32);
  bytes.writeUInt32BE(0xcafebabf, 0);
  bytes.writeUInt32BE(cpuTypes.length, 4);
  for (const [index, cpuType] of cpuTypes.entries()) {
    const entry = 8 + index * 32;
    const offset = tableEnd + index * 32;
    bytes.writeUInt32BE(cpuType, entry);
    bytes.writeBigUInt64BE(BigInt(offset), entry + 8);
    bytes.writeBigUInt64BE(32n, entry + 16);
    thinMachO(cpuType).copy(bytes, offset);
  }
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

  it('rejects big-endian headers for the supported Linux architectures', () => {
    const bigEndian = elf(62);
    bigEndian[5] = 2;
    bigEndian.writeUInt16BE(62, 18);
    expect(inspectExecutableArchitecture(bigEndian)).toEqual({
      format: 'unknown',
      architectures: [],
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

  it('parses valid fat64 tables and rejects inner 32-bit or big-endian Mach-O slices', () => {
    expect(inspectExecutableArchitecture(fatMachO64([0x01000007, 0x0100000c]))).toEqual({
      format: 'Mach-O fat',
      architectures: ['amd64', 'arm64'],
    });

    const thin32 = fatMachO([0x01000007]);
    thin32.writeUInt32LE(0xfeedface, 28);
    expect(inspectExecutableArchitecture(thin32)).toEqual({
      format: 'Mach-O fat',
      architectures: [],
    });

    const bigEndianInner = fatMachO([0x01000007]);
    bigEndianInner.writeUInt32BE(0xfeedfacf, 28);
    bigEndianInner.writeUInt32BE(0x01000007, 32);
    expect(inspectExecutableArchitecture(bigEndianInner)).toEqual({
      format: 'Mach-O fat',
      architectures: [],
    });
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

  it('fails closed on malformed ELF headers and bogus Mach-O fat slices', () => {
    const invalidElf = elf(62);
    invalidElf[4] = 1;
    expect(inspectExecutableArchitecture(invalidElf)).toEqual({
      format: 'unknown',
      architectures: [],
    });

    const invalidEndian = elf(62);
    invalidEndian[5] = 3;
    expect(inspectExecutableArchitecture(invalidEndian)).toEqual({
      format: 'unknown',
      architectures: [],
    });

    const malformedFat = fatMachO([0x01000007]);
    malformedFat.writeUInt32BE(malformedFat.length + 1, 16);
    expect(inspectExecutableArchitecture(malformedFat)).toEqual({
      format: 'Mach-O fat',
      architectures: [],
    });

    const mismatchedSlice = fatMachO([0x01000007]);
    thinMachO(0x0100000c).copy(mismatchedSlice, 28);
    expect(inspectExecutableArchitecture(mismatchedSlice)).toEqual({
      format: 'Mach-O fat',
      architectures: [],
    });
  });
});

describe('macAsarIntegrityHash', () => {
  it('uses the ASAR header digest consumed by the macOS verifier', () => {
    expect(macAsarIntegrityHash('serialized asar header')).toBe(
      'aeb9edfcc3a2bf243be47e6ef3de0afd243552920b8cb017123f217b65800039',
    );
  });

  it('keeps the production macOS verifier on the createHash path', () => {
    const verifier = readFileSync(new URL('../verify-package.mjs', import.meta.url), 'utf8');
    expect(verifier).toContain("import { createHash } from 'node:crypto';");
    expect(verifier).toContain('function verifyMacAsarIntegrity(resourcesDir, asarPath)');
    expect(verifier).toContain("const actual = createHash('sha256')");
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
