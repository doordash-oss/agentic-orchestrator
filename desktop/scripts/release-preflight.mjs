// Capture and recheck the local release provenance before anything is published.
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { statfsSync } from 'node:fs';
import { dirname, join } from 'node:path';
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

/** Validate local release prerequisites and preserve the exact source provenance for later gates. */
export function runReleasePreflight({
  cwd = rootDir,
  platform = process.platform,
  git: gitCommand = git,
  freeBytes = statfsSync(cwd).bavail * statfsSync(cwd).bsize,
  dockerInfo = (directory) => executeDocker('docker', ['info'], directory),
  ensureImage = (image) => ensurePinnedImage({ image, cwd }),
  verifySigningKey = () => signingKeyMatchesTrustRoot(process.env),
  env = process.env,
  evidencePath = join(cwd, 'desktop', 'dist', 'release-preflight.json'),
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
  if (dockerInfo(cwd) === false) {
    throw new Error('release preflight: Docker daemon is unavailable');
  }
  ensureImage(LINUX_BUILDER_IMAGE);
  ensureImage(LINUX_ARM64_VERIFIER_IMAGE);

  const evidence = Object.freeze({
    schema_version: 1,
    tag: provenance.tag,
    commit: provenance.commit,
    platform,
    captured_at: new Date().toISOString(),
    images: [LINUX_BUILDER_IMAGE, LINUX_ARM64_VERIFIER_IMAGE],
  });
  mkdirSync(dirname(evidencePath), { recursive: true });
  try {
    const existing = JSON.parse(readFileSync(evidencePath, 'utf8'));
    if (existing.tag !== evidence.tag || existing.commit !== evidence.commit) {
      throw new Error('release preflight evidence already belongs to a different tag or commit');
    }
    return Object.freeze(existing);
  } catch (error) {
    if (error?.code !== 'ENOENT') throw error;
  }
  writeFileSync(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`, { flag: 'wx' });
  return evidence;
}

/** Fail before publication if the source no longer matches the preflight evidence. */
export function verifyReleaseProvenance({
  cwd = rootDir,
  git: gitCommand = git,
  evidence,
  evidencePath = join(cwd, 'desktop', 'dist', 'release-preflight.json'),
} = {}) {
  const expected = evidence ?? JSON.parse(readFileSync(evidencePath, 'utf8'));
  let actual;
  try {
    actual = capture({ cwd, gitCommand });
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

function main() {
  try {
    if (process.argv[2] === 'verify') {
      const evidence = verifyReleaseProvenance();
      console.log(`release provenance verified for ${evidence.tag} (${evidence.commit})`);
    } else if (process.argv[2] === undefined) {
      const evidence = runReleasePreflight();
      console.log(`release preflight passed for ${evidence.tag} (${evidence.commit})`);
    } else {
      throw new Error('usage: release-preflight.mjs [verify]');
    }
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
