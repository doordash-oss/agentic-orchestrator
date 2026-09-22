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
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  contractEvidenceShot,
  contractSettingsEvidenceShot,
  createFeatureViaForm,
  launchApp,
  openSettings,
  persistAppLogs,
  selectSettingsPane,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import {
  REQUIRED_SLACK_SCOPES,
  startScriptedSlackServer,
  type ScriptedSlackServer,
} from '../helpers/slack';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('Slack credential failure warns in the toolbar and routes back to settings', async ({}, testInfo) => {
  const world = createWorld('settings-slack-credential-warning', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const token = 'xoxb-agentico-credential-warning-8n4q';
  fs.appendFileSync(
    world.configPath,
    [
      'server:',
      '  name: Slack credential server',
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
      '  last_validated_at: 2026-09-22T09:00:00Z',
      '',
    ].join('\n'),
  );
  createRepo(world, 'alpha', { commit: true });
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    fakeSlack = await startScriptedSlackServer();
    fakeSlack.enqueue({ body: { ok: false, error: 'invalid_auth' } });
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-credential-warning',
      env: { AGENTICO_SLACK_API_BASE: fakeSlack.baseUrl },
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(
      handle.page.getByRole('button', { name: 'Show Slack credential warning' }),
    ).toHaveCount(0);

    const settings = await openSettings(handle);
    await selectSettingsPane(settings, 'Slack');
    await expect(
      settings.getByText('Connected to E2E Workspace as Agentico E2E (bot)', { exact: true }),
    ).toBeVisible();

    await settings.getByRole('button', { name: 'Check connection' }).click();
    await expect(
      settings.getByText('Slack rejected the saved token', { exact: true }).first(),
    ).toBeVisible();
    await expect(settings.getByText('slack_invalid_token', { exact: true }).first()).toBeVisible();
    await expect(
      handle.page.getByRole('button', { name: 'Show Slack credential warning' }),
    ).toBeVisible();

    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-credential-error-state-top-of-the-pane-the-cre-900x640',
      900,
      640,
      'light',
    );
    const checkConnection = settings.getByRole('button', { name: 'Check connection' });
    await checkConnection.scrollIntoViewIfNeeded();
    await expect(checkConnection).toBeVisible();
    await expect(checkConnection).toBeEnabled();
    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-credential-error-state-scrolled-to-the-recover-900x640',
      900,
      640,
      'light',
    );
    await settings
      .getByText('Slack rejected the saved token', { exact: true })
      .first()
      .scrollIntoViewIfNeeded();
    await contractSettingsEvidenceShot(
      handle,
      settings,
      'settings-window-on-the-slack-pane-in-the-credential-error-state-dark-theme-900x640',
      900,
      640,
      'dark',
    );
    await contractEvidenceShot(
      handle,
      'main-window-on-overview-with-the-slack-warning-trigger-and-its-dot-visible-besid-1440x900',
      1440,
      900,
      'light',
    );

    const trigger = handle.page.getByRole('button', { name: 'Show Slack credential warning' });
    await trigger.click();
    const popover = handle.page.getByRole('region', { name: 'Slack needs attention' });
    await expect(popover.getByRole('heading', { name: 'Slack token is invalid' })).toBeVisible();
    await expect(popover).toContainText('Slack rejected the token.');
    await contractEvidenceShot(
      handle,
      'main-window-with-the-slack-warning-popover-open-showing-the-canonical-title-the-1440x900',
      1440,
      900,
      'light',
    );
    await contractEvidenceShot(
      handle,
      'main-window-with-the-slack-warning-popover-open-dark-theme-1440x900',
      1440,
      900,
      'dark',
    );

    for (const theme of ['light', 'dark'] as const) {
      await setWindowSize(handle, 400, 600);
      await setTheme(handle, theme);
      for (const locator of [
        popover,
        popover.getByRole('heading', { name: 'Slack token is invalid' }),
        popover.getByRole('button', { name: 'Open Slack settings' }),
        popover.getByRole('button', { name: 'Dismiss' }),
      ]) {
        const box = await locator.boundingBox();
        expect(box, `${theme} Slack warning element should have bounds`).not.toBeNull();
        expect(
          box!.x,
          `${theme} Slack warning element should start inside viewport`,
        ).toBeGreaterThanOrEqual(0);
        expect(
          box!.x + box!.width,
          `${theme} Slack warning element should end inside viewport`,
        ).toBeLessThanOrEqual(400);
      }
    }
    await handle.app.evaluate(({ BrowserWindow }, mainWebContentsId) => {
      const main = BrowserWindow.getAllWindows().find(
        (window) => window.webContents.id === mainWebContentsId,
      );
      main?.focus();
    }, handle.mainWebContentsId);
    await handle.page.bringToFront();
    await popover.getByRole('button', { name: 'Open Slack settings' }).focus();
    await handle.page.mouse.click(16, 300);
    await expect(popover).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await setWindowSize(handle, 1440, 900);
    await setTheme(handle, 'light');

    await selectSettingsPane(settings, 'Appearance');
    await trigger.click();
    await popover.getByRole('button', { name: 'Open Slack settings' }).click();
    await expect(settings.getByRole('option', { name: 'Slack', exact: true })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await expect(settings.getByRole('region', { name: 'Slack' })).toBeVisible();

    await trigger.click();
    await handle.page
      .getByRole('region', { name: 'Slack needs attention' })
      .getByRole('button', { name: 'Dismiss' })
      .click();
    await expect(trigger).toHaveCount(0);

    expect(fakeSlack.requests()).toEqual([
      { method: 'POST', path: '/api/auth.test', bearerPresent: true, fields: {} },
    ]);
    expect(await handle.page.locator('body').innerText()).not.toContain(token);
    expect(await settings.locator('body').innerText()).not.toContain(token);
    expect(fs.readFileSync(world.configPath, 'utf8')).toContain(`token: ${token}`);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-credential-warning');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});

test('feature cockpit renders seeded Slack destination warnings without a Slack call', async ({}, testInfo) => {
  const world = createWorld('settings-slack-destination-warnings', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  const featureName = 'Slack Delivery Warnings';
  const token = 'xoxb-agentico-destination-warnings-5k2p';
  let handle: AppHandle | null = null;
  let fakeSlack: ScriptedSlackServer | null = null;

  try {
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-destination-warnings-create',
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'Render durable Slack destination failures in the feature cockpit.',
      repoPatterns: [/alpha/],
      waitForReady: true,
    });
    const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
    const featureId = features[0]!.id;

    await closeApp(handle);
    handle = null;

    fs.appendFileSync(
      world.configPath,
      [
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
        '    - typed_text: "#team-x"',
        '      kind: channel',
        '      id: C-TEAM',
        '      display_name: "#team-x"',
        '    - typed_text: "@ada"',
        '      kind: user',
        '      id: U-ADA',
        '      display_name: Ada Lovelace',
        '  last_validated_at: 2026-09-22T09:00:00Z',
        '',
      ].join('\n'),
    );
    fs.writeFileSync(
      path.join(world.stateDir, featureId, 'slack.yaml'),
      [
        'version: 1',
        'destinations:',
        '  channel:C-TEAM:',
        '    kind: channel',
        '    slack_id: C-TEAM',
        '    display_name: "#team-x"',
        '    channel_id: C-TEAM',
        '    failure:',
        '      code: slack_recipient_not_notified',
        '      slack_error: is_archived',
        '      count: 5',
        '      first_failed_at: 2026-09-22T10:02:00Z',
        '      last_failed_at: 2026-09-22T10:10:00Z',
        '  user:U-ADA:',
        '    kind: user',
        '    slack_id: U-ADA',
        '    display_name: Ada Lovelace',
        '    channel_id: D-ADA',
        '    failure:',
        '      code: slack_delivery_retries_exhausted',
        '      slack_error: retries_exhausted',
        '      count: 2',
        '      first_failed_at: 2026-09-22T10:04:00Z',
        '      last_failed_at: 2026-09-22T10:08:00Z',
        '',
      ].join('\n'),
    );

    fakeSlack = await startScriptedSlackServer();
    handle = await launchApp(world, testInfo, {
      traceName: 'settings-slack-destination-warnings-seeded',
      env: { AGENTICO_SLACK_API_BASE: fakeSlack.baseUrl },
    });

    const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });
    const archivedWarning = cockpit.locator('.error-surface', {
      hasText: 'slack_recipient_not_notified',
    });
    const retriesWarning = cockpit.locator('.error-surface', {
      hasText: 'slack_delivery_retries_exhausted',
    });
    await expect(archivedWarning).toContainText('Slack notifications were not delivered');
    await expect(archivedWarning).toContainText(
      '5 Slack messages to #team-x were not delivered since 2026-09-22T10:02:00Z because Slack returned is_archived.',
    );
    await expect(retriesWarning).toContainText('Slack delivery retries were exhausted');
    await expect(retriesWarning).toContainText(
      '2 Slack messages to Ada Lovelace were not delivered since 2026-09-22T10:04:00Z because Slack returned retries_exhausted.',
    );
    await expect(cockpit.locator('.error-surface--warning')).toHaveCount(2);
    await expect(
      handle.page.getByRole('button', { name: /Attention inbox, 0 pending/ }),
    ).toBeVisible();

    await contractEvidenceShot(
      handle,
      'feature-cockpit-showing-two-compact-slack-warnings-one-naming-an-archived-channe-1440x900',
      1440,
      900,
      'light',
    );
    expect(fakeSlack.requests()).toEqual([]);
  } finally {
    if (handle !== null) {
      persistAppLogs(handle, 'settings-slack-destination-warnings');
      await closeApp(handle);
    }
    if (fakeSlack !== null) await fakeSlack.close();
    destroyWorld(world);
    assertNoLeakedProcesses(world);
  }
});
