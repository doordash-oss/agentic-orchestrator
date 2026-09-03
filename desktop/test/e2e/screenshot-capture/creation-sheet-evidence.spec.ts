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

/** The sheet overlays the draggable toolbar, which only exists on darwin. */
async function openSheet(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await openScene(page, 'creation-sheet', theme, 1440, 900, '.creation-sheet', {
    platform: 'darwin',
  });
  await expect(page.getByRole('button', { name: 'Next: Describe' })).toBeVisible();
}

/** Selects both discovered repositories and advances to Describe. */
async function reachDescribe(page: Page): Promise<void> {
  await page.getByRole('checkbox', { name: /signal-lab/ }).check();
  await page.getByRole('checkbox', { name: /orchestrator-core/ }).check();
  await page.getByRole('button', { name: 'Next: Describe' }).click();
}

/** Fills the brief with a reference chip, both attachment kinds, and a name. */
async function fillDescribe(page: Page): Promise<void> {
  await page
    .locator('#feature-description')
    .fill('Translate the README into Italian, keeping every code block and link untouched.');
  await page.locator('#feature-description').pressSequentially(' Match the tone of @sty');
  const reference = page.getByRole('option', { name: /signal-lab.*style-guide/ });
  await expect(reference).toBeVisible();
  await reference.click();
  await page.getByRole('button', { name: 'Attach files or photos' }).click();
  await page.getByRole('menuitem', { name: 'Add photos' }).click();
  await page.getByRole('button', { name: 'Attach files or photos' }).click();
  await page.getByRole('menuitem', { name: 'Add files' }).click();
  await page.locator('#feature-name').fill('translate README to Italian');
  await expect(page.getByText('reference-layout.png')).toBeVisible();
}

/** Advances a filled Describe step through Depth (Large) to Contract. */
async function reachContract(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Next: Depth' }).click();
  await page.getByRole('radio', { name: /Large/ }).check();
  await page.getByRole('button', { name: 'Next: Contract' }).click();
  await expect(page.getByRole('heading', { name: 'Review the run contract' })).toBeVisible();
}

test('creation sheet visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  // Describe: Repositories green-checked behind it, Describe accent-underlined,
  // a filled name and both chip kinds under the brief.
  await openSheet(page, 'dark');
  await reachDescribe(page);
  await fillDescribe(page);
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'describe-step-name-filled-reference-and-attachment-chips-rail-with-repositories-1440x900',
  );

  // Contract: depth cards, the retitled checkpoint group with the force-checked
  // roadmap/phase-plan pair, the live footer summary, and Create and start.
  await reachContract(page);
  await expect(page.getByText('Where the run stops for you')).toBeVisible();
  await expect(page.getByRole('checkbox', { name: /Roadmap review/ })).toBeChecked();
  await expect(page.getByRole('checkbox', { name: /Phase plan review/ })).toBeChecked();
  await expect(page.getByText('3 checkpoints · 2 repositories')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Create and start' })).toBeVisible();
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'contract-step-profile-cards-where-the-run-stops-for-you-group-with-the-roadmap-p-1440x900',
  );

  // Contract, scrolled to the per-phase model/effort rows and Start immediately.
  await openSheet(page, 'light');
  await reachDescribe(page);
  await page.locator('#feature-name').fill('translate README to Italian');
  await reachContract(page);
  await expect(page.getByLabel('Planning model')).toBeVisible();
  await page.getByRole('checkbox', { name: /Start immediately/ }).scrollIntoViewIfNeeded();
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'contract-step-scrolled-to-the-model-effort-pickers-and-the-start-immediately-row-1440x900',
  );

  // The discard confirmation over a dirty sheet.
  await openSheet(page, 'light');
  await reachDescribe(page);
  await page.locator('#feature-name').fill('translate README to Italian');
  await page.getByRole('button', { name: 'Cancel' }).click();
  await expect(page.getByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(page, 'discard-confirmation-over-a-dirty-creation-sheet-light-theme-1440x900');
});
