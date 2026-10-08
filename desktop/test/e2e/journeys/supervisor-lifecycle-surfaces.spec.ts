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

/**
 * The supervisor's lifecycle as the rest of the shell sees it, against the
 * packaged app, the real bundled server and the Claude stub: the pinned
 * Supervisor row reads "Working" from a feature page while a turn runs and
 * "Approve 1 request" while it waits on a permission (with the toolbar bell
 * and the tray's waiting grade agreeing); a turn that completes while the
 * window is hidden sets the row's unread dot and, only after the 10-second
 * settle, raises exactly one "Supervisor finished a turn." notification
 * whose click reveals the window on the Supervisor page; and the dot clears
 * only once that page is selected with focus back.
 *
 * The stub's gated-permission turn paces the scenario: it holds as running
 * until gate 1 opens, raises its permission request, and after the answer
 * holds as running again until gate 2 opens.
 */
import { expect, test, type Page } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import {
  activateNotification,
  capturedNotifications,
  chooseSupervisorModel,
  hideMainWindow,
  installNotificationCapture,
  mainWindowVisible,
  readProviderLog,
  sendSupervisorMessage,
  setMainWindowFocusOverride,
  supervisorPage,
  supervisorRow,
  trayGrade,
  waitForProviderLog,
} from '../helpers/supervisor';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  openSupervisorGate,
  SUPERVISOR_E2E_MARKERS,
  supervisorStubReply,
  waitFor,
} from '../helpers/world';

const RUN_NAME = 'supervisor-lifecycle-surfaces';
const DETOUR_FEATURE = 'Lifecycle detour';
const TURN_END_BODY = 'Supervisor finished a turn.';
/** The coordinator's settle; the notification must not land before it. */
const SETTLE_MS = 10_000;

test('supervisor lifecycle surfaces: sidebar row states, tray grade, unread dot and the settled turn-end notification', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(RUN_NAME, 'Supervisor lifecycle surfaces journey');
  const world = createWorld(RUN_NAME, {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    supervisorProvider: true,
  });
  createRepo(world, 'lifecycle-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    const page = handle.page;
    await expect(page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await installNotificationCapture(handle);
    // Pin the effective focus so ambient focus on a shared display cannot
    // notify (or clear the unread dot) behind the scenario's back.
    await setMainWindowFocusOverride(handle, true);
    transcript.step('app launched; notifications captured and focus pinned to the main window');

    transcript.section('At rest the Supervisor row shows no marker and no sub-line');
    const row = supervisorRow(page);
    await expect(row).toHaveAttribute('aria-selected', 'true');
    await expect(row).toHaveAttribute('data-supervisor-state', 'none');
    await expect(row).toHaveAttribute('data-unread', 'false');
    await expect(row.locator('.sidebar__row-subline')).toHaveCount(0);
    await expect.poll(() => trayGrade(handle!)).toBe('idle');

    transcript.section('A held turn reads "Working" on the row from a feature page');
    await chooseSupervisorModel(page);
    await sendSupervisorMessage(page, `Check the lab ${SUPERVISOR_E2E_MARKERS.gatedPermission}`);
    await waitForProviderLog(world, 'gated:1');
    await createFeatureViaForm(handle, {
      name: DETOUR_FEATURE,
      repoPatterns: [/lifecycle-lab/],
    });
    await expect(page.locator('.toolbar__title-name')).toHaveText(DETOUR_FEATURE);
    await expect(row).toHaveAttribute('aria-selected', 'false');
    await expect(row).toHaveAttribute('data-supervisor-state', 'working');
    await expect(row).toContainText('Working');
    await expect(row).toHaveAccessibleName('Supervisor');
    await expect.poll(() => trayGrade(handle!)).toBe('working');
    await evidenceShot(handle, 'supervisor-lifecycle-working');
    transcript.step('row shows the working marker and "Working"; tray grade is working');

    transcript.section('A permission request reads "Approve 1 request", in the inbox and the tray');
    openSupervisorGate(world, 1);
    await waitForProviderLog(world, 'pending:supervisor-perm-1');
    await expect(row).toHaveAttribute('data-supervisor-state', 'needs-response', {
      timeout: 30_000,
    });
    await expect(row).toContainText('Approve 1 request');
    await expect(row).not.toContainText('Working');
    await expect(row).toHaveAttribute('data-unread', 'false');
    await expect(page.getByRole('button', { name: /Attention inbox, 1 pending/ })).toBeVisible();
    await expect.poll(() => trayGrade(handle!)).toBe('waiting');
    // Focused: the request's own attention notification stays quiet.
    expect(await capturedNotifications(handle)).toHaveLength(0);
    await evidenceShot(handle, 'supervisor-lifecycle-needs-response');
    transcript.step('row needs a response; the bell counts one; tray grade is waiting');

    transcript.section('Approved, the turn keeps working until it is let go');
    await approveSupervisorPermission(page, 'supervisor-perm-1');
    await waitForProviderLog(world, 'response:supervisor-perm-1:');
    expect(readProviderLog(world)).toMatch(/response:supervisor-perm-1:.*"behavior":"allow"/);
    await expect(row).toHaveAttribute('data-supervisor-state', 'working', { timeout: 30_000 });
    await waitFor(
      async () =>
        (await page.evaluate(() => window.agentico.getAttention())).items.every(
          (item) => item.id !== 'supervisor-perm-1',
        ),
      'the answered supervisor request to leave the inbox',
    );
    await expect.poll(() => trayGrade(handle!)).toBe('working');
    transcript.step('approved through the attention bridge; the row is back to working');

    transcript.section('The turn completes while the window is hidden');
    await hideMainWindow(handle);
    expect(await mainWindowVisible(handle)).toBe(false);
    openSupervisorGate(world, 2);
    await waitFor(
      async () => {
        const state = await page.evaluate(() => window.agentico.getSupervisorState());
        return state.lifecycle === 'idle' && state.lastTurnOutcome === 'completed';
      },
      'the gated turn to complete',
      30_000,
    );
    const settledAt = Date.now();
    await expect(row).toHaveAttribute('data-unread', 'true');
    await expect(row).toHaveAttribute('data-supervisor-state', 'none');
    await expect(row.locator('.sidebar__row-unread')).toHaveCount(1);
    await expect(row.locator('.sidebar__row-subline')).toHaveCount(0);
    await expect.poll(() => trayGrade(handle!)).toBe('idle');
    // Still inside the settle: nothing has been raised yet.
    expect(Date.now() - settledAt).toBeLessThan(SETTLE_MS - 2_000);
    expect(await capturedNotifications(handle)).toHaveLength(0);
    transcript.step('turn completed while hidden: unread dot set, no notification yet');

    await waitFor(
      async () => (await capturedNotifications(handle!)).length > 0,
      'the settled turn-end notification',
      45_000,
    );
    const firedAfter = Date.now() - settledAt;
    // The settle started before this side saw the turn end, so allow for
    // the observation lag rather than the whole window.
    expect(firedAfter).toBeGreaterThanOrEqual(SETTLE_MS - 3_000);
    await page.waitForTimeout(1_000);
    const notifications = await capturedNotifications(handle);
    expect(notifications).toEqual([{ title: 'Agentico', body: TURN_END_BODY }]);
    transcript.json('captured notifications', { firedAfterMs: firedAfter, notifications });

    transcript.section('The click reveals the window on the Supervisor page; focus clears the dot');
    // Visible but unfocused first: selecting the page alone must not read it.
    await setMainWindowFocusOverride(handle, false);
    await activateNotification(handle, 0);
    await expect.poll(() => mainWindowVisible(handle!)).toBe(true);
    await expect(row).toHaveAttribute('aria-selected', 'true');
    await expect(supervisorPage(page)).toBeVisible();
    await expect(page.locator('.toolbar__title-name')).toHaveText('Supervisor');
    await expect(page.getByRole('region', { name: 'Supervisor conversation' })).toContainText(
      supervisorStubReply(1),
    );
    await page.waitForTimeout(500);
    await expect(row).toHaveAttribute('data-unread', 'true');
    transcript.step('the notification showed the Supervisor page; unfocused, the dot stayed');

    await setMainWindowFocusOverride(handle, true);
    await expect(row).toHaveAttribute('data-unread', 'false');
    await expect(row.locator('.sidebar__row-unread')).toHaveCount(0);
    expect(await capturedNotifications(handle)).toHaveLength(1);
    await expect.poll(() => trayGrade(handle!)).toBe('idle');
    await evidenceShot(handle, 'supervisor-lifecycle-read');
    transcript.step('focus restored on the selected page cleared the dot');

    transcript.json('provider log', readProviderLog(world).split('\n'));
    transcript.write(testInfo);
  } finally {
    // A gate the journey never reached would keep the stub asleep past
    // teardown; open both so it can read its closed stdin and exit.
    openSupervisorGate(world, 1);
    openSupervisorGate(world, 2);
    if (handle !== null) {
      persistAppLogs(handle, RUN_NAME);
      await closeApp(handle).catch(() => {});
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function approveSupervisorPermission(page: Page, requestId: string): Promise<void> {
  await page.evaluate(
    (id) => window.agentico.answerPermission({ requestId: id, decision: 'allow_once' }),
    requestId,
  );
}
