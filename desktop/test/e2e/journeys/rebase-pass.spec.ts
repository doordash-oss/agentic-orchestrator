/**
 * Rebase pass journey: aftercare → one-click "Start rebase pass" → kind-aware
 * pass workspace (no modal) → completion → pass history + parent returned to
 * aftercare. A second branch covers the up-to-date case where the server
 * returns `rebase_already_up_to_date` and the cockpit stays in aftercare.
 *
 * The behind case seeds a Published feature whose repo is genuinely behind its
 * target by advancing the local `main` branch after feature creation (the same
 * seeding strategy the local-merge fixture uses). The rebase-provider stub
 * resolves the merge during the child's implement phase and approves the
 * review, so the full child lifecycle runs against the real bundled server
 * without network or real provider CLIs.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type TestInfo } from '@playwright/test';
import { closeApp, createFeatureViaForm, launchApp, type AppHandle } from '../helpers/app';
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
import { setFeatureStatus } from '../helpers/seed';
import {
  featureYamlPath,
  parseFeatureRepoSources,
  replaceTopLevelBlock,
  setRepoBaseBranch,
  setRepoPublishable,
} from '../helpers/completionFixture';

/**
 * Advances the local `main` branch on the source repo so the feature branch
 * is behind its target. Also sets `base_branch: main` and `publishable: false`
 * in the feature manifest so the server's rebase preflight resolves the target
 * to `main` and uses `IsBehindLocal` (not `IsBehindRemote`).
 */
function seedBehindFeature(world: ReturnType<typeof createWorld>, featureId: string): void {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const sources = parseFeatureRepoSources(featureYaml);
  const alphaRepo = sources['alpha'];
  if (alphaRepo === undefined) {
    throw new Error('feature.yaml missing repo alpha');
  }
  featureYaml = setRepoPublishable(featureYaml, 'alpha', false);
  featureYaml = setRepoBaseBranch(featureYaml, 'alpha', 'main');
  featureYaml = replaceTopLevelBlock(featureYaml, 'checkpoints', [
    'checkpoints:',
    '    inquiry_review: false',
    '    roadmap_review: false',
    '    phase_plan_review: false',
  ]);
  fs.writeFileSync(featurePath, featureYaml);

  git(alphaRepo, 'checkout', 'main');
  fs.writeFileSync(path.join(alphaRepo, 'README.md'), '# alpha\nRemote advance for rebase\n');
  git(alphaRepo, 'add', '.');
  git(alphaRepo, 'commit', '-m', 'Remote advance for rebase pass journey');
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
  createRepo(world, 'alpha', { commit: true });
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
    const homeTab = handle.page.getByRole('tab', { name: 'Home' });
    await expect(homeTab).toBeVisible({ timeout: 60_000 });
    await homeTab.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded Published state with behind repo');

    transcript.section('Open feature cockpit and click "Start rebase pass"');
    const featureTab = handle.page.getByRole('tab', { name: featureName });
    await featureTab.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const startRebase = aftercare.getByRole('button', { name: /Start rebase pass/ });
    await expect(startRebase).toBeVisible();
    await startRebase.click();
    transcript.step('clicked "Start rebase pass" in aftercare');

    transcript.section('Assert pass workspace appears with Rebase labels, no dialog');
    await expect(handle.page.getByRole('dialog', { name: 'Rebase' })).not.toBeVisible({
      timeout: 5_000,
    });
    const pass = seededCockpit.getByRole('region', { name: 'Rebase pass' });
    await expect(pass).toBeVisible({ timeout: 60_000 });
    transcript.step('rebase pass workspace visible with kind-aware labeling, no modal');

    await expect(seededCockpit.getByText('Rebasing')).toBeVisible({ timeout: 30_000 });
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
    transcript.step(
      'rebase child completed; parent returned to aftercare with rebase pass in history',
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
    const homeTab = handle.page.getByRole('tab', { name: 'Home' });
    await expect(homeTab).toBeVisible({ timeout: 60_000 });
    await homeTab.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded Published state (up to date)');

    transcript.section('Open feature cockpit and click "Start rebase pass"');
    const featureTab = handle.page.getByRole('tab', { name: featureName });
    await featureTab.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const startRebase = aftercare.getByRole('button', { name: /Start rebase pass/ });
    await expect(startRebase).toBeVisible();
    await startRebase.click();
    transcript.step('clicked "Start rebase pass" on an up-to-date feature');

    transcript.section('Assert inline already-up-to-date notice, cockpit stays in aftercare');
    const errorAlert = seededCockpit.getByRole('alert');
    await expect(errorAlert).toBeVisible({ timeout: 30_000 });
    await expect(errorAlert).toContainText('rebase_already_up_to_date');
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
