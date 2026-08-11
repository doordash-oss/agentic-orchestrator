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
import { basename, isAbsolute, resolve, join, relative, sep } from 'node:path';

import {
  LINUX_ARM64_VERIFIER_IMAGE,
  LINUX_BUILDER_IMAGE,
  createLinuxDockerPlan,
  readArtifactEvidence,
  releaseVersionFromTag,
} from './lib/release-artifacts.mjs';
import { readReleaseEvidence, verifyReleaseProvenance } from './release-preflight.mjs';
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
  let directories;
  let runVolumesMayExist = false;
  let primaryError;
  let result;
  try {
    directories = prepareReleaseDirectories({
      repoRoot,
      gitWorktreeDir,
      gitCommonDir,
      gitEntry,
      runId,
    });
    const plan = createLinuxDockerPlan({
      repoRoot: directories.repoRoot,
      gitCommonDir: directories.gitCommonDir,
      gitEntry: directories.gitEntry,
      volumePrefix,
      cacheVolumePrefix: VOLUME_PREFIX,
      version,
      stagingDirs: directories.stagingDirs,
    });
    if (!resolveDockerAvailability(dockerAvailable)) {
      throw new Error('Docker daemon is unavailable; start Docker before packaging Linux releases');
    }

    removeFinalTargetOutputs(directories, version);
    ensureLinuxImages(execute);

    const completed = [];
    const receipts = [];
    for (const invocation of plan) {
      const receipt = `package-verification-linux-${invocation.arch}.json`;
      runVolumesMayExist = true;
      executeBoundDocker({ directories, arch: invocation.arch, execute, args: invocation.args });
      verifyProvenance();
      if (invocation.verificationArgs !== undefined) {
        validateStagingSet({
          directories,
          arch: invocation.arch,
          expected: targetOutputNames(invocation.arch, version, false),
        });
        executeBoundDocker({
          directories,
          arch: invocation.arch,
          execute,
          args: invocation.verificationArgs,
        });
        verifyProvenance();
      }
      assembleTargetOutputs({ directories, arch: invocation.arch, version, receipt });
      completed.push(invocation.arch);
      receipts.push(receipt);
    }
    result = { tag: exactTag, completed, receipts };
  } catch (error) {
    primaryError = error;
  }

  const cleanupErrors = [];
  if (runVolumesMayExist) {
    for (const suffix of [
      'node-modules',
      'verifier-node-modules',
      'electron',
      'electron-builder',
    ]) {
      try {
        execute('docker', ['volume', 'rm', '--force', `${volumePrefix}-${suffix}`]);
      } catch (error) {
        if (!isVolumeNotFound(error)) cleanupErrors.push(error);
      }
    }
  }
  if (directories !== undefined) {
    try {
      cleanupStagingRun(directories);
    } catch (error) {
      cleanupErrors.push(error);
    }
  }
  if (primaryError !== undefined && cleanupErrors.length > 0) {
    throw new AggregateError(
      [primaryError, ...cleanupErrors],
      `${errorMessage(primaryError)}; Linux release cleanup also failed: ${cleanupErrors.map(errorMessage).join('; ')}`,
      { cause: primaryError },
    );
  }
  if (primaryError !== undefined) throw primaryError;
  if (cleanupErrors.length > 0) {
    throw new AggregateError(
      cleanupErrors,
      `Linux release cleanup failed: ${cleanupErrors.map(errorMessage).join('; ')}`,
    );
  }
  return result;
}

function executeBoundDocker({ directories, arch, execute, args }) {
  revalidateReleaseDirectories(directories, arch);
  execute('docker', args);
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

function removeFinalTargetOutputs(directories, version) {
  for (const arch of ['x64', 'arm64']) {
    for (const name of targetOutputNames(arch, version)) {
      revalidateReleaseDirectories(directories, arch);
      rmSync(join(directories.distDir, name), { force: true });
    }
  }
  revalidateReleaseDirectories(directories);
  rmSync(join(directories.distDir, 'Agentico-x86_64.AppImage'), { force: true });
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

function isVolumeNotFound(error) {
  return /no such volume/i.test(errorMessage(error));
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}

/** Prepare real, contained final-output and per-worktree staging directories. */
export function prepareReleaseDirectories({
  repoRoot,
  gitWorktreeDir,
  gitCommonDir = gitWorktreeDir,
  gitEntry = gitCommonDir,
  runId,
}) {
  if (!/^[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(runId ?? '') || runId === '.' || runId === '..') {
    throw new Error(`invalid release run id: ${runId}`);
  }
  const repoToken = captureDirectory(repoRoot, 'repository root');
  const desktopToken = ensureContainedDirectory(repoToken, 'desktop', 'desktop directory');
  const distToken = ensureContainedDirectory(desktopToken, 'dist', 'final dist directory');
  const gitWorktreeToken = captureDirectory(gitWorktreeDir, 'per-worktree Git metadata');
  const gitCommonToken = captureDirectory(gitCommonDir, 'Git common directory');
  const gitEntryToken = captureGitEntry(gitEntry);
  const stagingBase = ensureContainedDirectory(
    gitWorktreeToken,
    'agentico-release-staging',
    'release staging root',
  );
  const runDir = ensureContainedDirectory(stagingBase, runId, 'release staging run directory');
  const stagingTokens = {};
  for (const arch of ['x64', 'arm64']) {
    const target = ensureContainedDirectory(runDir, arch, `${arch} release staging directory`);
    const entries = readdirSync(target.path);
    if (entries.length > 0) {
      throw new Error(
        `${arch} release staging directory contains stale or unexpected entries: ${entries.sort().join(', ')}`,
      );
    }
    stagingTokens[arch] = target;
  }
  const tokens = Object.freeze({
    repo: repoToken,
    desktop: desktopToken,
    dist: distToken,
    gitWorktree: gitWorktreeToken,
    gitCommon: gitCommonToken,
    gitEntry: gitEntryToken,
    stagingBase,
    run: runDir,
    staging: Object.freeze(stagingTokens),
  });
  return Object.freeze({
    repoRoot: repoToken.path,
    desktopDir: desktopToken.path,
    distDir: distToken.path,
    gitCommonDir: gitCommonToken.requested,
    gitEntry: gitEntryToken.requested,
    stagingRoot: runDir.path,
    stagingDirs: Object.freeze(
      Object.fromEntries(Object.entries(stagingTokens).map(([arch, token]) => [arch, token.path])),
    ),
    tokens,
  });
}

function captureDirectory(path, label) {
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
  return Object.freeze({
    requested,
    path: canonical,
    label,
    dev: canonicalStat.dev,
    ino: canonicalStat.ino,
    kind: 'directory',
  });
}

function captureGitEntry(path) {
  const requested = resolve(path);
  const stat = lstatSync(requested);
  if (stat.isSymbolicLink()) {
    throw new Error(`Git entry must not be a symbolic link: ${requested}`);
  }
  if (!stat.isDirectory() && !stat.isFile()) {
    throw new Error(`Git entry must be a directory or gitfile: ${requested}`);
  }
  const canonical = realpathSync(requested);
  const canonicalStat = lstatSync(canonical);
  return Object.freeze({
    requested,
    path: canonical,
    label: 'Git entry',
    dev: canonicalStat.dev,
    ino: canonicalStat.ino,
    kind: canonicalStat.isDirectory() ? 'directory' : 'file',
  });
}

function ensureContainedDirectory(parentToken, childName, label) {
  if (basename(childName) !== childName || childName === '.' || childName === '..') {
    throw new Error(`${label} has an unsafe name: ${childName}`);
  }
  const target = join(parentToken.path, childName);
  if (!existsSync(target)) mkdirSync(target);
  const token = captureDirectory(target, label);
  const relation = relative(parentToken.path, token.path);
  if (
    relation === '..' ||
    relation.startsWith(`..${sep}`) ||
    resolve(token.path) === resolve(parentToken.path)
  ) {
    throw new Error(`${label} escapes its expected parent: ${target}`);
  }
  return token;
}

function revalidatePathToken(token) {
  let stat;
  try {
    stat = lstatSync(token.requested);
  } catch (error) {
    throw new Error(`${token.label} is unavailable: ${token.requested}`, { cause: error });
  }
  if (stat.isSymbolicLink()) {
    throw new Error(`${token.label} must not be a symbolic link: ${token.requested}`);
  }
  const canonical = realpathSync(token.requested);
  const canonicalStat = lstatSync(canonical);
  const correctKind =
    token.kind === 'directory' ? canonicalStat.isDirectory() : canonicalStat.isFile();
  if (
    !correctKind ||
    canonical !== token.path ||
    canonicalStat.dev !== token.dev ||
    canonicalStat.ino !== token.ino
  ) {
    throw new Error(`${token.label} directory changed after validation: ${token.requested}`);
  }
}

function revalidateContainedToken(token, parentToken) {
  revalidatePathToken(parentToken);
  revalidatePathToken(token);
  const relation = relative(parentToken.path, token.path);
  if (relation === '..' || relation.startsWith(`..${sep}`) || token.path === parentToken.path) {
    throw new Error(`${token.label} escapes its expected parent: ${token.path}`);
  }
}

function revalidateReleaseDirectories(directories, arch) {
  const { tokens } = directories;
  revalidatePathToken(tokens.repo);
  revalidateContainedToken(tokens.desktop, tokens.repo);
  revalidateContainedToken(tokens.dist, tokens.desktop);
  revalidatePathToken(tokens.gitWorktree);
  revalidatePathToken(tokens.gitCommon);
  revalidatePathToken(tokens.gitEntry);
  revalidateContainedToken(tokens.stagingBase, tokens.gitWorktree);
  revalidateContainedToken(tokens.run, tokens.stagingBase);
  const arches = arch === undefined ? ['x64', 'arm64'] : [arch];
  for (const selected of arches) revalidateContainedToken(tokens.staging[selected], tokens.run);
}

function validateStagingSet({ directories, arch, expected }) {
  revalidateReleaseDirectories(directories, arch);
  const canonical = directories.stagingDirs[arch];
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

function assembleTargetOutputs({ directories, arch, version, receipt }) {
  revalidateReleaseDirectories(directories, arch);
  const stagingDir = directories.stagingDirs[arch];
  const distDir = directories.distDir;
  const expected = targetOutputNames(arch, version);
  validateStagingSet({ directories, arch, expected });
  const artifactNames = expected.filter((name) => name !== receipt);
  const stagedEvidence = Object.fromEntries(
    artifactNames.map((name) => {
      revalidateReleaseDirectories(directories, arch);
      return [name, readArtifactEvidence(join(stagingDir, name))];
    }),
  );
  let receiptValue;
  try {
    revalidateReleaseDirectories(directories, arch);
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
    revalidateReleaseDirectories(directories, arch);
    const destination = join(distDir, name);
    copyFileSync(join(stagingDir, name), destination, constants.COPYFILE_EXCL);
    const finalEvidence = readArtifactEvidence(destination);
    const staged = stagedEvidence[name];
    if (finalEvidence.sha256 !== staged.sha256 || finalEvidence.size !== staged.size) {
      throw new Error(`${arch} final artifact copy does not match staged bytes: ${name}`);
    }
    const entry = receiptArtifacts.find((artifact) => basename(artifact?.path ?? '') === name);
    revalidateReleaseDirectories(directories, arch);
    entry.path = finalEvidence.path;
  }
  revalidateReleaseDirectories(directories, arch);
  writeFileSync(join(distDir, receipt), `${JSON.stringify(receiptValue, null, 2)}\n`, {
    flag: 'wx',
  });

  for (const name of artifactNames) {
    revalidateReleaseDirectories(directories, arch);
    const finalEvidence = readArtifactEvidence(join(distDir, name));
    if (
      finalEvidence.sha256 !== stagedEvidence[name].sha256 ||
      finalEvidence.size !== stagedEvidence[name].size
    ) {
      throw new Error(`${arch} final inventory rehash failed for ${name}`);
    }
  }
}

function cleanupStagingRun(directories) {
  revalidateReleaseDirectories(directories);
  rmSync(directories.stagingRoot, { recursive: true });
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

export function resolveLinuxReleaseEvidence({
  env = process.env,
  readEvidence = (path, digest) =>
    readReleaseEvidence({ evidencePath: path, expectedDigest: digest }),
  verifyEvidence = (evidence) => verifyReleaseProvenance({ evidence }),
} = {}) {
  const path = env.AGENTICO_RELEASE_EVIDENCE_FILE;
  if (typeof path !== 'string' || !isAbsolute(path)) {
    throw new Error('AGENTICO_RELEASE_EVIDENCE_FILE must be an absolute validated path');
  }
  const digest = env.AGENTICO_RELEASE_EVIDENCE_SHA256;
  if (typeof digest !== 'string' || !/^[0-9a-f]{64}$/.test(digest)) {
    throw new Error('AGENTICO_RELEASE_EVIDENCE_SHA256 must bind the exact evidence bytes');
  }
  const evidence = readEvidence(path, digest);
  if (evidence.evidence_sha256 !== digest) {
    throw new Error('Linux release evidence digest does not match the parent handoff');
  }
  if (evidence.evidence_path !== path) {
    throw new Error('Linux release evidence path does not match captured preflight evidence');
  }
  return verifyEvidence(evidence);
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
  const evidence = resolveLinuxReleaseEvidence();
  const result = runLocalLinuxRelease({
    volumePrefix: `${VOLUME_PREFIX}-${evidence.run_id.replace(/[^a-z0-9]/gi, '')}`,
    verifyProvenance: () => verifyReleaseProvenance({ evidence }),
  });
  console.log(`Linux release packages verified for ${result.tag}: ${result.completed.join(', ')}`);
}

if (isMainModule(import.meta.url)) main();
