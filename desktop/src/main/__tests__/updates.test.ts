import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  UpdateCoordinator,
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
  it('downloads the exact package bytes and stages them only after signed checksum verification', async () => {
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

  it('selects the exact checksum manifest when its signature appears first', async () => {
    const fixture = signedFixture('v0.2.0', 'Agentico-mac-universal.dmg', 'macos package bytes', {
      signatureBeforeChecksum: true,
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
      checksumText: `${'0'.repeat(64)}  Agentico-mac-universal.dmg\n`,
      signatureText: `${sha256(Buffer.from('package bytes'))}  Agentico-mac-universal.dmg\n`,
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
      message: 'Signed update metadata could not be verified.',
    });
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
    expect(fs.existsSync(path.join(dir, 'updates', 'v0.2.0'))).toBe(false);
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
      message: 'The update package checksum did not match signed metadata.',
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
      message: 'The release feed offered an older version; downgrade rejected.',
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
    expect(fixture.requestedPackage).not.toHaveBeenCalled();
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

  it('keeps a verified staged package retryable when native installation fails', async () => {
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
      status: 'ready',
      signatureStatus: 'verified',
      message: 'The verified update could not be installed. Retry or use the release notes.',
    });
    expect(update.getState().message).not.toContain('/private/path');
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

interface SignedFixture {
  feed: unknown[];
  fetch: typeof fetch;
  requestedPackage: ReturnType<typeof vi.fn>;
  feedPath(dir: string): string;
}

function signedFixture(
  tag: string,
  packageName: string,
  packageText: string,
  options: {
    checksumText?: string;
    signatureText?: string;
    servedPackageBytes?: Buffer;
    extraReleases?: unknown[];
    redirectAssets?: boolean;
    signatureBeforeChecksum?: boolean;
  } = {},
): SignedFixture {
  const packageBytes = Buffer.from(packageText);
  const checksumText = options.checksumText ?? `${sha256(packageBytes)}  ${packageName}\n`;
  const signatureText = options.signatureText ?? checksumText;
  const checksumSignature = Buffer.concat([
    Buffer.from('agentico-ed25519:'),
    Buffer.from(
      sign(null, Buffer.from(signatureText), createPrivateKey(PRIVATE_KEY)).toString('base64'),
    ),
  ]);
  const checksumAsset = asset(tag, 'checksums.txt', Buffer.byteLength(checksumText));
  const signatureAsset = asset(tag, 'checksums.txt.sig', checksumSignature.byteLength);
  const assets = [
    asset(tag, packageName, packageBytes.byteLength),
    ...(options.signatureBeforeChecksum === true
      ? [signatureAsset, checksumAsset]
      : [checksumAsset, signatureAsset]),
  ];
  const feed = [...(options.extraReleases ?? []), release(tag, assets)];
  const requestedPackage = vi.fn();
  const files = new Map<string, Buffer>([
    ['feed', Buffer.from(JSON.stringify(feed))],
    [packageName, options.servedPackageBytes ?? packageBytes],
    ['checksums.txt', Buffer.from(checksumText)],
    ['checksums.txt.sig', checksumSignature],
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
