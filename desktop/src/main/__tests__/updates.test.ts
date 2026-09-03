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

import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  UpdateCoordinator,
  UpdateRestartPostponedError,
  createUpdateFixtureFetch,
  detectCanInstallInApp,
  detectPackageFormat,
} from '../updates';

const RELEASES_API = 'https://api.github.com/repos/doordash-oss/agentic-orchestrator/releases';
const PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAmhM+TNlJSPzGSFwd/DakW3G6MzxCpouletrsW4WAezE=
-----END PUBLIC KEY-----`;
// Test-only Ed25519 fixture key; not a production release credential.
const PRIVATE_KEY = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEINZMXBFPD1S98rCr5jnAqC4oCAf7E+GQBz6NrbxOncAr
-----END PRIVATE KEY-----`;

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-updates-'));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('UpdateCoordinator', () => {
  it('verifies a signed release envelope before downloading its exact package', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
    });
    expect(fixture.requestedPackage).toHaveBeenCalledOnce();
  });

  it('downloads the exact package bytes and stages them only after signed envelope verification', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
      packageFormat: 'macos',
    });

    const stagedPackage = path.join(dir, 'updates', 'v0.2.0', 'Agentico-mac-universal.dmg');
    const stagedMetadata = JSON.parse(
      fs.readFileSync(path.join(dir, 'updates', 'v0.2.0', 'selected-asset.json'), 'utf8'),
    ) as { packageSha256: string };
    expect(fs.readFileSync(stagedPackage, 'utf8')).toBe('macos package bytes');
    expect(stagedMetadata.packageSha256).toBe(sha256(Buffer.from('macos package bytes')));
  });

  it('selects the exact release envelope when its signature appears first', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes', {
      signatureBeforeEnvelope: true,
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
    });
  });

  it('follows GitHub release asset redirects through the current release asset host', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes', {
      redirectAssets: true,
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
    });
    expect(fixture.requestedPackage).toHaveBeenCalledOnce();
  });

  it('resumes already staged verified package bytes without downloading the package again', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes');
    const first = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });
    await expect(first.checkNow()).resolves.toMatchObject({ status: 'ready' });
    expect(fixture.requestedPackage).toHaveBeenCalledTimes(1);

    const second = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });
    await expect(second.checkNow()).resolves.toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
    });
    expect(fixture.requestedPackage).toHaveBeenCalledTimes(1);

    fs.writeFileSync(path.join(dir, 'updates', 'v0.2.0', 'Agentico-mac-universal.dmg'), 'corrupt');
    const third = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });
    await expect(third.checkNow()).resolves.toMatchObject({ status: 'ready' });
    expect(fixture.requestedPackage).toHaveBeenCalledTimes(2);
    expect(
      fs.readFileSync(path.join(dir, 'updates', 'v0.2.0', 'Agentico-mac-universal.dmg'), 'utf8'),
    ).toBe('macos package bytes');
  });

  it('rejects altered signed metadata before downloading a package', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      servedEnvelopeBytes: Buffer.from('{"tampered":true}\n'),
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      signatureStatus: 'failed',
      message: 'Agentico could not verify the downloaded update.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
    expect(fs.existsSync(path.join(dir, 'updates', 'v0.2.0'))).toBe(false);
  });

  it('does not let a stale signature failure classify a later check failure', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      servedEnvelopeBytes: Buffer.from('{"tampered":true}\n'),
    });
    let feedUnreachable = false;
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture: {
        ...fixture,
        fetch: (input, init) =>
          feedUnreachable && fetchUrl(input) === RELEASES_API
            ? Promise.reject(new TypeError('network down'))
            : fixture.fetch(input, init),
      },
    });

    // First check: the signed envelope is tampered — a genuine signature
    // failure whose verdict persists on the failed state.
    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      signatureStatus: 'failed',
      message: 'Agentico could not verify the downloaded update.',
    });

    // Second check: the feed itself is unreachable before any signature
    // is examined. The stale 'failed' verdict must not reclassify this
    // pre-verification failure as a signature failure.
    feedUnreachable = true;
    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
  });

  it('rejects replaying an older signed envelope under a higher release tag', async () => {
    const fixture = signedFixture('v0.3.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      envelopeTag: 'v0.2.0',
      envelopeVersion: '0.2.0',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects an envelope whose inventory omits one of the five desktop packages', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      omitEnvelopeArtifact: 'Agentico-arm64.AppImage',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects duplicate exact package assets instead of choosing the first one', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      duplicateReleaseAsset: 'Agentico-mac-universal.dmg',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects duplicate exact release-envelope assets instead of choosing the first one', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      duplicateReleaseAsset: 'desktop-release.json',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('keeps updates without a complete signed release envelope non-installable', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      omitReleaseAsset: 'desktop-release.json.sig',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'available',
      signatureStatus: 'unknown',
      message: 'An update is available, but the signed release envelope is incomplete.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects duplicate signed envelope entries for the selected package', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      duplicateEnvelopeArtifact: 'Agentico-mac-universal.dmg',
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it.each([
    ['unsupported schema', (envelope: TestReleaseEnvelope) => ({ ...envelope, schema_version: 2 })],
    [
      'invalid commit',
      (envelope: TestReleaseEnvelope) => ({ ...envelope, commit: 'not-a-commit' }),
    ],
    ['extra top-level field', (envelope: TestReleaseEnvelope) => ({ ...envelope, extra: true })],
    [
      'extra artifact field',
      (envelope: TestReleaseEnvelope) => ({
        ...envelope,
        artifacts: [{ ...envelope.artifacts[0], extra: true }, ...envelope.artifacts.slice(1)],
      }),
    ],
  ])('rejects signed release envelopes with %s', async (_label, transformEnvelope) => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      transformEnvelope,
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({ status: 'failed' });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects a GitHub package size that differs from the signed envelope', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      transformEnvelope: (envelope) => ({
        ...envelope,
        artifacts: envelope.artifacts.map((artifact) =>
          artifact.name === 'Agentico-mac-universal.dmg'
            ? { ...artifact, size: artifact.size + 1 }
            : artifact,
        ),
      }),
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'Agentico could not complete the update check.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('rejects fuzzy package names when the exact production filename is absent', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      omitReleaseAsset: 'Agentico-mac-universal.dmg',
      extraReleaseAsset: { name: 'Agentico-mac-arm64.dmg', size: 13 },
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'No compatible update package is available for this platform.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('does not resume staged bytes that were bound to a different signed envelope', async () => {
    const firstFixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    await makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture: firstFixture,
    }).checkNow();

    const secondFixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      envelopeCommit: 'fedcba9876543210fedcba9876543210fedcba98',
    });
    await expect(
      makeCoordinator({
        platform: 'darwin',
        arch: 'arm64',
        packageFormat: 'macos',
        fixture: secondFixture,
      }).checkNow(),
    ).resolves.toMatchObject({ status: 'ready' });
    expect(secondFixture.requestedPackage).toHaveBeenCalledOnce();
  });

  it('rejects tampered or partial packages without staging them', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      servedPackageBytes: Buffer.from('wrong content'),
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      signatureStatus: 'failed',
      message: 'Agentico could not verify the downloaded update.',
    });
    expect(fs.existsSync(path.join(dir, 'updates', 'v0.2.0'))).toBe(false);
  });

  it('rejects downgrade feeds and ignores prerelease candidates', async () => {
    const fixture = signedFixture('v0.0.9', 'Agentico-mac-universal.dmg', 'old package bytes', {
      extraReleases: [release('v0.3.0', [], { prerelease: true })],
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      message: 'The release feed offered an older version.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('keeps DEB installs in verified package-manager guidance instead of in-app install', async () => {
    const fixture = signedFixture('v0.2.0', 'agentico_0.2.0_amd64.deb', 'deb package bytes');
    const update = makeCoordinator({
      platform: 'linux',
      arch: 'x64',
      packageFormat: 'deb',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'available',
      targetVersion: '0.2.0',
      packageFormat: 'deb',
      signatureStatus: 'verified',
    });
    expect(update.getState().guidance?.join('\n')).toContain(
      'sudo apt install ./agentico_0.2.0_amd64.deb',
    );
    expect(update.getState().guidance?.join('\n')).toContain('desktop-release.json');
    expect(update.getState().guidance?.join('\n')).not.toContain('checksum');
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('derives the exact arm64 AppImage and versioned DEB production filenames', async () => {
    const appImageFixture = signedFixture(
      'v0.2.0',
      'Agentico-arm64.AppImage',
      'arm64 appimage bytes',
    );
    await expect(
      makeCoordinator({
        platform: 'linux',
        arch: 'arm64',
        packageFormat: 'appimage',
        fixture: appImageFixture,
      }).checkNow(),
    ).resolves.toMatchObject({ status: 'ready', signatureStatus: 'verified' });
    expect(appImageFixture.requestedPackage).toHaveBeenCalledOnce();

    const debFixture = signedFixture('v0.2.0', 'agentico_0.2.0_arm64.deb', 'arm64 deb bytes');
    const deb = makeCoordinator({
      platform: 'linux',
      arch: 'arm64',
      packageFormat: 'deb',
      fixture: debFixture,
    });
    await expect(deb.checkNow()).resolves.toMatchObject({
      status: 'available',
      signatureStatus: 'verified',
    });
    expect(deb.getState().guidance?.join('\n')).toContain('agentico_0.2.0_arm64.deb');
    expect(debFixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('falls back to signed-download guidance when AppImage cannot be replaced in app', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-x64.AppImage', 'appimage package bytes');
    const update = makeCoordinator({
      platform: 'linux',
      arch: 'x64',
      packageFormat: 'appimage',
      canInstallInApp: false,
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'available',
      targetVersion: '0.2.0',
      packageFormat: 'appimage',
      signatureStatus: 'verified',
      message: 'A verified update is available for manual signed installation.',
    });
    expect(update.getState().guidance?.join('\n')).toContain('cannot be safely replaced in app');
    expect(update.getState().guidance?.join('\n')).toContain('desktop-release.json.sig');
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
  });

  it('falls back to signed-download guidance when the macOS app location is not writable', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      canInstallInApp: false,
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'available',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
      message: 'A verified update is available for manual signed installation.',
    });
    expect(update.getState().guidance?.join('\n')).toContain(
      'macOS application location cannot be safely replaced in app',
    );
    expect(update.getState().guidance?.join('\n')).toContain('desktop-release.json.sig');
    expect(update.getState().guidance?.join('\n')).not.toContain('AppImage');
  });

  it('requires explicit install consent and persists active-work refusal state', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const onStateChanged = vi.fn();
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork: { featureCount: 1, amaActive: true, detectionFailed: false },
      onStateChanged,
    });
    await update.checkNow();

    await expect(
      update.installNow({ consent: true, stopActiveWork: false }),
    ).resolves.toMatchObject({
      status: 'ready',
      activeWorkSummary: '1 workflow and AMA session',
      message: 'Active work must be stopped before installing now.',
    });
    expect(update.getState()).toMatchObject({
      activeWorkSummary: '1 workflow and AMA session',
      message: 'Active work must be stopped before installing now.',
    });
    expect(onStateChanged).toHaveBeenCalledWith(
      expect.objectContaining({ message: 'Active work must be stopped before installing now.' }),
    );

    await expect(update.installWhenIdle()).resolves.toMatchObject({
      status: 'scheduled',
      activeWorkSummary: '1 workflow and AMA session',
    });
  });

  it('automatically restarts a scheduled install after authoritative work goes idle', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const restart = vi.fn();
    const activeWork = { featureCount: 1, amaActive: false, detectionFailed: false };
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork,
      restart,
    });
    await update.checkNow();
    await expect(update.installWhenIdle()).resolves.toMatchObject({
      status: 'scheduled',
      activeWorkSummary: '1 workflow',
    });

    activeWork.featureCount = 0;
    await expect(update.reconcileScheduledInstall()).resolves.toMatchObject({
      status: 'installing',
      activeWorkSummary: undefined,
      message: 'Restarting to apply the verified update.',
    });
    expect(restart).toHaveBeenCalledOnce();
    await update.reconcileScheduledInstall();
    expect(restart).toHaveBeenCalledOnce();
  });

  it('hands the exact verified staged package to the update installer', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const restart = vi.fn();
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      restart,
    });
    await update.checkNow();

    await update.restartToUpdate();

    expect(restart).toHaveBeenCalledWith({
      packageFormat: 'macos',
      packagePath: path.join(dir, 'updates', 'v0.2.0', 'Agentico-mac-universal.dmg'),
      targetVersion: '0.2.0',
    });
  });

  it('rechecks active work before applying a previously ready update', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const activeWork = { featureCount: 0, amaActive: false, detectionFailed: false };
    const restart = vi.fn();
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork,
      restart,
    });
    await update.checkNow();
    activeWork.featureCount = 1;

    await expect(Promise.resolve(update.restartToUpdate())).resolves.toMatchObject({
      status: 'ready',
      activeWorkSummary: '1 workflow',
      message: 'Active work must be stopped before installing now.',
    });
    expect(restart).not.toHaveBeenCalled();
  });

  it('authors the canonical check-failure error when the release feed cannot be fetched', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const failingFetch: typeof fetch = (input, init) =>
      fetchUrl(input).endsWith('/releases')
        ? Promise.reject(new Error('network unreachable at /Users/somebody/vpn'))
        : fixture.fetch(input, init);
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture: { ...fixture, fetch: failingFetch },
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      error: {
        code: 'E_UPDATE_CHECK_FAILED',
        class: 'blocking',
        title: 'Update check failed',
      },
    });
    const error = update.getState().error;
    expect(error?.summary).not.toContain('/Users/somebody');
    expect(error?.diagnostics).toContain('[path]');
    expect(error?.diagnostics ?? '').not.toContain('/Users/somebody');
  });

  it('authors the canonical signature-failure error when signed metadata fails verification', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes', {
      servedEnvelopeBytes: Buffer.from('{"tampered":true}\n'),
    });
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      signatureStatus: 'failed',
      error: {
        code: 'E_UPDATE_SIGNATURE_FAILED',
        class: 'blocking',
        title: 'Update signature verification failed',
        summary: 'Agentico could not verify the downloaded update.',
      },
    });
  });

  it('authors the canonical download-failure error when the package transfer is incomplete', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const truncatingFetch: typeof fetch = (input, init) => {
      if (fetchUrl(input).includes('Agentico-mac-universal.dmg')) {
        return Promise.resolve(
          new Response('short', {
            status: 200,
            headers: { 'content-length': '13', 'content-type': 'application/octet-stream' },
          }),
        );
      }
      return fixture.fetch(input, init);
    };
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture: { ...fixture, fetch: truncatingFetch },
    });

    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'failed',
      error: {
        code: 'E_UPDATE_DOWNLOAD_FAILED',
        class: 'blocking',
        title: 'Update download failed',
        summary: 'Agentico could not download the selected update.',
      },
    });
    expect(fs.existsSync(path.join(dir, 'updates', 'v0.2.0'))).toBe(false);
  });

  it('marks a failed install as failed with the canonical install error and redacted diagnostics', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      restart: vi.fn(() => Promise.reject(new Error('/private/path must stay private'))),
    });
    await update.checkNow();

    await expect(update.restartToUpdate()).resolves.toMatchObject({
      status: 'failed',
      signatureStatus: 'verified',
      message: 'Agentico could not install the verified update.',
      error: {
        code: 'E_UPDATE_INSTALL_FAILED',
        class: 'blocking',
        title: 'Update install failed',
        summary: 'Agentico could not install the verified update.',
        remediation: {
          hint: 'Retry the install from the Updates pane, or open the release notes.',
        },
      },
    });
    const error = update.getState().error;
    expect(error?.diagnostics).toContain('[path]');
    expect(error?.diagnostics ?? '').not.toContain('/private/path');
    // A fresh check recovers the staged, still-verified package.
    await expect(update.checkNow()).resolves.toMatchObject({ status: 'ready' });
    expect(update.getState().error).toBeUndefined();
  });

  it('keeps a verified staged package retryable, with an accurate message, when restart is postponed', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      restart: vi.fn(() => Promise.reject(new UpdateRestartPostponedError())),
    });
    await update.checkNow();

    await expect(update.restartToUpdate()).resolves.toMatchObject({
      status: 'ready',
      signatureStatus: 'verified',
      message: 'Restart was postponed. The verified update remains staged and ready to install.',
    });
  });

  it('keeps scheduled consent pending when authoritative activity remains non-idle', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const restart = vi.fn();
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork: { featureCount: 0, amaActive: false, detectionFailed: true },
      restart,
    });
    await update.checkNow();
    await update.installWhenIdle();

    await expect(update.reconcileScheduledInstall()).resolves.toMatchObject({
      status: 'scheduled',
      activeWorkSummary: 'Active work status could not be verified.',
    });
    expect(restart).not.toHaveBeenCalled();
  });

  it('keeps a verified update retryable when active work only partially stops', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork: { featureCount: 1, amaActive: false, detectionFailed: false },
      stopActiveWork: vi.fn(() =>
        Promise.resolve({ stopped: false, message: 'Feature alpha did not stop in time.' }),
      ),
    });
    await update.checkNow();

    await expect(update.installNow({ consent: true, stopActiveWork: true })).resolves.toMatchObject(
      {
        status: 'ready',
        signatureStatus: 'verified',
        activeWorkSummary: '1 workflow',
        message: 'Feature alpha did not stop in time.',
      },
    );
  });

  it('refreshes active-work summary after an update is ready', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const activeWork = { featureCount: 0, amaActive: false, detectionFailed: false };
    const update = makeCoordinator({
      platform: 'darwin',
      arch: 'arm64',
      packageFormat: 'macos',
      fixture,
      activeWork,
    });
    await expect(update.checkNow()).resolves.toMatchObject({
      status: 'ready',
      activeWorkSummary: undefined,
    });

    activeWork.amaActive = true;
    await expect(update.refreshActiveWorkSummary()).resolves.toMatchObject({
      status: 'ready',
      activeWorkSummary: 'AMA session',
    });
  });
});

describe('detectPackageFormat', () => {
  it('detects AppImage, DEB, and macOS formats without exposing paths', () => {
    expect(
      detectPackageFormat('darwin', {}, '/Applications/Agentico.app/Contents/MacOS/Agentico'),
    ).toBe('macos');
    expect(
      detectPackageFormat('linux', { APPIMAGE: '/tmp/Agentico.AppImage' }, '/tmp/AppRun'),
    ).toBe('appimage');
    expect(detectPackageFormat('linux', {}, '/opt/Agentico/agentico')).toBe('deb');
  });
});

describe('detectCanInstallInApp', () => {
  it('offers macOS self-install only when the app bundle parent is writable', () => {
    const execPath = '/Applications/Agentico.app/Contents/MacOS/Agentico';
    expect(
      detectCanInstallInApp('macos', {}, execPath, () => {
        throw new Error('read-only');
      }),
    ).toBe(false);
    expect(detectCanInstallInApp('macos', {}, execPath, () => undefined)).toBe(true);
    expect(detectCanInstallInApp('macos', {}, '/tmp/Electron', () => undefined)).toBe(false);
  });

  it('rejects DEB and read-only or nonstandard AppImage locations', () => {
    expect(detectCanInstallInApp('deb', {}, '/opt/Agentico/agentico')).toBe(false);
    expect(detectCanInstallInApp('appimage', {}, '/tmp/AppRun')).toBe(false);
    expect(
      detectCanInstallInApp(
        'appimage',
        { APPIMAGE: '/tmp/Agentico.AppImage' },
        '/tmp/AppRun',
        () => {
          throw new Error('read-only');
        },
      ),
    ).toBe(false);
    expect(
      detectCanInstallInApp(
        'appimage',
        { APPIMAGE: '/tmp/Agentico.AppImage' },
        '/tmp/AppRun',
        () => undefined,
      ),
    ).toBe(true);
  });

  it('honors the explicit install-mode overrides for packaged journeys', () => {
    // 'in-app' skips the replaceable-location checks, which point at the
    // installed bundle, not the linux-unpacked binary the journeys launch.
    expect(
      detectCanInstallInApp('appimage', { AGENTICO_UPDATE_INSTALL_MODE: 'in-app' }, '/tmp/AppRun'),
    ).toBe(true);
    expect(
      detectCanInstallInApp('macos', { AGENTICO_UPDATE_INSTALL_MODE: 'in-app' }, '/tmp/Electron'),
    ).toBe(true);
    // Package-manager-owned formats stay guidance-only even under 'in-app'.
    expect(
      detectCanInstallInApp('deb', { AGENTICO_UPDATE_INSTALL_MODE: 'in-app' }, '/tmp/agentico'),
    ).toBe(false);
    expect(
      detectCanInstallInApp('unknown', { AGENTICO_UPDATE_INSTALL_MODE: 'in-app' }, '/tmp/agentico'),
    ).toBe(false);
    // 'guidance' wins over replaceable locations.
    expect(
      detectCanInstallInApp(
        'appimage',
        { AGENTICO_UPDATE_INSTALL_MODE: 'guidance', APPIMAGE: '/tmp/Agentico.AppImage' },
        '/tmp/AppRun',
        () => undefined,
      ),
    ).toBe(false);
  });
});

describe('createUpdateFixtureFetch', () => {
  it('drives fixture files through the same response parsing path as production fetches', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'package bytes');
    const fixtureDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-update-fixture-'));
    try {
      const fixtureFeed = fixture.feedPath(fixtureDir);
      const fetch = createUpdateFixtureFetch(fixtureFeed);
      await expect((await fetch(RELEASES_API)).json()).resolves.toEqual(fixture.feed);
      await expect(
        (await fetch(assetUrl('v0.2.0', 'Agentico-mac-universal.dmg'))).text(),
      ).resolves.toBe('package bytes');
    } finally {
      fs.rmSync(fixtureDir, { recursive: true, force: true });
    }
  });
});

function makeCoordinator({
  platform,
  arch,
  packageFormat,
  fixture,
  activeWork = { featureCount: 0, amaActive: false, detectionFailed: false },
  canInstallInApp,
  onStateChanged,
  stopActiveWork = vi.fn(() => Promise.resolve({ stopped: true })),
  restart = vi.fn(),
}: {
  platform: NodeJS.Platform;
  arch: string;
  packageFormat: 'macos' | 'appimage' | 'deb';
  fixture: SignedFixture;
  activeWork?: { featureCount: number; amaActive: boolean; detectionFailed: boolean };
  canInstallInApp?: boolean;
  onStateChanged?: (state: ReturnType<UpdateCoordinator['getState']>) => void;
  stopActiveWork?: (active: {
    featureCount: number;
    amaActive: boolean;
    detectionFailed: boolean;
  }) => Promise<{ stopped: boolean; message?: string }>;
  restart?: (update: {
    packageFormat: 'macos' | 'appimage' | 'deb' | 'unknown';
    packagePath: string;
    targetVersion: string;
  }) => Promise<void> | void;
}) {
  return new UpdateCoordinator({
    currentVersion: '0.1.0',
    isPackaged: true,
    platform,
    arch,
    packageFormat,
    canInstallInApp,
    userDataDir: dir,
    fetch: fixture.fetch,
    releasePublicKey: createPublicKey(PUBLIC_KEY),
    now: () => new Date('2026-07-20T10:00:00.000Z'),
    setTimeout: (() => 1) as unknown as typeof setTimeout,
    clearTimeout: vi.fn(),
    onStateChanged,
    detectActiveWork: vi.fn(() => Promise.resolve(activeWork)),
    stopActiveWork,
    restart,
  });
}

function fetchUrl(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

interface SignedFixture {
  feed: unknown[];
  fetch: typeof fetch;
  requestedPackage: ReturnType<typeof vi.fn>;
  feedPath(dir: string): string;
}

interface TestReleaseEnvelope {
  schema_version: number;
  tag: string;
  version: string;
  commit: string;
  artifacts: Array<{ name: string; sha256: string; size: number }>;
}

function signedFixture(
  tag: string,
  packageName: string,
  packageText: string,
  options: {
    servedPackageBytes?: Buffer;
    servedEnvelopeBytes?: Buffer;
    extraReleases?: unknown[];
    redirectAssets?: boolean;
    signatureBeforeEnvelope?: boolean;
    envelopeTag?: string;
    envelopeVersion?: string;
    envelopeCommit?: string;
    omitEnvelopeArtifact?: string;
    duplicateEnvelopeArtifact?: string;
    duplicateReleaseAsset?: string;
    transformEnvelope?: (envelope: TestReleaseEnvelope) => unknown;
    omitReleaseAsset?: string;
    extraReleaseAsset?: { name: string; size: number };
  } = {},
): SignedFixture {
  const packageBytes = Buffer.from(packageText);
  const version = tag.replace(/^v/, '');
  const desktopPackages: Array<{ name: string; bytes: Buffer }> = [
    { name: 'Agentico-mac-universal.dmg', bytes: Buffer.from('fixture macos') },
    { name: 'Agentico-x64.AppImage', bytes: Buffer.from('fixture x64 appimage') },
    { name: 'Agentico-arm64.AppImage', bytes: Buffer.from('fixture arm64 appimage') },
    { name: `agentico_${version}_amd64.deb`, bytes: Buffer.from('fixture amd64 deb') },
    { name: `agentico_${version}_arm64.deb`, bytes: Buffer.from('fixture arm64 deb') },
  ].map((entry) => (entry.name === packageName ? { ...entry, bytes: packageBytes } : entry));
  let envelopeArtifacts = desktopPackages
    .filter(({ name }) => name !== options.omitEnvelopeArtifact)
    .map(({ name, bytes }) => ({
      name,
      sha256: sha256(bytes),
      size: bytes.byteLength,
    }));
  if (options.duplicateEnvelopeArtifact !== undefined) {
    const duplicate = envelopeArtifacts.find(
      ({ name }) => name === options.duplicateEnvelopeArtifact,
    );
    if (duplicate !== undefined) envelopeArtifacts = [...envelopeArtifacts, { ...duplicate }];
  }
  const envelope: TestReleaseEnvelope = {
    schema_version: 1,
    tag: options.envelopeTag ?? tag,
    version: options.envelopeVersion ?? version,
    commit: options.envelopeCommit ?? '0123456789abcdef0123456789abcdef01234567',
    artifacts: envelopeArtifacts,
  };
  const releaseEnvelope = Buffer.from(
    `${JSON.stringify(options.transformEnvelope?.(envelope) ?? envelope, null, 2)}\n`,
  );
  const releaseEnvelopeSignature = Buffer.from(
    `agentico-ed25519:${sign(null, releaseEnvelope, createPrivateKey(PRIVATE_KEY)).toString('base64')}`,
  );
  const envelopeAsset = asset(tag, 'desktop-release.json', releaseEnvelope.byteLength);
  const signatureAsset = asset(
    tag,
    'desktop-release.json.sig',
    releaseEnvelopeSignature.byteLength,
  );
  let assets = [
    ...desktopPackages.map(({ name, bytes }) => asset(tag, name, bytes.byteLength)),
    ...(options.signatureBeforeEnvelope === true
      ? [signatureAsset, envelopeAsset]
      : [envelopeAsset, signatureAsset]),
  ].filter(({ name }) => name !== options.omitReleaseAsset);
  if (options.extraReleaseAsset !== undefined) {
    assets = [
      asset(tag, options.extraReleaseAsset.name, options.extraReleaseAsset.size),
      ...assets,
    ];
  }
  if (options.duplicateReleaseAsset !== undefined) {
    const duplicate = assets.find(({ name }) => name === options.duplicateReleaseAsset);
    if (duplicate !== undefined) assets = [...assets, { ...duplicate }];
  }
  const feed = [...(options.extraReleases ?? []), release(tag, assets)];
  const requestedPackage = vi.fn();
  const files = new Map<string, Buffer>([
    ['feed', Buffer.from(JSON.stringify(feed))],
    [packageName, options.servedPackageBytes ?? packageBytes],
    ['desktop-release.json', options.servedEnvelopeBytes ?? releaseEnvelope],
    ['desktop-release.json.sig', releaseEnvelopeSignature],
    ...desktopPackages
      .filter(({ name }) => name !== packageName)
      .map(({ name, bytes }) => [name, bytes] as [string, Buffer]),
  ]);
  const fixtureFetch: typeof fetch = async (input) => {
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    if (url === RELEASES_API) {
      return response(files.get('feed')!);
    }
    const parsedUrl = new URL(url);
    if (options.redirectAssets === true && parsedUrl.hostname === 'github.com') {
      return new Response(null, {
        status: 302,
        headers: {
          location: `https://release-assets.githubusercontent.com/github-production-release-asset/fixture/${path.basename(parsedUrl.pathname)}?token=fixture`,
        },
      });
    }
    const name = path.basename(parsedUrl.pathname);
    const bytes = files.get(name);
    if (bytes === undefined) return new Response('not found', { status: 404 });
    if (name === packageName) requestedPackage();
    return response(bytes);
  };
  return {
    feed,
    fetch: fixtureFetch,
    requestedPackage,
    feedPath(fixtureDir: string): string {
      fs.mkdirSync(fixtureDir, { recursive: true });
      for (const [name, bytes] of files.entries()) {
        if (name !== 'feed') fs.writeFileSync(path.join(fixtureDir, name), bytes);
      }
      const feedPath = path.join(fixtureDir, 'release-feed.json');
      fs.writeFileSync(feedPath, `${JSON.stringify(feed, null, 2)}\n`);
      return feedPath;
    },
  };
}

function release(
  tag: string,
  assets: unknown[],
  options: { prerelease?: boolean; draft?: boolean } = {},
) {
  return {
    tag_name: tag,
    draft: options.draft ?? false,
    prerelease: options.prerelease ?? false,
    html_url: `https://github.com/doordash-oss/agentic-orchestrator/releases/tag/${tag}`,
    assets,
  };
}

function asset(tag: string, name: string, size: number) {
  return {
    name,
    size,
    browser_download_url: assetUrl(tag, name),
  };
}

function assetUrl(tag: string, name: string): string {
  return `https://github.com/doordash-oss/agentic-orchestrator/releases/download/${tag}/${name}`;
}

function response(bytes: Buffer): Response {
  return new Response(new Uint8Array(bytes), {
    status: 200,
    headers: { 'content-length': String(bytes.byteLength) },
  });
}

function sha256(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex');
}
