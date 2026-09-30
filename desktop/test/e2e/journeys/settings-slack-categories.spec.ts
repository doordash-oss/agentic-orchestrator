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

test('Slack settings save category defaults without any Slack call', async ({}, testInfo) => {
  const world = createWorld('settings-slack-categories', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const token = 'xoxb-agentico-categories-4r9w';
  fs.appendFileSync(
    world.configPath,
    [
      'server:',
      '  name: Slack categories server',
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
      '  last_validated_at: 2026-09-19T12:00:00Z',
      '',
    ].join('\n'),
  );
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-categories',
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
    const progress = settings.getByRole('checkbox', { name: /Progress/ });
    const needsInput = settings.getByRole('checkbox', { name: /Needs input/ });
    const problems = settings.getByRole('checkbox', { name: /Problems/ });
    await expect(progress).toBeChecked();
    await expect(needsInput).toBeChecked();
    await expect(problems).toBeChecked();
    await expect(
      settings.getByText(
        'Phase starts and completions, publishing, and completion, as thread replies.',
        { exact: true },
      ),
    ).toBeVisible();
    await expect(
      settings.getByText('Questions, permission requests, and review gates.', { exact: true }),
    ).toBeVisible();
    await expect(
      settings.getByText('Blocking failures and conditions that need action.', { exact: true }),
    ).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Save changes' })).toBeDisabled();

    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-connected-as-a-bot-notify-about-group-showing-900x640',
      900,
      640,
      'light',
    );

    await progress.uncheck();
    await expect(progress).not.toBeChecked();
    const saveBar = settings.locator('footer.config-editor__footer');
    const dirtyStatus = settings.getByText('Unsaved changes');
    const save = settings.getByRole('button', { name: 'Save changes' });
    await saveBar.scrollIntoViewIfNeeded();
    await expect(progress).toBeInViewport({ ratio: 1 });
    await expect(dirtyStatus).toBeInViewport({ ratio: 1 });
    await expect(save).toBeEnabled();
    await expect(save).toBeInViewport({ ratio: 1 });
    await expect(saveBar).toBeInViewport({ ratio: 1 });

    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-connected-as-a-bot-notify-about-group-with-pro-900x640',
      900,
      640,
      'dark',
    );

    await save.click();
    await expect(settings.getByText('Saved.')).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    await expect(progress).not.toBeChecked();
    await expect(needsInput).toBeChecked();
    await expect(problems).toBeChecked();

    const config = fs.readFileSync(world.configPath, 'utf8');
    expect(config).toContain('categories:');
    expect(config).toContain('progress: false');
    expect(config).toContain('needs_input: true');
    expect(config).toContain('problems: true');
    expect(fakeSlack.requests()).toEqual([]);

    await evidenceShot(handle, 'settings-slack-categories', settings);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-categories');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
