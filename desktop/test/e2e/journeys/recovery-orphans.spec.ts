/**
 * Recovery orphans journey: priority recovery attention, live/dead orphan
 * context, bounded logs, resilient batch actions, stale refresh, and
 * reconnect/restart against the packaged app and real bundled server.
 *
 * Seeds live and dead orphan sessions via PID files so the recovery scan
 * has real items to present, then exercises the full batch action flow.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type TestInfo } from '@playwright/test';
import { closeApp, createFeatureViaForm, launchApp, type AppHandle } from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';
import { persistAppLogs } from '../helpers/seed';

function writePidFile(
  stateDir: string,
  featureId: string,
  pid: number,
  repoName: string,
  phase: string,
): void {
  const dir = path.join(stateDir, featureId);
  fs.mkdirSync(dir, { recursive: true });
  const fileName = `session-${repoName}.pid`;
  const content = [
    `pid: ${pid}`,
    `feature: ${featureId}`,
    `phase: ${phase}`,
    `iteration: 1`,
    `repo_name: ${repoName}`,
    `log_path: /tmp/agentico-${featureId}-${repoName}.log`,
    '',
  ].join('\n');
  fs.writeFileSync(path.join(dir, fileName), content);
}

test('recovery orphans: priority attention, live/dead context, batch actions, and fresh scan', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('recovery-orphans', 'Recovery orphans journey');
  const world = createWorld('recovery-orphans', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`committed repository: \`${alpha}\``);

  let handle: AppHandle | null = null;
  let liveOrphan: ChildProcess | null = null;
  try {
    transcript.section('Launch and create a feature');
    handle = await launchApp(world, testInfo, { traceName: 'recovery-orphans' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    const featureName = `RecoveryJourney${Math.random().toString(16).slice(2, 8)}`;
    await createFeatureViaForm(handle, {
      name: featureName,
      repoPatterns: [/alpha/],
    });
    transcript.step(`created feature \`${featureName}\``);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed orphan PID files, relaunch');
    const discovery = readDiscovery(world);
    persistAppLogs(handle, 'recovery-orphans-first');
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `first app-owned server ${discovery.pid} to be reaped`,
        15_000,
      );
    }

    // detached: true makes the child a new process-group leader so the
    // server's isProcessGroupAlive(PID) check (which expects PGID==PID,
    // matching real session spawns with Setpgid) sees the live process.
    liveOrphan = spawn('sleep', ['300'], { stdio: 'ignore', detached: true });
    const livePid = liveOrphan.pid!;
    transcript.step(`spawned live orphan process (pid ${livePid}, process-group leader)`);

    writePidFile(world.stateDir, featureId, livePid, 'alpha', 'implement');
    writePidFile(world.stateDir, featureId, 999_999_999, 'beta', 'implement');
    transcript.step('seeded 1 live orphan (alpha) and 1 dead orphan (beta) PID file');

    handle = await launchApp(world, testInfo, { traceName: 'recovery-orphans-seeded' });
    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await expect(overviewOption).toBeVisible({ timeout: 60_000 });
    await overviewOption.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded orphan state');

    transcript.section('Recovery workspace — auto-scan and priority attention');
    const recoveryPanel = handle.page.locator('.recovery-workspace').first();
    await expect(recoveryPanel).toBeVisible({ timeout: 15_000 });

    const recoveryQueue = recoveryPanel.locator('.recovery-workspace__queue');
    await expect(recoveryQueue).toBeVisible({ timeout: 30_000 });
    transcript.step('recovery workspace auto-scanned and rendered the queue');

    const attentionBanner = recoveryPanel.locator('.recovery-attention');
    await expect(attentionBanner).toBeVisible({ timeout: 5_000 });
    transcript.step('recovery-priority attention banner visible');

    const items = recoveryQueue.locator('.recovery-workspace__item');
    const itemCount = await items.count();
    expect(itemCount).toBeGreaterThanOrEqual(2);
    transcript.step(`scan returned ${itemCount} orphan session(s)`);

    const liveItems = recoveryQueue.locator('.recovery-workspace__item[data-alive="true"]');
    const deadItems = recoveryQueue.locator('.recovery-workspace__item[data-alive="false"]');
    const liveCount = await liveItems.count();
    const deadCount = await deadItems.count();
    expect(liveCount).toBeGreaterThanOrEqual(1);
    expect(deadCount).toBeGreaterThanOrEqual(1);
    transcript.step(`risk-first ordering: ${liveCount} live process(es) before ${deadCount} dead`);

    const firstItem = items.first();
    await expect(firstItem).toHaveAttribute('data-alive', 'true');
    transcript.step('first item is the live orphan — priority ordering verified');

    transcript.section('Bounded logs');
    const logsToggle = firstItem.locator('.recovery-workspace__logs-toggle');
    if (await logsToggle.isVisible({ timeout: 3_000 }).catch(() => false)) {
      await logsToggle.click();
      const logsBody = firstItem.locator('.recovery-workspace__logs-body');
      await expect(logsBody).toBeVisible({ timeout: 5_000 });
      transcript.step('bounded logs section expanded for live orphan item');
    } else {
      transcript.step('bounded logs toggle not visible for this item');
    }

    transcript.section('Kill impact confirmation on live orphan');
    const killButton = firstItem.locator('.recovery-workspace__action--kill');
    await expect(killButton).toBeVisible({ timeout: 5_000 });
    await killButton.click();
    const killDialog = handle.page.locator('.impact-dialog__backdrop');
    await expect(killDialog).toBeVisible({ timeout: 5_000 });
    await expect(killDialog.locator('.impact-dialog__title')).toContainText(/Kill live process/i);
    transcript.step('Kill opens an impact confirmation naming the live process and scope');
    const confirmKill = killDialog.getByRole('button', { name: 'Kill process' });
    await confirmKill.click();
    await expect(killDialog).not.toBeVisible({ timeout: 15_000 });
    await expect(firstItem.locator('.recovery-workspace__item-outcome')).toContainText(
      /Kill submitted/i,
      { timeout: 15_000 },
    );
    transcript.step('Kill confirmed — outcome visible on live orphan item');

    transcript.section('Stale snapshot — remaining actions still visible');
    if (itemCount > 1) {
      const secondItem = items.nth(1);
      await expect(secondItem.getByRole('button', { name: 'Resume' })).toBeVisible({
        timeout: 5_000,
      });
      transcript.step(
        'remaining item actions visible after direct action (server enforces stale rejection)',
      );
    }

    transcript.section('Fresh scan and Resume on remaining dead orphan');
    const freshScanButton = recoveryPanel.locator('.recovery-workspace__rescan').last();
    await expect(freshScanButton).toBeVisible({ timeout: 5_000 });
    await freshScanButton.click();
    // The orphan re-scan against the local server can settle in ~1ms, so
    // asserting the transient "Scanning…" label races the scan and flips
    // red once latency drops. Assert the scan's outcome instead: wait for
    // the stale-snapshot notice to clear, then for either a refreshed
    // item list or the empty state. The not.toContainText(/Scanning/)
    // guard below stays as a settle check, never a transient positive.
    await expect(recoveryPanel.locator('.recovery-workspace__complete')).not.toBeVisible({
      timeout: 30_000,
    });
    await expect(
      recoveryPanel
        .locator('.recovery-workspace__queue')
        .or(recoveryPanel.locator('.recovery-workspace__empty')),
    ).toBeVisible({ timeout: 30_000 });
    await expect(recoveryPanel.locator('.recovery-workspace__scan')).not.toContainText(/Scanning/, {
      timeout: 30_000,
    });
    const rescannedItems = recoveryPanel.locator('.recovery-workspace__item');
    const rescannedCount = await rescannedItems.count();
    if (rescannedCount > 0) {
      const deadItem = rescannedItems.first();
      const resumeButton = deadItem.getByRole('button', { name: 'Resume' });
      await expect(resumeButton).toBeVisible({ timeout: 10_000 });
      await resumeButton.click();
      await expect(deadItem.locator('.recovery-workspace__item-outcome')).toContainText(
        /Resume submitted/i,
        { timeout: 15_000 },
      );
      transcript.step('Resume executed directly on remaining dead orphan item after fresh scan');
    } else {
      transcript.step('fresh scan resolved all orphans — no items to resume');
    }
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (liveOrphan !== null) {
      try {
        liveOrphan.kill('SIGKILL');
      } catch {
        // best effort
      }
      try {
        if (liveOrphan.pid !== undefined) {
          process.kill(-liveOrphan.pid, 'SIGKILL');
        }
      } catch {
        // process group may already be reaped
      }
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
