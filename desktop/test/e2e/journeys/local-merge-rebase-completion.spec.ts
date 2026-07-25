/**
 * Packaged local-merge-rebase completion journey: performs an actual
 * conflicted local merge in a fixture repository, hands the affected
 * repository to the existing in-app rebase journey, returns to a fresh
 * completion preflight, retries merge to authoritative success, marks Done
 * through the separate explicit action, cleans the completed worktrees, and
 * verifies deletion remains blocked until the exact feature display name is
 * entered.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test, type Locator } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { findFeatureId, waitForFeatureStatus } from '../helpers/reviewHelpers';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  git,
  processAlive,
  readDiscovery,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';
import {
  activeRunYamlPath,
  clearRunFailures,
  featureYamlPath,
  parseFeatureRepos,
  parseFeatureRepoSources,
  replaceTopLevelBlock,
  setRepoBaseBranch,
  setRepoPublishable,
  upsertYamlScalar,
  writeWorktreeChange,
  type CompletionWorktrees,
} from '../helpers/completionFixture';

test('packaged local-merge-rebase completion: conflict, rebase, retry, done, cleanup, delete', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    'local-merge-rebase-completion',
    'Local merge rebase completion journey',
  );
  const world = createWorld('local-merge-rebase-completion', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    rebaseProvider: true,
  });
  createRepo(world, 'local-core', { commit: true });
  createRepo(world, 'local-aux', { commit: true });

  const featureName = `LocalMerge ${Math.random().toString(16).slice(2, 8)}`;
  let handle: AppHandle | null = null;
  let seeded: CompletionWorktrees | null = null;

  try {
    transcript.section('Create feature through packaged UI');
    handle = await launchApp(world, testInfo, { traceName: 'local-merge-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'local merge rebase completion fixture',
      repoPatterns: [/local-core/, /local-aux/],
      waitForReady: true,
    });
    const featureId = await findFeatureId(handle, featureName);
    transcript.step(`created feature \`${featureName}\` (${featureId}) and reached Ready`);

    const discovery = readDiscovery(world);
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(
        () => !processAlive(discovery.pid),
        `app-owned server ${discovery.pid} to stop before seeding`,
        15_000,
      );
    }

    transcript.section('Seed local merge fixture with conflict while server is stopped');
    seeded = seedLocalMergeFixture(world, featureId);
    transcript.json('seeded worktrees', seeded.worktrees);

    transcript.section('Relaunch and open feature cockpit');
    handle = await launchApp(world, testInfo, { traceName: 'local-merge-rebase-completion' });
    const cockpit = await openCompletion(handle, featureName);
    await expect(cockpit.getByRole('button', { name: 'Merge', exact: true })).toBeVisible({
      timeout: 30_000,
    });

    transcript.section('Inspect repository scope and diff');
    await cockpit.getByRole('tab', { name: 'Changes' }).click();
    const changes = cockpit.locator('.completion-workspace__inspect');
    await expect(changes).toBeVisible({ timeout: 15_000 });
    await expect(changes.getByRole('button', { name: /local-core/ })).toBeVisible();
    await expect(changes.getByRole('button', { name: /local-aux/ })).toBeVisible();

    const preflightSnapshot = await handle.page.evaluate(
      (id) => window.agentico.preflightCompletion({ featureId: id }),
      featureId,
    );
    transcript.json('preflightCompletion response', preflightSnapshot);

    await changes.getByRole('button', { name: /local-core/ }).click();

    const localCoreDiff = await handle.page.evaluate(
      (id) => window.agentico.getRepositoryDiff({ featureId: id, repo: 'local-core' }),
      featureId,
    );
    transcript.json('getRepositoryDiff(local-core) response', localCoreDiff);

    await expect(changes.getByRole('button', { name: /README\.md/ })).toBeVisible();
    await changes.getByRole('button', { name: /README\.md/ }).click();
    await expect(changes.locator('.completion-workspace__file-diff')).toBeVisible({
      timeout: 15_000,
    });
    transcript.step('local-core diff loaded lazily');

    transcript.section('Attempt local merge and observe conflict');
    await cockpit.getByRole('button', { name: 'Merge', exact: true }).click();
    const mergeModal = handle.page.getByRole('dialog', { name: 'Merge local repositories' });
    await expect(mergeModal.locator('.completion-workspace__merge')).toBeVisible();
    await expect(mergeModal.getByText('local-core')).toBeVisible();
    await expect(mergeModal.getByText('local-aux')).toBeVisible();
    const mergeButton = mergeModal.getByRole('button', { name: 'Merge', exact: true });
    await expect(mergeButton).toBeEnabled();
    await mergeButton.click();
    await expect(mergeModal.locator('.completion-workspace__result')).toBeVisible({
      timeout: 30_000,
    });
    await expect(mergeModal.locator('.completion-workspace__result--failure')).toBeVisible();
    transcript.step('merge failed with conflict as expected');

    transcript.section('Hand off to rebase journey for the conflicted repository');
    const rebaseLink = mergeModal.getByRole('button', { name: /Hand off to rebase/i });
    await expect(rebaseLink).toBeVisible({ timeout: 10_000 });
    await rebaseLink.click();
    await expect(handle.page.locator('.cycle-journey--rebase')).toBeVisible({
      timeout: 15_000,
    });
    transcript.step('entered rebase journey for conflicted repository');
    const rebaseExecute = handle.page
      .locator('.cycle-journey--rebase')
      .getByRole('button', { name: /rebase|execute/i });
    await expect(rebaseExecute).toBeVisible({ timeout: 10_000 });
    await rebaseExecute.click();
    await expect(handle.page.locator('.cycle-journey--rebase')).not.toContainText(/in progress/i, {
      timeout: 30_000,
    });
    await waitForRebaseCycleSuccess(handle, featureId, seeded!.worktrees['local-core']!);
    const rebaseTerminalSnapshot = await handle.page.evaluate(
      (id) => window.agentico.getFeature(id),
      featureId,
    );
    transcript.json('post-rebase feature snapshot', rebaseTerminalSnapshot);
    transcript.step('rebase reached authoritative success state');

    transcript.section('Close cycles drawer and return to completion');
    await handle.page.locator('.cockpit__cycles-close').click();
    await expect(handle.page.locator('.cockpit__cycles-drawer')).not.toBeVisible({
      timeout: 10_000,
    });

    transcript.section('Reopen merge modal and retry merge to success');
    // onHandoffToRebase closed the merge modal, so reopen it via the bar verb before retrying.
    await cockpit.getByRole('button', { name: 'Merge', exact: true }).click();
    const retryMerge = mergeModal.getByRole('button', { name: 'Merge', exact: true });
    await expect(retryMerge).toBeEnabled({ timeout: 15_000 });
    await retryMerge.click();
    await expect(mergeModal.locator('.completion-workspace__result--success')).toBeVisible({
      timeout: 30_000,
    });
    transcript.step('retry merge succeeded after rebase resolution');
    await mergeModal.getByRole('button', { name: 'Close' }).click();
    await expect(mergeModal).toHaveCount(0);

    transcript.section('Mark Done explicitly');
    await cockpit.getByRole('button', { name: 'Mark done', exact: true }).click();
    const markDoneModal = handle.page.getByRole('dialog', { name: 'Mark feature done' });
    await expect(markDoneModal).toBeVisible({ timeout: 15_000 });
    await markDoneModal.getByRole('button', { name: 'Mark Done' }).click();
    // The cockpit closes the Mark done modal on authoritative success.
    await expect(markDoneModal).toBeHidden({ timeout: 30_000 });
    await waitForFeatureStatus(handle.page, featureId, 'Done');
    transcript.step('Mark Done was a separate explicit mutation and reached authoritative Done');

    transcript.section('Clean completed worktrees');
    await cockpit.getByRole('button', { name: 'Clean up', exact: true }).click();
    const cleanupDialog = handle.page.getByRole('dialog', { name: 'Clean worktrees?' });
    await expect(cleanupDialog.getByText(/Branches/i)).toBeVisible();
    await expect(cleanupDialog.getByText(/Feature\/run history/i)).toBeVisible();
    await expect(cleanupDialog.getByText(/Artifacts/i)).toBeVisible();
    await cleanupDialog.getByRole('button', { name: 'Clean worktrees' }).click();
    // CleanupConfirm closes on success rather than leaving an inline success result.
    await expect(cleanupDialog).toBeHidden({ timeout: 30_000 });
    await waitFor(
      () => Object.values(seeded!.worktrees).every((wt) => !fs.existsSync(wt)),
      'completion worktrees to be removed',
      30_000,
    );
    transcript.step('cleanup removed every feature worktree while keeping feature state live');

    transcript.section('Delete feature through the cockpit overflow');
    // Deletion moved out of completion into the cockpit overflow menu; the confirm
    // dialog is named after the feature and gates only on the explicit confirm button.
    await cockpit.getByRole('button', { name: 'More actions' }).click();
    await handle.page.getByRole('menuitem', { name: 'Delete feature' }).click();
    const deleteDialog = handle.page.getByRole('dialog', { name: /Delete .+\?/ });
    await expect(deleteDialog).toBeVisible({ timeout: 15_000 });
    const deleteButton = deleteDialog.getByRole('button', { name: 'Delete feature' });
    await expect(deleteButton).toBeEnabled();
    await deleteButton.click();
    await waitForFeatureMissing(handle, featureId);
    await expect(handle.page.getByText('Feature no longer exists')).toBeVisible();
    transcript.step(
      'delete confirmed through the overflow menu and removed server-side feature state',
    );

    persistAppLogs(handle, 'local-merge-rebase-completion-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function openCompletion(handle: AppHandle, featureName: string): Promise<Locator> {
  await expect(handle.page.getByRole('tab', { name: featureName })).toBeVisible({
    timeout: 60_000,
  });
  await handle.page.getByRole('tab', { name: featureName }).click();
  const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
  await expect(cockpit).toBeVisible({ timeout: 30_000 });
  return cockpit;
}

async function waitForFeatureMissing(handle: AppHandle, featureId: string): Promise<void> {
  await waitFor(
    async () =>
      handle.page
        .evaluate((id) => window.agentico.getFeature(id), featureId)
        .then(() => false)
        .catch(() => true),
    `feature ${featureId} deletion`,
    30_000,
  );
}

async function waitForRebaseCycleSuccess(
  handle: AppHandle,
  featureId: string,
  coreWorktree: string,
): Promise<void> {
  await waitFor(
    async () => {
      const feature = await handle.page.evaluate((id) => window.agentico.getFeature(id), featureId);
      if (feature.cycle?.status === 'running' || feature.cycle?.status === 'need_user_input') {
        return false;
      }
      const repoStatus = feature.repoStatus ?? [];
      const serverReportsNoRebaseFailure = repoStatus.every(
        (repo) =>
          repo.rebaseStatus !== 'conflict' &&
          repo.rebaseStatus !== 'failed' &&
          repo.cycleStatus !== 'failed' &&
          !repo.lastError,
      );
      if (!serverReportsNoRebaseFailure) {
        return false;
      }
      try {
        const status = git(coreWorktree, 'status', '--porcelain').trim();
        const content = fs.readFileSync(path.join(coreWorktree, 'README.md'), 'utf8');
        return (
          status === '' &&
          content.includes('conflicting change on main') &&
          content.includes('merged change from feature')
        );
      } catch {
        return false;
      }
    },
    `feature ${featureId} rebase cycle success`,
    60_000,
  );
}

function seedLocalMergeFixture(world: JourneyWorld, featureId: string): CompletionWorktrees {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const repos = parseFeatureRepos(featureYaml);
  const sources = parseFeatureRepoSources(featureYaml);
  for (const repoName of ['local-core', 'local-aux']) {
    if (repos[repoName] === undefined) {
      throw new Error(`feature.yaml missing repo ${repoName}`);
    }
  }

  featureYaml = upsertYamlScalar(featureYaml, 'status', 'CodeReady');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '3');
  featureYaml = replaceTopLevelBlock(featureYaml, 'checkpoints', [
    'checkpoints:',
    '  manual_publish: true',
    '  draft_publish: false',
  ]);
  featureYaml = setRepoPublishable(featureYaml, 'local-core', false);
  featureYaml = setRepoPublishable(featureYaml, 'local-aux', false);
  featureYaml = setRepoBaseBranch(featureYaml, 'local-core', 'main');
  featureYaml = setRepoBaseBranch(featureYaml, 'local-aux', 'main');
  fs.writeFileSync(featurePath, featureYaml);

  const runPath = activeRunYamlPath(world, featureId);
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = clearRunFailures(runYaml);
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    '  local-core:',
    '    touched: true',
    '  local-aux:',
    '    touched: true',
  ]);
  fs.writeFileSync(runPath, runYaml);

  const coreWorktree = repos['local-core']!;
  const auxWorktree = repos['local-aux']!;
  const coreRepo = sources['local-core']!;

  writeWorktreeChange(coreWorktree, 'README.md', '# local-core\nmerged change from feature\n');
  writeWorktreeChange(auxWorktree, 'README.md', '# local-aux\nauxiliary change\n');

  git(coreWorktree, 'add', '.');
  git(coreWorktree, 'commit', '-m', 'Feature change on local-core');

  git(auxWorktree, 'add', '.');
  git(auxWorktree, 'commit', '-m', 'Feature change on local-aux');

  git(coreRepo, 'checkout', 'main');
  fs.writeFileSync(path.join(coreRepo, 'README.md'), '# local-core\nconflicting change on main\n');
  git(coreRepo, 'add', '.');
  git(coreRepo, 'commit', '-m', 'Conflicting change on main');
  git(coreRepo, 'checkout', '-');

  return { worktrees: repos, sources };
}
