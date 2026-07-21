/**
 * Packaged attention-shell journey. The richer fixture-backed resolution
 * journeys build on this stable desktop contract: the global inbox is always
 * reachable by keyboard and returns focus to its invoking bell.
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

    await setTheme(handle, 'dark');
    await bell.focus();
    await handle.page.keyboard.press(
      process.platform === 'darwin' ? 'Meta+Shift+A' : 'Control+Shift+A',
    );

    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).toBeVisible();
    await expect(inbox.getByRole('heading', { name: 'Attention inbox' })).toBeVisible();
    await evidenceShot(handle, 'attention-inbox-wide-dark');

    await setWindowSize(handle, 720, 900);
    await evidenceShot(handle, 'attention-inbox-narrow-dark');

    await inbox.getByRole('button', { name: 'Close inbox' }).click();
    await expect(inbox).toHaveCount(0);
    await expect(bell).toBeFocused();

    await setWindowSize(handle, 1280, 900);
    await setTheme(handle, 'light');
    await bell.focus();
    await handle.page.keyboard.press(
      process.platform === 'darwin' ? 'Meta+Shift+A' : 'Control+Shift+A',
    );
    await expect(inbox).toBeVisible();
    await evidenceShot(handle, 'attention-inbox-wide-light');
    await setWindowSize(handle, 720, 900);
    await evidenceShot(handle, 'attention-inbox-narrow-light');
    await inbox.getByRole('button', { name: 'Close inbox' }).click();
    await expect(inbox).toHaveCount(0);
    await expect(bell).toBeFocused();

    transcript.step(
      'keyboard shortcut opened the global inbox in both themes; Close inbox restored focus to the attention bell',
    );
    persistAppLogs(handle, 'attention-inbox');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
