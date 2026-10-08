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

import fs from 'node:fs';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  SUPERVISOR_E2E_MARKERS,
  waitFor,
} from '../helpers/world';

test('supervisor changes settings while idle and during a held turn', async ({}, testInfo) => {
  const world = createWorld('supervisor-settings-change', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'settings-lab', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-settings-change' });
    const page = handle.page;
    const chip = page.getByTestId('supervisor-model-chip');
    const composer = page.getByRole('textbox', { name: 'Message the supervisor' });
    const conversation = page.getByRole('region', { name: 'Supervisor conversation' });
    const choose = async (name: string) => {
      const popover = page.getByRole('region', { name: 'Harness and model' });
      if (!(await popover.isVisible())) await chip.click();
      await popover
        .getByRole('group', { name: 'Claude' })
        .getByRole('radio', { name })
        .locator('..')
        .click();
      await page.keyboard.press('Escape');
    };
    const send = async (text: string) => {
      await composer.fill(text);
      await page.getByRole('button', { name: 'Send', exact: true }).click();
    };

    await choose('Haiku');
    await send('Hello');
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle');
    await choose('Sonnet');
    await expect(chip).toHaveAccessibleName('Claude Sonnet · Default');
    await expect(conversation).toContainText('Model changed to');
    await send('Second turn');
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle');
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(2);
    expect(fs.readFileSync(world.providerInvocationLog, 'utf8')).toContain('launch-model:sonnet');

    await send(`Hold ${SUPERVISOR_E2E_MARKERS.hold}`);
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute(
      'data-lifecycle',
      'running',
    );
    await chip.click();
    await page
      .getByRole('region', { name: 'Harness and model' })
      .getByRole('group', { name: 'Effort' })
      .getByText('High', { exact: true })
      .click();
    await expect(page.getByText(/Effort change pending/)).toBeVisible();
    await expect(chip).toHaveAccessibleName('Claude Sonnet · Default');
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByText(/Effort change pending/)).toHaveCount(0);
    // The tray click already dismissed the popover; an Escape here would now
    // stop the turn itself, so the explicit Stop below does it.
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle');

    await send(`Hold again ${SUPERVISOR_E2E_MARKERS.hold}`);
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute(
      'data-lifecycle',
      'running',
    );
    await choose('Haiku');
    await expect(page.getByText(/Model change pending/)).toBeVisible();
    await page.getByRole('button', { name: 'Stop turn & apply' }).click();
    await expect(chip).toHaveAccessibleName('Claude Haiku · Default');
    await expect(conversation).toContainText('Model changed to');
    await choose('Sonnet');
    await expect(chip).toHaveAccessibleName('Claude Sonnet · Default');
    await composer.fill('/effort high');
    await composer.press('Enter');
    await expect(chip).toHaveAccessibleName('Claude Sonnet · High');
    expect(await conversation.getByText('/effort high').count()).toBe(0);
    await waitFor(
      () => providerInvocationCount(world.providerInvocationLog) === 2,
      'no extra launch before another message',
    );
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'supervisor-settings-change');
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
