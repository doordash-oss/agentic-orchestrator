// Gate desktop release packages before publishing and their signed checksums after publishing.
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  expectedDesktopArtifacts,
  validateArtifactInventory,
  validateChecksumManifest,
} from './lib/release-artifacts.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const repoRoot = dirname(desktopDir);
const DESKTOP_DIST = join(desktopDir, 'dist');
const RECEIPTS = Object.freeze([
  'package-verification-darwin-universal.json',
  'package-verification-linux-x64.json',
  'package-verification-linux-arm64.json',
]);

/**
 * Verify a complete desktop package inventory or the signed post-release
 * checksum manifest. Returns evidence even for failures; callers must inspect
 * `ok` and exit nonzero when it is false.
 */
export function verifyReleaseArtifacts({
  mode,
  linuxOnly = false,
  tag,
  revision,
  desktopDist = DESKTOP_DIST,
  checksumsPath = join(repoRoot, 'dist', 'checksums.txt'),
  runSignatureVerification = verifySignature,
}) {
  const errors = [];
  if (mode === 'packages') {
    errors.push(
      ...validateArtifactInventory({
        tag,
        revision,
        files: readDistFiles(desktopDist, errors),
        sizes: readDistSizes(desktopDist, errors),
        receipts: readReceipts(desktopDist, linuxOnly, errors),
        linuxOnly,
      }),
    );
  } else if (mode === 'manifest') {
    let expected = [];
    try {
      expected = expectedDesktopArtifacts(tag).map(({ name }) => name);
    } catch (error) {
      errors.push(errorMessage(error));
    }
    if (!existsSync(checksumsPath)) {
      errors.push(`missing checksum manifest: ${checksumsPath}`);
    } else {
      try {
        errors.push(...validateChecksumManifest(readFileSync(checksumsPath, 'utf8'), expected));
      } catch (error) {
        errors.push(`could not read checksum manifest ${checksumsPath}: ${errorMessage(error)}`);
      }
      try {
        runSignatureVerification(checksumsPath);
      } catch (error) {
        errors.push(`checksums signature verification failed: ${errorMessage(error)}`);
      }
    }
  } else {
    errors.push(`unknown verification mode: ${mode}`);
  }

  const evidence = {
    verified_at: new Date().toISOString(),
    mode,
    linux_only: linuxOnly,
    tag,
    revision,
    ok: errors.length === 0,
    errors,
  };
  writeEvidence(desktopDist, evidence);
  return evidence;
}

function readDistFiles(distDir, errors) {
  try {
    return readdirSync(distDir);
  } catch (error) {
    errors.push(`could not list desktop dist directory ${distDir}: ${errorMessage(error)}`);
    return [];
  }
}

function readDistSizes(distDir, errors) {
  const sizes = {};
  for (const name of readDistFiles(distDir, errors)) {
    try {
      sizes[name] = statSync(join(distDir, name)).size;
    } catch (error) {
      errors.push(`could not stat desktop artifact ${name}: ${errorMessage(error)}`);
    }
  }
  return sizes;
}

function readReceipts(distDir, linuxOnly, errors) {
  const receipts = {};
  for (const name of RECEIPTS.filter((receipt) => !linuxOnly || receipt.includes('-linux-'))) {
    const path = join(distDir, name);
    if (!existsSync(path)) continue;
    try {
      receipts[name] = JSON.parse(readFileSync(path, 'utf8'));
    } catch (error) {
      errors.push(`could not parse verification receipt ${name}: ${errorMessage(error)}`);
    }
  }
  return receipts;
}

function verifySignature(checksumsPath) {
  execFileSync(
    process.execPath,
    [join(desktopDir, 'scripts', 'release-sign.mjs'), 'verify', checksumsPath],
    {
      stdio: 'pipe',
    },
  );
}

function writeEvidence(distDir, evidence) {
  mkdirSync(distDir, { recursive: true });
  writeFileSync(
    join(distDir, 'release-artifact-verification.json'),
    `${JSON.stringify(evidence, null, 2)}\n`,
  );
}

function errorMessage(error) {
  const stderr = error?.stderr?.toString().trim();
  return stderr || (error instanceof Error ? error.message : String(error));
}

function git(...args) {
  return execFileSync('git', args, { cwd: repoRoot, encoding: 'utf8' }).trim();
}

function main() {
  const [mode, ...flags] = process.argv.slice(2);
  const linuxOnly = flags.length === 1 && flags[0] === '--linux-only';
  if (
    !['packages', 'manifest'].includes(mode) ||
    (flags.length > 0 && !linuxOnly) ||
    (linuxOnly && mode !== 'packages')
  ) {
    console.error('usage: verify-release-artifacts.mjs packages [--linux-only]|manifest');
    process.exit(1);
  }

  let tag;
  let revision;
  try {
    tag = git('describe', '--tags', '--exact-match');
    revision = git('rev-parse', 'HEAD');
  } catch (error) {
    const evidence = {
      verified_at: new Date().toISOString(),
      mode,
      linux_only: linuxOnly,
      ok: false,
      errors: [`could not resolve exact release tag and revision: ${errorMessage(error)}`],
    };
    writeEvidence(DESKTOP_DIST, evidence);
    console.error(`verify-release-artifacts: FAIL:\n  ${evidence.errors.join('\n  ')}`);
    process.exit(1);
  }

  const evidence = verifyReleaseArtifacts({ mode, linuxOnly, tag, revision });
  if (!evidence.ok) {
    console.error(`verify-release-artifacts: FAIL:\n  ${evidence.errors.join('\n  ')}`);
    process.exit(1);
  }
  console.log(`verify-release-artifacts: ${mode} OK`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
