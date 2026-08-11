// Isolated committed-source workspace and immutable publication snapshot helpers.
import { execFileSync } from 'node:child_process';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  realpathSync,
  renameSync,
  fsyncSync,
  closeSync,
  constants,
  writeFileSync,
} from 'node:fs';
import { basename, dirname, join, relative, resolve, sep } from 'node:path';

import { readArtifactEvidence } from './lib/release-artifacts.mjs';

const RUN_ID = /^[a-zA-Z0-9][a-zA-Z0-9._-]*$/;
const FULL_SHA = /^[0-9a-f]{40}$/i;

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function contained(parent, child, label) {
  const parentPath = realpathSync(parent);
  const childPath = resolve(child);
  const relation = relative(parentPath, childPath);
  if (relation === '' || relation === '..' || relation.startsWith(`..${sep}`)) {
    throw new Error(`${label} escapes its validated parent`);
  }
  return childPath;
}

/** Drop ambient Go workspace/overlay flags and require lockfile-compatible module resolution. */
export function cleanGoEnvironment(env = process.env) {
  return { ...env, GOWORK: 'off', GOFLAGS: '-mod=readonly' };
}

function selectedEnvironment(env, names) {
  return Object.fromEntries(
    names.filter((name) => env[name] !== undefined).map((name) => [name, env[name]]),
  );
}

const BUILD_ENVIRONMENT_NAMES = Object.freeze([
  'PATH',
  'LANG',
  'LC_ALL',
  'LC_CTYPE',
  'TMPDIR',
  'TMP',
  'TEMP',
  'USER',
  'LOGNAME',
  'CI',
  'NPM_TOKEN',
  'NODE_AUTH_TOKEN',
]);

/** Build subprocesses get dependency auth but never publication/signing credentials. */
export function secretFreeBuildEnvironment(env, evidencePath, evidenceDigest, isolatedHome) {
  const clean = selectedEnvironment(env, BUILD_ENVIRONMENT_NAMES);
  clean.HOME = isolatedHome;
  clean.NPM_CONFIG_CACHE = join(isolatedHome, '.npm');
  clean.GOWORK = 'off';
  clean.GOFLAGS = '-mod=readonly';
  clean.AGENTICO_RELEASE_EVIDENCE_FILE = evidencePath;
  clean.AGENTICO_RELEASE_EVIDENCE_SHA256 = evidenceDigest;
  return clean;
}

const DOCKER_ENVIRONMENT_NAMES = Object.freeze([
  'PATH',
  'HOME',
  'USER',
  'LOGNAME',
  'TMPDIR',
  'DOCKER_HOST',
  'DOCKER_CONTEXT',
  'DOCKER_CONFIG',
  'DOCKER_CERT_PATH',
  'DOCKER_TLS_VERIFY',
  'HTTP_PROXY',
  'HTTPS_PROXY',
  'NO_PROXY',
  'http_proxy',
  'https_proxy',
  'no_proxy',
]);

/** Docker orchestration retains only operator Docker routing, never native npm overrides. */
export function dockerReleaseEnvironment(env, evidencePath, evidenceDigest) {
  return {
    ...selectedEnvironment(env, DOCKER_ENVIRONMENT_NAMES),
    AGENTICO_RELEASE_EVIDENCE_FILE: evidencePath,
    AGENTICO_RELEASE_EVIDENCE_SHA256: evidenceDigest,
  };
}

/** Manifest signing receives only key discovery state, never GitHub publication credentials. */
export function releaseSigningEnvironment(env) {
  return selectedEnvironment(env, [
    'PATH',
    'HOME',
    'TMPDIR',
    'TMP',
    'TEMP',
    'AGENTICO_RELEASE_SIGNING_KEY',
    'AGENTICO_RELEASE_SIGNING_KEY_FILE',
  ]);
}

/** Publication retains required credentials while rejecting ambient GoReleaser overrides. */
export function goreleaserEnvironment(env, tag) {
  const clean = cleanGoEnvironment(env);
  for (const name of Object.keys(clean)) {
    if (name.startsWith('GORELEASER_')) delete clean[name];
    if (name === 'NODE_OPTIONS' || /^npm_config_/i.test(name)) delete clean[name];
  }
  clean.GORELEASER_CURRENT_TAG = tag;
  return clean;
}

function captureWorkspaceToken(path) {
  const requested = resolve(path);
  const stat = lstatSync(requested);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new Error(`release workspace is not a real directory: ${requested}`);
  }
  return Object.freeze({
    path: realpathSync(requested),
    kind: 'directory',
    dev: stat.dev,
    ino: stat.ino,
  });
}

export function revalidateWorkspaceToken(token) {
  if (token?.kind !== 'directory' || typeof token.path !== 'string') {
    throw new Error('release workspace token is invalid');
  }
  let stat;
  try {
    stat = lstatSync(token.path);
  } catch (error) {
    throw new Error('release workspace changed after validation', { cause: error });
  }
  if (
    !stat.isDirectory() ||
    stat.isSymbolicLink() ||
    realpathSync(token.path) !== token.path ||
    stat.dev !== token.dev ||
    stat.ino !== token.ino
  ) {
    throw new Error('release workspace changed after validation');
  }
  return token;
}

function inodeBoundCleanup(workspace) {
  if (workspace.cleanupHelper === undefined) {
    throw new Error('release cleanup requires the precompiled evidence-bound helper');
  }
  const actual = readArtifactEvidence(workspace.cleanupHelper.path);
  if (
    actual.sha256 !== workspace.cleanupHelper.sha256 ||
    actual.size !== workspace.cleanupHelper.size
  ) {
    throw new Error('release cleanup helper changed after preflight evidence');
  }
  return execFileSync(
    workspace.cleanupHelper.path,
    [workspace.path, String(workspace.token.dev), String(workspace.token.ino)],
    {
      env: selectedEnvironment(process.env, ['PATH', 'TMPDIR', 'TMP', 'TEMP']),
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  );
}

function syncDirectory(path) {
  const fd = openSync(path, constants.O_RDONLY);
  try {
    fsyncSync(fd);
  } finally {
    closeSync(fd);
  }
}

function syncRegularFile(path) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
  try {
    fsyncSync(fd);
  } finally {
    closeSync(fd);
  }
}

/** Compile the committed cleanup helper before any build or publication subprocess receives credentials. */
export function compileReleaseCleanupHelper({ workspace, execute = execFileSync } = {}) {
  const commonDir = realpathSync(workspace.commonDir);
  const directory = contained(
    commonDir,
    join(commonDir, 'agentico-release-cleanup-helpers', workspace.runId),
    'release cleanup helper directory',
  );
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const path = join(directory, 'release-cleanup');
  const temporary = join(directory, '.release-cleanup.tmp');
  if (existsSync(path) || existsSync(temporary)) {
    throw new Error(`release cleanup helper path already exists: ${directory}`);
  }
  execute('go', ['build', '-trimpath', '-o', temporary, './desktop/scripts/release-cleanup'], {
    cwd: workspace.path,
    env: {
      ...selectedEnvironment(process.env, [
        'PATH',
        'HOME',
        'TMPDIR',
        'TMP',
        'TEMP',
        'USER',
        'LOGNAME',
        'GOCACHE',
        'GOMODCACHE',
        'GOPATH',
      ]),
      GOWORK: 'off',
      GOFLAGS: '-mod=readonly',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  chmodSync(temporary, 0o500);
  renameSync(temporary, path);
  syncDirectory(directory);
  const evidence = readArtifactEvidence(path);
  return Object.freeze({ path: evidence.path, sha256: evidence.sha256, size: evidence.size });
}

/** Create a detached Git worktree whose filesystem starts from only captured committed files. */
export function createDetachedReleaseWorkspace({
  operatorRoot,
  commit,
  runId,
  gitCommand = git,
} = {}) {
  if (!FULL_SHA.test(commit ?? '')) throw new Error('release workspace requires a full commit SHA');
  if (!RUN_ID.test(runId ?? '') || runId === '.' || runId === '..') {
    throw new Error(`invalid release run id: ${runId}`);
  }
  const commonDir = realpathSync(
    resolve(
      operatorRoot,
      gitCommand(operatorRoot, 'rev-parse', '--path-format=absolute', '--git-common-dir'),
    ),
  );
  const base = join(commonDir, 'agentico-release-workspaces');
  mkdirSync(base, { recursive: true, mode: 0o700 });
  const path = contained(base, join(base, runId), 'release workspace');
  if (existsSync(path)) throw new Error(`release workspace already exists: ${path}`);
  gitCommand(operatorRoot, 'worktree', 'add', '--detach', path, commit);
  const token = captureWorkspaceToken(path);
  const preservedWorkspace = `preserved at ${token.path} with Git administration for forensic inspection and manual cleanup`;
  let actual;
  let status;
  try {
    actual = gitCommand(path, 'rev-parse', 'HEAD');
    status = gitCommand(path, 'status', '--porcelain', '--untracked-files=all');
  } catch (error) {
    throw new Error(`could not validate detached release workspace; ${preservedWorkspace}`, {
      cause: error,
    });
  }
  if (actual !== commit || status !== '') {
    throw new Error(
      `detached release workspace does not exactly match captured committed source; ${preservedWorkspace}`,
    );
  }
  return Object.freeze({
    operatorRoot: realpathSync(operatorRoot),
    commonDir,
    path: token.path,
    commit,
    runId,
    token,
  });
}

/** Remove only a workspace object returned by createDetachedReleaseWorkspace. */
export function removeDetachedReleaseWorkspace(
  workspace,
  { cleanupCommand = inodeBoundCleanup } = {},
) {
  if (
    workspace === null ||
    typeof workspace !== 'object' ||
    !FULL_SHA.test(workspace.commit ?? '') ||
    !RUN_ID.test(workspace.runId ?? '')
  ) {
    throw new Error('refusing to remove an unvalidated release workspace');
  }
  const expected = contained(
    join(workspace.commonDir, 'agentico-release-workspaces'),
    join(workspace.commonDir, 'agentico-release-workspaces', workspace.runId),
    'release workspace',
  );
  if (resolve(workspace.path) !== expected) {
    throw new Error('refusing to remove a release workspace at an unexpected path');
  }
  revalidateWorkspaceToken(workspace.token);
  cleanupCommand(workspace);
}

function copyRegular(source, destination, syncFile) {
  const evidence = readArtifactEvidence(source);
  copyFileSync(evidence.path, destination);
  chmodSync(destination, 0o400);
  syncFile(destination);
  const copied = readArtifactEvidence(destination);
  if (copied.sha256 !== evidence.sha256 || copied.size !== evidence.size) {
    throw new Error(`publication copy digest mismatch for ${basename(source)}`);
  }
  return copied;
}

/** Copy the final receipt-bound package set into a read-only publication directory. */
export function createPublicationSnapshot({
  workspaceRoot,
  sourceDir,
  artifactNames,
  extraNames = [],
  receiptNames,
  snapshotDir,
  syncFile = syncRegularFile,
  syncDirectory: syncSnapshotDirectory = syncDirectory,
} = {}) {
  const root = realpathSync(workspaceRoot);
  const requestedRoot = resolve(workspaceRoot);
  const requested = resolve(snapshotDir ?? join(requestedRoot, 'desktop', 'dist', 'publication'));
  const requestedRelation = relative(requestedRoot, requested);
  if (
    requestedRelation === '' ||
    requestedRelation === '..' ||
    requestedRelation.startsWith(`..${sep}`)
  ) {
    throw new Error('publication snapshot escapes its validated parent');
  }
  const path = contained(root, join(root, requestedRelation), 'publication snapshot');
  if (existsSync(path)) throw new Error(`publication snapshot already exists: ${path}`);
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const artifacts = [];
  try {
    for (const name of artifactNames) {
      if (basename(name) !== name) throw new Error(`unsafe publication artifact name: ${name}`);
      artifacts.push(copyRegular(join(sourceDir, name), join(path, name), syncFile));
    }
    const receiptBoundArtifacts = [...artifacts];
    for (const name of extraNames) {
      if (basename(name) !== name) throw new Error(`unsafe publication extra name: ${name}`);
      if (artifactNames.includes(name)) throw new Error(`duplicate publication file name: ${name}`);
      artifacts.push(copyRegular(join(sourceDir, name), join(path, name), syncFile));
    }
    const byName = new Map(
      receiptBoundArtifacts.map((artifact) => [basename(artifact.path), artifact]),
    );
    const claims = new Map(receiptBoundArtifacts.map((artifact) => [basename(artifact.path), 0]));
    for (const name of receiptNames) {
      if (basename(name) !== name) throw new Error(`unsafe publication receipt name: ${name}`);
      const receipt = JSON.parse(readFileSync(join(sourceDir, name), 'utf8'));
      if (!Array.isArray(receipt.artifacts)) throw new Error(`${name} has no artifact evidence`);
      receipt.artifacts = receipt.artifacts.map((entry) => {
        const artifactName = basename(entry.path ?? '');
        const artifact = byName.get(artifactName);
        if (artifact === undefined) return entry;
        claims.set(artifactName, claims.get(artifactName) + 1);
        if (entry.sha256 !== artifact.sha256 || entry.size !== artifact.size) {
          throw new Error(`receipt digest does not match staged artifact: ${artifactName}`);
        }
        return { ...entry, path: artifact.path, sha256: artifact.sha256, size: artifact.size };
      });
      writeFileSync(join(path, name), `${JSON.stringify(receipt, null, 2)}\n`, { mode: 0o400 });
      syncFile(join(path, name));
    }
    for (const [name, count] of claims) {
      if (count !== 1) {
        throw new Error(
          `publication artifact ${name} has ${count} receipt claims, expected exactly 1`,
        );
      }
    }
    const snapshot = {
      schema_version: 1,
      path,
      artifacts: artifacts.map((artifact) => ({ ...artifact })),
      receipts: [...receiptNames],
      receipt_evidence: receiptNames.map((name) => readArtifactEvidence(join(path, name))),
    };
    const snapshotEvidencePath = join(path, 'publication-snapshot.json');
    writeFileSync(snapshotEvidencePath, `${JSON.stringify(snapshot, null, 2)}\n`, {
      mode: 0o400,
    });
    syncFile(snapshotEvidencePath);
    chmodSync(path, 0o500);
    syncSnapshotDirectory(path);
    syncSnapshotDirectory(dirname(path));
    return Object.freeze(snapshot);
  } catch (error) {
    // Preserve the failed snapshot as forensic evidence. Final inode-bound root cleanup owns it.
    throw error;
  }
}

/** Rehash every snapshotted artifact, failing if publication bytes drifted. */
export function verifyPublicationSnapshot(snapshot) {
  if (snapshot?.schema_version !== 1 || !Array.isArray(snapshot.artifacts)) {
    throw new Error('invalid publication snapshot evidence');
  }
  for (const expected of snapshot.artifacts) {
    const actual = readArtifactEvidence(expected.path);
    if (actual.sha256 !== expected.sha256 || actual.size !== expected.size) {
      throw new Error(`${basename(expected.path)} changed after snapshot creation`);
    }
  }
  if (!Array.isArray(snapshot.receipt_evidence)) {
    throw new Error('publication snapshot has no receipt evidence');
  }
  for (const expected of snapshot.receipt_evidence) {
    const actual = readArtifactEvidence(expected.path);
    if (actual.sha256 !== expected.sha256 || actual.size !== expected.size) {
      throw new Error(`${basename(expected.path)} changed after snapshot creation`);
    }
  }
  return snapshot;
}

/** Read snapshot evidence from an isolated release workspace. */
export function readPublicationSnapshot(workspaceRoot) {
  const path = join(workspaceRoot, 'desktop', 'dist', 'publication', 'publication-snapshot.json');
  return verifyPublicationSnapshot(JSON.parse(readFileSync(path, 'utf8')));
}
