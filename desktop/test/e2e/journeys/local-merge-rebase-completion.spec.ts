/**
 * Packaged local-merge-rebase completion journey: performs an actual
 * conflicted local merge in a fixture repository, reads the aftercare
 * rebase hint in the merge modal, launches the rebase pass from the
 * aftercare Rebase card, returns to a fresh completion preflight, retries
 * merge to authoritative atomic completion, cleans the completed worktrees,
 * and verifies the ordinary-feature delete confirmation removes the durable
 * record.
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
    const createdCockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'local merge rebase completion fixture',
      repoPatterns: [/local-core/, /local-aux/],
    });
    await expect(createdCockpit.getByText('Ready to start')).toBeVisible({ timeout: 60_000 });
    const featureId = await findFeatureId(handle, featureName);
    transcript.step(
      `created feature \`${featureName}\` (${featureId}) with durable setup complete`,
    );

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
    // Merge is offered only from the aftercare runway; the toolbar no longer carries delivery verbs.
    const aftercareRunway = handle.page.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercareRunway.getByRole('button', { name: /Merge this feature/ })).toBeVisible({
      timeout: 30_000,
    });
    await expect(handle.page.getByRole('button', { name: 'Merge', exact: true })).toHaveCount(0);

    transcript.section('Inspect repository scope and diff');
    await cockpit.getByRole('button', { name: 'View changes', exact: true }).click();
    const changesModal = handle.page.getByRole('dialog', { name: 'Feature changes' });
    const changes = changesModal.getByRole('region', { name: 'Changes' });
    await expect(changes).toBeVisible({ timeout: 15_000 });
    await expect(changes.getByRole('tab', { name: /local-core/ })).toBeVisible();
    await expect(changes.getByRole('tab', { name: /local-aux/ })).toBeVisible();

    const preflightSnapshot = await handle.page.evaluate(
      (id) => window.agentico.preflightCompletion({ featureId: id }),
      featureId,
    );
    transcript.json('preflightCompletion response', preflightSnapshot);

    await changes.getByRole('tab', { name: /local-core/ }).click();

    const localCoreDiff = await handle.page.evaluate(
      (id) => window.agentico.getRepositoryDiff({ featureId: id, repo: 'local-core' }),
      featureId,
    );
    transcript.json('getRepositoryDiff(local-core) response', localCoreDiff);

    transcript.step('local-core repository status loaded lazily');
    await changesModal.getByRole('button', { name: 'Close' }).click();
    await expect(changesModal).toBeHidden();

    transcript.section('Attempt local merge and observe conflict');
    await aftercareRunway.getByRole('button', { name: /Merge this feature/ }).click();
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

    transcript.section('Read rebase hint and launch rebase pass from aftercare');
    await expect(
      mergeModal.getByText(/Use Start rebase pass in the feature's aftercare workspace/),
    ).toBeVisible({ timeout: 10_000 });
    transcript.step('aftercare rebase hint appears as plain text with no launch button');
    await expect(mergeModal.getByRole('button', { name: /Hand off to rebase/i })).not.toBeVisible();

    await mergeModal.getByRole('button', { name: 'Close' }).click();
    await expect(mergeModal).not.toBeVisible({ timeout: 10_000 });

    const aftercare = handle.page.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 30_000 });
    const rebaseCard = aftercare.getByRole('button', { name: /Start rebase pass/ });
    await expect(rebaseCard).toBeVisible({ timeout: 10_000 });
    await rebaseCard.click();
    transcript.step(
      'clicked "Start rebase pass" on the aftercare Rebase card — rebase child launches',
    );

    const rebasePass = handle.page.getByRole('region', { name: 'Rebase pass' });
    await expect(rebasePass).toBeVisible({ timeout: 60_000 });
    transcript.step('rebase pass workspace visible with kind-aware labeling, no modal');

    // Wait for the rebase child to reach a terminal state (completed or
    // failed). The child's full Plan → Implement → Review lifecycle depends on
    // the provider stub handling multiple plan sub-phases; if the stub cannot
    // complete them the child fails, and the merge retry is skipped.
    await waitForRebaseChildTerminal(handle, featureId);
    const rebaseTerminalSnapshot = await handle.page.evaluate(
      (id) => window.agentico.getFeature(id),
      featureId,
    );
    const rebaseSucceeded = rebaseTerminalSnapshot.activeChild === undefined;
    transcript.json('post-rebase feature snapshot', rebaseTerminalSnapshot);
    transcript.step(
      rebaseSucceeded
        ? 'rebase child completed and returned to aftercare'
        : 'rebase child reached terminal state (Failed)',
    );

    if (!rebaseSucceeded) {
      persistAppLogs(handle, 'local-merge-rebase-completion-app-server');
      transcript.write(testInfo);
      expect(
        rebaseSucceeded,
        'rebase child ended Failed — merge retry/Done/cleanup/delete flow cannot be exercised',
      ).toBe(true);
    }

    transcript.section('Reopen merge modal and retry merge to success');
    // Re-enter the feature after the rebase pass's persisted terminal state. This mirrors a user
    // returning from the pass workspace and ensures the retry is driven by a fresh snapshot.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await openCompletion(handle, featureName);
    await aftercareRunway.getByRole('button', { name: /Merge this feature/ }).click();
    const retryMerge = mergeModal.getByRole('button', { name: 'Merge', exact: true });
    await expect(retryMerge).toBeEnabled({ timeout: 15_000 });
    await retryMerge.click();
    await expect(mergeModal.locator('.completion-workspace__result--success')).toBeVisible({
      timeout: 30_000,
    });
    transcript.step('retry merge succeeded after rebase resolution');
    await mergeModal.getByRole('button', { name: 'Close' }).click();
    await expect(mergeModal).toHaveCount(0);

    transcript.section('Observe atomic completion after local merge');
    await waitForFeatureStatus(handle.page, featureId, 'Done');
    await expect(
      handle.page.getByRole('button', { name: 'Mark done', exact: true }),
    ).toBeDisabled();
    transcript.step('successful local merge atomically reached authoritative Done');

    transcript.section('Clean completed worktrees');
    await handle.page.getByRole('button', { name: 'Clean up', exact: true }).click();
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
    await handle.page.getByLabel('More actions').click();
    await handle.page.getByRole('menuitem', { name: 'Delete feature' }).click();
    const deleteDialog = handle.page.getByRole('dialog', { name: /Delete .+\?/ });
    await expect(deleteDialog).toBeVisible({ timeout: 15_000 });
    const deleteButton = deleteDialog.getByRole('button', { name: 'Delete feature' });
    await expect(deleteButton).toBeEnabled();
    await deleteButton.click();
    await waitForFeatureMissing(handle, featureId);
    await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await expect(handle.page.getByRole('option', { name: featureName })).toHaveCount(0);
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
  await expect(handle.page.getByRole('option', { name: featureName })).toBeVisible({
    timeout: 60_000,
  });
  await handle.page.getByRole('option', { name: featureName }).click();
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

async function waitForRebaseChildTerminal(handle: AppHandle, featureId: string): Promise<void> {
  await waitFor(
    async () => {
      const feature = await handle.page.evaluate(
        (id: string) => window.agentico.getFeature(id),
        featureId,
      );
      return feature.activeChild === undefined || feature.activeChild.status === 'Failed';
    },
    `feature ${featureId} rebase child terminal state`,
    180_000,
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
  featureYaml = featureYaml.replace(/^(\s+review:).+$/m, '$1 sonnet[200K]');
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
