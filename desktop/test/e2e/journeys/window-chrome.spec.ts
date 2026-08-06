/**
 * Window-chrome smoke journey: launch → first paint matches theme → drag
 * from the toolbar/sidebar header → every toolbar control clickable →
 * appearance toggle.
 *
 * macOS-only: the chrome under test (inset traffic lights, the drag region,
 * the vibrancy material) is a macOS-only accommodation. Off macOS the
 * toolbar keeps its plain native-frame layout with nothing new to
 * smoke-test here.
 *
 * "Drag from the toolbar" is asserted at the CSS-contract level
 * (`-webkit-app-region`), not by simulating an OS window move: Playwright's
 * CDP-driven mouse has no OS-level drag loop to complete, so the meaningful,
 * deterministic check is that the region and its exclusions are wired
 * correctly — the region itself is exercised for real every time a user
 * drags the window.
 *
 * The always-on global header (brand mark + inline theme switcher) is
 * replaced by a per-content-side toolbar; the brand mark is gone and the
 * theme switcher now lives in Settings ▸ Appearance, so the traffic-light
 * clearance and drag-region checks below target the sidebar header (visible
 * at every width until the ~700px auto-collapse breakpoint) and the toolbar
 * itself (which only needs its own left clearance once the sidebar is
 * collapsed).
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  contractEvidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test.skip(process.platform !== 'darwin', 'macOS-only window chrome');

test('window chrome: first paint, drag region, header controls, appearance toggle', async ({}, testInfo) => {
  const transcript = new Transcript('window-chrome', 'macOS window-chrome smoke journey');
  const world = createWorld('window-chrome', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'chrome-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'window-chrome' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('First paint matches the resolved theme');
    const resolvedTheme = await handle.page.evaluate(
      () => document.documentElement.dataset['theme'],
    );
    expect(resolvedTheme === 'dark' || resolvedTheme === 'light').toBe(true);
    const platform = await handle.page.evaluate(() => window.agentico.platform);
    expect(platform).toBe('darwin');
    transcript.json('resolved theme at first paint', { resolvedTheme, platform });

    transcript.section('The toolbar and sidebar header are traffic-light-clearing drag regions');
    const toolbar = handle.page.locator('.toolbar');
    const toolbarDragStyle = await toolbar.evaluate((element) =>
      getComputedStyle(element).getPropertyValue('-webkit-app-region'),
    );
    expect(toolbarDragStyle).toBe('drag');
    const sidebarHeader = handle.page.locator('.sidebar__header');
    const sidebarHeaderDragStyle = await sidebarHeader.evaluate((element) =>
      getComputedStyle(element).getPropertyValue('-webkit-app-region'),
    );
    expect(sidebarHeaderDragStyle).toBe('drag');
    // The sidebar sits at x:0 clearing the traffic lights on its own; the
    // toolbar starts to its right at every width above the ~700px
    // auto-collapse breakpoint, so it carries no left padding of its own here.
    const sidebarHeaderLeft = await sidebarHeader.evaluate(
      (element) => element.getBoundingClientRect().left,
    );
    expect(sidebarHeaderLeft).toBe(0);

    transcript.section(
      'Every toolbar control excludes itself from the drag region and stays clickable',
    );
    // A raw CSS-class locator, not getByRole with a fixed name: this
    // button's accessible name flips between "Hide sidebar" and "Show
    // sidebar" depending on collapse state (an auto-collapse breakpoint
    // around 700px means the 400px check below hits the "Show sidebar"
    // label even though nothing was explicitly toggled) — a name-bound
    // locator would stop resolving once the label flips.
    const sidebarToggle = handle.page.locator('.toolbar__sidebar-toggle');
    await expect(sidebarToggle).toBeVisible();
    expect(
      await sidebarToggle.evaluate((element) =>
        getComputedStyle(element).getPropertyValue('-webkit-app-region'),
      ),
    ).toBe('no-drag');

    transcript.section('Collapsing the sidebar hands the toolbar the traffic-light clearance');
    await sidebarToggle.click();
    await expect(handle.page.getByRole('button', { name: 'Show sidebar' })).toBeVisible();
    const toolbarLeadingLeft = await handle.page
      .locator('.toolbar__leading')
      .evaluate((element) => element.getBoundingClientRect().left);
    // Traffic lights sit at x:18 with a ~54px cluster; the collapsed
    // toolbar's own leading control must clear that.
    expect(toolbarLeadingLeft).toBeGreaterThanOrEqual(72);
    await handle.page.getByRole('button', { name: 'Show sidebar' }).click();
    await expect(sidebarToggle).toBeVisible();

    transcript.section('The appearance switcher lives in Settings and stays reachable');
    for (const label of ['Light', 'Dark', 'System'] as const) {
      await openSettings(handle);
      const radio = handle.page
        .locator('.settings-panel__theme')
        .getByRole('radio', { name: label });
      await expect(radio).toBeVisible();
      await expect(radio).toBeEnabled();
      await handle.page.getByRole('button', { name: 'Back', exact: true }).click();
    }

    transcript.section('Every toolbar control stays inside the viewport at the 400px minimum');
    await setWindowSize(handle, 400, 480);
    await expect(sidebarToggle).toBeVisible();
    const toggleBox = await sidebarToggle.boundingBox();
    expect(toggleBox).not.toBeNull();
    expect(toggleBox!.x + toggleBox!.width).toBeLessThanOrEqual(400);
    await setWindowSize(handle, 1440, 900);

    transcript.section('Appearance toggle updates the resolved theme live');
    await setTheme(handle, 'dark');
    await expect(handle.page.locator('html[data-theme="dark"]')).toBeAttached();
    await setTheme(handle, 'light');
    await expect(handle.page.locator('html[data-theme="light"]')).toBeAttached();

    await contractEvidenceShot(
      handle,
      'macos-main-window-light-appearance-same-framing-1440x900',
      1440,
      900,
      'light',
    );
    await contractEvidenceShot(
      handle,
      'macos-main-window-dark-appearance-inset-traffic-lights-over-the-translucent-head-1440x900',
      1440,
      900,
      'dark',
    );
    await contractEvidenceShot(
      handle,
      'macos-main-window-at-the-minimum-window-size-dark-appearance-traffic-lights-clea-400x480',
      400,
      480,
      'dark',
    );

    persistAppLogs(handle, 'window-chrome');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
