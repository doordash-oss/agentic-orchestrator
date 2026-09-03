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

/**
 * Journey 6 — security assertions against the PACKAGED app, launched from a
 * read-only install root (like a real installation):
 *
 *  - the renderer holds no bearer material: not in the DOM, not in
 *    local/session storage or IndexedDB, and the preload exposes exactly the
 *    narrow window.agentico surface (no node globals);
 *  - navigation and window.open are denied, in every window;
 *  - the window census stays trusted: at most the main window plus one
 *    Settings window, both on the app's own origin behind the same preload;
 *  - settings.json is owner-only (0600) and carries app-local presentation
 *    state only — no domain state, no credentials;
 *  - no offline domain cache exists anywhere under userData after quit;
 *  - app/server logs never contain the bearer token;
 *  - the app runs correctly from an installation root with no write bits.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';
import { EXPECTED_API_SURFACE } from '../../fixtures/agentico-api-surface';

test('packaged security posture: no token in the renderer, locked-down window, clean local state, read-only install root', async ({}, testInfo) => {
  const transcript = new Transcript(
    'security-posture',
    'Journey 6 — packaged security assertions (read-only install root)',
  );
  const world = createWorld('security', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const marker = `SecMarker${Math.random().toString(16).slice(2, 10)}`;

  const roRoot = path.join(world.root, 'ro-install');
  let handle: AppHandle | null = null;
  try {
    transcript.section('Copy the verified install to a read-only root');
    const roExecutable = copyInstallReadOnly(packagedExecutable(), roRoot);
    transcript.step(`install copied to \`${roRoot}\` and stripped of every write bit`);
    expect(fs.statSync(roRoot).mode & 0o222).toBe(0);

    handle = await launchApp(world, testInfo, {
      executablePath: roExecutable,
      traceName: 'security-posture',
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched from the read-only root and reached the ready workspace');

    // Create real server-side domain state so the "nothing cached locally"
    // claims have something to bite on.
    const created = await handle.page.evaluate(
      (name) =>
        window.agentico.createFeature({
          name,
          description: 'security journey domain state',
          repoKeys: ['alpha'],
          useCurrentBranch: false,
        }),
      marker,
    );
    expect(created.featureId).toBeTruthy();
    transcript.step(`created server-side feature \`${marker}\` (${created.featureId}) over IPC`);

    const discovery = readDiscovery(world);
    expect(discovery).not.toBeNull();
    const token = discovery!.auth_token ?? '';
    expect(token.length).toBeGreaterThan(8);

    transcript.section('Renderer holds no bearer material');
    const rendererState = await handle.page.evaluate(() => ({
      apiSurface: Object.keys(window.agentico).sort(),
      hasRequire: 'require' in window,
      hasProcess: 'process' in window,
      hasIpcRenderer: 'ipcRenderer' in window,
      localStorageLength: window.localStorage.length,
      sessionStorageLength: window.sessionStorage.length,
    }));
    expect(rendererState.apiSurface).toEqual(EXPECTED_API_SURFACE);
    expect(rendererState.hasRequire).toBe(false);
    expect(rendererState.hasProcess).toBe(false);
    expect(rendererState.hasIpcRenderer).toBe(false);
    expect(rendererState.localStorageLength).toBe(0);
    expect(rendererState.sessionStorageLength).toBe(0);
    const databases = await handle.page.evaluate(() => window.indexedDB.databases());
    expect(databases).toEqual([]);
    transcript.json('renderer surface scan', { ...rendererState, indexedDBDatabases: databases });

    const domContent = await handle.page.content();
    expect(domContent).not.toContain(token);
    const tokenSearch = await handle.page.evaluate((needle) => {
      const haystacks: string[] = [document.documentElement.outerHTML];
      for (let i = 0; i < window.localStorage.length; i += 1) {
        const key = window.localStorage.key(i)!;
        haystacks.push(key, window.localStorage.getItem(key) ?? '');
      }
      for (let i = 0; i < window.sessionStorage.length; i += 1) {
        const key = window.sessionStorage.key(i)!;
        haystacks.push(key, window.sessionStorage.getItem(key) ?? '');
      }
      return haystacks.some((haystack) => haystack.includes(needle));
    }, token);
    expect(tokenSearch).toBe(false);
    transcript.step('bearer token appears nowhere in the DOM, localStorage, or sessionStorage');

    transcript.section('settings.json: owner-only, presentation-only');
    // Force a persisted settings write through the app's own surface. The
    // theme radiogroup lives in the Settings window's Appearance pane now, so
    // open that window and switch to the pane first.
    const settingsWindow = await openSettings(handle);
    await selectSettingsPane(settingsWindow, 'Appearance');
    await settingsWindow
      .getByRole('radiogroup', { name: 'Theme' })
      .getByRole('radio', { name: 'Dark' })
      .click();
    await expect(settingsWindow.locator('html[data-theme="dark"]')).toBeAttached();
    // The main window follows the same broadcast — no second origin, no
    // second bridge, one theme.
    await expect(handle.page.locator('html[data-theme="dark"]')).toBeAttached();
    await evidenceShot(handle, 'security-locked-window');
    const settingsPath = path.join(world.userData, 'settings.json');
    await waitFor(() => fs.existsSync(settingsPath), 'settings.json to exist', 10_000);
    const settingsMode = fs.statSync(settingsPath).mode & 0o777;
    expect(settingsMode).toBe(0o600);
    const settingsRaw = fs.readFileSync(settingsPath, 'utf8');
    const settings = JSON.parse(settingsRaw) as Record<string, unknown>;
    expect(Object.keys(settings).sort()).toEqual([
      'ama',
      'notifications',
      'runtime',
      'schemaVersion',
      'servers',
      'settingsWindow',
      'shell',
      'theme',
      'window',
      'wizard',
    ]);
    expect(settingsRaw).not.toContain(token);
    expect(settingsRaw).not.toContain(marker);
    transcript.json('settings.json (mode 0600) content', settings);

    // The app's own origin, read before anything tries to leave it.
    const appOrigin = await handle.page.evaluate(() => window.location.origin);

    // Last interactive step: a blocked navigation attempt leaves Playwright's
    // navigation tracker pending, so nothing after this may use locators.
    transcript.section('Navigation and window.open are denied; the window census is trusted');
    const beforeUrl = handle.page.url();
    await handle.page.evaluate(() => {
      window.location.href = 'https://example.invalid/exfil';
    });
    await handle.page.waitForTimeout(500);
    await handle.page.evaluate(() => window.stop());
    expect(handle.page.url()).toBe(beforeUrl);
    // Both windows (main and the Settings window opened above) must refuse to
    // spawn a third one, and every window that does exist must be the app's
    // own origin running the app's own preload — never a renderer that
    // arrived from somewhere else.
    for (const page of handle.app.windows()) {
      const opened = await page.evaluate(() => window.open('https://example.invalid/') === null);
      expect(opened).toBe(true);
    }
    await handle.page.waitForTimeout(300);
    const census = await Promise.all(
      handle.app.windows().map((page) =>
        page.evaluate(() => ({
          origin: window.location.origin,
          purpose: window.agentico.windowPurpose,
          hasRequire: 'require' in window,
        })),
      ),
    );
    // At most the main window plus one Settings window — nothing else.
    expect(census).toHaveLength(2);
    expect(census.map((entry) => entry.purpose).sort()).toEqual(['main', 'settings']);
    for (const entry of census) {
      expect(entry.origin).toBe(appOrigin);
      expect(entry.hasRequire).toBe(false);
    }
    transcript.json('trusted window census after the exfiltration attempts', census);
    transcript.step(
      'location.href navigation was blocked; window.open returned null in both windows and ' +
        'opened nothing; the only windows are the main window and one Settings window, both on ' +
        `\`${appOrigin}\``,
    );

    transcript.section('Quit, then scan every local byte');
    const serverPid = discovery!.pid;
    const logText = persistAppLogs(handle, 'security-app');
    await closeApp(handle);
    handle = null;
    await waitFor(() => !processAlive(serverPid), 'app-owned server to be reaped', 15_000);

    // 1. App/server logs never contain the token.
    expect(logText).not.toContain(token);
    // 2. No offline domain cache under userData: after quit, no file anywhere
    //    under userData contains the feature marker or the token.
    const userDataHits = scanTreeFor(world.userData, [marker, token]);
    expect(userDataHits).toEqual([]);
    // 3. Nothing outside the server's own owner-only credential files holds
    //    the token (state dir, setup logs, config, workspace, …).
    const allowedTokenFiles = new Set([
      path.join(world.runtimeDir, '.agentico-server-token'),
      path.join(world.runtimeDir, '.agentico-server.json'),
    ]);
    const tokenHits = scanTreeFor(world.root, [token]).filter((hit) => !allowedTokenFiles.has(hit));
    expect(tokenHits).toEqual([]);
    for (const credentialFile of allowedTokenFiles) {
      expect(fs.statSync(credentialFile).mode & 0o777).toBe(0o600);
    }
    transcript.step(
      'post-quit scans: no domain marker or token under userData; token exists only in the ' +
        'two owner-only (0600) server credential files; app logs are token-free',
    );
    transcript.codeBlock(
      'app log tail (redacted at source)',
      logText.split('\n').slice(-12).join('\n'),
    );

    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    restoreWriteBits(roRoot);
    destroyWorld(world);
  }
});

/** Copies the unpacked install next to the world and removes all write bits. */
function copyInstallReadOnly(executablePath: string, roRoot: string): string {
  fs.mkdirSync(roRoot, { recursive: true });
  if (process.platform === 'darwin') {
    // .../dist/mac-universal/Agentico.app/Contents/MacOS/Agentico
    const appBundle = path.resolve(executablePath, '../../..');
    execFileSync('cp', ['-R', appBundle, roRoot]);
    execFileSync('chmod', ['-R', 'a-w', roRoot]);
    return path.join(roRoot, path.basename(appBundle), 'Contents', 'MacOS', 'Agentico');
  }
  // .../dist/linux-unpacked/agentico
  const unpackedDir = path.dirname(executablePath);
  execFileSync('cp', ['-R', unpackedDir, roRoot]);
  execFileSync('chmod', ['-R', 'a-w', roRoot]);
  return path.join(roRoot, path.basename(unpackedDir), path.basename(executablePath));
}

function restoreWriteBits(roRoot: string): void {
  try {
    execFileSync('chmod', ['-R', 'u+w', roRoot]);
  } catch {
    // Directory may not exist when the copy step itself failed.
  }
}

/** Returns files under root containing any needle (files > 32 MiB skipped). */
function scanTreeFor(root: string, needles: string[]): string[] {
  const buffers = needles.filter((needle) => needle !== '').map((needle) => Buffer.from(needle));
  const hits: string[] = [];
  const walk = (dir: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        walk(full);
        continue;
      }
      if (!entry.isFile()) {
        continue;
      }
      try {
        if (fs.statSync(full).size > 32 * 1024 * 1024) {
          continue;
        }
        const content = fs.readFileSync(full);
        if (buffers.some((needle) => content.includes(needle))) {
          hits.push(full);
        }
      } catch {
        // Unreadable files (locks) cannot leak through this scan path.
      }
    }
  };
  walk(root);
  return hits;
}
