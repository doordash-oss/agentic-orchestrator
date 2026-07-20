import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  waitFor,
} from '../helpers/world';

test('packaged AMA dock streams singleton chat, restores drawer state, and retains ended transcripts', async ({}, testInfo) => {
  const world = createWorld('background-ama-notifications', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'ama-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-ama-notifications' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const dock = handle.page.getByRole('complementary', { name: 'Ask Agentico' });
    await expect(dock).toBeVisible();
    await expect(dock).toHaveAttribute('data-mode', 'compact');
    await dock
      .getByRole('textbox', { name: 'Ask Agentico' })
      .fill('Summarize the current workspace state.');
    await dock.getByRole('button', { name: 'Send' }).click();
    await expect(dock).toHaveAttribute('data-mode', 'expanded');
    await expect(dock.getByLabel('AMA transcript')).toContainText(/Backfill ready|Live semantic/, {
      timeout: 60_000,
    });
    const startedChat = await handle.page.evaluate(() => window.agentico.getSession('__chat__'));
    expect(startedChat.id).toBe('__chat__');
    const firstTranscript = await handle.page.evaluate(() =>
      window.agentico.getSessionTranscript({ sessionId: '__chat__', limit: 200 }),
    );
    expect(firstTranscript.messages.length).toBeGreaterThan(0);

    const followUp = await handle.page.evaluate(() =>
      window.agentico.startChat({ message: 'Follow up in the active AMA session.' }),
    );
    expect(followUp).toMatchObject({ sessionId: '__chat__', result: 'sent' });
    const chatSessions = await handle.page.evaluate(() =>
      window.agentico
        .listSessions()
        .then((sessions) => sessions.filter((session) => session.id === '__chat__')),
    );
    expect(chatSessions).toHaveLength(1);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    const settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.notifications.previewEnabled).toBe(false);
    expect(settings.ama.drawer).toBe('expanded');

    await dock.getByRole('button', { name: 'AMA', exact: true }).click();
    await expect(dock).toHaveAttribute('data-mode', 'compact');
    await handle.page.reload();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const restoredDock = handle.page.getByRole('complementary', { name: 'Ask Agentico' });
    await expect(restoredDock).toHaveAttribute('data-mode', 'compact');
    await restoredDock.getByRole('button', { name: 'AMA', exact: true }).click();
    await expect(restoredDock).toHaveAttribute('data-mode', 'expanded');
    await expect(restoredDock.getByLabel('AMA transcript')).toContainText(
      /Backfill ready|Live semantic/,
    );

    const end = restoredDock.getByRole('button', { name: 'End AMA', exact: true });
    await expect(end).toBeVisible();
    await end.click();
    const endConfirm = restoredDock.getByRole('group', { name: 'End AMA confirmation' });
    await expect(endConfirm).toContainText('transcript stays read-only');
    await endConfirm.getByRole('button', { name: 'End AMA', exact: true }).click();
    await expect(restoredDock).toContainText('AMA ended.');
    await expect(restoredDock).toContainText('Read-only transcript');
    const endedChat = await handle.page.evaluate(() => window.agentico.getSession('__chat__'));
    expect(endedChat.status.toLowerCase()).toMatch(/ended|stopped|complete|done|cancel/);
    const endedTranscript = await handle.page.evaluate(() =>
      window.agentico.getSessionTranscript({ sessionId: '__chat__', limit: 200 }),
    );
    expect(endedTranscript.messages.length).toBeGreaterThanOrEqual(firstTranscript.messages.length);
    expect(endedTranscript.messages.map((message) => message.text ?? '').join('\n')).toContain(
      firstTranscript.messages[0]?.text ?? '',
    );

    persistAppLogs(handle, 'background-ama-notifications-app-server');
  } finally {
    if (handle !== null) {
      await handle.page.evaluate(() => window.agentico.endChat()).catch(() => {});
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged attention notifications are private, deduplicated, bounded, exact-targeted, and do not steal focus', async ({}, testInfo) => {
  const world = createWorld('background-ama-notifications-attention', {
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

    const cockpit = await createFeatureViaForm(handle, {
      name: 'Background Notification Questions',
      description: 'Fixture-backed attention for notification and question routing.',
      repoPatterns: [/notify-lab/],
      waitForReady: true,
    });
    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();
    await waitForAttentionItem(handle, 'perm-allow-once');
    expect(await capturedNotifications(handle)).toHaveLength(0);

    await hideMainWindow(handle, { refreshBackground: false });
    await answerPermission(handle, 'perm-allow-once', 'allow_once');
    await waitForAttentionItem(handle, 'perm-stale');
    await waitForNotificationCount(handle, 1);
    let notifications = await capturedNotifications(handle);
    expect(notifications[0]?.body).toBe('Agentico needs attention.');
    expect(uniqueNotificationIdentities(notifications)).toHaveLength(notifications.length);
    await handle.app.evaluate(() => new Promise((resolve) => setTimeout(resolve, 500)));
    expect(await capturedNotifications(handle)).toHaveLength(1);

    await activateNotification(handle, 0);
    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).toBeVisible({ timeout: 30_000 });
    const focusedItem = inbox.getByRole('button', {
      name: /Permission.*Background Notification Questions/,
    });
    await expect(focusedItem).toBeFocused();

    await answerPermission(handle, 'perm-stale', 'deny');
    await waitForAttentionItem(handle, 'perm-deny');
    await answerPermission(handle, 'perm-deny', 'deny');
    await waitForAttentionItem(handle, 'perm-remember');
    await openPalette(handle);
    const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByLabel('Search commands').fill('settings');
    await expect(palette.getByLabel('Search commands')).toBeFocused();
    await answerPermission(handle, 'perm-remember', 'allow_remember');
    await waitForAttentionItem(handle, 'perm-remember-followup');
    await answerPermission(handle, 'perm-remember-followup', 'allow_once');
    await waitForAttentionItem(handle, 'ask-bundle');
    await expect(palette.getByLabel('Search commands')).toBeFocused();
    await handle.page.keyboard.press('Escape');
    await expect(palette).not.toBeVisible();

    await handle.page.evaluate(() =>
      window.agentico.updateSettings({ notifications: { previewEnabled: true } }),
    );
    await hideMainWindow(handle);
    await waitForNotificationCount(handle, 2);
    notifications = await capturedNotifications(handle);
    const askNotification = notifications[1];
    expect(askNotification?.body).toContain('Questions');
    expect(askNotification?.body).toContain('Background Notification Questions');
    expect(askNotification?.body.length ?? 0).toBeLessThanOrEqual(180);

    await activateNotification(handle, 1);
    await expect(inbox).toBeVisible({ timeout: 30_000 });
    await expect(inbox.getByText('Which verification tracks should be included?')).toBeVisible();
    await inbox.getByRole('checkbox', { name: /Unit tests/ }).check();
    await inbox.getByRole('checkbox', { name: /Packaged smoke/ }).check();
    await inbox.getByLabel(/Evidence note free text/).fill('Keep the routed question on target.');
    await inbox.getByRole('button', { name: 'Close inbox' }).click();

    const inlineQuestions = cockpit.getByRole('region', { name: 'Feature attention' });
    await expect(
      inlineQuestions.getByText('Which verification tracks should be included?'),
    ).toBeVisible();
    await expect(inlineQuestions.getByRole('checkbox', { name: /Unit tests/ })).toBeChecked();
    await expect(inlineQuestions.getByRole('checkbox', { name: /Packaged smoke/ })).toBeChecked();
    await expect(inlineQuestions.getByLabel(/Evidence note free text/)).toHaveValue(
      'Keep the routed question on target.',
    );
    await inlineQuestions.getByRole('button', { name: 'Submit answers' }).click();
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
