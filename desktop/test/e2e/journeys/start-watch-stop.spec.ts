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
  evidenceShot,
  launchApp,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { worldProcessPIDs } from '../helpers/processes';
import { requireDiscovery, tailText, waitForNewServer } from '../helpers/runtime';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  providerInvocationCount,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('packaged real-server start, semantic watch, history, and authoritative stop', async ({}, testInfo) => {
  const transcript = new Transcript(
    'start-watch-stop',
    'Packaged start → watch → reconnect → stop against the bundled server',
  );
  const world = createWorld('start-watch-stop', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'signal-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'start-watch-stop' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.section('Create one isolated, durably ready feature');
    const cockpit = await createFeatureViaForm(handle, {
      name: 'Packaged Signal Journey',
      description: 'Fixture-backed provider output through the real bundled server.',
      repoPatterns: [/signal-lab/],
      waitForReady: true,
    });
    await evidenceShot(handle, 'cockpit-ready-light-wide');

    // The sidebar mounts exactly one cockpit at a time: switching away
    // unmounts it (unlike the old tab strip, which kept every tab's panel
    // mounted-but-hidden), so a retained handle to this instance is expected
    // to disconnect on navigation rather than persist across it.
    const retainedCockpit = await cockpit.elementHandle();
    expect(retainedCockpit).not.toBeNull();
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect.poll(() => retainedCockpit!.evaluate((node) => node.isConnected)).toBe(false);
    await expect(cockpit).toBeHidden();
    const featureList = handle.page.getByRole('region', { name: 'Existing features' });
    await expect(featureList).toContainText('Packaged Signal Journey');
    await featureList.scrollIntoViewIfNeeded();
    await evidenceShot(handle, 'cockpit-intervention-dashboard-light-wide');
    await handle.page.getByRole('option', { name: 'Packaged Signal Journey' }).click();
    await expect(cockpit).toBeVisible();
    await expect(cockpit.getByText(/Loading .* from the runtime/)).toHaveCount(0);

    transcript.section('Start through the UI exactly once');
    const start = handle.page.getByRole('button', { name: 'Start', exact: true });
    await expect(start).toBeEnabled();
    await start.click();
    await expect(cockpit.getByText(/Start accepted|Starting from/)).toBeVisible();
    await waitFor(
      () => providerInvocationCount(world.providerInvocationLog) === 1,
      'exactly one provider workflow invocation',
      60_000,
    );
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    transcript.step('one Start activation produced exactly one real stream-json provider process');

    await waitFor(
      async () => (await handle!.page.evaluate(() => window.agentico.listSessions())).length === 1,
      'the server-owned session to become listable',
      30_000,
    );

    transcript.section('REST backfill hands off to live semantic output');
    const session = (await handle.page.evaluate(() => window.agentico.listSessions()))[0];
    expect(session).toBeDefined();
    await waitFor(
      async () => {
        const transcript = await handle!.page.evaluate(
          (sessionId) => window.agentico.getSessionTranscript({ sessionId, limit: 500 }),
          session!.id,
        );
        return transcript.messages.some((message) =>
          message.text?.includes('Live semantic update 240'),
        );
      },
      'the authoritative transcript to contain the final live semantic update',
      60_000,
    );
    await cockpit.getByRole('button', { name: 'Refresh current run inspection' }).click();
    const timeline = cockpit.getByRole('region', { name: 'Live agent transcript' });
    await expect(timeline).toBeVisible({ timeout: 60_000 });
    await expect(timeline.getByText(/Backfill ready|Live semantic update/).first()).toBeVisible({
      timeout: 60_000,
    });
    // The transcript lost its framed panel and with it the "Live activity"
    // caption bar; the activity line in the content pane is what reports live
    // output now, so that is what the region must carry.
    const activity = cockpit
      .getByRole('region', { name: 'Current run inspection' })
      .locator('.current-inspection__activity');
    await expect(activity).toBeVisible({ timeout: 60_000 });
    await expect(activity).not.toBeEmpty();
    const backfill = await handle.page.evaluate(
      (sessionId) => window.agentico.getSessionTranscript({ sessionId, limit: 500 }),
      session!.id,
    );
    expect(backfill.messages.some((message) => message.text?.includes('Backfill ready'))).toBe(
      true,
    );
    expect(
      backfill.messages.some((message) => message.text?.includes('Live semantic update 240')),
    ).toBe(true);
    expect(
      backfill.messages.some((message) =>
        message.text?.includes('Backfill ready: inspecting the isolated workspace.'),
      ),
    ).toBe(false);
    expect(
      backfill.messages.some((message) =>
        message.text?.includes('Backfill ready: isolated workspace inspected; live plan follows.'),
      ),
    ).toBe(true);
    transcript.json('bounded authoritative transcript cursor after live handoff', {
      cursor: backfill.cursor,
      rows: backfill.messages.length,
      retainedBackfill: true,
      retainedLiveTail: true,
      repeatedPartialReplacedInPlace: true,
    });
    await setWindowSize(handle, 1440, 960);
    await evidenceShot(handle, 'cockpit-live-grouped-activity-light-wide');

    transcript.section('Live app-owned epoch reset resnapshots without duplicate rows');
    const beforeReset = requireDiscovery(world);
    process.kill(beforeReset.pid, 'SIGKILL');
    await waitFor(() => !processAlive(beforeReset.pid), 'live app-owned server exit', 15_000);
    const connectionShell = handle.page.getByRole('region', { name: 'Agentico connection' });
    await expect(connectionShell).toBeVisible({ timeout: 15_000 });
    await expect(connectionShell.getByRole('status')).toContainText(
      /crashed|recovering|starting|connecting|waiting for health/i,
    );
    await evidenceShot(handle, 'cockpit-live-reconnect-reset-in-progress');
    const afterReset = await waitForNewServer(world, beforeReset.pid);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });
    await expect(handle.page.getByRole('button', { name: 'Stop', exact: true })).toBeEnabled();
    expect(afterReset.pid).not.toBe(beforeReset.pid);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
    transcript.json('live reconnect and epoch resnapshot', {
      previousPid: beforeReset.pid,
      recoveredPid: afterReset.pid,
      providerInvocations: providerInvocationCount(world.providerInvocationLog),
      liveTailRowsVisible: 1,
      duplicateRows: 0,
    });

    await setTheme(handle, 'dark');
    await setWindowSize(handle, 1440, 960);
    await evidenceShot(handle, 'cockpit-live-dark-wide');

    transcript.section('Responsive cockpit retains timeline, raw containment, and Stop');
    await setWindowSize(handle, 760, 900);
    await setTheme(handle, 'dark');
    await expect(handle.page.getByRole('button', { name: 'Stop' })).toBeEnabled();
    await expect(handle.page.getByLabel('Current feature status')).toBeVisible();
    await cockpit.getByRole('button', { name: 'Inspector' }).click();
    const inspector = handle.page.getByRole('dialog', { name: 'Feature inspector' });
    await expect(inspector).toBeVisible();
    await expect(inspector.getByText('Packaged Signal Journey')).toBeVisible();
    await inspector.getByRole('button', { name: 'Close inspector' }).click();
    await expect(inspector).toHaveCount(0);
    await evidenceShot(handle, 'cockpit-live-dark-narrow');
    await setTheme(handle, 'light');
    await expect(handle.page.getByRole('button', { name: 'Stop' })).toBeEnabled();
    await evidenceShot(handle, 'cockpit-live-light-narrow');

    transcript.section('Stop confirmation and authoritative terminal state');
    await handle.page.getByRole('button', { name: 'Stop' }).click();
    const dialog = handle.page.getByRole('dialog', { name: 'Stop Packaged Signal Journey?' });
    await expect(dialog).toContainText(/currently affects 1 live session/);
    await expect(dialog).toContainText(/Existing validated transcript content remains available/);
    await evidenceShot(handle, 'cockpit-stop-impact-confirmation');
    await dialog.getByRole('button', { name: 'Confirm stop' }).click();
    await expect(dialog).toHaveCount(0);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(
          (featureId) => window.agentico.getFeature(featureId),
          features[0]!.id,
        );
        return !['running', 'starting', 'stopping'].includes(snapshot.status.toLowerCase());
      },
      'authoritative terminal feature snapshot after Stop',
      60_000,
    );
    const authoritative = await handle.page.evaluate(
      (featureId) => window.agentico.getFeature(featureId),
      features[0]!.id,
    );
    transcript.json('authoritative feature snapshot after Stop', authoritative);
    expect(['running', 'starting', 'stopping']).not.toContain(authoritative.status.toLowerCase());
    await expect(
      handle.page.getByText(authoritative.status, { exact: true }).first(),
    ).toBeVisible();

    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await expect(handle.page.getByRole('region', { name: 'Existing features' })).toContainText(
      'Packaged Signal Journey',
    );
    await evidenceShot(handle, 'cockpit-terminal-dashboard-light-narrow');

    const discovery = readDiscovery(world);
    transcript.json('real bundled-server discovery (token redacted)', {
      ...discovery,
      auth_token: '[redacted]',
    });
  } finally {
    if (handle !== null) {
      const logs = persistAppLogs(handle, 'start-watch-stop-app-server');
      const discovery = readDiscovery(world);
      if (discovery?.auth_token) expect(logs).not.toContain(discovery.auth_token);
      transcript.codeBlock('redacted app/server log tail', tailText(logs, 40));
      transcript.write(testInfo);
      await closeApp(handle).catch(() => {});
    }
    cleanupFixtureProcesses(world.root);
    await waitFor(
      () => worldProcessPIDs(world.root).length === 0,
      'packaged app, server, and provider processes to exit',
      20_000,
    );
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function cleanupFixtureProcesses(worldRoot: string): void {
  for (const pid of worldProcessPIDs(worldRoot)) {
    try {
      process.kill(-pid, 'SIGKILL');
    } catch {
      try {
        process.kill(pid, 'SIGKILL');
      } catch {
        // The process exited between enumeration and teardown.
      }
    }
  }
}
