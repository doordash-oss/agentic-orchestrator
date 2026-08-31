import { expect, test, type Page } from '@playwright/test';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

async function open(
  page: Page,
  scene: string,
  theme: 'light' | 'dark',
  waitFor: string,
): Promise<void> {
  // The gate sheet overlays the draggable toolbar, which only exists on darwin.
  await openScene(page, scene, theme, 1440, 900, waitFor, { platform: 'darwin' });
}

test('question turn and gate sheet visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  await open(page, 'feature-question-bench', 'dark', '.question-turn');
  await page.waitForTimeout(400);
  await shoot(
    page,
    'inline-question-turn-pending-in-the-transcript-no-selection-attention-block-with-1440x900',
  );

  await open(page, 'feature-question-bench', 'light', '.question-turn');
  await page
    .getByRole('group', { name: 'Agent question' })
    .getByRole('radio', { name: /Replace it with the new translation/ })
    .locator('..')
    .click();
  await expect(page.getByText('Your reply — not sent yet')).toBeVisible();
  // Park the pointer off the option list so no row shows a hover state.
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(300);
  await shoot(
    page,
    'inline-question-turn-with-an-accent-filled-selection-and-the-your-reply-not-sent-1440x900',
  );

  await open(page, 'gate-sheet-plain', 'dark', '.need-input-sheet');
  await expect(page.getByRole('button', { name: 'Resume agent' })).toBeDisabled();
  await page.waitForTimeout(400);
  await shoot(
    page,
    'gate-sheet-plain-branch-with-two-questions-and-one-answer-drafted-spelled-out-ti-1440x900',
  );

  await open(page, 'gate-sheet-verification', 'light', '.need-input-sheet');
  await page.getByRole('radio', { name: /Waive blocked checks/ }).click();
  await expect(page.getByRole('button', { name: 'Waive and resume' })).toBeEnabled();
  await page.waitForTimeout(300);
  await shoot(
    page,
    'gate-sheet-verification-decision-branch-with-waive-and-resume-selected-and-its-w-1440x900',
  );
});
