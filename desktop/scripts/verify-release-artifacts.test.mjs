import { createHash } from 'node:crypto';
import { afterEach, describe, expect, it } from 'vitest';
import {
  mkdtempSync,
  mkdirSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  createPackageVerificationReceipt,
  expectedDesktopArtifacts,
  readArtifactEvidence,
  validateArtifactInventory,
  validateChecksumManifest,
} from './lib/release-artifacts.mjs';
import { runReleaseArtifactCli, verifyReleaseArtifacts } from './verify-release-artifacts.mjs';

const TAG = 'v0.150.0';
const REVISION = '1234567890abcdef1234567890abcdef12345678';
const DIST_DIR = '/release/desktop/dist';
const fixtureRoots = [];

afterEach(() => {
  for (const root of fixtureRoots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function receipt(os, arch, artifacts, artifactDir = DIST_DIR) {
  const paths = {
    dmg: 'Agentico-mac-universal.dmg',
    AppImage: `Agentico-${arch}.AppImage`,
    deb: `agentico_0.150.0_${arch === 'x64' ? 'amd64' : arch}.deb`,
  };
  return createPackageVerificationReceipt({
    target: { os, arch },
    artifacts: artifacts.map((format) => {
      const path = join(artifactDir, paths[format]);
      return {
        target: { os, arch },
        format,
        path,
        sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
        size: statSync(path).size,
        identity: {
          desktop_version: '0.150.0',
          server_version: TAG,
          server_revision: REVISION,
          os,
          arch,
        },
      };
    }),
    unpackedApp: join(artifactDir, 'unpacked', 'agentico'),
  });
}

function completeV0150Fixture(artifactDir = DIST_DIR) {
  const files = expectedDesktopArtifacts(TAG).map(({ name }) => name);
  const root =
    artifactDir === DIST_DIR ? mkdtempSync(join(tmpdir(), 'agentico-receipt-fixture-')) : null;
  const actualDir = root === null ? artifactDir : join(root, 'desktop', 'dist');
  if (root !== null) fixtureRoots.push(root);
  mkdirSync(actualDir, { recursive: true });
  for (const name of files) writeFileSync(join(actualDir, name), `artifact:${name}\n`);
  return {
    tag: TAG,
    revision: REVISION,
    artifactDir: actualDir,
    files,
    sizes: Object.fromEntries(files.map((name) => [name, 1])),
    receipts: {
      'package-verification-darwin-universal.json': receipt(
        'darwin',
        'universal',
        ['dmg'],
        actualDir,
      ),
      'package-verification-linux-x64.json': receipt(
        'linux',
        'x64',
        ['AppImage', 'deb'],
        actualDir,
      ),
      'package-verification-linux-arm64.json': receipt(
        'linux',
        'arm64',
        ['AppImage', 'deb'],
        actualDir,
      ),
    },
  };
}

describe('validateArtifactInventory', () => {
  it('accepts one verified receipt and nonempty file for every desktop artifact', () => {
    expect(validateArtifactInventory(completeV0150Fixture())).toEqual([]);
  });

  it('rejects an arm64 receipt carrying the x64 identity', () => {
    const fixture = completeV0150Fixture();
    fixture.receipts['package-verification-linux-arm64.json'].artifacts[0].identity.arch = 'x64';

    expect(validateArtifactInventory(fixture)).toContain(
      'package-verification-linux-arm64.json AppImage identity arch=x64, expected arm64',
    );
  });

  it('accumulates missing, duplicate, zero-byte, and wrong-version package failures', () => {
    const fixture = completeV0150Fixture();
    fixture.files = [
      ...fixture.files.filter((name) => name !== 'Agentico-arm64.AppImage'),
      'Agentico-x64.AppImage',
      'agentico_0.149.9_amd64.deb',
    ];
    fixture.sizes['Agentico-x64.AppImage'] = 0;

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        'missing desktop artifact: Agentico-arm64.AppImage',
        'duplicate desktop artifact: Agentico-x64.AppImage',
        'desktop artifact is empty: Agentico-x64.AppImage',
        'unexpected desktop package version: agentico_0.149.9_amd64.deb (expected 0.150.0)',
      ]),
    );
  });

  it('rejects receipt version, revision, OS, and required-target mismatches', () => {
    const fixture = completeV0150Fixture();
    const receipt = fixture.receipts['package-verification-linux-x64.json'];
    const appImage = receipt.artifacts.find((artifact) => artifact.format === 'AppImage');
    appImage.identity.desktop_version = '0.149.9';
    appImage.identity.server_version = 'v0.149.9';
    appImage.identity.server_revision = 'bad-revision';
    appImage.identity.os = 'darwin';
    receipt.artifacts = [appImage];

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        'package-verification-linux-x64.json AppImage identity desktop_version=0.149.9, expected 0.150.0',
        'package-verification-linux-x64.json AppImage identity server_version=v0.149.9, expected v0.150.0',
        `package-verification-linux-x64.json AppImage identity server_revision=bad-revision, expected ${REVISION}`,
        'package-verification-linux-x64.json AppImage identity os=darwin, expected linux',
        'package-verification-linux-x64.json does not verify deb',
      ]),
    );
  });

  it('requires every expected receipt', () => {
    const fixture = completeV0150Fixture();
    delete fixture.receipts['package-verification-darwin-universal.json'];

    expect(validateArtifactInventory(fixture)).toContain(
      'missing verification receipt: package-verification-darwin-universal.json',
    );
  });

  it('rejects receipt entries that are swapped between Linux targets', () => {
    const fixture = completeV0150Fixture();
    const x64 = fixture.receipts['package-verification-linux-x64.json'].artifacts;
    const arm64 = fixture.receipts['package-verification-linux-arm64.json'].artifacts;
    for (let index = 0; index < x64.length; index += 1) {
      [x64[index].path, arm64[index].path] = [arm64[index].path, x64[index].path];
    }

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        expect.stringContaining('package-verification-linux-x64.json AppImage path='),
        expect.stringContaining('package-verification-linux-arm64.json deb path='),
      ]),
    );
  });

  it('requires exactly one correctly formatted receipt entry for each artifact', () => {
    const fixture = completeV0150Fixture();
    const receipt = fixture.receipts['package-verification-linux-x64.json'];
    const [appImage, deb] = receipt.artifacts;
    appImage.path = join(fixture.artifactDir, 'agentico_0.150.0_amd64.deb');
    deb.path = join(fixture.artifactDir, 'Agentico-x64.AppImage');

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        expect.stringContaining('package-verification-linux-x64.json AppImage path='),
        expect.stringContaining('package-verification-linux-x64.json deb path='),
      ]),
    );
  });

  it('rejects duplicate and missing receipt entries independently', () => {
    const fixture = completeV0150Fixture();
    const appImage = fixture.receipts['package-verification-linux-x64.json'].artifacts[0];
    fixture.receipts['package-verification-linux-x64.json'].artifacts = [appImage, { ...appImage }];

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        'package-verification-linux-x64.json has 2 AppImage verification entries, expected exactly 1',
        'package-verification-linux-x64.json does not verify deb',
      ]),
    );
  });

  it('rejects an unexpected extra receipt entry rather than ignoring it', () => {
    const fixture = completeV0150Fixture();
    fixture.receipts['package-verification-linux-x64.json'].artifacts.push({
      format: 'zip',
      target: { os: 'linux', arch: 'x64' },
    });

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        'package-verification-linux-x64.json has 3 artifact verification entries, expected 2',
        'package-verification-linux-x64.json has unexpected artifact format zip',
      ]),
    );
  });

  it('rejects a matching filename from an unverified directory', () => {
    const fixture = completeV0150Fixture();
    fixture.receipts['package-verification-linux-x64.json'].artifacts[0].path =
      '/different-unverified-directory/Agentico-x64.AppImage';

    expect(validateArtifactInventory(fixture).join('\n')).toContain(
      'package-verification-linux-x64.json could not hash AppImage at /different-unverified-directory/Agentico-x64.AppImage',
    );
  });

  it('rejects an artifact whose bytes changed after its receipt was written', () => {
    const fixture = completeV0150Fixture();
    const name = 'Agentico-x64.AppImage';
    writeFileSync(join(fixture.artifactDir, name), 'tampered artifact bytes\n');

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        expect.stringContaining('package-verification-linux-x64.json AppImage SHA-256='),
        expect.stringContaining('package-verification-linux-x64.json AppImage size='),
      ]),
    );
  });

  it('rejects a symlink in place of a release artifact', () => {
    const fixture = completeV0150Fixture();
    const artifact = join(fixture.artifactDir, 'Agentico-x64.AppImage');
    const replacement = join(fixture.artifactDir, 'replacement.AppImage');
    writeFileSync(replacement, 'replacement\n');
    rmSync(artifact);
    symlinkSync(replacement, artifact);

    expect(() => readArtifactEvidence(artifact)).toThrow('not a regular file');
    expect(validateArtifactInventory(fixture).join('\n')).toContain('could not hash AppImage');
  });

  it('rejects a receipt that claims the current AppImage but stale DEB evidence', () => {
    const fixture = completeV0150Fixture();
    const artifacts = fixture.receipts['package-verification-linux-x64.json'].artifacts;
    const appImage = artifacts.find((artifact) => artifact.format === 'AppImage');
    const deb = artifacts.find((artifact) => artifact.format === 'deb');
    deb.identity = { ...appImage.identity, arch: 'arm64' };

    expect(validateArtifactInventory(fixture)).toContain(
      'package-verification-linux-x64.json deb identity arch=arm64, expected x64',
    );
  });

  it('can gate only the Linux packages for the local rehearsal', () => {
    const fixture = completeV0150Fixture();
    fixture.files = fixture.files.filter((name) => name !== 'Agentico-mac-universal.dmg');
    delete fixture.sizes['Agentico-mac-universal.dmg'];
    delete fixture.receipts['package-verification-darwin-universal.json'];

    expect(validateArtifactInventory({ ...fixture, linuxOnly: true })).toEqual([]);
  });
});

describe('validateChecksumManifest', () => {
  it('requires exactly one checksum line per desktop artifact', () => {
    const expected = expectedDesktopArtifacts(TAG).map(({ name }) => name);
    const manifest = expected.map((name) => `${'a'.repeat(64)}  ${name}`).join('\n');

    expect(validateChecksumManifest(manifest, expected)).toEqual([]);
    expect(
      validateChecksumManifest(manifest.replace(/.*Agentico-arm64.AppImage\n/, ''), expected),
    ).toContain('checksums.txt has no entry for Agentico-arm64.AppImage');
  });

  it('rejects malformed hashes and duplicate desktop checksum entries', () => {
    const expected = expectedDesktopArtifacts(TAG).map(({ name }) => name);
    const manifest = [
      ...expected.map((name) => `${'a'.repeat(64)}  ${name}`),
      `not-a-sha  Agentico-x64.AppImage`,
      `${'b'.repeat(64)}  Agentico-x64.AppImage`,
    ].join('\n');

    expect(validateChecksumManifest(manifest, expected)).toEqual(
      expect.arrayContaining([
        'checksums.txt has malformed SHA-256 for Agentico-x64.AppImage',
        'checksums.txt has 3 entries for Agentico-x64.AppImage, expected exactly 1',
      ]),
    );
  });
});

describe('verifyReleaseArtifacts', () => {
  it('writes package evidence after validating on-disk artifacts and receipts', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-artifacts-'));
    const desktopDist = join(root, 'desktop', 'dist');
    mkdirSync(desktopDist, { recursive: true });
    const fixture = completeV0150Fixture(desktopDist);
    for (const [name, receipt] of Object.entries(fixture.receipts)) {
      writeFileSync(join(desktopDist, name), `${JSON.stringify(receipt)}\n`);
    }

    try {
      expect(
        verifyReleaseArtifacts({
          mode: 'packages',
          tag: TAG,
          revision: REVISION,
          desktopDist,
        }),
      ).toMatchObject({ ok: true, errors: [] });
      expect(
        JSON.parse(readFileSync(join(desktopDist, 'release-artifact-verification.json'))),
      ).toMatchObject({
        mode: 'packages',
        ok: true,
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('writes failing manifest evidence while still checking its detached signature', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-artifacts-'));
    const desktopDist = join(root, 'desktop', 'dist');
    const checksumsPath = join(root, 'dist', 'checksums.txt');
    mkdirSync(desktopDist, { recursive: true });
    mkdirSync(join(root, 'dist'), { recursive: true });
    writeFileSync(checksumsPath, `${'a'.repeat(64)}  Agentico-mac-universal.dmg\n`);
    let verifiedPath;

    try {
      const result = verifyReleaseArtifacts({
        mode: 'manifest',
        tag: TAG,
        revision: REVISION,
        desktopDist,
        checksumsPath,
        runSignatureVerification: (path) => {
          verifiedPath = path;
          throw new Error('signature does not verify');
        },
      });

      expect(verifiedPath).toBe(checksumsPath);
      expect(result.errors).toEqual(
        expect.arrayContaining([
          'checksums.txt has no entry for Agentico-x64.AppImage',
          'checksums signature verification failed: signature does not verify',
        ]),
      );
      expect(
        JSON.parse(readFileSync(join(desktopDist, 'release-artifact-verification.json'))),
      ).toMatchObject({
        mode: 'manifest',
        ok: false,
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects a signed checksum whose desktop hash differs from verified receipt bytes', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-artifacts-'));
    const desktopDist = join(root, 'desktop', 'dist');
    const checksumsPath = join(root, 'dist', 'checksums.txt');
    mkdirSync(join(root, 'dist'), { recursive: true });
    const fixture = completeV0150Fixture(desktopDist);
    for (const [name, evidence] of Object.entries(fixture.receipts)) {
      writeFileSync(join(desktopDist, name), `${JSON.stringify(evidence)}\n`);
    }
    writeFileSync(
      checksumsPath,
      fixture.files.map((name) => `${'a'.repeat(64)}  ${name}`).join('\n'),
    );

    try {
      const result = verifyReleaseArtifacts({
        mode: 'manifest',
        tag: TAG,
        revision: REVISION,
        desktopDist,
        checksumsPath,
        runSignatureVerification: () => {},
      });
      expect(result.errors.join('\n')).toContain(
        'checksums.txt SHA-256 for Agentico-mac-universal.dmg=',
      );
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('preserves package failures when writing evidence itself fails', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-artifacts-'));
    const invalidDesktopDist = join(root, 'not-a-directory');
    writeFileSync(invalidDesktopDist, 'not a directory\n');

    try {
      const result = verifyReleaseArtifacts({
        mode: 'packages',
        tag: TAG,
        revision: REVISION,
        desktopDist: invalidDesktopDist,
      });

      expect(result).toMatchObject({ ok: false });
      expect(result.errors).toEqual(
        expect.arrayContaining([
          expect.stringContaining('could not list desktop dist directory'),
          'missing desktop artifact: Agentico-mac-universal.dmg',
          expect.stringContaining('could not write release artifact verification evidence'),
        ]),
      );
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('reports both inventory and evidence-write failures from the CLI boundary', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-release-artifacts-'));
    const invalidDesktopDist = join(root, 'not-a-directory');
    writeFileSync(invalidDesktopDist, 'not a directory\n');

    try {
      const result = runReleaseArtifactCli({
        args: ['packages'],
        desktopDist: invalidDesktopDist,
        gitCommand: (_cwd, ...args) => {
          if (args.join(' ') === 'describe --tags --exact-match') return TAG;
          if (args.join(' ') === 'rev-parse HEAD') return REVISION;
          throw new Error(`unexpected Git command: ${args.join(' ')}`);
        },
      });

      expect(result.status).toBe(1);
      expect(result.message).toContain('missing desktop artifact: Agentico-mac-universal.dmg');
      expect(result.message).toContain('could not write release artifact verification evidence');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('rejects invalid CLI flags without resolving release metadata', () => {
    expect(runReleaseArtifactCli({ args: ['manifest', '--linux-only'] })).toEqual({
      status: 1,
      message: 'usage: verify-release-artifacts.mjs packages [--linux-only]|manifest',
    });
  });
});
