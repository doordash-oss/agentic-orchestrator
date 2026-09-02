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
import { setFeatureStatus } from '../helpers/seed';
import { replaceTopLevelBlock, upsertYamlScalar } from '../helpers/yaml';
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
    // The durable failure renders once through the full error surface: the
    // alert role, the "Failed" class label, the caption naming the owning
    // task, the canonical code tag, and the catalog title — exactly once on
    // the page.
    const failureCard = cockpit.getByRole('alert');
    await expect(failureCard).toBeVisible({ timeout: 60_000 });
    await expect(failureCard.locator('.error-surface__label')).toHaveText('Failed');
    await expect(failureCard.locator('.error-surface__caption')).toHaveText('Worktree: beta');
    await expect(failureCard.locator('.error-surface__code')).toHaveText('worktree_setup_failed');
    await expect(failureCard.locator('.error-surface__title')).toHaveText('Worktree setup failed');
    await expect(handle.page.getByText('Worktree setup failed')).toHaveCount(1);
    // The owning repository sits under the Details disclosure.
    const details = failureCard.locator('details.error-surface__details');
    await details.locator('summary').click();
    await expect(details.getByText('beta')).toBeVisible();
    // Raw diagnostics sit behind the Diagnostics disclosure.
    const diagnostics = failureCard.locator('details.error-surface__diagnostics');
    await expect(diagnostics).toBeVisible();
    await diagnostics.locator('summary').click();
    await expect(diagnostics.getByText('no commits yet')).toBeVisible();
    transcript.step(
      'the failed card renders the owning task once — caption, code tag, catalog title, repository under Details, raw diagnostics behind the Diagnostics disclosure',
    );
    const retryButton = failureCard.getByRole('button', { name: 'Retry setup' });
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
    // The owning task carries the full canonical record: code and the raw
    // diagnostics.
    expect(failedTask?.error?.code).toBe('worktree_setup_failed');
    expect(failedTask?.error?.diagnostics).toContain('no commits yet');
    // The run's thin record carries the same code, a setup_task block naming
    // the owning task, and no diagnostics.
    expect(failed.failure?.code).toBe('worktree_setup_failed');
    expect(failed.failure?.context?.setup_task?.key).toBe('worktree:beta');
    expect(failed.failure?.diagnostics).toBeUndefined();
    const doneTask = failed.setup?.tasks.find((task) => task.repo === 'alpha');
    expect(doneTask?.status).toBe('done');

    transcript.section('Fix the repo externally, then Retry the SAME feature');
    const fixOut = git(beta, 'commit', '--allow-empty', '-m', 'Restore main');
    transcript.command(`git -C ${beta} commit --allow-empty -m "Restore main"`, fixOut);
    await retryButton.click();
    await expect(cockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled();
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
    // The persisted shell selection (identity only) restores; the feature
    // itself is reloaded from the server, not from any local cache.
    const restoredCockpit = handle.page.getByLabel('Feature Two Repo Feature');
    await expect(restoredCockpit).toBeVisible({ timeout: 60_000 });
    await expect(restoredCockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Start', exact: true })).toBeEnabled();
    await expect(
      restoredCockpit.getByText("Starting isn't available in this version yet."),
    ).toHaveCount(0);

    const restored = await handle.page.evaluate((id) => window.agentico.getFeature(id), featureId);
    expect(restored.id).toBe(featureId);
    expect(restored.setup?.status).toBe('done');
    expect(restored.setup?.attempt).toBe(2);
    const [settings, restoredState] = await handle.page.evaluate(() =>
      Promise.all([window.agentico.getSettings(), window.agentico.getConnectionStatus()]),
    );
    const restoredKey = restoredState.serverKey ?? null;
    expect(
      restoredKey === null ? null : (settings.shell.featureByServer[restoredKey] ?? null),
    ).toBe(featureId);
    transcript.json('restored shell prefs (identity only)', settings.shell);
    transcript.step(
      'relaunch restored the selected feature and reloaded the authoritative Ready-to-start state from the server',
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

test('iteration-budget failure restart extends the budget and clears the card', async ({}, testInfo) => {
  const transcript = new Transcript(
    'failure-retry-restart-budget',
    'Iteration-budget failure → card Restart → budget-extension dialog → extended restart',
  );
  const world = createWorld('failure-budget', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`committed repository discovered from the preset workspace root: \`${alpha}\``);

  let handle: AppHandle | null = null;
  try {
    transcript.section('Create the feature; setup completes');
    handle = await launchApp(world, testInfo, { traceName: 'failure-budget-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const featureName = 'IterBudget Feature';
    await createFeatureViaForm(handle, {
      name: featureName,
      repoPatterns: [/alpha/],
    });
    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    expect(features).toHaveLength(1);
    const featureId = features[0]!.id;
    await expect(handle.page.getByLabel(`Feature ${featureName}`)).toBeVisible();
    persistAppLogs(handle, 'failure-budget-first-run');
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
    transcript.step(
      'seeded Failed@implement with an iteration_budget_exhausted record and max_iterations 5',
    );

    transcript.section('Relaunch; the card offers Restart with the budget-extension dialog');
    handle = await launchApp(world, testInfo, { traceName: 'failure-budget-relaunch' });
    const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 60_000 });

    const failureCard = cockpit.getByRole('alert');
    await expect(failureCard).toBeVisible({ timeout: 60_000 });
    await expect(failureCard.locator('.error-surface__code')).toHaveText(
      'iteration_budget_exhausted',
    );
    await expect(failureCard.locator('.error-surface__title')).toHaveText(
      'Iteration budget exhausted',
    );
    const restartButton = failureCard.getByRole('button', { name: 'Restart' });
    await expect(restartButton).toBeEnabled();
    await evidenceShotBothThemes(handle, 'iteration-budget-failure');

    await restartButton.click();
    const dialog = handle.page.getByRole('dialog', { name: `Restart ${featureName}?` });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('maximum-iteration restart');
    transcript.step(
      'the card Restart opened the confirmation dialog with the budget-extension copy',
    );

    await dialog.getByRole('button', { name: 'Confirm restart' }).click();
    const afterRestart = await handle.page.evaluate(
      (id) => window.agentico.getFeature(id),
      featureId,
    );
    expect(afterRestart.status).not.toBe('Failed');
    expect(afterRestart.failure).toBeUndefined();
    // The durable card is gone; an ephemeral action rejection may remain as
    // the pre-existing compact surface, so scope to the full variant.
    await expect(cockpit.locator('.error-surface--full')).toHaveCount(0);
    transcript.json('authoritative snapshot after the budget-extension restart', afterRestart);
    transcript.step('the feature left Failed with the failure card gone');

    const seededBudget = fs.readFileSync(featurePath, 'utf8');
    expect(seededBudget).toContain('max_iterations: 15');
    transcript.step('the restart extended max_iterations from 5 to 15');

    persistAppLogs(handle, 'failure-budget-second-run');
    await closeApp(handle);
    handle = null;
    assertNoLeakedProcesses(world);
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
