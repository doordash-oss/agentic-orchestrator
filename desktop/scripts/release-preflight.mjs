// Capture and recheck the local release provenance before anything is published.
import { execFileSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import {
  closeSync,
  constants,
  existsSync,
  fchmodSync,
  fstatSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { homedir } from 'node:os';
import { statfsSync } from 'node:fs';
import { basename, dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  LINUX_ARM64_VERIFIER_IMAGE,
  LINUX_BUILDER_IMAGE,
  releaseVersionFromTag,
} from './lib/release-artifacts.mjs';
import {
  extractEmbeddedReleasePublicKey,
  privateKeyFromMaterial,
  publicKeyPem,
} from './lib/release-signing.mjs';
import { isMainModule } from './lib/main-entry.mjs';
import {
  createDetachedReleaseWorkspace,
  removeDetachedReleaseWorkspace,
} from './release-workspace.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const rootDir = dirname(desktopDir);
const MINIMUM_FREE_BYTES = 12 * 1024 ** 3;

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function executeDocker(command, args, cwd) {
  return execFileSync(command, args, { cwd, stdio: ['ignore', 'pipe', 'pipe'] });
}

function imageNotFound(error) {
  return /(?:no such image|no such object|image .* not found)/i.test(
    [error?.message, error?.stderr, error?.stdout].filter(Boolean).join('\n'),
  );
}

export function ensurePinnedImage({ image, cwd, execute = executeDocker }) {
  try {
    execute('docker', ['image', 'inspect', image], cwd);
  } catch (error) {
    if (!imageNotFound(error)) throw error;
    execute('docker', ['pull', image], cwd);
  }
}

function signingKeyMatchesTrustRoot(env) {
  let material = env.AGENTICO_RELEASE_SIGNING_KEY;
  if (typeof material !== 'string' || material.trim() === '') {
    const keyPath =
      env.AGENTICO_RELEASE_SIGNING_KEY_FILE ??
      join(homedir(), '.config', 'agentico-release', 'release-key.pem');
    if (!existsSync(keyPath)) return false;
    material = readFileSync(keyPath, 'utf8');
  }
  const key = privateKeyFromMaterial(material);
  const updates = readFileSync(join(desktopDir, 'src', 'main', 'updates.ts'), 'utf8');
  return publicKeyPem(key) === extractEmbeddedReleasePublicKey(updates);
}

function capture({ cwd, gitCommand }) {
  const status = gitCommand(cwd, 'status', '--porcelain', '--untracked-files=all');
  if (status !== '') throw new Error('release preflight: working tree is dirty');
  const tag = gitCommand(cwd, 'describe', '--tags', '--exact-match');
  releaseVersionFromTag(tag);
  const commit = gitCommand(cwd, 'rev-parse', 'HEAD');
  if (!/^[0-9a-f]{40}$/i.test(commit))
    throw new Error('release preflight: HEAD is not a full commit SHA');
  return { tag, commit };
}

export function evidencePathFor(cwd, gitCommand = git) {
  const path = gitCommand(cwd, 'rev-parse', '--git-path', 'agentico-release-preflight.json');
  return resolve(cwd, path);
}

export function validateReleaseEvidence(evidence) {
  if (evidence === null || typeof evidence !== 'object' || Array.isArray(evidence)) {
    throw new Error('release provenance evidence is not an object');
  }
  if (evidence.schema_version !== 3 || evidence.platform !== 'darwin') {
    throw new Error('release provenance evidence has an unsupported schema or platform');
  }
  releaseVersionFromTag(evidence.tag);
  if (!/^[0-9a-f]{40}$/i.test(evidence.commit ?? '')) {
    throw new Error('release provenance evidence has an invalid commit');
  }
  if (typeof evidence.captured_at !== 'string' || Number.isNaN(Date.parse(evidence.captured_at))) {
    throw new Error('release provenance evidence has an invalid timestamp');
  }
  if (
    JSON.stringify(evidence.images) !==
    JSON.stringify([LINUX_BUILDER_IMAGE, LINUX_ARM64_VERIFIER_IMAGE])
  ) {
    throw new Error('release provenance evidence does not contain the pinned release images');
  }
  if (!/^[0-9a-f]{8}-[0-9a-f-]{27}$/i.test(evidence.run_id ?? '')) {
    throw new Error('release provenance evidence has an invalid run_id');
  }
  if (
    typeof evidence.operator_root !== 'string' ||
    !evidence.operator_root.startsWith('/') ||
    typeof evidence.workspace_root !== 'string' ||
    !evidence.workspace_root.startsWith('/')
  ) {
    throw new Error('release provenance evidence has invalid workspace paths');
  }
  if (typeof evidence.evidence_path !== 'string' || !evidence.evidence_path.startsWith('/')) {
    throw new Error('release provenance evidence has invalid evidence path');
  }
  const token = evidence.workspace_token;
  if (
    token?.kind !== 'directory' ||
    token.path !== evidence.workspace_root ||
    !Number.isSafeInteger(token.dev) ||
    !Number.isSafeInteger(token.ino)
  ) {
    throw new Error('release provenance evidence has invalid workspace token');
  }
  return evidence;
}

function readEvidenceFile(path) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink()) {
    throw new Error('release provenance evidence is not a regular file');
  }
  return JSON.parse(readFileSync(path, 'utf8'));
}

export function writeReleaseEvidence(
  path,
  evidence,
  { randomId = randomUUID, renameFile = renameSync } = {},
) {
  const directory = dirname(path);
  mkdirSync(directory, { recursive: true });
  const temporary = join(directory, `.${basename(path)}.${randomId()}.tmp`);
  let created = false;
  let fd;
  try {
    fd = openSync(
      temporary,
      constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
      0o600,
    );
    created = true;
    writeFileSync(fd, `${JSON.stringify(evidence, null, 2)}\n`);
    fchmodSync(fd, 0o600);
    fsyncSync(fd);
    const owned = fstatSync(fd);
    closeSync(fd);
    fd = undefined;
    renameFile(temporary, path);
    const published = lstatSync(path);
    if (
      !published.isFile() ||
      published.isSymbolicLink() ||
      published.dev !== owned.dev ||
      published.ino !== owned.ino
    ) {
      throw new Error('release evidence temporary file changed before atomic publication');
    }
  } catch (error) {
    if (fd !== undefined) closeSync(fd);
    if (created) rmSync(temporary, { force: true });
    throw error;
  }
}

/** Validate local release prerequisites and preserve the exact source provenance for later gates. */
export function runReleasePreflight({
  cwd = rootDir,
  platform = process.platform,
  git: gitCommand = git,
  freeBytes = statfsSync(cwd).bavail * statfsSync(cwd).bsize,
  dockerInfo = (directory) => executeDocker('docker', ['info'], directory),
  ensureImage = (image) => ensurePinnedImage({ image, cwd }),
  goreleaserVersion = () => execFileSync('goreleaser', ['--version'], { encoding: 'utf8' }),
  verifySigningKey = () => signingKeyMatchesTrustRoot(process.env),
  env = process.env,
  evidencePath = evidencePathFor(cwd, gitCommand),
  createWorkspace = (options) => createDetachedReleaseWorkspace(options),
} = {}) {
  if (platform !== 'darwin') throw new Error('release preflight: releases must run on macOS');
  const provenance = capture({ cwd, gitCommand });
  if (freeBytes < MINIMUM_FREE_BYTES) {
    throw new Error('release preflight: at least 12 GiB of free disk space is required');
  }
  if (typeof env.GITHUB_TOKEN !== 'string' || env.GITHUB_TOKEN.trim() === '') {
    throw new Error('release preflight: GITHUB_TOKEN is required');
  }
  if (!verifySigningKey()) {
    throw new Error(
      'release preflight: Ed25519 signing key does not match the embedded updater trust root',
    );
  }
  const goreleaser = String(goreleaserVersion());
  const version = /v?(\d+)\.(\d+)/.exec(goreleaser);
  if (
    version === null ||
    Number(version[1]) < 2 ||
    (Number(version[1]) === 2 && Number(version[2]) < 10)
  ) {
    throw new Error('release preflight: GoReleaser v2.10 or later is required');
  }
  if (dockerInfo(cwd) === false) {
    throw new Error('release preflight: Docker daemon is unavailable');
  }
  ensureImage(LINUX_BUILDER_IMAGE);
  ensureImage(LINUX_ARM64_VERIFIER_IMAGE);

  const runId = randomUUID();
  const workspace = createWorkspace({ operatorRoot: cwd, commit: provenance.commit, runId });
  const evidence = Object.freeze({
    schema_version: 3,
    tag: provenance.tag,
    commit: provenance.commit,
    platform,
    captured_at: new Date().toISOString(),
    run_id: runId,
    images: [LINUX_BUILDER_IMAGE, LINUX_ARM64_VERIFIER_IMAGE],
    operator_root: resolve(cwd),
    workspace_root: workspace.path,
    workspace_token: workspace.token,
    evidence_path: resolve(evidencePath),
  });
  try {
    writeReleaseEvidence(evidencePath, evidence);
  } catch (error) {
    try {
      removeDetachedReleaseWorkspace(workspace);
    } catch {
      // Preserve the evidence-write failure.
    }
    throw error;
  }
  return evidence;
}

/** Fail before publication if the source no longer matches the preflight evidence. */
export function verifyReleaseProvenance({
  cwd = rootDir,
  git: gitCommand = git,
  evidence,
  evidencePath = evidencePathFor(cwd, gitCommand),
} = {}) {
  const expected = validateReleaseEvidence(evidence ?? readEvidenceFile(evidencePath));
  let actual;
  try {
    actual = capture({ cwd: expected.workspace_root, gitCommand });
  } catch (error) {
    if (error instanceof Error && error.message.includes('working tree is dirty')) {
      throw new Error('release provenance changed: working tree changed since preflight evidence');
    }
    throw error;
  }
  if (actual.commit !== expected.commit || actual.tag !== expected.tag) {
    throw new Error(
      'release provenance changed: HEAD or exact tag no longer matches preflight evidence',
    );
  }
  return expected;
}

export function verifyOperatorReleaseSubject({ evidence, git: gitCommand = git } = {}) {
  const expected = validateReleaseEvidence(evidence);
  const actual = capture({ cwd: expected.operator_root, gitCommand });
  if (actual.commit !== expected.commit || actual.tag !== expected.tag) {
    throw new Error('release resume subject no longer matches the captured operator tag and HEAD');
  }
  return expected;
}

export function readReleaseEvidence({
  cwd = rootDir,
  git: gitCommand = git,
  evidencePath = evidencePathFor(cwd, gitCommand),
} = {}) {
  return validateReleaseEvidence(readEvidenceFile(evidencePath));
}

export function cleanupReleaseWorkspace({
  cwd = rootDir,
  git: gitCommand = git,
  evidence,
  evidencePath = evidencePathFor(cwd, gitCommand),
  removeWorkspace = removeDetachedReleaseWorkspace,
  removeEvidence = true,
} = {}) {
  const trusted = validateReleaseEvidence(evidence ?? readEvidenceFile(evidencePath));
  removeWorkspace({
    operatorRoot: trusted.operator_root,
    commonDir: resolve(
      trusted.operator_root,
      gitCommand(trusted.operator_root, 'rev-parse', '--path-format=absolute', '--git-common-dir'),
    ),
    path: trusted.workspace_root,
    commit: trusted.commit,
    runId: trusted.run_id,
    token: trusted.workspace_token,
  });
  if (removeEvidence) {
    const path = resolve(trusted.evidence_path);
    const expectedPath = resolve(evidencePathFor(trusted.operator_root, gitCommand));
    if (path !== expectedPath) throw new Error('refusing to remove unexpected release evidence');
    if (existsSync(path)) {
      const stat = lstatSync(path);
      if (!stat.isFile() || stat.isSymbolicLink()) {
        throw new Error('refusing to remove replaced release evidence');
      }
      rmSync(path);
    }
  }
}

function main() {
  let evidence;
  try {
    if (process.argv[2] === 'verify') {
      evidence = verifyReleaseProvenance();
      console.log(`release provenance verified for ${evidence.tag} (${evidence.commit})`);
    } else if (process.argv[2] === undefined) {
      evidence = runReleasePreflight();
      console.log(`release preflight passed for ${evidence.tag} (${evidence.commit})`);
    } else {
      throw new Error('usage: release-preflight.mjs [verify]');
    }
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  } finally {
    if (process.argv[2] === undefined && evidence !== undefined) {
      cleanupReleaseWorkspace({ evidence });
    }
  }
}

if (isMainModule(import.meta.url)) main();
