import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { vi } from 'vitest';

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-update-installer-'));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('applyVerifiedUpdate', () => {
  it('atomically replaces the running macOS app with the verified release app', async () => {
    const installer = await loadInstaller();
    const { execPath, appPath } = installedMacApp('old application');
    const packagePath = path.join(dir, 'Agentico-mac-universal.dmg');
    fs.writeFileSync(packagePath, 'verified dmg bytes');

    const result = await installer.applyVerifiedUpdate(
      { packageFormat: 'macos', packagePath, targetVersion: '0.2.0' },
      {
        platform: 'darwin',
        execPath,
        runCommand: macCommandRunner('0.2.0', 'new application'),
      },
    );

    expect(fs.readFileSync(path.join(appPath, 'marker.txt'), 'utf8')).toBe('new application');
    expect(fs.readFileSync(path.join(result.previousPath, 'marker.txt'), 'utf8')).toBe(
      'old application',
    );
  });

  it('leaves the installed macOS app untouched when the package version is wrong', async () => {
    const installer = await loadInstaller();
    const { execPath, appPath } = installedMacApp('old application');
    const packagePath = path.join(dir, 'Agentico-mac-universal.dmg');
    fs.writeFileSync(packagePath, 'verified dmg bytes');

    await expect(
      installer.applyVerifiedUpdate(
        { packageFormat: 'macos', packagePath, targetVersion: '0.2.0' },
        {
          platform: 'darwin',
          execPath,
          runCommand: macCommandRunner('0.3.0', 'wrong application'),
        },
      ),
    ).rejects.toThrow('version did not match');
    expect(fs.readFileSync(path.join(appPath, 'marker.txt'), 'utf8')).toBe('old application');
  });

  it('atomically replaces a writable AppImage after validating its embedded build identity', async () => {
    const installer = await loadInstaller();
    const appImagePath = path.join(dir, 'install', 'Agentico.AppImage');
    const packagePath = path.join(dir, 'staged', 'Agentico-x64.AppImage');
    fs.mkdirSync(path.dirname(appImagePath), { recursive: true });
    fs.mkdirSync(path.dirname(packagePath), { recursive: true });
    fs.writeFileSync(appImagePath, 'old appimage');
    fs.writeFileSync(packagePath, 'new appimage');
    fs.chmodSync(appImagePath, 0o755);

    const result = await installer.applyVerifiedUpdate(
      { packageFormat: 'appimage', packagePath, targetVersion: '0.2.0' },
      {
        platform: 'linux',
        execPath: '/tmp/AppRun',
        appImagePath,
        runCommand: appImageCommandRunner('0.2.0'),
      },
    );

    expect(fs.readFileSync(appImagePath, 'utf8')).toBe('new appimage');
    expect(fs.readFileSync(result.previousPath, 'utf8')).toBe('old appimage');
    expect(fs.statSync(appImagePath).mode & 0o777).toBe(0o755);
  });

  it('relaunches AppImage updates through the stable installed image path', async () => {
    const installer = await loadInstaller();
    const relaunch = vi.fn();

    installer.relaunchUpdatedApplication(
      {
        packageFormat: 'appimage',
        packagePath: '/tmp/staged/Agentico-x64.AppImage',
        targetVersion: '0.2.0',
      },
      relaunch,
      '/opt/Agentico.AppImage',
    );

    expect(relaunch).toHaveBeenCalledWith({ execPath: '/opt/Agentico.AppImage' });
  });

  it('cleans the rollback app and staged package only after the new version starts', async () => {
    const installer = await loadInstaller();
    const { execPath, appPath } = installedMacApp('old application');
    const updatesDir = path.join(dir, 'user-data', 'updates');
    const stageDir = path.join(updatesDir, 'v0.2.0');
    const packagePath = path.join(stageDir, 'Agentico-mac-universal.dmg');
    const cleanupMarkerPath = path.join(updatesDir, 'install-cleanup.json');
    fs.mkdirSync(stageDir, { recursive: true });
    fs.writeFileSync(packagePath, 'verified dmg bytes');

    const result = await installer.applyVerifiedUpdate(
      { packageFormat: 'macos', packagePath, targetVersion: '0.2.0' },
      {
        platform: 'darwin',
        execPath,
        cleanupMarkerPath,
        runCommand: macCommandRunner('0.2.0', 'new application'),
      },
    );
    expect(fs.existsSync(result.previousPath)).toBe(true);
    expect(fs.existsSync(packagePath)).toBe(true);

    installer.cleanupAppliedUpdate({
      platform: 'darwin',
      execPath,
      currentVersion: '0.2.0',
      cleanupMarkerPath,
    });

    expect(fs.readFileSync(path.join(appPath, 'marker.txt'), 'utf8')).toBe('new application');
    expect(fs.existsSync(result.previousPath)).toBe(false);
    expect(fs.existsSync(stageDir)).toBe(false);
    expect(fs.existsSync(cleanupMarkerPath)).toBe(false);
  });
});

interface InstallerModule {
  applyVerifiedUpdate(
    update: {
      packageFormat: 'macos' | 'appimage';
      packagePath: string;
      targetVersion: string;
    },
    options: {
      platform: NodeJS.Platform;
      execPath: string;
      appImagePath?: string;
      cleanupMarkerPath?: string;
      runCommand(command: string, args: readonly string[], options?: { cwd?: string }): string;
    },
  ): Promise<{ previousPath: string }>;
  relaunchUpdatedApplication(
    update: {
      packageFormat: 'macos' | 'appimage';
      packagePath: string;
      targetVersion: string;
    },
    relaunch: (options?: { execPath?: string }) => void,
    appImagePath?: string,
  ): void;
  cleanupAppliedUpdate(options: {
    platform: NodeJS.Platform;
    execPath: string;
    appImagePath?: string;
    currentVersion: string;
    cleanupMarkerPath: string;
  }): void;
}

async function loadInstaller(): Promise<InstallerModule> {
  const specifier = ['..', 'updateInstaller'].join('/');
  const loaded = (await import(/* @vite-ignore */ specifier).catch(
    () => null,
  )) as InstallerModule | null;
  expect(loaded, 'the production update installer module exists').not.toBeNull();
  return loaded!;
}

function installedMacApp(marker: string): { execPath: string; appPath: string } {
  const appPath = path.join(dir, 'Applications', 'Agentico.app');
  const execPath = path.join(appPath, 'Contents', 'MacOS', 'Agentico');
  fs.mkdirSync(path.dirname(execPath), { recursive: true });
  fs.writeFileSync(execPath, 'old executable');
  fs.writeFileSync(path.join(appPath, 'marker.txt'), marker);
  return { execPath, appPath };
}

function macCommandRunner(version: string, marker: string) {
  return (command: string, args: readonly string[]): string => {
    if (command === '/usr/bin/hdiutil' && args[0] === 'attach') {
      const mountPoint = args[args.indexOf('-mountpoint') + 1]!;
      const sourceApp = path.join(mountPoint, 'Agentico.app');
      fs.mkdirSync(path.join(sourceApp, 'Contents', 'Resources'), { recursive: true });
      fs.writeFileSync(path.join(sourceApp, 'marker.txt'), marker);
      fs.writeFileSync(
        path.join(sourceApp, 'Contents', 'Resources', 'build-identity.json'),
        JSON.stringify({ desktop_version: version }),
      );
      return '';
    }
    if (command === '/usr/bin/hdiutil' && args[0] === 'detach') return '';
    if (command === '/usr/bin/ditto') {
      fs.cpSync(args[0]!, args[1]!, { recursive: true });
      return '';
    }
    if (command === '/usr/bin/codesign') return '';
    if (command === '/usr/libexec/PlistBuddy') return `${version}\n`;
    throw new Error(`unexpected command: ${command} ${args.join(' ')}`);
  };
}

function appImageCommandRunner(version: string) {
  return (command: string, args: readonly string[], options?: { cwd?: string }): string => {
    if (args[0] !== '--appimage-extract' || options?.cwd === undefined) {
      throw new Error(`unexpected command: ${command} ${args.join(' ')}`);
    }
    const resources = path.join(options.cwd, 'squashfs-root', 'resources');
    fs.mkdirSync(resources, { recursive: true });
    fs.writeFileSync(
      path.join(resources, 'build-identity.json'),
      JSON.stringify({ desktop_version: version }),
    );
    return '';
  };
}
