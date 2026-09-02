import { expect, test } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

/**
 * A Bash permission prompt that automatic review would have handled carries an
 * auto-approve offer. The cockpit's inline "Agent request" card shows the
 * offer under the usual Allow / Deny controls, then the notice after opting in.
 */
test('auto-approve offer on a permission prompt', async ({ page }) => {
  skipWithoutEvidenceDir();

  for (const theme of ['dark', 'light'] as const) {
    await openScene(page, 'attention-popover', theme, 1440, 900, '.cockpit', {
      platform: 'darwin',
    });
    const card = page.getByRole('region', { name: 'Agent request' });
    await expect(card).toBeVisible({ timeout: 15_000 });
    await card.scrollIntoViewIfNeeded();
    const offer = card.getByRole('group', { name: 'Auto-approve commands' });
    await expect(offer).toBeVisible();
    await expect(offer.getByText(/would have run without asking/)).toBeVisible();
    await expect(
      offer.getByRole('button', { name: 'Allow and auto-approve in this feature' }),
    ).toBeVisible();
    await expect(
      offer.getByRole('button', { name: 'Allow and auto-approve everywhere' }),
    ).toBeVisible();
    await page.mouse.move(130, 700);
    await page.waitForTimeout(300);
    await shoot(page, `auto-approve-offer-permission-card-${theme}-1440x900`);
    await card.screenshot({
      path: `${process.env['AGENTICO_EVIDENCE_DIR']}/auto-approve-offer-card-only-${theme}.png`,
    });

    if (theme === 'light') {
      await offer.getByRole('button', { name: 'Allow and auto-approve in this feature' }).click();
      await expect(page.getByText('Auto-approve commands is on for this feature')).toBeVisible();
      await page.waitForTimeout(300);
      await shoot(page, `auto-approve-offer-after-opt-in-${theme}-1440x900`);
    }
  }
});
