import { expect, test } from '@playwright/test';

for (const theme of ['light', 'dark'] as const) {
  test(`the pending question reads as a conversation turn in ${theme} mode`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto(`http://localhost:9871/?scene=feature-question&theme=${theme}`);

    await expect(page.getByRole('group', { name: 'Run phases' })).toBeVisible();
    await expect(page.locator('.phase-rail__segment')).toHaveCount(9);

    // The question is the agent's next turn inside the live transcript.
    const turn = page.getByRole('group', { name: 'Agent question' });
    await expect(turn).toBeVisible();
    await expect(
      turn.getByRole('heading', { name: /For the agentic-orchestrator project/ }),
    ).toBeVisible();
    await expect(turn.getByText('Recommended')).toBeVisible();
    await expect(turn.getByText(/\(Recommended\)/)).toHaveCount(0);

    const cards = turn.locator('.attention-option');
    await expect(cards).toHaveCount(4);
    for (const card of await cards.all()) {
      const box = await card.boundingBox();
      expect(box).not.toBeNull();
      expect(box!.x).toBeGreaterThanOrEqual(0);
      expect(box!.x + box!.width).toBeLessThanOrEqual(1440);
    }

    // The composer strip is docked under the activity; Send waits for an answer.
    const composer = page.getByRole('region', { name: 'Agent request' });
    await expect(composer.getByRole('button', { name: 'Send' })).toBeDisabled();
    await expect(
      composer.getByPlaceholder('Choose an option above, or type your own answer'),
    ).toBeVisible();

    await page.screenshot({
      path: testInfo.outputPath(`feature-question-${theme}.png`),
      fullPage: false,
    });

    const selectedOption = turn.getByRole('radio', { name: /Build user-facing features/ });
    const selectedCard = selectedOption.locator('..');
    await selectedCard.click();
    await expect(selectedOption).toBeChecked();
    expect(await selectedCard.evaluate((card) => getComputedStyle(card).boxShadow)).not.toBe(
      'none',
    );

    // The choice previews as your drafted reply and arms Send.
    await expect(page.getByText('Your reply — not sent yet')).toBeVisible();
    await expect(composer.getByRole('button', { name: 'Send' })).toBeEnabled();

    await page.screenshot({
      path: testInfo.outputPath(`feature-question-${theme}-selected.png`),
      fullPage: false,
    });
  });
}
