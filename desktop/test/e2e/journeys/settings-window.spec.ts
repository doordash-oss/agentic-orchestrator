/**
 * Settings-window journey against the PACKAGED app: Settings is a window of
 * its own, and this is the journey that owns that claim end to end.
 *
 *  - the native Settings menu item opens it, and the main window keeps its
 *    own view (no in-shell settings surface, nothing to go "Back" from);
 *  - opening it again focuses the one that exists instead of duplicating it;
 *  - selecting a pane moves the window title and the rendered pane together,
 *    and the choice is persisted;
 *  - both deep links (agentico://updates, agentico://diagnostics) land on
 *    their pane from the closed state and from the already-open state;
 *  - the last-viewed pane is restored across an app relaunch;
 *  - File ▸ Close Window (⌘W) closes Settings only: the main window and the
 *    app process survive, and the window's geometry is saved on the way out.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  awaitSettingsWindow,
  closeApp,
  closeSettings,
  evidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  settingsPageOrNull,
  type AppHandle,
  type SettingsPaneLabel,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, waitFor } from '../helpers/world';
import type { SettingsPaneId } from '../../../src/shared/ipc';

test('Settings is its own window: single instance, panes, deep links, restore, and ⌘W', async ({}, testInfo) => {
  // Two full packaged launches (the relaunch proves the pane restore), so this
  // journey needs more than the suite's single-launch default budget.
  test.setTimeout(360_000);
  const transcript = new Transcript(
    'settings-window',
    'Settings window — ownership, panes, deep links, restore, close',
  );
  const world = createWorld('settings-window', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | null = null;

  try {
    transcript.section('Launch: one window, no settings surface in it');
    handle = await launchApp(world, testInfo, { traceName: 'settings-window' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    expect(handle.app.windows()).toHaveLength(1);
    await expect(handle.page.locator('.settings-window')).toHaveCount(0);
    transcript.step('app launched with exactly one window and no in-shell settings surface');

    transcript.section('The native Settings menu item opens the Settings window');
    const settings = await openSettings(handle);
    expect(await settings.evaluate(() => window.agentico.windowPurpose)).toBe('settings');
    expect(handle.app.windows()).toHaveLength(2);
    // First-ever open lands on Workspace roots, and the main window is
    // untouched behind it.
    await expectPane(handle, settings, 'Workspace roots');
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();
    await expect(handle.page.locator('.settings-window')).toHaveCount(0);
    await evidenceShot(handle, 'settings-window-workspace-roots', settings);
    transcript.step(
      'Settings opened as a second window (purpose "settings") on the Workspace roots pane',
    );

    transcript.section('A second open focuses the existing window; it never duplicates');
    const reopened = await openSettings(handle);
    expect(reopened).toBe(settings);
    expect(handle.app.windows()).toHaveLength(2);
    expect(await browserWindowCount(handle)).toBe(2);
    transcript.step('the second Settings open focused the one window — still two windows total');

    transcript.section('Switching panes moves the title and the rendered pane together');
    await selectSettingsPane(settings, 'Notifications');
    await expectPane(handle, settings, 'Notifications');
    await expect(settings.locator('section[aria-label="Workspace roots"]')).toHaveCount(0);
    await selectSettingsPane(settings, 'Appearance');
    await expectPane(handle, settings, 'Appearance');
    await expect(settings.getByRole('radiogroup', { name: 'Appearance theme' })).toBeVisible();
    transcript.step('pane switches carried the window title and the rendered pane with them');

    transcript.section('Deep links land on their pane while the window is open');
    await deepLink(handle, 'agentico://diagnostics');
    await expectPane(handle, settings, 'Diagnostics');
    await deepLink(handle, 'agentico://updates');
    await expectPane(handle, settings, 'Updates');
    expect(handle.app.windows()).toHaveLength(2);
    transcript.step(
      'agentico://diagnostics and agentico://updates switched the open window to their panes',
    );

    transcript.section('Deep links open the window on their pane from the closed state');
    await closeSettings(handle);
    expect(handle.app.windows()).toHaveLength(1);
    await deepLink(handle, 'agentico://diagnostics');
    const fromDiagnostics = await awaitSettingsWindow(handle);
    await expectPane(handle, fromDiagnostics, 'Diagnostics');
    await evidenceShot(handle, 'settings-window-diagnostics-deep-link', fromDiagnostics);
    await closeSettings(handle);
    await deepLink(handle, 'agentico://updates');
    const fromUpdates = await awaitSettingsWindow(handle);
    await expectPane(handle, fromUpdates, 'Updates');
    expect(handle.app.windows()).toHaveLength(2);
    transcript.step(
      'from the closed state both deep links opened exactly one Settings window on their pane',
    );

    transcript.section('Closing saves the pane and the geometry');
    // A pane no deep link can produce, so the restore below can only come
    // from what was persisted.
    await selectSettingsPane(fromUpdates, 'Notifications');
    await expectStoredPane(world.userData, 'notifications');
    await closeSettings(handle);
    const stored = readSettings(world.userData);
    expect(stored.settingsWindow.bounds?.width).toBeGreaterThan(0);
    expect(stored.settingsWindow.bounds?.height).toBeGreaterThan(0);
    transcript.json('persisted settingsWindow preferences', stored.settingsWindow);

    transcript.section('The last-viewed pane is restored across a relaunch');
    persistAppLogs(handle, 'settings-window-first-launch');
    await closeApp(handle);
    handle = await launchApp(world, testInfo, { traceName: 'settings-window-relaunch' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    // Nothing reopens Settings at launch; it comes back only when asked for.
    expect(handle.app.windows()).toHaveLength(1);
    const restored = await openSettings(handle);
    await expectPane(handle, restored, 'Notifications');
    transcript.step('after relaunch the Settings window reopened on the last-viewed pane');

    transcript.section('⌘W closes Settings without quitting the app');
    await closeSettings(handle);
    expect(settingsPageOrNull(handle)).toBeNull();
    expect(handle.app.windows()).toHaveLength(1);
    expect(handle.appProcess.exitCode).toBeNull();
    expect(handle.appProcess.signalCode).toBeNull();
    expect(await mainWindowVisible(handle)).toBe(true);
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(connection.status).toBe('ready');
    transcript.step(
      'File ▸ Close Window closed Settings only: the main window is still visible and ready, ' +
        'and the app process never entered the quit-decision flow',
    );

    persistAppLogs(handle, 'settings-window-relaunch');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

/** The pane is selected, rendered, and named by the window title. */
async function expectPane(
  handle: AppHandle,
  settings: Page,
  label: SettingsPaneLabel,
): Promise<void> {
  await expect(settings.getByRole('option', { name: label, exact: true })).toHaveAttribute(
    'aria-selected',
    'true',
  );
  await expect(
    settings.locator('.settings-window__pane').getByRole('heading', { name: label }),
  ).toBeVisible();
  // document.title is written by an effect after the pane renders.
  await waitFor(
    async () => (await settings.title()) === label,
    `the Settings window title to become "${label}"`,
    10_000,
    100,
  );
  // The main window never renders a settings pane of its own.
  await expect(handle.page.locator('.settings-window')).toHaveCount(0);
}

/**
 * Delivers a real `agentico://` deep link through the app's own open-url
 * handler — the same event macOS raises for a registered protocol click.
 */
async function deepLink(handle: AppHandle, url: string): Promise<void> {
  await handle.app.evaluate(({ app }, target) => {
    app.emit('open-url', { preventDefault: () => undefined } as never, target);
  }, url);
}

async function browserWindowCount(handle: AppHandle): Promise<number> {
  return handle.app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().length);
}

async function mainWindowVisible(handle: AppHandle): Promise<boolean> {
  return handle.app.evaluate(
    ({ BrowserWindow }, mainId) =>
      BrowserWindow.getAllWindows()
        .find((window) => window.webContents.id === mainId)
        ?.isVisible() === true,
    handle.mainWebContentsId,
  );
}

interface StoredSettings {
  settingsWindow: {
    pane: SettingsPaneId;
    bounds?: { x: number; y: number; width: number; height: number };
  };
}

function readSettings(userData: string): StoredSettings {
  return JSON.parse(
    fs.readFileSync(path.join(userData, 'settings.json'), 'utf8'),
  ) as StoredSettings;
}

/** The pane write is asynchronous, so wait for it before relying on it. */
async function expectStoredPane(userData: string, pane: SettingsPaneId): Promise<void> {
  await waitFor(
    () => {
      try {
        return readSettings(userData).settingsWindow.pane === pane;
      } catch {
        return false;
      }
    },
    `settings.json settingsWindow.pane to become "${pane}"`,
    15_000,
  );
}
