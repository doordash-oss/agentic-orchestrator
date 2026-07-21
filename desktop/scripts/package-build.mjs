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
import { unpackedExecutablePath } from './lib/package-layout.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const require = createRequire(import.meta.url);
const unpackedOnly = process.argv.slice(2).includes('--unpacked');

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
if (unpackedOnly) {
  builderArgs.push('--dir');
  if (process.platform === 'darwin') {
    builderArgs.push('--universal');
  }
}
if (process.platform === 'linux') {
  const arch = process.env.AGENTICO_PACKAGE_ARCH ?? (process.arch === 'arm64' ? 'arm64' : 'x64');
  builderArgs.push(arch === 'arm64' ? '--arm64' : '--x64');
}

run(process.execPath, [nodeBin('electron-vite', 'bin/electron-vite.js'), 'build']);
run(process.execPath, [join(desktopDir, 'scripts', 'prepare-server.mjs')]);
run(process.execPath, [nodeBin('electron-builder', 'cli.js'), ...builderArgs]);
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
