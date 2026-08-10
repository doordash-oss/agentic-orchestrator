// Pure release artifact and Docker-build contract shared by local release scripts.

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
