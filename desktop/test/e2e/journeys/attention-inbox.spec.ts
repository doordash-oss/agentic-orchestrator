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

/**
 * Packaged attention-shell journey. The richer fixture-backed resolution
 * journeys build on this stable desktop contract: the global inbox is always
 * reachable by keyboard, and every dismissal path of the transient popover
 * (shortcut toggle, Escape, an outside click) returns focus to its bell.
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('attention inbox is keyboard reachable and restores focus to the bell', async ({}, testInfo) => {
  const transcript = new Transcript(
    'attention-inbox',
    'Global attention inbox keyboard shell and focus restoration',
  );
  const world = createWorld('attention-inbox', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'attention-inbox' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const bell = handle.page.getByRole('button', { name: /Attention inbox, \d+ pending/ });
    await expect(bell).toBeVisible();
    const toggleShortcut = process.platform === 'darwin' ? 'Meta+Shift+A' : 'Control+Shift+A';

    await setTheme(handle, 'dark');
    await bell.focus();
    await handle.page.keyboard.press(toggleShortcut);

    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).toBeVisible();
    await expect(inbox.getByRole('heading', { name: 'Attention inbox' })).toBeVisible();
    await evidenceShot(handle, 'attention-inbox-wide-dark');

    await setWindowSize(handle, 720, 900);
    await evidenceShot(handle, 'attention-inbox-narrow-dark');

    // The shortcut toggles: a second press dismisses and hands focus back.
    await handle.page.keyboard.press(toggleShortcut);
    await expect(inbox).toHaveCount(0);
    await expect(bell).toBeFocused();

    await setWindowSize(handle, 1280, 900);
    await setTheme(handle, 'light');
    await bell.click();
    await expect(inbox).toBeVisible();
    await evidenceShot(handle, 'attention-inbox-wide-light');
    await setWindowSize(handle, 720, 900);
    await evidenceShot(handle, 'attention-inbox-narrow-light');

    await handle.page.keyboard.press('Escape');
    await expect(inbox).toHaveCount(0);
    await expect(bell).toBeFocused();

    // An outside pointer dismisses the transient surface too.
    await setWindowSize(handle, 1280, 900);
    await bell.click();
    await expect(inbox).toBeVisible();
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect(inbox).toHaveCount(0);

    transcript.step(
      'the shortcut and the bell both opened the transient inbox popover in both themes; the shortcut toggle, Escape, and an outside click each dismissed it, and the keyboard paths restored focus to the attention bell',
    );
    persistAppLogs(handle, 'attention-inbox');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
