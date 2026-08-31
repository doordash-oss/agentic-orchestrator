import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  awaitSettingsWindow,
  closeApp,
  createFeatureViaForm,
  installRelaunchProbe,
  launchApp,
  persistAppLogs,
  relaunchCount,
  restoreRelaunchProbe,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';
import { updatePackageName, writeSignedUpdateFixture } from '../helpers/update-fixtures';

test('Stop Work and Install Now confirms impact, cancels partial stops, then succeeds after ownership-aware stop', async ({}, testInfo) => {
  const transcript = new Transcript(
    'distribution-update-stop-work',
    'Packaged Stop Work and Install Now',
  );
  const world = createWorld('update-stop-work', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const packageName = updatePackageName(process.platform === 'darwin' ? 'macos' : 'appimage');
  const fixture = writeSignedUpdateFixture(world.root, {
    packageName,
    packageText: 'stop package',
  });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, {
      traceName: 'distribution-update-stop-work',
      env: { AGENTICO_UPDATE_FIXTURE: fixture, AGENTICO_UPDATE_INSTALL_MODE: 'in-app' },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(
      handle.page.evaluate(() => window.agentico.checkForUpdates()),
    ).resolves.toMatchObject({
      status: 'ready',
      signatureStatus: 'verified',
    });
    await createFeatureViaForm(handle, {
      name: 'Update Stop Work',
      description: 'Durable workflow for update stop-and-install coverage.',
      repoPatterns: [/alpha/],
      waitForReady: true,
    });
    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();
    await expect(handle.page.getByRole('button', { name: 'Stop' })).toBeEnabled({
      timeout: 60_000,
    });
    const activeReady = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(activeReady.activeWorkSummary).toMatch(/1 workflow/);
    transcript.json('active workflow before Stop Work and Install Now', activeReady);

    // The popover's Updates action opens the Settings window on the Updates
    // pane, which is where the install controls live now.
    const updatePopover = handle.page.getByRole('region', { name: 'Available update' });
    await handle.page.getByRole('button', { name: 'Show available update' }).click();
    await updatePopover.getByRole('button', { name: 'Updates' }).click();
    const settings = await awaitSettingsWindow(handle);
    await handle.page.keyboard.press('Escape');
    await expect(updatePopover).toHaveCount(0);
    await settings.getByRole('button', { name: 'Stop Work and Install Now' }).click();
    const dialog = settings.getByRole('dialog', { name: 'Install update confirmation' });
    await expect(dialog).toContainText(/Workflows and AMA may be interrupted/);
    transcript.step('Stop Work and Install Now showed explicit workflow and AMA impact');

    await forcePartialStop(handle, 1);
    await dialog.getByRole('button', { name: 'Stop Work and Install Now' }).click();
    await expect(
      settings.getByLabel('Updates').getByText(/forced one unresolved stop outcome/i),
    ).toBeVisible({ timeout: 15_000 });
    const failed = await handle.page.evaluate(() => window.agentico.getUpdates());
    expect(failed.status).toBe('ready');
    expect(failed.message).toMatch(/forced one unresolved stop outcome/i);
    transcript.json('partial stop cancellation state', failed);

    await installRelaunchProbe(handle);
    await settings.getByRole('button', { name: 'Stop Work and Install Now' }).click();
    await settings
      .getByRole('dialog', { name: 'Install update confirmation' })
      .getByRole('button', { name: 'Stop Work and Install Now' })
      .click();
    await expect.poll(() => relaunchCount(handle!), { timeout: 20_000 }).toBeGreaterThanOrEqual(1);
    const installing = await handle.page.evaluate(() => window.agentico.getUpdates());
    expect(installing.status).toBe('installing');
    transcript.json('ownership-aware stop then install state', installing);
    await restoreRelaunchProbe(handle);

    persistAppLogs(handle, 'distribution-update-stop-work');
  } finally {
    if (handle !== null) {
      await restoreRelaunchProbe(handle).catch(() => undefined);
      await closeApp(handle);
    }
    // The stopped provider stub exits only after reading the interrupt from
    // its stdin poll loop; allow a bounded grace instead of asserting
    // immediately after closeApp.
    await assertNoLeakedProcessesEventually(world);
    destroyWorld(world);
  }
  transcript.write(testInfo);
});

async function assertNoLeakedProcessesEventually(world: JourneyWorld): Promise<void> {
  await waitFor(
    () => {
      try {
        assertNoLeakedProcesses(world);
        return true;
      } catch {
        return false;
      }
    },
    `no leaked processes for ${world.root}`,
    15_000,
  );
}

async function forcePartialStop(handle: AppHandle, count: number): Promise<void> {
  await handle.app.evaluate((_, value) => {
    const global = globalThis as typeof globalThis & {
      __agenticoForceStopFailureCount?: number;
    };
    global.__agenticoForceStopFailureCount = value;
  }, count);
}
