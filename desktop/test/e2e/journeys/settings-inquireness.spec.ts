/**
 * Packaged Electron regression: changing workspace-default inquireness in
 * Settings must not blank the renderer. This exercises the shipped app bundle,
 * real bundled Go server, real IPC bridge, and real persisted runtime config.
 *
 * The editor lives in the Settings window's "Workspace defaults" pane, so the
 * renderer under scrutiny here is that window's — with the main window's
 * liveness asserted alongside it, since both share the origin and the bridge.
 */
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
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const RUN_NAME = `settings-inquireness-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

test('changing Settings workspace inquireness keeps the packaged renderer alive', async ({}, testInfo) => {
  const world = createWorld('settings-inquireness', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });

  let handle: AppHandle | null = null;
  const rendererErrors: string[] = [];
  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    handle.page.on('pageerror', (error) => rendererErrors.push(`pageerror: ${error.message}`));
    handle.page.on('console', (message) => {
      if (message.type() === 'error') rendererErrors.push(`console: ${message.text()}`);
    });

    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const settings = await openSettings(handle);
    settings.on('pageerror', (error) =>
      rendererErrors.push(`settings pageerror: ${error.message}`),
    );
    settings.on('console', (message) => {
      if (message.type() === 'error') rendererErrors.push(`settings console: ${message.text()}`);
    });
    await selectSettingsPane(settings, 'Workspace defaults');

    const defaultsEditor = settings.locator('[aria-label="Workspace defaults editor"]');
    await defaultsEditor.scrollIntoViewIfNeeded();
    await expect(defaultsEditor).toBeVisible({ timeout: 15_000 });
    const before = await settings.evaluate(() => window.agentico.getWorkspaceDefaults());
    const target = before.inquireness === 'high' ? 'none' : 'high';
    const targetLabel = target === 'high' ? /High/ : /None/;
    const targetText = target === 'high' ? 'High' : 'None';

    await defaultsEditor.locator('.config-editor__segment', { hasText: targetText }).click();

    await expect(settings.getByRole('heading', { name: 'Workspace defaults' })).toBeVisible();
    await expect(defaultsEditor).toBeVisible();
    // Focusing the segment's hidden radio must not scroll the document: the
    // Settings window is 100vh, so any document scroll paints a blank window
    // while DOM-level visibility assertions still pass.
    expect(await settings.evaluate(() => document.scrollingElement?.scrollTop ?? 0)).toBe(0);
    await expect(defaultsEditor.getByRole('radio', { name: targetLabel })).toBeChecked();
    const save = defaultsEditor.getByRole('button', { name: 'Save changes' });
    await expect(save).toBeEnabled();
    await save.click();
    await expect(defaultsEditor.getByRole('status')).toContainText('Saved', {
      timeout: 15_000,
    });

    const workspaceDefaults = await settings.evaluate(() => window.agentico.getWorkspaceDefaults());
    expect(workspaceDefaults.inquireness).toBe(target);
    // The main window must be as alive as the Settings window it spawned.
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();
    expect(rendererErrors).toEqual([]);
    await evidenceShot(handle, `${RUN_NAME}-workspace-inquireness-high`, settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle);
    }
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
