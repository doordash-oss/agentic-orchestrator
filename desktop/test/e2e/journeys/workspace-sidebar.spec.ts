/**
 * Bench sidebar shell mechanics against the packaged app: pointer
 * selection, the roving-tabindex Arrow/Home/End keyboard model (scoped to
 * whatever a lane's `<details>` disclosure currently shows), ⌘2-9 absolute
 * sidebar-position selection (reachable even inside a collapsed lane, unlike
 * Arrow/Home/End), the sidebar-toggle button and ⌘⌃S collapse paths, and the
 * ~700px auto-collapse breakpoint. ⌘1 itself is out of scope here — it only
 * renamed the existing "Home" label to "Overview"; the dispatch path
 * (native menu accelerator → routeRequest) is unchanged and already covered
 * elsewhere.
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
      // Back to Overview so "New feature" is reachable for the next one.
      await handle.page.getByRole('option', { name: 'Overview' }).click();
    }
    const atRestGroup = handle.page.getByRole('group', { name: 'At rest' });
    await expect(atRestGroup).toBeVisible();
    for (const name of names) {
      await expect(atRestGroup.getByText(name, { exact: true })).toBeVisible();
    }
    transcript.step('three features created, all grouped under At rest');

    transcript.section('Pointer selection switches the mounted content');
    const overviewRow = handle.page.getByRole('option', { name: 'Overview' });
    const rows = handle.page.getByRole('listbox', { name: 'Features' }).getByRole('option');
    await expect(rows).toHaveCount(4); // Overview + the three created features.
    // The row's bare name only — at-rest rows also carry a status sub-line
    // ("Code ready") in the same option, which a full textContent read
    // would otherwise glue onto the name.
    const rowNames = await handle.page
      .getByRole('listbox', { name: 'Features' })
      .locator('.sidebar__row-name')
      .allTextContents();
    // Visual/DOM order: Overview first, then the three features in lane order.
    expect(rowNames[0]).toBe('Overview');
    const [, firstName, secondName, thirdName] = rowNames as [string, string, string, string];

    await rows.nth(1).click();
    await expect(handle.page.getByLabel(`Feature ${firstName}`)).toBeVisible({ timeout: 15_000 });
    await expect(rows.nth(1)).toHaveAttribute('aria-selected', 'true');
    await expect(overviewRow).toHaveAttribute('aria-selected', 'false');
    transcript.step(`clicking "${firstName}" mounted its cockpit`);

    transcript.section('Arrow/Home/End move focus and selection together through visible rows');
    await overviewRow.click();
    await expect(overviewRow).toHaveAttribute('aria-selected', 'true');
    await overviewRow.focus();

    await handle.page.keyboard.press('ArrowDown');
    await expect(rows.nth(1)).toBeFocused();
    await expect(rows.nth(1)).toHaveAttribute('aria-selected', 'true');
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
    await expect(overviewRow).toBeFocused();
    await expect(overviewRow).toHaveAttribute('aria-selected', 'true');
    transcript.step('ArrowDown/End/Home moved focus and the mounted content together');

    transcript.section(
      '⌘2-9 select by absolute sidebar position — reachable even inside a collapsed lane',
    );
    await handle.page.locator('summary.sidebar__lane-summary', { hasText: 'At rest' }).click();
    await expect(atRestGroup).toBeHidden();

    // ⌘2 → the 1st feature in absolute order, ⌘4 → the 3rd — both still
    // inside the now-collapsed At rest lane.
    await handle.page.keyboard.press('ControlOrMeta+2');
    await expect(handle.page.getByLabel(`Feature ${firstName}`)).toBeVisible({ timeout: 15_000 });
    await handle.page.keyboard.press('ControlOrMeta+4');
    await expect(handle.page.getByLabel(`Feature ${thirdName}`)).toBeVisible({ timeout: 15_000 });
    transcript.step(
      `⌘2 and ⌘4 selected "${firstName}" and "${thirdName}" while their lane stayed collapsed`,
    );

    await handle.page.locator('summary.sidebar__lane-summary', { hasText: 'At rest' }).click();
    await expect(atRestGroup).toBeVisible();

    transcript.section('The toolbar sidebar-toggle button collapses and persists the choice');
    await handle.page.getByRole('option', { name: 'Overview' }).click();
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
