import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { VerifiedUpdatePackage } from './updates';

export interface AppliedUpdate {
  installedPath: string;
  previousPath: string;
}

export interface UpdateInstallerOptions {
  platform?: NodeJS.Platform;
  execPath?: string;
  appImagePath?: string;
  cleanupMarkerPath?: string;
  runCommand?: (command: string, args: readonly string[], options?: { cwd?: string }) => string;
}

class UpdateInstallError extends Error {}

export async function applyVerifiedUpdate(
  update: VerifiedUpdatePackage,
  options: UpdateInstallerOptions = {},
): Promise<AppliedUpdate> {
  const platform = options.platform ?? process.platform;
  let applied: AppliedUpdate;
  if (platform === 'darwin' && update.packageFormat === 'macos') {
    applied = applyMacOSUpdate(update, {
      execPath: options.execPath ?? process.execPath,
      runCommand: options.runCommand ?? runCommand,
    });
  } else if (platform === 'linux' && update.packageFormat === 'appimage') {
    applied = applyAppImageUpdate(update, {
      appImagePath: options.appImagePath,
      runCommand: options.runCommand ?? runCommand,
    });
  } else {
    throw new UpdateInstallError('This verified update package cannot be installed in app.');
  }
  if (options.cleanupMarkerPath !== undefined) {
    writeCleanupMarker(options.cleanupMarkerPath, update, applied);
  }
  return applied;
}

export function relaunchUpdatedApplication(
  update: VerifiedUpdatePackage,
  relaunch: (options?: { execPath?: string }) => void,
  appImagePath?: string,
): void {
  if (update.packageFormat === 'appimage') {
    if (appImagePath === undefined || !path.isAbsolute(appImagePath)) {
      throw new UpdateInstallError('The updated AppImage relaunch path was invalid.');
    }
    relaunch({ execPath: appImagePath });
    return;
  }
  relaunch();
}

export function cleanupAppliedUpdate(options: {
  platform?: NodeJS.Platform;
  execPath?: string;
  appImagePath?: string;
  currentVersion: string;
  cleanupMarkerPath: string;
}): void {
  let receipt: CleanupReceipt;
  try {
    receipt = parseCleanupReceipt(fs.readFileSync(options.cleanupMarkerPath, 'utf8'));
  } catch {
    return;
  }
  const platform = options.platform ?? process.platform;
  let installedPath: string;
  try {
    installedPath =
      platform === 'darwin'
        ? macAppBundleForExecutable(options.execPath ?? process.execPath)
        : (options.appImagePath ?? '');
  } catch {
    return;
  }
  if (
    receipt.targetVersion !== stripLeadingV(options.currentVersion) ||
    receipt.installedPath !== installedPath ||
    !validPreviousPath(receipt.previousPath, installedPath) ||
    !validStagedPackagePath(receipt.stagedPackagePath, options.cleanupMarkerPath)
  ) {
    return;
  }
  try {
    const previous = fs.lstatSync(receipt.previousPath);
    if (!previous.isDirectory() && !previous.isFile()) return;
    fs.rmSync(receipt.previousPath, { recursive: true, force: true });
    fs.rmSync(path.dirname(receipt.stagedPackagePath), { recursive: true, force: true });
    fs.rmSync(options.cleanupMarkerPath, { force: true });
  } catch {
    // Best effort after a successful relaunch; leave the receipt for retry.
  }
}

function applyMacOSUpdate(
  update: VerifiedUpdatePackage,
  options: {
    execPath: string;
    runCommand: (command: string, args: readonly string[], options?: { cwd?: string }) => string;
  },
): AppliedUpdate {
  assertStagedPackage(update.packagePath, '.dmg');
  assertTargetVersion(update.targetVersion);
  const targetApp = macAppBundleForExecutable(options.execPath);
  if (!isDirectory(targetApp)) {
    throw new UpdateInstallError('The running application bundle could not be located.');
  }

  const mountPoint = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-update-mount-'));
  const replacementPath = uniqueSiblingPath(targetApp, 'incoming');
  let failure: unknown = null;
  let attached = false;
  try {
    options.runCommand('/usr/bin/hdiutil', [
      'attach',
      update.packagePath,
      '-nobrowse',
      '-readonly',
      '-mountpoint',
      mountPoint,
    ]);
    attached = true;
    const sourceApp = path.join(mountPoint, 'Agentico.app');
    if (!isDirectory(sourceApp)) {
      throw new UpdateInstallError('The macOS update package did not contain Agentico.app.');
    }
    assertMacAppVersion(sourceApp, update.targetVersion, options.runCommand);
    verifyMacAppSignature(sourceApp, options.runCommand);
    options.runCommand('/usr/bin/ditto', [sourceApp, replacementPath]);
    assertMacAppVersion(replacementPath, update.targetVersion, options.runCommand);
    verifyMacAppSignature(replacementPath, options.runCommand);
  } catch (error) {
    failure = error;
  }

  if (attached) {
    try {
      options.runCommand('/usr/bin/hdiutil', ['detach', mountPoint, '-force']);
    } catch (error) {
      failure ??= error;
    }
  }
  fs.rmSync(mountPoint, { recursive: true, force: true });
  if (failure !== null) {
    fs.rmSync(replacementPath, { recursive: true, force: true });
    throw installError(failure, 'The macOS update package could not be prepared.');
  }

  return replaceAtomically(targetApp, replacementPath);
}

function applyAppImageUpdate(
  update: VerifiedUpdatePackage,
  options: {
    appImagePath?: string;
    runCommand: (command: string, args: readonly string[], options?: { cwd?: string }) => string;
  },
): AppliedUpdate {
  assertStagedPackage(update.packagePath, '.appimage');
  assertTargetVersion(update.targetVersion);
  const targetPath = options.appImagePath;
  if (targetPath === undefined || !path.isAbsolute(targetPath) || !isRegularFile(targetPath)) {
    throw new UpdateInstallError('The running AppImage could not be located.');
  }
  try {
    fs.accessSync(targetPath, fs.constants.W_OK);
  } catch {
    throw new UpdateInstallError('The running AppImage is not writable.');
  }

  const extractDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-update-appimage-'));
  try {
    fs.chmodSync(update.packagePath, 0o700);
    options.runCommand(update.packagePath, ['--appimage-extract'], { cwd: extractDir });
    const identityVersion = readDesktopVersion(
      path.join(extractDir, 'squashfs-root', 'resources', 'build-identity.json'),
      'The AppImage update package had no valid build identity.',
    );
    if (identityVersion !== update.targetVersion) {
      throw new UpdateInstallError(
        'The AppImage update package version did not match the selected release.',
      );
    }
  } catch (error) {
    throw installError(error, 'The AppImage update package could not be inspected.');
  } finally {
    fs.rmSync(extractDir, { recursive: true, force: true });
  }

  const replacementPath = uniqueSiblingPath(targetPath, 'incoming');
  try {
    fs.copyFileSync(update.packagePath, replacementPath, fs.constants.COPYFILE_EXCL);
    fs.chmodSync(replacementPath, 0o755);
    const fd = fs.openSync(replacementPath, 'r');
    try {
      fs.fsyncSync(fd);
    } finally {
      fs.closeSync(fd);
    }
  } catch (error) {
    fs.rmSync(replacementPath, { force: true });
    throw installError(error, 'The AppImage update package could not be prepared.');
  }
  return replaceAtomically(targetPath, replacementPath);
}

function replaceAtomically(targetPath: string, replacementPath: string): AppliedUpdate {
  const previousPath = uniqueSiblingPath(targetPath, 'previous');
  try {
    fs.renameSync(targetPath, previousPath);
  } catch (error) {
    fs.rmSync(replacementPath, { recursive: true, force: true });
    throw installError(error, 'The installed application could not be replaced.');
  }
  try {
    fs.renameSync(replacementPath, targetPath);
  } catch (error) {
    try {
      fs.renameSync(previousPath, targetPath);
    } catch {
      throw new UpdateInstallError(
        'The application update could not be completed or rolled back. Reinstall Agentico manually.',
      );
    }
    throw installError(error, 'The installed application could not be replaced atomically.');
  }
  return { installedPath: targetPath, previousPath };
}

function assertMacAppVersion(
  appPath: string,
  targetVersion: string,
  command: (command: string, args: readonly string[]) => string,
): void {
  const infoPlist = path.join(appPath, 'Contents', 'Info.plist');
  const bundleVersion = command('/usr/libexec/PlistBuddy', [
    '-c',
    'Print :CFBundleShortVersionString',
    infoPlist,
  ]).trim();
  const identityVersion = readDesktopVersion(
    path.join(appPath, 'Contents', 'Resources', 'build-identity.json'),
    'The macOS update package had no valid build identity.',
  );
  if (bundleVersion !== targetVersion || identityVersion !== targetVersion) {
    throw new UpdateInstallError(
      'The macOS update package version did not match the selected release.',
    );
  }
}

function verifyMacAppSignature(
  appPath: string,
  command: (command: string, args: readonly string[]) => string,
): void {
  try {
    command('/usr/bin/codesign', ['--verify', '--deep', '--strict', appPath]);
  } catch {
    throw new UpdateInstallError('The macOS update package failed code-signature verification.');
  }
}

function macAppBundleForExecutable(execPath: string): string {
  const resolved = path.resolve(execPath);
  const marker = `${path.sep}Contents${path.sep}MacOS${path.sep}`;
  const markerIndex = resolved.lastIndexOf(marker);
  const appPath = markerIndex < 0 ? '' : resolved.slice(0, markerIndex);
  if (!appPath.endsWith('.app')) {
    throw new UpdateInstallError('The running application is not a replaceable macOS app bundle.');
  }
  return appPath;
}

function assertStagedPackage(packagePath: string, extension: string): void {
  if (!path.isAbsolute(packagePath) || !packagePath.toLowerCase().endsWith(extension)) {
    throw new UpdateInstallError('The staged update package path was invalid.');
  }
  try {
    if (!isRegularFile(packagePath)) throw new Error('not a file');
  } catch {
    throw new UpdateInstallError('The staged update package was unavailable.');
  }
}

function assertTargetVersion(version: string): void {
  if (!/^\d+\.\d+\.\d+$/.test(version)) {
    throw new UpdateInstallError('The selected update version was invalid.');
  }
}

interface CleanupReceipt {
  schemaVersion: 1;
  targetVersion: string;
  installedPath: string;
  previousPath: string;
  stagedPackagePath: string;
}

function writeCleanupMarker(
  markerPath: string,
  update: VerifiedUpdatePackage,
  applied: AppliedUpdate,
): void {
  try {
    fs.mkdirSync(path.dirname(markerPath), { recursive: true, mode: 0o700 });
    const tempPath = `${markerPath}.${process.pid}.tmp`;
    fs.writeFileSync(
      tempPath,
      `${JSON.stringify({
        schemaVersion: 1,
        targetVersion: update.targetVersion,
        installedPath: applied.installedPath,
        previousPath: applied.previousPath,
        stagedPackagePath: update.packagePath,
      })}\n`,
      { mode: 0o600 },
    );
    fs.renameSync(tempPath, markerPath);
  } catch {
    // The update is already installed; a missing receipt only retains backup files.
  }
}

function parseCleanupReceipt(text: string): CleanupReceipt {
  const parsed = JSON.parse(text) as unknown;
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('invalid cleanup receipt');
  }
  const value = parsed as Record<string, unknown>;
  if (
    value.schemaVersion !== 1 ||
    typeof value.targetVersion !== 'string' ||
    typeof value.installedPath !== 'string' ||
    typeof value.previousPath !== 'string' ||
    typeof value.stagedPackagePath !== 'string'
  ) {
    throw new Error('invalid cleanup receipt');
  }
  return {
    schemaVersion: 1,
    targetVersion: value.targetVersion,
    installedPath: value.installedPath,
    previousPath: value.previousPath,
    stagedPackagePath: value.stagedPackagePath,
  };
}

function validPreviousPath(previousPath: string, installedPath: string): boolean {
  return (
    path.isAbsolute(previousPath) &&
    path.dirname(previousPath) === path.dirname(installedPath) &&
    path.basename(previousPath).startsWith(`.${path.basename(installedPath)}.agentico-previous-`)
  );
}

function validStagedPackagePath(packagePath: string, markerPath: string): boolean {
  const updatesRoot = path.resolve(path.dirname(markerPath));
  const stageDir = path.resolve(path.dirname(packagePath));
  return stageDir !== updatesRoot && stageDir.startsWith(`${updatesRoot}${path.sep}`);
}

function stripLeadingV(version: string): string {
  return version.startsWith('v') ? version.slice(1) : version;
}

function uniqueSiblingPath(targetPath: string, label: string): string {
  const parent = path.dirname(targetPath);
  const name = path.basename(targetPath);
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const candidate = path.join(
      parent,
      `.${name}.agentico-${label}-${process.pid}-${Date.now()}-${attempt}`,
    );
    if (!fs.existsSync(candidate)) return candidate;
  }
  throw new UpdateInstallError('A safe temporary update path could not be allocated.');
}

function isDirectory(candidate: string): boolean {
  try {
    return fs.lstatSync(candidate).isDirectory();
  } catch {
    return false;
  }
}

function isRegularFile(candidate: string): boolean {
  try {
    return fs.lstatSync(candidate).isFile();
  } catch {
    return false;
  }
}

function readDesktopVersion(identityPath: string, errorMessage: string): string {
  try {
    const identity = JSON.parse(fs.readFileSync(identityPath, 'utf8')) as unknown;
    const desktopVersion =
      typeof identity === 'object' && identity !== null && !Array.isArray(identity)
        ? (identity as Record<string, unknown>).desktop_version
        : undefined;
    if (typeof desktopVersion !== 'string') {
      throw new Error('missing desktop_version');
    }
    return desktopVersion;
  } catch {
    throw new UpdateInstallError(errorMessage);
  }
}

function runCommand(
  command: string,
  args: readonly string[],
  options: { cwd?: string } = {},
): string {
  return execFileSync(command, [...args], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    ...options,
  });
}

function installError(error: unknown, fallback: string): UpdateInstallError {
  return error instanceof UpdateInstallError ? error : new UpdateInstallError(fallback);
}
