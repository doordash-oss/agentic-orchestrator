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

import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { createRepo, createWorld, destroyWorld, waitFor } from '../helpers/world';

test('packaged attention notifications are private, deduplicated, bounded, passive, and do not steal focus', async ({}, testInfo) => {
  const world = createWorld('background-attention-notifications', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    attentionProvider: true,
  });
  createRepo(world, 'notify-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-notifications' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await installNotificationCapture(handle);
    // The zero-notification assertion below expects no notifications, which
    // holds only when the main window is focused (shouldNotify returns false).
    // OS focus is an ambient resource the test does not otherwise control, so
    // deterministically focus the window and confirm via the published test
    // hook before any attention item can arrive.
    await ensureMainWindowFocus(handle);

    await createFeatureViaForm(handle, {
      name: 'Background Notification Questions',
      description: 'Fixture-backed attention for notification and question routing.',
      repoPatterns: [/notify-lab/],
      waitForReady: true,
    });
    await ensureMainWindowFocus(handle);
    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();
    await waitForAttentionItem(handle, 'perm-allow-once');
    expect(await capturedNotifications(handle)).toHaveLength(0);

    // Answer while still focused, so the answered item can never be the one
    // that notifies: a snapshot push landing between hide and answer would
    // otherwise notify for perm-allow-once as well as perm-stale, and the two
    // no-preview bodies are indistinguishable. Hide only once perm-stale is
    // the sole pending item, then let the background refresh notify for it.
    await answerPermission(handle, 'perm-allow-once', 'allow_once');
    await waitForAttentionItem(handle, 'perm-stale');
    await hideMainWindow(handle);
    await waitForNotificationCount(handle, 1);
    let notifications = await capturedNotifications(handle);
    expect(notifications[0]?.body).toBe('Agentico needs attention.');
    expect(uniqueNotificationIdentities(notifications)).toHaveLength(notifications.length);
    await handle.app.evaluate(() => new Promise((resolve) => setTimeout(resolve, 500)));
    expect(await capturedNotifications(handle)).toHaveLength(1);

    await activateNotification(handle, 0);
    // The notification click only shows the window. On Linux CI the focus
    // event does not reliably follow show(), and an unfocused-but-visible
    // window still satisfies shouldNotify — every later pending item would
    // notify before the preview-enabled setting below is enabled. Pin the
    // focus state through the published test hook instead of assuming it.
    await ensureMainWindowFocus(handle);
    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).not.toBeVisible();
    // Notification click only focuses the window (main/notifications.ts just
    // calls show(), no routing) — it stays on the feature's own cockpit. The
    // sidebar's waiting-lane sub-line is this shell's per-row
    // equivalent of the old tab-strip's "Blocking input for X: N pending"
    // badge, worded per WorkspaceShell.tsx's laneSubline (permission-kind
    // attention -> "Approve N request(s)").
    await expect(
      handle.page.getByRole('option', { name: /Background Notification Questions/ }),
    ).toContainText('Approve 1 request');

    await answerPermission(handle, 'perm-stale', 'deny');
    await waitForAttentionItem(handle, 'perm-deny');
    await answerPermission(handle, 'perm-deny', 'deny');
    await waitForAttentionItem(handle, 'perm-remember');
    await openPalette(handle);
    const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByLabel('Search features and commands').fill('settings');
    await expect(palette.getByLabel('Search features and commands')).toBeFocused();
    await answerPermission(handle, 'perm-remember', 'allow_remember');
    await waitForAttentionItem(handle, 'perm-remember-followup');
    await answerPermission(handle, 'perm-remember-followup', 'allow_once');
    await waitForAttentionItem(handle, 'ask-bundle');
    await expect(palette.getByLabel('Search features and commands')).toBeFocused();
    await handle.page.keyboard.press('Escape');
    await expect(palette).not.toBeVisible();
    // ask-bundle is pending from here on: keep the window deterministically
    // focused so it cannot notify before the preview setting lands.
    await ensureMainWindowFocus(handle);

    await handle.page.evaluate(() =>
      window.agentico.updateSettings({ notifications: { previewEnabled: true } }),
    );
    await hideMainWindow(handle);
    await waitForNotificationCount(handle, 2);
    notifications = await capturedNotifications(handle);
    // Exactly the two intended notifications: the hidden-window permission
    // notice and the previewed question. The test focus override prevents
    // other full-suite workers on the shared Linux display from injecting an
    // ambient no-preview notice into this count.
    expect(notifications).toHaveLength(2);
    const askNotification = notifications[1];
    expect(askNotification?.body).toContain('Questions');
    expect(askNotification?.body).toContain('Background Notification Questions');
    expect(askNotification?.body.length ?? 0).toBeLessThanOrEqual(180);

    await activateNotification(handle, 1);
    await expect(inbox).not.toBeVisible();
    await handle.page.getByRole('button', { name: /Attention inbox, 1 pending/ }).click();
    await expect(inbox).toBeVisible();
    await inbox
      .getByRole('button', { name: /Questions.*Background Notification Questions/ })
      .click();
    await expect(inbox).not.toBeVisible();

    const preview = handle.page.getByRole('dialog', { name: 'Live agent preview' });
    // The prompt and options render as the agent's conversation turn; the
    // composer strip in the "Agent request" footer sends the answer.
    const questions = preview.getByRole('group', { name: 'Agent question' });
    await expect(
      questions.getByText('Which verification tracks should be included?'),
    ).toBeVisible();
    await questions.getByText('Unit tests', { exact: true }).click();
    await questions.getByText('Packaged smoke', { exact: true }).click();
    await questions
      .getByLabel(/Evidence note free text/)
      .fill('Keep the routed question on target.');
    await preview
      .getByRole('region', { name: 'Agent request' })
      .getByRole('button', { name: /^Send/ })
      .click();
    await waitForAttentionMissing(handle, 'ask-bundle');

    persistAppLogs(handle, 'background-notifications-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function openPalette(handle: AppHandle): Promise<void> {
  await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
  await expect(handle.page.getByRole('dialog', { name: 'Command palette' })).toBeVisible();
}

async function installNotificationCapture(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ Notification }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ title: string; body: string; click?: () => void }>;
    };
    global.__agenticoNotifications = [];
    Notification.isSupported = () => true;
    Notification.prototype.on = function patchedOn(
      this: { __agenticoClick?: () => void },
      event: string,
      listener: () => void,
    ) {
      if (event === 'click') this.__agenticoClick = listener;
      return this;
    } as typeof Notification.prototype.on;
    Notification.prototype.show = function patchedShow(this: {
      title?: string;
      body?: string;
      __agenticoClick?: () => void;
    }) {
      global.__agenticoNotifications?.push({
        title: this.title ?? '',
        body: this.body ?? '',
        click: this.__agenticoClick,
      });
      return this;
    } as typeof Notification.prototype.show;
  });
}

async function capturedNotifications(
  handle: AppHandle,
): Promise<Array<{ title: string; body: string }>> {
  return handle.app.evaluate(() => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ title: string; body: string }>;
    };
    return global.__agenticoNotifications ?? [];
  });
}

async function activateNotification(handle: AppHandle, index: number): Promise<void> {
  await handle.app.evaluate((_electron, notificationIndex) => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ click?: () => void }>;
    };
    const notification = global.__agenticoNotifications?.[notificationIndex];
    if (notification?.click === undefined) {
      throw new Error(`notification ${notificationIndex} has no click listener`);
    }
    notification.click();
  }, index);
}

async function waitForNotificationCount(handle: AppHandle, count: number): Promise<void> {
  await waitFor(
    async () => (await capturedNotifications(handle)).length >= count,
    `${count} captured background notifications`,
    30_000,
  );
}

function uniqueNotificationIdentities(
  notifications: Array<{ title: string; body: string }>,
): Array<string> {
  return [
    ...new Set(notifications.map((notification) => `${notification.title}:${notification.body}`)),
  ];
}

async function hideMainWindow(
  handle: AppHandle,
  options: { refreshBackground?: boolean } = {},
): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow }, shouldRefresh) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    window.hide();
    const global = globalThis as typeof globalThis & {
      __agenticoRefreshBackgroundState?: () => void;
    };
    if (shouldRefresh) {
      global.__agenticoRefreshBackgroundState?.();
    }
  }, options.refreshBackground !== false);
}

async function ensureMainWindowFocus(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow, app }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoMainWindowFocusState?: { focused: boolean };
      __agenticoSetMainWindowAttentionFocusOverride?: (focused: boolean) => void;
    };
    global.__agenticoSetMainWindowAttentionFocusOverride?.(true);
    if (global.__agenticoMainWindowFocusState?.focused === true) return;
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    if (!window.isVisible()) window.show();
    app.focus({ steal: true });
    window.focus();
  });
  await waitFor(
    async () =>
      handle.app.evaluate(() => {
        const global = globalThis as typeof globalThis & {
          __agenticoMainWindowFocusState?: { focused: boolean };
        };
        return global.__agenticoMainWindowFocusState?.focused === true;
      }),
    'main window to report focused',
    5_000,
  );
}

async function waitForAttentionItem(handle: AppHandle, id: string): Promise<void> {
  await waitFor(
    async () => {
      const snapshot = await handle.page.evaluate(() => window.agentico.getAttention());
      return snapshot.items.some((item) => item.id === id);
    },
    `attention item ${id}`,
    60_000,
  );
}

async function waitForAttentionMissing(handle: AppHandle, id: string): Promise<void> {
  await waitFor(
    async () => {
      const snapshot = await handle.page.evaluate(() => window.agentico.getAttention());
      return !snapshot.items.some((item) => item.id === id);
    },
    `attention item ${id} to clear`,
    60_000,
  );
}

async function answerPermission(
  handle: AppHandle,
  requestId: string,
  decision: 'allow_once' | 'allow_remember' | 'deny',
): Promise<void> {
  await handle.page.evaluate(
    ({ id, answer }) =>
      window.agentico.answerPermission({
        requestId: id,
        decision: answer,
        ...(answer === 'allow_remember'
          ? { rememberPattern: 'Bash(npm test *)', rememberScope: 'global' }
          : {}),
      }),
    { id: requestId, answer: decision },
  );
}
