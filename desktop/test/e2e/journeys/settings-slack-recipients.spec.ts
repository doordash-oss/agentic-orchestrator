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
import {
  startScriptedSlackServer,
  validUserAuthTest,
  type ScriptedSlackServer,
} from '../helpers/slack';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack settings resolve and save default recipients', async ({}, testInfo) => {
  const world = createWorld('settings-slack-recipients', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  fs.appendFileSync(world.configPath, 'server:\n  name: Slack recipients server\n');
  createRepo(world, 'alpha', { commit: true });
  const token = 'xoxp-agentico-recipients-7r4p';
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    fakeSlack.enqueue(validUserAuthTest());
    fakeSlack.enqueueMethod('users.lookupByEmail', {
      body: {
        ok: true,
        user: {
          id: 'U-COLLEAGUE-E2E',
          name: 'grace',
          real_name: 'Grace Hopper',
          profile: { display_name: 'Grace' },
          deleted: false,
          is_bot: false,
        },
      },
    });
    fakeSlack.enqueueMethod(
      'conversations.list',
      {
        body: {
          ok: true,
          channels: [
            {
              id: 'G-PRIVATE-OPS',
              name: 'private-ops',
              is_private: true,
              is_member: false,
              is_archived: false,
            },
          ],
          response_metadata: { next_cursor: '' },
        },
      },
      {
        body: {
          ok: true,
          channels: [
            {
              id: 'C-ENGINEERING',
              name: 'eng',
              is_private: false,
              is_member: true,
              is_archived: false,
            },
          ],
          response_metadata: { next_cursor: '' },
        },
      },
      {
        body: {
          ok: true,
          channels: [
            {
              id: 'G-PRIVATE-OPS',
              name: 'private-ops',
              is_private: true,
              is_member: false,
              is_archived: false,
            },
          ],
          response_metadata: { next_cursor: '' },
        },
      },
    );

    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-recipients',
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
      settings.getByText('Connected to E2E Workspace as ada (user)', { exact: true }),
    ).toBeVisible();
    await expect(settings.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue('@ada');
    await expect(settings.getByText('you', { exact: true })).toBeVisible();
    let config = fs.readFileSync(world.configPath, 'utf8');
    expect(config).toContain('default_recipients:');
    expect(config).toMatch(/typed_text: ['"]@ada['"]/);
    expect(config).toContain('id: U-OWNER-E2E');

    await settings.getByRole('button', { name: 'Add recipient' }).click();
    const colleague = settings.getByRole('textbox', { name: 'Recipient 2' });
    await colleague.fill('grace@example.com');
    await colleague.blur();
    await expect(settings.getByText('Grace Hopper')).toBeVisible();

    await settings.getByRole('button', { name: 'Add recipient' }).click();
    const channel = settings.getByRole('textbox', { name: 'Recipient 3' });
    await channel.fill('#private-ops');
    await channel.blur();
    const channelError = settings.locator('#slack-recipient-3-error');
    await expect(channelError).toContainText('Agentico is not a member of #private-ops.');
    await expect(channelError).toContainText(
      'Invite the Agentico app to #private-ops in Slack, then try again.',
    );
    await expect(save).toBeDisabled();
    await expect(
      settings.getByText('Resolve or remove every recipient before saving.', { exact: true }),
    ).toBeVisible();

    await channel.fill('#eng');
    await channel.blur();
    await expect(settings.getByText('#eng').last()).toBeVisible();
    await expect(save).toBeEnabled();
    await save.click();
    await expect(save).toBeDisabled();

    config = fs.readFileSync(world.configPath, 'utf8');
    const ownerIndex = config.indexOf('id: U-OWNER-E2E');
    const colleagueIndex = config.indexOf('id: U-COLLEAGUE-E2E');
    const channelIndex = config.indexOf('id: C-ENGINEERING');
    expect(ownerIndex).toBeGreaterThan(-1);
    expect(colleagueIndex).toBeGreaterThan(ownerIndex);
    expect(channelIndex).toBeGreaterThan(colleagueIndex);

    expect(fakeSlack.requests()).toEqual([
      { method: 'POST', path: '/api/auth.test', bearerPresent: true, fields: {} },
      {
        method: 'POST',
        path: '/api/users.lookupByEmail',
        bearerPresent: true,
        fields: { email: 'grace@example.com' },
      },
      {
        method: 'POST',
        path: '/api/conversations.list',
        bearerPresent: true,
        fields: {
          types: 'public_channel,private_channel',
          exclude_archived: 'false',
          limit: '200',
        },
      },
      {
        method: 'POST',
        path: '/api/conversations.list',
        bearerPresent: true,
        fields: {
          types: 'public_channel,private_channel',
          exclude_archived: 'false',
          limit: '200',
        },
      },
    ]);

    if (process.env['AGENTICO_EVIDENCE_DIR']) {
      await settings.getByRole('button', { name: 'Add recipient' }).click();
      const evidenceRow = settings.getByRole('textbox', { name: 'Recipient 4' });
      await evidenceRow.fill('#private-ops');
      await evidenceRow.blur();
      const evidenceErrorId = await evidenceRow.getAttribute('aria-describedby');
      expect(evidenceErrorId).not.toBeNull();
      await expect(settings.locator(`#${evidenceErrorId!}`)).toContainText(
        'Invite the Agentico app to #private-ops in Slack, then try again.',
      );
      await evidenceRow.scrollIntoViewIfNeeded();
      await contractSettingsEvidenceShot(
        handle,
        settings,
        'settings-window-on-the-slack-pane-connected-as-a-user-notify-by-default-showing-900x640',
        900,
        640,
        'light',
      );
      await contractSettingsEvidenceShot(
        handle,
        settings,
        'settings-window-on-the-slack-pane-connected-as-a-user-notify-by-default-showing-900x640-5ae269d2',
        900,
        640,
        'dark',
      );
    }

    await evidenceShot(handle, 'settings-slack-recipients', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-recipients');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
