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
  contractSettingsEvidenceShot,
  evidenceShot,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  type AppHandle,
} from '../helpers/app';
import { REQUIRED_SLACK_SCOPES, startScriptedSlackServer } from '../helpers/slack';
import type { ScriptedSlackServer } from '../helpers/slack';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack settings report each test-message result', async ({}, testInfo) => {
  const world = createWorld('settings-slack-test-message', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const token = 'xoxb-agentico-test-message-3m8q';
  fs.appendFileSync(
    world.configPath,
    [
      'server:',
      '  name: Slack delivery server',
      'slack:',
      '  enabled: true',
      `  token: ${token}`,
      '  identity:',
      '    team_id: T-E2E',
      '    team_name: E2E Workspace',
      '    user_id: U-E2E',
      '    display_name: Agentico E2E',
      '    bot_id: B-E2E',
      '  granted_scopes:',
      ...REQUIRED_SLACK_SCOPES.map((scope) => `    - ${scope}`),
      '  default_recipients:',
      '    - typed_text: "@grace"',
      '      kind: user',
      '      id: U-COLLEAGUE-E2E',
      '      display_name: Grace Hopper',
      '    - typed_text: "#private-ops"',
      '      kind: channel',
      '      id: G-PRIVATE-OPS',
      '      display_name: "#private-ops"',
      '  last_validated_at: 2026-09-19T12:00:00Z',
      '',
    ].join('\n'),
  );
  createRepo(world, 'alpha', { commit: true });
  const originalConfig = fs.readFileSync(world.configPath, 'utf8');
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    fakeSlack.enqueueMethod('conversations.open', {
      body: { ok: true, channel: { id: 'D-GRACE-E2E' } },
    });
    fakeSlack.enqueueMethod(
      'chat.postMessage',
      { body: { ok: true } },
      { body: { ok: false, error: 'not_in_channel' } },
    );

    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-test-message',
      env: { AGENTICO_SLACK_API_BASE: fakeSlack.baseUrl },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Slack');

    await expect(
      settings.getByText('Connected to E2E Workspace as Agentico E2E (bot)', { exact: true }),
    ).toBeVisible();
    await expect(settings.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue('@grace');
    await expect(settings.getByRole('textbox', { name: 'Recipient 2' })).toHaveValue(
      '#private-ops',
    );
    await expect(settings.getByRole('button', { name: 'Save changes' })).toBeDisabled();

    await settings.getByRole('button', { name: 'Send test message' }).click();
    const results = settings.getByRole('list', { name: 'Test message results' });
    await expect(results.getByText('Sent')).toBeVisible();
    await expect(
      results.getByText('Agentico is not a member of #private-ops.', { exact: true }),
    ).toBeVisible();
    await expect(
      results.getByText('Invite the Agentico app to #private-ops in Slack, then try again.', {
        exact: true,
      }),
    ).toBeVisible();

    const requests = fakeSlack.requests();
    expect(requests).toHaveLength(3);
    expect(requests[0]).toEqual({
      method: 'POST',
      path: '/api/conversations.open',
      bearerPresent: true,
      fields: { users: 'U-COLLEAGUE-E2E' },
    });
    expect(requests[1]).toMatchObject({
      method: 'POST',
      path: '/api/chat.postMessage',
      bearerPresent: true,
      fields: { channel: 'D-GRACE-E2E' },
    });
    expect(requests[1]?.fields.text).toContain('Slack delivery server');
    expect(requests[2]).toMatchObject({
      method: 'POST',
      path: '/api/chat.postMessage',
      bearerPresent: true,
      fields: { channel: 'G-PRIVATE-OPS' },
    });
    expect(requests[2]?.fields.text).toContain('Slack delivery server');
    expect(fs.readFileSync(world.configPath, 'utf8')).toBe(originalConfig);

    await results.scrollIntoViewIfNeeded();
    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-after-send-test-message-results-showing-one-de-900x640',
      900,
      640,
      'light',
    );
    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-after-send-test-message-results-showing-one-de-900x640-b207d051',
      900,
      640,
      'dark',
    );

    await evidenceShot(handle, 'settings-slack-test-message', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-test-message');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
