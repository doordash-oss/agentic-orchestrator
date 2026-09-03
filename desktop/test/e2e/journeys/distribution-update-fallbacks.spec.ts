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

import { expect, test, type TestInfo } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';
import { updatePackageName, writeSignedUpdateFixture } from '../helpers/update-fixtures';

test('read-only AppImage and DEB updates fall back to signed guidance without self-install controls', async ({}, testInfo) => {
  test.setTimeout(180_000);
  const transcript = new Transcript(
    'distribution-update-fallbacks',
    'Packaged update format fallbacks',
  );
  await runFallbackCase(testInfo, transcript, 'appimage-guidance', 'appimage');
  await runFallbackCase(testInfo, transcript, 'deb-guidance', 'deb');
  transcript.write(testInfo);
});

async function runFallbackCase(
  testInfo: TestInfo,
  transcript: Transcript,
  name: string,
  format: 'appimage' | 'deb',
): Promise<void> {
  const world = createWorld(`update-${name}`, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const packageName = updatePackageName(format);
  const fixture = writeSignedUpdateFixture(world.root, {
    packageName,
    packageText: `${format} package bytes`,
  });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, {
      traceName: `distribution-update-fallbacks-${name}`,
      env: {
        AGENTICO_UPDATE_FIXTURE: fixture,
        AGENTICO_UPDATE_PACKAGE_FORMAT: format,
        ...(format === 'appimage' ? { AGENTICO_UPDATE_INSTALL_MODE: 'guidance' } : {}),
      },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const state = await handle.page.evaluate(() => window.agentico.checkForUpdates());
    expect(state.status).toBe('available');
    expect(state.signatureStatus).toBe('verified');
    expect(state.packageFormat).toBe(format);
    transcript.json(name, state);

    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Updates');
    await expect(settings.getByRole('heading', { name: 'Updates' })).toBeVisible();
    if (format === 'deb') {
      await expect(settings.getByText(/package manager/)).toBeVisible();
      await expect(
        settings.getByRole('button', { name: 'Copy the package-manager command' }),
      ).toBeVisible();
    } else {
      await expect(settings.getByText(/cannot be safely replaced in app/i)).toBeVisible();
    }
    await expect(settings.getByRole('button', { name: 'Restart to Update' })).toHaveCount(0);
    await expect(settings.getByRole('button', { name: 'Install When Idle' })).toHaveCount(0);
    await expect(settings.getByRole('button', { name: 'Stop Work and Install Now' })).toHaveCount(
      0,
    );
    persistAppLogs(handle, `distribution-update-fallbacks-${name}`);
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
}
