import { createHash, createPrivateKey, sign } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

// Test-only Ed25519 fixture key; not a production release credential.
const PRIVATE_KEY = `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEINZMXBFPD1S98rCr5jnAqC4oCAf7E+GQBz6NrbxOncAr
-----END PRIVATE KEY-----`;

export interface UpdateFixtureOptions {
  tag?: string;
  packageName?: string;
  packageText?: string;
  packageBytes?: Buffer;
  servedPackageBytes?: Buffer;
  servedEnvelopeBytes?: Buffer;
  includePrerelease?: boolean;
  onlyPrerelease?: boolean;
  malformedFeed?: boolean;
}

export function writeSignedUpdateFixture(root: string, options: UpdateFixtureOptions = {}): string {
  const dir = path.join(root, 'update-fixture');
  fs.mkdirSync(dir, { recursive: true });
  const tag = options.tag ?? 'v0.2.0';
  const version = tag.replace(/^v/, '');
  const arch = process.arch === 'arm64' ? 'arm64' : 'x64';
  const packageName =
    options.packageName ??
    (process.platform === 'darwin' ? 'Agentico-mac-universal.dmg' : `Agentico-${arch}.AppImage`);
  const packageBytes =
    options.packageBytes ?? Buffer.from(options.packageText ?? 'agentico update');
  const servedPackageBytes = options.servedPackageBytes ?? packageBytes;
  const packageFiles = [
    { name: 'Agentico-mac-universal.dmg', bytes: Buffer.from('fixture macos') },
    { name: 'Agentico-x64.AppImage', bytes: Buffer.from('fixture x64 appimage') },
    { name: 'Agentico-arm64.AppImage', bytes: Buffer.from('fixture arm64 appimage') },
    { name: `agentico_${version}_amd64.deb`, bytes: Buffer.from('fixture amd64 deb') },
    { name: `agentico_${version}_arm64.deb`, bytes: Buffer.from('fixture arm64 deb') },
  ].map((entry) => (entry.name === packageName ? { ...entry, bytes: packageBytes } : entry));
  const envelopeBytes = Buffer.from(
    `${JSON.stringify(
      {
        schema_version: 1,
        tag,
        version,
        commit: '0123456789abcdef0123456789abcdef01234567',
        artifacts: packageFiles.map(({ name, bytes }) => ({
          name,
          sha256: sha256(bytes),
          size: bytes.byteLength,
        })),
      },
      null,
      2,
    )}\n`,
  );
  const signature = sign(null, envelopeBytes, createPrivateKey(PRIVATE_KEY)).toString('base64');
  const envelopeSignature = Buffer.from(`agentico-ed25519:${signature}`);

  fs.writeFileSync(path.join(dir, packageName), servedPackageBytes);
  fs.writeFileSync(
    path.join(dir, 'desktop-release.json'),
    options.servedEnvelopeBytes ?? envelopeBytes,
  );
  fs.writeFileSync(path.join(dir, 'desktop-release.json.sig'), envelopeSignature);

  const assets = [
    ...packageFiles.map(({ name, bytes }) => asset(tag, name, bytes.byteLength)),
    asset(tag, 'desktop-release.json', envelopeBytes.byteLength),
    asset(tag, 'desktop-release.json.sig', envelopeSignature.byteLength),
  ];

  const releases = options.malformedFeed
    ? [{ tag_name: tag, draft: false, prerelease: false, assets }]
    : [
        ...(options.includePrerelease || options.onlyPrerelease
          ? [release('v0.3.0', assets, { prerelease: true })]
          : []),
        ...(options.onlyPrerelease ? [] : [release(tag, assets)]),
      ];
  const feedPath = path.join(dir, 'release-feed.json');
  fs.writeFileSync(feedPath, `${JSON.stringify(releases, null, 2)}\n`);
  return feedPath;
}

export function updatePackageName(format: 'macos' | 'appimage' | 'deb', tag = 'v0.2.0'): string {
  const arch = process.arch === 'arm64' ? 'arm64' : 'x64';
  if (format === 'macos') return 'Agentico-mac-universal.dmg';
  if (format === 'appimage') return `Agentico-${arch}.AppImage`;
  return `agentico_${tag.replace(/^v/, '')}_${arch === 'arm64' ? 'arm64' : 'amd64'}.deb`;
}

export function sha256(bytes: Buffer): string {
  return createHash('sha256').update(bytes).digest('hex');
}

function release(
  tag: string,
  assets: ReturnType<typeof asset>[],
  options: { prerelease?: boolean } = {},
) {
  return {
    tag_name: tag,
    draft: false,
    prerelease: options.prerelease ?? false,
    html_url: `https://github.com/doordash-oss/agentic-orchestrator/releases/tag/${tag}`,
    assets,
  };
}

function asset(tag: string, name: string, size: number) {
  return {
    name,
    size,
    browser_download_url: `https://github.com/doordash-oss/agentic-orchestrator/releases/download/${tag}/${name}`,
  };
}
