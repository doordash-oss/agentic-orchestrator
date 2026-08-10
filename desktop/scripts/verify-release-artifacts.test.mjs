import { describe, expect, it } from 'vitest';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import {
  expectedDesktopArtifacts,
  validateArtifactInventory,
  validateChecksumManifest,
} from './lib/release-artifacts.mjs';
import { verifyReleaseArtifacts } from './verify-release-artifacts.mjs';

const TAG = 'v0.150.0';
const REVISION = '1234567890abcdef1234567890abcdef12345678';

function receipt(os, arch, artifacts) {
  return {
    artifacts: artifacts.map((target) => ({ target, path: `/tmp/${target}` })),
    identity: {
      desktop_version: '0.150.0',
      server_version: TAG,
      server_revision: REVISION,
      os,
      arch,
    },
  };
}

function completeV0150Fixture() {
  const files = expectedDesktopArtifacts(TAG).map(({ name }) => name);
  return {
    tag: TAG,
    revision: REVISION,
    files,
    sizes: Object.fromEntries(files.map((name) => [name, 1])),
    receipts: {
      'package-verification-darwin-universal.json': receipt('darwin', 'universal', ['dmg']),
      'package-verification-linux-x64.json': receipt('linux', 'x64', ['AppImage', 'deb']),
      'package-verification-linux-arm64.json': receipt('linux', 'arm64', ['AppImage', 'deb']),
    },
  };
}

describe('validateArtifactInventory', () => {
  it('accepts one verified receipt and nonempty file for every desktop artifact', () => {
    expect(validateArtifactInventory(completeV0150Fixture())).toEqual([]);
  });

  it('rejects an arm64 receipt carrying the x64 identity', () => {
    const fixture = completeV0150Fixture();
    fixture.receipts['package-verification-linux-arm64.json'].identity.arch = 'x64';

    expect(validateArtifactInventory(fixture)).toContain(
      'package-verification-linux-arm64.json identity arch=x64, expected arm64',
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
    receipt.identity.desktop_version = '0.149.9';
    receipt.identity.server_version = 'v0.149.9';
    receipt.identity.server_revision = 'bad-revision';
    receipt.identity.os = 'darwin';
    receipt.artifacts = [{ target: 'AppImage' }];

    expect(validateArtifactInventory(fixture)).toEqual(
      expect.arrayContaining([
        'package-verification-linux-x64.json identity desktop_version=0.149.9, expected 0.150.0',
        'package-verification-linux-x64.json identity server_version=v0.149.9, expected v0.150.0',
        `package-verification-linux-x64.json identity server_revision=bad-revision, expected ${REVISION}`,
        'package-verification-linux-x64.json identity os=darwin, expected linux',
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
    const fixture = completeV0150Fixture();
    for (const name of fixture.files) writeFileSync(join(desktopDist, name), 'artifact\n');
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
});
