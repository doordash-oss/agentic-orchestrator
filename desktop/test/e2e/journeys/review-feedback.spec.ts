/**
 * Review-feedback initialization journey: aftercare → modal → fetch from
 * a local GitHub API fixture → deselect one → gate toggle → confirm →
 * kind-aware pass workspace with auto-start attempted, all against the
 * packaged app and bundled server.
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
      body: 'This function could be simplified.',
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

test('review-feedback initialization: modal → fetch → deselect → gate → confirm → kind-aware workspace', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('review-feedback', 'Review-feedback initialization journey');
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
      description: 'review feedback initialization journey',
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
    const homeTab = handle.page.getByRole('tab', { name: 'Home' });
    await expect(homeTab).toBeVisible({ timeout: 60_000 });
    await homeTab.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('relaunched with fake GitHub API via AGENTICO_GITHUB_API_BASE');

    transcript.section('Open feature cockpit and launch review feedback');
    const featureTab = handle.page.getByRole('tab', { name: featureName });
    await featureTab.click();
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

    const modal = handle.page.getByRole('dialog', { name: 'Address review feedback' });
    await expect(modal).toBeVisible({ timeout: 15_000 });
    transcript.step('review-feedback modal opened');

    const commentGroups = modal.getByLabel('Review feedback by repository');
    await expect(commentGroups).toBeVisible({ timeout: 15_000 });
    await expect(modal.getByText('alpha')).toBeVisible();
    await expect(modal.getByText('Consider using context.WithCancel for cleanup.')).toBeVisible();
    await expect(modal.getByText('This function could be simplified.')).toBeVisible();
    await expect(modal.getByText('Has this been tested with large inputs?')).toBeVisible();
    await expect(modal.getByText('Overall looks good, just a few nits.')).toBeVisible();
    transcript.step('comments fetched from fake GitHub API and rendered grouped by repo');

    const checkboxes = modal.getByLabel('Review feedback by repository').getByRole('checkbox');
    await expect(checkboxes).toHaveCount(4);
    for (let i = 0; i < 4; i++) {
      await expect(checkboxes.nth(i)).toBeChecked();
    }
    transcript.step('all four comments pre-selected');

    await checkboxes.nth(0).uncheck();
    transcript.step('deselected one comment');

    const gateToggle = modal.getByRole('checkbox', {
      name: /Pause for Roadmap and Phase plan review/,
    });
    await gateToggle.check();
    transcript.step('toggled the Roadmap/Plan gate');

    const launchButton = modal.getByRole('button', { name: /Launch child/ });
    await expect(launchButton).toBeEnabled();
    await launchButton.click();
    transcript.step('launched the review-feedback child pass');

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
