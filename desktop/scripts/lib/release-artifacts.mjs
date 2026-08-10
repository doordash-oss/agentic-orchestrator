// Pure release artifact and Docker-build contract shared by local release scripts.
import { createHash } from 'node:crypto';
import { lstatSync, readFileSync, realpathSync } from 'node:fs';
import { resolve } from 'node:path';

export const LINUX_BUILDER_IMAGE =
  'electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9';
export const LINUX_ARM64_VERIFIER_IMAGE =
  'node:22.22.2-bookworm@sha256:62e4daa6819762bbd3072af77cc282ab72c631c4aed30dd7980192babaf385b3';

const GO_LINUX_TARBALLS = Object.freeze({
  amd64: Object.freeze({
    name: 'go1.25.0.linux-amd64.tar.gz',
    sha256: '2852af0cb20a13139b3448992e69b868e50ed0f8a1e5940ee1de9e19a123b613',
  }),
  arm64: Object.freeze({
    name: 'go1.25.0.linux-arm64.tar.gz',
    sha256: '05de75d6994a2783699815ee553bd5a9327d8b79991de36e38b66862782f54ae',
  }),
});
const RELEASE_TAG = /^v(\d+\.\d+\.\d+)$/;
const LINUX_ARCHITECTURES = new Set(['x64', 'arm64']);
export const PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION = 2;

/** Build the sole schema-v2 target receipt used by package verification and release fixtures. */
export function createPackageVerificationReceipt({
  target,
  artifacts,
  unpackedApp,
  verifiedAt,
  host,
}) {
  if (!isTarget(target)) throw new Error('package verification receipt requires a target');
  if (!Array.isArray(artifacts)) throw new Error('package verification receipt requires artifacts');
  const identity = summaryIdentity(artifacts);
  return {
    schema_version: PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION,
    verified_at: verifiedAt ?? new Date().toISOString(),
    host: host === undefined ? undefined : { ...host },
    target: { ...target },
    artifacts: artifacts.map((artifact) => ({
      target: { ...artifact.target },
      format: artifact.format,
      path: artifact.path,
      sha256: artifact.sha256,
      size: artifact.size,
      identity: { ...artifact.identity },
    })),
    unpacked_app: unpackedApp,
    // Packaged E2E and transcript helpers consume this compatibility summary;
    // publication gates validate the independent artifact identities above.
    identity,
  };
}

function summaryIdentity(artifacts) {
  const first = artifacts[0]?.identity;
  if (first === null || typeof first !== 'object' || Array.isArray(first)) {
    throw new Error('cannot derive summary identity without an artifact identity');
  }
  const serialized = JSON.stringify(first);
  if (!artifacts.every((artifact) => JSON.stringify(artifact?.identity) === serialized)) {
    throw new Error('cannot derive summary identity: artifact identities disagree');
  }
  return { ...first };
}

/** Read a regular, non-symlinked artifact and bind its canonical path, digest, and size. */
export function readArtifactEvidence(path) {
  const requestedPath = resolve(path);
  const before = lstatSync(requestedPath);
  if (!before.isFile() || before.isSymbolicLink()) {
    throw new Error(`artifact is not a regular file: ${requestedPath}`);
  }
  const canonicalPath = realpathSync(requestedPath);
  const bytes = readFileSync(canonicalPath);
  const after = lstatSync(canonicalPath);
  if (
    !after.isFile() ||
    after.isSymbolicLink() ||
    before.dev !== after.dev ||
    before.ino !== after.ino ||
    before.size !== after.size ||
    before.mtimeMs !== after.mtimeMs
  ) {
    throw new Error(`artifact changed while reading: ${canonicalPath}`);
  }
  return Object.freeze({
    path: canonicalPath,
    sha256: createHash('sha256').update(bytes).digest('hex'),
    size: bytes.length,
  });
}

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
  artifactDir,
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
    validateReceipt({ receiptsByName, target, tag, revision, artifactDir, errors });
  }
  return errors;
}

/**
 * Ensure each expected desktop filename appears exactly once with a valid
 * SHA-256 digest. Other release checksums (for CLI archives) are intentionally
 * ignored; this gate owns only the desktop additions.
 */
export function validateChecksumManifest(text, expectedNames, expectedDigests = {}) {
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
    } else if (
      expectedDigests[name] !== undefined &&
      hash.toLowerCase() !== expectedDigests[name]
    ) {
      errors.push(
        `checksums.txt SHA-256 for ${name}=${hash.toLowerCase()}, expected receipt ${expectedDigests[name]}`,
      );
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

/** Extract the exact release artifact digest that each valid target receipt attests to. */
export function receiptArtifactDigests({ tag, revision, receipts, linuxOnly = false }) {
  const errors = [];
  const digests = {};
  let artifacts;
  try {
    artifacts = expectedDesktopArtifacts(tag).filter(({ os }) => !linuxOnly || os === 'linux');
  } catch (error) {
    return { errors: [error instanceof Error ? error.message : String(error)], digests };
  }
  const receiptsByName = receipts !== null && typeof receipts === 'object' ? receipts : {};
  for (const target of receiptTargets(artifacts)) {
    const receipt = receiptsByName[target.receipt];
    if (receipt === undefined || receipt === null || typeof receipt !== 'object') {
      errors.push(`missing valid verification receipt: ${target.receipt}`);
      continue;
    }
    if (receipt.schema_version !== PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION) {
      errors.push(
        `${target.receipt} schema_version=${receipt.schema_version}, expected ${PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION}`,
      );
    }
    if (receipt.target?.os !== target.os || receipt.target?.arch !== target.arch) {
      errors.push(
        `${target.receipt} target=${formatTarget(receipt.target)}, expected ${target.os}/${target.arch}`,
      );
    }
    const verified = Array.isArray(receipt.artifacts) ? receipt.artifacts : [];
    for (const expectedArtifact of target.artifacts) {
      const matches = verified.filter((artifact) => artifact?.format === expectedArtifact.format);
      if (matches.length !== 1) {
        errors.push(
          `${target.receipt} must contain exactly one ${expectedArtifact.format} receipt entry`,
        );
        continue;
      }
      const artifact = matches[0];
      if (artifact.target?.os !== target.os || artifact.target?.arch !== target.arch) {
        errors.push(
          `${target.receipt} ${expectedArtifact.format} target=${formatTarget(artifact.target)}, expected ${target.os}/${target.arch}`,
        );
      }
      if (!/^[0-9a-f]{64}$/i.test(artifact.sha256 ?? '')) {
        errors.push(`${target.receipt} ${expectedArtifact.format} has invalid SHA-256`);
      } else {
        digests[expectedArtifact.name] = artifact.sha256.toLowerCase();
      }
      validateArtifactIdentity({
        identity: artifact.identity,
        target,
        tag,
        revision,
        prefix: `${target.receipt} ${expectedArtifact.format} identity`,
        errors,
      });
    }
  }
  return { errors, digests };
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
  const mounts = [
    '-v',
    `${repoRoot}:${repoRoot}:ro`,
    '-v',
    `${gitCommonDir}:${gitCommonDir}:ro`,
    '-v',
    `${repoRoot}/desktop/dist:${repoRoot}/desktop/dist`,
    '-v',
    `${repoRoot}/desktop/out:${repoRoot}/desktop/out`,
    '-v',
    `${repoRoot}/desktop/resources:${repoRoot}/desktop/resources`,
    '-v',
    `${repoRoot}/node_modules:${repoRoot}/node_modules`,
    '-v',
    `${volumePrefix}-electron:/root/.cache/electron`,
    '-v',
    `${volumePrefix}-electron-builder:/root/.cache/electron-builder`,
    '--workdir',
    repoRoot,
  ];
  return Object.freeze(
    ['x64', 'arm64'].map((arch) => {
      const builderCommand = [...goBootstrapCommand('amd64'), 'npm ci'];
      builderCommand.push(
        arch === 'arm64'
          ? 'npm run package:build --workspace desktop'
          : 'npm run package:verify --workspace desktop',
      );
      const invocation = {
        arch,
        args: Object.freeze([
          'run',
          '--rm',
          '--platform',
          'linux/amd64',
          '-e',
          `AGENTICO_PACKAGE_ARCH=${arch}`,
          ...mounts,
          LINUX_BUILDER_IMAGE,
          'bash',
          '-lc',
          builderCommand.join(' && '),
        ]),
      };
      if (arch === 'arm64') {
        invocation.verificationArgs = Object.freeze([
          'run',
          '--rm',
          '--platform',
          'linux/arm64',
          '-e',
          'AGENTICO_PACKAGE_ARCH=arm64',
          ...mounts,
          LINUX_ARM64_VERIFIER_IMAGE,
          'bash',
          '-lc',
          [...goBootstrapCommand('arm64'), 'node desktop/scripts/verify-package.mjs'].join(' && '),
        ]);
      }
      return Object.freeze(invocation);
    }),
  );
}

function goBootstrapCommand(arch) {
  const toolchain = GO_LINUX_TARBALLS[arch];
  if (toolchain === undefined) throw new Error(`unsupported Go toolchain architecture: ${arch}`);
  return [
    'set -euo pipefail',
    `go_tarball=/tmp/${toolchain.name}`,
    `curl --fail --location --retry 3 --output \"$go_tarball\" https://go.dev/dl/${toolchain.name}`,
    `echo \"${toolchain.sha256}  $go_tarball\" | sha256sum --check --status`,
    'rm -rf /usr/local/go',
    'tar -C /usr/local -xzf \"$go_tarball\"',
    'rm -f \"$go_tarball\"',
    'export PATH=/usr/local/go/bin:$PATH',
    'go version',
  ];
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

function validateReceipt({ receiptsByName, target, tag, revision, artifactDir, errors }) {
  const receipt = receiptsByName[target.receipt];
  if (receipt === undefined) {
    errors.push(`missing verification receipt: ${target.receipt}`);
    return;
  }
  if (receipt === null || typeof receipt !== 'object' || Array.isArray(receipt)) {
    errors.push(`verification receipt is not a JSON object: ${target.receipt}`);
    return;
  }

  if (receipt.schema_version !== PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION) {
    errors.push(
      `${target.receipt} schema_version=${receipt.schema_version}, expected ${PACKAGE_VERIFICATION_RECEIPT_SCHEMA_VERSION}`,
    );
  }
  if (receipt.target?.os !== target.os || receipt.target?.arch !== target.arch) {
    errors.push(
      `${target.receipt} target=${formatTarget(receipt.target)}, expected ${target.os}/${target.arch}`,
    );
  }

  const verified = Array.isArray(receipt.artifacts) ? receipt.artifacts : [];
  if (verified.length !== target.artifacts.length) {
    errors.push(
      `${target.receipt} has ${verified.length} artifact verification entries, expected ${target.artifacts.length}`,
    );
  }
  for (const artifact of verified) {
    if (
      !target.artifacts.some((expectedArtifact) => expectedArtifact.format === artifact?.format)
    ) {
      errors.push(
        `${target.receipt} has unexpected artifact format ${artifact?.format ?? '(missing)'}`,
      );
    }
  }
  for (const expectedArtifact of target.artifacts) {
    const matches = verified.filter((artifact) => artifact?.format === expectedArtifact.format);
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
    const artifact = matches[0];
    const path = artifact.path;
    const expectedPath =
      typeof artifactDir === 'string' ? resolve(artifactDir, expectedArtifact.name) : null;
    let expectedEvidence;
    let actualEvidence;
    try {
      expectedEvidence = expectedPath === null ? null : readArtifactEvidence(expectedPath);
      actualEvidence = typeof path === 'string' ? readArtifactEvidence(path) : null;
    } catch (error) {
      errors.push(
        `${target.receipt} could not hash ${expectedArtifact.format} at ${path ?? expectedPath}: ` +
          `${error instanceof Error ? error.message : String(error)}`,
      );
      continue;
    }
    if (expectedPath === null) {
      errors.push(
        `${target.receipt} cannot bind ${expectedArtifact.format}: no artifact directory`,
      );
    } else if (actualEvidence?.path !== expectedEvidence?.path) {
      errors.push(
        `${target.receipt} ${expectedArtifact.format} path=${path}, expected ${expectedEvidence.path}`,
      );
    }
    if (artifact.target?.os !== target.os || artifact.target?.arch !== target.arch) {
      errors.push(
        `${target.receipt} ${expectedArtifact.format} target=${formatTarget(artifact.target)}, ` +
          `expected ${target.os}/${target.arch}`,
      );
    }
    if (expectedEvidence !== null && actualEvidence?.path === expectedEvidence.path) {
      if (artifact.sha256 !== expectedEvidence.sha256) {
        errors.push(
          `${target.receipt} ${expectedArtifact.format} SHA-256=${artifact.sha256}, expected ${expectedEvidence.sha256}`,
        );
      }
      if (artifact.size !== expectedEvidence.size) {
        errors.push(
          `${target.receipt} ${expectedArtifact.format} size=${artifact.size}, expected ${expectedEvidence.size}`,
        );
      }
    }
    validateArtifactIdentity({
      identity: artifact.identity,
      target,
      tag,
      revision,
      prefix: `${target.receipt} ${expectedArtifact.format} identity`,
      errors,
    });
  }
}

function validateArtifactIdentity({ identity, target, tag, revision, prefix, errors }) {
  if (identity === null || typeof identity !== 'object' || Array.isArray(identity)) {
    errors.push(`${prefix} is missing`);
    return;
  }
  const expected = {
    desktop_version: releaseVersionFromTag(tag),
    server_version: tag,
    server_revision: revision,
    os: target.os,
    arch: target.arch,
  };
  for (const [field, value] of Object.entries(expected)) {
    if (identity[field] !== value) {
      errors.push(`${prefix} ${field}=${identity[field]}, expected ${value}`);
    }
  }
}

function formatTarget(target) {
  return target !== null && typeof target === 'object'
    ? `${target.os ?? '(unknown)'}/${target.arch ?? '(unknown)'}`
    : String(target);
}

function isTarget(target) {
  return (
    target !== null &&
    typeof target === 'object' &&
    typeof target.os === 'string' &&
    typeof target.arch === 'string'
  );
}
