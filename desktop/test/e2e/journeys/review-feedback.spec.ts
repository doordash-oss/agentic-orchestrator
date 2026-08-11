/**
 * Review-feedback durable-draft journey: aftercare entry → full-screen
 * workspace → fetch from a local GitHub API fixture → deselect one card →
 * Back (waits for the save) → re-entry restores the server-owned selection →
 * gate toggle → constant-size launch dispatch → kind-aware pass workspace
 * with auto-start attempted, all against the packaged app and bundled server.
 *
 * The feature is seeded to Published with a pr_url so the server enables the
 * "Address review feedback" aftercare action. A local HTTP server serves
 * deterministic comment fixtures at the GitHub REST API paths, and the
 * bundled server is pointed at it via AGENTICO_GITHUB_API_BASE so no real
 * GitHub network is touched and no gh credentials are needed.
 */
import { expect, test, type TestInfo } from '@playwright/test';
import http from 'node:http';
import fs from 'node:fs';
import { closeApp, createFeatureViaForm, launchApp, type AppHandle } from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  addBareRemote,
  createRepo,
  createWorld,
  destroyWorld,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';
import { setFeatureStatus } from '../helpers/seed';
import { activeRunYamlPath, clearRunFailures } from '../helpers/completionFixture';
import { replaceTopLevelBlock } from '../helpers/yaml';

/**
 * Starts a local HTTP server that serves deterministic review-feedback
 * comment fixtures at the GitHub REST API paths the fetch handler calls.
 * Returns the server URL to pass as AGENTICO_GITHUB_API_BASE.
 */
function startFakeGitHubAPI(owner: string, repo: string, prNumber: number): Promise<http.Server> {
  const reviewComments = JSON.stringify([
    {
      id: 101,
      path: 'src/main.go',
      line: 42,
      body: 'Consider using context.WithCancel for cleanup.',
      user: { login: 'alice' },
      created_at: '2026-08-02T09:00:00Z',
    },
    {
      id: 102,
      path: 'src/util.go',
      line: 15,
      // 80 KiB body: the draft slice only ever travels by reference, so a
      // comment this large must not block the launch dispatch the way the
      // old full-payload mutation did.
      body: `This function could be simplified. ${'util '.repeat(20480)}`,
      diff_hunk: `@@ -14 +14,2 @@ ${'-ctx\n'.repeat(20480)}`,
      user: { login: 'bob' },
      created_at: '2026-08-02T09:30:00Z',
    },
  ]);
  const issueComments = JSON.stringify([
    {
      id: 201,
      body: 'Has this been tested with large inputs?',
      user: { login: 'carol' },
      created_at: '2026-08-02T10:00:00Z',
    },
  ]);
  const reviews = JSON.stringify([
    {
      id: 301,
      body: 'Overall looks good, just a few nits.',
      user: { login: 'dana' },
      submitted_at: '2026-08-02T11:00:00Z',
    },
  ]);
  const routes: Record<string, string> = {
    [`/repos/${owner}/${repo}/pulls/${prNumber}/comments`]: reviewComments,
    [`/repos/${owner}/${repo}/issues/${prNumber}/comments`]: issueComments,
    [`/repos/${owner}/${repo}/pulls/${prNumber}/reviews`]: reviews,
  };
  const server = http.createServer((req, res) => {
    const urlPath = req.url ?? '';
    const key = urlPath.split('?')[0]!;
    const body = routes[key];
    if (body !== undefined) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(body);
      return;
    }
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ message: `unexpected path: ${key}` }));
  });
  return new Promise((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve(server));
  });
}

function serverBaseUrl(server: http.Server): string {
  const addr = server.address();
  if (addr === null || typeof addr === 'string') {
    throw new Error('fake GitHub API server is not listening on an inet addr');
  }
  return `http://127.0.0.1:${addr.port}`;
}

test('review-feedback draft journey: entry → save → restore → bounded launch → child transition', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('review-feedback', 'Review-feedback durable draft journey');
  const world = createWorld('review-feedback', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  addBareRemote(world, alpha);
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`committed repository: \`${alpha}\``);

  const ghOwner = 'e2e';
  const ghRepo = 'alpha';
  const ghPrNumber = 1;
  const prURL = `https://github.com/${ghOwner}/${ghRepo}/pull/${ghPrNumber}`;

  let handle: AppHandle | null = null;
  let fakeGitHub: http.Server | null = null;
  try {
    transcript.section('Launch');
    handle = await launchApp(world, testInfo, { traceName: 'review-feedback' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create feature through the UI form and open cockpit');
    const featureName = `ReviewFeedback${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'review feedback draft journey',
      repoPatterns: [/alpha/],
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    transcript.step(`created feature \`${featureName}\` through the form; cockpit visible`);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published + pr_url, start fake GitHub API, relaunch');
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

    const runPath = activeRunYamlPath(world, featureId);
    let runYaml = fs.readFileSync(runPath, 'utf8');
    runYaml = clearRunFailures(runYaml);
    runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
      'repo_states:',
      '  alpha:',
      '    touched: true',
      `    pr_url: ${prURL}`,
    ]);
    fs.writeFileSync(runPath, runYaml);
    transcript.step(`seeded pr_url ${prURL} in repo_states`);

    fakeGitHub = await startFakeGitHubAPI(ghOwner, ghRepo, ghPrNumber);
    const apiBase = serverBaseUrl(fakeGitHub);
    transcript.step(`fake GitHub API listening at \`${apiBase}\``);

    handle = await launchApp(world, testInfo, {
      traceName: 'review-feedback-seeded',
      env: {
        AGENTICO_GITHUB_API_BASE: apiBase,
      },
    });
    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await expect(overviewOption).toBeVisible({ timeout: 60_000 });
    await overviewOption.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched with fake GitHub API via AGENTICO_GITHUB_API_BASE');

    transcript.section('Open feature cockpit and enter the review-feedback workspace');
    const featureOption = handle.page.getByRole('option', { name: featureName });
    await featureOption.click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });

    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    await expect(aftercare).toBeVisible({ timeout: 15_000 });

    const addressReviewFeedback = aftercare.getByRole('button', {
      name: /Address review feedback/,
    });
    await expect(addressReviewFeedback).toBeVisible();
    await addressReviewFeedback.click();
    transcript.step('clicked "Address review feedback" in aftercare');

    const workspace = handle.page.getByRole('dialog', { name: 'Address review feedback' });
    await expect(workspace).toBeVisible({ timeout: 15_000 });
    transcript.step('full-screen workspace opened (no modal dialog framed it)');

    const feed = workspace.getByLabel('Review feedback');
    await expect(feed).toBeVisible({ timeout: 15_000 });
    const rail = workspace.getByLabel('Feedback scope');
    await expect(rail).toBeVisible();
    await expect(rail.getByText('All feedback')).toBeVisible();
    await expect(rail.getByText('alpha')).toBeVisible();
    await expect(
      workspace.getByText('Consider using context.WithCancel for cleanup.'),
    ).toBeVisible();
    await expect(workspace.getByText('This function could be simplified.')).toBeVisible();
    await expect(workspace.getByText('Has this been tested with large inputs?')).toBeVisible();
    await expect(workspace.getByText('Overall looks good, just a few nits.')).toBeVisible();
    transcript.step('repository scope rail plus all four comments rendered');

    const checkboxes = feed.getByRole('checkbox');
    await expect(checkboxes).toHaveCount(4);
    for (let i = 0; i < 4; i++) {
      await expect(checkboxes.nth(i)).toBeChecked();
    }
    transcript.step('first fetch pre-selected every visible reference');

    transcript.section('Deselect one card, confirm, Back, and restore on re-entry');
    await checkboxes.nth(0).uncheck();
    // Wait for the reference-only selection save to be acknowledged: the
    // ledger reflects the committed count once the save overlay drains.
    await expect(workspace.getByText('3 of 4 selected')).toBeVisible({ timeout: 15_000 });
    await expect(workspace.getByText(/\bsaving\b/)).toHaveCount(0, { timeout: 15_000 });
    transcript.step('deselected one card; the server acknowledged the committed save');

    await workspace.getByRole('button', { name: 'Back', exact: true }).first().click();
    await expect(workspace).toHaveCount(0);
    transcript.step('Back returned to the cockpit after the save committed');

    await addressReviewFeedback.click();
    await expect(workspace).toBeVisible({ timeout: 15_000 });
    const restoredBoxes = workspace.getByLabel('Review feedback').getByRole('checkbox');
    await expect(restoredBoxes).toHaveCount(4);
    await expect(restoredBoxes.nth(0)).not.toBeChecked();
    await expect(restoredBoxes.nth(1)).toBeChecked();
    await expect(restoredBoxes.nth(2)).toBeChecked();
    await expect(restoredBoxes.nth(3)).toBeChecked();
    transcript.step('re-entry restored the server-owned selection (3 of 4 kept)');

    transcript.section('Launch from the committed draft with an 80 KiB comment');
    // Constant-size dispatch proof: the selected 80 KiB comment (body + diff
    // hunk) far exceeds the mutation body limit the old full-payload request
    // hit, so a successful child transition proves launch travels by draft
    // revision only and the server resolves the complete current content.

    const gateToggle = workspace.getByRole('checkbox', {
      name: /Pause for Roadmap and Phase plan review/,
    });
    await gateToggle.check();
    transcript.step('toggled the Roadmap/Plan gate');

    const launchButton = workspace.getByRole('button', { name: /Launch child \(3\)/ });
    await expect(launchButton).toBeEnabled();
    await launchButton.click();
    transcript.step('launched the review-feedback child pass from the committed draft');

    const pass = seededCockpit.getByRole('region', { name: 'Review feedback pass' });
    await expect(pass).toBeVisible({ timeout: 60_000 });
    transcript.step('review-feedback pass workspace visible with kind-aware labeling');

    // Auto-start: the start dispatch fires once setup completes, so the pass
    // transitions out of the "ready" state without a manual click.
    await expect(pass).toHaveAttribute('data-state', 'working', { timeout: 60_000 });
    transcript.step('auto-start attempted: pass transitioned to working without a manual click');
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    fakeGitHub?.close();
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
