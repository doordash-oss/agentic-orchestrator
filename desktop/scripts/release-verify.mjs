// Static verification for the local, operator-driven release chain.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { LINUX_ARM64_VERIFIER_IMAGE, LINUX_BUILDER_IMAGE } from './lib/release-artifacts.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const rootDir = dirname(desktopDir);

/** Check that the checked-in release surfaces describe the actual local operator model. */
export function verifyLocalReleaseModel({
  makefile = readFileSync(join(rootDir, 'Makefile'), 'utf8'),
  builder = readFileSync(join(desktopDir, 'electron-builder.yml'), 'utf8'),
  signingScript = readFileSync(join(desktopDir, 'scripts', 'release-sign.mjs'), 'utf8'),
} = {}) {
  const failures = [];
  const requiredCommands = [
    'node desktop/scripts/release-preflight.mjs',
    'npm ci',
    'npm run package:verify --workspace desktop',
    'npm run package:linux:release --workspace desktop',
    'npm run release:artifacts:verify --workspace desktop -- packages',
    'node desktop/scripts/release-preflight.mjs verify',
    'node desktop/scripts/release-goreleaser.mjs',
    'npm run release:artifacts:verify --workspace desktop -- manifest',
  ];
  for (const command of requiredCommands) {
    if (!makefile.includes(command)) failures.push(`release verification requires ${command}`);
  }
  const recheck = makefile.indexOf('node desktop/scripts/release-preflight.mjs verify');
  const goreleaser = makefile.indexOf('node desktop/scripts/release-goreleaser.mjs');
  if (recheck === -1 || goreleaser === -1 || recheck > goreleaser) {
    failures.push(
      'release verification requires provenance verification immediately before GoReleaser',
    );
  }
  if (!builder.includes('hardenedRuntime: true')) failures.push('hardened runtime is not enabled');
  if (!builder.includes('protocols:')) failures.push('agentico protocol registration is missing');
  if (!signingScript.includes('ed25519') || !signingScript.includes('embedded trust root')) {
    failures.push('release signing must use the updater Ed25519 trust root');
  }
  if (
    !LINUX_BUILDER_IMAGE.includes('@sha256:') ||
    !LINUX_ARM64_VERIFIER_IMAGE.includes('@sha256:')
  ) {
    failures.push('Linux builder and arm64 verifier images must be digest pinned');
  }
  return failures;
}

function main() {
  const audit = execFileSync(process.execPath, [join(desktopDir, 'scripts', 'audit-release.mjs')], {
    cwd: desktopDir,
    stdio: 'inherit',
  });
  void audit;
  const failures = verifyLocalReleaseModel();
  if (failures.length > 0) {
    console.error(`release verification failed:\n- ${failures.join('\n- ')}`);
    process.exitCode = 1;
    return;
  }
  console.log('release verification passed');
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
