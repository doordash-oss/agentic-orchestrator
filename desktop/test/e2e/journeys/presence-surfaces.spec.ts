/**
 * Presence-surfaces journey: one durable failure (a seeded
 * iteration_budget_exhausted record) must appear as one owner plus matching
 * indicators everywhere — the Failed sidebar lane with the catalog title as
 * its sub-line, the bell and its single pending item, the inbox row and its
 * detail, a chip that focuses the owning card, and exactly one background
 * notification — and everything clears together after a successful restart.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type TestInfo } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShotBothThemes,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { setFeatureStatus } from '../helpers/seed';
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, waitFor } from '../helpers/world';

test('presence surfaces: one failure, one owner, every surface agrees', async ({}, testInfo: TestInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    'presence-surfaces',
    'Seeded failure → Failed lane, bell, inbox row, focused card, chip, one notification, restart clears all',
  );
  const world = createWorld('presence-surfaces', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'presence-lab', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step('committed repository: presence-lab');

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch and create the feature');
    handle = await launchApp(world, testInfo, { traceName: 'presence-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const featureName = 'Presence Feature';
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'presence surfaces journey',
      repoPatterns: [/presence-lab/],
      waitForReady: true,
    });
    const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
    expect(features).toHaveLength(1);
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);
    await closeApp(handle);
    handle = null;

    transcript.section('Seed a Failed feature with an iteration_budget_exhausted record');
    setFeatureStatus(world.stateDir, featureId, 'Failed');
    const featurePath = path.join(world.stateDir, featureId, 'feature.yaml');
    let featureYaml = fs.readFileSync(featurePath, 'utf8');
    featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '2');
    featureYaml = upsertYamlScalar(featureYaml, 'max_iterations', '5');
    fs.writeFileSync(featurePath, featureYaml);
    const runPath = path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml');
    const runYaml = replaceTopLevelBlock(fs.readFileSync(runPath, 'utf8'), 'failure', [
      'failure:',
      '  code: iteration_budget_exhausted',
      '  context:',
      '    phase:',
      '      name: implement',
      '      iteration: 3',
      '  diagnostics: phase hit the configured iteration ceiling',
    ]);
    fs.writeFileSync(runPath, runYaml);
    transcript.step('seeded Failed@implement with an iteration_budget_exhausted record');

    transcript.section('Relaunch; the sidebar groups the failure under Failed');
    handle = await launchApp(world, testInfo, { traceName: 'presence-relaunch' });
    const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });
    // The notification assertions below need a deterministically focused
    // window while the failure item is pending, and the capture must live in
    // this app instance.
    await ensureMainWindowFocus(handle);
    await installNotificationCapture(handle);

    const sidebar = handle.page.getByRole('navigation', { name: 'Feature sidebar' });
    const failedGroup = sidebar.getByRole('group', { name: 'Failed' });
    const failedRow = failedGroup.getByRole('option', { name: new RegExp(featureName) });
    await expect(failedRow).toBeVisible({ timeout: 60_000 });
    await expect(failedRow.locator('.sidebar__row-subline')).toHaveText(
      'Iteration budget exhausted',
    );
    transcript.step('the Failed lane carries the feature with the catalog title as its sub-line');

    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await overviewOption.click();
    const overviewRow = handle.page
      .getByRole('region', { name: 'Existing features' })
      .locator('li', { hasText: featureName });
    await expect(overviewRow.locator('.overview-row__state')).toHaveText(
      'Iteration budget exhausted',
    );
    transcript.step('the Overview row state mirrors the sidebar sub-line');
    await handle.page.getByRole('option', { name: new RegExp(featureName) }).click();
    await expect(cockpit).toBeVisible({ timeout: 30_000 });

    transcript.section('Bell, inbox row, and the owning card');
    const errorItemId = `error:${featureId}:run::iteration_budget_exhausted`;
    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(() => window.agentico.getAttention());
        return snapshot.items.some((item) => item.id === errorItemId);
      },
      `error attention item ${errorItemId}`,
      60_000,
    );
    const bell = handle.page.getByRole('button', { name: 'Attention inbox, 1 pending' });
    await expect(bell).toBeVisible({ timeout: 30_000 });
    await bell.click();
    const inbox = handle.page.getByRole('complementary', { name: 'Attention inbox' });
    await expect(inbox).toBeVisible();
    const inboxRow = inbox.getByRole('button', { name: /Failed/ });
    await expect(inboxRow).toContainText(featureName);
    transcript.step(
      'the bell reports one pending item; the inbox row reads Failed with the feature name',
    );

    const failureCard = cockpit.getByRole('alert');
    await expect(failureCard).toBeVisible({ timeout: 30_000 });
    await inboxRow.click();
    await expect(inbox).not.toBeVisible();
    await expect(cockpit).toBeVisible();
    // The jump resolved the item's reference through the owner-card registry.
    await expect(failureCard).toBeFocused({ timeout: 15_000 });
    transcript.step('the inbox row landed on the cockpit with the failure card focused');

    // The routed item's detail — the live-preview overlay's attention footer —
    // shows the class-label eyebrow and the catalog title, never remediation
    // or diagnostics.
    const detail = handle.page.locator('.attention-detail').filter({
      hasText: 'Iteration budget exhausted',
    });
    await expect(detail).toBeVisible({ timeout: 30_000 });
    await expect(detail.locator('.error-surface__remediation')).toHaveCount(0);
    transcript.step("the item's detail shows the title without remediation or diagnostics");

    // Leave the overlay; its focus restoration returns to the card.
    await handle.page.getByRole('button', { name: 'Exit full screen' }).click();
    await expect(handle.page.getByRole('dialog', { name: 'Live agent preview' })).not.toBeVisible();
    await expect(failureCard).toBeFocused({ timeout: 15_000 });

    const chip = handle.page.getByRole('button', {
      name: 'Failed. Iteration budget exhausted. Focus the error card.',
    });
    await expect(chip).toBeVisible({ timeout: 30_000 });
    await chip.click();
    await expect(failureCard).toBeFocused();
    transcript.step(
      'the chip reads Failed with the title in its accessible name and focuses the card',
    );

    await evidenceShotBothThemes(handle, 'presence-surfaces-failure');

    transcript.section('One background notification while hidden, previews enabled');
    await handle.page.evaluate(() =>
      window.agentico.updateSettings({ notifications: { previewEnabled: true } }),
    );
    await ensureMainWindowFocus(handle);
    expect(await capturedNotifications(handle)).toHaveLength(0);
    await hideMainWindow(handle);
    await waitForNotificationCount(handle, 1);
    const notifications = await capturedNotifications(handle);
    expect(notifications).toHaveLength(1);
    expect(notifications[0]?.body).toContain('Failed');
    expect(notifications[0]?.body).toContain(featureName);
    expect(notifications[0]?.body).toContain('Iteration budget exhausted');
    expect(notifications[0]?.body.length ?? 0).toBeLessThanOrEqual(180);
    await handle.app.evaluate(() => new Promise((resolve) => setTimeout(resolve, 500)));
    expect(await capturedNotifications(handle)).toHaveLength(1);
    transcript.step('exactly one preview notification carried the class label, feature, and title');

    transcript.section('Restart clears the item and the lane entry');
    await activateNotification(handle, 0);
    await ensureMainWindowFocus(handle);
    const restartButton = failureCard.getByRole('button', { name: 'Restart' });
    await expect(restartButton).toBeEnabled({ timeout: 30_000 });
    await restartButton.click();
    const dialog = handle.page.getByRole('dialog', { name: `Restart ${featureName}?` });
    await expect(dialog).toBeVisible();
    await dialog.getByRole('button', { name: 'Confirm restart' }).click();
    const afterRestart = await handle.page.evaluate(
      (id) => window.agentico.getFeature(id),
      featureId,
    );
    expect(afterRestart.status).not.toBe('Failed');
    expect(afterRestart.errors).toHaveLength(0);
    transcript.step('the restart cleared the owned error from the authoritative snapshot');

    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(() => window.agentico.getAttention());
        return !snapshot.items.some((item) => item.id === errorItemId);
      },
      `error attention item ${errorItemId} to clear`,
      30_000,
    );
    await expect(
      handle.page.getByRole('button', { name: 'Attention inbox, 0 pending' }),
    ).toBeVisible({ timeout: 30_000 });
    await expect(handle.page.getByRole('group', { name: 'Failed' })).toHaveCount(0);
    transcript.step('the bell and the Failed lane entry cleared with it');

    persistAppLogs(handle, 'presence-surfaces-app-server');
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
  transcript.write(testInfo);
});

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

async function hideMainWindow(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    window.hide();
    const global = globalThis as typeof globalThis & {
      __agenticoRefreshBackgroundState?: () => void;
    };
    global.__agenticoRefreshBackgroundState?.();
  });
}

async function ensureMainWindowFocus(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow, app }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoMainWindowFocusState?: { focused: boolean };
    };
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
