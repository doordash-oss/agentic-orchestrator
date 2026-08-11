// Static verification for the local, operator-driven release chain.
import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { isMainModule } from './lib/main-entry.mjs';

import { LINUX_ARM64_VERIFIER_IMAGE, LINUX_BUILDER_IMAGE } from './lib/release-artifacts.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const rootDir = dirname(desktopDir);

/** Check that the checked-in release surfaces describe the actual local operator model. */
export function verifyLocalReleaseModel({
  makefile = readFileSync(join(rootDir, 'Makefile'), 'utf8'),
  builder = readFileSync(join(desktopDir, 'electron-builder.yml'), 'utf8'),
  signingScript = readFileSync(join(desktopDir, 'scripts', 'release-sign.mjs'), 'utf8'),
  releaseRunner = readFileSync(join(desktopDir, 'scripts', 'release-run.mjs'), 'utf8'),
  prepareServer = readFileSync(join(desktopDir, 'scripts', 'prepare-server.mjs'), 'utf8'),
  goreleaserConfig = readFileSync(join(rootDir, '.goreleaser.yaml'), 'utf8'),
} = {}) {
  const failures = [];
  const requiredCommands = ['node desktop/scripts/release-run.mjs'];
  for (const command of requiredCommands) {
    if (!makefile.includes(command)) failures.push(`release verification requires ${command}`);
  }
  if (/RELEASE_TAG|RELEASE_COMMIT|GORELEASER_FLAGS/.test(makefile)) {
    failures.push('release Makefile must not accept caller-controlled publication identity');
  }
  for (const token of [
    'createPublicationSnapshot',
    'verifyProvenance(evidence)',
    'await reserveTag({ evidence })',
    'publish({ evidence, snapshot })',
    'await verifyRemote({ evidence, snapshot })',
  ]) {
    if (!releaseRunner.includes(token))
      failures.push(`release runner is missing audited step: ${token}`);
  }
  if (!prepareServer.includes("'-mod=readonly'") || !prepareServer.includes('cleanGoEnvironment')) {
    failures.push('desktop Go build must disable workspaces/overlays and require readonly modules');
  }
  if (
    !goreleaserConfig.includes('GOWORK=off') ||
    !goreleaserConfig.includes('GOFLAGS=-mod=readonly')
  ) {
    failures.push(
      'GoReleaser builds must disable workspaces/overlays and require readonly modules',
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

if (isMainModule(import.meta.url)) main();
