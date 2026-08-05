/**
 * Window-chrome smoke journey: launch → first paint matches theme → drag
 * from header → every header control clickable → appearance toggle.
 *
 * macOS-only: the chrome under test (inset traffic lights, the drag region,
 * the vibrancy material) is a macOS-only accommodation. Off macOS the
 * header keeps its plain native-frame layout with nothing new to
 * smoke-test here.
 *
 * "Drag from header" is asserted at the CSS-contract level
 * (`-webkit-app-region`), not by simulating an OS window move: Playwright's
 * CDP-driven mouse has no OS-level drag loop to complete, so the meaningful,
 * deterministic check is that the region and its exclusions are wired
 * correctly — the region itself is exercised for real every time a user
 * drags the window.
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  contractEvidenceShot,
  launchApp,
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

    transcript.section('The header is a traffic-light-clearing drag region');
    const globalBar = handle.page.locator('.global-bar');
    const dragStyle = await globalBar.evaluate((element) =>
      getComputedStyle(element).getPropertyValue('-webkit-app-region'),
    );
    expect(dragStyle).toBe('drag');
    const brandLeft = await handle.page
      .locator('.global-bar__brand')
      .evaluate((element) => element.getBoundingClientRect().left);
    // Traffic lights sit at x:18 with a ~54px cluster; header content must
    // clear that at every width down to the 400px minimum.
    expect(brandLeft).toBeGreaterThanOrEqual(72);

    transcript.section(
      'Every header control excludes itself from the drag region and stays clickable',
    );
    const bell = handle.page.getByRole('button', { name: /Attention inbox/ });
    await expect(bell).toBeVisible();
    expect(
      await bell.evaluate((element) =>
        getComputedStyle(element).getPropertyValue('-webkit-app-region'),
      ),
    ).toBe('no-drag');
    await bell.click();
    await expect(handle.page.locator('#attention-inbox')).toBeVisible();
    await handle.page.keyboard.press('Escape');

    for (const label of ['Light', 'Dark', 'System'] as const) {
      const radio = handle.page.locator('.theme-switcher').getByRole('radio', { name: label });
      expect(
        await radio.evaluate((element) =>
          getComputedStyle(element).getPropertyValue('-webkit-app-region'),
        ),
      ).toBe('no-drag');
      await expect(radio).toBeEnabled();
    }

    transcript.section('Traffic lights stay clear of header content at the 400px minimum');
    await setWindowSize(handle, 400, 480);
    const brandLeftAtMinimum = await handle.page
      .locator('.global-bar__brand')
      .evaluate((element) => element.getBoundingClientRect().left);
    expect(brandLeftAtMinimum).toBeGreaterThanOrEqual(72);

    transcript.section('Every header control stays inside the viewport at the 400px minimum');
    await expect(bell).toBeVisible();
    const bellBox = await bell.boundingBox();
    expect(bellBox).not.toBeNull();
    expect(bellBox!.x + bellBox!.width).toBeLessThanOrEqual(400);
    for (const label of ['Light', 'Dark', 'System'] as const) {
      const radio = handle.page.locator('.theme-switcher').getByRole('radio', { name: label });
      await expect(radio).toBeVisible();
      await expect(radio).toBeEnabled();
      const box = await radio.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x + box!.width).toBeLessThanOrEqual(400);
    }
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
