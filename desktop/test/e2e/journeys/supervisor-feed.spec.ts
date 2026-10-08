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
import { createWorld, destroyWorld, seedSupervisorConversation } from '../helpers/world';

test('supervisor feed replays Markdown and every completed file diff in both themes', async ({}, testInfo) => {
  const world = createWorld('supervisor-feed', {
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
    { kind: 'user', data: { text: 'Make the welcome message clearer.' } },
    {
      kind: 'tool_use',
      data: {
        content: [
          { type: 'tool_use', id: 'edit-1', name: 'Write', input: { file_path: 'src/welcome.ts' } },
        ],
      },
    },
    {
      kind: 'tool_result',
      data: {
        content: [{ type: 'tool_result', tool_use_id: 'edit-1', content: 'Updated two files' }],
        file_changes: [
          {
            Path: 'src/welcome.ts',
            Operation: 'update',
            Detail: '-export const greeting = "Hello";\n+export const greeting = "Good morning.";',
            HasDiffPatch: true,
            AddedLines: 1,
            RemovedLines: 1,
          },
          {
            Path: 'src/welcome.test.ts',
            Operation: 'write',
            Detail: '+expect(greeting).toBe("Good morning.");',
            HasDiffPatch: true,
            AddedLines: 1,
          },
        ],
      },
    },
    {
      kind: 'assistant',
      data: {
        content: [
          {
            type: 'text',
            text: '**Done.** The welcome now uses a time-of-day greeting.\n\n- Updated `welcome.ts`\n- Added a regression test\n\nBoth checks pass.',
          },
        ],
      },
    },
  ];
  fs.writeFileSync(
    file,
    records
      .map((record, index) =>
        JSON.stringify({ ...base, seq: index + 1, id: `feed-${index}`, ...record }),
      )
      .join('\n') + '\n',
  );
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-feed' });
    const feed = handle.page.getByRole('region', { name: 'Supervisor conversation' });
    await expect(feed.getByRole('region', { name: 'Diff for src/welcome.ts' })).toContainText(
      'Good morning.',
      { timeout: 60_000 },
    );
    await expect(feed.getByRole('region', { name: 'Diff for src/welcome.test.ts' })).toContainText(
      'expect(greeting)',
    );
    await expect(feed.locator('.conversation__markdown strong')).toHaveText('Done.');
    await expect(feed.locator('.conversation__markdown li')).toHaveCount(2);
    await expect(feed.locator('.conversation__file-change')).toHaveCount(2);
    await evidenceShotBothThemes(handle, 'supervisor-feed');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
