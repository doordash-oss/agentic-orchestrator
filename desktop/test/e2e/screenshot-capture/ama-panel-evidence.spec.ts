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

/** The panel floats inside the window chrome, which only exists on darwin. */
async function openPanel(page: Page, theme: 'light' | 'dark'): Promise<void> {
  await openScene(page, 'ama-panel', theme, 1440, 900, '.ama-panel', { platform: 'darwin' });
  await expect(page.getByRole('complementary', { name: 'Ask Agentico' })).toBeVisible();
  await expect(page.getByText('Which features still touch the old polled preview?')).toBeVisible();
  await expect(page.getByRole('button', { name: / — switch server$/ })).toBeVisible();
}

/** Pastes the fixture image into the composer, producing the chip. */
async function attachImage(page: Page): Promise<void> {
  await page.locator('.ama-panel__composer textarea').evaluate((element) => {
    const transfer = new DataTransfer();
    transfer.items.add(new File(['fixture'], 'cockpit-poll.png', { type: 'image/png' }));
    element.dispatchEvent(
      new ClipboardEvent('paste', { clipboardData: transfer, bubbles: true, cancelable: true }),
    );
  });
  await expect(page.getByText('cockpit-poll.png')).toBeVisible();
}

/** A real pointer drag from the centre of `selector` by (dx, dy). */
async function dragBy(page: Page, selector: string, dx: number, dy: number): Promise<void> {
  const box = await page.locator(selector).boundingBox();
  if (box === null) throw new Error(`${selector} has no box to drag`);
  const fromX = box.x + box.width / 2;
  const fromY = box.y + box.height / 2;
  await page.mouse.move(fromX, fromY);
  await page.mouse.down();
  await page.mouse.move(fromX + dx, fromY + dy, { steps: 8 });
  await page.mouse.up();
}

test('ama panel visual evidence', async ({ page }) => {
  skipWithoutEvidenceDir();

  // The default bottom-trailing placement over a running cockpit, with the
  // accent-ruled "You" turn and the server-identity sidebar footer.
  await openPanel(page, 'dark');
  await page.mouse.move(700, 300);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'panel-open-at-the-default-bottom-trailing-placement-over-a-running-cockpit-a-use-1440x900',
  );

  // The attachment chip and the End session confirmation in the composer region.
  await openPanel(page, 'light');
  await attachImage(page);
  await page.getByRole('button', { name: 'End session', exact: true }).first().click();
  await expect(page.getByRole('group', { name: 'End session confirmation' })).toBeVisible();
  await page.mouse.move(700, 300);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'panel-open-with-an-attachment-chip-and-the-end-session-confirmation-visible-in-t-1440x900',
  );

  // Dragged and resized away from the default footprint.
  await openPanel(page, 'dark');
  await dragBy(page, '.ama-panel__header', -360, -220);
  await dragBy(page, '.ama-panel__grip[data-edge="nw"]', -90, -60);
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(
    page,
    'panel-dragged-and-resized-to-a-non-default-position-and-size-dark-theme-1440x900',
  );

  // The expanded presentation over the workspace.
  await openPanel(page, 'light');
  await page.getByRole('button', { name: 'Expand AMA' }).click();
  await expect(page.getByRole('dialog', { name: 'Expanded AMA' })).toBeVisible();
  await page.mouse.move(1400, 860);
  await page.waitForTimeout(400);
  await shoot(page, 'maximized-modal-expand-over-the-workspace-light-theme-1440x900');
});
