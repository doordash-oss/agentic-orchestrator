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
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { worldProcessPIDs } from '../helpers/processes';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  waitFor,
} from '../helpers/world';

test('zero-gap operations: dismissible watch, live inspection, bounded files, and fresh bulk state', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const world = createWorld('zero-gap-operations', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'operations-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'zero-gap-operations' });
    const featureName = `Zero Gap Operations ${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'Exercise the complete read-only operational inspection surface.',
      repoPatterns: [/operations-lab/],
      waitForReady: true,
    });

    await handle.page.getByRole('button', { name: 'Start', exact: true }).click();
    await waitFor(
      () => providerInvocationCount(world.providerInvocationLog) === 1,
      'one authoritative provider invocation',
      60_000,
    );

    const feature = (
      await handle.page.evaluate(() => window.agentico.listFeatures())
    ).features.find((candidate) => candidate.name === featureName);
    if (feature === undefined) throw new Error('created operations feature was not listed');
    const detail = await handle.page.evaluate(
      (featureId) => window.agentico.getFeature(featureId),
      feature.id,
    );
    const runPath = path.join(
      world.stateDir,
      feature.id,
      'runs',
      `run-${String(detail.activeRun).padStart(3, '0')}`,
    );
    await waitFor(
      () => fs.existsSync(path.join(runPath, 'run.yaml')),
      'active run fixture',
      30_000,
    );
    fs.mkdirSync(path.join(runPath, 'logs'), { recursive: true });
    fs.writeFileSync(
      path.join(runPath, 'logs', 'phase.log'),
      'bounded packaged phase log for current-run inspection\n',
    );
    let phaseLogId = '';
    await waitFor(
      async () => {
        const result = await handle!.page.evaluate(
          ({ featureId, runNumber }) => window.agentico.listRunLogs({ featureId, runNumber }),
          { featureId: feature.id, runNumber: detail.activeRun },
        );
        phaseLogId = result.logs.find((log) => log.path === 'logs/phase.log')?.id ?? '';
        return phaseLogId !== '';
      },
      'the authoritative run-log catalogue to include phase.log',
      30_000,
    );
    const phaseLogText = await handle.page.evaluate(
      ({ featureId, runNumber, logId }) =>
        window.agentico.getRunLogContent({ featureId, runNumber, logId }),
      { featureId: feature.id, runNumber: detail.activeRun, logId: phaseLogId },
    );
    expect(phaseLogText.text).toContain('bounded packaged phase log');

    const inspection = cockpit.getByRole('region', { name: 'Current run inspection' });
    await expect(inspection).toBeVisible({ timeout: 60_000 });
    // Refresh moved to the stage bar's trailing side when the transcript shed
    // its frame, so it is cockpit chrome now, not a control inside the region.
    await cockpit.getByRole('button', { name: 'Refresh current run inspection' }).click();
    // The fixture can finish between the Start response and this assertion;
    // the current-run contract remains inspectable in either live or freshly
    // completed state, while Context proves the authoritative preview loaded.
    // Context now lives in the phase rail above the stage area, not inside
    // the "Current run inspection" region itself.
    await expect(cockpit.locator('.phase-rail__trio').getByText(/Context/)).toBeVisible({
      timeout: 60_000,
    });
    // The phase log exists for the whole active run. A provider session log is
    // phase-dependent and may not exist yet while the first phase is starting.
    // Files is a top-level stage-bar segment now, not a control inside the
    // "Current run inspection" region.
    await cockpit.getByRole('tab', { name: 'Files' }).click();
    await expect(cockpit.getByRole('region', { name: 'Run artifacts' })).toBeVisible();
    await expect(cockpit.getByRole('region', { name: 'Bounded logs' })).toBeVisible();

    await contractEvidenceShot(
      handle,
      'feature-cockpit-with-live-preview-logs-artifacts-navigation-and-repository-label-1728x1117',
      1728,
      1117,
      'light',
    );

    // Back to Live — the Files segment has no conversation of its own.
    await cockpit.getByRole('tab', { name: 'Live' }).click();
    const timeline = cockpit.getByRole('region', { name: 'Live agent transcript' });
    await expect(timeline.getByText(/Backfill ready|Live semantic update/).first()).toBeVisible({
      timeout: 60_000,
    });
    const session = (await handle.page.evaluate(() => window.agentico.listSessions()))[0];
    if (!session) throw new Error('workflow session was not listed');
    await waitFor(
      async () => {
        const transcript = await handle!.page.evaluate(
          (sessionId) => window.agentico.getSessionTranscript({ sessionId, limit: 500 }),
          session.id,
        );
        return transcript.messages.some((message) =>
          message.text?.includes('Live semantic update 240'),
        );
      },
      'the authoritative transcript to contain the final live semantic update',
      60_000,
    );

    // The sidebar has no "close tab" affordance — every feature always has a
    // row. Navigating to Overview unmounts the cockpit (resetting transient
    // view state) without removing the feature from the sidebar; reopening
    // via the Overview feature list's "Open" button is the equivalent of the
    // old reopen-a-closed-tab path.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    const list = handle.page.getByRole('region', { name: 'Existing features' });
    const row = list.locator('li').filter({ hasText: featureName });
    await row.getByRole('button', { name: 'Open' }).click();
    const reopened = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(reopened.getByRole('region', { name: 'Current run inspection' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(reopened.getByRole('region', { name: 'Live agent transcript' })).toBeVisible();

    await handle.page.getByRole('option', { name: 'Overview' }).click();
    const bulk = handle.page.getByRole('region', { name: 'Bulk resume and retry' });
    await bulk.getByRole('button', { name: 'Fresh preview' }).click();
    await expect(bulk.getByText(/No features are eligible/)).toBeVisible({
      timeout: 30_000,
    });
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    await handle.page.getByRole('option', { name: featureName }).click();
    await handle.page.getByRole('button', { name: 'Stop', exact: true }).click();
    const stopDialog = handle.page.getByRole('dialog', { name: `Stop ${featureName}?` });
    await expect(stopDialog).toContainText(/live session/);
    await stopDialog.getByRole('button', { name: 'Confirm stop' }).click();
    await expect(stopDialog).toHaveCount(0);
    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(
          (featureId) => window.agentico.getFeature(featureId),
          (await handle!.page.evaluate(() => window.agentico.listFeatures())).features.find(
            (feature) => feature.name === featureName,
          )!.id,
        );
        return !['running', 'starting', 'stopping'].includes(snapshot.status.toLowerCase());
      },
      'authoritative terminal state after stop',
      60_000,
    );
    // The authoritative state flips before the provider shell finishes
    // consuming its interrupt record; allow that bounded child reap to land.
    await handle.page.waitForTimeout(1_500);
    persistAppLogs(handle, 'zero-gap-operations-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    cleanupFixtureProcesses(world.root);
    await waitFor(
      () => worldProcessPIDs(world.root).length === 0,
      'fixture process cleanup',
      5_000,
    );
    await assertNoLeakedProcesses(world);
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
