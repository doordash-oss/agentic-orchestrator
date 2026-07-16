/**
 * Journeys 5 + 2 — partial setup failure, retry on the SAME feature, then
 * restart persistence, all against the packaged app and real bundled server:
 *
 * two discovered repos → the second becomes unusable after discovery but
 * before setup dispatch (the ref behind HEAD is deleted, so worktree
 * creation fails while discovery still sees a git repository) →
 * cockpit shows completed + failed tasks with a safe error → repo fixed
 * externally → Retry re-runs only unfinished work on the same feature id →
 * Ready to start → app relaunch against the same state dir restores the tab
 * and shows the authoritative Ready to start again, with nothing started.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShotBothThemes,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  git,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('partial setup failure, retry on the same feature, restart persistence', async ({}, testInfo) => {
  const transcript = new Transcript(
    'failure-retry-restart',
    'Journeys 5 + 2 — setup failure → retry (same feature) → restart persistence',
  );
  const world = createWorld('failure-retry', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  const beta = createRepo(world, 'beta', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(
    `two committed repositories discovered from the preset workspace root: \`${alpha}\`, \`${beta}\``,
  );

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch (already-ready runtime goes straight to the workspace)');
    handle = await launchApp(world, testInfo, { traceName: 'failure-retry-restart' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const mainRef = path.join(beta, '.git', 'refs', 'heads', 'main');

    transcript.section('Create the feature on both repos; setup fails partially');
    const cockpit = await createFeatureViaForm(handle, {
      name: 'Two Repo Feature',
      repoPatterns: [/alpha/, /beta/],
      beforeSubmit: () => {
        transcript.section('Invalidate the second repo after discovery, before dispatch');
        // Deleting the ref behind HEAD leaves the repository discoverable
        // (IsGitRepo passes) and creation valid (HEAD still symrefs main), but
        // strips every commit — so exactly the second worktree task fails with
        // the engine's safe "no commits yet" error.
        fs.rmSync(mainRef, { force: true });
        fs.rmSync(path.join(beta, '.git', 'packed-refs'), { force: true });
        transcript.step(`deleted \`${mainRef}\` (and packed-refs): beta now has an unborn HEAD`);
      },
    });
    await expect(cockpit.getByText('setup failed')).toBeVisible({ timeout: 60_000 });
    const alphaTask = cockpit.locator('.task-row', { hasText: 'Worktree: alpha' });
    const betaTask = cockpit.locator('.task-row', { hasText: 'Worktree: beta' });
    await expect(alphaTask).toContainText('Done');
    await expect(betaTask).toContainText('Failed');
    await expect(cockpit.getByText('1 of 2 tasks complete')).toBeVisible();
    const retryButton = cockpit.getByRole('button', { name: 'Retry setup' });
    await expect(retryButton).toBeEnabled();
    await evidenceShotBothThemes(handle, 'setup-failure-retry');

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    expect(features).toHaveLength(1);
    const featureId = features[0]!.id;
    const failed = await handle.page.evaluate((id) => window.agentico.getFeature(id), featureId);
    transcript.json('authoritative snapshot after the partial failure', failed);
    expect(failed.setup?.status).toBe('failed');
    const failedTask = failed.setup?.tasks.find((task) => task.repo === 'beta');
    expect(failedTask?.status).toBe('failed');
    expect(failedTask?.error).toBeTruthy();
    const doneTask = failed.setup?.tasks.find((task) => task.repo === 'alpha');
    expect(doneTask?.status).toBe('done');

    transcript.section('Fix the repo externally, then Retry the SAME feature');
    const fixOut = git(beta, 'commit', '--allow-empty', '-m', 'Restore main');
    transcript.command(`git -C ${beta} commit --allow-empty -m "Restore main"`, fixOut);
    await retryButton.click();
    await expect(cockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    await expect(cockpit.getByText('2 of 2 tasks complete')).toBeVisible();
    await expect(cockpit.getByText('(attempt 2)')).toBeVisible();
    await expect(cockpit.getByRole('button', { name: 'Start' })).toBeVisible();
    await expect(cockpit.getByRole('button', { name: 'Start' })).toBeEnabled();
    await expect(cockpit.getByText("Starting isn't available in this version yet.")).toHaveCount(0);

    const afterRetry = await handle.page.evaluate(
      (id) => window.agentico.getFeature(id),
      featureId,
    );
    expect(afterRetry.id).toBe(featureId); // same feature — never a duplicate create
    expect(afterRetry.setup?.status).toBe('done');
    expect(afterRetry.setup?.attempt).toBe(2);
    const list = await handle.page.evaluate(() => window.agentico.listFeatures());
    expect(list).toHaveLength(1);
    transcript.json('authoritative snapshot after retry (same id, attempt 2)', afterRetry);

    transcript.section('Quit; the app-owned server is reaped');
    const firstDiscovery = readDiscovery(world);
    expect(firstDiscovery).not.toBeNull();
    persistAppLogs(handle, 'failure-retry-first-run');
    await closeApp(handle);
    await waitFor(
      () => !processAlive(firstDiscovery!.pid),
      `first app-owned server ${firstDiscovery!.pid} to be reaped`,
      15_000,
    );
    transcript.step(`first app-owned server pid ${firstDiscovery!.pid} reaped after quit`);
    assertNoLeakedProcesses(world);

    transcript.section('Journey 2 — relaunch against the same state dir');
    handle = await launchApp(world, testInfo, { traceName: 'failure-retry-restart-relaunch' });
    // The persisted tab (identity + title hint only) restores; the feature
    // itself is reloaded from the server, not from any local cache.
    const restoredCockpit = handle.page.getByLabel('Feature Two Repo Feature');
    await expect(restoredCockpit).toBeVisible({ timeout: 60_000 });
    await expect(restoredCockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    await expect(restoredCockpit.getByText('2 of 2 tasks complete')).toBeVisible();
    await expect(restoredCockpit.getByRole('button', { name: 'Start' })).toBeVisible();
    await expect(restoredCockpit.getByRole('button', { name: 'Start' })).toBeEnabled();
    await expect(
      restoredCockpit.getByText("Starting isn't available in this version yet."),
    ).toHaveCount(0);

    const restored = await handle.page.evaluate((id) => window.agentico.getFeature(id), featureId);
    expect(restored.id).toBe(featureId);
    expect(restored.setup?.status).toBe('done');
    expect(restored.setup?.attempt).toBe(2);
    const settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.tabs.open.map((tab) => tab.featureId)).toEqual([featureId]);
    expect(settings.tabs.activeFeatureId).toBe(featureId);
    transcript.json('restored tab prefs (identity + presentation only)', settings.tabs);
    transcript.step(
      'relaunch restored the feature tab and reloaded the authoritative Ready-to-start state from the server',
    );

    // Still nothing started after failure, retry, and restart.
    expect(findSessionEntries(world.stateDir)).toEqual([]);
    transcript.step('no session material exists under the state dir — orchestration never began');

    const secondDiscovery = readDiscovery(world);
    persistAppLogs(handle, 'failure-retry-second-run');
    await closeApp(handle);
    if (secondDiscovery !== null) {
      await waitFor(
        () => !processAlive(secondDiscovery.pid),
        `second app-owned server ${secondDiscovery.pid} to be reaped`,
        15_000,
      );
    }
    assertNoLeakedProcesses(world);
    transcript.step('second app instance quit cleanly; no leaked processes');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function findSessionEntries(root: string): string[] {
  const matches: string[] = [];
  const walk = (dir: string): void => {
    let entries: fs.Dirent[];
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (/session/i.test(entry.name)) {
        matches.push(full);
      }
      if (entry.isDirectory()) {
        walk(full);
      }
    }
  };
  walk(root);
  return matches;
}
