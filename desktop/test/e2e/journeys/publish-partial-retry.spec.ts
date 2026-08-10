/**
 * Packaged publish partial retry journey: exercises the real publish mutation
 * against hermetic fixture repositories, pushes to a local bare origin,
 * deterministically fails PR creation for one repository to produce a
 * structured per-repository partial outcome, and verifies retry defaults only
 * to failed or still-unpublished repositories without republishing confirmed
 * successes.
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
import { findFeatureId } from '../helpers/reviewHelpers';
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
  setRepoPublishable,
  upsertYamlScalar,
  writeWorktreeChange,
  type CompletionWorktrees,
} from '../helpers/completionFixture';

interface PublishFixture extends CompletionWorktrees {
  origins: Record<string, string>;
}

test('packaged publish partial retry: push succeeds, PR creation fails, retry scope defaults to failed', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript('publish-partial-retry', 'Publish partial retry journey');
  const world = createWorld('publish-partial-retry', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'publish-api', { commit: true });
  createRepo(world, 'publish-web', { commit: true });
  createRepo(world, 'local-only', { commit: true });

  const featureName = `PublishRetry ${Math.random().toString(16).slice(2, 8)}`;
  let handle: AppHandle | null = null;
  let seeded: PublishFixture | null = null;

  try {
    transcript.section('Create feature through packaged UI');
    handle = await launchApp(world, testInfo, { traceName: 'publish-retry-create' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'publish partial retry fixture',
      repoPatterns: [/publish-api/, /publish-web/, /local-only/],
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

    transcript.section('Seed publish fixture with local bare origins while server is stopped');
    seeded = seedPublishFixture(world, featureId);
    transcript.json('seeded worktrees', seeded.worktrees);

    transcript.section('Relaunch and open feature cockpit');
    handle = await launchApp(world, testInfo, { traceName: 'publish-partial-retry' });
    const cockpit = await openCompletion(handle, featureName);
    // Publish is offered only from the aftercare runway; the toolbar no longer carries delivery verbs.
    const aftercareRunway = handle.page.getByRole('region', { name: 'Feature aftercare' });
    const publishRow = aftercareRunway.getByRole('button', { name: /Publish this feature/ });
    await expect(publishRow).toBeVisible({ timeout: 30_000 });
    await expect(handle.page.getByRole('button', { name: 'Publish', exact: true })).toHaveCount(0);

    transcript.section('Inspect repository scope and diffs');
    await cockpit.getByRole('button', { name: 'View changes', exact: true }).click();
    const changesModal = handle.page.getByRole('dialog', { name: 'Feature changes' });
    const changes = changesModal.getByRole('region', { name: 'Changes' });
    await expect(changes).toBeVisible({ timeout: 15_000 });
    await expect(changes.getByRole('tab', { name: /publish-api/ })).toBeVisible();
    await expect(changes.getByRole('tab', { name: /publish-web/ })).toBeVisible();
    await expect(changes.getByRole('tab', { name: /local-only/ })).toBeVisible();

    const preflightSnapshot = await handle.page.evaluate(
      (id) => window.agentico.preflightCompletion({ featureId: id }),
      featureId,
    );
    transcript.json('preflightCompletion response', preflightSnapshot);

    await changes.getByRole('tab', { name: /publish-api/ }).click();

    const publishApiDiff = await handle.page.evaluate(
      (id) => window.agentico.getRepositoryDiff({ featureId: id, repo: 'publish-api' }),
      featureId,
    );
    transcript.json('getRepositoryDiff(publish-api) response', publishApiDiff);

    transcript.step('publish-api repository status loaded lazily');
    await changesModal.getByRole('button', { name: 'Close' }).click();

    transcript.section('Open publish sheet and generate a PR narrative');
    await publishRow.click();
    const publishModal = handle.page.getByRole('dialog', { name: 'Publish reviewed changes' });
    await expect(publishModal.locator('.completion-workspace__publish')).toBeVisible();
    // publish-api is pre-seeded as already published — only publish-web is in the publish set.
    await expect(publishModal.getByRole('checkbox', { name: 'publish-api' })).toHaveCount(0);
    await expect(publishModal.getByRole('checkbox', { name: 'publish-web' })).toBeChecked();
    await expect(publishModal.getByRole('checkbox', { name: 'local-only' })).toHaveCount(0);
    await expect(publishModal.getByText('Already published')).toBeVisible({ timeout: 10_000 });
    await expect(publishModal.getByText('Required')).toBeVisible();
    await expect(publishModal.getByText('Optional')).toBeVisible();
    await publishModal.getByRole('button', { name: 'Generate narrative' }).click();
    await expect(publishModal.getByPlaceholder('Enter PR title')).not.toHaveValue('');
    await expect(publishModal.getByPlaceholder('Enter PR description')).not.toHaveValue('');
    transcript.step(
      'publish modal preselected only the eligible unpublished repo and generated PR text',
    );

    transcript.section('Execute publish and observe partial outcome');
    const publishButton = publishModal.getByRole('button', { name: 'Publish', exact: true });
    await expect(publishButton).toBeEnabled();
    await publishButton.click();
    await expect(publishModal.getByRole('alert')).toContainText(
      "Agentico couldn't prepare this publish.",
      {
        timeout: 60_000,
      },
    );
    assertPublishedBranch(seeded, 'publish-web');
    transcript.step(
      'publish action completed with partial outcome (push succeeded, PR creation failed)',
    );

    transcript.section('Verify retry scope defaults to failed/unpublished repositories');
    // Reopen the publish modal so it re-derives scope from the post-publish preflight.
    await publishModal.getByRole('button', { name: 'Cancel' }).click();
    await expect(publishModal).toHaveCount(0);
    await aftercareRunway
      .getByRole('button', { name: /Publish (this feature|new commits)/ })
      .click();
    await expect(publishModal.locator('.completion-workspace__publish')).toBeVisible();
    // publish-api was pre-seeded as already published — it must NOT appear in the retry checkbox set.
    await expect(publishModal.getByRole('checkbox', { name: 'publish-api' })).toHaveCount(0);
    // publish-web failed PR creation — it remains eligible and must be preselected for retry.
    const webCheckbox = publishModal.getByRole('checkbox', { name: 'publish-web' });
    await expect(webCheckbox).toBeVisible({ timeout: 10_000 });
    await expect(webCheckbox).toBeChecked();
    // local-only is untouched — it must be excluded from the retry scope entirely.
    await expect(publishModal.getByRole('checkbox', { name: 'local-only' })).toHaveCount(0);
    // The already-published repo must appear in the "Already published" group, not as a retry candidate.
    await expect(publishModal.getByText('Already published')).toBeVisible({ timeout: 10_000 });
    transcript.step('retry scope defaults only to failed or still-unpublished repositories');

    persistAppLogs(handle, 'publish-partial-retry-app-server');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged publish existing pull-request updates omit title and narrative fields', async ({}, testInfo) => {
  test.setTimeout(300_000);
  const transcript = new Transcript(
    'publish-existing-pr-update',
    'Publish existing pull-request update journey',
  );
  const existingPrRepo = 'existing-pr-repo';
  const world = createWorld('publish-existing-pr-update', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, existingPrRepo, { commit: true });

  const featureName = `PublishExistingPR ${Math.random().toString(16).slice(2, 8)}`;
  let handle: AppHandle | null = null;
  let seeded: PublishFixture | null = null;

  try {
    transcript.section('Create feature through packaged UI');
    handle = await launchApp(world, testInfo, { traceName: 'publish-existing-pr-create' });
    await createFeatureViaForm(handle, {
      name: featureName,
      description: 'title-less existing pull-request update fixture',
      repoPatterns: [new RegExp(existingPrRepo)],
      waitForReady: true,
    });
    const featureId = await findFeatureId(handle, featureName);

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

    transcript.section('Seed a published pull-request branch, then add an unpublished local commit');
    seeded = seedExistingPRUpdateFixture(world, featureId, existingPrRepo);
    transcript.json('seeded worktrees', seeded.worktrees);

    transcript.section('Publish the existing pull-request update through the packaged app');
    handle = await launchApp(world, testInfo, { traceName: 'publish-existing-pr-update' });
    await openCompletion(handle, featureName);
    const aftercareRunway = handle.page.getByRole('region', { name: 'Feature aftercare' });
    const publishRow = aftercareRunway.getByRole('button', { name: /Publish new commits/ });
    await expect(publishRow).toBeVisible({ timeout: 30_000 });
    await publishRow.click();

    const publishModal = handle.page.getByRole('dialog', { name: 'Publish reviewed changes' });
    await expect(publishModal.getByText('Unpublished changes')).toBeVisible();
    await expect(publishModal.getByRole('checkbox', { name: existingPrRepo })).toBeChecked();
    await expect(publishModal.getByLabel('PR title')).toHaveCount(0);
    await expect(publishModal.getByLabel('PR body')).toHaveCount(0);
    await expect(publishModal.getByRole('button', { name: 'Generate PR narrative' })).toHaveCount(0);
    await expect(publishModal.getByRole('button', { name: 'Generate narrative' })).toHaveCount(0);
    await publishModal.getByRole('button', { name: 'Publish updates' }).click();
    await expect(publishModal.getByRole('status')).toContainText(/published/i);
    assertPublishedBranch(seeded, existingPrRepo);
    transcript.step('existing pull-request branch was updated without PR title or narrative controls');

    persistAppLogs(handle, 'publish-existing-pr-update-app-server');
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

function seedPublishFixture(world: JourneyWorld, featureId: string): PublishFixture {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const repos = parseFeatureRepos(featureYaml);
  const sources = parseFeatureRepoSources(featureYaml);
  const origins: Record<string, string> = {};
  for (const repoName of ['publish-api', 'publish-web', 'local-only']) {
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
  featureYaml = setRepoPublishable(featureYaml, 'publish-api', true);
  featureYaml = setRepoPublishable(featureYaml, 'publish-web', true);
  featureYaml = setRepoPublishable(featureYaml, 'local-only', true);
  fs.writeFileSync(featurePath, featureYaml);

  const runPath = activeRunYamlPath(world, featureId);
  let runYaml = fs.readFileSync(runPath, 'utf8');
  runYaml = clearRunFailures(runYaml);
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    '  publish-api:',
    '    touched: true',
    '    pr_url: https://github.example/local-bare/publish-api/pull/1',
    '  publish-web:',
    '    touched: true',
    '  local-only:',
    '    touched: false',
  ]);
  fs.writeFileSync(runPath, runYaml);

  for (const repoName of ['publish-api', 'publish-web', 'local-only']) {
    const repoPath = sources[repoName]!;
    const barePath = path.join(world.root, `${repoName}-origin.git`);
    origins[repoName] = barePath;
    git(world.root, 'init', '--bare', barePath, '--initial-branch=main');
    git(repoPath, 'remote', 'add', 'origin', barePath);
    git(repoPath, 'push', '-u', 'origin', 'main');
  }

  for (const repoName of ['publish-api', 'publish-web']) {
    const worktree = repos[repoName]!;
    writeWorktreeChange(worktree, 'README.md', `# ${repoName}\nfeature change\n`);
    git(worktree, 'add', '.');
    git(worktree, 'commit', '-m', `Feature change on ${repoName}`);
  }

  return { worktrees: repos, sources, origins };
}

function seedExistingPRUpdateFixture(
  world: JourneyWorld,
  featureId: string,
  repoName: string,
): PublishFixture {
  const featurePath = featureYamlPath(world, featureId);
  let featureYaml = fs.readFileSync(featurePath, 'utf8');
  const repos = parseFeatureRepos(featureYaml);
  const sources = parseFeatureRepoSources(featureYaml);
  const worktree = repos[repoName];
  const source = sources[repoName];
  if (worktree === undefined || source === undefined) {
    throw new Error(`feature.yaml missing repo ${repoName}`);
  }

  featureYaml = upsertYamlScalar(featureYaml, 'status', 'CodeReady');
  featureYaml = upsertYamlScalar(featureYaml, 'current_phase', '3');
  featureYaml = replaceTopLevelBlock(featureYaml, 'checkpoints', [
    'checkpoints:',
    '  manual_publish: true',
    '  draft_publish: false',
  ]);
  featureYaml = setRepoPublishable(featureYaml, repoName, true);
  fs.writeFileSync(featurePath, featureYaml);

  const runPath = activeRunYamlPath(world, featureId);
  let runYaml = clearRunFailures(fs.readFileSync(runPath, 'utf8'));
  runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
    'repo_states:',
    `  ${repoName}:`,
    '    touched: true',
    `    pr_url: https://github.example/local-bare/${repoName}/pull/1`,
  ]);
  fs.writeFileSync(runPath, runYaml);

  const origin = path.join(world.root, `${repoName}-origin.git`);
  git(world.root, 'init', '--bare', origin, '--initial-branch=main');
  git(source, 'remote', 'add', 'origin', origin);
  git(source, 'push', '-u', 'origin', 'main');

  writeWorktreeChange(worktree, 'README.md', '# existing pull request\npublished change\n');
  git(worktree, 'add', '.');
  git(worktree, 'commit', '-m', 'Published pull-request change');
  const branch = git(worktree, 'rev-parse', '--abbrev-ref', 'HEAD').trim();
  git(worktree, 'push', '-u', 'origin', branch);

  writeWorktreeChange(worktree, 'README.md', '# existing pull request\nunpublished update\n');
  git(worktree, 'add', '.');
  git(worktree, 'commit', '-m', 'Unpublished pull-request update');

  return { worktrees: repos, sources, origins: { [repoName]: origin } };
}

function assertPublishedBranch(seeded: PublishFixture, repoName: string): void {
  const worktree = seeded.worktrees[repoName];
  const origin = seeded.origins[repoName];
  if (worktree === undefined || origin === undefined) {
    throw new Error(`missing publish fixture paths for ${repoName}`);
  }
  const branch = git(worktree, 'rev-parse', '--abbrev-ref', 'HEAD').trim();
  const localHead = git(worktree, 'rev-parse', 'HEAD').trim();
  const remoteHead = git(worktree, '--git-dir', origin, 'rev-parse', branch).trim();
  expect(remoteHead).toBe(localHead);
}
