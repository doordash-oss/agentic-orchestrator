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
  SUPERVISOR_E2E_MARKERS,
  waitFor,
} from '../helpers/world';

test('supervisor switches harness, restores on launch failure, and cancels a queued switch', async ({}, testInfo) => {
  const world = createWorld('supervisor-harness-switch', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
    unlaunchableCodex: true,
  });
  createRepo(world, 'switch-lab', { commit: true });
  let handle: AppHandle | null = null;
  const codexLaunches = (): number => {
    if (!fs.existsSync(world.codexInvocationLog)) return 0;
    return fs
      .readFileSync(world.codexInvocationLog, 'utf8')
      .split('\n')
      .filter((line) => line.includes('app-server')).length;
  };
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-harness-switch' });
    const page = handle.page;
    const chip = page.getByTestId('supervisor-model-chip');
    const composer = page.getByRole('textbox', { name: 'Message the supervisor' });
    const status = page.getByTestId('supervisor-status');
    const conversation = page.getByRole('region', { name: 'Supervisor conversation' });
    const popover = page.getByRole('region', { name: 'Harness and model' });
    const send = async (text: string) => {
      await composer.fill(text);
      await page.getByRole('button', { name: 'Send', exact: true }).click();
    };

    await chip.click();
    await popover
      .getByRole('group', { name: 'Claude' })
      .getByRole('radio', { name: 'Haiku' })
      .locator('..')
      .click();
    await page.keyboard.press('Escape');
    await send('Remember this conversation');
    await expect(status).toHaveAttribute('data-lifecycle', 'idle');

    await chip.click();
    await popover
      .getByRole('group', { name: 'Codex' })
      .getByRole('button', { name: 'Switch to Codex…' })
      .click();
    const dialog = page.getByRole('dialog', { name: 'Switch to Codex' });
    await expect(dialog).toContainText(
      'This conversation will be rebuilt for Codex at the next message.',
    );
    await expect(dialog).toContainText('Sub-agents and background shells');
    await dialog.getByRole('button', { name: 'Switch', exact: true }).click();
    await expect(conversation).toContainText('Switched to Codex');
    await expect(chip).toHaveAccessibleName(/Codex .* · Default/);
    await send('Try the new harness');
    await waitFor(() => codexLaunches() >= 1, 'first Codex app-server launch');
    await expect(status).toHaveAttribute('data-lifecycle', 'failed', { timeout: 45_000 });
    await expect(chip).toHaveAccessibleName('Claude Haiku · Default');
    const card = page.getByRole('alert');
    await expect(card).toContainText("Couldn't switch to Codex — still using Claude");
    await expect(composer).toHaveValue('Try the new harness');
    const firstLaunches = codexLaunches();
    expect(firstLaunches).toBeGreaterThanOrEqual(1);

    await card.getByRole('button', { name: 'Retry' }).click();
    await waitFor(() => codexLaunches() > firstLaunches, 'second Codex app-server launch');
    await expect(status).toHaveAttribute('data-lifecycle', 'failed', { timeout: 45_000 });
    await expect(card).toContainText("Couldn't switch to Codex — still using Claude");
    await send('Continue on Claude');
    await expect(status).toHaveAttribute('data-lifecycle', 'idle');
    await expect(chip).toHaveAccessibleName('Claude Haiku · Default');
    await expect(conversation).toContainText('Supervisor reply');

    await send(`Hold ${SUPERVISOR_E2E_MARKERS.hold}`);
    await expect(status).toHaveAttribute('data-lifecycle', 'running');
    await chip.click();
    const codexRadios = popover.getByRole('group', { name: 'Codex' }).getByRole('radio');
    const chosenModel = await codexRadios.nth(1).getAttribute('value');
    await codexRadios.nth(1).locator('..').click();
    await expect(dialog).toBeVisible();
    if (chosenModel !== null) {
      await expect(dialog.getByRole('combobox', { name: 'Model' })).toHaveValue(
        chosenModel.slice('codex:'.length),
      );
    }
    await dialog.getByRole('button', { name: 'Switch', exact: true }).click();
    await expect(page.getByText(/Switch to Codex pending/)).toBeVisible();
    await expect(chip).toHaveAccessibleName('Claude Haiku · Default');
    await page.getByRole('button', { name: 'Cancel' }).click();
    await expect(page.getByText(/Switch to Codex pending/)).toHaveCount(0);
    await expect(chip).toHaveAccessibleName('Claude Haiku · Default');
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await expect(status).toHaveAttribute('data-lifecycle', 'idle');
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'supervisor-harness-switch');
      await closeApp(handle);
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
