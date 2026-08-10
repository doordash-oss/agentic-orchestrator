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
