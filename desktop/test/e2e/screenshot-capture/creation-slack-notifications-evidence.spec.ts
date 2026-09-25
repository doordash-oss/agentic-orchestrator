/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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

test('inherited category values fit across supported window widths', async ({ page }) => {
  for (const width of [400, 480, 600, 700, 900, 1440]) {
    const group = await contract(page, 'creation-sheet-slack', 'light');
    await page.setViewportSize({ width, height: 700 });
    for (const select of await group.getByRole('combobox').all()) {
      const dimensions = await select.evaluate((element) => {
        const control = element as HTMLSelectElement;
        const style = getComputedStyle(control);
        const canvas = document.createElement('canvas');
        const context = canvas.getContext('2d');
        if (!context) throw new Error('Canvas text measurement unavailable');
        context.font = style.font;
        return {
          actual: control.clientWidth,
          needed:
            context.measureText(control.selectedOptions[0]?.textContent ?? '').width +
            parseFloat(style.paddingLeft) +
            parseFloat(style.paddingRight) +
            20,
        };
      });
      expect(dimensions.actual, `select at ${width}px`).toBeGreaterThanOrEqual(dimensions.needed);
    }
  }
});
