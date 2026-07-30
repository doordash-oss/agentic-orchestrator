import { expect, test } from '@playwright/test';

for (const theme of ['light', 'dark'] as const) {
  test(`feature question cards and phase ladder fit the wide cockpit in ${theme} mode`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`http://localhost:9871/?scene=feature-question&theme=${theme}`);

    await expect(page.getByRole('group', { name: 'Feature pipeline' })).toBeVisible();
    await expect(page.locator('.phase-ladder__step')).toHaveCount(9);
    await expect(page.locator('.phase-spine')).toHaveCount(0);
    await expect(page.getByText('Automatic review')).toHaveCount(0);
    await expect(page.getByText('Durable setup')).toHaveCount(0);
    await expect(
      page.getByRole('heading', { name: /For the agentic-orchestrator project/ }),
    ).toBeVisible();
    await expect(page.getByText('Recommended')).toBeVisible();
    await expect(page.getByText(/\(Recommended\)/)).toHaveCount(0);
    await expect(page.getByPlaceholder('Type your own answer here')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Submit' })).toBeDisabled();

    const questionPrompt = page.getByText(/For the agentic-orchestrator project/, { exact: true });
    await expect(questionPrompt).toHaveCount(1);

    const cards = page.locator('.attention-question .attention-option');
    await expect(cards).toHaveCount(5);
    for (const card of await cards.all()) {
      const box = await card.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(1440);
    }

    await page.screenshot({
      path: testInfo.outputPath(`feature-question-${theme}.png`),
      fullPage: false,
    });

    const selectedOption = page.getByRole('radio', { name: /Build user-facing features/ });
    const selectedCard = selectedOption.locator('..');
    await selectedCard.click();
    await expect(selectedOption).toBeChecked();
    expect(await selectedCard.evaluate((card) => getComputedStyle(card).boxShadow)).not.toBe(
      'none',
    );

    await page.screenshot({
      path: testInfo.outputPath(`feature-question-${theme}-selected.png`),
      fullPage: false,
    });
  });
}
