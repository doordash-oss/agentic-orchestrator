import fs from 'node:fs';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  contractEvidenceShot,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import {
  activeRunYamlPath,
  clearRunFailures,
  featureYamlPath,
  parseFeatureRepos,
  replaceTopLevelBlock,
  setRepoPublishable,
  upsertYamlScalar,
  writeWorktreeChange,
} from '../helpers/completionFixture';
import { worldProcessPIDs } from '../helpers/processes';
import {
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('zero-gap completion and global parity: diff, irreversible impact, AMA, recovery, and keyboard', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const world = createWorld('zero-gap-completion-global', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'completion-lab', { commit: true });
  createRepo(world, 'completion-ready', { commit: true });
  const featureName = `Zero Gap Completion ${Math.random().toString(16).slice(2, 8)}`;
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'zero-gap-completion-create' });
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'Exercise completion and global recovery controls.',
      repoPatterns: [/completion-lab/, /completion-ready/],
      waitForReady: true,
    });
    const feature = (await handle.page.evaluate(() => window.agentico.listFeatures())).find(
      (candidate) => candidate.name === featureName,
    );
    expect(feature).toBeDefined();

    const discovery = readDiscovery(world);
    await closeApp(handle);
    handle = null;
    if (discovery !== null) {
      await waitFor(() => !processAlive(discovery.pid), 'app-owned server shutdown', 15_000);
    }
    seedCompletion(world, feature!.id);

    handle = await launchApp(world, testInfo, { traceName: 'zero-gap-completion-global' });
    await expect(handle.page.getByRole('option', { name: featureName })).toBeVisible({
      timeout: 60_000,
    });
    await handle.page.getByRole('option', { name: featureName }).click();
    const cockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(cockpit).toBeVisible({ timeout: 30_000 });

    // Delivery lives on the aftercare runway; the toolbar keeps wrap-up only.
    const aftercare = handle.page.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 30_000 });
    await expect(handle.page.getByRole('button', { name: 'Publish', exact: true })).toHaveCount(0);
    const publishVerb = aftercare.getByRole('button', { name: /Publish this feature/ });
    await expect(publishVerb).toBeVisible({ timeout: 30_000 });
    await publishVerb.click();
    const publishModal = handle.page.getByRole('dialog', { name: 'Publish reviewed changes' });
    await expect(publishModal).toBeVisible({ timeout: 15_000 });
    await expect(publishModal.locator('.completion-workspace__publish')).toBeVisible();
    await publishModal.getByRole('button', { name: 'Cancel' }).click();
    await expect(publishModal).toHaveCount(0);

    // Aftercare opens read-only repository + diff inspection in a workspace dialog,
    // from the What-shipped receipt's Changes row.
    await cockpit.getByRole('button', { name: 'View changes', exact: true }).click();
    const changesModal = handle.page.getByRole('dialog', { name: 'Feature changes' });
    const changes = changesModal.getByRole('region', { name: 'Changes' });
    await expect(changes).toBeVisible({ timeout: 15_000 });
    await changes.getByRole('tab', { name: /completion-lab/ }).click();
    await expect(changes.getByText('Inspecting')).toBeVisible();
    await changesModal.getByRole('button', { name: 'Close' }).click();

    // Merge opens its floating modal from the runway's delivery row; Clean up
    // and Mark done remain the toolbar's wrap-up controls.
    await expect(handle.page.getByRole('button', { name: 'Merge', exact: true })).toHaveCount(0);
    await aftercare.getByRole('button', { name: /Merge this feature/ }).click();
    const mergeModal = handle.page.getByRole('dialog', { name: 'Merge local repositories' });
    await expect(mergeModal).toBeVisible({ timeout: 15_000 });
    await mergeModal.getByRole('button', { name: 'Close' }).click();
    await expect(mergeModal).toHaveCount(0);

    await handle.page.getByRole('button', { name: 'Clean up', exact: true }).click();
    const cleanupDialog = handle.page.getByRole('dialog', { name: 'Clean worktrees?' });
    await expect(cleanupDialog).toBeVisible({ timeout: 15_000 });
    await expect(cleanupDialog.getByRole('button', { name: 'Clean worktrees' })).toBeVisible();
    await contractEvidenceShot(
      handle,
      'completion-verbs-in-cockpit-bar-with-publish-changes-diff-merge-and-cleanup-modal-1440x900',
      1440,
      900,
      'dark',
    );
    await cleanupDialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(cleanupDialog).toHaveCount(0);

    await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
    const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
    await expect(palette).toBeVisible();
    await expect(palette.getByLabel('Search features and commands')).toBeFocused();
    await handle.page.keyboard.press('Escape');
    await expect(palette).toHaveCount(0);

    await handle.page.keyboard.press('Alt+Space');
    const ama = handle.page.getByRole('complementary', { name: 'Ask Agentico' });
    await expect(ama).toBeVisible();
    await ama.getByRole('textbox', { name: 'Ask Agentico' }).fill('Summarize completion state.');
    await ama.getByRole('button', { name: 'Send' }).click();
    await expect(ama.getByLabel('AMA transcript')).toContainText(/Backfill ready|Live semantic/, {
      timeout: 60_000,
    });
    await handle.page.keyboard.press('Alt+Space');
    await expect(ama).toHaveCount(0);

    await handle.page.getByRole('option', { name: 'Overview' }).click();
    const recovery = handle.page.getByRole('region', { name: 'Recovery workspace' });
    await expect(recovery).toBeVisible();
    await expect(recovery.getByRole('button', { name: 'Skip' })).toHaveCount(0);
    const bulk = handle.page.getByRole('region', { name: 'Bulk resume and retry' });
    await bulk.getByRole('button', { name: 'Fresh preview' }).click();
    await expect(bulk.getByText(/No features are eligible/)).toBeVisible({
      timeout: 30_000,
    });
    await contractEvidenceShot(
      handle,
      'global-attention-ama-panel-recovery-entry-and-bulk-action-status-remain-reachable-760x900',
      760,
      900,
      'light',
    );
    persistAppLogs(handle, 'zero-gap-completion-global-app-server');
  } finally {
    if (handle !== null) {
      await handle.page.evaluate(() => window.agentico.endChat()).catch(() => {});
      await closeApp(handle).catch(() => {});
    }
    await waitFor(() => !hasWorldProcesses(world.root), 'provider and server child reap', 10_000);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

function seedCompletion(world: ReturnType<typeof createWorld>, featureId: string): void {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const worktree = parseFeatureRepos(featureYaml)['completion-lab'];
  const readyWorktree = parseFeatureRepos(featureYaml)['completion-ready'];
  if (worktree === undefined) throw new Error('completion fixture worktree missing');
  if (readyWorktree === undefined) throw new Error('completion-ready fixture worktree missing');
  featureYaml = upsertYamlScalar(featureYaml, 'status', 'CodeReady');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '3');
  featureYaml = setRepoPublishable(featureYaml, 'completion-lab', false);
  featureYaml = setRepoPublishable(featureYaml, 'completion-ready', true);
  fs.writeFileSync(featurePath, featureYaml);

  const runPath = activeRunYamlPath(world, featureId);
  let runYaml = clearRunFailures(fs.readFileSync(runPath, 'utf8'));
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    '  completion-lab:',
    '    touched: true',
    '  completion-ready:',
    '    touched: true',
  ]);
  fs.writeFileSync(runPath, runYaml);
  writeWorktreeChange(worktree, 'README.md', '# completion-lab\nzero-gap completion change\n');
  writeWorktreeChange(
    readyWorktree,
    'README.md',
    '# completion-ready\nzero-gap publishable change\n',
  );
}

function hasWorldProcesses(worldRoot: string): boolean {
  return worldProcessPIDs(worldRoot).length > 0;
}
