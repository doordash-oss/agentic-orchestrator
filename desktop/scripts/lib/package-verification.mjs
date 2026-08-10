import { join } from 'node:path';

import { unpackedExecutablePath } from './package-layout.mjs';
import { selectPackageArtifact } from './release-artifacts.mjs';

/** Build the target-specific paths and distributable inventory for package verification. */
export function createPackageVerificationPlan({ desktopDir, target, files }) {
  const distDir = join(desktopDir, 'dist');
  const formats = target.os === 'darwin' ? ['dmg'] : ['AppImage', 'deb'];
  return Object.freeze({
    target,
    artifacts: Object.freeze(
      (files === undefined ? [] : formats).map((format) =>
        Object.freeze({
          format,
          path: join(distDir, selectPackageArtifact(files, target, format)),
        }),
      ),
    ),
    unpackedApp: unpackedExecutablePath(desktopDir, target.os, target.arch),
    receipts: Object.freeze({
      compatibility: join(distDir, 'package-verification.json'),
      target: join(distDir, `package-verification-${target.os}-${target.arch}.json`),
    }),
  });
}

/** Return an identity-to-target mismatch message, or null when the identity matches. */
export function packageIdentityTargetError(identity, target) {
  if (identity.os !== target.os) {
    return `build-identity.json os=${identity.os} does not match package target ${target.os}`;
  }
  if (identity.arch !== target.arch) {
    return `build-identity.json arch=${identity.arch}, expected ${target.arch} for package target`;
  }
  return null;
}

/**
 * Identify executable architectures directly from the binary header. This
 * deliberately does not consult the host: cross-container package checks must
 * prove the target embedded in the artifact, not the machine running them.
 */
export function inspectExecutableArchitecture(bytes) {
  const buffer = Buffer.isBuffer(bytes) ? bytes : Buffer.from(bytes);
  if (buffer.length >= 20 && buffer.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) {
    const littleEndian = buffer[5] === 1;
    const machine = littleEndian ? buffer.readUInt16LE(18) : buffer.readUInt16BE(18);
    return { format: 'ELF', architectures: elfArchitecture(machine) };
  }

  if (buffer.length >= 8) {
    const magic = buffer.readUInt32BE(0);
    if ([0xcafebabe, 0xcafebabf, 0xbebafeca, 0xbfbafeca].includes(magic)) {
      const littleEndian = magic === 0xbebafeca || magic === 0xbfbafeca;
      const is64 = magic === 0xcafebabf || magic === 0xbfbafeca;
      const readU32 = (offset) =>
        littleEndian ? buffer.readUInt32LE(offset) : buffer.readUInt32BE(offset);
      const count = readU32(4);
      const entrySize = is64 ? 32 : 20;
      if (count > 32 || buffer.length < 8 + count * entrySize) {
        return { format: 'Mach-O fat', architectures: [] };
      }
      const architectures = new Set();
      for (let index = 0; index < count; index += 1) {
        for (const architecture of machOArchitecture(readU32(8 + index * entrySize))) {
          architectures.add(architecture);
        }
      }
      return { format: 'Mach-O fat', architectures: [...architectures].sort() };
    }
  }
  return { format: 'unknown', architectures: [] };
}

/** Return a precise target mismatch, or null when executable evidence is sufficient. */
export function executableArchitectureError(evidence, target) {
  const actual = Array.isArray(evidence?.architectures) ? evidence.architectures : [];
  if (target.os === 'darwin' && target.arch === 'universal') {
    if (
      evidence?.format !== 'Mach-O fat' ||
      !actual.includes('amd64') ||
      !actual.includes('arm64')
    ) {
      return `macOS universal executable requires amd64 and arm64 Mach-O fat slices, found ${describeArchitectures(evidence)}`;
    }
    return null;
  }
  const expected = target.arch === 'x64' ? 'amd64' : 'arm64';
  if (evidence?.format !== 'ELF' || actual.length !== 1 || actual[0] !== expected) {
    return `Linux ${target.arch} executable requires ${expected}, found ${describeArchitectures(evidence)}`;
  }
  return null;
}

/** Return a precise Debian control-field mismatch, or null when it matches the target. */
export function debArchitectureError(controlArchitecture, target) {
  const expected = target.arch === 'x64' ? 'amd64' : 'arm64';
  if (controlArchitecture === expected) return null;
  return `deb control Architecture=${controlArchitecture || '(missing)'}, expected ${expected} for ${target.arch}`;
}

function elfArchitecture(machine) {
  if (machine === 62) return ['amd64'];
  if (machine === 183) return ['arm64'];
  return [];
}

function machOArchitecture(cpuType) {
  if (cpuType === 0x01000007) return ['amd64'];
  if (cpuType === 0x0100000c) return ['arm64'];
  return [];
}

function describeArchitectures(evidence) {
  const architectures = Array.isArray(evidence?.architectures) ? evidence.architectures : [];
  return architectures.length === 0
    ? `${evidence?.format ?? 'unknown'} (no recognized architecture)`
    : architectures.join(', ');
}
