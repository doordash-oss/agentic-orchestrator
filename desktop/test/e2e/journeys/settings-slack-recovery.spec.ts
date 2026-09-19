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
import {
  startScriptedSlackServer,
  validBotAuthTest,
  type ScriptedSlackServer,
} from '../helpers/slack';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack settings recover a saved token after Slack becomes reachable', async ({}, testInfo) => {
  const world = createWorld('settings-slack-recovery', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  fs.appendFileSync(world.configPath, 'server:\n  name: Slack recovery server\n');
  createRepo(world, 'alpha', { commit: true });
  const token = 'xoxb-agentico-recovery-4r2k';
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    fakeSlack.enqueue(
      { status: 503, body: { ok: false, error: 'temporarily_unavailable' } },
      validBotAuthTest(),
    );
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-recovery',
      env: { AGENTICO_SLACK_API_BASE: fakeSlack.baseUrl },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Slack');

    await settings.getByLabel('Slack token').fill(token);
    const save = settings.getByRole('button', { name: 'Save changes' });
    await save.click();
    await expect(
      settings.getByText('Token saved, but Slack could not be reached', { exact: true }),
    ).toBeVisible();
    await expect(settings.getByText('Slack could not be reached', { exact: true })).toBeVisible();
    await expect(settings.getByText(/^Last checked /)).toBeVisible();
    await expect(settings.locator('.slack-settings__token-display code')).toHaveText(/4r2k$/);
    await expect(save).toBeDisabled();

    const warningConfig = fs.readFileSync(world.configPath, 'utf8');
    expect(warningConfig).toContain(`token: ${token}`);
    expect(warningConfig).not.toContain('identity:');
    await evidenceShot(handle, 'settings-slack-warning', settings);

    await settings.getByRole('button', { name: 'Check connection' }).click();
    await expect(
      settings.getByText('Connected to E2E Workspace as Agentico E2E (bot); 13 scopes granted.', {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      settings.getByText('Connected to E2E Workspace as Agentico E2E (bot)', { exact: true }),
    ).toBeVisible();
    await expect(save).toBeDisabled();
    await expect(settings.getByLabel('Slack token')).toHaveCount(0);
    expect(await settings.locator('body').innerText()).not.toContain(token);

    expect(fakeSlack.requests()).toEqual([
      { method: 'POST', path: '/api/auth.test', bearerPresent: true },
      { method: 'POST', path: '/api/auth.test', bearerPresent: true },
    ]);
    const recoveredConfig = fs.readFileSync(world.configPath, 'utf8');
    expect(recoveredConfig).toContain(`token: ${token}`);
    expect(recoveredConfig).toContain('team_id: T-E2E');
    expect(recoveredConfig).toContain('team_name: E2E Workspace');
    expect(recoveredConfig).toContain('user_id: U-E2E');
    expect(recoveredConfig).toContain('display_name: Agentico E2E');
    expect(recoveredConfig).toContain('bot_id: B-E2E');

    await evidenceShot(handle, 'settings-slack-recovered', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-recovery');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
