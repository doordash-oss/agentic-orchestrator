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

/**
 * The "Notify about" category group on the connected Slack pane, at the
 * Settings window's default footprint (900x640): all three switches on in
 * the light theme, and Progress switched off with the save bar dirty in the
 * dark theme.
 */
const DEFAULT_WIDTH = 900;
const DEFAULT_HEIGHT = 640;

const LIST_MATERIAL = {
  dark: 'rgba(38, 41, 48, 0.72)',
  light: 'rgba(246, 246, 248, 0.76)',
} as const;

async function openSlackPane(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await openScene(
    page,
    'settings-slack-connected',
    theme,
    DEFAULT_WIDTH,
    DEFAULT_HEIGHT,
    '.settings-window__nav-list',
    {
      platform: 'darwin',
    },
  );
  await expect(page.locator('.settings-window__pane-row[data-selected="true"]')).toHaveText(
    'Slack',
  );
  await expect(page.getByRole('region', { name: 'Slack', exact: true })).toBeVisible({
    timeout: 15_000,
  });
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

test('slack notify-about categories visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  // All three switches on, connected as a bot, light theme.
  await openSlackPane(page, 'light');
  const group = page.locator('section[aria-labelledby="slack-categories-title"]');
  await expect(group.getByRole('heading', { name: 'Notify about' })).toBeVisible();
  for (const name of ['Progress', 'Needs input', 'Problems'] as const) {
    const toggle = page.getByRole('checkbox', { name: new RegExp(name) });
    await expect(toggle).toBeVisible();
    await expect(toggle).toBeChecked();
  }
  await expect(group).toBeVisible();
  await expect(page.getByText(/Connected to Agentico Workspace/)).toBeVisible();
  await shoot(
    page,
    'settings-window-on-the-slack-pane-connected-as-a-bot-notify-about-group-showing-900x640',
  );

  // Progress switched off with the save bar dirty, dark theme.
  await openSlackPane(page, 'dark');
  const progress = page.getByRole('checkbox', { name: /Progress/ });
  await expect(progress).toBeChecked();
  await progress.uncheck();
  await expect(progress).not.toBeChecked();
  const saveBar = page.locator('footer.config-editor__footer');
  const dirtyStatus = page.getByText('Unsaved changes');
  const saveButton = page.getByRole('button', { name: 'Save changes' });
  await saveBar.scrollIntoViewIfNeeded();
  await expect(progress).toBeInViewport({ ratio: 1 });
  await expect(dirtyStatus).toBeInViewport({ ratio: 1 });
  await expect(saveButton).toBeEnabled();
  await expect(saveButton).toBeInViewport({ ratio: 1 });
  await expect(saveBar).toBeInViewport({ ratio: 1 });
  await expect(page.getByRole('checkbox', { name: /Needs input/ })).toBeChecked();
  await expect(page.getByRole('checkbox', { name: /Problems/ })).toBeChecked();
  await shoot(
    page,
    'settings-window-on-the-slack-pane-connected-as-a-bot-notify-about-group-with-pro-900x640',
  );
});
