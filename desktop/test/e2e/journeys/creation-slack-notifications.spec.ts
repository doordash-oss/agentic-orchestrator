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
import { expect, test, type Page } from '@playwright/test';
import { closeApp, evidenceShot, launchApp, setTheme, type AppHandle } from '../helpers/app';
import {
  REQUIRED_SLACK_SCOPES,
  startScriptedSlackServer,
  type ScriptedSlackServer,
} from '../helpers/slack';
import { createRepo, createWorld, destroyWorld, type JourneyWorld } from '../helpers/world';

test.describe.configure({ mode: 'serial' });

const SHOTS = {
  light:
    'creation-sheet-light-theme-slack-configured-contract-step-scrolled-to-the-notifi-1440x900',
  dark: 'creation-sheet-dark-theme-slack-configured-the-same-notifications-group-in-the-s-1440x900',
  unconfigured:
    'creation-sheet-light-theme-slack-not-configured-contract-step-scrolled-to-the-no-1440x900',
};

async function contract(page: Page) {
  await page.getByRole('button', { name: 'New feature' }).click();
  const sheet = page.getByRole('dialog', { name: 'New feature' });
  await sheet.getByRole('checkbox', { name: /alpha/ }).check();
  await sheet.getByRole('button', { name: 'Next: Describe' }).click();
  await sheet.getByLabel('Name').fill('Notifications journey');
  await sheet.getByRole('button', { name: 'Next: Depth' }).click();
  await sheet.getByRole('button', { name: 'Next: Contract' }).click();
  return sheet;
}

async function captureGroup(handle: AppHandle, name: string) {
  await handle.page.getByRole('group', { name: 'Notifications' }).scrollIntoViewIfNeeded();
  await evidenceShot(handle, name);
}

test('creation Notifications resolves additions, retains overrides, and persists them', async ({}, testInfo) => {
  const world = createWorld('creation-slack-notifications', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  fs.appendFileSync(
    world.configPath,
    [
      'slack:',
      '  enabled: true',
      '  token: xoxb-agentico-creation-e2e',
      '  default_recipients:',
      '    - typed_text: "@ada"',
      '      kind: user',
      '      id: U-OWNER-E2E',
      '      display_name: Ada Lovelace',
      '  categories:',
      '    progress: true',
      '    needs_input: false',
      '    problems: true',
      '  granted_scopes:',
      ...REQUIRED_SLACK_SCOPES.map((scope) => `    - ${scope}`),
      '',
    ].join('\n'),
  );
  let handle: AppHandle | undefined;
  let slack: ScriptedSlackServer | undefined;
  try {
    slack = await startScriptedSlackServer();
    slack.enqueueMethod(
      'conversations.list',
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
      { body: { ok: true, channels: [], response_metadata: { next_cursor: '' } } },
    );
    handle = await launchApp(world, testInfo, {
      traceName: 'creation-slack-notifications',
      env: { AGENTICO_SLACK_API_BASE: slack.baseUrl },
    });
    const app = handle;
    await app.page.setViewportSize({ width: 1440, height: 900 });
    const sheet = await contract(app.page);
    const group = sheet.getByRole('group', { name: 'Notifications' });
    await expect(group.getByRole('checkbox', { name: /Mute Slack updates/ })).not.toBeChecked();
    await expect(group.getByRole('combobox', { name: 'Needs input' })).toContainText(
      'Workspace default (off)',
    );
    await expect(group.getByText(/Ada Lovelace/)).toBeVisible();
    const first = group.getByRole('textbox', { name: 'Recipient 1' });
    await first.fill('#eng');
    await first.press('Enter');
    await expect(group.getByText('#eng', { exact: true })).toBeVisible();
    await group.getByRole('button', { name: 'Add recipient' }).click();
    const second = group.getByRole('textbox', { name: 'Recipient 2' });
    await second.fill('#missing');
    await second.blur();
    await expect(second).toHaveAttribute('aria-invalid', 'true');
    const errorId = await second.getAttribute('aria-describedby');
    expect(errorId).toBeTruthy();
    await expect(group.locator(`#${errorId!}`)).not.toBeEmpty();
    await setTheme(app, 'light');
    await captureGroup(app, SHOTS.light);
    await setTheme(app, 'dark');
    await captureGroup(app, SHOTS.dark);
    await sheet.getByRole('button', { name: 'Create and start' }).click();
    await expect(group.getByText(/Resolve or remove every recipient/)).toBeVisible();
    await group.getByRole('button', { name: 'Remove recipient 2' }).click();
    await group.getByRole('combobox', { name: 'Progress' }).selectOption('off');
    await group.getByRole('checkbox', { name: /Mute Slack updates/ }).check();
    await expect(group.getByText(/no effect while muted/)).toBeVisible();
    await expect(group.getByRole('combobox', { name: 'Progress' })).toHaveValue('off');
    await sheet.getByRole('checkbox', { name: 'Start immediately' }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(sheet).not.toBeVisible();
    const featureFiles = fs
      .readdirSync(world.stateDir, { withFileTypes: true })
      .filter(
        (entry) =>
          entry.isDirectory() &&
          fs.existsSync(path.join(world.stateDir, entry.name, 'feature.yaml')),
      );
    expect(featureFiles).toHaveLength(1);
    const stored = fs.readFileSync(
      path.join(world.stateDir, featureFiles[0]!.name, 'feature.yaml'),
      'utf8',
    );
    expect(stored).toContain('slack_notifications:');
    expect(stored).toContain('mode: muted');
    expect(stored).toMatch(/progress:\s*["']?off["']?/);
    expect(stored).toMatch(/typed_text:\s*["']?#eng["']?/);
    expect(stored).toContain('id: C-ENGINEERING');
    expect(stored).not.toContain('#missing');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    if (slack !== undefined) await slack.close();
    destroyWorld(world);
  }
});

test('creation Notifications stays empty without configured Slack', async ({}, testInfo) => {
  const world: JourneyWorld = createWorld('creation-slack-unconfigured', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'creation-slack-unconfigured' });
    const app = handle;
    await app.page.setViewportSize({ width: 1440, height: 900 });
    const sheet = await contract(app.page);
    const group = sheet.getByRole('group', { name: 'Notifications' });
    await expect(group.getByText('Set up Slack in Settings')).toBeVisible();
    await expect(group.getByRole('textbox')).toHaveCount(0);
    await setTheme(app, 'light');
    await captureGroup(app, SHOTS.unconfigured);
    await sheet.getByRole('checkbox', { name: 'Start immediately' }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();
    await expect(sheet).not.toBeVisible();
    const featureFiles = fs
      .readdirSync(world.stateDir, { withFileTypes: true })
      .filter(
        (entry) =>
          entry.isDirectory() &&
          fs.existsSync(path.join(world.stateDir, entry.name, 'feature.yaml')),
      );
    expect(featureFiles).toHaveLength(1);
    expect(
      fs.readFileSync(path.join(world.stateDir, featureFiles[0]!.name, 'feature.yaml'), 'utf8'),
    ).not.toContain('slack_notifications:');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    destroyWorld(world);
  }
});
