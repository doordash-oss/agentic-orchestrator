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
import { expect, test, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  SUPERVISOR_E2E_MARKERS,
  waitFor,
} from '../helpers/world';

const RUN_NAME = 'supervisor-compaction';

async function send(page: Page, text: string): Promise<void> {
  await page.getByRole('textbox', { name: 'Message the supervisor' }).fill(text);
  await page
    .locator('.supervisor-page__dock')
    .getByRole('button', { name: 'Send', exact: true })
    .click();
  await expect(page.getByTestId('supervisor-status')).toHaveAttribute('data-lifecycle', 'idle', {
    timeout: 60_000,
  });
}

test('supervisor compaction: summary, context ring and native resume', async ({}, testInfo) => {
  const transcript = new Transcript(RUN_NAME, 'Packaged Claude compaction and context usage');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'supervisor-compaction-lab', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(page.getByTestId('supervisor-model-chip')).toBeVisible({ timeout: 60_000 });
    await page.getByTestId('supervisor-model-chip').click();
    await page
      .getByRole('region', { name: 'Harness and model' })
      .getByText('Haiku', { exact: true })
      .click();
    await page.keyboard.press('Escape');
    const ring = page.getByTestId('supervisor-context-ring');
    await expect(ring).toHaveAccessibleName('Context usage unknown');

    await send(page, 'Remember the cedar tree.');
    await expect(ring).toHaveAccessibleName(/Context usage \d+%/);
    transcript.step('first reply filled the context ring');

    await send(page, `Fill the context ${SUPERVISOR_E2E_MARKERS.usageHigh}`);
    await expect(ring).toHaveAccessibleName(/Context usage 85%, near the limit/);
    await expect(ring).toHaveAttribute('data-tone', 'warning');
    transcript.step('85 percent context switched the ring to warning');

    await send(page, `Preserve the cedar tree ${SUPERVISOR_E2E_MARKERS.compact}`);
    const conversation = page.getByRole('region', { name: 'Supervisor conversation' });
    await expect(conversation.getByText('Conversation compacted', { exact: true })).toBeVisible();
    const disclosure = conversation.getByRole('button', { name: 'Show summary' });
    await expect(disclosure).toHaveAttribute('aria-expanded', 'false');
    await disclosure.click();
    await expect(conversation.getByText(/Summary:.*Preserve the cedar tree/)).toBeVisible();
    await expect(conversation.getByRole('button', { name: 'Hide summary' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    await expect(ring).toHaveAccessibleName(/Context usage 5%/);
    transcript.step('native compaction showed a collapsed summary and lowered context usage');

    await page.evaluate(() => window.agentico.endSupervisor());
    await expect(ring).toHaveAccessibleName('Context usage unknown');
    await send(page, 'What survived the compaction?');
    await expect(
      conversation.getByText(/Resumed after compaction with \d+ prior messages: Summary:/),
    ).toBeVisible();
    await waitFor(
      () => fs.readFileSync(world.providerInvocationLog, 'utf8').includes('resume-compacted:'),
      'the rebuilt native Claude file to contain a compact boundary',
    );
    transcript.step('the resumed provider read a native boundary and flagged summary');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
