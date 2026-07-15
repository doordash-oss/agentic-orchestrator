// Package inspection gate for the current host's native target ("current CI
// matrix target"). Opens every artifact electron-builder produced for this
// host — the universal DMG on macOS, the native-arch AppImage and deb on
// Linux — and asserts, failing loudly on any gap:
//
//   1. Desktop app payload: app.asar containing the built main/preload/
//      renderer entry points and the offline @fontsource font assets.
//   2. Bundled server: resources/bin/agentico exists, is executable, and on
//      macOS is a true universal (x86_64 + arm64) binary.
//   3. Identity: resources/build-identity.json parses, satisfies the closed
//      schema (validateBuildIdentity), matches the host target, and
//      cross-checks against the binary's actual identity — the version and
//      revision the binary itself reports via --version, and the GOOS the Go
//      toolchain stamped into it (go version -m).
//
// On success it writes dist/package-verification.json recording the verified
// artifacts, the identity, and the deterministic unpacked-app path the
// packaged E2E journeys (Task 6b) launch — so 6b consumes a proven layout
// instead of re-deriving it. The identity-rejection logic itself is
// unit-tested in scripts/lib/identity.test.mjs against tampered fixtures.
import { execFileSync } from 'node:child_process';
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

import {
  crossCheckServerBinary,
  parseAgenticoVersionOutput,
  parseGoBuildInfo,
  validateBuildIdentity,
} from './lib/identity.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const distDir = join(desktopDir, 'dist');
const require = createRequire(import.meta.url);

class VerificationFailure extends Error {}

function fail(message) {
  throw new VerificationFailure(message);
}

function findArtifact(suffix) {
  const matches = readdirSync(distDir).filter((name) => name.endsWith(suffix));
  if (matches.length !== 1) {
    fail(
      `expected exactly one *${suffix} in ${distDir}, found ${matches.length}` +
        (matches.length > 0 ? ` (${matches.join(', ')})` : '') +
        '; run `npm run package:build` first',
    );
  }
  return join(distDir, matches[0]);
}

/** Assert the app payload inside an extracted/mounted resources directory. */
function verifyResources(resourcesDir, { expectUniversalBinary }) {
  // 1. Desktop app payload.
  const asarPath = join(resourcesDir, 'app.asar');
  if (!existsSync(asarPath)) {
    fail(`missing app.asar in ${resourcesDir}`);
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
  const fonts = entries.filter(
    (entry) => entry.startsWith('/out/renderer/') && entry.endsWith('.woff2'),
  );
  if (fonts.length === 0) {
    fail('app.asar carries no offline .woff2 font assets under out/renderer/');
  }

  // 2. Identity file: present, schema-complete, and for this host target.
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
  if (identity.os !== process.platform) {
    fail(`build-identity.json os=${identity.os} does not match host ${process.platform}`);
  }
  const expectedArch =
    process.platform === 'darwin' ? 'universal' : process.arch === 'arm64' ? 'arm64' : 'x64';
  if (identity.arch !== expectedArch) {
    fail(`build-identity.json arch=${identity.arch}, expected ${expectedArch} for this host`);
  }

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
  if (expectUniversalBinary) {
    const archs = execFileSync('lipo', ['-archs', binaryPath], { encoding: 'utf8' }).trim();
    for (const arch of ['x86_64', 'arm64']) {
      if (!archs.split(/\s+/).includes(arch)) {
        fail(`bundled server binary is not universal (lipo -archs: "${archs}")`);
      }
    }
  }
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
  });
  if (mismatches.length > 0) {
    fail(`bundled server does not match build-identity.json:\n  ${mismatches.join('\n  ')}`);
  }
  return identity;
}

function verifyDmg(dmgPath) {
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
    return verifyResources(join(appDir, 'Contents', 'Resources'), {
      expectUniversalBinary: true,
    });
  } finally {
    execFileSync('hdiutil', ['detach', mountPoint, '-force'], { stdio: 'inherit' });
    rmSync(mountPoint, { recursive: true, force: true });
  }
}

function verifyAppImage(appImagePath) {
  const workDir = mkdtempSync(join(tmpdir(), 'agentico-verify-appimage-'));
  try {
    // --appimage-extract works without FUSE and always unpacks to
    // ./squashfs-root under the current working directory.
    execFileSync(appImagePath, ['--appimage-extract'], { cwd: workDir, stdio: 'ignore' });
    return verifyResources(join(workDir, 'squashfs-root', 'resources'), {
      expectUniversalBinary: false,
    });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

function verifyDeb(debPath) {
  const workDir = mkdtempSync(join(tmpdir(), 'agentico-verify-deb-'));
  try {
    execFileSync('dpkg-deb', ['-x', debPath, workDir], { stdio: 'inherit' });
    const resourcesDir = join(workDir, 'opt', 'Agentico', 'resources');
    if (!existsSync(resourcesDir)) {
      fail(`deb payload has no resources dir at opt/Agentico/resources (${debPath})`);
    }
    return verifyResources(resourcesDir, { expectUniversalBinary: false });
  } finally {
    rmSync(workDir, { recursive: true, force: true });
  }
}

function main() {
  const startedAt = Date.now();
  const verified = [];
  let identity;
  let unpackedApp;
  if (process.platform === 'darwin') {
    const dmg = findArtifact('.dmg');
    console.log(`verify-package: inspecting ${dmg}`);
    identity = verifyDmg(dmg);
    verified.push({ target: 'dmg', path: dmg });
    unpackedApp = join(distDir, 'mac-universal', 'Agentico.app', 'Contents', 'MacOS', 'Agentico');
  } else if (process.platform === 'linux') {
    const appImage = findArtifact('.AppImage');
    console.log(`verify-package: inspecting ${appImage}`);
    identity = verifyAppImage(appImage);
    verified.push({ target: 'AppImage', path: appImage });
    const deb = findArtifact('.deb');
    console.log(`verify-package: inspecting ${deb}`);
    identity = verifyDeb(deb);
    verified.push({ target: 'deb', path: deb });
    const unpackedDir = process.arch === 'arm64' ? 'linux-arm64-unpacked' : 'linux-unpacked';
    unpackedApp = join(distDir, unpackedDir, 'agentico');
  } else {
    fail(`unsupported verification host: ${process.platform}`);
  }

  // The deterministic launch path for the packaged E2E journeys (Task 6b):
  // electron-builder's unpacked staging output, byte-identical in layout to
  // the payload verified above. Dev fallback for 6b when no package exists:
  // `npm run package:build` first, or run the app with electron-vite dev +
  // AGENTICO_SERVER_BIN.
  if (!existsSync(unpackedApp)) {
    fail(`expected unpacked app for packaged E2E at ${unpackedApp}`);
  }
  const manifest = {
    verified_at: new Date().toISOString(),
    host: { os: process.platform, arch: process.arch },
    artifacts: verified,
    unpacked_app: unpackedApp,
    identity,
  };
  writeFileSync(
    join(distDir, 'package-verification.json'),
    `${JSON.stringify(manifest, null, 2)}\n`,
  );
  const seconds = ((Date.now() - startedAt) / 1000).toFixed(1);
  console.log(`verify-package: OK (${verified.map((v) => v.target).join(', ')}) in ${seconds}s`);
  console.log(`verify-package: wrote ${join(distDir, 'package-verification.json')}`);
}

try {
  main();
} catch (error) {
  if (error instanceof VerificationFailure) {
    console.error(`verify-package: FAIL: ${error.message}`);
    process.exit(1);
  }
  throw error;
}
