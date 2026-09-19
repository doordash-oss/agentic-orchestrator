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
  evidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack settings show the not-set-up connection guide', async ({}, testInfo) => {
  const world = createWorld('settings-slack', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  fs.appendFileSync(world.configPath, 'server:\n  name: Slack empty server\n');
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | null = null;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'settings-slack' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Slack');

    await expect(settings.getByRole('heading', { name: 'Slack' })).toBeVisible();
    await expect(settings.getByText('Slack empty server', { exact: true })).toBeVisible();
    await expect(settings.getByText('Not set up', { exact: true })).toBeVisible();
    const guide = settings.getByRole('button', { name: 'Set up the Slack app' });
    await expect(guide).toHaveAttribute('aria-expanded', 'true');
    await expect(settings.getByText('Create a Slack app from the manifest')).toBeVisible();
    await expect(settings.getByText('Install the app to the workspace')).toBeVisible();
    await expect(settings.getByText('Paste the token below and save')).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Copy manifest' })).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Create a Slack app' })).toBeVisible();
    await expect(settings.getByLabel('Slack token')).toHaveAttribute('type', 'password');
    await expect(settings.getByRole('checkbox', { name: /Enabled/ })).not.toBeChecked();
    await expect(settings.getByRole('button', { name: 'Check connection' })).toBeDisabled();
    await expect(settings.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    await evidenceShot(handle, 'settings-slack-not-set-up', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack');
      await closeApp(handle);
    }
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
