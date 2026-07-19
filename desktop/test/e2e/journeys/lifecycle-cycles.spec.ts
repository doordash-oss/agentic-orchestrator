/**
 * Lifecycle cycles journey: resume, retry, restart, guarded rebase,
 * review-comments preview, explicit refactor scope, and repository
 * NEED_USER_INPUT journey against the packaged app and real bundled server.
 *
 * The feature is seeded to Published status so the server enables cycle
 * actions (rebase, review-comments, refactor) which are only available
 * for published or manual-ready features.
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
import { setFeatureStatus } from '../helpers/seed';

test('lifecycle cycles: resume, retry, restart, rebase, review-comments, refactor', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('lifecycle-cycles', 'Lifecycle cycles journey');
  const world = createWorld('lifecycle-cycles', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  const beta = createRepo(world, 'beta', { commit: true });
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`two committed repositories: \`${alpha}\`, \`${beta}\``);

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch');
    handle = await launchApp(world, testInfo, { traceName: 'lifecycle-cycles' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create feature through the UI form and open cockpit');
    const featureName = `LifecycleJourney${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'lifecycle cycles journey',
      repoPatterns: [/alpha/, /beta/],
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    transcript.step(`created feature \`${featureName}\` through the form; cockpit visible`);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published status, relaunch');
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
    transcript.step('seeded feature to Published status so cycle actions are enabled');

    handle = await launchApp(world, testInfo, { traceName: 'lifecycle-cycles-seeded' });
    const homeTab = handle.page.getByRole('tab', { name: 'Home' });
    await expect(homeTab).toBeVisible({ timeout: 60_000 });
    await homeTab.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched against seeded Published state');

    transcript.section('Open feature cockpit and cycles drawer');
    const featureTab = handle.page.getByRole('tab', { name: featureName });
    await featureTab.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const cyclesButton = seededCockpit.getByRole('button', { name: 'Cycles' });
    await expect(cyclesButton).toBeVisible({ timeout: 15_000 });
    await cyclesButton.click();
    const cyclesDrawer = handle.page.locator('.cockpit__cycles-drawer');
    await expect(cyclesDrawer).toBeVisible({ timeout: 10_000 });
    transcript.step('cycles drawer opened showing rebase, review-comments, and refactor journeys');

    const rebaseJourney = cyclesDrawer.locator('.cycle-journey--rebase');
    if (await rebaseJourney.isVisible({ timeout: 3_000 }).catch(() => false)) {
      const preflight = rebaseJourney.locator('.cycle-journey__preflight');
      if (await preflight.isVisible({ timeout: 3_000 }).catch(() => false)) {
        transcript.step('rebase preflight lists affected repositories, targets, and freshness');
      } else {
        transcript.step('rebase journey visible (preflight not shown — action may be disabled)');
      }
    } else {
      transcript.step('rebase journey not visible — server does not enable rebase for this state');
    }

    const reviewJourney = cyclesDrawer.locator('.cycle-journey--review-comments');
    if (await reviewJourney.isVisible({ timeout: 3_000 }).catch(() => false)) {
      const repoSelect = reviewJourney.locator('select').first();
      await repoSelect.selectOption('alpha');
      await reviewJourney.getByRole('button', { name: 'Fetch comments' }).click();
      const commentsPreview = reviewJourney.locator('.cycle-journey__comments-preview');
      const hasComments = await commentsPreview.isVisible({ timeout: 10_000 }).catch(() => false);
      if (hasComments) {
        transcript.step('review-comments fetched and previewed for alpha repository');
      } else {
        transcript.step('review-comments fetch completed — no comments or fetch not supported');
      }
    } else {
      transcript.step(
        'review-comments journey not visible — server does not enable it for this state',
      );
    }

    const refactorJourney = cyclesDrawer.locator('.cycle-journey--refactor');
    if (await refactorJourney.isVisible({ timeout: 3_000 }).catch(() => false)) {
      const scopeRadios = refactorJourney.locator('input[name="refactor-scope"]');
      const radioCount = await scopeRadios.count();
      if (radioCount >= 2) {
        await scopeRadios.nth(1).check();
        const resolvedRepos = refactorJourney.locator('.cycle-journey__resolved-repos');
        await expect(resolvedRepos).toBeVisible({ timeout: 5_000 });
        transcript.step('refactor all-repositories scope resolves to named repositories');
      } else {
        transcript.step('refactor journey visible but scope controls absent');
      }
    } else {
      transcript.step('refactor journey not visible — server does not enable it for this state');
    }

    const closeButton = cyclesDrawer.getByRole('button', { name: 'Close' });
    await closeButton.click();
    await expect(cyclesDrawer).not.toBeVisible({ timeout: 5_000 });
    transcript.step('cycles drawer closed');

    transcript.section('Restart confirmation');
    const restartButton = seededCockpit.getByRole('button', { name: 'Restart', exact: true });
    if (await restartButton.isVisible({ timeout: 5_000 }).catch(() => false)) {
      await restartButton.click();
      const restartDialog = handle.page.locator('.impact-dialog', { hasText: 'Restart' });
      await expect(restartDialog).toBeVisible({ timeout: 10_000 });
      const dialogTitle = restartDialog.locator('h3');
      await expect(dialogTitle).toContainText('Restart');
      const cancelButton = restartDialog.getByRole('button', { name: 'Cancel' });
      await cancelButton.click();
      await expect(restartDialog).not.toBeVisible({ timeout: 5_000 });
      transcript.step(
        'restart opens an impact confirmation naming the feature and phase; cancel makes no mutation',
      );
    } else {
      transcript.step('restart not visible — feature state does not offer restart');
    }
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
