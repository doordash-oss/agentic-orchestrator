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

import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import {
  chooseSupervisorModel,
  sendSupervisorMessage,
  supervisorComposer,
  supervisorRow,
} from '../helpers/supervisor';
import { createWorld, destroyWorld, SUPERVISOR_E2E_MARKERS } from '../helpers/world';

test('background work stays visible between turns, across renderer reload and after a restart', async ({}, testInfo) => {
  const world = createWorld('supervisor-background-work', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-background-work' });
    let page = handle.page;
    await expect(supervisorComposer(page)).toBeVisible({ timeout: 60_000 });
    await chooseSupervisorModel(page);
    await sendSupervisorMessage(page, SUPERVISOR_E2E_MARKERS.backgroundWork);
    let panel = page.getByRole('region', { name: 'Background work', exact: true });
    await expect(panel).toContainText('1 monitor watching · 1 task running');
    await expect(page.getByTestId('supervisor-status')).toHaveText(
      'Ready · 1 monitor watching · 1 task running',
    );
    await expect(supervisorRow(page)).toContainText('1 monitor watching');
    await sendSupervisorMessage(page, 'Continue the conversation');
    await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle');
    await expect(panel).toContainText('Watch translation questions');
    await page.reload();
    panel = page.getByRole('region', { name: 'Background work', exact: true });
    await expect(panel).toContainText('1 monitor watching · 1 task running');
    await page.setViewportSize({ width: 760, height: 680 });
    await panel.locator('summary').filter({ hasText: 'Watch translation questions' }).click();
    await expect(panel).toContainText('Schedule: every minute');
    await expect(panel).toContainText('Last confirmed');
    await expect(supervisorComposer(page)).toBeVisible();
    const boxes = await Promise.all([panel.boundingBox(), supervisorComposer(page).boundingBox()]);
    expect(boxes[0]!.y + boxes[0]!.height).toBeLessThan(boxes[1]!.y);
    await evidenceShot(handle, 'background-work-watching');
    await panel.getByRole('button', { name: 'Ask to stop' }).click();
    await expect(panel).not.toContainText('1 monitor watching');
    await expect(panel).toContainText('1 task running');
    await panel.getByText('Recent activity (1)', { exact: true }).click();
    await expect(panel).toContainText('Stopped');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, {
      traceName: 'supervisor-background-work-restarted',
    });
    page = handle.page;
    panel = page.getByRole('region', { name: 'Background work', exact: true });
    await expect(panel).toContainText('Interrupted', { timeout: 60_000 });
    await expect(panel).toContainText('1 task needs attention');
    await expect(panel).not.toContainText('1 task running');
    await panel.locator('summary').filter({ hasText: 'Run test suite' }).click();
    await expect(panel).toContainText('Session ended; execution is no longer confirmed');
    await evidenceShot(handle, 'background-work-interrupted');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
