import { expect, test } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * A Bash permission prompt that auto mode would have handled carries a split
 * button beside Allow once and Deny: the main action enables auto mode for
 * this feature, the chevron menu offers all features. Shots cover the closed
 * button, the open menu, and the notice after opting in.
 */
test('auto mode split button on a permission prompt', async ({ page }) => {
  skipWithoutEvidenceDir();

  for (const theme of ['dark', 'light'] as const) {
    await openScene(page, 'attention-popover', theme, 1440, 900, '.cockpit', {
      platform: 'darwin',
    });
    const card = page.getByRole('region', { name: 'Agent request' });
    await expect(card).toBeVisible({ timeout: 15_000 });
    await card.scrollIntoViewIfNeeded();
    const split = card.getByRole('group', { name: 'Enable auto mode' });
    const main = split.getByRole('button', { name: 'Enable auto mode (this feature only)' });
    await expect(main).toBeVisible();
    await page.mouse.move(130, 700);
    await page.waitForTimeout(300);
    await shoot(page, `auto-mode-permission-card-${theme}-1440x900`);
    await card.screenshot({
      path: `${process.env['AGENTICO_EVIDENCE_DIR']}/auto-mode-card-only-${theme}.png`,
    });

    await split.getByLabel('More auto mode options').click();
    const all = card.getByRole('menuitem', { name: 'Enable auto mode (all features)' });
    await expect(all).toBeVisible();
    await page.waitForTimeout(200);
    // The menu floats below the card box, so this one needs the full window.
    await shoot(page, `auto-mode-menu-open-${theme}-1440x900`);

    if (theme === 'light') {
      await all.click();
      await expect(page.getByText('Allowed. Auto mode is on for all features.')).toBeVisible();
      await page.waitForTimeout(300);
      await shoot(page, `auto-mode-after-opt-in-${theme}-1440x900`);
    }
  }
});
