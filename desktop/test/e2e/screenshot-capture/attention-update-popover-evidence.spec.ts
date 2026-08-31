import { expect, test, type Page } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * Both popovers hang off the toolbar, which is the macOS drag region, so every
 * shot runs with the darwin chrome the popover ring and shadow are tuned for.
 *
 * The shell's own `useTheme()` writes `<html data-theme>` once its persisted
 * preference resolves, which lands after the capture helper sets the requested
 * theme. Asserting a token value only the requested theme produces — on the
 * sidebar, the surface furthest from the popover being photographed — means a
 * theme that loses that race fails the spec instead of yielding a
 * plausible-looking capture of the wrong theme.
 */
const SIDEBAR_MATERIAL = {
  dark: 'rgba(38, 41, 48, 0.72)',
  light: 'rgba(246, 246, 248, 0.76)',
} as const;

async function expectTheme(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await expect(page.locator(`html[data-theme="${theme}"]`)).toBeAttached();
  await expect
    .poll(() => page.locator('.sidebar').evaluate((el) => getComputedStyle(el).backgroundColor))
    .toBe(SIDEBAR_MATERIAL[theme]);
}

async function openAttentionScene(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await openScene(page, 'attention-popover', theme, 1440, 900, '.attention-bell', {
    platform: 'darwin',
  });
  // A real feature cockpit behind the popover, not an empty Overview.
  await expect(page.locator('.cockpit').first()).toBeVisible({ timeout: 15_000 });
  await expect(page.locator('.toolbar__title-name')).toHaveText('History and Rewind');
  await expectTheme(page, theme);
}

async function openUpdateScene(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await openScene(page, 'update-popover', theme, 1440, 900, '.update-trigger', {
    platform: 'darwin',
  });
  await expect(page.getByRole('button', { name: 'Show available update' })).toBeVisible();
  // The bell badge and the Overview "Waiting on you" lane read from different
  // sources (the attention snapshot and the feature summaries); both captures of
  // this scene show them side by side, so hold them to the same number.
  await expect(page.locator('.attention-bell__count')).toHaveText('1');
  await expect(page.locator('.overview-lane__count').first()).toHaveText('1');
  await expectTheme(page, theme);
}

/** The bell is the only way into the inbox popover. */
async function openInbox(page: Page): Promise<void> {
  await page.getByRole('button', { name: /Attention inbox, \d+ pending/ }).click();
  await expect(page.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
}

/**
 * Parks the pointer clear of both surfaces so no hover state leaks in. The
 * empty sidebar below the feature lanes is the one region that carries no
 * hoverable row in any of these four scenes — parking on the content pane left
 * a token-driven row highlight that reads as a selection at thumbnail size.
 */
async function settle(page: Page): Promise<void> {
  await page.mouse.move(130, 700);
  await page.waitForTimeout(400);
}

test('attention and update popover visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  // The inbox open from the bell over a running cockpit: a non-zero badge on
  // the bell and a genuinely mixed list (gate, permission, review).
  await openAttentionScene(page, 'dark');
  await openInbox(page);
  await expect(page.locator('.attention-bell__count')).toBeVisible();
  await expect(page.locator('.attention-bell__count')).toHaveText('3');
  await expect(page.locator('.attention-popover__item')).toHaveCount(3);
  await expect(page.locator('.attention-popover__kind', { hasText: 'Input gate' })).toBeVisible();
  await expect(page.locator('.attention-popover__kind', { hasText: 'Permission' })).toBeVisible();
  await expect(page.locator('.attention-popover__kind', { hasText: 'Review' })).toBeVisible();
  await settle(page);
  await shoot(
    page,
    'attention-popover-open-from-the-bell-over-a-feature-cockpit-badge-showing-a-non-1440x900',
  );

  // The ownerless gate expanded inline — the only item kind the popover answers
  // in place — showing the structured verification decision branch.
  await openAttentionScene(page, 'light');
  await openInbox(page);
  await page.locator('.attention-popover__item', { hasText: 'Input gate' }).click();
  await expect(page.locator('.need-input-verification')).toBeVisible();
  await expect(page.getByText('Deployment smoke test')).toBeInViewport();
  // The popover scrolls past 34rem, so the decision branch has to be in frame,
  // not merely rendered.
  await expect(page.getByRole('radio', { name: /granted access/ })).toBeInViewport();
  await expect(page.getByRole('radio', { name: /Waive blocked checks/ })).toBeInViewport();
  await settle(page);
  await shoot(
    page,
    'attention-popover-with-an-ownerless-item-expanded-inline-gate-detail-with-the-ve-1440x900',
  );

  // The update popover open from its toolbar trigger with all three actions,
  // and the non-interactive sidebar footer dot that shares its predicate.
  await openUpdateScene(page, 'light');
  await page.getByRole('button', { name: 'Show available update' }).click();
  await expect(page.getByRole('region', { name: 'Available update' })).toBeVisible();
  await expect(page.locator('.update-popover__action', { hasText: 'Updates' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Install When Idle' })).toBeVisible();
  await expect(page.locator('.update-popover__action', { hasText: 'Dismiss' })).toBeVisible();
  await expect(page.getByRole('img', { name: 'Update available' })).toBeVisible();
  await settle(page);
  await shoot(
    page,
    'update-popover-open-from-the-toolbar-update-button-showing-all-three-actions-sid-1440x900',
  );

  // Overview at rest: the permanent bell and the transient update button side by
  // side in the toolbar, no popover open, and nothing in the flow between the
  // toolbar and the content pane where the banner used to sit.
  await openUpdateScene(page, 'dark');
  await expect(page.getByRole('button', { name: /Attention inbox, \d+ pending/ })).toBeVisible();
  await expect(page.getByRole('complementary', { name: 'Attention inbox' })).toHaveCount(0);
  await expect(page.getByRole('region', { name: 'Available update' })).toHaveCount(0);
  const flowChildren = await page
    .locator('.content-column')
    .evaluate((column) =>
      Array.from(column.children).map((child) => child.className.split(' ')[0]),
    );
  expect(flowChildren).toEqual(['toolbar', 'content-pane']);
  await settle(page);
  await shoot(
    page,
    'overview-with-the-always-visible-bell-and-the-transient-update-button-in-the-too-1440x900',
  );
});
