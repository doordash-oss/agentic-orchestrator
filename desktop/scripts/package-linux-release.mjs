// Build and verify Linux desktop release packages locally in sequential Docker containers.
import { execFileSync } from 'node:child_process';
import { existsSync, statSync, statfsSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  LINUX_BUILDER_IMAGE,
  createLinuxDockerPlan,
  releaseVersionFromTag,
} from './lib/release-artifacts.mjs';

const MINIMUM_FREE_BYTES = 12 * 1024 ** 3;
const VOLUME_PREFIX = 'agentico-release';

/** Run the local Linux package-and-verify builds after validating release prerequisites. */
export function runLinuxRelease({
  repoRoot,
  gitCommonDir,
  gitStatus,
  exactTag,
  freeBytes,
  dockerAvailable,
  execute,
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
  if (!dockerAvailable) {
    throw new Error('Docker daemon is unavailable; start Docker before packaging Linux releases');
  }

  const plan = createLinuxDockerPlan({ repoRoot, gitCommonDir, volumePrefix });
  ensureLinuxBuilderImage(execute);

  const completed = [];
  const receipts = [];
  for (const invocation of plan) {
    execute('docker', invocation.args);
    const receipt = `package-verification-linux-${invocation.arch}.json`;
    requireBuildOutputs(repoRoot, invocation.arch, version, receipt);
    completed.push(invocation.arch);
    receipts.push(receipt);
  }

  return { tag: exactTag, completed, receipts };
}

function ensureLinuxBuilderImage(execute) {
  try {
    execute('docker', ['image', 'inspect', LINUX_BUILDER_IMAGE]);
  } catch {
    execute('docker', ['pull', LINUX_BUILDER_IMAGE]);
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
  execFileSync(command, args, { cwd, stdio: 'inherit' });
}

function localPlanOptions(cwd) {
  const repoRoot = git(cwd, 'rev-parse', '--show-toplevel');
  return {
    repoRoot,
    gitCommonDir: resolve(repoRoot, git(repoRoot, 'rev-parse', '--git-common-dir')),
    volumePrefix: VOLUME_PREFIX,
  };
}

function localReleaseOptions(cwd) {
  const { repoRoot, gitCommonDir, volumePrefix } = localPlanOptions(cwd);
  const stats = statfsSync(repoRoot);
  return {
    repoRoot,
    gitCommonDir,
    volumePrefix,
    exactTag: git(repoRoot, 'describe', '--tags', '--exact-match'),
    gitStatus: git(repoRoot, 'status', '--porcelain'),
    freeBytes: stats.bavail * stats.bsize,
    dockerAvailable: commandSucceeds('docker', ['info'], repoRoot),
    execute: (command, args) => executeDocker(command, args, repoRoot),
  };
}

function main() {
  const printPlan = process.argv.slice(2).includes('--print-plan');
  if (printPlan) {
    const plan = createLinuxDockerPlan(localPlanOptions(process.cwd()));
    console.log(JSON.stringify(plan, null, 2));
    return;
  }
  const result = runLinuxRelease(localReleaseOptions(process.cwd()));
  console.log(`Linux release packages verified for ${result.tag}: ${result.completed.join(', ')}`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
