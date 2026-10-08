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
  evidenceShot,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import { createWorld, destroyWorld, seedSupervisorConversation } from '../helpers/world';

test('expanded supervisor activity keeps messages separate and the composer clear', async ({}, testInfo) => {
  const world = createWorld('supervisor-activity-layout', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  const id = seedSupervisorConversation(world, 4);
  const file = path.join(world.stateDir, 'supervisor', 'conversations', id, 'transcript.jsonl');
  const base = {
    conversation_id: id,
    generation: 0,
    turn_id: 'g0.t1',
    visibility: 'content',
    created_at: '2026-01-01T00:00:00Z',
  };
  const records = [
    { kind: 'user', data: { text: 'Review the supervisor transcript layout.' } },
    {
      kind: 'assistant',
      data: {
        content: [
          { type: 'text', text: 'Reviewing the conversation and activity history. '.repeat(24) },
        ],
      },
    },
    ...Array.from({ length: 5 }, (_, index) => ({
      kind: 'tool_use',
      data: {
        content: [
          {
            type: 'tool_use',
            id: `layout-tool-${index}`,
            name: 'Bash',
            input: {
              command: `inspect-${index} ${'desktop/src/renderer/src/features/supervisor/ConversationTranscript.tsx '.repeat(5)}`,
            },
          },
        ],
      },
    })),
    {
      kind: 'assistant',
      data: {
        content: [
          {
            type: 'text',
            text: 'The following reply stays below the expanded tool history. '.repeat(6),
          },
        ],
      },
    },
    {
      kind: 'tool_use',
      data: {
        content: [
          {
            type: 'tool_use',
            id: 'layout-final',
            name: 'Bash',
            input: { command: 'verify-layout' },
          },
        ],
      },
    },
  ];
  fs.writeFileSync(
    file,
    records
      .map((record, index) =>
        JSON.stringify({ ...base, seq: index + 1, id: `layout-${index}`, ...record }),
      )
      .join('\n') + '\n',
  );
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-activity-layout' });
    const page = handle.page;
    const feed = page.getByRole('region', { name: 'Supervisor conversation' });
    const history = feed
      .locator('.conversation__activity-history:visible')
      .filter({ hasText: '5 activity steps' });
    await expect(history).toBeVisible({ timeout: 60_000 });
    await expect(feed.locator('.conversation__activity-copy:visible')).toHaveText([
      'Worked',
      'Worked',
    ]);

    for (const width of [1000, 620]) {
      await page.setViewportSize({ width, height: 650 });
      const summary = history.locator('summary');
      await summary.scrollIntoViewIfNeeded();
      const before = await summary.boundingBox();
      await summary.click();
      await expect(history).toHaveAttribute('open', '');
      const after = await summary.boundingBox();
      expect(Math.abs(after!.y - before!.y)).toBeLessThan(2);
      const layout = await history.evaluate((details) => {
        const row = details.closest('.conversation__activity')!;
        const next = row.nextElementSibling!;
        const list = details.querySelector('ol')!;
        return {
          rowBottom: row.getBoundingClientRect().bottom,
          listBottom: list.getBoundingClientRect().bottom,
          nextTop: next.getBoundingClientRect().top,
        };
      });
      expect(layout.listBottom).toBeLessThanOrEqual(layout.rowBottom + 1);
      expect(layout.nextTop - layout.rowBottom).toBeGreaterThanOrEqual(15);
      await feed.evaluate((element) => {
        element.scrollTop = element.scrollHeight;
      });
      const clearance = await page.evaluate(() => {
        const activity = [...document.querySelectorAll('.conversation__activity')]
          .filter((row) => row.getClientRects().length > 0)
          .at(-1)!;
        const composer = document.querySelector('.supervisor-page__dock .composer')!;
        return composer.getBoundingClientRect().top - activity.getBoundingClientRect().bottom;
      });
      expect(clearance).toBeGreaterThanOrEqual(32);
      await evidenceShot(handle, `supervisor-activity-layout-${width}`);
      await summary.click();
      await expect(history).not.toHaveAttribute('open');
    }
    const single = feed
      .locator('.conversation__activity-history:visible')
      .filter({ hasText: '1 activity step' });
    await single.locator('summary').click();
    await expect(single.locator('li')).toContainText('verify-layout');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
