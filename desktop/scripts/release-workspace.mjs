// Isolated committed-source workspace and immutable publication snapshot helpers.
import { execFileSync } from 'node:child_process';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { basename, join, relative, resolve, sep } from 'node:path';

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

const RELEASE_SECRET_NAMES = new Set([
  'GITHUB_TOKEN',
  'GH_TOKEN',
  'GH_ENTERPRISE_TOKEN',
  'GITHUB_ENTERPRISE_TOKEN',
  'HOMEBREW_GITHUB_API_TOKEN',
  'AGENTICO_RELEASE_SIGNING_KEY',
  'AGENTICO_RELEASE_SIGNING_KEY_FILE',
  'AGENTICO_RELEASE_NOTES_FILE',
]);

/** Build subprocesses get dependency auth but never publication/signing credentials. */
export function secretFreeBuildEnvironment(env, evidencePath) {
  const clean = cleanGoEnvironment(env);
  for (const name of RELEASE_SECRET_NAMES) delete clean[name];
  for (const name of Object.keys(clean)) {
    if (name.startsWith('GORELEASER_')) delete clean[name];
  }
  clean.AGENTICO_RELEASE_EVIDENCE_FILE = evidencePath;
  return clean;
}

/** Publication retains required credentials while rejecting ambient GoReleaser overrides. */
export function goreleaserEnvironment(env, tag) {
  const clean = cleanGoEnvironment(env);
  for (const name of Object.keys(clean)) {
    if (name.startsWith('GORELEASER_')) delete clean[name];
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
  const actual = gitCommand(path, 'rev-parse', 'HEAD');
  const status = gitCommand(path, 'status', '--porcelain', '--untracked-files=all');
  if (actual !== commit || status !== '') {
    try {
      gitCommand(operatorRoot, 'worktree', 'remove', '--force', path);
    } catch {
      // Preserve the primary provenance failure; recovery can prune the validated Git path.
    }
    throw new Error('detached release workspace does not exactly match captured committed source');
  }
  const token = captureWorkspaceToken(path);
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
export function removeDetachedReleaseWorkspace(workspace, { gitCommand = git } = {}) {
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
  if (existsSync(workspace.path)) {
    const snapshot = join(workspace.path, 'desktop', 'dist', 'publication');
    if (existsSync(snapshot)) {
      const stat = lstatSync(snapshot);
      if (stat.isDirectory() && !stat.isSymbolicLink()) chmodSync(snapshot, 0o700);
    }
    gitCommand(workspace.operatorRoot, 'worktree', 'remove', '--force', workspace.path);
  }
}

function copyRegular(source, destination) {
  const evidence = readArtifactEvidence(source);
  copyFileSync(evidence.path, destination);
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
  receiptNames,
  snapshotDir,
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
      artifacts.push(copyRegular(join(sourceDir, name), join(path, name)));
    }
    const byName = new Map(artifacts.map((artifact) => [basename(artifact.path), artifact]));
    const claims = new Map(artifacts.map((artifact) => [basename(artifact.path), 0]));
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
    writeFileSync(
      join(path, 'publication-snapshot.json'),
      `${JSON.stringify(snapshot, null, 2)}\n`,
      {
        mode: 0o400,
      },
    );
    for (const artifact of artifacts) chmodSync(artifact.path, 0o400);
    chmodSync(path, 0o500);
    return Object.freeze(snapshot);
  } catch (error) {
    try {
      chmodSync(path, 0o700);
      rmSync(path, { recursive: true, force: true });
    } catch {
      // Preserve the primary staging failure.
    }
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
