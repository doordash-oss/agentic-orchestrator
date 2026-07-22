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

    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();
    await waitFor(
      () => providerInvocationCount(world.providerInvocationLog) === 1,
      'one authoritative provider invocation',
      60_000,
    );

    const feature = (await handle.page.evaluate(() => window.agentico.listFeatures())).find(
      (candidate) => candidate.name === featureName,
    );
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

    const inspection = cockpit.getByRole('region', { name: 'Current run inspection' });
    await expect(inspection).toBeVisible({ timeout: 60_000 });
    await inspection.getByRole('button', { name: 'Refresh' }).click();
    // The fixture can finish between the Start response and this assertion;
    // the current-run contract remains inspectable in either live or freshly
    // completed state, while Context proves the authoritative preview loaded.
    await expect(inspection.getByText(/Context/)).toBeVisible({ timeout: 60_000 });
    // The phase log exists for the whole active run. A provider session log is
    // phase-dependent and may not exist yet while the first phase is starting.
    await inspection.getByRole('button', { name: 'Open log logs/phase.log' }).click();
    await expect(inspection.getByLabel('Current run log content')).toBeVisible({
      timeout: 30_000,
    });

    await contractEvidenceShot(
      handle,
      'feature-cockpit-with-live-preview-logs-artifacts-navigation-and-repository-label-1728x1117',
      1728,
      1117,
      'light',
    );

    const timeline = cockpit.getByRole('region', { name: 'Current run timeline' });
    await expect(timeline.getByText(/Live semantic update 240/)).toBeVisible({ timeout: 60_000 });

    await handle.page.getByRole('button', { name: `Close ${featureName} tab` }).click();
    await expect(handle.page.getByRole('tab', { name: featureName })).toHaveCount(0);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    const list = handle.page.getByRole('region', { name: 'Existing features' });
    const row = list.locator('.feature-list__item').filter({ hasText: featureName });
    await row.getByRole('button', { name: 'Open' }).click();
    const reopened = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(reopened.getByRole('region', { name: 'Current run timeline' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(
      reopened
        .getByRole('region', { name: 'Current run timeline' })
        .getByText(/Live semantic update 240/),
    ).toHaveCount(1);

    await handle.page.getByRole('tab', { name: 'Home' }).click();
    const bulk = handle.page.getByRole('region', { name: 'Bulk resume and retry' });
    await bulk.getByRole('button', { name: 'Fresh preview' }).click();
    await expect(bulk.getByText(/No features are eligible/)).toBeVisible({
      timeout: 30_000,
    });
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);

    await handle.page.getByRole('tab', { name: featureName }).click();
    await reopened.getByRole('button', { name: 'Stop', exact: true }).click();
    const stopDialog = handle.page.getByRole('dialog', { name: `Stop ${featureName}?` });
    await expect(stopDialog).toContainText(/live session/);
    await stopDialog.getByRole('button', { name: 'Confirm stop' }).click();
    await expect(stopDialog).toHaveCount(0);
    await waitFor(
      async () => {
        const snapshot = await handle!.page.evaluate(
          (featureId) => window.agentico.getFeature(featureId),
          (await handle!.page.evaluate(() => window.agentico.listFeatures())).find(
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
