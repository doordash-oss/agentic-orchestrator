import { generateKeyPairSync } from 'node:crypto';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';

import {
  createDesktopReleaseManifest,
  verifyDesktopReleaseManifest,
} from './desktop-release-manifest.mjs';
import { expectedDesktopArtifacts } from './release-artifacts.mjs';
import { publicKeyPem, signReleasePayload } from './release-signing.mjs';

const roots = [];
afterEach(() => roots.splice(0).forEach((root) => rmSync(root, { recursive: true, force: true })));

describe('signed desktop release manifest', () => {
  it('writes canonical schema-v1 bytes in the receipt-bound artifact order and verifies all bytes', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-desktop-release-manifest-'));
    roots.push(root);
    const tag = 'v0.150.0';
    const commit = 'a'.repeat(40);
    for (const { name } of expectedDesktopArtifacts(tag)) writeFileSync(join(root, name), name);

    const manifestPath = join(root, 'desktop-release.json');
    const manifest = createDesktopReleaseManifest({ tag, commit, artifactDir: root, manifestPath });
    const bytes = readFileSync(manifestPath);
    expect(bytes.toString()).toBe(`${JSON.stringify(manifest, null, 2)}\n`);
    expect(manifest.artifacts.map(({ name }) => name)).toEqual(
      expectedDesktopArtifacts(tag).map(({ name }) => name),
    );
    expect(Object.keys(manifest.artifacts[0])).toEqual(['name', 'sha256', 'size']);

    const { privateKey } = generateKeyPairSync('ed25519');
    writeFileSync(`${manifestPath}.sig`, `${signReleasePayload(bytes, privateKey)}\n`);
    chmodSync(manifestPath, 0o400);
    chmodSync(`${manifestPath}.sig`, 0o400);
    expect(
      verifyDesktopReleaseManifest({
        tag,
        commit,
        artifactDir: root,
        manifestPath,
        publicKey: publicKeyPem(privateKey),
      }),
    ).toEqual(manifest);
  });

  it('rejects artifact drift even when the signed manifest bytes are unchanged', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-desktop-release-manifest-drift-'));
    roots.push(root);
    const tag = 'v0.150.0';
    const commit = 'b'.repeat(40);
    for (const { name } of expectedDesktopArtifacts(tag)) writeFileSync(join(root, name), name);
    const manifestPath = join(root, 'desktop-release.json');
    createDesktopReleaseManifest({ tag, commit, artifactDir: root, manifestPath });
    const { privateKey } = generateKeyPairSync('ed25519');
    writeFileSync(
      `${manifestPath}.sig`,
      `${signReleasePayload(readFileSync(manifestPath), privateKey)}\n`,
    );
    writeFileSync(join(root, expectedDesktopArtifacts(tag)[0].name), 'replacement');
    expect(() =>
      verifyDesktopReleaseManifest({
        tag,
        commit,
        artifactDir: root,
        manifestPath,
        publicKey: publicKeyPem(privateKey),
      }),
    ).toThrow(/artifact evidence changed/);
  });
});
