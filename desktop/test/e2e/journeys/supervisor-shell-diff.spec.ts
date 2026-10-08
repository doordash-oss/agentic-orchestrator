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
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShotBothThemes,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import { createRepo, createWorld, destroyWorld, SUPERVISOR_E2E_MARKERS } from '../helpers/world';

test('shell edits appear as live diff nuggets before the command completes', async ({}, testInfo) => {
  const world = createWorld('supervisor-shell-diff', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  const repo = createRepo(world, 'shell-diff', { commit: true });
  fs.writeFileSync(path.join(repo, 'app.ts'), 'const value = 1;\n');
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-shell-diff' });
    const page = handle.page;
    const chip = page.getByTestId('supervisor-model-chip');
    await expect(chip).toBeVisible({ timeout: 60000 });
    await chip.click();
    await page
      .getByRole('region', { name: 'Harness and model' })
      .getByRole('group', { name: 'Claude' })
      .getByText('Haiku', { exact: true })
      .click();
    await page.keyboard.press('Escape');
    await page
      .getByRole('textbox', { name: 'Message the supervisor' })
      .fill(`${SUPERVISOR_E2E_MARKERS.shellDiff}[${repo}]`);
    await page.getByRole('button', { name: 'Send', exact: true }).click();
    const feed = page.getByRole('region', { name: 'Supervisor conversation' });
    await expect(feed.getByText(/Rewrite two source files/)).toBeVisible();
    fs.writeFileSync(`${world.supervisorGatePath}.diff`, '');
    const changed = feed.getByRole('region', { name: /Diff for .*app.ts/ });
    await expect(changed).toContainText('const value = 2;', { timeout: 20000 });
    await expect(changed).toContainText('const value = 1;');
    await expect(feed.getByRole('region', { name: /Diff for .*new.ts/ })).toContainText(
      'export const added = true;',
    );
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute(
      'data-lifecycle',
      'running',
    );
    await expect(feed.locator('.conversation__file-change')).toHaveCount(2);
    await evidenceShotBothThemes(handle, 'supervisor-live-shell-diffs');
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle');
    await expect(feed.locator('.conversation__file-change')).toHaveCount(2);
    await page.reload();
    await expect(feed.locator('.conversation__file-change')).toHaveCount(2);
    await expect(feed.getByRole('region', { name: /Diff for .*app.ts/ })).toContainText(
      'const value = 2;',
    );
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
