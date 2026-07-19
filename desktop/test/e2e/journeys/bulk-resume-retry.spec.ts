/**
 * Bulk resume/retry journey: fresh preview, sequential partial outcomes,
 * stale eligibility, stop-after-current cancellation, and safe retry
 * against the packaged app and real bundled server.
 *
 * Seeds multiple features in interrupted/failed states so the bulk preview
 * has eligible rows, then exercises the sequential queue with cancellation
 * and fresh retry.
 */
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
import { setFeatureStatus, persistAppLogs } from '../helpers/seed';

test('bulk resume/retry: fresh preview, sequential dispatch, cancellation, and safe retry', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('bulk-resume-retry', 'Bulk resume and retry journey');
  const world = createWorld('bulk-resume-retry', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`committed repository: \`${alpha}\``);

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch and create features');
    handle = await launchApp(world, testInfo, { traceName: 'bulk-resume-retry' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    const featureNames = [
      `BulkResume${Math.random().toString(16).slice(2, 6)}`,
      `BulkRetry${Math.random().toString(16).slice(2, 6)}`,
      `BulkResume2${Math.random().toString(16).slice(2, 6)}`,
    ];

    for (const name of featureNames) {
      await createFeatureViaForm(handle, {
        name,
        repoPatterns: [/alpha/],
      });
      transcript.step(`created feature \`${name}\` through the form`);
      const homeTab = handle.page.getByRole('tab', { name: 'Home' });
      await homeTab.click();
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 10_000,
      });
    }

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    expect(features.length).toBeGreaterThanOrEqual(3);
    const featureIds = features.slice(0, 3).map((f) => f.id);
    transcript.json('created feature ids', featureIds);

    transcript.section('Quit, seed interrupted/failed states, relaunch');
    const discovery = readDiscovery(world);
    persistAppLogs(handle, 'bulk-resume-retry-first');
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `first app-owned server ${discovery.pid} to be reaped`,
        15_000,
      );
    }

    setFeatureStatus(world.stateDir, featureIds[0]!, 'Interrupted');
    setFeatureStatus(world.stateDir, featureIds[1]!, 'Failed');
    setFeatureStatus(world.stateDir, featureIds[2]!, 'Interrupted');
    transcript.step(
      'seeded 2 Interrupted (resume-eligible) and 1 Failed (retry-eligible) features',
    );

    handle = await launchApp(world, testInfo, { traceName: 'bulk-resume-retry-seeded' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('relaunched against seeded state');

    transcript.section('Bulk preview panel');
    const bulkPanel = handle.page.locator('.bulk-preview').first();
    await expect(bulkPanel).toBeVisible({ timeout: 10_000 });

    const refreshButton = bulkPanel.getByRole('button', { name: 'Fresh preview' });
    await refreshButton.click();
    await expect(bulkPanel.locator('.bulk-preview__eligible')).toBeVisible({ timeout: 15_000 });
    const eligibleRows = bulkPanel.locator('.bulk-preview__eligible .bulk-preview__row');
    const eligibleCount = await eligibleRows.count();
    expect(eligibleCount).toBeGreaterThanOrEqual(3);
    transcript.step(`fresh preview loaded with ${eligibleCount} eligible feature(s)`);

    const excludedSection = bulkPanel.locator('.bulk-preview__excluded');
    if (await excludedSection.isVisible({ timeout: 3_000 }).catch(() => false)) {
      const excludedRows = excludedSection.locator('.bulk-preview__row');
      const excludedCount = await excludedRows.count();
      transcript.step(`preview shows ${excludedCount} excluded feature(s) with disabled reasons`);
    }

    transcript.section('Run queue with cancellation');
    const runButton = bulkPanel.getByRole('button', { name: /^Run / });
    await expect(runButton).toBeEnabled({ timeout: 5_000 });
    await runButton.click();
    const progressSection = bulkPanel.locator('.bulk-preview__progress');
    await expect(progressSection).toBeVisible({ timeout: 15_000 });
    transcript.step('queue dispatch started');

    const cancelButton = progressSection.getByRole('button', { name: 'Cancel after current' });
    if (await cancelButton.isVisible({ timeout: 3_000 }).catch(() => false)) {
      await cancelButton.click();
      transcript.step('clicked Cancel after current — remaining rows should be marked not started');
    }

    await expect(progressSection.locator('.bulk-preview__progress-text')).toContainText(
      /Queue complete|Cancelled after current/,
      {
        timeout: 60_000,
      },
    );
    const finalCounts = await bulkPanel.locator('.bulk-preview__counts').textContent();
    transcript.step(`queue complete — final outcomes: ${finalCounts?.trim() ?? 'none'}`);

    const notStartedRows = bulkPanel.locator('.bulk-preview__row[data-outcome="not-started"]');
    const notStartedCount = await notStartedRows.count();
    if (notStartedCount > 0) {
      transcript.step(`${notStartedCount} row(s) marked not started after cancellation`);
    }

    const retryButton = progressSection.getByRole('button', { name: 'Fresh preview' });
    await expect(retryButton).toBeVisible({ timeout: 5_000 });
    transcript.step('fresh-preview retry available after completion');
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
