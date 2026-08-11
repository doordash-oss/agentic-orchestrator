/**
 * Review-feedback triage journey: aftercare entry → full-screen multi-repo
 * workspace → repository-sectioned feed with ledger coverage → author filter
 * + visible-only bulk clear over one repo's comments (hidden selections
 * preserved) → repository scope change retaining the path filter → Back →
 * re-entry restores the server-owned selection with view state reset →
 * narrow drawer exposes the same inbox controls → gate toggle →
 * constant-size launch dispatch → kind-aware pass workspace with auto-start
 * attempted, all against the packaged app and bundled server.
 *
 * The feature is seeded to Published with a pr_url per repository so the
 * server enables the "Address review feedback" aftercare action. A local
 * HTTP server serves deterministic comment fixtures for both repositories at
 * the GitHub REST API paths, and the bundled server is pointed at it via
 * AGENTICO_GITHUB_API_BASE so no real GitHub network is touched and no gh
 * credentials are needed.
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

interface RepoFixture {
  reviewComments: unknown[];
  issueComments: unknown[];
  reviews: unknown[];
}

/**
 * Starts a local HTTP server that serves deterministic review-feedback
 * comment fixtures for several repositories at the GitHub REST API paths the
 * fetch handler calls. Returns the server URL for AGENTICO_GITHUB_API_BASE.
 */
function startFakeGitHubAPI(fixtures: Record<string, RepoFixture>): Promise<http.Server> {
  const routes: Record<string, string> = {};
  for (const [repoPath, fixture] of Object.entries(fixtures)) {
    routes[`/repos/${repoPath}/pulls/1/comments`] = JSON.stringify(fixture.reviewComments);
    routes[`/repos/${repoPath}/issues/1/comments`] = JSON.stringify(fixture.issueComments);
    routes[`/repos/${repoPath}/pulls/1/reviews`] = JSON.stringify(fixture.reviews);
  }
  const server = http.createServer((req, res) => {
    const key = (req.url ?? '').split('?')[0]!;
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

test('multi-repo review-feedback triage: sections → filtered bulk clear → restore → drawer → bounded launch → child transition', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript('review-feedback', 'Multi-repository review triage journey');
  const world = createWorld('review-feedback', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  const beta = createRepo(world, 'beta', { commit: true });
  addBareRemote(world, alpha);
  addBareRemote(world, beta);
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\``);
  transcript.step(`committed repositories: \`${alpha}\`, \`${beta}\``);

  const prUrls: Record<string, string> = {
    alpha: 'https://github.com/e2e/alpha/pull/1',
    beta: 'https://github.com/e2e/beta/pull/1',
  };

  let handle: AppHandle | null = null;
  let fakeGitHub: http.Server | null = null;
  try {
    transcript.section('Launch');
    handle = await launchApp(world, testInfo, { traceName: 'review-feedback' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create a two-repo feature through the UI form and open cockpit');
    const featureName = `ReviewFeedback${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'multi-repo review feedback triage journey',
      repoPatterns: [/alpha/, /beta/],
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    transcript.step(`created feature \`${featureName}\` through the form; cockpit visible`);

    const features = await handle.page.evaluate(() => window.agentico.listFeatures());
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published + pr_urls, start fake GitHub API, relaunch');
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
      `    pr_url: ${prUrls.alpha}`,
      '  beta:',
      '    touched: true',
      `    pr_url: ${prUrls.beta}`,
    ]);
    fs.writeFileSync(runPath, runYaml);
    transcript.step('seeded pr_urls for alpha and beta in repo_states');

    fakeGitHub = await startFakeGitHubAPI({
      'e2e/alpha': {
        reviewComments: [
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
            // 80 KiB body: the bulk/launch slice only ever travels by
            // reference, so a comment this large must not block dispatch the
            // way the old full-payload mutation did.
            body: `This function could be simplified. ${'util '.repeat(20480)}`,
            diff_hunk: `@@ -14 +14,2 @@ ${'-ctx\n'.repeat(20480)}`,
            user: { login: 'bob' },
            created_at: '2026-08-02T09:30:00Z',
          },
        ],
        issueComments: [],
        reviews: [],
      },
      'e2e/beta': {
        reviewComments: [],
        issueComments: [
          {
            id: 201,
            body: 'Has this been tested with large inputs?',
            user: { login: 'carol' },
            created_at: '2026-08-02T10:00:00Z',
          },
        ],
        reviews: [
          {
            id: 301,
            body: 'Overall looks good, just a few nits.',
            user: { login: 'dana' },
            submitted_at: '2026-08-02T11:00:00Z',
          },
        ],
      },
    });
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

    transcript.section('Repository-sectioned feed with ledger coverage');
    const alphaSection = feed.getByRole('region', { name: 'alpha' });
    const betaSection = feed.getByRole('region', { name: 'beta' });
    await expect(alphaSection.getByRole('heading', { name: 'alpha' })).toBeVisible();
    await expect(betaSection.getByRole('heading', { name: 'beta' })).toBeVisible();
    // Sections follow the parent's stable repository order.
    const sectionNames = await feed
      .getByRole('region')
      .evaluateAll((regions) => regions.map((region) => region.getAttribute('aria-label')));
    expect(sectionNames).toEqual(['alpha', 'beta']);
    // Oldest-first within a section, with the server's repo ledger.
    await expect(
      alphaSection.getByText('Consider using context.WithCancel for cleanup.'),
    ).toBeVisible();
    await expect(alphaSection.getByText('This function could be simplified.')).toBeVisible();
    await expect(betaSection.getByText('Has this been tested with large inputs?')).toBeVisible();
    await expect(betaSection.getByText('Overall looks good, just a few nits.')).toBeVisible();
    await expect(alphaSection.getByText('2 of 2 selected')).toBeVisible();
    await expect(betaSection.getByText('2 of 2 selected')).toBeVisible();
    // First fetch pre-selects everything; the ledger starts full.
    const boxes = feed.getByRole('checkbox');
    await expect(boxes).toHaveCount(4);
    for (let i = 0; i < 4; i++) {
      await expect(boxes.nth(i)).toBeChecked();
    }
    transcript.step('stable per-repo sections, ledger ratios, all four comments pre-selected');

    transcript.section('Filter by author, clear the visible set, preserve hidden selections');
    // Facets derive from the active scope: four authors across both repos.
    await rail.getByRole('checkbox', { name: 'alice' }).check();
    await expect(feed.getByText('1 of 4 comments visible').first()).toBeVisible();
    await expect(feed.getByRole('button', { name: 'Clear all filters' }).first()).toBeVisible();
    // Only alice's comment is visible, so only it can be bulk-cleared.
    await rail.getByRole('button', { name: 'Clear visible (1)' }).click();
    await expect(workspace.getByText('3 of 4 selected').first()).toBeVisible({ timeout: 15_000 });
    await expect(workspace.getByText(/\bsaving\b/)).toHaveCount(0, { timeout: 15_000 });
    await feed.getByRole('button', { name: 'Clear all filters' }).first().click();
    await expect(feed.getByText('4 of 4 comments visible').first()).toBeVisible();
    // The hidden selections survived the filtered bulk action untouched.
    await expect(feed.getByLabel(/Has this been tested with large inputs?/)).toBeChecked();
    await expect(feed.getByLabel(/Consider using context.WithCancel/)).not.toBeChecked();
    transcript.step('author-filtered Clear visible touched only the visible reference');

    transcript.section('Repository scope narrows the feed and retains the path query');
    await rail.getByRole('searchbox', { name: 'File path' }).fill('src/util');
    await expect(feed.getByText('1 of 4 comments visible').first()).toBeVisible();
    await rail.getByRole('radio', { name: /beta/ }).check();
    // The path query survived the scope change and applies to beta (whose
    // comments carry no paths, so nothing matches until it is cleared).
    await expect(rail.getByRole('searchbox', { name: 'File path' })).toHaveValue('src/util');
    await expect(feed.getByText('0 of 2 comments visible').first()).toBeVisible();
    await expect(feed.getByText(/No comments match the active filters/)).toBeVisible();
    // Author values unavailable in beta were pruned, not hidden.
    await expect(rail.getByRole('checkbox', { name: 'alice' })).toHaveCount(0);
    await feed.getByRole('button', { name: 'Clear all filters' }).first().click();
    await expect(feed.getByText('2 of 2 comments visible').first()).toBeVisible();
    await rail.getByRole('radio', { name: /All feedback/ }).check();
    await expect(feed.getByText('4 of 4 comments visible').first()).toBeVisible();
    transcript.step('scope scoping pruned facets, retained path, and hid nothing selectable');

    transcript.section('Back and re-entry: selections survive, view state resets');
    await workspace.getByRole('button', { name: 'Back', exact: true }).first().click();
    await expect(workspace).toHaveCount(0);
    await addressReviewFeedback.click();
    await expect(workspace).toBeVisible({ timeout: 15_000 });
    const restoredBoxes = workspace.getByLabel('Review feedback').getByRole('checkbox');
    await expect(restoredBoxes).toHaveCount(4);
    await expect(workspace.getByText('3 of 4 selected').first()).toBeVisible();
    await expect(feed.getByLabel(/Consider using context.WithCancel/)).not.toBeChecked();
    // Scope and filters restarted from their defaults: All, unconstrained.
    await expect(rail.getByRole('radio', { name: /All feedback/ })).toBeChecked();
    await expect(rail.getByRole('searchbox', { name: 'File path' })).toHaveValue('');
    transcript.step('re-entry restored the server-owned selection on a fresh view');

    transcript.section('Narrow window: drawer exposes the same inbox controls');
    await handle.page.setViewportSize({ width: 800, height: 900 });
    await expect(workspace.getByLabel('Feedback scope')).toHaveCount(0);
    const drawerOpener = workspace.getByRole('button', { name: 'Repositories and filters' });
    await expect(drawerOpener).toHaveAttribute('aria-expanded', 'false');
    await drawerOpener.click();
    await expect(drawerOpener).toHaveAttribute('aria-expanded', 'true');
    const drawer = handle.page.getByRole('dialog', { name: 'Repositories and filters' });
    await expect(drawer.getByRole('radio', { name: /All feedback/ })).toBeChecked();
    await expect(drawer.getByRole('radio', { name: /beta/ })).toBeVisible();
    await expect(drawer.getByRole('checkbox', { name: 'alice' })).toBeVisible();
    await expect(drawer.getByRole('searchbox', { name: 'File path' })).toBeVisible();
    // Same bulk actions, same committed counts (nothing locally pending).
    await expect(drawer.getByRole('button', { name: 'Clear visible (3)' })).toBeEnabled();
    await expect(drawer.getByRole('button', { name: 'Select visible (1)' })).toBeEnabled();
    // Filtering from inside the drawer does not dismiss it.
    await drawer.getByRole('checkbox', { name: 'alice' }).check();
    await expect(drawer).toBeVisible();
    await expect(feed.getByText('1 of 4 comments visible').first()).toBeVisible();
    await handle.page.keyboard.press('Escape');
    await expect(drawer).toHaveCount(0);
    await expect(drawerOpener).toBeFocused();
    transcript.step('narrow drawer matched the rail and returned focus to its opener');

    await handle.page.setViewportSize({ width: 1440, height: 900 });
    const wideRail = workspace.getByLabel('Feedback scope');
    await expect(wideRail).toBeVisible();
    await feed.getByRole('button', { name: 'Clear all filters' }).first().click();
    await expect(feed.getByText('4 of 4 comments visible').first()).toBeVisible();

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
