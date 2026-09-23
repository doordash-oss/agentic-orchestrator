import { expect, test, type Page } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

async function contract(page: Page, scene: string, theme: 'light' | 'dark') {
  await openScene(page, scene, theme, 1440, 900, '.creation-sheet');
  await page.getByRole('checkbox', { name: /signal-lab/ }).check();
  await page.getByRole('button', { name: 'Next: Describe' }).click();
  await page.getByLabel('Name').fill('Notifications sample');
  await page.getByRole('button', { name: 'Next: Depth' }).click();
  await page.getByRole('button', { name: 'Next: Contract' }).click();
  const group = page.getByRole('group', { name: 'Notifications' });
  await group.scrollIntoViewIfNeeded();
  return group;
}

test('creation Notifications configured and unconfigured screenshots', async ({ page }) => {
  skipWithoutEvidenceDir();
  for (const [theme, name] of [
    [
      'light',
      'creation-sheet-light-theme-slack-configured-contract-step-scrolled-to-the-notifi-1440x900',
    ],
    [
      'dark',
      'creation-sheet-dark-theme-slack-configured-the-same-notifications-group-in-the-s-1440x900',
    ],
  ] as const) {
    const group = await contract(page, 'creation-sheet-slack', theme);
    await expect(group.getByRole('combobox', { name: 'Needs input' })).toContainText(
      'Workspace default (off)',
    );
    await expect(group.getByText(/Ada Lovelace/)).toBeVisible();
    const first = group.getByRole('textbox', { name: 'Recipient 1' });
    await first.fill('#eng');
    await first.press('Enter');
    await expect(group.getByText('#eng', { exact: true })).toBeVisible();
    await group.getByRole('button', { name: 'Add recipient' }).click();
    const second = group.getByRole('textbox', { name: 'Recipient 2' });
    await second.fill('#private-ops');
    await second.blur();
    await expect(second).toHaveAttribute('aria-invalid', 'true');
    await expect(group.getByText(/Agentico cannot send to #private-ops/)).toBeVisible();
    await group.scrollIntoViewIfNeeded();
    await page.mouse.move(1400, 860);
    await shoot(page, name);
  }

  const unconfigured = await contract(page, 'creation-sheet-slack-off', 'light');
  await expect(unconfigured.getByText('Set up Slack in Settings')).toBeVisible();
  await expect(unconfigured.getByRole('textbox')).toHaveCount(0);
  await page.mouse.move(1400, 860);
  await shoot(
    page,
    'creation-sheet-light-theme-slack-not-configured-contract-step-scrolled-to-the-no-1440x900',
  );
});
