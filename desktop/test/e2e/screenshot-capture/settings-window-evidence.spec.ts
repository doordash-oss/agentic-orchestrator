import { expect, test, type Page } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * The Settings window at its own default footprint (900x640), in the real
 * paned chrome: the source list on the sidebar material beside exactly one
 * pane. Every shot runs with the darwin platform dataset, since the inset
 * traffic lights and the vibrant list material are what the window is judged
 * on.
 *
 * The scene's own `useTheme()` writes `<html data-theme>` once its persisted
 * preference resolves, which lands after the capture helper sets the
 * requested theme. Asserting a token value only the requested theme produces
 * — on the pane list, the surface furthest from the pane being photographed
 * — means a theme that loses that race fails the spec instead of yielding a
 * plausible-looking capture of the wrong theme.
 */
const LIST_MATERIAL = {
  dark: 'rgba(38, 41, 48, 0.72)',
  light: 'rgba(246, 246, 248, 0.76)',
} as const;

const DEFAULT_WIDTH = 900;
const DEFAULT_HEIGHT = 640;

/** The eight panes, in the order the source list shows them. */
const PANE_LABELS = [
  'Workspace roots',
  'Providers',
  'Appearance',
  'Updates',
  'Notifications',
  'Diagnostics',
  'Advanced',
  'Workspace defaults',
] as const;

async function openSettingsScene(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  paneLabel: string,
  paneRegion: string,
): Promise<void> {
  await openScene(page, scene, theme, DEFAULT_WIDTH, DEFAULT_HEIGHT, '.settings-window__nav-list', {
    platform: 'darwin',
  });
  // The source list carries every pane with its glyph, and the selected row is
  // the one pane rendered — the two halves of the window that make it a
  // settings window rather than a scrolling panel.
  await expect(page.locator('.settings-window__pane-row')).toHaveText([...PANE_LABELS]);
  await expect(page.locator('.settings-window__pane-row[data-selected="true"]')).toHaveText(
    paneLabel,
  );
  await expect(page.getByRole('region', { name: paneRegion })).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(`html[data-theme="${theme}"]`)).toBeAttached();
  await expect
    .poll(() =>
      page.locator('.settings-window__nav').evaluate((el) => getComputedStyle(el).backgroundColor),
    )
    .toBe(LIST_MATERIAL[theme]);
  // Park the pointer off both columns so no row hover reads as a selection.
  await page.mouse.move(DEFAULT_WIDTH - 8, DEFAULT_HEIGHT - 8);
  await page.waitForTimeout(300);
}

test('settings window visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  // First open: the default pane, so the capture shows what a person sees the
  // very first time the window exists.
  await openSettingsScene(
    page,
    'settings-workspace-roots',
    'light',
    'Workspace roots',
    'Workspace roots',
  );
  await expect(page.getByRole('button', { name: 'Add workspace root' })).toBeVisible();
  await shoot(
    page,
    'settings-window-workspace-roots-pane-as-first-opened-pane-source-list-with-icons-900x640',
  );

  // Where the `updates` deep link lands.
  await openSettingsScene(page, 'settings-updates-ready', 'dark', 'Updates', 'Updates');
  await expect(page.getByRole('button', { name: 'Check for updates' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Restart to Update' })).toBeVisible();
  await shoot(
    page,
    'settings-window-updates-pane-as-the-updates-deep-link-lands-dark-theme-900x640',
  );

  // Where the `diagnostics` deep link lands.
  await openSettingsScene(page, 'settings-diagnostics', 'light', 'Diagnostics', 'Diagnostics');
  await expect(page.getByRole('button', { name: 'Reveal Folder' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Clear Diagnostics' })).toBeVisible();
  await expect(page.locator('.settings-panel__diagnostic').first()).toBeVisible();
  await shoot(
    page,
    'settings-window-diagnostics-pane-as-the-diagnostics-deep-link-lands-light-theme-900x640',
  );

  // The theme switcher in its new home.
  await openSettingsScene(page, 'settings-appearance', 'dark', 'Appearance', 'Appearance');
  await expect(page.getByRole('radiogroup', { name: 'Appearance theme' })).toBeVisible();
  for (const label of ['System', 'Light', 'Dark'] as const) {
    await expect(page.getByRole('radio', { name: label })).toBeVisible();
  }
  await shoot(
    page,
    'settings-window-appearance-pane-with-the-theme-radio-in-its-new-home-dark-theme-900x640',
  );
});
