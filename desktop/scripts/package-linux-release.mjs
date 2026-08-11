// Build and verify Linux desktop release packages locally in sequential Docker containers.
import { execFileSync } from 'node:child_process';
import {
  constants,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  realpathSync,
  rmSync,
  statfsSync,
  writeFileSync,
} from 'node:fs';
import { basename, resolve, join, relative, sep } from 'node:path';

import {
  LINUX_ARM64_VERIFIER_IMAGE,
  LINUX_BUILDER_IMAGE,
  createLinuxDockerPlan,
  readArtifactEvidence,
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
  gitWorktreeDir = gitCommonDir,
  gitEntry,
  gitStatus,
  exactTag,
  freeBytes,
  dockerAvailable,
  execute,
  verifyProvenance = () => {},
  volumePrefix = VOLUME_PREFIX,
  runId = volumePrefix,
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

  const directories = prepareReleaseDirectories({ repoRoot, gitWorktreeDir, runId });
  const plan = createLinuxDockerPlan({
    repoRoot: directories.repoRoot,
    gitCommonDir,
    gitEntry,
    volumePrefix,
    version,
    stagingDirs: directories.stagingDirs,
  });
  removeFinalTargetOutputs(directories.distDir, version);
  ensureLinuxImages(execute);

  const completed = [];
  const receipts = [];
  for (const invocation of plan) {
    const receipt = `package-verification-linux-${invocation.arch}.json`;
    execute('docker', invocation.args);
    verifyProvenance();
    if (invocation.verificationArgs !== undefined) {
      validateStagingSet({
        directory: directories.stagingDirs[invocation.arch],
        expected: targetOutputNames(invocation.arch, version, false),
      });
      execute('docker', invocation.verificationArgs);
      verifyProvenance();
    }
    assembleTargetOutputs({
      stagingDir: directories.stagingDirs[invocation.arch],
      distDir: directories.distDir,
      arch: invocation.arch,
      version,
      receipt,
    });
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

function targetOutputNames(arch, version, includeReceipt = true) {
  const debArch = arch === 'x64' ? 'amd64' : 'arm64';
  const names = [`Agentico-${arch}.AppImage`, `agentico_${version}_${debArch}.deb`];
  if (includeReceipt) names.push(`package-verification-linux-${arch}.json`);
  return names;
}

function removeFinalTargetOutputs(distDir, version) {
  for (const arch of ['x64', 'arm64']) {
    for (const name of targetOutputNames(arch, version))
      rmSync(join(distDir, name), { force: true });
  }
  rmSync(join(distDir, 'Agentico-x86_64.AppImage'), { force: true });
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

/** Prepare real, contained final-output and per-worktree staging directories. */
export function prepareReleaseDirectories({ repoRoot, gitWorktreeDir, runId }) {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(runId ?? '') || runId === '.' || runId === '..') {
    throw new Error(`invalid release run id: ${runId}`);
  }
  const canonicalRepo = requireRealDirectory(repoRoot, 'repository root');
  const desktopDir = ensureContainedDirectory(canonicalRepo, 'desktop', 'desktop directory');
  const distDir = ensureContainedDirectory(desktopDir, 'dist', 'final dist directory');
  const canonicalGitDir = requireRealDirectory(gitWorktreeDir, 'per-worktree Git metadata');
  const stagingBase = ensureContainedDirectory(
    canonicalGitDir,
    'agentico-release-staging',
    'release staging root',
  );
  const runDir = ensureContainedDirectory(stagingBase, runId, 'release staging run directory');
  const stagingDirs = {};
  for (const arch of ['x64', 'arm64']) {
    const target = ensureContainedDirectory(runDir, arch, `${arch} release staging directory`);
    const entries = readdirSync(target);
    if (entries.length > 0) {
      throw new Error(
        `${arch} release staging directory contains stale or unexpected entries: ${entries.sort().join(', ')}`,
      );
    }
    stagingDirs[arch] = target;
  }
  return Object.freeze({
    repoRoot: canonicalRepo,
    desktopDir,
    distDir,
    stagingRoot: runDir,
    stagingDirs: Object.freeze(stagingDirs),
  });
}

function requireRealDirectory(path, label) {
  const requested = resolve(path);
  let stat;
  try {
    stat = lstatSync(requested);
  } catch (error) {
    throw new Error(`${label} is unavailable: ${requested}`, { cause: error });
  }
  if (stat.isSymbolicLink()) throw new Error(`${label} must not be a symbolic link: ${requested}`);
  if (!stat.isDirectory()) throw new Error(`${label} is not a directory: ${requested}`);
  const canonical = realpathSync(requested);
  const canonicalStat = lstatSync(canonical);
  if (!canonicalStat.isDirectory() || canonicalStat.isSymbolicLink()) {
    throw new Error(`${label} is not a real directory: ${requested}`);
  }
  return canonical;
}

function ensureContainedDirectory(parent, childName, label) {
  if (basename(childName) !== childName || childName === '.' || childName === '..') {
    throw new Error(`${label} has an unsafe name: ${childName}`);
  }
  const target = join(parent, childName);
  if (!existsSync(target)) mkdirSync(target);
  const canonical = requireRealDirectory(target, label);
  const relation = relative(parent, canonical);
  if (
    relation === '..' ||
    relation.startsWith(`..${sep}`) ||
    resolve(canonical) === resolve(parent)
  ) {
    throw new Error(`${label} escapes its expected parent: ${target}`);
  }
  return canonical;
}

function validateStagingSet({ directory, expected }) {
  const canonical = requireRealDirectory(directory, 'release staging directory');
  const actual = readdirSync(canonical).sort();
  const wanted = [...expected].sort();
  if (JSON.stringify(actual) !== JSON.stringify(wanted)) {
    const unexpected = actual.filter((name) => !wanted.includes(name));
    const missing = wanted.filter((name) => !actual.includes(name));
    throw new Error(
      `unexpected staging entries${unexpected.length > 0 ? `: ${unexpected.join(', ')}` : ''}` +
        `${missing.length > 0 ? `; missing required entries: ${missing.join(', ')}` : ''}`,
    );
  }
  for (const name of actual) {
    const path = join(canonical, name);
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink()) {
      throw new Error(`staging entry is not a regular file: ${path}`);
    }
    if (stat.size <= 0) throw new Error(`staging entry is empty: ${path}`);
    const resolved = realpathSync(path);
    const relation = relative(canonical, resolved);
    if (relation === '..' || relation.startsWith(`..${sep}`)) {
      throw new Error(`staging entry escapes its target directory: ${path}`);
    }
  }
}

function assembleTargetOutputs({ stagingDir, distDir, arch, version, receipt }) {
  const expected = targetOutputNames(arch, version);
  validateStagingSet({ directory: stagingDir, expected });
  const artifactNames = expected.filter((name) => name !== receipt);
  const stagedEvidence = Object.fromEntries(
    artifactNames.map((name) => [name, readArtifactEvidence(join(stagingDir, name))]),
  );
  let receiptValue;
  try {
    receiptValue = JSON.parse(readFileSync(join(stagingDir, receipt), 'utf8'));
  } catch (error) {
    throw new Error(`${arch} staging receipt is not valid JSON`, { cause: error });
  }
  const receiptArtifacts = Array.isArray(receiptValue.artifacts) ? receiptValue.artifacts : [];
  for (const name of artifactNames) {
    const entry = receiptArtifacts.find((artifact) => basename(artifact?.path ?? '') === name);
    if (entry === undefined) throw new Error(`${arch} staging receipt does not bind ${name}`);
    const evidence = stagedEvidence[name];
    if (entry.sha256 !== evidence.sha256 || entry.size !== evidence.size) {
      throw new Error(`${arch} staging receipt evidence does not match ${name}`);
    }
  }

  for (const name of artifactNames) {
    const destination = join(distDir, name);
    copyFileSync(join(stagingDir, name), destination, constants.COPYFILE_EXCL);
    const finalEvidence = readArtifactEvidence(destination);
    const staged = stagedEvidence[name];
    if (finalEvidence.sha256 !== staged.sha256 || finalEvidence.size !== staged.size) {
      throw new Error(`${arch} final artifact copy does not match staged bytes: ${name}`);
    }
    const entry = receiptArtifacts.find((artifact) => basename(artifact?.path ?? '') === name);
    entry.path = finalEvidence.path;
  }
  writeFileSync(join(distDir, receipt), `${JSON.stringify(receiptValue, null, 2)}\n`, {
    flag: 'wx',
  });

  for (const name of artifactNames) {
    const finalEvidence = readArtifactEvidence(join(distDir, name));
    if (
      finalEvidence.sha256 !== stagedEvidence[name].sha256 ||
      finalEvidence.size !== stagedEvidence[name].size
    ) {
      throw new Error(`${arch} final inventory rehash failed for ${name}`);
    }
  }
}

function git(cwd, ...args) {
  return execFileSync('git', args, {
    cwd,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
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
    gitWorktreeDir: resolve(repoRoot, gitCommand(repoRoot, 'rev-parse', '--git-dir')),
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
  const { repoRoot, gitEntry, gitCommonDir, gitWorktreeDir } = localPlanOptions(cwd, gitCommand);
  const stats = statfs(repoRoot);
  return runLinuxRelease({
    repoRoot,
    gitCommonDir,
    gitEntry,
    gitWorktreeDir,
    runId: volumePrefix,
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
    const local = localPlanOptions(process.cwd());
    let version = '0.0.0';
    try {
      version = releaseVersionFromTag(git(process.cwd(), 'describe', '--tags', '--exact-match'));
    } catch {
      // A print-only plan remains useful before tagging and never contacts Docker.
    }
    const stagingBase = join(local.gitWorktreeDir, 'agentico-release-staging', 'print-plan');
    const plan = createLinuxDockerPlan({
      ...local,
      volumePrefix: VOLUME_PREFIX,
      version,
      stagingDirs: { x64: join(stagingBase, 'x64'), arm64: join(stagingBase, 'arm64') },
    });
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
