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
import { assertNoLeakedProcesses, closeApp, launchApp, type AppHandle } from '../helpers/app';
import { createWorld, destroyWorld, seedSupervisorConversation } from '../helpers/world';

test('supervisor shows a long message in full and copies replies', async ({}, testInfo) => {
  const world = createWorld('supervisor-message-copy', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  const id = seedSupervisorConversation(world, 2);
  const file = path.join(world.stateDir, 'supervisor', 'conversations', id, 'transcript.jsonl');
  const base = {
    conversation_id: id,
    generation: 0,
    turn_id: 'g0.t1',
    visibility: 'content',
    created_at: '2026-01-01T00:00:00Z',
  };
  const longText = Array.from({ length: 60 }, (_, i) => `Line ${i + 1} of the request.`).join('\n');
  const records = [
    { kind: 'user', data: { text: longText } },
    { kind: 'assistant', data: { content: [{ type: 'text', text: 'The **reply** to copy.' }] } },
  ];
  fs.writeFileSync(
    file,
    records
      .map((record, index) =>
        JSON.stringify({ ...base, seq: index + 1, id: `copy-${index}`, ...record }),
      )
      .join('\n') + '\n',
  );
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'supervisor-message-copy' });
    const app = handle.app;
    const feed = handle.page.getByRole('region', { name: 'Supervisor conversation' });
    const reply = feed.getByRole('article', { name: 'Supervisor' });
    await expect(reply).toContainText('The reply to copy.', { timeout: 60_000 });
    await expect(feed.getByRole('article', { name: 'You' })).toHaveText(longText);

    const readClipboard = () => app.evaluate(({ clipboard }) => clipboard.readText());
    await app.evaluate(({ clipboard }) => clipboard.writeText('sentinel'));
    await reply.hover();
    await reply.getByRole('button', { name: 'Copy message' }).click();
    await expect.poll(readClipboard).toBe('The **reply** to copy.');
    await expect(reply.getByRole('button', { name: 'Copied' })).toBeVisible();

    // Right-clicking a selection offers Copy; the native popup is recorded,
    // not shown, so the journey never blocks on an OS menu.
    await app.evaluate(({ Menu }) => {
      const state = globalThis as typeof globalThis & { contextMenus: string[][] };
      state.contextMenus = [];
      Menu.prototype.popup = function (this: Electron.Menu) {
        state.contextMenus.push(this.items.map((item) => item.role ?? item.type));
      };
    });
    const paragraph = reply.locator('.conversation__markdown p');
    await paragraph.click({ clickCount: 3 });
    await paragraph.click({ button: 'right' });
    await expect
      .poll(() =>
        app.evaluate(
          () => (globalThis as typeof globalThis & { contextMenus: string[][] }).contextMenus,
        ),
      )
      .toEqual([['copy']]);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
