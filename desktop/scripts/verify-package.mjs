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

// Package inspection gate for the resolved package target. Normally opens the
// universal DMG on macOS, or the explicitly selected AppImage and deb on
// Linux, and asserts, failing loudly on any gap. `--unpacked` checks the
// runnable app directory directly for packaged Playwright; this avoids
// requiring disk-image facilities merely to launch the package in a sandbox.
//
//   1. Desktop app payload: app.asar containing the built main/preload/
//      renderer entry points and carrying no bundled text webfont — the
//      renderer draws text with system faces only (--bench-font-* in
//      tokens.css), so any shipped .woff2/.woff/.ttf/.otf/.eot is a
//      regression back to bundled webfonts. The only permitted payload is
//      monaco-editor's codicon glyph font (see findDisallowedFontAssets).
//   2. Bundled server: resources/bin/agentico exists, is executable, and on
//      macOS is a true universal (x86_64 + arm64) binary.
//   3. Identity: resources/build-identity.json parses, satisfies the closed
//      schema (validateBuildIdentity), matches the package target, and
//      cross-checks against the binary's actual identity — the version and
//      revision the binary itself reports via --version, and the GOOS the Go
//      toolchain stamped into it (go version -m).
//
// On success it writes compatibility and target-specific verification receipts
// recording the verified artifacts, identity, and deterministic unpacked-app
// path the packaged E2E journeys (Task 6b) launch — so 6b consumes a proven
// layout instead of re-deriving it. The identity-rejection logic itself is
// unit-tested in scripts/lib/identity.test.mjs against tampered fixtures.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  accessSync,
  constants,
  existsSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { getCurrentFuseWire } from '@electron/fuses';

import {
  crossCheckServerBinary,
  parseAgenticoVersionOutput,
  parseGoBuildInfo,
  validateBuildIdentity,
} from './lib/identity.mjs';
import { auditFuseWire } from './lib/fuse-policy.mjs';
import { findDisallowedFontAssets } from './lib/package-layout.mjs';
import {
  createPackageVerificationPlan,
  debArchitectureError,
  executableArchitectureError,
  inspectExecutableArchitecture,
  packageIdentityTargetError,
  shouldRequireUnpackedApp,
} from './lib/package-verification.mjs';
import {
  createPackageVerificationReceipt,
  readArtifactEvidence,
  resolvePackageTarget,
} from './lib/release-artifacts.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const distDir = join(desktopDir, 'dist');
const require = createRequire(import.meta.url);
const unpackedOnly = process.argv.slice(2).includes('--unpacked');
const artifactsOnly = process.env.AGENTICO_VERIFY_ARTIFACTS_ONLY === '1';
const packageTarget = resolvePackageTarget(
  process.platform,
  process.arch,
  process.env.AGENTICO_PACKAGE_ARCH,
);

class VerificationFailure extends Error {}

function fail(message) {
  throw new VerificationFailure(message);
}

/** Assert the app payload inside an extracted/mounted resources directory. */
function verifyResources(resourcesDir, { electronExecutablePath, target }) {
  // 1. Desktop app payload.
  const asarPath = join(resourcesDir, 'app.asar');
  if (!existsSync(asarPath)) {
    fail(`missing app.asar in ${resourcesDir}`);
  }
  if (process.platform === 'darwin' && target.os === 'darwin') {
    verifyMacAsarIntegrity(resourcesDir, asarPath);
  }
  const asar = require('@electron/asar');
  const entries = asar.listPackage(asarPath).map((entry) => entry.replaceAll('\\', '/'));
  for (const required of [
    '/out/main/index.js',
    '/out/preload/index.cjs',
    '/out/renderer/index.html',
  ]) {
    if (!entries.includes(required)) {
      fail(`app.asar is missing ${required}`);
    }
  }
  const fonts = findDisallowedFontAssets(entries);
  if (fonts.length > 0) {
    fail(
      `app.asar ships ${fonts.length} bundled font asset(s); the renderer must ` +
        'use system text faces only:\n  ' +
        fonts.slice(0, 10).join('\n  ') +
        (fonts.length > 10 ? `\n  ...and ${fonts.length - 10} more` : ''),
    );
  }

  // 2. Identity file: present, schema-complete, and for the package target.
  const identityPath = join(resourcesDir, 'build-identity.json');
  if (!existsSync(identityPath)) {
    fail(`missing build-identity.json in ${resourcesDir}`);
  }
  let identity;
  try {
    identity = JSON.parse(readFileSync(identityPath, 'utf8'));
  } catch (error) {
    fail(`build-identity.json is not valid JSON: ${error.message}`);
  }
  const validation = validateBuildIdentity(identity);
  if (!validation.ok) {
    fail(`build-identity.json is invalid:\n  ${validation.errors.join('\n  ')}`);
  }
  const identityTargetError = packageIdentityTargetError(identity, target);
  if (identityTargetError !== null) {
    fail(identityTargetError);
  }

  verifyExecutableTarget(electronExecutablePath, target, 'Electron executable');

  // 3. Bundled server binary: executable and matching the identity.
  const binaryPath = join(resourcesDir, 'bin', 'agentico');
  if (!existsSync(binaryPath) || !statSync(binaryPath).isFile()) {
    fail(`missing bundled server binary at ${binaryPath}`);
  }
  try {
    accessSync(binaryPath, constants.X_OK);
  } catch {
    fail(`bundled server binary is not executable: ${binaryPath}`);
  }
  verifyExecutableTarget(binaryPath, target, 'bundled server binary');
  const reported = parseAgenticoVersionOutput(
    execFileSync(binaryPath, ['--version'], { encoding: 'utf8' }),
  );
  if (reported === null) {
    fail(`could not parse \`${binaryPath} --version\` output`);
  }
  const buildInfo = parseGoBuildInfo(
    execFileSync('go', ['version', '-m', binaryPath], { encoding: 'utf8' }),
  );
  const mismatches = crossCheckServerBinary(identity, {
    reportedVersion: reported.version,
    reportedRevision: reported.revision,
    reportedGoos: buildInfo.goos === null ? identity.os : buildInfo.goos,
    reportedGoarch: buildInfo.goarch,
  });
  if (mismatches.length > 0) {
    fail(`bundled server does not match build-identity.json:\n  ${mismatches.join('\n  ')}`);
  }
  return identity;
}

function verifyExecutableTarget(executablePath, target, label) {
  let evidence;
  try {
    evidence = inspectExecutableArchitecture(readFileSync(executablePath));
  } catch (error) {
    fail(`could not inspect ${label} at ${executablePath}: ${error.message}`);
  }
  const mismatch = executableArchitectureError(evidence, target);
  if (mismatch !== null) fail(`${label} architecture mismatch at ${executablePath}: ${mismatch}`);
}

async function verifyBoundArtifact({ format, path, verify }) {
  let before;
  try {
    before = readArtifactEvidence(path);
  } catch (error) {
    fail(`could not bind ${format} artifact before inspection: ${error.message}`);
  }
  const identity = await verify(before.path);
  let after;
  try {
    after = readArtifactEvidence(before.path);
  } catch (error) {
    fail(`could not bind ${format} artifact after inspection: ${error.message}`);
  }
  if (before.sha256 !== after.sha256 || before.size !== after.size || before.path !== after.path) {
    fail(`verified ${format} artifact changed during inspection: ${before.path}`);
  }
  return {
    target: { ...packageTarget },
    format,
    path: after.path,
    sha256: after.sha256,
    size: after.size,
    identity: { ...identity },
  };
}

function verifyMacAsarIntegrity(resourcesDir, asarPath) {
  // Electron validates the serialized ASAR header, matching the algorithm
  // used by electron-builder and @electron/universal. A whole-file digest is
  // a different value and would make a valid archive fail closed at launch.
  const asar = require('@electron/asar');
  const actual = createHash('sha256')
    .update(asar.getRawHeader(asarPath).headerString)
    .digest('hex');
  const appDir = dirname(resourcesDir);
  const declarations = [];
  const pending = [appDir];
  while (pending.length > 0) {
    const current = pending.pop();
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      const candidate = join(current, entry.name);
      if (entry.isDirectory()) {
        pending.push(candidate);
      } else if (entry.isFile() && entry.name === 'Info.plist') {
        try {
          const expected = execFileSync(
            '/usr/libexec/PlistBuddy',
            ['-c', 'Print :ElectronAsarIntegrity:Resources/app.asar:hash', candidate],
            { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] },
          ).trim();
          declarations.push({ path: candidate, expected });
        } catch {
          // Not every nested bundle carries the app ASAR declaration.
        }
      }
    }
  }
  if (declarations.length === 0) {
    fail(`macOS app has no embedded ASAR integrity declarations under ${appDir}`);
  }
  const mismatch = declarations.find(({ expected }) => expected !== actual);
  if (mismatch !== undefined) {
    fail(
      `macOS embedded ASAR integrity mismatch in ${mismatch.path}: ` +
        `Info.plist=${mismatch.expected}, app.asar=${actual}`,
    );
  }
}

async function verifyDmg(dmgPath) {
  const mountPoint = mkdtempSync(join(tmpdir(), 'agentico-verify-dmg-'));
  execFileSync(
    'hdiutil',
    ['attach', dmgPath, '-nobrowse', '-readonly', '-mountpoint', mountPoint],
    {
      stdio: 'inherit',
    },
  );
  try {
    const appDir = join(mountPoint, 'Agentico.app');
    if (!existsSync(appDir)) {
      fail(`DMG does not contain Agentico.app (${dmgPath})`);
    }
    verifyMacAppSignature(appDir);
    await verifyElectronFuses(join(appDir, 'Contents', 'MacOS', 'Agentico'));
    return verifyResources(join(appDir, 'Contents', 'Resources'), {
      electronExecutablePath: join(appDir, 'Contents', 'MacOS', 'Agentico'),
      target: packageTarget,
    });
  } finally {
    execFileSync('hdiutil', ['detach', mountPoint, '-force'], { stdio: 'inherit' });
    rmSync(mountPoint, { recursive: true, force: true });
  }
}

async function verifyAppImage(appImagePath) {
  const workDir = mkdtempSync(join(tmpdir(), 'agentico-verify-appimage-'));
  try {
    // --appimage-extract works without FUSE and always unpacks to
    // ./squashfs-root under the current working directory.
    execFileSync(appImagePath, ['--appimage-extract'], { cwd: workDir, stdio: 'ignore' });
    await verifyElectronFuses(join(workDir, 'squashfs-root', 'agentico'));
    return verifyResources(join(workDir, 'squashfs-root', 'resources'), {
      electronExecutablePath: join(workDir, 'squashfs-root', 'agentico'),
      target: packageTarget,
    });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

async function verifyDeb(debPath) {
  const workDir = mkdtempSync(join(tmpdir(), 'agentico-verify-deb-'));
  try {
    const controlArchitecture = execFileSync('dpkg-deb', ['--field', debPath, 'Architecture'], {
      encoding: 'utf8',
    }).trim();
    const debArchitectureMismatch = debArchitectureError(controlArchitecture, packageTarget);
    if (debArchitectureMismatch !== null) fail(debArchitectureMismatch);
    execFileSync('dpkg-deb', ['-x', debPath, workDir], { stdio: 'inherit' });
    const resourcesDir = join(workDir, 'opt', 'Agentico', 'resources');
    if (!existsSync(resourcesDir)) {
      fail(`deb payload has no resources dir at opt/Agentico/resources (${debPath})`);
    }
    await verifyElectronFuses(join(workDir, 'opt', 'Agentico', 'agentico'));
    return verifyResources(resourcesDir, {
      electronExecutablePath: join(workDir, 'opt', 'Agentico', 'agentico'),
      target: packageTarget,
    });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

async function verifyUnpackedApp(executablePath) {
  if (!existsSync(executablePath)) {
    fail(`expected unpacked app for packaged E2E at ${executablePath}`);
  }
  if (process.platform === 'darwin') {
    verifyMacAppSignature(dirname(dirname(dirname(executablePath))));
  }
  await verifyElectronFuses(executablePath);
  const resourcesDir =
    process.platform === 'darwin'
      ? join(dirname(dirname(executablePath)), 'Resources')
      : join(dirname(executablePath), 'resources');
  return verifyResources(resourcesDir, {
    electronExecutablePath: executablePath,
    target: packageTarget,
  });
}

function verifyMacAppSignature(appDir) {
  try {
    execFileSync('codesign', ['--verify', '--deep', '--strict', appDir], { stdio: 'pipe' });
  } catch (error) {
    const detail = error.stderr?.toString().trim();
    fail(
      `macOS app is not runnable with a valid code signature (${appDir})${detail ? `: ${detail}` : ''}`,
    );
  }
}

async function verifyElectronFuses(executablePath) {
  let fuses;
  try {
    fuses = await getCurrentFuseWire(executablePath);
  } catch (error) {
    fail(`could not read Electron fuses from ${executablePath}: ${error.message}`);
  }
  const mismatches = auditFuseWire(fuses);
  if (mismatches.length > 0) {
    fail(`Electron fuses are not hardened:\n  ${mismatches.join('\n  ')}`);
  }
}

async function main() {
  const startedAt = Date.now();
  const verified = [];
  let identity;
  let verificationPlan;
  try {
    verificationPlan = createPackageVerificationPlan({
      desktopDir,
      target: packageTarget,
      files: unpackedOnly ? undefined : readdirSync(distDir),
    });
  } catch (error) {
    fail(
      `${error instanceof Error ? error.message : String(error)} in ${distDir}; ` +
        'run `npm run package:build` first',
    );
  }
  const unpackedApp = verificationPlan.unpackedApp;
  if (unpackedOnly) {
    console.log(`verify-package: inspecting unpacked app ${unpackedApp}`);
    identity = await verifyUnpackedApp(unpackedApp);
    verified.push({ target: 'unpacked', path: unpackedApp, identity });
  } else if (process.platform === 'darwin') {
    const dmg = verificationPlan.artifacts[0].path;
    console.log(`verify-package: inspecting ${dmg}`);
    const artifact = await verifyBoundArtifact({ format: 'dmg', path: dmg, verify: verifyDmg });
    identity = artifact.identity;
    verified.push(artifact);
  } else if (process.platform === 'linux') {
    const appImage = verificationPlan.artifacts[0].path;
    console.log(`verify-package: inspecting ${appImage}`);
    const appImageArtifact = await verifyBoundArtifact({
      format: 'AppImage',
      path: appImage,
      verify: verifyAppImage,
    });
    identity = appImageArtifact.identity;
    verified.push(appImageArtifact);
    const deb = verificationPlan.artifacts[1].path;
    console.log(`verify-package: inspecting ${deb}`);
    const debArtifact = await verifyBoundArtifact({ format: 'deb', path: deb, verify: verifyDeb });
    identity = debArtifact.identity;
    verified.push(debArtifact);
  } else {
    fail(`unsupported verification host: ${process.platform}`);
  }

  // The deterministic launch path for the packaged E2E journeys (Task 6b):
  // electron-builder's unpacked staging output, byte-identical in layout to
  // the payload verified above. Dev fallback for 6b when no package exists:
  // `npm run package:build` first, or run the app with electron-vite dev +
  // AGENTICO_SERVER_BIN.
  if (shouldRequireUnpackedApp({ unpackedOnly, artifactsOnly }) && !existsSync(unpackedApp)) {
    fail(`expected unpacked app for packaged E2E at ${unpackedApp}`);
  }
  const manifest = unpackedOnly
    ? {
        verified_at: new Date().toISOString(),
        host: { os: process.platform, arch: process.arch },
        artifacts: verified,
        unpacked_app: unpackedApp,
        identity,
      }
    : createPackageVerificationReceipt({
        target: packageTarget,
        artifacts: verified,
        unpackedApp: artifactsOnly ? undefined : unpackedApp,
        host: { os: process.platform, arch: process.arch },
      });
  const receipt = `${JSON.stringify(manifest, null, 2)}\n`;
  const { compatibility: compatibilityReceipt, target: targetReceipt } = verificationPlan.receipts;
  writeFileSync(compatibilityReceipt, receipt);
  if (!unpackedOnly) writeFileSync(targetReceipt, receipt);
  const seconds = ((Date.now() - startedAt) / 1000).toFixed(1);
  console.log(`verify-package: OK (${verified.map((v) => v.target).join(', ')}) in ${seconds}s`);
  console.log(`verify-package: wrote ${compatibilityReceipt}`);
  if (!unpackedOnly) console.log(`verify-package: wrote ${targetReceipt}`);
}

try {
  await main();
} catch (error) {
  if (error instanceof VerificationFailure) {
    console.error(`verify-package: FAIL: ${error.message}`);
    process.exit(1);
  }
  throw error;
}
