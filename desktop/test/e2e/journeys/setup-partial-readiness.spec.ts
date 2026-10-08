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
 * Partial readiness against the packaged app and the real bundled server: the
 * Claude stub is signed in (one ready provider) but the runtime config names
 * a pipeline profile the server rejects. Instead of the full-page wizard the
 * app launches into the Supervisor page with a compact banner naming the
 * configuration issue; "Open setup", the palette's "Setup…" and the Navigate
 * menu reach the same wizard as a sheet; "New feature" stays enabled and
 * surfaces the server's not_ready refusal; and once the configuration is
 * fixed, "Check again" inside the sheet takes the banner and the sheet away.
 * (first-launch keeps proving the full-page wizard when no provider is ready.)
 */
import { expect, test, type Locator, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { supervisorPage, supervisorRow } from '../helpers/supervisor';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const RUN_NAME = 'setup-partial-readiness';

test('partial readiness: banner, setup sheet, Setup… command and not_ready creation until Check again clears it', async ({}, testInfo) => {
  test.setTimeout(240_000);
  const transcript = new Transcript(RUN_NAME, 'Partial-readiness setup banner and sheet journey');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    invalidPipelineProfile: true,
  });
  createRepo(world, 'setup-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;

    transcript.section(
      'One ready provider with an invalid config launches into the Supervisor page',
    );
    const banner = setupBanner(page);
    await expect(banner).toBeVisible({ timeout: 60_000 });
    await expect(page.getByRole('heading', { name: 'Set up Agentico' })).toHaveCount(0);
    await expect(supervisorRow(page)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    await expect(banner).toContainText('Invalid configuration');
    await expect(banner.getByRole('button', { name: 'Open setup' })).toBeVisible();
    const readiness = await page.evaluate(() => window.agentico.getRuntimeReadiness());
    expect(readiness.ready).toBe(false);
    expect(readiness.configuration.valid).toBe(false);
    expect(readiness.providers.some((provider) => provider.ready)).toBe(true);
    // The sheet never opens on its own.
    await expect(setupSheet(page)).toHaveCount(0);
    await evidenceShot(handle, 'setup-partial-banner');
    transcript.json('readiness while partially ready', {
      ready: readiness.ready,
      configuration: readiness.configuration,
    });

    transcript.section('"Open setup" opens the wizard as a sheet naming the configuration issue');
    await banner.getByRole('button', { name: 'Open setup' }).click();
    const sheet = setupSheet(page);
    await expect(sheet).toBeVisible();
    await expect(sheet).toContainText('Invalid configuration');
    await expect(sheet.getByRole('button', { name: /Check again/ })).toBeVisible();
    await evidenceShot(handle, 'setup-partial-sheet');
    await page.keyboard.press('Escape');
    await expect(sheet).toHaveCount(0);
    await expect(banner).toBeVisible();
    transcript.step('sheet opened from the banner and Escape put it away; the banner stays');

    transcript.section('The palette lists Setup… and the Navigate menu enables it');
    const palette = await openPalette(page);
    await palette.getByLabel('Search features and commands').fill('Setup');
    const setupOption = palette.getByRole('option', { name: /Setup…/ });
    await expect(setupOption).toBeVisible();
    await setupOption.click();
    await expect(palette).toHaveCount(0);
    await expect(sheet).toBeVisible();
    await sheet.getByRole('button', { name: 'Close', exact: true }).click();
    await expect(sheet).toHaveCount(0);
    await expect.poll(() => setupMenuItem(handle!)).toEqual({ label: 'Setup…', enabled: true });
    transcript.step('palette Setup… opened the sheet; Navigate ▸ Setup… is enabled');

    transcript.section('"New feature" stays enabled and surfaces the server\'s not_ready refusal');
    const newFeature = page.getByRole('button', { name: 'New feature' });
    await expect(newFeature).toBeEnabled();
    await newFeature.click();
    const form = page.getByRole('form', { name: 'Create a feature' });
    await expect(form).toBeVisible();
    await page.getByRole('checkbox', { name: /setup-lab/ }).check();
    await page.getByRole('button', { name: 'Next: Describe' }).click();
    await page.locator('#feature-name').fill('Premature feature');
    await page.getByRole('button', { name: 'Next: Depth' }).click();
    await page.getByRole('button', { name: 'Next: Contract' }).click();
    await page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await page.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(form).toContainText('Runtime not ready', { timeout: 30_000 });
    await expect(form).toBeVisible();
    await evidenceShot(handle, 'setup-partial-not-ready');
    expect((await page.evaluate(() => window.agentico.listFeatures())).features).toHaveLength(0);
    // The refused draft is still dirty, so Escape asks before discarding it.
    await page.keyboard.press('Escape');
    await page
      .getByRole('dialog', { name: 'Discard feature draft' })
      .getByRole('button', { name: 'Discard draft' })
      .click();
    await expect(form).toHaveCount(0);
    transcript.step('creation was refused with not_ready and no feature exists');

    transcript.section('Fixing the configuration and "Check again" clear the banner and sheet');
    await page.evaluate(async () => {
      const defaults = await window.agentico.getWorkspaceDefaults();
      await window.agentico.updateWorkspaceDefaults({ ...defaults, pipeline: 'large' });
    });
    // Fixed on the server, but the shell holds the last snapshot until asked.
    await expect(banner).toBeVisible();
    await banner.getByRole('button', { name: 'Open setup' }).click();
    await expect(sheet).toBeVisible();
    await sheet.getByRole('button', { name: /Check again/ }).click();
    await expect(sheet).toHaveCount(0, { timeout: 30_000 });
    await expect(banner).toHaveCount(0);
    await expect(supervisorPage(page)).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Set up Agentico' })).toHaveCount(0);
    expect((await page.evaluate(() => window.agentico.getRuntimeReadiness())).ready).toBe(true);
    await expect.poll(() => setupMenuItem(handle!)).toEqual({ label: 'Setup…', enabled: false });
    const completePalette = await openPalette(page);
    await completePalette.getByLabel('Search features and commands').fill('Setup');
    await expect(completePalette.getByRole('option', { name: /Setup…/ })).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(completePalette).toHaveCount(0);
    await evidenceShot(handle, 'setup-partial-complete');
    transcript.step('banner and sheet gone; Setup… left the palette and its menu item disabled');

    persistAppLogs(handle, RUN_NAME);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function setupBanner(page: Page): Locator {
  return page.getByRole('region', { name: 'Setup incomplete' });
}

function setupSheet(page: Page): Locator {
  return page.getByRole('dialog', { name: 'Set up Agentico' });
}

async function openPalette(page: Page): Promise<Locator> {
  await page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
  const palette = page.getByRole('dialog', { name: 'Command palette' });
  await expect(palette).toBeVisible();
  return palette;
}

/** Navigate ▸ Setup… as the live application menu has it. */
async function setupMenuItem(
  handle: AppHandle,
): Promise<{ label: string; enabled: boolean } | null> {
  return handle.app.evaluate(({ Menu }) => {
    const item = Menu.getApplicationMenu()?.getMenuItemById('global.setup');
    return item == null ? null : { label: item.label, enabled: item.enabled };
  });
}
