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

// Build the unsigned development package for the current host target:
//   1. electron-vite production build (out/)
//   2. stage the matching Go server + build-identity.json (resources/)
//   3. electron-builder with publishing disabled (dist/)
//
// macOS normally produces the universal DMG; Linux produces AppImage + deb
// for the host arch. `--unpacked` produces only the runnable app directory
// used by packaged Playwright journeys, avoiding a disk-image mount on hosts
// where hdiutil/FUSE is unavailable. AGENTICO_PACKAGE_ARCH=x64|arm64
// cross-builds the other Linux arch — the staged Go binary and the
// electron-builder arch flag move together so packages can never carry a
// mismatched server.
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { flipFuses, FuseVersion } from '@electron/fuses';
import { PRODUCTION_FUSE_POLICY } from './lib/fuse-policy.mjs';
import { normalizeLinuxAppImage } from './lib/linux-artifacts.mjs';
import { unpackedExecutablePath } from './lib/package-layout.mjs';
import { desktopVersionFromExactTag } from './lib/release-version.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const require = createRequire(import.meta.url);
const unpackedOnly = process.argv.slice(2).includes('--unpacked');

// A package built exactly on a clean release tag carries that tag's version —
// the same tag identity the Go server gets via git describe — so the packaged
// app.getVersion() can be compared against the GitHub Releases feed. Any other
// build (untagged HEAD, dirty tree) keeps the static development version from
// package.json. AGENTICO_DESKTOP_VERSION hands the same value to
// prepare-server.mjs for build-identity.json's desktop_version.
function releaseTagVersion() {
  const gitOptions = { cwd: desktopDir, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] };
  try {
    if (execFileSync('git', ['status', '--porcelain'], gitOptions).trim() !== '') {
      return null;
    }
    return desktopVersionFromExactTag(
      execFileSync('git', ['describe', '--tags', '--exact-match'], gitOptions),
    );
  } catch {
    return null;
  }
}

function run(command, args) {
  console.log(`package-build: ${command} ${args.join(' ')}`);
  execFileSync(command, args, { cwd: desktopDir, stdio: 'inherit' });
}

function nodeBin(pkg, relBin) {
  return join(dirname(require.resolve(`${pkg}/package.json`)), relBin);
}

// npm workspaces hoist electron to the repo-root node_modules, where
// electron-builder's project-local lookup cannot see it; pin the actually
// installed version explicitly so packaging always matches the dev runtime.
const electronVersion = require('electron/package.json').version;
const builderArgs = ['--publish', 'never', `--config.electronVersion=${electronVersion}`];
let linuxPackageArch;
const stampedVersion = releaseTagVersion();
if (stampedVersion !== null) {
  builderArgs.push(`--config.extraMetadata.version=${stampedVersion}`);
  process.env.AGENTICO_DESKTOP_VERSION = stampedVersion;
  console.log(`package-build: stamping desktop version ${stampedVersion} from the release tag`);
}
if (unpackedOnly) {
  builderArgs.push('--dir');
  if (process.platform === 'darwin') {
    builderArgs.push('--universal');
  }
}
if (process.platform === 'linux') {
  linuxPackageArch =
    process.env.AGENTICO_PACKAGE_ARCH ?? (process.arch === 'arm64' ? 'arm64' : 'x64');
  builderArgs.push(linuxPackageArch === 'arm64' ? '--arm64' : '--x64');
}

// electron-builder skips macOS signing on pull-request builds unless forced.
// The dev build only ever applies the ad-hoc identity ('-') from
// electron-builder.yml — no certificate exists to leak — and verify-package
// rejects DMGs whose app cannot launch unsigned on arm64.
process.env.CSC_FOR_PULL_REQUEST ??= 'true';

run(process.execPath, [nodeBin('electron-vite', 'bin/electron-vite.js'), 'build']);
run(process.execPath, [join(desktopDir, 'scripts', 'prepare-server.mjs')]);
run(process.execPath, [nodeBin('electron-builder', 'cli.js'), ...builderArgs]);
if (process.platform === 'linux' && !unpackedOnly) {
  const appImage = normalizeLinuxAppImage(join(desktopDir, 'dist'), linuxPackageArch);
  console.log(`package-build: normalized Linux AppImage name to ${appImage}`);
}
if (unpackedOnly) {
  await enforceUnpackedFuses();
}

async function enforceUnpackedFuses() {
  const executable = unpackedExecutablePath(
    desktopDir,
    process.platform,
    process.env.AGENTICO_PACKAGE_ARCH ?? process.arch,
  );
  await flipFuses(executable, {
    version: FuseVersion.V1,
    strictlyRequireAllFuses: false,
    ...Object.fromEntries(PRODUCTION_FUSE_POLICY.map((fuse) => [fuse.option, fuse.expected])),
  });
  console.log(`package-build: enforced production Electron fuses on ${executable}`);
  if (process.platform === 'darwin') {
    const appDir = dirname(dirname(dirname(executable)));
    // Flipping Electron fuses mutates the Mach-O after electron-builder's
    // packaging step and invalidates its linker signature. Re-sign the whole
    // unpacked development bundle so `make install` produces a runnable app.
    run('codesign', ['--force', '--deep', '--sign', '-', appDir]);
    console.log(`package-build: ad-hoc signed unpacked macOS app ${appDir}`);
  }
}
