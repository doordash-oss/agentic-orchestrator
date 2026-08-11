// Build and verify Linux desktop release packages locally in sequential Docker containers.
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, rmSync, statSync, statfsSync } from 'node:fs';
import { resolve, join } from 'node:path';

import {
  LINUX_ARM64_VERIFIER_IMAGE,
  LINUX_BUILDER_IMAGE,
  createLinuxDockerPlan,
  releaseVersionFromTag,
} from './lib/release-artifacts.mjs';
import { verifyReleaseProvenance } from './release-preflight.mjs';
import { isMainModule } from './lib/main-entry.mjs';

const MINIMUM_FREE_BYTES = 12 * 1024 ** 3;
const VOLUME_PREFIX = 'agentico-release';

/** Run the local Linux package-and-verify builds after validating release prerequisites. */
export function runLinuxRelease({
  repoRoot,
  gitCommonDir,
  gitEntry,
  gitStatus,
  exactTag,
  freeBytes,
  dockerAvailable,
  execute,
  verifyProvenance = () => {},
  volumePrefix = VOLUME_PREFIX,
}) {
  if (gitStatus.trim() !== '') {
    throw new Error(
      'working tree is dirty; commit or stash changes before packaging Linux releases',
    );
  }
  const version = releaseVersionFromTag(exactTag);
  if (freeBytes < MINIMUM_FREE_BYTES) {
    throw new Error('at least 12 GiB of free disk space is required for Linux release packaging');
  }
  if (!resolveDockerAvailability(dockerAvailable)) {
    throw new Error('Docker daemon is unavailable; start Docker before packaging Linux releases');
  }

  const plan = createLinuxDockerPlan({ repoRoot, gitCommonDir, gitEntry, volumePrefix });
  for (const directory of ['desktop/dist', 'desktop/out', 'desktop/resources']) {
    mkdirSync(join(repoRoot, directory), { recursive: true });
  }
  ensureLinuxImages(execute);

  const completed = [];
  const receipts = [];
  for (const invocation of plan) {
    const receipt = `package-verification-linux-${invocation.arch}.json`;
    removeBuildOutputs(repoRoot, invocation.arch, version, receipt);
    execute('docker', invocation.args);
    verifyProvenance();
    if (invocation.verificationArgs !== undefined) {
      execute('docker', invocation.verificationArgs);
      verifyProvenance();
    }
    requireBuildOutputs(repoRoot, invocation.arch, version, receipt);
    completed.push(invocation.arch);
    receipts.push(receipt);
  }

  return { tag: exactTag, completed, receipts };
}

function ensureLinuxImages(execute) {
  for (const image of [LINUX_BUILDER_IMAGE, LINUX_ARM64_VERIFIER_IMAGE]) {
    try {
      execute('docker', ['image', 'inspect', image]);
    } catch (error) {
      if (!isImageNotFound(error)) throw error;
      execute('docker', ['pull', image]);
    }
  }
}

function requireBuildOutputs(repoRoot, arch, version, receipt) {
  const debArch = arch === 'x64' ? 'amd64' : arch;
  const required = [receipt, `Agentico-${arch}.AppImage`, `agentico_${version}_${debArch}.deb`];
  const distDir = join(repoRoot, 'desktop', 'dist');
  const missing = required.filter((name) => !isFile(join(distDir, name)));
  if (missing.length > 0) {
    throw new Error(
      `${arch} package verification did not produce required release files: ${missing.join(', ')}`,
    );
  }
}

function removeBuildOutputs(repoRoot, arch, version, receipt) {
  const debArch = arch === 'x64' ? 'amd64' : arch;
  const artifacts = [receipt, `Agentico-${arch}.AppImage`, `agentico_${version}_${debArch}.deb`];
  if (arch === 'x64') artifacts.push('Agentico-x86_64.AppImage');
  for (const name of artifacts) {
    rmSync(join(repoRoot, 'desktop', 'dist', name), { force: true });
  }
}

function resolveDockerAvailability(dockerAvailable) {
  return typeof dockerAvailable === 'function' ? dockerAvailable() : dockerAvailable;
}

function isImageNotFound(error) {
  const detail = [error?.message, error?.stderr, error?.stdout]
    .filter((value) => value !== undefined)
    .map((value) => String(value))
    .join('\n');
  return /(?:no such image|no such object|image .* not found)/i.test(detail);
}

function isFile(path) {
  try {
    return existsSync(path) && statSync(path).isFile();
  } catch {
    return false;
  }
}

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function commandSucceeds(command, args, cwd) {
  try {
    execFileSync(command, args, { cwd, stdio: 'ignore' });
    return true;
  } catch {
    return false;
  }
}

function executeDocker(command, args, cwd) {
  const inspect = args[0] === 'image' && args[1] === 'inspect';
  execFileSync(command, args, { cwd, stdio: inspect ? ['ignore', 'pipe', 'pipe'] : 'inherit' });
}

function localPlanOptions(cwd, gitCommand = git) {
  const repoRoot = gitCommand(cwd, 'rev-parse', '--show-toplevel');
  return {
    repoRoot,
    gitEntry: resolve(repoRoot, '.git'),
    gitCommonDir: resolve(repoRoot, gitCommand(repoRoot, 'rev-parse', '--git-common-dir')),
    volumePrefix: VOLUME_PREFIX,
  };
}

export function runLocalLinuxRelease({
  cwd = process.cwd(),
  gitCommand = git,
  statfs = statfsSync,
  dockerInfo = (repoRoot) => commandSucceeds('docker', ['info'], repoRoot),
  execute,
  verifyProvenance = () => {},
  volumePrefix = VOLUME_PREFIX,
} = {}) {
  const { repoRoot, gitEntry, gitCommonDir } = localPlanOptions(cwd, gitCommand);
  const stats = statfs(repoRoot);
  return runLinuxRelease({
    repoRoot,
    gitCommonDir,
    gitEntry,
    volumePrefix,
    exactTag: gitCommand(repoRoot, 'describe', '--tags', '--exact-match'),
    gitStatus: gitCommand(repoRoot, 'status', '--porcelain'),
    freeBytes: stats.bavail * stats.bsize,
    dockerAvailable: () => dockerInfo(repoRoot),
    execute:
      execute === undefined ? (command, args) => executeDocker(command, args, repoRoot) : execute,
    verifyProvenance,
  });
}

function main() {
  const printPlan = process.argv.slice(2).includes('--print-plan');
  if (printPlan) {
    const plan = createLinuxDockerPlan(localPlanOptions(process.cwd()));
    console.log(JSON.stringify(plan, null, 2));
    return;
  }
  const evidence = verifyReleaseProvenance();
  const result = runLocalLinuxRelease({
    volumePrefix: `${VOLUME_PREFIX}-${evidence.run_id.replace(/[^a-z0-9]/gi, '')}`,
    verifyProvenance: () => verifyReleaseProvenance(),
  });
  console.log(`Linux release packages verified for ${result.tag}: ${result.completed.join(', ')}`);
}

if (isMainModule(import.meta.url)) main();
