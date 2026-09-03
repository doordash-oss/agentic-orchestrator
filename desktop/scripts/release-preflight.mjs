/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Capture and recheck the local release provenance before anything is published.
import { execFileSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
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
  compileReleaseCleanupHelper,
  removeDetachedReleaseWorkspace,
  revalidateWorkspaceToken,
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
  if (evidence.schema_version !== 4 || evidence.platform !== 'darwin') {
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
  if (
    typeof evidence.cleanup_helper?.path !== 'string' ||
    !evidence.cleanup_helper.path.startsWith('/') ||
    !/^[0-9a-f]{64}$/.test(evidence.cleanup_helper.sha256 ?? '') ||
    !Number.isSafeInteger(evidence.cleanup_helper.size) ||
    evidence.cleanup_helper.size <= 0
  ) {
    throw new Error('release provenance evidence has invalid cleanup helper evidence');
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
  if (evidence.evidence_sha256 !== undefined && !/^[0-9a-f]{64}$/.test(evidence.evidence_sha256)) {
    throw new Error('release provenance evidence has an invalid byte digest');
  }
  return evidence;
}

function readEvidenceFile(path, expectedDigest) {
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink()) {
    throw new Error('release provenance evidence is not a regular file');
  }
  const bytes = readFileSync(path);
  const digest = createHash('sha256').update(bytes).digest('hex');
  if (expectedDigest !== undefined && digest !== expectedDigest) {
    throw new Error('release provenance evidence digest changed during handoff');
  }
  return { ...JSON.parse(bytes.toString('utf8')), evidence_sha256: digest };
}

function fsyncDirectory(path) {
  const fd = openSync(path, constants.O_RDONLY);
  try {
    fsyncSync(fd);
  } finally {
    closeSync(fd);
  }
}

export function writeReleaseEvidence(
  path,
  evidence,
  { randomId = randomUUID, renameFile = renameSync, syncDirectory = fsyncDirectory } = {},
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
    const bytes = Buffer.from(`${JSON.stringify(evidence, null, 2)}\n`, 'utf8');
    writeFileSync(fd, bytes);
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
    syncDirectory(directory);
    return createHash('sha256').update(bytes).digest('hex');
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
  compileCleanupHelper = (options) => compileReleaseCleanupHelper(options),
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
  let cleanupHelper;
  try {
    cleanupHelper = compileCleanupHelper({ workspace });
    const coreEvidence = Object.freeze({
      schema_version: 4,
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
      cleanup_helper: cleanupHelper,
    });
    const evidenceSha256 = writeReleaseEvidence(evidencePath, coreEvidence);
    return Object.freeze({ ...coreEvidence, evidence_sha256: evidenceSha256 });
  } catch (error) {
    if (cleanupHelper !== undefined) {
      try {
        removeDetachedReleaseWorkspace({ ...workspace, cleanupHelper });
      } catch {
        // Preserve the primary preflight failure and leave only captured evidence for inspection.
      }
    }
    throw error;
  }
}

/** Fail before publication if the source no longer matches the preflight evidence. */
export function verifyReleaseProvenance({
  cwd = rootDir,
  git: gitCommand = git,
  evidence,
  evidencePath = evidencePathFor(cwd, gitCommand),
} = {}) {
  const expected = validateReleaseEvidence(evidence ?? readEvidenceFile(evidencePath));
  revalidateWorkspaceToken(expected.workspace_token);
  if (expected.evidence_sha256 !== undefined) {
    const persisted = validateReleaseEvidence(
      readEvidenceFile(expected.evidence_path, expected.evidence_sha256),
    );
    if (JSON.stringify(persisted) !== JSON.stringify(expected)) {
      throw new Error('release provenance evidence changed after preflight handoff');
    }
  }
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
  expectedDigest,
} = {}) {
  return validateReleaseEvidence(readEvidenceFile(evidencePath, expectedDigest));
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
  if (trusted.evidence_sha256 !== undefined) {
    const persisted = validateReleaseEvidence(
      readEvidenceFile(trusted.evidence_path, trusted.evidence_sha256),
    );
    if (JSON.stringify(persisted) !== JSON.stringify(trusted)) {
      throw new Error('refusing cleanup because release evidence changed after handoff');
    }
  }
  const commonDir = resolve(
    trusted.operator_root,
    gitCommand(trusted.operator_root, 'rev-parse', '--path-format=absolute', '--git-common-dir'),
  );
  const expectedHelper = join(
    commonDir,
    'agentico-release-cleanup-helpers',
    trusted.run_id,
    'release-cleanup',
  );
  if (resolve(trusted.cleanup_helper.path) !== resolve(expectedHelper)) {
    throw new Error('refusing cleanup with an unexpected cleanup helper path');
  }
  removeWorkspace({
    operatorRoot: trusted.operator_root,
    commonDir,
    path: trusted.workspace_root,
    commit: trusted.commit,
    runId: trusted.run_id,
    token: trusted.workspace_token,
    cleanupHelper: trusted.cleanup_helper,
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
