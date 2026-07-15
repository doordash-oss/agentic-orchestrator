// Build the unsigned development package for the current host target:
//   1. electron-vite production build (out/)
//   2. stage the matching Go server + build-identity.json (resources/)
//   3. electron-builder with publishing disabled (dist/)
//
// macOS always produces the universal DMG; Linux produces AppImage + deb for
// the host arch (AGENTICO_PACKAGE_ARCH=x64|arm64 cross-builds the other
// Linux arch — the staged Go binary and the electron-builder arch flag move
// together so packages can never carry a mismatched server).
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const require = createRequire(import.meta.url);

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
if (process.platform === 'linux') {
  const arch = process.env.AGENTICO_PACKAGE_ARCH ?? (process.arch === 'arm64' ? 'arm64' : 'x64');
  builderArgs.push(arch === 'arm64' ? '--arm64' : '--x64');
}

run(process.execPath, [nodeBin('electron-vite', 'bin/electron-vite.js'), 'build']);
run(process.execPath, [join(desktopDir, 'scripts', 'prepare-server.mjs')]);
run(process.execPath, [nodeBin('electron-builder', 'cli.js'), ...builderArgs]);
