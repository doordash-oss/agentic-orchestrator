/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
import path from 'node:path';
import {
  closeApp,
  contractEvidenceShot,
  createFeatureViaForm,
  launchApp,
  type AppHandle,
} from '../helpers/app';
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
function startFakeGitHubAPI(fixtures: Record<string, RepoFixture>): Promise<{
  server: http.Server;
  /** Mutable route bodies: rewrite entries to change what the app fetches. */
  routes: Record<string, string>;
}> {
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
    server.listen(0, '127.0.0.1', () => resolve({ server, routes }));
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

    const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
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

    const fake = await startFakeGitHubAPI({
      'e2e/alpha': {
        reviewComments: [
          {
            id: 101,
            path: 'src/main.go',
            line: 42,
            // Rich, deliberately adversarial body: approved GFM should render
            // richly while HTML stays inert text, dangerous links are blocked
            // in place, SVG images are rejected, and valid https destinations
            // surface only as external-browser actions.
            body: [
              '**Critical:** `cleanup()` never cancels the derived context.',
              '',
              '- [x] reproduces in e2e',
              '',
              '```go',
              'ctx, cancel := context.WithCancel(ctx)',
              '```',
              '',
              'Beware <script>alert(1)</script> payloads, avoid',
              '[bad link](javascript:alert(1)), and note the brand asset',
              '![logo](https://files.example.com/logo.svg). Full notes at',
              '[the Go docs](https://go.dev/doc). Trace:',
              '![trace](https://files.example.com/trace.png)',
            ].join('\n'),
            diff_hunk: '@@ -40,3 +40,4 @@ main()\n defer close(quit)\n-cancel()\n+cancel()',
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
    fakeGitHub = fake.server;
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
    await expect(alphaSection.getByText('never cancels the derived context.')).toBeVisible();
    await expect(alphaSection.getByText('This function could be simplified.')).toBeVisible();
    await expect(betaSection.getByText('Has this been tested with large inputs?')).toBeVisible();
    await expect(betaSection.getByText('Overall looks good, just a few nits.')).toBeVisible();
    await expect(alphaSection.getByText('2 of 2 selected')).toBeVisible();
    await expect(betaSection.getByText('2 of 2 selected')).toBeVisible();
    // First fetch pre-selects everything; the ledger starts full.
    const boxes = feed.getByRole('checkbox', { name: /^Select feedback/ });
    await expect(boxes).toHaveCount(4);
    for (let i = 0; i < 4; i++) {
      await expect(boxes.nth(i)).toBeChecked();
    }
    // The feed, not a clipped repository card, owns vertical scrolling. A
    // wheel gesture anywhere over the right-hand column reaches later cards.
    expect(await feed.evaluate((element) => element.scrollHeight > element.clientHeight)).toBe(
      true,
    );
    await feed.hover();
    await handle.page.mouse.wheel(0, 600);
    await expect.poll(() => feed.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    await boxes.last().scrollIntoViewIfNeeded();
    await expect(boxes.last()).toBeVisible();
    await feed.evaluate((element) => element.scrollTo(0, 0));
    await contractEvidenceShot(
      handle,
      'review-feedback-native-cockpit-wide-light',
      1440,
      900,
      'light',
    );
    await contractEvidenceShot(
      handle,
      'review-feedback-native-cockpit-wide-dark',
      1440,
      900,
      'dark',
    );
    transcript.step('stable per-repo sections, ledger ratios, all four comments pre-selected');

    transcript.section('Rich feedback cards: safe GFM, trust-boundary actions, diff context');
    // Approved GFM renders richly; author HTML never becomes DOM.
    await expect(alphaSection.getByText(/<script>alert\(1\)<\/script>/)).toBeVisible();
    await expect(feed.locator('script')).toHaveCount(0);
    await expect(feed.locator('img')).toHaveCount(0);
    // Curated-language fenced code is class-highlighted and scrollable.
    await expect(alphaSection.locator('pre code.hljs.language-go')).toBeVisible();
    // Task items are disabled/read-only and distinct from selection controls.
    const taskItem = alphaSection.getByRole('checkbox', { name: 'Task completed (read-only)' });
    await expect(taskItem).toBeChecked();
    await expect(taskItem).toBeDisabled();
    // Links and images are explicit external actions disclosing the hostname.
    await expect(
      alphaSection.getByRole('button', { name: 'Open link externally: the Go docs (go.dev)' }),
    ).toBeVisible();
    await expect(alphaSection.getByText('Link blocked')).toBeVisible();
    await expect(alphaSection.getByText('Image blocked')).toBeVisible();
    await expect(
      alphaSection.getByRole('button', {
        name: 'Open image externally: trace (files.example.com)',
      }),
    ).toBeVisible();
    // Diff context is a labelled, raw-text region with line semantics.
    const diff = alphaSection.getByRole('group', { name: 'Diff' }).first();
    await expect(diff).toBeVisible();
    await expect(diff.locator('[data-kind="hunk"]')).toHaveCount(1);
    await expect(diff.locator('[data-kind="add"]')).toHaveCount(1);
    await expect(diff.locator('[data-kind="remove"]')).toHaveCount(1);
    transcript.step('adversarial Markdown stayed inert; links/images are boundary actions only');

    transcript.section('Scrollable full-comment reader on the oversized card');
    // The 80 KiB comment starts as a compact preview and opens in a modal.
    const hugeCard = alphaSection.locator('article', {
      hasText: 'This function could be simplified',
    });
    const hugeCardToggle = hugeCard.getByRole('button', { name: 'View full comment' });
    await expect(hugeCardToggle).toHaveCount(1);
    await expect(betaSection.getByRole('button', { name: 'View full comment' })).toHaveCount(2);
    await hugeCardToggle.click();
    const commentDialog = handle.page.getByRole('dialog', { name: /Review comment from bob/ });
    await expect(commentDialog).toBeVisible();
    await expect(commentDialog.getByText('This function could be simplified.')).toBeVisible();
    await expect(commentDialog.getByRole('button', { name: 'Close comment' })).toBeFocused();
    // Reading the complete comment never touches selection state.
    await expect(feed.getByLabel(/This function could be simplified/)).toBeChecked();
    await expect(alphaSection.getByText('2 of 2 selected')).toBeVisible();
    await handle.page.keyboard.press('Escape');
    await expect(commentDialog).toHaveCount(0);
    await expect(hugeCardToggle).toBeFocused();
    transcript.step('full comment opened in a focused modal and selection stayed invariant');

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
    await expect(feed.getByLabel(/never cancels the derived context/)).not.toBeChecked();
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
    const restoredBoxes = workspace
      .getByLabel('Review feedback')
      .getByRole('checkbox', { name: /^Select feedback/ });
    await expect(restoredBoxes).toHaveCount(4);
    await expect(workspace.getByText('3 of 4 selected').first()).toBeVisible();
    await expect(feed.getByLabel(/never cancels the derived context/)).not.toBeChecked();
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
    await contractEvidenceShot(
      handle,
      'review-feedback-native-cockpit-narrow-drawer-light',
      800,
      900,
      'light',
    );
    await contractEvidenceShot(
      handle,
      'review-feedback-native-cockpit-narrow-drawer-dark',
      800,
      900,
      'dark',
    );
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

    const launchButton = workspace.getByRole('button', { name: /Address comments \(3\)/ });
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

test('review-feedback recovery: failed-save Retry/Reload → conflict convergence → interrupted-launch replay with original counts', async ({}, testInfo: TestInfo) => {
  const transcript = new Transcript(
    'review-feedback-recovery',
    'Failed-save recovery, conflict convergence, and interrupted-launch replay journey',
  );
  const world = createWorld('review-feedback-recovery', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  const alpha = createRepo(world, 'alpha', { commit: true });
  addBareRemote(world, alpha);
  transcript.section('World');
  transcript.step(`isolated world at \`${world.root}\`; repository: \`${alpha}\``);

  const prUrl = 'https://github.com/e2e/alpha/pull/1';
  // Shared fixture objects: the launch phase rewrites them so reconciliation
  // observes exactly one selected comment changed since review.
  const alphaComments = [
    {
      id: 101,
      path: 'src/main.go',
      line: 42,
      body: '`cleanup()` never cancels the derived context.',
      diff_hunk: '@@ -40 +40 @@\n-cancel()\n+cancel()',
      user: { login: 'alice' },
      created_at: '2026-08-02T09:00:00Z',
    },
    {
      id: 102,
      path: 'src/util.go',
      line: 15,
      body: 'This function could be simplified.',
      diff_hunk: '@@ -14 +14,2 @@\n-old\n+new',
      user: { login: 'bob' },
      created_at: '2026-08-02T09:30:00Z',
    },
  ];

  let handle: AppHandle | null = null;
  let fakeGitHub: http.Server | null = null;
  try {
    transcript.section('Launch and create the feature');
    handle = await launchApp(world, testInfo, { traceName: 'review-feedback-recovery' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const featureName = `ReviewFeedbackRecovery${Math.random().toString(16).slice(2, 8)}`;
    const cockpit = await createFeatureViaForm(handle, {
      name: featureName,
      description: 'review feedback recovery journey',
      repoPatterns: [/alpha/],
    });
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    const features = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
    const featureId = features[0]!.id;
    transcript.json('feature id', featureId);

    transcript.section('Quit, seed Published + pr_url, relaunch against the fake GitHub API');
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
    const runPath = activeRunYamlPath(world, featureId);
    let runYaml = fs.readFileSync(runPath, 'utf8');
    runYaml = clearRunFailures(runYaml);
    runYaml = replaceTopLevelBlock(runYaml, 'repo_states', [
      'repo_states:',
      '  alpha:',
      '    touched: true',
      `    pr_url: ${prUrl}`,
    ]);
    fs.writeFileSync(runPath, runYaml);

    const fake = await startFakeGitHubAPI({
      'e2e/alpha': { reviewComments: alphaComments, issueComments: [], reviews: [] },
    });
    fakeGitHub = fake.server;
    handle = await launchApp(world, testInfo, {
      traceName: 'review-feedback-recovery-seeded',
      env: { AGENTICO_GITHUB_API_BASE: serverBaseUrl(fake.server) },
    });
    const overviewOption = handle.page.getByRole('option', { name: 'Overview' });
    await expect(overviewOption).toBeVisible({ timeout: 60_000 });
    await overviewOption.click();
    await handle.page.getByRole('option', { name: featureName }).click();
    const seededCockpit = handle.page.getByLabel(`Feature ${featureName}`);
    await expect(seededCockpit).toBeVisible({ timeout: 30_000 });
    const aftercare = seededCockpit.getByRole('region', { name: 'Feature aftercare' });
    const addressReviewFeedback = aftercare.getByRole('button', {
      name: /Address review feedback/,
    });
    await addressReviewFeedback.click();
    const workspace = handle.page.getByRole('dialog', { name: 'Address review feedback' });
    await expect(workspace).toBeVisible({ timeout: 15_000 });
    const feed = workspace.getByLabel('Review feedback');
    const comment101 = feed.getByLabel(/never cancels the derived context/);
    const comment102 = feed.getByLabel(/This function could be simplified/);
    await expect(comment101).toBeChecked();
    await expect(comment102).toBeChecked();
    transcript.step('workspace opened with both alpha comments pre-selected');

    transcript.section('Failed save: unsaved marker, Retry save commits the outstanding choice');
    // Packaged failure injection happens beneath the renderer: the pending
    // draft lives at <stateDir>/<featureId>/review-feedback/draft.json, and
    // the server persists every selection mutation there. Making the draft
    // directory read-only turns the next save into a genuine non-conflict
    // server failure without touching the frozen context-bridge preload.
    const draftDir = path.join(world.stateDir, featureId, 'review-feedback');
    fs.chmodSync(draftDir, 0o555);
    await comment101.click();
    const alert = workspace.getByRole('alert');
    await expect(alert).toContainText('Choices not saved');
    await expect(alert).toBeFocused();
    await expect(workspace.getByText('Unsaved choice', { exact: true })).toBeVisible();
    await expect(comment101).not.toBeChecked();
    await expect(comment102).toBeDisabled();
    await expect(
      workspace.getByRole('button', { name: 'Unsaved choices — retry or reload' }),
    ).toBeDisabled();
    await expect(workspace.getByRole('button', { name: 'Back', exact: true })).toBeDisabled();
    transcript.step('failed save remained visible as an unsaved choice; mutations frozen');

    fs.chmodSync(draftDir, 0o755);
    await workspace.getByRole('button', { name: 'Retry save' }).click();
    await expect(alert).toHaveCount(0, { timeout: 15_000 });
    await expect(workspace.getByText('Unsaved choice', { exact: true })).toHaveCount(0);
    await expect(comment101).not.toBeChecked();
    await expect(workspace.getByText('1 of 2 selected').first()).toBeVisible();
    transcript.step('Retry save committed the outstanding choice; markers cleared');

    transcript.section('Failed save again: Reload saved selections abandons the overlay');
    fs.chmodSync(draftDir, 0o555);
    await comment101.click();
    await expect(workspace.getByRole('alert')).toContainText('Choices not saved');
    await expect(comment101).toBeChecked();
    fs.chmodSync(draftDir, 0o755);
    await workspace.getByRole('button', { name: 'Reload saved selections' }).click();
    await expect(workspace.getByRole('alert')).toHaveCount(0, { timeout: 15_000 });
    await expect(comment101).not.toBeChecked();
    await expect(workspace.getByText('Unsaved choice', { exact: true })).toHaveCount(0);
    await expect(workspace.getByText(/Saved selections reloaded/)).toBeVisible();
    await expect(workspace.getByText('1 of 2 selected').first()).toBeVisible();
    transcript.step('Reload adopted the authoritative draft and cleared the unsaved overlay');

    transcript.section('Conflict convergence: a concurrent writer commits first');
    // A second writer (the bundled server itself, driven directly) commits an
    // unrelated change on the current committed revision; the workspace's next
    // save must converge instead of replaying over it.
    const draftNow = await handle.page.evaluate((id) => {
      const api = window.agentico as unknown as {
        fetchReviewFeedback(request: { featureId: string }): Promise<{ revision: number }>;
      };
      return api.fetchReviewFeedback({ featureId: id });
    }, featureId);
    const ref101 = 'alpha:review:101';
    await handle.page.evaluate(
      ([id, revision, ref]) => {
        const api = window.agentico as unknown as {
          updateReviewFeedbackSelection(request: {
            featureId: string;
            expectedRevision: number;
            updates: Array<{ stableRef: string; selected: boolean }>;
          }): Promise<unknown>;
        };
        return api.updateReviewFeedbackSelection({
          featureId: id,
          expectedRevision: revision,
          updates: [{ stableRef: ref, selected: true }],
        });
      },
      [featureId, draftNow.revision, ref101] as const,
    );
    await comment102.click();
    const conflictAlert = workspace.getByRole('alert');
    await expect(conflictAlert).toContainText('Selections reloaded', { timeout: 15_000 });
    await expect(conflictAlert).toBeFocused();
    // The workspace adopted the other writer's committed view: 101 selected.
    await expect(comment101).toBeChecked({ timeout: 15_000 });
    await expect(comment102).toBeChecked();
    await expect(workspace.getByText('2 of 2 selected').first()).toBeVisible();
    transcript.step('UI save conflicted, refetched, and adopted the committed view');

    transcript.section('Interrupted-launch replay: same child, original counts, one auto-start');
    // Re-enter so the workspace's acknowledged revision is provably fresh.
    await workspace.getByRole('button', { name: 'Back', exact: true }).click();
    await expect(workspace).toHaveCount(0);
    await addressReviewFeedback.click();
    await expect(workspace).toBeVisible({ timeout: 15_000 });
    // The workspace's acknowledged revision is the durable draft revision on
    // disk: read it directly, because an out-of-band fetch would itself
    // advance the revision and leave the workspace view stale.
    const draftOnDisk = JSON.parse(fs.readFileSync(path.join(draftDir, 'draft.json'), 'utf8')) as {
      revision: number;
    };
    // One selected comment changes after the draft snapshot: reconciliation
    // at launch counts it as "changed since review".
    alphaComments[1]!.body = 'This function could be simplified — please show me how.';
    fake.routes['/repos/e2e/alpha/pulls/1/comments'] = JSON.stringify(alphaComments);
    // The gate choice must match for the replay to claim the original receipt.
    const gateToggle = workspace.getByRole('checkbox', {
      name: /Pause for Roadmap and Phase plan review/,
    });
    await gateToggle.check();
    const originalLaunch = await handle.page.evaluate(
      ([id, revision]) => {
        const api = window.agentico as unknown as {
          launchReviewFeedbackChild(request: {
            parentId: string;
            expectedRevision: number;
            gate: boolean;
          }): Promise<{ childId?: string; parentId: string; changed?: number }>;
        };
        return api.launchReviewFeedbackChild({
          parentId: id,
          expectedRevision: revision,
          gate: true,
        });
      },
      [featureId, draftOnDisk.revision] as const,
    );
    const originalChildId = originalLaunch.childId;
    expect(originalChildId).toBeTruthy();
    transcript.step(`original (interrupted) launch created child \`${originalChildId}\``);
    // A transport-level repeat of the same launch request (the response was
    // "lost") replays the durable receipt: same child, same original counts.
    const replayLaunch = await handle.page.evaluate(
      ([id, revision]) => {
        const api = window.agentico as unknown as {
          launchReviewFeedbackChild(request: {
            parentId: string;
            expectedRevision: number;
            gate: boolean;
          }): Promise<{ childId?: string; parentId: string; changed?: number }>;
        };
        return api.launchReviewFeedbackChild({
          parentId: id,
          expectedRevision: revision,
          gate: true,
        });
      },
      [featureId, draftOnDisk.revision] as const,
    );
    expect(replayLaunch.childId).toBe(originalChildId);
    expect(replayLaunch.changed).toBe(originalLaunch.changed);
    transcript.step('transport-level replay returned the same child and counts');

    const replayClick = workspace.getByRole('button', { name: /Address comments \(2\)/ });
    await expect(replayClick).toBeEnabled();
    await replayClick.click();
    // Replay transitions like the original dispatch: the workspace leaves,
    // the SAME child workspace opens, and the durable banner carries the
    // original reconciliation counts.
    await expect(workspace).toHaveCount(0, { timeout: 30_000 });
    const pass = seededCockpit.getByRole('region', { name: 'Review feedback pass' });
    await expect(pass).toBeVisible({ timeout: 60_000 });
    await expect(handle.page.getByText('1 changed since review')).toBeVisible({ timeout: 30_000 });
    transcript.step('replay transitioned to the child with the original reconciliation banner');

    // Exactly one child exists, carried on the parent's activeChild slot.
    const afterReplay = (await handle.page.evaluate(() => window.agentico.listFeatures())).features;
    expect(afterReplay).toHaveLength(1);
    expect(afterReplay[0]!.activeChild?.id).toBe(originalChildId);
    // Single auto-started child: the pass leaves "ready" without a manual click.
    await expect(pass).toHaveAttribute('data-state', 'working', { timeout: 60_000 });
    transcript.step('one child, one receipt, one auto-start — no duplicate dispatch');
  } finally {
    if (handle !== null) {
      await closeApp(handle);
    }
    fakeGitHub?.close();
    destroyWorld(world);
  }
  transcript.write(testInfo);
});
