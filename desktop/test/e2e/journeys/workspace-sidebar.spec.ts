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
 * Bench sidebar shell mechanics against the packaged app: pointer
 * selection, the roving-tabindex Arrow/Home/End keyboard model (scoped to
 * whatever a lane's `<details>` disclosure currently shows), ⌘2-9 absolute
 * sidebar-position selection (reachable even inside a collapsed lane, unlike
 * Arrow/Home/End), ⌘1 going home to the pinned Supervisor row (the first
 * row, selected on launch, and never counted by ⌘2-9), the sidebar-toggle
 * button and ⌘⌃S collapse paths, and the ~700px auto-collapse breakpoint.
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const RUN_NAME = `workspace-sidebar-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

test('workspace sidebar: pointer, keyboard, ⌘2-9, and collapse against the packaged app', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Bench sidebar shell-mechanics journey');
  const world = createWorld('workspace-sidebar', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'sidebar-lab', { commit: true });

  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('The pinned Supervisor row is first and selected on launch');
    const listbox = handle.page.getByRole('listbox', { name: 'Features' });
    const rows = listbox.getByRole('option');
    const supervisorRow = rows.nth(0);
    const supervisorPage = handle.page.getByRole('region', { name: 'Supervisor', exact: true });
    await expect(supervisorRow).toHaveAccessibleName('Supervisor');
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage).toBeVisible();
    await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveCount(0);
    // At rest (never started) the row carries no state marker, no unread
    // dot and no sub-line: just the glyph and its name.
    await expect(supervisorRow).toHaveAttribute('data-supervisor-state', 'none');
    await expect(supervisorRow).toHaveAttribute('data-unread', 'false');
    await expect(supervisorRow.locator('.sidebar__row-subline')).toHaveCount(0);
    await expect(supervisorRow.locator('.sidebar__row-unread')).toHaveCount(0);
    await expect(supervisorRow).toHaveText('Supervisor');
    transcript.step('Supervisor is the only pinned row, first and selected, its page mounted');

    transcript.section('Resize the sidebar with pointer and keyboard');
    const divider = handle.page.getByRole('separator', { name: 'Resize sidebar' });
    const sidebar = handle.page.locator('nav.sidebar');
    // hover() runs Playwright's actionability checks (stable layout, the
    // handle is the actual hit target) before the raw pointer gesture; a bare
    // mouse.move to a pre-measured coordinate can land the press on whatever
    // is painted there when the shell is still settling.
    await divider.hover({ position: { x: 1, y: 100 } });
    const grip = await divider.boundingBox();
    if (!grip) throw new Error('Sidebar resize handle has no bounds');
    await handle.page.mouse.down();
    await expect(divider).toHaveAttribute('data-resizing', 'true');
    await handle.page.mouse.move(grip.x + 1 + 100, grip.y + 100, { steps: 10 });
    await handle.page.mouse.up();
    await expect(sidebar).toHaveCSS('width', '360px');
    await divider.press('ArrowRight');
    await expect(sidebar).toHaveCSS('width', '370px');
    await divider.press('Home');
    await expect(sidebar).toHaveCSS('width', '200px');
    await divider.press('End');
    await expect(sidebar).toHaveCSS('width', '520px');
    await divider.dblclick();
    await expect(sidebar).toHaveCSS('width', '260px');
    await divider.press('ArrowRight');
    await expect
      .poll(
        async () =>
          (await handle!.page.evaluate(() => window.agentico.getSettings())).shell.sidebarWidth,
      )
      .toBe(270);

    transcript.section('Create three features — they land together in the At rest lane');
    const names = ['Sidebar Alpha', 'Sidebar Beta', 'Sidebar Gamma'];
    for (const name of names) {
      // Wait for setup so every feature settles into the stable CodeReady
      // ("At rest") status before the next one is created — otherwise a
      // feature still mid-setup would transiently classify as Running.
      await createFeatureViaForm(handle, {
        name,
        repoPatterns: [/sidebar-lab/],
        waitForReady: true,
      });
      // "New feature" stays in the toolbar on the feature page just opened,
      // so the next creation starts from right here.
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();
    }
    const atRestGroup = handle.page.getByRole('group', { name: 'At rest' });
    await expect(atRestGroup).toBeVisible();
    for (const name of names) {
      await expect(atRestGroup.getByText(name, { exact: true })).toBeVisible();
    }
    transcript.step('three features created, all grouped under At rest');

    transcript.section('Pointer selection switches the mounted content');
    // The pinned Supervisor row + the three created features — no Overview.
    await expect(rows).toHaveCount(4);
    // The row's bare name only — at-rest rows also carry a status sub-line
    // ("Code ready") in the same option, which a full textContent read
    // would otherwise glue onto the name.
    const rowNames = await listbox.locator('.sidebar__row-name').allTextContents();
    // Visual/DOM order: the pinned Supervisor row, then the three features in
    // lane order.
    expect(rowNames[0]).toBe('Supervisor');
    const [, firstName, secondName, thirdName] = rowNames as [string, string, string, string];

    await rows.nth(1).click();
    await expect(handle.page.getByLabel(`Feature ${firstName}`)).toBeVisible({ timeout: 15_000 });
    await expect(rows.nth(1)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'false');
    await expect(supervisorPage).toHaveCount(0);
    transcript.step(`clicking "${firstName}" mounted its cockpit in place of the Supervisor page`);

    transcript.section('Arrow/Home/End move focus and selection together through visible rows');
    await supervisorRow.click();
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage).toBeVisible({ timeout: 15_000 });
    await supervisorRow.focus();

    await handle.page.keyboard.press('ArrowDown');
    await expect(rows.nth(1)).toBeFocused();
    await expect(rows.nth(1)).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'false');
    await expect(handle.page.getByLabel(`Feature ${firstName}`)).toBeVisible({ timeout: 15_000 });

    await handle.page.keyboard.press('ArrowDown');
    await expect(rows.nth(2)).toBeFocused();
    await expect(rows.nth(2)).toHaveAttribute('aria-selected', 'true');
    await expect(handle.page.getByLabel(`Feature ${secondName}`)).toBeVisible({ timeout: 15_000 });

    await handle.page.keyboard.press('End');
    await expect(rows.nth(3)).toBeFocused();
    await expect(rows.nth(3)).toHaveAttribute('aria-selected', 'true');
    await expect(handle.page.getByLabel(`Feature ${thirdName}`)).toBeVisible({ timeout: 15_000 });

    await handle.page.keyboard.press('Home');
    await expect(supervisorRow).toBeFocused();
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage).toBeVisible({ timeout: 15_000 });

    // ArrowUp from the first row wraps to the last, so the Supervisor row is
    // inside the roving cycle in both directions.
    await handle.page.keyboard.press('ArrowUp');
    await expect(rows.nth(3)).toBeFocused();
    await expect(rows.nth(3)).toHaveAttribute('aria-selected', 'true');
    transcript.step(
      'ArrowDown/End/Home/ArrowUp moved focus and the mounted content together, Supervisor row included',
    );

    transcript.section(
      '⌘2-9 select by absolute sidebar position — reachable even inside a collapsed lane',
    );
    await handle.page.locator('summary.sidebar__lane-summary', { hasText: 'At rest' }).click();
    await expect(atRestGroup).toBeHidden();

    // ⌘2 → the 1st feature in absolute order, ⌘4 → the 3rd — both still
    // inside the now-collapsed At rest lane. The pinned Supervisor row is not
    // numbered, so it never shifts the feature shortcuts.
    await handle.page.keyboard.press('ControlOrMeta+2');
    await expect(handle.page.getByLabel(`Feature ${firstName}`)).toBeVisible({ timeout: 15_000 });
    await handle.page.keyboard.press('ControlOrMeta+4');
    await expect(handle.page.getByLabel(`Feature ${thirdName}`)).toBeVisible({ timeout: 15_000 });
    transcript.step(
      `⌘2 and ⌘4 selected "${firstName}" and "${thirdName}" while their lane stayed collapsed`,
    );

    // ⌘1 is a native-menu accelerator (Navigate ▸ Supervisor), which a
    // synthetic renderer key event never reaches, so it is driven through the
    // real menu item the accelerator fires.
    const home = await handle.app.evaluate(({ BrowserWindow, Menu }) => {
      const item = Menu.getApplicationMenu()?.getMenuItemById('global.home');
      if (item == null) throw new Error('menu item global.home missing');
      item.click(undefined, BrowserWindow.getAllWindows()[0], undefined);
      return { label: item.label, accelerator: item.accelerator ?? null };
    });
    expect(home).toEqual({ label: 'Supervisor', accelerator: 'CommandOrControl+1' });
    await expect(supervisorRow).toHaveAttribute('aria-selected', 'true', { timeout: 15_000 });
    await expect(supervisorPage).toBeVisible({ timeout: 15_000 });
    await expect(handle.page.getByLabel(`Feature ${thirdName}`)).toHaveCount(0);
    transcript.step('⌘1 (Navigate ▸ Supervisor) went home to the Supervisor page');

    await handle.page.locator('summary.sidebar__lane-summary', { hasText: 'At rest' }).click();
    await expect(atRestGroup).toBeVisible();

    transcript.section('The toolbar sidebar-toggle button collapses and persists the choice');
    // A raw CSS locator, not getByRole: `.sidebar[data-collapsed='true']` sets
    // `display: none` (app.css), which removes the element from the
    // accessibility tree entirely — a role-based locator would stop
    // resolving the instant the sidebar collapses, even though the element
    // (and its data-collapsed attribute) is still very much in the DOM.
    const nav = handle.page.locator('nav.sidebar');
    await expect(nav).toHaveAttribute('data-collapsed', 'false');
    await handle.page.getByRole('button', { name: 'Hide sidebar' }).click();
    await expect(nav).toHaveAttribute('data-collapsed', 'true');
    let settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.shell.sidebarCollapsed).toBe(true);
    transcript.step('toolbar toggle collapsed the sidebar and persisted shell.sidebarCollapsed');

    await handle.page.getByRole('button', { name: 'Show sidebar' }).click();
    await expect(nav).toHaveAttribute('data-collapsed', 'false');
    settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.shell.sidebarCollapsed).toBe(false);

    transcript.section('⌘⌃S toggles the same persisted collapse path');
    await handle.page.keyboard.press('Meta+Control+S');
    await expect(nav).toHaveAttribute('data-collapsed', 'true');
    settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.shell.sidebarCollapsed).toBe(true);
    transcript.step(
      '⌘⌃S collapsed the sidebar and persisted shell.sidebarCollapsed, same as the button',
    );

    transcript.section('The explicit collapse survives a full app restart');
    persistAppLogs(handle, `${RUN_NAME}-first-run`);
    await closeApp(handle);
    handle = await launchApp(world, testInfo, { traceName: `${RUN_NAME}-relaunch` });
    await expect(handle.page.locator('nav.sidebar')).toHaveAttribute('data-collapsed', 'true', {
      timeout: 15_000,
    });
    const restoredSettings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(restoredSettings.shell.sidebarCollapsed).toBe(true);
    expect(restoredSettings.shell.sidebarWidth).toBe(270);
    transcript.step('relaunch against the same state dir restored the explicit collapse');

    // Un-collapse before the narrow-viewport check so the breakpoint's own
    // auto-collapse (not the persisted explicit choice) is what's exercised.
    await handle.page.keyboard.press('Meta+Control+S');
    await expect(handle.page.locator('nav.sidebar')).toHaveAttribute('data-collapsed', 'false');

    transcript.section(
      'Below ~700px the sidebar auto-collapses without touching the persisted setting',
    );
    await setWindowSize(handle, 640, 900);
    await expect(handle.page.locator('nav.sidebar')).toHaveAttribute('data-collapsed', 'true');
    const narrowSettings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(narrowSettings.shell.sidebarCollapsed).toBe(false);

    await setWindowSize(handle, 1440, 900);
    await expect(handle.page.locator('nav.sidebar')).toHaveAttribute('data-collapsed', 'false');
    await expect(handle.page.locator('nav.sidebar')).toHaveCSS('width', '270px');
    transcript.step(
      'narrow-viewport auto-collapse was purely visual and re-expanded above the breakpoint',
    );

    persistAppLogs(handle, RUN_NAME);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
