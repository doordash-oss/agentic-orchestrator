/**
 * Main-process desktop update coordinator. Feed access, version comparison,
 * verified-action state, active-work decisions, and native restart control all
 * stay here; the renderer sees only the redacted UpdateState.
 */
import {
  createHash,
  createPublicKey,
  verify as verifyDetachedSignatureBytes,
  type KeyObject,
} from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import type { UpdateInstallNowRequest, UpdatePackageFormat, UpdateState } from '../shared/ipc';
import type { DiagnosticsService } from './diagnostics';

const REPO_API = 'https://api.github.com/repos/doordash-oss/agentic-orchestrator/releases';
const RELEASE_NOTES = 'https://github.com/doordash-oss/agentic-orchestrator/releases';
const CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000;
const CHECK_JITTER_MS = 20 * 60 * 1000;
const FETCH_TIMEOUT_MS = 8000;
const MAX_RELEASE_BYTES = 1024 * 1024;
const MAX_CHECKSUM_BYTES = 1024 * 1024;
const MAX_SIGNATURE_BYTES = 64 * 1024;
const MAX_PACKAGE_BYTES = 512 * 1024 * 1024;
const TRUSTED_DOWNLOAD_HOSTS = new Set([
  'api.github.com',
  'github.com',
  'objects.githubusercontent.com',
  'github-releases.githubusercontent.com',
]);
const DEFAULT_RELEASE_PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAmhM+TNlJSPzGSFwd/DakW3G6MzxCpouletrsW4WAezE=
-----END PUBLIC KEY-----`;

export interface UpdateActiveWork {
  featureCount: number;
  amaActive: boolean;
  detectionFailed: boolean;
}

export interface UpdateCoordinatorOptions {
  currentVersion: string;
  isPackaged: boolean;
  platform?: NodeJS.Platform;
  arch?: string;
  packageFormat?: UpdatePackageFormat;
  userDataDir: string;
  diagnostics?: DiagnosticsService;
  fetch?: typeof fetch;
  releasePublicKey?: string | KeyObject;
  canInstallInApp?: boolean;
  now?: () => Date;
  setTimeout?: typeof setTimeout;
  clearTimeout?: typeof clearTimeout;
  onStateChanged?: (state: UpdateState) => void;
  detectActiveWork(): Promise<UpdateActiveWork>;
  stopActiveWork(active: UpdateActiveWork): Promise<{ stopped: boolean; message?: string }>;
  restart(): void;
}

interface ReleaseAsset {
  name: string;
  size: number;
  browser_download_url: string;
}

interface ReleaseCandidate {
  tag_name: string;
  draft: boolean;
  prerelease: boolean;
  html_url: string;
  assets: ReleaseAsset[];
}

interface SelectedAsset {
  packageAsset: ReleaseAsset;
  checksumAsset?: ReleaseAsset;
  signatureAsset?: ReleaseAsset;
}

type FetchHeaders = Record<string, string>;

interface VerifiedMetadata {
  packageSha256: string;
  checksumAsset: string;
  signatureAsset: string;
}

interface StagedPackageMetadata {
  tag: string;
  package: string;
  checksum: string;
  signature: string;
  packageSha256: string;
}

export class UpdateCoordinator {
  private state: UpdateState;
  private inFlight: Promise<UpdateState> | null = null;
  private scheduledInstallInFlight: Promise<UpdateState> | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;

  private readonly fetchImpl: typeof fetch;
  private readonly now: () => Date;
  private readonly setTimer: typeof setTimeout;
  private readonly clearTimer: typeof clearTimeout;
  private readonly platform: NodeJS.Platform;
  private readonly arch: string;
  private readonly packageFormat: UpdatePackageFormat;
  private readonly releasePublicKey: string | KeyObject;
  private readonly canInstallInApp: boolean;

  constructor(private readonly options: UpdateCoordinatorOptions) {
    this.fetchImpl = options.fetch ?? fetch;
    this.now = options.now ?? (() => new Date());
    this.setTimer = options.setTimeout ?? setTimeout;
    this.clearTimer = options.clearTimeout ?? clearTimeout;
    this.platform = options.platform ?? process.platform;
    this.arch = options.arch ?? process.arch;
    this.packageFormat =
      options.packageFormat ?? detectPackageFormat(this.platform, {}, process.execPath);
    this.releasePublicKey = options.releasePublicKey ?? DEFAULT_RELEASE_PUBLIC_KEY;
    this.canInstallInApp = options.canInstallInApp ?? this.packageFormat !== 'deb';
    this.state = {
      status: 'idle',
      currentVersion: stripLeadingV(options.currentVersion),
      packageFormat: this.packageFormat,
      signatureStatus: 'unknown',
      nextCheckAt: this.nextCheckAt(this.nextCheckDelayMs()).toISOString(),
      message: options.isPackaged
        ? 'Update checks will run after the runtime is ready.'
        : 'Development builds are not updated in place.',
      guidance: options.isPackaged
        ? undefined
        : ['Pull the source checkout and rebuild, or install a signed desktop package.'],
    };
  }

  getState(): UpdateState {
    return this.state;
  }

  async refreshActiveWorkSummary(): Promise<UpdateState> {
    if (!isInstallable(this.state)) {
      return this.state;
    }
    this.state = {
      ...this.state,
      activeWorkSummary: await this.activeWorkSummaryForInstallSurface(),
    };
    this.notify();
    return this.state;
  }

  startAutomaticChecks(): void {
    if (!this.options.isPackaged) {
      return;
    }
    if (this.timer !== null) {
      return;
    }
    void this.checkNow();
    this.scheduleNext();
  }

  stopAutomaticChecks(): void {
    if (this.timer !== null) {
      this.clearTimer(this.timer);
      this.timer = null;
    }
  }

  checkNow(): Promise<UpdateState> {
    if (this.inFlight !== null) {
      return this.inFlight;
    }
    this.inFlight = this.performCheck()
      .then(() => {
        this.notify();
        return this.state;
      })
      .finally(() => {
        this.inFlight = null;
      });
    return this.inFlight;
  }

  async installWhenIdle(): Promise<UpdateState> {
    if (!isInstallable(this.state)) {
      return this.fail('No verified update is ready to install.');
    }
    const active = await this.options.detectActiveWork();
    if (!active.detectionFailed && active.featureCount === 0 && !active.amaActive) {
      return this.restartToUpdate();
    }
    this.state = {
      ...this.state,
      status: 'scheduled',
      activeWorkSummary: activeSummary(active),
      message: 'Update installation is scheduled for the next idle window.',
    };
    this.options.diagnostics?.record('update', 'info', this.state.message);
    this.notify();
    return this.state;
  }

  reconcileScheduledInstall(): Promise<UpdateState> {
    if (this.state.status !== 'scheduled' || !isInstallable(this.state)) {
      return Promise.resolve(this.state);
    }
    if (this.scheduledInstallInFlight !== null) {
      return this.scheduledInstallInFlight;
    }
    this.scheduledInstallInFlight = this.performScheduledInstallReconciliation().finally(() => {
      this.scheduledInstallInFlight = null;
    });
    return this.scheduledInstallInFlight;
  }

  async installNow(request: UpdateInstallNowRequest): Promise<UpdateState> {
    if (!request.consent) {
      return this.fail('Installing an update requires explicit consent.');
    }
    if (!isInstallable(this.state)) {
      return this.fail('No verified update is ready to install.');
    }
    const active = await this.options.detectActiveWork();
    if (
      (active.featureCount > 0 || active.amaActive || active.detectionFailed) &&
      !request.stopActiveWork
    ) {
      this.state = {
        ...this.state,
        activeWorkSummary: activeSummary(active),
        message: 'Active work must be stopped before installing now.',
      };
      this.options.diagnostics?.record('update', 'warn', this.state.message);
      this.notify();
      return this.state;
    }
    if (request.stopActiveWork) {
      const result = await this.options.stopActiveWork(active);
      if (!result.stopped) {
        this.state = {
          ...this.state,
          status: 'ready',
          activeWorkSummary: activeSummary(active),
          message: result.message ?? 'Active work could not be stopped safely. Retry or cancel.',
        };
        this.options.diagnostics?.record('update', 'warn', this.state.message);
        this.notify();
        return this.state;
      }
    }
    return this.restartToUpdate();
  }

  restartToUpdate(): UpdateState {
    if (this.state.status === 'installing') {
      return this.state;
    }
    if (!isInstallable(this.state)) {
      return this.fail('No verified update is ready to install.');
    }
    this.state = {
      ...this.state,
      status: 'installing',
      message: 'Restarting to apply the verified update.',
      activeWorkSummary: undefined,
    };
    this.options.diagnostics?.record('update', 'info', this.state.message);
    this.notify();
    this.options.restart();
    return this.state;
  }

  private async performCheck(): Promise<UpdateState> {
    if (!this.options.isPackaged) {
      this.state = {
        ...this.state,
        status: 'current',
        checkedAt: this.now().toISOString(),
        message: 'Development builds are not updated in place.',
        guidance: ['Pull the source checkout and rebuild, or install a signed desktop package.'],
      };
      return this.state;
    }

    this.state = {
      ...this.state,
      status: 'checking',
      checkedAt: this.now().toISOString(),
      message: 'Checking the stable GitHub Releases feed.',
    };
    this.options.diagnostics?.record('update', 'info', this.state.message);

    try {
      const latest = await this.fetchLatestStableRelease();
      const current = parseSemver(this.state.currentVersion);
      const target = parseSemver(latest.tag_name);
      if (target === null || current === null) {
        return this.fail('The release feed did not contain a compatible SemVer identity.');
      }
      const versionComparison = compareSemver(target, current);
      if (versionComparison < 0) {
        return this.fail('The release feed offered an older version; downgrade rejected.', {
          releaseNotesUrl: latest.html_url,
        });
      }
      if (versionComparison === 0) {
        this.state = {
          ...this.state,
          status: 'current',
          signatureStatus: 'unknown',
          targetVersion: undefined,
          releaseNotesUrl: latest.html_url,
          message: 'Agentico is up to date.',
          guidance: undefined,
        };
        return this.state;
      }

      const selected = selectAsset(latest.assets, this.platform, this.arch, this.packageFormat);
      if (selected === null) {
        return this.fail(
          `No ${this.packageFormat} update package is available for ${this.platform}/${this.arch}.`,
          {
            targetVersion: stripLeadingV(latest.tag_name),
            releaseNotesUrl: latest.html_url,
            guidance: ['Open the release notes to choose a signed artifact manually.'],
          },
        );
      }
      const metadata = await this.verifyReleaseMetadata(selected);
      if (metadata === null) {
        this.state = {
          ...this.state,
          status: 'available',
          targetVersion: stripLeadingV(latest.tag_name),
          releaseNotesUrl: latest.html_url,
          signatureStatus: 'unknown',
          message: 'An update is available, but signed checksum metadata is incomplete.',
          guidance: ['Open the release notes and verify the signed artifact manually.'],
        };
        return this.state;
      }

      if (this.packageFormat === 'deb') {
        this.state = {
          ...this.state,
          status: 'available',
          targetVersion: stripLeadingV(latest.tag_name),
          releaseNotesUrl: latest.html_url,
          signatureStatus: 'verified',
          message: 'A verified DEB update is available.',
          guidance: [
            'DEB installs are updated by the package manager, not by in-app replacement.',
            'Download the signed DEB and checksum from the GitHub release.',
            `Install with: sudo apt install ./${selected.packageAsset.name}`,
          ],
        };
        return this.state;
      }

      if (!this.canInstallInApp) {
        this.state = {
          ...this.state,
          status: 'available',
          targetVersion: stripLeadingV(latest.tag_name),
          releaseNotesUrl: latest.html_url,
          signatureStatus: 'verified',
          message: 'A verified update is available for manual signed installation.',
          guidance: [
            'This AppImage location cannot be safely replaced in app.',
            `Download ${selected.packageAsset.name} and ${metadata.checksumAsset} from the GitHub release.`,
            'Verify the checksum before replacing the AppImage manually.',
          ],
        };
        return this.state;
      }

      const resumed = await this.tryResumeStagedPackage(latest, selected, metadata);
      if (!resumed) {
        await this.downloadAndStagePackage(latest, selected, metadata);
      }
      this.state = {
        ...this.state,
        status: 'ready',
        targetVersion: stripLeadingV(latest.tag_name),
        releaseNotesUrl: latest.html_url,
        signatureStatus: 'verified',
        progress: undefined,
        message: 'A verified update is downloaded and ready to install.',
        guidance: undefined,
        activeWorkSummary: await this.activeWorkSummaryForInstallSurface(),
      };
      return this.state;
    } catch (error) {
      return this.fail(safeMessage(error));
    } finally {
      this.scheduleNext();
    }
  }

  private async fetchLatestStableRelease(): Promise<ReleaseCandidate> {
    const json = await fetchJson(this.fetchImpl, REPO_API, MAX_RELEASE_BYTES);
    const releases = Array.isArray(json) ? json : [json];
    const candidates = releases
      .map(parseReleaseCandidate)
      .filter((r): r is ReleaseCandidate => r !== null);
    const stable = candidates
      .filter(
        (release) =>
          !release.draft && !release.prerelease && parseSemver(release.tag_name) !== null,
      )
      .sort((a, b) => compareSemver(parseSemver(b.tag_name)!, parseSemver(a.tag_name)!))[0];
    if (stable === undefined) {
      throw new Error('The release feed did not contain a stable SemVer release.');
    }
    return stable;
  }

  private async verifyReleaseMetadata(selected: SelectedAsset): Promise<VerifiedMetadata | null> {
    if (selected.checksumAsset === undefined || selected.signatureAsset === undefined) {
      return null;
    }
    const checksumBytes = await fetchBytes(
      this.fetchImpl,
      selected.checksumAsset.browser_download_url,
      MAX_CHECKSUM_BYTES,
      FETCH_TIMEOUT_MS,
    );
    const signatureBytes = await fetchBytes(
      this.fetchImpl,
      selected.signatureAsset.browser_download_url,
      MAX_SIGNATURE_BYTES,
      FETCH_TIMEOUT_MS,
    );
    if (!verifyReleaseSignature(checksumBytes, signatureBytes, this.releasePublicKey)) {
      this.state = { ...this.state, signatureStatus: 'failed' };
      throw new Error('Signed update metadata could not be verified.');
    }
    return {
      packageSha256: checksumForPackage(checksumBytes.toString('utf8'), selected.packageAsset.name),
      checksumAsset: selected.checksumAsset.name,
      signatureAsset: selected.signatureAsset.name,
    };
  }

  private async downloadAndStagePackage(
    release: ReleaseCandidate,
    selected: SelectedAsset,
    metadata: VerifiedMetadata,
  ): Promise<void> {
    if (selected.packageAsset.size <= 0 || selected.packageAsset.size > MAX_PACKAGE_BYTES) {
      throw new Error('The update package size is outside the allowed bounds.');
    }
    this.state = {
      ...this.state,
      status: 'downloading',
      targetVersion: stripLeadingV(release.tag_name),
      releaseNotesUrl: release.html_url,
      signatureStatus: 'verified',
      progress: { downloadedBytes: 0, totalBytes: selected.packageAsset.size },
      message: 'Downloading the verified update package.',
    };
    this.notify();
    const packageBytes = await fetchBytes(
      this.fetchImpl,
      selected.packageAsset.browser_download_url,
      MAX_PACKAGE_BYTES,
      60_000,
    );
    if (packageBytes.byteLength !== selected.packageAsset.size) {
      this.state = { ...this.state, signatureStatus: 'failed' };
      throw new Error('The update package download was incomplete.');
    }
    const packageSha256 = sha256Buffer(packageBytes);
    if (packageSha256 !== metadata.packageSha256) {
      this.state = { ...this.state, signatureStatus: 'failed' };
      throw new Error('The update package checksum did not match signed metadata.');
    }
    const stageDir = path.join(this.options.userDataDir, 'updates', release.tag_name);
    fs.mkdirSync(stageDir, { recursive: true, mode: 0o700 });
    const packagePath = path.join(stageDir, safeAssetName(selected.packageAsset.name));
    writeFileAtomic(packagePath, packageBytes, 0o600);
    const metadataPath = path.join(stageDir, 'selected-asset.json');
    writeFileAtomic(
      metadataPath,
      Buffer.from(
        `${JSON.stringify(
          {
            tag: release.tag_name,
            package: selected.packageAsset.name,
            checksum: metadata.checksumAsset,
            signature: metadata.signatureAsset,
            packageSha256,
          },
          null,
          2,
        )}\n`,
      ),
      0o600,
    );
    this.options.diagnostics?.record(
      'update',
      'info',
      'Verified update package staged.',
      `${release.tag_name} ${selected.packageAsset.name}`,
    );
  }

  private async tryResumeStagedPackage(
    release: ReleaseCandidate,
    selected: SelectedAsset,
    metadata: VerifiedMetadata,
  ): Promise<boolean> {
    if (selected.packageAsset.size <= 0 || selected.packageAsset.size > MAX_PACKAGE_BYTES) {
      return false;
    }
    const stageDir = path.join(this.options.userDataDir, 'updates', release.tag_name);
    const metadataPath = path.join(stageDir, 'selected-asset.json');
    let stagedMetadata: StagedPackageMetadata;
    try {
      stagedMetadata = parseStagedPackageMetadata(fs.readFileSync(metadataPath, 'utf8'));
    } catch {
      return false;
    }
    if (
      stagedMetadata.tag !== release.tag_name ||
      stagedMetadata.package !== selected.packageAsset.name ||
      stagedMetadata.checksum !== metadata.checksumAsset ||
      stagedMetadata.signature !== metadata.signatureAsset ||
      stagedMetadata.packageSha256 !== metadata.packageSha256
    ) {
      return false;
    }
    const packagePath = path.join(stageDir, safeAssetName(selected.packageAsset.name));
    let packageBytes: Buffer;
    try {
      packageBytes = fs.readFileSync(packagePath);
    } catch {
      return false;
    }
    if (
      packageBytes.byteLength !== selected.packageAsset.size ||
      sha256Buffer(packageBytes) !== metadata.packageSha256
    ) {
      return false;
    }
    this.options.diagnostics?.record(
      'update',
      'info',
      'Verified staged update package resumed.',
      `${release.tag_name} ${selected.packageAsset.name}`,
    );
    return true;
  }

  private scheduleNext(): void {
    if (!this.options.isPackaged) {
      return;
    }
    if (this.timer !== null) {
      this.clearTimer(this.timer);
    }
    const delay = this.nextCheckDelayMs();
    this.state = { ...this.state, nextCheckAt: this.nextCheckAt(delay).toISOString() };
    this.timer = this.setTimer(() => {
      this.timer = null;
      void this.checkNow();
    }, delay);
  }

  private nextCheckDelayMs(): number {
    return CHECK_INTERVAL_MS + Math.floor(Math.random() * CHECK_JITTER_MS);
  }

  private nextCheckAt(delayMs: number): Date {
    return new Date(this.now().getTime() + delayMs);
  }

  private fail(
    message: string,
    patch: Partial<Pick<UpdateState, 'targetVersion' | 'releaseNotesUrl' | 'guidance'>> = {},
  ): UpdateState {
    this.state = {
      ...this.state,
      ...patch,
      status: 'failed',
      signatureStatus: this.state.signatureStatus,
      message,
    };
    this.options.diagnostics?.record('update', 'warn', message);
    this.notify();
    return this.state;
  }

  private notify(): void {
    this.options.onStateChanged?.(this.state);
  }

  private async activeWorkSummaryForInstallSurface(): Promise<string | undefined> {
    try {
      const active = await this.options.detectActiveWork();
      if (active.detectionFailed || active.featureCount > 0 || active.amaActive) {
        return activeSummary(active);
      }
    } catch {
      return activeSummary({ featureCount: 0, amaActive: false, detectionFailed: true });
    }
    return undefined;
  }

  private async performScheduledInstallReconciliation(): Promise<UpdateState> {
    let active: UpdateActiveWork;
    try {
      active = await this.options.detectActiveWork();
    } catch {
      active = { featureCount: 0, amaActive: false, detectionFailed: true };
    }
    if (!active.detectionFailed && active.featureCount === 0 && !active.amaActive) {
      this.options.diagnostics?.record(
        'update',
        'info',
        'Scheduled update consent remained valid after work went idle.',
      );
      return this.restartToUpdate();
    }
    this.state = {
      ...this.state,
      status: 'scheduled',
      activeWorkSummary: activeSummary(active),
      message: 'Update installation is scheduled for the next idle window.',
    };
    this.notify();
    return this.state;
  }
}

export function detectPackageFormat(
  platform: NodeJS.Platform,
  env: NodeJS.ProcessEnv,
  execPath: string,
): UpdatePackageFormat {
  const forced = env.AGENTICO_UPDATE_PACKAGE_FORMAT;
  if (forced === 'macos' || forced === 'appimage' || forced === 'deb' || forced === 'unknown') {
    return forced;
  }
  if (platform === 'darwin') return 'macos';
  if (platform === 'linux') {
    if (env.APPIMAGE !== undefined || execPath.endsWith('.AppImage')) return 'appimage';
    if (execPath.startsWith('/opt/Agentico/') || execPath.startsWith('/usr/')) return 'deb';
    return 'appimage';
  }
  return 'unknown';
}

export function detectCanInstallInApp(
  format: UpdatePackageFormat,
  env: NodeJS.ProcessEnv,
  execPath: string,
  access: (path: string, mode: number) => void = fs.accessSync,
): boolean {
  const forced = env.AGENTICO_UPDATE_INSTALL_MODE;
  if (forced === 'in-app') return format !== 'deb';
  if (forced === 'guidance') return false;
  if (format === 'macos') return true;
  if (format === 'deb' || format === 'unknown') return false;
  const appImagePath = env.APPIMAGE ?? (execPath.endsWith('.AppImage') ? execPath : undefined);
  if (appImagePath === undefined || !path.isAbsolute(appImagePath)) return false;
  try {
    access(appImagePath, fs.constants.W_OK);
    return true;
  } catch {
    return false;
  }
}

export function createUpdateFixtureFetch(feedPath: string): typeof fetch {
  const fixtureDir = path.dirname(feedPath);
  const fixtureFetch: typeof fetch = async (input) => {
    const url = requestUrl(input);
    if (url === REPO_API) {
      return fileResponse(feedPath, 'application/vnd.github+json');
    }
    let basename: string;
    try {
      basename = safeAssetName(decodeURIComponent(path.basename(new URL(url).pathname)));
    } catch {
      return new Response('invalid fixture URL', { status: 400 });
    }
    const assetPath = path.join(fixtureDir, basename);
    if (!assetPath.startsWith(`${fixtureDir}${path.sep}`) || !fs.existsSync(assetPath)) {
      return new Response('fixture asset not found', { status: 404 });
    }
    return fileResponse(assetPath, 'application/octet-stream');
  };
  return fixtureFetch;
}

function parseReleaseCandidate(value: unknown): ReleaseCandidate | null {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null;
  const v = value as Record<string, unknown>;
  if (
    typeof v.tag_name !== 'string' ||
    typeof v.draft !== 'boolean' ||
    typeof v.prerelease !== 'boolean' ||
    typeof v.html_url !== 'string' ||
    !Array.isArray(v.assets)
  ) {
    return null;
  }
  const assets = v.assets
    .map((asset) => {
      if (typeof asset !== 'object' || asset === null || Array.isArray(asset)) return null;
      const a = asset as Record<string, unknown>;
      return typeof a.name === 'string' &&
        typeof a.size === 'number' &&
        typeof a.browser_download_url === 'string' &&
        a.browser_download_url.startsWith(`${RELEASE_NOTES}/download/`)
        ? {
            name: a.name,
            size: a.size,
            browser_download_url: a.browser_download_url,
          }
        : null;
    })
    .filter((asset): asset is ReleaseAsset => asset !== null);
  return {
    tag_name: v.tag_name,
    draft: v.draft,
    prerelease: v.prerelease,
    html_url: v.html_url,
    assets,
  };
}

function parseStagedPackageMetadata(text: string): StagedPackageMetadata {
  const parsed = JSON.parse(text) as unknown;
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('staged update metadata is not an object');
  }
  const candidate = parsed as Record<string, unknown>;
  if (
    typeof candidate.tag !== 'string' ||
    typeof candidate.package !== 'string' ||
    typeof candidate.checksum !== 'string' ||
    typeof candidate.signature !== 'string' ||
    typeof candidate.packageSha256 !== 'string'
  ) {
    throw new Error('staged update metadata is incomplete');
  }
  return {
    tag: candidate.tag,
    package: candidate.package,
    checksum: candidate.checksum,
    signature: candidate.signature,
    packageSha256: candidate.packageSha256,
  };
}

function selectAsset(
  assets: readonly ReleaseAsset[],
  platform: NodeJS.Platform,
  arch: string,
  format: UpdatePackageFormat,
): SelectedAsset | null {
  const archNeedle = arch === 'arm64' ? /(arm64|aarch64)/i : /(x64|amd64|x86_64)/i;
  const candidates = assets.filter((asset) => {
    if (format === 'macos')
      return /\.(dmg|zip)$/i.test(asset.name) && /mac|darwin/i.test(asset.name);
    if (format === 'appimage')
      return /\.AppImage$/i.test(asset.name) && archNeedle.test(asset.name);
    if (format === 'deb') return /\.deb$/i.test(asset.name) && archNeedle.test(asset.name);
    return platform === 'darwin' ? /\.(dmg|zip)$/i.test(asset.name) : archNeedle.test(asset.name);
  });
  const packageAsset = candidates[0];
  if (packageAsset === undefined) return null;
  const checksumAsset = assets.find((asset) => /sha256|checksums?/i.test(asset.name));
  const signatureAsset =
    checksumAsset === undefined
      ? undefined
      : assets.find(
          (asset) =>
            asset.name === `${checksumAsset.name}.sig` ||
            asset.name === `${checksumAsset.name}.asc`,
        );
  return { packageAsset, checksumAsset, signatureAsset };
}

function isInstallable(state: UpdateState): boolean {
  return (
    (state.status === 'ready' || state.status === 'scheduled') &&
    state.signatureStatus === 'verified'
  );
}

function activeSummary(active: UpdateActiveWork): string {
  if (active.detectionFailed) return 'Active work status could not be verified.';
  const parts: string[] = [];
  if (active.featureCount > 0) {
    parts.push(`${active.featureCount} workflow${active.featureCount === 1 ? '' : 's'}`);
  }
  if (active.amaActive) parts.push('AMA session');
  return parts.length === 0 ? 'No active workflows or AMA sessions.' : parts.join(' and ');
}

async function fetchJson(fetchImpl: typeof fetch, url: string, maxBytes: number): Promise<unknown> {
  const bytes = await fetchBytes(fetchImpl, url, maxBytes, FETCH_TIMEOUT_MS, {
    Accept: 'application/vnd.github+json',
  });
  return JSON.parse(bytes.toString('utf8')) as unknown;
}

async function fetchBytes(
  fetchImpl: typeof fetch,
  url: string,
  maxBytes: number,
  timeoutMs: number,
  headers: FetchHeaders = {},
): Promise<Buffer> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetchWithTrustedRedirect(fetchImpl, url, controller.signal, headers);
    const length = response.headers.get('content-length');
    if (length !== null && Number(length) > maxBytes) {
      throw new Error('The update response exceeded the size limit.');
    }
    if (!response.ok) {
      throw new Error(`GitHub Releases returned HTTP ${response.status}.`);
    }
    const bytes = Buffer.from(await response.arrayBuffer());
    if (bytes.byteLength > maxBytes) {
      throw new Error('The update response exceeded the size limit.');
    }
    return bytes;
  } finally {
    clearTimeout(timer);
  }
}

async function fetchWithTrustedRedirect(
  fetchImpl: typeof fetch,
  url: string,
  signal: AbortSignal,
  headers: FetchHeaders,
): Promise<Response> {
  let current = url;
  for (let redirectCount = 0; redirectCount < 3; redirectCount += 1) {
    assertTrustedDownloadUrl(current);
    const response = await fetchImpl(current, {
      method: 'GET',
      redirect: 'manual',
      signal,
      headers,
    });
    if (!isRedirectStatus(response.status)) {
      return response;
    }
    const location = response.headers.get('location');
    if (location === null) {
      throw new Error('The update download redirected without a Location header.');
    }
    current = new URL(location, current).toString();
    assertTrustedDownloadUrl(current);
  }
  throw new Error('The update download redirected too many times.');
}

function isRedirectStatus(status: number): boolean {
  return status >= 300 && status < 400;
}

function assertTrustedDownloadUrl(url: string): void {
  const parsed = new URL(url);
  if (parsed.protocol !== 'https:' || !TRUSTED_DOWNLOAD_HOSTS.has(parsed.hostname)) {
    throw new Error('The update feed referenced an untrusted download host.');
  }
}

function verifyReleaseSignature(
  payload: Buffer,
  signaturePayload: Buffer,
  publicKey: string | KeyObject,
): boolean {
  try {
    const key = typeof publicKey === 'string' ? createPublicKey(publicKey) : publicKey;
    return verifyDetachedSignatureBytes(null, payload, key, parseSignature(signaturePayload));
  } catch {
    return false;
  }
}

function parseSignature(signaturePayload: Buffer): Buffer {
  const text = signaturePayload.toString('utf8').trim();
  const match = /^agentico-ed25519:([A-Za-z0-9+/=]+)$/.exec(text);
  if (match === null) {
    throw new Error('Unsupported update signature format.');
  }
  const encoded = match[1];
  if (encoded === undefined) {
    throw new Error('Unsupported update signature format.');
  }
  return Buffer.from(encoded, 'base64');
}

function checksumForPackage(checksumText: string, packageName: string): string {
  for (const line of checksumText.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (trimmed === '') continue;
    const match = /^([a-fA-F0-9]{64})\s+\*?(.+)$/.exec(trimmed);
    const digest = match?.[1];
    const candidateName = match?.[2];
    if (
      digest !== undefined &&
      candidateName !== undefined &&
      path.basename(candidateName) === packageName
    ) {
      return digest.toLowerCase();
    }
  }
  throw new Error('Signed checksum metadata did not name the selected update package.');
}

function writeFileAtomic(filePath: string, data: Buffer, mode: number): void {
  const tempPath = `${filePath}.${process.pid}.${Date.now()}.tmp`;
  fs.writeFileSync(tempPath, data, { mode });
  fs.renameSync(tempPath, filePath);
  fs.chmodSync(filePath, mode);
}

function parseSemver(value: string): [number, number, number] | null {
  const match = /^v?([0-9]+)\.([0-9]+)\.([0-9]+)$/.exec(value.trim());
  if (match === null) return null;
  return [Number(match[1]), Number(match[2]), Number(match[3])];
}

function compareSemver(a: [number, number, number], b: [number, number, number]): number {
  return a[0] - b[0] || a[1] - b[1] || a[2] - b[2];
}

function stripLeadingV(value: string): string {
  return value.startsWith('v') ? value.slice(1) : value;
}

function safeMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Update check failed.';
}

function sha256Buffer(value: Buffer): string {
  return createHash('sha256').update(value).digest('hex');
}

function safeAssetName(name: string): string {
  return path.basename(name).replace(/[^A-Za-z0-9._-]/g, '_');
}

function requestUrl(input: Parameters<typeof fetch>[0]): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

function fileResponse(filePath: string, contentType: string): Response {
  const bytes = fs.readFileSync(filePath);
  return new Response(new Uint8Array(bytes), {
    status: 200,
    headers: {
      'content-length': String(bytes.byteLength),
      'content-type': contentType,
    },
  });
}
