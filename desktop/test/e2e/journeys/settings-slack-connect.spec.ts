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
  REQUIRED_SLACK_SCOPES,
  startScriptedSlackServer,
  validBotAuthTest,
  type ScriptedSlackServer,
} from '../helpers/slack';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack settings save a valid token through the packaged app', async ({}, testInfo) => {
  const world = createWorld('settings-slack-connect', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  fs.appendFileSync(world.configPath, 'server:\n  name: Slack setup server\n');
  createRepo(world, 'alpha', { commit: true });
  const token = 'xoxb-agentico-packaged-9z7q';
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    fakeSlack.enqueue(validBotAuthTest());
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-connect',
      env: { AGENTICO_SLACK_API_BASE: fakeSlack.baseUrl },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Slack');

    await expect(settings.getByText('Slack setup server', { exact: true })).toBeVisible();
    const tokenInput = settings.getByLabel('Slack token');
    await tokenInput.fill(token);
    await expect(settings.getByRole('checkbox', { name: /Enabled/ })).toBeChecked();
    const save = settings.getByRole('button', { name: 'Save changes' });
    await expect(save).toBeEnabled();
    await save.click();

    await expect(
      settings.getByText('Connected to E2E Workspace as Agentico E2E (bot)', { exact: true }),
    ).toBeVisible();
    await expect(settings.locator('.slack-settings__token-display code')).toHaveText(/9z7q$/);
    await expect(settings.getByLabel('Slack token')).toHaveCount(0);
    await expect(settings.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    expect(await settings.locator('body').innerText()).not.toContain(token);

    expect(fakeSlack.requests()).toEqual([
      { method: 'POST', path: '/api/auth.test', bearerPresent: true },
    ]);
    expect(fs.statSync(world.configPath).mode & 0o777).toBe(0o600);
    const config = fs.readFileSync(world.configPath, 'utf8');
    expect(config).toContain(`token: ${token}`);
    expect(config).toContain('team_id: T-E2E');
    expect(config).toContain('team_name: E2E Workspace');
    expect(config).toContain('user_id: U-E2E');
    expect(config).toContain('display_name: Agentico E2E');
    expect(config).toContain('bot_id: B-E2E');
    const grantedBlock = /granted_scopes:\n((?:\s+- [^\n]+\n)+)/.exec(config)?.[1] ?? '';
    const grantedScopes = Array.from(grantedBlock.matchAll(/^\s+- (.+)$/gm), (match) => match[1]);
    expect(grantedScopes).toEqual([...REQUIRED_SLACK_SCOPES].sort());

    await evidenceShot(handle, 'settings-slack-connected', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-connect');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
