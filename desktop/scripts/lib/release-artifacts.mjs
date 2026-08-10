// Pure release artifact and Docker-build contract shared by local release scripts.
import { basename, resolve } from 'node:path';

export const LINUX_BUILDER_IMAGE =
  'electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9';

const RELEASE_TAG = /^v(\d+\.\d+\.\d+)$/;
const LINUX_ARCHITECTURES = new Set(['x64', 'arm64']);

/** Return the package version encoded by an exact release tag. */
export function releaseVersionFromTag(tag) {
  const match = typeof tag === 'string' ? RELEASE_TAG.exec(tag) : null;
  if (match === null) {
    throw new Error(`invalid release tag: ${tag}`);
  }
  return match[1];
}

/** Return the complete, platform-specific desktop release inventory. */
export function expectedDesktopArtifacts(tag) {
  const version = releaseVersionFromTag(tag);
  return Object.freeze([
    Object.freeze({
      name: 'Agentico-mac-universal.dmg',
      os: 'darwin',
      arch: 'universal',
      format: 'dmg',
    }),
    Object.freeze({ name: 'Agentico-x64.AppImage', os: 'linux', arch: 'x64', format: 'AppImage' }),
    Object.freeze({
      name: 'Agentico-arm64.AppImage',
      os: 'linux',
      arch: 'arm64',
      format: 'AppImage',
    }),
    Object.freeze({
      name: `agentico_${version}_amd64.deb`,
      os: 'linux',
      arch: 'x64',
      format: 'deb',
    }),
    Object.freeze({
      name: `agentico_${version}_arm64.deb`,
      os: 'linux',
      arch: 'arm64',
      format: 'deb',
    }),
  ]);
}

/**
 * Validate every desktop distributable and its target-specific package receipt.
 * Returns all observed failures so the release operator can repair a whole
 * incomplete inventory in one pass.
 */
export function validateArtifactInventory({
  tag,
  revision,
  files,
  receipts,
  sizes,
  linuxOnly = false,
}) {
  const errors = [];
  let artifacts;
  try {
    artifacts = expectedDesktopArtifacts(tag).filter(({ os }) => !linuxOnly || os === 'linux');
  } catch (error) {
    return [error instanceof Error ? error.message : String(error)];
  }

  const names = Array.isArray(files) ? files : [];
  const sizeByName = sizes !== null && typeof sizes === 'object' ? sizes : {};
  const receiptsByName = receipts !== null && typeof receipts === 'object' ? receipts : {};
  const version = releaseVersionFromTag(tag);

  for (const { name } of artifacts) {
    const count = names.filter((file) => file === name).length;
    if (count === 0) errors.push(`missing desktop artifact: ${name}`);
    else if (count > 1) errors.push(`duplicate desktop artifact: ${name}`);
    if (count > 0 && (!Number.isFinite(sizeByName[name]) || sizeByName[name] <= 0)) {
      errors.push(`desktop artifact is empty: ${name}`);
    }
  }

  for (const name of names) {
    const match = /^agentico_(.+)_(amd64|arm64)\.deb$/.exec(name);
    if (match !== null && match[1] !== version) {
      errors.push(`unexpected desktop package version: ${name} (expected ${version})`);
    }
  }

  for (const target of receiptTargets(artifacts)) {
    validateReceipt({ receiptsByName, target, tag, revision, errors });
  }
  return errors;
}

/**
 * Ensure each expected desktop filename appears exactly once with a valid
 * SHA-256 digest. Other release checksums (for CLI archives) are intentionally
 * ignored; this gate owns only the desktop additions.
 */
export function validateChecksumManifest(text, expectedNames) {
  const errors = [];
  const expected = new Set(expectedNames);
  const entries = new Map([...expected].map((name) => [name, 0]));
  const lines = typeof text === 'string' ? text.split(/\r?\n/) : [];

  for (const line of lines) {
    if (line.trim() === '') continue;
    const fields = /^(\S+)\s+\*?(.+?)\s*$/.exec(line);
    if (fields === null) continue;
    const [, hash, name] = fields;
    if (!expected.has(name)) continue;
    entries.set(name, entries.get(name) + 1);
    if (!/^[0-9a-f]{64}$/i.test(hash)) {
      errors.push(`checksums.txt has malformed SHA-256 for ${name}`);
    }
  }

  for (const [name, count] of entries) {
    if (count === 0) errors.push(`checksums.txt has no entry for ${name}`);
    else if (count !== 1) {
      errors.push(`checksums.txt has ${count} entries for ${name}, expected exactly 1`);
    }
  }
  return errors;
}

/** Resolve the package target independently from the process running verification. */
export function resolvePackageTarget(platform, processArch, packageArch) {
  if (platform === 'darwin') {
    if (packageArch !== undefined) {
      throw new Error(`unsupported package architecture: ${packageArch}`);
    }
    return Object.freeze({ os: 'darwin', arch: 'universal' });
  }
  if (platform !== 'linux') {
    throw new Error(`unsupported package platform: ${platform}`);
  }

  const arch = packageArch ?? processArch;
  if (!LINUX_ARCHITECTURES.has(arch)) {
    throw new Error(`unsupported package architecture: ${arch}`);
  }
  return Object.freeze({ os: 'linux', arch });
}

/** Select the one distributable matching a package target and format. */
export function selectPackageArtifact(files, target, format) {
  const matches = files.filter((file) => matchesArtifact(file, target, format));
  if (matches.length !== 1) {
    throw new Error(
      `expected exactly one ${target.arch} ${format} artifact, found ${matches.length}`,
    );
  }
  return matches[0];
}

/** Create the ordered Docker invocations for Linux x64 and arm64 packaging. */
export function createLinuxDockerPlan({ repoRoot, gitCommonDir, volumePrefix }) {
  const command = ['bash', '-lc', 'npm ci && npm run package:verify --workspace desktop'];
  return Object.freeze(
    ['x64', 'arm64'].map((arch) =>
      Object.freeze({
        arch,
        args: Object.freeze([
          'run',
          '--rm',
          '--platform',
          'linux/amd64',
          '-e',
          `AGENTICO_PACKAGE_ARCH=${arch}`,
          '-v',
          `${repoRoot}:${repoRoot}`,
          '-v',
          `${gitCommonDir}:${gitCommonDir}`,
          '-v',
          `${volumePrefix}-node-modules:/repo/node_modules`,
          '-v',
          `${volumePrefix}-electron:/root/.cache/electron`,
          '-v',
          `${volumePrefix}-electron-builder:/root/.cache/electron-builder`,
          '--workdir',
          repoRoot,
          LINUX_BUILDER_IMAGE,
          ...command,
        ]),
      }),
    ),
  );
}

function matchesArtifact(file, target, format) {
  if (target.os === 'darwin' && target.arch === 'universal' && format === 'dmg') {
    return file === 'Agentico-mac-universal.dmg';
  }
  if (target.os !== 'linux') return false;
  if (format === 'AppImage') {
    return file === `Agentico-${target.arch}.AppImage`;
  }
  if (format === 'deb') {
    const debArch = target.arch === 'x64' ? 'amd64' : target.arch;
    return new RegExp(`^agentico_.+_${debArch}\\.deb$`).test(file);
  }
  return false;
}

function receiptTargets(artifacts) {
  const targets = new Map();
  for (const artifact of artifacts) {
    const key = `${artifact.os}-${artifact.arch}`;
    const existing = targets.get(key) ?? {
      os: artifact.os,
      arch: artifact.arch,
      receipt: `package-verification-${artifact.os}-${artifact.arch}.json`,
      artifacts: [],
    };
    existing.artifacts.push(artifact);
    targets.set(key, existing);
  }
  return targets.values();
}

function validateReceipt({ receiptsByName, target, tag, revision, errors }) {
  const receipt = receiptsByName[target.receipt];
  if (receipt === undefined) {
    errors.push(`missing verification receipt: ${target.receipt}`);
    return;
  }
  if (receipt === null || typeof receipt !== 'object' || Array.isArray(receipt)) {
    errors.push(`verification receipt is not a JSON object: ${target.receipt}`);
    return;
  }

  const identity = receipt.identity;
  if (identity === null || typeof identity !== 'object' || Array.isArray(identity)) {
    errors.push(`${target.receipt} has no build identity`);
  } else {
    const expected = {
      desktop_version: releaseVersionFromTag(tag),
      server_version: tag,
      server_revision: revision,
      os: target.os,
      arch: target.arch,
    };
    for (const [field, value] of Object.entries(expected)) {
      if (identity[field] !== value) {
        errors.push(`${target.receipt} identity ${field}=${identity[field]}, expected ${value}`);
      }
    }
  }

  const verified = Array.isArray(receipt.artifacts) ? receipt.artifacts : [];
  for (const expectedArtifact of target.artifacts) {
    const matches = verified.filter((artifact) => artifact?.target === expectedArtifact.format);
    if (matches.length === 0) {
      errors.push(`${target.receipt} does not verify ${expectedArtifact.format}`);
      continue;
    }
    if (matches.length !== 1) {
      errors.push(
        `${target.receipt} has ${matches.length} ${expectedArtifact.format} verification entries, ` +
          'expected exactly 1',
      );
      continue;
    }
    const path = matches[0].path;
    if (typeof path !== 'string' || basename(resolve(path)) !== expectedArtifact.name) {
      errors.push(
        `${target.receipt} ${expectedArtifact.format} path=${path}, ` +
          `expected basename ${expectedArtifact.name}`,
      );
    }
  }
}
