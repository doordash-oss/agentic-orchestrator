import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import type { TestInfo } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  installRelaunchProbe,
  launchApp,
  persistAppLogs,
  relaunchCount,
  restoreRelaunchProbe,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';
import { updatePackageName, writeSignedUpdateFixture } from '../helpers/update-fixtures';

test('verified download, Install When Idle, and Restart to Update require explicit consent', async ({}, testInfo) => {
  const transcript = new Transcript(
    'distribution-update-consent',
    'Packaged verified download and install consent',
  );
  await assertRestartToUpdateConsent(testInfo, transcript);

  const world = createWorld('update-consent', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const packageName = updatePackageName(process.platform === 'darwin' ? 'macos' : 'appimage');
  const fixture = writeSignedUpdateFixture(world.root, {
    packageName,
    packageText: 'verified package bytes',
  });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, {
      traceName: 'distribution-update-consent',
      env: { AGENTICO_UPDATE_FIXTURE: fixture, AGENTICO_UPDATE_INSTALL_MODE: 'in-app' },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const state = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(state).toMatchObject({
      status: 'ready',
      targetVersion: '0.2.0',
      signatureStatus: 'verified',
    });
    const stagedPackage = path.join(world.userData, 'updates', 'v0.2.0', packageName);
    expect(fs.readFileSync(stagedPackage, 'utf8')).toBe('verified package bytes');
    transcript.json('ready after automatic verified download', state);

    const updateTrigger = handle.page.getByRole('button', { name: 'Show available update' });
    const updatePopover = handle.page.getByRole('region', { name: 'Available update' });
    await expect(updateTrigger).toBeVisible({ timeout: 10_000 });

    // The pending update announces itself ambiently: a non-interactive dot on
    // the sidebar footer status row, and nothing at all in the content flow.
    await expect(handle.page.getByRole('img', { name: 'Update available' })).toBeVisible();
    expect(await contentColumnChildren(handle)).toEqual(['toolbar', 'content-pane']);

    await updateTrigger.click();
    await expect(updatePopover).toBeVisible();
    await expect(
      updatePopover.getByRole('heading', { name: 'Agentico 0.2.0 is available' }),
    ).toBeVisible();
    expect(await contentColumnChildren(handle)).toEqual(['toolbar', 'content-pane']);
    transcript.step('pending update surfaced as a toolbar popover and a sidebar footer dot only');

    await updatePopover.getByRole('button', { name: 'Updates' }).click();
    const updatesPanel = handle.page.getByRole('region', { name: 'Updates' });
    await expect(updatesPanel).toContainText('0.2.0');
    await expect(updatesPanel).toContainText('verified');
    await expect(updatesPanel.getByRole('button', { name: 'Release notes' })).toBeVisible();
    await handle.page.keyboard.press('Escape');
    await expect(updatePopover).toHaveCount(0);
    await expect(updateTrigger).toBeFocused();
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();

    await setForcedActiveWork(handle, true);
    const activeReady = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(activeReady.activeWorkSummary).toMatch(/1 workflow/);
    transcript.json('forced active workflow before Install When Idle', activeReady);

    await updateTrigger.click();
    await expect(updatePopover).toContainText(/1 workflow/);
    await updatePopover.getByRole('button', { name: 'Updates' }).click();
    await handle.page.keyboard.press('Escape');
    await expect(updatePopover).toHaveCount(0);
    await handle.page.getByRole('button', { name: 'Stop Work and Install Now' }).click();
    await handle.page
      .getByRole('dialog', { name: 'Install update confirmation' })
      .getByRole('button', { name: 'Cancel' })
      .click();
    await expect(
      handle.page.getByRole('dialog', { name: 'Install update confirmation' }),
    ).toHaveCount(0);
    transcript.step('Stop Work and Install Now confirmation canceled without restart');

    await updateTrigger.click();
    await updatePopover.getByRole('button', { name: 'Install When Idle' }).click();
    // The popover survives update-state refreshes, so the scheduled message
    // lands in the same surface that took the consent.
    await expect(updatePopover).toContainText('scheduled for the next idle window', {
      timeout: 10_000,
    });
    await expect(updatePopover.getByRole('button', { name: 'Scheduled for Idle' })).toBeDisabled();
    const scheduled = await handle.page.evaluate(() => window.agentico.getUpdates());
    expect(scheduled.status).toBe('scheduled');
    expect(scheduled.activeWorkSummary).toMatch(/1 workflow/);
    transcript.json('scheduled update state', scheduled);

    await installRelaunchProbe(handle);
    await setForcedActiveWork(handle, false);
    await expect.poll(() => relaunchCount(handle!), { timeout: 20_000 }).toBeGreaterThanOrEqual(1);
    const installing = await handle.page.evaluate(() => window.agentico.getUpdates());
    expect(installing.status).toBe('installing');
    expect(installing.activeWorkSummary).toBeUndefined();
    transcript.json('Install When Idle automatic relaunch probe', installing);
    await restoreRelaunchProbe(handle);

    persistAppLogs(handle, 'distribution-update-consent');
  } finally {
    if (handle !== null) {
      await restoreRelaunchProbe(handle).catch(() => undefined);
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
  transcript.write(testInfo);
});

async function assertRestartToUpdateConsent(
  testInfo: TestInfo,
  transcript: Transcript,
): Promise<void> {
  const world = createWorld('update-restart-consent', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const packageName = updatePackageName(process.platform === 'darwin' ? 'macos' : 'appimage');
  const fixture = writeSignedUpdateFixture(world.root, {
    packageName,
    packageText: 'restart consent package bytes',
  });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, {
      traceName: 'distribution-update-restart-consent',
      env: { AGENTICO_UPDATE_FIXTURE: fixture, AGENTICO_UPDATE_INSTALL_MODE: 'in-app' },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const state = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(state.status).toBe('ready');
    const updateTrigger = handle.page.getByRole('button', { name: 'Show available update' });
    await expect(updateTrigger).toBeVisible({ timeout: 10_000 });
    await updateTrigger.click();
    await handle.page
      .getByRole('region', { name: 'Available update' })
      .getByRole('button', { name: 'Updates' })
      .click();
    await handle.page.keyboard.press('Escape');
    await expect(handle.page.getByRole('region', { name: 'Available update' })).toHaveCount(0);
    await installRelaunchProbe(handle);
    await handle.page.getByRole('button', { name: 'Restart to Update' }).click();
    await expect.poll(() => relaunchCount(handle!), { timeout: 10_000 }).toBeGreaterThanOrEqual(1);
    const installing = await handle.page.evaluate(() => window.agentico.getUpdates());
    expect(installing.status).toBe('installing');
    transcript.json('explicit Restart to Update relaunch probe', installing);
    persistAppLogs(handle, 'distribution-update-restart-consent');
  } finally {
    if (handle !== null) {
      await restoreRelaunchProbe(handle).catch(() => undefined);
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
}

/**
 * The first-token class names of `.content-column`'s direct children. An update
 * notice must never take flow space, so this stays `['toolbar', 'content-pane']`
 * whether or not an update is pending and whether or not its popover is open.
 */
async function contentColumnChildren(handle: AppHandle): Promise<string[]> {
  return await handle.page.evaluate(() =>
    Array.from(document.querySelector('.content-column')!.children).map(
      (child) => child.className.split(' ')[0]!,
    ),
  );
}

async function setForcedActiveWork(handle: AppHandle, active: boolean): Promise<void> {
  await handle.app.evaluate((_electron, isActive) => {
    const global = globalThis as typeof globalThis & {
      __agenticoForcedActiveWork?: {
        featureIds?: string[];
        featureLabels?: Record<string, string>;
        chatActive?: boolean;
        detectionFailed?: boolean;
      };
      __agenticoRefreshBackgroundState?: () => void;
    };
    global.__agenticoForcedActiveWork = isActive
      ? {
          featureIds: ['e2e-update-consent-work'],
          featureLabels: { 'e2e-update-consent-work': 'Update Consent Work' },
        }
      : { featureIds: [], featureLabels: {} };
    global.__agenticoRefreshBackgroundState?.();
  }, active);
}
