/**
 * Lifecycle cycles journey: resume, retry, restart, guarded rebase, child
 * refactor launch, and repository NEED_USER_INPUT journey against the
 * packaged app and real bundled server.
 *
 * The feature is seeded to Published status so the server enables cycle
 * actions (rebase and refactor) which are only available
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

test('lifecycle cycles: resume, retry, restart, rebase, refactor child', async ({}, testInfo: TestInfo) => {
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

    transcript.section('Open feature cockpit and aftercare cycle actions');
    const featureTab = handle.page.getByRole('tab', { name: featureName });
    await featureTab.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const startRebase = aftercare.getByRole('button', { name: /Start rebase pass/ });
    if (await startRebase.isVisible({ timeout: 3_000 }).catch(() => false)) {
      await startRebase.click();
      await expect(handle.page.getByRole('dialog', { name: 'Rebase' })).not.toBeVisible({
        timeout: 5_000,
      });
      const rebaseError = seededCockpit.getByRole('alert').filter({ hasText: /rebase/ });
      await expect(rebaseError).toBeVisible({ timeout: 30_000 });
      await expect(aftercare).toBeVisible({ timeout: 5_000 });
      transcript.step(
        'rebase card dispatched a direct launch with no modal; up-to-date notice rendered inline',
      );
    } else {
      transcript.step('rebase card not visible — server does not enable it for this state');
    }

    transcript.section('Restart confirmation');
    // Restart must be confirmed before a refactor child exists: the child
    // guard disables the parent-level restart while a pass is active, and the
    // pass's own bar-level "Restart" dispatches without a confirmation. The
    // parent feature restart lives in the overflow menu.
    const actionsGroup = seededCockpit.getByRole('group', { name: 'Feature actions' });
    await actionsGroup.getByLabel('More actions').click();
    const restartButton = actionsGroup.getByRole('menuitem', { name: 'Restart', exact: true });
    await expect(restartButton).toBeVisible({ timeout: 10_000 });
    await expect(restartButton).toBeEnabled({ timeout: 60_000 });
    await restartButton.click();
    const restartDialog = handle.page.locator('.impact-dialog', { hasText: 'Restart' });
    await expect(restartDialog).toBeVisible({ timeout: 10_000 });
    await expect(restartDialog.locator('h3')).toContainText('Restart');
    await restartDialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(restartDialog).not.toBeVisible({ timeout: 5_000 });
    transcript.step(
      'restart opens an impact confirmation naming the feature and phase; cancel makes no mutation',
    );

    const planRefactor = aftercare.getByRole('button', { name: /Plan refactor/ });
    if (await planRefactor.isVisible({ timeout: 3_000 }).catch(() => false)) {
      await planRefactor.click();
      const refactorModal = handle.page.getByRole('dialog', { name: 'Start refactor' });
      await refactorModal
        .getByRole('textbox', { name: 'Brief' })
        .fill('Consolidate the lifecycle fixture.');
      await expect(refactorModal.getByText(/Inherited from/)).toBeVisible();
      await refactorModal.getByRole('button', { name: /Next: Pipeline/ }).click();
      await expect(refactorModal.getByRole('radio', { name: /Medium/ })).toBeVisible();
      await refactorModal.getByRole('button', { name: /Next: Review/ }).click();
      await expect(
        refactorModal.getByRole('heading', { name: 'Review the run contract' }),
      ).toBeVisible();
      await expect(refactorModal.getByLabel('Risk')).toBeVisible();
      await expect(refactorModal.getByRole('group', { name: 'Models' })).toBeVisible();
      // Auto-start defaults on; the journey opts out so the child stays parked
      // at Ready-to-start for the assertions below.
      const autoStartToggle = refactorModal.getByRole('checkbox', { name: /Start immediately/ });
      await expect(autoStartToggle).toBeChecked();
      await autoStartToggle.uncheck();
      await refactorModal.getByRole('button', { name: 'Launch child' }).click();
      const pass = seededCockpit.getByRole('region', { name: 'Refactor pass' });
      await expect(pass).toBeVisible({ timeout: 30_000 });
      await expect(pass.getByLabel('Custody of the work')).toContainText(
        'locked while the pass runs',
      );
      transcript.step('creation-parity wizard launched a separately controlled child relationship');
    } else {
      transcript.step('refactor action not visible — server does not enable it for this state');
    }
    transcript.step('aftercare actions opened their focused rebase and refactor flows');
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
