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
 * Rebase pass journey: aftercare → one-click "Start rebase pass" → kind-aware
 * pass workspace (no modal) → completion → pass history + parent returned to
 * aftercare. A second branch covers the up-to-date case where the server
 * returns `rebase_already_up_to_date` and the cockpit stays in aftercare.
 *
 * The behind case seeds a Published feature whose publishable repo is genuinely
 * behind its target by advancing `origin/main` after feature creation. Its
 * existing pull-request branch stays at the pre-rebase parent tip, so child
 * completion must refresh the aftercare preflight and reveal Publish updates
 * without closing the tab. The rebase-provider stub resolves the merge during
 * the child's implement phase and approves the review, so the full child
 * lifecycle runs against the real bundled server without network or real
 * provider CLIs.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type TestInfo } from '@playwright/test';
import { closeApp, createFeatureViaForm, launchApp, type AppHandle } from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  addBareRemote,
  createRepo,
  createWorld,
  destroyWorld,
  git,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';
import { setFeatureStatus } from '../helpers/seed';
import {
  activeRunYamlPath,
  featureYamlPath,
  parseFeatureRepos,
  parseFeatureRepoSources,
  replaceTopLevelBlock,
  setRepoBaseBranch,
  setRepoPublishable,
} from '../helpers/completionFixture';

/**
 * Publishes the current feature tip as the existing pull-request branch, then
 * advances `origin/main` so the feature branch is behind its remote target.
 * Manual publish keeps the post-rebase parent work local until the user chooses
 * Publish updates, and the seeded PR URL makes the preflight classify it as a
 * fast-forward update to an existing pull request rather than a first publication.
 */
function seedBehindFeature(world: ReturnType<typeof createWorld>, featureId: string): void {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const sources = parseFeatureRepoSources(featureYaml);
  const worktrees = parseFeatureRepos(featureYaml);
  const alphaRepo = sources['alpha'];
  const alphaWorktree = worktrees['alpha'];
  if (alphaRepo === undefined) {
    throw new Error('feature.yaml missing repo alpha');
  }
  if (alphaWorktree === undefined) {
    throw new Error('feature.yaml missing worktree alpha');
  }
  featureYaml = setRepoPublishable(featureYaml, 'alpha', true);
  featureYaml = setRepoBaseBranch(featureYaml, 'alpha', 'main');
  featureYaml = replaceTopLevelBlock(featureYaml, 'checkpoints', [
    'checkpoints:',
    '    inquiry_review: false',
    '    roadmap_review: false',
    '    phase_plan_review: false',
    '    manual_publish: true',
  ]);
  fs.writeFileSync(featurePath, featureYaml);

  let runYaml = fs.readFileSync(activeRunYamlPath(world, featureId), 'utf8');
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    '    alpha:',
    '        touched: true',
    '        pr_url: https://github.example/agentico/alpha/pull/1',
  ]);
  fs.writeFileSync(activeRunYamlPath(world, featureId), runYaml);

  git(alphaWorktree, 'push', '-u', 'origin', 'HEAD');

  git(alphaRepo, 'checkout', 'main');
  fs.writeFileSync(path.join(alphaRepo, 'README.md'), '# alpha\nRemote advance for rebase\n');
  git(alphaRepo, 'add', '.');
  git(alphaRepo, 'commit', '-m', 'Remote advance for rebase pass journey');
  git(alphaRepo, 'push', 'origin', 'main');
  git(alphaRepo, 'checkout', '-');
}

test('rebase pass: behind feature → card click → pass workspace → completion → history', async ({}, testInfo: TestInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript('rebase-pass', 'Rebase pass journey');
  const world = createWorld('rebase-pass', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    rebaseProvider: true,
  });
  const alphaRepo = createRepo(world, 'alpha', { commit: true });
  addBareRemote(world, alphaRepo);
  git(alphaRepo, 'push', '-u', 'origin', 'main');
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step('committed repository: alpha');

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch');
    handle = await launchApp(world, testInfo, { traceName: 'rebase-pass' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create feature through the UI form and open cockpit');
    const featureName = `RebasePass${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'rebase pass journey',
      repoPatterns: [/alpha/],
      waitForReady: true,
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    transcript.step(`created feature \`${featureName}\` through the form; cockpit visible`);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published, advance main, relaunch');
    const discovery = readDiscovery(world);
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `first app-owned server ${discovery.pid} to be reaped`,
        15_000,
      );
    }

    setFeatureStatus(world.stateDir, featureId, 'Published');
    transcript.step('seeded feature to Published status');

    seedBehindFeature(world, featureId);
    transcript.step('advanced local main so the feature is behind its target');

    handle = await launchApp(world, testInfo, { traceName: 'rebase-pass-seeded' });
    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await expect(overviewOption).toBeVisible({ timeout: 60_000 });
    await overviewOption.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded Published state with behind repo');

    transcript.section('Open feature cockpit and click "Start rebase pass"');
    const featureOption = handle.page.getByRole('option', { name: featureName });
    await featureOption.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const startRebase = aftercare.getByRole('button', { name: /Start rebase pass/ });
    await expect(startRebase).toBeVisible();
    await expect(aftercare.getByRole('button', { name: /Publish new commits/ })).toHaveCount(0);
    await startRebase.click();
    transcript.step('clicked "Start rebase pass" in aftercare');

    transcript.section('Assert pass workspace appears with Rebase labels, no dialog');
    await expect(handle.page.getByRole('dialog', { name: 'Rebase' })).not.toBeVisible({
      timeout: 5_000,
    });
    const pass = seededCockpit.getByRole('region', { name: 'Rebase pass' });
    await expect(pass).toBeVisible({ timeout: 60_000 });
    transcript.step('rebase pass workspace visible with kind-aware labeling, no modal');

    await expect(handle.page.getByText('Rebasing')).toBeVisible({ timeout: 30_000 });
    transcript.step('status chip reads "Rebasing" while the pass is active');

    transcript.section('Wait for the rebase pass to reach a terminal state');
    await waitFor(
      async () => {
        const feature = await handle!.page.evaluate(
          (id: string) => window.agentico.getFeature(id),
          featureId,
        );
        return feature.activeChild === undefined;
      },
      `feature ${featureId} rebase child to complete (Failed is not accepted)`,
      180_000,
    );
    const finalSnapshot = await handle.page.evaluate(
      (id: string) => window.agentico.getFeature(id),
      featureId,
    );
    expect(finalSnapshot.activeChild).toBeUndefined();
    const hasRebase = (finalSnapshot.childHistory ?? []).some((child) => child.kind === 'rebase');
    expect(hasRebase, 'rebase pass should appear in child history after completion').toBe(true);
    await expect(aftercare).toBeVisible({ timeout: 30_000 });
    const publishUpdates = aftercare.getByRole('button', { name: /Publish new commits/ });
    await expect(publishUpdates).toBeVisible({ timeout: 30_000 });
    await expect(publishUpdates).toContainText('Publish updates');
    transcript.step(
      'rebase child completed; parent returned to aftercare with Publish updates and pass history',
    );
    transcript.json('final feature status', finalSnapshot.status);
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});

test('rebase pass up-to-date: card click renders inline notice, stays in aftercare', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('rebase-pass-up-to-date', 'Rebase pass up-to-date journey');
  const world = createWorld('rebase-pass-uptodate', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    rebaseProvider: true,
  });
  createRepo(world, 'alpha', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step('committed repository: alpha (not advanced)');

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch');
    handle = await launchApp(world, testInfo, { traceName: 'rebase-pass-uptodate' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create feature through the UI form and open cockpit');
    const featureName = `RebaseUptoDate${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'rebase pass up-to-date journey',
      repoPatterns: [/alpha/],
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    transcript.step(`created feature \`${featureName}\` through the form; cockpit visible`);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published (repo not advanced), relaunch');
    const discovery = readDiscovery(world);
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `first app-owned server ${discovery.pid} to be reaped`,
        15_000,
      );
    }

    setFeatureStatus(world.stateDir, featureId, 'Published');
    transcript.step('seeded feature to Published status (repo is up to date)');

    handle = await launchApp(world, testInfo, { traceName: 'rebase-pass-uptodate-seeded' });
    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await expect(overviewOption).toBeVisible({ timeout: 60_000 });
    await overviewOption.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded Published state (up to date)');

    transcript.section('Open feature cockpit and click "Start rebase pass"');
    const featureOption = handle.page.getByRole('option', { name: featureName });
    await featureOption.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const startRebase = aftercare.getByRole('button', { name: /Start rebase pass/ });
    await expect(startRebase).toBeVisible();
    await startRebase.click();
    transcript.step('clicked "Start rebase pass" on an up-to-date feature');

    transcript.section('Assert inline already-up-to-date notice, cockpit stays in aftercare');
    // The catalog renders the already-up-to-date rejection as a warning
    // surface, so it reads as a status — not an alert. Scope past the
    // cockpit's other live statuses by the stable code tag.
    const errorStatus = seededCockpit
      .getByRole('status')
      .filter({ hasText: 'rebase_already_up_to_date' });
    await expect(errorStatus).toBeVisible({ timeout: 30_000 });
    await expect(errorStatus).toContainText('rebase_already_up_to_date');
    transcript.step('inline already-up-to-date notice rendered near the aftercare surface');

    await expect(aftercare).toBeVisible({ timeout: 5_000 });
    await expect(seededCockpit.getByRole('region', { name: 'Rebase pass' })).not.toBeVisible({
      timeout: 5_000,
    });
    transcript.step('cockpit stayed in aftercare; no pass workspace was mounted');

    await expect(startRebase).toBeEnabled({ timeout: 5_000 });
    transcript.step('card remains enabled for another click');
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
